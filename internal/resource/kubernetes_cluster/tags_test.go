package kubernetes_cluster

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

// tagsAPI is the cluster API for the tag paths: it records every write body and
// answers reads with the cluster carrying `tags`.
type tagsAPI struct {
	t    *testing.T
	tags map[string]string

	mu     sync.Mutex
	posts  []map[string]json.RawMessage
	puts   []map[string]json.RawMessage
	others []string
}

func (a *tagsAPI) serve(w http.ResponseWriter, r *http.Request) {
	const base = "/v1/tenants/t-1/kubernetes-clusters"
	raw, _ := io.ReadAll(r.Body)
	var body map[string]json.RawMessage
	_ = json.Unmarshal(raw, &body)
	cluster := func() apiKubernetesCluster {
		c := runningCluster()
		c.Tags = a.tags
		return c
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	switch {
	case r.Method == http.MethodPost && r.URL.Path == base:
		a.posts = append(a.posts, body)
		w.WriteHeader(http.StatusCreated)
		writeJSON(a.t, w, cluster())
	case r.Method == http.MethodPut && r.URL.Path == base+"/c-1":
		a.puts = append(a.puts, body)
		if v, ok := body["tags"]; ok && string(v) != "null" {
			next := map[string]string{}
			_ = json.Unmarshal(v, &next)
			a.tags = next
		}
		writeJSON(a.t, w, cluster())
	case r.Method == http.MethodGet && r.URL.Path == base+"/c-1":
		writeJSON(a.t, w, cluster())
	case r.Method == http.MethodGet && r.URL.Path == base+"/c-1/node-pools":
		writeJSON(a.t, w, apiNodePoolList{NodePools: []apiNodePool{initialPool(statusActive)}})
	case r.Method == http.MethodGet && r.URL.Path == base+"/c-1/node-pools/np-1":
		writeJSON(a.t, w, initialPool(statusActive))
	case r.Method == http.MethodGet && r.URL.Path == base+"/c-1/kubeconfig":
		writeJSON(a.t, w, apiKubeconfig{Kubeconfig: "kubeconfig-yaml"})
	default:
		a.others = append(a.others, r.Method+" "+r.URL.Path)
		w.WriteHeader(http.StatusNotFound)
	}
}

func (a *tagsAPI) resource(defaults map[string]string) (*kubernetesClusterResource, func()) {
	srv := httptest.NewServer(http.HandlerFunc(a.serve))
	c := client.NewClient(srv.URL, "test-key", client.WithHTTPClient(srv.Client()), // pragma: allowlist secret
		client.WithDefaultTags(tftags.Defaults{Tags: defaults}))
	c.SetTenantIDForTest("t-1")
	return testResource(c), srv.Close
}

// The cluster's tags (default_tags merged) ride the create; the initial pool's
// never do — the API refuses initialNodePool.tags, and gives the pool a copy of
// the cluster's merged set instead.
func TestCreate_TagsOnTheClusterNeverOnTheInitialPool(t *testing.T) {
	api := &tagsAPI{t: t}
	r, stop := api.resource(map[string]string{"env": "prod"})
	defer stop()

	planM := pinnedCreatePlan()
	planM.AddonVersions = types.MapNull(types.StringType)
	planM.Tags = tagMap("team", "web")
	planM.TagsAll = types.MapUnknown(types.StringType)
	plan := buildPlan(t, planM)
	resp := resource.CreateResponse{State: emptyState(t)}
	r.Create(context.Background(), resource.CreateRequest{Plan: plan, Config: tfsdk.Config(plan)}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("create: %v", resp.Diagnostics.Errors())
	}
	if len(api.posts) != 1 {
		t.Fatalf("want one POST, got %d", len(api.posts))
	}
	var tags map[string]string
	if err := json.Unmarshal(api.posts[0]["tags"], &tags); err != nil {
		t.Fatalf("tags: %v (%s)", err, api.posts[0]["tags"])
	}
	if len(tags) != 2 || tags["env"] != "prod" || tags["team"] != "web" {
		t.Errorf("create tags = %v, want default_tags ∪ tags", tags)
	}
	var pool map[string]json.RawMessage
	if err := json.Unmarshal(api.posts[0]["initialNodePool"], &pool); err != nil {
		t.Fatalf("initialNodePool: %v", err)
	}
	if v, sent := pool["tags"]; sent {
		t.Errorf("initialNodePool.tags was sent (%s); the API refuses it", v)
	}
}

// The cluster PUT has PATCH semantics, so an update sends only what changed: a
// rename carries no tags, a tag change carries no name, and a clear is an explicit {}.
func TestUpdate_PutCarriesOnlyWhatChanged(t *testing.T) {
	cases := []struct {
		name       string
		stateTags  types.Map
		planTags   types.Map
		rename     bool
		wantPuts   int
		wantFields map[string]string // field -> exact JSON
	}{
		{"rename only", tagMap("env", "prod"), tagMap("env", "prod"), true, 1, map[string]string{"name": `"renamed"`}},
		{"tags only", tagMap("env", "prod"), tagMap("env", "dev"), false, 1, map[string]string{"tags": `{"env":"dev"}`}},
		{"both", tagMap("env", "prod"), tagMap("env", "dev"), true, 1, map[string]string{"name": `"renamed"`, "tags": `{"env":"dev"}`}},
		{"clear", tagMap("env", "prod"), types.MapNull(types.StringType), false, 1, map[string]string{"tags": `{}`}},
		{"nothing about name or tags", tagMap("env", "prod"), tagMap("env", "prod"), false, 0, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			api := &tagsAPI{t: t, tags: map[string]string{"env": "prod"}}
			r, stop := api.resource(nil)
			defer stop()

			stateM := stateModel()
			stateM.Tags, stateM.TagsAll = tc.stateTags, tagMap("env", "prod")
			planM := stateModel()
			planM.Tags, planM.TagsAll = tc.planTags, types.MapUnknown(types.StringType)
			if tc.rename {
				planM.Name = types.StringValue("renamed")
			}
			state, plan := buildState(t, stateM), buildPlan(t, planM)
			resp := resource.UpdateResponse{State: state}
			r.Update(context.Background(), resource.UpdateRequest{Plan: plan, State: state, Config: tfsdk.Config(plan)}, &resp)
			if resp.Diagnostics.HasError() {
				t.Fatalf("update: %v", resp.Diagnostics.Errors())
			}
			if len(api.others) > 0 {
				t.Errorf("unexpected requests: %v", api.others)
			}
			if len(api.puts) != tc.wantPuts {
				t.Fatalf("want %d PUT, got %d: %v", tc.wantPuts, len(api.puts), api.puts)
			}
			if tc.wantPuts == 0 {
				return
			}
			body := api.puts[0]
			if len(body) != len(tc.wantFields) {
				t.Errorf("PUT body %v, want exactly %v", body, tc.wantFields)
			}
			for f, want := range tc.wantFields {
				if got := string(body[f]); got != want {
					t.Errorf("PUT %s = %s, want %s", f, got, want)
				}
			}
			var got KubernetesClusterModel
			resp.Diagnostics.Append(resp.State.Get(context.Background(), &got)...)
			if !got.TagsAll.Equal(types.MapValueMust(types.StringType, toAttr(api.tags))) {
				t.Errorf("tags_all = %v, want the read-back %v", got.TagsAll, api.tags)
			}
		})
	}
}

