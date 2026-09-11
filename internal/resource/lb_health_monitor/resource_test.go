package lb_health_monitor

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

func TestHealthMonitorModelFromAPI(t *testing.T) {
	hm := &apiHealthMonitor{
		ID:            "hm-1",
		PoolID:        "pool-1",
		Type:          "http",
		Delay:         10,
		Timeout:       5,
		MaxRetries:    3,
		HTTPMethod:    "GET",
		URLPath:       "/healthz",
		ExpectedCodes: "200-299",
		CreatedAt:     "2025-01-01T00:00:00Z",
	}

	var model HealthMonitorModel
	model.fromAPI(context.Background(), "lb-1", hm, &diag.Diagnostics{})
	if model.ID.ValueString() != "hm-1" {
		t.Errorf("expected ID hm-1, got %s", model.ID.ValueString())
	}
	if model.LoadBalancerID.ValueString() != "lb-1" {
		t.Errorf("expected lb-1, got %s", model.LoadBalancerID.ValueString())
	}
	if model.Type.ValueString() != "http" {
		t.Errorf("expected http, got %s", model.Type.ValueString())
	}
	if model.Delay.ValueInt64() != 10 {
		t.Errorf("expected delay 10, got %d", model.Delay.ValueInt64())
	}
	if model.URLPath.ValueString() != "/healthz" {
		t.Errorf("expected /healthz, got %s", model.URLPath.ValueString())
	}
	if model.ExpectedCodes.ValueString() != "200-299" {
		t.Errorf("expected 200-299, got %s", model.ExpectedCodes.ValueString())
	}
}

// TestHealthMonitorToUpdateRequestExpectedCodes verifies the M3 fix:
// expected_codes is included in the update request so a change isn't dropped.
func TestHealthMonitorToUpdateRequestExpectedCodes(t *testing.T) {
	m := &HealthMonitorModel{
		Delay:         types.Int64Value(10),
		Timeout:       types.Int64Value(5),
		MaxRetries:    types.Int64Value(3),
		ExpectedCodes: types.StringValue("200,202"),
	}
	req := m.toUpdateRequest(context.Background(), types.MapNull(types.StringType), &diag.Diagnostics{})
	if req.ExpectedCodes == nil {
		t.Fatalf("expected ExpectedCodes in update request, got nil")
	}
	if *req.ExpectedCodes != "200,202" {
		t.Errorf("expected 200,202, got %s", *req.ExpectedCodes)
	}

	// Null expected_codes should be omitted.
	empty := &HealthMonitorModel{ExpectedCodes: types.StringNull()}
	if empty.toUpdateRequest(context.Background(), types.MapNull(types.StringType), &diag.Diagnostics{}).ExpectedCodes != nil {
		t.Errorf("expected nil ExpectedCodes when unset")
	}
}

func TestHealthMonitorImportValid(t *testing.T) {
	r := NewResource().(*healthMonitorResource)
	schemaResp := importSchema(t, r)
	ctx := context.Background()

	resp := &resource.ImportStateResponse{State: tfsdk.State{Schema: schemaResp.Schema, Raw: emptyHM(ctx, schemaResp)}}
	r.ImportState(ctx, resource.ImportStateRequest{ID: "lb-4/pool-5"}, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("import failed: %v", resp.Diagnostics.Errors())
	}
	var lbID, poolID types.String
	resp.State.GetAttribute(ctx, path.Root("load_balancer_id"), &lbID)
	resp.State.GetAttribute(ctx, path.Root("pool_id"), &poolID)
	if lbID.ValueString() != "lb-4" || poolID.ValueString() != "pool-5" {
		t.Errorf("expected lb-4/pool-5, got %s/%s", lbID.ValueString(), poolID.ValueString())
	}
}

