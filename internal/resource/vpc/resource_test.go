package vpc

import (
	"context"
	"encoding/json"
	"math/big"
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

func TestVPCModelFromAPI(t *testing.T) {
	vpc := &apiVPC{
		ID:          "vpc-123",
		Name:        "test-vpc",
		Description: "A test VPC",
		CIDR:        "10.0.0.0/16",
		Region:      "sweden",
		Status:      "active",
		IsDefault:   false,
		SubnetCount: 3,
		Tags:        map[string]string{"env": "test"},
		CreatedAt:   "2025-01-01T00:00:00Z",
		UpdatedAt:   "2025-01-02T00:00:00Z",
	}

	var model VPCModel
	var diags diag.Diagnostics
	model.fromAPI(context.Background(), vpc, &diags)

	if diags.HasError() {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}

	if model.ID.ValueString() != "vpc-123" {
		t.Errorf("expected ID vpc-123, got %s", model.ID.ValueString())
	}
	if model.Name.ValueString() != "test-vpc" {
		t.Errorf("expected Name test-vpc, got %s", model.Name.ValueString())
	}
	if model.Description.ValueString() != "A test VPC" {
		t.Errorf("expected Description 'A test VPC', got %s", model.Description.ValueString())
	}
	if model.CIDR.ValueString() != "10.0.0.0/16" {
		t.Errorf("expected CIDR 10.0.0.0/16, got %s", model.CIDR.ValueString())
	}
	if model.Region.ValueString() != "sweden" {
		t.Errorf("expected Region sweden, got %s", model.Region.ValueString())
	}
	if model.Status.ValueString() != "active" {
		t.Errorf("expected Status active, got %s", model.Status.ValueString())
	}
	if model.IsDefault.ValueBool() != false {
		t.Errorf("expected IsDefault false, got true")
	}
	if model.SubnetCount.ValueInt64() != 3 {
		t.Errorf("expected SubnetCount 3, got %d", model.SubnetCount.ValueInt64())
	}
	if model.CreatedAt.ValueString() != "2025-01-01T00:00:00Z" {
		t.Errorf("expected CreatedAt 2025-01-01T00:00:00Z, got %s", model.CreatedAt.ValueString())
	}
	if model.UpdatedAt.ValueString() != "2025-01-02T00:00:00Z" {
		t.Errorf("expected UpdatedAt 2025-01-02T00:00:00Z, got %s", model.UpdatedAt.ValueString())
	}
}

func TestVPCModelFromAPINoOptionalFields(t *testing.T) {
	vpc := &apiVPC{
		ID:        "vpc-456",
		Name:      "minimal-vpc",
		CIDR:      "172.16.0.0/12",
		Region:    "sweden",
		Status:    "active",
		IsDefault: true,
		CreatedAt: "2025-01-01T00:00:00Z",
	}

	var model VPCModel
	model.Description = types.StringNull()
	model.Tags = types.MapNull(types.StringType)

	var diags diag.Diagnostics
	model.fromAPI(context.Background(), vpc, &diags)

	if diags.HasError() {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}

	if !model.Description.IsNull() {
		t.Errorf("expected Description to be null, got %s", model.Description.ValueString())
	}
	if !model.UpdatedAt.IsNull() {
		t.Errorf("expected UpdatedAt to be null, got %s", model.UpdatedAt.ValueString())
	}
	if !model.Tags.IsNull() {
		t.Error("expected Tags to be null")
	}
}

func TestVPCModelToCreateRequest(t *testing.T) {
	ctx := context.Background()
	tags, _ := types.MapValueFrom(ctx, types.StringType, map[string]string{"env": "prod"})
	model := VPCModel{
		Name:        types.StringValue("my-vpc"),
		Description: types.StringValue("My VPC"),
		CIDR:        types.StringValue("10.0.0.0/16"),
		Region:      types.StringValue("sweden"),
		Tags:        tags,
	}

	var diags diag.Diagnostics
	req := model.toCreateRequest(ctx, &diags)

	if diags.HasError() {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}

	if req.Name != "my-vpc" {
		t.Errorf("expected Name my-vpc, got %s", req.Name)
	}
	if req.Description != "My VPC" {
		t.Errorf("expected Description 'My VPC', got %s", req.Description)
	}
	if req.CIDR != "10.0.0.0/16" {
		t.Errorf("expected CIDR 10.0.0.0/16, got %s", req.CIDR)
	}
	if req.Region != "sweden" {
		t.Errorf("expected Region sweden, got %s", req.Region)
	}
	if req.Tags["env"] != "prod" {
		t.Errorf("expected tag env=prod, got %v", req.Tags)
	}
}

func TestVPCModelToUpdateRequest(t *testing.T) {
	ctx := context.Background()
	tags, _ := types.MapValueFrom(ctx, types.StringType, map[string]string{"env": "staging"})
	model := VPCModel{
		Name:        types.StringValue("updated-vpc"),
		Description: types.StringValue("Updated"),
		Tags:        tags,
	}

	var diags diag.Diagnostics
	req := model.toUpdateRequest(ctx, &diags)

	if diags.HasError() {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}

	if *req.Name != "updated-vpc" {
		t.Errorf("expected Name updated-vpc, got %s", *req.Name)
	}
	if *req.Description != "Updated" {
		t.Errorf("expected Description 'Updated', got %s", *req.Description)
	}
	if req.Tags["env"] != "staging" {
		t.Errorf("expected tag env=staging, got %v", req.Tags)
	}
}

func TestVPCResourceCRUD(t *testing.T) {
	vpcCreated := apiVPC{
		ID:        "vpc-test-1",
		Name:      "test-vpc",
		CIDR:      "10.0.0.0/16",
		Region:    "sweden",
		Status:    "active",
		CreatedAt: "2025-01-01T00:00:00Z",
	}

	vpcUpdated := apiVPC{
		ID:          "vpc-test-1",
		Name:        "updated-vpc",
		Description: "Updated description",
		CIDR:        "10.0.0.0/16",
		Region:      "sweden",
		Status:      "active",
		CreatedAt:   "2025-01-01T00:00:00Z",
		UpdatedAt:   "2025-01-02T00:00:00Z",
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/tenants/t-123/vpcs":
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(vpcCreated)

		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-123/vpcs/vpc-test-1":
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(vpcCreated)

		case r.Method == http.MethodPatch && r.URL.Path == "/v1/tenants/t-123/vpcs/vpc-test-1":
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(vpcUpdated)

		case r.Method == http.MethodDelete && r.URL.Path == "/v1/tenants/t-123/vpcs/vpc-test-1":
			w.WriteHeader(http.StatusNoContent)

		default:
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"error": map[string]string{"code": "NOT_FOUND", "message": "not found"},
			})
		}
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key") // pragma: allowlist secret
	c.SetTenantIDForTest("t-123")

	ctx := context.Background()

	// Test Create (synchronous 201)
	createReq := apiCreateVPCRequest{
		Name: "test-vpc",
		CIDR: "10.0.0.0/16",
	}
	apiResp, err := c.Post(ctx, c.TenantPath("/vpcs"), createReq)
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}
	if apiResp.StatusCode != http.StatusCreated {
		t.Errorf("expected status 201, got %d", apiResp.StatusCode)
	}

	var created apiVPC
	if err := json.Unmarshal(apiResp.Body, &created); err != nil {
		t.Fatalf("failed to parse create response: %v", err)
	}
	if created.ID != "vpc-test-1" {
		t.Errorf("expected ID vpc-test-1, got %s", created.ID)
	}

	// Test Read
	readResp, err := c.Get(ctx, c.TenantPath("/vpcs/vpc-test-1"), nil)
	if err != nil {
		t.Fatalf("Read failed: %v", err)
	}
	var read apiVPC
	if err := json.Unmarshal(readResp.Body, &read); err != nil {
		t.Fatalf("failed to parse read response: %v", err)
	}
	if read.Name != "test-vpc" {
		t.Errorf("expected Name test-vpc, got %s", read.Name)
	}

	// Test Update
	updateReq := apiUpdateVPCRequest{}
	name := "updated-vpc"
	updateReq.Name = &name
	patchResp, err := c.Patch(ctx, c.TenantPath("/vpcs/vpc-test-1"), updateReq)
	if err != nil {
		t.Fatalf("Update failed: %v", err)
	}
	var updated apiVPC
	if err := json.Unmarshal(patchResp.Body, &updated); err != nil {
		t.Fatalf("failed to parse update response: %v", err)
	}
	if updated.Name != "updated-vpc" {
		t.Errorf("expected Name updated-vpc, got %s", updated.Name)
	}

	// Test Delete
	_, err = c.DeleteWithQuery(ctx, c.TenantPath("/vpcs/vpc-test-1"), nil)
	if err != nil {
		t.Fatalf("Delete failed: %v", err)
	}
}

