package lb_member

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

func TestMemberToCreateRequest(t *testing.T) {
	m := MemberModel{
		Address:      types.StringValue("10.0.0.20"),
		ProtocolPort: types.Int64Value(8443),
		Name:         types.StringValue("node-b"),
		SubnetID:     types.StringValue("subnet-9"),
		Weight:       types.Int64Value(7),
		CrossVPC:     types.BoolValue(true),
	}
	req := m.toCreateRequest()
	if req.Address != "10.0.0.20" {
		t.Errorf("expected address 10.0.0.20, got %s", req.Address)
	}
	if req.ProtocolPort != 8443 {
		t.Errorf("expected port 8443, got %d", req.ProtocolPort)
	}
	if req.Name != "node-b" {
		t.Errorf("expected name node-b, got %s", req.Name)
	}
	if req.SubnetID != "subnet-9" {
		t.Errorf("expected subnetId subnet-9, got %s", req.SubnetID)
	}
	if req.Weight != 7 {
		t.Errorf("expected weight 7, got %d", req.Weight)
	}
	if !req.CrossVPC {
		t.Error("expected crossVpc true")
	}
}

func TestMemberToCreateRequestMinimal(t *testing.T) {
	m := MemberModel{
		Address:      types.StringValue("10.0.0.1"),
		ProtocolPort: types.Int64Value(80),
		Name:         types.StringNull(),
		SubnetID:     types.StringNull(),
		Weight:       types.Int64Null(),
		CrossVPC:     types.BoolNull(),
	}
	req := m.toCreateRequest()
	if req.Name != "" || req.SubnetID != "" || req.Weight != 0 || req.CrossVPC {
		t.Errorf("expected zero-value optionals, got %+v", req)
	}
}

func TestMemberToUpdateRequest(t *testing.T) {
	m := MemberModel{
		Name:   types.StringValue("renamed"),
		Weight: types.Int64Value(9),
	}
	req := m.toUpdateRequest()
	if req.Name == nil || *req.Name != "renamed" {
		t.Error("expected name renamed in update request")
	}
	if req.Weight == nil || *req.Weight != 9 {
		t.Error("expected weight 9 in update request")
	}
}

func TestMemberToUpdateRequestEmpty(t *testing.T) {
	m := MemberModel{
		Name:   types.StringNull(),
		Weight: types.Int64Null(),
	}
	req := m.toUpdateRequest()
	if req.Name != nil || req.Weight != nil {
		t.Error("expected empty update request when no mutable fields set")
	}
}

func TestMemberFromAPINulls(t *testing.T) {
	mem := &apiMember{
		ID:           "mem-2",
		PoolID:       "pool-2",
		Address:      "10.0.0.2",
		ProtocolPort: 80,
		Weight:       1,
		CreatedAt:    "2025-01-01T00:00:00Z",
	}
	var m MemberModel
	m.fromAPI("lb-2", mem)
	if !m.Name.IsNull() {
		t.Error("expected null name")
	}
	if !m.SubnetID.IsNull() {
		t.Error("expected null subnet_id")
	}
	if !m.UpdatedAt.IsNull() {
		t.Error("expected null updated_at")
	}
}

// --- resource lifecycle tests ---

func TestMemberNewResource(t *testing.T) {
	if NewResource() == nil {
		t.Fatal("expected non-nil resource")
	}
}

func TestMemberSchema(t *testing.T) {
	r := NewResource()
	var resp resource.SchemaResponse
	r.Schema(context.Background(), resource.SchemaRequest{}, &resp)
	for _, attr := range []string{"id", "load_balancer_id", "pool_id", "address", "protocol_port", "name", "weight", "subnet_id", "cross_vpc", "created_at", "updated_at"} {
		if _, ok := resp.Schema.Attributes[attr]; !ok {
			t.Errorf("expected attribute %q", attr)
		}
	}
}

func TestMemberConfigureNilProviderData(t *testing.T) {
	r := &memberResource{}
	var resp resource.ConfigureResponse
	r.Configure(context.Background(), resource.ConfigureRequest{}, &resp)
	if resp.Diagnostics.HasError() {
		t.Errorf("expected no errors, got %v", resp.Diagnostics.Errors())
	}
}

func TestMemberConfigureWrongType(t *testing.T) {
	r := &memberResource{}
	var resp resource.ConfigureResponse
	r.Configure(context.Background(), resource.ConfigureRequest{ProviderData: "nope"}, &resp)
	if !resp.Diagnostics.HasError() {
		t.Error("expected error for wrong provider data type")
	}
}

