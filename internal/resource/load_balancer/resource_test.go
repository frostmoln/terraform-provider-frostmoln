package load_balancer

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
)

func TestLoadBalancerModelFromAPI(t *testing.T) {
	lb := &apiLoadBalancer{
		ID:                 "lb-123",
		Name:               "test-lb",
		Description:        "A test LB",
		VPCID:              "vpc-1",
		SubnetID:           "subnet-1",
		VIPAddress:         "10.0.0.5",
		VIPPortID:          "port-1",
		Provider:           "amphora",
		FlavorID:           "flv-1",
		Status:             "active",
		ProvisioningStatus: "ACTIVE",
		OperatingStatus:    "ONLINE",
		Tags:               map[string]string{"env": "test"},
		CreatedAt:          "2025-01-01T00:00:00Z",
		UpdatedAt:          "2025-01-02T00:00:00Z",
	}

	var model LoadBalancerModel
	var diags diag.Diagnostics
	model.fromAPI(context.Background(), lb, &diags)
	if diags.HasError() {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}

	if model.ID.ValueString() != "lb-123" {
		t.Errorf("expected ID lb-123, got %s", model.ID.ValueString())
	}
	if model.Name.ValueString() != "test-lb" {
		t.Errorf("expected Name test-lb, got %s", model.Name.ValueString())
	}
	if model.VPCID.ValueString() != "vpc-1" {
		t.Errorf("expected VPCID vpc-1, got %s", model.VPCID.ValueString())
	}
	if model.Provider.ValueString() != "amphora" {
		t.Errorf("expected Provider amphora, got %s", model.Provider.ValueString())
	}
	if model.VIPAddress.ValueString() != "10.0.0.5" {
		t.Errorf("expected VIPAddress 10.0.0.5, got %s", model.VIPAddress.ValueString())
	}
	if model.VIPPortID.ValueString() != "port-1" {
		t.Errorf("expected VIPPortID port-1, got %s", model.VIPPortID.ValueString())
	}
	if model.OperatingStatus.ValueString() != "ONLINE" {
		t.Errorf("expected OperatingStatus ONLINE, got %s", model.OperatingStatus.ValueString())
	}
	if model.UpdatedAt.ValueString() != "2025-01-02T00:00:00Z" {
		t.Errorf("expected UpdatedAt set, got %s", model.UpdatedAt.ValueString())
	}
}

func TestLoadBalancerModelToCreateRequest(t *testing.T) {
	ctx := context.Background()
	tags, _ := types.MapValueFrom(ctx, types.StringType, map[string]string{"env": "prod"})
	model := LoadBalancerModel{
		Name:        types.StringValue("my-lb"),
		VPCID:       types.StringValue("vpc-9"),
		SubnetID:    types.StringValue("subnet-9"),
		Description: types.StringValue("My LB"),
		Provider:    types.StringValue("ovn"),
		FlavorID:    types.StringNull(),
		VIPAddress:  types.StringNull(),
		Tags:        tags,
	}

	var diags diag.Diagnostics
	req := model.toCreateRequest(ctx, &diags)
	if diags.HasError() {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}
	if req.Name != "my-lb" || req.VPCID != "vpc-9" || req.SubnetID != "subnet-9" {
		t.Errorf("unexpected create request: %+v", req)
	}
	if req.Provider != "ovn" {
		t.Errorf("expected provider ovn, got %s", req.Provider)
	}
	if req.Tags["env"] != "prod" {
		t.Errorf("expected tag env=prod, got %v", req.Tags)
	}
}

func getSchema(t *testing.T) resource.SchemaResponse {
	t.Helper()
	r := NewResource()
	var schemaResp resource.SchemaResponse
	r.Schema(context.Background(), resource.SchemaRequest{}, &schemaResp)
	return schemaResp
}

func newTestResource(c *client.Client) *loadBalancerResource {
	return &loadBalancerResource{
		client:       c,
		pollInterval: 5 * time.Millisecond,
		pollTimeout:  2 * time.Second,
	}
}

