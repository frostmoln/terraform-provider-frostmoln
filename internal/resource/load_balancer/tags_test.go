package load_balancer

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
)

func tagTestLBModel() LoadBalancerModel {
	return LoadBalancerModel{
		ID: types.StringValue("lb-1"), Name: types.StringValue("lb"), VPCID: types.StringValue("vpc-1"),
		SubnetID: types.StringValue("subnet-1"), Description: types.StringNull(), VIPAddress: types.StringValue("10.0.0.7"),
		Scheme: types.StringValue("internal"), PublicIPID: types.StringNull(), PublicIPAddress: types.StringNull(),
		Type: types.StringValue("l7"), FlavorID: types.StringNull(), Tags: types.MapNull(types.StringType),
		VIPPortID: types.StringValue("port-1"), Status: types.StringValue("active"),
		ProvisioningStatus: types.StringValue("ACTIVE"), OperatingStatus: types.StringValue("ONLINE"),
		CreatedAt: types.StringValue("2025-01-01T00:00:00Z"), UpdatedAt: types.StringNull(),
		TagsAll: types.MapNull(types.StringType),
	}
}

func tagTestLBWire(tags map[string]string) apiLoadBalancer {
	return apiLoadBalancer{
		ID: "lb-1", Name: "lb", VPCID: "vpc-1", SubnetID: "subnet-1", VIPAddress: "10.0.0.7", VIPPortID: "port-1",
		Scheme: "internal", Type: "l7", Status: "active", ProvisioningStatus: "ACTIVE", OperatingStatus: "ONLINE",
		CreatedAt: "2025-01-01T00:00:00Z", Tags: tags,
	}
}

func tagTestLBState(t *testing.T, m LoadBalancerModel) tfsdk.State {
	t.Helper()
	s := tfsdk.State{Schema: getSchema(t).Schema}
	if d := s.Set(context.Background(), &m); d.HasError() {
		t.Fatalf("fixture: %v", d)
	}
	return s
}

func tagTestLBPlan(t *testing.T, m LoadBalancerModel) tfsdk.Plan {
	t.Helper()
	p := tfsdk.Plan{Schema: getSchema(t).Schema}
	if d := p.Set(context.Background(), &m); d.HasError() {
		t.Fatalf("fixture: %v", d)
	}
	return p
}

// TestUpdateClearsTags: removing every tag must reach network as `"tags": {}`.
//
// The bare LB PUT is served by network, which acts on tags only when the field
// is non-nil and then merges: the incoming map replaces the customer's tags
// while platform-owned keys are carried over (network/internal/service/impl/
// load_balancer_service.go, Update → nlmeta.MergePlatformOwnedTags). The
// provider sent nothing for a removed block and an `omitempty`-dropped {} for
// an emptied one, so the old tags survived and the apply failed with an
// inconsistent result.
//
// The fake replaces only on a non-null `tags`, network's own condition.
func TestUpdateClearsTags(t *testing.T) {
	for _, tc := range []struct {
		name string
		plan types.Map
	}{
		{"tags block removed", types.MapNull(types.StringType)},
		{"tags set to empty map", types.MapValueMust(types.StringType, map[string]attr.Value{})},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stored := map[string]string{"a": "b"}
			var put, hadTags, tagsNull bool
			var sent map[string]string

			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				// The update reads the current tags before it writes them.
				if r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-1/load-balancers/lb-1" {
					_ = json.NewEncoder(w).Encode(tagTestLBWire(stored))
					return
				}
				if r.Method != http.MethodPut || r.URL.Path != "/v1/tenants/t-1/load-balancers/lb-1" {
					t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
					w.WriteHeader(http.StatusNotFound)
					return
				}
				put = true
				raw, _ := io.ReadAll(r.Body)
				var env map[string]json.RawMessage
				if err := json.Unmarshal(raw, &env); err != nil {
					t.Errorf("PUT body is not a JSON object: %s", raw)
				}
				field, ok := env["tags"]
				hadTags = ok
				tagsNull = ok && string(field) == "null"
				if ok && !tagsNull {
					sent = map[string]string{}
					_ = json.Unmarshal(field, &sent)
					stored = sent
				}
				_ = json.NewEncoder(w).Encode(tagTestLBWire(stored))
			}))
			defer srv.Close()

			c := client.NewClient(srv.URL, "test-key", client.WithHTTPClient(srv.Client())) // pragma: allowlist secret
			c.SetTenantIDForTest("t-1")
			r := newTestResource(c)

			stateModel := tagTestLBModel()
			stateModel.Tags = types.MapValueMust(types.StringType, map[string]attr.Value{"a": types.StringValue("b")})
			planModel := tagTestLBModel()
			planModel.Tags = tc.plan

			state := tagTestLBState(t, stateModel)
			resp := resource.UpdateResponse{State: state}
			r.Update(context.Background(), resource.UpdateRequest{Plan: tagTestLBPlan(t, planModel), State: state}, &resp)
			if resp.Diagnostics.HasError() {
				t.Fatalf("update failed: %v", resp.Diagnostics.Errors())
			}

			if !put {
				t.Fatal("no PUT was sent")
			}
			if !hadTags || tagsNull {
				t.Fatalf("PUT body carried no non-null `tags` (present=%v, null=%v); network keeps the old tags", hadTags, tagsNull)
			}
			if len(sent) != 0 {
				t.Errorf("PUT tags = %v, want {}", sent)
			}

			var got LoadBalancerModel
			resp.State.Get(context.Background(), &got)
			if !got.Tags.Equal(tc.plan) {
				t.Errorf("state tags = %v after update, want the planned %v (inconsistent result after apply)", got.Tags, tc.plan)
			}
		})
	}
}

