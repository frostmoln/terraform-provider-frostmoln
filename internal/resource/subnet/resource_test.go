package subnet

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

func TestSubnetModelFromAPI(t *testing.T) {
	subnet := &apiSubnet{
		ID:           "subnet-123",
		Name:         "test-subnet",
		Description:  "A test subnet",
		CIDR:         "10.0.1.0/24",
		VPCID:        "vpc-456",
		Zone:         "sweden-a",
		GatewayIP:    "10.0.1.1",
		DNSServers:   []string{"8.8.8.8", "8.8.4.4"},
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
	if model.Zone.ValueString() != "sweden-a" {
		t.Errorf("expected Zone sweden-a, got %s", model.Zone.ValueString())
	}
	if model.GatewayIP.ValueString() != "10.0.1.1" {
		t.Errorf("expected GatewayIP 10.0.1.1, got %s", model.GatewayIP.ValueString())
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
		Zone:        types.StringValue("sweden-a"),
		GatewayIP:   types.StringValue("10.0.1.1"),
		DNSServers:  dns,
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
	if req.Zone != "sweden-a" {
		t.Errorf("expected Zone sweden-a, got %s", req.Zone)
	}
	if req.GatewayIP != "10.0.1.1" {
		t.Errorf("expected GatewayIP 10.0.1.1, got %s", req.GatewayIP)
	}
	if len(req.DNSServers) != 1 || req.DNSServers[0] != "1.1.1.1" {
		t.Errorf("expected DNSServers [1.1.1.1], got %v", req.DNSServers)
	}
	if req.Tags["env"] != "prod" {
		t.Errorf("expected tag env=prod, got %v", req.Tags)
	}
}

// TestSubnetCreateRequestDNSWireContract locks the create request to the
// backend contract: provisioning reads DNS servers under `dnsNameservers`,
// while the read response (apiSubnet) still serializes them as `dnsServers`.
func TestSubnetCreateRequestDNSWireContract(t *testing.T) {
	ctx := context.Background()
	dns, _ := types.ListValueFrom(ctx, types.StringType, []string{"1.1.1.1"})
	model := SubnetModel{
		Name:       types.StringValue("dns-subnet"),
		CIDR:       types.StringValue("10.0.1.0/24"),
		VPCID:      types.StringValue("vpc-1"),
		DNSServers: dns,
	}

	var diags diag.Diagnostics
	req := model.toCreateRequest(ctx, &diags)
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
	if _, ok := wire["dnsNameservers"]; !ok {
		t.Errorf("create request must send DNS servers as dnsNameservers, got %s", raw)
	}
	if _, ok := wire["dnsServers"]; ok {
		t.Errorf("create request must not send dnsServers, got %s", raw)
	}
}

// TestSubnetReadParsesDNSServers proves the read response is still parsed from
// the `dnsServers` field (apiSubnet json tag is unchanged).
func TestSubnetReadParsesDNSServers(t *testing.T) {
	var subnet apiSubnet
	if err := json.Unmarshal([]byte(`{
		"id": "subnet-w",
		"name": "wire",
		"cidrBlock": "10.0.9.0/24",
		"vpcId": "vpc-9",
		"status": "active",
		"availableIpCount": 250,
		"dnsServers": ["9.9.9.9"],
		"createdAt": "2025-01-01T00:00:00Z"
	}`), &subnet); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}
	if len(subnet.DNSServers) != 1 || subnet.DNSServers[0] != "9.9.9.9" {
		t.Errorf("expected dnsServers [9.9.9.9], got %v", subnet.DNSServers)
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
			_ = json.NewEncoder(w).Encode(subnetData)

		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-123/subnets/subnet-test-1":
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(subnetData)

		case r.Method == http.MethodPut && r.URL.Path == "/v1/tenants/t-123/subnets/subnet-test-1":
			subnetData.Tags = map[string]string{"env": "updated"}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(subnetData)

		case r.Method == http.MethodDelete && r.URL.Path == "/v1/tenants/t-123/subnets/subnet-test-1":
			w.WriteHeader(http.StatusNoContent)

		default:
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]string{"code": "NOT_FOUND", "message": "not found"})
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
	patchResp, err := c.Put(ctx, c.TenantPath("/subnets/subnet-test-1"), updateReq)
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
		_ = json.NewEncoder(w).Encode(map[string]string{"code": "NOT_FOUND", "message": "subnet not found"})
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

// --- tfsdk-level resource method tests ---

func subnetSchemaHelper(t *testing.T) schema.Schema {
	t.Helper()
	r := NewResource()
	resp := &resource.SchemaResponse{}
	r.Schema(context.Background(), resource.SchemaRequest{}, resp)
	return resp.Schema
}

func subnetObjectType() tftypes.Object {
	return tftypes.Object{
		AttributeTypes: map[string]tftypes.Type{
			"id":            tftypes.String,
			"name":          tftypes.String,
			"description":   tftypes.String,
			"cidr":          tftypes.String,
			"vpc_id":        tftypes.String,
			"zone":          tftypes.String,
			"gateway_ip":    tftypes.String,
			"dns_servers":   tftypes.List{ElementType: tftypes.String},
			"tags":          tftypes.Map{ElementType: tftypes.String},
			"status":        tftypes.String,
			"available_ips": tftypes.Number,
			"created_at":    tftypes.String,
		},
	}
}

func TestSubnetNewResource(t *testing.T) {
	r := NewResource()
	if r == nil {
		t.Fatal("expected non-nil resource")
	}
}

func TestSubnetMetadata(t *testing.T) {
	r := NewResource()
	req := resource.MetadataRequest{ProviderTypeName: "frostmoln"}
	resp := &resource.MetadataResponse{}
	r.Metadata(context.Background(), req, resp)

	if resp.TypeName != "frostmoln_subnet" {
		t.Errorf("expected type name frostmoln_subnet, got %s", resp.TypeName)
	}
}

func TestSubnetSchema(t *testing.T) {
	r := NewResource()
	resp := &resource.SchemaResponse{}
	r.Schema(context.Background(), resource.SchemaRequest{}, resp)

	for _, attr := range []string{"id", "name", "description", "cidr", "vpc_id", "zone", "gateway_ip", "dns_servers", "tags", "status", "available_ips", "created_at"} {
		if _, ok := resp.Schema.Attributes[attr]; !ok {
			t.Errorf("expected attribute %s in schema", attr)
		}
	}
}

func TestSubnetConfigureNilProviderData(t *testing.T) {
	r := NewResource()
	resp := &resource.ConfigureResponse{}
	r.(resource.ResourceWithConfigure).Configure(context.Background(), resource.ConfigureRequest{ProviderData: nil}, resp)
	if resp.Diagnostics.HasError() {
		t.Errorf("expected no errors, got %v", resp.Diagnostics)
	}
}

func TestSubnetConfigureWrongType(t *testing.T) {
	r := NewResource()
	resp := &resource.ConfigureResponse{}
	r.(resource.ResourceWithConfigure).Configure(context.Background(), resource.ConfigureRequest{ProviderData: 3.14}, resp)
	if !resp.Diagnostics.HasError() {
		t.Error("expected error for wrong type")
	}
}

func TestSubnetConfigureValidClient(t *testing.T) {
	r := NewResource()
	c := client.NewClient("http://localhost", "test-key") // pragma: allowlist secret
	resp := &resource.ConfigureResponse{}
	r.(resource.ResourceWithConfigure).Configure(context.Background(), resource.ConfigureRequest{ProviderData: c}, resp)
	if resp.Diagnostics.HasError() {
		t.Errorf("expected no errors, got %v", resp.Diagnostics)
	}
}

func TestSubnetResourceCreate(t *testing.T) {
	subnetResp := apiSubnet{
		ID:           "subnet-new-1",
		Name:         "web-subnet",
		CIDR:         "10.0.1.0/24",
		VPCID:        "vpc-123",
		Zone:         "sweden-a",
		GatewayIP:    "10.0.1.1",
		Status:       "active",
		AvailableIPs: 250,
		CreatedAt:    "2025-06-01T12:00:00Z",
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/v1/tenants/t-123/subnets" {
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(subnetResp)
			return
		}
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]string{"code": "NOT_FOUND", "message": "not found"})
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key") // pragma: allowlist secret
	c.SetTenantIDForTest("t-123")

	r := NewResource()
	r.(resource.ResourceWithConfigure).Configure(context.Background(), resource.ConfigureRequest{ProviderData: c}, &resource.ConfigureResponse{})

	s := subnetSchemaHelper(t)
	planVal := tftypes.NewValue(subnetObjectType(), map[string]tftypes.Value{
		"id":            tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"name":          tftypes.NewValue(tftypes.String, "web-subnet"),
		"description":   tftypes.NewValue(tftypes.String, nil),
		"cidr":          tftypes.NewValue(tftypes.String, "10.0.1.0/24"),
		"vpc_id":        tftypes.NewValue(tftypes.String, "vpc-123"),
		"zone":          tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"gateway_ip":    tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"dns_servers":   tftypes.NewValue(tftypes.List{ElementType: tftypes.String}, nil),
		"tags":          tftypes.NewValue(tftypes.Map{ElementType: tftypes.String}, nil),
		"status":        tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"available_ips": tftypes.NewValue(tftypes.Number, tftypes.UnknownValue),
		"created_at":    tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
	})

	plan := tfsdk.Plan{Schema: s, Raw: planVal}
	resp := &resource.CreateResponse{State: tfsdk.State{Schema: s}}
	r.Create(context.Background(), resource.CreateRequest{Plan: plan}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected errors: %v", resp.Diagnostics)
	}

	var state SubnetModel
	resp.State.Get(context.Background(), &state)

	if state.ID.ValueString() != "subnet-new-1" {
		t.Errorf("expected ID subnet-new-1, got %s", state.ID.ValueString())
	}
	if state.Name.ValueString() != "web-subnet" {
		t.Errorf("expected Name web-subnet, got %s", state.Name.ValueString())
	}
	if state.CIDR.ValueString() != "10.0.1.0/24" {
		t.Errorf("expected CIDR 10.0.1.0/24, got %s", state.CIDR.ValueString())
	}
	if state.Zone.ValueString() != "sweden-a" {
		t.Errorf("expected Zone sweden-a, got %s", state.Zone.ValueString())
	}
	if state.AvailableIPs.ValueInt64() != 250 {
		t.Errorf("expected AvailableIPs 250, got %d", state.AvailableIPs.ValueInt64())
	}
}

