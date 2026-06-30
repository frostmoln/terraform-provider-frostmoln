package webserver_domain

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
)

// --- Resource unit tests ---

func TestNewResource(t *testing.T) {
	if NewResource() == nil {
		t.Fatal("expected non-nil resource")
	}
}

func TestMetadata(t *testing.T) {
	r := NewResource()
	var resp resource.MetadataResponse
	r.Metadata(context.Background(), resource.MetadataRequest{ProviderTypeName: "frostmoln"}, &resp)
	if resp.TypeName != "frostmoln_webserver_domain" {
		t.Errorf("expected frostmoln_webserver_domain, got %s", resp.TypeName)
	}
}

func TestSchema(t *testing.T) {
	r := NewResource()
	var resp resource.SchemaResponse
	r.Schema(context.Background(), resource.SchemaRequest{}, &resp)
	for _, attr := range []string{"id", "instance_id", "domain_name", "tls_enabled", "is_default", "created_at"} {
		if _, ok := resp.Schema.Attributes[attr]; !ok {
			t.Errorf("expected attribute %s in schema", attr)
		}
	}
	// The webserver service does not return a status field for domain bindings.
	if _, ok := resp.Schema.Attributes["status"]; ok {
		t.Error("did not expect a status attribute in schema")
	}
}

func TestConfigureNilProviderData(t *testing.T) {
	r := &webserverDomainResource{}
	var resp resource.ConfigureResponse
	r.Configure(context.Background(), resource.ConfigureRequest{}, &resp)
	if resp.Diagnostics.HasError() {
		t.Errorf("expected no errors, got %v", resp.Diagnostics.Errors())
	}
}

func TestConfigureWrongType(t *testing.T) {
	r := &webserverDomainResource{}
	var resp resource.ConfigureResponse
	r.Configure(context.Background(), resource.ConfigureRequest{ProviderData: "bad"}, &resp)
	if !resp.Diagnostics.HasError() {
		t.Error("expected error for wrong provider data type")
	}
}

func TestPollDefaults(t *testing.T) {
	r := &webserverDomainResource{}
	if r.getPollInterval() != 5*time.Second {
		t.Errorf("expected default interval 5s, got %v", r.getPollInterval())
	}
	if r.getPollTimeout() != 10*time.Minute {
		t.Errorf("expected default timeout 10m, got %v", r.getPollTimeout())
	}
	r2 := &webserverDomainResource{pollInterval: time.Second, pollTimeout: time.Minute}
	if r2.getPollInterval() != time.Second || r2.getPollTimeout() != time.Minute {
		t.Error("expected overridden poll values")
	}
}

func TestUpdateNotSupported(t *testing.T) {
	r := &webserverDomainResource{}
	var resp resource.UpdateResponse
	r.Update(context.Background(), resource.UpdateRequest{}, &resp)
	if !resp.Diagnostics.HasError() {
		t.Error("expected update to be unsupported")
	}
}

// --- tfsdk helpers ---

func buildDomainState(t *testing.T, model webserverDomainModel) tfsdk.State {
	t.Helper()
	r := NewResource()
	var schemaResp resource.SchemaResponse
	r.Schema(context.Background(), resource.SchemaRequest{}, &schemaResp)
	state := tfsdk.State{Schema: schemaResp.Schema}
	if diags := state.Set(context.Background(), &model); diags.HasError() {
		t.Fatalf("failed to set state: %v", diags.Errors())
	}
	return state
}

func buildDomainPlan(t *testing.T, model webserverDomainModel) tfsdk.Plan {
	t.Helper()
	r := NewResource()
	var schemaResp resource.SchemaResponse
	r.Schema(context.Background(), resource.SchemaRequest{}, &schemaResp)
	plan := tfsdk.Plan{Schema: schemaResp.Schema}
	if diags := plan.Set(context.Background(), &model); diags.HasError() {
		t.Fatalf("failed to set plan: %v", diags.Errors())
	}
	return plan
}

func emptyDomainState(t *testing.T) tfsdk.State {
	t.Helper()
	r := NewResource()
	var schemaResp resource.SchemaResponse
	r.Schema(context.Background(), resource.SchemaRequest{}, &schemaResp)
	raw := tftypes.NewValue(schemaResp.Schema.Type().TerraformType(context.Background()), nil)
	return tfsdk.State{Schema: schemaResp.Schema, Raw: raw}
}

func fullDomainModel() webserverDomainModel {
	return webserverDomainModel{
		ID:         types.StringValue("dom-1"),
		InstanceID: types.StringValue("inst-1"),
		DomainName: types.StringValue("example.com"),
		TLSEnabled: types.BoolValue(true),
		IsDefault:  types.BoolValue(false),
		CreatedAt:  types.StringValue("2025-01-01T00:00:00Z"),
	}
}

