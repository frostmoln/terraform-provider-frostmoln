package load_balancer

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
)

// --- model unit tests ---

func TestLoadBalancerToUpdateRequest(t *testing.T) {
	ctx := context.Background()
	tags, _ := types.MapValueFrom(ctx, types.StringType, map[string]string{"env": "prod"})
	m := LoadBalancerModel{
		Name:        types.StringValue("renamed"),
		Description: types.StringValue("new desc"),
		Tags:        tags,
	}
	var diags diag.Diagnostics
	req := m.toUpdateRequest(ctx, &diags)
	if diags.HasError() {
		t.Fatalf("unexpected diags: %v", diags)
	}
	if req.Name == nil || *req.Name != "renamed" {
		t.Error("expected name in update")
	}
	if req.Description == nil || *req.Description != "new desc" {
		t.Error("expected description in update")
	}
	if req.Tags["env"] != "prod" {
		t.Errorf("expected tag env=prod, got %v", req.Tags)
	}
}

func TestLoadBalancerToUpdateRequestNullDescription(t *testing.T) {
	ctx := context.Background()
	m := LoadBalancerModel{
		Name:        types.StringValue("n"),
		Description: types.StringNull(),
		Tags:        types.MapNull(types.StringType),
	}
	var diags diag.Diagnostics
	req := m.toUpdateRequest(ctx, &diags)
	// null description maps to an explicit empty string (clear).
	if req.Description == nil || *req.Description != "" {
		t.Errorf("expected empty description for null, got %v", req.Description)
	}
}

func TestLoadBalancerFromAPIInternalDefaults(t *testing.T) {
	lb := &apiLoadBalancer{
		ID:                 "lb-1",
		Name:               "lb",
		VPCID:              "vpc-1",
		SubnetID:           "subnet-1",
		Status:             "active",
		ProvisioningStatus: "ACTIVE",
		OperatingStatus:    "ONLINE",
		CreatedAt:          "2025-01-01T00:00:00Z",
	}
	var m LoadBalancerModel
	var diags diag.Diagnostics
	m.fromAPI(context.Background(), lb, &diags)
	if diags.HasError() {
		t.Fatalf("unexpected diags: %v", diags)
	}
	if m.Scheme.ValueString() != "internal" {
		t.Errorf("expected scheme internal default, got %s", m.Scheme.ValueString())
	}
	if !m.VIPAddress.IsNull() {
		t.Error("expected null vip_address")
	}
	if !m.FloatingIPID.IsNull() {
		t.Error("expected null floating_ip_id")
	}
	if !m.Tags.IsNull() {
		t.Error("expected null tags")
	}
}

// --- resource lifecycle tests ---

func TestLoadBalancerNewResource(t *testing.T) {
	if NewResource() == nil {
		t.Fatal("expected non-nil resource")
	}
}

func TestLoadBalancerSchema(t *testing.T) {
	schemaResp := getSchema(t)
	for _, attr := range []string{"id", "name", "vpc_id", "subnet_id", "description", "vip_address", "scheme", "floating_ip_id", "floating_ip_address", "provider_type", "flavor_id", "tags", "vip_port_id", "status", "provisioning_status", "operating_status", "created_at", "updated_at"} {
		if _, ok := schemaResp.Schema.Attributes[attr]; !ok {
			t.Errorf("expected attribute %q", attr)
		}
	}
}

func TestLoadBalancerConfigureNilProviderData(t *testing.T) {
	r := &loadBalancerResource{}
	var resp resource.ConfigureResponse
	r.Configure(context.Background(), resource.ConfigureRequest{}, &resp)
	if resp.Diagnostics.HasError() {
		t.Errorf("expected no errors, got %v", resp.Diagnostics.Errors())
	}
}

func TestLoadBalancerConfigureWrongType(t *testing.T) {
	r := &loadBalancerResource{}
	var resp resource.ConfigureResponse
	r.Configure(context.Background(), resource.ConfigureRequest{ProviderData: "nope"}, &resp)
	if !resp.Diagnostics.HasError() {
		t.Error("expected error for wrong provider data type")
	}
}

func TestLoadBalancerConfigureValidClient(t *testing.T) {
	r := &loadBalancerResource{}
	c := client.NewClient("http://127.0.0.1:1", "test-key") // pragma: allowlist secret
	var resp resource.ConfigureResponse
	r.Configure(context.Background(), resource.ConfigureRequest{ProviderData: c}, &resp)
	if resp.Diagnostics.HasError() {
		t.Errorf("expected no errors with valid client, got %v", resp.Diagnostics.Errors())
	}
	if r.client != c {
		t.Error("expected client to be set")
	}
}

