package lb_health_monitor

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

func TestHealthMonitorToCreateRequest(t *testing.T) {
	m := HealthMonitorModel{
		Type:          types.StringValue("http"),
		Delay:         types.Int64Value(5),
		Timeout:       types.Int64Value(3),
		MaxRetries:    types.Int64Value(3),
		HTTPMethod:    types.StringValue("GET"),
		URLPath:       types.StringValue("/healthz"),
		ExpectedCodes: types.StringValue("200-299"),
	}
	req := m.toCreateRequest()
	if req.Type != "http" || req.Delay != 5 || req.Timeout != 3 || req.MaxRetries != 3 {
		t.Errorf("unexpected req: %+v", req)
	}
	if req.HTTPMethod != "GET" || req.URLPath != "/healthz" || req.ExpectedCodes != "200-299" {
		t.Errorf("http optionals not propagated: %+v", req)
	}
}

func TestHealthMonitorToCreateRequestTCP(t *testing.T) {
	m := HealthMonitorModel{
		Type:          types.StringValue("tcp"),
		Delay:         types.Int64Value(10),
		Timeout:       types.Int64Value(5),
		MaxRetries:    types.Int64Value(2),
		HTTPMethod:    types.StringNull(),
		URLPath:       types.StringNull(),
		ExpectedCodes: types.StringNull(),
	}
	req := m.toCreateRequest()
	if req.HTTPMethod != "" || req.URLPath != "" || req.ExpectedCodes != "" {
		t.Errorf("expected empty http fields for tcp monitor, got %+v", req)
	}
}

func TestHealthMonitorToUpdateRequest(t *testing.T) {
	m := HealthMonitorModel{
		Delay:         types.Int64Value(7),
		Timeout:       types.Int64Value(4),
		MaxRetries:    types.Int64Value(5),
		HTTPMethod:    types.StringValue("HEAD"),
		URLPath:       types.StringValue("/ping"),
		ExpectedCodes: types.StringValue("200"),
	}
	req := m.toUpdateRequest()
	if req.Delay == nil || *req.Delay != 7 {
		t.Error("expected delay in update")
	}
	if req.MaxRetries == nil || *req.MaxRetries != 5 {
		t.Error("expected maxRetries in update")
	}
	if req.HTTPMethod == nil || *req.HTTPMethod != "HEAD" {
		t.Error("expected httpMethod in update")
	}
}

func TestHealthMonitorFromAPINulls(t *testing.T) {
	hm := &apiHealthMonitor{
		ID:         "hm-1",
		PoolID:     "pool-1",
		Type:       "tcp",
		Delay:      5,
		Timeout:    3,
		MaxRetries: 3,
		CreatedAt:  "2025-01-01T00:00:00Z",
	}
	var m HealthMonitorModel
	m.fromAPI("lb-1", hm)
	if !m.HTTPMethod.IsNull() || !m.URLPath.IsNull() || !m.ExpectedCodes.IsNull() {
		t.Error("expected null http fields")
	}
	if !m.UpdatedAt.IsNull() {
		t.Error("expected null updated_at")
	}
	if m.LoadBalancerID.ValueString() != "lb-1" {
		t.Errorf("expected lbID preserved, got %s", m.LoadBalancerID.ValueString())
	}
}

// --- resource lifecycle tests ---

func TestHealthMonitorNewResource(t *testing.T) {
	if NewResource() == nil {
		t.Fatal("expected non-nil resource")
	}
}

func TestHealthMonitorSchema(t *testing.T) {
	r := NewResource()
	var resp resource.SchemaResponse
	r.Schema(context.Background(), resource.SchemaRequest{}, &resp)
	for _, attr := range []string{"id", "load_balancer_id", "pool_id", "type", "delay", "timeout", "max_retries", "url_path", "http_method", "expected_codes", "created_at", "updated_at"} {
		if _, ok := resp.Schema.Attributes[attr]; !ok {
			t.Errorf("expected attribute %q", attr)
		}
	}
}

func TestHealthMonitorConfigureNilProviderData(t *testing.T) {
	r := &healthMonitorResource{}
	var resp resource.ConfigureResponse
	r.Configure(context.Background(), resource.ConfigureRequest{}, &resp)
	if resp.Diagnostics.HasError() {
		t.Errorf("expected no errors, got %v", resp.Diagnostics.Errors())
	}
}

func TestHealthMonitorConfigureWrongType(t *testing.T) {
	r := &healthMonitorResource{}
	var resp resource.ConfigureResponse
	r.Configure(context.Background(), resource.ConfigureRequest{ProviderData: "nope"}, &resp)
	if !resp.Diagnostics.HasError() {
		t.Error("expected error for wrong provider data type")
	}
}

