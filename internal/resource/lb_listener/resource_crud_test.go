package lb_listener

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
)

// --- model unit tests ---

func cidrList(t *testing.T, cidrs ...string) types.List {
	t.Helper()
	l, diags := types.ListValueFrom(context.Background(), types.StringType, cidrs)
	if diags.HasError() {
		t.Fatalf("list build failed: %v", diags.Errors())
	}
	return l
}

func headerMap(t *testing.T, m map[string]string) types.Map {
	t.Helper()
	mv, diags := types.MapValueFrom(context.Background(), types.StringType, m)
	if diags.HasError() {
		t.Fatalf("map build failed: %v", diags.Errors())
	}
	return mv
}

func TestListenerToCreateRequest(t *testing.T) {
	ctx := context.Background()
	var diags diag.Diagnostics
	m := ListenerModel{
		Name:             types.StringValue("l1"),
		Protocol:         types.StringValue("http"),
		ProtocolPort:     types.Int64Value(80),
		AllowedCIDRs:     cidrList(t, "0.0.0.0/0"),
		InsertHeaders:    headerMap(t, map[string]string{"X-Fwd": "on"}),
		DefaultPoolID:    types.StringValue("pool-1"),
		TLSCertificateID: types.StringValue("cert-1"),
		ConnectionLimit:  types.Int64Value(1000),
	}
	req := m.toCreateRequest(ctx, &diags)
	if diags.HasError() {
		t.Fatalf("unexpected diags: %v", diags.Errors())
	}
	if req.Name != "l1" || req.Protocol != "http" || req.ProtocolPort != 80 {
		t.Errorf("unexpected req: %+v", req)
	}
	if req.DefaultPoolID != "pool-1" || req.TLSCertificateID != "cert-1" || req.ConnectionLimit != 1000 {
		t.Errorf("optionals not propagated: %+v", req)
	}
	if len(req.AllowedCIDRs) != 1 || req.AllowedCIDRs[0] != "0.0.0.0/0" {
		t.Errorf("allowedCidrs not propagated: %v", req.AllowedCIDRs)
	}
	if req.InsertHeaders["X-Fwd"] != "on" {
		t.Errorf("insertHeaders not propagated: %v", req.InsertHeaders)
	}
}

func TestListenerToUpdateRequest(t *testing.T) {
	ctx := context.Background()
	var diags diag.Diagnostics
	m := ListenerModel{
		Name:            types.StringValue("renamed"),
		DefaultPoolID:   types.StringValue("pool-2"),
		ConnectionLimit: types.Int64Value(2000),
		AllowedCIDRs:    cidrList(t, "10.0.0.0/8"),
		InsertHeaders:   headerMap(t, map[string]string{"H": "v"}),
	}
	req := m.toUpdateRequest(ctx, &diags)
	if diags.HasError() {
		t.Fatalf("unexpected diags: %v", diags.Errors())
	}
	if req.Name == nil || *req.Name != "renamed" {
		t.Error("expected name in update")
	}
	if req.DefaultPoolID == nil || *req.DefaultPoolID != "pool-2" {
		t.Error("expected defaultPoolId in update")
	}
	if req.ConnectionLimit == nil || *req.ConnectionLimit != 2000 {
		t.Error("expected connectionLimit in update")
	}
}

func TestListenerFromAPIEmpty(t *testing.T) {
	ctx := context.Background()
	var diags diag.Diagnostics
	l := &apiListener{
		ID:             "l-1",
		LoadBalancerID: "lb-1",
		Name:           "l",
		Protocol:       "tcp",
		ProtocolPort:   443,
		AdminStateUp:   true,
		CreatedAt:      "2025-01-01T00:00:00Z",
	}
	var m ListenerModel
	m.fromAPI(ctx, l, &diags)
	if diags.HasError() {
		t.Fatalf("unexpected diags: %v", diags.Errors())
	}
	if !m.DefaultPoolID.IsNull() {
		t.Error("expected null default_pool_id")
	}
	if !m.TLSCertificateID.IsNull() {
		t.Error("expected null tls_certificate_id")
	}
	if !m.InsertHeaders.IsNull() {
		t.Error("expected null insert_headers")
	}
	if !m.UpdatedAt.IsNull() {
		t.Error("expected null updated_at")
	}
	// allowed_cidrs should be an empty (non-null) list
	if m.AllowedCIDRs.IsNull() {
		t.Error("expected empty (non-null) allowed_cidrs list")
	}
}

// --- resource lifecycle tests ---

func TestListenerNewResource(t *testing.T) {
	if NewResource() == nil {
		t.Fatal("expected non-nil resource")
	}
}

func TestListenerSchema(t *testing.T) {
	r := NewResource()
	var resp resource.SchemaResponse
	r.Schema(context.Background(), resource.SchemaRequest{}, &resp)
	for _, attr := range []string{"id", "load_balancer_id", "name", "protocol", "protocol_port", "allowed_cidrs", "insert_headers", "default_pool_id", "tls_certificate_id", "connection_limit", "admin_state_up", "created_at", "updated_at"} {
		if _, ok := resp.Schema.Attributes[attr]; !ok {
			t.Errorf("expected attribute %q", attr)
		}
	}
}

