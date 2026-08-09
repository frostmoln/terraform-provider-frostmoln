package lb_health_monitor

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

// tagBody is what a health-monitor write actually put on the wire. hadTags and
// clearSet are probed off the raw envelope because `tags: {}` and
// `clearTags: true` are NOT interchangeable against provisioning, and a struct
// decode would hide which key was present.
type tagBody struct {
	method   string
	path     string
	hadTags  bool
	tags     map[string]string
	clearSet bool
}

func (b *tagBody) capture(r *http.Request) {
	b.method, b.path = r.Method, r.URL.Path
	raw, _ := io.ReadAll(r.Body)
	var envelope map[string]json.RawMessage
	_ = json.Unmarshal(raw, &envelope)
	field, ok := envelope["tags"]
	b.hadTags = ok
	b.tags = map[string]string{}
	if ok {
		_ = json.Unmarshal(field, &b.tags)
	}
	b.clearSet = false
	if field, ok := envelope["clearTags"]; ok {
		_ = json.Unmarshal(field, &b.clearSet)
	}
}

func tagClient(t *testing.T, srv *httptest.Server) *client.Client {
	t.Helper()
	c := client.NewClient(srv.URL, "test-key", client.WithHTTPClient(srv.Client())) // pragma: allowlist secret
	c.SetTenantIDForTest("t-1")
	return c
}

func mapValue(t *testing.T, kv map[string]string) types.Map {
	t.Helper()
	m, diags := types.MapValueFrom(context.Background(), types.StringType, kv)
	if diags.HasError() {
		t.Fatalf("failed to build map: %v", diags.Errors())
	}
	return m
}

const hmPath = "/v1/tenants/t-1/load-balancers/lb-1/pools/pool-1/healthmonitor"

func sampleAPIHM(tags map[string]string) apiHealthMonitor {
	return apiHealthMonitor{
		ID: "hm-1", PoolID: "pool-1", Type: "http", Delay: 5, Timeout: 3, MaxRetries: 3,
		HTTPMethod: "GET", URLPath: "/healthz", ExpectedCodes: "200",
		CreatedAt: "2025-01-01T00:00:00Z", Tags: tags,
	}
}

