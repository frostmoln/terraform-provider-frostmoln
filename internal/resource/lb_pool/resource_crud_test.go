package lb_pool

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
)

// --- model unit tests ---

func TestPoolToCreateRequestNoPersistence(t *testing.T) {
	m := PoolModel{
		Name:          types.StringValue("p1"),
		Protocol:      types.StringValue("http"),
		LBAlgorithm:   types.StringValue("round_robin"),
		ProxyProtocol: types.StringValue("v2"),
		ListenerID:    types.StringValue("l-1"),
	}
	req := m.toCreateRequest()
	if req.Name != "p1" || req.Protocol != "http" || req.LBAlgorithm != "round_robin" {
		t.Errorf("unexpected req: %+v", req)
	}
	if req.ProxyProtocol != "v2" {
		t.Errorf("expected proxyProtocol v2, got %s", req.ProxyProtocol)
	}
	if req.ListenerID != "l-1" {
		t.Errorf("expected listenerId l-1, got %s", req.ListenerID)
	}
	if req.SessionPersistence != nil {
		t.Error("expected nil session persistence")
	}
}

func TestPoolToCreateRequestWithPersistence(t *testing.T) {
	m := PoolModel{
		Name:        types.StringValue("p1"),
		Protocol:    types.StringValue("http"),
		LBAlgorithm: types.StringValue("source_ip"),
		SessionPersistence: &SessionPersistenceModel{
			Type:                   types.StringValue("APP_COOKIE"),
			CookieName:             types.StringValue("SESS"),
			PersistenceTimeout:     types.Int64Value(60),
			PersistenceGranularity: types.StringValue("255.255.255.0"),
		},
	}
	req := m.toCreateRequest()
	if req.SessionPersistence == nil {
		t.Fatal("expected session persistence")
	}
	if req.SessionPersistence.Type != "APP_COOKIE" || req.SessionPersistence.CookieName != "SESS" {
		t.Errorf("unexpected persistence: %+v", req.SessionPersistence)
	}
	if req.SessionPersistence.PersistenceTimeout != 60 {
		t.Errorf("expected timeout 60, got %d", req.SessionPersistence.PersistenceTimeout)
	}
}

func TestPoolToUpdateRequest(t *testing.T) {
	m := PoolModel{
		Name:        types.StringValue("renamed"),
		LBAlgorithm: types.StringValue("least_connections"),
	}
	req := m.toUpdateRequest()
	if req.Name == nil || *req.Name != "renamed" {
		t.Error("expected name in update")
	}
	if req.LBAlgorithm == nil || *req.LBAlgorithm != "least_connections" {
		t.Error("expected lbAlgorithm in update")
	}
}

func TestPoolFromAPIWithPersistence(t *testing.T) {
	p := &apiPool{
		ID:             "pool-1",
		LoadBalancerID: "lb-1",
		Name:           "p",
		Protocol:       "http",
		LBAlgorithm:    "source_ip",
		ProxyProtocol:  "v1",
		SessionPersistence: &apiSessionPersistence{
			Type:                   "SOURCE_IP",
			PersistenceGranularity: "255.255.255.0",
		},
		CreatedAt: "2025-01-01T00:00:00Z",
		UpdatedAt: "2025-02-01T00:00:00Z",
	}
	var m PoolModel
	m.fromAPI(context.Background(), p, nil)
	if m.SessionPersistence == nil {
		t.Fatal("expected session persistence")
	}
	if m.SessionPersistence.Type.ValueString() != "SOURCE_IP" {
		t.Errorf("expected SOURCE_IP, got %s", m.SessionPersistence.Type.ValueString())
	}
	if m.ProxyProtocol.ValueString() != "v1" {
		t.Errorf("expected proxy_protocol v1, got %s", m.ProxyProtocol.ValueString())
	}
}

func TestPoolFromAPINoPersistence(t *testing.T) {
	p := &apiPool{
		ID:             "pool-1",
		LoadBalancerID: "lb-1",
		Name:           "p",
		Protocol:       "tcp",
		LBAlgorithm:    "round_robin",
		CreatedAt:      "2025-01-01T00:00:00Z",
	}
	var m PoolModel
	m.fromAPI(context.Background(), p, nil)
	if m.SessionPersistence != nil {
		t.Error("expected nil session persistence")
	}
	if !m.ListenerID.IsNull() {
		t.Error("expected null listener_id")
	}
	if !m.UpdatedAt.IsNull() {
		t.Error("expected null updated_at")
	}
}

// --- resource lifecycle tests ---

func TestPoolNewResource(t *testing.T) {
	if NewResource() == nil {
		t.Fatal("expected non-nil resource")
	}
}

