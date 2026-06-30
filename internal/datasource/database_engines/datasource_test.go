package database_engines

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
	if resp.TypeName != "frostmoln_database_engines" {
		t.Errorf("expected type name frostmoln_database_engines, got %s", resp.TypeName)
	}
}

func TestSchema(t *testing.T) {
	ds := NewDataSource()
	req := datasource.SchemaRequest{}
	var resp datasource.SchemaResponse
	ds.Schema(context.Background(), req, &resp)

	if _, ok := resp.Schema.Attributes["engines"]; !ok {
		t.Error("expected engines attribute in schema")
	}
}

func TestConfigureNilProviderData(t *testing.T) {
	ds := &databaseEnginesDataSource{}
	req := datasource.ConfigureRequest{}
	var resp datasource.ConfigureResponse
	ds.Configure(context.Background(), req, &resp)
	if resp.Diagnostics.HasError() {
		t.Errorf("expected no errors for nil provider data, got %v", resp.Diagnostics.Errors())
	}
}

func TestConfigureWrongType(t *testing.T) {
	ds := &databaseEnginesDataSource{}
	req := datasource.ConfigureRequest{
		ProviderData: "not-a-client",
	}
	var resp datasource.ConfigureResponse
	ds.Configure(context.Background(), req, &resp)
	if !resp.Diagnostics.HasError() {
		t.Error("expected error for wrong provider data type")
	}
}

func TestRead(t *testing.T) {
	ctx := context.Background()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/databases/engines" {
			t.Errorf("unexpected path: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(apiDatabaseEngineList{
			Engines: []apiDatabaseEngine{
				{
					Engine: "postgresql",
					Versions: []apiDatabaseVersion{
						{Version: "15", Status: "supported", EndOfLife: "2027-11-11", IsDefault: false},
						{Version: "16", Status: "current", IsDefault: true},
					},
				},
				{
					Engine: "mysql",
					Versions: []apiDatabaseVersion{
						{Version: "8.0", Status: "supported", IsDefault: false},
						{Version: "8.4", Status: "current", IsDefault: true},
					},
				},
			},
		})
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key", client.WithHTTPClient(server.Client()))

	ds := &databaseEnginesDataSource{client: c}

	var schemaResp datasource.SchemaResponse
	ds.Schema(ctx, datasource.SchemaRequest{}, &schemaResp)

	stateVal := tftypes.NewValue(schemaResp.Schema.Type().TerraformType(ctx), nil)
	state := tfsdk.State{Schema: schemaResp.Schema, Raw: stateVal}

	// Build a config with a null engines list, derived from the schema's own type
	// (engines is a nested list of objects with a nested list of version objects).
	enginesType := schemaResp.Schema.Attributes["engines"].GetType().TerraformType(ctx)
	configVal := tftypes.NewValue(schemaResp.Schema.Type().TerraformType(ctx), map[string]tftypes.Value{
		"engines": tftypes.NewValue(enginesType, nil),
	})
	config := tfsdk.Config{Schema: schemaResp.Schema, Raw: configVal}

	readResp := datasource.ReadResponse{State: state}
	ds.Read(ctx, datasource.ReadRequest{Config: config}, &readResp)

	if readResp.Diagnostics.HasError() {
		t.Fatalf("read failed: %v", readResp.Diagnostics.Errors())
	}

	var result databaseEnginesModel
	readResp.State.Get(ctx, &result)
	if result.Engines.IsNull() || result.Engines.IsUnknown() {
		t.Fatal("expected non-null engines list")
	}

	var items []engineItemModel
	diags := result.Engines.ElementsAs(ctx, &items, false)
	if diags.HasError() {
		t.Fatalf("failed to extract engines: %v", diags.Errors())
	}
	if len(items) != 2 {
		t.Fatalf("expected 2 engines, got %d", len(items))
	}
	if items[0].Engine.ValueString() != "postgresql" {
		t.Errorf("expected first engine postgresql, got %s", items[0].Engine.ValueString())
	}
	if items[1].Engine.ValueString() != "mysql" {
		t.Errorf("expected second engine mysql, got %s", items[1].Engine.ValueString())
	}

	var pgVersions []versionItemModel
	if d := items[0].Versions.ElementsAs(ctx, &pgVersions, false); d.HasError() {
		t.Fatalf("failed to extract postgresql versions: %v", d.Errors())
	}
	if len(pgVersions) != 2 {
		t.Fatalf("expected 2 postgresql versions, got %d", len(pgVersions))
	}
	if pgVersions[0].Version.ValueString() != "15" || pgVersions[0].Status.ValueString() != "supported" {
		t.Errorf("unexpected first version: %+v", pgVersions[0])
	}
	if pgVersions[0].EndOfLife.ValueString() != "2027-11-11" {
		t.Errorf("expected end_of_life 2027-11-11, got %s", pgVersions[0].EndOfLife.ValueString())
	}
	if pgVersions[1].Version.ValueString() != "16" || pgVersions[1].Status.ValueString() != "current" {
		t.Errorf("unexpected second version: %+v", pgVersions[1])
	}
	if !pgVersions[1].IsDefault.ValueBool() {
		t.Error("expected version 16 to be the default")
	}
	if !pgVersions[1].EndOfLife.IsNull() {
		t.Error("expected null end_of_life for version with no EOL")
	}
}
