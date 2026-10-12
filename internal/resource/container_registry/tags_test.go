package container_registry

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

// tagsOf is a known map(string) value.
func tagsOf(kv map[string]string) types.Map {
	out := map[string]attr.Value{}
	for k, v := range kv {
		out[k] = types.StringValue(v)
	}
	return types.MapValueMust(types.StringType, out)
}

// registryModel is the enabled registry's state with the given tags.
func registryModel(tags, tagsAll types.Map) ContainerRegistryModel {
	return ContainerRegistryModel{
		ID: types.StringValue("tenant-456"), Enabled: types.BoolValue(true),
		Endpoint:          types.StringValue("registry.sweden.frostmoln.cloud"),
		Namespace:         types.StringValue("t-abc"),
		StorageLimitBytes: types.Int64Null(), StorageUsedBytes: types.Int64Null(),
		Tags: tags, TagsAll: tagsAll,
	}
}

func registryRaw(t *testing.T, m ContainerRegistryModel) tftypes.Value {
	t.Helper()
	st := tfsdk.State{Schema: registrySchema(t)}
	if d := st.Set(context.Background(), &m); d.HasError() {
		t.Fatalf("building value: %v", d)
	}
	return st.Raw
}

// recorder answers the registry routes from a canned settings body and records
// every request other than the provider's GET /v1/me.
type recorder struct {
	mu       sync.Mutex
	requests []string
	bodies   map[string]map[string]json.RawMessage
}

func (r *recorder) record(req *http.Request) map[string]json.RawMessage {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.requests = append(r.requests, req.Method+" "+req.URL.Path)
	raw, _ := io.ReadAll(req.Body)
	body := map[string]json.RawMessage{}
	_ = json.Unmarshal(raw, &body)
	if r.bodies == nil {
		r.bodies = map[string]map[string]json.RawMessage{}
	}
	r.bodies[req.Method] = body
	return body
}

func sentTags(t *testing.T, body map[string]json.RawMessage) map[string]string {
	t.Helper()
	raw, ok := body["tags"]
	if !ok {
		t.Fatalf("the request carried no tags: %v", body)
	}
	out := map[string]string{}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("tags = %s: %v", raw, err)
	}
	return out
}

func mapsEqual(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if w, ok := b[k]; !ok || w != v {
			return false
		}
	}
	return true
}

func stateTags(t *testing.T, st tfsdk.State) (tags, all map[string]string) {
	t.Helper()
	var got ContainerRegistryModel
	if d := st.Get(context.Background(), &got); d.HasError() {
		t.Fatalf("state get: %v", d)
	}
	conv := func(m types.Map) map[string]string {
		if m.IsNull() {
			return nil
		}
		out := map[string]string{}
		for k, v := range m.Elements() {
			out[k] = v.(types.String).ValueString()
		}
		return out
	}
	return conv(got.Tags), conv(got.TagsAll)
}

// The opt-in carries the configured tags, and tags_all reads back what the
// registry holds — the tenant's default tags included.
func TestCreateSendsTagsInTheOptIn(t *testing.T) {
	rec := &recorder{}
	server := newMeAndRegistryServer(t, func(w http.ResponseWriter, r *http.Request) {
		rec.record(r)
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, `{"enabled":true,"endpoint":"registry.sweden.frostmoln.cloud","namespace":"t-abc",
			"tags":{"team":"web","cost-center":"42"}}`)
	})
	defer server.Close()

	s := registrySchema(t)
	plan := registryModel(tagsOf(map[string]string{"team": "web"}), types.MapUnknown(types.StringType))
	resp := &resource.CreateResponse{State: tfsdk.State{Schema: s, Raw: tftypes.NewValue(registryTFType(t), nil)}}
	configuredRegistryResource(t, server.URL).Create(context.Background(),
		resource.CreateRequest{Plan: tfsdk.Plan{Schema: s, Raw: registryRaw(t, plan)}}, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("create: %v", resp.Diagnostics)
	}
	if got := sentTags(t, rec.bodies[http.MethodPost]); !mapsEqual(got, map[string]string{"team": "web"}) {
		t.Errorf("the opt-in sent tags %v, want team=web", got)
	}
	tags, all := stateTags(t, resp.State)
	if !mapsEqual(tags, map[string]string{"team": "web"}) || !mapsEqual(all, map[string]string{"team": "web", "cost-center": "42"}) {
		t.Errorf("tags = %v, tags_all = %v", tags, all)
	}
}

