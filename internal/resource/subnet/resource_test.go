package subnet

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"git.nl.cloud/NordicLight/terraform-provider-frostmoln/internal/client"
)

func TestSubnetModelFromAPI(t *testing.T) {
	subnet := &apiSubnet{
		ID:           "subnet-123",
		Name:         "test-subnet",
		Description:  "A test subnet",
		CIDR:         "10.0.1.0/24",
		VPCID:        "vpc-456",
		Zone:         "eu-north-1a",
		GatewayIP:    "10.0.1.1",
		DNSServers:   []string{"8.8.8.8", "8.8.4.4"},
		IsPublic:     true,
		Status:       "active",
		AvailableIPs: 250,
		Tags:         map[string]string{"env": "test"},
		CreatedAt:    "2025-01-01T00:00:00Z",
	}

	var model SubnetModel
	var diags diag.Diagnostics
	model.fromAPI(context.Background(), subnet, &diags)

	if diags.HasError() {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}

	if model.ID.ValueString() != "subnet-123" {
		t.Errorf("expected ID subnet-123, got %s", model.ID.ValueString())
	}
	if model.Name.ValueString() != "test-subnet" {
		t.Errorf("expected Name test-subnet, got %s", model.Name.ValueString())
	}
	if model.Description.ValueString() != "A test subnet" {
		t.Errorf("expected Description 'A test subnet', got %s", model.Description.ValueString())
	}
	if model.CIDR.ValueString() != "10.0.1.0/24" {
		t.Errorf("expected CIDR 10.0.1.0/24, got %s", model.CIDR.ValueString())
	}
	if model.VPCID.ValueString() != "vpc-456" {
		t.Errorf("expected VPCID vpc-456, got %s", model.VPCID.ValueString())
	}
	if model.Zone.ValueString() != "eu-north-1a" {
		t.Errorf("expected Zone eu-north-1a, got %s", model.Zone.ValueString())
	}
	if model.GatewayIP.ValueString() != "10.0.1.1" {
		t.Errorf("expected GatewayIP 10.0.1.1, got %s", model.GatewayIP.ValueString())
	}
	if model.IsPublic.ValueBool() != true {
		t.Error("expected IsPublic true, got false")
	}
	if model.Status.ValueString() != "active" {
		t.Errorf("expected Status active, got %s", model.Status.ValueString())
	}
	if model.AvailableIPs.ValueInt64() != 250 {
		t.Errorf("expected AvailableIPs 250, got %d", model.AvailableIPs.ValueInt64())
	}
	if model.CreatedAt.ValueString() != "2025-01-01T00:00:00Z" {
		t.Errorf("expected CreatedAt 2025-01-01T00:00:00Z, got %s", model.CreatedAt.ValueString())
	}
}

func TestSubnetModelFromAPIMinimal(t *testing.T) {
	subnet := &apiSubnet{
		ID:        "subnet-789",
		Name:      "minimal-subnet",
		CIDR:      "10.0.2.0/24",
		VPCID:     "vpc-456",
		Status:    "active",
		CreatedAt: "2025-01-01T00:00:00Z",
	}

	var model SubnetModel
	model.Description = types.StringNull()
	model.DNSServers = types.ListNull(types.StringType)
	model.Tags = types.MapNull(types.StringType)

	var diags diag.Diagnostics
	model.fromAPI(context.Background(), subnet, &diags)

	if diags.HasError() {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}

	if !model.Description.IsNull() {
		t.Errorf("expected Description to be null, got %s", model.Description.ValueString())
	}
	if !model.Zone.IsNull() {
		t.Errorf("expected Zone to be null, got %s", model.Zone.ValueString())
	}
	if !model.GatewayIP.IsNull() {
		t.Errorf("expected GatewayIP to be null, got %s", model.GatewayIP.ValueString())
	}
	if !model.DNSServers.IsNull() {
		t.Error("expected DNSServers to be null")
	}
	if !model.Tags.IsNull() {
		t.Error("expected Tags to be null")
	}
}

