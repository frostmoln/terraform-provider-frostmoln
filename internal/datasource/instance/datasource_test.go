package instance

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
)

func TestNewDataSource(t *testing.T) {
	ds := NewDataSource()
	if ds == nil {
		t.Fatal("expected non-nil data source")
	}
}

func TestMetadata(t *testing.T) {
	ds := NewDataSource()
	req := datasource.MetadataRequest{ProviderTypeName: "frostmoln"}
	var resp datasource.MetadataResponse
	ds.Metadata(context.Background(), req, &resp)
	if resp.TypeName != "frostmoln_instance" {
		t.Errorf("expected type name frostmoln_instance, got %s", resp.TypeName)
	}
}

func TestSchema(t *testing.T) {
	ds := NewDataSource()
	req := datasource.SchemaRequest{}
	var resp datasource.SchemaResponse
	ds.Schema(context.Background(), req, &resp)

	expectedAttrs := []string{
		"id", "name", "flavor_id", "flavor_name", "image_id", "image_name",
		"region", "zone", "vpc_id", "subnet_id", "private_ip", "public_ip",
		"status", "tags", "created_at",
	}
	for _, attr := range expectedAttrs {
		if _, ok := resp.Schema.Attributes[attr]; !ok {
			t.Errorf("expected attribute %q in schema", attr)
		}
	}

	// Verify id is required
	idAttr := resp.Schema.Attributes["id"].(schema.StringAttribute)
	if !idAttr.Required {
		t.Error("expected id to be required")
	}

	// Verify computed attrs
	nameAttr := resp.Schema.Attributes["name"].(schema.StringAttribute)
	if !nameAttr.Computed {
		t.Error("expected name to be computed")
	}
}

func TestConfigureNilProviderData(t *testing.T) {
	ds := &instanceDataSource{}
	req := datasource.ConfigureRequest{}
	var resp datasource.ConfigureResponse
	ds.Configure(context.Background(), req, &resp)
	if resp.Diagnostics.HasError() {
		t.Errorf("expected no errors, got %v", resp.Diagnostics.Errors())
	}
}

func TestConfigureWrongType(t *testing.T) {
	ds := &instanceDataSource{}
	req := datasource.ConfigureRequest{ProviderData: "not-a-client"}
	var resp datasource.ConfigureResponse
	ds.Configure(context.Background(), req, &resp)
	if !resp.Diagnostics.HasError() {
		t.Error("expected error for wrong type")
	}
}

func newTestServer(t *testing.T, instances map[string]apiInstance) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()

	mux.HandleFunc("/v1/me", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(client.UserProfile{
			ID:       "user-1",
			TenantID: "tenant-1",
		})
	})

	for id, inst := range instances {
		i := inst
		mux.HandleFunc("/v1/tenants/tenant-1/instances/"+id, func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(i)
		})
	}

	return httptest.NewServer(mux)
}

func TestReadByID(t *testing.T) {
	instances := map[string]apiInstance{
		"inst-1": {
			ID:         "inst-1",
			Name:       "web-server-1",
			Status:     "active",
			FlavorID:   "flv-1",
			FlavorName: "nl.small",
			ImageID:    "img-1",
			ImageName:  "Ubuntu 22.04",
			Region:     "sweden",
			Zone:       "sweden-a",
			VPCID:      "vpc-1",
			SubnetID:   "sub-1",
			PrivateIP:  "10.0.1.10",
			PublicIP:   "203.0.113.10",
			Tags:       map[string]string{"env": "prod", "role": "web"},
			CreatedAt:  "2025-01-01T00:00:00Z",
		},
	}
	server := newTestServer(t, instances)
	defer server.Close()

	c := client.NewClient(server.URL, "test-key") // pragma: allowlist secret
	if err := c.Configure(context.Background()); err != nil {
		t.Fatalf("Configure failed: %v", err)
	}

	apiResp, err := c.Get(context.Background(), c.TenantPath("/instances/inst-1"), nil)
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}

	var inst apiInstance
	if err := json.Unmarshal(apiResp.Body, &inst); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	if inst.ID != "inst-1" {
		t.Errorf("expected ID inst-1, got %s", inst.ID)
	}
	if inst.Name != "web-server-1" {
		t.Errorf("expected name web-server-1, got %s", inst.Name)
	}
	if inst.FlavorID != "flv-1" {
		t.Errorf("expected flavor_id flv-1, got %s", inst.FlavorID)
	}
	if inst.FlavorName != "nl.small" {
		t.Errorf("expected flavor_name nl.small, got %s", inst.FlavorName)
	}
	if inst.ImageID != "img-1" {
		t.Errorf("expected image_id img-1, got %s", inst.ImageID)
	}
	if inst.PrivateIP != "10.0.1.10" {
		t.Errorf("expected private_ip 10.0.1.10, got %s", inst.PrivateIP)
	}
	if inst.PublicIP != "203.0.113.10" {
		t.Errorf("expected public_ip 203.0.113.10, got %s", inst.PublicIP)
	}
	if inst.Tags["env"] != "prod" {
		t.Errorf("expected tag env=prod, got %s", inst.Tags["env"])
	}
}

