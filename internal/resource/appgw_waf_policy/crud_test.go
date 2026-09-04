package appgw_waf_policy

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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

func planOf(t *testing.T, m PolicyModel) tfsdk.Plan {
	t.Helper()
	p := schemaOf(t)
	if d := p.Set(context.Background(), &m); d.HasError() {
		t.Fatalf("plan: %v", d.Errors())
	}
	return p
}

func stateOf(t *testing.T, m PolicyModel) tfsdk.State {
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

const wpBase = "/v1/tenants/t-1/application-gateways/agw-1/waf-policies"

func wpFixture() apiPolicy {
	active, draft := 2, 3
	return apiPolicy{
		ID: "wp-1", GatewayID: "agw-1", Name: "default", Mode: "detect",
		ParanoiaLevel: 2, AnomalyScoreThreshold: 5, CRSVersion: "4.7.0",
		FailMode: "open", RequestBodyLimitBytes: 16384,
		ActiveVersion: &active, DraftVersion: &draft, CreatedAt: "2026-08-01T00:00:00Z",
	}
}

func wpModel() PolicyModel {
	return PolicyModel{
		GatewayID: types.StringValue("agw-1"),
		Name:      types.StringValue("default"),
		// TYPED nulls, not the zero types.List. A list value carrying no
		// element type fails the framework's type check the moment the model is
		// written into a plan or state, with a "MISSING TYPE" conversion error
		// that says nothing about the attribute that is actually wrong.
		AllowedMethods:                      types.ListNull(types.StringType),
		AllowedRequestContentTypes:          types.ListNull(types.StringType),
		EffectiveAllowedMethods:             types.ListNull(types.StringType),
		EffectiveAllowedRequestContentTypes: types.ListNull(types.StringType),
	}
}

func TestPolicyCreateReadDelete(t *testing.T) {
	c, _ := serve(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == wpBase:
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(wpFixture())
		case r.Method == http.MethodGet && r.URL.Path == wpBase+"/wp-1":
			_ = json.NewEncoder(w).Encode(wpFixture())
		case r.Method == http.MethodDelete && r.URL.Path == wpBase+"/wp-1":
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	})
	pr := &policyResource{client: c}

	createResp := resource.CreateResponse{State: emptyState(t)}
	pr.Create(context.Background(), resource.CreateRequest{Plan: planOf(t, wpModel())}, &createResp)
	if createResp.Diagnostics.HasError() {
		t.Fatalf("create: %v", createResp.Diagnostics.Errors())
	}
	var created PolicyModel
	createResp.State.Get(context.Background(), &created)
	if created.ID.ValueString() != "wp-1" || created.Mode.ValueString() != "detect" {
		t.Fatalf("create result wrong: %+v", created)
	}
	if created.ActiveVersion.ValueInt64() != 2 {
		t.Errorf("active_version = %v, want 2", created.ActiveVersion)
	}

	readResp := resource.ReadResponse{State: stateOf(t, created)}
	pr.Read(context.Background(), resource.ReadRequest{State: stateOf(t, created)}, &readResp)
	if readResp.Diagnostics.HasError() {
		t.Fatalf("read: %v", readResp.Diagnostics.Errors())
	}

	delResp := resource.DeleteResponse{State: stateOf(t, created)}
	pr.Delete(context.Background(), resource.DeleteRequest{State: stateOf(t, created)}, &delResp)
	if delResp.Diagnostics.HasError() {
		t.Fatalf("delete: %v", delResp.Diagnostics.Errors())
	}
}

// TestPolicyVersionsAreNullBeforeAnythingIsPublished. Null and 0 are different:
// version 0 does not exist, and reporting it would make an unpublished policy
// look like one enforcing something.
func TestPolicyVersionsAreNullBeforeAnythingIsPublished(t *testing.T) {
	var m PolicyModel
	p := wpFixture()
	p.ActiveVersion, p.DraftVersion = nil, nil
	m.fromAPI(&p)
	if !m.ActiveVersion.IsNull() || !m.DraftVersion.IsNull() {
		t.Fatalf("unpublished versions must be null, got %v / %v", m.ActiveVersion, m.DraftVersion)
	}
}

// TestPolicyUpdateSendsOnlyWhatChanged.
//
// 🔴 RE-SENDING AN UNCHANGED managed_ruleset_version IS NOT A NO-OP. The server
// validates a SUPPLIED pin against the running build while deliberately
// exempting a STORED one, because migrating a stale pin is the platform's job.
// Sending it anyway turns that exemption off and can fail an apply that changed
// nothing about it.
func TestPolicyUpdateSendsOnlyWhatChanged(t *testing.T) {
	var body map[string]any
	c, _ := serve(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&body)
		f := wpFixture()
		f.Mode = "block"
		_ = json.NewEncoder(w).Encode(f)
	})
	pr := &policyResource{client: c}

	var state PolicyModel
	state.fromAPI(&apiPolicy{
		ID: "wp-1", GatewayID: "agw-1", Name: "default", Mode: "detect",
		ParanoiaLevel: 2, AnomalyScoreThreshold: 5, CRSVersion: "4.7.0",
		FailMode: "open", RequestBodyLimitBytes: 16384,
	})
	plan := state
	plan.Mode = types.StringValue("block") // the only change

	resp := resource.UpdateResponse{State: stateOf(t, state)}
	pr.Update(context.Background(), resource.UpdateRequest{
		Plan: planOf(t, plan), State: stateOf(t, state),
	}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("update: %v", resp.Diagnostics.Errors())
	}
	if _, sent := body["crsVersion"]; sent {
		t.Fatalf("crsVersion was sent though it did not change: %v", body["crsVersion"])
	}
	if body["mode"] != "block" {
		t.Fatalf("mode was not sent: %v", body)
	}
	for _, unchanged := range []string{"paranoiaLevel", "failMode", "requestBodyLimitBytes", "anomalyScoreThreshold"} {
		if _, sent := body[unchanged]; sent {
			t.Errorf("%s was sent though it did not change", unchanged)
		}
	}
	// A settings change is authored, not enforced, and that has to be said.
	if resp.Diagnostics.WarningsCount() == 0 {
		t.Error("changing a setting must warn that it is not enforced until published and applied")
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

	for _, bad := range []string{"", "agw-1", "agw-1/", "/wp-1"} {
		if _, err := run(bad); !err {
			t.Errorf("import id %q was accepted; the format is %s", bad, "{gateway_id}/{policy_id}")
		}
	}

	resp, err := run("agw-1/wp-1")
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
		if d := resp.State.GetAttribute(context.Background(), tfpath.Root("id"), &got); d.HasError() {
			t.Fatalf("read id: %v", d.Errors())
		}
		if got.ValueString() != "wp-1" {
			t.Errorf("id = %q, want wp-1", got.ValueString())
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