// TestHealthMonitorCreateSendsTags: a configured `tags` block must reach the
// create body as camelCase `tags`. Before this change HealthMonitorModel had no
// tags attribute at all, so nothing could be configured or sent.
func TestHealthMonitorCreateSendsTags(t *testing.T) {
	var body tagBody
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == hmPath {
			body.capture(r)
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(sampleAPIHM(map[string]string{"env": "prod"}))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	r := &healthMonitorResource{client: tagClient(t, srv)}
	planModel := sampleHMModel()
	planModel.Tags = mapValue(t, map[string]string{"env": "prod"})
	plan := buildHMPlan(t, planModel)

	resp := resource.CreateResponse{State: buildHMState(t, sampleHMModel())}
	r.Create(context.Background(), resource.CreateRequest{Plan: plan}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("create failed: %v", resp.Diagnostics.Errors())
	}

	if !body.hadTags {
		t.Fatal("create body carried no `tags` key")
	}
	if body.tags["env"] != "prod" {
		t.Errorf("tags = %#v, want env=prod", body.tags)
	}

	var got HealthMonitorModel
	resp.State.Get(context.Background(), &got)
	if got.Tags.IsNull() {
		t.Error("tags were not written back into state after create")
	}
}

// TestHealthMonitorUpdateSendsTagsAndSurvivesTheAsyncResponse covers both the
// tags reaching the PUT body (PUT, not PATCH — the gateway routes every
// mutation verb on a /healthmonitor path to provisioning, which registers PUT)
// and the 202 + Operation envelope being polled and re-read rather than parsed
// as a monitor, which would blank every attribute in state.
func TestHealthMonitorUpdateSendsTagsAndSurvivesTheAsyncResponse(t *testing.T) {
	var body tagBody
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPut:
			body.capture(r)
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(client.Operation{OperationID: "op-1", Status: "pending"})
		case r.URL.Path == "/v1/operations/op-1":
			_ = json.NewEncoder(w).Encode(client.Operation{OperationID: "op-1", Status: "completed", ResourceID: "hm-1"})
		case r.Method == http.MethodGet:
			_ = json.NewEncoder(w).Encode(sampleAPIHM(map[string]string{"env": "prod"}))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	r := &healthMonitorResource{client: tagClient(t, srv)}
	state := buildHMState(t, sampleHMModel())
	planModel := sampleHMModel()
	planModel.Tags = mapValue(t, map[string]string{"env": "prod"})
	plan := buildHMPlan(t, planModel)

	resp := resource.UpdateResponse{State: state}
	r.Update(context.Background(), resource.UpdateRequest{Plan: plan, State: state}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("update failed: %v", resp.Diagnostics.Errors())
	}

	if body.method != http.MethodPut {
		t.Errorf("method = %s, want PUT (provisioning's health-monitor update verb)", body.method)
	}
	if body.path != hmPath {
		t.Errorf("path = %s, want %s", body.path, hmPath)
	}
	if !body.hadTags {
		t.Fatal("update body carried no `tags` key")
	}
	if body.clearSet {
		t.Error("update body set clearTags alongside tags; provisioning 400s on the pair")
	}

	var got HealthMonitorModel
	resp.State.Get(context.Background(), &got)
	if got.Type.ValueString() != "http" {
		t.Errorf("type = %q after update; the 202 Operation envelope was parsed as a monitor and blanked state", got.Type.ValueString())
	}
	if got.Tags.IsNull() {
		t.Error("tags are null in state after update; the async response was not re-read")
	}
}

// TestHealthMonitorUpdateClearsTagsWhenRemovedFromConfig: dropping the `tags`
// block must go out as clearTags. An omitted or empty map is collapsed to
// "absent" by proto3 on the gRPC hop to network, so the tags would survive and
// the same removal would replan forever.
func TestHealthMonitorUpdateClearsTagsWhenRemovedFromConfig(t *testing.T) {
	var body tagBody
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPut:
			body.capture(r)
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(client.Operation{OperationID: "op-1", Status: "pending"})
		case r.URL.Path == "/v1/operations/op-1":
			_ = json.NewEncoder(w).Encode(client.Operation{OperationID: "op-1", Status: "completed", ResourceID: "hm-1"})
		case r.Method == http.MethodGet:
			_ = json.NewEncoder(w).Encode(sampleAPIHM(nil))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	r := &healthMonitorResource{client: tagClient(t, srv)}
	stateModel := sampleHMModel()
	stateModel.Tags = mapValue(t, map[string]string{"env": "prod"})
	state := buildHMState(t, stateModel)

	planModel := sampleHMModel()
	planModel.Tags = types.MapNull(types.StringType)
	plan := buildHMPlan(t, planModel)

	resp := resource.UpdateResponse{State: state}
	r.Update(context.Background(), resource.UpdateRequest{Plan: plan, State: state}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("update failed: %v", resp.Diagnostics.Errors())
	}

	if !body.clearSet {
		t.Error("removing the tags block did not send clearTags; the tags would silently survive")
	}
	if body.hadTags {
		t.Errorf("body carried `tags` (%#v) alongside clearTags; provisioning 400s on the pair", body.tags)
	}
}

// TestHealthMonitorReadDetectsTagDrift: a tag changed outside Terraform must
// land in state on refresh, which is what makes the next plan show the drift.
func TestHealthMonitorReadDetectsTagDrift(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(sampleAPIHM(map[string]string{"env": "staging"}))
	}))
	defer srv.Close()

	r := &healthMonitorResource{client: tagClient(t, srv)}
	stateModel := sampleHMModel()
	stateModel.Tags = mapValue(t, map[string]string{"env": "prod"})
	state := buildHMState(t, stateModel)

	resp := resource.ReadResponse{State: state}
	r.Read(context.Background(), resource.ReadRequest{State: state}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("read failed: %v", resp.Diagnostics.Errors())
	}

	var got HealthMonitorModel
	resp.State.Get(context.Background(), &got)
	tags := map[string]string{}
	got.Tags.ElementsAs(context.Background(), &tags, false)
	if tags["env"] != "staging" {
		t.Errorf("state tags = %#v after refresh, want the server's env=staging — drift is invisible", tags)
	}
}

// TestHealthMonitorReadUntaggedIsNullNotEmpty: an untagged monitor must read
// back as a NULL map, or it diffs forever against a config with no tags block.
func TestHealthMonitorReadUntaggedIsNullNotEmpty(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(sampleAPIHM(nil))
	}))
	defer srv.Close()

	r := &healthMonitorResource{client: tagClient(t, srv)}
	state := buildHMState(t, sampleHMModel())
	resp := resource.ReadResponse{State: state}
	r.Read(context.Background(), resource.ReadRequest{State: state}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("read failed: %v", resp.Diagnostics.Errors())
	}

	var got HealthMonitorModel
	resp.State.Get(context.Background(), &got)
	if !got.Tags.IsNull() {
		t.Errorf("untagged monitor read back as %#v, want a null map", got.Tags)
	}
}

// TestHealthMonitorSchemaHasTags pins the attribute itself.
func TestHealthMonitorSchemaHasTags(t *testing.T) {
	var schemaResp resource.SchemaResponse
	NewResource().Schema(context.Background(), resource.SchemaRequest{}, &schemaResp)
	attr, ok := schemaResp.Schema.Attributes["tags"]
	if !ok {
		t.Fatal("frostmoln_lb_health_monitor has no `tags` attribute")
	}
	if !attr.IsOptional() {
		t.Error("`tags` must be Optional")
	}
}