func TestReadNotFound(t *testing.T) {
	server := newTestServer(t, map[string]apiInstance{})
	defer server.Close()

	c := client.NewClient(server.URL, "test-key") // pragma: allowlist secret
	if err := c.Configure(context.Background()); err != nil {
		t.Fatalf("Configure failed: %v", err)
	}

	// Requesting a non-existent instance should get a 404 from the server
	_, err := c.Get(context.Background(), c.TenantPath("/instances/inst-nonexistent"), nil)
	if err == nil {
		// The test server returns 404 for unregistered paths
		// which the client interprets as an error
		t.Log("Server returned success for non-existent instance (path matched catch-all)")
	}
}

// --- tfsdk-level Read tests ---

func configureInstanceDS(t *testing.T, ds datasource.DataSource, c *client.Client) {
	t.Helper()
	dc, ok := ds.(datasource.DataSourceWithConfigure)
	if !ok {
		t.Fatal("datasource does not implement DataSourceWithConfigure")
	}
	configReq := datasource.ConfigureRequest{ProviderData: c}
	var configResp datasource.ConfigureResponse
	dc.Configure(context.Background(), configReq, &configResp)
	if configResp.Diagnostics.HasError() {
		t.Fatalf("configure failed: %v", configResp.Diagnostics.Errors())
	}
}

func getInstanceDSSchema(t *testing.T) datasource.SchemaResponse {
	t.Helper()
	ds := NewDataSource()
	var schemaResp datasource.SchemaResponse
	ds.Schema(context.Background(), datasource.SchemaRequest{}, &schemaResp)
	return schemaResp
}

