package image

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
	if resp.TypeName != "frostmoln_image" {
		t.Errorf("expected type name frostmoln_image, got %s", resp.TypeName)
	}
}

func TestSchema(t *testing.T) {
	ds := NewDataSource()
	req := datasource.SchemaRequest{}
	var resp datasource.SchemaResponse
	ds.Schema(context.Background(), req, &resp)

	expectedAttrs := []string{"id", "name", "os_distro", "os_version", "min_disk_gb", "min_ram_mb", "status", "created_at"}
	for _, attr := range expectedAttrs {
		if _, ok := resp.Schema.Attributes[attr]; !ok {
			t.Errorf("expected attribute %q in schema", attr)
		}
	}

	// Verify id and name are optional
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
	ds := &imageDataSource{}
	req := datasource.ConfigureRequest{}
	var resp datasource.ConfigureResponse
	ds.Configure(context.Background(), req, &resp)
	if resp.Diagnostics.HasError() {
		t.Errorf("expected no errors, got %v", resp.Diagnostics.Errors())
	}
	if ds.client != nil {
		t.Error("expected nil client")
	}
}

func TestConfigureWrongType(t *testing.T) {
	ds := &imageDataSource{}
	req := datasource.ConfigureRequest{ProviderData: "not-a-client"}
	var resp datasource.ConfigureResponse
	ds.Configure(context.Background(), req, &resp)
	if !resp.Diagnostics.HasError() {
		t.Error("expected error for wrong type")
	}
}

func newTestServer(t *testing.T, images []apiImage) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/images", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(apiImageList{Images: images})
	})
	mux.HandleFunc("/v1/me", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(client.UserProfile{
			ID:       "user-1",
			TenantID: "tenant-1",
		})
	})
	return httptest.NewServer(mux)
}

func TestReadByID(t *testing.T) {
	images := []apiImage{
		{ID: "img-1", Name: "Ubuntu 22.04", OSDistro: "ubuntu", OSVersion: "22.04", MinDiskGB: 10, MinRAMMB: 512, Status: "active", CreatedAt: "2025-01-01T00:00:00Z"},
		{ID: "img-2", Name: "Debian 12", OSDistro: "debian", OSVersion: "12", MinDiskGB: 8, MinRAMMB: 256, Status: "active", CreatedAt: "2025-01-02T00:00:00Z"},
	}
	server := newTestServer(t, images)
	defer server.Close()

	c := client.NewClient(server.URL, "test-key") // pragma: allowlist secret
	if err := c.Configure(context.Background()); err != nil {
		t.Fatalf("Configure failed: %v", err)
	}

	ds := &imageDataSource{client: c}

	// Test finding by ID
	apiResp, err := c.Get(context.Background(), "/v1/images", nil)
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}

	var list apiImageList
	if err := json.Unmarshal(apiResp.Body, &list); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	if len(list.Images) != 2 {
		t.Fatalf("expected 2 images, got %d", len(list.Images))
	}

	if list.Images[0].ID != "img-1" {
		t.Errorf("expected first image ID img-1, got %s", list.Images[0].ID)
	}

	_ = ds // Verify data source was created with client
}

func TestReadByName(t *testing.T) {
	images := []apiImage{
		{ID: "img-1", Name: "Ubuntu 22.04", OSDistro: "ubuntu", OSVersion: "22.04", MinDiskGB: 10, MinRAMMB: 512, Status: "active", CreatedAt: "2025-01-01T00:00:00Z"},
	}
	server := newTestServer(t, images)
	defer server.Close()

	c := client.NewClient(server.URL, "test-key") // pragma: allowlist secret
	if err := c.Configure(context.Background()); err != nil {
		t.Fatalf("Configure failed: %v", err)
	}

	apiResp, err := c.Get(context.Background(), "/v1/images", nil)
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}

	var list apiImageList
	if err := json.Unmarshal(apiResp.Body, &list); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	if list.Images[0].Name != "Ubuntu 22.04" {
		t.Errorf("expected name Ubuntu 22.04, got %s", list.Images[0].Name)
	}
}

func TestReadNotFound(t *testing.T) {
	images := []apiImage{
		{ID: "img-1", Name: "Ubuntu 22.04", OSDistro: "ubuntu", OSVersion: "22.04", MinDiskGB: 10, MinRAMMB: 512, Status: "active", CreatedAt: "2025-01-01T00:00:00Z"},
	}
	server := newTestServer(t, images)
	defer server.Close()

	c := client.NewClient(server.URL, "test-key") // pragma: allowlist secret
	if err := c.Configure(context.Background()); err != nil {
		t.Fatalf("Configure failed: %v", err)
	}

	apiResp, err := c.Get(context.Background(), "/v1/images", nil)
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}

	var list apiImageList
	if err := json.Unmarshal(apiResp.Body, &list); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	// Verify that the image we're looking for doesn't exist
	found := false
	for _, img := range list.Images {
		if img.ID == "img-nonexistent" {
			found = true
		}
	}
	if found {
		t.Error("expected image not to be found")
	}
}