func TestHealthMonitorImportMalformed(t *testing.T) {
	r := NewResource().(*healthMonitorResource)
	schemaResp := importSchema(t, r)
	ctx := context.Background()

	for _, bad := range []string{"lb-4", "", "lb-4/", "/pool-5"} {
		resp := &resource.ImportStateResponse{State: tfsdk.State{Schema: schemaResp.Schema, Raw: emptyHM(ctx, schemaResp)}}
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

func emptyHM(ctx context.Context, schemaResp resource.SchemaResponse) tftypes.Value {
	tfType := schemaResp.Schema.Type().TerraformType(ctx)
	return tftypes.NewValue(tfType, map[string]tftypes.Value{
		"id":               tftypes.NewValue(tftypes.String, nil),
		"load_balancer_id": tftypes.NewValue(tftypes.String, nil),
		"pool_id":          tftypes.NewValue(tftypes.String, nil),
		"type":             tftypes.NewValue(tftypes.String, nil),
		"delay":            tftypes.NewValue(tftypes.Number, nil),
		"timeout":          tftypes.NewValue(tftypes.Number, nil),
		"max_retries":      tftypes.NewValue(tftypes.Number, nil),
		"url_path":         tftypes.NewValue(tftypes.String, nil),
		"http_method":      tftypes.NewValue(tftypes.String, nil),
		"expected_codes":   tftypes.NewValue(tftypes.String, nil),
		"tags":             tftypes.NewValue(tftypes.Map{ElementType: tftypes.String}, nil),
		"created_at":       tftypes.NewValue(tftypes.String, nil),
		"updated_at":       tftypes.NewValue(tftypes.String, nil),
		"timeouts":         tftypes.NewValue(tfType.(tftypes.Object).AttributeTypes["timeouts"], nil),
	})
}

func TestHealthMonitorMetadata(t *testing.T) {
	r := NewResource()
	resp := &resource.MetadataResponse{}
	r.Metadata(context.Background(), resource.MetadataRequest{ProviderTypeName: "frostmoln"}, resp)
	if resp.TypeName != "frostmoln_lb_health_monitor" {
		t.Errorf("expected frostmoln_lb_health_monitor, got %s", resp.TypeName)
	}
}

// fastMonitorDeleteResource is the configured resource with the operation wait
// cut to milliseconds, the seam the async-delete tests drive.
func fastMonitorDeleteResource(t *testing.T, c *client.Client) *healthMonitorResource {
	t.Helper()
	r := &healthMonitorResource{client: c}
	r.pollInterval = 5 * time.Millisecond
	r.pollTimeout = 100 * time.Millisecond
	return r
}

// TestDeleteWaitsForTheDeleteOperation: a health-monitor delete routes through
// provisioning and answers 202 with an Operation envelope BEFORE the platform
// has decided anything. The destroy is done when the operation says so, not
// when the 202 lands — the monitor may still be running behind that envelope.
func TestDeleteWaitsForTheDeleteOperation(t *testing.T) {
	polled := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodDelete && r.URL.Path == hmPath:
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"operationId": "op-del", "status": "pending", "resourceType": "health-monitor",
			})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-1/operations/op-del":
			polled++
			_ = json.NewEncoder(w).Encode(map[string]any{
				"operationId": "op-del", "status": "completed", "resourceType": "health-monitor",
			})
		default:
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]string{"code": "NOT_FOUND", "message": "not found"})
		}
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key", client.WithHTTPClient(server.Client())) // pragma: allowlist secret
	c.SetTenantIDForTest("t-1")
	r := fastMonitorDeleteResource(t, c)

	state := buildHMState(t, sampleHMModel())
	resp := resource.DeleteResponse{State: state}
	r.Delete(context.Background(), resource.DeleteRequest{State: state}, &resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected errors: %v", resp.Diagnostics)
	}
	if polled == 0 {
		t.Error("expected the delete operation endpoint to be polled; a 202 envelope alone is not a destroy")
	}
}

// TestDeleteOperationFailureRefusesAndKeepsState: the workflow decided NO and
// said why. Nothing was changed, the monitor still exists, and the diagnostic
// must carry the platform's own reason so the practitioner knows what to deal
// with before destroying again.
func TestDeleteOperationFailureRefusesAndKeepsState(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodDelete && r.URL.Path == hmPath:
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"operationId": "op-del-fail", "status": "pending", "resourceType": "health-monitor",
			})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-1/operations/op-del-fail":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"operationId": "op-del-fail", "status": "failed", "resourceType": "health-monitor",
				"error": "the pool is still attached to a listener",
			})
		default:
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]string{"code": "NOT_FOUND", "message": "not found"})
		}
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key", client.WithHTTPClient(server.Client())) // pragma: allowlist secret
	c.SetTenantIDForTest("t-1")
	r := fastMonitorDeleteResource(t, c)

	state := buildHMState(t, sampleHMModel())
	resp := resource.DeleteResponse{State: state}
	r.Delete(context.Background(), resource.DeleteRequest{State: state}, &resp)

	if !resp.Diagnostics.HasError() {
		t.Fatal("a delete whose operation the platform refused must error, not report success")
	}
	if summary := resp.Diagnostics.Errors()[0].Summary(); !strings.Contains(summary, "Refused By The Platform") {
		t.Errorf("expected the refused-deletion summary, got %q", summary)
	}
	if detail := resp.Diagnostics.Errors()[0].Detail(); !strings.Contains(detail, "the pool is still attached to a listener") {
		t.Errorf("the platform's own reason must survive into the diagnostic, got: %s", detail)
	}
}

