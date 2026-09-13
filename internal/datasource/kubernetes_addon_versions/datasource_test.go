package kubernetes_addon_versions

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
)

func TestMetadata(t *testing.T) {
	var resp datasource.MetadataResponse
	NewDataSource().Metadata(context.Background(), datasource.MetadataRequest{ProviderTypeName: "frostmoln"}, &resp)
	if resp.TypeName != "frostmoln_kubernetes_addon_versions" {
		t.Errorf("type name = %s", resp.TypeName)
	}
}

// FIX 5: `addon` is a path segment. ".." once path-joined to /kubernetes/versions and
// listed Kubernetes versions as if they were an addon's.
func TestAddonValidator(t *testing.T) {
	ctx := context.Background()
	var sr datasource.SchemaResponse
	NewDataSource().Schema(ctx, datasource.SchemaRequest{}, &sr)
	vs := sr.Schema.Attributes["addon"].(schema.StringAttribute).Validators
	for value, wantErr := range map[string]bool{
		"external-dns": false, "..": true, ".": true, "a/b": true, "External": true, "": true,
		strings.Repeat("a", 65): true,
	} {
		var resp validator.StringResponse
		for _, v := range vs {
			v.ValidateString(ctx, validator.StringRequest{Path: path.Root("addon"), ConfigValue: types.StringValue(value)}, &resp)
		}
		if resp.Diagnostics.HasError() != wantErr {
			t.Errorf("addon %q: error = %v, want %v", value, resp.Diagnostics.HasError(), wantErr)
		}
	}
}

func read(t *testing.T, handler http.HandlerFunc) datasource.ReadResponse {
	t.Helper()
	server := httptest.NewServer(handler)
	defer server.Close()
	ds := &addonVersionsDataSource{client: client.NewClient(server.URL, "test-key", client.WithHTTPClient(server.Client()))}

	ctx := context.Background()
	var schemaResp datasource.SchemaResponse
	ds.Schema(ctx, datasource.SchemaRequest{}, &schemaResp)
	cfg := tfsdk.State{Schema: schemaResp.Schema}
	if diags := cfg.Set(ctx, &addonVersionsModel{
		Addon:    types.StringValue("external-dns"),
		Versions: types.ListNull(types.ObjectType{AttrTypes: versionAttrTypes}),
	}); diags.HasError() {
		t.Fatalf("config: %v", diags.Errors())
	}
	resp := datasource.ReadResponse{State: tfsdk.State{
		Schema: schemaResp.Schema,
		Raw:    tftypes.NewValue(schemaResp.Schema.Type().TerraformType(ctx), nil),
	}}
	ds.Read(ctx, datasource.ReadRequest{Config: tfsdk.Config{Schema: schemaResp.Schema, Raw: cfg.Raw}}, &resp)
	return resp
}

// TRAP 1: the server order is the contract. This order is one every sort would change
// (semver ranks v0.22.0-1 below v0.22.0; lexical puts "weird" last anyway but moves
// v0.21.0 first).
func TestRead_PreservesServerOrder(t *testing.T) {
	want := []apiAddonVersion{
		{Version: "v0.22.0-1", Status: "current", IsDefault: true},
		{Version: "v0.21.0", Status: "deprecated"},
		{Version: "v0.22.0", Status: "supported"},
		{Version: "weird", Status: "a-status-from-the-future"},
	}
	resp := read(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/kubernetes/addons/external-dns/versions" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(apiAddonVersionList{Versions: want})
	})
	if resp.Diagnostics.HasError() {
		t.Fatalf("read failed: %v", resp.Diagnostics.Errors())
	}
	var m addonVersionsModel
	resp.State.Get(context.Background(), &m)
	var got []addonVersionItemModel
	if diags := m.Versions.ElementsAs(context.Background(), &got, false); diags.HasError() {
		t.Fatalf("elements: %v", diags.Errors())
	}
	if len(got) != len(want) {
		t.Fatalf("got %d versions, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i].Version.ValueString() != want[i].Version || got[i].Status.ValueString() != want[i].Status ||
			got[i].IsDefault.ValueBool() != want[i].IsDefault {
			t.Errorf("versions[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestRead_NotFoundIsError(t *testing.T) {
	resp := read(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]any{"code": "not_found", "message": "no such addon"})
	})
	if !resp.Diagnostics.HasError() {
		t.Error("expected an error for an unknown addon")
	}
}
