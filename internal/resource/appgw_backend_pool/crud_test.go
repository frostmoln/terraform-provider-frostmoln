package appgw_backend_pool

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

func planOf(t *testing.T, m PoolModel) tfsdk.Plan {
	t.Helper()
	p := schemaOf(t)
	if d := p.Set(context.Background(), &m); d.HasError() {
		t.Fatalf("plan: %v", d.Errors())
	}
	return p
}

func stateOf(t *testing.T, m PoolModel) tfsdk.State {
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

const poolBase = "/v1/tenants/t-1/application-gateways/agw-1/backend-pools"

func poolFixture() apiPool {
	return apiPool{
		ID: "pool-1", GatewayID: "agw-1", Name: "web", Protocol: "http",
		Algorithm: "round_robin", SessionAffinity: "none",
		TimeoutConnectMS: 2000, TimeoutResponseMS: 30000, CreatedAt: "2026-08-01T00:00:00Z",
	}
}

func poolModel() PoolModel {
	return PoolModel{
		GatewayID: types.StringValue("agw-1"),
		Name:      types.StringValue("web"),
	}
}

func TestPoolCreateReadDelete(t *testing.T) {
	c, _ := serve(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == poolBase:
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(poolFixture())
		case r.Method == http.MethodGet && r.URL.Path == poolBase+"/pool-1":
			_ = json.NewEncoder(w).Encode(poolFixture())
		case r.Method == http.MethodDelete && r.URL.Path == poolBase+"/pool-1":
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	})
	pr := &poolResource{client: c}

	createResp := resource.CreateResponse{State: emptyState(t)}
	pr.Create(context.Background(), resource.CreateRequest{Plan: planOf(t, poolModel())}, &createResp)
	if createResp.Diagnostics.HasError() {
		t.Fatalf("create: %v", createResp.Diagnostics.Errors())
	}
	var created PoolModel
	createResp.State.Get(context.Background(), &created)
	if created.ID.ValueString() != "pool-1" {
		t.Fatalf("id = %q", created.ID.ValueString())
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

// TestPoolCreateOmitsTLSVerifyWhenUnset is the security-relevant one.
//
// 🔴 A PLAIN BOOL WOULD SEND false FOR ANYONE WHO NEVER MENTIONED IT, which on
// an https pool silently turns OFF backend certificate verification. The
// pointer, and this test, are what keep the platform default winning.
func TestPoolCreateOmitsTLSVerifyWhenUnset(t *testing.T) {
	var body map[string]any
	c, _ := serve(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(poolFixture())
	})
	pr := &poolResource{client: c}

	m := poolModel()
	m.Protocol = types.StringValue("https")
	m.TLSVerifyBackend = types.BoolNull()
	resp := resource.CreateResponse{State: emptyState(t)}
	pr.Create(context.Background(), resource.CreateRequest{Plan: planOf(t, m)}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("create: %v", resp.Diagnostics.Errors())
	}
	if v, present := body["tlsVerifyBackend"]; present {
		t.Fatalf("tlsVerifyBackend was sent as %v though the configuration never set it — "+
			"the platform default must win", v)
	}

	// An explicit false must still be sent: the practitioner asked for it.
	body = nil
	m.TLSVerifyBackend = types.BoolValue(false)
	resp = resource.CreateResponse{State: emptyState(t)}
	pr.Create(context.Background(), resource.CreateRequest{Plan: planOf(t, m)}, &resp)
	if v, present := body["tlsVerifyBackend"]; !present || v != false {
		t.Fatalf("an explicit tls_verify_backend = false must be sent, got %v (present=%v)", v, present)
	}
}

func TestPoolUpdateIsRefused(t *testing.T) {
	var resp resource.UpdateResponse
	(&poolResource{}).Update(context.Background(), resource.UpdateRequest{}, &resp)
	if !resp.Diagnostics.HasError() {
		t.Fatal("Update must refuse: the API has no backend pool update")
	}
}

func TestPoolValidateConfig(t *testing.T) {
	r := NewResource().(resource.ResourceWithValidateConfig)
	check := func(m PoolModel) (errs bool, warns bool) {
		p := planOf(t, m)
		var resp resource.ValidateConfigResponse
		r.ValidateConfig(context.Background(), resource.ValidateConfigRequest{
			Config: tfsdk.Config(p),
		}, &resp)
		return resp.Diagnostics.HasError(), resp.Diagnostics.WarningsCount() > 0
	}

	cookieNoName := poolModel()
	cookieNoName.SessionAffinity = types.StringValue("cookie")
	if errs, _ := check(cookieNoName); !errs {
		t.Error("cookie affinity with no cookie name must be refused")
	}

	// Backend TLS settings on a plaintext pool are silently ineffective, which
	// is worth a warning: a practitioner may believe the hop is verified.
	tlsOnHTTP := poolModel()
	tlsOnHTTP.Protocol = types.StringValue("http")
	tlsOnHTTP.TLSServerName = types.StringValue("api.internal")
	if _, warns := check(tlsOnHTTP); !warns {
		t.Error("backend TLS settings on an http pool must warn")
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

	for _, bad := range []string{"", "agw-1", "agw-1/", "/pool-1"} {
		if _, err := run(bad); !err {
			t.Errorf("import id %q was accepted; the format is %s", bad, "{gateway_id}/{pool_id}")
		}
	}

	resp, err := run("agw-1/pool-1")
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
		if got.ValueString() != "pool-1" {
			t.Errorf("id = %q, want pool-1", got.ValueString())
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

// 🔴 THE COOKIE NAME GOES THROUGH THE SAME RULES AS A HEADER NAME, AND FOR A
// SHARPER REASON.
//
// The server shares its RFC token regexp AND its `#`/apostrophe refusal between
// header names and cookie names, because the cookie name is rendered as a BARE,
// whitespace-separated argument: `cookie <name> insert indirect nocache
// httponly`. So a `#` truncates the line, leaving a cookie directive with no
// mode, and an apostrophe opens strong quoting mid-word. Either way the proxy
// refuses its WHOLE configuration and the appliance keeps serving the previous
// revision — while the API reports the new one.
//
// ValidateConfig said it "mirrors the server's cookie rule" and checked only
// that a name was present.
func TestPoolValidateConfigChecksTheCookieName(t *testing.T) {
	r := NewResource().(resource.ResourceWithValidateConfig)
	check := func(name string) bool {
		m := poolModel()
		m.SessionAffinity = types.StringValue("cookie")
		m.SessionCookieName = types.StringValue(name)
		p := planOf(t, m)
		var resp resource.ValidateConfigResponse
		r.ValidateConfig(context.Background(), resource.ValidateConfigRequest{
			Config: tfsdk.Config(p),
		}, &resp)
		return resp.Diagnostics.HasError()
	}

	for _, tc := range []struct{ what, name string }{
		{"a space", "session id"},
		{"an equals sign", "session=id"},
		{"a semicolon", "session;id"},
		// Legal in a cookie name, fatal to the rendered configuration.
		{"a '#'", "session#id"},
		{"an apostrophe", "session'id"},
		{"over 64 bytes", strings.Repeat("a", 65)},
	} {
		if !check(tc.name) {
			t.Errorf("%s: accepted at plan time; the server refuses it, so the practitioner "+
				"meets it at apply with the pool already created", tc.what)
		}
	}

	// The converse, so the test cannot pass by refusing everything — and the
	// boundary, which must match the server's `>` exactly.
	for _, ok := range []string{"session-id", "SESSIONID", "sess_id.v2", strings.Repeat("a", 64)} {
		if check(ok) {
			t.Errorf("%q was refused; the server accepts it, so this provider is stricter "+
				"than the platform", ok)
		}
	}
}
