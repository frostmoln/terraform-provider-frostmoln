package kubernetes_tiers

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
	if resp.TypeName != "frostmoln_kubernetes_tiers" {
		t.Errorf("expected type name frostmoln_kubernetes_tiers, got %s", resp.TypeName)
	}
}

func TestSchema(t *testing.T) {
	ds := NewDataSource()
	var resp datasource.SchemaResponse
	ds.Schema(context.Background(), datasource.SchemaRequest{}, &resp)
	if _, ok := resp.Schema.Attributes["tiers"]; !ok {
		t.Error("expected tiers attribute in schema")
	}
}

func TestConfigureNilProviderData(t *testing.T) {
	ds := &kubernetesTiersDataSource{}
	var resp datasource.ConfigureResponse
	ds.Configure(context.Background(), datasource.ConfigureRequest{}, &resp)
	if resp.Diagnostics.HasError() {
		t.Errorf("expected no errors for nil provider data, got %v", resp.Diagnostics.Errors())
	}
}

func TestConfigureWrongType(t *testing.T) {
	ds := &kubernetesTiersDataSource{}
	var resp datasource.ConfigureResponse
	ds.Configure(context.Background(), datasource.ConfigureRequest{ProviderData: "not-a-client"}, &resp)
	if !resp.Diagnostics.HasError() {
		t.Error("expected error for wrong provider data type")
	}
}

func emptyState(t *testing.T, ds *kubernetesTiersDataSource) tfsdk.State {
	t.Helper()
	var schemaResp datasource.SchemaResponse
	ds.Schema(context.Background(), datasource.SchemaRequest{}, &schemaResp)
	stateVal := tftypes.NewValue(schemaResp.Schema.Type().TerraformType(context.Background()), nil)
	return tfsdk.State{Schema: schemaResp.Schema, Raw: stateVal}
}

func TestRead(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/kubernetes/tiers" {
			t.Errorf("unexpected path: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(apiKubernetesTierList{
			Tiers: []apiKubernetesTier{
				{Key: "development", Name: "Development", ControlPlaneNodes: 1, HAEnabled: false, IsDefault: true},
				{Key: "standard", Name: "Standard", ControlPlaneNodes: 3, HAEnabled: true},
			},
		})
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key", client.WithHTTPClient(server.Client()))
	ds := &kubernetesTiersDataSource{client: c}

	readResp := datasource.ReadResponse{State: emptyState(t, ds)}
	ds.Read(context.Background(), datasource.ReadRequest{}, &readResp)
	if readResp.Diagnostics.HasError() {
		t.Fatalf("read failed: %v", readResp.Diagnostics.Errors())
	}

	var result kubernetesTiersModel
	readResp.State.Get(context.Background(), &result)
	var items []kubernetesTierItemModel
	if diags := result.Tiers.ElementsAs(context.Background(), &items, false); diags.HasError() {
		t.Fatalf("failed to extract tiers: %v", diags.Errors())
	}
	if len(items) != 2 {
		t.Fatalf("expected 2 tiers, got %d", len(items))
	}
	if items[0].Key.ValueString() != "development" || !items[0].IsDefault.ValueBool() {
		t.Errorf("unexpected first tier: %+v", items[0])
	}
	if items[1].ControlPlaneNodes.ValueInt64() != 3 || !items[1].HAEnabled.ValueBool() {
		t.Errorf("unexpected second tier: %+v", items[1])
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
	ds := &kubernetesTiersDataSource{client: c}

	readResp := datasource.ReadResponse{State: emptyState(t, ds)}
	ds.Read(context.Background(), datasource.ReadRequest{}, &readResp)
	if !readResp.Diagnostics.HasError() {
		t.Error("expected error for API failure")
	}
}
