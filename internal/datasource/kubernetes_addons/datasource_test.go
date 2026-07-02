package kubernetes_addons

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
	if resp.TypeName != "frostmoln_kubernetes_addons" {
		t.Errorf("expected type name frostmoln_kubernetes_addons, got %s", resp.TypeName)
	}
}

func TestSchema(t *testing.T) {
	ds := NewDataSource()
	var resp datasource.SchemaResponse
	ds.Schema(context.Background(), datasource.SchemaRequest{}, &resp)
	if _, ok := resp.Schema.Attributes["addons"]; !ok {
		t.Error("expected addons attribute in schema")
	}
}

func TestConfigureNilProviderData(t *testing.T) {
	ds := &kubernetesAddonsDataSource{}
	var resp datasource.ConfigureResponse
	ds.Configure(context.Background(), datasource.ConfigureRequest{}, &resp)
	if resp.Diagnostics.HasError() {
		t.Errorf("expected no errors for nil provider data, got %v", resp.Diagnostics.Errors())
	}
}

func TestConfigureWrongType(t *testing.T) {
	ds := &kubernetesAddonsDataSource{}
	var resp datasource.ConfigureResponse
	ds.Configure(context.Background(), datasource.ConfigureRequest{ProviderData: "not-a-client"}, &resp)
	if !resp.Diagnostics.HasError() {
		t.Error("expected error for wrong provider data type")
	}
}

func emptyState(t *testing.T, ds *kubernetesAddonsDataSource) tfsdk.State {
	t.Helper()
	var schemaResp datasource.SchemaResponse
	ds.Schema(context.Background(), datasource.SchemaRequest{}, &schemaResp)
	stateVal := tftypes.NewValue(schemaResp.Schema.Type().TerraformType(context.Background()), nil)
	return tfsdk.State{Schema: schemaResp.Schema, Raw: stateVal}
}

func TestRead(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/kubernetes/addons" {
			t.Errorf("unexpected path: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(apiKubernetesAddonList{
			Addons: []apiKubernetesAddon{
				{Key: "external-secrets", Name: "External Secrets Operator", Description: "Sync secrets from Frostmoln Secrets.", IsDefault: true, Disabled: false},
				{Key: "cert-manager", Name: "cert-manager", Description: "Automated TLS certificates.", IsDefault: false, Disabled: true},
			},
		})
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key", client.WithHTTPClient(server.Client()))
	ds := &kubernetesAddonsDataSource{client: c}

	readResp := datasource.ReadResponse{State: emptyState(t, ds)}
	ds.Read(context.Background(), datasource.ReadRequest{}, &readResp)
	if readResp.Diagnostics.HasError() {
		t.Fatalf("read failed: %v", readResp.Diagnostics.Errors())
	}

	var result kubernetesAddonsModel
	readResp.State.Get(context.Background(), &result)
	var items []kubernetesAddonItemModel
	if diags := result.Addons.ElementsAs(context.Background(), &items, false); diags.HasError() {
		t.Fatalf("failed to extract addons: %v", diags.Errors())
	}
	if len(items) != 2 {
		t.Fatalf("expected 2 addons, got %d", len(items))
	}
	if items[0].Key.ValueString() != "external-secrets" || !items[0].IsDefault.ValueBool() {
		t.Errorf("unexpected first addon: %+v", items[0])
	}
	if items[0].Disabled.ValueBool() {
		t.Error("expected external-secrets to be enabled (disabled=false)")
	}
	if items[1].Key.ValueString() != "cert-manager" || !items[1].Disabled.ValueBool() {
		t.Errorf("expected cert-manager disabled, got %+v", items[1])
	}
	if items[1].Description.ValueString() != "Automated TLS certificates." {
		t.Errorf("unexpected description: %q", items[1].Description.ValueString())
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
	ds := &kubernetesAddonsDataSource{client: c}

	readResp := datasource.ReadResponse{State: emptyState(t, ds)}
	ds.Read(context.Background(), datasource.ReadRequest{}, &readResp)
	if !readResp.Diagnostics.HasError() {
		t.Error("expected error for API failure")
	}
}
