// Package timeouts provides the customer-tunable `timeouts` block shared by
// every async resource.
//
// terraform-plugin-framework provides no timeouts mechanism (no
// ResourceWithTimeouts interface; fwschema only nods to the SDKv2
// hand-rolled machinery an all-framework provider cannot reach), so the block
// is hand-rolled here and each resource resolves its wait budgets from it.
// The shape intentionally matches the big-3 practitioners' muscle memory:
//
//	timeouts {
//	  create = "45m"
//	  update = "30m"
//	  delete = "30m"
//	}
//
// Mechanics, re-verified by a dev_overrides smoke test on 2026-09-10
// (project-docs/product/TF-CONVERGENCE-WALL-PLAN.md, Gate 2): the block is a
// SingleNestedBlock that persists in state, never plans diff noise when
// unchanged, and never forces a replacement when changed — a timeouts change
// is an in-place no-op on the infrastructure.
//
// Only the machine's TIME BUDGETS are customer-tunable. The poll interval and
// the transient-retry budgets stay provider-internal (the per-resource
// pollInterval/pollTimeout test-injection fields keep that role), because the
// interval does not bound anything a practitioner can meaningfully trade.
package timeouts

import (
	"context"
	"fmt"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// Model is the `timeouts` block on a resource. Resources embed it as
//
//	Timeouts *timeouts.Model `tfsdk:"timeouts"`
//
// in their Terraform model; a nil pointer is an absent block.
type Model struct {
	Create types.String `tfsdk:"create"`
	Update types.String `tfsdk:"update"`
	Delete types.String `tfsdk:"delete"`
}

// Budgets is the resolved wait budget per verb: the configured override, or
// the resource's default — the value today's code hardcodes — when unset.
type Budgets struct {
	Create time.Duration
	Update time.Duration
	Delete time.Duration
}

// Uniform budgets every verb at d. For resources whose create, update and
// delete waits share one constant today this keeps that behavior exactly.
func Uniform(d time.Duration) Budgets {
	return Budgets{Create: d, Update: d, Delete: d}
}

// Resolve maps the configured block onto effective budgets. A verb the
// practitioner left unset falls back to its default; an invalid duration is
// impossible to reach here through the normal surface (the block's validator
// rejects it at plan time), so the error is a last-resort guard, not the
// primary validation.
func (m *Model) Resolve(defaults Budgets) (Budgets, error) {
	if m == nil {
		return defaults, nil
	}

	create, err := resolveVerb(m.Create, defaults.Create, "create")
	if err != nil {
		return Budgets{}, err
	}
	update, err := resolveVerb(m.Update, defaults.Update, "update")
	if err != nil {
		return Budgets{}, err
	}
	delete, err := resolveVerb(m.Delete, defaults.Delete, "delete")
	if err != nil {
		return Budgets{}, err
	}
	return Budgets{Create: create, Update: update, Delete: delete}, nil
}

func resolveVerb(v types.String, d time.Duration, verb string) (time.Duration, error) {
	if v.IsNull() || v.IsUnknown() {
		return d, nil
	}
	out, err := time.ParseDuration(v.ValueString())
	if err != nil {
		return 0, fmt.Errorf("timeouts.%s: %q is not a valid duration: %w", verb, v.ValueString(), err)
	}
	return out, nil
}

// Schema returns the `timeouts` SingleNestedBlock. Each resource embeds the
// returned block (by value, fresh per call) in its schema; nothing here is
// stateful, so every resource can call it directly:
//
//	Blocks: map[string]schema.Block{"timeouts": timeouts.Schema()},
func Schema() schema.SingleNestedBlock {
	return schema.SingleNestedBlock{
		Attributes: map[string]schema.Attribute{
			"create": schema.StringAttribute{
				Description: "How long the provider waits for the create operation to converge before giving up (e.g. \"45m\", \"2h\").",
				Optional:    true,
				Validators:  []validator.String{Duration()},
			},
			"update": schema.StringAttribute{
				Description: "How long the provider waits for an update (resize, in-place change) to converge before giving up (e.g. \"30m\").",
				Optional:    true,
				Validators:  []validator.String{Duration()},
			},
			"delete": schema.StringAttribute{
				Description: "How long the provider waits for the delete to complete before giving up (e.g. \"30m\").",
				Optional:    true,
				Validators:  []validator.String{Duration()},
			},
		},
	}
}

// Duration returns the validator every timeouts attribute carries: the value
// must parse with time.ParseDuration. The plan-time rejection is the primary
// UX — the error names the attribute path.
func Duration() validator.String {
	return durationValidator{}
}

type durationValidator struct{}

func (durationValidator) Description(_ context.Context) string {
	return "value must be a duration string time.ParseDuration accepts (e.g. 45m, 2h30m, 1h)"
}

func (v durationValidator) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}

func (durationValidator) ValidateString(_ context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	if _, err := time.ParseDuration(req.ConfigValue.ValueString()); err != nil {
		resp.Diagnostics.AddAttributeError(
			req.Path,
			"Invalid timeouts duration",
			fmt.Sprintf("%s must parse as a Go duration, e.g. 45m, 2h30m, 1h: %s",
				req.Path, err),
		)
	}
}
