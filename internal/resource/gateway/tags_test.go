package gateway

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/tftags"
)

// tagsBackend is a gateway API that records every write body as raw JSON keys,
// so a test can tell an absent field from a zero one, and answers with the
// gateway carrying its current tags plus serverStamped (a tenant default the
// platform merged in at create).
type tagsBackend struct {
	mu     sync.Mutex
	tags   map[string]string
	writes []map[string]json.RawMessage
	gets   int
}

func (b *tagsBackend) gateway() apiGateway {
	return apiGateway{
		ID: "gw-1", VPCID: "vpc-1", Mode: ModePublicIP, Status: "active", Origin: "explicit",
		SourceAddress: "198.51.100.7", Tags: b.tags,
	}
}

func (b *tagsBackend) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b.mu.Lock()
	defer b.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	raw, _ := io.ReadAll(r.Body)
	body := map[string]json.RawMessage{}
	_ = json.Unmarshal(raw, &body)
	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/v1/tenants/t-1/gateways":
		b.writes = append(b.writes, body)
		b.tags = map[string]string{"tenant-default": "td"}
		if t, ok := body["tags"]; ok {
			_ = json.Unmarshal(t, &b.tags)
			b.tags["tenant-default"] = "td"
		}
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(b.gateway())
	case r.Method == http.MethodPatch && r.URL.Path == "/v1/tenants/t-1/gateways/gw-1":
		b.writes = append(b.writes, body)
		if t, ok := body["tags"]; ok {
			b.tags = map[string]string{}
			_ = json.Unmarshal(t, &b.tags)
		}
		_ = json.NewEncoder(w).Encode(b.gateway())
	case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-1/gateways/gw-1":
		b.gets++
		_ = json.NewEncoder(w).Encode(b.gateway())
	case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-1/gateways":
		b.gets++
		_ = json.NewEncoder(w).Encode(apiGatewayList{Gateways: []apiGateway{b.gateway()}, TotalCount: 1})
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func (b *tagsBackend) lastWrite(t *testing.T) map[string]json.RawMessage {
	t.Helper()
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.writes) == 0 {
		t.Fatal("no write reached the API")
	}
	return b.writes[len(b.writes)-1]
}

func tagsResource(t *testing.T, b *tagsBackend, defaults map[string]string) (*gatewayResource, func()) {
	t.Helper()
	srv := httptest.NewServer(b)
	c := client.NewClient(srv.URL, "k", client.WithHTTPClient(srv.Client()), // pragma: allowlist secret
		client.WithDefaultTags(tftags.Defaults{Tags: defaults}))
	c.SetTenantIDForTest("t-1")
	return &gatewayResource{client: c}, srv.Close
}

func strMap(t *testing.T, m map[string]string) types.Map {
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

func mapOf(t *testing.T, m types.Map) map[string]string {
	t.Helper()
	if m.IsNull() || m.IsUnknown() {
		return nil
	}
	out := map[string]string{}
	if d := m.ElementsAs(context.Background(), &out, false); d.HasError() {
		t.Fatalf("map: %v", d)
	}
	return out
}

// steadyGateway is gw-1 as state holds it after a create with the given tags.
func steadyGateway(t *testing.T, tags, tagsAll map[string]string) GatewayModel {
	return GatewayModel{
		ID: types.StringValue("gw-1"), VPCID: types.StringValue("vpc-1"), Mode: types.StringValue(ModePublicIP),
		SourceAddress: types.StringValue("198.51.100.7"), Status: types.StringValue("active"),
		Origin: types.StringValue("explicit"), PublicIPID: types.StringNull(),
		AcknowledgeConnectivityLoss: types.BoolNull(),
		Tags:                        strMap(t, tags), TagsAll: strMap(t, tagsAll),
	}
}

func gwPlan(t *testing.T, m GatewayModel) tfsdk.Plan {
	t.Helper()
	p := tfsdk.Plan{Schema: gwSchema(t)}
	if d := p.Set(context.Background(), &m); d.HasError() {
		t.Fatalf("plan: %v", d)
	}
	return p
}

func gwState(t *testing.T, m GatewayModel) tfsdk.State {
	t.Helper()
	s := tfsdk.State{Schema: gwSchema(t)}
	if d := s.Set(context.Background(), &m); d.HasError() {
		t.Fatalf("state: %v", d)
	}
	return s
}

func sentTags(t *testing.T, body map[string]json.RawMessage) (map[string]string, bool) {
	t.Helper()
	raw, ok := body["tags"]
	if !ok {
		return nil, false
	}
	out := map[string]string{}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("tags on the wire are not a map: %s", raw)
	}
	return out, true
}