// A key added outside Terraform between the refresh and the apply is kept: the
// update reads the cluster's tags before it writes them.
func TestUpdate_KeepsAKeyAddedSinceTheRefresh(t *testing.T) {
	api := &tagsAPI{t: t, tags: map[string]string{"env": "prod", "portal": "p"}}
	r, stop := api.resource(nil)
	defer stop()
	stateM := stateModel()
	stateM.Tags, stateM.TagsAll = tagMap("env", "prod"), tagMap("env", "prod")
	planM := stateModel()
	planM.Tags, planM.TagsAll = tagMap("env", "dev"), types.MapUnknown(types.StringType)
	state, plan := buildState(t, stateM), buildPlan(t, planM)
	resp := resource.UpdateResponse{State: state}
	r.Update(context.Background(), resource.UpdateRequest{Plan: plan, State: state, Config: tfsdk.Config(plan)}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("update: %v", resp.Diagnostics.Errors())
	}
	if len(api.puts) != 1 || string(api.puts[0]["tags"]) != `{"env":"dev","portal":"p"}` {
		t.Errorf("PUT = %v, want the portal key kept", api.puts)
	}
}

// The schema says what the API does to the initial pool, and that this resource
// does not manage that pool's tags.
func TestSchema_TagsDocumentTheInitialPoolCopy(t *testing.T) {
	var sr resource.SchemaResponse
	NewResource().Schema(context.Background(), resource.SchemaRequest{}, &sr)
	desc := sr.Schema.Attributes["tags"].GetDescription()
	for _, want := range []string{"ONE-TIME COPY", "do NOT reach", "fm kubernetes node-pool tags"} {
		if !strings.Contains(desc, want) {
			t.Errorf("tags description lacks %q:\n%s", want, desc)
		}
	}
	if _, ok := sr.Schema.Attributes["tags_all"]; !ok {
		t.Error("no tags_all")
	}
	if len(sr.Schema.Attributes["tags"].(interface{ MapValidators() []validator.Map }).MapValidators()) == 0 {
		t.Error("tags has no offer-policy validator")
	}
}

func toAttr(m map[string]string) map[string]attr.Value {
	out := map[string]attr.Value{}
	for k, v := range m {
		out[k] = types.StringValue(v)
	}
	return out
}
