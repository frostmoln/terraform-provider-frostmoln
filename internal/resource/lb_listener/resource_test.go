package lb_listener

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

func TestListenerModelFromAPI(t *testing.T) {
	l := &apiListener{
		ID:              "lst-1",
		LoadBalancerID:  "lb-1",
		Name:            "web",
		Protocol:        "https",
		ProtocolPort:    443,
		DefaultPoolID:   "pool-1",
		ConnectionLimit: 1000,
		AllowedCIDRs:    []string{"0.0.0.0/0"},
		InsertHeaders:   map[string]string{"X-Forwarded-For": "true"},
		AdminStateUp:    true,
		CreatedAt:       "2025-01-01T00:00:00Z",
	}

	var model ListenerModel
	var diags diag.Diagnostics
	model.fromAPI(context.Background(), l, &diags)
	if diags.HasError() {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}
	if model.ID.ValueString() != "lst-1" {
		t.Errorf("expected ID lst-1, got %s", model.ID.ValueString())
	}
	if model.Protocol.ValueString() != "https" {
		t.Errorf("expected protocol https, got %s", model.Protocol.ValueString())
	}
	if model.ProtocolPort.ValueInt64() != 443 {
		t.Errorf("expected port 443, got %d", model.ProtocolPort.ValueInt64())
	}
	if model.DefaultPoolID.ValueString() != "pool-1" {
		t.Errorf("expected pool pool-1, got %s", model.DefaultPoolID.ValueString())
	}
	var cidrs []string
	model.AllowedCIDRs.ElementsAs(context.Background(), &cidrs, false)
	if len(cidrs) != 1 || cidrs[0] != "0.0.0.0/0" {
		t.Errorf("expected allowed_cidrs [0.0.0.0/0], got %v", cidrs)
	}
}

// TestListenerConnectionLimitReflected verifies the M1 fix: connection_limit is
// Optional+Computed, so fromAPI always reflects the backend value (including the
// backend default and 0) rather than churning to null.
func TestListenerConnectionLimitReflected(t *testing.T) {
	for _, cl := range []int{0, 1000, -1} {
		l := &apiListener{
			ID:              "lst-1",
			LoadBalancerID:  "lb-1",
			Name:            "web",
			Protocol:        "tcp",
			ProtocolPort:    80,
			ConnectionLimit: cl,
			AllowedCIDRs:    []string{"0.0.0.0/0"},
			CreatedAt:       "2025-01-01T00:00:00Z",
		}
		var model ListenerModel
		var diags diag.Diagnostics
		model.fromAPI(context.Background(), l, &diags)
		if model.ConnectionLimit.IsNull() {
			t.Errorf("connection_limit=%d: expected reflected value, got null", cl)
		}
		if model.ConnectionLimit.ValueInt64() != int64(cl) {
			t.Errorf("connection_limit=%d: got %d", cl, model.ConnectionLimit.ValueInt64())
		}
	}
}

func TestListenerImportValid(t *testing.T) {
	r := NewResource().(*listenerResource)
	schemaResp := importSchema(t, r)
	ctx := context.Background()

	resp := &resource.ImportStateResponse{State: tfsdk.State{Schema: schemaResp.Schema, Raw: emptyListener(ctx, schemaResp)}}
	r.ImportState(ctx, resource.ImportStateRequest{ID: "lb-1/lst-2"}, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("import failed: %v", resp.Diagnostics.Errors())
	}
	var lbID, id types.String
	resp.State.GetAttribute(ctx, path.Root("load_balancer_id"), &lbID)
	resp.State.GetAttribute(ctx, path.Root("id"), &id)
	if lbID.ValueString() != "lb-1" || id.ValueString() != "lst-2" {
		t.Errorf("expected lb-1/lst-2, got %s/%s", lbID.ValueString(), id.ValueString())
	}
}