func TestLoadBalancerImportState(t *testing.T) {
	r := NewResource()
	schemaResp := getSchema(t)
	ctx := context.Background()
	tfType := schemaResp.Schema.Type().TerraformType(ctx)
	emptyState := tftypes.NewValue(tfType, nil)
	resp := &resource.ImportStateResponse{State: tfsdk.State{Schema: schemaResp.Schema, Raw: emptyState}}
	r.(resource.ResourceWithImportState).ImportState(ctx, resource.ImportStateRequest{ID: "lb-imp-1"}, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("import failed: %v", resp.Diagnostics.Errors())
	}
	var id types.String
	resp.State.GetAttribute(ctx, path.Root("id"), &id)
	if id.ValueString() != "lb-imp-1" {
		t.Errorf("expected id lb-imp-1, got %s", id.ValueString())
	}
}

// TestLoadBalancerCreateSync201 exercises the synchronous 201 create path.
func TestLoadBalancerCreateSync201(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/me":
			_ = json.NewEncoder(w).Encode(map[string]string{"id": "u-1", "tenantId": "t-123"})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/tenants/t-123/load-balancers":
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(apiLoadBalancer{
				ID: "lb-sync-1", Name: "test-lb", VPCID: "vpc-1", SubnetID: "subnet-1",
				Provider: "amphora", Status: "active", ProvisioningStatus: "ACTIVE",
				OperatingStatus: "ONLINE", VIPAddress: "10.0.0.9", CreatedAt: "2025-01-01T00:00:00Z",
			})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-123/load-balancers/lb-sync-1":
			_ = json.NewEncoder(w).Encode(apiLoadBalancer{
				ID: "lb-sync-1", Name: "test-lb", VPCID: "vpc-1", SubnetID: "subnet-1",
				Provider: "amphora", Status: "active", ProvisioningStatus: "ACTIVE",
				OperatingStatus: "ONLINE", VIPAddress: "10.0.0.9", CreatedAt: "2025-01-01T00:00:00Z",
			})
		default:
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"code": "NOT_FOUND", "message": "nf"}})
		}
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key") // pragma: allowlist secret
	if err := c.Configure(context.Background()); err != nil {
		t.Fatalf("configure failed: %v", err)
	}
	r := newTestResource(c)
	schemaResp := getSchema(t)
	ctx := context.Background()

	createReq := resource.CreateRequest{Plan: tfsdk.Plan{Schema: schemaResp.Schema, Raw: planValue(t, schemaResp, ctx)}}
	var createResp resource.CreateResponse
	createResp.State = tfsdk.State{Schema: schemaResp.Schema}
	r.Create(ctx, createReq, &createResp)
	if createResp.Diagnostics.HasError() {
		t.Fatalf("create failed: %v", createResp.Diagnostics.Errors())
	}
	var model LoadBalancerModel
	createResp.State.Get(ctx, &model)
	if model.ID.ValueString() != "lb-sync-1" {
		t.Errorf("expected ID lb-sync-1, got %s", model.ID.ValueString())
	}
}

func TestLoadBalancerCreateAPIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/me":
			_ = json.NewEncoder(w).Encode(map[string]string{"id": "u-1", "tenantId": "t-123"})
		default:
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"code": "INTERNAL", "message": "boom"}})
		}
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key") // pragma: allowlist secret
	if err := c.Configure(context.Background()); err != nil {
		t.Fatalf("configure failed: %v", err)
	}
	r := newTestResource(c)
	schemaResp := getSchema(t)
	ctx := context.Background()

	createReq := resource.CreateRequest{Plan: tfsdk.Plan{Schema: schemaResp.Schema, Raw: planValue(t, schemaResp, ctx)}}
	var createResp resource.CreateResponse
	createResp.State = tfsdk.State{Schema: schemaResp.Schema}
	r.Create(ctx, createReq, &createResp)
	if !createResp.Diagnostics.HasError() {
		t.Error("expected error on API failure")
	}
}

func lbStateValue(t *testing.T, schemaResp resource.SchemaResponse, ctx context.Context, id string) tftypes.Value {
	t.Helper()
	tfType := schemaResp.Schema.Type().TerraformType(ctx)
	return tftypes.NewValue(tfType, map[string]tftypes.Value{
		"id":                  tftypes.NewValue(tftypes.String, id),
		"name":                tftypes.NewValue(tftypes.String, "lb"),
		"vpc_id":              tftypes.NewValue(tftypes.String, "vpc-1"),
		"subnet_id":           tftypes.NewValue(tftypes.String, "subnet-1"),
		"description":         tftypes.NewValue(tftypes.String, nil),
		"vip_address":         tftypes.NewValue(tftypes.String, "10.0.0.7"),
		"scheme":              tftypes.NewValue(tftypes.String, "internal"),
		"floating_ip_id":      tftypes.NewValue(tftypes.String, nil),
		"floating_ip_address": tftypes.NewValue(tftypes.String, nil),
		"provider_type":       tftypes.NewValue(tftypes.String, "amphora"),
		"flavor_id":           tftypes.NewValue(tftypes.String, nil),
		"tags":                tftypes.NewValue(tftypes.Map{ElementType: tftypes.String}, nil),
		"vip_port_id":         tftypes.NewValue(tftypes.String, "port-1"),
		"status":              tftypes.NewValue(tftypes.String, "active"),
		"provisioning_status": tftypes.NewValue(tftypes.String, "ACTIVE"),
		"operating_status":    tftypes.NewValue(tftypes.String, "ONLINE"),
		"created_at":          tftypes.NewValue(tftypes.String, "2025-01-01T00:00:00Z"),
		"updated_at":          tftypes.NewValue(tftypes.String, nil),
	})
}

