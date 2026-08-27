package appgw_route

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

func planOf(t *testing.T, m RouteModel) tfsdk.Plan {
	t.Helper()
	p := schemaOf(t)
	if d := p.Set(context.Background(), &m); d.HasError() {
		t.Fatalf("plan: %v", d.Errors())
	}
	return p
}

func stateOf(t *testing.T, m RouteModel) tfsdk.State {
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

const rtBase = "/v1/tenants/t-1/application-gateways/agw-1/listeners/lsn-1/routes"

func rtFixture() apiRoute {
	return apiRoute{
		ID: "rt-1", ListenerID: "lsn-1", Name: "api", Priority: 110,
		PathMatchType: "prefix", Path: "/v1", Action: "forward", BackendPoolID: "pool-1",
		Enabled: true, CreatedAt: "2026-08-01T00:00:00Z",
	}
}

func rtModel() RouteModel {
	return RouteModel{
		GatewayID:            types.StringValue("agw-1"),
		ListenerID:           types.StringValue("lsn-1"),
		Name:                 types.StringValue("api"),
		BackendPoolID:        types.StringValue("pool-1"),
		Priority:             types.Int64Null(),
		RequestHeadersSet:    types.MapNull(types.StringType),
		ResponseHeadersSet:   types.MapNull(types.StringType),
		RequestHeadersRemove: types.ListNull(types.StringType),
	}
}

func TestRouteCreateReadDelete(t *testing.T) {
	c, _ := serve(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == rtBase:
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(rtFixture())
		case r.Method == http.MethodGet && r.URL.Path == rtBase+"/rt-1":
			_ = json.NewEncoder(w).Encode(rtFixture())
		case r.Method == http.MethodDelete && r.URL.Path == rtBase+"/rt-1":
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	})
	rr := &routeResource{client: c}

	createResp := resource.CreateResponse{State: emptyState(t)}
	rr.Create(context.Background(), resource.CreateRequest{Plan: planOf(t, rtModel())}, &createResp)
	if createResp.Diagnostics.HasError() {
		t.Fatalf("create: %v", createResp.Diagnostics.Errors())
	}
	var created RouteModel
	createResp.State.Get(context.Background(), &created)
	if created.ID.ValueString() != "rt-1" {
		t.Fatalf("id = %q", created.ID.ValueString())
	}
	// gateway_id is not in the route payload (a route belongs to a listener),
	// so it must be carried over from the plan rather than blanked.
	if created.GatewayID.ValueString() != "agw-1" {
		t.Fatalf("gateway_id = %q; the route payload does not carry it, so it must survive from "+
			"the plan", created.GatewayID.ValueString())
	}
	if created.Priority.ValueInt64() != 110 {
		t.Errorf("priority = %d, want the server-assigned 110", created.Priority.ValueInt64())
	}

	readResp := resource.ReadResponse{State: stateOf(t, created)}
	rr.Read(context.Background(), resource.ReadRequest{State: stateOf(t, created)}, &readResp)
	if readResp.Diagnostics.HasError() {
		t.Fatalf("read: %v", readResp.Diagnostics.Errors())
	}
	var refreshed RouteModel
	readResp.State.Get(context.Background(), &refreshed)
	if refreshed.GatewayID.ValueString() != "agw-1" {
		t.Error("read blanked gateway_id")
	}

	delResp := resource.DeleteResponse{State: stateOf(t, created)}
	rr.Delete(context.Background(), resource.DeleteRequest{State: stateOf(t, created)}, &delResp)
	if delResp.Diagnostics.HasError() {
		t.Fatalf("delete: %v", delResp.Diagnostics.Errors())
	}
}

func TestRouteReadRemovesAVanishedResource(t *testing.T) {
	c, _ := serve(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]string{"code": "NOT_FOUND", "message": "gone"})
	})
	rr := &routeResource{client: c}
	m := rtModel()
	m.ID = types.StringValue("rt-1")
	resp := resource.ReadResponse{State: stateOf(t, m)}
	rr.Read(context.Background(), resource.ReadRequest{State: stateOf(t, m)}, &resp)
	if resp.Diagnostics.HasError() || !resp.State.Raw.IsNull() {
		t.Fatal("a 404 must remove the route from state without erroring")
	}
}

func TestRouteUpdateIsRefused(t *testing.T) {
	var resp resource.UpdateResponse
	(&routeResource{}).Update(context.Background(), resource.UpdateRequest{}, &resp)
	if !resp.Diagnostics.HasError() {
		t.Fatal("Update must refuse: the API has no route update")
	}
}

// TestRouteValidateConfigCompilesTheRegexLocally. A bad pattern found at apply
// time has usually already created the gateway and the listener, leaving a
// half-built stack to unpick over a typo.
func TestRouteValidateConfigCompilesTheRegexLocally(t *testing.T) {
	r := NewResource().(resource.ResourceWithValidateConfig)
	check := func(m RouteModel) bool {
		p := planOf(t, m)
		var resp resource.ValidateConfigResponse
		r.ValidateConfig(context.Background(), resource.ValidateConfigRequest{
			Config: tfsdk.Config(p)}, &resp)
		return resp.Diagnostics.HasError()
	}

	bad := rtModel()
	bad.PathMatchType = types.StringValue("regex")
	bad.Path = types.StringValue("([unclosed")
	if !check(bad) {
		t.Error("an uncompilable regex must be refused at plan time")
	}

	missing := rtModel()
	missing.PathMatchType = types.StringValue("regex")
	if !check(missing) {
		t.Error("regex matching with no path must be refused")
	}

	good := rtModel()
	good.PathMatchType = types.StringValue("regex")
	good.Path = types.StringValue("^/v[0-9]+/")
	if check(good) {
		t.Error("a valid RE2 pattern was refused")
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

	for _, bad := range []string{"", "agw-1", "agw-1/lsn-1", "agw-1//rt-1", "a/b/c/d"} {
		if _, err := run(bad); !err {
			t.Errorf("import id %q was accepted; the format is %s", bad, "{gateway_id}/{listener_id}/{route_id}")
		}
	}

	resp, err := run("agw-1/lsn-1/rt-1")
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
		if d := resp.State.GetAttribute(context.Background(), tfpath.Root("listener_id"), &got); d.HasError() {
			t.Fatalf("read listener_id: %v", d.Errors())
		}
		if got.ValueString() != "lsn-1" {
			t.Errorf("listener_id = %q, want lsn-1", got.ValueString())
		}
	}
	{
		var got types.String
		if d := resp.State.GetAttribute(context.Background(), tfpath.Root("id"), &got); d.HasError() {
			t.Fatalf("read id: %v", d.Errors())
		}
		if got.ValueString() != "rt-1" {
			t.Errorf("id = %q, want rt-1", got.ValueString())
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