func TestListenerConfigureNilProviderData(t *testing.T) {
	r := &listenerResource{}
	var resp resource.ConfigureResponse
	r.Configure(context.Background(), resource.ConfigureRequest{}, &resp)
	if resp.Diagnostics.HasError() {
		t.Errorf("expected no errors, got %v", resp.Diagnostics.Errors())
	}
}

func TestListenerConfigureWrongType(t *testing.T) {
	r := &listenerResource{}
	var resp resource.ConfigureResponse
	r.Configure(context.Background(), resource.ConfigureRequest{ProviderData: 42}, &resp)
	if !resp.Diagnostics.HasError() {
		t.Error("expected error for wrong provider data type")
	}
}

func buildListenerState(t *testing.T, model ListenerModel) tfsdk.State {
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

func buildListenerPlan(t *testing.T, model ListenerModel) tfsdk.Plan {
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

func sampleListenerModel(t *testing.T) ListenerModel {
	return ListenerModel{
		ID:               types.StringValue("l-1"),
		LoadBalancerID:   types.StringValue("lb-1"),
		Name:             types.StringValue("listener"),
		Protocol:         types.StringValue("http"),
		ProtocolPort:     types.Int64Value(80),
		AllowedCIDRs:     cidrList(t, "0.0.0.0/0"),
		InsertHeaders:    types.MapNull(types.StringType),
		DefaultPoolID:    types.StringNull(),
		TLSCertificateID: types.StringNull(),
		ConnectionLimit:  types.Int64Value(0),
		AdminStateUp:     types.BoolValue(true),
		CreatedAt:        types.StringValue("2025-01-01T00:00:00Z"),
		UpdatedAt:        types.StringNull(),
	}
}

func TestListenerCreate(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/v1/tenants/t-1/load-balancers/lb-1/listeners" {
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(apiListener{
				ID: "l-new", LoadBalancerID: "lb-1", Name: "listener", Protocol: "http",
				ProtocolPort: 80, AllowedCIDRs: []string{"0.0.0.0/0"}, ConnectionLimit: 1000,
				AdminStateUp: true, CreatedAt: "2025-01-01T00:00:00Z",
			})
			return
		}
		t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key", client.WithHTTPClient(server.Client())) // pragma: allowlist secret
	c.SetTenantIDForTest("t-1")
	r := &listenerResource{client: c}

	plan := buildListenerPlan(t, ListenerModel{
		LoadBalancerID:   types.StringValue("lb-1"),
		Name:             types.StringValue("listener"),
		Protocol:         types.StringValue("http"),
		ProtocolPort:     types.Int64Value(80),
		AllowedCIDRs:     cidrList(t, "0.0.0.0/0"),
		InsertHeaders:    types.MapNull(types.StringType),
		DefaultPoolID:    types.StringNull(),
		TLSCertificateID: types.StringNull(),
		ConnectionLimit:  types.Int64Null(),
	})
	resp := resource.CreateResponse{State: buildListenerState(t, sampleListenerModel(t))}
	r.Create(context.Background(), resource.CreateRequest{Plan: plan}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("create failed: %v", resp.Diagnostics.Errors())
	}
	var result ListenerModel
	resp.State.Get(context.Background(), &result)
	if result.ID.ValueString() != "l-new" {
		t.Errorf("expected ID l-new, got %s", result.ID.ValueString())
	}
	if result.ConnectionLimit.ValueInt64() != 1000 {
		t.Errorf("expected connection_limit 1000, got %d", result.ConnectionLimit.ValueInt64())
	}
}

func TestListenerCreateAPIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"code": "BAD", "message": "bad"})
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key", client.WithHTTPClient(server.Client())) // pragma: allowlist secret
	c.SetTenantIDForTest("t-1")
	r := &listenerResource{client: c}

	plan := buildListenerPlan(t, ListenerModel{
		LoadBalancerID:   types.StringValue("lb-1"),
		Name:             types.StringValue("listener"),
		Protocol:         types.StringValue("http"),
		ProtocolPort:     types.Int64Value(80),
		AllowedCIDRs:     cidrList(t, "0.0.0.0/0"),
		InsertHeaders:    types.MapNull(types.StringType),
		DefaultPoolID:    types.StringNull(),
		TLSCertificateID: types.StringNull(),
		ConnectionLimit:  types.Int64Null(),
	})
	resp := resource.CreateResponse{State: buildListenerState(t, sampleListenerModel(t))}
	r.Create(context.Background(), resource.CreateRequest{Plan: plan}, &resp)
	if !resp.Diagnostics.HasError() {
		t.Error("expected error on API failure")
	}
}

