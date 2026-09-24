package kubernetes_cluster_addons

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
	if resp.TypeName != "frostmoln_kubernetes_cluster_addons" {
		t.Errorf("type name = %s", resp.TypeName)
	}
}

// FIX 5: `cluster_id` is a path segment; "." and ".." would path-join to another route.
func TestClusterIDValidator(t *testing.T) {
	ctx := context.Background()
	var sr datasource.SchemaResponse
	NewDataSource().Schema(ctx, datasource.SchemaRequest{}, &sr)
	vs := sr.Schema.Attributes["cluster_id"].(schema.StringAttribute).Validators
	for value, wantErr := range map[string]bool{
		"c-1": false, "3f2504e0-4f89-41d3-9a0c-0305e82c3301": false, ".": true, "..": true, "": true,
	} {
		var resp validator.StringResponse
		for _, v := range vs {
			v.ValidateString(ctx, validator.StringRequest{Path: path.Root("cluster_id"), ConfigValue: types.StringValue(value)}, &resp)
		}
		if resp.Diagnostics.HasError() != wantErr {
			t.Errorf("cluster_id %q: error = %v, want %v", value, resp.Diagnostics.HasError(), wantErr)
		}
	}
}

func read(t *testing.T, handler http.HandlerFunc) (datasource.ReadResponse, clusterAddonsModel) {
	t.Helper()
	server := httptest.NewServer(handler)
	defer server.Close()
	c := client.NewClient(server.URL, "test-key", client.WithHTTPClient(server.Client()))
	c.SetTenantIDForTest("t-1")
	ds := &clusterAddonsDataSource{client: c}

	ctx := context.Background()
	var schemaResp datasource.SchemaResponse
	ds.Schema(ctx, datasource.SchemaRequest{}, &schemaResp)
	cfg := tfsdk.State{Schema: schemaResp.Schema}
	if diags := cfg.Set(ctx, &clusterAddonsModel{
		ClusterID: types.StringValue("c-1"),
		Addons:    types.ListNull(types.ObjectType{AttrTypes: addonStateAttrTypes}),
	}); diags.HasError() {
		t.Fatalf("config: %v", diags.Errors())
	}
	resp := datasource.ReadResponse{State: tfsdk.State{
		Schema: schemaResp.Schema,
		Raw:    tftypes.NewValue(schemaResp.Schema.Type().TerraformType(ctx), nil),
	}}
	ds.Read(ctx, datasource.ReadRequest{Config: tfsdk.Config{Schema: schemaResp.Schema, Raw: cfg.Raw}}, &resp)
	var m clusterAddonsModel
	if !resp.Diagnostics.HasError() {
		resp.State.Get(ctx, &m)
	}
	return resp, m
}

// TRAP 1 + TRAP 4: platform order (selection first, then owed removals — not sorted) and
// open vocabularies stored verbatim, including values no enum knows.
func TestRead_PlatformOrderAndOpenVocabulary(t *testing.T) {
	want := []apiClusterAddonState{
		{Key: "external-secrets", PinnedVersion: "v0.22.0-1", AppliedVersion: "v0.22.0", State: "converging", PinnedStatus: "deprecated"},
		{Key: "cert-manager", State: "unsupported"},
		{Key: "external-dns", PinnedVersion: "weird", AppliedVersion: "v0.21.0", State: "hibernating-from-the-future", PinnedStatus: "lts-from-the-future"},
	}
	resp, m := read(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/tenants/t-1/kubernetes-clusters/c-1/addons" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(apiClusterAddonStateList{Addons: want})
	})
	if resp.Diagnostics.HasError() {
		t.Fatalf("read failed: %v", resp.Diagnostics.Errors())
	}
	var got []clusterAddonItemModel
	if diags := m.Addons.ElementsAs(context.Background(), &got, false); diags.HasError() {
		t.Fatalf("elements: %v", diags.Errors())
	}
	if len(got) != len(want) {
		t.Fatalf("got %d entries, want %d", len(got), len(want))
	}
	for i, w := range want {
		g := got[i]
		if g.Key.ValueString() != w.Key || g.PinnedVersion.ValueString() != w.PinnedVersion ||
			g.AppliedVersion.ValueString() != w.AppliedVersion || g.State.ValueString() != w.State ||
			g.PinnedStatus.ValueString() != w.PinnedStatus {
			t.Errorf("addons[%d] = %+v, want %+v verbatim", i, g, w)
		}
	}
}

// TRAP 5: 409 (mid-create), 501 (deployment) and 503 (unreachable) mean
// "no version data": a warning and a null list — never an error, never a guess.
func TestRead_NoVersionDataIsWarningNotError(t *testing.T) {
	for _, status := range []int{http.StatusConflict, http.StatusNotImplemented, http.StatusServiceUnavailable} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			resp, m := read(t, func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(status)
				_ = json.NewEncoder(w).Encode(map[string]any{"code": "x", "message": "server says why"})
			})
			if resp.Diagnostics.HasError() {
				t.Fatalf("status %d must not be an error: %v", status, resp.Diagnostics.Errors())
			}
			if resp.Diagnostics.WarningsCount() == 0 {
				t.Errorf("status %d: expected a warning", status)
			}
			if !m.Addons.IsNull() {
				t.Errorf("status %d: addons must be null (no version data), got %v", status, m.Addons)
			}
		})
	}
}

func TestRead_NotFoundIsError(t *testing.T) {
	resp, _ := read(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]any{"code": "not_found", "message": "no such cluster"})
	})
	if !resp.Diagnostics.HasError() {
		t.Error("expected an error for an unknown cluster")
	}
}