// TestDeleteAcceptedButUnwatchableIsClassified: the destroy was accepted but
// its envelope parses to nothing, so the workflow cannot be watched from here.
// That is neither a success nor a verified absence — it must read as an
// unknown outcome, never a silent return.
func TestDeleteAcceptedButUnwatchableIsClassified(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodDelete && r.URL.Path == hmPath:
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte("not-json"))
		default:
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]string{"code": "NOT_FOUND", "message": "not found"})
		}
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key", client.WithHTTPClient(server.Client())) // pragma: allowlist secret
	c.SetTenantIDForTest("t-1")
	r := fastMonitorDeleteResource(t, c)

	state := buildHMState(t, sampleHMModel())
	resp := resource.DeleteResponse{State: state}
	r.Delete(context.Background(), resource.DeleteRequest{State: state}, &resp)

	if !resp.Diagnostics.HasError() {
		t.Fatal("an accepted delete whose operation cannot be watched must error, not silently succeed")
	}
	if summary := resp.Diagnostics.Errors()[0].Summary(); !strings.Contains(summary, "Outcome Is Unknown") {
		t.Errorf("expected the unknown-outcome classification, got %q", summary)
	}
}

// hmCreatePlanValue builds a create-plan for the singleton monitor of
// pool-1: computed attributes unknown, HTTP shape unset.
func hmCreatePlanValue(ctx context.Context, schemaResp resource.SchemaResponse) tftypes.Value {
	tfType := schemaResp.Schema.Type().TerraformType(ctx)
	return tftypes.NewValue(tfType, map[string]tftypes.Value{
		"id":               tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"load_balancer_id": tftypes.NewValue(tftypes.String, "lb-1"),
		"pool_id":          tftypes.NewValue(tftypes.String, "pool-1"),
		"type":             tftypes.NewValue(tftypes.String, "http"),
		"delay":            tftypes.NewValue(tftypes.Number, 5),
		"timeout":          tftypes.NewValue(tftypes.Number, 3),
		"max_retries":      tftypes.NewValue(tftypes.Number, 3),
		"url_path":         tftypes.NewValue(tftypes.String, nil),
		"http_method":      tftypes.NewValue(tftypes.String, nil),
		"expected_codes":   tftypes.NewValue(tftypes.String, nil),
		"tags":             tftypes.NewValue(tftypes.Map{ElementType: tftypes.String}, nil),
		"created_at":       tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"updated_at":       tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"timeouts":         tftypes.NewValue(tfType.(tftypes.Object).AttributeTypes["timeouts"], nil),
	})
}

func runCreate(t *testing.T, r resource.Resource, schemaResp resource.SchemaResponse, planVal tftypes.Value) *resource.CreateResponse {
	t.Helper()
	ctx := context.Background()
	resp := &resource.CreateResponse{State: tfsdk.State{Schema: schemaResp.Schema}}
	r.Create(ctx, resource.CreateRequest{
		Plan: tfsdk.Plan{Schema: schemaResp.Schema, Raw: planVal},
	}, resp)
	return resp
}

// TestHealthMonitorCreateAdoptsAfterTimeout pins the Gate 3 discovery-adopt
// fallback for the name-less singleton: a 202 whose operation never completes
// resolves through the pool's healthmonitor GET — 200 means the monitor
// exists and its row is adopted, honestly read, with the shared warning.
func TestHealthMonitorCreateAdoptsAfterTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/tenants/t-123/load-balancers/lb-1/pools/pool-1/healthmonitor":
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"operationId": "op-adopt-1", "status": "pending", "resourceType": "health-monitor",
			})

		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-123/operations/op-adopt-1":
			// The saga never lands while the provider waits.
			_ = json.NewEncoder(w).Encode(map[string]any{
				"operationId": "op-adopt-1", "status": "pending", "resourceType": "health-monitor",
			})

		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-123/load-balancers/lb-1/pools/pool-1/healthmonitor":
			// The sweep's listing and the honest read are the same singleton GET.
			// The createdAt is NOW: the adopt sweep refuses a monitor that
			// predates the apply's created-at floor (that would be somebody
			// else's monitor) — see the pre-floor refusal test below.
			_ = json.NewEncoder(w).Encode(apiHealthMonitor{
				ID:         "hm-adopted-1",
				PoolID:     "pool-1",
				Type:       "http",
				Delay:      5,
				Timeout:    3,
				MaxRetries: 3,
				CreatedAt:  time.Now().UTC().Add(-time.Second).Format(time.RFC3339),
			})

		default:
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]string{"code": "NOT_FOUND", "message": "not found"})
		}
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key") // pragma: allowlist secret
	c.SetTenantIDForTest("t-123")
	r := fastMonitorDeleteResource(t, c)
	schemaResp := importSchema(t, r)
	ctx := context.Background()

	resp := runCreate(t, r, schemaResp, hmCreatePlanValue(ctx, schemaResp))

	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected errors: %v", resp.Diagnostics)
	}

	warnings := resp.Diagnostics.Warnings()
	if len(warnings) != 1 || !strings.Contains(warnings[0].Summary(), "Was Adopted After The Apply Timed Out") {
		t.Fatalf("expected exactly one adoption warning, got %d warning(s)", len(warnings))
	}

	var state HealthMonitorModel
	resp.State.Get(ctx, &state)
	if state.ID.ValueString() != "hm-adopted-1" {
		t.Errorf("expected adopted id hm-adopted-1, got %s", state.ID.ValueString())
	}
}

