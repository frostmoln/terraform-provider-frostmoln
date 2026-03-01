package floating_ip

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

func TestFloatingIPModelFromAPI(t *testing.T) {
	fip := &apiFloatingIP{
		ID:         "fip-123",
		Address:    "203.0.113.10",
		Region:     "eu-north-1",
		Status:     "active",
		InstanceID: "inst-456",
		PrivateIP:  "10.0.1.5",
		Tags:       map[string]string{"env": "test"},
		CreatedAt:  "2025-01-01T00:00:00Z",
	}

	var model FloatingIPModel
	var diags diag.Diagnostics
	model.fromAPI(context.Background(), fip, &diags)

	if diags.HasError() {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}

	if model.ID.ValueString() != "fip-123" {
		t.Errorf("expected ID fip-123, got %s", model.ID.ValueString())
	}
	if model.Address.ValueString() != "203.0.113.10" {
		t.Errorf("expected Address 203.0.113.10, got %s", model.Address.ValueString())
	}
	if model.Region.ValueString() != "eu-north-1" {
		t.Errorf("expected Region eu-north-1, got %s", model.Region.ValueString())
	}
	if model.Status.ValueString() != "active" {
		t.Errorf("expected Status active, got %s", model.Status.ValueString())
	}
	if model.InstanceID.ValueString() != "inst-456" {
		t.Errorf("expected InstanceID inst-456, got %s", model.InstanceID.ValueString())
	}
	if model.PrivateIP.ValueString() != "10.0.1.5" {
		t.Errorf("expected PrivateIP 10.0.1.5, got %s", model.PrivateIP.ValueString())
	}
	if model.CreatedAt.ValueString() != "2025-01-01T00:00:00Z" {
		t.Errorf("expected CreatedAt 2025-01-01T00:00:00Z, got %s", model.CreatedAt.ValueString())
	}
}

func TestFloatingIPModelFromAPIMinimal(t *testing.T) {
	fip := &apiFloatingIP{
		ID:        "fip-789",
		Address:   "203.0.113.20",
		Region:    "eu-west-1",
		Status:    "available",
		CreatedAt: "2025-01-01T00:00:00Z",
	}

	var model FloatingIPModel
	model.Tags = types.MapNull(types.StringType)

	var diags diag.Diagnostics
	model.fromAPI(context.Background(), fip, &diags)

	if diags.HasError() {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}

	if !model.InstanceID.IsNull() {
		t.Errorf("expected InstanceID to be null, got %s", model.InstanceID.ValueString())
	}
	if !model.PrivateIP.IsNull() {
		t.Errorf("expected PrivateIP to be null, got %s", model.PrivateIP.ValueString())
	}
	if !model.Tags.IsNull() {
		t.Error("expected Tags to be null")
	}
}

func TestFloatingIPModelToAllocateRequest(t *testing.T) {
	ctx := context.Background()
	tags, _ := types.MapValueFrom(ctx, types.StringType, map[string]string{"env": "prod"})

	model := FloatingIPModel{
		Region: types.StringValue("eu-north-1"),
		Tags:   tags,
	}

	var diags diag.Diagnostics
	req := model.toAllocateRequest(ctx, &diags)

	if diags.HasError() {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}

	if req.Region != "eu-north-1" {
		t.Errorf("expected Region eu-north-1, got %s", req.Region)
	}
	if req.Tags["env"] != "prod" {
		t.Errorf("expected tag env=prod, got %v", req.Tags)
	}
}

func TestFloatingIPModelToAllocateRequestMinimal(t *testing.T) {
	model := FloatingIPModel{
		Region: types.StringNull(),
		Tags:   types.MapNull(types.StringType),
	}

	var diags diag.Diagnostics
	req := model.toAllocateRequest(context.Background(), &diags)

	if diags.HasError() {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}

	if req.Region != "" {
		t.Errorf("expected Region empty, got %s", req.Region)
	}
	if req.Tags != nil {
		t.Errorf("expected Tags nil, got %v", req.Tags)
	}
}

