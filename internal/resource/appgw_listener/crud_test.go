package appgw_listener

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

func planOf(t *testing.T, m ListenerModel) tfsdk.Plan {
	t.Helper()
	p := schemaOf(t)
	if d := p.Set(context.Background(), &m); d.HasError() {
		t.Fatalf("plan: %v", d.Errors())
	}
	return p
}

func stateOf(t *testing.T, m ListenerModel) tfsdk.State {
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

const lsnBase = "/v1/tenants/t-1/application-gateways/agw-1/listeners"

func lsnFixture() apiListener {
	return apiListener{
		ID: "lsn-1", GatewayID: "agw-1", Name: "https", Protocol: "https", Port: 443,
		TLSMinVersion: "1.2", TLSCipherProfile: "modern", GeoBlockMode: "off",
		Enabled: true, CreatedAt: "2026-08-01T00:00:00Z",
	}
}

func lsnModel() ListenerModel {
	// The list fields need an explicit null-with-element-type: a zero
	// types.List carries no element type and cannot be written into a schema
	// that declares one.
	nullList := types.ListNull(types.StringType)
	return ListenerModel{
		GatewayID:         types.StringValue("agw-1"),
		Name:              types.StringValue("https"),
		Protocol:          types.StringValue("https"),
		Port:              types.Int64Value(443),
		SNICertificateIDs: nullList,
		AllowedCIDRs:      nullList,
		DeniedCIDRs:       nullList,
		GeoCountries:      nullList,
	}
}

func TestListenerCreateReadDelete(t *testing.T) {
	var seen []string
	c, _ := serve(t, func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Method+" "+r.URL.Path)
		switch {
		case r.Method == http.MethodPost && r.URL.Path == lsnBase:
			// The server answers 202 with the LISTENER, not an operation
			// envelope: the row is written synchronously and 202 says only that
			// it is not yet serving.
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(lsnFixture())
		case r.Method == http.MethodGet && r.URL.Path == lsnBase+"/lsn-1":
			_ = json.NewEncoder(w).Encode(lsnFixture())
		case r.Method == http.MethodDelete && r.URL.Path == lsnBase+"/lsn-1":
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	})
	r := &listenerResource{client: c}

	createResp := resource.CreateResponse{State: emptyState(t)}
	r.Create(context.Background(), resource.CreateRequest{Plan: planOf(t, lsnModel())}, &createResp)
	if createResp.Diagnostics.HasError() {
		t.Fatalf("create: %v", createResp.Diagnostics.Errors())
	}
	var created ListenerModel
	createResp.State.Get(context.Background(), &created)
	if created.ID.ValueString() != "lsn-1" {
		t.Fatalf("id = %q, want lsn-1", created.ID.ValueString())
	}

	readResp := resource.ReadResponse{State: stateOf(t, created)}
	r.Read(context.Background(), resource.ReadRequest{State: stateOf(t, created)}, &readResp)
	if readResp.Diagnostics.HasError() {
		t.Fatalf("read: %v", readResp.Diagnostics.Errors())
	}

	delResp := resource.DeleteResponse{State: stateOf(t, created)}
	r.Delete(context.Background(), resource.DeleteRequest{State: stateOf(t, created)}, &delResp)
	if delResp.Diagnostics.HasError() {
		t.Fatalf("delete: %v", delResp.Diagnostics.Errors())
	}
}

// TestListenerReadRemovesAVanishedResource. A 404 on refresh means it was
// deleted outside Terraform; erroring instead would wedge every subsequent plan.
func TestListenerReadRemovesAVanishedResource(t *testing.T) {
	c, _ := serve(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]string{"code": "NOT_FOUND", "message": "gone"})
	})
	r := &listenerResource{client: c}
	m := lsnModel()
	m.ID = types.StringValue("lsn-1")

	resp := resource.ReadResponse{State: stateOf(t, m)}
	r.Read(context.Background(), resource.ReadRequest{State: stateOf(t, m)}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("a 404 must not be an error: %v", resp.Diagnostics.Errors())
	}
	if !resp.State.Raw.IsNull() {
		t.Error("a vanished listener must be removed from state")
	}
}

// TestListenerUpdateIsRefused. Every attribute carries RequiresReplace because
// the API has no update, so Update is unreachable — and must SAY so rather than
// silently succeed, which is how a provider reports a change that never
// happened.
func TestListenerUpdateIsRefused(t *testing.T) {
	r := &listenerResource{}
	var resp resource.UpdateResponse
	r.Update(context.Background(), resource.UpdateRequest{}, &resp)
	if !resp.Diagnostics.HasError() {
		t.Fatal("Update must refuse: a silent no-op would report a change that did not happen")
	}
}

func TestListenerCreateSurfacesAnAPIError(t *testing.T) {
	c, _ := serve(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"code": "INVALID_REQUEST", "message": "no"})
	})
	r := &listenerResource{client: c}
	resp := resource.CreateResponse{State: emptyState(t)}
	r.Create(context.Background(), resource.CreateRequest{Plan: planOf(t, lsnModel())}, &resp)
	if !resp.Diagnostics.HasError() {
		t.Fatal("a 400 must surface as an error")
	}
}

// TestListenerValidateConfig pins the two cross-field rules the server enforces,
// caught here so a mistake is a plan-time error rather than a 400 partway
// through an apply that has already built the gateway.
func TestListenerValidateConfig(t *testing.T) {
	r := NewResource().(resource.ResourceWithValidateConfig)
	check := func(m ListenerModel) []string {
		// tfsdk.Config has no Set; build the raw value through a Plan (which
		// does) and carry it across with the same schema.
		p := planOf(t, m)
		cfg := tfsdk.Config(p)
		var resp resource.ValidateConfigResponse
		r.ValidateConfig(context.Background(), resource.ValidateConfigRequest{Config: cfg}, &resp)
		var out []string
		for _, e := range resp.Diagnostics.Errors() {
			out = append(out, e.Summary())
		}
		return out
	}

	ok := lsnModel()
	if errs := check(ok); len(errs) != 0 {
		t.Fatalf("a valid https listener was refused: %v", errs)
	}

	// A certificate on an http listener would never be served.
	httpWithCert := lsnModel()
	httpWithCert.Protocol = types.StringValue("http")
	httpWithCert.Port = types.Int64Value(80)
	httpWithCert.DefaultCertificateID = types.StringValue("cert-1")
	if errs := check(httpWithCert); len(errs) == 0 {
		t.Error("a certificate on an http listener must be refused")
	}

	// Geo filtering with no countries would apply to everything or nothing.
	geoNoCountries := lsnModel()
	geoNoCountries.GeoBlockMode = types.StringValue("deny")
	if errs := check(geoNoCountries); len(errs) == 0 {
		t.Error("geo_block_mode = deny with no countries must be refused")
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

	for _, bad := range []string{"", "agw-1", "agw-1/", "/lsn-1", "a/b/c"} {
		if _, err := run(bad); !err {
			t.Errorf("import id %q was accepted; the format is %s", bad, "{gateway_id}/{listener_id}")
		}
	}

	resp, err := run("agw-1/lsn-1")
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
		if got.ValueString() != "lsn-1" {
			t.Errorf("id = %q, want lsn-1", got.ValueString())
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
