package floating_ip

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
)

// writeInstanceWithPort encodes the subset of the instance read response that
// resolvePortID consumes: networks[0].portId. The backend resolves the Neutron
// port from the instance, so association tests stub the instance GET.
func writeInstanceWithPort(w http.ResponseWriter, instanceID, portID string) {
	_ = json.NewEncoder(w).Encode(map[string]any{
		"id":       instanceID,
		"networks": []map[string]any{{"portId": portID}},
	})
}

func TestFloatingIPModelFromAPI(t *testing.T) {
	fip := &apiFloatingIP{
		ID:        "fip-123",
		Address:   "203.0.113.10",
		Status:    "active",
		PortID:    "port-9",
		PrivateIP: "10.0.1.5",
		Tags:      map[string]string{"env": "test"},
		CreatedAt: "2025-01-01T00:00:00Z",
	}

	var model FloatingIPModel
	// instance_id is preserved from config (never read from the wire); while the
	// FIP is attached (portId present) the configured value is kept.
	model.InstanceID = types.StringValue("inst-456")

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
	if model.Status.ValueString() != "active" {
		t.Errorf("expected Status active, got %s", model.Status.ValueString())
	}
	if model.InstanceID.ValueString() != "inst-456" {
		t.Errorf("expected InstanceID preserved as inst-456, got %s", model.InstanceID.ValueString())
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
		Status:    "available",
		CreatedAt: "2025-01-01T00:00:00Z",
	}

	var model FloatingIPModel
	// A detached FIP (no portId) clears any previously-configured instance_id.
	model.InstanceID = types.StringValue("inst-stale")
	model.Tags = types.MapNull(types.StringType)

	var diags diag.Diagnostics
	model.fromAPI(context.Background(), fip, &diags)

	if diags.HasError() {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}

	if !model.InstanceID.IsNull() {
		t.Errorf("expected InstanceID to be cleared (no portId), got %s", model.InstanceID.ValueString())
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
		Tags: tags,
	}

	var diags diag.Diagnostics
	req := model.toAllocateRequest(ctx, &diags)

	if diags.HasError() {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}

	if req.Tags["env"] != "prod" {
		t.Errorf("expected tag env=prod, got %v", req.Tags)
	}
}

func TestFloatingIPModelToAllocateRequestMinimal(t *testing.T) {
	model := FloatingIPModel{
		Tags: types.MapNull(types.StringType),
	}

	var diags diag.Diagnostics
	req := model.toAllocateRequest(context.Background(), &diags)

	if diags.HasError() {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}

	if req.Tags != nil {
		t.Errorf("expected Tags nil, got %v", req.Tags)
	}
}

// TestFloatingIPAllocateRequestWireContract locks the allocate request to the
// backend contract: region is not part of the wire body (ADR-0022), only tags.
func TestFloatingIPAllocateRequestWireContract(t *testing.T) {
	ctx := context.Background()
	tags, _ := types.MapValueFrom(ctx, types.StringType, map[string]string{"env": "prod"})
	model := FloatingIPModel{Tags: tags}

	var diags diag.Diagnostics
	req := model.toAllocateRequest(ctx, &diags)
	if diags.HasError() {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}

	raw, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}
	var wire map[string]any
	if err := json.Unmarshal(raw, &wire); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}
	if _, ok := wire["region"]; ok {
		t.Errorf("allocate request must not carry a region field, got %s", raw)
	}
}

// TestFloatingIPAssociateRequestWireContract locks the associate request to the
// backend contract: it carries portId, never instanceId.
func TestFloatingIPAssociateRequestWireContract(t *testing.T) {
	raw, err := json.Marshal(apiAssociateFloatingIPRequest{PortID: "port-abc"})
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}
	var wire map[string]any
	if err := json.Unmarshal(raw, &wire); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}
	if wire["portId"] != "port-abc" {
		t.Errorf("expected portId=port-abc, got %s", raw)
	}
	if _, ok := wire["instanceId"]; ok {
		t.Errorf("associate request must not carry an instanceId field, got %s", raw)
	}
}

func TestFloatingIPResourceCRUD(t *testing.T) {
	fipData := apiFloatingIP{
		ID:        "fip-test-1",
		Address:   "203.0.113.50",
		Status:    "available",
		CreatedAt: "2025-01-01T00:00:00Z",
	}

	fipAssociated := apiFloatingIP{
		ID:        "fip-test-1",
		Address:   "203.0.113.50",
		Status:    "active",
		PortID:    "port-abc",
		PrivateIP: "10.0.1.5",
		CreatedAt: "2025-01-01T00:00:00Z",
	}

	fipDisassociated := apiFloatingIP{
		ID:        "fip-test-1",
		Address:   "203.0.113.50",
		Status:    "available",
		CreatedAt: "2025-01-01T00:00:00Z",
	}

	associated := false

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/tenants/t-123/floating-ips":
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(fipData)

		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-123/floating-ips/fip-test-1":
			w.WriteHeader(http.StatusOK)
			if associated {
				_ = json.NewEncoder(w).Encode(fipAssociated)
			} else {
				_ = json.NewEncoder(w).Encode(fipDisassociated)
			}

		case r.Method == http.MethodPost && r.URL.Path == "/v1/tenants/t-123/floating-ips/fip-test-1/associate":
			associated = true
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(fipAssociated)

		case r.Method == http.MethodPost && r.URL.Path == "/v1/tenants/t-123/floating-ips/fip-test-1/disassociate":
			associated = false
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(fipDisassociated)

		case r.Method == http.MethodPatch && r.URL.Path == "/v1/tenants/t-123/floating-ips/fip-test-1":
			w.WriteHeader(http.StatusOK)
			if associated {
				_ = json.NewEncoder(w).Encode(fipAssociated)
			} else {
				_ = json.NewEncoder(w).Encode(fipDisassociated)
			}

		case r.Method == http.MethodDelete && r.URL.Path == "/v1/tenants/t-123/floating-ips/fip-test-1":
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

	// Test Allocate (allocate request only carries tags; no region).
	allocateReq := apiAllocateFloatingIPRequest{}
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

	// Test Associate (by resolved portId).
	assocReq := apiAssociateFloatingIPRequest{PortID: "port-abc"}
	assocResp, err := c.Post(ctx, c.TenantPath("/floating-ips/fip-test-1/associate"), assocReq)
	if err != nil {
		t.Fatalf("Associate failed: %v", err)
	}
	var assocFIP apiFloatingIP
	if err := json.Unmarshal(assocResp.Body, &assocFIP); err != nil {
		t.Fatalf("failed to parse associate response: %v", err)
	}
	if assocFIP.PortID != "port-abc" {
		t.Errorf("expected PortID port-abc, got %s", assocFIP.PortID)
	}

	// Test Disassociate.
	_, err = c.Post(ctx, c.TenantPath("/floating-ips/fip-test-1/disassociate"), nil)
	if err != nil {
		t.Fatalf("Disassociate failed: %v", err)
	}

	// Verify disassociated (no portId on the wire).
	readResp, err := c.Get(ctx, c.TenantPath("/floating-ips/fip-test-1"), nil)
	if err != nil {
		t.Fatalf("Read failed: %v", err)
	}
	var readFIP apiFloatingIP
	if err := json.Unmarshal(readResp.Body, &readFIP); err != nil {
		t.Fatalf("failed to parse read response: %v", err)
	}
	if readFIP.PortID != "" {
		t.Errorf("expected PortID empty after disassociate, got %s", readFIP.PortID)
	}

	// Test Delete.
	_, err = c.Delete(ctx, c.TenantPath("/floating-ips/fip-test-1"))
	if err != nil {
		t.Fatalf("Delete failed: %v", err)
	}
}

