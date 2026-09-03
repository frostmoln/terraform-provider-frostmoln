package appgw_backend

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

func planOf(t *testing.T, m BackendModel) tfsdk.Plan {
	t.Helper()
	p := schemaOf(t)
	if d := p.Set(context.Background(), &m); d.HasError() {
		t.Fatalf("plan: %v", d.Errors())
	}
	return p
}

func stateOf(t *testing.T, m BackendModel) tfsdk.State {
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

const bkBase = "/v1/tenants/t-1/application-gateways/agw-1/backend-pools/pool-1/backends"

func bkFixture() apiBackend {
	return apiBackend{
		ID: "b-1", PoolID: "pool-1", SourceKind: "address", Address: "10.0.1.10",
		Port: 8080, Weight: 1, Status: "healthy", Enabled: true, CreatedAt: "2026-08-01T00:00:00Z",
	}
}

func bkModel() BackendModel {
	return BackendModel{
		GatewayID: types.StringValue("agw-1"),
		PoolID:    types.StringValue("pool-1"),
		Address:   types.StringValue("10.0.1.10"),
		Port:      types.Int64Value(8080),
	}
}

func TestBackendCreateReadDelete(t *testing.T) {
	c, _ := serve(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == bkBase:
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(bkFixture())
		case r.Method == http.MethodGet && r.URL.Path == bkBase:
			// 🔴 THERE IS NO GET-BY-ID FOR A BACKEND. Read has to list the pool.
			_ = json.NewEncoder(w).Encode(apiBackendListResponse{Backends: []apiBackend{bkFixture()}})
		case r.Method == http.MethodDelete && r.URL.Path == bkBase+"/b-1":
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	})
	br := &backendResource{client: c}

	createResp := resource.CreateResponse{State: emptyState(t)}
	br.Create(context.Background(), resource.CreateRequest{Plan: planOf(t, bkModel())}, &createResp)
	if createResp.Diagnostics.HasError() {
		t.Fatalf("create: %v", createResp.Diagnostics.Errors())
	}
	var created BackendModel
	createResp.State.Get(context.Background(), &created)
	if created.ID.ValueString() != "b-1" || created.GatewayID.ValueString() != "agw-1" {
		t.Fatalf("create result wrong: %+v", created)
	}

	readResp := resource.ReadResponse{State: stateOf(t, created)}
	br.Read(context.Background(), resource.ReadRequest{State: stateOf(t, created)}, &readResp)
	if readResp.Diagnostics.HasError() {
		t.Fatalf("read: %v", readResp.Diagnostics.Errors())
	}
	if readResp.State.Raw.IsNull() {
		t.Fatal("read removed a backend that IS in the pool listing")
	}

	delResp := resource.DeleteResponse{State: stateOf(t, created)}
	br.Delete(context.Background(), resource.DeleteRequest{State: stateOf(t, created)}, &delResp)
	if delResp.Diagnostics.HasError() {
		t.Fatalf("delete: %v", delResp.Diagnostics.Errors())
	}
	// Destroying a backend closes no ingress rule, and the practitioner has to
	// be told: the authorization is per (group, protocol, port) and may be
	// shared with backends that are staying.
	if delResp.Diagnostics.WarningsCount() == 0 {
		t.Error("delete must warn that the ingress rule is still open")
	}
}

// TestBackendReadRemovesOneMissingFromTheListing. Absent from the pool listing
// is the only signal available that it was deleted outside Terraform.
func TestBackendReadRemovesOneMissingFromTheListing(t *testing.T) {
	c, _ := serve(t, func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(apiBackendListResponse{Backends: []apiBackend{
			{ID: "someone-else", PoolID: "pool-1", Port: 80},
		}})
	})
	br := &backendResource{client: c}
	m := bkModel()
	m.ID = types.StringValue("b-1")
	resp := resource.ReadResponse{State: stateOf(t, m)}
	br.Read(context.Background(), resource.ReadRequest{State: stateOf(t, m)}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("read: %v", resp.Diagnostics.Errors())
	}
	if !resp.State.Raw.IsNull() {
		t.Error("a backend absent from the pool listing must be removed from state")
	}
}

func TestBackendUpdateIsRefused(t *testing.T) {
	var resp resource.UpdateResponse
	(&backendResource{}).Update(context.Background(), resource.UpdateRequest{}, &resp)
	if !resp.Diagnostics.HasError() {
		t.Fatal("Update must refuse: the API has no backend update")
	}
}

// TestBackendValidateConfig pins the discriminated union AND the address rules
// the server enforces, so an obvious mistake is caught before the gateway and
// the pool have been built.
func TestBackendValidateConfig(t *testing.T) {
	r := NewResource().(resource.ResourceWithValidateConfig)
	check := func(m BackendModel) bool {
		p := planOf(t, m)
		var resp resource.ValidateConfigResponse
		r.ValidateConfig(context.Background(), resource.ValidateConfigRequest{
			Config: tfsdk.Config(p),
		}, &resp)
		return resp.Diagnostics.HasError()
	}

	if check(bkModel()) {
		t.Error("a plain address backend was refused")
	}

	loopback := bkModel()
	loopback.Address = types.StringValue("127.0.0.1")
	if !check(loopback) {
		t.Error("a loopback address must be refused: the gateway forwards to it from its appliance")
	}

	notAnIP := bkModel()
	notAnIP.Address = types.StringValue("backend.internal")
	if !check(notAnIP) {
		t.Error("a hostname must be refused; the field is an IP address")
	}

	// 🔴 THE SERVER SUPPORTS ONLY sourceKind "address" TODAY
	// (impl/backend.go: `only sourceKind "address" is available yet`), so the
	// provider must not offer the other two. Advertising them made a clean plan
	// 400 at apply time, after the gateway, pool and listener were built.
	//
	// ValidateConfig's default arm is the defence if the enum ever gains a
	// value without the platform gaining the resolution to go with it.
	instanceKind := bkModel()
	instanceKind.SourceKind = types.StringValue("instance")
	instanceKind.SourceID = types.StringValue("i-1")
	instanceKind.Address = types.StringNull()
	if !check(instanceKind) {
		t.Error("source_kind = instance must be refused: the platform does not resolve it yet")
	}

	sourceIDOnAddress := bkModel()
	sourceIDOnAddress.SourceID = types.StringValue("i-1")
	if !check(sourceIDOnAddress) {
		t.Error("source_id on an address backend must be refused")
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

	for _, bad := range []string{"", "agw-1", "agw-1/pool-1", "agw-1//b-1"} {
		if _, err := run(bad); !err {
			t.Errorf("import id %q was accepted; the format is %s", bad, "{gateway_id}/{pool_id}/{backend_id}")
		}
	}

	resp, err := run("agw-1/pool-1/b-1")
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
		if d := resp.State.GetAttribute(context.Background(), tfpath.Root("id"), &got); d.HasError() {
			t.Fatalf("read id: %v", d.Errors())
		}
		if got.ValueString() != "b-1" {
			t.Errorf("id = %q, want b-1", got.ValueString())
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