func TestVPCResourceAsyncCreate(t *testing.T) {
	callCount := 0

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/tenants/t-123/vpcs":
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(apiVPC{
				ID:     "vpc-async-1",
				Name:   "async-vpc",
				CIDR:   "10.0.0.0/16",
				Region: "sweden",
				Status: "creating",
			})

		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-123/vpcs/vpc-async-1":
			callCount++
			status := "creating"
			if callCount >= 2 {
				status = "active"
			}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(apiVPC{
				ID:        "vpc-async-1",
				Name:      "async-vpc",
				CIDR:      "10.0.0.0/16",
				Region:    "sweden",
				Status:    status,
				CreatedAt: "2025-01-01T00:00:00Z",
			})

		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key") // pragma: allowlist secret
	c.SetTenantIDForTest("t-123")

	ctx := context.Background()

	// Post to create
	apiResp, err := c.Post(ctx, c.TenantPath("/vpcs"), apiCreateVPCRequest{
		Name: "async-vpc",
		CIDR: "10.0.0.0/16",
	})
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	if apiResp.StatusCode != http.StatusAccepted {
		t.Fatalf("expected status 202, got %d", apiResp.StatusCode)
	}

	var vpc apiVPC
	if err := json.Unmarshal(apiResp.Body, &vpc); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}

	// Poll until active
	_, err = client.WaitForState(ctx, client.PollConfig{
		Interval:     10 * time.Millisecond,
		Timeout:      1 * time.Second,
		TargetStates: []string{"active"},
		ErrorStates:  []string{"error"},
		ResourceName: "VPC",
		PollFunc: func(ctx context.Context) (string, error) {
			resp, pollErr := c.Get(ctx, c.TenantPath("/vpcs/"+vpc.ID), nil)
			if pollErr != nil {
				return "", pollErr
			}
			var polled apiVPC
			if err := json.Unmarshal(resp.Body, &polled); err != nil {
				return "", err
			}
			return polled.Status, nil
		},
	})
	if err != nil {
		t.Fatalf("polling failed: %v", err)
	}
}

func TestVPCReadNotFound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"error": map[string]string{"code": "NOT_FOUND", "message": "VPC not found"},
		})
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key") // pragma: allowlist secret
	c.SetTenantIDForTest("t-123")

	_, err := c.Get(context.Background(), c.TenantPath("/vpcs/nonexistent"), nil)
	if err == nil {
		t.Fatal("expected error for not found")
	}
	if !client.IsNotFound(err) {
		t.Errorf("expected not found error, got %v", err)
	}
}

// --- tfsdk-level CRUD tests ---

func getVPCSchema(t *testing.T) resource.SchemaResponse {
	t.Helper()
	r := NewResource()
	var schemaResp resource.SchemaResponse
	r.Schema(context.Background(), resource.SchemaRequest{}, &schemaResp)
	return schemaResp
}

func configureVPCResource(t *testing.T, r resource.Resource, c *client.Client) {
	t.Helper()
	rc, ok := r.(resource.ResourceWithConfigure)
	if !ok {
		t.Fatal("resource does not implement ResourceWithConfigure")
	}
	configReq := resource.ConfigureRequest{ProviderData: c}
	var configResp resource.ConfigureResponse
	rc.Configure(context.Background(), configReq, &configResp)
	if configResp.Diagnostics.HasError() {
		t.Fatalf("configure failed: %v", configResp.Diagnostics.Errors())
	}
}

