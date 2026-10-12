package dns_record

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/tftags"
)

const recordPath = "/v1/tenants/t-1/dns/zones/z-1/records/rec-1"

// recordBackend records every write's raw JSON keys (so an absent field and a
// zero one differ) and answers with the recordset and its current tags.
type recordBackend struct {
	tags   map[string]string
	writes []map[string]json.RawMessage
}

func (b *recordBackend) record() apiDNSRecord {
	return apiDNSRecord{
		ID: "rec-1", ZoneID: "z-1", Name: "www", Type: "A", Records: []string{"203.0.113.10"}, TTL: 300,
		Tags: b.tags, CreatedAt: "2026-10-12T00:00:00Z",
	}
}

func (b *recordBackend) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	raw, _ := io.ReadAll(r.Body)
	body := map[string]json.RawMessage{}
	_ = json.Unmarshal(raw, &body)
	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/v1/tenants/t-1/dns/zones/z-1/records":
		b.writes = append(b.writes, body)
		b.tags = map[string]string{}
		_ = json.Unmarshal(body["tags"], &b.tags)
		b.tags["tenant-default"] = "td"
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(b.record())
	case r.Method == http.MethodPut && r.URL.Path == recordPath:
		b.writes = append(b.writes, body)
		if t, ok := body["tags"]; ok {
			b.tags = map[string]string{}
			_ = json.Unmarshal(t, &b.tags)
		}
		_ = json.NewEncoder(w).Encode(b.record())
	case r.Method == http.MethodGet && r.URL.Path == recordPath:
		_ = json.NewEncoder(w).Encode(b.record())
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func recordResource(t *testing.T, b *recordBackend, defaults map[string]string) (*dnsRecordResource, func()) {
	t.Helper()
	srv := httptest.NewServer(b)
	c := client.NewClient(srv.URL, "k", client.WithHTTPClient(srv.Client()), // pragma: allowlist secret
		client.WithDefaultTags(tftags.Defaults{Tags: defaults}))
	c.SetTenantIDForTest("t-1")
	return &dnsRecordResource{client: c}, srv.Close
}

func tagMap(t *testing.T, m map[string]string) types.Map {
	t.Helper()
	if m == nil {
		return types.MapNull(types.StringType)
	}
	v, d := types.MapValueFrom(context.Background(), types.StringType, m)
	if d.HasError() {
		t.Fatalf("map: %v", d)
	}
	return v
}

func steadyRecord(t *testing.T, tags, tagsAll map[string]string) DNSRecordModel {
	return DNSRecordModel{
		ID: types.StringValue("rec-1"), ZoneID: types.StringValue("z-1"), Name: types.StringValue("www"),
		Type: types.StringValue("A"), Records: recordSet(t, "203.0.113.10"), TTL: types.Int64Value(300),
		Comment: types.StringNull(), CreatedAt: types.StringValue("2026-10-12T00:00:00Z"), UpdatedAt: types.StringNull(),
		Tags: tagMap(t, tags), TagsAll: tagMap(t, tagsAll),
	}
}

func recSchemaState(t *testing.T, m DNSRecordModel) tfsdk.State {
	t.Helper()
	var sr resource.SchemaResponse
	NewResource().Schema(context.Background(), resource.SchemaRequest{}, &sr)
	s := tfsdk.State{Schema: sr.Schema}
	if d := s.Set(context.Background(), &m); d.HasError() {
		t.Fatalf("state: %v", d)
	}
	return s
}

func recPlan(t *testing.T, m DNSRecordModel) tfsdk.Plan {
	t.Helper()
	st := recSchemaState(t, m)
	return tfsdk.Plan(st)
}

