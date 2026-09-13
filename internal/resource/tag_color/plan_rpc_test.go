package tag_color_test

import (
	"context"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/tfsdk"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/tagsettings/tagsettingstest"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	fwresource "github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/provider"
	tagcolor "go.frostmoln.internal/terraform-provider-frostmoln/internal/resource/tag_color"
)

// These drive the real protocol RPCs — ValidateResourceConfig (where the
// attribute validators run, before validate/plan/refresh/import alike) and
// PlanResourceChange (where the plan modifiers decide replace vs update) — so
// the claims the schema makes about WHEN a value is refused and WHAT a change
// does are established rather than assumed.

const typeName = "frostmoln_tag_color"

const (
	orgA   = "22222222-2222-4222-8222-222222222222"
	orgB   = "33333333-3333-4333-8333-333333333333"
	ruleID = "44444444-4444-4444-8444-444444444444"
)

func objType(t *testing.T) tftypes.Object {
	t.Helper()
	var sr fwresource.SchemaResponse
	tagcolor.NewResource().Schema(context.Background(), fwresource.SchemaRequest{}, &sr)
	o, ok := sr.Schema.Type().TerraformType(context.Background()).(tftypes.Object)
	if !ok {
		t.Fatal("not an object type")
	}
	return o
}

func value(t *testing.T, overrides map[string]tftypes.Value) tftypes.Value {
	t.Helper()
	o := objType(t)
	attrs := make(map[string]tftypes.Value, len(o.AttributeTypes))
	for name, at := range o.AttributeTypes {
		attrs[name] = tftypes.NewValue(at, nil)
	}
	for name, v := range overrides {
		if _, ok := o.AttributeTypes[name]; !ok {
			t.Fatalf("%q is not an attribute", name)
		}
		attrs[name] = v
	}
	return tftypes.NewValue(o, attrs)
}

func dyn(t *testing.T, v tftypes.Value) *tfprotov6.DynamicValue {
	t.Helper()
	dv, err := tfprotov6.NewDynamicValue(objType(t), v)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	return &dv
}

func s(v string) tftypes.Value { return tftypes.NewValue(tftypes.String, v) }

var null = tftypes.NewValue(tftypes.String, nil)

func validate(t *testing.T, cfg map[string]tftypes.Value) []*tfprotov6.Diagnostic {
	t.Helper()
	resp, err := providerserver.NewProtocol6(provider.New("test")())().ValidateResourceConfig(context.Background(),
		&tfprotov6.ValidateResourceConfigRequest{TypeName: typeName, Config: dyn(t, value(t, cfg))})
	if err != nil {
		t.Fatalf("ValidateResourceConfig: %v", err)
	}
	return resp.Diagnostics
}

func hasError(diags []*tfprotov6.Diagnostic) bool {
	for _, d := range diags {
		if d.Severity == tfprotov6.DiagnosticSeverityError {
			return true
		}
	}
	return false
}

func text(diags []*tfprotov6.Diagnostic) string {
	var b strings.Builder
	for _, d := range diags {
		b.WriteString(d.Summary + ": " + d.Detail + "\n")
	}
	return b.String()
}

