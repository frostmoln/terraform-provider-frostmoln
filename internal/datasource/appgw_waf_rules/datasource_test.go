package appgw_waf_rules

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
)

const draftBody = `{"version":{"version":3},"rules":[
  {"ruleKey":"mine","owner":"tenant","kind":"raw","raw":"SecRule ARGS \"@rx x\" \"id:1\"",
   "secRuleId":4000001,"ordinal":10,"enabled":true,"revision":4},
  {"ruleKey":"platform-cve-2026-1234","owner":"platform","kind":"raw","secRuleId":4500001,
   "enabled":true,"optedOut":true,"optOutAllowed":true,"revision":1},
  {"ruleKey":"platform-selfprotect","owner":"platform","kind":"raw","secRuleId":4500002,
   "enabled":true,"optOutAllowed":false,"revision":1}
],"exclusions":[]}`

func read(t *testing.T, d *rulesDataSource, m RulesModel) (RulesModel, string) {
	t.Helper()
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_, _ = w.Write([]byte(draftBody))
	}))
	t.Cleanup(srv.Close)
	c := client.NewClient(srv.URL, "k", client.WithHTTPClient(srv.Client())) // pragma: allowlist secret
	c.SetTenantIDForTest("t-1")
	d.client = c

	var sr datasource.SchemaResponse
	d.Schema(context.Background(), datasource.SchemaRequest{}, &sr)
	st := tfsdk.State{Schema: sr.Schema}
	if diags := st.Set(context.Background(), &m); diags.HasError() {
		t.Fatalf("config: %v", diags.Errors())
	}
	resp := datasource.ReadResponse{State: tfsdk.State{Schema: sr.Schema}}
	d.Read(context.Background(), datasource.ReadRequest{
		Config: tfsdk.Config{Schema: sr.Schema, Raw: st.Raw}}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("read: %v", resp.Diagnostics.Errors())
	}
	var out RulesModel
	resp.State.Get(context.Background(), &out)
	return out, gotPath
}

func baseModel() RulesModel {
	return RulesModel{
		GatewayID: types.StringValue("agw-1"),
		PolicyID:  types.StringValue("wp-1"),
	}
}

// TestTenantSourceExcludesPlatformRules.
//
// 🔴 THIS FILTER IS THE WHOLE POINT. Without it, every emergency virtual patch
// the platform ships appears in the list a configuration iterates over,
// dirtying `terraform plan` for a change the customer did not make and cannot
// undo. The second time that happens they stop trusting plans.
func TestTenantSourceExcludesPlatformRules(t *testing.T) {
	out, _ := read(t, NewTenantDataSource().(*rulesDataSource), baseModel())
	if len(out.Rules) != 1 {
		t.Fatalf("the tenant source returned %d rules, want only the tenant-owned one: %+v",
			len(out.Rules), out.Rules)
	}
	if out.Rules[0].RuleKey.ValueString() != "mine" {
		t.Fatalf("returned %q, want the tenant's rule", out.Rules[0].RuleKey.ValueString())
	}
	if out.Version.ValueInt64() != 3 {
		t.Errorf("version = %v, want 3", out.Version)
	}
}

// TestPlatformSourceReturnsOnlyPlatformRules, including the opt-out state that
// makes a withdrawn protection visible to a configuration that asserts on it.
func TestPlatformSourceReturnsOnlyPlatformRules(t *testing.T) {
	out, _ := read(t, NewPlatformDataSource().(*rulesDataSource), baseModel())
	if len(out.Rules) != 2 {
		t.Fatalf("the platform source returned %d rules, want 2: %+v", len(out.Rules), out.Rules)
	}
	byKey := map[string]RuleModel{}
	for _, r := range out.Rules {
		byKey[r.RuleKey.ValueString()] = r
	}
	if !byKey["platform-cve-2026-1234"].OptedOut.ValueBool() {
		t.Error("an opted-out platform rule must report opted_out = true")
	}
	if byKey["platform-selfprotect"].OptOutAllowed.ValueBool() {
		t.Error("a self-protection rule must report opt_out_allowed = false")
	}
}

// TestSourceSelectsDraftOrActive. They differ whenever there are unpublished
// changes, so reading the wrong one silently answers a different question.
func TestSourceSelectsDraftOrActive(t *testing.T) {
	_, path := read(t, NewTenantDataSource().(*rulesDataSource), baseModel())
	if want := "/v1/tenants/t-1/application-gateways/agw-1/waf-policies/wp-1/draft"; path != want {
		t.Fatalf("default read hit %q, want %q", path, want)
	}

	m := baseModel()
	m.Source = types.StringValue("active")
	_, path = read(t, NewTenantDataSource().(*rulesDataSource), m)
	if want := "/v1/tenants/t-1/application-gateways/agw-1/waf-policies/wp-1/versions/active"; path != want {
		t.Fatalf("source = active hit %q, want %q", path, want)
	}
}

// TestUnknownSourceIsRefused rather than silently falling back to the draft,
// which would answer a question nobody asked.
func TestUnknownSourceIsRefused(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(draftBody))
	}))
	defer srv.Close()
	c := client.NewClient(srv.URL, "k", client.WithHTTPClient(srv.Client())) // pragma: allowlist secret
	c.SetTenantIDForTest("t-1")
	d := NewTenantDataSource().(*rulesDataSource)
	d.client = c

	var sr datasource.SchemaResponse
	d.Schema(context.Background(), datasource.SchemaRequest{}, &sr)
	m := baseModel()
	m.Source = types.StringValue("published")
	st := tfsdk.State{Schema: sr.Schema}
	_ = st.Set(context.Background(), &m)
	resp := datasource.ReadResponse{State: tfsdk.State{Schema: sr.Schema}}
	d.Read(context.Background(), datasource.ReadRequest{
		Config: tfsdk.Config{Schema: sr.Schema, Raw: st.Raw}}, &resp)
	if !resp.Diagnostics.HasError() {
		t.Fatal("an unknown source must be refused, not silently read as the draft")
	}
}
