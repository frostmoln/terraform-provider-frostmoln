package appgw_versions

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
)

const versionsBody = `{"versions":[
  {"id":"v1","engine":"haproxy","version":"1.0.0","status":"current","isDefault":true,
   "crsVersionDefault":"4.7.0","launchable":true},
  {"id":"v0","engine":"haproxy","version":"0.9.0","status":"eol","isDefault":false,
   "crsVersionDefault":"4.5.0","launchable":false,"endOfLife":"2026-01-01T00:00:00Z"}
],"totalCount":2}`

func read(t *testing.T, m VersionsModel) VersionsModel {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/application-gateways/versions" {
			t.Errorf("unexpected path %s -- the catalog is NOT tenant-scoped", r.URL.Path)
		}
		_, _ = w.Write([]byte(versionsBody))
	}))
	t.Cleanup(srv.Close)
	c := client.NewClient(srv.URL, "k", client.WithHTTPClient(srv.Client())) // pragma: allowlist secret
	c.SetTenantIDForTest("t-1")

	d := NewDataSource().(*versionsDataSource)
	d.client = c
	var sr datasource.SchemaResponse
	d.Schema(context.Background(), datasource.SchemaRequest{}, &sr)

	st := tfsdk.State{Schema: sr.Schema}
	if diags := st.Set(context.Background(), &m); diags.HasError() {
		t.Fatalf("config: %v", diags.Errors())
	}
	resp := datasource.ReadResponse{State: tfsdk.State{Schema: sr.Schema}}
	d.Read(context.Background(), datasource.ReadRequest{
		Config: tfsdk.Config{Schema: sr.Schema, Raw: st.Raw}}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("read: %v", resp.Diagnostics.Errors())
	}
	var out VersionsModel
	resp.State.Get(context.Background(), &out)
	return out
}

// TestDefaultIsReportedEvenWhenFiltered. The default is what a gateway gets
// when none is named, so it must be knowable regardless of the filter — and it
// is read from `isDefault` rather than guessed from the list's order.
func TestDefaultIsReportedEvenWhenFiltered(t *testing.T) {
	all := read(t, VersionsModel{})
	if all.Default.ValueString() != "1.0.0" {
		t.Fatalf("default = %q, want 1.0.0", all.Default.ValueString())
	}
	if len(all.Versions) != 2 {
		t.Fatalf("unfiltered read returned %d versions, want 2", len(all.Versions))
	}

	launchable := read(t, VersionsModel{LaunchableOnly: types.BoolValue(true)})
	if launchable.Default.ValueString() != "1.0.0" {
		t.Errorf("default = %q with launchable_only, want 1.0.0", launchable.Default.ValueString())
	}
	if len(launchable.Versions) != 1 || launchable.Versions[0].Version.ValueString() != "1.0.0" {
		t.Fatalf("launchable_only returned %+v, want only the current version", launchable.Versions)
	}
}

// TestLaunchableIsTakenFromTheServer. Re-deriving it from `status` is what the
// schema tells practitioners not to do, so the data source must not do it
// either: the eol row is not launchable and the current one is, and both come
// from the server's own field.
func TestLaunchableIsTakenFromTheServer(t *testing.T) {
	out := read(t, VersionsModel{})
	byVersion := map[string]VersionModel{}
	for _, v := range out.Versions {
		byVersion[v.Version.ValueString()] = v
	}
	if !byVersion["1.0.0"].Launchable.ValueBool() {
		t.Error("1.0.0 must be launchable")
	}
	if byVersion["0.9.0"].Launchable.ValueBool() {
		t.Error("0.9.0 is eol and must not be launchable")
	}
	if byVersion["0.9.0"].EndOfLife.ValueString() == "" {
		t.Error("an end-of-life date was dropped")
	}
	if byVersion["1.0.0"].ManagedRulesetVersion.ValueString() != "4.7.0" {
		t.Error("the managed ruleset version was dropped")
	}
}