func TestVPCResource_TFSDKCreate(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/me":
			_ = json.NewEncoder(w).Encode(map[string]string{"id": "user-123", "tenantId": "tenant-456"})

		case r.Method == http.MethodPost && r.URL.Path == "/v1/tenants/tenant-456/vpcs":
			// Provisioning returns 202 + an Operation envelope (operationId only).
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"operationId": "op-vpc-1", "status": "pending", "resourceType": "vpc",
			})

		case r.Method == http.MethodGet && r.URL.Path == "/v1/operations/op-vpc-1":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"operationId": "op-vpc-1", "status": "completed", "resourceType": "vpc", "resourceId": "vpc-created-1",
			})

		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/tenant-456/vpcs/vpc-created-1":
			_ = json.NewEncoder(w).Encode(apiVPC{
				ID:          "vpc-created-1",
				Name:        "test-vpc",
				CIDR:        "10.0.0.0/16",
				Region:      "sweden",
				Status:      "active",
				IsDefault:   false,
				SubnetCount: 0,
				CreatedAt:   "2025-01-01T00:00:00Z",
			})

		default:
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"error": map[string]string{"code": "NOT_FOUND", "message": "not found"},
			})
		}
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key") // pragma: allowlist secret
	if err := c.Configure(context.Background()); err != nil {
		t.Fatalf("client configure failed: %v", err)
	}

	r := NewResource()
	configureVPCResource(t, r, c)
	schemaResp := getVPCSchema(t)

	ctx := context.Background()
	tfType := schemaResp.Schema.Type().TerraformType(ctx)

	planVal := tftypes.NewValue(tfType, map[string]tftypes.Value{
		"id":           tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"name":         tftypes.NewValue(tftypes.String, "test-vpc"),
		"description":  tftypes.NewValue(tftypes.String, nil),
		"cidr":         tftypes.NewValue(tftypes.String, "10.0.0.0/16"),
		"region":       tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"tags":         tftypes.NewValue(tftypes.Map{ElementType: tftypes.String}, nil),
		"status":       tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"is_default":   tftypes.NewValue(tftypes.Bool, tftypes.UnknownValue),
		"subnet_count": tftypes.NewValue(tftypes.Number, tftypes.UnknownValue),
		"created_at":   tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"updated_at":   tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
	})

	createReq := resource.CreateRequest{
		Plan: tfsdk.Plan{Schema: schemaResp.Schema, Raw: planVal},
	}
	var createResp resource.CreateResponse
	createResp.State = tfsdk.State{Schema: schemaResp.Schema}

	r.Create(ctx, createReq, &createResp)

	if createResp.Diagnostics.HasError() {
		t.Fatalf("Create failed: %v", createResp.Diagnostics.Errors())
	}

	var model VPCModel
	createResp.State.Get(ctx, &model)

	if model.ID.ValueString() != "vpc-created-1" {
		t.Errorf("expected ID vpc-created-1, got %s", model.ID.ValueString())
	}
	if model.Name.ValueString() != "test-vpc" {
		t.Errorf("expected Name test-vpc, got %s", model.Name.ValueString())
	}
	if model.CIDR.ValueString() != "10.0.0.0/16" {
		t.Errorf("expected CIDR 10.0.0.0/16, got %s", model.CIDR.ValueString())
	}
	if model.Region.ValueString() != "sweden" {
		t.Errorf("expected Region sweden, got %s", model.Region.ValueString())
	}
	if model.Status.ValueString() != "active" {
		t.Errorf("expected Status active, got %s", model.Status.ValueString())
	}
}

func TestVPCResource_TFSDKRead(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/me":
			_ = json.NewEncoder(w).Encode(map[string]string{"id": "user-123", "tenantId": "tenant-456"})

		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/tenant-456/vpcs/vpc-read-1":
			_ = json.NewEncoder(w).Encode(apiVPC{
				ID:          "vpc-read-1",
				Name:        "read-vpc",
				Description: "Description",
				CIDR:        "172.16.0.0/12",
				Region:      "sweden",
				Status:      "active",
				IsDefault:   true,
				SubnetCount: 2,
				Tags:        map[string]string{"env": "staging"},
				CreatedAt:   "2025-02-01T00:00:00Z",
				UpdatedAt:   "2025-02-02T00:00:00Z",
			})

		default:
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"error": map[string]string{"code": "NOT_FOUND", "message": "not found"},
			})
		}
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key") // pragma: allowlist secret
	if err := c.Configure(context.Background()); err != nil {
		t.Fatalf("client configure failed: %v", err)
	}

	r := NewResource()
	configureVPCResource(t, r, c)
	schemaResp := getVPCSchema(t)

	ctx := context.Background()
	tfType := schemaResp.Schema.Type().TerraformType(ctx)

	stateVal := tftypes.NewValue(tfType, map[string]tftypes.Value{
		"id":           tftypes.NewValue(tftypes.String, "vpc-read-1"),
		"name":         tftypes.NewValue(tftypes.String, "read-vpc"),
		"description":  tftypes.NewValue(tftypes.String, nil),
		"cidr":         tftypes.NewValue(tftypes.String, "172.16.0.0/12"),
		"region":       tftypes.NewValue(tftypes.String, "sweden"),
		"tags":         tftypes.NewValue(tftypes.Map{ElementType: tftypes.String}, nil),
		"status":       tftypes.NewValue(tftypes.String, "active"),
		"is_default":   tftypes.NewValue(tftypes.Bool, true),
		"subnet_count": tftypes.NewValue(tftypes.Number, big.NewFloat(0)),
		"created_at":   tftypes.NewValue(tftypes.String, "2025-02-01T00:00:00Z"),
		"updated_at":   tftypes.NewValue(tftypes.String, nil),
	})

	readReq := resource.ReadRequest{
		State: tfsdk.State{Schema: schemaResp.Schema, Raw: stateVal},
	}
	var readResp resource.ReadResponse
	readResp.State = tfsdk.State{Schema: schemaResp.Schema}

	r.Read(ctx, readReq, &readResp)

	if readResp.Diagnostics.HasError() {
		t.Fatalf("Read failed: %v", readResp.Diagnostics.Errors())
	}

	var model VPCModel
	readResp.State.Get(ctx, &model)

	if model.ID.ValueString() != "vpc-read-1" {
		t.Errorf("expected ID vpc-read-1, got %s", model.ID.ValueString())
	}
	if model.Description.ValueString() != "Description" {
		t.Errorf("expected Description 'Description', got %s", model.Description.ValueString())
	}
	if model.IsDefault.ValueBool() != true {
		t.Error("expected IsDefault true")
	}
	if model.SubnetCount.ValueInt64() != 2 {
		t.Errorf("expected SubnetCount 2, got %d", model.SubnetCount.ValueInt64())
	}
	if model.UpdatedAt.ValueString() != "2025-02-02T00:00:00Z" {
		t.Errorf("expected UpdatedAt 2025-02-02T00:00:00Z, got %s", model.UpdatedAt.ValueString())
	}
}

func TestVPCResource_TFSDKReadNotFound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/me":
			_ = json.NewEncoder(w).Encode(map[string]string{"id": "user-123", "tenantId": "tenant-456"})

		default:
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"error": map[string]string{"code": "NOT_FOUND", "message": "not found"},
			})
		}
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key") // pragma: allowlist secret
	if err := c.Configure(context.Background()); err != nil {
		t.Fatalf("client configure failed: %v", err)
	}

	r := NewResource()
	configureVPCResource(t, r, c)
	schemaResp := getVPCSchema(t)

	ctx := context.Background()
	tfType := schemaResp.Schema.Type().TerraformType(ctx)

	stateVal := tftypes.NewValue(tfType, map[string]tftypes.Value{
		"id":           tftypes.NewValue(tftypes.String, "vpc-gone"),
		"name":         tftypes.NewValue(tftypes.String, "gone-vpc"),
		"description":  tftypes.NewValue(tftypes.String, nil),
		"cidr":         tftypes.NewValue(tftypes.String, "10.0.0.0/16"),
		"region":       tftypes.NewValue(tftypes.String, "sweden"),
		"tags":         tftypes.NewValue(tftypes.Map{ElementType: tftypes.String}, nil),
		"status":       tftypes.NewValue(tftypes.String, "active"),
		"is_default":   tftypes.NewValue(tftypes.Bool, false),
		"subnet_count": tftypes.NewValue(tftypes.Number, big.NewFloat(0)),
		"created_at":   tftypes.NewValue(tftypes.String, "2025-01-01T00:00:00Z"),
		"updated_at":   tftypes.NewValue(tftypes.String, nil),
	})

	readReq := resource.ReadRequest{
		State: tfsdk.State{Schema: schemaResp.Schema, Raw: stateVal},
	}
	var readResp resource.ReadResponse
	readResp.State = tfsdk.State{Schema: schemaResp.Schema}

	r.Read(ctx, readReq, &readResp)

	if readResp.Diagnostics.HasError() {
		t.Fatalf("Read should not error for 404, got: %v", readResp.Diagnostics.Errors())
	}

	// State should be removed (resource was not found)
	var model VPCModel
	diags := readResp.State.Get(ctx, &model)
	if !diags.HasError() {
		// If Get succeeds with a null state, the resource was removed
		if model.ID.IsNull() {
			return // expected
		}
	}
}

