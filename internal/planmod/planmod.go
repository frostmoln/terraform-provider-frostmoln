// Package planmod holds reusable terraform-plugin-framework plan modifiers
// shared across resources.
package planmod

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
)

// Int64GrowOnly returns an Int64 plan modifier that rejects a decrease of an
// attribute (prior state -> plan) with a plan-time error. Managed-service
// storage can be grown online but the backend never shrinks a volume, so a
// shrink must fail at plan rather than during apply (a RequiresReplace here
// would silently destroy the instance).
//
// Known limitation: attribute plan modifiers run even when a sibling
// RequiresReplace attribute is simultaneously forcing a destroy+recreate, so a
// shrink combined with a replace-triggering change in the same apply is rejected
// here rather than allowed as part of the recreate. That combination is rare
// (the replace-triggering attributes — version/vpc_id/subnet_id — already
// destroy the instance) and the caller can perform the change in two steps.
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
		resp.Diagnostics.AddAttributeError(
			req.Path,
			"Attribute cannot be decreased",
			fmt.Sprintf(
				"%q can only be increased (currently %d %s, plan requests %d %s). Storage is grown online and cannot be shrunk; to reduce it, destroy and recreate the instance.",
				req.Path, req.StateValue.ValueInt64(), m.unit, req.PlanValue.ValueInt64(), m.unit,
			),
		)
	}
}

// StringErrorOnChange returns a String plan modifier that rejects any change of
// an attribute (prior state -> plan) with a plan-time error carrying the given
// detail. Used for attributes the backend cannot yet change in place (e.g.
// flavor_id: flavor resize is not yet supported) so the change surfaces at plan
// instead of being silently dropped by an in-place update.
func StringErrorOnChange(detail string) planmodifier.String {
	return stringErrorOnChange{detail: detail}
}

type stringErrorOnChange struct {
	detail string
}

func (m stringErrorOnChange) Description(_ context.Context) string {
	return "cannot be changed in place"
}

func (m stringErrorOnChange) MarkdownDescription(ctx context.Context) string {
	return m.Description(ctx)
}

func (m stringErrorOnChange) PlanModifyString(_ context.Context, req planmodifier.StringRequest, resp *planmodifier.StringResponse) {
	// Skip on create (no prior state) or when either value is not yet known.
	if req.StateValue.IsNull() || req.PlanValue.IsNull() || req.PlanValue.IsUnknown() || req.StateValue.IsUnknown() {
		return
	}
	if !req.PlanValue.Equal(req.StateValue) {
		resp.Diagnostics.AddAttributeError(req.Path, "Attribute cannot be changed", m.detail)
	}
}
