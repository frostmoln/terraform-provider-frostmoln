package flavor

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
	if resp.TypeName != "frostmoln_flavor" {
		t.Errorf("expected type name frostmoln_flavor, got %s", resp.TypeName)
	}
}

func TestSchema(t *testing.T) {
	ds := NewDataSource()
	req := datasource.SchemaRequest{}
	var resp datasource.SchemaResponse
	ds.Schema(context.Background(), req, &resp)

	expectedAttrs := []string{"id", "name", "vcpus", "ram_mb", "disk_gb", "category", "family", "generation", "status", "successor_id", "base_vcpu_ratio", "vcpu_multiplier"}
	for _, attr := range expectedAttrs {
		if _, ok := resp.Schema.Attributes[attr]; !ok {
			t.Errorf("expected attribute %q in schema", attr)
		}
	}

	idAttr := resp.Schema.Attributes["id"].(schema.StringAttribute)
	if !idAttr.Optional {
		t.Error("expected id to be optional")
	}
	nameAttr := resp.Schema.Attributes["name"].(schema.StringAttribute)
	if !nameAttr.Optional {
		t.Error("expected name to be optional")
	}
}

func TestConfigureNilProviderData(t *testing.T) {
	ds := &flavorDataSource{}
	req := datasource.ConfigureRequest{}
	var resp datasource.ConfigureResponse
	ds.Configure(context.Background(), req, &resp)
	if resp.Diagnostics.HasError() {
		t.Errorf("expected no errors, got %v", resp.Diagnostics.Errors())
	}
}

func TestConfigureWrongType(t *testing.T) {
	ds := &flavorDataSource{}
	req := datasource.ConfigureRequest{ProviderData: "not-a-client"}
	var resp datasource.ConfigureResponse
	ds.Configure(context.Background(), req, &resp)
	if !resp.Diagnostics.HasError() {
		t.Error("expected error for wrong type")
	}
}

func newTestServer(t *testing.T, flavors []apiFlavor) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/flavors", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(apiFlavorList{Flavors: flavors})
	})
	mux.HandleFunc("/v1/me", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(client.UserProfile{
			ID:       "user-1",
			TenantID: "tenant-1",
		})
	})
	return httptest.NewServer(mux)
}

func TestReadByID(t *testing.T) {
	flavors := []apiFlavor{
		{ID: "flv-1", Name: "nl.small", VCPUs: 1, RAMMB: 1024, DiskGB: 20, Category: "general", Family: "gp", Generation: 1, Status: "active", BaseVCPURatio: 4.0, VCPUMultiplier: 1.0},
		{ID: "flv-2", Name: "nl.medium", VCPUs: 2, RAMMB: 2048, DiskGB: 40, Category: "general", Family: "gp", Generation: 1, Status: "active", BaseVCPURatio: 4.0, VCPUMultiplier: 1.0},
	}
	server := newTestServer(t, flavors)
	defer server.Close()

	c := client.NewClient(server.URL, "test-key") // pragma: allowlist secret
	if err := c.Configure(context.Background()); err != nil {
		t.Fatalf("Configure failed: %v", err)
	}

	apiResp, err := c.Get(context.Background(), "/v1/flavors", nil)
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}

	var list apiFlavorList
	if err := json.Unmarshal(apiResp.Body, &list); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	if len(list.Flavors) != 2 {
		t.Fatalf("expected 2 flavors, got %d", len(list.Flavors))
	}

	// Verify lookup by ID
	var found *apiFlavor
	for i := range list.Flavors {
		if list.Flavors[i].ID == "flv-1" {
			found = &list.Flavors[i]
			break
		}
	}
	if found == nil {
		t.Fatal("expected to find flavor with ID flv-1")
	}
	if found.Name != "nl.small" {
		t.Errorf("expected name nl.small, got %s", found.Name)
	}
	if found.VCPUs != 1 {
		t.Errorf("expected 1 vcpu, got %d", found.VCPUs)
	}
}

