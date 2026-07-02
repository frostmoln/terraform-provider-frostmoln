package kubernetes_versions

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
	if NewDataSource() == nil {
		t.Fatal("expected non-nil data source")
	}
}

func TestMetadata(t *testing.T) {
	ds := NewDataSource()
	req := datasource.MetadataRequest{ProviderTypeName: "frostmoln"}
	var resp datasource.MetadataResponse
	ds.Metadata(context.Background(), req, &resp)
	if resp.TypeName != "frostmoln_kubernetes_versions" {
		t.Errorf("expected type name frostmoln_kubernetes_versions, got %s", resp.TypeName)
	}
}

func TestSchema(t *testing.T) {
	ds := NewDataSource()
	var resp datasource.SchemaResponse
	ds.Schema(context.Background(), datasource.SchemaRequest{}, &resp)
	if _, ok := resp.Schema.Attributes["versions"]; !ok {
		t.Error("expected versions attribute in schema")
	}
}

func TestConfigureNilProviderData(t *testing.T) {
	ds := &kubernetesVersionsDataSource{}
	var resp datasource.ConfigureResponse
	ds.Configure(context.Background(), datasource.ConfigureRequest{}, &resp)
	if resp.Diagnostics.HasError() {
		t.Errorf("expected no errors for nil provider data, got %v", resp.Diagnostics.Errors())
	}
}

func TestConfigureWrongType(t *testing.T) {
	ds := &kubernetesVersionsDataSource{}
	var resp datasource.ConfigureResponse
	ds.Configure(context.Background(), datasource.ConfigureRequest{ProviderData: "not-a-client"}, &resp)
	if !resp.Diagnostics.HasError() {
		t.Error("expected error for wrong provider data type")
	}
}

func emptyState(t *testing.T, ds *kubernetesVersionsDataSource) tfsdk.State {
	t.Helper()
	var schemaResp datasource.SchemaResponse
	ds.Schema(context.Background(), datasource.SchemaRequest{}, &schemaResp)
	stateVal := tftypes.NewValue(schemaResp.Schema.Type().TerraformType(context.Background()), nil)
	return tfsdk.State{Schema: schemaResp.Schema, Raw: stateVal}
}

func TestRead(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/kubernetes/versions" {
			t.Errorf("unexpected path: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(apiKubernetesVersionList{
			Versions: []apiKubernetesVersion{
				{ID: "v-1", Version: "1.35", Status: "current", IsDefault: true, EndOfLife: "2027-06-28"},
				{ID: "v-2", Version: "1.34", Status: "supported"},
			},
		})
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key", client.WithHTTPClient(server.Client()))
	ds := &kubernetesVersionsDataSource{client: c}

	readResp := datasource.ReadResponse{State: emptyState(t, ds)}
	ds.Read(context.Background(), datasource.ReadRequest{}, &readResp)
	if readResp.Diagnostics.HasError() {
		t.Fatalf("read failed: %v", readResp.Diagnostics.Errors())
	}

	var result kubernetesVersionsModel
	readResp.State.Get(context.Background(), &result)
	var items []kubernetesVersionItemModel
	if diags := result.Versions.ElementsAs(context.Background(), &items, false); diags.HasError() {
		t.Fatalf("failed to extract versions: %v", diags.Errors())
	}
	if len(items) != 2 {
		t.Fatalf("expected 2 versions, got %d", len(items))
	}
	if items[0].Version.ValueString() != "1.35" || !items[0].IsDefault.ValueBool() {
		t.Errorf("unexpected first version: %+v", items[0])
	}
	if !items[1].EndOfLife.IsNull() {
		t.Error("expected null end_of_life for version without one")
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
	ds := &kubernetesVersionsDataSource{client: c}

	readResp := datasource.ReadResponse{State: emptyState(t, ds)}
	ds.Read(context.Background(), datasource.ReadRequest{}, &readResp)
	if !readResp.Diagnostics.HasError() {
		t.Error("expected error for API failure")
	}
}