// domainJSON returns the API binding shape (no status field per the contract).
func domainJSON() apiWebserverDomain {
	return apiWebserverDomain{
		ID:         "dom-1",
		InstanceID: "inst-1",
		DomainName: "example.com",
		TLSEnabled: true,
		IsDefault:  false,
		CreatedAt:  "2025-01-01T00:00:00Z",
	}
}

const (
	domainsPath = "/v1/tenants/t-1/webservers/inst-1/domains"
	domainPath  = "/v1/tenants/t-1/webservers/inst-1/domains/dom-1"
)

// --- CRUD tests ---

func TestCreate(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == domainsPath:
			var body apiCreateWebserverDomainRequest
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body.DomainName != "example.com" {
				t.Errorf("expected domainName example.com, got %s", body.DomainName)
			}
			if body.TLSEnabled == nil || !*body.TLSEnabled {
				t.Error("expected tlsEnabled true in request")
			}
			// The Add endpoint is synchronous and returns the created binding directly.
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(domainJSON())
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key", client.WithHTTPClient(server.Client())) // pragma: allowlist secret
	c.SetTenantIDForTest("t-1")

	r := &webserverDomainResource{client: c}

	plan := buildDomainPlan(t, webserverDomainModel{
		InstanceID: types.StringValue("inst-1"),
		DomainName: types.StringValue("example.com"),
		TLSEnabled: types.BoolValue(true),
		IsDefault:  types.BoolValue(false),
	})

	createResp := resource.CreateResponse{State: emptyDomainState(t)}
	r.Create(context.Background(), resource.CreateRequest{Plan: plan}, &createResp)
	if createResp.Diagnostics.HasError() {
		t.Fatalf("create failed: %v", createResp.Diagnostics.Errors())
	}
	var result webserverDomainModel
	createResp.State.Get(context.Background(), &result)
	if result.ID.ValueString() != "dom-1" {
		t.Errorf("expected ID dom-1, got %s", result.ID.ValueString())
	}
	if result.DomainName.ValueString() != "example.com" {
		t.Errorf("expected domain example.com, got %s", result.DomainName.ValueString())
	}
	if !result.TLSEnabled.ValueBool() {
		t.Error("expected tls_enabled true")
	}
}

func TestCreateMinimal(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == domainsPath:
			var body apiCreateWebserverDomainRequest
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body.TLSEnabled != nil || body.IsDefault != nil {
				t.Error("expected nil optional bool pointers for null values")
			}
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(domainJSON())
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key", client.WithHTTPClient(server.Client())) // pragma: allowlist secret
	c.SetTenantIDForTest("t-1")

	r := &webserverDomainResource{client: c}
	plan := buildDomainPlan(t, webserverDomainModel{
		InstanceID: types.StringValue("inst-1"),
		DomainName: types.StringValue("example.com"),
		TLSEnabled: types.BoolNull(),
		IsDefault:  types.BoolNull(),
	})

	createResp := resource.CreateResponse{State: emptyDomainState(t)}
	r.Create(context.Background(), resource.CreateRequest{Plan: plan}, &createResp)
	if createResp.Diagnostics.HasError() {
		t.Fatalf("create failed: %v", createResp.Diagnostics.Errors())
	}
}

func TestCreateAPIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]string{"code": "INTERNAL", "message": "boom"})
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key", client.WithHTTPClient(server.Client())) // pragma: allowlist secret
	c.SetTenantIDForTest("t-1")

	r := &webserverDomainResource{client: c}
	plan := buildDomainPlan(t, webserverDomainModel{
		InstanceID: types.StringValue("inst-1"),
		DomainName: types.StringValue("example.com"),
		TLSEnabled: types.BoolValue(true),
		IsDefault:  types.BoolValue(false),
	})

	createResp := resource.CreateResponse{State: emptyDomainState(t)}
	r.Create(context.Background(), resource.CreateRequest{Plan: plan}, &createResp)
	if !createResp.Diagnostics.HasError() {
		t.Error("expected error on create API failure")
	}
}

func TestRead(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Read fetches the instance's domain list and finds the binding by id.
		if r.Method == http.MethodGet && r.URL.Path == domainsPath {
			_ = json.NewEncoder(w).Encode(apiWebserverDomainList{Domains: []apiWebserverDomain{domainJSON()}})
			return
		}
		t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key", client.WithHTTPClient(server.Client())) // pragma: allowlist secret
	c.SetTenantIDForTest("t-1")

	r := &webserverDomainResource{client: c}
	state := buildDomainState(t, fullDomainModel())

	readResp := resource.ReadResponse{State: state}
	r.Read(context.Background(), resource.ReadRequest{State: state}, &readResp)
	if readResp.Diagnostics.HasError() {
		t.Fatalf("read failed: %v", readResp.Diagnostics.Errors())
	}
	var result webserverDomainModel
	readResp.State.Get(context.Background(), &result)
	if result.DomainName.ValueString() != "example.com" {
		t.Errorf("expected domain example.com, got %s", result.DomainName.ValueString())
	}
}