func planValue(t *testing.T, schemaResp resource.SchemaResponse, ctx context.Context) tftypes.Value {
	t.Helper()
	tfType := schemaResp.Schema.Type().TerraformType(ctx)
	return tftypes.NewValue(tfType, map[string]tftypes.Value{
		"id":                  tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"name":                tftypes.NewValue(tftypes.String, "test-lb"),
		"vpc_id":              tftypes.NewValue(tftypes.String, "vpc-1"),
		"subnet_id":           tftypes.NewValue(tftypes.String, "subnet-1"),
		"description":         tftypes.NewValue(tftypes.String, nil),
		"vip_address":         tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"provider_type":       tftypes.NewValue(tftypes.String, "amphora"),
		"flavor_id":           tftypes.NewValue(tftypes.String, nil),
		"tags":                tftypes.NewValue(tftypes.Map{ElementType: tftypes.String}, nil),
		"vip_port_id":         tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"status":              tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"provisioning_status": tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"operating_status":    tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"created_at":          tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"updated_at":          tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
	})
}

// TestLoadBalancerCreateAsyncOperationPoll exercises the 202 -> operation-poll
// -> GET load-balancer create path.
func TestLoadBalancerCreateAsyncOperationPoll(t *testing.T) {
	opCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/me":
			json.NewEncoder(w).Encode(map[string]string{"id": "u-1", "tenantId": "t-123"})

		case r.Method == http.MethodPost && r.URL.Path == "/v1/tenants/t-123/load-balancers":
			w.WriteHeader(http.StatusAccepted)
			json.NewEncoder(w).Encode(client.Operation{
				OperationID:  "op-1",
				Status:       "pending",
				ResourceType: "load_balancer",
			})

		case r.Method == http.MethodGet && r.URL.Path == "/v1/operations/op-1":
			opCalls++
			status := "running"
			if opCalls >= 2 {
				status = "completed"
			}
			json.NewEncoder(w).Encode(client.Operation{
				OperationID:  "op-1",
				Status:       status,
				ResourceType: "load_balancer",
				ResourceID:   "lb-async-1",
			})

		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-123/load-balancers/lb-async-1":
			json.NewEncoder(w).Encode(apiLoadBalancer{
				ID:                 "lb-async-1",
				Name:               "test-lb",
				VPCID:              "vpc-1",
				SubnetID:           "subnet-1",
				Provider:           "amphora",
				Status:             "active",
				ProvisioningStatus: "ACTIVE",
				OperatingStatus:    "ONLINE",
				VIPAddress:         "10.0.0.7",
				CreatedAt:          "2025-01-01T00:00:00Z",
			})

		default:
			w.WriteHeader(http.StatusNotFound)
			json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"code": "NOT_FOUND", "message": "nf"}})
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

	createReq := resource.CreateRequest{
		Plan: tfsdk.Plan{Schema: schemaResp.Schema, Raw: planValue(t, schemaResp, ctx)},
	}
	var createResp resource.CreateResponse
	createResp.State = tfsdk.State{Schema: schemaResp.Schema}

	r.Create(ctx, createReq, &createResp)
	if createResp.Diagnostics.HasError() {
		t.Fatalf("Create failed: %v", createResp.Diagnostics.Errors())
	}

	var model LoadBalancerModel
	createResp.State.Get(ctx, &model)
	if model.ID.ValueString() != "lb-async-1" {
		t.Errorf("expected ID lb-async-1, got %s", model.ID.ValueString())
	}
	if model.Status.ValueString() != "active" {
		t.Errorf("expected status active, got %s", model.Status.ValueString())
	}
	if model.VIPAddress.ValueString() != "10.0.0.7" {
		t.Errorf("expected vip 10.0.0.7, got %s", model.VIPAddress.ValueString())
	}
}

// TestLoadBalancerCreateOperationFailed verifies a failed operation surfaces an error.
func TestLoadBalancerCreateOperationFailed(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/me":
			json.NewEncoder(w).Encode(map[string]string{"id": "u-1", "tenantId": "t-123"})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/tenants/t-123/load-balancers":
			w.WriteHeader(http.StatusAccepted)
			json.NewEncoder(w).Encode(client.Operation{OperationID: "op-f", Status: "pending"})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/operations/op-f":
			json.NewEncoder(w).Encode(client.Operation{OperationID: "op-f", Status: "failed", Error: "quota exceeded"})
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

	createReq := resource.CreateRequest{
		Plan: tfsdk.Plan{Schema: schemaResp.Schema, Raw: planValue(t, schemaResp, ctx)},
	}
	var createResp resource.CreateResponse
	createResp.State = tfsdk.State{Schema: schemaResp.Schema}

	r.Create(ctx, createReq, &createResp)
	if !createResp.Diagnostics.HasError() {
		t.Fatal("expected error when operation fails")
	}
}

