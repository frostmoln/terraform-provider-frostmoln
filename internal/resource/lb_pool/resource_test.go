package lb_pool

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
)

func TestPoolModelFromAPI(t *testing.T) {
	p := &apiPool{
		ID:             "pool-1",
		LoadBalancerID: "lb-1",
		ListenerID:     "lst-1",
		Name:           "backend",
		Protocol:       "http",
		LBAlgorithm:    "round_robin",
		ProxyProtocol:  "v2",
		SessionPersistence: &apiSessionPersistence{
			Type:               "APP_COOKIE",
			CookieName:         "SESSIONID",
			PersistenceTimeout: 3600,
		},
		CreatedAt: "2025-01-01T00:00:00Z",
	}

	var model PoolModel
	var diags diag.Diagnostics
	model.fromAPI(context.Background(), p, &diags)
	if diags.HasError() {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}
	if model.ID.ValueString() != "pool-1" {
		t.Errorf("expected ID pool-1, got %s", model.ID.ValueString())
	}
	if model.LBAlgorithm.ValueString() != "round_robin" {
		t.Errorf("expected round_robin, got %s", model.LBAlgorithm.ValueString())
	}
	if model.ProxyProtocol.ValueString() != "v2" {
		t.Errorf("expected v2, got %s", model.ProxyProtocol.ValueString())
	}
	if model.SessionPersistence == nil {
		t.Fatalf("expected session_persistence to be set")
	}
	if model.SessionPersistence.Type.ValueString() != "APP_COOKIE" {
		t.Errorf("expected APP_COOKIE, got %s", model.SessionPersistence.Type.ValueString())
	}
	if model.SessionPersistence.CookieName.ValueString() != "SESSIONID" {
		t.Errorf("expected SESSIONID, got %s", model.SessionPersistence.CookieName.ValueString())
	}
	if model.SessionPersistence.PersistenceTimeout.ValueInt64() != 3600 {
		t.Errorf("expected 3600, got %d", model.SessionPersistence.PersistenceTimeout.ValueInt64())
	}
}

// TestPoolToCreateRequestSessionPersistence verifies session_persistence is
// wired into the create request (M2 regression guard).
func TestPoolToCreateRequestSessionPersistence(t *testing.T) {
	m := &PoolModel{
		Name:        types.StringValue("backend"),
		Protocol:    types.StringValue("http"),
		LBAlgorithm: types.StringValue("round_robin"),
		SessionPersistence: &SessionPersistenceModel{
			Type:               types.StringValue("APP_COOKIE"),
			CookieName:         types.StringValue("SESSIONID"),
			PersistenceTimeout: types.Int64Value(60),
		},
	}
	req := m.toCreateRequest(context.Background(), &diag.Diagnostics{})
	if req.SessionPersistence == nil {
		t.Fatalf("expected session_persistence in create request")
	}
	if req.SessionPersistence.Type != "APP_COOKIE" {
		t.Errorf("expected APP_COOKIE, got %s", req.SessionPersistence.Type)
	}
	if req.SessionPersistence.CookieName != "SESSIONID" {
		t.Errorf("expected SESSIONID, got %s", req.SessionPersistence.CookieName)
	}
	if req.SessionPersistence.PersistenceTimeout != 60 {
		t.Errorf("expected 60, got %d", req.SessionPersistence.PersistenceTimeout)
	}

	up := m.toUpdateRequest(context.Background(), types.MapNull(types.StringType), &diag.Diagnostics{})
	if up.SessionPersistence == nil || up.SessionPersistence.Type != "APP_COOKIE" {
		t.Errorf("expected session_persistence in update request")
	}

	empty := &PoolModel{Name: types.StringValue("x"), Protocol: types.StringValue("tcp"), LBAlgorithm: types.StringValue("round_robin")}
	if empty.toCreateRequest(context.Background(), &diag.Diagnostics{}).SessionPersistence != nil {
		t.Errorf("expected nil session_persistence when unset")
	}
}

func TestPoolImportValid(t *testing.T) {
	r := NewResource().(*poolResource)
	schemaResp := importSchema(t, r)
	ctx := context.Background()

	resp := &resource.ImportStateResponse{State: tfsdk.State{Schema: schemaResp.Schema, Raw: emptyPool(ctx, schemaResp)}}
	r.ImportState(ctx, resource.ImportStateRequest{ID: "lb-7/pool-9"}, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("import failed: %v", resp.Diagnostics.Errors())
	}
	var lbID, id types.String
	resp.State.GetAttribute(ctx, path.Root("load_balancer_id"), &lbID)
	resp.State.GetAttribute(ctx, path.Root("id"), &id)
	if lbID.ValueString() != "lb-7" || id.ValueString() != "pool-9" {
		t.Errorf("expected lb-7/pool-9, got %s/%s", lbID.ValueString(), id.ValueString())
	}
}