func buildHMState(t *testing.T, model HealthMonitorModel) tfsdk.State {
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

func buildHMPlan(t *testing.T, model HealthMonitorModel) tfsdk.Plan {
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

func sampleHMModel() HealthMonitorModel {
	return HealthMonitorModel{
		ID:             types.StringValue("hm-1"),
		LoadBalancerID: types.StringValue("lb-1"),
		PoolID:         types.StringValue("pool-1"),
		Type:           types.StringValue("http"),
		Delay:          types.Int64Value(5),
		Timeout:        types.Int64Value(3),
		MaxRetries:     types.Int64Value(3),
		URLPath:        types.StringValue("/healthz"),
		HTTPMethod:     types.StringValue("GET"),
		ExpectedCodes:  types.StringValue("200"),
		CreatedAt:      types.StringValue("2025-01-01T00:00:00Z"),
		UpdatedAt:      types.StringNull(),
	}
}

func TestHealthMonitorCreate(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/v1/tenants/t-1/load-balancers/lb-1/pools/pool-1/healthmonitor" {
			var body apiCreateHealthMonitorRequest
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body.Type != "http" {
				t.Errorf("expected type http, got %s", body.Type)
			}
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(apiHealthMonitor{
				ID: "hm-new", PoolID: "pool-1", Type: "http", Delay: 5, Timeout: 3, MaxRetries: 3,
				HTTPMethod: "GET", URLPath: "/healthz", ExpectedCodes: "200", CreatedAt: "2025-01-01T00:00:00Z",
			})
			return
		}
		t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key", client.WithHTTPClient(server.Client())) // pragma: allowlist secret
	c.SetTenantIDForTest("t-1")
	r := &healthMonitorResource{client: c}

	plan := buildHMPlan(t, HealthMonitorModel{
		LoadBalancerID: types.StringValue("lb-1"),
		PoolID:         types.StringValue("pool-1"),
		Type:           types.StringValue("http"),
		Delay:          types.Int64Value(5),
		Timeout:        types.Int64Value(3),
		MaxRetries:     types.Int64Value(3),
		URLPath:        types.StringValue("/healthz"),
		HTTPMethod:     types.StringValue("GET"),
		ExpectedCodes:  types.StringValue("200"),
	})
	resp := resource.CreateResponse{State: buildHMState(t, sampleHMModel())}
	r.Create(context.Background(), resource.CreateRequest{Plan: plan}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("create failed: %v", resp.Diagnostics.Errors())
	}
	var result HealthMonitorModel
	resp.State.Get(context.Background(), &result)
	if result.ID.ValueString() != "hm-new" {
		t.Errorf("expected ID hm-new, got %s", result.ID.ValueString())
	}
	if result.LoadBalancerID.ValueString() != "lb-1" {
		t.Errorf("expected lbID preserved, got %s", result.LoadBalancerID.ValueString())
	}
}

func TestHealthMonitorCreateAPIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusConflict)
		_ = json.NewEncoder(w).Encode(map[string]string{"code": "CONFLICT", "message": "exists"})
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key", client.WithHTTPClient(server.Client())) // pragma: allowlist secret
	c.SetTenantIDForTest("t-1")
	r := &healthMonitorResource{client: c}

	plan := buildHMPlan(t, HealthMonitorModel{
		LoadBalancerID: types.StringValue("lb-1"),
		PoolID:         types.StringValue("pool-1"),
		Type:           types.StringValue("tcp"),
		Delay:          types.Int64Value(5),
		Timeout:        types.Int64Value(3),
		MaxRetries:     types.Int64Value(3),
		URLPath:        types.StringNull(),
		HTTPMethod:     types.StringNull(),
		ExpectedCodes:  types.StringNull(),
	})
	resp := resource.CreateResponse{State: buildHMState(t, sampleHMModel())}
	r.Create(context.Background(), resource.CreateRequest{Plan: plan}, &resp)
	if !resp.Diagnostics.HasError() {
		t.Error("expected error on API failure")
	}
}

func TestHealthMonitorRead(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-1/load-balancers/lb-1/pools/pool-1/healthmonitor" {
			_ = json.NewEncoder(w).Encode(apiHealthMonitor{
				ID: "hm-1", PoolID: "pool-1", Type: "http", Delay: 5, Timeout: 3, MaxRetries: 3,
				HTTPMethod: "GET", URLPath: "/healthz", ExpectedCodes: "200", CreatedAt: "2025-01-01T00:00:00Z",
			})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key", client.WithHTTPClient(server.Client())) // pragma: allowlist secret
	c.SetTenantIDForTest("t-1")
	r := &healthMonitorResource{client: c}

	state := buildHMState(t, sampleHMModel())
	resp := resource.ReadResponse{State: state}
	r.Read(context.Background(), resource.ReadRequest{State: state}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("read failed: %v", resp.Diagnostics.Errors())
	}
	var result HealthMonitorModel
	resp.State.Get(context.Background(), &result)
	if result.URLPath.ValueString() != "/healthz" {
		t.Errorf("expected url_path /healthz, got %s", result.URLPath.ValueString())
	}
}

