// Package tlsattr holds the require_tls plan and apply rules of the managed-offer
// resources that gained TLS in MANAGED-OFFER-TLS-AND-INTERNAL-DNS-PLAN §8 Phases 4c-4e
// (frostmoln_mysql_instance, frostmoln_redis_instance, frostmoln_valkey_instance,
// frostmoln_nginx_instance, frostmoln_apache_instance). They are the rules
// frostmoln_messaging_instance (4a) and frostmoln_postgres_instance (4b) carry inline.
//
// require_tls is Optional+Computed without a schema Default: the platform owns the
// default (TLS required for the databases and caches once TLS is available there, false
// for the webservers), so an omitted value must reach the API as an omission. It is fixed
// at create: a REAL change replaces the instance, and a state written before the
// attribute existed is never replaced for it.
package tlsattr

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/boolplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// RequireTLSPlanModifiers are require_tls's plan modifiers, in order.
//
// UseNonNullStateForUnknown, NOT UseStateForUnknown: the latter copies a NULL prior value
// too (framework v1.19), so a state written before the attribute existed, planned with
// -refresh=false, would plan null, and the apply, which reads the real value, would fail
// as an inconsistent result. A null prior leaves the plan unknown.
func RequireTLSPlanModifiers() []planmodifier.Bool {
	return []planmodifier.Bool{
		boolplanmodifier.UseNonNullStateForUnknown(),
		boolplanmodifier.RequiresReplaceIf(RequireTLSChanged,
			"Changing require_tls replaces the instance: the requirement is fixed at create.",
			"Changing `require_tls` replaces the instance: the requirement is fixed at create."),
	}
}

// RequireTLSChanged is require_tls's RequiresReplaceIf: a replacement only for a REAL
// change, a known planned value against a known, non-null prior value that differs.
//
// A NULL prior value is never a change. It is the state an older provider wrote (the
// attribute did not exist), and under `terraform plan -refresh=false` no Read fills it
// in, so a plain RequiresReplace() would destroy the instance on the first plan after the
// upgrade. Update instead checks a configured value against the instance before it writes
// anything (CheckRequireTLSUnchanged). It reads PlanValue, not ConfigValue: after
// UseNonNullStateForUnknown an omitted attribute plans the (non-null) prior value, so
// only a configured different value differs.
func RequireTLSChanged(_ context.Context, req planmodifier.BoolRequest, resp *boolplanmodifier.RequiresReplaceIfFuncResponse) {
	if req.StateValue.IsNull() || req.StateValue.IsUnknown() || req.PlanValue.IsUnknown() || req.PlanValue.IsNull() {
		return
	}
	resp.RequiresReplace = req.PlanValue.ValueBool() != req.StateValue.ValueBool()
}

// CheckRequireTLSUnchanged is the apply-time half of RequireTLSChanged: with no prior
// value (state from an older provider, planned without refresh) a configured
// require_tls is checked against the instance before Update writes anything. The
// platform fixes the requirement at create (REQUIRE_TLS_IMMUTABLE), so a mismatch is
// refused here, naming the value the instance holds, instead of failing later as an
// inconsistent result. current reads the instance's requirement; it is called only when
// the check applies. what names the offer in a read failure ("MySQL instance").
func CheckRequireTLSUnchanged(ctx context.Context, plan, state types.Bool, what string,
	current func(context.Context) (bool, error), diags *diag.Diagnostics) {
	if !state.IsNull() || plan.IsNull() || plan.IsUnknown() {
		return
	}
	held, err := current(ctx)
	if err != nil {
		diags.AddError("Failed to read "+what, err.Error())
		return
	}
	if held != plan.ValueBool() {
		diags.AddAttributeError(path.Root("require_tls"), "require_tls cannot be changed",
			fmt.Sprintf("The instance's require_tls is %t and is fixed at create (REQUIRE_TLS_IMMUTABLE). Set "+
				"require_tls = %t, or run a refreshed plan, which replaces the instance to change it. Nothing was changed.",
				held, held))
	}
}

// RequireTLSForCreate is the create request's requireTls: sent ONLY when configured, so
// an omission reaches the service as an omission and the platform default applies.
func RequireTLSForCreate(v types.Bool) *bool {
	if v.IsNull() || v.IsUnknown() {
		return nil
	}
	b := v.ValueBool()
	return &b
}

// StringFromWire maps an absent string (a hostname not assigned, or a service that
// predates the field) to null.
func StringFromWire(s string) types.String {
	if s == "" {
		return types.StringNull()
	}
	return types.StringValue(s)
}

// Int64FromWire maps an absent port (0: not served) to null, never 0.
func Int64FromWire(n int) types.Int64 {
	if n <= 0 {
		return types.Int64Null()
	}
	return types.Int64Value(int64(n))
}
