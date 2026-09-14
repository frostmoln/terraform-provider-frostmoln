package tenant_default_tags_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	fwresource "github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/provider"
	tdt "go.frostmoln.internal/terraform-provider-frostmoln/internal/resource/tenant_default_tags"
)

// Real protocol RPCs: ValidateResourceConfig (the attribute validators, which
// run before validate/plan/refresh/import) and PlanResourceChange (whether a
// change updates or replaces).

const (
	typeName = "frostmoln_tenant_default_tags"
	tenantA  = "1111abcd-1111-4111-8111-11111111abcd"
)

func objType(t *testing.T) tftypes.Object {
	t.Helper()
	var sr fwresource.SchemaResponse
	tdt.NewResource().Schema(context.Background(), fwresource.SchemaRequest{}, &sr)
	return sr.Schema.Type().TerraformType(context.Background()).(tftypes.Object)
}

func s(v string) tftypes.Value { return tftypes.NewValue(tftypes.String, v) }

var null = tftypes.NewValue(tftypes.String, nil)

func tags(kv map[string]tftypes.Value) tftypes.Value {
	return tftypes.NewValue(tftypes.Map{ElementType: tftypes.String}, kv)
}

func value(t *testing.T, id, tenant, tg tftypes.Value) tftypes.Value {
	t.Helper()
	return tftypes.NewValue(objType(t), map[string]tftypes.Value{
		"id": id, "tenant_id": tenant, "tags": tg,
		"apply_to_existing_on_change": tftypes.NewValue(tftypes.Bool, false),
		"timeouts":                    tftypes.NewValue(objType(t).AttributeTypes["timeouts"], nil),
	})
}

func dyn(t *testing.T, v tftypes.Value) *tfprotov6.DynamicValue {
	t.Helper()
	dv, err := tfprotov6.NewDynamicValue(objType(t), v)
	if err != nil {
		t.Fatal(err)
	}
	return &dv
}

func text(diags []*tfprotov6.Diagnostic) (string, bool) {
	var b strings.Builder
	isErr := false
	for _, d := range diags {
		if d.Severity == tfprotov6.DiagnosticSeverityError {
			isErr = true
		}
		b.WriteString(d.Summary + ": " + d.Detail + "\n")
	}
	return b.String(), isErr
}

// The rules are identity's ValidateTenantDefaultTags (via tftags), so each is
// pinned at both edges — refused where the server refuses, accepted just
// inside — and the plan-time check is never stricter than the platform.
func TestValidate_TenantDefaultTagRules(t *testing.T) {
	t.Parallel()
	ten := map[string]tftypes.Value{}
	for i := 0; i < 10; i++ {
		ten[fmt.Sprintf("k%d", i)] = s("v")
	}
	eleven := map[string]tftypes.Value{"k10": s("v")}
	for k, v := range ten {
		eleven[k] = v
	}
	cases := []struct {
		name   string
		tags   map[string]tftypes.Value
		refuse bool
		want   string
	}{
		{"empty set", map[string]tftypes.Value{}, false, ""},
		{"ordinary", map[string]tftypes.Value{"env": s("prod"), "cost-center": s("eu 42/a@b=c")}, false, ""},
		{"ten tags", ten, false, ""},
		{"eleven tags", eleven, true, "maximum 10"},
		{"empty value", map[string]tftypes.Value{"env": s("")}, false, ""},
		{"null value", map[string]tftypes.Value{"env": null}, true, "null"},
		{"slash in key", map[string]tftypes.Value{"team/owner": s("x")}, true, "not allowed"},
		{"64-byte key", map[string]tftypes.Value{strings.Repeat("k", 64): s("x")}, false, ""},
		{"65-byte key", map[string]tftypes.Value{strings.Repeat("k", 65): s("x")}, true, "1 to 64 bytes"},
		{"quote in value", map[string]tftypes.Value{"env": s(`a"b`)}, true, "not allowed"},
		{"reserved prefix any case", map[string]tftypes.Value{"Frostmoln_team": s("x")}, true, "reserved"},
		{"instance-reserved prefix", map[string]tftypes.Value{"os_type": s("x")}, true, "reserved"},
		{"reserved key any case", map[string]tftypes.Value{"ACL": s("x")}, true, "reserved"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			resp, err := providerserver.NewProtocol6(provider.New("test")())().ValidateResourceConfig(context.Background(),
				&tfprotov6.ValidateResourceConfigRequest{TypeName: typeName, Config: dyn(t, value(t, null, null, tags(tc.tags)))})
			if err != nil {
				t.Fatal(err)
			}
			out, refused := text(resp.Diagnostics)
			if refused != tc.refuse {
				t.Fatalf("refused = %v, want %v:\n%s", refused, tc.refuse, out)
			}
			if tc.refuse && !strings.Contains(out, tc.want) {
				t.Errorf("the refusal must say %q, got:\n%s", tc.want, out)
			}
		})
	}
}

