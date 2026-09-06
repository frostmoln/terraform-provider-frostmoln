// Package planmod holds reusable terraform-plugin-framework plan modifiers
// shared across resources.
package planmod

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// Int64GrowOnly returns an Int64 plan modifier that WARNS when an attribute is
// decreased (prior state -> plan). Managed-service storage can be grown online
// but the backend never shrinks a volume, so the shrink is refused — the refusal
// itself lives in the resource's Update (apply time), not here.
//
// It must stay a warning: a plan modifier that raises an ERROR also blocks
// `terraform destroy`. Terraform's destroy plan still runs a refresh phase that
// computes an ordinary (non-null) plan, and attribute plan modifiers DO run
// there — the framework only skips them when the planned state is null, which
// that phase is not. A customer who grew the volume out of band (portal, CLI)
// and then ran `terraform destroy` got the shrink error instead of a destroy,
// with no way out but to edit the HCL or pass -refresh=false (pilot report,
// 2026-09-06). redis/valkey were never affected: they guard the shrink at apply
// only, which is the shape this now matches.
//
// A RequiresReplace here would be worse still — a typo'd storage_gb would
// silently destroy and recreate the instance.
//
// One behaviour changed with the downgrade: a shrink combined with a sibling
// RequiresReplace change (version/vpc_id/subnet_id) used to be blocked here and
// now applies, recreating the instance at the smaller size. Terraform shows that
// as a -/+ replacement the practitioner approves, and the warning says so.
func Int64GrowOnly(unit string) planmodifier.Int64 {
	return int64GrowOnly{unit: unit}
}

type int64GrowOnly struct {
	unit string
}

func (m int64GrowOnly) Description(_ context.Context) string {
	return "can only be increased, never decreased"
}

func (m int64GrowOnly) MarkdownDescription(ctx context.Context) string {
	return m.Description(ctx)
}

func (m int64GrowOnly) PlanModifyInt64(_ context.Context, req planmodifier.Int64Request, resp *planmodifier.Int64Response) {
	// Skip on create (no prior state) or when either value is not yet known.
	if req.StateValue.IsNull() || req.PlanValue.IsNull() || req.PlanValue.IsUnknown() || req.StateValue.IsUnknown() {
		return
	}
	if req.PlanValue.ValueInt64() < req.StateValue.ValueInt64() {
		resp.Diagnostics.AddAttributeWarning(
			req.Path,
			"Attribute cannot be decreased",
			fmt.Sprintf(
				"%q can only be increased (currently %d %s, plan requests %d %s). Storage is grown online and cannot be "+
					"shrunk, so this apply will fail — unless this plan already replaces the instance for another "+
					"reason, in which case it is recreated at the smaller size and its data is lost. A destroy is unaffected.",
				req.Path, req.StateValue.ValueInt64(), m.unit, req.PlanValue.ValueInt64(), m.unit,
			),
		)
	}
}

// StringWarnOnChange returns a String plan modifier that WARNS on any change of
// an attribute (prior state -> plan), carrying the given detail. Used for
// attributes the backend cannot yet change in place (e.g. flavor_id: flavor
// resize is not yet supported) so the change surfaces at plan; the refusal
// itself lives in the resource's Update. See Int64GrowOnly for why this cannot
// raise an error (it would block `terraform destroy`).
func StringWarnOnChange(detail string) planmodifier.String {
	return stringWarnOnChange{detail: detail}
}

type stringWarnOnChange struct {
	detail string
}

func (m stringWarnOnChange) Description(_ context.Context) string {
	return "cannot be changed in place"
}

func (m stringWarnOnChange) MarkdownDescription(ctx context.Context) string {
	return m.Description(ctx)
}

func (m stringWarnOnChange) PlanModifyString(_ context.Context, req planmodifier.StringRequest, resp *planmodifier.StringResponse) {
	// Skip on create (no prior state) or when either value is not yet known.
	if req.StateValue.IsNull() || req.PlanValue.IsNull() || req.PlanValue.IsUnknown() || req.StateValue.IsUnknown() {
		return
	}
	if !req.PlanValue.Equal(req.StateValue) {
		resp.Diagnostics.AddAttributeWarning(req.Path, "Attribute cannot be changed", m.detail+" This apply will fail; a destroy is unaffected.")
	}
}

// StringUseStateOrDefault returns a String plan modifier for an Optional+Computed
// attribute whose value the SERVER owns: it pins the prior state value (like
// UseStateForUnknown), except that a NULL prior state plans the given default
// instead of planning null.
//
// A schema Default cannot serve this role. TransformDefaults substitutes the
// default whenever the CONFIG value is null irrespective of prior state, so it
// overwrites a server-side value the practitioner deliberately set and then
// dropped from HCL. This modifier only ever fills a gap.
//
// The null-state arm is what makes it different from plain UseStateForUnknown,
// which copies a null state value straight into the plan (it bails only when the
// WHOLE prior state is null, i.e. on create — see the framework's
// use_state_for_unknown.go). Planning null against an attribute the resource then
// reads back as a real value is an inconsistent-result error at apply, which is
// what state written before the attribute became Computed would otherwise hit
// under `-refresh=false` (no Read runs, so nothing normalizes the stale null).
func StringUseStateOrDefault(def string) planmodifier.String {
	return stringUseStateOrDefault{def: def}
}

type stringUseStateOrDefault struct {
	def string
}

func (m stringUseStateOrDefault) Description(_ context.Context) string {
	return "keeps the value already in state; a null state plans the server-side default"
}

func (m stringUseStateOrDefault) MarkdownDescription(ctx context.Context) string {
	return m.Description(ctx)
}

func (m stringUseStateOrDefault) PlanModifyString(_ context.Context, req planmodifier.StringRequest, resp *planmodifier.StringResponse) {
	// A known planned value comes from config — never override it.
	if !req.PlanValue.IsUnknown() {
		return
	}
	// Leave it unknown on create (no prior state to pin, and the server decides)
	// and while the config value is still an unresolved interpolation.
	if req.State.Raw.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	if req.StateValue.IsNull() {
		resp.PlanValue = types.StringValue(m.def)
		return
	}
	resp.PlanValue = req.StateValue
}

// Int64UseStateOrDefault is the Int64 form of StringUseStateOrDefault.
func Int64UseStateOrDefault(def int64) planmodifier.Int64 {
	return int64UseStateOrDefault{def: def}
}

type int64UseStateOrDefault struct {
	def int64
}

func (m int64UseStateOrDefault) Description(_ context.Context) string {
	return "keeps the value already in state; a null state plans the server-side default"
}

func (m int64UseStateOrDefault) MarkdownDescription(ctx context.Context) string {
	return m.Description(ctx)
}

func (m int64UseStateOrDefault) PlanModifyInt64(_ context.Context, req planmodifier.Int64Request, resp *planmodifier.Int64Response) {
	if !req.PlanValue.IsUnknown() {
		return
	}
	if req.State.Raw.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	if req.StateValue.IsNull() {
		resp.PlanValue = types.Int64Value(m.def)
		return
	}
	resp.PlanValue = req.StateValue
}
