package kubernetes_node_pool

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/tftags"
)

func tagMap(kv ...string) types.Map {
	elems := map[string]attr.Value{}
	for i := 0; i+1 < len(kv); i += 2 {
		elems[kv[i]] = types.StringValue(kv[i+1])
	}
	return types.MapValueMust(types.StringType, elems)
}

// A tag change is ONE PATCH carrying `tags` alone — never a scale, never another
// field — and an update that changes no tag sends no PATCH at all.
func TestUpdate_TagsArePatchedAlone(t *testing.T) {
	cases := []struct {
		name      string
		planTags  types.Map
		wantPatch string // "" = no PATCH
	}{
		{"replace", tagMap("env", "dev"), `{"tags":{"env":"dev"}}`},
		{"clear", types.MapNull(types.StringType), `{"tags":{}}`},
		{"unchanged", tagMap("env", "prod"), ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var mu sync.Mutex
			var patches, others []string
			tags := map[string]string{"env": "prod"}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				defer mu.Unlock()
				p := testPool(statusActive)
				switch {
				case r.Method == http.MethodPatch && r.URL.Path == poolPath:
					raw, _ := io.ReadAll(r.Body)
					patches = append(patches, string(raw))
					var body struct{ Tags map[string]string }
					_ = json.Unmarshal(raw, &body)
					tags = body.Tags
					p.Tags = tags
					writeJSON(t, w, p)
				case r.Method == http.MethodGet && r.URL.Path == poolPath:
					p.Tags = tags
					writeJSON(t, w, p)
				default:
					others = append(others, r.Method+" "+r.URL.Path)
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer server.Close()
			r := testResource(newTestClient(t, server))

			prior := KubernetesNodePoolModel{
				ID: types.StringValue("np-2"), ClusterID: types.StringValue("c-1"),
				Name: types.StringValue("workers"), FlavorID: types.StringValue("k8s.gp1.medium"),
				NodeCount: types.Int64Value(2), Status: types.StringValue(statusActive),
				CreatedAt: types.StringValue("2026-07-01T00:00:00Z"),
				Tags:      tagMap("env", "prod"), TagsAll: tagMap("env", "prod"),
			}
			planned := prior
			planned.Tags, planned.TagsAll = tc.planTags, types.MapUnknown(types.StringType)
			state, plan := buildState(t, prior), buildPlan(t, planned)
			resp := resource.UpdateResponse{State: state}
			r.Update(context.Background(), resource.UpdateRequest{Plan: plan, State: state, Config: tfsdk.Config(plan)}, &resp)
			if resp.Diagnostics.HasError() {
				t.Fatalf("update: %v", resp.Diagnostics.Errors())
			}
			if len(others) > 0 {
				t.Errorf("unexpected requests (a tag change must not scale): %v", others)
			}
			switch {
			case tc.wantPatch == "" && len(patches) != 0:
				t.Errorf("an unchanged tag set still PATCHed: %v", patches)
			case tc.wantPatch != "" && (len(patches) != 1 || strings.TrimSpace(patches[0]) != tc.wantPatch):
				t.Errorf("PATCH bodies = %v, want exactly %s", patches, tc.wantPatch)
			}
		})
	}
}

// The create sends default_tags ∪ tags.
func TestCreate_SendsMergedTags(t *testing.T) {
	var body map[string]json.RawMessage
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == poolsPath:
			raw, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(raw, &body)
			w.WriteHeader(http.StatusCreated)
			p := testPool("creating")
			p.Tags = map[string]string{"team": "web"}
			writeJSON(t, w, p)
		case r.Method == http.MethodGet && r.URL.Path == poolPath:
			p := testPool(statusActive)
			p.Tags = map[string]string{"team": "web"}
			writeJSON(t, w, p)
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	r := testResource(newTestClient(t, server))
	planned := KubernetesNodePoolModel{
		ClusterID: types.StringValue("c-1"), Name: types.StringValue("workers"),
		FlavorID: types.StringValue("k8s.gp1.medium"), NodeCount: types.Int64Value(2),
		Tags: tagMap("team", "web"), TagsAll: types.MapUnknown(types.StringType),
	}
	plan := buildPlan(t, planned)
	resp := resource.CreateResponse{State: emptyState(t)}
	r.Create(context.Background(), resource.CreateRequest{Plan: plan, Config: tfsdk.Config(plan)}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("create: %v", resp.Diagnostics.Errors())
	}
	if got := string(body["tags"]); got != `{"team":"web"}` {
		t.Errorf("create tags = %s", got)
	}
	var got KubernetesNodePoolModel
	resp.Diagnostics.Append(resp.State.Get(context.Background(), &got)...)
	if !got.Tags.Equal(tagMap("team", "web")) || !got.TagsAll.Equal(tagMap("team", "web")) {
		t.Errorf("state tags = %v / %v", got.Tags, got.TagsAll)
	}
}

// default_tags plus the pool's tags over the offer's 32-key budget warn at plan time
// (the apply would be refused); within it, nothing is said. Never an error: a plan
// error would also block a destroy.
func TestModifyPlan_WarnsWhenDefaultTagsPushTheBudgetOver32(t *testing.T) {
	for _, tc := range []struct {
		own      int
		wantWarn bool
	}{{25, true}, {22, false}} {
		defaults := map[string]string{}
		for i := 0; i < 10; i++ {
			defaults["d"+strings.Repeat("x", i)] = "v"
		}
		c := client.NewClient("http://unused", "k", client.WithDefaultTags(tftags.Defaults{Tags: defaults})) // pragma: allowlist secret
		r := testResource(c)
		kv := []string{}
		for i := 0; i < tc.own; i++ {
			kv = append(kv, "o"+strings.Repeat("x", i), "v")
		}
		planM := KubernetesNodePoolModel{
			ClusterID: types.StringValue("c-1"), FlavorID: types.StringValue("f"), NodeCount: types.Int64Value(1),
			Tags: tagMap(kv...), TagsAll: types.MapUnknown(types.StringType),
		}
		plan := buildPlan(t, planM)
		resp := resource.ModifyPlanResponse{Plan: plan}
		r.ModifyPlan(context.Background(), resource.ModifyPlanRequest{Plan: plan, State: emptyState(t), Config: tfsdk.Config(plan)}, &resp)
		if resp.Diagnostics.HasError() {
			t.Fatalf("own=%d: errors %v", tc.own, resp.Diagnostics.Errors())
		}
		if got := len(resp.Diagnostics.Warnings()) > 0; got != tc.wantWarn {
			t.Errorf("own=%d: warning=%v, want %v (%v)", tc.own, got, tc.wantWarn, resp.Diagnostics)
		}
	}
}

func TestSchema_TagsCarryTheOfferValidator(t *testing.T) {
	var sr resource.SchemaResponse
	NewResource().Schema(context.Background(), resource.SchemaRequest{}, &sr)
	if len(sr.Schema.Attributes["tags"].(interface{ MapValidators() []validator.Map }).MapValidators()) == 0 {
		t.Error("tags has no validator")
	}
}