func TestSubnetResourceRead(t *testing.T) {
	subnetResp := apiSubnet{
		ID:           "subnet-read-1",
		Name:         "read-subnet",
		Description:  "a test",
		CIDR:         "10.0.2.0/24",
		VPCID:        "vpc-456",
		Status:       "active",
		AvailableIPs: 200,
		CreatedAt:    "2025-06-01T12:00:00Z",
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-123/subnets/subnet-read-1" {
			_ = json.NewEncoder(w).Encode(subnetResp)
			return
		}
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]string{"code": "NOT_FOUND", "message": "not found"})
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key") // pragma: allowlist secret
	c.SetTenantIDForTest("t-123")

	r := NewResource()
	r.(resource.ResourceWithConfigure).Configure(context.Background(), resource.ConfigureRequest{ProviderData: c}, &resource.ConfigureResponse{})

	s := subnetSchemaHelper(t)
	stateVal := tftypes.NewValue(subnetObjectType(), map[string]tftypes.Value{
		"id":            tftypes.NewValue(tftypes.String, "subnet-read-1"),
		"name":          tftypes.NewValue(tftypes.String, "read-subnet"),
		"description":   tftypes.NewValue(tftypes.String, nil),
		"cidr":          tftypes.NewValue(tftypes.String, "10.0.2.0/24"),
		"vpc_id":        tftypes.NewValue(tftypes.String, "vpc-456"),
		"zone":          tftypes.NewValue(tftypes.String, nil),
		"gateway_ip":    tftypes.NewValue(tftypes.String, nil),
		"dns_servers":   tftypes.NewValue(tftypes.List{ElementType: tftypes.String}, nil),
		"tags":          tftypes.NewValue(tftypes.Map{ElementType: tftypes.String}, nil),
		"status":        tftypes.NewValue(tftypes.String, "active"),
		"available_ips": tftypes.NewValue(tftypes.Number, 200),
		"created_at":    tftypes.NewValue(tftypes.String, "2025-06-01T12:00:00Z"),
	})

	state := tfsdk.State{Schema: s, Raw: stateVal}
	resp := &resource.ReadResponse{State: state}
	r.Read(context.Background(), resource.ReadRequest{State: state}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected errors: %v", resp.Diagnostics)
	}

	var model SubnetModel
	resp.State.Get(context.Background(), &model)
	if model.Name.ValueString() != "read-subnet" {
		t.Errorf("expected Name read-subnet, got %s", model.Name.ValueString())
	}
	if model.Description.ValueString() != "a test" {
		t.Errorf("expected Description 'a test', got %s", model.Description.ValueString())
	}
}