func TestListenerRead(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-1/load-balancers/lb-1/listeners/l-1" {
			_ = json.NewEncoder(w).Encode(apiListener{
				ID: "l-1", LoadBalancerID: "lb-1", Name: "listener", Protocol: "http",
				ProtocolPort: 80, AllowedCIDRs: []string{"0.0.0.0/0"}, ConnectionLimit: 500,
				AdminStateUp: true, CreatedAt: "2025-01-01T00:00:00Z",
			})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key", client.WithHTTPClient(server.Client())) // pragma: allowlist secret
	c.SetTenantIDForTest("t-1")
	r := &listenerResource{client: c}

	state := buildListenerState(t, sampleListenerModel(t))
	resp := resource.ReadResponse{State: state}
	r.Read(context.Background(), resource.ReadRequest{State: state}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("read failed: %v", resp.Diagnostics.Errors())
	}
	var result ListenerModel
	resp.State.Get(context.Background(), &result)
	if result.ConnectionLimit.ValueInt64() != 500 {
		t.Errorf("expected connection_limit 500, got %d", result.ConnectionLimit.ValueInt64())
	}
}

func TestListenerReadNotFound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]string{"code": "NOT_FOUND", "message": "nf"})
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key", client.WithHTTPClient(server.Client())) // pragma: allowlist secret
	c.SetTenantIDForTest("t-1")
	r := &listenerResource{client: c}

	state := buildListenerState(t, sampleListenerModel(t))
	resp := resource.ReadResponse{State: state}
	r.Read(context.Background(), resource.ReadRequest{State: state}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("expected no error for 404, got %v", resp.Diagnostics.Errors())
	}
	var result ListenerModel
	if diags := resp.State.Get(context.Background(), &result); !diags.HasError() {
		if result.ID.ValueString() != "" {
			t.Error("expected state removed after 404")
		}
	}
}

func TestListenerUpdate(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut && r.URL.Path == "/v1/tenants/t-1/load-balancers/lb-1/listeners/l-1" {
			_ = json.NewEncoder(w).Encode(apiListener{
				ID: "l-1", LoadBalancerID: "lb-1", Name: "renamed", Protocol: "http",
				ProtocolPort: 80, AllowedCIDRs: []string{"10.0.0.0/8"}, ConnectionLimit: 1500,
				AdminStateUp: true, CreatedAt: "2025-01-01T00:00:00Z", UpdatedAt: "2025-02-01T00:00:00Z",
			})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key", client.WithHTTPClient(server.Client())) // pragma: allowlist secret
	c.SetTenantIDForTest("t-1")
	r := &listenerResource{client: c}

	state := buildListenerState(t, sampleListenerModel(t))
	plan := buildListenerPlan(t, ListenerModel{
		ID:               types.StringValue("l-1"),
		LoadBalancerID:   types.StringValue("lb-1"),
		Name:             types.StringValue("renamed"),
		Protocol:         types.StringValue("http"),
		ProtocolPort:     types.Int64Value(80),
		AllowedCIDRs:     cidrList(t, "10.0.0.0/8"),
		InsertHeaders:    types.MapNull(types.StringType),
		DefaultPoolID:    types.StringNull(),
		TLSCertificateID: types.StringNull(),
		ConnectionLimit:  types.Int64Value(1500),
		AdminStateUp:     types.BoolValue(true),
		CreatedAt:        types.StringValue("2025-01-01T00:00:00Z"),
		UpdatedAt:        types.StringNull(),
	})
	resp := resource.UpdateResponse{State: state}
	r.Update(context.Background(), resource.UpdateRequest{Plan: plan, State: state}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("update failed: %v", resp.Diagnostics.Errors())
	}
	var result ListenerModel
	resp.State.Get(context.Background(), &result)
	if result.Name.ValueString() != "renamed" {
		t.Errorf("expected name renamed, got %s", result.Name.ValueString())
	}
	if result.UpdatedAt.ValueString() != "2025-02-01T00:00:00Z" {
		t.Errorf("expected updated_at set, got %s", result.UpdatedAt.ValueString())
	}
}

func TestListenerDelete(t *testing.T) {
	deleted := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete && r.URL.Path == "/v1/tenants/t-1/load-balancers/lb-1/listeners/l-1" {
			deleted = true
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key", client.WithHTTPClient(server.Client())) // pragma: allowlist secret
	c.SetTenantIDForTest("t-1")
	r := &listenerResource{client: c}

	state := buildListenerState(t, sampleListenerModel(t))
	resp := resource.DeleteResponse{State: state}
	r.Delete(context.Background(), resource.DeleteRequest{State: state}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("delete failed: %v", resp.Diagnostics.Errors())
	}
	if !deleted {
		t.Error("expected DELETE to be called")
	}
}

func TestListenerDeleteAlreadyGone(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]string{"code": "NOT_FOUND", "message": "nf"})
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key", client.WithHTTPClient(server.Client())) // pragma: allowlist secret
	c.SetTenantIDForTest("t-1")
	r := &listenerResource{client: c}

	state := buildListenerState(t, sampleListenerModel(t))
	resp := resource.DeleteResponse{State: state}
	r.Delete(context.Background(), resource.DeleteRequest{State: state}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("delete of already-gone listener should not error, got %v", resp.Diagnostics.Errors())
	}
}