func TestPoolSchema(t *testing.T) {
	r := NewResource()
	var resp resource.SchemaResponse
	r.Schema(context.Background(), resource.SchemaRequest{}, &resp)
	for _, attr := range []string{"id", "load_balancer_id", "listener_id", "name", "protocol", "lb_algorithm", "proxy_protocol", "session_persistence", "created_at", "updated_at"} {
		if _, ok := resp.Schema.Attributes[attr]; !ok {
			t.Errorf("expected attribute %q", attr)
		}
	}
}

func TestPoolConfigureNilProviderData(t *testing.T) {
	r := &poolResource{}
	var resp resource.ConfigureResponse
	r.Configure(context.Background(), resource.ConfigureRequest{}, &resp)
	if resp.Diagnostics.HasError() {
		t.Errorf("expected no errors, got %v", resp.Diagnostics.Errors())
	}
}

func TestPoolConfigureWrongType(t *testing.T) {
	r := &poolResource{}
	var resp resource.ConfigureResponse
	r.Configure(context.Background(), resource.ConfigureRequest{ProviderData: "nope"}, &resp)
	if !resp.Diagnostics.HasError() {
		t.Error("expected error for wrong provider data type")
	}
}

func buildPoolState(t *testing.T, model PoolModel) tfsdk.State {
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

func buildPoolPlan(t *testing.T, model PoolModel) tfsdk.Plan {
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

func samplePoolModel() PoolModel {
	return PoolModel{
		ID:             types.StringValue("pool-1"),
		LoadBalancerID: types.StringValue("lb-1"),
		ListenerID:     types.StringNull(),
		Name:           types.StringValue("pool"),
		Protocol:       types.StringValue("http"),
		LBAlgorithm:    types.StringValue("round_robin"),
		ProxyProtocol:  types.StringValue("none"),
		CreatedAt:      types.StringValue("2025-01-01T00:00:00Z"),
		UpdatedAt:      types.StringNull(),
	}
}

func TestPoolCreate(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/v1/tenants/t-1/load-balancers/lb-1/pools" {
			var body apiCreatePoolRequest
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body.Name != "pool" {
				t.Errorf("expected name pool, got %s", body.Name)
			}
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(apiPool{
				ID: "pool-new", LoadBalancerID: "lb-1", Name: "pool", Protocol: "http",
				LBAlgorithm: "round_robin", ProxyProtocol: "none", CreatedAt: "2025-01-01T00:00:00Z",
			})
			return
		}
		t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key", client.WithHTTPClient(server.Client())) // pragma: allowlist secret
	c.SetTenantIDForTest("t-1")
	r := &poolResource{client: c}

	plan := buildPoolPlan(t, PoolModel{
		LoadBalancerID: types.StringValue("lb-1"),
		ListenerID:     types.StringNull(),
		Name:           types.StringValue("pool"),
		Protocol:       types.StringValue("http"),
		LBAlgorithm:    types.StringValue("round_robin"),
		ProxyProtocol:  types.StringValue("none"),
	})
	resp := resource.CreateResponse{State: buildPoolState(t, samplePoolModel())}
	r.Create(context.Background(), resource.CreateRequest{Plan: plan}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("create failed: %v", resp.Diagnostics.Errors())
	}
	var result PoolModel
	resp.State.Get(context.Background(), &result)
	if result.ID.ValueString() != "pool-new" {
		t.Errorf("expected ID pool-new, got %s", result.ID.ValueString())
	}
}

func TestPoolCreateAPIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]string{"code": "INTERNAL", "message": "boom"})
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key", client.WithHTTPClient(server.Client())) // pragma: allowlist secret
	c.SetTenantIDForTest("t-1")
	r := &poolResource{client: c}

	plan := buildPoolPlan(t, PoolModel{
		LoadBalancerID: types.StringValue("lb-1"),
		ListenerID:     types.StringNull(),
		Name:           types.StringValue("pool"),
		Protocol:       types.StringValue("http"),
		LBAlgorithm:    types.StringValue("round_robin"),
		ProxyProtocol:  types.StringValue("none"),
	})
	resp := resource.CreateResponse{State: buildPoolState(t, samplePoolModel())}
	r.Create(context.Background(), resource.CreateRequest{Plan: plan}, &resp)
	if !resp.Diagnostics.HasError() {
		t.Error("expected error on API failure")
	}
}

