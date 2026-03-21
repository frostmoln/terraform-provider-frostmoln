package images

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
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
	if resp.TypeName != "frostmoln_images" {
		t.Errorf("expected type name frostmoln_images, got %s", resp.TypeName)
	}
}

func TestSchema(t *testing.T) {
	ds := NewDataSource()
	req := datasource.SchemaRequest{}
	var resp datasource.SchemaResponse
	ds.Schema(context.Background(), req, &resp)

	// Check filter attributes
	for _, attr := range []string{"os_distro", "name_regex"} {
		a, ok := resp.Schema.Attributes[attr]
		if !ok {
			t.Errorf("expected attribute %q in schema", attr)
			continue
		}
		strAttr := a.(schema.StringAttribute)
		if !strAttr.Optional {
			t.Errorf("expected %q to be optional", attr)
		}
	}

	// Check images list attribute exists
	if _, ok := resp.Schema.Attributes["images"]; !ok {
		t.Error("expected images attribute in schema")
	}
}

func TestConfigureNilProviderData(t *testing.T) {
	ds := &imagesDataSource{}
	req := datasource.ConfigureRequest{}
	var resp datasource.ConfigureResponse
	ds.Configure(context.Background(), req, &resp)
	if resp.Diagnostics.HasError() {
		t.Errorf("expected no errors, got %v", resp.Diagnostics.Errors())
	}
}

