package flavors

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
	req := datasource.MetadataRequest{ProviderTypeName: "fm"}
	var resp datasource.MetadataResponse
	ds.Metadata(context.Background(), req, &resp)
	if resp.TypeName != "fm_flavors" {
		t.Errorf("expected type name fm_flavors, got %s", resp.TypeName)
	}
}

func TestSchema(t *testing.T) {
	ds := NewDataSource()
	req := datasource.SchemaRequest{}
	var resp datasource.SchemaResponse
	ds.Schema(context.Background(), req, &resp)

	categoryAttr, ok := resp.Schema.Attributes["category"]
	if !ok {
		t.Fatal("expected category attribute in schema")
	}
	strAttr := categoryAttr.(schema.StringAttribute)
	if !strAttr.Optional {
		t.Error("expected category to be optional")
	}

	if _, ok := resp.Schema.Attributes["flavors"]; !ok {
		t.Error("expected flavors attribute in schema")
	}
}

func TestConfigureNilProviderData(t *testing.T) {
	ds := &flavorsDataSource{}
	req := datasource.ConfigureRequest{}
	var resp datasource.ConfigureResponse
	ds.Configure(context.Background(), req, &resp)
	if resp.Diagnostics.HasError() {
		t.Errorf("expected no errors, got %v", resp.Diagnostics.Errors())
	}
}

func TestConfigureWrongType(t *testing.T) {
	ds := &flavorsDataSource{}
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
		json.NewEncoder(w).Encode(apiFlavorList{Flavors: flavors})
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
	flavors := []apiFlavor{
		{ID: "flv-1", Name: "nl.small", VCPUs: 1, RAMMB: 1024, DiskGB: 20, Category: "general"},
		{ID: "flv-2", Name: "nl.medium", VCPUs: 2, RAMMB: 2048, DiskGB: 40, Category: "general"},
		{ID: "flv-3", Name: "nl.compute.large", VCPUs: 8, RAMMB: 4096, DiskGB: 80, Category: "compute"},
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

	if len(list.Flavors) != 3 {
		t.Fatalf("expected 3 flavors, got %d", len(list.Flavors))
	}
}

func TestFilterByCategory(t *testing.T) {
	flavors := []apiFlavor{
		{ID: "flv-1", Name: "nl.small", VCPUs: 1, RAMMB: 1024, DiskGB: 20, Category: "general"},
		{ID: "flv-2", Name: "nl.medium", VCPUs: 2, RAMMB: 2048, DiskGB: 40, Category: "general"},
		{ID: "flv-3", Name: "nl.compute.large", VCPUs: 8, RAMMB: 4096, DiskGB: 80, Category: "compute"},
	}

	categoryFilter := "compute"
	var filtered []apiFlavor
	for _, f := range flavors {
		if f.Category == categoryFilter {
			filtered = append(filtered, f)
		}
	}

	if len(filtered) != 1 {
		t.Fatalf("expected 1 compute flavor, got %d", len(filtered))
	}
	if filtered[0].Name != "nl.compute.large" {
		t.Errorf("expected name nl.compute.large, got %s", filtered[0].Name)
	}
}

func TestFlavorItemAttrTypes(t *testing.T) {
	expectedKeys := []string{"id", "name", "vcpus", "ram_mb", "disk_gb", "category"}
	for _, key := range expectedKeys {
		if _, ok := flavorItemAttrTypes[key]; !ok {
			t.Errorf("expected key %q in flavorItemAttrTypes", key)
		}
	}
}

func TestAPIFlavorListSerialization(t *testing.T) {
	list := apiFlavorList{
		Flavors: []apiFlavor{
			{ID: "flv-1", Name: "nl.small", VCPUs: 1, RAMMB: 1024, DiskGB: 20},
		},
	}

	data, err := json.Marshal(list)
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}

	var decoded apiFlavorList
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	if len(decoded.Flavors) != 1 {
		t.Fatalf("expected 1 flavor, got %d", len(decoded.Flavors))
	}
}