func TestSubnetResourceReadNotFoundRemovesState(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]string{"code": "NOT_FOUND", "message": "not found"})
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key") // pragma: allowlist secret
	c.SetTenantIDForTest("t-123")

	r := NewResource()
	r.(resource.ResourceWithConfigure).Configure(context.Background(), resource.ConfigureRequest{ProviderData: c}, &resource.ConfigureResponse{})

	s := subnetSchemaHelper(t)
	stateVal := tftypes.NewValue(subnetObjectType(), map[string]tftypes.Value{
		"id":            tftypes.NewValue(tftypes.String, "subnet-gone"),
		"name":          tftypes.NewValue(tftypes.String, "gone"),
		"description":   tftypes.NewValue(tftypes.String, nil),
		"cidr":          tftypes.NewValue(tftypes.String, "10.0.0.0/24"),
		"vpc_id":        tftypes.NewValue(tftypes.String, "vpc-123"),
		"zone":          tftypes.NewValue(tftypes.String, nil),
		"gateway_ip":    tftypes.NewValue(tftypes.String, nil),
		"dns_servers":   tftypes.NewValue(tftypes.List{ElementType: tftypes.String}, nil),
		"tags":          tftypes.NewValue(tftypes.Map{ElementType: tftypes.String}, nil),
		"status":        tftypes.NewValue(tftypes.String, "active"),
		"available_ips": tftypes.NewValue(tftypes.Number, 250),
		"created_at":    tftypes.NewValue(tftypes.String, "2025-01-01T00:00:00Z"),
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

// The subnet update must actually CARRY the name.
//
// apiUpdateSubnetRequest was tags-only, while `name` is Required with no
// RequiresReplace — so a rename sent nothing, network kept the old name,
// fromAPI wrote the old name back into state, and the apply failed with
// "Provider produced inconsistent result after apply". Nobody saw it because
// the update never reached network: the api-gateway sent PUT /subnets/{id} to
// provisioning, which has no handler for it.
func TestSubnetUpdateSendsTheName(t *testing.T) {
	var body map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/tenants/t-123/subnets/subnet-upd-1" && r.Method == http.MethodPut {
			_ = json.NewDecoder(r.Body).Decode(&body)
			_ = json.NewEncoder(w).Encode(apiSubnet{
				ID: "subnet-upd-1", Name: "upd-subnet", CIDR: "10.0.1.0/24",
				VPCID: "vpc-1", Status: "available", AvailableIPs: 250,
				CreatedAt: "2025-06-01T12:00:00Z",
			})
			return
		}
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]string{"code": "NOT_FOUND", "message": "not found"})
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key") // pragma: allowlist secret
	c.SetTenantIDForTest("t-123")

	r := NewResource()
	r.(resource.ResourceWithConfigure).Configure(context.Background(), resource.ConfigureRequest{ProviderData: c}, &resource.ConfigureResponse{})

	s := subnetSchemaHelper(t)
	mk := func(name string) tftypes.Value {
		return tftypes.NewValue(subnetObjectType(), map[string]tftypes.Value{
			"id":            tftypes.NewValue(tftypes.String, "subnet-upd-1"),
			"name":          tftypes.NewValue(tftypes.String, name),
			"description":   tftypes.NewValue(tftypes.String, nil),
			"cidr":          tftypes.NewValue(tftypes.String, "10.0.1.0/24"),
			"vpc_id":        tftypes.NewValue(tftypes.String, "vpc-1"),
			"zone":          tftypes.NewValue(tftypes.String, nil),
			"gateway_ip":    tftypes.NewValue(tftypes.String, nil),
			"dns_servers":   tftypes.NewValue(tftypes.List{ElementType: tftypes.String}, nil),
			"tags":          tftypes.NewValue(tftypes.Map{ElementType: tftypes.String}, nil),
			"status":        tftypes.NewValue(tftypes.String, "available"),
			"available_ips": tftypes.NewValue(tftypes.Number, 250),
			"created_at":    tftypes.NewValue(tftypes.String, "2025-06-01T12:00:00Z"),
		})
	}

	resp := &resource.UpdateResponse{State: tfsdk.State{Schema: s}}
	r.Update(context.Background(), resource.UpdateRequest{
		Plan:  tfsdk.Plan{Schema: s, Raw: mk("upd-subnet")},
		State: tfsdk.State{Schema: s, Raw: mk("old-subnet")},
	}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected errors: %v", resp.Diagnostics)
	}
	if got, ok := body["name"]; !ok || got != "upd-subnet" {
		t.Fatalf("update body carried name=%v (present=%v); a rename that sends no name is silently dropped", got, ok)
	}
	// tags removed from config must CLEAR, so the field is present and empty —
	// never omitted, which the backend reads as "keep".
	tags, ok := body["tags"]
	if !ok {
		t.Fatal("update body omitted tags entirely — the backend reads that as KEEP, so tags can never be cleared")
	}
	if m, isMap := tags.(map[string]any); !isMap || len(m) != 0 {
		t.Fatalf("tags = %v, want an empty object", tags)
	}
}