func TestHealthMonitorReadNotFound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]string{"code": "NOT_FOUND", "message": "nf"})
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key", client.WithHTTPClient(server.Client())) // pragma: allowlist secret
	c.SetTenantIDForTest("t-1")
	r := &healthMonitorResource{client: c}

	state := buildHMState(t, sampleHMModel())
	resp := resource.ReadResponse{State: state}
	r.Read(context.Background(), resource.ReadRequest{State: state}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("expected no error for 404, got %v", resp.Diagnostics.Errors())
	}
	var result HealthMonitorModel
	if diags := resp.State.Get(context.Background(), &result); !diags.HasError() {
		if result.ID.ValueString() != "" {
			t.Error("expected state removed after 404")
		}
	}
}

func TestHealthMonitorUpdate(t *testing.T) {
	var updated apiUpdateHealthMonitorRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut && r.URL.Path == "/v1/tenants/t-1/load-balancers/lb-1/pools/pool-1/healthmonitor" {
			_ = json.NewDecoder(r.Body).Decode(&updated)
			_ = json.NewEncoder(w).Encode(apiHealthMonitor{
				ID: "hm-1", PoolID: "pool-1", Type: "http", Delay: 7, Timeout: 4, MaxRetries: 5,
				HTTPMethod: "GET", URLPath: "/healthz", ExpectedCodes: "200", CreatedAt: "2025-01-01T00:00:00Z",
				UpdatedAt: "2025-02-01T00:00:00Z",
			})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key", client.WithHTTPClient(server.Client())) // pragma: allowlist secret
	c.SetTenantIDForTest("t-1")
	r := &healthMonitorResource{client: c}

	state := buildHMState(t, sampleHMModel())
	plan := buildHMPlan(t, HealthMonitorModel{
		ID:             types.StringValue("hm-1"),
		LoadBalancerID: types.StringValue("lb-1"),
		PoolID:         types.StringValue("pool-1"),
		Type:           types.StringValue("http"),
		Delay:          types.Int64Value(7),
		Timeout:        types.Int64Value(4),
		MaxRetries:     types.Int64Value(5),
		URLPath:        types.StringValue("/healthz"),
		HTTPMethod:     types.StringValue("GET"),
		ExpectedCodes:  types.StringValue("200"),
		CreatedAt:      types.StringValue("2025-01-01T00:00:00Z"),
		UpdatedAt:      types.StringNull(),
	})
	resp := resource.UpdateResponse{State: state}
	r.Update(context.Background(), resource.UpdateRequest{Plan: plan, State: state}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("update failed: %v", resp.Diagnostics.Errors())
	}
	if updated.Delay == nil || *updated.Delay != 7 {
		t.Error("expected delay in update body")
	}
	var result HealthMonitorModel
	resp.State.Get(context.Background(), &result)
	if result.MaxRetries.ValueInt64() != 5 {
		t.Errorf("expected max_retries 5, got %d", result.MaxRetries.ValueInt64())
	}
}

func TestHealthMonitorDelete(t *testing.T) {
	deleted := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete && r.URL.Path == "/v1/tenants/t-1/load-balancers/lb-1/pools/pool-1/healthmonitor" {
			deleted = true
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key", client.WithHTTPClient(server.Client())) // pragma: allowlist secret
	c.SetTenantIDForTest("t-1")
	r := &healthMonitorResource{client: c}

	state := buildHMState(t, sampleHMModel())
	resp := resource.DeleteResponse{State: state}
	r.Delete(context.Background(), resource.DeleteRequest{State: state}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("delete failed: %v", resp.Diagnostics.Errors())
	}
	if !deleted {
		t.Error("expected DELETE to be called")
	}
}

func TestHealthMonitorDeleteAlreadyGone(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]string{"code": "NOT_FOUND", "message": "nf"})
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key", client.WithHTTPClient(server.Client())) // pragma: allowlist secret
	c.SetTenantIDForTest("t-1")
	r := &healthMonitorResource{client: c}

	state := buildHMState(t, sampleHMModel())
	resp := resource.DeleteResponse{State: state}
	r.Delete(context.Background(), resource.DeleteRequest{State: state}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("delete of already-gone monitor should not error, got %v", resp.Diagnostics.Errors())
	}
}