func TestReadNotInList(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == domainsPath {
			// The binding is no longer present in the instance's domain list.
			_ = json.NewEncoder(w).Encode(apiWebserverDomainList{Domains: []apiWebserverDomain{}})
			return
		}
		t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key", client.WithHTTPClient(server.Client())) // pragma: allowlist secret
	c.SetTenantIDForTest("t-1")

	r := &webserverDomainResource{client: c}
	state := buildDomainState(t, fullDomainModel())

	readResp := resource.ReadResponse{State: state}
	r.Read(context.Background(), resource.ReadRequest{State: state}, &readResp)
	if readResp.Diagnostics.HasError() {
		t.Fatalf("expected no error when binding absent, got %v", readResp.Diagnostics.Errors())
	}
	var result webserverDomainModel
	if diags := readResp.State.Get(context.Background(), &result); !diags.HasError() {
		if result.ID.ValueString() != "" {
			t.Error("expected state removed when binding not in list")
		}
	}
}

func TestDelete(t *testing.T) {
	deleted := atomic.Bool{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodDelete && r.URL.Path == domainPath:
			// Remove is synchronous (HTTP 204); no polling.
			deleted.Store(true)
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key", client.WithHTTPClient(server.Client())) // pragma: allowlist secret
	c.SetTenantIDForTest("t-1")

	r := &webserverDomainResource{client: c}
	state := buildDomainState(t, fullDomainModel())

	deleteResp := resource.DeleteResponse{State: state}
	r.Delete(context.Background(), resource.DeleteRequest{State: state}, &deleteResp)
	if deleteResp.Diagnostics.HasError() {
		t.Fatalf("delete failed: %v", deleteResp.Diagnostics.Errors())
	}
	if !deleted.Load() {
		t.Error("expected DELETE to be called")
	}
}

func TestCreateBadResponseBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == domainsPath {
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte("not json"))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key", client.WithHTTPClient(server.Client())) // pragma: allowlist secret
	c.SetTenantIDForTest("t-1")

	r := &webserverDomainResource{client: c}
	plan := buildDomainPlan(t, webserverDomainModel{
		InstanceID: types.StringValue("inst-1"),
		DomainName: types.StringValue("example.com"),
		TLSEnabled: types.BoolValue(true),
		IsDefault:  types.BoolValue(false),
	})

	createResp := resource.CreateResponse{State: emptyDomainState(t)}
	r.Create(context.Background(), resource.CreateRequest{Plan: plan}, &createResp)
	if !createResp.Diagnostics.HasError() {
		t.Error("expected error for bad response body on create")
	}
}

func TestReadBadResponseBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == domainsPath {
			_, _ = w.Write([]byte("not json"))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key", client.WithHTTPClient(server.Client())) // pragma: allowlist secret
	c.SetTenantIDForTest("t-1")

	r := &webserverDomainResource{client: c}
	state := buildDomainState(t, fullDomainModel())

	readResp := resource.ReadResponse{State: state}
	r.Read(context.Background(), resource.ReadRequest{State: state}, &readResp)
	if !readResp.Diagnostics.HasError() {
		t.Error("expected error for bad response body on read")
	}
}

func TestReadServerError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]string{"code": "INTERNAL", "message": "boom"})
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key", client.WithHTTPClient(server.Client())) // pragma: allowlist secret
	c.SetTenantIDForTest("t-1")

	r := &webserverDomainResource{client: c}
	state := buildDomainState(t, fullDomainModel())

	readResp := resource.ReadResponse{State: state}
	r.Read(context.Background(), resource.ReadRequest{State: state}, &readResp)
	if !readResp.Diagnostics.HasError() {
		t.Error("expected error for server error on read")
	}
}

func TestDeleteServerError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete && r.URL.Path == domainPath {
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(map[string]string{"code": "INTERNAL", "message": "boom"})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key", client.WithHTTPClient(server.Client())) // pragma: allowlist secret
	c.SetTenantIDForTest("t-1")

	r := &webserverDomainResource{client: c}
	state := buildDomainState(t, fullDomainModel())

	deleteResp := resource.DeleteResponse{State: state}
	r.Delete(context.Background(), resource.DeleteRequest{State: state}, &deleteResp)
	if !deleteResp.Diagnostics.HasError() {
		t.Error("expected error on delete server error")
	}
}

func TestDeleteAlreadyGone(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]string{"code": "NOT_FOUND", "message": "gone"})
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key", client.WithHTTPClient(server.Client())) // pragma: allowlist secret
	c.SetTenantIDForTest("t-1")

	r := &webserverDomainResource{client: c}
	state := buildDomainState(t, fullDomainModel())

	deleteResp := resource.DeleteResponse{State: state}
	r.Delete(context.Background(), resource.DeleteRequest{State: state}, &deleteResp)
	if deleteResp.Diagnostics.HasError() {
		t.Fatalf("delete of gone resource should not error, got %v", deleteResp.Diagnostics.Errors())
	}
}
