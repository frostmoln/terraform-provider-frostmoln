package database_types

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
	if resp.TypeName != "frostmoln_database_types" {
		t.Errorf("expected type name frostmoln_database_types, got %s", resp.TypeName)
	}
}

func TestSchema(t *testing.T) {
	ds := NewDataSource()
	req := datasource.SchemaRequest{}
	var resp datasource.SchemaResponse
	ds.Schema(context.Background(), req, &resp)

	if _, ok := resp.Schema.Attributes["types"]; !ok {
		t.Error("expected types attribute in schema")
	}
}

func TestConfigureNilProviderData(t *testing.T) {
	ds := &databaseTypesDataSource{}
	req := datasource.ConfigureRequest{}
	var resp datasource.ConfigureResponse
	ds.Configure(context.Background(), req, &resp)
	if resp.Diagnostics.HasError() {
		t.Errorf("expected no errors for nil provider data, got %v", resp.Diagnostics.Errors())
	}
}

func TestConfigureWrongType(t *testing.T) {
	ds := &databaseTypesDataSource{}
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
		if r.URL.Path != "/v1/databases/types" {
			t.Errorf("unexpected path: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(apiDatabaseTypeList{
			Types: []apiDatabaseType{
				{
					Type: "postgresql",
					Versions: []apiDatabaseVersion{
						{Version: "15", Status: "supported", EndOfLife: "2027-11-11", IsDefault: false},
						{Version: "16", Status: "current", IsDefault: true},
					},
				},
				{
					Type: "mysql",
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

	ds := &databaseTypesDataSource{client: c}

	var schemaResp datasource.SchemaResponse
	ds.Schema(ctx, datasource.SchemaRequest{}, &schemaResp)

	stateVal := tftypes.NewValue(schemaResp.Schema.Type().TerraformType(ctx), nil)
	state := tfsdk.State{Schema: schemaResp.Schema, Raw: stateVal}

	// Build a config with a null types list, derived from the schema's own type
	// (types is a nested list of objects with a nested list of version objects).
	typesType := schemaResp.Schema.Attributes["types"].GetType().TerraformType(ctx)
	configVal := tftypes.NewValue(schemaResp.Schema.Type().TerraformType(ctx), map[string]tftypes.Value{
		"types": tftypes.NewValue(typesType, nil),
	})
	config := tfsdk.Config{Schema: schemaResp.Schema, Raw: configVal}

	readResp := datasource.ReadResponse{State: state}
	ds.Read(ctx, datasource.ReadRequest{Config: config}, &readResp)

	if readResp.Diagnostics.HasError() {
		t.Fatalf("read failed: %v", readResp.Diagnostics.Errors())
	}

	var result databaseTypesModel
	readResp.State.Get(ctx, &result)
	if result.Types.IsNull() || result.Types.IsUnknown() {
		t.Fatal("expected non-null types list")
	}

	var items []typeItemModel
	diags := result.Types.ElementsAs(ctx, &items, false)
	if diags.HasError() {
		t.Fatalf("failed to extract types: %v", diags.Errors())
	}
	if len(items) != 2 {
		t.Fatalf("expected 2 types, got %d", len(items))
	}
	if items[0].Type.ValueString() != "postgresql" {
		t.Errorf("expected first type postgresql, got %s", items[0].Type.ValueString())
	}
	if items[1].Type.ValueString() != "mysql" {
		t.Errorf("expected second type mysql, got %s", items[1].Type.ValueString())
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
