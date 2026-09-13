package security_group

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
)

// TestReadEmptyTagsRoundTrips: `tags = {}` must read back as {}, not null.
// network omits an empty tag map, and fromAPI answered every untagged read with
// null, so a config saying {} failed its apply with an inconsistent result and
// could never converge.
func TestReadEmptyTagsRoundTrips(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/tenants/t-1/security-groups/sg-1" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(apiSecurityGroup{
			ID: "sg-1", Name: "web-sg", VPCID: "vpc-abc", CreatedAt: "2025-06-01T12:00:00Z",
		})
	}))
	defer srv.Close()

	c := client.NewClient(srv.URL, "test-key", client.WithHTTPClient(srv.Client())) // pragma: allowlist secret
	c.SetTenantIDForTest("t-1")
	r := configuredSGResource(t, c)

	state := tfsdk.State{Schema: sgSchema(t)}
	if d := state.Set(context.Background(), &SecurityGroupModel{
		ID: types.StringValue("sg-1"), Name: types.StringValue("web-sg"), Description: types.StringNull(),
		VPCID: types.StringValue("vpc-abc"), Tags: types.MapValueMust(types.StringType, map[string]attr.Value{}),
		IsDefault: types.BoolValue(false), DeleteDefaultEgress: types.BoolValue(false),
		CreatedAt: types.StringValue("2025-06-01T12:00:00Z"),
	}); d.HasError() {
		t.Fatalf("fixture: %v", d)
	}

	resp := resource.ReadResponse{State: state}
	r.Read(context.Background(), resource.ReadRequest{State: state}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("read failed: %v", resp.Diagnostics.Errors())
	}

	var got SecurityGroupModel
	resp.State.Get(context.Background(), &got)
	if got.Tags.IsNull() || len(got.Tags.Elements()) != 0 {
		t.Errorf("`tags = {}` read back as %#v, want an empty non-null map", got.Tags)
	}
}

// TestReadFiltersPlatformTags: platform-owned `frostmoln_*` tags must never reach state. network
// refuses them on every customer write (nlmeta.IsReservedTagKey), so no config
// can converge on one: copying it into state is a permanent diff. Filtering
// runs before the empty/non-empty decision, so a read carrying only platform
// keys is "no tags".
func TestReadFiltersPlatformTags(t *testing.T) {
	empty := types.MapValueMust(types.StringType, map[string]attr.Value{})
	for _, tc := range []struct {
		name    string
		api     map[string]string
		prior   types.Map
		want    map[string]string
		wantNil bool
	}{
		{"customer and platform keys", map[string]string{"k": "v", "frostmoln_managed_by": "cluster"}, types.MapNull(types.StringType), map[string]string{"k": "v"}, false},
		{"only platform keys, no tags configured", map[string]string{"frostmoln_managed_by": "cluster"}, types.MapNull(types.StringType), nil, true},
		{"only platform keys, tags = {}", map[string]string{"frostmoln_managed_by": "cluster"}, empty, map[string]string{}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_ = json.NewEncoder(w).Encode(apiSecurityGroup{
					ID: "sg-1", Name: "web-sg", VPCID: "vpc-abc", CreatedAt: "2025-06-01T12:00:00Z", Tags: tc.api,
				})
			}))
			defer srv.Close()

			c := client.NewClient(srv.URL, "test-key", client.WithHTTPClient(srv.Client())) // pragma: allowlist secret
			c.SetTenantIDForTest("t-1")
			r := configuredSGResource(t, c)

			state := tfsdk.State{Schema: sgSchema(t)}
			if d := state.Set(context.Background(), &SecurityGroupModel{
				ID: types.StringValue("sg-1"), Name: types.StringValue("web-sg"), Description: types.StringNull(),
				VPCID: types.StringValue("vpc-abc"), Tags: tc.prior,
				IsDefault: types.BoolValue(false), DeleteDefaultEgress: types.BoolValue(false),
				CreatedAt: types.StringValue("2025-06-01T12:00:00Z"),
			}); d.HasError() {
				t.Fatalf("fixture: %v", d)
			}
			resp := resource.ReadResponse{State: state}
			r.Read(context.Background(), resource.ReadRequest{State: state}, &resp)
			if resp.Diagnostics.HasError() {
				t.Fatalf("read failed: %v", resp.Diagnostics.Errors())
			}

			var got SecurityGroupModel
			resp.State.Get(context.Background(), &got)
			if tc.wantNil {
				if !got.Tags.IsNull() {
					t.Errorf("state tags = %v, want null", got.Tags)
				}
				return
			}
			if got.Tags.IsNull() {
				t.Fatalf("state tags = null, want %v", tc.want)
			}
			gotMap := map[string]string{}
			got.Tags.ElementsAs(context.Background(), &gotMap, false)
			if len(gotMap) != len(tc.want) {
				t.Fatalf("state tags = %v, want %v", gotMap, tc.want)
			}
			for k, v := range tc.want {
				if gotMap[k] != v {
					t.Errorf("state tags[%q] = %q, want %q", k, gotMap[k], v)
				}
			}
		})
	}
}
