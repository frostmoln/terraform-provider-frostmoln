package postgres_versions

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
	if resp.TypeName != "frostmoln_postgres_versions" {
		t.Errorf("expected type name frostmoln_postgres_versions, got %s", resp.TypeName)
	}
}

func TestSchema(t *testing.T) {
	ds := NewDataSource()
	var resp datasource.SchemaResponse
	ds.Schema(context.Background(), datasource.SchemaRequest{}, &resp)

	if _, ok := resp.Schema.Attributes["versions"]; !ok {
		t.Error("expected attribute versions in schema")
	}
	versionsAttr := resp.Schema.Attributes["versions"].(schema.ListNestedAttribute)
	if !versionsAttr.Computed {
		t.Error("expected versions to be computed")
	}
}

func TestConfigureNilProviderData(t *testing.T) {
	ds := &postgresVersionsDataSource{}
	var resp datasource.ConfigureResponse
	ds.Configure(context.Background(), datasource.ConfigureRequest{}, &resp)
	if resp.Diagnostics.HasError() {
		t.Errorf("expected no errors, got %v", resp.Diagnostics.Errors())
	}
}

func TestConfigureWrongType(t *testing.T) {
	ds := &postgresVersionsDataSource{}
	var resp datasource.ConfigureResponse
	ds.Configure(context.Background(), datasource.ConfigureRequest{ProviderData: "not-a-client"}, &resp)
	if !resp.Diagnostics.HasError() {
		t.Error("expected error for wrong type")
	}
}

// --- helpers ---

func newTestServer(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/me", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(client.UserProfile{ID: "user-1", TenantID: "tenant-1"})
	})
	mux.HandleFunc("/", handler)
	return httptest.NewServer(mux)
}

func configureDS(t *testing.T, ds datasource.DataSource, c *client.Client) {
	t.Helper()
	dc, ok := ds.(datasource.DataSourceWithConfigure)
	if !ok {
		t.Fatal("datasource does not implement DataSourceWithConfigure")
	}
	var resp datasource.ConfigureResponse
	dc.Configure(context.Background(), datasource.ConfigureRequest{ProviderData: c}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("configure failed: %v", resp.Diagnostics.Errors())
	}
}

func getDSSchema(t *testing.T) datasource.SchemaResponse {
	t.Helper()
	ds := NewDataSource()
	var resp datasource.SchemaResponse
	ds.Schema(context.Background(), datasource.SchemaRequest{}, &resp)
	return resp
}

// emptyConfigVal builds a config with versions set to null (matching what
// Terraform passes in: the computed list is unknown/null at read time).
func emptyConfigVal(t *testing.T) tftypes.Value {
	t.Helper()
	schemaResp := getDSSchema(t)
	tfType := schemaResp.Schema.Type().TerraformType(context.Background())
	objType := tfType.(tftypes.Object)
	return tftypes.NewValue(tfType, map[string]tftypes.Value{
		"versions": tftypes.NewValue(objType.AttributeTypes["versions"], nil),
	})
}

func TestReadVersions(t *testing.T) {
	server := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/v1/databases/versions" {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(apiPostgresVersionList{
				Versions: []apiPostgresVersion{
					{Version: "16", Status: "supported", EndOfLife: "2028-11-09"},
					{Version: "15", Status: "supported"},
				},
			})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	})
	defer server.Close()

	c := client.NewClient(server.URL, "test-key") // pragma: allowlist secret
	if err := c.Configure(context.Background()); err != nil {
		t.Fatalf("Configure failed: %v", err)
	}

	ds := NewDataSource()
	configureDS(t, ds, c)
	schemaResp := getDSSchema(t)

	ctx := context.Background()
	readReq := datasource.ReadRequest{
		Config: tfsdk.Config{Schema: schemaResp.Schema, Raw: emptyConfigVal(t)},
	}
	var readResp datasource.ReadResponse
	readResp.State = tfsdk.State{Schema: schemaResp.Schema}

	ds.Read(ctx, readReq, &readResp)
	if readResp.Diagnostics.HasError() {
		t.Fatalf("Read failed: %v", readResp.Diagnostics.Errors())
	}

	var state postgresVersionsModel
	readResp.State.Get(ctx, &state)

	var items []postgresVersionItemModel
	diags := state.Versions.ElementsAs(ctx, &items, false)
	if diags.HasError() {
		t.Fatalf("ElementsAs failed: %v", diags.Errors())
	}
	if len(items) != 2 {
		t.Fatalf("expected 2 versions, got %d", len(items))
	}
	if items[0].Version.ValueString() != "16" {
		t.Errorf("expected version 16, got %s", items[0].Version.ValueString())
	}
	if items[0].Status.ValueString() != "supported" {
		t.Errorf("expected status supported, got %s", items[0].Status.ValueString())
	}
	if items[0].EndOfLife.ValueString() != "2028-11-09" {
		t.Errorf("expected end_of_life 2028-11-09, got %s", items[0].EndOfLife.ValueString())
	}
	if !items[1].EndOfLife.IsNull() {
		t.Error("expected null end_of_life for version 15")
	}
}

