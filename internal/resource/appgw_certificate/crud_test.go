package appgw_certificate

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

func planOf(t *testing.T, m CertificateModel) tfsdk.Plan {
	t.Helper()
	p := schemaOf(t)
	if d := p.Set(context.Background(), &m); d.HasError() {
		t.Fatalf("plan: %v", d.Errors())
	}
	return p
}

func stateOf(t *testing.T, m CertificateModel) tfsdk.State {
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

const certBase = "/v1/tenants/t-1/application-gateways/agw-1/certificates"

func certFixture() apiCertificate {
	return apiCertificate{
		ID: "c-1", GatewayID: "agw-1", Name: "www", Source: "uploaded", Status: "active",
		CommonName: "www.example.com", SANs: []string{"www.example.com"},
		FingerprintSHA256: "aa:bb", NotBefore: "2026-01-01T00:00:00Z", NotAfter: "2027-01-01T00:00:00Z",
		CreatedAt: "2026-08-01T00:00:00Z",
	}
}

func certModel() CertificateModel {
	return CertificateModel{
		GatewayID:     types.StringValue("agw-1"),
		Name:          types.StringValue("www"),
		ChainPEM:      types.StringValue("-----BEGIN CERTIFICATE-----\nzz\n-----END CERTIFICATE-----"),
		PrivateKeyPEM: types.StringValue("-----BEGIN PRIVATE KEY-----\nyy\n-----END PRIVATE KEY-----"), // pragma: allowlist secret
		SANs:          types.ListNull(types.StringType),
	}
}

// TestCertificateCreateSendsPrivateKeyPem.
//
// 🔴 privateKeyPem, NOT privateKey. The server binds it as required, so the
// wrong name is not a dropped field — it is a 400 on every upload. The
// assertion is written against the SERVER's tag rather than a literal this
// resource also chose, because asserting the latter validates the client
// against itself.
func TestCertificateCreateSendsPrivateKeyPem(t *testing.T) {
	var body map[string]any
	c, _ := serve(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(certFixture())
	})
	cr := &certificateResource{client: c}

	resp := resource.CreateResponse{State: emptyState(t)}
	cr.Create(context.Background(), resource.CreateRequest{Plan: planOf(t, certModel())}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("create: %v", resp.Diagnostics.Errors())
	}
	if _, ok := body["privateKeyPem"]; !ok {
		t.Fatalf("the key was not sent as privateKeyPem; body carried %v", keys(body))
	}
	if _, ok := body["privateKey"]; ok {
		t.Fatal("privateKey is not a field the server binds: sending it drops the key and the " +
			"request is refused as missing privateKeyPem")
	}
}

func keys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// TestCertificateReadPreservesTheKeyAndChain.
//
// 🔴 THE SERVER NEVER RETURNS THE PRIVATE KEY, and returns the chain only
// sometimes. Overwriting either from a refresh would blank a Required attribute
// and produce a permanent diff — or, for the key, destroy the only copy that
// exists outside the platform.
func TestCertificateReadPreservesTheKeyAndChain(t *testing.T) {
	c, _ := serve(t, func(w http.ResponseWriter, _ *http.Request) {
		// Deliberately no chainPem and no key, which is what a real read gives.
		f := certFixture()
		f.ChainPEM = ""
		_ = json.NewEncoder(w).Encode(f)
	})
	cr := &certificateResource{client: c}

	prior := certModel()
	prior.ID = types.StringValue("c-1")
	resp := resource.ReadResponse{State: stateOf(t, prior)}
	cr.Read(context.Background(), resource.ReadRequest{State: stateOf(t, prior)}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("read: %v", resp.Diagnostics.Errors())
	}
	var after CertificateModel
	resp.State.Get(context.Background(), &after)
	if after.PrivateKeyPEM.ValueString() != prior.PrivateKeyPEM.ValueString() {
		t.Fatal("the private key was lost on refresh; the platform never returns it, so state is " +
			"the only copy")
	}
	if after.ChainPEM.ValueString() != prior.ChainPEM.ValueString() {
		t.Fatal("the chain was blanked on refresh, which would give a permanent diff")
	}
	// Server-owned fields DO refresh.
	if after.CommonName.ValueString() != "www.example.com" {
		t.Errorf("common_name = %v, want the server's value", after.CommonName)
	}
}

func TestCertificateReadRemovesAVanishedResource(t *testing.T) {
	c, _ := serve(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]string{"code": "NOT_FOUND", "message": "gone"})
	})
	cr := &certificateResource{client: c}
	m := certModel()
	m.ID = types.StringValue("c-1")
	resp := resource.ReadResponse{State: stateOf(t, m)}
	cr.Read(context.Background(), resource.ReadRequest{State: stateOf(t, m)}, &resp)
	if resp.Diagnostics.HasError() || !resp.State.Raw.IsNull() {
		t.Fatal("a 404 must remove the certificate from state without erroring")
	}
}

func TestCertificateDeleteAndUpdate(t *testing.T) {
	c, _ := serve(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete && r.URL.Path == certBase+"/c-1" {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	})
	cr := &certificateResource{client: c}
	m := certModel()
	m.ID = types.StringValue("c-1")

	del := resource.DeleteResponse{State: stateOf(t, m)}
	cr.Delete(context.Background(), resource.DeleteRequest{State: stateOf(t, m)}, &del)
	if del.Diagnostics.HasError() {
		t.Fatalf("delete: %v", del.Diagnostics.Errors())
	}

	var upd resource.UpdateResponse
	cr.Update(context.Background(), resource.UpdateRequest{}, &upd)
	if !upd.Diagnostics.HasError() {
		t.Fatal("Update must refuse: the API has no certificate update")
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

	for _, bad := range []string{"", "agw-1", "agw-1/", "/c-1"} {
		if _, err := run(bad); !err {
			t.Errorf("import id %q was accepted; the format is %s", bad, "{gateway_id}/{certificate_id}")
		}
	}

	resp, err := run("agw-1/c-1")
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
		if got.ValueString() != "c-1" {
			t.Errorf("id = %q, want c-1", got.ValueString())
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