func TestFloatingIPReadNotFound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
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

// --- tfsdk-level resource method tests ---

func fipSchema(t *testing.T) schema.Schema {
	t.Helper()
	r := NewResource()
	resp := &resource.SchemaResponse{}
	r.Schema(context.Background(), resource.SchemaRequest{}, resp)
	return resp.Schema
}

func fipObjectType() tftypes.Object {
	return tftypes.Object{
		AttributeTypes: map[string]tftypes.Type{
			"id":          tftypes.String,
			"address":     tftypes.String,
			"instance_id": tftypes.String,
			"tags":        tftypes.Map{ElementType: tftypes.String},
			"status":      tftypes.String,
			"private_ip":  tftypes.String,
			"created_at":  tftypes.String,
		},
	}
}

func TestFIPNewResource(t *testing.T) {
	r := NewResource()
	if r == nil {
		t.Fatal("expected non-nil resource")
	}
}

func TestFIPMetadata(t *testing.T) {
	r := NewResource()
	req := resource.MetadataRequest{ProviderTypeName: "frostmoln"}
	resp := &resource.MetadataResponse{}
	r.Metadata(context.Background(), req, resp)

	if resp.TypeName != "frostmoln_floating_ip" {
		t.Errorf("expected type name frostmoln_floating_ip, got %s", resp.TypeName)
	}
}

func TestFIPSchema(t *testing.T) {
	r := NewResource()
	resp := &resource.SchemaResponse{}
	r.Schema(context.Background(), resource.SchemaRequest{}, resp)

	for _, attr := range []string{"id", "address", "instance_id", "tags", "status", "private_ip", "created_at"} {
		if _, ok := resp.Schema.Attributes[attr]; !ok {
			t.Errorf("expected attribute %s in schema", attr)
		}
	}
	if _, ok := resp.Schema.Attributes["region"]; ok {
		t.Error("did not expect a region attribute in schema")
	}
}

func TestFIPConfigureNilProviderData(t *testing.T) {
	r := NewResource()
	resp := &resource.ConfigureResponse{}
	r.(resource.ResourceWithConfigure).Configure(context.Background(), resource.ConfigureRequest{ProviderData: nil}, resp)
	if resp.Diagnostics.HasError() {
		t.Errorf("expected no errors, got %v", resp.Diagnostics)
	}
}

func TestFIPConfigureWrongType(t *testing.T) {
	r := NewResource()
	resp := &resource.ConfigureResponse{}
	r.(resource.ResourceWithConfigure).Configure(context.Background(), resource.ConfigureRequest{ProviderData: "bad"}, resp)
	if !resp.Diagnostics.HasError() {
		t.Error("expected error for wrong type")
	}
}

func TestFIPConfigureValidClient(t *testing.T) {
	r := NewResource()
	c := client.NewClient("http://localhost", "test-key") // pragma: allowlist secret
	resp := &resource.ConfigureResponse{}
	r.(resource.ResourceWithConfigure).Configure(context.Background(), resource.ConfigureRequest{ProviderData: c}, resp)
	if resp.Diagnostics.HasError() {
		t.Errorf("expected no errors, got %v", resp.Diagnostics)
	}
}

func TestFIPResourceCreate(t *testing.T) {
	fipResp := apiFloatingIP{
		ID:        "fip-new-1",
		Address:   "203.0.113.100",
		Status:    "available",
		CreatedAt: "2025-06-01T12:00:00Z",
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/tenants/t-123/floating-ips":
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(fipResp)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-123/floating-ips/fip-new-1":
			_ = json.NewEncoder(w).Encode(fipResp)
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

	r := NewResource()
	r.(resource.ResourceWithConfigure).Configure(context.Background(), resource.ConfigureRequest{ProviderData: c}, &resource.ConfigureResponse{})

	s := fipSchema(t)
	planVal := tftypes.NewValue(fipObjectType(), map[string]tftypes.Value{
		"id":          tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"address":     tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"instance_id": tftypes.NewValue(tftypes.String, nil),
		"tags":        tftypes.NewValue(tftypes.Map{ElementType: tftypes.String}, nil),
		"status":      tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"private_ip":  tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"created_at":  tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
	})

	plan := tfsdk.Plan{Schema: s, Raw: planVal}
	resp := &resource.CreateResponse{State: tfsdk.State{Schema: s}}
	r.Create(context.Background(), resource.CreateRequest{Plan: plan}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected errors: %v", resp.Diagnostics)
	}

	var state FloatingIPModel
	resp.State.Get(context.Background(), &state)

	if state.ID.ValueString() != "fip-new-1" {
		t.Errorf("expected ID fip-new-1, got %s", state.ID.ValueString())
	}
	if state.Address.ValueString() != "203.0.113.100" {
		t.Errorf("expected Address 203.0.113.100, got %s", state.Address.ValueString())
	}
	if state.Status.ValueString() != "available" {
		t.Errorf("expected Status available, got %s", state.Status.ValueString())
	}
}

func TestFIPResourceCreateWithAssociation(t *testing.T) {
	fipAllocated := apiFloatingIP{
		ID:        "fip-assoc-1",
		Address:   "203.0.113.101",
		Status:    "available",
		CreatedAt: "2025-06-01T12:00:00Z",
	}

	fipAssociated := apiFloatingIP{
		ID:        "fip-assoc-1",
		Address:   "203.0.113.101",
		Status:    "active",
		PortID:    "port-abc",
		PrivateIP: "10.0.1.5",
		CreatedAt: "2025-06-01T12:00:00Z",
	}

	var associateBody apiAssociateFloatingIPRequest

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/tenants/t-123/floating-ips":
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(fipAllocated)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-123/instances/inst-123":
			// resolvePortID reads the instance's first network port.
			writeInstanceWithPort(w, "inst-123", "port-abc")
		case r.Method == http.MethodPost && r.URL.Path == "/v1/tenants/t-123/floating-ips/fip-assoc-1/associate":
			_ = json.NewDecoder(r.Body).Decode(&associateBody)
			_ = json.NewEncoder(w).Encode(fipAssociated)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-123/floating-ips/fip-assoc-1":
			// Final read after the (sync, in this mock) associate -> associated state.
			_ = json.NewEncoder(w).Encode(fipAssociated)
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

	r := NewResource()
	r.(resource.ResourceWithConfigure).Configure(context.Background(), resource.ConfigureRequest{ProviderData: c}, &resource.ConfigureResponse{})

	s := fipSchema(t)
	planVal := tftypes.NewValue(fipObjectType(), map[string]tftypes.Value{
		"id":          tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"address":     tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"instance_id": tftypes.NewValue(tftypes.String, "inst-123"),
		"tags":        tftypes.NewValue(tftypes.Map{ElementType: tftypes.String}, nil),
		"status":      tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"private_ip":  tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"created_at":  tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
	})

	plan := tfsdk.Plan{Schema: s, Raw: planVal}
	resp := &resource.CreateResponse{State: tfsdk.State{Schema: s}}
	r.Create(context.Background(), resource.CreateRequest{Plan: plan}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected errors: %v", resp.Diagnostics)
	}

	// The provider must resolve the instance to its port and associate by portId.
	if associateBody.PortID != "port-abc" {
		t.Errorf("expected associate request portId=port-abc, got %q", associateBody.PortID)
	}

	var state FloatingIPModel
	resp.State.Get(context.Background(), &state)

	if state.InstanceID.ValueString() != "inst-123" {
		t.Errorf("expected InstanceID inst-123, got %s", state.InstanceID.ValueString())
	}
	if state.Status.ValueString() != "active" {
		t.Errorf("expected Status active, got %s", state.Status.ValueString())
	}
}

func TestFIPResourceRead(t *testing.T) {
	fipResp := apiFloatingIP{
		ID:        "fip-read-1",
		Address:   "203.0.113.50",
		Status:    "available",
		CreatedAt: "2025-06-01T12:00:00Z",
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-123/floating-ips/fip-read-1" {
			_ = json.NewEncoder(w).Encode(fipResp)
			return
		}
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"error": map[string]string{"code": "NOT_FOUND", "message": "not found"},
		})
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key") // pragma: allowlist secret
	c.SetTenantIDForTest("t-123")

	r := NewResource()
	r.(resource.ResourceWithConfigure).Configure(context.Background(), resource.ConfigureRequest{ProviderData: c}, &resource.ConfigureResponse{})

	s := fipSchema(t)
	stateVal := tftypes.NewValue(fipObjectType(), map[string]tftypes.Value{
		"id":          tftypes.NewValue(tftypes.String, "fip-read-1"),
		"address":     tftypes.NewValue(tftypes.String, "203.0.113.50"),
		"instance_id": tftypes.NewValue(tftypes.String, nil),
		"tags":        tftypes.NewValue(tftypes.Map{ElementType: tftypes.String}, nil),
		"status":      tftypes.NewValue(tftypes.String, "available"),
		"private_ip":  tftypes.NewValue(tftypes.String, nil),
		"created_at":  tftypes.NewValue(tftypes.String, "2025-06-01T12:00:00Z"),
	})

	state := tfsdk.State{Schema: s, Raw: stateVal}
	resp := &resource.ReadResponse{State: state}
	r.Read(context.Background(), resource.ReadRequest{State: state}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected errors: %v", resp.Diagnostics)
	}

	var model FloatingIPModel
	resp.State.Get(context.Background(), &model)
	if model.Address.ValueString() != "203.0.113.50" {
		t.Errorf("expected Address 203.0.113.50, got %s", model.Address.ValueString())
	}
}