// TestLoadBalancerCreateOperationNoResourceID verifies the M4 fix: a completed
// operation that returns an empty resource ID surfaces an explicit diagnostic
// instead of fetching with an empty id and orphaning the load balancer.
func TestLoadBalancerCreateOperationNoResourceID(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/me":
			json.NewEncoder(w).Encode(map[string]string{"id": "u-1", "tenantId": "t-123"})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/tenants/t-123/load-balancers":
			w.WriteHeader(http.StatusAccepted)
			json.NewEncoder(w).Encode(client.Operation{OperationID: "op-n", Status: "pending"})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/operations/op-n":
			// Completed but no ResourceID.
			json.NewEncoder(w).Encode(client.Operation{OperationID: "op-n", Status: "completed"})
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

	createReq := resource.CreateRequest{
		Plan: tfsdk.Plan{Schema: schemaResp.Schema, Raw: planValue(t, schemaResp, ctx)},
	}
	var createResp resource.CreateResponse
	createResp.State = tfsdk.State{Schema: schemaResp.Schema}

	r.Create(ctx, createReq, &createResp)
	if !createResp.Diagnostics.HasError() {
		t.Fatal("expected error when completed operation returns no resource ID")
	}
}

// TestLoadBalancerDeleteAsyncOperationPoll exercises the 202 -> operation-poll delete path.
func TestLoadBalancerDeleteAsyncOperationPoll(t *testing.T) {
	opCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/me":
			json.NewEncoder(w).Encode(map[string]string{"id": "u-1", "tenantId": "t-123"})
		case r.Method == http.MethodDelete && r.URL.Path == "/v1/tenants/t-123/load-balancers/lb-del-1":
			w.WriteHeader(http.StatusAccepted)
			json.NewEncoder(w).Encode(client.Operation{OperationID: "op-d", Status: "pending"})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/operations/op-d":
			opCalls++
			status := "running"
			if opCalls >= 2 {
				status = "completed"
			}
			json.NewEncoder(w).Encode(client.Operation{OperationID: "op-d", Status: status, ResourceID: "lb-del-1"})
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

	stateVal := tftypes.NewValue(tfType, map[string]tftypes.Value{
		"id":                  tftypes.NewValue(tftypes.String, "lb-del-1"),
		"name":                tftypes.NewValue(tftypes.String, "del-lb"),
		"vpc_id":              tftypes.NewValue(tftypes.String, "vpc-1"),
		"subnet_id":           tftypes.NewValue(tftypes.String, "subnet-1"),
		"description":         tftypes.NewValue(tftypes.String, nil),
		"vip_address":         tftypes.NewValue(tftypes.String, "10.0.0.7"),
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

	deleteReq := resource.DeleteRequest{State: tfsdk.State{Schema: schemaResp.Schema, Raw: stateVal}}
	var deleteResp resource.DeleteResponse
	deleteResp.State = tfsdk.State{Schema: schemaResp.Schema}

	r.Delete(ctx, deleteReq, &deleteResp)
	if deleteResp.Diagnostics.HasError() {
		t.Fatalf("Delete failed: %v", deleteResp.Diagnostics.Errors())
	}
	if opCalls < 2 {
		t.Errorf("expected operation to be polled until completed, got %d calls", opCalls)
	}
}

func TestLoadBalancerMetadata(t *testing.T) {
	r := NewResource()
	resp := &resource.MetadataResponse{}
	r.Metadata(context.Background(), resource.MetadataRequest{ProviderTypeName: "frostmoln"}, resp)
	if resp.TypeName != "frostmoln_load_balancer" {
		t.Errorf("expected frostmoln_load_balancer, got %s", resp.TypeName)
	}
}
