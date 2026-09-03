// Package writeonly reads a write-only attribute out of a resource's
// configuration at apply time, and re-checks there every rule the schema
// validators state but cannot always enforce.
//
// 🔴 THE PLAN-TIME VALIDATORS SELF-DISABLE ON AN UNKNOWN VALUE. Every one of
// them returns early when the value it guards is unknown, by design — the
// framework cannot know yet whether the rule will be satisfied:
//
//   - stringvalidator.LengthAtLeast   (length_at_least.go: IsNull || IsUnknown -> return)
//   - stringvalidator.AlsoRequires    (also_requires.go:   IsNull || IsUnknown -> return)
//   - stringvalidator.ConflictsWith   (conflicts_with.go:  IsNull || IsUnknown -> return)
//   - resourcevalidator.ExactlyOneOf  (collects unknown paths, then returns before
//     the "neither set" branch)
//
// A write-only value sourced from another resource or data source — the very
// pattern the write-only form exists to serve, since the point is not to write
// the value down anywhere — is unknown at plan. So for exactly those
// configurations, none of the schema rules run at all, and the failures they
// exist to prevent become reachable on a green apply:
//
//   - a value that resolves to "" passes every null guard, and where the wire
//     field carries omitempty it is then dropped entirely: an instance boots
//     with no console password, and nothing in the plan showed it, because the
//     write-only path has no plan line to show.
//   - a value with no version companion is created once and then never updated
//     again, because the companion is the only change signal there is.
//   - a version companion with no value sends nothing at all: a silent no-op.
//   - both the write-only and the legacy attribute set puts the material in
//     state, on a configuration the practitioner believes is write-only.
//
// Config is fully resolved by the time Terraform calls ApplyResourceChange, so
// this is the layer at which the rules can actually be enforced. The validators
// stay: they are the friendly, early refusal for the common case where the
// value is known at plan. This is the one that cannot be skipped.
//
// EVERY write-only attribute in the provider goes through this — frostmoln_secret,
// frostmoln_instance (user_data and console_password), frostmoln_launch_template
// and frostmoln_appgw_certificate. A new one that does not is a gap, not a
// choice; internal/provider/writeonly_contract_test.go asserts the half that is
// observable from outside, that ExactlyOne agrees with the resource's own
// resource-level ExactlyOneOf.
package writeonly

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// Attr names one write-only triple: the write-only attribute, the
// practitioner-set version companion carrying its change detection, and the
// legacy attribute it replaces.
type Attr struct {
	// WO, Version and Legacy are the attribute names as they appear in the
	// schema.
	WO, Version, Legacy string

	// Subject names the thing being acted on, for the diagnostic detail —
	// "the instance", "the certificate".
	Subject string

	// ExactlyOne says a configuration must supply one of WO or Legacy. Set it
	// where the legacy attribute was Required before the write-only form
	// existed and was relaxed to Optional under a resource-level ExactlyOneOf:
	// that validator is then the only thing holding the floor, and it is
	// skipped along with the rest when a value is unknown at plan.
	ExactlyOne bool
}

// Read returns the write-only value, or a null value with a diagnostic
// appended. Every refusal fails CLOSED: the caller stops before the platform is
// called, rather than proceeding with a value that is empty or unpaired.
func (a Attr) Read(ctx context.Context, config tfsdk.Config, diags *diag.Diagnostics) types.String {
	var wo, version, legacy types.String
	diags.Append(config.GetAttribute(ctx, path.Root(a.WO), &wo)...)
	diags.Append(config.GetAttribute(ctx, path.Root(a.Version), &version)...)
	diags.Append(config.GetAttribute(ctx, path.Root(a.Legacy), &legacy)...)
	if diags.HasError() {
		return types.StringNull()
	}

	// Unknown keeps its own summary, matched exactly by the provider-level
	// contract test. It is the one refusal that indicates a bug rather than a
	// mistaken configuration, so it says so.
	if wo.IsUnknown() {
		diags.AddError(
			a.WO+" Is Unknown At Apply",
			fmt.Sprintf("The write-only %s value was still unknown when %s was processed, so it "+
				"could not be sent. Nothing was changed. This is a bug in the provider or in "+
				"Terraform — please report it.", a.WO, a.Subject),
		)
		return types.StringNull()
	}

	set := !wo.IsNull()
	switch {
	case set && wo.ValueString() == "":
		diags.AddError(
			a.WO+" Is Empty",
			fmt.Sprintf("%s was set to an empty string. That is never a meaningful value here, and "+
				"it is not the way to say \"none\" — omit the attribute for that. An empty value "+
				"resolved from a variable, a template or a data source is refused at plan time when "+
				"it is known then, and here when it was not.", a.WO),
		)
	case set && version.IsNull():
		diags.AddError(
			a.Version+" Is Required With "+a.WO,
			fmt.Sprintf("%s is set but %s is not. Terraform cannot see a write-only value, so the "+
				"version companion is the ONLY signal that would ever make a later apply send a new "+
				"one: without it %s would be sent once and never again.", a.WO, a.Version, a.WO),
		)
	case !set && !version.IsNull():
		diags.AddError(
			a.WO+" Is Required With "+a.Version,
			fmt.Sprintf("%s is set but %s is not, so there is nothing to send. The apply would "+
				"otherwise succeed having changed nothing at all.", a.Version, a.WO),
		)
	case set && !legacy.IsNull():
		diags.AddError(
			a.WO+" And "+a.Legacy+" Are Mutually Exclusive",
			fmt.Sprintf("Both %s and %s are set. %s is written to Terraform state in plaintext, so "+
				"accepting this would store the value the write-only attribute exists to keep out "+
				"of state.", a.WO, a.Legacy, a.Legacy),
		)
	case a.ExactlyOne && !set && legacy.IsNull():
		diags.AddError(
			"Missing "+a.WO+" Or "+a.Legacy,
			fmt.Sprintf("Exactly one of %s and %s must be set; neither is.", a.Legacy, a.WO),
		)
	}
	if diags.HasError() {
		return types.StringNull()
	}
	return wo
}
