package regions

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
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
	if resp.TypeName != "frostmoln_regions" {
		t.Errorf("expected type name frostmoln_regions, got %s", resp.TypeName)
	}
}

func TestSchema(t *testing.T) {
	ds := NewDataSource()
	req := datasource.SchemaRequest{}
	var resp datasource.SchemaResponse
	ds.Schema(context.Background(), req, &resp)

	if _, ok := resp.Schema.Attributes["regions"]; !ok {
		t.Error("expected regions attribute in schema")
	}
}

func TestConfigureNilProviderData(t *testing.T) {
	ds := &regionsDataSource{}
	req := datasource.ConfigureRequest{}
	var resp datasource.ConfigureResponse
	ds.Configure(context.Background(), req, &resp)
	if resp.Diagnostics.HasError() {
		t.Errorf("expected no errors, got %v", resp.Diagnostics.Errors())
	}
}

func TestConfigureWrongType(t *testing.T) {
	ds := &regionsDataSource{}
	req := datasource.ConfigureRequest{ProviderData: "not-a-client"}
	var resp datasource.ConfigureResponse
	ds.Configure(context.Background(), req, &resp)
	if !resp.Diagnostics.HasError() {
		t.Error("expected error for wrong type")
	}
}

func newTestServer(t *testing.T, regions []apiRegion) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/regions", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(apiRegionList{Data: regions}); err != nil {
			t.Errorf("failed to encode regions: %v", err)
		}
	})
	mux.HandleFunc("/v1/me", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(client.UserProfile{
			ID:       "user-1",
			TenantID: "tenant-1",
		}); err != nil {
			t.Errorf("failed to encode profile: %v", err)
		}
	})
	return httptest.NewServer(mux)
}

func sampleRegions() []apiRegion {
	return []apiRegion{
		{
			ID:          "sweden",
			Name:        "Sweden",
			Description: "Swedish region",
			Country:     "SE",
			Status:      "active",
			IsDefault:   true,
			AvailabilityZones: []apiAZ{
				{ID: "falkenberg", Name: "Falkenberg", City: "Falkenberg", Status: "active", IsDefault: true},
			},
		},
		{
			ID:                "germany",
			Name:              "Germany",
			Description:       "German region",
			Country:           "DE",
			Status:            "maintenance",
			IsDefault:         false,
			AvailabilityZones: []apiAZ{},
		},
	}
}

func TestReadAll(t *testing.T) {
	server := newTestServer(t, sampleRegions())
	defer server.Close()

	c := client.NewClient(server.URL, "test-key") // pragma: allowlist secret
	if err := c.Configure(context.Background()); err != nil {
		t.Fatalf("Configure failed: %v", err)
	}

	apiResp, err := c.Get(context.Background(), "/v1/regions", nil)
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}

	var list apiRegionList
	if err := json.Unmarshal(apiResp.Body, &list); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	if len(list.Data) != 2 {
		t.Fatalf("expected 2 regions, got %d", len(list.Data))
	}
}

func TestRegionItemAttrTypes(t *testing.T) {
	expectedKeys := []string{"id", "name", "description", "country", "status", "is_default", "availability_zones"}
	for _, key := range expectedKeys {
		if _, ok := regionItemAttrTypes[key]; !ok {
			t.Errorf("expected key %q in regionItemAttrTypes", key)
		}
	}
}

func TestAZItemAttrTypes(t *testing.T) {
	expectedKeys := []string{"id", "name", "city", "status", "is_default"}
	for _, key := range expectedKeys {
		if _, ok := azItemAttrTypes[key]; !ok {
			t.Errorf("expected key %q in azItemAttrTypes", key)
		}
	}
}

func TestAPIRegionListSerialization(t *testing.T) {
	list := apiRegionList{Data: sampleRegions()}

	data, err := json.Marshal(list)
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}

	var decoded apiRegionList
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	if len(decoded.Data) != 2 {
		t.Fatalf("expected 2 regions, got %d", len(decoded.Data))
	}
	if decoded.Data[0].AvailabilityZones[0].ID != "falkenberg" {
		t.Errorf("expected first AZ id falkenberg, got %s", decoded.Data[0].AvailabilityZones[0].ID)
	}
}

// --- tfsdk-level Read tests ---

func configureRegionsDS(t *testing.T, ds datasource.DataSource, c *client.Client) {
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

func getRegionsDSSchema(t *testing.T) datasource.SchemaResponse {
	t.Helper()
	ds := NewDataSource()
	var schemaResp datasource.SchemaResponse
	ds.Schema(context.Background(), datasource.SchemaRequest{}, &schemaResp)
	return schemaResp
}

func TestTFSDK_ReadAllRegions(t *testing.T) {
	server := newTestServer(t, sampleRegions())
	defer server.Close()

	c := client.NewClient(server.URL, "test-key") // pragma: allowlist secret
	if err := c.Configure(context.Background()); err != nil {
		t.Fatalf("Configure failed: %v", err)
	}

	ds := NewDataSource()
	configureRegionsDS(t, ds, c)
	schemaResp := getRegionsDSSchema(t)

	ctx := context.Background()
	tfType := schemaResp.Schema.Type().TerraformType(ctx)

	configVal := tftypes.NewValue(tfType, map[string]tftypes.Value{
		"regions": tftypes.NewValue(schemaResp.Schema.Attributes["regions"].GetType().TerraformType(ctx), nil),
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

	var state regionsModel
	readResp.State.Get(ctx, &state)

	var items []regionItemModel
	state.Regions.ElementsAs(ctx, &items, false)

	if len(items) != 2 {
		t.Fatalf("expected 2 regions, got %d", len(items))
	}

	// Verify the first region's scalar fields and nested AZ.
	first := items[0]
	if first.ID.ValueString() != "sweden" {
		t.Errorf("expected first region id sweden, got %s", first.ID.ValueString())
	}
	if first.Country.ValueString() != "SE" {
		t.Errorf("expected first region country SE, got %s", first.Country.ValueString())
	}
	if !first.IsDefault.ValueBool() {
		t.Error("expected first region to be default")
	}

	var azItems []azItemModel
	first.AvailabilityZones.ElementsAs(ctx, &azItems, false)
	if len(azItems) != 1 {
		t.Fatalf("expected 1 availability zone in first region, got %d", len(azItems))
	}
	if azItems[0].ID.ValueString() != "falkenberg" {
		t.Errorf("expected AZ id falkenberg, got %s", azItems[0].ID.ValueString())
	}
	if azItems[0].City.ValueString() != "Falkenberg" {
		t.Errorf("expected AZ city Falkenberg, got %s", azItems[0].City.ValueString())
	}
	if !azItems[0].IsDefault.ValueBool() {
		t.Error("expected AZ to be default")
	}

	// Second region has an empty AZ list.
	var secondAZ []azItemModel
	items[1].AvailabilityZones.ElementsAs(ctx, &secondAZ, false)
	if len(secondAZ) != 0 {
		t.Errorf("expected 0 availability zones in second region, got %d", len(secondAZ))
	}
}