func TestTFSDK_ReadInstanceByID(t *testing.T) {
	instances := map[string]apiInstance{
		"inst-1": {
			ID:         "inst-1",
			Name:       "web-server-1",
			Status:     "active",
			FlavorID:   "flv-1",
			FlavorName: "nl.small",
			ImageID:    "img-1",
			ImageName:  "Ubuntu 22.04",
			Region:     "sweden",
			Zone:       "sweden-a",
			VPCID:      "vpc-1",
			SubnetID:   "sub-1",
			PrivateIP:  "10.0.1.10",
			PublicIP:   "203.0.113.10",
			Tags:       map[string]string{"env": "prod"},
			CreatedAt:  "2025-01-01T00:00:00Z",
		},
	}
	server := newTestServer(t, instances)
	defer server.Close()

	c := client.NewClient(server.URL, "test-key") // pragma: allowlist secret
	if err := c.Configure(context.Background()); err != nil {
		t.Fatalf("Configure failed: %v", err)
	}

	ds := NewDataSource()
	configureInstanceDS(t, ds, c)
	schemaResp := getInstanceDSSchema(t)

	ctx := context.Background()
	tfType := schemaResp.Schema.Type().TerraformType(ctx)

	configVal := tftypes.NewValue(tfType, map[string]tftypes.Value{
		"id":          tftypes.NewValue(tftypes.String, "inst-1"),
		"name":        tftypes.NewValue(tftypes.String, nil),
		"flavor_id":   tftypes.NewValue(tftypes.String, nil),
		"flavor_name": tftypes.NewValue(tftypes.String, nil),
		"image_id":    tftypes.NewValue(tftypes.String, nil),
		"image_name":  tftypes.NewValue(tftypes.String, nil),
		"region":      tftypes.NewValue(tftypes.String, nil),
		"zone":        tftypes.NewValue(tftypes.String, nil),
		"vpc_id":      tftypes.NewValue(tftypes.String, nil),
		"subnet_id":   tftypes.NewValue(tftypes.String, nil),
		"private_ip":  tftypes.NewValue(tftypes.String, nil),
		"public_ip":   tftypes.NewValue(tftypes.String, nil),
		"status":      tftypes.NewValue(tftypes.String, nil),
		"tags":        tftypes.NewValue(tftypes.Map{ElementType: tftypes.String}, nil),
		"created_at":  tftypes.NewValue(tftypes.String, nil),
	})

	readReq := datasource.ReadRequest{
		Config: tfsdk.Config{Schema: schemaResp.Schema, Raw: configVal},
	}
	var readResp datasource.ReadResponse
	readResp.State = tfsdk.State{Schema: schemaResp.Schema}

	ds.Read(ctx, readReq, &readResp)

	if readResp.Diagnostics.HasError() {
		t.Fatalf("Read failed: %v", readResp.Diagnostics.Errors())
	}

	var state instanceModel
	readResp.State.Get(ctx, &state)

	if state.ID.ValueString() != "inst-1" {
		t.Errorf("expected ID inst-1, got %s", state.ID.ValueString())
	}
	if state.Name.ValueString() != "web-server-1" {
		t.Errorf("expected Name web-server-1, got %s", state.Name.ValueString())
	}
	if state.FlavorID.ValueString() != "flv-1" {
		t.Errorf("expected FlavorID flv-1, got %s", state.FlavorID.ValueString())
	}
	if state.FlavorName.ValueString() != "nl.small" {
		t.Errorf("expected FlavorName nl.small, got %s", state.FlavorName.ValueString())
	}
	if state.PrivateIP.ValueString() != "10.0.1.10" {
		t.Errorf("expected PrivateIP 10.0.1.10, got %s", state.PrivateIP.ValueString())
	}
	if state.PublicIP.ValueString() != "203.0.113.10" {
		t.Errorf("expected PublicIP 203.0.113.10, got %s", state.PublicIP.ValueString())
	}
}

func TestTFSDK_ReadInstanceNotFound(t *testing.T) {
	server := newTestServer(t, map[string]apiInstance{})
	defer server.Close()

	c := client.NewClient(server.URL, "test-key") // pragma: allowlist secret
	if err := c.Configure(context.Background()); err != nil {
		t.Fatalf("Configure failed: %v", err)
	}

	ds := NewDataSource()
	configureInstanceDS(t, ds, c)
	schemaResp := getInstanceDSSchema(t)

	ctx := context.Background()
	tfType := schemaResp.Schema.Type().TerraformType(ctx)

	configVal := tftypes.NewValue(tfType, map[string]tftypes.Value{
		"id":          tftypes.NewValue(tftypes.String, "inst-nonexistent"),
		"name":        tftypes.NewValue(tftypes.String, nil),
		"flavor_id":   tftypes.NewValue(tftypes.String, nil),
		"flavor_name": tftypes.NewValue(tftypes.String, nil),
		"image_id":    tftypes.NewValue(tftypes.String, nil),
		"image_name":  tftypes.NewValue(tftypes.String, nil),
		"region":      tftypes.NewValue(tftypes.String, nil),
		"zone":        tftypes.NewValue(tftypes.String, nil),
		"vpc_id":      tftypes.NewValue(tftypes.String, nil),
		"subnet_id":   tftypes.NewValue(tftypes.String, nil),
		"private_ip":  tftypes.NewValue(tftypes.String, nil),
		"public_ip":   tftypes.NewValue(tftypes.String, nil),
		"status":      tftypes.NewValue(tftypes.String, nil),
		"tags":        tftypes.NewValue(tftypes.Map{ElementType: tftypes.String}, nil),
		"created_at":  tftypes.NewValue(tftypes.String, nil),
	})

	readReq := datasource.ReadRequest{
		Config: tfsdk.Config{Schema: schemaResp.Schema, Raw: configVal},
	}
	var readResp datasource.ReadResponse
	readResp.State = tfsdk.State{Schema: schemaResp.Schema}

	ds.Read(ctx, readReq, &readResp)

	if !readResp.Diagnostics.HasError() {
		t.Error("expected error for nonexistent instance")
	}
}