func plan(t *testing.T, prior, proposed, config tftypes.Value) *tfprotov6.PlanResourceChangeResponse {
	t.Helper()
	resp, err := providerserver.NewProtocol6(provider.New("test")())().PlanResourceChange(context.Background(),
		&tfprotov6.PlanResourceChangeRequest{
			TypeName: typeName, PriorState: dyn(t, prior), ProposedNewState: dyn(t, proposed), Config: dyn(t, config),
		})
	if err != nil {
		t.Fatal(err)
	}
	if out, isErr := text(resp.Diagnostics); isErr {
		t.Fatalf("plan: %s", out)
	}
	return resp
}

func TestPlan_TagChangesUpdateInPlace(t *testing.T) {
	t.Parallel()
	prior := value(t, s(tenantA), s(tenantA), tags(map[string]tftypes.Value{"env": s("prod")}))
	newTags := tags(map[string]tftypes.Value{"env": s("staging"), "team": s("web")})
	resp := plan(t, prior, value(t, s(tenantA), s(tenantA), newTags), value(t, null, null, newTags))
	if len(resp.RequiresReplace) != 0 {
		t.Fatalf("a tag change must update in place, got replace on %v", resp.RequiresReplace)
	}
}

// Flipping apply_to_existing_on_change alone is an in-place update of the flag
// (never a replacement, which would clear the tenant's defaults), and leaving
// it out of the configuration plans it as false.
func TestPlan_ApplyToExistingFlag(t *testing.T) {
	t.Parallel()
	b := func(v any) tftypes.Value { return tftypes.NewValue(tftypes.Bool, v) }
	withFlag := func(v tftypes.Value, flag tftypes.Value) tftypes.Value {
		var attrs map[string]tftypes.Value
		_ = v.As(&attrs)
		attrs["apply_to_existing_on_change"] = flag
		return tftypes.NewValue(objType(t), attrs)
	}
	tg := tags(map[string]tftypes.Value{"env": s("prod")})
	prior := value(t, s(tenantA), s(tenantA), tg)

	resp := plan(t, prior, withFlag(prior, b(true)), withFlag(value(t, null, null, tg), b(true)))
	if len(resp.RequiresReplace) != 0 {
		t.Fatalf("a flag-only change must not replace, got %v", resp.RequiresReplace)
	}
	planned, err := resp.PlannedState.Unmarshal(objType(t))
	if err != nil {
		t.Fatal(err)
	}
	var attrs map[string]tftypes.Value
	_ = planned.As(&attrs)
	if !attrs["apply_to_existing_on_change"].Equal(b(true)) {
		t.Errorf("planned flag = %v, want true", attrs["apply_to_existing_on_change"])
	}

	// Omitted from the configuration: its default, false.
	resp = plan(t, tftypes.NewValue(objType(t), nil), withFlag(value(t, null, null, tg), b(nil)), withFlag(value(t, null, null, tg), b(nil)))
	planned, _ = resp.PlannedState.Unmarshal(objType(t))
	_ = planned.As(&attrs)
	if !attrs["apply_to_existing_on_change"].Equal(b(false)) {
		t.Errorf("an omitted flag plans %v, want false", attrs["apply_to_existing_on_change"])
	}
}