// An ADOPTED registry gets the configured tags ADDED to the ones it holds: the
// opt-in that would have carried them was refused as already done, and a full
// replace would drop the keys set in the portal.
func TestAdoptAddsConfiguredTagsToTheExistingOnes(t *testing.T) {
	rec := &recorder{}
	server := newMeAndRegistryServer(t, func(w http.ResponseWriter, r *http.Request) {
		body := rec.record(r)
		switch r.Method {
		case http.MethodPost:
			w.WriteHeader(http.StatusConflict)
			_, _ = io.WriteString(w, `{"code":"conflict","message":"already enabled","details":{"reason":"REGISTRY_ALREADY_ENABLED"}}`)
		case http.MethodGet:
			_, _ = io.WriteString(w, `{"enabled":true,"endpoint":"e","namespace":"t-abc","tags":{"portal":"p","team":"ops"}}`)
		case http.MethodPatch:
			_ = json.NewEncoder(w).Encode(map[string]any{"enabled": true, "endpoint": "e", "namespace": "t-abc", "tags": body["tags"]})
		}
	})
	defer server.Close()

	s := registrySchema(t)
	plan := registryModel(tagsOf(map[string]string{"team": "web"}), types.MapUnknown(types.StringType))
	resp := &resource.CreateResponse{State: tfsdk.State{Schema: s, Raw: tftypes.NewValue(registryTFType(t), nil)}}
	configuredRegistryResource(t, server.URL).Create(context.Background(),
		resource.CreateRequest{Plan: tfsdk.Plan{Schema: s, Raw: registryRaw(t, plan)}}, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("create: %v", resp.Diagnostics)
	}
	if got := sentTags(t, rec.bodies[http.MethodPatch]); !mapsEqual(got, map[string]string{"portal": "p", "team": "web"}) {
		t.Errorf("the adoption PATCH sent %v, want the existing keys plus the configured one", got)
	}
	_, all := stateTags(t, resp.State)
	if !mapsEqual(all, map[string]string{"portal": "p", "team": "web"}) {
		t.Errorf("tags_all = %v", all)
	}
}