// TestReadEmptyTagsRoundTrips: `tags = {}` must read back as {}, not null.
// network omits an empty tag map, and fromAPI answered every untagged read
// with null, so a config saying {} could never converge.
func TestReadEmptyTagsRoundTrips(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(tagTestLBWire(nil))
	}))
	defer srv.Close()

	c := client.NewClient(srv.URL, "test-key", client.WithHTTPClient(srv.Client())) // pragma: allowlist secret
	c.SetTenantIDForTest("t-1")
	r := newTestResource(c)

	m := tagTestLBModel()
	m.Tags = types.MapValueMust(types.StringType, map[string]attr.Value{})
	state := tagTestLBState(t, m)
	resp := resource.ReadResponse{State: state}
	r.Read(context.Background(), resource.ReadRequest{State: state}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("read failed: %v", resp.Diagnostics.Errors())
	}

	var got LoadBalancerModel
	resp.State.Get(context.Background(), &got)
	if got.Tags.IsNull() {
		t.Error("`tags = {}` read back as null; the next apply reports an inconsistent result")
	}
}

// TestUpdateWithoutTagChangeSendsNoTags: a rename or description edit must not
// put a `tags` key on the wire at all. network acts on any non-nil tags map, so
// sending the planned map (or {}) on every update turns an unrelated edit into
// a tag write — and a tags clear whenever the config has no `tags` block.
func TestUpdateWithoutTagChangeSendsNoTags(t *testing.T) {
	for _, tc := range []struct {
		name string
		tags types.Map
	}{
		{"untagged", types.MapNull(types.StringType)},
		{"tagged", types.MapValueMust(types.StringType, map[string]attr.Value{"a": types.StringValue("b")})},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var put, hadTags bool
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				// The update reads the current tags before it writes them: the
				// state's, since nothing changed them.
				if r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-1/load-balancers/lb-1" {
					cur := tagTestLBWire(nil)
					if !tc.tags.IsNull() {
						cur.Tags = map[string]string{"a": "b"}
					}
					_ = json.NewEncoder(w).Encode(cur)
					return
				}
				if r.Method != http.MethodPut || r.URL.Path != "/v1/tenants/t-1/load-balancers/lb-1" {
					t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
					w.WriteHeader(http.StatusNotFound)
					return
				}
				put = true
				raw, _ := io.ReadAll(r.Body)
				var env map[string]json.RawMessage
				if err := json.Unmarshal(raw, &env); err != nil {
					t.Errorf("PUT body is not a JSON object: %s", raw)
				}
				_, hadTags = env["tags"]
				out := tagTestLBWire(nil)
				out.Name = "renamed"
				if !tc.tags.IsNull() {
					out.Tags = map[string]string{"a": "b"}
				}
				_ = json.NewEncoder(w).Encode(out)
			}))
			defer srv.Close()

			c := client.NewClient(srv.URL, "test-key", client.WithHTTPClient(srv.Client())) // pragma: allowlist secret
			c.SetTenantIDForTest("t-1")
			r := newTestResource(c)

			stateModel := tagTestLBModel()
			stateModel.Tags = tc.tags
			planModel := stateModel
			planModel.Name = types.StringValue("renamed")

			state := tagTestLBState(t, stateModel)
			resp := resource.UpdateResponse{State: state}
			r.Update(context.Background(), resource.UpdateRequest{Plan: tagTestLBPlan(t, planModel), State: state}, &resp)
			if resp.Diagnostics.HasError() {
				t.Fatalf("update failed: %v", resp.Diagnostics.Errors())
			}
			if !put {
				t.Fatal("no PUT was sent")
			}
			if hadTags {
				t.Error("a rename with unchanged tags put a `tags` key on the wire; network treats it as a tag write")
			}
		})
	}
}