// TestGatewayCreateSendsMergedTags: the create body carries default_tags ∪ tags
// (a resource key wins), and a key the platform merged in (a tenant default)
// lands in tags_all, never in tags.
func TestGatewayCreateSendsMergedTags(t *testing.T) {
	b := &tagsBackend{}
	r, done := tagsResource(t, b, map[string]string{"env": "prod", "team": "ops"})
	defer done()

	plan := steadyGateway(t, map[string]string{"team": "web"}, nil)
	plan.ID, plan.SourceAddress, plan.Status, plan.Origin = types.StringUnknown(), types.StringUnknown(), types.StringUnknown(), types.StringUnknown()
	plan.TagsAll = types.MapUnknown(types.StringType)
	resp := resource.CreateResponse{State: gwState(t, steadyGateway(t, nil, nil))}
	r.Create(context.Background(), resource.CreateRequest{Plan: gwPlan(t, plan)}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("create: %v", resp.Diagnostics)
	}

	got, ok := sentTags(t, b.lastWrite(t))
	if !ok || !tftags.Equal(got, map[string]string{"env": "prod", "team": "web"}) {
		t.Errorf("create sent tags %v (present=%v), want default_tags merged with tags, the resource key winning", got, ok)
	}
	var st GatewayModel
	resp.State.Get(context.Background(), &st)
	if want := map[string]string{"team": "web"}; !tftags.Equal(mapOf(t, st.Tags), want) {
		t.Errorf("tags = %v, want %v", mapOf(t, st.Tags), want)
	}
	if want := map[string]string{"env": "prod", "team": "web", "tenant-default": "td"}; !tftags.Equal(mapOf(t, st.TagsAll), want) {
		t.Errorf("tags_all = %v, want %v (the server-stamped key included)", mapOf(t, st.TagsAll), want)
	}
}

// TestGatewayTagsOnlyUpdateSendsOnlyTags is the safety property of this
// change: a tags edit must not read as a mode change. The PATCH carries `tags`
// and nothing else — no mode, no acknowledgement, no public IP — so it needs
// no acknowledge_connectivity_loss and the platform never touches the VPC's
// connectivity for it.
func TestGatewayTagsOnlyUpdateSendsOnlyTags(t *testing.T) {
	b := &tagsBackend{tags: map[string]string{"app": "a", "tenant-default": "td"}}
	r, done := tagsResource(t, b, nil)
	defer done()

	state := steadyGateway(t, map[string]string{"app": "a"}, map[string]string{"app": "a", "tenant-default": "td"})
	plan := steadyGateway(t, map[string]string{"app": "b"}, nil)
	plan.TagsAll = types.MapUnknown(types.StringType)
	resp := resource.UpdateResponse{State: gwState(t, state)}
	r.Update(context.Background(), resource.UpdateRequest{Plan: gwPlan(t, plan), State: gwState(t, state)}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("a tags-only update must need no acknowledgement: %v", resp.Diagnostics)
	}

	body := b.lastWrite(t)
	if len(body) != 1 {
		keys := make([]string, 0, len(body))
		for k := range body {
			keys = append(keys, k)
		}
		t.Fatalf("tags-only PATCH carried %v, want `tags` alone", keys)
	}
	// The tenant default the configuration does not manage is kept.
	if got, _ := sentTags(t, body); !tftags.Equal(got, map[string]string{"app": "b", "tenant-default": "td"}) {
		t.Errorf("PATCH tags = %v, want the new key plus the unmanaged one kept", got)
	}
	var st GatewayModel
	resp.State.Get(context.Background(), &st)
	if !tftags.Equal(mapOf(t, st.TagsAll), map[string]string{"app": "b", "tenant-default": "td"}) {
		t.Errorf("tags_all = %v", mapOf(t, st.TagsAll))
	}
	if st.SourceAddress.ValueString() != "198.51.100.7" || st.Mode.ValueString() != ModePublicIP {
		t.Errorf("observed values changed on a tags-only update: %+v", st)
	}
}

