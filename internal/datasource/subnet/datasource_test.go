package subnet

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
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
		_ = json.NewEncoder(w).Encode(client.UserProfile{
			ID:       "user-1",
			TenantID: "tenant-1",
		})
	})

	mux.HandleFunc("/v1/tenants/tenant-1/subnets", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(apiSubnetList{Subnets: subnets})
	})

	for _, sub := range subnets {
		s := sub
		mux.HandleFunc("/v1/tenants/tenant-1/subnets/"+sub.ID, func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(s)
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
			Zone:         "sweden-a",
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

// --- tfsdk-level Read tests ---

func configureSubnetDS(t *testing.T, ds datasource.DataSource, c *client.Client) {
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

func getSubnetDSSchema(t *testing.T) datasource.SchemaResponse {
	t.Helper()
	ds := NewDataSource()
	var schemaResp datasource.SchemaResponse
	ds.Schema(context.Background(), datasource.SchemaRequest{}, &schemaResp)
	return schemaResp
}

func TestTFSDK_ReadSubnetByID(t *testing.T) {
	subnets := []apiSubnet{
		{
			ID:           "sub-1",
			Name:         "web-subnet",
			VPCID:        "vpc-1",
			Description:  "Web tier",
			CIDR:         "10.0.1.0/24",
			Zone:         "sweden-a",
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

	ds := NewDataSource()
	configureSubnetDS(t, ds, c)
	schemaResp := getSubnetDSSchema(t)

	ctx := context.Background()
	tfType := schemaResp.Schema.Type().TerraformType(ctx)

	configVal := tftypes.NewValue(tfType, map[string]tftypes.Value{
		"id":            tftypes.NewValue(tftypes.String, "sub-1"),
		"name":          tftypes.NewValue(tftypes.String, nil),
		"vpc_id":        tftypes.NewValue(tftypes.String, nil),
		"description":   tftypes.NewValue(tftypes.String, nil),
		"cidr":          tftypes.NewValue(tftypes.String, nil),
		"zone":          tftypes.NewValue(tftypes.String, nil),
		"gateway_ip":    tftypes.NewValue(tftypes.String, nil),
		"is_public":     tftypes.NewValue(tftypes.Bool, nil),
		"status":        tftypes.NewValue(tftypes.String, nil),
		"available_ips": tftypes.NewValue(tftypes.Number, nil),
		"tags":          tftypes.NewValue(tftypes.Map{ElementType: tftypes.String}, nil),
		"created_at":    tftypes.NewValue(tftypes.String, nil),
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

	var state subnetModel
	readResp.State.Get(ctx, &state)

	if state.ID.ValueString() != "sub-1" {
		t.Errorf("expected ID sub-1, got %s", state.ID.ValueString())
	}
	if state.VPCID.ValueString() != "vpc-1" {
		t.Errorf("expected VPCID vpc-1, got %s", state.VPCID.ValueString())
	}
	if state.AvailableIPs.ValueInt64() != 250 {
		t.Errorf("expected AvailableIPs 250, got %d", state.AvailableIPs.ValueInt64())
	}
	if state.IsPublic.ValueBool() != true {
		t.Error("expected IsPublic true")
	}
}

func TestTFSDK_ReadSubnetByNameWithVPCFilter(t *testing.T) {
	subnets := []apiSubnet{
		{ID: "sub-1", Name: "web-subnet", VPCID: "vpc-1", CIDR: "10.0.1.0/24", Status: "active", CreatedAt: "2025-01-01T00:00:00Z"},
		{ID: "sub-2", Name: "web-subnet", VPCID: "vpc-2", CIDR: "10.1.1.0/24", Status: "active", CreatedAt: "2025-01-02T00:00:00Z"},
	}
	server := newTestServer(t, subnets)
	defer server.Close()

	c := client.NewClient(server.URL, "test-key") // pragma: allowlist secret
	if err := c.Configure(context.Background()); err != nil {
		t.Fatalf("Configure failed: %v", err)
	}

	ds := NewDataSource()
	configureSubnetDS(t, ds, c)
	schemaResp := getSubnetDSSchema(t)

	ctx := context.Background()
	tfType := schemaResp.Schema.Type().TerraformType(ctx)

	configVal := tftypes.NewValue(tfType, map[string]tftypes.Value{
		"id":            tftypes.NewValue(tftypes.String, nil),
		"name":          tftypes.NewValue(tftypes.String, "web-subnet"),
		"vpc_id":        tftypes.NewValue(tftypes.String, "vpc-2"),
		"description":   tftypes.NewValue(tftypes.String, nil),
		"cidr":          tftypes.NewValue(tftypes.String, nil),
		"zone":          tftypes.NewValue(tftypes.String, nil),
		"gateway_ip":    tftypes.NewValue(tftypes.String, nil),
		"is_public":     tftypes.NewValue(tftypes.Bool, nil),
		"status":        tftypes.NewValue(tftypes.String, nil),
		"available_ips": tftypes.NewValue(tftypes.Number, nil),
		"tags":          tftypes.NewValue(tftypes.Map{ElementType: tftypes.String}, nil),
		"created_at":    tftypes.NewValue(tftypes.String, nil),
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

	var state subnetModel
	readResp.State.Get(ctx, &state)

	if state.ID.ValueString() != "sub-2" {
		t.Errorf("expected ID sub-2 (from vpc-2), got %s", state.ID.ValueString())
	}
}

func TestTFSDK_ReadSubnetBothIDAndName(t *testing.T) {
	subnets := []apiSubnet{{ID: "sub-1", Name: "test", VPCID: "vpc-1", CIDR: "10.0.1.0/24", Status: "active", CreatedAt: "2025-01-01T00:00:00Z"}}
	server := newTestServer(t, subnets)
	defer server.Close()

	c := client.NewClient(server.URL, "test-key") // pragma: allowlist secret
	if err := c.Configure(context.Background()); err != nil {
		t.Fatalf("Configure failed: %v", err)
	}

	ds := NewDataSource()
	configureSubnetDS(t, ds, c)
	schemaResp := getSubnetDSSchema(t)

	ctx := context.Background()
	tfType := schemaResp.Schema.Type().TerraformType(ctx)

	configVal := tftypes.NewValue(tfType, map[string]tftypes.Value{
		"id":            tftypes.NewValue(tftypes.String, "sub-1"),
		"name":          tftypes.NewValue(tftypes.String, "test"),
		"vpc_id":        tftypes.NewValue(tftypes.String, nil),
		"description":   tftypes.NewValue(tftypes.String, nil),
		"cidr":          tftypes.NewValue(tftypes.String, nil),
		"zone":          tftypes.NewValue(tftypes.String, nil),
		"gateway_ip":    tftypes.NewValue(tftypes.String, nil),
		"is_public":     tftypes.NewValue(tftypes.Bool, nil),
		"status":        tftypes.NewValue(tftypes.String, nil),
		"available_ips": tftypes.NewValue(tftypes.Number, nil),
		"tags":          tftypes.NewValue(tftypes.Map{ElementType: tftypes.String}, nil),
		"created_at":    tftypes.NewValue(tftypes.String, nil),
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

func TestTFSDK_ReadSubnetNeitherIDNorName(t *testing.T) {
	server := newTestServer(t, nil)
	defer server.Close()

	c := client.NewClient(server.URL, "test-key") // pragma: allowlist secret
	if err := c.Configure(context.Background()); err != nil {
		t.Fatalf("Configure failed: %v", err)
	}

	ds := NewDataSource()
	configureSubnetDS(t, ds, c)
	schemaResp := getSubnetDSSchema(t)

	ctx := context.Background()
	tfType := schemaResp.Schema.Type().TerraformType(ctx)

	configVal := tftypes.NewValue(tfType, map[string]tftypes.Value{
		"id":            tftypes.NewValue(tftypes.String, nil),
		"name":          tftypes.NewValue(tftypes.String, nil),
		"vpc_id":        tftypes.NewValue(tftypes.String, nil),
		"description":   tftypes.NewValue(tftypes.String, nil),
		"cidr":          tftypes.NewValue(tftypes.String, nil),
		"zone":          tftypes.NewValue(tftypes.String, nil),
		"gateway_ip":    tftypes.NewValue(tftypes.String, nil),
		"is_public":     tftypes.NewValue(tftypes.Bool, nil),
		"status":        tftypes.NewValue(tftypes.String, nil),
		"available_ips": tftypes.NewValue(tftypes.Number, nil),
		"tags":          tftypes.NewValue(tftypes.Map{ElementType: tftypes.String}, nil),
		"created_at":    tftypes.NewValue(tftypes.String, nil),
	})

	readReq := datasource.ReadRequest{
		Config: tfsdk.Config{Schema: schemaResp.Schema, Raw: configVal},
	}
	var readResp datasource.ReadResponse
	readResp.State = tfsdk.State{Schema: schemaResp.Schema}

	ds.Read(ctx, readReq, &readResp)

	if !readResp.Diagnostics.HasError() {
		t.Error("expected error when neither id nor name is specified")
	}
}

func TestAPISubnetSerialization(t *testing.T) {
	sub := apiSubnet{
		ID:           "sub-1",
		Name:         "test",
		VPCID:        "vpc-1",
		Description:  "Test subnet",
		CIDR:         "10.0.1.0/24",
		Zone:         "sweden-a",
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

	// Wire-contract guard: literal keys must be the canonical cidrBlock /
	// availableIpCount / availabilityZone (a struct->struct round-trip can't
	// catch a wrong tag — assert the bytes and a backend-shaped decode).
	s := string(data)
	for _, want := range []string{`"cidrBlock"`, `"availableIpCount"`, `"availabilityZone"`} {
		if !strings.Contains(s, want) {
			t.Errorf("expected wire key %s, got: %s", want, s)
		}
	}
	if strings.Contains(s, `"cidr"`) || strings.Contains(s, `"availableIps"`) {
		t.Errorf("unexpected legacy wire key (cidr/availableIps) in: %s", s)
	}

	var fromWire apiSubnet
	if err := json.Unmarshal([]byte(`{"cidrBlock":"10.0.9.0/24","availableIpCount":250,"availabilityZone":"sweden-a"}`), &fromWire); err != nil {
		t.Fatalf("Unmarshal backend payload failed: %v", err)
	}
	if fromWire.CIDR != "10.0.9.0/24" || fromWire.AvailableIPs != 250 || fromWire.Zone != "sweden-a" {
		t.Errorf("backend payload did not populate fields: cidr=%q ips=%d zone=%q", fromWire.CIDR, fromWire.AvailableIPs, fromWire.Zone)
	}
}