func TestSubnetResourceUpdate(t *testing.T) {
	// The in-place update MUST be PUT. network registers PUT /subnets/{id};
	// NOBODY registers PATCH — not network, not provisioning — so a PATCH
	// matches no api-gateway rule and comes back PATH_NOT_ROUTED. The fake
	// answers on ANY verb so a regression reads as "wrong verb", not as a 404.
	var updateMethod string

	subnetResp := apiSubnet{
		ID:           "subnet-upd-1",
		Name:         "upd-subnet",
		CIDR:         "10.0.1.0/24",
		VPCID:        "vpc-123",
		Status:       "active",
		AvailableIPs: 250,
		Tags:         map[string]string{"env": "prod"},
		CreatedAt:    "2025-06-01T12:00:00Z",
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/tenants/t-123/subnets/subnet-upd-1" {
			updateMethod = r.Method
			_ = json.NewEncoder(w).Encode(subnetResp)
			return
		}
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]string{"code": "NOT_FOUND", "message": "not found"})
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key") // pragma: allowlist secret
	c.SetTenantIDForTest("t-123")

	r := NewResource()
	r.(resource.ResourceWithConfigure).Configure(context.Background(), resource.ConfigureRequest{ProviderData: c}, &resource.ConfigureResponse{})

	s := subnetSchemaHelper(t)

	stateVal := tftypes.NewValue(subnetObjectType(), map[string]tftypes.Value{
		"id":            tftypes.NewValue(tftypes.String, "subnet-upd-1"),
		"name":          tftypes.NewValue(tftypes.String, "upd-subnet"),
		"description":   tftypes.NewValue(tftypes.String, nil),
		"cidr":          tftypes.NewValue(tftypes.String, "10.0.1.0/24"),
		"vpc_id":        tftypes.NewValue(tftypes.String, "vpc-123"),
		"zone":          tftypes.NewValue(tftypes.String, nil),
		"gateway_ip":    tftypes.NewValue(tftypes.String, nil),
		"dns_servers":   tftypes.NewValue(tftypes.List{ElementType: tftypes.String}, nil),
		"tags":          tftypes.NewValue(tftypes.Map{ElementType: tftypes.String}, nil),
		"status":        tftypes.NewValue(tftypes.String, "active"),
		"available_ips": tftypes.NewValue(tftypes.Number, 250),
		"created_at":    tftypes.NewValue(tftypes.String, "2025-06-01T12:00:00Z"),
	})

	planVal := tftypes.NewValue(subnetObjectType(), map[string]tftypes.Value{
		"id":          tftypes.NewValue(tftypes.String, "subnet-upd-1"),
		"name":        tftypes.NewValue(tftypes.String, "upd-subnet"),
		"description": tftypes.NewValue(tftypes.String, nil),
		"cidr":        tftypes.NewValue(tftypes.String, "10.0.1.0/24"),
		"vpc_id":      tftypes.NewValue(tftypes.String, "vpc-123"),
		"zone":        tftypes.NewValue(tftypes.String, nil),
		"gateway_ip":  tftypes.NewValue(tftypes.String, nil),
		"dns_servers": tftypes.NewValue(tftypes.List{ElementType: tftypes.String}, nil),
		"tags": tftypes.NewValue(tftypes.Map{ElementType: tftypes.String}, map[string]tftypes.Value{
			"env": tftypes.NewValue(tftypes.String, "prod"),
		}),
		"status":        tftypes.NewValue(tftypes.String, "active"),
		"available_ips": tftypes.NewValue(tftypes.Number, 250),
		"created_at":    tftypes.NewValue(tftypes.String, "2025-06-01T12:00:00Z"),
	})

	state := tfsdk.State{Schema: s, Raw: stateVal}
	plan := tfsdk.Plan{Schema: s, Raw: planVal}
	resp := &resource.UpdateResponse{State: tfsdk.State{Schema: s}}
	r.Update(context.Background(), resource.UpdateRequest{Plan: plan, State: state}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected errors: %v", resp.Diagnostics)
	}

	if updateMethod != http.MethodPut {
		t.Fatalf("in-place subnet update sent %s, want PUT (PATCH is routed nowhere)", updateMethod)
	}

	var model SubnetModel
	resp.State.Get(context.Background(), &model)
	if model.Name.ValueString() != "upd-subnet" {
		t.Errorf("expected Name upd-subnet, got %s", model.Name.ValueString())
	}
}