func TestListenerImportMalformed(t *testing.T) {
	r := NewResource().(*listenerResource)
	schemaResp := importSchema(t, r)
	ctx := context.Background()

	for _, bad := range []string{"only-one", "lb-1/", "/lst-2", ""} {
		resp := &resource.ImportStateResponse{State: tfsdk.State{Schema: schemaResp.Schema, Raw: emptyListener(ctx, schemaResp)}}
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

func emptyListener(ctx context.Context, schemaResp resource.SchemaResponse) tftypes.Value {
	tfType := schemaResp.Schema.Type().TerraformType(ctx)
	return tftypes.NewValue(tfType, map[string]tftypes.Value{
		"id":                 tftypes.NewValue(tftypes.String, nil),
		"load_balancer_id":   tftypes.NewValue(tftypes.String, nil),
		"name":               tftypes.NewValue(tftypes.String, nil),
		"protocol":           tftypes.NewValue(tftypes.String, nil),
		"protocol_port":      tftypes.NewValue(tftypes.Number, nil),
		"allowed_cidrs":      tftypes.NewValue(tftypes.List{ElementType: tftypes.String}, nil),
		"insert_headers":     tftypes.NewValue(tftypes.Map{ElementType: tftypes.String}, nil),
		"default_pool_id":    tftypes.NewValue(tftypes.String, nil),
		"tls_certificate_id": tftypes.NewValue(tftypes.String, nil),
		"connection_limit":   tftypes.NewValue(tftypes.Number, nil),
		"admin_state_up":     tftypes.NewValue(tftypes.Bool, nil),
		"created_at":         tftypes.NewValue(tftypes.String, nil),
		"updated_at":         tftypes.NewValue(tftypes.String, nil),
		"timeouts":           tftypes.NewValue(tfType.(tftypes.Object).AttributeTypes["timeouts"], nil),
	})
}

func TestListenerMetadata(t *testing.T) {
	r := NewResource()
	resp := &resource.MetadataResponse{}
	r.Metadata(context.Background(), resource.MetadataRequest{ProviderTypeName: "frostmoln"}, resp)
	if resp.TypeName != "frostmoln_lb_listener" {
		t.Errorf("expected frostmoln_lb_listener, got %s", resp.TypeName)
	}
}

// listenerCreatePlanValue builds a create-plan for a listener on lb-1:
// computed attributes unknown, allowed_cidrs the deny-by-default single CIDR.
func listenerCreatePlanValue(ctx context.Context, schemaResp resource.SchemaResponse) tftypes.Value {
	tfType := schemaResp.Schema.Type().TerraformType(ctx)
	cidrs, _ := types.ListValueFrom(ctx, types.StringType, []string{"0.0.0.0/0"})
	raw, _ := cidrs.ToTerraformValue(ctx)
	return tftypes.NewValue(tfType, map[string]tftypes.Value{
		"id":                 tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"load_balancer_id":   tftypes.NewValue(tftypes.String, "lb-1"),
		"name":               tftypes.NewValue(tftypes.String, "adopted-listener"),
		"protocol":           tftypes.NewValue(tftypes.String, "tcp"),
		"protocol_port":      tftypes.NewValue(tftypes.Number, 8443),
		"allowed_cidrs":      raw,
		"insert_headers":     tftypes.NewValue(tftypes.Map{ElementType: tftypes.String}, nil),
		"default_pool_id":    tftypes.NewValue(tftypes.String, nil),
		"tls_certificate_id": tftypes.NewValue(tftypes.String, nil),
		"connection_limit":   tftypes.NewValue(tftypes.Number, tftypes.UnknownValue),
		"admin_state_up":     tftypes.NewValue(tftypes.Bool, tftypes.UnknownValue),
		"created_at":         tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"updated_at":         tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"timeouts":           tftypes.NewValue(tfType.(tftypes.Object).AttributeTypes["timeouts"], nil),
	})
}

func newFastListener(c *client.Client) *listenerResource {
	r := NewResource().(*listenerResource)
	r.pollInterval = 5 * time.Millisecond
	r.pollTimeout = 100 * time.Millisecond
	r.Configure(context.Background(), resource.ConfigureRequest{ProviderData: c}, &resource.ConfigureResponse{})
	return r
}

// TestListenerCreateAdoptsAfterTimeout pins the Gate 3 discovery-adopt
// fallback: a 202 whose operation never completes resolves, once the parent
// listing holds exactly one name match created after the floor, into an
// adopted state row and the shared adoption warning — never an error.
func TestListenerCreateAdoptsAfterTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/tenants/t-123/load-balancers/lb-1/listeners":
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"operationId": "op-adopt-1", "status": "pending", "resourceType": "listener",
			})

		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-123/operations/op-adopt-1":
			// The saga never lands while the provider waits.
			_ = json.NewEncoder(w).Encode(map[string]any{
				"operationId": "op-adopt-1", "status": "pending", "resourceType": "listener",
			})

		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-123/load-balancers/lb-1/listeners":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"listeners": []apiListener{{
					ID:        "lst-adopted-1",
					Name:      "adopted-listener",
					CreatedAt: time.Now().UTC().Format(time.RFC3339), // after the floor
				}},
			})

		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-123/load-balancers/lb-1/listeners/lst-adopted-1":
			_ = json.NewEncoder(w).Encode(apiListener{
				ID:             "lst-adopted-1",
				LoadBalancerID: "lb-1",
				Name:           "adopted-listener",
				Protocol:       "tcp",
				ProtocolPort:   8443,
				AdminStateUp:   true,
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
	r := newFastListener(c)
	schemaResp := importSchema(t, r)
	ctx := context.Background()

	resp := &resource.CreateResponse{State: tfsdk.State{Schema: schemaResp.Schema}}
	r.Create(ctx, resource.CreateRequest{
		Plan: tfsdk.Plan{Schema: schemaResp.Schema, Raw: listenerCreatePlanValue(ctx, schemaResp)},
	}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected errors: %v", resp.Diagnostics)
	}

	warnings := resp.Diagnostics.Warnings()
	if len(warnings) != 1 || !strings.Contains(warnings[0].Summary(), "Was Adopted After The Apply Timed Out") {
		t.Fatalf("expected exactly one adoption warning, got %d warning(s)", len(warnings))
	}

	var state ListenerModel
	resp.State.Get(ctx, &state)
	if state.ID.ValueString() != "lst-adopted-1" {
		t.Errorf("expected adopted id lst-adopted-1, got %s", state.ID.ValueString())
	}
}

// TestListenerCreateRefusedByOperation pins the refused arm: the operation's
// terminal failure is the platform deciding NO — an error naming the refusal,
// no adoption sweep (no listing GET), state stays null.
func TestListenerCreateRefusedByOperation(t *testing.T) {
	listingGets := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/tenants/t-123/load-balancers/lb-1/listeners":
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"operationId": "op-refused-1", "status": "pending", "resourceType": "listener",
			})

		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-123/operations/op-refused-1":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"operationId": "op-refused-1", "status": "failed", "resourceType": "listener",
				"error": "certificate not found",
			})

		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-123/load-balancers/lb-1/listeners":
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
	r := newFastListener(c)
	schemaResp := importSchema(t, r)
	ctx := context.Background()

	resp := &resource.CreateResponse{State: tfsdk.State{Schema: schemaResp.Schema}}
	r.Create(ctx, resource.CreateRequest{
		Plan: tfsdk.Plan{Schema: schemaResp.Schema, Raw: listenerCreatePlanValue(ctx, schemaResp)},
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
