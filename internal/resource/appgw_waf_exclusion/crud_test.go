package appgw_waf_exclusion

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	tfpath "github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
)

// schemaOf returns this resource's schema, which every plan and state in these
// tests is built against.
func schemaOf(t *testing.T) tfsdk.Plan {
	t.Helper()
	r := NewResource()
	var sr resource.SchemaResponse
	r.Schema(context.Background(), resource.SchemaRequest{}, &sr)
	if sr.Diagnostics.HasError() {
		t.Fatalf("schema: %v", sr.Diagnostics.Errors())
	}
	return tfsdk.Plan{Schema: sr.Schema}
}

func planOf(t *testing.T, m ExclusionModel) tfsdk.Plan {
	t.Helper()
	p := schemaOf(t)
	if d := p.Set(context.Background(), &m); d.HasError() {
		t.Fatalf("plan: %v", d.Errors())
	}
	return p
}

func stateOf(t *testing.T, m ExclusionModel) tfsdk.State {
	t.Helper()
	p := schemaOf(t)
	s := tfsdk.State{Schema: p.Schema}
	if d := s.Set(context.Background(), &m); d.HasError() {
		t.Fatalf("state: %v", d.Errors())
	}
	return s
}

// emptyState is what Create writes into.
func emptyState(t *testing.T) tfsdk.State {
	t.Helper()
	return tfsdk.State{Schema: schemaOf(t).Schema}
}

// serve builds a client pointed at a handler, with the tenant pre-resolved.
func serve(t *testing.T, h http.HandlerFunc) (*client.Client, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	c := client.NewClient(srv.URL, "test-key", client.WithHTTPClient(srv.Client())) // pragma: allowlist secret
	c.SetTenantIDForTest("t-1")
	return c, srv
}

// TestConfigureRejectsUnexpectedProviderData. A provider that hands the wrong
// type must be a clear error, not a nil-pointer panic on the first API call.
func TestConfigureRejectsUnexpectedProviderData(t *testing.T) {
	r := NewResource().(interface {
		Configure(context.Context, resource.ConfigureRequest, *resource.ConfigureResponse)
	})
	var resp resource.ConfigureResponse
	r.Configure(context.Background(), resource.ConfigureRequest{}, &resp)
	if resp.Diagnostics.HasError() {
		t.Errorf("a nil ProviderData is the not-yet-configured case and must be silent: %v",
			resp.Diagnostics.Errors())
	}
	r.Configure(context.Background(), resource.ConfigureRequest{ProviderData: 42}, &resp)
	if !resp.Diagnostics.HasError() {
		t.Error("the wrong ProviderData type must be an error, not a later panic")
	}
}

var (
	_ = json.Marshal
	_ = types.StringValue
)

const exBase = "/v1/tenants/t-1/application-gateways/agw-1/waf-policies/wp-1"

func exFixture() apiExclusion {
	return apiExclusion{
		RuleKey: "allow-search", TargetSecRuleID: 942100, Variable: "ARGS",
		Selector: "q", PathPattern: "^/search", Ordinal: 1, Revision: 2,
		CreatedAt: "2026-08-01T00:00:00Z", UpdatedAt: "2026-08-01T00:00:00Z",
	}
}

func exModel() ExclusionModel {
	return ExclusionModel{
		GatewayID:       types.StringValue("agw-1"),
		PolicyID:        types.StringValue("wp-1"),
		RuleKey:         types.StringValue("allow-search"),
		TargetSecRuleID: types.Int64Value(942100),
		Variable:        types.StringValue("ARGS"),
	}
}

func TestExclusionCreateReadUpdateDelete(t *testing.T) {
	c, _ := serve(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPut && r.URL.Path == exBase+"/exclusions/allow-search":
			_ = json.NewEncoder(w).Encode(exFixture())
		case r.Method == http.MethodGet && r.URL.Path == exBase+"/draft":
			_ = json.NewEncoder(w).Encode(apiDraftResponse{Exclusions: []apiExclusion{exFixture()}})
		case r.Method == http.MethodDelete && r.URL.Path == exBase+"/exclusions/allow-search":
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	})
	er := &exclusionResource{client: c}

	createResp := resource.CreateResponse{State: emptyState(t)}
	er.Create(context.Background(), resource.CreateRequest{Plan: planOf(t, exModel())}, &createResp)
	if createResp.Diagnostics.HasError() {
		t.Fatalf("create: %v", createResp.Diagnostics.Errors())
	}
	var created ExclusionModel
	createResp.State.Get(context.Background(), &created)
	if created.Revision.ValueInt64() != 2 {
		t.Fatalf("revision = %v — it is what orders the publication", created.Revision)
	}
	if created.GatewayID.ValueString() != "agw-1" || created.PolicyID.ValueString() != "wp-1" {
		t.Errorf("addressing was lost: %+v", created)
	}

	readResp := resource.ReadResponse{State: stateOf(t, created)}
	er.Read(context.Background(), resource.ReadRequest{State: stateOf(t, created)}, &readResp)
	if readResp.Diagnostics.HasError() || readResp.State.Raw.IsNull() {
		t.Fatalf("read: %v", readResp.Diagnostics.Errors())
	}

	updResp := resource.UpdateResponse{State: stateOf(t, created)}
	er.Update(context.Background(), resource.UpdateRequest{Plan: planOf(t, created)}, &updResp)
	if updResp.Diagnostics.HasError() {
		t.Fatalf("update: %v", updResp.Diagnostics.Errors())
	}

	delResp := resource.DeleteResponse{State: stateOf(t, created)}
	er.Delete(context.Background(), resource.DeleteRequest{State: stateOf(t, created)}, &delResp)
	if delResp.Diagnostics.HasError() {
		t.Fatalf("delete: %v", delResp.Diagnostics.Errors())
	}
}