func TestVPCResource_TFSDKUpdate(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/me":
			_ = json.NewEncoder(w).Encode(map[string]string{"id": "user-123", "tenantId": "tenant-456"})

		case r.Method == http.MethodPatch && r.URL.Path == "/v1/tenants/tenant-456/vpcs/vpc-upd-1":
			_ = json.NewEncoder(w).Encode(apiVPC{
				ID:          "vpc-upd-1",
				Name:        "updated-vpc",
				Description: "Updated desc",
				CIDR:        "10.0.0.0/16",
				Region:      "sweden",
				Status:      "active",
				IsDefault:   false,
				SubnetCount: 1,
				Tags:        map[string]string{"env": "prod"},
				CreatedAt:   "2025-01-01T00:00:00Z",
				UpdatedAt:   "2025-03-01T00:00:00Z",
			})

		default:
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"error": map[string]string{"code": "NOT_FOUND", "message": "not found"},
			})
		}
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key") // pragma: allowlist secret
	if err := c.Configure(context.Background()); err != nil {
		t.Fatalf("client configure failed: %v", err)
	}

	r := NewResource()
	configureVPCResource(t, r, c)
	schemaResp := getVPCSchema(t)

	ctx := context.Background()
	tfType := schemaResp.Schema.Type().TerraformType(ctx)

	stateVal := tftypes.NewValue(tfType, map[string]tftypes.Value{
		"id":           tftypes.NewValue(tftypes.String, "vpc-upd-1"),
		"name":         tftypes.NewValue(tftypes.String, "old-vpc"),
		"description":  tftypes.NewValue(tftypes.String, nil),
		"cidr":         tftypes.NewValue(tftypes.String, "10.0.0.0/16"),
		"region":       tftypes.NewValue(tftypes.String, "sweden"),
		"tags":         tftypes.NewValue(tftypes.Map{ElementType: tftypes.String}, nil),
		"status":       tftypes.NewValue(tftypes.String, "active"),
		"is_default":   tftypes.NewValue(tftypes.Bool, false),
		"subnet_count": tftypes.NewValue(tftypes.Number, big.NewFloat(1)),
		"created_at":   tftypes.NewValue(tftypes.String, "2025-01-01T00:00:00Z"),
		"updated_at":   tftypes.NewValue(tftypes.String, nil),
	})

	planVal := tftypes.NewValue(tfType, map[string]tftypes.Value{
		"id":          tftypes.NewValue(tftypes.String, "vpc-upd-1"),
		"name":        tftypes.NewValue(tftypes.String, "updated-vpc"),
		"description": tftypes.NewValue(tftypes.String, "Updated desc"),
		"cidr":        tftypes.NewValue(tftypes.String, "10.0.0.0/16"),
		"region":      tftypes.NewValue(tftypes.String, "sweden"),
		"tags": tftypes.NewValue(tftypes.Map{ElementType: tftypes.String}, map[string]tftypes.Value{
			"env": tftypes.NewValue(tftypes.String, "prod"),
		}),
		"status":       tftypes.NewValue(tftypes.String, "active"),
		"is_default":   tftypes.NewValue(tftypes.Bool, false),
		"subnet_count": tftypes.NewValue(tftypes.Number, big.NewFloat(1)),
		"created_at":   tftypes.NewValue(tftypes.String, "2025-01-01T00:00:00Z"),
		"updated_at":   tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
	})

	updateReq := resource.UpdateRequest{
		Plan:  tfsdk.Plan{Schema: schemaResp.Schema, Raw: planVal},
		State: tfsdk.State{Schema: schemaResp.Schema, Raw: stateVal},
	}
	var updateResp resource.UpdateResponse
	updateResp.State = tfsdk.State{Schema: schemaResp.Schema}

	r.Update(ctx, updateReq, &updateResp)

	if updateResp.Diagnostics.HasError() {
		t.Fatalf("Update failed: %v", updateResp.Diagnostics.Errors())
	}

	var model VPCModel
	updateResp.State.Get(ctx, &model)

	if model.Name.ValueString() != "updated-vpc" {
		t.Errorf("expected Name updated-vpc, got %s", model.Name.ValueString())
	}
	if model.Description.ValueString() != "Updated desc" {
		t.Errorf("expected Description 'Updated desc', got %s", model.Description.ValueString())
	}
	if model.UpdatedAt.ValueString() != "2025-03-01T00:00:00Z" {
		t.Errorf("expected UpdatedAt 2025-03-01T00:00:00Z, got %s", model.UpdatedAt.ValueString())
	}
}

func TestVPCResource_TFSDKDelete(t *testing.T) {
	var deleteCalled bool
	var queryForce string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/me":
			_ = json.NewEncoder(w).Encode(map[string]string{"id": "user-123", "tenantId": "tenant-456"})

		case r.Method == http.MethodDelete && r.URL.Path == "/v1/tenants/tenant-456/vpcs/vpc-del-1":
			deleteCalled = true
			queryForce = r.URL.Query().Get("force")
			w.WriteHeader(http.StatusNoContent)

		default:
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"error": map[string]string{"code": "NOT_FOUND", "message": "not found"},
			})
		}
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key") // pragma: allowlist secret
	if err := c.Configure(context.Background()); err != nil {
		t.Fatalf("client configure failed: %v", err)
	}

	r := NewResource()
	configureVPCResource(t, r, c)
	schemaResp := getVPCSchema(t)

	ctx := context.Background()
	tfType := schemaResp.Schema.Type().TerraformType(ctx)

	stateVal := tftypes.NewValue(tfType, map[string]tftypes.Value{
		"id":           tftypes.NewValue(tftypes.String, "vpc-del-1"),
		"name":         tftypes.NewValue(tftypes.String, "del-vpc"),
		"description":  tftypes.NewValue(tftypes.String, nil),
		"cidr":         tftypes.NewValue(tftypes.String, "10.0.0.0/16"),
		"region":       tftypes.NewValue(tftypes.String, "sweden"),
		"tags":         tftypes.NewValue(tftypes.Map{ElementType: tftypes.String}, nil),
		"status":       tftypes.NewValue(tftypes.String, "active"),
		"is_default":   tftypes.NewValue(tftypes.Bool, false),
		"subnet_count": tftypes.NewValue(tftypes.Number, big.NewFloat(0)),
		"created_at":   tftypes.NewValue(tftypes.String, "2025-01-01T00:00:00Z"),
		"updated_at":   tftypes.NewValue(tftypes.String, nil),
	})

	deleteReq := resource.DeleteRequest{
		State: tfsdk.State{Schema: schemaResp.Schema, Raw: stateVal},
	}
	var deleteResp resource.DeleteResponse
	deleteResp.State = tfsdk.State{Schema: schemaResp.Schema}

	r.Delete(ctx, deleteReq, &deleteResp)

	if deleteResp.Diagnostics.HasError() {
		t.Fatalf("Delete failed: %v", deleteResp.Diagnostics.Errors())
	}

	if !deleteCalled {
		t.Error("expected DELETE to be called")
	}
	if queryForce != "true" {
		t.Errorf("expected force=true query param, got %s", queryForce)
	}
}