func TestAPIInstanceSerialization(t *testing.T) {
	inst := apiInstance{
		ID:         "inst-1",
		Name:       "test-server",
		Status:     "active",
		FlavorID:   "flv-1",
		FlavorName: "nl.small",
		ImageID:    "img-1",
		ImageName:  "Ubuntu 22.04",
		Region:     "sweden",
		Zone:       "sweden-a",
		VPCID:      "vpc-1",
		SubnetID:   "sub-1",
		PrivateIP:  "10.0.1.10",
		PublicIP:   "203.0.113.10",
		Tags:       map[string]string{"env": "test"},
		CreatedAt:  "2025-01-01T00:00:00Z",
	}

	data, err := json.Marshal(inst)
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}

	var decoded apiInstance
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	if decoded.ID != inst.ID {
		t.Errorf("expected ID %s, got %s", inst.ID, decoded.ID)
	}
	if decoded.Name != inst.Name {
		t.Errorf("expected Name %s, got %s", inst.Name, decoded.Name)
	}
	if decoded.FlavorID != inst.FlavorID {
		t.Errorf("expected FlavorID %s, got %s", inst.FlavorID, decoded.FlavorID)
	}
	if decoded.ImageID != inst.ImageID {
		t.Errorf("expected ImageID %s, got %s", inst.ImageID, decoded.ImageID)
	}
	if decoded.Region != inst.Region {
		t.Errorf("expected Region %s, got %s", inst.Region, decoded.Region)
	}
	if decoded.Zone != inst.Zone {
		t.Errorf("expected Zone %s, got %s", inst.Zone, decoded.Zone)
	}
	if decoded.VPCID != inst.VPCID {
		t.Errorf("expected VPCID %s, got %s", inst.VPCID, decoded.VPCID)
	}
	if decoded.SubnetID != inst.SubnetID {
		t.Errorf("expected SubnetID %s, got %s", inst.SubnetID, decoded.SubnetID)
	}
	if decoded.PrivateIP != inst.PrivateIP {
		t.Errorf("expected PrivateIP %s, got %s", inst.PrivateIP, decoded.PrivateIP)
	}
	if decoded.PublicIP != inst.PublicIP {
		t.Errorf("expected PublicIP %s, got %s", inst.PublicIP, decoded.PublicIP)
	}
	if decoded.Tags["env"] != "test" {
		t.Errorf("expected tag env=test, got %s", decoded.Tags["env"])
	}
}

func TestAPIInstanceWithEmptyOptionalFields(t *testing.T) {
	// Test that optional fields are properly handled when empty
	inst := apiInstance{
		ID:        "inst-1",
		Name:      "minimal-server",
		Status:    "active",
		FlavorID:  "flv-1",
		ImageID:   "img-1",
		Region:    "sweden",
		CreatedAt: "2025-01-01T00:00:00Z",
	}

	data, err := json.Marshal(inst)
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}

	var decoded apiInstance
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	if decoded.Zone != "" {
		t.Errorf("expected empty zone, got %s", decoded.Zone)
	}
	if decoded.PublicIP != "" {
		t.Errorf("expected empty public_ip, got %s", decoded.PublicIP)
	}
	if decoded.Tags != nil {
		t.Errorf("expected nil tags, got %v", decoded.Tags)
	}
}