func TestSubnetResourceDelete(t *testing.T) {
	deleted := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete && r.URL.Path == "/v1/tenants/t-123/subnets/subnet-del-1" {
			deleted = true
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]string{"code": "NOT_FOUND", "message": "not found"})
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key") // pragma: allowlist secret
	c.SetTenantIDForTest("t-123")

	r := NewResource()
	r.(resource.ResourceWithConfigure).Configure(context.Background(), resource.ConfigureRequest{ProviderData: c}, &resource.ConfigureResponse{})

	s := subnetSchemaHelper(t)
	stateVal := tftypes.NewValue(subnetObjectType(), map[string]tftypes.Value{
		"id":            tftypes.NewValue(tftypes.String, "subnet-del-1"),
		"name":          tftypes.NewValue(tftypes.String, "delete-me"),
		"description":   tftypes.NewValue(tftypes.String, nil),
		"cidr":          tftypes.NewValue(tftypes.String, "10.0.0.0/24"),
		"vpc_id":        tftypes.NewValue(tftypes.String, "vpc-123"),
		"zone":          tftypes.NewValue(tftypes.String, nil),
		"gateway_ip":    tftypes.NewValue(tftypes.String, nil),
		"dns_servers":   tftypes.NewValue(tftypes.List{ElementType: tftypes.String}, nil),
		"tags":          tftypes.NewValue(tftypes.Map{ElementType: tftypes.String}, nil),
		"status":        tftypes.NewValue(tftypes.String, "active"),
		"available_ips": tftypes.NewValue(tftypes.Number, 250),
		"created_at":    tftypes.NewValue(tftypes.String, "2025-01-01T00:00:00Z"),
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

func TestSubnetResourceDeleteAlreadyGone(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]string{"code": "NOT_FOUND", "message": "not found"})
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key") // pragma: allowlist secret
	c.SetTenantIDForTest("t-123")

	r := NewResource()
	r.(resource.ResourceWithConfigure).Configure(context.Background(), resource.ConfigureRequest{ProviderData: c}, &resource.ConfigureResponse{})

	s := subnetSchemaHelper(t)
	stateVal := tftypes.NewValue(subnetObjectType(), map[string]tftypes.Value{
		"id":            tftypes.NewValue(tftypes.String, "subnet-gone"),
		"name":          tftypes.NewValue(tftypes.String, "gone"),
		"description":   tftypes.NewValue(tftypes.String, nil),
		"cidr":          tftypes.NewValue(tftypes.String, "10.0.0.0/24"),
		"vpc_id":        tftypes.NewValue(tftypes.String, "vpc-123"),
		"zone":          tftypes.NewValue(tftypes.String, nil),
		"gateway_ip":    tftypes.NewValue(tftypes.String, nil),
		"dns_servers":   tftypes.NewValue(tftypes.List{ElementType: tftypes.String}, nil),
		"tags":          tftypes.NewValue(tftypes.Map{ElementType: tftypes.String}, nil),
		"status":        tftypes.NewValue(tftypes.String, "active"),
		"available_ips": tftypes.NewValue(tftypes.Number, 250),
		"created_at":    tftypes.NewValue(tftypes.String, "2025-01-01T00:00:00Z"),
	})

	state := tfsdk.State{Schema: s, Raw: stateVal}
	resp := &resource.DeleteResponse{}
	r.Delete(context.Background(), resource.DeleteRequest{State: state}, resp)

	if resp.Diagnostics.HasError() {
		t.Errorf("expected no errors when deleting already-gone subnet, got %v", resp.Diagnostics)
	}
}