func TestExclusionReadRemovesOneDeletedFromTheDraft(t *testing.T) {
	c, _ := serve(t, func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(apiDraftResponse{Exclusions: nil})
	})
	er := &exclusionResource{client: c}
	resp := resource.ReadResponse{State: stateOf(t, exModel())}
	er.Read(context.Background(), resource.ReadRequest{State: stateOf(t, exModel())}, &resp)
	if resp.Diagnostics.HasError() || !resp.State.Raw.IsNull() {
		t.Fatal("an exclusion absent from the draft must be removed from state")
	}
}

// TestExclusionValidateConfigRequiresExactlyOneTarget. Both or neither is
// meaningless: an exclusion narrows one rule, or every rule carrying a tag.
func TestExclusionValidateConfigRequiresExactlyOneTarget(t *testing.T) {
	r := NewResource().(resource.ResourceWithValidateConfig)
	check := func(m ExclusionModel) bool {
		p := planOf(t, m)
		var resp resource.ValidateConfigResponse
		r.ValidateConfig(context.Background(), resource.ValidateConfigRequest{
			Config: tfsdk.Config(p),
		}, &resp)
		return resp.Diagnostics.HasError()
	}

	if check(exModel()) {
		t.Error("a valid exclusion targeting one SecRule was refused")
	}

	neither := exModel()
	neither.TargetSecRuleID = types.Int64Null()
	if !check(neither) {
		t.Error("naming no target must be refused")
	}

	both := exModel()
	both.TargetTag = types.StringValue("attack-sqli")
	if !check(both) {
		t.Error("naming both targets must be refused")
	}

	tagOnly := exModel()
	tagOnly.TargetSecRuleID = types.Int64Null()
	tagOnly.TargetTag = types.StringValue("attack-sqli")
	if check(tagOnly) {
		t.Error("targeting a tag alone is valid and was refused")
	}

	longPattern := exModel()
	longPattern.PathPattern = types.StringValue(strings.Repeat("x", 1025))
	if !check(longPattern) {
		t.Error("an over-long path_pattern must be refused")
	}
}

// TestImportState pins the composite id format.
//
// An import id is the only interface a practitioner has for adopting an
// existing resource, and getting it wrong silently produces a resource with
// empty addressing that fails on its first refresh with an unrelated error.
// The malformed cases must be refused by name.
func TestImportState(t *testing.T) {
	r := NewResource().(resource.ResourceWithImportState)

	run := func(id string) (resource.ImportStateResponse, bool) {
		resp := resource.ImportStateResponse{State: importState(t)}
		r.ImportState(context.Background(), resource.ImportStateRequest{ID: id}, &resp)
		return resp, resp.Diagnostics.HasError()
	}

	for _, bad := range []string{"", "agw-1", "agw-1/wp-1", "agw-1//k"} {
		if _, err := run(bad); !err {
			t.Errorf("import id %q was accepted; the format is %s", bad, "{gateway_id}/{policy_id}/{rule_key}")
		}
	}

	resp, err := run("agw-1/wp-1/allow-search")
	if err {
		t.Fatalf("a well-formed import id was refused: %v", resp.Diagnostics.Errors())
	}
	{
		var got types.String
		if d := resp.State.GetAttribute(context.Background(), tfpath.Root("gateway_id"), &got); d.HasError() {
			t.Fatalf("read gateway_id: %v", d.Errors())
		}
		if got.ValueString() != "agw-1" {
			t.Errorf("gateway_id = %q, want agw-1", got.ValueString())
		}
	}
	{
		var got types.String
		if d := resp.State.GetAttribute(context.Background(), tfpath.Root("policy_id"), &got); d.HasError() {
			t.Fatalf("read policy_id: %v", d.Errors())
		}
		if got.ValueString() != "wp-1" {
			t.Errorf("policy_id = %q, want wp-1", got.ValueString())
		}
	}
	{
		var got types.String
		if d := resp.State.GetAttribute(context.Background(), tfpath.Root("rule_key"), &got); d.HasError() {
			t.Fatalf("read rule_key: %v", d.Errors())
		}
		if got.ValueString() != "allow-search" {
			t.Errorf("rule_key = %q, want allow-search", got.ValueString())
		}
	}
}

// importState is the state Terraform hands ImportState: an object whose every
// attribute is NULL, not the zero tfsdk.State — writing an attribute into the
// latter fails, because there is no object to write into.
//
// Derived from the schema rather than hand-listed, so adding an attribute does
// not silently leave it out.
func importState(t *testing.T) tfsdk.State {
	t.Helper()
	s := schemaOf(t).Schema
	obj := s.Type().TerraformType(context.Background()).(tftypes.Object)
	attrs := make(map[string]tftypes.Value, len(obj.AttributeTypes))
	for name, at := range obj.AttributeTypes {
		attrs[name] = tftypes.NewValue(at, nil)
	}
	return tfsdk.State{Schema: s, Raw: tftypes.NewValue(obj, attrs)}
}