func TestLoadBalancerRead(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/me":
			_ = json.NewEncoder(w).Encode(map[string]string{"id": "u-1", "tenantId": "t-123"})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-123/load-balancers/lb-r-1":
			_ = json.NewEncoder(w).Encode(apiLoadBalancer{
				ID: "lb-r-1", Name: "lb", VPCID: "vpc-1", SubnetID: "subnet-1", Provider: "amphora",
				Status: "active", ProvisioningStatus: "ACTIVE", OperatingStatus: "DEGRADED",
				VIPAddress: "10.0.0.7", CreatedAt: "2025-01-01T00:00:00Z",
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key") // pragma: allowlist secret
	if err := c.Configure(context.Background()); err != nil {
		t.Fatalf("configure failed: %v", err)
	}
	r := newTestResource(c)
	schemaResp := getSchema(t)
	ctx := context.Background()

	readReq := resource.ReadRequest{State: tfsdk.State{Schema: schemaResp.Schema, Raw: lbStateValue(t, schemaResp, ctx, "lb-r-1")}}
	var readResp resource.ReadResponse
	readResp.State = tfsdk.State{Schema: schemaResp.Schema}
	r.Read(ctx, readReq, &readResp)
	if readResp.Diagnostics.HasError() {
		t.Fatalf("read failed: %v", readResp.Diagnostics.Errors())
	}
	var model LoadBalancerModel
	readResp.State.Get(ctx, &model)
	if model.OperatingStatus.ValueString() != "DEGRADED" {
		t.Errorf("expected operating_status DEGRADED, got %s", model.OperatingStatus.ValueString())
	}
}

func TestLoadBalancerReadNotFound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/me":
			_ = json.NewEncoder(w).Encode(map[string]string{"id": "u-1", "tenantId": "t-123"})
		default:
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"code": "NOT_FOUND", "message": "nf"}})
		}
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key") // pragma: allowlist secret
	if err := c.Configure(context.Background()); err != nil {
		t.Fatalf("configure failed: %v", err)
	}
	r := newTestResource(c)
	schemaResp := getSchema(t)
	ctx := context.Background()

	readReq := resource.ReadRequest{State: tfsdk.State{Schema: schemaResp.Schema, Raw: lbStateValue(t, schemaResp, ctx, "lb-gone")}}
	var readResp resource.ReadResponse
	readResp.State = tfsdk.State{Schema: schemaResp.Schema}
	r.Read(ctx, readReq, &readResp)
	if readResp.Diagnostics.HasError() {
		t.Fatalf("expected no error for 404, got %v", readResp.Diagnostics.Errors())
	}
	var model LoadBalancerModel
	if diags := readResp.State.Get(ctx, &model); !diags.HasError() {
		if model.ID.ValueString() != "" {
			t.Error("expected state removed after 404")
		}
	}
}