func TestPoolImportMalformed(t *testing.T) {
	r := NewResource().(*poolResource)
	schemaResp := importSchema(t, r)
	ctx := context.Background()

	for _, bad := range []string{"only", "lb-7/", "/pool", ""} {
		resp := &resource.ImportStateResponse{State: tfsdk.State{Schema: schemaResp.Schema, Raw: emptyPool(ctx, schemaResp)}}
		r.ImportState(ctx, resource.ImportStateRequest{ID: bad}, resp)
		if !resp.Diagnostics.HasError() {
			t.Errorf("expected error for malformed import ID %q", bad)
		}
	}
}

func importSchema(t *testing.T, r resource.Resource) resource.SchemaResponse {
	t.Helper()
	var schemaResp resource.SchemaResponse
	r.Schema(context.Background(), resource.SchemaRequest{}, &schemaResp)
	return schemaResp
}

func emptyPool(ctx context.Context, schemaResp resource.SchemaResponse) tftypes.Value {
	tfType := schemaResp.Schema.Type().TerraformType(ctx)
	spType := tftypes.Object{AttributeTypes: map[string]tftypes.Type{
		"type":                    tftypes.String,
		"cookie_name":             tftypes.String,
		"persistence_timeout":     tftypes.Number,
		"persistence_granularity": tftypes.String,
	}}
	return tftypes.NewValue(tfType, map[string]tftypes.Value{
		"id":                  tftypes.NewValue(tftypes.String, nil),
		"load_balancer_id":    tftypes.NewValue(tftypes.String, nil),
		"listener_id":         tftypes.NewValue(tftypes.String, nil),
		"name":                tftypes.NewValue(tftypes.String, nil),
		"protocol":            tftypes.NewValue(tftypes.String, nil),
		"lb_algorithm":        tftypes.NewValue(tftypes.String, nil),
		"proxy_protocol":      tftypes.NewValue(tftypes.String, nil),
		"session_persistence": tftypes.NewValue(spType, nil),
		"tags":                tftypes.NewValue(tftypes.Map{ElementType: tftypes.String}, nil),
		"created_at":          tftypes.NewValue(tftypes.String, nil),
		"updated_at":          tftypes.NewValue(tftypes.String, nil),
		"timeouts":            tftypes.NewValue(tfType.(tftypes.Object).AttributeTypes["timeouts"], nil),
	})
}

func TestPoolMetadata(t *testing.T) {
	r := NewResource()
	resp := &resource.MetadataResponse{}
	r.Metadata(context.Background(), resource.MetadataRequest{ProviderTypeName: "frostmoln"}, resp)
	if resp.TypeName != "frostmoln_lb_pool" {
		t.Errorf("expected frostmoln_lb_pool, got %s", resp.TypeName)
	}
}

// poolCreatePlanValue builds a create-plan for a pool on lb-1: computed
// attributes unknown, no session persistence, no tags.
func poolCreatePlanValue(ctx context.Context, schemaResp resource.SchemaResponse) tftypes.Value {
	tfType := schemaResp.Schema.Type().TerraformType(ctx)
	return tftypes.NewValue(tfType, map[string]tftypes.Value{
		"id":                  tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"load_balancer_id":    tftypes.NewValue(tftypes.String, "lb-1"),
		"listener_id":         tftypes.NewValue(tftypes.String, nil),
		"name":                tftypes.NewValue(tftypes.String, "adopted-pool"),
		"protocol":            tftypes.NewValue(tftypes.String, "tcp"),
		"lb_algorithm":        tftypes.NewValue(tftypes.String, "round_robin"),
		"proxy_protocol":      tftypes.NewValue(tftypes.String, "none"),
		"session_persistence": tftypes.NewValue(tfType.(tftypes.Object).AttributeTypes["session_persistence"], nil),
		"tags":                tftypes.NewValue(tftypes.Map{ElementType: tftypes.String}, nil),
		"created_at":          tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"updated_at":          tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"timeouts":            tftypes.NewValue(tfType.(tftypes.Object).AttributeTypes["timeouts"], nil),
	})
}

