package lb_pool

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

// tagBody is what a pool write actually put on the wire.
//
// hadTags and clearTags are probed off the raw envelope rather than decoded
// into apiUpdatePoolRequest, because the question these tests answer is which
// KEY the body carried: `tags: {}` and `clearTags: true` are not
// interchangeable against provisioning, and a struct decode hides the
// difference.
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

// TestPoolCreateSendsTags: a configured `tags` block must reach the create body
// as camelCase `tags`. Before this change PoolModel had no tags attribute at
// all, so nothing could be configured and nothing was ever sent.
func TestPoolCreateSendsTags(t *testing.T) {
	var body tagBody
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			body.capture(r)
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(apiPool{
				ID: "pool-new", LoadBalancerID: "lb-1", Name: "pool", Protocol: "http",
				LBAlgorithm: "round_robin", ProxyProtocol: "none", CreatedAt: "2025-01-01T00:00:00Z",
				Tags: map[string]string{"env": "prod"},
			})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	r := &poolResource{client: tagClient(t, srv)}
	plan := buildPoolPlan(t, PoolModel{
		LoadBalancerID: types.StringValue("lb-1"),
		ListenerID:     types.StringNull(),
		Name:           types.StringValue("pool"),
		Protocol:       types.StringValue("http"),
		LBAlgorithm:    types.StringValue("round_robin"),
		ProxyProtocol:  types.StringValue("none"),
		Tags:           mapValue(t, map[string]string{"env": "prod"}),
	})
	resp := resource.CreateResponse{State: buildPoolState(t, samplePoolModel())}
	r.Create(context.Background(), resource.CreateRequest{Plan: plan}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("create failed: %v", resp.Diagnostics.Errors())
	}

	if body.path != "/v1/tenants/t-1/load-balancers/lb-1/pools" {
		t.Errorf("path = %s", body.path)
	}
	if !body.hadTags {
		t.Fatal("create body carried no `tags` key")
	}
	if body.tags["env"] != "prod" {
		t.Errorf("tags = %#v, want env=prod", body.tags)
	}

	var got PoolModel
	resp.State.Get(context.Background(), &got)
	if got.Tags.IsNull() {
		t.Error("tags were not written back into state after create")
	}
}