// TestSubnetResourceDeleteWaitsForOperation asserts Delete waits for the async
// delete operation to complete before returning. This is the fix for the
// replace race (Ambix 019f2176 Bug 2): without the wait, a destroy-then-create
// replace races and the create resolves to the still-deleting old subnet's id.
func TestSubnetResourceDeleteWaitsForOperation(t *testing.T) {
	var opPolled bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodDelete && r.URL.Path == "/v1/tenants/t-123/subnets/subnet-del-1":
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(client.Operation{OperationID: "op-del-1", Status: "pending", ResourceType: "subnet"})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-123/operations/op-del-1":
			opPolled = true
			_ = json.NewEncoder(w).Encode(client.Operation{OperationID: "op-del-1", Status: "completed", ResourceType: "subnet", ResourceID: "subnet-del-1"})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key") // pragma: allowlist secret
	c.SetTenantIDForTest("t-123")

	r := NewResource()
	r.(resource.ResourceWithConfigure).Configure(context.Background(), resource.ConfigureRequest{ProviderData: c}, &resource.ConfigureResponse{})

	resp := &resource.DeleteResponse{}
	r.Delete(context.Background(), resource.DeleteRequest{State: subnetDeleteState(t)}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected errors: %v", resp.Diagnostics)
	}
	if !opPolled {
		t.Error("expected Delete to wait on the delete operation, but the operation was never polled")
	}
}