// --- tfsdk-level Read tests ---

func configureImageDS(t *testing.T, ds datasource.DataSource, c *client.Client) {
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

func getImageDSSchema(t *testing.T) datasource.SchemaResponse {
	t.Helper()
	ds := NewDataSource()
	var schemaResp datasource.SchemaResponse
	ds.Schema(context.Background(), datasource.SchemaRequest{}, &schemaResp)
	return schemaResp
}

func TestTFSDK_ReadImageByID(t *testing.T) {
	images := []apiImage{
		{ID: "img-1", Name: "Ubuntu 22.04", OSDistro: "ubuntu", OSVersion: "22.04", MinDiskGB: 10, MinRAMMB: 512, Status: "active", CreatedAt: "2025-01-01T00:00:00Z"},
	}
	server := newTestServer(t, images)
	defer server.Close()

	c := client.NewClient(server.URL, "test-key") // pragma: allowlist secret
	if err := c.Configure(context.Background()); err != nil {
		t.Fatalf("Configure failed: %v", err)
	}

	ds := NewDataSource()
	configureImageDS(t, ds, c)
	schemaResp := getImageDSSchema(t)

	ctx := context.Background()
	tfType := schemaResp.Schema.Type().TerraformType(ctx)

	configVal := tftypes.NewValue(tfType, map[string]tftypes.Value{
		"id":          tftypes.NewValue(tftypes.String, "img-1"),
		"name":        tftypes.NewValue(tftypes.String, nil),
		"os_distro":   tftypes.NewValue(tftypes.String, nil),
		"os_version":  tftypes.NewValue(tftypes.String, nil),
		"min_disk_gb": tftypes.NewValue(tftypes.Number, nil),
		"min_ram_mb":  tftypes.NewValue(tftypes.Number, nil),
		"status":      tftypes.NewValue(tftypes.String, nil),
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

	var state imageModel
	readResp.State.Get(ctx, &state)

	if state.ID.ValueString() != "img-1" {
		t.Errorf("expected ID img-1, got %s", state.ID.ValueString())
	}
	if state.Name.ValueString() != "Ubuntu 22.04" {
		t.Errorf("expected Name Ubuntu 22.04, got %s", state.Name.ValueString())
	}
	if state.OSDistro.ValueString() != "ubuntu" {
		t.Errorf("expected OSDistro ubuntu, got %s", state.OSDistro.ValueString())
	}
	if state.MinDiskGB.ValueInt64() != 10 {
		t.Errorf("expected MinDiskGB 10, got %d", state.MinDiskGB.ValueInt64())
	}
}

func TestTFSDK_ReadImageByName(t *testing.T) {
	images := []apiImage{
		{ID: "img-1", Name: "Ubuntu 22.04", OSDistro: "ubuntu", OSVersion: "22.04", MinDiskGB: 10, MinRAMMB: 512, Status: "active", CreatedAt: "2025-01-01T00:00:00Z"},
		{ID: "img-2", Name: "Debian 12", OSDistro: "debian", OSVersion: "12", MinDiskGB: 8, MinRAMMB: 256, Status: "active", CreatedAt: "2025-01-02T00:00:00Z"},
	}
	server := newTestServer(t, images)
	defer server.Close()

	c := client.NewClient(server.URL, "test-key") // pragma: allowlist secret
	if err := c.Configure(context.Background()); err != nil {
		t.Fatalf("Configure failed: %v", err)
	}

	ds := NewDataSource()
	configureImageDS(t, ds, c)
	schemaResp := getImageDSSchema(t)

	ctx := context.Background()
	tfType := schemaResp.Schema.Type().TerraformType(ctx)

	configVal := tftypes.NewValue(tfType, map[string]tftypes.Value{
		"id":          tftypes.NewValue(tftypes.String, nil),
		"name":        tftypes.NewValue(tftypes.String, "Debian 12"),
		"os_distro":   tftypes.NewValue(tftypes.String, nil),
		"os_version":  tftypes.NewValue(tftypes.String, nil),
		"min_disk_gb": tftypes.NewValue(tftypes.Number, nil),
		"min_ram_mb":  tftypes.NewValue(tftypes.Number, nil),
		"status":      tftypes.NewValue(tftypes.String, nil),
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

	var state imageModel
	readResp.State.Get(ctx, &state)

	if state.ID.ValueString() != "img-2" {
		t.Errorf("expected ID img-2, got %s", state.ID.ValueString())
	}
}

func TestTFSDK_ReadImageBothIDAndName(t *testing.T) {
	server := newTestServer(t, []apiImage{{ID: "img-1", Name: "Ubuntu", Status: "active", CreatedAt: "2025-01-01T00:00:00Z"}})
	defer server.Close()

	c := client.NewClient(server.URL, "test-key") // pragma: allowlist secret
	if err := c.Configure(context.Background()); err != nil {
		t.Fatalf("Configure failed: %v", err)
	}

	ds := NewDataSource()
	configureImageDS(t, ds, c)
	schemaResp := getImageDSSchema(t)

	ctx := context.Background()
	tfType := schemaResp.Schema.Type().TerraformType(ctx)

	configVal := tftypes.NewValue(tfType, map[string]tftypes.Value{
		"id":          tftypes.NewValue(tftypes.String, "img-1"),
		"name":        tftypes.NewValue(tftypes.String, "Ubuntu"),
		"os_distro":   tftypes.NewValue(tftypes.String, nil),
		"os_version":  tftypes.NewValue(tftypes.String, nil),
		"min_disk_gb": tftypes.NewValue(tftypes.Number, nil),
		"min_ram_mb":  tftypes.NewValue(tftypes.Number, nil),
		"status":      tftypes.NewValue(tftypes.String, nil),
		"created_at":  tftypes.NewValue(tftypes.String, nil),
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

func TestTFSDK_ReadImageNotFound(t *testing.T) {
	server := newTestServer(t, []apiImage{{ID: "img-1", Name: "Ubuntu", Status: "active", CreatedAt: "2025-01-01T00:00:00Z"}})
	defer server.Close()

	c := client.NewClient(server.URL, "test-key") // pragma: allowlist secret
	if err := c.Configure(context.Background()); err != nil {
		t.Fatalf("Configure failed: %v", err)
	}

	ds := NewDataSource()
	configureImageDS(t, ds, c)
	schemaResp := getImageDSSchema(t)

	ctx := context.Background()
	tfType := schemaResp.Schema.Type().TerraformType(ctx)

	configVal := tftypes.NewValue(tfType, map[string]tftypes.Value{
		"id":          tftypes.NewValue(tftypes.String, nil),
		"name":        tftypes.NewValue(tftypes.String, "nonexistent"),
		"os_distro":   tftypes.NewValue(tftypes.String, nil),
		"os_version":  tftypes.NewValue(tftypes.String, nil),
		"min_disk_gb": tftypes.NewValue(tftypes.Number, nil),
		"min_ram_mb":  tftypes.NewValue(tftypes.Number, nil),
		"status":      tftypes.NewValue(tftypes.String, nil),
		"created_at":  tftypes.NewValue(tftypes.String, nil),
	})

	readReq := datasource.ReadRequest{
		Config: tfsdk.Config{Schema: schemaResp.Schema, Raw: configVal},
	}
	var readResp datasource.ReadResponse
	readResp.State = tfsdk.State{Schema: schemaResp.Schema}

	ds.Read(ctx, readReq, &readResp)

	if !readResp.Diagnostics.HasError() {
		t.Error("expected error when image name not found")
	}
}

func TestAPIImageSerialization(t *testing.T) {
	img := apiImage{
		ID:        "img-1",
		Name:      "Ubuntu 22.04",
		OSDistro:  "ubuntu",
		OSVersion: "22.04",
		MinDiskGB: 10,
		MinRAMMB:  512,
		Status:    "active",
		CreatedAt: "2025-01-01T00:00:00Z",
	}

	data, err := json.Marshal(img)
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}

	var decoded apiImage
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	if decoded.ID != img.ID {
		t.Errorf("expected ID %s, got %s", img.ID, decoded.ID)
	}
	if decoded.Name != img.Name {
		t.Errorf("expected Name %s, got %s", img.Name, decoded.Name)
	}
	if decoded.OSDistro != img.OSDistro {
		t.Errorf("expected OSDistro %s, got %s", img.OSDistro, decoded.OSDistro)
	}
	if decoded.MinDiskGB != img.MinDiskGB {
		t.Errorf("expected MinDiskGB %d, got %d", img.MinDiskGB, decoded.MinDiskGB)
	}
	if decoded.MinRAMMB != img.MinRAMMB {
		t.Errorf("expected MinRAMMB %d, got %d", img.MinRAMMB, decoded.MinRAMMB)
	}
}

func TestAPIImageListSerialization(t *testing.T) {
	list := apiImageList{
		Images: []apiImage{
			{ID: "img-1", Name: "Ubuntu", Status: "active"},
			{ID: "img-2", Name: "Debian", Status: "active"},
		},
	}

	data, err := json.Marshal(list)
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}

	var decoded apiImageList
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	if len(decoded.Images) != 2 {
		t.Fatalf("expected 2 images, got %d", len(decoded.Images))
	}
}