// TestPoolUpdateSendsTagsAndSurvivesTheAsyncResponse covers BOTH halves of the
// update contract:
//
//   - the tags reach the PUT body (PUT, not PATCH — the gateway sends every
//     mutation verb on a /pools path to provisioning, which registers PUT);
//   - the 202 + Operation envelope provisioning answers with is polled and
//     re-read, not parsed as a pool. Parsing it as a pool yields a zero-valued
//     object, so every attribute — tags included — would be blanked in state and
//     Terraform would report an inconsistent result after apply.
func TestPoolUpdateSendsTagsAndSurvivesTheAsyncResponse(t *testing.T) {
	var body tagBody
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPut:
			body.capture(r)
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(client.Operation{
				OperationID: "op-1", Status: "pending", ResourceType: "load_balancer_pool",
			})
		case r.URL.Path == "/v1/tenants/t-1/operations/op-1":
			_ = json.NewEncoder(w).Encode(client.Operation{
				OperationID: "op-1", Status: "completed", ResourceID: "pool-1",
			})
		case r.Method == http.MethodGet:
			_ = json.NewEncoder(w).Encode(apiPool{
				ID: "pool-1", LoadBalancerID: "lb-1", Name: "pool", Protocol: "http",
				LBAlgorithm: "round_robin", ProxyProtocol: "none", CreatedAt: "2025-01-01T00:00:00Z",
				Tags: map[string]string{"env": "prod"},
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	r := &poolResource{client: tagClient(t, srv)}
	state := buildPoolState(t, samplePoolModel())
	planModel := samplePoolModel()
	planModel.Tags = mapValue(t, map[string]string{"env": "prod"})
	plan := buildPoolPlan(t, planModel)

	resp := resource.UpdateResponse{State: state}
	r.Update(context.Background(), resource.UpdateRequest{Plan: plan, State: state}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("update failed: %v", resp.Diagnostics.Errors())
	}

	if body.method != http.MethodPut {
		t.Errorf("method = %s, want PUT (provisioning's pool-update verb)", body.method)
	}
	if !body.hadTags {
		t.Fatal("update body carried no `tags` key")
	}
	if body.clearSet {
		t.Error("update body set clearTags alongside tags; provisioning 400s on the pair")
	}
	if body.tags["env"] != "prod" {
		t.Errorf("tags = %#v, want env=prod", body.tags)
	}

	var got PoolModel
	resp.State.Get(context.Background(), &got)
	if got.Name.ValueString() != "pool" {
		t.Errorf("name = %q after update; the 202 Operation envelope was parsed as a pool and blanked state", got.Name.ValueString())
	}
	if got.Tags.IsNull() {
		t.Error("tags are null in state after update; the async response was not re-read")
	}
}

// TestPoolUpdateClearsTagsWhenRemovedFromConfig: dropping the `tags` block must
// go out as clearTags, NOT as an omission. Provisioning forwards the map to
// network over gRPC, where proto3 cannot distinguish an absent map from an
// empty one — so an omitted or empty `tags` reads as "leave them alone", the
// tags survive, and every subsequent plan shows the same pending removal.
func TestPoolUpdateClearsTagsWhenRemovedFromConfig(t *testing.T) {
	var body tagBody
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPut:
			body.capture(r)
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(client.Operation{OperationID: "op-1", Status: "pending"})
		case r.URL.Path == "/v1/tenants/t-1/operations/op-1":
			_ = json.NewEncoder(w).Encode(client.Operation{OperationID: "op-1", Status: "completed", ResourceID: "pool-1"})
		case r.Method == http.MethodGet:
			_ = json.NewEncoder(w).Encode(apiPool{
				ID: "pool-1", LoadBalancerID: "lb-1", Name: "pool", Protocol: "http",
				LBAlgorithm: "round_robin", ProxyProtocol: "none", CreatedAt: "2025-01-01T00:00:00Z",
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	r := &poolResource{client: tagClient(t, srv)}
	stateModel := samplePoolModel()
	stateModel.Tags = mapValue(t, map[string]string{"env": "prod"})
	state := buildPoolState(t, stateModel)

	planModel := samplePoolModel()
	planModel.Tags = types.MapNull(types.StringType)
	plan := buildPoolPlan(t, planModel)

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

// TestPoolReadDetectsTagDrift: a tag changed outside Terraform must land in
// state on refresh, which is what makes the next plan show the drift.
func TestPoolReadDetectsTagDrift(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(apiPool{
			ID: "pool-1", LoadBalancerID: "lb-1", Name: "pool", Protocol: "http",
			LBAlgorithm: "round_robin", ProxyProtocol: "none", CreatedAt: "2025-01-01T00:00:00Z",
			Tags: map[string]string{"env": "staging"},
		})
	}))
	defer srv.Close()

	r := &poolResource{client: tagClient(t, srv)}
	stateModel := samplePoolModel()
	stateModel.Tags = mapValue(t, map[string]string{"env": "prod"})
	state := buildPoolState(t, stateModel)

	resp := resource.ReadResponse{State: state}
	r.Read(context.Background(), resource.ReadRequest{State: state}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("read failed: %v", resp.Diagnostics.Errors())
	}

	var got PoolModel
	resp.State.Get(context.Background(), &got)
	tags := map[string]string{}
	got.Tags.ElementsAs(context.Background(), &tags, false)
	if tags["env"] != "staging" {
		t.Errorf("state tags = %#v after refresh, want the server's env=staging — drift is invisible", tags)
	}
}

// TestPoolReadUntaggedIsNullNotEmpty: an untagged pool must read back as a NULL
// map. An empty-but-present map would diff forever against a config with no
// `tags` block.
func TestPoolReadUntaggedIsNullNotEmpty(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(apiPool{
			ID: "pool-1", LoadBalancerID: "lb-1", Name: "pool", Protocol: "http",
			LBAlgorithm: "round_robin", ProxyProtocol: "none", CreatedAt: "2025-01-01T00:00:00Z",
		})
	}))
	defer srv.Close()

	r := &poolResource{client: tagClient(t, srv)}
	state := buildPoolState(t, samplePoolModel())
	resp := resource.ReadResponse{State: state}
	r.Read(context.Background(), resource.ReadRequest{State: state}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("read failed: %v", resp.Diagnostics.Errors())
	}

	var got PoolModel
	resp.State.Get(context.Background(), &got)
	if !got.Tags.IsNull() {
		t.Errorf("untagged pool read back as %#v, want a null map", got.Tags)
	}
}

// TestPoolSchemaHasTags pins the attribute itself, so removing it from the
// schema fails here rather than only in the wire tests.
func TestPoolSchemaHasTags(t *testing.T) {
	var schemaResp resource.SchemaResponse
	NewResource().Schema(context.Background(), resource.SchemaRequest{}, &schemaResp)
	attr, ok := schemaResp.Schema.Attributes["tags"]
	if !ok {
		t.Fatal("frostmoln_lb_pool has no `tags` attribute")
	}
	if !attr.IsOptional() {
		t.Error("`tags` must be Optional")
	}
}
