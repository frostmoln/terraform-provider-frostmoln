package kubernetes_cluster

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
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
	// poolTags are the initial pool's own tags. A create sets them to a copy of the
	// cluster's tags, as the platform does; the pool PATCH replaces them.
	poolTags map[string]string
	// patchFailures are the statuses the next pool PATCHes answer before one succeeds.
	patchFailures []int

	mu      sync.Mutex
	posts   []map[string]json.RawMessage
	puts    []map[string]json.RawMessage
	patches []map[string]json.RawMessage
	others  []string
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
	pool := func() apiNodePool {
		p := initialPool(statusActive)
		p.Tags = a.poolTags
		return p
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	switch {
	case r.Method == http.MethodPost && r.URL.Path == base:
		a.posts = append(a.posts, body)
		a.tags = map[string]string{}
		_ = json.Unmarshal(body["tags"], &a.tags)
		a.poolTags = map[string]string{}
		for k, v := range a.tags {
			a.poolTags[k] = v
		}
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
		writeJSON(a.t, w, apiNodePoolList{NodePools: []apiNodePool{pool()}})
	case r.Method == http.MethodGet && r.URL.Path == base+"/c-1/node-pools/np-1":
		writeJSON(a.t, w, pool())
	case r.Method == http.MethodPatch && r.URL.Path == base+"/c-1/node-pools/np-1":
		a.patches = append(a.patches, body)
		if len(a.patchFailures) > 0 {
			code := a.patchFailures[0]
			a.patchFailures = a.patchFailures[1:]
			w.WriteHeader(code)
			writeJSON(a.t, w, map[string]string{"code": "failed", "message": "injected"})
			return
		}
		next := map[string]string{}
		_ = json.Unmarshal(body["tags"], &next)
		a.poolTags = next
		writeJSON(a.t, w, pool())
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
	r := testResource(c)
	r.poolTagRetryInterval = time.Millisecond
	return r, srv.Close
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

// The schema says what the API does to the initial pool, and where that pool's tags
// are managed instead.
func TestSchema_TagsDocumentTheInitialPoolCopy(t *testing.T) {
	var sr resource.SchemaResponse
	NewResource().Schema(context.Background(), resource.SchemaRequest{}, &sr)
	desc := sr.Schema.Attributes["tags"].GetDescription()
	for _, want := range []string{"ONE-TIME COPY", "do NOT reach", "initial_node_pool.tags"} {
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

// poolTagsOf decodes one recorded PATCH body's tags.
func poolTagsOf(t *testing.T, body map[string]json.RawMessage) map[string]string {
	t.Helper()
	out := map[string]string{}
	if err := json.Unmarshal(body["tags"], &out); err != nil {
		t.Fatalf("PATCH tags: %v (%s)", err, body["tags"])
	}
	return out
}

func mapValues(t *testing.T, m types.Map) map[string]string {
	t.Helper()
	out := map[string]string{}
	if m.IsNull() || m.IsUnknown() {
		t.Fatalf("map is null or unknown: %v", m)
	}
	for k, v := range m.Elements() {
		out[k] = v.(types.String).ValueString()
	}
	return out
}

// initial_node_pool.tags never ride the create (the API refuses them). The pool is
// created with the platform's copy of the cluster's merged tags; the provider then
// writes the configured keys over that copy, keeping the copied keys.
func TestCreate_InitialPoolTagsArePatchedOntoTheCopy(t *testing.T) {
	api := &tagsAPI{t: t}
	r, stop := api.resource(map[string]string{"env": "prod"})
	defer stop()

	planM := pinnedCreatePlan()
	planM.AddonVersions = types.MapNull(types.StringType)
	planM.Tags = tagMap("team", "web")
	planM.TagsAll = types.MapUnknown(types.StringType)
	pool := *planM.InitialNodePool
	pool.Tags, pool.TagsAll = tagMap("role", "workers"), types.MapUnknown(types.StringType)
	planM.InitialNodePool = &pool
	plan := buildPlan(t, planM)
	resp := resource.CreateResponse{State: emptyState(t)}
	r.Create(context.Background(), resource.CreateRequest{Plan: plan, Config: tfsdk.Config(plan)}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("create: %v", resp.Diagnostics.Errors())
	}
	var sent map[string]json.RawMessage
	_ = json.Unmarshal(api.posts[0]["initialNodePool"], &sent)
	if v, ok := sent["tags"]; ok {
		t.Errorf("initialNodePool.tags was sent (%s); the API refuses it", v)
	}
	if len(api.patches) != 1 {
		t.Fatalf("want one pool PATCH, got %d", len(api.patches))
	}
	want := map[string]string{"env": "prod", "team": "web", "role": "workers"}
	if got := poolTagsOf(t, api.patches[0]); !tftags.Equal(got, want) {
		t.Errorf("pool PATCH = %v, want %v", got, want)
	}
	var st KubernetesClusterModel
	resp.Diagnostics.Append(resp.State.Get(context.Background(), &st)...)
	if got := mapValues(t, st.InitialNodePool.Tags); !tftags.Equal(got, map[string]string{"role": "workers"}) {
		t.Errorf("initial_node_pool.tags = %v, want only the configured key", got)
	}
	if got := mapValues(t, st.InitialNodePool.TagsAll); !tftags.Equal(got, want) {
		t.Errorf("initial_node_pool.tags_all = %v, want %v", got, want)
	}
}

// No initial_node_pool.tags: the copy is already the desired set, so the pool is
// never written, and tags_all shows the copy.
func TestCreate_NoInitialPoolTagsNoPoolWrite(t *testing.T) {
	api := &tagsAPI{t: t}
	r, stop := api.resource(map[string]string{"env": "prod"})
	defer stop()

	planM := pinnedCreatePlan()
	planM.AddonVersions = types.MapNull(types.StringType)
	planM.Tags = tagMap("team", "web")
	planM.TagsAll = types.MapUnknown(types.StringType)
	pool := *planM.InitialNodePool
	pool.Tags, pool.TagsAll = types.MapNull(types.StringType), types.MapUnknown(types.StringType)
	planM.InitialNodePool = &pool
	plan := buildPlan(t, planM)
	resp := resource.CreateResponse{State: emptyState(t)}
	r.Create(context.Background(), resource.CreateRequest{Plan: plan, Config: tfsdk.Config(plan)}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("create: %v", resp.Diagnostics.Errors())
	}
	if len(api.patches) != 0 {
		t.Errorf("pool PATCHes = %v, want none", api.patches)
	}
	var st KubernetesClusterModel
	resp.Diagnostics.Append(resp.State.Get(context.Background(), &st)...)
	if !st.InitialNodePool.Tags.IsNull() {
		t.Errorf("initial_node_pool.tags = %v, want null", st.InitialNodePool.Tags)
	}
	if got := mapValues(t, st.InitialNodePool.TagsAll); !tftags.Equal(got, map[string]string{"env": "prod", "team": "web"}) {
		t.Errorf("initial_node_pool.tags_all = %v, want the copy", got)
	}
}

// An update PATCHes the pool's tags alone: a configured change applied, a removed
// managed key cleared, and a key added outside Terraform since the refresh kept —
// read right before the write. The cluster is not written.
func TestUpdate_InitialPoolTags(t *testing.T) {
	api := &tagsAPI{t: t, poolTags: map[string]string{"role": "a", "gone": "x", "team": "web", "late": "l"}}
	r, stop := api.resource(nil)
	defer stop()

	stateM := stateModel()
	statePool := *stateM.InitialNodePool
	statePool.Tags = tagMap("role", "a", "gone", "x")
	statePool.TagsAll = tagMap("role", "a", "gone", "x", "team", "web")
	stateM.InitialNodePool = &statePool
	planM := stateModel()
	planPool := statePool
	planPool.Tags, planPool.TagsAll = tagMap("role", "b"), types.MapUnknown(types.StringType)
	planM.InitialNodePool = &planPool
	state, plan := buildState(t, stateM), buildPlan(t, planM)
	resp := resource.UpdateResponse{State: state}
	r.Update(context.Background(), resource.UpdateRequest{Plan: plan, State: state, Config: tfsdk.Config(plan)}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("update: %v", resp.Diagnostics.Errors())
	}
	if len(api.puts) != 0 {
		t.Errorf("cluster PUTs = %v, want none", api.puts)
	}
	want := map[string]string{"role": "b", "team": "web", "late": "l"}
	if len(api.patches) != 1 {
		t.Fatalf("want one pool PATCH, got %d", len(api.patches))
	}
	if got := poolTagsOf(t, api.patches[0]); !tftags.Equal(got, want) {
		t.Errorf("pool PATCH = %v, want %v", got, want)
	}
	var st KubernetesClusterModel
	resp.Diagnostics.Append(resp.State.Get(context.Background(), &st)...)
	if got := mapValues(t, st.InitialNodePool.TagsAll); !tftags.Equal(got, want) {
		t.Errorf("initial_node_pool.tags_all = %v, want %v", got, want)
	}
	if got := mapValues(t, st.InitialNodePool.Tags); !tftags.Equal(got, map[string]string{"role": "b"}) {
		t.Errorf("initial_node_pool.tags = %v, want the configured value", got)
	}
	if got := st.InitialNodePool.NodeCount.ValueInt64(); got != 2 {
		t.Errorf("node_count = %d, want the prior 2 (a tags-only update re-reads only the tags)", got)
	}
}

// A refresh reads the pool's tags: `tags` keeps the configured keys with the
// platform's values, `tags_all` holds every key.
func TestRead_InitialPoolTags(t *testing.T) {
	api := &tagsAPI{t: t, poolTags: map[string]string{"role": "changed", "portal": "p"}}
	r, stop := api.resource(nil)
	defer stop()

	stateM := stateModel()
	pool := *stateM.InitialNodePool
	pool.Tags, pool.TagsAll = tagMap("role", "a"), tagMap("role", "a")
	stateM.InitialNodePool = &pool
	state := buildState(t, stateM)
	resp := resource.ReadResponse{State: state}
	r.Read(context.Background(), resource.ReadRequest{State: state}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("read: %v", resp.Diagnostics.Errors())
	}
	var st KubernetesClusterModel
	resp.Diagnostics.Append(resp.State.Get(context.Background(), &st)...)
	if got := mapValues(t, st.InitialNodePool.Tags); !tftags.Equal(got, map[string]string{"role": "changed"}) {
		t.Errorf("initial_node_pool.tags = %v, want the drifted value", got)
	}
	if got := mapValues(t, st.InitialNodePool.TagsAll); !tftags.Equal(got, map[string]string{"role": "changed", "portal": "p"}) {
		t.Errorf("initial_node_pool.tags_all = %v", got)
	}
}

// A cluster tag that overrides a default is the cluster's: the copy carried it onto
// the pool, and the create writes the default back there (default_tags apply to the
// pool as to any resource).
func TestCreate_ClusterOverrideOfADefaultIsNotThePools(t *testing.T) {
	api := &tagsAPI{t: t}
	r, stop := api.resource(map[string]string{"team": "ops"})
	defer stop()

	planM := pinnedCreatePlan()
	planM.AddonVersions = types.MapNull(types.StringType)
	planM.Tags = tagMap("team", "web")
	planM.TagsAll = types.MapUnknown(types.StringType)
	pool := *planM.InitialNodePool
	pool.Tags, pool.TagsAll = types.MapNull(types.StringType), types.MapUnknown(types.StringType)
	planM.InitialNodePool = &pool
	plan := buildPlan(t, planM)
	resp := resource.CreateResponse{State: emptyState(t)}
	r.Create(context.Background(), resource.CreateRequest{Plan: plan, Config: tfsdk.Config(plan)}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("create: %v", resp.Diagnostics.Errors())
	}
	if len(api.patches) != 1 || !tftags.Equal(poolTagsOf(t, api.patches[0]), map[string]string{"team": "ops"}) {
		t.Errorf("pool PATCHes = %v, want one carrying the default", api.patches)
	}
}

// createWithPoolTags runs a create whose cluster and initial pool carry the given tags.
func createWithPoolTags(t *testing.T, api *tagsAPI, defaults map[string]string, cluster, pool types.Map) resource.CreateResponse {
	t.Helper()
	r, stop := api.resource(defaults)
	defer stop()
	planM := pinnedCreatePlan()
	planM.AddonVersions = types.MapNull(types.StringType)
	planM.Tags, planM.TagsAll = cluster, types.MapUnknown(types.StringType)
	p := *planM.InitialNodePool
	p.Tags, p.TagsAll = pool, types.MapUnknown(types.StringType)
	planM.InitialNodePool = &p
	plan := buildPlan(t, planM)
	resp := resource.CreateResponse{State: emptyState(t)}
	r.Create(context.Background(), resource.CreateRequest{Plan: plan, Config: tfsdk.Config(plan)}, &resp)
	return resp
}

func manyTags(prefix string, n int) types.Map {
	kv := make([]string, 0, 2*n)
	for i := 0; i < n; i++ {
		kv = append(kv, fmt.Sprintf("%s%02d", prefix, i), "v")
	}
	return tagMap(kv...)
}

// A pool tag set the platform would refuse is refused before the cluster POST:
// past it, the refusal would taint a cluster that is already provisioning.
func TestCreate_InitialPoolOverBudgetIsRefusedBeforeTheCreate(t *testing.T) {
	api := &tagsAPI{t: t}
	resp := createWithPoolTags(t, api, nil, manyTags("c", 20), manyTags("p", 13))
	if !resp.Diagnostics.HasError() {
		t.Fatal("want an error")
	}
	if len(api.posts) != 0 {
		t.Errorf("the cluster was created (%d POSTs); the refusal must come first", len(api.posts))
	}
}

// A transient failure of the pool PATCH right after the create is retried; a
// refusal (4xx) is not.
func TestCreate_InitialPoolTagWriteRetriesTransientFailures(t *testing.T) {
	api := &tagsAPI{t: t, patchFailures: []int{http.StatusBadGateway, http.StatusServiceUnavailable}}
	resp := createWithPoolTags(t, api, nil, types.MapNull(types.StringType), tagMap("role", "w"))
	if resp.Diagnostics.HasError() {
		t.Fatalf("create: %v", resp.Diagnostics.Errors())
	}
	if len(api.patches) != 3 {
		t.Errorf("PATCH attempts = %d, want 3 (two 5xx, then the write)", len(api.patches))
	}

	api = &tagsAPI{t: t, patchFailures: []int{http.StatusBadRequest}}
	resp = createWithPoolTags(t, api, nil, types.MapNull(types.StringType), tagMap("role", "w"))
	if !resp.Diagnostics.HasError() {
		t.Fatal("a 400 must fail the create")
	}
	if len(api.patches) != 1 {
		t.Errorf("PATCH attempts = %d, want 1: a refusal is not retried", len(api.patches))
	}
}

// A platform-reserved key on the pool never reaches state, and is never written back.
func TestUpdate_InitialPoolReservedKeysStayOut(t *testing.T) {
	api := &tagsAPI{t: t, poolTags: map[string]string{"role": "a", "Frostmoln_Legacy": "x", "frostmoln-note": "y"}}
	r, stop := api.resource(nil)
	defer stop()

	stateM := stateModel()
	sp := *stateM.InitialNodePool
	sp.Tags, sp.TagsAll = tagMap("role", "a"), tagMap("role", "a")
	stateM.InitialNodePool = &sp
	planM := stateModel()
	pp := sp
	pp.Tags, pp.TagsAll = tagMap("role", "b"), types.MapUnknown(types.StringType)
	planM.InitialNodePool = &pp
	state, plan := buildState(t, stateM), buildPlan(t, planM)
	resp := resource.UpdateResponse{State: state}
	r.Update(context.Background(), resource.UpdateRequest{Plan: plan, State: state, Config: tfsdk.Config(plan)}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("update: %v", resp.Diagnostics.Errors())
	}
	if len(api.patches) != 1 || !tftags.Equal(poolTagsOf(t, api.patches[0]), map[string]string{"role": "b"}) {
		t.Errorf("pool PATCHes = %v, want only role=b", api.patches)
	}
	var st KubernetesClusterModel
	resp.Diagnostics.Append(resp.State.Get(context.Background(), &st)...)
	if got := mapValues(t, st.InitialNodePool.TagsAll); len(got) != 1 {
		t.Errorf("initial_node_pool.tags_all = %v, want no reserved key", got)
	}
}

// Changing retained_volumes_on_destroy together with the initial pool's tags is not
// a state-only update: the pool's tags are written.
func TestUpdate_DestroyChoiceWithInitialPoolTagsWritesThePool(t *testing.T) {
	api := &tagsAPI{t: t, poolTags: map[string]string{"role": "a"}}
	r, stop := api.resource(nil)
	defer stop()

	stateM := stateModel()
	sp := *stateM.InitialNodePool
	sp.Tags, sp.TagsAll = tagMap("role", "a"), tagMap("role", "a")
	stateM.InitialNodePool = &sp
	planM := stateModel()
	planM.RetainedVolumesOnDestroy = types.StringValue("keep")
	pp := sp
	pp.Tags, pp.TagsAll = tagMap("role", "b"), types.MapUnknown(types.StringType)
	planM.InitialNodePool = &pp
	state, plan := buildState(t, stateM), buildPlan(t, planM)
	resp := resource.UpdateResponse{State: state}
	r.Update(context.Background(), resource.UpdateRequest{Plan: plan, State: state, Config: tfsdk.Config(plan)}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("update: %v", resp.Diagnostics.Errors())
	}
	if len(api.patches) != 1 {
		t.Errorf("pool PATCHes = %d, want 1", len(api.patches))
	}
	var st KubernetesClusterModel
	resp.Diagnostics.Append(resp.State.Get(context.Background(), &st)...)
	if got := mapValues(t, st.InitialNodePool.Tags); !tftags.Equal(got, map[string]string{"role": "b"}) {
		t.Errorf("initial_node_pool.tags = %v, want the configured value", got)
	}
}

// The create plan warns about a cluster key that overrides a default (the pool gets
// the default) and about a pool over the tag budget.
func TestModifyPlan_WarnsAboutInitialPoolTags(t *testing.T) {
	cases := []struct {
		name          string
		defaults      map[string]string
		cluster, pool types.Map
		want          string
	}{
		{"override", map[string]string{"team": "ops"}, tagMap("team", "web"), types.MapNull(types.StringType),
			"Cluster Tag Overrides A Default On The Initial Node Pool"},
		{"budget", nil, manyTags("c", 20), manyTags("p", 13), "Too Many Tags On The Initial Node Pool"},
		{"none", map[string]string{"team": "ops"}, tagMap("team", "web"), tagMap("team", "web"), ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			planM := pinnedCreatePlan()
			planM.AddonVersions = types.MapNull(types.StringType)
			planM.Tags, planM.TagsAll = tc.cluster, types.MapUnknown(types.StringType)
			p := *planM.InitialNodePool
			p.Tags, p.TagsAll = tc.pool, types.MapUnknown(types.StringType)
			planM.InitialNodePool = &p
			plan := buildPlan(t, planM)
			var diags diag.Diagnostics
			warnInitialPoolTags(context.Background(), tftags.Defaults{Tags: tc.defaults},
				resource.ModifyPlanRequest{Plan: plan, State: emptyState(t)}, &diags)
			var got []string
			for _, d := range diags.Warnings() {
				got = append(got, d.Summary())
			}
			if tc.want == "" && len(got) != 0 || tc.want != "" && (len(got) != 1 || got[0] != tc.want) {
				t.Errorf("warnings = %v, want %q", got, tc.want)
			}
		})
	}
}