// TestGatewayClearTagsSendsEmptyMap: removing every tag sends `tags: {}` — an
// explicit clear, not an omitted field (which would leave them as they are).
func TestGatewayClearTagsSendsEmptyMap(t *testing.T) {
	b := &tagsBackend{tags: map[string]string{"app": "a"}}
	r, done := tagsResource(t, b, nil)
	defer done()

	state := steadyGateway(t, map[string]string{"app": "a"}, map[string]string{"app": "a"})
	plan := steadyGateway(t, nil, nil)
	plan.TagsAll = types.MapUnknown(types.StringType)
	resp := resource.UpdateResponse{State: gwState(t, state)}
	r.Update(context.Background(), resource.UpdateRequest{Plan: gwPlan(t, plan), State: gwState(t, state)}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("update: %v", resp.Diagnostics)
	}
	body := b.lastWrite(t)
	if raw := string(body["tags"]); raw != "{}" {
		t.Errorf("clear sent tags=%q, want {}", raw)
	}
	if _, ok := body["mode"]; ok {
		t.Error("a clear carried a mode")
	}
}

// TestGatewayModeChangeCarriesTags: a mode change and a tags change in one
// apply go out as ONE PATCH with the acknowledgement and the tags.
func TestGatewayModeChangeCarriesTags(t *testing.T) {
	b := &tagsBackend{tags: map[string]string{"app": "a"}}
	r, done := tagsResource(t, b, nil)
	defer done()

	state := steadyGateway(t, map[string]string{"app": "a"}, map[string]string{"app": "a"})
	state.Mode = types.StringValue(otherMode)
	plan := steadyGateway(t, map[string]string{"app": "b"}, nil)
	plan.AcknowledgeConnectivityLoss = types.BoolValue(true)
	plan.TagsAll = types.MapUnknown(types.StringType)
	plan.SourceAddress, plan.Status, plan.Origin = types.StringUnknown(), types.StringUnknown(), types.StringUnknown()
	resp := resource.UpdateResponse{State: gwState(t, state)}
	r.Update(context.Background(), resource.UpdateRequest{Plan: gwPlan(t, plan), State: gwState(t, state)}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("update: %v", resp.Diagnostics)
	}
	body := b.lastWrite(t)
	if string(body["mode"]) != `"public_ip"` || string(body["acknowledgeConnectivityLoss"]) != "true" {
		t.Errorf("mode PATCH = %v, want the mode and the acknowledgement", body)
	}
	if got, _ := sentTags(t, body); !tftags.Equal(got, map[string]string{"app": "b"}) {
		t.Errorf("mode PATCH tags = %v, want the new set", got)
	}
}

// TestGatewayModeChangeStillNeedsAcknowledgement: tags riding along do not
// relax the acknowledgement a mode change needs, and nothing is sent.
func TestGatewayModeChangeStillNeedsAcknowledgement(t *testing.T) {
	b := &tagsBackend{tags: map[string]string{"app": "a"}}
	r, done := tagsResource(t, b, nil)
	defer done()

	state := steadyGateway(t, map[string]string{"app": "a"}, map[string]string{"app": "a"})
	state.Mode = types.StringValue(otherMode)
	plan := steadyGateway(t, map[string]string{"app": "b"}, nil)
	plan.TagsAll = types.MapUnknown(types.StringType)
	resp := resource.UpdateResponse{State: gwState(t, state)}
	r.Update(context.Background(), resource.UpdateRequest{Plan: gwPlan(t, plan), State: gwState(t, state)}, &resp)
	if !resp.Diagnostics.HasError() {
		t.Fatal("a mode change without the acknowledgement was accepted")
	}
	if len(b.writes) != 0 || b.gets != 0 {
		t.Errorf("a refused mode change still reached the API: %d writes, %d reads", len(b.writes), b.gets)
	}
}

// TestGatewayReadKeepsServerStampedKeysOutOfTags: a key the platform holds that
// the configuration never set reads back into tags_all only, so it plans no diff.
func TestGatewayReadKeepsServerStampedKeysOutOfTags(t *testing.T) {
	b := &tagsBackend{tags: map[string]string{"app": "a", "tenant-default": "td", "Frostmoln_Legacy": "x"}}
	r, done := tagsResource(t, b, nil)
	defer done()

	state := steadyGateway(t, map[string]string{"app": "a"}, map[string]string{"app": "a"})
	resp := resource.ReadResponse{State: gwState(t, state)}
	r.Read(context.Background(), resource.ReadRequest{State: gwState(t, state)}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("read: %v", resp.Diagnostics)
	}
	var st GatewayModel
	resp.State.Get(context.Background(), &st)
	if !tftags.Equal(mapOf(t, st.Tags), map[string]string{"app": "a"}) {
		t.Errorf("tags = %v, want only the configured key", mapOf(t, st.Tags))
	}
	if !tftags.Equal(mapOf(t, st.TagsAll), map[string]string{"app": "a", "tenant-default": "td"}) {
		t.Errorf("tags_all = %v, want the stamped key and no reserved one", mapOf(t, st.TagsAll))
	}
}