func TestReadByName(t *testing.T) {
	flavors := []apiFlavor{
		{ID: "flv-1", Name: "nl.small", VCPUs: 1, RAMMB: 1024, DiskGB: 20, Category: "general", Family: "gp", Generation: 1, Status: "active", BaseVCPURatio: 4.0, VCPUMultiplier: 1.0},
	}
	server := newTestServer(t, flavors)
	defer server.Close()

	c := client.NewClient(server.URL, "test-key") // pragma: allowlist secret
	if err := c.Configure(context.Background()); err != nil {
		t.Fatalf("Configure failed: %v", err)
	}

	apiResp, err := c.Get(context.Background(), "/v1/flavors", nil)
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}

	var list apiFlavorList
	if err := json.Unmarshal(apiResp.Body, &list); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	// Verify lookup by name
	var found *apiFlavor
	for i := range list.Flavors {
		if list.Flavors[i].Name == "nl.small" {
			found = &list.Flavors[i]
			break
		}
	}
	if found == nil {
		t.Fatal("expected to find flavor with name nl.small")
	}
}

func TestReadNotFound(t *testing.T) {
	flavors := []apiFlavor{
		{ID: "flv-1", Name: "nl.small", VCPUs: 1, RAMMB: 1024, DiskGB: 20, Category: "general", Family: "gp", Generation: 1, Status: "active", BaseVCPURatio: 4.0, VCPUMultiplier: 1.0},
	}
	server := newTestServer(t, flavors)
	defer server.Close()

	c := client.NewClient(server.URL, "test-key") // pragma: allowlist secret
	if err := c.Configure(context.Background()); err != nil {
		t.Fatalf("Configure failed: %v", err)
	}

	apiResp, err := c.Get(context.Background(), "/v1/flavors", nil)
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}

	var list apiFlavorList
	if err := json.Unmarshal(apiResp.Body, &list); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	found := false
	for _, f := range list.Flavors {
		if f.ID == "flv-nonexistent" {
			found = true
		}
	}
	if found {
		t.Error("expected flavor not to be found")
	}
}

// --- tfsdk-level Read tests ---