func keysOf(m map[string]json.RawMessage) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// TestDNSRecordCreateSendsMergedTags: default_tags ∪ tags reach the create, and
// the key the platform merged in lands in tags_all only.
func TestDNSRecordCreateSendsMergedTags(t *testing.T) {
	b := &recordBackend{}
	r, done := recordResource(t, b, map[string]string{"env": "prod"})
	defer done()

	plan := steadyRecord(t, map[string]string{"app": "a"}, nil)
	plan.ID, plan.CreatedAt, plan.UpdatedAt = types.StringUnknown(), types.StringUnknown(), types.StringUnknown()
	plan.TagsAll = types.MapUnknown(types.StringType)
	resp := resource.CreateResponse{State: recSchemaState(t, steadyRecord(t, nil, nil))}
	r.Create(context.Background(), resource.CreateRequest{Plan: recPlan(t, plan)}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("create: %v", resp.Diagnostics)
	}
	got := map[string]string{}
	_ = json.Unmarshal(b.writes[0]["tags"], &got)
	if !tftags.Equal(got, map[string]string{"env": "prod", "app": "a"}) {
		t.Errorf("create sent tags %v", got)
	}
	var st DNSRecordModel
	resp.State.Get(context.Background(), &st)
	all := map[string]string{}
	st.TagsAll.ElementsAs(context.Background(), &all, false)
	if !tftags.Equal(all, map[string]string{"env": "prod", "app": "a", "tenant-default": "td"}) {
		t.Errorf("tags_all = %v", all)
	}
	own := map[string]string{}
	st.Tags.ElementsAs(context.Background(), &own, false)
	if !tftags.Equal(own, map[string]string{"app": "a"}) {
		t.Errorf("tags = %v", own)
	}
}

// TestDNSRecordTagsOnlyUpdateSendsOnlyTags: a tags change alone must not resend
// the record's values, TTL or comment — the platform writes the zone (and bumps
// its serial) for those, and for tags alone it does not.
func TestDNSRecordTagsOnlyUpdateSendsOnlyTags(t *testing.T) {
	b := &recordBackend{tags: map[string]string{"app": "a"}}
	r, done := recordResource(t, b, nil)
	defer done()

	state := steadyRecord(t, map[string]string{"app": "a"}, map[string]string{"app": "a"})
	plan := steadyRecord(t, map[string]string{"app": "b"}, nil)
	plan.TagsAll = types.MapUnknown(types.StringType)
	resp := resource.UpdateResponse{State: recSchemaState(t, state)}
	r.Update(context.Background(), resource.UpdateRequest{Plan: recPlan(t, plan), State: recSchemaState(t, state)}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("update: %v", resp.Diagnostics)
	}
	if len(b.writes) != 1 {
		t.Fatalf("%d writes, want 1", len(b.writes))
	}
	if keys := keysOf(b.writes[0]); len(keys) != 1 || keys[0] != "tags" {
		t.Errorf("tags-only PUT carried %v, want [tags]", keys)
	}
	if string(b.writes[0]["tags"]) != `{"app":"b"}` {
		t.Errorf("tags = %s", b.writes[0]["tags"])
	}
}

// TestDNSRecordClearTagsSendsEmptyMap: removing every tag sends `tags: {}`.
func TestDNSRecordClearTagsSendsEmptyMap(t *testing.T) {
	b := &recordBackend{tags: map[string]string{"app": "a"}}
	r, done := recordResource(t, b, nil)
	defer done()

	state := steadyRecord(t, map[string]string{"app": "a"}, map[string]string{"app": "a"})
	plan := steadyRecord(t, nil, nil)
	plan.TagsAll = types.MapUnknown(types.StringType)
	resp := resource.UpdateResponse{State: recSchemaState(t, state)}
	r.Update(context.Background(), resource.UpdateRequest{Plan: recPlan(t, plan), State: recSchemaState(t, state)}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("update: %v", resp.Diagnostics)
	}
	if got := string(b.writes[0]["tags"]); got != "{}" {
		t.Errorf("clear sent tags=%q, want {}", got)
	}
}

// TestDNSRecordValueChangeLeavesTagsAlone: a value change with no tag change
// sends the record's fields and no `tags` key, so tags set outside Terraform
// are not overwritten.
func TestDNSRecordValueChangeLeavesTagsAlone(t *testing.T) {
	b := &recordBackend{tags: map[string]string{"app": "a", "portal": "p"}}
	r, done := recordResource(t, b, nil)
	defer done()

	state := steadyRecord(t, map[string]string{"app": "a"}, map[string]string{"app": "a", "portal": "p"})
	plan := steadyRecord(t, map[string]string{"app": "a"}, map[string]string{"app": "a", "portal": "p"})
	plan.TTL = types.Int64Value(600)
	resp := resource.UpdateResponse{State: recSchemaState(t, state)}
	r.Update(context.Background(), resource.UpdateRequest{Plan: recPlan(t, plan), State: recSchemaState(t, state)}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("update: %v", resp.Diagnostics)
	}
	if _, ok := b.writes[0]["tags"]; ok {
		t.Errorf("a value change sent tags: %s", b.writes[0]["tags"])
	}
	if _, ok := b.writes[0]["ttl"]; !ok {
		t.Error("the TTL change was not sent")
	}
}