// TestValidateMirrorsTheServerNeverStricter pins both edges of every rule: the
// refusal fires where identity's ValidateTagColorRule refuses, and the value
// just inside the edge is accepted, so the plan-time check can never refuse a
// rule the platform would have taken.
func TestValidateMirrorsTheServerNeverStricter(t *testing.T) {
	t.Parallel()
	base := func(over map[string]tftypes.Value) map[string]tftypes.Value {
		cfg := map[string]tftypes.Value{"key": s("env"), "color": s("#d73a49")}
		for k, v := range over {
			cfg[k] = v
		}
		return cfg
	}
	cases := []struct {
		name   string
		cfg    map[string]tftypes.Value
		refuse bool
		want   string
	}{
		{"minimal", base(nil), false, ""},
		{"uppercase colour", base(map[string]tftypes.Value{"color": s("#ABCDEF")}), false, ""},
		{"mixed-case colour", base(map[string]tftypes.Value{"color": s("#aBcDeF")}), false, ""},
		{"short colour", base(map[string]tftypes.Value{"color": s("#12345")}), true, "#RRGGBB"},
		{"named colour", base(map[string]tftypes.Value{"color": s("red")}), true, "#RRGGBB"},
		{"alpha colour", base(map[string]tftypes.Value{"color": s("#11223344")}), true, "#RRGGBB"},
		{"no hash", base(map[string]tftypes.Value{"color": s("d73a49")}), true, "#RRGGBB"},
		{"empty key", base(map[string]tftypes.Value{"key": s("")}), true, "1 to 255"},
		{"255-char key", base(map[string]tftypes.Value{"key": s(strings.Repeat("k", 255))}), false, ""},
		{"255 multibyte runes", base(map[string]tftypes.Value{"key": s(strings.Repeat("å", 255))}), false, ""},
		{"256-char key", base(map[string]tftypes.Value{"key": s(strings.Repeat("k", 256))}), true, "1 to 255"},
		{"key with a slash and spaces", base(map[string]tftypes.Value{"key": s("team/owner name")}), false, ""},
		{"key with a control char", base(map[string]tftypes.Value{"key": s("env\n")}), true, "control or formatting"},
		{"key with a bidi override", base(map[string]tftypes.Value{"key": s("env\u202e")}), true, "control or formatting"},
		{"reserved prefix", base(map[string]tftypes.Value{"key": s("frostmoln_x")}), true, "reserved"},
		{"reserved dash prefix", base(map[string]tftypes.Value{"key": s("frostmoln-x")}), true, "reserved"},
		{"reserved prefix is case-sensitive", base(map[string]tftypes.Value{"key": s("Frostmoln_Team")}), false, ""},
		{"empty value", base(map[string]tftypes.Value{"value": s("")}), false, ""},
		{"256-char value", base(map[string]tftypes.Value{"value": s(strings.Repeat("v", 256))}), false, ""},
		{"257-char value", base(map[string]tftypes.Value{"value": s(strings.Repeat("v", 257))}), true, "256"},
		{"value with quotes and slashes", base(map[string]tftypes.Value{"value": s(`a "b" \c/d`)}), false, ""},
		{"value with a line separator", base(map[string]tftypes.Value{"value": s("a\u2028b")}), true, "control or formatting"},
		{"organization id", base(map[string]tftypes.Value{"organization_id": s(orgA)}), false, ""},
		// Parseable but not canonical: refused naming the canonical spelling, so
		// the configuration can never disagree with what import/refresh record.
		{"uppercase organization id", base(map[string]tftypes.Value{"organization_id": s("ABCDEF00-2222-4222-8222-222222222222")}), true, "Write the organization id as"},
		{"braced organization id", base(map[string]tftypes.Value{"organization_id": s("{" + orgA + "}")}), true, "Write the organization id as"},
		{"organization id not a uuid", base(map[string]tftypes.Value{"organization_id": s("my-org")}), true, "organization id"},
		{"organization id dot segment", base(map[string]tftypes.Value{"organization_id": s("..")}), true, "organization id"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			diags := validate(t, tc.cfg)
			if got := hasError(diags); got != tc.refuse {
				t.Fatalf("refused = %v, want %v:\n%s", got, tc.refuse, text(diags))
			}
			if tc.refuse && !strings.Contains(text(diags), tc.want) {
				t.Errorf("the refusal must say %q, got:\n%s", tc.want, text(diags))
			}
		})
	}
}

func plan(t *testing.T, prior, config tftypes.Value) *tfprotov6.PlanResourceChangeResponse {
	t.Helper()
	// Terraform core's proposed new state: config for configurable
	// attributes, prior state where the config is null on a computed one.
	var pa, ca map[string]tftypes.Value
	if err := prior.As(&pa); err != nil {
		t.Fatal(err)
	}
	if err := config.As(&ca); err != nil {
		t.Fatal(err)
	}
	proposed := map[string]tftypes.Value{}
	for k, v := range ca {
		proposed[k] = v
		if v.IsNull() {
			switch k {
			case "id", "organization_id", "created_at", "updated_at":
				proposed[k] = pa[k]
			}
		}
	}
	resp, err := providerserver.NewProtocol6(provider.New("test")())().PlanResourceChange(context.Background(),
		&tfprotov6.PlanResourceChangeRequest{
			TypeName:         typeName,
			PriorState:       dyn(t, prior),
			ProposedNewState: dyn(t, tftypes.NewValue(objType(t), proposed)),
			Config:           dyn(t, config),
		})
	if err != nil {
		t.Fatalf("PlanResourceChange: %v", err)
	}
	if hasError(resp.Diagnostics) {
		t.Fatalf("plan: %s", text(resp.Diagnostics))
	}
	return resp
}

func settled(t *testing.T) tftypes.Value {
	return value(t, map[string]tftypes.Value{
		"id":              s(ruleID),
		"organization_id": s(orgA),
		"key":             s("env"),
		"value":           s("prod"),
		"color":           s("#d73a49"),
		"created_at":      s("2026-09-13T12:00:01.000000Z"),
		"updated_at":      s("2026-09-13T12:00:01.000000Z"),
	})
}

func replaces(resp *tfprotov6.PlanResourceChangeResponse) []string {
	var out []string
	for _, p := range resp.RequiresReplace {
		out = append(out, p.String())
	}
	return out
}

