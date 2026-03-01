package vpc

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"git.nl.cloud/NordicLight/terraform-provider-frostmoln/internal/client"
)

func TestVPCModelFromAPI(t *testing.T) {
	vpc := &apiVPC{
		ID:          "vpc-123",
		Name:        "test-vpc",
		Description: "A test VPC",
		CIDR:        "10.0.0.0/16",
		Region:      "eu-north-1",
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
	if model.Region.ValueString() != "eu-north-1" {
		t.Errorf("expected Region eu-north-1, got %s", model.Region.ValueString())
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
		Region:    "eu-west-1",
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
		Region:      types.StringValue("eu-north-1"),
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
	if req.Region != "eu-north-1" {
		t.Errorf("expected Region eu-north-1, got %s", req.Region)
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
		Region:    "eu-north-1",
		Status:    "active",
		CreatedAt: "2025-01-01T00:00:00Z",
	}

	vpcUpdated := apiVPC{
		ID:          "vpc-test-1",
		Name:        "updated-vpc",
		Description: "Updated description",
		CIDR:        "10.0.0.0/16",
		Region:      "eu-north-1",
		Status:      "active",
		CreatedAt:   "2025-01-01T00:00:00Z",
		UpdatedAt:   "2025-01-02T00:00:00Z",
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/tenants/t-123/vpcs":
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(vpcCreated)

		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-123/vpcs/vpc-test-1":
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(vpcCreated)

		case r.Method == http.MethodPatch && r.URL.Path == "/v1/tenants/t-123/vpcs/vpc-test-1":
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(vpcUpdated)

		case r.Method == http.MethodDelete && r.URL.Path == "/v1/tenants/t-123/vpcs/vpc-test-1":
			w.WriteHeader(http.StatusNoContent)

		default:
			w.WriteHeader(http.StatusNotFound)
			json.NewEncoder(w).Encode(map[string]interface{}{
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
			json.NewEncoder(w).Encode(apiVPC{
				ID:     "vpc-async-1",
				Name:   "async-vpc",
				CIDR:   "10.0.0.0/16",
				Region: "eu-north-1",
				Status: "creating",
			})

		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-123/vpcs/vpc-async-1":
			callCount++
			status := "creating"
			if callCount >= 2 {
				status = "active"
			}
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(apiVPC{
				ID:        "vpc-async-1",
				Name:      "async-vpc",
				CIDR:      "10.0.0.0/16",
				Region:    "eu-north-1",
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
		json.NewEncoder(w).Encode(map[string]interface{}{
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
