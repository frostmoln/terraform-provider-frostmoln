package nginx_instance

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	resSchema "github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
)

// tls_enabled is read-only (Ambix 01a10bc7): no managed webserver serves TLS,
// and the API refuses tlsEnabled: true with tls_not_available. Pinned here:
// `true` is refused at plan time and again in Create/Update before any request,
// `false`/null pass, the field never reaches the wire, and a state holding a
// stale true (written by an older provider) refreshes to the backend's value.

// requireNoTLSEnabledOnWire fails when the serialized request carries tlsEnabled.
func requireNoTLSEnabledOnWire(t *testing.T, req any) {
	t.Helper()
	b, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "tlsEnabled") {
		t.Errorf("request carries tlsEnabled (read-only): %s", b)
	}
}

func TestSchemaTLSEnabledIsReadOnly(t *testing.T) {
	var schemaResp resource.SchemaResponse
	NewResource().Schema(context.Background(), resource.SchemaRequest{}, &schemaResp)
	attr, ok := schemaResp.Schema.Attributes["tls_enabled"].(resSchema.BoolAttribute)
	if !ok {
		t.Fatal("expected a Bool attribute tls_enabled")
	}
	if !attr.Computed || !attr.Optional {
		t.Errorf("tls_enabled must stay Optional+Computed (existing `tls_enabled = false` configs keep planning), got optional=%v computed=%v", attr.Optional, attr.Computed)
	}
	if len(attr.Validators) != 1 {
		t.Fatalf("expected the plan-time refusal validator on tls_enabled, got %d", len(attr.Validators))
	}
	for _, tc := range []struct {
		value   types.Bool
		refused bool
	}{
		{types.BoolValue(true), true},
		{types.BoolValue(false), false},
		{types.BoolNull(), false},
		{types.BoolUnknown(), false},
	} {
		resp := &validator.BoolResponse{}
		attr.Validators[0].ValidateBool(context.Background(), validator.BoolRequest{
			Path: path.Root("tls_enabled"), ConfigValue: tc.value,
		}, resp)
		if resp.Diagnostics.HasError() != tc.refused {
			t.Errorf("tls_enabled = %s: refused=%v, want %v", tc.value, resp.Diagnostics.HasError(), tc.refused)
		}
		if tc.refused && resp.Diagnostics.Errors()[0].Summary() != tlsEnabledRefusalTitle {
			t.Errorf("summary %q, want %q", resp.Diagnostics.Errors()[0].Summary(), tlsEnabledRefusalTitle)
		}
	}
}

// configOf is the configuration a plan was made from, for an Update test whose
// configuration sets exactly what the plan holds.
func configOf(plan tfsdk.Plan) tfsdk.Config {
	return tfsdk.Config{Schema: plan.Schema, Raw: plan.Raw}
}

// failOnAnyRequest is a server that records (and fails on) every request.
func failOnAnyRequest(t *testing.T, requested *atomic.Bool) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requested.Store(true)
		t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusInternalServerError)
	}))
}

func TestCreateRefusesTLSEnabledTrue(t *testing.T) {
	var requested atomic.Bool
	server := failOnAnyRequest(t, &requested)
	defer server.Close()
	c := client.NewClient(server.URL, "test-key", client.WithHTTPClient(server.Client())) // pragma: allowlist secret
	c.SetTenantIDForTest("t-1")
	r := newTestNginxResource(c)

	model := baseNginxModel()
	model.TLSEnabled = types.BoolValue(true)
	resp := resource.CreateResponse{State: emptyNginxInstanceState(t)}
	r.Create(context.Background(), resource.CreateRequest{Plan: buildNginxInstancePlan(t, model)}, &resp)

	if !resp.Diagnostics.HasError() || resp.Diagnostics.Errors()[0].Summary() != tlsEnabledRefusalTitle {
		t.Fatalf("expected the tls_enabled refusal, got %v", resp.Diagnostics)
	}
	if requested.Load() {
		t.Error("the refusal must fire before any API request")
	}
}

func TestUpdateRefusesTLSEnabledTrue(t *testing.T) {
	var requested atomic.Bool
	server := failOnAnyRequest(t, &requested)
	defer server.Close()
	c := client.NewClient(server.URL, "test-key", client.WithHTTPClient(server.Client())) // pragma: allowlist secret
	c.SetTenantIDForTest("t-1")
	r := newTestNginxResource(c)

	stateModel := baseNginxModel()
	stateModel.ID = types.StringValue("w-1")
	stateModel.Status = types.StringValue("running")
	planModel := stateModel
	planModel.Name = types.StringValue("renamed")
	planModel.TLSEnabled = types.BoolValue(true)
	state := buildNginxInstanceState(t, stateModel)

	plan := buildNginxInstancePlan(t, planModel)
	resp := resource.UpdateResponse{State: state}
	r.Update(context.Background(), resource.UpdateRequest{Plan: plan, Config: configOf(plan), State: state}, &resp)

	if !resp.Diagnostics.HasError() || resp.Diagnostics.Errors()[0].Summary() != tlsEnabledRefusalTitle {
		t.Fatalf("expected the tls_enabled refusal, got %v", resp.Diagnostics)
	}
	if requested.Load() {
		t.Error("the refusal must fire before any API request")
	}
}

