package subnet

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
	if resp.TypeName != "frostmoln_subnet" {
		t.Errorf("expected type name frostmoln_subnet, got %s", resp.TypeName)
	}
}

func TestSchema(t *testing.T) {
	ds := NewDataSource()
	req := datasource.SchemaRequest{}
	var resp datasource.SchemaResponse
	ds.Schema(context.Background(), req, &resp)

	expectedAttrs := []string{"id", "name", "vpc_id", "description", "cidr", "zone", "gateway_ip", "is_public", "status", "available_ips", "tags", "created_at"}
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
	vpcIDAttr := resp.Schema.Attributes["vpc_id"].(schema.StringAttribute)
	if !vpcIDAttr.Optional {
		t.Error("expected vpc_id to be optional")
	}
}

func TestConfigureNilProviderData(t *testing.T) {
	ds := &subnetDataSource{}
	req := datasource.ConfigureRequest{}
	var resp datasource.ConfigureResponse
	ds.Configure(context.Background(), req, &resp)
	if resp.Diagnostics.HasError() {
		t.Errorf("expected no errors, got %v", resp.Diagnostics.Errors())
	}
}

func TestConfigureWrongType(t *testing.T) {
	ds := &subnetDataSource{}
	req := datasource.ConfigureRequest{ProviderData: "not-a-client"}
	var resp datasource.ConfigureResponse
	ds.Configure(context.Background(), req, &resp)
	if !resp.Diagnostics.HasError() {
		t.Error("expected error for wrong type")
	}
}

func newTestServer(t *testing.T, subnets []apiSubnet) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()

	mux.HandleFunc("/v1/me", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(client.UserProfile{
			ID:       "user-1",
			TenantID: "tenant-1",
		})
	})

	mux.HandleFunc("/v1/tenants/tenant-1/subnets", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(apiSubnetList{Subnets: subnets})
	})

	for _, sub := range subnets {
		s := sub
		mux.HandleFunc("/v1/tenants/tenant-1/subnets/"+sub.ID, func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(s)
		})
	}

	return httptest.NewServer(mux)
}

func TestReadByID(t *testing.T) {
	subnets := []apiSubnet{
		{
			ID:           "sub-1",
			Name:         "web-subnet",
			VPCID:        "vpc-1",
			Description:  "Web tier subnet",
			CIDR:         "10.0.1.0/24",
			Zone:         "eu-north-1a",
			GatewayIP:    "10.0.1.1",
			IsPublic:     true,
			Status:       "active",
			AvailableIPs: 250,
			Tags:         map[string]string{"tier": "web"},
			CreatedAt:    "2025-01-01T00:00:00Z",
		},
	}
	server := newTestServer(t, subnets)
	defer server.Close()

	c := client.NewClient(server.URL, "test-key") // pragma: allowlist secret
	if err := c.Configure(context.Background()); err != nil {
		t.Fatalf("Configure failed: %v", err)
	}

	apiResp, err := c.Get(context.Background(), c.TenantPath("/subnets/sub-1"), nil)
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}

	var sub apiSubnet
	if err := json.Unmarshal(apiResp.Body, &sub); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	if sub.ID != "sub-1" {
		t.Errorf("expected ID sub-1, got %s", sub.ID)
	}
	if sub.Name != "web-subnet" {
		t.Errorf("expected name web-subnet, got %s", sub.Name)
	}
	if sub.VPCID != "vpc-1" {
		t.Errorf("expected vpc_id vpc-1, got %s", sub.VPCID)
	}
	if sub.AvailableIPs != 250 {
		t.Errorf("expected 250 available IPs, got %d", sub.AvailableIPs)
	}
}