// TestSubnetResourceDeleteOperationFailed asserts Delete surfaces the real
// workflow error (e.g. subnet in use) when the delete operation fails, instead
// of silently succeeding.
func TestSubnetResourceDeleteOperationFailed(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodDelete:
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(client.Operation{OperationID: "op-del-1", Status: "pending", ResourceType: "subnet"})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-123/operations/op-del-1":
			_ = json.NewEncoder(w).Encode(client.Operation{OperationID: "op-del-1", Status: "failed", ResourceType: "subnet", Error: "subnet has active resources and cannot be deleted"})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key") // pragma: allowlist secret
	c.SetTenantIDForTest("t-123")

	r := NewResource()
	r.(resource.ResourceWithConfigure).Configure(context.Background(), resource.ConfigureRequest{ProviderData: c}, &resource.ConfigureResponse{})

	resp := &resource.DeleteResponse{}
	r.Delete(context.Background(), resource.DeleteRequest{State: subnetDeleteState(t)}, resp)

	if !resp.Diagnostics.HasError() {
		t.Fatal("expected an error when the delete operation fails")
	}
	if !strings.Contains(resp.Diagnostics.Errors()[0].Detail(), "active resources") {
		t.Errorf("expected the real workflow error to be surfaced, got: %s", resp.Diagnostics.Errors()[0].Detail())
	}
}

// subnetDeleteState builds a minimal tfsdk.State for id subnet-del-1.
func subnetDeleteState(t *testing.T) tfsdk.State {
	t.Helper()
	s := subnetSchemaHelper(t)
	stateVal := tftypes.NewValue(subnetObjectType(), map[string]tftypes.Value{
		"id":            tftypes.NewValue(tftypes.String, "subnet-del-1"),
		"name":          tftypes.NewValue(tftypes.String, "delete-me"),
		"description":   tftypes.NewValue(tftypes.String, nil),
		"cidr":          tftypes.NewValue(tftypes.String, "10.0.0.0/24"),
		"vpc_id":        tftypes.NewValue(tftypes.String, "vpc-123"),
		"zone":          tftypes.NewValue(tftypes.String, nil),
		"gateway_ip":    tftypes.NewValue(tftypes.String, nil),
		"dns_servers":   tftypes.NewValue(tftypes.List{ElementType: tftypes.String}, nil),
		"tags":          tftypes.NewValue(tftypes.Map{ElementType: tftypes.String}, nil),
		"status":        tftypes.NewValue(tftypes.String, "active"),
		"available_ips": tftypes.NewValue(tftypes.Number, 250),
		"created_at":    tftypes.NewValue(tftypes.String, "2025-01-01T00:00:00Z"),
	})
	return tfsdk.State{Schema: s, Raw: stateVal}
}

// Ensure fmt is used.
var _ = fmt.Sprintf