// TestHealthMonitorCreateTimesOutVerifiedAbsent pins the absent arm of the
// singleton sweep: the pool has no healthmonitor after the wait gives up, so
// the 404 is the verified absence — an error that is safe to re-apply,
// nothing recorded in state.
func TestHealthMonitorCreateTimesOutVerifiedAbsent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/tenants/t-123/load-balancers/lb-1/pools/pool-1/healthmonitor":
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"operationId": "op-absent-1", "status": "pending", "resourceType": "health-monitor",
			})

		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-123/operations/op-absent-1":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"operationId": "op-absent-1", "status": "pending", "resourceType": "health-monitor",
			})

		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-123/load-balancers/lb-1/pools/pool-1/healthmonitor":
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]string{"code": "NOT_FOUND", "message": "not found"})

		default:
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]string{"code": "NOT_FOUND", "message": "not found"})
		}
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key") // pragma: allowlist secret
	c.SetTenantIDForTest("t-123")
	r := fastMonitorDeleteResource(t, c)
	schemaResp := importSchema(t, r)
	ctx := context.Background()

	resp := runCreate(t, r, schemaResp, hmCreatePlanValue(ctx, schemaResp))

	if !resp.Diagnostics.HasError() {
		t.Fatal("expected the verified-absence error when the pool has no monitor")
	}
	if summary := resp.Diagnostics.Errors()[0].Summary(); !strings.Contains(summary, "Verified Absent") {
		t.Errorf("expected the Verified Absent summary, got %q", summary)
	}
	if !resp.State.Raw.IsNull() {
		t.Error("expected null state after a verified absence")
	}
}

// TestAdoptRefusesAPreExistingMonitor: the pool's singleton monitor predating
// the apply's created-at floor is somebody else's — the sweep refuses to
// guess rather than bind a pre-existing monitor into this apply's state.
func TestAdoptRefusesAPreExistingMonitor(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/tenants/t-123/load-balancers/lb-1/pools/pool-1/healthmonitor":
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"operationId": "op-old-1", "status": "pending", "resourceType": "health-monitor",
			})

		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-123/operations/op-old-1":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"operationId": "op-old-1", "status": "pending", "resourceType": "health-monitor",
			})

		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-123/load-balancers/lb-1/pools/pool-1/healthmonitor":
			_ = json.NewEncoder(w).Encode(apiHealthMonitor{
				ID:        "hm-old-1",
				PoolID:    "pool-1",
				Type:      "http",
				CreatedAt: "2025-06-01T12:00:00Z", // years before any floor
			})

		default:
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]string{"code": "NOT_FOUND", "message": "not found"})
		}
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key") // pragma: allowlist secret
	c.SetTenantIDForTest("t-123")
	r := fastMonitorDeleteResource(t, c)
	schemaResp := importSchema(t, r)
	ctx := context.Background()

	resp := runCreate(t, r, schemaResp, hmCreatePlanValue(ctx, schemaResp))

	if !resp.Diagnostics.HasError() {
		t.Fatal("a pre-existing monitor must NOT be adopted into this apply's state")
	}
	if !strings.Contains(resp.Diagnostics.Errors()[0].Detail(), "predates this apply") {
		t.Errorf("the refusal must say the monitor predates the apply, got: %s", resp.Diagnostics.Errors()[0].Detail())
	}
	if !resp.State.Raw.IsNull() {
		t.Error("expected null state after the sweep's refusal to adopt a pre-existing monitor")
	}
}