func TestReadByName(t *testing.T) {
	subnets := []apiSubnet{
		{ID: "sub-1", Name: "web-subnet", VPCID: "vpc-1", CIDR: "10.0.1.0/24", Status: "active", CreatedAt: "2025-01-01T00:00:00Z"},
		{ID: "sub-2", Name: "db-subnet", VPCID: "vpc-1", CIDR: "10.0.2.0/24", Status: "active", CreatedAt: "2025-01-02T00:00:00Z"},
	}
	server := newTestServer(t, subnets)
	defer server.Close()

	c := client.NewClient(server.URL, "test-key") // pragma: allowlist secret
	if err := c.Configure(context.Background()); err != nil {
		t.Fatalf("Configure failed: %v", err)
	}

	apiResp, err := c.Get(context.Background(), c.TenantPath("/subnets"), nil)
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}

	var list apiSubnetList
	if err := json.Unmarshal(apiResp.Body, &list); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	var found *apiSubnet
	for i := range list.Subnets {
		if list.Subnets[i].Name == "db-subnet" {
			found = &list.Subnets[i]
			break
		}
	}

	if found == nil {
		t.Fatal("expected to find subnet with name db-subnet")
	}
	if found.ID != "sub-2" {
		t.Errorf("expected ID sub-2, got %s", found.ID)
	}
}

func TestReadByNameWithVPCFilter(t *testing.T) {
	subnets := []apiSubnet{
		{ID: "sub-1", Name: "web-subnet", VPCID: "vpc-1", CIDR: "10.0.1.0/24", Status: "active", CreatedAt: "2025-01-01T00:00:00Z"},
		{ID: "sub-2", Name: "web-subnet", VPCID: "vpc-2", CIDR: "10.1.1.0/24", Status: "active", CreatedAt: "2025-01-02T00:00:00Z"},
	}

	// Simulate filtering by VPC ID
	vpcIDFilter := "vpc-2"
	var found *apiSubnet
	for i := range subnets {
		if subnets[i].Name == "web-subnet" && subnets[i].VPCID == vpcIDFilter {
			found = &subnets[i]
			break
		}
	}

	if found == nil {
		t.Fatal("expected to find subnet with name web-subnet in VPC vpc-2")
	}
	if found.ID != "sub-2" {
		t.Errorf("expected ID sub-2, got %s", found.ID)
	}
}

func TestReadNotFound(t *testing.T) {
	subnets := []apiSubnet{
		{ID: "sub-1", Name: "web-subnet", VPCID: "vpc-1", CIDR: "10.0.1.0/24", Status: "active", CreatedAt: "2025-01-01T00:00:00Z"},
	}

	found := false
	for _, s := range subnets {
		if s.Name == "nonexistent" {
			found = true
		}
	}
	if found {
		t.Error("expected subnet not to be found")
	}
}

func TestAPISubnetSerialization(t *testing.T) {
	sub := apiSubnet{
		ID:           "sub-1",
		Name:         "test",
		VPCID:        "vpc-1",
		Description:  "Test subnet",
		CIDR:         "10.0.1.0/24",
		Zone:         "eu-north-1a",
		GatewayIP:    "10.0.1.1",
		IsPublic:     true,
		Status:       "active",
		AvailableIPs: 250,
		Tags:         map[string]string{"env": "test"},
		CreatedAt:    "2025-01-01T00:00:00Z",
	}

	data, err := json.Marshal(sub)
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}

	var decoded apiSubnet
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	if decoded.ID != sub.ID {
		t.Errorf("expected ID %s, got %s", sub.ID, decoded.ID)
	}
	if decoded.VPCID != sub.VPCID {
		t.Errorf("expected VPCID %s, got %s", sub.VPCID, decoded.VPCID)
	}
	if decoded.IsPublic != sub.IsPublic {
		t.Errorf("expected IsPublic %v, got %v", sub.IsPublic, decoded.IsPublic)
	}
	if decoded.AvailableIPs != sub.AvailableIPs {
		t.Errorf("expected AvailableIPs %d, got %d", sub.AvailableIPs, decoded.AvailableIPs)
	}
	if decoded.Tags["env"] != "test" {
		t.Errorf("expected tag env=test, got %s", decoded.Tags["env"])
	}
}