func TestPoolRead(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-1/load-balancers/lb-1/pools/pool-1" {
			_ = json.NewEncoder(w).Encode(apiPool{
				ID: "pool-1", LoadBalancerID: "lb-1", Name: "pool", Protocol: "http",
				LBAlgorithm: "least_connections", ProxyProtocol: "none", CreatedAt: "2025-01-01T00:00:00Z",
			})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key", client.WithHTTPClient(server.Client())) // pragma: allowlist secret
	c.SetTenantIDForTest("t-1")
	r := &poolResource{client: c}

	state := buildPoolState(t, samplePoolModel())
	resp := resource.ReadResponse{State: state}
	r.Read(context.Background(), resource.ReadRequest{State: state}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("read failed: %v", resp.Diagnostics.Errors())
	}
	var result PoolModel
	resp.State.Get(context.Background(), &result)
	if result.LBAlgorithm.ValueString() != "least_connections" {
		t.Errorf("expected lb_algorithm least_connections, got %s", result.LBAlgorithm.ValueString())
	}
}

func TestPoolReadNotFound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]string{"code": "NOT_FOUND", "message": "nf"})
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key", client.WithHTTPClient(server.Client())) // pragma: allowlist secret
	c.SetTenantIDForTest("t-1")
	r := &poolResource{client: c}

	state := buildPoolState(t, samplePoolModel())
	resp := resource.ReadResponse{State: state}
	r.Read(context.Background(), resource.ReadRequest{State: state}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("expected no error for 404, got %v", resp.Diagnostics.Errors())
	}
	var result PoolModel
	if diags := resp.State.Get(context.Background(), &result); !diags.HasError() {
		if result.ID.ValueString() != "" {
			t.Error("expected state removed after 404")
		}
	}
}

func TestPoolUpdate(t *testing.T) {
	var updated apiUpdatePoolRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut && r.URL.Path == "/v1/tenants/t-1/load-balancers/lb-1/pools/pool-1" {
			_ = json.NewDecoder(r.Body).Decode(&updated)
			_ = json.NewEncoder(w).Encode(apiPool{
				ID: "pool-1", LoadBalancerID: "lb-1", Name: "renamed", Protocol: "http",
				LBAlgorithm: "source_ip", ProxyProtocol: "none", CreatedAt: "2025-01-01T00:00:00Z",
				UpdatedAt: "2025-02-01T00:00:00Z",
			})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key", client.WithHTTPClient(server.Client())) // pragma: allowlist secret
	c.SetTenantIDForTest("t-1")
	r := &poolResource{client: c}

	state := buildPoolState(t, samplePoolModel())
	plan := buildPoolPlan(t, PoolModel{
		ID:             types.StringValue("pool-1"),
		LoadBalancerID: types.StringValue("lb-1"),
		ListenerID:     types.StringNull(),
		Name:           types.StringValue("renamed"),
		Protocol:       types.StringValue("http"),
		LBAlgorithm:    types.StringValue("source_ip"),
		ProxyProtocol:  types.StringValue("none"),
		CreatedAt:      types.StringValue("2025-01-01T00:00:00Z"),
		UpdatedAt:      types.StringNull(),
	})
	resp := resource.UpdateResponse{State: state}
	r.Update(context.Background(), resource.UpdateRequest{Plan: plan, State: state}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("update failed: %v", resp.Diagnostics.Errors())
	}
	if updated.Name == nil || *updated.Name != "renamed" {
		t.Error("expected name in update body")
	}
	var result PoolModel
	resp.State.Get(context.Background(), &result)
	if result.Name.ValueString() != "renamed" {
		t.Errorf("expected name renamed, got %s", result.Name.ValueString())
	}
}

func TestPoolDelete(t *testing.T) {
	deleted := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete && r.URL.Path == "/v1/tenants/t-1/load-balancers/lb-1/pools/pool-1" {
			deleted = true
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key", client.WithHTTPClient(server.Client())) // pragma: allowlist secret
	c.SetTenantIDForTest("t-1")
	r := &poolResource{client: c}

	state := buildPoolState(t, samplePoolModel())
	resp := resource.DeleteResponse{State: state}
	r.Delete(context.Background(), resource.DeleteRequest{State: state}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("delete failed: %v", resp.Diagnostics.Errors())
	}
	if !deleted {
		t.Error("expected DELETE to be called")
	}
}

func TestPoolDeleteAlreadyGone(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]string{"code": "NOT_FOUND", "message": "nf"})
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key", client.WithHTTPClient(server.Client())) // pragma: allowlist secret
	c.SetTenantIDForTest("t-1")
	r := &poolResource{client: c}

	state := buildPoolState(t, samplePoolModel())
	resp := resource.DeleteResponse{State: state}
	r.Delete(context.Background(), resource.DeleteRequest{State: state}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("delete of already-gone pool should not error, got %v", resp.Diagnostics.Errors())
	}
}