func TestReadEmptyVersions(t *testing.T) {
	server := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/v1/databases/versions" {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(apiPostgresVersionList{})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	})
	defer server.Close()

	c := client.NewClient(server.URL, "test-key") // pragma: allowlist secret
	if err := c.Configure(context.Background()); err != nil {
		t.Fatalf("Configure failed: %v", err)
	}

	ds := NewDataSource()
	configureDS(t, ds, c)
	schemaResp := getDSSchema(t)

	ctx := context.Background()
	readReq := datasource.ReadRequest{
		Config: tfsdk.Config{Schema: schemaResp.Schema, Raw: emptyConfigVal(t)},
	}
	var readResp datasource.ReadResponse
	readResp.State = tfsdk.State{Schema: schemaResp.Schema}

	ds.Read(ctx, readReq, &readResp)
	if readResp.Diagnostics.HasError() {
		t.Fatalf("Read failed: %v", readResp.Diagnostics.Errors())
	}

	var state postgresVersionsModel
	readResp.State.Get(ctx, &state)
	var items []postgresVersionItemModel
	state.Versions.ElementsAs(ctx, &items, false)
	if len(items) != 0 {
		t.Errorf("expected 0 versions, got %d", len(items))
	}
}

func TestReadAPIError(t *testing.T) {
	server := newTestServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"code": "INTERNAL_ERROR", "message": "server error",
		})
	})
	defer server.Close()

	c := client.NewClient(server.URL, "test-key") // pragma: allowlist secret
	if err := c.Configure(context.Background()); err != nil {
		t.Fatalf("Configure failed: %v", err)
	}

	ds := NewDataSource()
	configureDS(t, ds, c)
	schemaResp := getDSSchema(t)

	ctx := context.Background()
	readReq := datasource.ReadRequest{
		Config: tfsdk.Config{Schema: schemaResp.Schema, Raw: emptyConfigVal(t)},
	}
	var readResp datasource.ReadResponse
	readResp.State = tfsdk.State{Schema: schemaResp.Schema}

	ds.Read(ctx, readReq, &readResp)
	if !readResp.Diagnostics.HasError() {
		t.Error("expected error for API failure")
	}
}

func TestReadBadResponseBody(t *testing.T) {
	server := newTestServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("not json"))
	})
	defer server.Close()

	c := client.NewClient(server.URL, "test-key") // pragma: allowlist secret
	if err := c.Configure(context.Background()); err != nil {
		t.Fatalf("Configure failed: %v", err)
	}

	ds := NewDataSource()
	configureDS(t, ds, c)
	schemaResp := getDSSchema(t)

	ctx := context.Background()
	readReq := datasource.ReadRequest{
		Config: tfsdk.Config{Schema: schemaResp.Schema, Raw: emptyConfigVal(t)},
	}
	var readResp datasource.ReadResponse
	readResp.State = tfsdk.State{Schema: schemaResp.Schema}

	ds.Read(ctx, readReq, &readResp)
	if !readResp.Diagnostics.HasError() {
		t.Error("expected error for bad response body")
	}
}