// TestReadFiltersPlatformTags: platform-owned `frostmoln_*` tags (the ADR-0115
// cluster-owned pair, frostmoln_enclave_key, ...) must never reach state.
// network refuses them on every customer write and carries them across every
// tag update (nlmeta.MergePlatformOwnedTags), so no config can ever converge on
// them: copying them into state is a permanent diff.
func TestReadFiltersPlatformTags(t *testing.T) {
	empty := types.MapValueMust(types.StringType, map[string]attr.Value{})
	for _, tc := range []struct {
		name    string
		api     map[string]string
		prior   types.Map
		want    map[string]string
		wantNil bool
	}{
		{"customer and platform keys", map[string]string{"k": "v", "frostmoln_managed_by": "cluster"}, types.MapValueMust(types.StringType, map[string]attr.Value{"k": types.StringValue("v")}), map[string]string{"k": "v"}, false},
		// A key the configuration does not name lives in tags_all only.
		{"customer key not configured", map[string]string{"k": "v", "frostmoln_managed_by": "cluster"}, types.MapNull(types.StringType), nil, true},
		{"only platform keys, no tags configured", map[string]string{"frostmoln_managed_by": "cluster"}, types.MapNull(types.StringType), nil, true},
		{"only platform keys, tags = {}", map[string]string{"frostmoln_managed_by": "cluster"}, empty, map[string]string{}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_ = json.NewEncoder(w).Encode(tagTestLBWire(tc.api))
			}))
			defer srv.Close()

			c := client.NewClient(srv.URL, "test-key", client.WithHTTPClient(srv.Client())) // pragma: allowlist secret
			c.SetTenantIDForTest("t-1")
			r := newTestResource(c)

			m := tagTestLBModel()
			m.Tags = tc.prior
			state := tagTestLBState(t, m)
			resp := resource.ReadResponse{State: state}
			r.Read(context.Background(), resource.ReadRequest{State: state}, &resp)
			if resp.Diagnostics.HasError() {
				t.Fatalf("read failed: %v", resp.Diagnostics.Errors())
			}

			var got LoadBalancerModel
			resp.State.Get(context.Background(), &got)
			// tags_all is the whole filtered read-back: the customer key, never the
			// platform's.
			if _, leaked := got.TagsAll.Elements()["frostmoln_managed_by"]; leaked {
				t.Errorf("a platform-owned key reached tags_all: %v", got.TagsAll)
			}
			if _, has := tc.api["k"]; has {
				if _, kept := got.TagsAll.Elements()["k"]; !kept {
					t.Errorf("tags_all = %v, want the customer key k", got.TagsAll)
				}
			}
			assertTags(t, got.Tags, tc.want, tc.wantNil)
		})
	}
}

func assertTags(t *testing.T, got types.Map, want map[string]string, wantNil bool) {
	t.Helper()
	if wantNil {
		if !got.IsNull() {
			t.Errorf("state tags = %v, want null", got)
		}
		return
	}
	if got.IsNull() {
		t.Fatalf("state tags = null, want %v", want)
	}
	gotMap := map[string]string{}
	got.ElementsAs(context.Background(), &gotMap, false)
	if len(gotMap) != len(want) {
		t.Fatalf("state tags = %v, want %v", gotMap, want)
	}
	for k, v := range want {
		if gotMap[k] != v {
			t.Errorf("state tags[%q] = %q, want %q", k, gotMap[k], v)
		}
	}
}
