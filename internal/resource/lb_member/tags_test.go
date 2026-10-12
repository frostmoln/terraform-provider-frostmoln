package lb_member

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
)

const memberPath = "/v1/tenants/t-1/load-balancers/lb-1/pools/pool-1/members/mem-1"

// memberTagsBackend answers member reads with its current tags and records
// each PUT body's raw keys, so `tags: {}` and `clearTags: true` are told apart.
type memberTagsBackend struct {
	tags map[string]string
	puts []map[string]json.RawMessage
}

func (b *memberTagsBackend) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	mem := apiMember{
		ID: "mem-1", PoolID: "pool-1", Name: "node-a", Address: "10.0.0.10", ProtocolPort: 8080,
		SubnetID: "subnet-1", Weight: 5, CreatedAt: "2025-01-01T00:00:00Z",
	}
	switch {
	case r.Method == http.MethodPut && r.URL.Path == memberPath:
		raw, _ := io.ReadAll(r.Body)
		body := map[string]json.RawMessage{}
		_ = json.Unmarshal(raw, &body)
		b.puts = append(b.puts, body)
		if t, ok := body["tags"]; ok {
			b.tags = map[string]string{}
			_ = json.Unmarshal(t, &b.tags)
		}
		if string(body["clearTags"]) == "true" {
			b.tags = nil
		}
		mem.Tags = b.tags
		_ = json.NewEncoder(w).Encode(mem)
	case r.Method == http.MethodGet && r.URL.Path == memberPath:
		mem.Tags = b.tags
		_ = json.NewEncoder(w).Encode(mem)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func memberWithTags(t *testing.T, tags, tagsAll map[string]string) MemberModel {
	t.Helper()
	m := sampleMemberModel()
	m.Tags, m.TagsAll = types.MapNull(types.StringType), types.MapNull(types.StringType)
	if tags != nil {
		m.Tags, _ = types.MapValueFrom(context.Background(), types.StringType, tags)
	}
	if tagsAll != nil {
		m.TagsAll, _ = types.MapValueFrom(context.Background(), types.StringType, tagsAll)
	}
	return m
}

func runMemberUpdate(t *testing.T, b *memberTagsBackend, state, plan MemberModel) MemberModel {
	t.Helper()
	srv := httptest.NewServer(b)
	defer srv.Close()
	c := client.NewClient(srv.URL, "k", client.WithHTTPClient(srv.Client())) // pragma: allowlist secret
	c.SetTenantIDForTest("t-1")
	r := &memberResource{client: c}
	resp := resource.UpdateResponse{State: buildMemberState(t, state)}
	r.Update(context.Background(), resource.UpdateRequest{Plan: buildMemberPlan(t, plan), State: buildMemberState(t, state)}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("update: %v", resp.Diagnostics)
	}
	var out MemberModel
	resp.State.Get(context.Background(), &out)
	return out
}

// TestMemberTagsOnlyUpdateSendsTags: a tags change goes out as `tags`, with the
// keys the configuration does not manage (set outside Terraform) kept, and no
// clearTags beside it (provisioning refuses the two together).
func TestMemberTagsOnlyUpdateSendsTags(t *testing.T) {
	b := &memberTagsBackend{tags: map[string]string{"app": "a", "portal": "p"}}
	plan := memberWithTags(t, map[string]string{"app": "b"}, nil)
	plan.TagsAll = types.MapUnknown(types.StringType)
	out := runMemberUpdate(t, b, memberWithTags(t, map[string]string{"app": "a"}, map[string]string{"app": "a", "portal": "p"}), plan)

	body := b.puts[0]
	if string(body["tags"]) != `{"app":"b","portal":"p"}` {
		t.Errorf("tags = %s, want the new key plus the unmanaged one kept", body["tags"])
	}
	if _, ok := body["clearTags"]; ok {
		t.Error("a tags update also carried clearTags")
	}
	all := map[string]string{}
	out.TagsAll.ElementsAs(context.Background(), &all, false)
	if len(all) != 2 || all["app"] != "b" || all["portal"] != "p" {
		t.Errorf("tags_all = %v", all)
	}
}

// TestMemberClearTagsSendsClearFlag: emptying the set goes out as clearTags,
// never `tags: {}` — an empty map is lost on provisioning's gRPC hop and the
// update would "succeed" with every tag still there.
func TestMemberClearTagsSendsClearFlag(t *testing.T) {
	b := &memberTagsBackend{tags: map[string]string{"app": "a"}}
	plan := memberWithTags(t, nil, nil)
	plan.TagsAll = types.MapUnknown(types.StringType)
	runMemberUpdate(t, b, memberWithTags(t, map[string]string{"app": "a"}, map[string]string{"app": "a"}), plan)

	body := b.puts[0]
	if string(body["clearTags"]) != "true" {
		t.Errorf("clear sent clearTags=%s, want true", body["clearTags"])
	}
	if _, ok := body["tags"]; ok {
		t.Errorf("clear also sent tags=%s", body["tags"])
	}
}

// TestMemberUpdateWithoutTagChangeSendsNoTags: a weight change leaves the tags
// out of the body, so tags set elsewhere are untouched.
func TestMemberUpdateWithoutTagChangeSendsNoTags(t *testing.T) {
	b := &memberTagsBackend{tags: map[string]string{"app": "a"}}
	state := memberWithTags(t, map[string]string{"app": "a"}, map[string]string{"app": "a"})
	plan := state
	plan.Weight = types.Int64Value(9)
	runMemberUpdate(t, b, state, plan)
	if _, ok := b.puts[0]["tags"]; ok {
		t.Errorf("a weight change sent tags=%s", b.puts[0]["tags"])
	}
	if _, ok := b.puts[0]["clearTags"]; ok {
		t.Error("a weight change sent clearTags")
	}
}