func TestLoadBalancerUpdate(t *testing.T) {
	var updated apiUpdateLoadBalancerRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/me":
			_ = json.NewEncoder(w).Encode(map[string]string{"id": "u-1", "tenantId": "t-123"})
		case r.Method == http.MethodPut && r.URL.Path == "/v1/tenants/t-123/load-balancers/lb-u-1":
			_ = json.NewDecoder(r.Body).Decode(&updated)
			_ = json.NewEncoder(w).Encode(apiLoadBalancer{
				ID: "lb-u-1", Name: "renamed", VPCID: "vpc-1", SubnetID: "subnet-1", Provider: "amphora",
				Status: "active", ProvisioningStatus: "ACTIVE", OperatingStatus: "ONLINE",
				VIPAddress: "10.0.0.7", CreatedAt: "2025-01-01T00:00:00Z", UpdatedAt: "2025-02-01T00:00:00Z",
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key") // pragma: allowlist secret
	if err := c.Configure(context.Background()); err != nil {
		t.Fatalf("configure failed: %v", err)
	}
	r := newTestResource(c)
	schemaResp := getSchema(t)
	ctx := context.Background()
	tfType := schemaResp.Schema.Type().TerraformType(ctx)

	planVal := tftypes.NewValue(tfType, map[string]tftypes.Value{
		"id":                  tftypes.NewValue(tftypes.String, "lb-u-1"),
		"name":                tftypes.NewValue(tftypes.String, "renamed"),
		"vpc_id":              tftypes.NewValue(tftypes.String, "vpc-1"),
		"subnet_id":           tftypes.NewValue(tftypes.String, "subnet-1"),
		"description":         tftypes.NewValue(tftypes.String, nil),
		"vip_address":         tftypes.NewValue(tftypes.String, "10.0.0.7"),
		"scheme":              tftypes.NewValue(tftypes.String, "internal"),
		"floating_ip_id":      tftypes.NewValue(tftypes.String, nil),
		"floating_ip_address": tftypes.NewValue(tftypes.String, nil),
		"provider_type":       tftypes.NewValue(tftypes.String, "amphora"),
		"flavor_id":           tftypes.NewValue(tftypes.String, nil),
		"tags":                tftypes.NewValue(tftypes.Map{ElementType: tftypes.String}, nil),
		"vip_port_id":         tftypes.NewValue(tftypes.String, "port-1"),
		"status":              tftypes.NewValue(tftypes.String, "active"),
		"provisioning_status": tftypes.NewValue(tftypes.String, "ACTIVE"),
		"operating_status":    tftypes.NewValue(tftypes.String, "ONLINE"),
		"created_at":          tftypes.NewValue(tftypes.String, "2025-01-01T00:00:00Z"),
		"updated_at":          tftypes.NewValue(tftypes.String, nil),
	})

	updateReq := resource.UpdateRequest{
		Plan:  tfsdk.Plan{Schema: schemaResp.Schema, Raw: planVal},
		State: tfsdk.State{Schema: schemaResp.Schema, Raw: lbStateValue(t, schemaResp, ctx, "lb-u-1")},
	}
	var updateResp resource.UpdateResponse
	updateResp.State = tfsdk.State{Schema: schemaResp.Schema}
	r.Update(ctx, updateReq, &updateResp)
	if updateResp.Diagnostics.HasError() {
		t.Fatalf("update failed: %v", updateResp.Diagnostics.Errors())
	}
	if updated.Name == nil || *updated.Name != "renamed" {
		t.Error("expected name in update body")
	}
	var model LoadBalancerModel
	updateResp.State.Get(ctx, &model)
	if model.UpdatedAt.ValueString() != "2025-02-01T00:00:00Z" {
		t.Errorf("expected updated_at set, got %s", model.UpdatedAt.ValueString())
	}
}

func TestLoadBalancerDeleteSync(t *testing.T) {
	deleted := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/me":
			_ = json.NewEncoder(w).Encode(map[string]string{"id": "u-1", "tenantId": "t-123"})
		case r.Method == http.MethodDelete && r.URL.Path == "/v1/tenants/t-123/load-balancers/lb-d-1":
			deleted = true
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key") // pragma: allowlist secret
	if err := c.Configure(context.Background()); err != nil {
		t.Fatalf("configure failed: %v", err)
	}
	r := newTestResource(c)
	schemaResp := getSchema(t)
	ctx := context.Background()

	deleteReq := resource.DeleteRequest{State: tfsdk.State{Schema: schemaResp.Schema, Raw: lbStateValue(t, schemaResp, ctx, "lb-d-1")}}
	var deleteResp resource.DeleteResponse
	deleteResp.State = tfsdk.State{Schema: schemaResp.Schema}
	r.Delete(ctx, deleteReq, &deleteResp)
	if deleteResp.Diagnostics.HasError() {
		t.Fatalf("delete failed: %v", deleteResp.Diagnostics.Errors())
	}
	if !deleted {
		t.Error("expected DELETE to be called")
	}
}

func TestLoadBalancerDeleteAlreadyGone(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/me":
			_ = json.NewEncoder(w).Encode(map[string]string{"id": "u-1", "tenantId": "t-123"})
		default:
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"code": "NOT_FOUND", "message": "nf"}})
		}
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key") // pragma: allowlist secret
	if err := c.Configure(context.Background()); err != nil {
		t.Fatalf("configure failed: %v", err)
	}
	r := newTestResource(c)
	schemaResp := getSchema(t)
	ctx := context.Background()

	deleteReq := resource.DeleteRequest{State: tfsdk.State{Schema: schemaResp.Schema, Raw: lbStateValue(t, schemaResp, ctx, "lb-gone")}}
	var deleteResp resource.DeleteResponse
	deleteResp.State = tfsdk.State{Schema: schemaResp.Schema}
	r.Delete(ctx, deleteReq, &deleteResp)
	if deleteResp.Diagnostics.HasError() {
		t.Fatalf("delete of already-gone lb should not error, got %v", deleteResp.Diagnostics.Errors())
	}
}
