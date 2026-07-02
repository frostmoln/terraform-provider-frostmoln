package kubernetes_flavors

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
	if resp.TypeName != "frostmoln_kubernetes_flavors" {
		t.Errorf("expected type name frostmoln_kubernetes_flavors, got %s", resp.TypeName)
	}
}

func TestSchema(t *testing.T) {
	ds := NewDataSource()
	var resp datasource.SchemaResponse
	ds.Schema(context.Background(), datasource.SchemaRequest{}, &resp)
	if _, ok := resp.Schema.Attributes["flavors"]; !ok {
		t.Error("expected flavors attribute in schema")
	}
}

func TestConfigureNilProviderData(t *testing.T) {
	ds := &kubernetesFlavorsDataSource{}
	var resp datasource.ConfigureResponse
	ds.Configure(context.Background(), datasource.ConfigureRequest{}, &resp)
	if resp.Diagnostics.HasError() {
		t.Errorf("expected no errors for nil provider data, got %v", resp.Diagnostics.Errors())
	}
}

func TestConfigureWrongType(t *testing.T) {
	ds := &kubernetesFlavorsDataSource{}
	var resp datasource.ConfigureResponse
	ds.Configure(context.Background(), datasource.ConfigureRequest{ProviderData: "not-a-client"}, &resp)
	if !resp.Diagnostics.HasError() {
		t.Error("expected error for wrong provider data type")
	}
}

func emptyState(t *testing.T, ds *kubernetesFlavorsDataSource) tfsdk.State {
	t.Helper()
	var schemaResp datasource.SchemaResponse
	ds.Schema(context.Background(), datasource.SchemaRequest{}, &schemaResp)
	stateVal := tftypes.NewValue(schemaResp.Schema.Type().TerraformType(context.Background()), nil)
	return tfsdk.State{Schema: schemaResp.Schema, Raw: stateVal}
}

func TestRead(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/kubernetes/flavors" {
			t.Errorf("unexpected path: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(apiKubernetesFlavorList{
			Flavors: []apiKubernetesFlavor{
				{ID: "k8s.gp1.small", Name: "K8s GP1 Small", Family: "general-purpose", VCPUs: 2, RAMMB: 4096, DiskGB: 40, PricingTier: "k8s-gp1-small"},
				{ID: "k8s.gp1.medium", Name: "K8s GP1 Medium", Family: "general-purpose", VCPUs: 4, RAMMB: 8192, DiskGB: 80},
			},
		})
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key", client.WithHTTPClient(server.Client()))
	ds := &kubernetesFlavorsDataSource{client: c}

	readResp := datasource.ReadResponse{State: emptyState(t, ds)}
	ds.Read(context.Background(), datasource.ReadRequest{}, &readResp)
	if readResp.Diagnostics.HasError() {
		t.Fatalf("read failed: %v", readResp.Diagnostics.Errors())
	}

	var result kubernetesFlavorsModel
	readResp.State.Get(context.Background(), &result)
	var items []kubernetesFlavorItemModel
	if diags := result.Flavors.ElementsAs(context.Background(), &items, false); diags.HasError() {
		t.Fatalf("failed to extract flavors: %v", diags.Errors())
	}
	if len(items) != 2 {
		t.Fatalf("expected 2 flavors, got %d", len(items))
	}
	if items[0].ID.ValueString() != "k8s.gp1.small" || items[0].VCPUs.ValueInt64() != 2 {
		t.Errorf("unexpected first flavor: %+v", items[0])
	}
	if items[0].PricingTier.ValueString() != "k8s-gp1-small" {
		t.Errorf("expected pricing_tier k8s-gp1-small, got %s", items[0].PricingTier.ValueString())
	}
	if !items[1].PricingTier.IsNull() {
		t.Error("expected null pricing_tier for flavor without one")
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
	ds := &kubernetesFlavorsDataSource{client: c}

	readResp := datasource.ReadResponse{State: emptyState(t, ds)}
	ds.Read(context.Background(), datasource.ReadRequest{}, &readResp)
	if !readResp.Diagnostics.HasError() {
		t.Error("expected error for API failure")
	}
}