// TestFIPResourceReadParsesWireContract proves the read parses the backend's
// floatingIpAddress / fixedIpAddress / portId fields, and that a present portId
// preserves the configured instance_id.
func TestFIPResourceReadParsesWireContract(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-123/floating-ips/fip-wire-1" {
			_, _ = w.Write([]byte(`{
				"id": "fip-wire-1",
				"floatingIpAddress": "203.0.113.77",
				"status": "active",
				"portId": "port-zzz",
				"fixedIpAddress": "10.0.5.9",
				"createdAt": "2025-06-01T12:00:00Z"
			}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key") // pragma: allowlist secret
	c.SetTenantIDForTest("t-123")

	r := NewResource()
	r.(resource.ResourceWithConfigure).Configure(context.Background(), resource.ConfigureRequest{ProviderData: c}, &resource.ConfigureResponse{})

	s := fipSchema(t)
	stateVal := tftypes.NewValue(fipObjectType(), map[string]tftypes.Value{
		"id":          tftypes.NewValue(tftypes.String, "fip-wire-1"),
		"address":     tftypes.NewValue(tftypes.String, "203.0.113.77"),
		"instance_id": tftypes.NewValue(tftypes.String, "inst-kept"),
		"tags":        tftypes.NewValue(tftypes.Map{ElementType: tftypes.String}, nil),
		"status":      tftypes.NewValue(tftypes.String, "active"),
		"private_ip":  tftypes.NewValue(tftypes.String, "10.0.5.9"),
		"created_at":  tftypes.NewValue(tftypes.String, "2025-06-01T12:00:00Z"),
	})

	state := tfsdk.State{Schema: s, Raw: stateVal}
	resp := &resource.ReadResponse{State: state}
	r.Read(context.Background(), resource.ReadRequest{State: state}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected errors: %v", resp.Diagnostics)
	}

	var model FloatingIPModel
	resp.State.Get(context.Background(), &model)
	if model.Address.ValueString() != "203.0.113.77" {
		t.Errorf("expected Address from floatingIpAddress, got %s", model.Address.ValueString())
	}
	if model.PrivateIP.ValueString() != "10.0.5.9" {
		t.Errorf("expected PrivateIP from fixedIpAddress, got %s", model.PrivateIP.ValueString())
	}
	if model.InstanceID.ValueString() != "inst-kept" {
		t.Errorf("expected instance_id preserved (portId present), got %s", model.InstanceID.ValueString())
	}
}

func TestFIPResourceReadNotFoundRemovesState(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"error": map[string]string{"code": "NOT_FOUND", "message": "not found"},
		})
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key") // pragma: allowlist secret
	c.SetTenantIDForTest("t-123")

	r := NewResource()
	r.(resource.ResourceWithConfigure).Configure(context.Background(), resource.ConfigureRequest{ProviderData: c}, &resource.ConfigureResponse{})

	s := fipSchema(t)
	stateVal := tftypes.NewValue(fipObjectType(), map[string]tftypes.Value{
		"id":          tftypes.NewValue(tftypes.String, "fip-gone"),
		"address":     tftypes.NewValue(tftypes.String, "203.0.113.99"),
		"instance_id": tftypes.NewValue(tftypes.String, nil),
		"tags":        tftypes.NewValue(tftypes.Map{ElementType: tftypes.String}, nil),
		"status":      tftypes.NewValue(tftypes.String, "available"),
		"private_ip":  tftypes.NewValue(tftypes.String, nil),
		"created_at":  tftypes.NewValue(tftypes.String, "2025-01-01T00:00:00Z"),
	})

	state := tfsdk.State{Schema: s, Raw: stateVal}
	resp := &resource.ReadResponse{State: state}
	r.Read(context.Background(), resource.ReadRequest{State: state}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected errors: %v", resp.Diagnostics)
	}

	if !resp.State.Raw.IsNull() {
		t.Error("expected state to be null after not found")
	}
}

func TestFIPResourceUpdate(t *testing.T) {
	associated := false
	fipResp := apiFloatingIP{
		ID:        "fip-upd-1",
		Address:   "203.0.113.50",
		Status:    "available",
		CreatedAt: "2025-06-01T12:00:00Z",
	}
	fipAssocResp := apiFloatingIP{
		ID:        "fip-upd-1",
		Address:   "203.0.113.50",
		Status:    "active",
		PortID:    "port-new",
		PrivateIP: "10.0.1.10",
		CreatedAt: "2025-06-01T12:00:00Z",
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/tenants/t-123/floating-ips/fip-upd-1/disassociate":
			associated = false
			_ = json.NewEncoder(w).Encode(fipResp)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-123/instances/inst-new":
			writeInstanceWithPort(w, "inst-new", "port-new")
		case r.Method == http.MethodPost && r.URL.Path == "/v1/tenants/t-123/floating-ips/fip-upd-1/associate":
			associated = true
			_ = json.NewEncoder(w).Encode(fipAssocResp)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-123/floating-ips/fip-upd-1":
			if associated {
				_ = json.NewEncoder(w).Encode(fipAssocResp)
			} else {
				_ = json.NewEncoder(w).Encode(fipResp)
			}
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

	r := NewResource()
	r.(resource.ResourceWithConfigure).Configure(context.Background(), resource.ConfigureRequest{ProviderData: c}, &resource.ConfigureResponse{})

	s := fipSchema(t)

	// State: previously associated with inst-old
	stateVal := tftypes.NewValue(fipObjectType(), map[string]tftypes.Value{
		"id":          tftypes.NewValue(tftypes.String, "fip-upd-1"),
		"address":     tftypes.NewValue(tftypes.String, "203.0.113.50"),
		"instance_id": tftypes.NewValue(tftypes.String, "inst-old"),
		"tags":        tftypes.NewValue(tftypes.Map{ElementType: tftypes.String}, nil),
		"status":      tftypes.NewValue(tftypes.String, "active"),
		"private_ip":  tftypes.NewValue(tftypes.String, "10.0.1.5"),
		"created_at":  tftypes.NewValue(tftypes.String, "2025-06-01T12:00:00Z"),
	})

	// Plan: change association to inst-new
	planVal := tftypes.NewValue(fipObjectType(), map[string]tftypes.Value{
		"id":          tftypes.NewValue(tftypes.String, "fip-upd-1"),
		"address":     tftypes.NewValue(tftypes.String, "203.0.113.50"),
		"instance_id": tftypes.NewValue(tftypes.String, "inst-new"),
		"tags":        tftypes.NewValue(tftypes.Map{ElementType: tftypes.String}, nil),
		"status":      tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"private_ip":  tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"created_at":  tftypes.NewValue(tftypes.String, "2025-06-01T12:00:00Z"),
	})

	state := tfsdk.State{Schema: s, Raw: stateVal}
	plan := tfsdk.Plan{Schema: s, Raw: planVal}
	resp := &resource.UpdateResponse{State: tfsdk.State{Schema: s}}
	r.Update(context.Background(), resource.UpdateRequest{Plan: plan, State: state}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected errors: %v", resp.Diagnostics)
	}

	var model FloatingIPModel
	resp.State.Get(context.Background(), &model)
	if model.InstanceID.ValueString() != "inst-new" {
		t.Errorf("expected InstanceID inst-new, got %s", model.InstanceID.ValueString())
	}
	if model.Status.ValueString() != "active" {
		t.Errorf("expected Status active, got %s", model.Status.ValueString())
	}
}

func TestFIPResourceDelete(t *testing.T) {
	deleted := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete && r.URL.Path == "/v1/tenants/t-123/floating-ips/fip-del-1" {
			deleted = true
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"error": map[string]string{"code": "NOT_FOUND", "message": "not found"},
		})
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key") // pragma: allowlist secret
	c.SetTenantIDForTest("t-123")

	r := NewResource()
	r.(resource.ResourceWithConfigure).Configure(context.Background(), resource.ConfigureRequest{ProviderData: c}, &resource.ConfigureResponse{})

	s := fipSchema(t)
	stateVal := tftypes.NewValue(fipObjectType(), map[string]tftypes.Value{
		"id":          tftypes.NewValue(tftypes.String, "fip-del-1"),
		"address":     tftypes.NewValue(tftypes.String, "203.0.113.50"),
		"instance_id": tftypes.NewValue(tftypes.String, nil),
		"tags":        tftypes.NewValue(tftypes.Map{ElementType: tftypes.String}, nil),
		"status":      tftypes.NewValue(tftypes.String, "available"),
		"private_ip":  tftypes.NewValue(tftypes.String, nil),
		"created_at":  tftypes.NewValue(tftypes.String, "2025-01-01T00:00:00Z"),
	})

	state := tfsdk.State{Schema: s, Raw: stateVal}
	resp := &resource.DeleteResponse{}
	r.Delete(context.Background(), resource.DeleteRequest{State: state}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected errors: %v", resp.Diagnostics)
	}
	if !deleted {
		t.Error("expected delete to be called")
	}
}

func TestFIPResourceDeleteAlreadyGone(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"error": map[string]string{"code": "NOT_FOUND", "message": "not found"},
		})
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key") // pragma: allowlist secret
	c.SetTenantIDForTest("t-123")

	r := NewResource()
	r.(resource.ResourceWithConfigure).Configure(context.Background(), resource.ConfigureRequest{ProviderData: c}, &resource.ConfigureResponse{})

	s := fipSchema(t)
	stateVal := tftypes.NewValue(fipObjectType(), map[string]tftypes.Value{
		"id":          tftypes.NewValue(tftypes.String, "fip-already-gone"),
		"address":     tftypes.NewValue(tftypes.String, "203.0.113.99"),
		"instance_id": tftypes.NewValue(tftypes.String, nil),
		"tags":        tftypes.NewValue(tftypes.Map{ElementType: tftypes.String}, nil),
		"status":      tftypes.NewValue(tftypes.String, "available"),
		"private_ip":  tftypes.NewValue(tftypes.String, nil),
		"created_at":  tftypes.NewValue(tftypes.String, "2025-01-01T00:00:00Z"),
	})

	state := tfsdk.State{Schema: s, Raw: stateVal}
	resp := &resource.DeleteResponse{}
	r.Delete(context.Background(), resource.DeleteRequest{State: state}, resp)

	if resp.Diagnostics.HasError() {
		t.Errorf("expected no errors when deleting already-gone FIP, got %v", resp.Diagnostics)
	}
}

// --- Additional tests for coverage gaps ---

func TestFIPResourceCreateAPIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/v1/tenants/t-123/floating-ips" {
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"error": map[string]string{"code": "INTERNAL_ERROR", "message": "server error"},
			})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key") // pragma: allowlist secret
	c.SetTenantIDForTest("t-123")

	r := NewResource()
	r.(resource.ResourceWithConfigure).Configure(context.Background(), resource.ConfigureRequest{ProviderData: c}, &resource.ConfigureResponse{})

	s := fipSchema(t)
	planVal := tftypes.NewValue(fipObjectType(), map[string]tftypes.Value{
		"id":          tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"address":     tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"instance_id": tftypes.NewValue(tftypes.String, nil),
		"tags":        tftypes.NewValue(tftypes.Map{ElementType: tftypes.String}, nil),
		"status":      tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"private_ip":  tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"created_at":  tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
	})

	plan := tfsdk.Plan{Schema: s, Raw: planVal}
	resp := &resource.CreateResponse{State: tfsdk.State{Schema: s}}
	r.Create(context.Background(), resource.CreateRequest{Plan: plan}, resp)

	if !resp.Diagnostics.HasError() {
		t.Error("expected error for API failure on create")
	}
}

func TestFIPResourceCreateBadResponseBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/v1/tenants/t-123/floating-ips" {
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte("not json"))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key") // pragma: allowlist secret
	c.SetTenantIDForTest("t-123")

	r := NewResource()
	r.(resource.ResourceWithConfigure).Configure(context.Background(), resource.ConfigureRequest{ProviderData: c}, &resource.ConfigureResponse{})

	s := fipSchema(t)
	planVal := tftypes.NewValue(fipObjectType(), map[string]tftypes.Value{
		"id":          tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"address":     tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"instance_id": tftypes.NewValue(tftypes.String, nil),
		"tags":        tftypes.NewValue(tftypes.Map{ElementType: tftypes.String}, nil),
		"status":      tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"private_ip":  tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"created_at":  tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
	})

	plan := tfsdk.Plan{Schema: s, Raw: planVal}
	resp := &resource.CreateResponse{State: tfsdk.State{Schema: s}}
	r.Create(context.Background(), resource.CreateRequest{Plan: plan}, resp)

	if !resp.Diagnostics.HasError() {
		t.Error("expected error for bad response body")
	}
}

func TestFIPResourceCreateAssociationError(t *testing.T) {
	fipResp := apiFloatingIP{
		ID:        "fip-ae-1",
		Address:   "203.0.113.55",
		Status:    "available",
		CreatedAt: "2025-06-01T12:00:00Z",
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/tenants/t-123/floating-ips":
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(fipResp)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-123/instances/inst-fail":
			writeInstanceWithPort(w, "inst-fail", "port-fail")
		case r.Method == http.MethodPost && r.URL.Path == "/v1/tenants/t-123/floating-ips/fip-ae-1/associate":
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"error": map[string]string{"code": "INTERNAL_ERROR", "message": "association failed"},
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
	c.SetTenantIDForTest("t-123")

	r := NewResource()
	r.(resource.ResourceWithConfigure).Configure(context.Background(), resource.ConfigureRequest{ProviderData: c}, &resource.ConfigureResponse{})

	s := fipSchema(t)
	planVal := tftypes.NewValue(fipObjectType(), map[string]tftypes.Value{
		"id":          tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"address":     tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"instance_id": tftypes.NewValue(tftypes.String, "inst-fail"),
		"tags":        tftypes.NewValue(tftypes.Map{ElementType: tftypes.String}, nil),
		"status":      tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"private_ip":  tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"created_at":  tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
	})

	plan := tfsdk.Plan{Schema: s, Raw: planVal}
	resp := &resource.CreateResponse{State: tfsdk.State{Schema: s}}
	r.Create(context.Background(), resource.CreateRequest{Plan: plan}, resp)

	if !resp.Diagnostics.HasError() {
		t.Error("expected error for association failure")
	}
}

// TestFIPResourceCreateResolvePortError covers the path where the target
// instance has no resolvable network port for association.
func TestFIPResourceCreateResolvePortError(t *testing.T) {
	fipResp := apiFloatingIP{
		ID:        "fip-rp-1",
		Address:   "203.0.113.56",
		Status:    "available",
		CreatedAt: "2025-06-01T12:00:00Z",
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/tenants/t-123/floating-ips":
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(fipResp)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-123/instances/inst-noport":
			// Instance exists but exposes no network port.
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "inst-noport", "networks": []map[string]any{}})
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

	r := NewResource()
	r.(resource.ResourceWithConfigure).Configure(context.Background(), resource.ConfigureRequest{ProviderData: c}, &resource.ConfigureResponse{})

	s := fipSchema(t)
	planVal := tftypes.NewValue(fipObjectType(), map[string]tftypes.Value{
		"id":          tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"address":     tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"instance_id": tftypes.NewValue(tftypes.String, "inst-noport"),
		"tags":        tftypes.NewValue(tftypes.Map{ElementType: tftypes.String}, nil),
		"status":      tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"private_ip":  tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"created_at":  tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
	})

	plan := tfsdk.Plan{Schema: s, Raw: planVal}
	resp := &resource.CreateResponse{State: tfsdk.State{Schema: s}}
	r.Create(context.Background(), resource.CreateRequest{Plan: plan}, resp)

	if !resp.Diagnostics.HasError() {
		t.Error("expected error when the instance has no network port")
	}
}

func TestFIPResourceCreateAssociationBadResponseThenReread(t *testing.T) {
	fipAllocated := apiFloatingIP{
		ID:        "fip-reread-1",
		Address:   "203.0.113.60",
		Status:    "available",
		CreatedAt: "2025-06-01T12:00:00Z",
	}

	fipAssociated := apiFloatingIP{
		ID:        "fip-reread-1",
		Address:   "203.0.113.60",
		Status:    "active",
		PortID:    "port-789",
		PrivateIP: "10.0.1.20",
		CreatedAt: "2025-06-01T12:00:00Z",
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/tenants/t-123/floating-ips":
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(fipAllocated)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-123/instances/inst-789":
			writeInstanceWithPort(w, "inst-789", "port-789")
		case r.Method == http.MethodPost && r.URL.Path == "/v1/tenants/t-123/floating-ips/fip-reread-1/associate":
			// Return a non-JSON body; the provider does not parse a sync (200) associate body.
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("ok"))
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-123/floating-ips/fip-reread-1":
			_ = json.NewEncoder(w).Encode(fipAssociated)
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

	r := NewResource()
	r.(resource.ResourceWithConfigure).Configure(context.Background(), resource.ConfigureRequest{ProviderData: c}, &resource.ConfigureResponse{})

	s := fipSchema(t)
	planVal := tftypes.NewValue(fipObjectType(), map[string]tftypes.Value{
		"id":          tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"address":     tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"instance_id": tftypes.NewValue(tftypes.String, "inst-789"),
		"tags":        tftypes.NewValue(tftypes.Map{ElementType: tftypes.String}, nil),
		"status":      tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"private_ip":  tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"created_at":  tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
	})

	plan := tfsdk.Plan{Schema: s, Raw: planVal}
	resp := &resource.CreateResponse{State: tfsdk.State{Schema: s}}
	r.Create(context.Background(), resource.CreateRequest{Plan: plan}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected errors: %v", resp.Diagnostics)
	}

	var state FloatingIPModel
	resp.State.Get(context.Background(), &state)

	if state.InstanceID.ValueString() != "inst-789" {
		t.Errorf("expected InstanceID inst-789, got %s", state.InstanceID.ValueString())
	}
	if state.Status.ValueString() != "active" {
		t.Errorf("expected Status active, got %s", state.Status.ValueString())
	}
}

func TestFIPResourceCreateAssocRereadGetError(t *testing.T) {
	fipAllocated := apiFloatingIP{
		ID:        "fip-rre-1",
		Address:   "203.0.113.70",
		Status:    "available",
		CreatedAt: "2025-06-01T12:00:00Z",
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/tenants/t-123/floating-ips":
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(fipAllocated)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-123/instances/inst-fail":
			writeInstanceWithPort(w, "inst-fail", "port-fail")
		case r.Method == http.MethodPost && r.URL.Path == "/v1/tenants/t-123/floating-ips/fip-rre-1/associate":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("ok"))
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-123/floating-ips/fip-rre-1":
			// Re-read also fails
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"error": map[string]string{"code": "INTERNAL_ERROR", "message": "read failed"},
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
	c.SetTenantIDForTest("t-123")

	r := NewResource()
	r.(resource.ResourceWithConfigure).Configure(context.Background(), resource.ConfigureRequest{ProviderData: c}, &resource.ConfigureResponse{})

	s := fipSchema(t)
	planVal := tftypes.NewValue(fipObjectType(), map[string]tftypes.Value{
		"id":          tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"address":     tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"instance_id": tftypes.NewValue(tftypes.String, "inst-fail"),
		"tags":        tftypes.NewValue(tftypes.Map{ElementType: tftypes.String}, nil),
		"status":      tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"private_ip":  tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"created_at":  tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
	})

	plan := tfsdk.Plan{Schema: s, Raw: planVal}
	resp := &resource.CreateResponse{State: tfsdk.State{Schema: s}}
	r.Create(context.Background(), resource.CreateRequest{Plan: plan}, resp)

	if !resp.Diagnostics.HasError() {
		t.Error("expected error when re-read also fails")
	}
}

func TestFIPResourceCreateAssocRereadBadJSON(t *testing.T) {
	fipAllocated := apiFloatingIP{
		ID:        "fip-rbj-1",
		Address:   "203.0.113.71",
		Status:    "available",
		CreatedAt: "2025-06-01T12:00:00Z",
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/tenants/t-123/floating-ips":
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(fipAllocated)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-123/instances/inst-fail":
			writeInstanceWithPort(w, "inst-fail", "port-fail")
		case r.Method == http.MethodPost && r.URL.Path == "/v1/tenants/t-123/floating-ips/fip-rbj-1/associate":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("ok"))
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-123/floating-ips/fip-rbj-1":
			// Re-read succeeds HTTP-wise but returns bad JSON
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("bad json"))
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

	r := NewResource()
	r.(resource.ResourceWithConfigure).Configure(context.Background(), resource.ConfigureRequest{ProviderData: c}, &resource.ConfigureResponse{})

	s := fipSchema(t)
	planVal := tftypes.NewValue(fipObjectType(), map[string]tftypes.Value{
		"id":          tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"address":     tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"instance_id": tftypes.NewValue(tftypes.String, "inst-fail"),
		"tags":        tftypes.NewValue(tftypes.Map{ElementType: tftypes.String}, nil),
		"status":      tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"private_ip":  tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"created_at":  tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
	})

	plan := tfsdk.Plan{Schema: s, Raw: planVal}
	resp := &resource.CreateResponse{State: tfsdk.State{Schema: s}}
	r.Create(context.Background(), resource.CreateRequest{Plan: plan}, resp)

	if !resp.Diagnostics.HasError() {
		t.Error("expected error when re-read returns bad JSON")
	}
}

func TestFIPResourceReadAPIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"error": map[string]string{"code": "INTERNAL_ERROR", "message": "server error"},
		})
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key") // pragma: allowlist secret
	c.SetTenantIDForTest("t-123")

	r := NewResource()
	r.(resource.ResourceWithConfigure).Configure(context.Background(), resource.ConfigureRequest{ProviderData: c}, &resource.ConfigureResponse{})

	s := fipSchema(t)
	stateVal := tftypes.NewValue(fipObjectType(), map[string]tftypes.Value{
		"id":          tftypes.NewValue(tftypes.String, "fip-err-1"),
		"address":     tftypes.NewValue(tftypes.String, "203.0.113.50"),
		"instance_id": tftypes.NewValue(tftypes.String, nil),
		"tags":        tftypes.NewValue(tftypes.Map{ElementType: tftypes.String}, nil),
		"status":      tftypes.NewValue(tftypes.String, "available"),
		"private_ip":  tftypes.NewValue(tftypes.String, nil),
		"created_at":  tftypes.NewValue(tftypes.String, "2025-01-01T00:00:00Z"),
	})

	state := tfsdk.State{Schema: s, Raw: stateVal}
	resp := &resource.ReadResponse{State: state}
	r.Read(context.Background(), resource.ReadRequest{State: state}, resp)

	if !resp.Diagnostics.HasError() {
		t.Error("expected error for API failure on read")
	}
}

func TestFIPResourceReadBadJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("not json"))
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key") // pragma: allowlist secret
	c.SetTenantIDForTest("t-123")

	r := NewResource()
	r.(resource.ResourceWithConfigure).Configure(context.Background(), resource.ConfigureRequest{ProviderData: c}, &resource.ConfigureResponse{})

	s := fipSchema(t)
	stateVal := tftypes.NewValue(fipObjectType(), map[string]tftypes.Value{
		"id":          tftypes.NewValue(tftypes.String, "fip-bad-1"),
		"address":     tftypes.NewValue(tftypes.String, "203.0.113.50"),
		"instance_id": tftypes.NewValue(tftypes.String, nil),
		"tags":        tftypes.NewValue(tftypes.Map{ElementType: tftypes.String}, nil),
		"status":      tftypes.NewValue(tftypes.String, "available"),
		"private_ip":  tftypes.NewValue(tftypes.String, nil),
		"created_at":  tftypes.NewValue(tftypes.String, "2025-01-01T00:00:00Z"),
	})

	state := tfsdk.State{Schema: s, Raw: stateVal}
	resp := &resource.ReadResponse{State: state}
	r.Read(context.Background(), resource.ReadRequest{State: state}, resp)

	if !resp.Diagnostics.HasError() {
		t.Error("expected error for bad JSON in read response")
	}
}

func TestFIPResourceUpdateDisassociateOnly(t *testing.T) {
	fipResp := apiFloatingIP{
		ID:        "fip-dis-1",
		Address:   "203.0.113.50",
		Status:    "available",
		CreatedAt: "2025-06-01T12:00:00Z",
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/tenants/t-123/floating-ips/fip-dis-1/disassociate":
			_ = json.NewEncoder(w).Encode(fipResp)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-123/floating-ips/fip-dis-1":
			_ = json.NewEncoder(w).Encode(fipResp)
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

	r := NewResource()
	r.(resource.ResourceWithConfigure).Configure(context.Background(), resource.ConfigureRequest{ProviderData: c}, &resource.ConfigureResponse{})

	s := fipSchema(t)

	// State: currently associated
	stateVal := tftypes.NewValue(fipObjectType(), map[string]tftypes.Value{
		"id":          tftypes.NewValue(tftypes.String, "fip-dis-1"),
		"address":     tftypes.NewValue(tftypes.String, "203.0.113.50"),
		"instance_id": tftypes.NewValue(tftypes.String, "inst-old"),
		"tags":        tftypes.NewValue(tftypes.Map{ElementType: tftypes.String}, nil),
		"status":      tftypes.NewValue(tftypes.String, "active"),
		"private_ip":  tftypes.NewValue(tftypes.String, "10.0.1.5"),
		"created_at":  tftypes.NewValue(tftypes.String, "2025-06-01T12:00:00Z"),
	})

	// Plan: instance_id removed (null)
	planVal := tftypes.NewValue(fipObjectType(), map[string]tftypes.Value{
		"id":          tftypes.NewValue(tftypes.String, "fip-dis-1"),
		"address":     tftypes.NewValue(tftypes.String, "203.0.113.50"),
		"instance_id": tftypes.NewValue(tftypes.String, nil),
		"tags":        tftypes.NewValue(tftypes.Map{ElementType: tftypes.String}, nil),
		"status":      tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"private_ip":  tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"created_at":  tftypes.NewValue(tftypes.String, "2025-06-01T12:00:00Z"),
	})

	state := tfsdk.State{Schema: s, Raw: stateVal}
	plan := tfsdk.Plan{Schema: s, Raw: planVal}
	resp := &resource.UpdateResponse{State: tfsdk.State{Schema: s}}
	r.Update(context.Background(), resource.UpdateRequest{Plan: plan, State: state}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected errors: %v", resp.Diagnostics)
	}

	var model FloatingIPModel
	resp.State.Get(context.Background(), &model)
	if !model.InstanceID.IsNull() {
		t.Errorf("expected InstanceID to be null, got %s", model.InstanceID.ValueString())
	}
	if model.Status.ValueString() != "available" {
		t.Errorf("expected Status available, got %s", model.Status.ValueString())
	}
}

func TestFIPResourceUpdateTagsOnly(t *testing.T) {
	fipResp := apiFloatingIP{
		ID:        "fip-tags-1",
		Address:   "203.0.113.50",
		Status:    "available",
		Tags:      map[string]string{"env": "prod"},
		CreatedAt: "2025-06-01T12:00:00Z",
	}

	var patchCalled bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPatch && r.URL.Path == "/v1/tenants/t-123/floating-ips/fip-tags-1":
			patchCalled = true
			_ = json.NewEncoder(w).Encode(fipResp)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-123/floating-ips/fip-tags-1":
			_ = json.NewEncoder(w).Encode(fipResp)
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

	r := NewResource()
	r.(resource.ResourceWithConfigure).Configure(context.Background(), resource.ConfigureRequest{ProviderData: c}, &resource.ConfigureResponse{})

	s := fipSchema(t)

	// State: no tags
	stateVal := tftypes.NewValue(fipObjectType(), map[string]tftypes.Value{
		"id":          tftypes.NewValue(tftypes.String, "fip-tags-1"),
		"address":     tftypes.NewValue(tftypes.String, "203.0.113.50"),
		"instance_id": tftypes.NewValue(tftypes.String, nil),
		"tags":        tftypes.NewValue(tftypes.Map{ElementType: tftypes.String}, nil),
		"status":      tftypes.NewValue(tftypes.String, "available"),
		"private_ip":  tftypes.NewValue(tftypes.String, nil),
		"created_at":  tftypes.NewValue(tftypes.String, "2025-06-01T12:00:00Z"),
	})

	// Plan: add tags
	planVal := tftypes.NewValue(fipObjectType(), map[string]tftypes.Value{
		"id":          tftypes.NewValue(tftypes.String, "fip-tags-1"),
		"address":     tftypes.NewValue(tftypes.String, "203.0.113.50"),
		"instance_id": tftypes.NewValue(tftypes.String, nil),
		"tags": tftypes.NewValue(tftypes.Map{ElementType: tftypes.String}, map[string]tftypes.Value{
			"env": tftypes.NewValue(tftypes.String, "prod"),
		}),
		"status":     tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"private_ip": tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"created_at": tftypes.NewValue(tftypes.String, "2025-06-01T12:00:00Z"),
	})

	state := tfsdk.State{Schema: s, Raw: stateVal}
	plan := tfsdk.Plan{Schema: s, Raw: planVal}
	resp := &resource.UpdateResponse{State: tfsdk.State{Schema: s}}
	r.Update(context.Background(), resource.UpdateRequest{Plan: plan, State: state}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected errors: %v", resp.Diagnostics)
	}

	if !patchCalled {
		t.Error("expected PATCH to be called for tags update")
	}
}

func TestFIPResourceUpdateDisassociateError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/tenants/t-123/floating-ips/fip-de-1/disassociate":
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"error": map[string]string{"code": "INTERNAL_ERROR", "message": "disassociate failed"},
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key") // pragma: allowlist secret
	c.SetTenantIDForTest("t-123")

	r := NewResource()
	r.(resource.ResourceWithConfigure).Configure(context.Background(), resource.ConfigureRequest{ProviderData: c}, &resource.ConfigureResponse{})

	s := fipSchema(t)

	stateVal := tftypes.NewValue(fipObjectType(), map[string]tftypes.Value{
		"id":          tftypes.NewValue(tftypes.String, "fip-de-1"),
		"address":     tftypes.NewValue(tftypes.String, "203.0.113.50"),
		"instance_id": tftypes.NewValue(tftypes.String, "inst-old"),
		"tags":        tftypes.NewValue(tftypes.Map{ElementType: tftypes.String}, nil),
		"status":      tftypes.NewValue(tftypes.String, "active"),
		"private_ip":  tftypes.NewValue(tftypes.String, "10.0.1.5"),
		"created_at":  tftypes.NewValue(tftypes.String, "2025-06-01T12:00:00Z"),
	})

	planVal := tftypes.NewValue(fipObjectType(), map[string]tftypes.Value{
		"id":          tftypes.NewValue(tftypes.String, "fip-de-1"),
		"address":     tftypes.NewValue(tftypes.String, "203.0.113.50"),
		"instance_id": tftypes.NewValue(tftypes.String, nil),
		"tags":        tftypes.NewValue(tftypes.Map{ElementType: tftypes.String}, nil),
		"status":      tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"private_ip":  tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"created_at":  tftypes.NewValue(tftypes.String, "2025-06-01T12:00:00Z"),
	})

	state := tfsdk.State{Schema: s, Raw: stateVal}
	plan := tfsdk.Plan{Schema: s, Raw: planVal}
	resp := &resource.UpdateResponse{State: tfsdk.State{Schema: s}}
	r.Update(context.Background(), resource.UpdateRequest{Plan: plan, State: state}, resp)

	if !resp.Diagnostics.HasError() {
		t.Error("expected error for disassociate failure")
	}
}

func TestFIPResourceUpdateAssociateError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/tenants/t-123/floating-ips/fip-ae2-1/disassociate":
			_ = json.NewEncoder(w).Encode(apiFloatingIP{})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-123/instances/inst-new":
			writeInstanceWithPort(w, "inst-new", "port-new")
		case r.Method == http.MethodPost && r.URL.Path == "/v1/tenants/t-123/floating-ips/fip-ae2-1/associate":
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"error": map[string]string{"code": "INTERNAL_ERROR", "message": "associate failed"},
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key") // pragma: allowlist secret
	c.SetTenantIDForTest("t-123")

	r := NewResource()
	r.(resource.ResourceWithConfigure).Configure(context.Background(), resource.ConfigureRequest{ProviderData: c}, &resource.ConfigureResponse{})

	s := fipSchema(t)

	stateVal := tftypes.NewValue(fipObjectType(), map[string]tftypes.Value{
		"id":          tftypes.NewValue(tftypes.String, "fip-ae2-1"),
		"address":     tftypes.NewValue(tftypes.String, "203.0.113.50"),
		"instance_id": tftypes.NewValue(tftypes.String, "inst-old"),
		"tags":        tftypes.NewValue(tftypes.Map{ElementType: tftypes.String}, nil),
		"status":      tftypes.NewValue(tftypes.String, "active"),
		"private_ip":  tftypes.NewValue(tftypes.String, "10.0.1.5"),
		"created_at":  tftypes.NewValue(tftypes.String, "2025-06-01T12:00:00Z"),
	})

	planVal := tftypes.NewValue(fipObjectType(), map[string]tftypes.Value{
		"id":          tftypes.NewValue(tftypes.String, "fip-ae2-1"),
		"address":     tftypes.NewValue(tftypes.String, "203.0.113.50"),
		"instance_id": tftypes.NewValue(tftypes.String, "inst-new"),
		"tags":        tftypes.NewValue(tftypes.Map{ElementType: tftypes.String}, nil),
		"status":      tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"private_ip":  tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"created_at":  tftypes.NewValue(tftypes.String, "2025-06-01T12:00:00Z"),
	})

	state := tfsdk.State{Schema: s, Raw: stateVal}
	plan := tfsdk.Plan{Schema: s, Raw: planVal}
	resp := &resource.UpdateResponse{State: tfsdk.State{Schema: s}}
	r.Update(context.Background(), resource.UpdateRequest{Plan: plan, State: state}, resp)

	if !resp.Diagnostics.HasError() {
		t.Error("expected error for associate failure during update")
	}
}

func TestFIPResourceUpdatePatchError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPatch && r.URL.Path == "/v1/tenants/t-123/floating-ips/fip-pe-1":
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"error": map[string]string{"code": "INTERNAL_ERROR", "message": "patch failed"},
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key") // pragma: allowlist secret
	c.SetTenantIDForTest("t-123")

	r := NewResource()
	r.(resource.ResourceWithConfigure).Configure(context.Background(), resource.ConfigureRequest{ProviderData: c}, &resource.ConfigureResponse{})

	s := fipSchema(t)

	stateVal := tftypes.NewValue(fipObjectType(), map[string]tftypes.Value{
		"id":          tftypes.NewValue(tftypes.String, "fip-pe-1"),
		"address":     tftypes.NewValue(tftypes.String, "203.0.113.50"),
		"instance_id": tftypes.NewValue(tftypes.String, nil),
		"tags":        tftypes.NewValue(tftypes.Map{ElementType: tftypes.String}, nil),
		"status":      tftypes.NewValue(tftypes.String, "available"),
		"private_ip":  tftypes.NewValue(tftypes.String, nil),
		"created_at":  tftypes.NewValue(tftypes.String, "2025-06-01T12:00:00Z"),
	})

	planVal := tftypes.NewValue(fipObjectType(), map[string]tftypes.Value{
		"id":          tftypes.NewValue(tftypes.String, "fip-pe-1"),
		"address":     tftypes.NewValue(tftypes.String, "203.0.113.50"),
		"instance_id": tftypes.NewValue(tftypes.String, nil),
		"tags": tftypes.NewValue(tftypes.Map{ElementType: tftypes.String}, map[string]tftypes.Value{
			"env": tftypes.NewValue(tftypes.String, "prod"),
		}),
		"status":     tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"private_ip": tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"created_at": tftypes.NewValue(tftypes.String, "2025-06-01T12:00:00Z"),
	})

	state := tfsdk.State{Schema: s, Raw: stateVal}
	plan := tfsdk.Plan{Schema: s, Raw: planVal}
	resp := &resource.UpdateResponse{State: tfsdk.State{Schema: s}}
	r.Update(context.Background(), resource.UpdateRequest{Plan: plan, State: state}, resp)

	if !resp.Diagnostics.HasError() {
		t.Error("expected error for PATCH failure during tag update")
	}
}

func TestFIPResourceUpdateReadError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-123/floating-ips/fip-re-1":
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"error": map[string]string{"code": "INTERNAL_ERROR", "message": "read failed"},
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key") // pragma: allowlist secret
	c.SetTenantIDForTest("t-123")

	r := NewResource()
	r.(resource.ResourceWithConfigure).Configure(context.Background(), resource.ConfigureRequest{ProviderData: c}, &resource.ConfigureResponse{})

	s := fipSchema(t)

	// State and plan with same instance_id and tags (no changes to trigger)
	stateVal := tftypes.NewValue(fipObjectType(), map[string]tftypes.Value{
		"id":          tftypes.NewValue(tftypes.String, "fip-re-1"),
		"address":     tftypes.NewValue(tftypes.String, "203.0.113.50"),
		"instance_id": tftypes.NewValue(tftypes.String, nil),
		"tags":        tftypes.NewValue(tftypes.Map{ElementType: tftypes.String}, nil),
		"status":      tftypes.NewValue(tftypes.String, "available"),
		"private_ip":  tftypes.NewValue(tftypes.String, nil),
		"created_at":  tftypes.NewValue(tftypes.String, "2025-06-01T12:00:00Z"),
	})

	planVal := tftypes.NewValue(fipObjectType(), map[string]tftypes.Value{
		"id":          tftypes.NewValue(tftypes.String, "fip-re-1"),
		"address":     tftypes.NewValue(tftypes.String, "203.0.113.50"),
		"instance_id": tftypes.NewValue(tftypes.String, nil),
		"tags":        tftypes.NewValue(tftypes.Map{ElementType: tftypes.String}, nil),
		"status":      tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"private_ip":  tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"created_at":  tftypes.NewValue(tftypes.String, "2025-06-01T12:00:00Z"),
	})

	state := tfsdk.State{Schema: s, Raw: stateVal}
	plan := tfsdk.Plan{Schema: s, Raw: planVal}
	resp := &resource.UpdateResponse{State: tfsdk.State{Schema: s}}
	r.Update(context.Background(), resource.UpdateRequest{Plan: plan, State: state}, resp)

	if !resp.Diagnostics.HasError() {
		t.Error("expected error for read failure during update")
	}
}

func TestFIPResourceUpdateReadBadJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-123/floating-ips/fip-rbj-1":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("not json"))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key") // pragma: allowlist secret
	c.SetTenantIDForTest("t-123")

	r := NewResource()
	r.(resource.ResourceWithConfigure).Configure(context.Background(), resource.ConfigureRequest{ProviderData: c}, &resource.ConfigureResponse{})

	s := fipSchema(t)

	stateVal := tftypes.NewValue(fipObjectType(), map[string]tftypes.Value{
		"id":          tftypes.NewValue(tftypes.String, "fip-rbj-1"),
		"address":     tftypes.NewValue(tftypes.String, "203.0.113.50"),
		"instance_id": tftypes.NewValue(tftypes.String, nil),
		"tags":        tftypes.NewValue(tftypes.Map{ElementType: tftypes.String}, nil),
		"status":      tftypes.NewValue(tftypes.String, "available"),
		"private_ip":  tftypes.NewValue(tftypes.String, nil),
		"created_at":  tftypes.NewValue(tftypes.String, "2025-06-01T12:00:00Z"),
	})

	planVal := tftypes.NewValue(fipObjectType(), map[string]tftypes.Value{
		"id":          tftypes.NewValue(tftypes.String, "fip-rbj-1"),
		"address":     tftypes.NewValue(tftypes.String, "203.0.113.50"),
		"instance_id": tftypes.NewValue(tftypes.String, nil),
		"tags":        tftypes.NewValue(tftypes.Map{ElementType: tftypes.String}, nil),
		"status":      tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"private_ip":  tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"created_at":  tftypes.NewValue(tftypes.String, "2025-06-01T12:00:00Z"),
	})

	state := tfsdk.State{Schema: s, Raw: stateVal}
	plan := tfsdk.Plan{Schema: s, Raw: planVal}
	resp := &resource.UpdateResponse{State: tfsdk.State{Schema: s}}
	r.Update(context.Background(), resource.UpdateRequest{Plan: plan, State: state}, resp)

	if !resp.Diagnostics.HasError() {
		t.Error("expected error for bad JSON on read during update")
	}
}

func TestFIPResourceDeleteAPIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"error": map[string]string{"code": "INTERNAL_ERROR", "message": "server error"},
		})
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key") // pragma: allowlist secret
	c.SetTenantIDForTest("t-123")

	r := NewResource()
	r.(resource.ResourceWithConfigure).Configure(context.Background(), resource.ConfigureRequest{ProviderData: c}, &resource.ConfigureResponse{})

	s := fipSchema(t)
	stateVal := tftypes.NewValue(fipObjectType(), map[string]tftypes.Value{
		"id":          tftypes.NewValue(tftypes.String, "fip-del-err"),
		"address":     tftypes.NewValue(tftypes.String, "203.0.113.50"),
		"instance_id": tftypes.NewValue(tftypes.String, nil),
		"tags":        tftypes.NewValue(tftypes.Map{ElementType: tftypes.String}, nil),
		"status":      tftypes.NewValue(tftypes.String, "available"),
		"private_ip":  tftypes.NewValue(tftypes.String, nil),
		"created_at":  tftypes.NewValue(tftypes.String, "2025-01-01T00:00:00Z"),
	})

	state := tfsdk.State{Schema: s, Raw: stateVal}
	resp := &resource.DeleteResponse{}
	r.Delete(context.Background(), resource.DeleteRequest{State: state}, resp)

	if !resp.Diagnostics.HasError() {
		t.Error("expected error for API failure on delete")
	}
}

func TestFIPResourceImportState(t *testing.T) {
	r := NewResource()
	s := fipSchema(t)

	ctx := context.Background()
	importReq := resource.ImportStateRequest{ID: "fip-import-1"}
	stateVal := tftypes.NewValue(fipObjectType(), map[string]tftypes.Value{
		"id":          tftypes.NewValue(tftypes.String, nil),
		"address":     tftypes.NewValue(tftypes.String, nil),
		"instance_id": tftypes.NewValue(tftypes.String, nil),
		"tags":        tftypes.NewValue(tftypes.Map{ElementType: tftypes.String}, nil),
		"status":      tftypes.NewValue(tftypes.String, nil),
		"private_ip":  tftypes.NewValue(tftypes.String, nil),
		"created_at":  tftypes.NewValue(tftypes.String, nil),
	})
	importResp := &resource.ImportStateResponse{
		State: tfsdk.State{Schema: s, Raw: stateVal},
	}

	r.(resource.ResourceWithImportState).ImportState(ctx, importReq, importResp)

	if importResp.Diagnostics.HasError() {
		t.Fatalf("ImportState failed: %v", importResp.Diagnostics)
	}

	var model FloatingIPModel
	importResp.State.Get(ctx, &model)

	if model.ID.ValueString() != "fip-import-1" {
		t.Errorf("expected ID fip-import-1, got %s", model.ID.ValueString())
	}
}

// Ensure fmt is used.
var _ = fmt.Sprintf