func TestFloatingIPResourceCRUD(t *testing.T) {
	fipData := apiFloatingIP{
		ID:        "fip-test-1",
		Address:   "203.0.113.50",
		Region:    "eu-north-1",
		Status:    "available",
		CreatedAt: "2025-01-01T00:00:00Z",
	}

	fipAssociated := apiFloatingIP{
		ID:         "fip-test-1",
		Address:    "203.0.113.50",
		Region:     "eu-north-1",
		Status:     "active",
		InstanceID: "inst-123",
		PrivateIP:  "10.0.1.5",
		CreatedAt:  "2025-01-01T00:00:00Z",
	}

	fipDisassociated := apiFloatingIP{
		ID:        "fip-test-1",
		Address:   "203.0.113.50",
		Region:    "eu-north-1",
		Status:    "available",
		CreatedAt: "2025-01-01T00:00:00Z",
	}

	associated := false

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/tenants/t-123/floating-ips":
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(fipData)

		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-123/floating-ips/fip-test-1":
			w.WriteHeader(http.StatusOK)
			if associated {
				json.NewEncoder(w).Encode(fipAssociated)
			} else {
				json.NewEncoder(w).Encode(fipDisassociated)
			}

		case r.Method == http.MethodPost && r.URL.Path == "/v1/tenants/t-123/floating-ips/fip-test-1/associate":
			associated = true
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(fipAssociated)

		case r.Method == http.MethodPost && r.URL.Path == "/v1/tenants/t-123/floating-ips/fip-test-1/disassociate":
			associated = false
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(fipDisassociated)

		case r.Method == http.MethodPatch && r.URL.Path == "/v1/tenants/t-123/floating-ips/fip-test-1":
			w.WriteHeader(http.StatusOK)
			if associated {
				json.NewEncoder(w).Encode(fipAssociated)
			} else {
				json.NewEncoder(w).Encode(fipDisassociated)
			}

		case r.Method == http.MethodDelete && r.URL.Path == "/v1/tenants/t-123/floating-ips/fip-test-1":
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

	// Test Allocate
	allocateReq := apiAllocateFloatingIPRequest{Region: "eu-north-1"}
	apiResp, err := c.Post(ctx, c.TenantPath("/floating-ips"), allocateReq)
	if err != nil {
		t.Fatalf("Allocate failed: %v", err)
	}
	if apiResp.StatusCode != http.StatusCreated {
		t.Errorf("expected status 201, got %d", apiResp.StatusCode)
	}

	var allocated apiFloatingIP
	if err := json.Unmarshal(apiResp.Body, &allocated); err != nil {
		t.Fatalf("failed to parse allocate response: %v", err)
	}
	if allocated.ID != "fip-test-1" {
		t.Errorf("expected ID fip-test-1, got %s", allocated.ID)
	}
	if allocated.Address != "203.0.113.50" {
		t.Errorf("expected Address 203.0.113.50, got %s", allocated.Address)
	}

	// Test Associate
	assocReq := apiAssociateFloatingIPRequest{InstanceID: "inst-123"}
	assocResp, err := c.Post(ctx, c.TenantPath("/floating-ips/fip-test-1/associate"), assocReq)
	if err != nil {
		t.Fatalf("Associate failed: %v", err)
	}
	var assocFIP apiFloatingIP
	if err := json.Unmarshal(assocResp.Body, &assocFIP); err != nil {
		t.Fatalf("failed to parse associate response: %v", err)
	}
	if assocFIP.InstanceID != "inst-123" {
		t.Errorf("expected InstanceID inst-123, got %s", assocFIP.InstanceID)
	}

	// Test Disassociate
	_, err = c.Post(ctx, c.TenantPath("/floating-ips/fip-test-1/disassociate"), nil)
	if err != nil {
		t.Fatalf("Disassociate failed: %v", err)
	}

	// Verify disassociated
	readResp, err := c.Get(ctx, c.TenantPath("/floating-ips/fip-test-1"), nil)
	if err != nil {
		t.Fatalf("Read failed: %v", err)
	}
	var readFIP apiFloatingIP
	if err := json.Unmarshal(readResp.Body, &readFIP); err != nil {
		t.Fatalf("failed to parse read response: %v", err)
	}
	if readFIP.InstanceID != "" {
		t.Errorf("expected InstanceID empty after disassociate, got %s", readFIP.InstanceID)
	}

	// Test Delete
	_, err = c.Delete(ctx, c.TenantPath("/floating-ips/fip-test-1"))
	if err != nil {
		t.Fatalf("Delete failed: %v", err)
	}
}

func TestFloatingIPReadNotFound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"error": map[string]string{"code": "NOT_FOUND", "message": "floating IP not found"},
		})
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key") // pragma: allowlist secret
	c.SetTenantIDForTest("t-123")

	_, err := c.Get(context.Background(), c.TenantPath("/floating-ips/nonexistent"), nil)
	if err == nil {
		t.Fatal("expected error for not found")
	}
	if !client.IsNotFound(err) {
		t.Errorf("expected not found error, got %v", err)
	}
}
