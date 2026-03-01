package flavor

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
	if resp.TypeName != "fm_flavor" {
		t.Errorf("expected type name fm_flavor, got %s", resp.TypeName)
	}
}

func TestSchema(t *testing.T) {
	ds := NewDataSource()
	req := datasource.SchemaRequest{}
	var resp datasource.SchemaResponse
	ds.Schema(context.Background(), req, &resp)

	expectedAttrs := []string{"id", "name", "vcpus", "ram_mb", "disk_gb", "category"}
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

func TestReadByID(t *testing.T) {
	flavors := []apiFlavor{
		{ID: "flv-1", Name: "nl.small", VCPUs: 1, RAMMB: 1024, DiskGB: 20, Category: "general"},
		{ID: "flv-2", Name: "nl.medium", VCPUs: 2, RAMMB: 2048, DiskGB: 40, Category: "general"},
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
		{ID: "flv-1", Name: "nl.small", VCPUs: 1, RAMMB: 1024, DiskGB: 20, Category: "general"},
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
		{ID: "flv-1", Name: "nl.small", VCPUs: 1, RAMMB: 1024, DiskGB: 20, Category: "general"},
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

func TestAPIFlavorSerialization(t *testing.T) {
	f := apiFlavor{
		ID:       "flv-1",
		Name:     "nl.small",
		VCPUs:    1,
		RAMMB:    1024,
		DiskGB:   20,
		Category: "general",
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
}
