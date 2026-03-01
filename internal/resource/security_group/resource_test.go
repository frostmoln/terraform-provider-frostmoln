package security_group

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

func TestSecurityGroupModelFromAPI(t *testing.T) {
	sg := &apiSecurityGroup{
		ID:          "sg-123",
		Name:        "test-sg",
		Description: "A test security group",
		VPCID:       "vpc-456",
		IsDefault:   false,
		Tags:        map[string]string{"env": "test"},
		CreatedAt:   "2025-01-01T00:00:00Z",
	}

	var model SecurityGroupModel
	var diags diag.Diagnostics
	model.fromAPI(context.Background(), sg, &diags)

	if diags.HasError() {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}

	if model.ID.ValueString() != "sg-123" {
		t.Errorf("expected ID sg-123, got %s", model.ID.ValueString())
	}
	if model.Name.ValueString() != "test-sg" {
		t.Errorf("expected Name test-sg, got %s", model.Name.ValueString())
	}
	if model.Description.ValueString() != "A test security group" {
		t.Errorf("expected Description 'A test security group', got %s", model.Description.ValueString())
	}
	if model.VPCID.ValueString() != "vpc-456" {
		t.Errorf("expected VPCID vpc-456, got %s", model.VPCID.ValueString())
	}
	if model.IsDefault.ValueBool() != false {
		t.Error("expected IsDefault false, got true")
	}
	if model.CreatedAt.ValueString() != "2025-01-01T00:00:00Z" {
		t.Errorf("expected CreatedAt 2025-01-01T00:00:00Z, got %s", model.CreatedAt.ValueString())
	}
}

func TestSecurityGroupModelFromAPIMinimal(t *testing.T) {
	sg := &apiSecurityGroup{
		ID:        "sg-789",
		Name:      "minimal-sg",
		IsDefault: true,
		CreatedAt: "2025-01-01T00:00:00Z",
	}

	var model SecurityGroupModel
	model.Description = types.StringNull()
	model.Tags = types.MapNull(types.StringType)

	var diags diag.Diagnostics
	model.fromAPI(context.Background(), sg, &diags)

	if diags.HasError() {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}

	if !model.Description.IsNull() {
		t.Errorf("expected Description to be null, got %s", model.Description.ValueString())
	}
	if !model.VPCID.IsNull() {
		t.Errorf("expected VPCID to be null, got %s", model.VPCID.ValueString())
	}
	if !model.Tags.IsNull() {
		t.Error("expected Tags to be null")
	}
	if !model.IsDefault.ValueBool() {
		t.Error("expected IsDefault true, got false")
	}
}

func TestSecurityGroupModelToCreateRequest(t *testing.T) {
	ctx := context.Background()
	tags, _ := types.MapValueFrom(ctx, types.StringType, map[string]string{"env": "prod"})

	model := SecurityGroupModel{
		Name:        types.StringValue("my-sg"),
		Description: types.StringValue("My security group"),
		VPCID:       types.StringValue("vpc-123"),
		Tags:        tags,
	}

	var diags diag.Diagnostics
	req := model.toCreateRequest(ctx, &diags)

	if diags.HasError() {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}

	if req.Name != "my-sg" {
		t.Errorf("expected Name my-sg, got %s", req.Name)
	}
	if req.Description != "My security group" {
		t.Errorf("expected Description 'My security group', got %s", req.Description)
	}
	if req.VPCID != "vpc-123" {
		t.Errorf("expected VPCID vpc-123, got %s", req.VPCID)
	}
	if req.Tags["env"] != "prod" {
		t.Errorf("expected tag env=prod, got %v", req.Tags)
	}
}

