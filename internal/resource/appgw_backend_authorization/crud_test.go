package appgw_backend_authorization

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

func planOf(t *testing.T, m AuthorizationModel) tfsdk.Plan {
	t.Helper()
	p := schemaOf(t)
	if d := p.Set(context.Background(), &m); d.HasError() {
		t.Fatalf("plan: %v", d.Errors())
	}
	return p
}

func stateOf(t *testing.T, m AuthorizationModel) tfsdk.State {
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

var _ = json.Marshal
var _ = types.StringValue

const authzBase = "/v1/tenants/t-1/application-gateways/agw-1"

func authzFixture(adopted bool) apiAuthorization {
	return apiAuthorization{
		ID: "a-1", GatewayID: "agw-1", TargetSecurityGroupID: "sg-1",
		Protocol: "tcp", PortMin: 8080, PortMax: 8080, Adopted: adopted,
		AuthorizedBy: "jonas", CreatedAt: "2026-08-01T00:00:00Z",
	}
}

func authzModel() AuthorizationModel {
	return AuthorizationModel{
		GatewayID:       types.StringValue("agw-1"),
		PoolID:          types.StringValue("pool-1"),
		BackendID:       types.StringValue("b-1"),
		SecurityGroupID: types.StringValue("sg-1"),
		AdoptExisting:   types.BoolValue(false),
	}
}

func TestAuthorizationCreateReadDelete(t *testing.T) {
	c, _ := serve(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost &&
			r.URL.Path == authzBase+"/backend-pools/pool-1/backends/b-1/authorize":
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(authzFixture(false))
		case r.Method == http.MethodGet && r.URL.Path == authzBase+"/authorizations":
			_ = json.NewEncoder(w).Encode(apiAuthorizationListResponse{
				Items: []apiAuthorization{authzFixture(false)}})
		case r.Method == http.MethodDelete && r.URL.Path == authzBase+"/authorizations/a-1":
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	})
	ar := &authorizationResource{client: c}

	createResp := resource.CreateResponse{State: emptyState(t)}
	ar.Create(context.Background(), resource.CreateRequest{Plan: planOf(t, authzModel())}, &createResp)
	if createResp.Diagnostics.HasError() {
		t.Fatalf("create: %v", createResp.Diagnostics.Errors())
	}
	var created AuthorizationModel
	createResp.State.Get(context.Background(), &created)
	if created.ID.ValueString() != "a-1" {
		t.Fatalf("id = %q", created.ID.ValueString())
	}
	// The authorization payload carries neither pool nor backend -- the rule is
	// scoped to neither -- so Terraform's addressing must survive the create.
	if created.PoolID.ValueString() != "pool-1" || created.BackendID.ValueString() != "b-1" {
		t.Fatalf("pool/backend addressing was lost: %+v", created)
	}

	readResp := resource.ReadResponse{State: stateOf(t, created)}
	ar.Read(context.Background(), resource.ReadRequest{State: stateOf(t, created)}, &readResp)
	if readResp.Diagnostics.HasError() || readResp.State.Raw.IsNull() {
		t.Fatalf("read: %v", readResp.Diagnostics.Errors())
	}

	delResp := resource.DeleteResponse{State: stateOf(t, created)}
	ar.Delete(context.Background(), resource.DeleteRequest{State: stateOf(t, created)}, &delResp)
	if delResp.Diagnostics.HasError() {
		t.Fatalf("delete: %v", delResp.Diagnostics.Errors())
	}
}

// TestAuthorizationRefusesAnUnaskedForAdoption.
//
// 🔴 A WARNING IS NOT ENOUGH, AND create_before_destroy IS WHY. The platform
// keys one row per (gateway, security group, protocol, port) and its upsert
// RETURNS THE EXISTING ROW'S ID, so an adopted resource shares its identity
// with whatever created the rule first. Terraform then creates (getting that
// shared id) and destroys the prior object BY THAT ID — deleting the rule it
// just "created". The apply is green, state says the path is open, and the
// backend is unreachable. A warning printed during the create is two steps too
// early to read as a prediction of that.
func TestAuthorizationRefusesAnUnaskedForAdoption(t *testing.T) {
	c, _ := serve(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(authzFixture(true))
	})
	ar := &authorizationResource{client: c}
	resp := resource.CreateResponse{State: emptyState(t)}
	ar.Create(context.Background(), resource.CreateRequest{Plan: planOf(t, authzModel())}, &resp)

	if !resp.Diagnostics.HasError() {
		t.Fatal("adopting an existing rule without adopt_existing must REFUSE, not warn")
	}
	detail := resp.Diagnostics.Errors()[0].Detail()
	if !strings.Contains(detail, "adopt_existing") {
		t.Errorf("the refusal must name the opt-in that permits it:\n%s", detail)
	}

	// 🔴 STATE MUST STILL BE WRITTEN. The authorization row exists either way,
	// and leaving Terraform unaware of it strands an open ingress rule with
	// nothing to revoke it.
	if resp.State.Raw.IsNull() {
		t.Fatal("the refusal dropped the resource from state, stranding an open ingress rule")
	}
}