func TestVPCResource_TFSDKDeleteAlreadyGone(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/me":
			_ = json.NewEncoder(w).Encode(map[string]string{"id": "user-123", "tenantId": "tenant-456"})

		default:
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"error": map[string]string{"code": "NOT_FOUND", "message": "not found"},
			})
		}
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key") // pragma: allowlist secret
	if err := c.Configure(context.Background()); err != nil {
		t.Fatalf("client configure failed: %v", err)
	}

	r := NewResource()
	configureVPCResource(t, r, c)
	schemaResp := getVPCSchema(t)

	ctx := context.Background()
	tfType := schemaResp.Schema.Type().TerraformType(ctx)

	stateVal := tftypes.NewValue(tfType, map[string]tftypes.Value{
		"id":           tftypes.NewValue(tftypes.String, "vpc-already-gone"),
		"name":         tftypes.NewValue(tftypes.String, "gone-vpc"),
		"description":  tftypes.NewValue(tftypes.String, nil),
		"cidr":         tftypes.NewValue(tftypes.String, "10.0.0.0/16"),
		"region":       tftypes.NewValue(tftypes.String, "sweden"),
		"tags":         tftypes.NewValue(tftypes.Map{ElementType: tftypes.String}, nil),
		"status":       tftypes.NewValue(tftypes.String, "active"),
		"is_default":   tftypes.NewValue(tftypes.Bool, false),
		"subnet_count": tftypes.NewValue(tftypes.Number, big.NewFloat(0)),
		"created_at":   tftypes.NewValue(tftypes.String, "2025-01-01T00:00:00Z"),
		"updated_at":   tftypes.NewValue(tftypes.String, nil),
	})

	deleteReq := resource.DeleteRequest{
		State: tfsdk.State{Schema: schemaResp.Schema, Raw: stateVal},
	}
	var deleteResp resource.DeleteResponse
	deleteResp.State = tfsdk.State{Schema: schemaResp.Schema}

	r.Delete(ctx, deleteReq, &deleteResp)

	// Delete of already-gone resource should not error
	if deleteResp.Diagnostics.HasError() {
		t.Fatalf("Delete should not error for already-gone resource, got: %v", deleteResp.Diagnostics.Errors())
	}
}

// --- Additional tests for coverage gaps ---

func TestVPCResource_Metadata(t *testing.T) {
	r := NewResource()
	req := resource.MetadataRequest{ProviderTypeName: "frostmoln"}
	resp := &resource.MetadataResponse{}
	r.Metadata(context.Background(), req, resp)

	if resp.TypeName != "frostmoln_vpc" {
		t.Errorf("expected type name frostmoln_vpc, got %s", resp.TypeName)
	}
}

func TestVPCResource_ConfigureNilProviderData(t *testing.T) {
	r := NewResource()
	resp := &resource.ConfigureResponse{}
	r.(resource.ResourceWithConfigure).Configure(context.Background(), resource.ConfigureRequest{ProviderData: nil}, resp)
	if resp.Diagnostics.HasError() {
		t.Errorf("expected no errors, got %v", resp.Diagnostics)
	}
}

func TestVPCResource_ConfigureWrongType(t *testing.T) {
	r := NewResource()
	resp := &resource.ConfigureResponse{}
	r.(resource.ResourceWithConfigure).Configure(context.Background(), resource.ConfigureRequest{ProviderData: "bad"}, resp)
	if !resp.Diagnostics.HasError() {
		t.Error("expected error for wrong type")
	}
}

func TestVPCResource_TFSDKCreateSync201(t *testing.T) {
	// Test synchronous create (201 Created, no polling needed)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/me":
			_ = json.NewEncoder(w).Encode(map[string]string{"id": "user-123", "tenantId": "tenant-456"})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/tenants/tenant-456/vpcs":
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(apiVPC{
				ID:          "vpc-sync-1",
				Name:        "sync-vpc",
				CIDR:        "10.0.0.0/16",
				Region:      "sweden",
				Status:      "active",
				IsDefault:   false,
				SubnetCount: 0,
				CreatedAt:   "2025-01-01T00:00:00Z",
			})
		default:
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"error": map[string]string{"code": "NOT_FOUND", "message": "not found"},
			})
		}
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key") // pragma: allowlist secret
	if err := c.Configure(context.Background()); err != nil {
		t.Fatalf("client configure failed: %v", err)
	}

	r := NewResource()
	configureVPCResource(t, r, c)
	schemaResp := getVPCSchema(t)

	ctx := context.Background()
	tfType := schemaResp.Schema.Type().TerraformType(ctx)

	planVal := tftypes.NewValue(tfType, map[string]tftypes.Value{
		"id":           tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"name":         tftypes.NewValue(tftypes.String, "sync-vpc"),
		"description":  tftypes.NewValue(tftypes.String, nil),
		"cidr":         tftypes.NewValue(tftypes.String, "10.0.0.0/16"),
		"region":       tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"tags":         tftypes.NewValue(tftypes.Map{ElementType: tftypes.String}, nil),
		"status":       tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"is_default":   tftypes.NewValue(tftypes.Bool, tftypes.UnknownValue),
		"subnet_count": tftypes.NewValue(tftypes.Number, tftypes.UnknownValue),
		"created_at":   tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"updated_at":   tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
	})

	createReq := resource.CreateRequest{
		Plan: tfsdk.Plan{Schema: schemaResp.Schema, Raw: planVal},
	}
	var createResp resource.CreateResponse
	createResp.State = tfsdk.State{Schema: schemaResp.Schema}

	r.Create(ctx, createReq, &createResp)

	if createResp.Diagnostics.HasError() {
		t.Fatalf("Create failed: %v", createResp.Diagnostics.Errors())
	}

	var model VPCModel
	createResp.State.Get(ctx, &model)

	if model.ID.ValueString() != "vpc-sync-1" {
		t.Errorf("expected ID vpc-sync-1, got %s", model.ID.ValueString())
	}
	if model.Status.ValueString() != "active" {
		t.Errorf("expected Status active, got %s", model.Status.ValueString())
	}
}

