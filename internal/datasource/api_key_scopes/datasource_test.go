package apikeyscopes

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
	if ds := NewDataSource(); ds == nil {
		t.Fatal("expected non-nil data source")
	}
}

func TestMetadata(t *testing.T) {
	ds := NewDataSource()
	req := datasource.MetadataRequest{ProviderTypeName: "frostmoln"}
	var resp datasource.MetadataResponse
	ds.Metadata(context.Background(), req, &resp)
	if resp.TypeName != "frostmoln_api_key_scopes" {
		t.Errorf("expected type name frostmoln_api_key_scopes, got %s", resp.TypeName)
	}
}

func TestSchema(t *testing.T) {
	ds := NewDataSource()
	var resp datasource.SchemaResponse
	ds.Schema(context.Background(), datasource.SchemaRequest{}, &resp)

	if _, ok := resp.Schema.Attributes["scopes"]; !ok {
		t.Error("expected scopes attribute in schema")
	}
	// Operations are the access-policy grammar and are enforced nowhere as a key
	// scope, so this data source must not advertise them for granting.
	if _, ok := resp.Schema.Attributes["operations"]; ok {
		t.Error("operations must not be exposed: they mint but are denied at every service")
	}
}

func TestConfigureNilProviderData(t *testing.T) {
	ds := &apiKeyScopesDataSource{}
	var resp datasource.ConfigureResponse
	ds.Configure(context.Background(), datasource.ConfigureRequest{}, &resp)
	if resp.Diagnostics.HasError() {
		t.Errorf("expected no errors, got %v", resp.Diagnostics.Errors())
	}
}

func TestConfigureWrongType(t *testing.T) {
	ds := &apiKeyScopesDataSource{}
	var resp datasource.ConfigureResponse
	ds.Configure(context.Background(), datasource.ConfigureRequest{ProviderData: "not-a-client"}, &resp)
	if !resp.Diagnostics.HasError() {
		t.Error("expected error for wrong type")
	}
}

func TestScopeItemAttrTypes(t *testing.T) {
	for _, key := range []string{"scope", "service", "description"} {
		if _, ok := scopeItemAttrTypes[key]; !ok {
			t.Errorf("expected key %q in scopeItemAttrTypes", key)
		}
	}
}

// newTestServer serves identity's real catalog shape: the global "*" is present
// (it is meaningful when CHECKING a scope) alongside a per-service wildcard.
func newTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/scopes", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(apiScopeList{
			Scopes: []apiScope{
				{Scope: "*", Description: "Full access to all resources"},
				{Scope: "compute:read", Description: "Read compute resources"},
				{Scope: "compute:*", Description: "Full access to compute resources"},
			},
		}); err != nil {
			t.Errorf("failed to encode scopes: %v", err)
		}
	})
	mux.HandleFunc("/v1/me", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(client.UserProfile{ID: "user-1", TenantID: "tenant-1"}); err != nil {
			t.Errorf("failed to encode profile: %v", err)
		}
	})
	return httptest.NewServer(mux)
}

func TestTFSDK_ReadCatalog(t *testing.T) {
	server := newTestServer(t)
	defer server.Close()

	c := client.NewClient(server.URL, "test-key") // pragma: allowlist secret
	if err := c.Configure(context.Background()); err != nil {
		t.Fatalf("Configure failed: %v", err)
	}

	ds := NewDataSource()
	dc, ok := ds.(datasource.DataSourceWithConfigure)
	if !ok {
		t.Fatal("datasource does not implement DataSourceWithConfigure")
	}
	var configResp datasource.ConfigureResponse
	dc.Configure(context.Background(), datasource.ConfigureRequest{ProviderData: c}, &configResp)
	if configResp.Diagnostics.HasError() {
		t.Fatalf("configure failed: %v", configResp.Diagnostics.Errors())
	}

	var schemaResp datasource.SchemaResponse
	ds.Schema(context.Background(), datasource.SchemaRequest{}, &schemaResp)

	ctx := context.Background()
	tfType := schemaResp.Schema.Type().TerraformType(ctx)
	configVal := tftypes.NewValue(tfType, map[string]tftypes.Value{
		"scopes": tftypes.NewValue(schemaResp.Schema.Attributes["scopes"].GetType().TerraformType(ctx), nil),
	})

	readReq := datasource.ReadRequest{Config: tfsdk.Config{Schema: schemaResp.Schema, Raw: configVal}}
	var readResp datasource.ReadResponse
	readResp.State = tfsdk.State{Schema: schemaResp.Schema}

	ds.Read(ctx, readReq, &readResp)
	if readResp.Diagnostics.HasError() {
		t.Fatalf("Read failed: %v", readResp.Diagnostics.Errors())
	}

	var state apiKeyScopesModel
	readResp.State.Get(ctx, &state)

	var scopes []scopeItemModel
	state.Scopes.ElementsAs(ctx, &scopes, false)

	// The global "*" must be dropped — the API rejects it when granting, so a
	// plan built from it would fail on apply.
	if len(scopes) != 2 {
		t.Fatalf("expected 2 grantable scopes, got %d: %+v", len(scopes), scopes)
	}
	for _, s := range scopes {
		if s.Scope.ValueString() == "*" {
			t.Error("the global wildcard must not be offered")
		}
	}
	if got := scopes[0].Scope.ValueString(); got != "compute:read" {
		t.Errorf("expected first scope compute:read, got %s", got)
	}
	if got := scopes[0].Service.ValueString(); got != "compute" {
		t.Errorf("expected service compute, got %s", got)
	}
	if got := scopes[0].Description.ValueString(); got != "Read compute resources" {
		t.Errorf("unexpected description: %s", got)
	}
	// A per-service wildcard IS grantable on an API key, so it stays.
	if got := scopes[1].Scope.ValueString(); got != "compute:*" {
		t.Errorf("expected per-service wildcard to be kept, got %s", got)
	}
}