func buildMemberState(t *testing.T, model MemberModel) tfsdk.State {
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

func buildMemberPlan(t *testing.T, model MemberModel) tfsdk.Plan {
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

func sampleMemberModel() MemberModel {
	return MemberModel{
		ID:             types.StringValue("mem-1"),
		LoadBalancerID: types.StringValue("lb-1"),
		PoolID:         types.StringValue("pool-1"),
		Address:        types.StringValue("10.0.0.10"),
		ProtocolPort:   types.Int64Value(8080),
		Name:           types.StringValue("node-a"),
		Weight:         types.Int64Value(5),
		SubnetID:       types.StringValue("subnet-1"),
		CrossVPC:       types.BoolValue(false),
		CreatedAt:      types.StringValue("2025-01-01T00:00:00Z"),
		UpdatedAt:      types.StringNull(),
	}
}

func TestMemberCreate(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/v1/tenants/t-1/load-balancers/lb-1/pools/pool-1/members" {
			var body apiCreateMemberRequest
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decode failed: %v", err)
			}
			if body.Address != "10.0.0.10" {
				t.Errorf("expected address 10.0.0.10, got %s", body.Address)
			}
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(apiMember{
				ID: "mem-new", PoolID: "pool-1", Name: "node-a", Address: "10.0.0.10",
				ProtocolPort: 8080, SubnetID: "subnet-1", Weight: 5, CreatedAt: "2025-01-01T00:00:00Z",
			})
			return
		}
		t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key", client.WithHTTPClient(server.Client())) // pragma: allowlist secret
	c.SetTenantIDForTest("t-1")
	r := &memberResource{client: c}

	plan := buildMemberPlan(t, MemberModel{
		LoadBalancerID: types.StringValue("lb-1"),
		PoolID:         types.StringValue("pool-1"),
		Address:        types.StringValue("10.0.0.10"),
		ProtocolPort:   types.Int64Value(8080),
		Name:           types.StringValue("node-a"),
		Weight:         types.Int64Value(5),
		SubnetID:       types.StringValue("subnet-1"),
		CrossVPC:       types.BoolValue(true),
	})
	resp := resource.CreateResponse{State: buildMemberState(t, sampleMemberModel())}
	r.Create(context.Background(), resource.CreateRequest{Plan: plan}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("create failed: %v", resp.Diagnostics.Errors())
	}
	var result MemberModel
	resp.State.Get(context.Background(), &result)
	if result.ID.ValueString() != "mem-new" {
		t.Errorf("expected ID mem-new, got %s", result.ID.ValueString())
	}
	if result.LoadBalancerID.ValueString() != "lb-1" {
		t.Errorf("expected lbID preserved, got %s", result.LoadBalancerID.ValueString())
	}
	if !result.CrossVPC.ValueBool() {
		t.Error("expected cross_vpc preserved as true (write-only)")
	}
}

func TestMemberCreateAPIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]string{"code": "INTERNAL", "message": "boom"})
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key", client.WithHTTPClient(server.Client())) // pragma: allowlist secret
	c.SetTenantIDForTest("t-1")
	r := &memberResource{client: c}

	plan := buildMemberPlan(t, MemberModel{
		LoadBalancerID: types.StringValue("lb-1"),
		PoolID:         types.StringValue("pool-1"),
		Address:        types.StringValue("10.0.0.10"),
		ProtocolPort:   types.Int64Value(8080),
		Name:           types.StringNull(),
		Weight:         types.Int64Null(),
		SubnetID:       types.StringNull(),
		CrossVPC:       types.BoolNull(),
	})
	resp := resource.CreateResponse{State: buildMemberState(t, sampleMemberModel())}
	r.Create(context.Background(), resource.CreateRequest{Plan: plan}, &resp)
	if !resp.Diagnostics.HasError() {
		t.Error("expected error on API failure")
	}
}

func TestMemberRead(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-1/load-balancers/lb-1/pools/pool-1/members/mem-1" {
			_ = json.NewEncoder(w).Encode(apiMember{
				ID: "mem-1", PoolID: "pool-1", Name: "node-a", Address: "10.0.0.10",
				ProtocolPort: 8080, SubnetID: "subnet-1", Weight: 5, CreatedAt: "2025-01-01T00:00:00Z",
			})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key", client.WithHTTPClient(server.Client())) // pragma: allowlist secret
	c.SetTenantIDForTest("t-1")
	r := &memberResource{client: c}

	state := buildMemberState(t, sampleMemberModel())
	resp := resource.ReadResponse{State: state}
	r.Read(context.Background(), resource.ReadRequest{State: state}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("read failed: %v", resp.Diagnostics.Errors())
	}
	var result MemberModel
	resp.State.Get(context.Background(), &result)
	if result.Address.ValueString() != "10.0.0.10" {
		t.Errorf("expected address preserved, got %s", result.Address.ValueString())
	}
}