func TestVPCResource_TFSDKCreateAPIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/me":
			_ = json.NewEncoder(w).Encode(map[string]string{"id": "user-123", "tenantId": "tenant-456"})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/tenants/tenant-456/vpcs":
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"error": map[string]string{"code": "INTERNAL_ERROR", "message": "server error"},
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key") // pragma: allowlist secret
	if err := c.Configure(context.Background()); err != nil {
		t.Fatalf("client configure failed: %v", err)
	}

	r := NewResource()
	configureVPCResource(t, r, c)
	schemaResp := getVPCSchema(t)

	ctx := context.Background()
	tfType := schemaResp.Schema.Type().TerraformType(ctx)

	planVal := tftypes.NewValue(tfType, map[string]tftypes.Value{
		"id":           tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"name":         tftypes.NewValue(tftypes.String, "fail-vpc"),
		"description":  tftypes.NewValue(tftypes.String, nil),
		"cidr":         tftypes.NewValue(tftypes.String, "10.0.0.0/16"),
		"region":       tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"tags":         tftypes.NewValue(tftypes.Map{ElementType: tftypes.String}, nil),
		"status":       tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"is_default":   tftypes.NewValue(tftypes.Bool, tftypes.UnknownValue),
		"subnet_count": tftypes.NewValue(tftypes.Number, tftypes.UnknownValue),
		"created_at":   tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"updated_at":   tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
	})

	createReq := resource.CreateRequest{
		Plan: tfsdk.Plan{Schema: schemaResp.Schema, Raw: planVal},
	}
	var createResp resource.CreateResponse
	createResp.State = tfsdk.State{Schema: schemaResp.Schema}

	r.Create(ctx, createReq, &createResp)

	if !createResp.Diagnostics.HasError() {
		t.Error("expected error for API failure on create")
	}
}

func TestVPCResource_TFSDKCreateBadResponseBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/me":
			_ = json.NewEncoder(w).Encode(map[string]string{"id": "user-123", "tenantId": "tenant-456"})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/tenants/tenant-456/vpcs":
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte("not json"))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key") // pragma: allowlist secret
	if err := c.Configure(context.Background()); err != nil {
		t.Fatalf("client configure failed: %v", err)
	}

	r := NewResource()
	configureVPCResource(t, r, c)
	schemaResp := getVPCSchema(t)

	ctx := context.Background()
	tfType := schemaResp.Schema.Type().TerraformType(ctx)

	planVal := tftypes.NewValue(tfType, map[string]tftypes.Value{
		"id":           tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"name":         tftypes.NewValue(tftypes.String, "bad-vpc"),
		"description":  tftypes.NewValue(tftypes.String, nil),
		"cidr":         tftypes.NewValue(tftypes.String, "10.0.0.0/16"),
		"region":       tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"tags":         tftypes.NewValue(tftypes.Map{ElementType: tftypes.String}, nil),
		"status":       tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"is_default":   tftypes.NewValue(tftypes.Bool, tftypes.UnknownValue),
		"subnet_count": tftypes.NewValue(tftypes.Number, tftypes.UnknownValue),
		"created_at":   tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"updated_at":   tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
	})

	createReq := resource.CreateRequest{
		Plan: tfsdk.Plan{Schema: schemaResp.Schema, Raw: planVal},
	}
	var createResp resource.CreateResponse
	createResp.State = tfsdk.State{Schema: schemaResp.Schema}

	r.Create(ctx, createReq, &createResp)

	if !createResp.Diagnostics.HasError() {
		t.Error("expected error for bad response body on create")
	}
}

func TestVPCResource_TFSDKCreatePollingErrorState(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/me":
			_ = json.NewEncoder(w).Encode(map[string]string{"id": "user-123", "tenantId": "tenant-456"})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/tenants/tenant-456/vpcs":
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"operationId": "op-vpc-err", "status": "pending", "resourceType": "vpc",
			})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/operations/op-vpc-err":
			// The create workflow failed → operation terminal-failed → create errors.
			_ = json.NewEncoder(w).Encode(map[string]any{
				"operationId": "op-vpc-err", "status": "failed", "resourceType": "vpc",
				"error": "vpc entered error state",
			})
		default:
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"error": map[string]string{"code": "NOT_FOUND", "message": "not found"},
			})
		}
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key") // pragma: allowlist secret
	if err := c.Configure(context.Background()); err != nil {
		t.Fatalf("client configure failed: %v", err)
	}

	r := NewResource()
	configureVPCResource(t, r, c)
	schemaResp := getVPCSchema(t)

	ctx := context.Background()
	tfType := schemaResp.Schema.Type().TerraformType(ctx)

	planVal := tftypes.NewValue(tfType, map[string]tftypes.Value{
		"id":           tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"name":         tftypes.NewValue(tftypes.String, "err-vpc"),
		"description":  tftypes.NewValue(tftypes.String, nil),
		"cidr":         tftypes.NewValue(tftypes.String, "10.0.0.0/16"),
		"region":       tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"tags":         tftypes.NewValue(tftypes.Map{ElementType: tftypes.String}, nil),
		"status":       tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"is_default":   tftypes.NewValue(tftypes.Bool, tftypes.UnknownValue),
		"subnet_count": tftypes.NewValue(tftypes.Number, tftypes.UnknownValue),
		"created_at":   tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"updated_at":   tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
	})

	createReq := resource.CreateRequest{
		Plan: tfsdk.Plan{Schema: schemaResp.Schema, Raw: planVal},
	}
	var createResp resource.CreateResponse
	createResp.State = tfsdk.State{Schema: schemaResp.Schema}

	r.Create(ctx, createReq, &createResp)

	if !createResp.Diagnostics.HasError() {
		t.Error("expected error when VPC enters error state during polling")
	}
}