func TestSubnetModelToCreateRequest(t *testing.T) {
	ctx := context.Background()
	dns, _ := types.ListValueFrom(ctx, types.StringType, []string{"1.1.1.1"})
	tags, _ := types.MapValueFrom(ctx, types.StringType, map[string]string{"env": "prod"})

	model := SubnetModel{
		Name:        types.StringValue("my-subnet"),
		Description: types.StringValue("My subnet"),
		CIDR:        types.StringValue("10.0.1.0/24"),
		VPCID:       types.StringValue("vpc-123"),
		Zone:        types.StringValue("eu-north-1a"),
		GatewayIP:   types.StringValue("10.0.1.1"),
		DNSServers:  dns,
		IsPublic:    types.BoolValue(true),
		Tags:        tags,
	}

	var diags diag.Diagnostics
	req := model.toCreateRequest(ctx, &diags)

	if diags.HasError() {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}

	if req.Name != "my-subnet" {
		t.Errorf("expected Name my-subnet, got %s", req.Name)
	}
	if req.CIDR != "10.0.1.0/24" {
		t.Errorf("expected CIDR 10.0.1.0/24, got %s", req.CIDR)
	}
	if req.VPCID != "vpc-123" {
		t.Errorf("expected VPCID vpc-123, got %s", req.VPCID)
	}
	if req.Zone != "eu-north-1a" {
		t.Errorf("expected Zone eu-north-1a, got %s", req.Zone)
	}
	if req.GatewayIP != "10.0.1.1" {
		t.Errorf("expected GatewayIP 10.0.1.1, got %s", req.GatewayIP)
	}
	if len(req.DNSServers) != 1 || req.DNSServers[0] != "1.1.1.1" {
		t.Errorf("expected DNSServers [1.1.1.1], got %v", req.DNSServers)
	}
	if !req.IsPublic {
		t.Error("expected IsPublic true")
	}
	if req.Tags["env"] != "prod" {
		t.Errorf("expected tag env=prod, got %v", req.Tags)
	}
}

func TestSubnetResourceCRUD(t *testing.T) {
	subnetData := apiSubnet{
		ID:           "subnet-test-1",
		Name:         "test-subnet",
		CIDR:         "10.0.1.0/24",
		VPCID:        "vpc-123",
		Status:       "active",
		AvailableIPs: 250,
		CreatedAt:    "2025-01-01T00:00:00Z",
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/tenants/t-123/subnets":
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(subnetData)

		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-123/subnets/subnet-test-1":
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(subnetData)

		case r.Method == http.MethodPatch && r.URL.Path == "/v1/tenants/t-123/subnets/subnet-test-1":
			subnetData.Tags = map[string]string{"env": "updated"}
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(subnetData)

		case r.Method == http.MethodDelete && r.URL.Path == "/v1/tenants/t-123/subnets/subnet-test-1":
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

	// Test Create
	createReq := apiCreateSubnetRequest{
		Name:  "test-subnet",
		CIDR:  "10.0.1.0/24",
		VPCID: "vpc-123",
	}
	apiResp, err := c.Post(ctx, c.TenantPath("/subnets"), createReq)
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}
	if apiResp.StatusCode != http.StatusCreated {
		t.Errorf("expected status 201, got %d", apiResp.StatusCode)
	}

	var created apiSubnet
	if err := json.Unmarshal(apiResp.Body, &created); err != nil {
		t.Fatalf("failed to parse create response: %v", err)
	}
	if created.ID != "subnet-test-1" {
		t.Errorf("expected ID subnet-test-1, got %s", created.ID)
	}

	// Test Read
	readResp, err := c.Get(ctx, c.TenantPath("/subnets/subnet-test-1"), nil)
	if err != nil {
		t.Fatalf("Read failed: %v", err)
	}
	var read apiSubnet
	if err := json.Unmarshal(readResp.Body, &read); err != nil {
		t.Fatalf("failed to parse read response: %v", err)
	}
	if read.Name != "test-subnet" {
		t.Errorf("expected Name test-subnet, got %s", read.Name)
	}

	// Test Update (tags only)
	updateReq := apiUpdateSubnetRequest{
		Tags: map[string]string{"env": "updated"},
	}
	patchResp, err := c.Patch(ctx, c.TenantPath("/subnets/subnet-test-1"), updateReq)
	if err != nil {
		t.Fatalf("Update failed: %v", err)
	}
	var updated apiSubnet
	if err := json.Unmarshal(patchResp.Body, &updated); err != nil {
		t.Fatalf("failed to parse update response: %v", err)
	}
	if updated.Tags["env"] != "updated" {
		t.Errorf("expected tag env=updated, got %v", updated.Tags)
	}

	// Test Delete
	_, err = c.Delete(ctx, c.TenantPath("/subnets/subnet-test-1"))
	if err != nil {
		t.Fatalf("Delete failed: %v", err)
	}
}

func TestSubnetReadNotFound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"error": map[string]string{"code": "NOT_FOUND", "message": "subnet not found"},
		})
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key") // pragma: allowlist secret
	c.SetTenantIDForTest("t-123")

	_, err := c.Get(context.Background(), c.TenantPath("/subnets/nonexistent"), nil)
	if err == nil {
		t.Fatal("expected error for not found")
	}
	if !client.IsNotFound(err) {
		t.Errorf("expected not found error, got %v", err)
	}
}