func TestConfigureWrongType(t *testing.T) {
	ds := &imagesDataSource{}
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

func TestReadAll(t *testing.T) {
	images := []apiImage{
		{ID: "img-1", Name: "Ubuntu 22.04", OSDistro: "ubuntu", OSVersion: "22.04", MinDiskGB: 10, MinRAMMB: 512, Status: "active"},
		{ID: "img-2", Name: "Debian 12", OSDistro: "debian", OSVersion: "12", MinDiskGB: 8, MinRAMMB: 256, Status: "active"},
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

	if len(list.Images) != 2 {
		t.Fatalf("expected 2 images, got %d", len(list.Images))
	}
}

func TestFilterByOSDistro(t *testing.T) {
	images := []apiImage{
		{ID: "img-1", Name: "Ubuntu 22.04", OSDistro: "ubuntu", OSVersion: "22.04", MinDiskGB: 10, MinRAMMB: 512, Status: "active"},
		{ID: "img-2", Name: "Debian 12", OSDistro: "debian", OSVersion: "12", MinDiskGB: 8, MinRAMMB: 256, Status: "active"},
		{ID: "img-3", Name: "Ubuntu 24.04", OSDistro: "ubuntu", OSVersion: "24.04", MinDiskGB: 12, MinRAMMB: 1024, Status: "active"},
	}

	// Simulate filtering logic
	osDistroFilter := "ubuntu"
	var filtered []apiImage
	for _, img := range images {
		if img.OSDistro == osDistroFilter {
			filtered = append(filtered, img)
		}
	}

	if len(filtered) != 2 {
		t.Fatalf("expected 2 ubuntu images, got %d", len(filtered))
	}
	for _, img := range filtered {
		if img.OSDistro != "ubuntu" {
			t.Errorf("expected os_distro ubuntu, got %s", img.OSDistro)
		}
	}
}

func TestFilterByNameRegex(t *testing.T) {
	images := []apiImage{
		{ID: "img-1", Name: "Ubuntu 22.04", OSDistro: "ubuntu", Status: "active"},
		{ID: "img-2", Name: "Debian 12", OSDistro: "debian", Status: "active"},
		{ID: "img-3", Name: "Ubuntu 24.04", OSDistro: "ubuntu", Status: "active"},
	}

	re := regexp.MustCompile(`Ubuntu \d+`)
	var filtered []apiImage
	for _, img := range images {
		if re.MatchString(img.Name) {
			filtered = append(filtered, img)
		}
	}

	if len(filtered) != 2 {
		t.Fatalf("expected 2 Ubuntu images, got %d", len(filtered))
	}
}

// --- tfsdk-level Read tests ---

func configureImagesDS(t *testing.T, ds datasource.DataSource, c *client.Client) {
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

func getImagesDSSchema(t *testing.T) datasource.SchemaResponse {
	t.Helper()
	ds := NewDataSource()
	var schemaResp datasource.SchemaResponse
	ds.Schema(context.Background(), datasource.SchemaRequest{}, &schemaResp)
	return schemaResp
}

func TestTFSDK_ReadAllImages(t *testing.T) {
	images := []apiImage{
		{ID: "img-1", Name: "Ubuntu 22.04", OSDistro: "ubuntu", OSVersion: "22.04", MinDiskGB: 10, MinRAMMB: 512, Status: "active"},
		{ID: "img-2", Name: "Debian 12", OSDistro: "debian", OSVersion: "12", MinDiskGB: 8, MinRAMMB: 256, Status: "active"},
	}
	server := newTestServer(t, images)
	defer server.Close()

	c := client.NewClient(server.URL, "test-key") // pragma: allowlist secret
	if err := c.Configure(context.Background()); err != nil {
		t.Fatalf("Configure failed: %v", err)
	}

	ds := NewDataSource()
	configureImagesDS(t, ds, c)
	schemaResp := getImagesDSSchema(t)

	ctx := context.Background()
	tfType := schemaResp.Schema.Type().TerraformType(ctx)

	configVal := tftypes.NewValue(tfType, map[string]tftypes.Value{
		"os_distro":  tftypes.NewValue(tftypes.String, nil),
		"name_regex": tftypes.NewValue(tftypes.String, nil),
		"images":     tftypes.NewValue(schemaResp.Schema.Attributes["images"].GetType().TerraformType(ctx), nil),
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

	var state imagesModel
	readResp.State.Get(ctx, &state)

	var items []imageItemModel
	state.Images.ElementsAs(ctx, &items, false)

	if len(items) != 2 {
		t.Errorf("expected 2 images, got %d", len(items))
	}
}

func TestTFSDK_ReadImagesFilterByOSDistro(t *testing.T) {
	images := []apiImage{
		{ID: "img-1", Name: "Ubuntu 22.04", OSDistro: "ubuntu", OSVersion: "22.04", MinDiskGB: 10, MinRAMMB: 512, Status: "active"},
		{ID: "img-2", Name: "Debian 12", OSDistro: "debian", OSVersion: "12", MinDiskGB: 8, MinRAMMB: 256, Status: "active"},
		{ID: "img-3", Name: "Ubuntu 24.04", OSDistro: "ubuntu", OSVersion: "24.04", MinDiskGB: 12, MinRAMMB: 1024, Status: "active"},
	}
	server := newTestServer(t, images)
	defer server.Close()

	c := client.NewClient(server.URL, "test-key") // pragma: allowlist secret
	if err := c.Configure(context.Background()); err != nil {
		t.Fatalf("Configure failed: %v", err)
	}

	ds := NewDataSource()
	configureImagesDS(t, ds, c)
	schemaResp := getImagesDSSchema(t)

	ctx := context.Background()
	tfType := schemaResp.Schema.Type().TerraformType(ctx)

	configVal := tftypes.NewValue(tfType, map[string]tftypes.Value{
		"os_distro":  tftypes.NewValue(tftypes.String, "ubuntu"),
		"name_regex": tftypes.NewValue(tftypes.String, nil),
		"images":     tftypes.NewValue(schemaResp.Schema.Attributes["images"].GetType().TerraformType(ctx), nil),
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

	var state imagesModel
	readResp.State.Get(ctx, &state)

	var items []imageItemModel
	state.Images.ElementsAs(ctx, &items, false)

	if len(items) != 2 {
		t.Errorf("expected 2 ubuntu images, got %d", len(items))
	}
}

func TestTFSDK_ReadImagesFilterByNameRegex(t *testing.T) {
	images := []apiImage{
		{ID: "img-1", Name: "Ubuntu 22.04", OSDistro: "ubuntu", OSVersion: "22.04", MinDiskGB: 10, MinRAMMB: 512, Status: "active"},
		{ID: "img-2", Name: "Debian 12", OSDistro: "debian", OSVersion: "12", MinDiskGB: 8, MinRAMMB: 256, Status: "active"},
		{ID: "img-3", Name: "Ubuntu 24.04", OSDistro: "ubuntu", OSVersion: "24.04", MinDiskGB: 12, MinRAMMB: 1024, Status: "active"},
	}
	server := newTestServer(t, images)
	defer server.Close()

	c := client.NewClient(server.URL, "test-key") // pragma: allowlist secret
	if err := c.Configure(context.Background()); err != nil {
		t.Fatalf("Configure failed: %v", err)
	}

	ds := NewDataSource()
	configureImagesDS(t, ds, c)
	schemaResp := getImagesDSSchema(t)

	ctx := context.Background()
	tfType := schemaResp.Schema.Type().TerraformType(ctx)

	configVal := tftypes.NewValue(tfType, map[string]tftypes.Value{
		"os_distro":  tftypes.NewValue(tftypes.String, nil),
		"name_regex": tftypes.NewValue(tftypes.String, `^Debian`),
		"images":     tftypes.NewValue(schemaResp.Schema.Attributes["images"].GetType().TerraformType(ctx), nil),
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

	var state imagesModel
	readResp.State.Get(ctx, &state)

	var items []imageItemModel
	state.Images.ElementsAs(ctx, &items, false)

	if len(items) != 1 {
		t.Errorf("expected 1 Debian image, got %d", len(items))
	}
	if len(items) > 0 && items[0].Name.ValueString() != "Debian 12" {
		t.Errorf("expected name Debian 12, got %s", items[0].Name.ValueString())
	}
}

func TestAPIImageListSerialization(t *testing.T) {
	list := apiImageList{
		Images: []apiImage{
			{ID: "img-1", Name: "Ubuntu", Status: "active"},
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

	if len(decoded.Images) != 1 {
		t.Fatalf("expected 1 image, got %d", len(decoded.Images))
	}
	if decoded.Images[0].Name != "Ubuntu" {
		t.Errorf("expected name Ubuntu, got %s", decoded.Images[0].Name)
	}
}

func TestImageItemAttrTypes(t *testing.T) {
	expectedKeys := []string{"id", "name", "os_distro", "os_version", "min_disk_gb", "min_ram_mb", "status"}
	for _, key := range expectedKeys {
		if _, ok := imageItemAttrTypes[key]; !ok {
			t.Errorf("expected key %q in imageItemAttrTypes", key)
		}
	}
}
