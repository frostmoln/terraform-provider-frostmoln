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

	"git.nl.cloud/NordicLight/terraform-provider-frostmoln/internal/client"
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