// TestAuthorizationAdoptsWhenAskedTo. The opt-in is the informed-decision path,
// and it must actually work.
func TestAuthorizationAdoptsWhenAskedTo(t *testing.T) {
	c, _ := serve(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(authzFixture(true))
	})
	ar := &authorizationResource{client: c}
	m := authzModel()
	m.AdoptExisting = types.BoolValue(true)
	resp := resource.CreateResponse{State: emptyState(t)}
	ar.Create(context.Background(), resource.CreateRequest{Plan: planOf(t, m)}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("adopt_existing = true must permit the adoption: %v", resp.Diagnostics.Errors())
	}
	var out AuthorizationModel
	resp.State.Get(context.Background(), &out)
	if !out.Adopted.ValueBool() {
		t.Error("an adopted authorization must record that it is shared")
	}
	if !out.AdoptExisting.ValueBool() {
		t.Error("the opt-in was lost: fromAPI must not clobber configuration")
	}
}

// TestAdoptedIsPreservedAcrossARefresh.
//
// 🔴 `adopted` IS NOT PERSISTED SERVER-SIDE — there is no column, so every
// listing reports false. Taking the server's answer would silently turn a
// shared rule into one that looks exclusive, which is exactly the fact a
// practitioner needs before destroying it.
func TestAdoptedIsPreservedAcrossARefresh(t *testing.T) {
	c, _ := serve(t, func(w http.ResponseWriter, _ *http.Request) {
		// As Postgres answers: adopted absent, therefore false.
		_ = json.NewEncoder(w).Encode(apiAuthorizationListResponse{
			Items: []apiAuthorization{authzFixture(false)}})
	})
	ar := &authorizationResource{client: c}
	prior := authzModel()
	prior.ID = types.StringValue("a-1")
	prior.Adopted = types.BoolValue(true)
	prior.AdoptExisting = types.BoolValue(true)

	resp := resource.ReadResponse{State: stateOf(t, prior)}
	ar.Read(context.Background(), resource.ReadRequest{State: stateOf(t, prior)}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("read: %v", resp.Diagnostics.Errors())
	}
	var out AuthorizationModel
	resp.State.Get(context.Background(), &out)
	if !out.Adopted.ValueBool() {
		t.Fatal("a refresh turned a SHARED authorization into one that looks exclusive")
	}
}

// TestAuthorizationReadRemovesOneClosedElsewhere. Absent from the listing means
// the path was revoked outside Terraform.
func TestAuthorizationReadRemovesOneClosedElsewhere(t *testing.T) {
	c, _ := serve(t, func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(apiAuthorizationListResponse{Items: nil})
	})
	ar := &authorizationResource{client: c}
	m := authzModel()
	m.ID = types.StringValue("a-1")
	resp := resource.ReadResponse{State: stateOf(t, m)}
	ar.Read(context.Background(), resource.ReadRequest{State: stateOf(t, m)}, &resp)
	if resp.Diagnostics.HasError() || !resp.State.Raw.IsNull() {
		t.Fatal("an authorization absent from the listing must be removed from state")
	}
}

func TestAuthorizationUpdateIsRefused(t *testing.T) {
	var resp resource.UpdateResponse
	(&authorizationResource{}).Update(context.Background(), resource.UpdateRequest{}, &resp)
	if !resp.Diagnostics.HasError() {
		t.Fatal("Update must refuse: changing what a rule permits means revoking and re-opening")
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

	for _, bad := range []string{"", "agw-1", "agw-1/pool-1/b-1", "agw-1//b-1/a-1"} {
		if _, err := run(bad); !err {
			t.Errorf("import id %q was accepted; the format is %s", bad, "{gateway_id}/{pool_id}/{backend_id}/{authorization_id}")
		}
	}

	resp, err := run("agw-1/pool-1/b-1/a-1")
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
		if d := resp.State.GetAttribute(context.Background(), tfpath.Root("pool_id"), &got); d.HasError() {
			t.Fatalf("read pool_id: %v", d.Errors())
		}
		if got.ValueString() != "pool-1" {
			t.Errorf("pool_id = %q, want pool-1", got.ValueString())
		}
	}
	{
		var got types.String
		if d := resp.State.GetAttribute(context.Background(), tfpath.Root("backend_id"), &got); d.HasError() {
			t.Fatalf("read backend_id: %v", d.Errors())
		}
		if got.ValueString() != "b-1" {
			t.Errorf("backend_id = %q, want b-1", got.ValueString())
		}
	}
	{
		var got types.String
		if d := resp.State.GetAttribute(context.Background(), tfpath.Root("id"), &got); d.HasError() {
			t.Fatalf("read id: %v", d.Errors())
		}
		if got.ValueString() != "a-1" {
			t.Errorf("id = %q, want a-1", got.ValueString())
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
