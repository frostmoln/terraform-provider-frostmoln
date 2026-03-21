package mysql_versions

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
	if resp.TypeName != "frostmoln_mysql_versions" {
		t.Errorf("expected type name frostmoln_mysql_versions, got %s", resp.TypeName)
	}
}

func TestSchema(t *testing.T) {
	ds := NewDataSource()
	req := datasource.SchemaRequest{}
	var resp datasource.SchemaResponse
	ds.Schema(context.Background(), req, &resp)

	if _, ok := resp.Schema.Attributes["versions"]; !ok {
		t.Error("expected versions attribute in schema")
	}
}

func TestConfigureNilProviderData(t *testing.T) {
	ds := &mysqlVersionsDataSource{}
	req := datasource.ConfigureRequest{}
	var resp datasource.ConfigureResponse
	ds.Configure(context.Background(), req, &resp)
	if resp.Diagnostics.HasError() {
		t.Errorf("expected no errors for nil provider data, got %v", resp.Diagnostics.Errors())
	}
}

func TestConfigureWrongType(t *testing.T) {
	ds := &mysqlVersionsDataSource{}
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
		if r.URL.Path != "/v1/databases/versions" {
			t.Errorf("unexpected path: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if r.URL.Query().Get("engine") != "mysql" {
			t.Errorf("expected engine=mysql query param, got %s", r.URL.Query().Get("engine"))
		}
		_ = json.NewEncoder(w).Encode(apiMysqlVersionList{
			Versions: []apiMysqlVersion{
				{Version: "8.0", Status: "supported", EndOfLife: "2026-04-01"},
				{Version: "8.4", Status: "supported"},
				{Version: "9.2", Status: "supported"},
			},
		})
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key", client.WithHTTPClient(server.Client()))

	ds := &mysqlVersionsDataSource{client: c}

	var schemaResp datasource.SchemaResponse
	ds.Schema(context.Background(), datasource.SchemaRequest{}, &schemaResp)

	stateVal := tftypes.NewValue(schemaResp.Schema.Type().TerraformType(context.Background()), nil)
	state := tfsdk.State{Schema: schemaResp.Schema, Raw: stateVal}

	// Build config from schema too
	configVal := tftypes.NewValue(schemaResp.Schema.Type().TerraformType(context.Background()), map[string]tftypes.Value{
		"versions": tftypes.NewValue(
			tftypes.List{ElementType: tftypes.Object{
				AttributeTypes: map[string]tftypes.Type{
					"version":     tftypes.String,
					"status":      tftypes.String,
					"end_of_life": tftypes.String,
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

	var result mysqlVersionsModel
	readResp.State.Get(context.Background(), &result)
	if result.Versions.IsNull() || result.Versions.IsUnknown() {
		t.Fatal("expected non-null versions list")
	}

	var items []mysqlVersionItemModel
	diags := result.Versions.ElementsAs(context.Background(), &items, false)
	if diags.HasError() {
		t.Fatalf("failed to extract versions: %v", diags.Errors())
	}
	if len(items) != 3 {
		t.Errorf("expected 3 versions, got %d", len(items))
	}
}

func TestReadAPIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"code": "INTERNAL_ERROR", "message": "server error",
		})
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key", client.WithHTTPClient(server.Client()))

	ds := &mysqlVersionsDataSource{client: c}

	var schemaResp datasource.SchemaResponse
	ds.Schema(context.Background(), datasource.SchemaRequest{}, &schemaResp)

	stateVal := tftypes.NewValue(schemaResp.Schema.Type().TerraformType(context.Background()), nil)
	state := tfsdk.State{Schema: schemaResp.Schema, Raw: stateVal}

	configVal := tftypes.NewValue(schemaResp.Schema.Type().TerraformType(context.Background()), map[string]tftypes.Value{
		"versions": tftypes.NewValue(
			tftypes.List{ElementType: tftypes.Object{
				AttributeTypes: map[string]tftypes.Type{
					"version":     tftypes.String,
					"status":      tftypes.String,
					"end_of_life": tftypes.String,
				},
			}},
			nil,
		),
	})
	config := tfsdk.Config{Schema: schemaResp.Schema, Raw: configVal}

	readResp := datasource.ReadResponse{State: state}
	ds.Read(context.Background(), datasource.ReadRequest{Config: config}, &readResp)

	if !readResp.Diagnostics.HasError() {
		t.Error("expected error for API failure")
	}
}