func newFastPool(c *client.Client) *poolResource {
	r := NewResource().(*poolResource)
	r.pollInterval = 5 * time.Millisecond
	r.pollTimeout = 100 * time.Millisecond
	r.Configure(context.Background(), resource.ConfigureRequest{ProviderData: c}, &resource.ConfigureResponse{})
	return r
}

// TestPoolCreateAdoptsAfterTimeout pins the Gate 3 discovery-adopt fallback: a
// 202 whose operation never completes resolves, once the parent listing holds
// exactly one name match created after the floor, into an adopted state row
// and the shared adoption warning — never an error.
func TestPoolCreateAdoptsAfterTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/tenants/t-123/load-balancers/lb-1/pools":
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"operationId": "op-adopt-1", "status": "pending", "resourceType": "pool",
			})

		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-123/operations/op-adopt-1":
			// The saga never lands while the provider waits.
			_ = json.NewEncoder(w).Encode(map[string]any{
				"operationId": "op-adopt-1", "status": "pending", "resourceType": "pool",
			})

		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-123/load-balancers/lb-1/pools":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"pools": []apiPool{{
					ID:        "pool-adopted-1",
					Name:      "adopted-pool",
					CreatedAt: time.Now().UTC().Format(time.RFC3339), // after the floor
				}},
			})

		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-123/load-balancers/lb-1/pools/pool-adopted-1":
			_ = json.NewEncoder(w).Encode(apiPool{
				ID:             "pool-adopted-1",
				LoadBalancerID: "lb-1",
				Name:           "adopted-pool",
				Protocol:       "tcp",
				LBAlgorithm:    "round_robin",
				CreatedAt:      "2025-06-01T12:00:00Z",
			})

		default:
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]string{"code": "NOT_FOUND", "message": "not found"})
		}
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key") // pragma: allowlist secret
	c.SetTenantIDForTest("t-123")
	r := newFastPool(c)
	schemaResp := importSchema(t, r)
	ctx := context.Background()

	resp := &resource.CreateResponse{State: tfsdk.State{Schema: schemaResp.Schema}}
	r.Create(ctx, resource.CreateRequest{
		Plan: tfsdk.Plan{Schema: schemaResp.Schema, Raw: poolCreatePlanValue(ctx, schemaResp)},
	}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected errors: %v", resp.Diagnostics)
	}

	warnings := resp.Diagnostics.Warnings()
	if len(warnings) != 1 || !strings.Contains(warnings[0].Summary(), "Was Adopted After The Apply Timed Out") {
		t.Fatalf("expected exactly one adoption warning, got %d warning(s)", len(warnings))
	}

	var state PoolModel
	resp.State.Get(ctx, &state)
	if state.ID.ValueString() != "pool-adopted-1" {
		t.Errorf("expected adopted id pool-adopted-1, got %s", state.ID.ValueString())
	}
}

// TestPoolCreateRefusedByOperation pins the refused arm: the operation's
// terminal failure is the platform deciding NO — an error naming the refusal,
// no adoption sweep (no listing GET), state stays null.
func TestPoolCreateRefusedByOperation(t *testing.T) {
	listingGets := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/tenants/t-123/load-balancers/lb-1/pools":
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"operationId": "op-refused-1", "status": "pending", "resourceType": "pool",
			})

		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-123/operations/op-refused-1":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"operationId": "op-refused-1", "status": "failed", "resourceType": "pool",
				"error": "listener not found",
			})

		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-123/load-balancers/lb-1/pools":
			listingGets++
			w.WriteHeader(http.StatusOK)

		default:
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]string{"code": "NOT_FOUND", "message": "not found"})
		}
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key") // pragma: allowlist secret
	c.SetTenantIDForTest("t-123")
	r := newFastPool(c)
	schemaResp := importSchema(t, r)
	ctx := context.Background()

	resp := &resource.CreateResponse{State: tfsdk.State{Schema: schemaResp.Schema}}
	r.Create(ctx, resource.CreateRequest{
		Plan: tfsdk.Plan{Schema: schemaResp.Schema, Raw: poolCreatePlanValue(ctx, schemaResp)},
	}, resp)

	if !resp.Diagnostics.HasError() {
		t.Fatal("expected an error when the platform refuses the create")
	}
	if !strings.Contains(resp.Diagnostics.Errors()[0].Summary(), "Refused") {
		t.Errorf("expected a Refused summary, got %s", resp.Diagnostics.Errors()[0].Summary())
	}
	if listingGets != 0 {
		t.Errorf("refused arm must not sweep the listing, got %d listing GET(s)", listingGets)
	}
	if !resp.State.Raw.IsNull() {
		t.Error("expected null state after a refused create")
	}
}