func TestMemberReadNotFound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]string{"code": "NOT_FOUND", "message": "nf"})
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key", client.WithHTTPClient(server.Client())) // pragma: allowlist secret
	c.SetTenantIDForTest("t-1")
	r := &memberResource{client: c}

	state := buildMemberState(t, sampleMemberModel())
	resp := resource.ReadResponse{State: state}
	r.Read(context.Background(), resource.ReadRequest{State: state}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("expected no error for 404, got %v", resp.Diagnostics.Errors())
	}
	var result MemberModel
	if diags := resp.State.Get(context.Background(), &result); !diags.HasError() {
		if result.ID.ValueString() != "" {
			t.Error("expected state removed after 404")
		}
	}
}

func TestMemberUpdate(t *testing.T) {
	var updated apiUpdateMemberRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut && r.URL.Path == "/v1/tenants/t-1/load-balancers/lb-1/pools/pool-1/members/mem-1" {
			_ = json.NewDecoder(r.Body).Decode(&updated)
			_ = json.NewEncoder(w).Encode(apiMember{
				ID: "mem-1", PoolID: "pool-1", Name: "renamed", Address: "10.0.0.10",
				ProtocolPort: 8080, SubnetID: "subnet-1", Weight: 9, CreatedAt: "2025-01-01T00:00:00Z",
			})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key", client.WithHTTPClient(server.Client())) // pragma: allowlist secret
	c.SetTenantIDForTest("t-1")
	r := &memberResource{client: c}

	state := buildMemberState(t, sampleMemberModel())
	plan := buildMemberPlan(t, MemberModel{
		ID:             types.StringValue("mem-1"),
		LoadBalancerID: types.StringValue("lb-1"),
		PoolID:         types.StringValue("pool-1"),
		Address:        types.StringValue("10.0.0.10"),
		ProtocolPort:   types.Int64Value(8080),
		Name:           types.StringValue("renamed"),
		Weight:         types.Int64Value(9),
		SubnetID:       types.StringValue("subnet-1"),
		CrossVPC:       types.BoolValue(false),
		CreatedAt:      types.StringValue("2025-01-01T00:00:00Z"),
		UpdatedAt:      types.StringNull(),
	})
	resp := resource.UpdateResponse{State: state}
	r.Update(context.Background(), resource.UpdateRequest{Plan: plan, State: state}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("update failed: %v", resp.Diagnostics.Errors())
	}
	if updated.Name == nil || *updated.Name != "renamed" {
		t.Error("expected name in update request body")
	}
	var result MemberModel
	resp.State.Get(context.Background(), &result)
	if result.Weight.ValueInt64() != 9 {
		t.Errorf("expected weight 9, got %d", result.Weight.ValueInt64())
	}
}

func TestMemberDelete(t *testing.T) {
	deleted := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete && r.URL.Path == "/v1/tenants/t-1/load-balancers/lb-1/pools/pool-1/members/mem-1" {
			deleted = true
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key", client.WithHTTPClient(server.Client())) // pragma: allowlist secret
	c.SetTenantIDForTest("t-1")
	r := &memberResource{client: c}

	state := buildMemberState(t, sampleMemberModel())
	resp := resource.DeleteResponse{State: state}
	r.Delete(context.Background(), resource.DeleteRequest{State: state}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("delete failed: %v", resp.Diagnostics.Errors())
	}
	if !deleted {
		t.Error("expected DELETE to be called")
	}
}

func TestMemberDeleteAlreadyGone(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]string{"code": "NOT_FOUND", "message": "nf"})
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key", client.WithHTTPClient(server.Client())) // pragma: allowlist secret
	c.SetTenantIDForTest("t-1")
	r := &memberResource{client: c}

	state := buildMemberState(t, sampleMemberModel())
	resp := resource.DeleteResponse{State: state}
	r.Delete(context.Background(), resource.DeleteRequest{State: state}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("delete of already-gone member should not error, got %v", resp.Diagnostics.Errors())
	}
}