// A state written by an older provider can hold tls_enabled = true (the API
// used to store the flag). Nothing migrates it locally: the schema is
// unchanged, and the next refresh reads the platform's observed value.
func TestReadRefreshesAStaleTLSEnabledTrue(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/tenants/t-1/webservers/w-1" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = fmt.Fprint(w, `{"id":"w-1","name":"my-nginx","type":"nginx","typeVersion":"1.27","flavorId":"web.co1.small",`+
			`"storageGb":20,"vpcId":"vpc-1","subnetId":"sn-1","tlsEnabled":false,"status":"running","port":80,`+
			`"createdAt":"2025-01-01T00:00:00Z","tags":{}}`)
	}))
	defer server.Close()
	c := client.NewClient(server.URL, "test-key", client.WithHTTPClient(server.Client())) // pragma: allowlist secret
	c.SetTenantIDForTest("t-1")
	r := newTestNginxResource(c)

	model := baseNginxModel()
	model.ID = types.StringValue("w-1")
	model.TLSEnabled = types.BoolValue(true)
	state := buildNginxInstanceState(t, model)
	resp := resource.ReadResponse{State: state}
	r.Read(context.Background(), resource.ReadRequest{State: state}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("read failed: %v", resp.Diagnostics.Errors())
	}
	var got NginxInstanceModel
	resp.State.Get(context.Background(), &got)
	if got.TLSEnabled.ValueBool() {
		t.Error("a refresh must replace a stale tls_enabled = true with the observed false")
	}
}

// A tls_enabled-only difference (a state still holding true, config false)
// issues no PUT: the field is never written.
func TestUpdateRequestNeverCarriesTLSEnabled(t *testing.T) {
	state := baseNginxModel()
	state.TLSEnabled = types.BoolValue(true)
	plan := baseNginxModel()
	plan.TLSEnabled = types.BoolValue(false)
	req := plan.toUpdateRequest(&state)
	if req.hasChanges() {
		t.Error("a tls_enabled-only difference must not issue a PUT")
	}
	requireNoTLSEnabledOnWire(t, req)
}

// A state `true` written by an older provider survives into the plan of a config
// that never set tls_enabled (UseStateForUnknown, e.g. under -refresh=false). The
// Update belt reads the configuration, so it does not refuse an attribute the user
// never set: the update proceeds, and the read-back takes the observed value.
func TestUpdateDoesNotRefuseAStaleStateTrueTheConfigNeverSet(t *testing.T) {
	var puts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPut && r.URL.Path == "/v1/tenants/t-1/webservers/w-1":
			puts.Add(1)
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			if _, ok := body["tlsEnabled"]; ok {
				t.Errorf("the PUT carries tlsEnabled: %v", body)
			}
			_, _ = fmt.Fprint(w, `{}`)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-1/webservers/w-1":
			_, _ = fmt.Fprint(w, `{"id":"w-1","name":"renamed","type":"x","typeVersion":"1","flavorId":"web.co1.small",`+
				`"storageGb":20,"vpcId":"vpc-1","subnetId":"sn-1","tlsEnabled":false,"status":"running","port":80,`+
				`"createdAt":"2025-01-01T00:00:00Z","tags":{}}`)
		case strings.HasSuffix(r.URL.Path, "/events"):
			w.WriteHeader(http.StatusNotFound)
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	c := client.NewClient(server.URL, "test-key", client.WithHTTPClient(server.Client())) // pragma: allowlist secret
	c.SetTenantIDForTest("t-1")
	r := newTestNginxResource(c)

	stateModel := baseNginxModel()
	stateModel.ID = types.StringValue("w-1")
	stateModel.Status = types.StringValue("running")
	stateModel.TLSEnabled = types.BoolValue(true)
	state := buildNginxInstanceState(t, stateModel)
	planModel := stateModel
	planModel.Name = types.StringValue("renamed")
	plan := buildNginxInstancePlan(t, planModel)
	configModel := planModel
	configModel.TLSEnabled = types.BoolNull()
	config := tfsdk.Config{Schema: plan.Schema, Raw: buildNginxInstancePlan(t, configModel).Raw}

	resp := resource.UpdateResponse{State: state}
	r.Update(context.Background(), resource.UpdateRequest{Plan: plan, Config: config, State: state}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("a tls_enabled the configuration never set was refused: %v", resp.Diagnostics.Errors())
	}
	if puts.Load() != 1 {
		t.Errorf("PUTs = %d, want 1 (the rename)", puts.Load())
	}
	var got NginxInstanceModel
	resp.State.Get(context.Background(), &got)
	if got.TLSEnabled.ValueBool() {
		t.Error("the read-back must take the observed tls_enabled = false")
	}
}
