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
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/databases/engines" {
			t.Errorf("unexpected path: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(apiDatabaseEngineList{
			Engines: []apiDatabaseEngine{
				{
					Name:        "postgresql",
					Description: "PostgreSQL relational database",
					Versions:    []string{"14", "15", "16", "17"},
				},
				{
					Name:        "mysql",
					Description: "MySQL relational database",
					Versions:    []string{"8.0", "8.4", "9.2"},
				},
			},
		})
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key", client.WithHTTPClient(server.Client()))

	ds := &databaseEnginesDataSource{client: c}

	var schemaResp datasource.SchemaResponse
	ds.Schema(context.Background(), datasource.SchemaRequest{}, &schemaResp)

	stateVal := tftypes.NewValue(schemaResp.Schema.Type().TerraformType(context.Background()), nil)
	state := tfsdk.State{Schema: schemaResp.Schema, Raw: stateVal}

	// Build a config with null engines
	configVal := tftypes.NewValue(schemaResp.Schema.Type().TerraformType(context.Background()), map[string]tftypes.Value{
		"engines": tftypes.NewValue(
			tftypes.List{ElementType: tftypes.Object{
				AttributeTypes: map[string]tftypes.Type{
					"name":        tftypes.String,
					"description": tftypes.String,
					"versions":    tftypes.List{ElementType: tftypes.String},
				},
			}},
			nil, // null list
		),
	})
	config := tfsdk.Config{Schema: schemaResp.Schema, Raw: configVal}

	readResp := datasource.ReadResponse{State: state}
	ds.Read(context.Background(), datasource.ReadRequest{Config: config}, &readResp)

	if readResp.Diagnostics.HasError() {
		t.Fatalf("read failed: %v", readResp.Diagnostics.Errors())
	}

	var result databaseEnginesModel
	readResp.State.Get(context.Background(), &result)
	if result.Engines.IsNull() || result.Engines.IsUnknown() {
		t.Fatal("expected non-null engines list")
	}

	var items []databaseEngineItemModel
	diags := result.Engines.ElementsAs(context.Background(), &items, false)
	if diags.HasError() {
		t.Fatalf("failed to extract engines: %v", diags.Errors())
	}
	if len(items) != 2 {
		t.Errorf("expected 2 engines, got %d", len(items))
	}
	if items[0].Name.ValueString() != "postgresql" {
		t.Errorf("expected first engine postgresql, got %s", items[0].Name.ValueString())
	}
	if items[1].Name.ValueString() != "mysql" {
		t.Errorf("expected second engine mysql, got %s", items[1].Name.ValueString())
	}
}