// A tags change is a PATCH of the settings carrying `tags` and nothing else —
// never a POST (the opt-in) and never a replace — computed against the tags the
// registry holds now, so a key set outside Terraform survives.
func TestTagsChangeIsAPatchOfTheSettings(t *testing.T) {
	rec := &recorder{}
	server := newMeAndRegistryServer(t, func(w http.ResponseWriter, r *http.Request) {
		body := rec.record(r)
		switch r.Method {
		case http.MethodGet:
			_, _ = io.WriteString(w, `{"enabled":true,"endpoint":"e","namespace":"t-abc","tags":{"env":"prod","portal":"p"}}`)
		case http.MethodPatch:
			_ = json.NewEncoder(w).Encode(map[string]any{"enabled": true, "endpoint": "e", "namespace": "t-abc", "tags": body["tags"]})
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
	})
	defer server.Close()

	s := registrySchema(t)
	state := registryModel(tagsOf(map[string]string{"env": "prod"}), tagsOf(map[string]string{"env": "prod", "portal": "p"}))
	plan := registryModel(tagsOf(map[string]string{"env": "staging"}), types.MapUnknown(types.StringType))
	resp := &resource.UpdateResponse{State: tfsdk.State{Schema: s, Raw: registryRaw(t, plan)}}
	configuredRegistryResource(t, server.URL).Update(context.Background(), resource.UpdateRequest{
		Plan: tfsdk.Plan{Schema: s, Raw: registryRaw(t, plan)}, State: tfsdk.State{Schema: s, Raw: registryRaw(t, state)},
	}, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("update: %v", resp.Diagnostics)
	}
	patch := rec.bodies[http.MethodPatch]
	if len(patch) != 1 {
		t.Errorf("the PATCH carried %v, want `tags` alone", patch)
	}
	if got := sentTags(t, patch); !mapsEqual(got, map[string]string{"env": "staging", "portal": "p"}) {
		t.Errorf("the PATCH sent %v, want env=staging with the portal key kept", got)
	}
	for _, req := range rec.requests {
		if req != "GET /v1/tenants/tenant-456/registry/settings" && req != "PATCH /v1/tenants/tenant-456/registry/settings" {
			t.Errorf("unexpected request %s", req)
		}
	}
	tags, all := stateTags(t, resp.State)
	if !mapsEqual(tags, map[string]string{"env": "staging"}) || !mapsEqual(all, map[string]string{"env": "staging", "portal": "p"}) {
		t.Errorf("tags = %v, tags_all = %v", tags, all)
	}
}

// A refresh that answers the registry but not its tags keeps the tags state
// holds — recording null or {} would plan a spurious change — and warns.
func TestReadKeepsTagsWhenTheyAreUnavailable(t *testing.T) {
	server := newMeAndRegistryServer(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"enabled":true,"endpoint":"e","namespace":"t-abc","tags":null,"tagsUnavailable":true}`)
	})
	defer server.Close()

	s := registrySchema(t)
	state := registryModel(tagsOf(map[string]string{"env": "prod"}), tagsOf(map[string]string{"env": "prod", "portal": "p"}))
	raw := registryRaw(t, state)
	resp := &resource.ReadResponse{State: tfsdk.State{Schema: s, Raw: raw.Copy()}}
	configuredRegistryResource(t, server.URL).Read(context.Background(), resource.ReadRequest{State: tfsdk.State{Schema: s, Raw: raw}}, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("read: %v", resp.Diagnostics)
	}
	if resp.Diagnostics.WarningsCount() != 1 {
		t.Errorf("diagnostics = %v, want one warning", resp.Diagnostics)
	}
	tags, all := stateTags(t, resp.State)
	if !mapsEqual(tags, map[string]string{"env": "prod"}) || !mapsEqual(all, map[string]string{"env": "prod", "portal": "p"}) {
		t.Errorf("tags = %v, tags_all = %v, want the prior values kept", tags, all)
	}
}

// An update cannot compute its write against tags it cannot read: it fails and
// sends nothing, rather than dropping keys Terraform does not manage.
func TestUpdateRefusesWhenTagsAreUnavailable(t *testing.T) {
	server := newMeAndRegistryServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
		_, _ = io.WriteString(w, `{"enabled":true,"endpoint":"e","namespace":"t-abc","tags":null,"tagsUnavailable":true}`)
	})
	defer server.Close()

	s := registrySchema(t)
	state := registryModel(tagsOf(map[string]string{"env": "prod"}), tagsOf(map[string]string{"env": "prod"}))
	plan := registryModel(tagsOf(map[string]string{"env": "staging"}), types.MapUnknown(types.StringType))
	resp := &resource.UpdateResponse{State: tfsdk.State{Schema: s, Raw: registryRaw(t, plan)}}
	configuredRegistryResource(t, server.URL).Update(context.Background(), resource.UpdateRequest{
		Plan: tfsdk.Plan{Schema: s, Raw: registryRaw(t, plan)}, State: tfsdk.State{Schema: s, Raw: registryRaw(t, state)},
	}, resp)
	if !resp.Diagnostics.HasError() {
		t.Fatal("the update must fail while the tags cannot be read")
	}
}
