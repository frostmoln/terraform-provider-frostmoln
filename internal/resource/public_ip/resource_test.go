package public_ip

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
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

func TestPublicIPModelFromAPI(t *testing.T) {
	fip := &apiPublicIP{
		ID:        "fip-123",
		Address:   "203.0.113.10",
		Status:    "active",
		PortID:    "port-9",
		PrivateIP: "10.0.1.5",
		Tags:      map[string]string{"env": "test"},
		CreatedAt: "2025-01-01T00:00:00Z",
	}

	var model PublicIPModel
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

func TestPublicIPModelFromAPIMinimal(t *testing.T) {
	fip := &apiPublicIP{
		ID:        "fip-789",
		Address:   "203.0.113.20",
		Status:    "available",
		CreatedAt: "2025-01-01T00:00:00Z",
	}

	var model PublicIPModel
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

func TestPublicIPModelToAllocateRequest(t *testing.T) {
	ctx := context.Background()
	tags, _ := types.MapValueFrom(ctx, types.StringType, map[string]string{"env": "prod"})

	model := PublicIPModel{
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

func TestPublicIPModelToAllocateRequestMinimal(t *testing.T) {
	model := PublicIPModel{
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

// TestPublicIPAllocateRequestWireContract locks the allocate request to the
// backend contract: region is not part of the wire body (ADR-0022), only tags.
func TestPublicIPAllocateRequestWireContract(t *testing.T) {
	ctx := context.Background()
	tags, _ := types.MapValueFrom(ctx, types.StringType, map[string]string{"env": "prod"})
	model := PublicIPModel{Tags: tags}

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

// TestPublicIPAssociateRequestWireContract locks the associate request to the
// backend contract: it carries portId, never instanceId.
func TestPublicIPAssociateRequestWireContract(t *testing.T) {
	raw, err := json.Marshal(apiAssociatePublicIPRequest{PortID: "port-abc"})
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

func TestPublicIPResourceCRUD(t *testing.T) {
	fipData := apiPublicIP{
		ID:        "fip-test-1",
		Address:   "203.0.113.50",
		Status:    "available",
		CreatedAt: "2025-01-01T00:00:00Z",
	}

	fipAssociated := apiPublicIP{
		ID:        "fip-test-1",
		Address:   "203.0.113.50",
		Status:    "active",
		PortID:    "port-abc",
		PrivateIP: "10.0.1.5",
		CreatedAt: "2025-01-01T00:00:00Z",
	}

	fipDisassociated := apiPublicIP{
		ID:        "fip-test-1",
		Address:   "203.0.113.50",
		Status:    "available",
		CreatedAt: "2025-01-01T00:00:00Z",
	}

	associated := false

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/tenants/t-123/public-ips":
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(fipData)

		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-123/public-ips/fip-test-1":
			w.WriteHeader(http.StatusOK)
			if associated {
				_ = json.NewEncoder(w).Encode(fipAssociated)
			} else {
				_ = json.NewEncoder(w).Encode(fipDisassociated)
			}

		case r.Method == http.MethodPost && r.URL.Path == "/v1/tenants/t-123/public-ips/fip-test-1/associate":
			associated = true
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(fipAssociated)

		case r.Method == http.MethodPost && r.URL.Path == "/v1/tenants/t-123/public-ips/fip-test-1/disassociate":
			associated = false
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(fipDisassociated)

		case r.Method == http.MethodPatch && r.URL.Path == "/v1/tenants/t-123/public-ips/fip-test-1":
			w.WriteHeader(http.StatusOK)
			if associated {
				_ = json.NewEncoder(w).Encode(fipAssociated)
			} else {
				_ = json.NewEncoder(w).Encode(fipDisassociated)
			}

		case r.Method == http.MethodDelete && r.URL.Path == "/v1/tenants/t-123/public-ips/fip-test-1":
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
	allocateReq := apiAllocatePublicIPRequest{}
	apiResp, err := c.Post(ctx, c.TenantPath("/public-ips"), allocateReq)
	if err != nil {
		t.Fatalf("Allocate failed: %v", err)
	}
	if apiResp.StatusCode != http.StatusCreated {
		t.Errorf("expected status 201, got %d", apiResp.StatusCode)
	}

	var allocated apiPublicIP
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
	assocReq := apiAssociatePublicIPRequest{PortID: "port-abc"}
	assocResp, err := c.Post(ctx, c.TenantPath("/public-ips/fip-test-1/associate"), assocReq)
	if err != nil {
		t.Fatalf("Associate failed: %v", err)
	}
	var assocFIP apiPublicIP
	if err := json.Unmarshal(assocResp.Body, &assocFIP); err != nil {
		t.Fatalf("failed to parse associate response: %v", err)
	}
	if assocFIP.PortID != "port-abc" {
		t.Errorf("expected PortID port-abc, got %s", assocFIP.PortID)
	}

	// Test Disassociate.
	_, err = c.Post(ctx, c.TenantPath("/public-ips/fip-test-1/disassociate"), nil)
	if err != nil {
		t.Fatalf("Disassociate failed: %v", err)
	}

	// Verify disassociated (no portId on the wire).
	readResp, err := c.Get(ctx, c.TenantPath("/public-ips/fip-test-1"), nil)
	if err != nil {
		t.Fatalf("Read failed: %v", err)
	}
	var readFIP apiPublicIP
	if err := json.Unmarshal(readResp.Body, &readFIP); err != nil {
		t.Fatalf("failed to parse read response: %v", err)
	}
	if readFIP.PortID != "" {
		t.Errorf("expected PortID empty after disassociate, got %s", readFIP.PortID)
	}

	// Test Delete.
	_, err = c.Delete(ctx, c.TenantPath("/public-ips/fip-test-1"))
	if err != nil {
		t.Fatalf("Delete failed: %v", err)
	}
}

func TestPublicIPReadNotFound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"error": map[string]string{"code": "NOT_FOUND", "message": "public IP not found"},
		})
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key") // pragma: allowlist secret
	c.SetTenantIDForTest("t-123")

	_, err := c.Get(context.Background(), c.TenantPath("/public-ips/nonexistent"), nil)
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
			"attachment":  fipAttachmentType(),

			"acknowledge_address_loss": tftypes.Bool,
		},
	}
}

// fipAttachmentType is the `attachment` object: what is holding the address.
func fipAttachmentType() tftypes.Object {
	return tftypes.Object{
		AttributeTypes: map[string]tftypes.Type{
			"kind":        tftypes.String,
			"resource_id": tftypes.String,
			"vpc_id":      tftypes.String,
		},
	}
}

// fipAttachmentNull is the prior/plan value of `attachment` in a test that is
// not about the attachment: null, which every code path under test overwrites
// from the API response.
func fipAttachmentNull() tftypes.Value {
	return tftypes.NewValue(fipAttachmentType(), nil)
}

// fipGatewayBoundState is the state row of an address that IS a VPC's outbound path
// source — the shape every address-loss guard is about. ack arms
// acknowledge_address_loss, which is what lets a test get past the local gate
// and exercise the platform-side path behind it.
func fipGatewayBoundState(ack bool) tftypes.Value {
	ackVal := tftypes.NewValue(tftypes.Bool, nil)
	if ack {
		ackVal = tftypes.NewValue(tftypes.Bool, true)
	}
	return tftypes.NewValue(fipObjectType(), map[string]tftypes.Value{
		"acknowledge_address_loss": ackVal,
		"id":                       tftypes.NewValue(tftypes.String, "pip-1"),
		"address":                  tftypes.NewValue(tftypes.String, "203.0.113.10"),
		"instance_id":              tftypes.NewValue(tftypes.String, nil),
		"tags":                     tftypes.NewValue(tftypes.Map{ElementType: tftypes.String}, nil),
		"status":                   tftypes.NewValue(tftypes.String, "in_use"),
		"private_ip":               tftypes.NewValue(tftypes.String, nil),
		"created_at":               tftypes.NewValue(tftypes.String, "2026-01-01T00:00:00Z"),
		"attachment":               fipGatewayAttachment("gw-1", "vpc-1"),
	})
}

// fipAttachmentOfKind is an attachment of an arbitrary kind, for the case the
// platform names something this build does not recognise.
func fipAttachmentOfKind(kind string) tftypes.Value {
	return tftypes.NewValue(fipAttachmentType(), map[string]tftypes.Value{
		"kind":        tftypes.NewValue(tftypes.String, kind),
		"resource_id": tftypes.NewValue(tftypes.String, "res-1"),
		"vpc_id":      tftypes.NewValue(tftypes.String, nil),
	})
}

// fipStateWithAttachment is fipGatewayBoundState with an arbitrary attachment.
func fipStateWithAttachment(att tftypes.Value, ack bool) tftypes.Value {
	ackVal := tftypes.NewValue(tftypes.Bool, nil)
	if ack {
		ackVal = tftypes.NewValue(tftypes.Bool, true)
	}
	return tftypes.NewValue(fipObjectType(), map[string]tftypes.Value{
		"acknowledge_address_loss": ackVal,
		"id":                       tftypes.NewValue(tftypes.String, "pip-1"),
		"address":                  tftypes.NewValue(tftypes.String, "203.0.113.10"),
		"instance_id":              tftypes.NewValue(tftypes.String, nil),
		"tags":                     tftypes.NewValue(tftypes.Map{ElementType: tftypes.String}, nil),
		"status":                   tftypes.NewValue(tftypes.String, "available"),
		"private_ip":               tftypes.NewValue(tftypes.String, nil),
		"created_at":               tftypes.NewValue(tftypes.String, "2026-01-01T00:00:00Z"),
		"attachment":               att,
	})
}

// fipGatewayAttachment is a recorded gateway attachment, for the guards that
// refuse to let a VPC's outbound source address be released silently.
func fipGatewayAttachment(gatewayID, vpcID string) tftypes.Value {
	return tftypes.NewValue(fipAttachmentType(), map[string]tftypes.Value{
		"kind":        tftypes.NewValue(tftypes.String, AttachmentKindGateway),
		"resource_id": tftypes.NewValue(tftypes.String, gatewayID),
		"vpc_id":      tftypes.NewValue(tftypes.String, vpcID),
	})
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

	if resp.TypeName != "frostmoln_public_ip" {
		t.Errorf("expected type name frostmoln_public_ip, got %s", resp.TypeName)
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
	fipResp := apiPublicIP{
		ID:        "fip-new-1",
		Address:   "203.0.113.100",
		Status:    "available",
		CreatedAt: "2025-06-01T12:00:00Z",
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/tenants/t-123/public-ips":
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(fipResp)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-123/public-ips/fip-new-1":
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
		"acknowledge_address_loss": tftypes.NewValue(tftypes.Bool, nil),
		"attachment":               fipAttachmentNull(),
		"id":                       tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"address":                  tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"instance_id":              tftypes.NewValue(tftypes.String, nil),
		"tags":                     tftypes.NewValue(tftypes.Map{ElementType: tftypes.String}, nil),
		"status":                   tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"private_ip":               tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"created_at":               tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
	})

	plan := tfsdk.Plan{Schema: s, Raw: planVal}
	resp := &resource.CreateResponse{State: tfsdk.State{Schema: s}}
	r.Create(context.Background(), resource.CreateRequest{Plan: plan}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected errors: %v", resp.Diagnostics)
	}

	var state PublicIPModel
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
	fipAllocated := apiPublicIP{
		ID:        "fip-assoc-1",
		Address:   "203.0.113.101",
		Status:    "available",
		CreatedAt: "2025-06-01T12:00:00Z",
	}

	fipAssociated := apiPublicIP{
		ID:        "fip-assoc-1",
		Address:   "203.0.113.101",
		Status:    "active",
		PortID:    "port-abc",
		PrivateIP: "10.0.1.5",
		CreatedAt: "2025-06-01T12:00:00Z",
	}

	var associateBody apiAssociatePublicIPRequest

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/tenants/t-123/public-ips":
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(fipAllocated)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-123/instances/inst-123":
			// resolvePortID reads the instance's first network port.
			writeInstanceWithPort(w, "inst-123", "port-abc")
		case r.Method == http.MethodPost && r.URL.Path == "/v1/tenants/t-123/public-ips/fip-assoc-1/associate":
			_ = json.NewDecoder(r.Body).Decode(&associateBody)
			_ = json.NewEncoder(w).Encode(fipAssociated)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-123/public-ips/fip-assoc-1":
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
		"acknowledge_address_loss": tftypes.NewValue(tftypes.Bool, nil),
		"attachment":               fipAttachmentNull(),
		"id":                       tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"address":                  tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"instance_id":              tftypes.NewValue(tftypes.String, "inst-123"),
		"tags":                     tftypes.NewValue(tftypes.Map{ElementType: tftypes.String}, nil),
		"status":                   tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"private_ip":               tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"created_at":               tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
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

	var state PublicIPModel
	resp.State.Get(context.Background(), &state)

	if state.InstanceID.ValueString() != "inst-123" {
		t.Errorf("expected InstanceID inst-123, got %s", state.InstanceID.ValueString())
	}
	if state.Status.ValueString() != "active" {
		t.Errorf("expected Status active, got %s", state.Status.ValueString())
	}
}

func TestFIPResourceRead(t *testing.T) {
	fipResp := apiPublicIP{
		ID:        "fip-read-1",
		Address:   "203.0.113.50",
		Status:    "available",
		CreatedAt: "2025-06-01T12:00:00Z",
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-123/public-ips/fip-read-1" {
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
		"acknowledge_address_loss": tftypes.NewValue(tftypes.Bool, nil),
		"attachment":               fipAttachmentNull(),
		"id":                       tftypes.NewValue(tftypes.String, "fip-read-1"),
		"address":                  tftypes.NewValue(tftypes.String, "203.0.113.50"),
		"instance_id":              tftypes.NewValue(tftypes.String, nil),
		"tags":                     tftypes.NewValue(tftypes.Map{ElementType: tftypes.String}, nil),
		"status":                   tftypes.NewValue(tftypes.String, "available"),
		"private_ip":               tftypes.NewValue(tftypes.String, nil),
		"created_at":               tftypes.NewValue(tftypes.String, "2025-06-01T12:00:00Z"),
	})

	state := tfsdk.State{Schema: s, Raw: stateVal}
	resp := &resource.ReadResponse{State: state}
	r.Read(context.Background(), resource.ReadRequest{State: state}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected errors: %v", resp.Diagnostics)
	}

	var model PublicIPModel
	resp.State.Get(context.Background(), &model)
	if model.Address.ValueString() != "203.0.113.50" {
		t.Errorf("expected Address 203.0.113.50, got %s", model.Address.ValueString())
	}
}

// TestFIPResourceReadParsesWireContract proves the read parses the backend's
// publicIpAddress / fixedIpAddress / portId fields, and that a present portId
// preserves the configured instance_id.
func TestFIPResourceReadParsesWireContract(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-123/public-ips/fip-wire-1" {
			_, _ = w.Write([]byte(`{
				"id": "fip-wire-1",
				"publicIpAddress": "203.0.113.77",
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
		"acknowledge_address_loss": tftypes.NewValue(tftypes.Bool, nil),
		"attachment":               fipAttachmentNull(),
		"id":                       tftypes.NewValue(tftypes.String, "fip-wire-1"),
		"address":                  tftypes.NewValue(tftypes.String, "203.0.113.77"),
		"instance_id":              tftypes.NewValue(tftypes.String, "inst-kept"),
		"tags":                     tftypes.NewValue(tftypes.Map{ElementType: tftypes.String}, nil),
		"status":                   tftypes.NewValue(tftypes.String, "active"),
		"private_ip":               tftypes.NewValue(tftypes.String, "10.0.5.9"),
		"created_at":               tftypes.NewValue(tftypes.String, "2025-06-01T12:00:00Z"),
	})

	state := tfsdk.State{Schema: s, Raw: stateVal}
	resp := &resource.ReadResponse{State: state}
	r.Read(context.Background(), resource.ReadRequest{State: state}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected errors: %v", resp.Diagnostics)
	}

	var model PublicIPModel
	resp.State.Get(context.Background(), &model)
	if model.Address.ValueString() != "203.0.113.77" {
		t.Errorf("expected Address from publicIpAddress, got %s", model.Address.ValueString())
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
		"acknowledge_address_loss": tftypes.NewValue(tftypes.Bool, nil),
		"attachment":               fipAttachmentNull(),
		"id":                       tftypes.NewValue(tftypes.String, "fip-gone"),
		"address":                  tftypes.NewValue(tftypes.String, "203.0.113.99"),
		"instance_id":              tftypes.NewValue(tftypes.String, nil),
		"tags":                     tftypes.NewValue(tftypes.Map{ElementType: tftypes.String}, nil),
		"status":                   tftypes.NewValue(tftypes.String, "available"),
		"private_ip":               tftypes.NewValue(tftypes.String, nil),
		"created_at":               tftypes.NewValue(tftypes.String, "2025-01-01T00:00:00Z"),
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
	fipResp := apiPublicIP{
		ID:        "fip-upd-1",
		Address:   "203.0.113.50",
		Status:    "available",
		CreatedAt: "2025-06-01T12:00:00Z",
	}
	fipAssocResp := apiPublicIP{
		ID:        "fip-upd-1",
		Address:   "203.0.113.50",
		Status:    "active",
		PortID:    "port-new",
		PrivateIP: "10.0.1.10",
		CreatedAt: "2025-06-01T12:00:00Z",
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/tenants/t-123/public-ips/fip-upd-1/disassociate":
			associated = false
			_ = json.NewEncoder(w).Encode(fipResp)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-123/instances/inst-new":
			writeInstanceWithPort(w, "inst-new", "port-new")
		case r.Method == http.MethodPost && r.URL.Path == "/v1/tenants/t-123/public-ips/fip-upd-1/associate":
			associated = true
			_ = json.NewEncoder(w).Encode(fipAssocResp)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-123/public-ips/fip-upd-1":
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
		"acknowledge_address_loss": tftypes.NewValue(tftypes.Bool, nil),
		"attachment":               fipAttachmentNull(),
		"id":                       tftypes.NewValue(tftypes.String, "fip-upd-1"),
		"address":                  tftypes.NewValue(tftypes.String, "203.0.113.50"),
		"instance_id":              tftypes.NewValue(tftypes.String, "inst-old"),
		"tags":                     tftypes.NewValue(tftypes.Map{ElementType: tftypes.String}, nil),
		"status":                   tftypes.NewValue(tftypes.String, "active"),
		"private_ip":               tftypes.NewValue(tftypes.String, "10.0.1.5"),
		"created_at":               tftypes.NewValue(tftypes.String, "2025-06-01T12:00:00Z"),
	})

	// Plan: change association to inst-new
	planVal := tftypes.NewValue(fipObjectType(), map[string]tftypes.Value{
		"acknowledge_address_loss": tftypes.NewValue(tftypes.Bool, nil),
		"attachment":               fipAttachmentNull(),
		"id":                       tftypes.NewValue(tftypes.String, "fip-upd-1"),
		"address":                  tftypes.NewValue(tftypes.String, "203.0.113.50"),
		"instance_id":              tftypes.NewValue(tftypes.String, "inst-new"),
		"tags":                     tftypes.NewValue(tftypes.Map{ElementType: tftypes.String}, nil),
		"status":                   tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"private_ip":               tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"created_at":               tftypes.NewValue(tftypes.String, "2025-06-01T12:00:00Z"),
	})

	state := tfsdk.State{Schema: s, Raw: stateVal}
	plan := tfsdk.Plan{Schema: s, Raw: planVal}
	resp := &resource.UpdateResponse{State: tfsdk.State{Schema: s}}
	r.Update(context.Background(), resource.UpdateRequest{Plan: plan, State: state}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected errors: %v", resp.Diagnostics)
	}

	var model PublicIPModel
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
		if r.Method == http.MethodDelete && r.URL.Path == "/v1/tenants/t-123/public-ips/fip-del-1" {
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
		"acknowledge_address_loss": tftypes.NewValue(tftypes.Bool, nil),
		"attachment":               fipAttachmentNull(),
		"id":                       tftypes.NewValue(tftypes.String, "fip-del-1"),
		"address":                  tftypes.NewValue(tftypes.String, "203.0.113.50"),
		"instance_id":              tftypes.NewValue(tftypes.String, nil),
		"tags":                     tftypes.NewValue(tftypes.Map{ElementType: tftypes.String}, nil),
		"status":                   tftypes.NewValue(tftypes.String, "available"),
		"private_ip":               tftypes.NewValue(tftypes.String, nil),
		"created_at":               tftypes.NewValue(tftypes.String, "2025-01-01T00:00:00Z"),
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
		"acknowledge_address_loss": tftypes.NewValue(tftypes.Bool, nil),
		"attachment":               fipAttachmentNull(),
		"id":                       tftypes.NewValue(tftypes.String, "fip-already-gone"),
		"address":                  tftypes.NewValue(tftypes.String, "203.0.113.99"),
		"instance_id":              tftypes.NewValue(tftypes.String, nil),
		"tags":                     tftypes.NewValue(tftypes.Map{ElementType: tftypes.String}, nil),
		"status":                   tftypes.NewValue(tftypes.String, "available"),
		"private_ip":               tftypes.NewValue(tftypes.String, nil),
		"created_at":               tftypes.NewValue(tftypes.String, "2025-01-01T00:00:00Z"),
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
		if r.Method == http.MethodPost && r.URL.Path == "/v1/tenants/t-123/public-ips" {
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
		"acknowledge_address_loss": tftypes.NewValue(tftypes.Bool, nil),
		"attachment":               fipAttachmentNull(),
		"id":                       tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"address":                  tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"instance_id":              tftypes.NewValue(tftypes.String, nil),
		"tags":                     tftypes.NewValue(tftypes.Map{ElementType: tftypes.String}, nil),
		"status":                   tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"private_ip":               tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"created_at":               tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
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
		if r.Method == http.MethodPost && r.URL.Path == "/v1/tenants/t-123/public-ips" {
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
		"acknowledge_address_loss": tftypes.NewValue(tftypes.Bool, nil),
		"attachment":               fipAttachmentNull(),
		"id":                       tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"address":                  tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"instance_id":              tftypes.NewValue(tftypes.String, nil),
		"tags":                     tftypes.NewValue(tftypes.Map{ElementType: tftypes.String}, nil),
		"status":                   tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"private_ip":               tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"created_at":               tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
	})

	plan := tfsdk.Plan{Schema: s, Raw: planVal}
	resp := &resource.CreateResponse{State: tfsdk.State{Schema: s}}
	r.Create(context.Background(), resource.CreateRequest{Plan: plan}, resp)

	if !resp.Diagnostics.HasError() {
		t.Error("expected error for bad response body")
	}
}

func TestFIPResourceCreateAssociationError(t *testing.T) {
	fipResp := apiPublicIP{
		ID:        "fip-ae-1",
		Address:   "203.0.113.55",
		Status:    "available",
		CreatedAt: "2025-06-01T12:00:00Z",
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/tenants/t-123/public-ips":
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(fipResp)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-123/instances/inst-fail":
			writeInstanceWithPort(w, "inst-fail", "port-fail")
		case r.Method == http.MethodPost && r.URL.Path == "/v1/tenants/t-123/public-ips/fip-ae-1/associate":
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
		"acknowledge_address_loss": tftypes.NewValue(tftypes.Bool, nil),
		"attachment":               fipAttachmentNull(),
		"id":                       tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"address":                  tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"instance_id":              tftypes.NewValue(tftypes.String, "inst-fail"),
		"tags":                     tftypes.NewValue(tftypes.Map{ElementType: tftypes.String}, nil),
		"status":                   tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"private_ip":               tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"created_at":               tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
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
	fipResp := apiPublicIP{
		ID:        "fip-rp-1",
		Address:   "203.0.113.56",
		Status:    "available",
		CreatedAt: "2025-06-01T12:00:00Z",
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/tenants/t-123/public-ips":
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
		"acknowledge_address_loss": tftypes.NewValue(tftypes.Bool, nil),
		"attachment":               fipAttachmentNull(),
		"id":                       tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"address":                  tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"instance_id":              tftypes.NewValue(tftypes.String, "inst-noport"),
		"tags":                     tftypes.NewValue(tftypes.Map{ElementType: tftypes.String}, nil),
		"status":                   tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"private_ip":               tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"created_at":               tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
	})

	plan := tfsdk.Plan{Schema: s, Raw: planVal}
	resp := &resource.CreateResponse{State: tfsdk.State{Schema: s}}
	r.Create(context.Background(), resource.CreateRequest{Plan: plan}, resp)

	if !resp.Diagnostics.HasError() {
		t.Error("expected error when the instance has no network port")
	}
}

func TestFIPResourceCreateAssociationBadResponseThenReread(t *testing.T) {
	fipAllocated := apiPublicIP{
		ID:        "fip-reread-1",
		Address:   "203.0.113.60",
		Status:    "available",
		CreatedAt: "2025-06-01T12:00:00Z",
	}

	fipAssociated := apiPublicIP{
		ID:        "fip-reread-1",
		Address:   "203.0.113.60",
		Status:    "active",
		PortID:    "port-789",
		PrivateIP: "10.0.1.20",
		CreatedAt: "2025-06-01T12:00:00Z",
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/tenants/t-123/public-ips":
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(fipAllocated)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-123/instances/inst-789":
			writeInstanceWithPort(w, "inst-789", "port-789")
		case r.Method == http.MethodPost && r.URL.Path == "/v1/tenants/t-123/public-ips/fip-reread-1/associate":
			// Return a non-JSON body; the provider does not parse a sync (200) associate body.
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("ok"))
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-123/public-ips/fip-reread-1":
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
		"acknowledge_address_loss": tftypes.NewValue(tftypes.Bool, nil),
		"attachment":               fipAttachmentNull(),
		"id":                       tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"address":                  tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"instance_id":              tftypes.NewValue(tftypes.String, "inst-789"),
		"tags":                     tftypes.NewValue(tftypes.Map{ElementType: tftypes.String}, nil),
		"status":                   tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"private_ip":               tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"created_at":               tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
	})

	plan := tfsdk.Plan{Schema: s, Raw: planVal}
	resp := &resource.CreateResponse{State: tfsdk.State{Schema: s}}
	r.Create(context.Background(), resource.CreateRequest{Plan: plan}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected errors: %v", resp.Diagnostics)
	}

	var state PublicIPModel
	resp.State.Get(context.Background(), &state)

	if state.InstanceID.ValueString() != "inst-789" {
		t.Errorf("expected InstanceID inst-789, got %s", state.InstanceID.ValueString())
	}
	if state.Status.ValueString() != "active" {
		t.Errorf("expected Status active, got %s", state.Status.ValueString())
	}
}

func TestFIPResourceCreateAssocRereadGetError(t *testing.T) {
	fipAllocated := apiPublicIP{
		ID:        "fip-rre-1",
		Address:   "203.0.113.70",
		Status:    "available",
		CreatedAt: "2025-06-01T12:00:00Z",
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/tenants/t-123/public-ips":
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(fipAllocated)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-123/instances/inst-fail":
			writeInstanceWithPort(w, "inst-fail", "port-fail")
		case r.Method == http.MethodPost && r.URL.Path == "/v1/tenants/t-123/public-ips/fip-rre-1/associate":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("ok"))
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-123/public-ips/fip-rre-1":
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
		"acknowledge_address_loss": tftypes.NewValue(tftypes.Bool, nil),
		"attachment":               fipAttachmentNull(),
		"id":                       tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"address":                  tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"instance_id":              tftypes.NewValue(tftypes.String, "inst-fail"),
		"tags":                     tftypes.NewValue(tftypes.Map{ElementType: tftypes.String}, nil),
		"status":                   tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"private_ip":               tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"created_at":               tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
	})

	plan := tfsdk.Plan{Schema: s, Raw: planVal}
	resp := &resource.CreateResponse{State: tfsdk.State{Schema: s}}
	r.Create(context.Background(), resource.CreateRequest{Plan: plan}, resp)

	if !resp.Diagnostics.HasError() {
		t.Error("expected error when re-read also fails")
	}
}

func TestFIPResourceCreateAssocRereadBadJSON(t *testing.T) {
	fipAllocated := apiPublicIP{
		ID:        "fip-rbj-1",
		Address:   "203.0.113.71",
		Status:    "available",
		CreatedAt: "2025-06-01T12:00:00Z",
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/tenants/t-123/public-ips":
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(fipAllocated)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-123/instances/inst-fail":
			writeInstanceWithPort(w, "inst-fail", "port-fail")
		case r.Method == http.MethodPost && r.URL.Path == "/v1/tenants/t-123/public-ips/fip-rbj-1/associate":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("ok"))
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-123/public-ips/fip-rbj-1":
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
		"acknowledge_address_loss": tftypes.NewValue(tftypes.Bool, nil),
		"attachment":               fipAttachmentNull(),
		"id":                       tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"address":                  tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"instance_id":              tftypes.NewValue(tftypes.String, "inst-fail"),
		"tags":                     tftypes.NewValue(tftypes.Map{ElementType: tftypes.String}, nil),
		"status":                   tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"private_ip":               tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"created_at":               tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
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
		"acknowledge_address_loss": tftypes.NewValue(tftypes.Bool, nil),
		"attachment":               fipAttachmentNull(),
		"id":                       tftypes.NewValue(tftypes.String, "fip-err-1"),
		"address":                  tftypes.NewValue(tftypes.String, "203.0.113.50"),
		"instance_id":              tftypes.NewValue(tftypes.String, nil),
		"tags":                     tftypes.NewValue(tftypes.Map{ElementType: tftypes.String}, nil),
		"status":                   tftypes.NewValue(tftypes.String, "available"),
		"private_ip":               tftypes.NewValue(tftypes.String, nil),
		"created_at":               tftypes.NewValue(tftypes.String, "2025-01-01T00:00:00Z"),
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
		"acknowledge_address_loss": tftypes.NewValue(tftypes.Bool, nil),
		"attachment":               fipAttachmentNull(),
		"id":                       tftypes.NewValue(tftypes.String, "fip-bad-1"),
		"address":                  tftypes.NewValue(tftypes.String, "203.0.113.50"),
		"instance_id":              tftypes.NewValue(tftypes.String, nil),
		"tags":                     tftypes.NewValue(tftypes.Map{ElementType: tftypes.String}, nil),
		"status":                   tftypes.NewValue(tftypes.String, "available"),
		"private_ip":               tftypes.NewValue(tftypes.String, nil),
		"created_at":               tftypes.NewValue(tftypes.String, "2025-01-01T00:00:00Z"),
	})

	state := tfsdk.State{Schema: s, Raw: stateVal}
	resp := &resource.ReadResponse{State: state}
	r.Read(context.Background(), resource.ReadRequest{State: state}, resp)

	if !resp.Diagnostics.HasError() {
		t.Error("expected error for bad JSON in read response")
	}
}

func TestFIPResourceUpdateDisassociateOnly(t *testing.T) {
	fipResp := apiPublicIP{
		ID:        "fip-dis-1",
		Address:   "203.0.113.50",
		Status:    "available",
		CreatedAt: "2025-06-01T12:00:00Z",
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/tenants/t-123/public-ips/fip-dis-1/disassociate":
			_ = json.NewEncoder(w).Encode(fipResp)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-123/public-ips/fip-dis-1":
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
		"acknowledge_address_loss": tftypes.NewValue(tftypes.Bool, nil),
		"attachment":               fipAttachmentNull(),
		"id":                       tftypes.NewValue(tftypes.String, "fip-dis-1"),
		"address":                  tftypes.NewValue(tftypes.String, "203.0.113.50"),
		"instance_id":              tftypes.NewValue(tftypes.String, "inst-old"),
		"tags":                     tftypes.NewValue(tftypes.Map{ElementType: tftypes.String}, nil),
		"status":                   tftypes.NewValue(tftypes.String, "active"),
		"private_ip":               tftypes.NewValue(tftypes.String, "10.0.1.5"),
		"created_at":               tftypes.NewValue(tftypes.String, "2025-06-01T12:00:00Z"),
	})

	// Plan: instance_id removed (null)
	planVal := tftypes.NewValue(fipObjectType(), map[string]tftypes.Value{
		"acknowledge_address_loss": tftypes.NewValue(tftypes.Bool, nil),
		"attachment":               fipAttachmentNull(),
		"id":                       tftypes.NewValue(tftypes.String, "fip-dis-1"),
		"address":                  tftypes.NewValue(tftypes.String, "203.0.113.50"),
		"instance_id":              tftypes.NewValue(tftypes.String, nil),
		"tags":                     tftypes.NewValue(tftypes.Map{ElementType: tftypes.String}, nil),
		"status":                   tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"private_ip":               tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"created_at":               tftypes.NewValue(tftypes.String, "2025-06-01T12:00:00Z"),
	})

	state := tfsdk.State{Schema: s, Raw: stateVal}
	plan := tfsdk.Plan{Schema: s, Raw: planVal}
	resp := &resource.UpdateResponse{State: tfsdk.State{Schema: s}}
	r.Update(context.Background(), resource.UpdateRequest{Plan: plan, State: state}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected errors: %v", resp.Diagnostics)
	}

	var model PublicIPModel
	resp.State.Get(context.Background(), &model)
	if !model.InstanceID.IsNull() {
		t.Errorf("expected InstanceID to be null, got %s", model.InstanceID.ValueString())
	}
	if model.Status.ValueString() != "available" {
		t.Errorf("expected Status available, got %s", model.Status.ValueString())
	}
}

func TestFIPResourceUpdateTagsOnly(t *testing.T) {
	fipResp := apiPublicIP{
		ID:        "fip-tags-1",
		Address:   "203.0.113.50",
		Status:    "available",
		Tags:      map[string]string{"env": "prod"},
		CreatedAt: "2025-06-01T12:00:00Z",
	}

	var patchCalled bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPatch && r.URL.Path == "/v1/tenants/t-123/public-ips/fip-tags-1":
			patchCalled = true
			_ = json.NewEncoder(w).Encode(fipResp)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-123/public-ips/fip-tags-1":
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
		"acknowledge_address_loss": tftypes.NewValue(tftypes.Bool, nil),
		"attachment":               fipAttachmentNull(),
		"id":                       tftypes.NewValue(tftypes.String, "fip-tags-1"),
		"address":                  tftypes.NewValue(tftypes.String, "203.0.113.50"),
		"instance_id":              tftypes.NewValue(tftypes.String, nil),
		"tags":                     tftypes.NewValue(tftypes.Map{ElementType: tftypes.String}, nil),
		"status":                   tftypes.NewValue(tftypes.String, "available"),
		"private_ip":               tftypes.NewValue(tftypes.String, nil),
		"created_at":               tftypes.NewValue(tftypes.String, "2025-06-01T12:00:00Z"),
	})

	// Plan: add tags
	planVal := tftypes.NewValue(fipObjectType(), map[string]tftypes.Value{
		"acknowledge_address_loss": tftypes.NewValue(tftypes.Bool, nil),
		"attachment":               fipAttachmentNull(),
		"id":                       tftypes.NewValue(tftypes.String, "fip-tags-1"),
		"address":                  tftypes.NewValue(tftypes.String, "203.0.113.50"),
		"instance_id":              tftypes.NewValue(tftypes.String, nil),
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
		case r.Method == http.MethodPost && r.URL.Path == "/v1/tenants/t-123/public-ips/fip-de-1/disassociate":
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
		"acknowledge_address_loss": tftypes.NewValue(tftypes.Bool, nil),
		"attachment":               fipAttachmentNull(),
		"id":                       tftypes.NewValue(tftypes.String, "fip-de-1"),
		"address":                  tftypes.NewValue(tftypes.String, "203.0.113.50"),
		"instance_id":              tftypes.NewValue(tftypes.String, "inst-old"),
		"tags":                     tftypes.NewValue(tftypes.Map{ElementType: tftypes.String}, nil),
		"status":                   tftypes.NewValue(tftypes.String, "active"),
		"private_ip":               tftypes.NewValue(tftypes.String, "10.0.1.5"),
		"created_at":               tftypes.NewValue(tftypes.String, "2025-06-01T12:00:00Z"),
	})

	planVal := tftypes.NewValue(fipObjectType(), map[string]tftypes.Value{
		"acknowledge_address_loss": tftypes.NewValue(tftypes.Bool, nil),
		"attachment":               fipAttachmentNull(),
		"id":                       tftypes.NewValue(tftypes.String, "fip-de-1"),
		"address":                  tftypes.NewValue(tftypes.String, "203.0.113.50"),
		"instance_id":              tftypes.NewValue(tftypes.String, nil),
		"tags":                     tftypes.NewValue(tftypes.Map{ElementType: tftypes.String}, nil),
		"status":                   tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"private_ip":               tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"created_at":               tftypes.NewValue(tftypes.String, "2025-06-01T12:00:00Z"),
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
		case r.Method == http.MethodPost && r.URL.Path == "/v1/tenants/t-123/public-ips/fip-ae2-1/disassociate":
			_ = json.NewEncoder(w).Encode(apiPublicIP{})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-123/instances/inst-new":
			writeInstanceWithPort(w, "inst-new", "port-new")
		case r.Method == http.MethodPost && r.URL.Path == "/v1/tenants/t-123/public-ips/fip-ae2-1/associate":
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
		"acknowledge_address_loss": tftypes.NewValue(tftypes.Bool, nil),
		"attachment":               fipAttachmentNull(),
		"id":                       tftypes.NewValue(tftypes.String, "fip-ae2-1"),
		"address":                  tftypes.NewValue(tftypes.String, "203.0.113.50"),
		"instance_id":              tftypes.NewValue(tftypes.String, "inst-old"),
		"tags":                     tftypes.NewValue(tftypes.Map{ElementType: tftypes.String}, nil),
		"status":                   tftypes.NewValue(tftypes.String, "active"),
		"private_ip":               tftypes.NewValue(tftypes.String, "10.0.1.5"),
		"created_at":               tftypes.NewValue(tftypes.String, "2025-06-01T12:00:00Z"),
	})

	planVal := tftypes.NewValue(fipObjectType(), map[string]tftypes.Value{
		"acknowledge_address_loss": tftypes.NewValue(tftypes.Bool, nil),
		"attachment":               fipAttachmentNull(),
		"id":                       tftypes.NewValue(tftypes.String, "fip-ae2-1"),
		"address":                  tftypes.NewValue(tftypes.String, "203.0.113.50"),
		"instance_id":              tftypes.NewValue(tftypes.String, "inst-new"),
		"tags":                     tftypes.NewValue(tftypes.Map{ElementType: tftypes.String}, nil),
		"status":                   tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"private_ip":               tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"created_at":               tftypes.NewValue(tftypes.String, "2025-06-01T12:00:00Z"),
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
		case r.Method == http.MethodPatch && r.URL.Path == "/v1/tenants/t-123/public-ips/fip-pe-1":
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
		"acknowledge_address_loss": tftypes.NewValue(tftypes.Bool, nil),
		"attachment":               fipAttachmentNull(),
		"id":                       tftypes.NewValue(tftypes.String, "fip-pe-1"),
		"address":                  tftypes.NewValue(tftypes.String, "203.0.113.50"),
		"instance_id":              tftypes.NewValue(tftypes.String, nil),
		"tags":                     tftypes.NewValue(tftypes.Map{ElementType: tftypes.String}, nil),
		"status":                   tftypes.NewValue(tftypes.String, "available"),
		"private_ip":               tftypes.NewValue(tftypes.String, nil),
		"created_at":               tftypes.NewValue(tftypes.String, "2025-06-01T12:00:00Z"),
	})

	planVal := tftypes.NewValue(fipObjectType(), map[string]tftypes.Value{
		"acknowledge_address_loss": tftypes.NewValue(tftypes.Bool, nil),
		"attachment":               fipAttachmentNull(),
		"id":                       tftypes.NewValue(tftypes.String, "fip-pe-1"),
		"address":                  tftypes.NewValue(tftypes.String, "203.0.113.50"),
		"instance_id":              tftypes.NewValue(tftypes.String, nil),
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
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-123/public-ips/fip-re-1":
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
		"acknowledge_address_loss": tftypes.NewValue(tftypes.Bool, nil),
		"attachment":               fipAttachmentNull(),
		"id":                       tftypes.NewValue(tftypes.String, "fip-re-1"),
		"address":                  tftypes.NewValue(tftypes.String, "203.0.113.50"),
		"instance_id":              tftypes.NewValue(tftypes.String, nil),
		"tags":                     tftypes.NewValue(tftypes.Map{ElementType: tftypes.String}, nil),
		"status":                   tftypes.NewValue(tftypes.String, "available"),
		"private_ip":               tftypes.NewValue(tftypes.String, nil),
		"created_at":               tftypes.NewValue(tftypes.String, "2025-06-01T12:00:00Z"),
	})

	planVal := tftypes.NewValue(fipObjectType(), map[string]tftypes.Value{
		"acknowledge_address_loss": tftypes.NewValue(tftypes.Bool, nil),
		"attachment":               fipAttachmentNull(),
		"id":                       tftypes.NewValue(tftypes.String, "fip-re-1"),
		"address":                  tftypes.NewValue(tftypes.String, "203.0.113.50"),
		"instance_id":              tftypes.NewValue(tftypes.String, nil),
		"tags":                     tftypes.NewValue(tftypes.Map{ElementType: tftypes.String}, nil),
		"status":                   tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"private_ip":               tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"created_at":               tftypes.NewValue(tftypes.String, "2025-06-01T12:00:00Z"),
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
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-123/public-ips/fip-rbj-1":
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
		"acknowledge_address_loss": tftypes.NewValue(tftypes.Bool, nil),
		"attachment":               fipAttachmentNull(),
		"id":                       tftypes.NewValue(tftypes.String, "fip-rbj-1"),
		"address":                  tftypes.NewValue(tftypes.String, "203.0.113.50"),
		"instance_id":              tftypes.NewValue(tftypes.String, nil),
		"tags":                     tftypes.NewValue(tftypes.Map{ElementType: tftypes.String}, nil),
		"status":                   tftypes.NewValue(tftypes.String, "available"),
		"private_ip":               tftypes.NewValue(tftypes.String, nil),
		"created_at":               tftypes.NewValue(tftypes.String, "2025-06-01T12:00:00Z"),
	})

	planVal := tftypes.NewValue(fipObjectType(), map[string]tftypes.Value{
		"acknowledge_address_loss": tftypes.NewValue(tftypes.Bool, nil),
		"attachment":               fipAttachmentNull(),
		"id":                       tftypes.NewValue(tftypes.String, "fip-rbj-1"),
		"address":                  tftypes.NewValue(tftypes.String, "203.0.113.50"),
		"instance_id":              tftypes.NewValue(tftypes.String, nil),
		"tags":                     tftypes.NewValue(tftypes.Map{ElementType: tftypes.String}, nil),
		"status":                   tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"private_ip":               tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"created_at":               tftypes.NewValue(tftypes.String, "2025-06-01T12:00:00Z"),
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
		"acknowledge_address_loss": tftypes.NewValue(tftypes.Bool, nil),
		"attachment":               fipAttachmentNull(),
		"id":                       tftypes.NewValue(tftypes.String, "fip-del-err"),
		"address":                  tftypes.NewValue(tftypes.String, "203.0.113.50"),
		"instance_id":              tftypes.NewValue(tftypes.String, nil),
		"tags":                     tftypes.NewValue(tftypes.Map{ElementType: tftypes.String}, nil),
		"status":                   tftypes.NewValue(tftypes.String, "available"),
		"private_ip":               tftypes.NewValue(tftypes.String, nil),
		"created_at":               tftypes.NewValue(tftypes.String, "2025-01-01T00:00:00Z"),
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
		"acknowledge_address_loss": tftypes.NewValue(tftypes.Bool, nil),
		"attachment":               fipAttachmentNull(),
		"id":                       tftypes.NewValue(tftypes.String, nil),
		"address":                  tftypes.NewValue(tftypes.String, nil),
		"instance_id":              tftypes.NewValue(tftypes.String, nil),
		"tags":                     tftypes.NewValue(tftypes.Map{ElementType: tftypes.String}, nil),
		"status":                   tftypes.NewValue(tftypes.String, nil),
		"private_ip":               tftypes.NewValue(tftypes.String, nil),
		"created_at":               tftypes.NewValue(tftypes.String, nil),
	})
	importResp := &resource.ImportStateResponse{
		State: tfsdk.State{Schema: s, Raw: stateVal},
	}

	r.(resource.ResourceWithImportState).ImportState(ctx, importReq, importResp)

	if importResp.Diagnostics.HasError() {
		t.Fatalf("ImportState failed: %v", importResp.Diagnostics)
	}

	var model PublicIPModel
	importResp.State.Get(ctx, &model)

	if model.ID.ValueString() != "fip-import-1" {
		t.Errorf("expected ID fip-import-1, got %s", model.ID.ValueString())
	}
}

// Ensure fmt is used.
var _ = fmt.Sprintf

// --- attachment ---

// TestPublicIPAttachmentFromAPI: the platform's explicit statement is taken as
// given, including the VPC whose outbound path the address serves.
func TestPublicIPAttachmentFromAPI(t *testing.T) {
	var m PublicIPModel
	var diags diag.Diagnostics
	m.fromAPI(context.Background(), &apiPublicIP{
		ID: "pip-1", Address: "203.0.113.10", Status: "in_use",
		Attachment: &apiPublicIPAttachment{
			Kind: AttachmentKindGateway, ResourceID: "gw-1", VPCID: "vpc-1",
		},
	}, &diags)
	if diags.HasError() {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}

	if !m.IsGatewayBound() {
		t.Error("a gateway attachment must be recognised as gateway-bound")
	}
	if m.AttachedVPCID() != "vpc-1" {
		t.Errorf("expected vpc-1, got %q", m.AttachedVPCID())
	}
	if m.Attachment.Attributes()["resource_id"].(types.String).ValueString() != "gw-1" {
		t.Errorf("unexpected resource_id: %v", m.Attachment)
	}
}

// TestPublicIPAttachmentDefaultsFromPortID pins the ONE inference drawn from a
// response that carries no attachment object.
//
// A non-empty portId is positive evidence of a port, which is what portId meant
// before the object existed. The ABSENCE of a port is not evidence of the
// absence of an attachment — an outbound source address has never had one — so
// that case must be "unknown", never "none". Calling it "none" would tell a
// practitioner an address is free at the one moment giving it away cannot be
// undone: a platform rolled back below the version that reports attachments
// still has gateway-attached addresses, and answers for them with no object.
//
// Either way the object is non-null, so `attachment.kind` is readable in every
// configuration rather than erroring on a null.
func TestPublicIPAttachmentDefaultsFromPortID(t *testing.T) {
	for _, tc := range []struct {
		name     string
		portID   string
		wantKind string
		wantID   bool
	}{
		{"no port", "", AttachmentKindUnknown, false},
		{"attached to a port", "port-9", AttachmentKindPort, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var m PublicIPModel
			var diags diag.Diagnostics
			m.fromAPI(context.Background(), &apiPublicIP{
				ID: "pip-1", Address: "203.0.113.10", Status: "available", PortID: tc.portID,
			}, &diags)
			if diags.HasError() {
				t.Fatalf("unexpected diagnostics: %v", diags)
			}
			if m.Attachment.IsNull() {
				t.Fatal("attachment must never be null: a configuration reading attachment.kind would break")
			}
			kind := m.Attachment.Attributes()["kind"].(types.String).ValueString()
			if kind != tc.wantKind {
				t.Errorf("expected kind %q, got %q", tc.wantKind, kind)
			}
			gotID := !m.Attachment.Attributes()["resource_id"].(types.String).IsNull()
			if gotID != tc.wantID {
				t.Errorf("resource_id present=%v, want %v", gotID, tc.wantID)
			}
			if m.IsGatewayBound() {
				t.Error("a port attachment is not an gateway binding")
			}
		})
	}
}

// TestPublicIPDestroyPlanWarnsWhenEgressBound is the "not silently
// destroyable" guard.
//
// Nothing else in the plan says the address is in use: `terraform plan` renders
// one ordinary "will be destroyed" line, and `instance_id` is empty precisely
// BECAUSE the address is a VPC's outbound source rather than an instance's. The
// practitioner reads "an unused address goes away" and approves an apply that
// takes a whole VPC off-net.
func TestPublicIPDestroyPlanWarnsWhenEgressBound(t *testing.T) {
	s := fipSchema(t)
	r, ok := NewResource().(resource.ResourceWithModifyPlan)
	if !ok {
		t.Fatal("the resource must implement ModifyPlan so a destroy of an gateway-bound address is " +
			"visible in the PLAN, not only as an apply-time refusal")
	}

	stateVal := tftypes.NewValue(fipObjectType(), map[string]tftypes.Value{
		"acknowledge_address_loss": tftypes.NewValue(tftypes.Bool, nil),
		"id":                       tftypes.NewValue(tftypes.String, "pip-1"),
		"address":                  tftypes.NewValue(tftypes.String, "203.0.113.10"),
		"instance_id":              tftypes.NewValue(tftypes.String, nil),
		"tags":                     tftypes.NewValue(tftypes.Map{ElementType: tftypes.String}, nil),
		"status":                   tftypes.NewValue(tftypes.String, "in_use"),
		"private_ip":               tftypes.NewValue(tftypes.String, nil),
		"created_at":               tftypes.NewValue(tftypes.String, "2026-01-01T00:00:00Z"),
		"attachment":               fipGatewayAttachment("gw-1", "vpc-1"),
	})

	resp := &resource.ModifyPlanResponse{Plan: tfsdk.Plan{Schema: s}}
	r.ModifyPlan(context.Background(), resource.ModifyPlanRequest{
		// A destroy plan: the plan is null, the state is not.
		Plan:  tfsdk.Plan{Schema: s},
		State: tfsdk.State{Schema: s, Raw: stateVal},
	}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("the destroy must not be REFUSED — the correct configuration destroys the gateway "+
			"first, and at plan time the attachment is still recorded: %v", resp.Diagnostics)
	}
	if resp.Diagnostics.WarningsCount() == 0 {
		t.Fatal("destroying a VPC's outbound source address must be surfaced in the plan")
	}
	text := resp.Diagnostics.Warnings()[0].Summary() + "\n" + resp.Diagnostics.Warnings()[0].Detail()
	for _, want := range []string{"vpc-1", "203.0.113.10", "DNS", "frostmoln_gateway"} {
		if !strings.Contains(text, want) {
			t.Errorf("the warning must contain %q, got:\n%s", want, text)
		}
	}
}

// TestPublicIPDestroyPlanSilentWhenUnattached: the warning has to be worth
// reading. An idle address is destroyed with no drama, or the warning becomes
// noise everybody scrolls past.
func TestPublicIPDestroyPlanSilentWhenUnattached(t *testing.T) {
	s := fipSchema(t)
	r := NewResource().(resource.ResourceWithModifyPlan)

	stateVal := tftypes.NewValue(fipObjectType(), map[string]tftypes.Value{
		"acknowledge_address_loss": tftypes.NewValue(tftypes.Bool, nil),
		"id":                       tftypes.NewValue(tftypes.String, "pip-1"),
		"address":                  tftypes.NewValue(tftypes.String, "203.0.113.10"),
		"instance_id":              tftypes.NewValue(tftypes.String, nil),
		"tags":                     tftypes.NewValue(tftypes.Map{ElementType: tftypes.String}, nil),
		"status":                   tftypes.NewValue(tftypes.String, "available"),
		"private_ip":               tftypes.NewValue(tftypes.String, nil),
		"created_at":               tftypes.NewValue(tftypes.String, "2026-01-01T00:00:00Z"),
		"attachment": tftypes.NewValue(fipAttachmentType(), map[string]tftypes.Value{
			"kind":        tftypes.NewValue(tftypes.String, AttachmentKindNone),
			"resource_id": tftypes.NewValue(tftypes.String, nil),
			"vpc_id":      tftypes.NewValue(tftypes.String, nil),
		}),
	})

	resp := &resource.ModifyPlanResponse{Plan: tfsdk.Plan{Schema: s}}
	r.ModifyPlan(context.Background(), resource.ModifyPlanRequest{
		Plan:  tfsdk.Plan{Schema: s},
		State: tfsdk.State{Schema: s, Raw: stateVal},
	}, resp)

	if len(resp.Diagnostics) != 0 {
		t.Errorf("destroying an idle address must be quiet, got %v", resp.Diagnostics)
	}
}

// TestPublicIPModifyPlanRecomputesOnAssociationChange: attachment, private_ip
// and status are all rewritten by an association change. For a Computed-only
// attribute the proposed new state carries the PRIOR value, so without this the
// plan pins the old port and the old private IP as KNOWN and the apply returns
// new ones — "Provider produced inconsistent result after apply", which no
// retry clears.
func TestPublicIPModifyPlanRecomputesOnAssociationChange(t *testing.T) {
	s := fipSchema(t)
	r := NewResource().(resource.ResourceWithModifyPlan)

	fip := func(instanceID string) tftypes.Value {
		inst := tftypes.NewValue(tftypes.String, nil)
		if instanceID != "" {
			inst = tftypes.NewValue(tftypes.String, instanceID)
		}
		return tftypes.NewValue(fipObjectType(), map[string]tftypes.Value{
			"acknowledge_address_loss": tftypes.NewValue(tftypes.Bool, nil),
			"id":                       tftypes.NewValue(tftypes.String, "pip-1"),
			"address":                  tftypes.NewValue(tftypes.String, "203.0.113.10"),
			"instance_id":              inst,
			"tags":                     tftypes.NewValue(tftypes.Map{ElementType: tftypes.String}, nil),
			"status":                   tftypes.NewValue(tftypes.String, "in_use"),
			"private_ip":               tftypes.NewValue(tftypes.String, "10.0.0.5"),
			"created_at":               tftypes.NewValue(tftypes.String, "2026-01-01T00:00:00Z"),
			"attachment": tftypes.NewValue(fipAttachmentType(), map[string]tftypes.Value{
				"kind":        tftypes.NewValue(tftypes.String, AttachmentKindPort),
				"resource_id": tftypes.NewValue(tftypes.String, "port-old"),
				"vpc_id":      tftypes.NewValue(tftypes.String, nil),
			}),
		})
	}

	t.Run("moved to another instance", func(t *testing.T) {
		resp := &resource.ModifyPlanResponse{Plan: tfsdk.Plan{Schema: s, Raw: fip("inst-new")}}
		r.ModifyPlan(context.Background(), resource.ModifyPlanRequest{
			Plan:  tfsdk.Plan{Schema: s, Raw: fip("inst-new")},
			State: tfsdk.State{Schema: s, Raw: fip("inst-old")},
		}, resp)
		if resp.Diagnostics.HasError() {
			t.Fatalf("unexpected diagnostics: %v", resp.Diagnostics)
		}

		var planned PublicIPModel
		resp.Plan.Get(context.Background(), &planned)
		if !planned.Attachment.IsUnknown() {
			t.Errorf("attachment must be re-planned across an association change, got %v", planned.Attachment)
		}
		if !planned.PrivateIP.IsUnknown() || !planned.Status.IsUnknown() {
			t.Errorf("private_ip and status must be re-planned too, got %v / %v", planned.PrivateIP, planned.Status)
		}
	})

	t.Run("association unchanged", func(t *testing.T) {
		resp := &resource.ModifyPlanResponse{Plan: tfsdk.Plan{Schema: s, Raw: fip("inst-old")}}
		r.ModifyPlan(context.Background(), resource.ModifyPlanRequest{
			Plan:  tfsdk.Plan{Schema: s, Raw: fip("inst-old")},
			State: tfsdk.State{Schema: s, Raw: fip("inst-old")},
		}, resp)

		var planned PublicIPModel
		resp.Plan.Get(context.Background(), &planned)
		if planned.Attachment.IsUnknown() || planned.PrivateIP.IsUnknown() || planned.Status.IsUnknown() {
			t.Error("with the association unchanged nothing may be re-planned — an unknown against a " +
				"known state value is a diff on every single run")
		}
	})
}

// TestPublicIPDeleteInUseByGateway: the platform's SYNCHRONOUS 409 — the
// case it can still see, because this resource was destroyed on its own with
// the gateway left up. Unreadable as a raw envelope, since the address looks
// unused from every other angle. The acknowledgement is set here so the request
// is actually sent; the local gate has its own test.
func TestPublicIPDeleteInUseByGateway(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusConflict)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"error": map[string]string{
				"code":    "PUBLIC_IP_IN_USE_BY_GATEWAY",
				"message": "public IP is the outbound source address of vpc-1",
			},
		})
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key") // pragma: allowlist secret
	c.SetTenantIDForTest("t-123")
	r := NewResource()
	r.(resource.ResourceWithConfigure).Configure(context.Background(),
		resource.ConfigureRequest{ProviderData: c}, &resource.ConfigureResponse{})

	s := fipSchema(t)
	stateVal := fipGatewayBoundState(true)

	resp := &resource.DeleteResponse{State: tfsdk.State{Schema: s, Raw: stateVal}}
	r.Delete(context.Background(), resource.DeleteRequest{
		State: tfsdk.State{Schema: s, Raw: stateVal},
	}, resp)

	if !resp.Diagnostics.HasError() {
		t.Fatal("expected the release to be refused")
	}
	var text string
	for _, d := range resp.Diagnostics.Errors() {
		text += d.Summary() + "\n" + d.Detail() + "\n"
	}
	if !strings.Contains(text, "frostmoln_gateway") {
		t.Errorf("the diagnostic must point at the resource that holds the address:\n%s", text)
	}
	if !strings.Contains(text, "Nothing was changed") {
		t.Errorf("the diagnostic must say nothing was changed:\n%s", text)
	}
	if !strings.Contains(text, "public IP is the outbound source address of vpc-1") {
		t.Errorf("the diagnostic must still quote what the API said:\n%s", text)
	}
}

// TestPublicIPSchemaExposesAttachment: a practitioner has to be able to tell an
// gateway-bound address from an unattached one, and `instance_id` cannot do it.
func TestPublicIPSchemaExposesAttachment(t *testing.T) {
	s := fipSchema(t)
	att, ok := s.Attributes["attachment"].(schema.SingleNestedAttribute)
	if !ok {
		t.Fatalf("expected attachment to be a SingleNestedAttribute, got %T", s.Attributes["attachment"])
	}
	if !att.Computed || att.Optional || att.Required {
		t.Error("attachment is observed, never configured: it must be Computed-only")
	}
	for _, name := range []string{"kind", "resource_id", "vpc_id"} {
		if _, ok := att.Attributes[name]; !ok {
			t.Errorf("expected %q inside attachment", name)
		}
	}
	kind := att.Attributes["kind"].GetDescription()
	if !strings.Contains(kind, AttachmentKindGateway) {
		t.Errorf("kind must document the gateway value, got:\n%s", kind)
	}
}

// --- the address-loss gate ---

// TestPublicIPDeleteRefusesEgressBoundWithoutAcknowledgement is THE regression
// guard for the irreversible action on this surface.
//
// The platform CANNOT refuse this on the path a correct configuration produces.
// `frostmoln_gateway.public_ip_id` makes the gateway depend on this
// resource, so `terraform destroy` destroys the gateway FIRST; the platform
// hands the address back as an ordinary unattached address
// (`network/internal/service/impl/gateway_public_ip.go`
// releasePinnedAddress clears the gateway binding), and the DELETE that follows
// is — from its side — the release of something idle. It succeeds. The address
// returns to a shared pool and is re-issued to whoever asks next; a partner's
// allow-list entry and the DNS record stop matching, and nothing brings it
// back.
//
// This provider is therefore the only thing that can refuse it, and it refuses
// from the PRE-APPLY state, which still records the binding the gateway's
// destroy has just undone.
//
// Remove the gate and this test must fail: the request goes out.
func TestPublicIPDeleteRefusesEgressBoundWithoutAcknowledgement(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key") // pragma: allowlist secret
	c.SetTenantIDForTest("t-123")
	r := NewResource()
	r.(resource.ResourceWithConfigure).Configure(context.Background(),
		resource.ConfigureRequest{ProviderData: c}, &resource.ConfigureResponse{})

	s := fipSchema(t)
	stateVal := fipGatewayBoundState(false)

	resp := &resource.DeleteResponse{State: tfsdk.State{Schema: s, Raw: stateVal}}
	r.Delete(context.Background(), resource.DeleteRequest{
		State: tfsdk.State{Schema: s, Raw: stateVal},
	}, resp)

	if !resp.Diagnostics.HasError() {
		t.Fatal("releasing a VPC's outbound source address must be refused without an explicit " +
			"acknowledgement — the address does not come back")
	}
	if calls != 0 {
		t.Errorf("the refusal must happen BEFORE any request is sent; the server saw %d call(s), so the "+
			"address may already be gone", calls)
	}
	text := fipDiagText(resp.Diagnostics)
	if !strings.Contains(text, "acknowledge_address_loss") {
		t.Errorf("the refusal must name the attribute that allows it:\n%s", text)
	}
	if !strings.Contains(text, "203.0.113.10") || !strings.Contains(text, "vpc-1") {
		t.Errorf("the refusal must name the address and the VPC that depends on it:\n%s", text)
	}
	if !strings.Contains(text, "Nothing has been changed") {
		t.Errorf("the refusal must say nothing was changed:\n%s", text)
	}
}

// TestPublicIPDeleteProceedsWithAcknowledgement: the gate is a gate, not a
// wall. A practitioner who states the intent gets the destroy.
func TestPublicIPDeleteProceedsWithAcknowledgement(t *testing.T) {
	var gotMethod, gotPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key") // pragma: allowlist secret
	c.SetTenantIDForTest("t-123")
	r := NewResource()
	r.(resource.ResourceWithConfigure).Configure(context.Background(),
		resource.ConfigureRequest{ProviderData: c}, &resource.ConfigureResponse{})

	s := fipSchema(t)
	stateVal := fipGatewayBoundState(true)

	resp := &resource.DeleteResponse{State: tfsdk.State{Schema: s, Raw: stateVal}}
	r.Delete(context.Background(), resource.DeleteRequest{
		State: tfsdk.State{Schema: s, Raw: stateVal},
	}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("with the acknowledgement given the destroy must proceed: %v", resp.Diagnostics)
	}
	if gotMethod != http.MethodDelete || gotPath != "/v1/tenants/t-123/public-ips/pip-1" {
		t.Errorf("unexpected request %s %s", gotMethod, gotPath)
	}
}

// TestPublicIPDeleteRefusesUnrecognisedAttachmentKind: a kind this build does
// not know is the platform stating positively that SOMETHING holds the address
// — the platform's own type documents that new kinds arrive without a schema
// change. Reading it as "free" would make every future attachment kind silently
// destroyable by an older provider, so the switch fails CLOSED.
func TestPublicIPDeleteRefusesUnrecognisedAttachmentKind(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key") // pragma: allowlist secret
	c.SetTenantIDForTest("t-123")
	r := NewResource()
	r.(resource.ResourceWithConfigure).Configure(context.Background(),
		resource.ConfigureRequest{ProviderData: c}, &resource.ConfigureResponse{})

	s := fipSchema(t)
	stateVal := fipStateWithAttachment(fipAttachmentOfKind("ingress_gateway"), false)

	resp := &resource.DeleteResponse{State: tfsdk.State{Schema: s, Raw: stateVal}}
	r.Delete(context.Background(), resource.DeleteRequest{
		State: tfsdk.State{Schema: s, Raw: stateVal},
	}, resp)

	if !resp.Diagnostics.HasError() {
		t.Fatal("an attachment kind this build does not recognise must be treated as attached, not as free")
	}
	if calls != 0 {
		t.Errorf("nothing may be sent, the server saw %d call(s)", calls)
	}
	text := fipDiagText(resp.Diagnostics)
	if !strings.Contains(text, "ingress_gateway") {
		t.Errorf("the refusal must quote the kind it did not recognise:\n%s", text)
	}
	// It must NOT claim a VPC's outbound path is affected — the provider does not know
	// that, and sending the practitioner to check the wrong thing wastes the
	// one moment they are paying attention.
	if strings.Contains(text, "outbound traffic leaves the platform from") {
		t.Errorf("an unrecognised kind must not be described as an gateway binding:\n%s", text)
	}
}

// TestPublicIPDeleteAllowsUnknownAndPortAttachments: the gate must not become a
// wall around ordinary addresses.
//
//   - "unknown" is the platform not answering. Refusing would block every
//     destroy against a build that does not report attachments — a breaking
//     change made on a guess.
//   - "port" is an instance's address, whose association this same resource
//     manages; destroying it has always released the address and the plan shows
//     the instance losing it.
func TestPublicIPDeleteAllowsUnknownAndPortAttachments(t *testing.T) {
	for _, kind := range []string{AttachmentKindUnknown, AttachmentKindPort, AttachmentKindNone} {
		t.Run(kind, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				calls++
				w.WriteHeader(http.StatusNoContent)
			}))
			defer server.Close()

			c := client.NewClient(server.URL, "test-key") // pragma: allowlist secret
			c.SetTenantIDForTest("t-123")
			r := NewResource()
			r.(resource.ResourceWithConfigure).Configure(context.Background(),
				resource.ConfigureRequest{ProviderData: c}, &resource.ConfigureResponse{})

			s := fipSchema(t)
			stateVal := fipStateWithAttachment(fipAttachmentOfKind(kind), false)

			resp := &resource.DeleteResponse{State: tfsdk.State{Schema: s, Raw: stateVal}}
			r.Delete(context.Background(), resource.DeleteRequest{
				State: tfsdk.State{Schema: s, Raw: stateVal},
			}, resp)

			if resp.Diagnostics.HasError() {
				t.Fatalf("kind %q must not need the acknowledgement: %v", kind, resp.Diagnostics)
			}
			if calls != 1 {
				t.Errorf("expected the delete to be sent once, saw %d", calls)
			}
		})
	}
}

// --- the asynchronous release ---

// TestPublicIPDeleteWaitsForTheOperation is the second half of the same
// address-loss story, and it is the one a 204-shaped mental model misses.
//
// DELETE /public-ips/{id} is routed to provisioning, not to network
// (`api-gateway/internal/service/definitions.go`: network-public-ips is
// GET/OPTIONS only; the writes go to provisioning-network-writes). Provisioning
// starts a Temporal workflow and answers 202 IMMEDIATELY, before the platform
// has decided anything (`provisioning/internal/handler/http/network_handler.go`
// DeletePublicIP → operationAccepted).
//
// Treating that 202 as success drops the resource out of Terraform's state
// while the release is still in flight. A refusal then lands on an operation
// nobody reads, the next apply sees the resource missing and plans a create,
// and the create allocates a DIFFERENT address — losing exactly the stable
// address the feature exists to provide.
func TestPublicIPDeleteWaitsForTheOperation(t *testing.T) {
	var polls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"operationId": "op-1", "status": "pending", "resourceType": "public_ip",
			})
			return
		}
		if r.URL.Path == "/v1/operations/op-1" {
			polls++
			status := "running"
			if polls > 1 {
				status = "failed"
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"operationId": "op-1", "status": status, "resourceType": "public_ip",
				"error": "PUBLIC_IP_IN_USE_BY_GATEWAY: public IP is the outbound source address of vpc-1",
			})
			return
		}
		http.Error(w, "unexpected", http.StatusNotFound)
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key") // pragma: allowlist secret
	c.SetTenantIDForTest("t-123")
	r := NewResource()
	r.(resource.ResourceWithConfigure).Configure(context.Background(),
		resource.ConfigureRequest{ProviderData: c}, &resource.ConfigureResponse{})

	s := fipSchema(t)
	stateVal := fipGatewayBoundState(true)

	resp := &resource.DeleteResponse{State: tfsdk.State{Schema: s, Raw: stateVal}}
	r.Delete(context.Background(), resource.DeleteRequest{
		State: tfsdk.State{Schema: s, Raw: stateVal},
	}, resp)

	if polls == 0 {
		t.Fatal("a 202 is not a release: Delete must WAIT for the operation before telling Terraform " +
			"the address is gone")
	}
	if !resp.Diagnostics.HasError() {
		t.Fatal("an operation that reports a refusal must FAIL the apply — a clean return removes the " +
			"resource from state and the next apply allocates a different address")
	}
	text := fipDiagText(resp.Diagnostics)
	if !strings.Contains(text, "frostmoln_gateway") {
		t.Errorf("the async refusal must read like the synchronous one:\n%s", text)
	}
	if !strings.Contains(text, "the outbound source address of vpc-1") {
		t.Errorf("the diagnostic must quote what the platform said:\n%s", text)
	}
}

// TestPublicIPDeleteAcceptsACompletedOperation: waiting must not turn an
// ordinary successful release into a failed apply.
func TestPublicIPDeleteAcceptsACompletedOperation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(map[string]any{"operationId": "op-1", "status": "pending"})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"operationId": "op-1", "status": "completed", "resourceId": "pip-1",
		})
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key") // pragma: allowlist secret
	c.SetTenantIDForTest("t-123")
	r := NewResource()
	r.(resource.ResourceWithConfigure).Configure(context.Background(),
		resource.ConfigureRequest{ProviderData: c}, &resource.ConfigureResponse{})

	s := fipSchema(t)
	stateVal := fipStateWithAttachment(fipAttachmentOfKind(AttachmentKindNone), false)

	resp := &resource.DeleteResponse{State: tfsdk.State{Schema: s, Raw: stateVal}}
	r.Delete(context.Background(), resource.DeleteRequest{
		State: tfsdk.State{Schema: s, Raw: stateVal},
	}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("a completed release must succeed: %v", resp.Diagnostics)
	}
}

// TestPublicIPAcknowledgeAddressLossIsOptionalOnly: a Computed default would let
// the provider acknowledge on the practitioner's behalf, which is the entire
// thing this attribute exists to prevent.
func TestPublicIPAcknowledgeAddressLossIsOptionalOnly(t *testing.T) {
	s := fipSchema(t)
	ack, ok := s.Attributes["acknowledge_address_loss"].(schema.BoolAttribute)
	if !ok {
		t.Fatalf("expected acknowledge_address_loss to be a BoolAttribute, got %T",
			s.Attributes["acknowledge_address_loss"])
	}
	if !ack.Optional || ack.Required || ack.Computed {
		t.Error("acknowledge_address_loss must be Optional-only")
	}
	if !strings.Contains(ack.Description, "prevent_destroy") {
		t.Errorf("the attribute must point at Terraform's own plan-time guard, which covers cases this "+
			"one does not:\n%s", ack.Description)
	}
	if !strings.Contains(s.Description, "prevent_destroy") {
		t.Errorf("the resource description must show prevent_destroy: the address is not recoverable, "+
			"and a plan-time guard is the only one that stops `destroy -auto-approve`:\n%s", s.Description)
	}
}

// TestPublicIPDestroyPlanWarnsForUnrecognisedKind: the plan-time half of the
// fail-closed rule. Without it, a kind added after this build silently stops
// the warning as well as the gate.
func TestPublicIPDestroyPlanWarnsForUnrecognisedKind(t *testing.T) {
	s := fipSchema(t)
	r := NewResource().(resource.ResourceWithModifyPlan)

	resp := &resource.ModifyPlanResponse{Plan: tfsdk.Plan{Schema: s}}
	r.ModifyPlan(context.Background(), resource.ModifyPlanRequest{
		Plan:  tfsdk.Plan{Schema: s},
		State: tfsdk.State{Schema: s, Raw: fipStateWithAttachment(fipAttachmentOfKind("ingress_gateway"), false)},
	}, resp)

	if resp.Diagnostics.WarningsCount() == 0 {
		t.Fatal("an unrecognised attachment kind must still be surfaced in the plan")
	}
}

// TestPublicIPDestroyPlanQuietForUnknownAttachment: "the platform did not say"
// must neither warn nor reassure. A warning on every address against a build
// that does not report attachments is noise, and noise is what makes the real
// warning invisible.
func TestPublicIPDestroyPlanQuietForUnknownAttachment(t *testing.T) {
	s := fipSchema(t)
	r := NewResource().(resource.ResourceWithModifyPlan)

	resp := &resource.ModifyPlanResponse{Plan: tfsdk.Plan{Schema: s}}
	r.ModifyPlan(context.Background(), resource.ModifyPlanRequest{
		Plan:  tfsdk.Plan{Schema: s},
		State: tfsdk.State{Schema: s, Raw: fipStateWithAttachment(fipAttachmentOfKind(AttachmentKindUnknown), false)},
	}, resp)

	if len(resp.Diagnostics) != 0 {
		t.Errorf("an unreported attachment must be quiet, got %v", resp.Diagnostics)
	}
}

// fipDiagText flattens the error diagnostics so an assertion can match on what
// the practitioner actually reads.
func fipDiagText(diags diag.Diagnostics) string {
	var b strings.Builder
	for _, d := range diags.Errors() {
		b.WriteString(d.Summary())
		b.WriteString("\n")
		b.WriteString(d.Detail())
		b.WriteString("\n")
	}
	return b.String()
}