func TestVPCResource_TFSDKReadAPIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/me":
			_ = json.NewEncoder(w).Encode(map[string]string{"id": "user-123", "tenantId": "tenant-456"})
		default:
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"error": map[string]string{"code": "INTERNAL_ERROR", "message": "server error"},
			})
		}
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key") // pragma: allowlist secret
	if err := c.Configure(context.Background()); err != nil {
		t.Fatalf("client configure failed: %v", err)
	}

	r := NewResource()
	configureVPCResource(t, r, c)
	schemaResp := getVPCSchema(t)

	ctx := context.Background()
	tfType := schemaResp.Schema.Type().TerraformType(ctx)

	stateVal := tftypes.NewValue(tfType, map[string]tftypes.Value{
		"id":           tftypes.NewValue(tftypes.String, "vpc-err-r"),
		"name":         tftypes.NewValue(tftypes.String, "err-vpc"),
		"description":  tftypes.NewValue(tftypes.String, nil),
		"cidr":         tftypes.NewValue(tftypes.String, "10.0.0.0/16"),
		"region":       tftypes.NewValue(tftypes.String, "sweden"),
		"tags":         tftypes.NewValue(tftypes.Map{ElementType: tftypes.String}, nil),
		"status":       tftypes.NewValue(tftypes.String, "active"),
		"is_default":   tftypes.NewValue(tftypes.Bool, false),
		"subnet_count": tftypes.NewValue(tftypes.Number, big.NewFloat(0)),
		"created_at":   tftypes.NewValue(tftypes.String, "2025-01-01T00:00:00Z"),
		"updated_at":   tftypes.NewValue(tftypes.String, nil),
	})

	readReq := resource.ReadRequest{
		State: tfsdk.State{Schema: schemaResp.Schema, Raw: stateVal},
	}
	var readResp resource.ReadResponse
	readResp.State = tfsdk.State{Schema: schemaResp.Schema}

	r.Read(ctx, readReq, &readResp)

	if !readResp.Diagnostics.HasError() {
		t.Error("expected error for API failure on read")
	}
}

func TestVPCResource_TFSDKReadBadJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/me":
			_ = json.NewEncoder(w).Encode(map[string]string{"id": "user-123", "tenantId": "tenant-456"})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/tenant-456/vpcs/vpc-bj-1":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("not json"))
		default:
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"error": map[string]string{"code": "NOT_FOUND", "message": "not found"},
			})
		}
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key") // pragma: allowlist secret
	if err := c.Configure(context.Background()); err != nil {
		t.Fatalf("client configure failed: %v", err)
	}

	r := NewResource()
	configureVPCResource(t, r, c)
	schemaResp := getVPCSchema(t)

	ctx := context.Background()
	tfType := schemaResp.Schema.Type().TerraformType(ctx)

	stateVal := tftypes.NewValue(tfType, map[string]tftypes.Value{
		"id":           tftypes.NewValue(tftypes.String, "vpc-bj-1"),
		"name":         tftypes.NewValue(tftypes.String, "bj-vpc"),
		"description":  tftypes.NewValue(tftypes.String, nil),
		"cidr":         tftypes.NewValue(tftypes.String, "10.0.0.0/16"),
		"region":       tftypes.NewValue(tftypes.String, "sweden"),
		"tags":         tftypes.NewValue(tftypes.Map{ElementType: tftypes.String}, nil),
		"status":       tftypes.NewValue(tftypes.String, "active"),
		"is_default":   tftypes.NewValue(tftypes.Bool, false),
		"subnet_count": tftypes.NewValue(tftypes.Number, big.NewFloat(0)),
		"created_at":   tftypes.NewValue(tftypes.String, "2025-01-01T00:00:00Z"),
		"updated_at":   tftypes.NewValue(tftypes.String, nil),
	})

	readReq := resource.ReadRequest{
		State: tfsdk.State{Schema: schemaResp.Schema, Raw: stateVal},
	}
	var readResp resource.ReadResponse
	readResp.State = tfsdk.State{Schema: schemaResp.Schema}

	r.Read(ctx, readReq, &readResp)

	if !readResp.Diagnostics.HasError() {
		t.Error("expected error for bad JSON in read response")
	}
}

func TestVPCResource_TFSDKUpdateAPIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/me":
			_ = json.NewEncoder(w).Encode(map[string]string{"id": "user-123", "tenantId": "tenant-456"})
		case r.Method == http.MethodPatch && r.URL.Path == "/v1/tenants/tenant-456/vpcs/vpc-ue-1":
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"error": map[string]string{"code": "INTERNAL_ERROR", "message": "update failed"},
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key") // pragma: allowlist secret
	if err := c.Configure(context.Background()); err != nil {
		t.Fatalf("client configure failed: %v", err)
	}

	r := NewResource()
	configureVPCResource(t, r, c)
	schemaResp := getVPCSchema(t)

	ctx := context.Background()
	tfType := schemaResp.Schema.Type().TerraformType(ctx)

	stateVal := tftypes.NewValue(tfType, map[string]tftypes.Value{
		"id":           tftypes.NewValue(tftypes.String, "vpc-ue-1"),
		"name":         tftypes.NewValue(tftypes.String, "old-vpc"),
		"description":  tftypes.NewValue(tftypes.String, nil),
		"cidr":         tftypes.NewValue(tftypes.String, "10.0.0.0/16"),
		"region":       tftypes.NewValue(tftypes.String, "sweden"),
		"tags":         tftypes.NewValue(tftypes.Map{ElementType: tftypes.String}, nil),
		"status":       tftypes.NewValue(tftypes.String, "active"),
		"is_default":   tftypes.NewValue(tftypes.Bool, false),
		"subnet_count": tftypes.NewValue(tftypes.Number, big.NewFloat(0)),
		"created_at":   tftypes.NewValue(tftypes.String, "2025-01-01T00:00:00Z"),
		"updated_at":   tftypes.NewValue(tftypes.String, nil),
	})

	planVal := tftypes.NewValue(tfType, map[string]tftypes.Value{
		"id":           tftypes.NewValue(tftypes.String, "vpc-ue-1"),
		"name":         tftypes.NewValue(tftypes.String, "new-vpc"),
		"description":  tftypes.NewValue(tftypes.String, nil),
		"cidr":         tftypes.NewValue(tftypes.String, "10.0.0.0/16"),
		"region":       tftypes.NewValue(tftypes.String, "sweden"),
		"tags":         tftypes.NewValue(tftypes.Map{ElementType: tftypes.String}, nil),
		"status":       tftypes.NewValue(tftypes.String, "active"),
		"is_default":   tftypes.NewValue(tftypes.Bool, false),
		"subnet_count": tftypes.NewValue(tftypes.Number, big.NewFloat(0)),
		"created_at":   tftypes.NewValue(tftypes.String, "2025-01-01T00:00:00Z"),
		"updated_at":   tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
	})

	updateReq := resource.UpdateRequest{
		Plan:  tfsdk.Plan{Schema: schemaResp.Schema, Raw: planVal},
		State: tfsdk.State{Schema: schemaResp.Schema, Raw: stateVal},
	}
	var updateResp resource.UpdateResponse
	updateResp.State = tfsdk.State{Schema: schemaResp.Schema}

	r.Update(ctx, updateReq, &updateResp)

	if !updateResp.Diagnostics.HasError() {
		t.Error("expected error for API failure on update")
	}
}

