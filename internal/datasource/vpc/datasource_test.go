package vpc

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
	req := datasource.MetadataRequest{ProviderTypeName: "frostmoln"}
	var resp datasource.MetadataResponse
	ds.Metadata(context.Background(), req, &resp)
	if resp.TypeName != "frostmoln_vpc" {
		t.Errorf("expected type name frostmoln_vpc, got %s", resp.TypeName)
	}
}

func TestSchema(t *testing.T) {
	ds := NewDataSource()
	req := datasource.SchemaRequest{}
	var resp datasource.SchemaResponse
	ds.Schema(context.Background(), req, &resp)

	expectedAttrs := []string{"id", "name", "description", "cidr", "region", "status", "is_default", "subnet_count", "tags", "created_at"}
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

	// Verify tags is a map attribute
	tagsAttr := resp.Schema.Attributes["tags"].(schema.MapAttribute)
	if !tagsAttr.Computed {
		t.Error("expected tags to be computed")
	}
}

func TestConfigureNilProviderData(t *testing.T) {
	ds := &vpcDataSource{}
	req := datasource.ConfigureRequest{}
	var resp datasource.ConfigureResponse
	ds.Configure(context.Background(), req, &resp)
	if resp.Diagnostics.HasError() {
		t.Errorf("expected no errors, got %v", resp.Diagnostics.Errors())
	}
}

func TestConfigureWrongType(t *testing.T) {
	ds := &vpcDataSource{}
	req := datasource.ConfigureRequest{ProviderData: "not-a-client"}
	var resp datasource.ConfigureResponse
	ds.Configure(context.Background(), req, &resp)
	if !resp.Diagnostics.HasError() {
		t.Error("expected error for wrong type")
	}
}

func newTestServer(t *testing.T, vpcs []apiVPC) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()

	mux.HandleFunc("/v1/me", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(client.UserProfile{
			ID:       "user-1",
			TenantID: "tenant-1",
		})
	})

	// Handle list
	mux.HandleFunc("/v1/tenants/tenant-1/vpcs", func(w http.ResponseWriter, r *http.Request) {
		// Check if this is a specific VPC lookup (has trailing path)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(apiVPCList{VPCs: vpcs})
	})

	// Handle individual VPC lookups
	for _, vpc := range vpcs {
		v := vpc // capture loop variable
		mux.HandleFunc("/v1/tenants/tenant-1/vpcs/"+vpc.ID, func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(v)
		})
	}

	return httptest.NewServer(mux)
}

func TestReadByID(t *testing.T) {
	vpcs := []apiVPC{
		{
			ID:          "vpc-1",
			Name:        "production",
			Description: "Production VPC",
			CIDR:        "10.0.0.0/16",
			Region:      "eu-north-1",
			Status:      "active",
			IsDefault:   false,
			SubnetCount: 3,
			Tags:        map[string]string{"env": "prod"},
			CreatedAt:   "2025-01-01T00:00:00Z",
		},
	}
	server := newTestServer(t, vpcs)
	defer server.Close()

	c := client.NewClient(server.URL, "test-key") // pragma: allowlist secret
	if err := c.Configure(context.Background()); err != nil {
		t.Fatalf("Configure failed: %v", err)
	}

	apiResp, err := c.Get(context.Background(), c.TenantPath("/vpcs/vpc-1"), nil)
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}

	var vpc apiVPC
	if err := json.Unmarshal(apiResp.Body, &vpc); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	if vpc.ID != "vpc-1" {
		t.Errorf("expected ID vpc-1, got %s", vpc.ID)
	}
	if vpc.Name != "production" {
		t.Errorf("expected name production, got %s", vpc.Name)
	}
	if vpc.CIDR != "10.0.0.0/16" {
		t.Errorf("expected CIDR 10.0.0.0/16, got %s", vpc.CIDR)
	}
	if vpc.Tags["env"] != "prod" {
		t.Errorf("expected tag env=prod, got %s", vpc.Tags["env"])
	}
}

func TestReadByName(t *testing.T) {
	vpcs := []apiVPC{
		{ID: "vpc-1", Name: "production", CIDR: "10.0.0.0/16", Region: "eu-north-1", Status: "active", CreatedAt: "2025-01-01T00:00:00Z"},
		{ID: "vpc-2", Name: "staging", CIDR: "10.1.0.0/16", Region: "eu-north-1", Status: "active", CreatedAt: "2025-01-02T00:00:00Z"},
	}
	server := newTestServer(t, vpcs)
	defer server.Close()

	c := client.NewClient(server.URL, "test-key") // pragma: allowlist secret
	if err := c.Configure(context.Background()); err != nil {
		t.Fatalf("Configure failed: %v", err)
	}

	apiResp, err := c.Get(context.Background(), c.TenantPath("/vpcs"), nil)
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}

	var list apiVPCList
	if err := json.Unmarshal(apiResp.Body, &list); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	var found *apiVPC
	for i := range list.VPCs {
		if list.VPCs[i].Name == "staging" {
			found = &list.VPCs[i]
			break
		}
	}

	if found == nil {
		t.Fatal("expected to find VPC with name staging")
	}
	if found.ID != "vpc-2" {
		t.Errorf("expected ID vpc-2, got %s", found.ID)
	}
}

func TestReadNotFound(t *testing.T) {
	vpcs := []apiVPC{
		{ID: "vpc-1", Name: "production", CIDR: "10.0.0.0/16", Region: "eu-north-1", Status: "active", CreatedAt: "2025-01-01T00:00:00Z"},
	}
	server := newTestServer(t, vpcs)
	defer server.Close()

	c := client.NewClient(server.URL, "test-key") // pragma: allowlist secret
	if err := c.Configure(context.Background()); err != nil {
		t.Fatalf("Configure failed: %v", err)
	}

	apiResp, err := c.Get(context.Background(), c.TenantPath("/vpcs"), nil)
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}

	var list apiVPCList
	if err := json.Unmarshal(apiResp.Body, &list); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	found := false
	for _, vpc := range list.VPCs {
		if vpc.Name == "nonexistent" {
			found = true
		}
	}
	if found {
		t.Error("expected VPC not to be found")
	}
}

func TestAPIVPCSerialization(t *testing.T) {
	vpc := apiVPC{
		ID:          "vpc-1",
		Name:        "test",
		Description: "Test VPC",
		CIDR:        "10.0.0.0/16",
		Region:      "eu-north-1",
		Status:      "active",
		IsDefault:   true,
		SubnetCount: 2,
		Tags:        map[string]string{"env": "test"},
		CreatedAt:   "2025-01-01T00:00:00Z",
	}

	data, err := json.Marshal(vpc)
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}

	var decoded apiVPC
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	if decoded.ID != vpc.ID {
		t.Errorf("expected ID %s, got %s", vpc.ID, decoded.ID)
	}
	if decoded.IsDefault != vpc.IsDefault {
		t.Errorf("expected IsDefault %v, got %v", vpc.IsDefault, decoded.IsDefault)
	}
	if decoded.SubnetCount != vpc.SubnetCount {
		t.Errorf("expected SubnetCount %d, got %d", vpc.SubnetCount, decoded.SubnetCount)
	}
	if decoded.Tags["env"] != "test" {
		t.Errorf("expected tag env=test, got %s", decoded.Tags["env"])
	}
}