func configureFlavorDS(t *testing.T, ds datasource.DataSource, c *client.Client) {
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

func getFlavorDSSchema(t *testing.T) datasource.SchemaResponse {
	t.Helper()
	ds := NewDataSource()
	var schemaResp datasource.SchemaResponse
	ds.Schema(context.Background(), datasource.SchemaRequest{}, &schemaResp)
	return schemaResp
}

func TestTFSDK_ReadFlavorByID(t *testing.T) {
	flavors := []apiFlavor{
		{ID: "flv-1", Name: "nl.small", VCPUs: 1, RAMMB: 1024, DiskGB: 20, Category: "general", Family: "gp", Generation: 1, Status: "active", BaseVCPURatio: 4.0, VCPUMultiplier: 1.0},
		{ID: "flv-2", Name: "nl.medium", VCPUs: 2, RAMMB: 2048, DiskGB: 40, Category: "general", Family: "gp", Generation: 1, Status: "active", BaseVCPURatio: 4.0, VCPUMultiplier: 1.0},
	}
	server := newTestServer(t, flavors)
	defer server.Close()

	c := client.NewClient(server.URL, "test-key") // pragma: allowlist secret
	if err := c.Configure(context.Background()); err != nil {
		t.Fatalf("Configure failed: %v", err)
	}

	ds := NewDataSource()
	configureFlavorDS(t, ds, c)
	schemaResp := getFlavorDSSchema(t)

	ctx := context.Background()
	tfType := schemaResp.Schema.Type().TerraformType(ctx)

	configVal := tftypes.NewValue(tfType, map[string]tftypes.Value{
		"id":              tftypes.NewValue(tftypes.String, "flv-1"),
		"name":            tftypes.NewValue(tftypes.String, nil),
		"vcpus":           tftypes.NewValue(tftypes.Number, nil),
		"ram_mb":          tftypes.NewValue(tftypes.Number, nil),
		"disk_gb":         tftypes.NewValue(tftypes.Number, nil),
		"category":        tftypes.NewValue(tftypes.String, nil),
		"family":          tftypes.NewValue(tftypes.String, nil),
		"generation":      tftypes.NewValue(tftypes.Number, nil),
		"status":          tftypes.NewValue(tftypes.String, nil),
		"successor_id":    tftypes.NewValue(tftypes.String, nil),
		"base_vcpu_ratio": tftypes.NewValue(tftypes.Number, nil),
		"vcpu_multiplier": tftypes.NewValue(tftypes.Number, nil),
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

	var state flavorModel
	readResp.State.Get(ctx, &state)

	if state.ID.ValueString() != "flv-1" {
		t.Errorf("expected ID flv-1, got %s", state.ID.ValueString())
	}
	if state.Name.ValueString() != "nl.small" {
		t.Errorf("expected Name nl.small, got %s", state.Name.ValueString())
	}
	if state.VCPUs.ValueInt64() != 1 {
		t.Errorf("expected VCPUs 1, got %d", state.VCPUs.ValueInt64())
	}
	if state.RAMMB.ValueInt64() != 1024 {
		t.Errorf("expected RAMMB 1024, got %d", state.RAMMB.ValueInt64())
	}
	if state.Family.ValueString() != "gp" {
		t.Errorf("expected Family gp, got %s", state.Family.ValueString())
	}
	if state.Generation.ValueInt64() != 1 {
		t.Errorf("expected Generation 1, got %d", state.Generation.ValueInt64())
	}
	if state.Status.ValueString() != "active" {
		t.Errorf("expected Status active, got %s", state.Status.ValueString())
	}
	if state.BaseVCPURatio.ValueFloat64() != 4.0 {
		t.Errorf("expected BaseVCPURatio 4.0, got %f", state.BaseVCPURatio.ValueFloat64())
	}
	if state.VCPUMultiplier.ValueFloat64() != 1.0 {
		t.Errorf("expected VCPUMultiplier 1.0, got %f", state.VCPUMultiplier.ValueFloat64())
	}
}

func TestTFSDK_ReadFlavorByName(t *testing.T) {
	flavors := []apiFlavor{
		{ID: "flv-1", Name: "nl.small", VCPUs: 1, RAMMB: 1024, DiskGB: 20, Category: "general", Family: "gp", Generation: 1, Status: "active", BaseVCPURatio: 4.0, VCPUMultiplier: 1.0},
		{ID: "flv-2", Name: "nl.medium", VCPUs: 2, RAMMB: 2048, DiskGB: 40, Category: "general", Family: "gp", Generation: 1, Status: "active", BaseVCPURatio: 4.0, VCPUMultiplier: 1.0},
	}
	server := newTestServer(t, flavors)
	defer server.Close()

	c := client.NewClient(server.URL, "test-key") // pragma: allowlist secret
	if err := c.Configure(context.Background()); err != nil {
		t.Fatalf("Configure failed: %v", err)
	}

	ds := NewDataSource()
	configureFlavorDS(t, ds, c)
	schemaResp := getFlavorDSSchema(t)

	ctx := context.Background()
	tfType := schemaResp.Schema.Type().TerraformType(ctx)

	configVal := tftypes.NewValue(tfType, map[string]tftypes.Value{
		"id":              tftypes.NewValue(tftypes.String, nil),
		"name":            tftypes.NewValue(tftypes.String, "nl.medium"),
		"vcpus":           tftypes.NewValue(tftypes.Number, nil),
		"ram_mb":          tftypes.NewValue(tftypes.Number, nil),
		"disk_gb":         tftypes.NewValue(tftypes.Number, nil),
		"category":        tftypes.NewValue(tftypes.String, nil),
		"family":          tftypes.NewValue(tftypes.String, nil),
		"generation":      tftypes.NewValue(tftypes.Number, nil),
		"status":          tftypes.NewValue(tftypes.String, nil),
		"successor_id":    tftypes.NewValue(tftypes.String, nil),
		"base_vcpu_ratio": tftypes.NewValue(tftypes.Number, nil),
		"vcpu_multiplier": tftypes.NewValue(tftypes.Number, nil),
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

	var state flavorModel
	readResp.State.Get(ctx, &state)

	if state.ID.ValueString() != "flv-2" {
		t.Errorf("expected ID flv-2, got %s", state.ID.ValueString())
	}
}

func TestTFSDK_ReadFlavorBothIDAndName(t *testing.T) {
	server := newTestServer(t, []apiFlavor{{ID: "flv-1", Name: "nl.small", VCPUs: 1, RAMMB: 1024, DiskGB: 20, Family: "gp", Generation: 1, Status: "active", BaseVCPURatio: 4.0, VCPUMultiplier: 1.0}})
	defer server.Close()

	c := client.NewClient(server.URL, "test-key") // pragma: allowlist secret
	if err := c.Configure(context.Background()); err != nil {
		t.Fatalf("Configure failed: %v", err)
	}

	ds := NewDataSource()
	configureFlavorDS(t, ds, c)
	schemaResp := getFlavorDSSchema(t)

	ctx := context.Background()
	tfType := schemaResp.Schema.Type().TerraformType(ctx)

	configVal := tftypes.NewValue(tfType, map[string]tftypes.Value{
		"id":              tftypes.NewValue(tftypes.String, "flv-1"),
		"name":            tftypes.NewValue(tftypes.String, "nl.small"),
		"vcpus":           tftypes.NewValue(tftypes.Number, nil),
		"ram_mb":          tftypes.NewValue(tftypes.Number, nil),
		"disk_gb":         tftypes.NewValue(tftypes.Number, nil),
		"category":        tftypes.NewValue(tftypes.String, nil),
		"family":          tftypes.NewValue(tftypes.String, nil),
		"generation":      tftypes.NewValue(tftypes.Number, nil),
		"status":          tftypes.NewValue(tftypes.String, nil),
		"successor_id":    tftypes.NewValue(tftypes.String, nil),
		"base_vcpu_ratio": tftypes.NewValue(tftypes.Number, nil),
		"vcpu_multiplier": tftypes.NewValue(tftypes.Number, nil),
	})

	readReq := datasource.ReadRequest{
		Config: tfsdk.Config{Schema: schemaResp.Schema, Raw: configVal},
	}
	var readResp datasource.ReadResponse
	readResp.State = tfsdk.State{Schema: schemaResp.Schema}

	ds.Read(ctx, readReq, &readResp)

	if !readResp.Diagnostics.HasError() {
		t.Error("expected error when both id and name are specified")
	}
}

func TestTFSDK_ReadFlavorNotFound(t *testing.T) {
	server := newTestServer(t, []apiFlavor{{ID: "flv-1", Name: "nl.small", VCPUs: 1, RAMMB: 1024, DiskGB: 20, Family: "gp", Generation: 1, Status: "active", BaseVCPURatio: 4.0, VCPUMultiplier: 1.0}})
	defer server.Close()

	c := client.NewClient(server.URL, "test-key") // pragma: allowlist secret
	if err := c.Configure(context.Background()); err != nil {
		t.Fatalf("Configure failed: %v", err)
	}

	ds := NewDataSource()
	configureFlavorDS(t, ds, c)
	schemaResp := getFlavorDSSchema(t)

	ctx := context.Background()
	tfType := schemaResp.Schema.Type().TerraformType(ctx)

	configVal := tftypes.NewValue(tfType, map[string]tftypes.Value{
		"id":              tftypes.NewValue(tftypes.String, nil),
		"name":            tftypes.NewValue(tftypes.String, "nonexistent"),
		"vcpus":           tftypes.NewValue(tftypes.Number, nil),
		"ram_mb":          tftypes.NewValue(tftypes.Number, nil),
		"disk_gb":         tftypes.NewValue(tftypes.Number, nil),
		"category":        tftypes.NewValue(tftypes.String, nil),
		"family":          tftypes.NewValue(tftypes.String, nil),
		"generation":      tftypes.NewValue(tftypes.Number, nil),
		"status":          tftypes.NewValue(tftypes.String, nil),
		"successor_id":    tftypes.NewValue(tftypes.String, nil),
		"base_vcpu_ratio": tftypes.NewValue(tftypes.Number, nil),
		"vcpu_multiplier": tftypes.NewValue(tftypes.Number, nil),
	})

	readReq := datasource.ReadRequest{
		Config: tfsdk.Config{Schema: schemaResp.Schema, Raw: configVal},
	}
	var readResp datasource.ReadResponse
	readResp.State = tfsdk.State{Schema: schemaResp.Schema}

	ds.Read(ctx, readReq, &readResp)

	if !readResp.Diagnostics.HasError() {
		t.Error("expected error when flavor name not found")
	}
}

func TestAPIFlavorSerialization(t *testing.T) {
	f := apiFlavor{
		ID:             "flv-1",
		Name:           "nl.small",
		VCPUs:          1,
		RAMMB:          1024,
		DiskGB:         20,
		Category:       "general",
		Family:         "gp",
		Generation:     1,
		Status:         "active",
		SuccessorID:    "",
		BaseVCPURatio:  4.0,
		VCPUMultiplier: 1.0,
	}

	data, err := json.Marshal(f)
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}

	var decoded apiFlavor
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	if decoded.ID != f.ID {
		t.Errorf("expected ID %s, got %s", f.ID, decoded.ID)
	}
	if decoded.VCPUs != f.VCPUs {
		t.Errorf("expected VCPUs %d, got %d", f.VCPUs, decoded.VCPUs)
	}
	if decoded.RAMMB != f.RAMMB {
		t.Errorf("expected RAMMB %d, got %d", f.RAMMB, decoded.RAMMB)
	}
	if decoded.DiskGB != f.DiskGB {
		t.Errorf("expected DiskGB %d, got %d", f.DiskGB, decoded.DiskGB)
	}
	if decoded.Family != f.Family {
		t.Errorf("expected Family %s, got %s", f.Family, decoded.Family)
	}
	if decoded.Generation != f.Generation {
		t.Errorf("expected Generation %d, got %d", f.Generation, decoded.Generation)
	}
	if decoded.Status != f.Status {
		t.Errorf("expected Status %s, got %s", f.Status, decoded.Status)
	}
	if decoded.BaseVCPURatio != f.BaseVCPURatio {
		t.Errorf("expected BaseVCPURatio %f, got %f", f.BaseVCPURatio, decoded.BaseVCPURatio)
	}
	if decoded.VCPUMultiplier != f.VCPUMultiplier {
		t.Errorf("expected VCPUMultiplier %f, got %f", f.VCPUMultiplier, decoded.VCPUMultiplier)
	}
}

func TestTFSDK_ReadDeprecatedFlavorWarning(t *testing.T) {
	flavors := []apiFlavor{
		{ID: "flv-old", Name: "nl.old", VCPUs: 1, RAMMB: 1024, DiskGB: 20, Category: "general", Family: "gp", Generation: 1, Status: "deprecated", SuccessorID: "flv-new", BaseVCPURatio: 4.0, VCPUMultiplier: 1.0},
	}
	server := newTestServer(t, flavors)
	defer server.Close()

	c := client.NewClient(server.URL, "test-key") // pragma: allowlist secret
	if err := c.Configure(context.Background()); err != nil {
		t.Fatalf("Configure failed: %v", err)
	}

	ds := NewDataSource()
	configureFlavorDS(t, ds, c)
	schemaResp := getFlavorDSSchema(t)

	ctx := context.Background()
	tfType := schemaResp.Schema.Type().TerraformType(ctx)

	configVal := tftypes.NewValue(tfType, map[string]tftypes.Value{
		"id":              tftypes.NewValue(tftypes.String, "flv-old"),
		"name":            tftypes.NewValue(tftypes.String, nil),
		"vcpus":           tftypes.NewValue(tftypes.Number, nil),
		"ram_mb":          tftypes.NewValue(tftypes.Number, nil),
		"disk_gb":         tftypes.NewValue(tftypes.Number, nil),
		"category":        tftypes.NewValue(tftypes.String, nil),
		"family":          tftypes.NewValue(tftypes.String, nil),
		"generation":      tftypes.NewValue(tftypes.Number, nil),
		"status":          tftypes.NewValue(tftypes.String, nil),
		"successor_id":    tftypes.NewValue(tftypes.String, nil),
		"base_vcpu_ratio": tftypes.NewValue(tftypes.Number, nil),
		"vcpu_multiplier": tftypes.NewValue(tftypes.Number, nil),
	})

	readReq := datasource.ReadRequest{
		Config: tfsdk.Config{Schema: schemaResp.Schema, Raw: configVal},
	}
	var readResp datasource.ReadResponse
	readResp.State = tfsdk.State{Schema: schemaResp.Schema}

	ds.Read(ctx, readReq, &readResp)

	// Should NOT have errors (deprecated is a warning, not an error)
	if readResp.Diagnostics.HasError() {
		t.Fatalf("Read should not fail for deprecated flavor: %v", readResp.Diagnostics.Errors())
	}

	// Should have a warning
	foundWarning := false
	for _, d := range readResp.Diagnostics {
		if d.Summary() == "Deprecated Flavor" {
			foundWarning = true
			break
		}
	}
	if !foundWarning {
		t.Error("expected deprecation warning diagnostic for deprecated flavor")
	}

	var state flavorModel
	readResp.State.Get(ctx, &state)

	if state.Status.ValueString() != "deprecated" {
		t.Errorf("expected Status deprecated, got %s", state.Status.ValueString())
	}
	if state.SuccessorID.ValueString() != "flv-new" {
		t.Errorf("expected SuccessorID flv-new, got %s", state.SuccessorID.ValueString())
	}
}

func TestTFSDK_ReadRetiredFlavorError(t *testing.T) {
	flavors := []apiFlavor{
		{ID: "flv-retired", Name: "nl.retired", VCPUs: 1, RAMMB: 1024, DiskGB: 20, Category: "general", Family: "gp", Generation: 1, Status: "retired", SuccessorID: "flv-new", BaseVCPURatio: 4.0, VCPUMultiplier: 1.0},
	}
	server := newTestServer(t, flavors)
	defer server.Close()

	c := client.NewClient(server.URL, "test-key") // pragma: allowlist secret
	if err := c.Configure(context.Background()); err != nil {
		t.Fatalf("Configure failed: %v", err)
	}

	ds := NewDataSource()
	configureFlavorDS(t, ds, c)
	schemaResp := getFlavorDSSchema(t)

	ctx := context.Background()
	tfType := schemaResp.Schema.Type().TerraformType(ctx)

	configVal := tftypes.NewValue(tfType, map[string]tftypes.Value{
		"id":              tftypes.NewValue(tftypes.String, "flv-retired"),
		"name":            tftypes.NewValue(tftypes.String, nil),
		"vcpus":           tftypes.NewValue(tftypes.Number, nil),
		"ram_mb":          tftypes.NewValue(tftypes.Number, nil),
		"disk_gb":         tftypes.NewValue(tftypes.Number, nil),
		"category":        tftypes.NewValue(tftypes.String, nil),
		"family":          tftypes.NewValue(tftypes.String, nil),
		"generation":      tftypes.NewValue(tftypes.Number, nil),
		"status":          tftypes.NewValue(tftypes.String, nil),
		"successor_id":    tftypes.NewValue(tftypes.String, nil),
		"base_vcpu_ratio": tftypes.NewValue(tftypes.Number, nil),
		"vcpu_multiplier": tftypes.NewValue(tftypes.Number, nil),
	})

	readReq := datasource.ReadRequest{
		Config: tfsdk.Config{Schema: schemaResp.Schema, Raw: configVal},
	}
	var readResp datasource.ReadResponse
	readResp.State = tfsdk.State{Schema: schemaResp.Schema}

	ds.Read(ctx, readReq, &readResp)

	// Should have an error for retired flavor
	if !readResp.Diagnostics.HasError() {
		t.Error("expected error for retired flavor")
	}

	foundError := false
	for _, d := range readResp.Diagnostics.Errors() {
		if d.Summary() == "Retired Flavor" {
			foundError = true
			break
		}
	}
	if !foundError {
		t.Error("expected 'Retired Flavor' error diagnostic")
	}
}