func plannedAttr(t *testing.T, resp *tfprotov6.PlanResourceChangeResponse, name string) tftypes.Value {
	t.Helper()
	v, err := resp.PlannedState.Unmarshal(objType(t))
	if err != nil {
		t.Fatal(err)
	}
	var attrs map[string]tftypes.Value
	if err := v.As(&attrs); err != nil {
		t.Fatal(err)
	}
	return attrs[name]
}

// Retargeting the key or value, or recolouring, is an in-place update: the
// API's PUT replaces all three. A RequiresReplace on any of them would delete
// and re-create the rule for nothing — and, since it is re-created with a new
// id, break anything that referenced it.
func TestPlan_KeyValueColourChangesUpdateInPlace(t *testing.T) {
	t.Parallel()
	for _, change := range []map[string]tftypes.Value{
		{"key": s("team")},
		{"value": s("")},
		{"value": null},
		{"color": s("#D73A49")},
	} {
		cfg := map[string]tftypes.Value{"key": s("env"), "value": s("prod"), "color": s("#d73a49")}
		for k, v := range change {
			cfg[k] = v
		}
		resp := plan(t, settled(t), value(t, cfg))
		if r := replaces(resp); len(r) != 0 {
			t.Errorf("change %v must update in place, but the plan replaces on %v", change, r)
		}
		if id := plannedAttr(t, resp, "id"); !id.Equal(s(ruleID)) {
			t.Errorf("change %v: planned id = %v, want the rule's id kept", change, id)
		}
	}
}

// organization_id omitted from the configuration (the intended usage) must
// keep the resolved organization and never plan a replacement — the ordering
// trap planmodifier_order_test.go describes, here for this resource.
func TestPlan_OmittedOrganizationKeepsStateAndDoesNotReplace(t *testing.T) {
	t.Parallel()
	resp := plan(t, settled(t), value(t, map[string]tftypes.Value{
		"key": s("env"), "value": s("prod"), "color": s("#123456"),
	}))
	if r := replaces(resp); len(r) != 0 {
		t.Fatalf("an omitted organization_id must not replace, got %v", r)
	}
	if org := plannedAttr(t, resp, "organization_id"); !org.Equal(s(orgA)) {
		t.Errorf("planned organization_id = %v, want the state's %s", org, orgA)
	}
}

func TestPlan_ChangingOrganizationReplaces(t *testing.T) {
	t.Parallel()
	resp := plan(t, settled(t), value(t, map[string]tftypes.Value{
		"organization_id": s(orgB), "key": s("env"), "value": s("prod"), "color": s("#d73a49"),
	}))
	if r := replaces(resp); len(r) != 1 || !strings.Contains(r[0], "organization_id") {
		t.Fatalf("moving a rule to another organization must replace it, got %v", r)
	}
}

// An import spelled in upper case records the canonical organization id, so the
// canonical organization_id a configuration must use plans no replacement.
func TestImportUpperCaseThenPlanAgainstCanonicalConfigDoesNotReplace(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := tagsettingstest.New(t)
	ruleID := f.Seed(f.OrgID, "env", nil, "#d73a49")

	c := client.NewClient(f.Server.URL, "test-key") // pragma: allowlist secret
	if err := c.Configure(ctx); err != nil {
		t.Fatal(err)
	}
	r := tagcolor.NewResource()
	var cr fwresource.ConfigureResponse
	r.(fwresource.ResourceWithConfigure).Configure(ctx, fwresource.ConfigureRequest{ProviderData: c}, &cr)
	var sr fwresource.SchemaResponse
	r.Schema(ctx, fwresource.SchemaRequest{}, &sr)

	imp := &fwresource.ImportStateResponse{State: tfsdk.State{Schema: sr.Schema, Raw: tftypes.NewValue(objType(t), nil)}}
	r.(fwresource.ResourceWithImportState).ImportState(ctx,
		fwresource.ImportStateRequest{ID: strings.ToUpper(f.OrgID) + "/" + strings.ToUpper(ruleID)}, imp)
	if imp.Diagnostics.HasError() {
		t.Fatalf("import: %v", imp.Diagnostics.Errors())
	}
	read := &fwresource.ReadResponse{State: imp.State}
	r.Read(ctx, fwresource.ReadRequest{State: imp.State}, read)
	if read.Diagnostics.HasError() {
		t.Fatalf("read: %v", read.Diagnostics.Errors())
	}

	resp := plan(t, read.State.Raw, value(t, map[string]tftypes.Value{
		"organization_id": s(f.OrgID), "key": s("env"), "color": s("#d73a49"),
	}))
	if r := replaces(resp); len(r) != 0 {
		t.Fatalf("an upper-case import must not plan a replacement against the canonical id, got %v", r)
	}
}