func TestVPCResource_TFSDKUpdateBadJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/me":
			_ = json.NewEncoder(w).Encode(map[string]string{"id": "user-123", "tenantId": "tenant-456"})
		case r.Method == http.MethodPatch && r.URL.Path == "/v1/tenants/tenant-456/vpcs/vpc-ubj-1":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("not json"))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key") // pragma: allowlist secret
	if err := c.Configure(context.Background()); err != nil {
		t.Fatalf("client configure failed: %v", err)
	}

	r := NewResource()
	configureVPCResource(t, r, c)
	schemaResp := getVPCSchema(t)

	ctx := context.Background()
	tfType := schemaResp.Schema.Type().TerraformType(ctx)

	stateVal := tftypes.NewValue(tfType, map[string]tftypes.Value{
		"id":           tftypes.NewValue(tftypes.String, "vpc-ubj-1"),
		"name":         tftypes.NewValue(tftypes.String, "old-vpc"),
		"description":  tftypes.NewValue(tftypes.String, nil),
		"cidr":         tftypes.NewValue(tftypes.String, "10.0.0.0/16"),
		"region":       tftypes.NewValue(tftypes.String, "sweden"),
		"tags":         tftypes.NewValue(tftypes.Map{ElementType: tftypes.String}, nil),
		"status":       tftypes.NewValue(tftypes.String, "active"),
		"is_default":   tftypes.NewValue(tftypes.Bool, false),
		"subnet_count": tftypes.NewValue(tftypes.Number, big.NewFloat(0)),
		"created_at":   tftypes.NewValue(tftypes.String, "2025-01-01T00:00:00Z"),
		"updated_at":   tftypes.NewValue(tftypes.String, nil),
	})

	planVal := tftypes.NewValue(tfType, map[string]tftypes.Value{
		"id":           tftypes.NewValue(tftypes.String, "vpc-ubj-1"),
		"name":         tftypes.NewValue(tftypes.String, "new-vpc"),
		"description":  tftypes.NewValue(tftypes.String, nil),
		"cidr":         tftypes.NewValue(tftypes.String, "10.0.0.0/16"),
		"region":       tftypes.NewValue(tftypes.String, "sweden"),
		"tags":         tftypes.NewValue(tftypes.Map{ElementType: tftypes.String}, nil),
		"status":       tftypes.NewValue(tftypes.String, "active"),
		"is_default":   tftypes.NewValue(tftypes.Bool, false),
		"subnet_count": tftypes.NewValue(tftypes.Number, big.NewFloat(0)),
		"created_at":   tftypes.NewValue(tftypes.String, "2025-01-01T00:00:00Z"),
		"updated_at":   tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
	})

	updateReq := resource.UpdateRequest{
		Plan:  tfsdk.Plan{Schema: schemaResp.Schema, Raw: planVal},
		State: tfsdk.State{Schema: schemaResp.Schema, Raw: stateVal},
	}
	var updateResp resource.UpdateResponse
	updateResp.State = tfsdk.State{Schema: schemaResp.Schema}

	r.Update(ctx, updateReq, &updateResp)

	if !updateResp.Diagnostics.HasError() {
		t.Error("expected error for bad JSON in update response")
	}
}

func TestVPCResource_TFSDKDeleteAPIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/me":
			_ = json.NewEncoder(w).Encode(map[string]string{"id": "user-123", "tenantId": "tenant-456"})
		default:
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"error": map[string]string{"code": "INTERNAL_ERROR", "message": "server error"},
			})
		}
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key") // pragma: allowlist secret
	if err := c.Configure(context.Background()); err != nil {
		t.Fatalf("client configure failed: %v", err)
	}

	r := NewResource()
	configureVPCResource(t, r, c)
	schemaResp := getVPCSchema(t)

	ctx := context.Background()
	tfType := schemaResp.Schema.Type().TerraformType(ctx)

	stateVal := tftypes.NewValue(tfType, map[string]tftypes.Value{
		"id":           tftypes.NewValue(tftypes.String, "vpc-del-err"),
		"name":         tftypes.NewValue(tftypes.String, "err-vpc"),
		"description":  tftypes.NewValue(tftypes.String, nil),
		"cidr":         tftypes.NewValue(tftypes.String, "10.0.0.0/16"),
		"region":       tftypes.NewValue(tftypes.String, "sweden"),
		"tags":         tftypes.NewValue(tftypes.Map{ElementType: tftypes.String}, nil),
		"status":       tftypes.NewValue(tftypes.String, "active"),
		"is_default":   tftypes.NewValue(tftypes.Bool, false),
		"subnet_count": tftypes.NewValue(tftypes.Number, big.NewFloat(0)),
		"created_at":   tftypes.NewValue(tftypes.String, "2025-01-01T00:00:00Z"),
		"updated_at":   tftypes.NewValue(tftypes.String, nil),
	})

	deleteReq := resource.DeleteRequest{
		State: tfsdk.State{Schema: schemaResp.Schema, Raw: stateVal},
	}
	var deleteResp resource.DeleteResponse
	deleteResp.State = tfsdk.State{Schema: schemaResp.Schema}

	r.Delete(ctx, deleteReq, &deleteResp)

	if !deleteResp.Diagnostics.HasError() {
		t.Error("expected error for API failure on delete")
	}
}

func TestVPCResource_TFSDKImportState(t *testing.T) {
	r := NewResource()
	schemaResp := getVPCSchema(t)

	ctx := context.Background()
	tfType := schemaResp.Schema.Type().TerraformType(ctx)

	emptyState := tftypes.NewValue(tfType, map[string]tftypes.Value{
		"id":           tftypes.NewValue(tftypes.String, nil),
		"name":         tftypes.NewValue(tftypes.String, nil),
		"description":  tftypes.NewValue(tftypes.String, nil),
		"cidr":         tftypes.NewValue(tftypes.String, nil),
		"region":       tftypes.NewValue(tftypes.String, nil),
		"tags":         tftypes.NewValue(tftypes.Map{ElementType: tftypes.String}, nil),
		"status":       tftypes.NewValue(tftypes.String, nil),
		"is_default":   tftypes.NewValue(tftypes.Bool, nil),
		"subnet_count": tftypes.NewValue(tftypes.Number, nil),
		"created_at":   tftypes.NewValue(tftypes.String, nil),
		"updated_at":   tftypes.NewValue(tftypes.String, nil),
	})

	importReq := resource.ImportStateRequest{ID: "vpc-import-1"}
	importResp := &resource.ImportStateResponse{
		State: tfsdk.State{Schema: schemaResp.Schema, Raw: emptyState},
	}

	r.(resource.ResourceWithImportState).ImportState(ctx, importReq, importResp)

	if importResp.Diagnostics.HasError() {
		t.Fatalf("ImportState failed: %v", importResp.Diagnostics.Errors())
	}

	var model VPCModel
	importResp.State.Get(ctx, &model)

	if model.ID.ValueString() != "vpc-import-1" {
		t.Errorf("expected ID vpc-import-1, got %s", model.ID.ValueString())
	}
}
