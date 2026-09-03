package appgw_certificate

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	tfpath "github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
)

// resourceSchema returns this resource's schema as declared, so a test can read
// an attribute's flags, validators and plan modifiers.
func resourceSchema(t *testing.T) schema.Schema {
	t.Helper()
	r := NewResource()
	var sr resource.SchemaResponse
	r.Schema(context.Background(), resource.SchemaRequest{}, &sr)
	if sr.Diagnostics.HasError() {
		t.Fatalf("schema: %v", sr.Diagnostics.Errors())
	}
	return sr.Schema
}

// schemaOf returns this resource's schema, which every plan and state in these
// tests is built against.
func schemaOf(t *testing.T) tfsdk.Plan {
	t.Helper()
	return tfsdk.Plan{Schema: resourceSchema(t)}
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

// configOf is the CONFIG Terraform hands an apply. Create reads
// private_key_pem_wo from here and nowhere else, and a zero tfsdk.Config panics
// on the first read — so every Create in these tests supplies one.
func configOf(t *testing.T, m CertificateModel) tfsdk.Config {
	t.Helper()
	st := stateOf(t, m)
	return tfsdk.Config(st)
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
	cr.Create(context.Background(), resource.CreateRequest{
		Plan: planOf(t, certModel()), Config: configOf(t, certModel()),
	}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("create: %v", resp.Diagnostics.Errors())
	}
	// The VALUE, not just the key. `privateKeyPem` has no omitempty, so a
	// mutation that overwrites it with the (null) write-only value still leaves
	// the field present — carrying "", which the server refuses as missing.
	if got, _ := body["privateKeyPem"].(string); got != certModel().PrivateKeyPEM.ValueString() {
		t.Fatalf("the configured key was not sent as privateKeyPem (got %q); body carried %v",
			got, keys(body))
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
		t.Fatal("the private key was lost on refresh; the provider never adopts one from a " +
			"response, so prior state is the only copy it has")
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

// TestCertificateSchemaWriteOnlyAttributes pins the shape the provider-level
// contract deliberately leaves per-resource: the legacy attribute's relaxation
// from Required, and the version companion's plan modifiers.
func TestCertificateSchemaWriteOnlyAttributes(t *testing.T) {
	attrs := resourceSchema(t).Attributes

	legacy, ok := attrs["private_key_pem"].(schema.StringAttribute)
	if !ok {
		t.Fatal("private_key_pem is not a StringAttribute")
	}
	if legacy.Required {
		t.Error("private_key_pem must be relaxed to Optional, or private_key_pem_wo is unreachable " +
			"and every existing configuration is forced onto Terraform 1.11")
	}
	if !legacy.Optional || legacy.Computed {
		t.Error("private_key_pem must be Optional-only")
	}

	wo, ok := attrs["private_key_pem_wo"].(schema.StringAttribute)
	if !ok {
		t.Fatal("private_key_pem_wo missing from schema")
	}
	if !wo.WriteOnly || !wo.Sensitive || wo.Computed {
		t.Error("private_key_pem_wo must be WriteOnly and Sensitive and not Computed")
	}

	ver, ok := attrs["private_key_pem_wo_version"].(schema.StringAttribute)
	if !ok {
		t.Fatal("private_key_pem_wo_version missing from schema")
	}
	if ver.WriteOnly || ver.Sensitive {
		t.Error("private_key_pem_wo_version must be stored in state and readable: it is the only " +
			"change signal, and the argument against deriving it from the key depends on it " +
			"printing in plan output")
	}
}

// TestPrivateKeyPEMWOVersionForcesReplacement is the assertion this resource
// cannot inherit from anywhere.
//
// 🔴 Update() on this resource is a hard error — the certificate API has no
// update operation. Without RequiresReplace on the version companion, a version
// bump is the only diff in the plan, Terraform plans an IN-PLACE update, and the
// apply fails: the rotation path the write-only pair exists to provide never
// works at all. The right modifier is a function of the resource's own update
// capability, which the schema does not express, so the provider-level contract
// asserts nothing about it — launch_template's companion must NOT replace.
func TestPrivateKeyPEMWOVersionForcesReplacement(t *testing.T) {
	ctx := context.Background()
	ver, ok := resourceSchema(t).Attributes["private_key_pem_wo_version"].(schema.StringAttribute)
	if !ok {
		t.Fatal("private_key_pem_wo_version is not a StringAttribute")
	}
	if len(ver.PlanModifiers) == 0 {
		t.Fatal("private_key_pem_wo_version has no plan modifiers, so a version bump plans an " +
			"in-place update that Update() refuses")
	}

	before, after := certModel(), certModel()
	before.PrivateKeyPEM, after.PrivateKeyPEM = types.StringNull(), types.StringNull()
	before.PrivateKeyPEMWOVer = types.StringValue("1")
	after.PrivateKeyPEMWOVer = types.StringValue("2")

	// Non-null State.Raw and Plan.Raw: RequiresReplace reads them to tell an
	// update apart from a create (null state) or a destroy (null plan).
	req := planmodifier.StringRequest{
		Path:        tfpath.Root("private_key_pem_wo_version"),
		StateValue:  types.StringValue("1"),
		PlanValue:   types.StringValue("2"),
		ConfigValue: types.StringValue("2"),
		State:       stateOf(t, before),
		Plan:        planOf(t, after),
	}
	var resp planmodifier.StringResponse
	for _, m := range ver.PlanModifiers {
		m.PlanModifyString(ctx, req, &resp)
	}
	if !resp.RequiresReplace {
		t.Error("a changed private_key_pem_wo_version must force the certificate to be replaced; " +
			"an in-place update is refused by Update(), so rotation would be dead on arrival")
	}
}

// TestCertificateCreateSendsTheWriteOnlyKey drives the write-only path end to
// end: the key reaches the platform under the server's own field name, and
// neither it nor the legacy attribute lands in state.
func TestCertificateCreateSendsTheWriteOnlyKey(t *testing.T) {
	const key = "-----BEGIN PRIVATE KEY-----\nwo\n-----END PRIVATE KEY-----" // pragma: allowlist secret

	var body map[string]any
	c, _ := serve(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(certFixture())
	})
	cr := &certificateResource{client: c}

	// The plan carries no key at all: a write-only attribute is null in the
	// plan by construction, and the legacy one is unset on this path.
	plan := certModel()
	plan.PrivateKeyPEM = types.StringNull()
	cfg := plan
	cfg.PrivateKeyPEMWO = types.StringValue(key)
	cfg.PrivateKeyPEMWOVer = types.StringValue("1")
	plan.PrivateKeyPEMWOVer = types.StringValue("1")

	resp := resource.CreateResponse{State: emptyState(t)}
	cr.Create(context.Background(), resource.CreateRequest{
		Plan: planOf(t, plan), Config: configOf(t, cfg),
	}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("create: %v", resp.Diagnostics.Errors())
	}

	// The literal wire key, not the struct field: a fixture that decodes into
	// the struct that encoded it is tag-symmetric, so a renamed json tag would
	// round-trip undetected while the key never reaches the platform.
	if got, _ := body["privateKeyPem"].(string); got != key {
		t.Fatalf("the write-only key was not sent as privateKeyPem; body carried %v", keys(body))
	}

	var after CertificateModel
	resp.State.Get(context.Background(), &after)
	if !after.PrivateKeyPEMWO.IsNull() {
		t.Error("private_key_pem_wo landed in state, which is the whole thing it exists not to do")
	}
	if !after.PrivateKeyPEM.IsNull() {
		t.Error("private_key_pem was populated on the write-only path, putting the key in state " +
			"by the back door")
	}
	if after.PrivateKeyPEMWOVer.ValueString() != "1" {
		t.Errorf("private_key_pem_wo_version = %v, want it preserved: it is the only change signal",
			after.PrivateKeyPEMWOVer)
	}
}

// TestCertificateReadDoesNotAdoptAKeyFromTheAPI feeds a refresh a response that
// DOES carry the key.
//
// 🔴 This is the leak stage 1 hit on frostmoln_secret. resource.ReadRequest
// carries no config, so a guard keyed on the config value cannot run here at
// all; the protection is that apiCertificate has no field to decode a key into,
// which makes adoption a compile error. Adding one to "round out" the type would
// defeat the write-only path on the first plan, silently.
func TestCertificateReadDoesNotAdoptAKeyFromTheAPI(t *testing.T) {
	// The structural half, asserted rather than assumed. A decode field is
	// INERT until someone assigns it, so it lands in review looking like
	// harmless completeness — and then adoption is one line away instead of a
	// compile error. Stage 3 found exactly this on launch_template: a userData
	// decode field nothing read, next to save/restore pairs that were no-ops.
	assertNoPrivateKeyField(t, reflect.TypeOf(apiCertificate{}))

	c, _ := serve(t, func(w http.ResponseWriter, _ *http.Request) {
		raw, _ := json.Marshal(certFixture())
		var m map[string]any
		_ = json.Unmarshal(raw, &m)
		m["privateKeyPem"] = "-----BEGIN PRIVATE KEY-----\nleaked\n-----END PRIVATE KEY-----" // pragma: allowlist secret
		_ = json.NewEncoder(w).Encode(m)
	})
	cr := &certificateResource{client: c}

	prior := certModel()
	prior.ID = types.StringValue("c-1")
	prior.PrivateKeyPEM = types.StringNull()
	prior.PrivateKeyPEMWOVer = types.StringValue("1")

	resp := resource.ReadResponse{State: stateOf(t, prior)}
	cr.Read(context.Background(), resource.ReadRequest{State: stateOf(t, prior)}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("read: %v", resp.Diagnostics.Errors())
	}

	var after CertificateModel
	resp.State.Get(context.Background(), &after)
	if !after.PrivateKeyPEM.IsNull() {
		t.Fatalf("refresh adopted a key the API returned into private_key_pem (%v); on the "+
			"write-only path that writes the key into state on the first plan", after.PrivateKeyPEM)
	}
	if !after.PrivateKeyPEMWO.IsNull() {
		t.Error("private_key_pem_wo must stay null in state")
	}
	if after.PrivateKeyPEMWOVer.ValueString() != "1" {
		t.Error("the version companion must be preserved across a refresh")
	}
}

// TestCertificateRequiresOneOfTheKeyForms covers the half of the mutual
// exclusion the provider-level contract does not: it tests both-set, never
// neither-set. private_key_pem was Required before private_key_pem_wo existed,
// so relaxing it without the ExactlyOneOf would newly admit a certificate with
// no key at all — accepted at plan time and refused by the server.
func TestCertificateRequiresOneOfTheKeyForms(t *testing.T) {
	ctx := context.Background()
	cv, ok := NewResource().(resource.ResourceWithConfigValidators)
	if !ok {
		t.Fatal("the resource declares no config validators, so nothing requires a key at all")
	}

	m := certModel()
	m.PrivateKeyPEM = types.StringNull()

	var diags diag.Diagnostics
	for _, v := range cv.ConfigValidators(ctx) {
		resp := &resource.ValidateConfigResponse{}
		v.ValidateResource(ctx, resource.ValidateConfigRequest{Config: configOf(t, m)}, resp)
		diags.Append(resp.Diagnostics...)
	}
	if !diags.HasError() {
		t.Error("a config with neither private_key_pem nor private_key_pem_wo must be refused")
	}
}

// assertNoPrivateKeyField refuses any field the API's private key could decode
// into.
//
// It matches the field NAME as well as the json tag, because an exported field
// with NO tag still decodes `privateKeyPem` — encoding/json falls back to a
// case-insensitive match on the field name — and `Tag.Get("json")` is empty for
// it. Tag-only matching would wave through the most likely spelling of the
// mistake: someone adding `PrivateKeyPEM string` while copying the model struct.
func assertNoPrivateKeyField(t *testing.T, typ reflect.Type) {
	t.Helper()
	for _, f := range reflect.VisibleFields(typ) {
		if !f.IsExported() {
			continue
		}
		hay := strings.ToLower(f.Name + " " + f.Tag.Get("json"))
		for _, needle := range []string{"privatekey", "secretkey", "keypem"} {
			if strings.Contains(hay, needle) {
				t.Fatalf("%s declares %s (json %q): the response type must have nowhere to decode "+
					"key material into, which is what makes adopting it a compile error rather "+
					"than a judgement call", typ.Name(), f.Name, f.Tag.Get("json"))
			}
		}
	}
}

// TestCertificateEveryConfigurableAttributeReplaces pins the invariant Update()
// depends on.
//
// 🔴 Update() is an unconditional error: "Reaching this code means an attribute
// was added to the schema without RequiresReplace." Nothing asserted that rule
// in general — only the one new version companion had its modifiers pinned — so
// the NEXT attribute added here would ship an apply-time hard error with no
// plan-time signal at all. A write-only attribute is exempt: it is null in
// prior state and in the plan, so it can never produce a diff to replace on.
func TestCertificateEveryConfigurableAttributeReplaces(t *testing.T) {
	ctx := context.Background()
	// Non-null State.Raw and Plan.Raw: RequiresReplace reads them to tell an
	// update apart from a create (null state) or a destroy (null plan).
	state, plan := stateOf(t, certModel()), planOf(t, certModel())

	for name, a := range resourceSchema(t).Attributes {
		// Optional+Computed is still CONFIGURABLE and can still produce a
		// diff, so it must not be skipped — only a pure Computed attribute
		// can. A write-only attribute is exempt for a real reason: it is null
		// in prior state AND in the plan, so no modifier can ever fire on it.
		if (a.IsComputed() && !a.IsOptional()) || a.IsWriteOnly() {
			continue
		}
		sa, ok := a.(schema.StringAttribute)
		if !ok {
			t.Errorf("%s: not a StringAttribute — extend this walk", name)
			continue
		}
		req := planmodifier.StringRequest{
			Path:        tfpath.Root(name),
			StateValue:  types.StringValue("before"),
			PlanValue:   types.StringValue("after"),
			ConfigValue: types.StringValue("after"),
			State:       state,
			Plan:        plan,
		}
		var resp planmodifier.StringResponse
		for _, m := range sa.PlanModifiers {
			m.PlanModifyString(ctx, req, &resp)
		}
		if !resp.RequiresReplace {
			t.Errorf("%s is configurable but a change to it does not force a replacement. The "+
				"certificate API has no update operation, so it would plan an in-place update that "+
				"Update() refuses — an apply-time hard error with nothing in the plan to warn "+
				"about it", name)
		}
	}
}