func TestSecurityGroupModelToUpdateRequest(t *testing.T) {
	ctx := context.Background()
	tags, _ := types.MapValueFrom(ctx, types.StringType, map[string]string{"env": "staging"})

	model := SecurityGroupModel{
		Name:        types.StringValue("updated-sg"),
		Description: types.StringValue("Updated"),
		Tags:        tags,
	}

	var diags diag.Diagnostics
	req := model.toUpdateRequest(ctx, &diags)

	if diags.HasError() {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}

	if *req.Name != "updated-sg" {
		t.Errorf("expected Name updated-sg, got %s", *req.Name)
	}
	if *req.Description != "Updated" {
		t.Errorf("expected Description 'Updated', got %s", *req.Description)
	}
	if req.Tags["env"] != "staging" {
		t.Errorf("expected tag env=staging, got %v", req.Tags)
	}
}

func TestSecurityGroupResourceCRUD(t *testing.T) {
	sgCreated := apiSecurityGroup{
		ID:        "sg-test-1",
		Name:      "test-sg",
		VPCID:     "vpc-123",
		IsDefault: false,
		CreatedAt: "2025-01-01T00:00:00Z",
	}

	sgUpdated := apiSecurityGroup{
		ID:          "sg-test-1",
		Name:        "updated-sg",
		Description: "Updated",
		VPCID:       "vpc-123",
		IsDefault:   false,
		CreatedAt:   "2025-01-01T00:00:00Z",
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/tenants/t-123/security-groups":
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(sgCreated)

		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-123/security-groups/sg-test-1":
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(sgCreated)

		case r.Method == http.MethodPatch && r.URL.Path == "/v1/tenants/t-123/security-groups/sg-test-1":
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(sgUpdated)

		case r.Method == http.MethodDelete && r.URL.Path == "/v1/tenants/t-123/security-groups/sg-test-1":
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
	createReq := apiCreateSecurityGroupRequest{
		Name:  "test-sg",
		VPCID: "vpc-123",
	}
	apiResp, err := c.Post(ctx, c.TenantPath("/security-groups"), createReq)
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}
	if apiResp.StatusCode != http.StatusCreated {
		t.Errorf("expected status 201, got %d", apiResp.StatusCode)
	}

	var created apiSecurityGroup
	if err := json.Unmarshal(apiResp.Body, &created); err != nil {
		t.Fatalf("failed to parse create response: %v", err)
	}
	if created.ID != "sg-test-1" {
		t.Errorf("expected ID sg-test-1, got %s", created.ID)
	}

	// Test Read
	readResp, err := c.Get(ctx, c.TenantPath("/security-groups/sg-test-1"), nil)
	if err != nil {
		t.Fatalf("Read failed: %v", err)
	}
	var read apiSecurityGroup
	if err := json.Unmarshal(readResp.Body, &read); err != nil {
		t.Fatalf("failed to parse read response: %v", err)
	}
	if read.Name != "test-sg" {
		t.Errorf("expected Name test-sg, got %s", read.Name)
	}

	// Test Update
	updateReq := apiUpdateSecurityGroupRequest{}
	name := "updated-sg"
	updateReq.Name = &name
	patchResp, err := c.Patch(ctx, c.TenantPath("/security-groups/sg-test-1"), updateReq)
	if err != nil {
		t.Fatalf("Update failed: %v", err)
	}
	var updated apiSecurityGroup
	if err := json.Unmarshal(patchResp.Body, &updated); err != nil {
		t.Fatalf("failed to parse update response: %v", err)
	}
	if updated.Name != "updated-sg" {
		t.Errorf("expected Name updated-sg, got %s", updated.Name)
	}

	// Test Delete
	_, err = c.Delete(ctx, c.TenantPath("/security-groups/sg-test-1"))
	if err != nil {
		t.Fatalf("Delete failed: %v", err)
	}
}

func TestSecurityGroupReadNotFound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"error": map[string]string{"code": "NOT_FOUND", "message": "security group not found"},
		})
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key") // pragma: allowlist secret
	c.SetTenantIDForTest("t-123")

	_, err := c.Get(context.Background(), c.TenantPath("/security-groups/nonexistent"), nil)
	if err == nil {
		t.Fatal("expected error for not found")
	}
	if !client.IsNotFound(err) {
		t.Errorf("expected not found error, got %v", err)
	}
}
