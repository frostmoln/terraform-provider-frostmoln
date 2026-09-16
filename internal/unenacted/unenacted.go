// Package unenacted holds the plan-time refusal validators for attributes
// that record intent the platform performs NOWHERE — not on create, and not
// on update. Unlike create-immutable attributes (where a RequiresReplace at
// least produces a consistent replacement) or the frostmoln_secret trio
// (whose create genuinely enacts the value and only the update is refused),
// these attributes are inert in both verbs: the value is stored, echoed back
// on read, and never drives anything on the platform side — the CLASS A
// findings of the 2026-09 convergence audit (parameter_group_id on the
// managed databases; tls_enabled on a webserver domain binding). Saying
// nothing lets `terraform apply` report success for a change it never made;
// refusing names the real platform constraint at plan time instead.
//
// A validate-stage refusal is destroy-safe, but for the precise reason the
// planmod package documents for its neighbours — and it is NOT "a destroy
// carries no configuration": terraform core plans every non-destroy
// instance change through a config re-validation (including `-refresh-only`),
// and plans destroy changes through planDestroy, which never invokes config
// validation. So the refusal fires on every plan that would act on the
// configuration, and never on the destroy plan, which is exactly the wanted
// gate. What this correctness does NOT buy: a plan while the line is still
// in HCL is told no outright — that is the point; the remedy the messages
// name (remove the line, then apply to drain the stored value) is the only
// exit for legacy state.
//
// The resources carrying these attributes keep a matching refusal in their
// Create/Update as a belt: a configuration value that resolves only at
// apply (a reference to an attribute of a resource created in the same
// apply) is still unknown when the plan is validated, so the validator lets
// it pass and the CRUD hook — which by then sees every value known — is
// the last honest word before any request is sent.
package unenacted

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
)

// String refuses every known, non-null configuration value, with the
// platform constraint as the reason. A null or unknown configuration value
// passes: null stays null for lack of anything to refuse, and an unknown
// value has not yet resolved — the resource's create/update re-checks it
// once every value is known (see the package comment).
func String(title, constraint string) validator.String {
	return stringRefusal{title: title, constraint: constraint}
}

type stringRefusal struct {
	title      string
	constraint string
}

func (v stringRefusal) Description(_ context.Context) string {
	return v.title + " (value refused at plan time)"
}

func (v stringRefusal) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}

func (v stringRefusal) ValidateString(_ context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	resp.Diagnostics.AddAttributeError(req.Path, v.title, v.constraint)
}

// BoolTrue refuses an explicit true, with the platform constraint as the
// reason. It is value-shaped rather than blanket (unlike String) because
// the honest default differs per attribute: an explicit false on a
// never-enacted capability flag matches the platform's actual behaviour
// (nothing is provisioned either way), while a true records a promise the
// platform does not keep. Null and unknown values pass, for the same
// reasons as String.
func BoolTrue(title, constraint string) validator.Bool {
	return boolTrueRefusal{title: title, constraint: constraint}
}

type boolTrueRefusal struct {
	title      string
	constraint string
}

func (v boolTrueRefusal) Description(_ context.Context) string {
	return v.title + " (true refused at plan time)"
}

func (v boolTrueRefusal) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}

func (v boolTrueRefusal) ValidateBool(_ context.Context, req validator.BoolRequest, resp *validator.BoolResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	if !req.ConfigValue.ValueBool() {
		return
	}
	resp.Diagnostics.AddAttributeError(req.Path, v.title, v.constraint)
}
