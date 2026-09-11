package lb_member

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/boolplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
)

func TestMemberModelFromAPI(t *testing.T) {
	mem := &apiMember{
		ID:           "mem-1",
		PoolID:       "pool-1",
		Name:         "node-a",
		Address:      "10.0.0.10",
		ProtocolPort: 8080,
		SubnetID:     "subnet-1",
		Weight:       5,
		CreatedAt:    "2025-01-01T00:00:00Z",
	}

	var model MemberModel
	model.fromAPI("lb-1", mem)
	if model.ID.ValueString() != "mem-1" {
		t.Errorf("expected ID mem-1, got %s", model.ID.ValueString())
	}
	if model.LoadBalancerID.ValueString() != "lb-1" {
		t.Errorf("expected lb-1, got %s", model.LoadBalancerID.ValueString())
	}
	if model.Address.ValueString() != "10.0.0.10" {
		t.Errorf("expected address 10.0.0.10, got %s", model.Address.ValueString())
	}
	if model.ProtocolPort.ValueInt64() != 8080 {
		t.Errorf("expected port 8080, got %d", model.ProtocolPort.ValueInt64())
	}
	if model.Weight.ValueInt64() != 5 {
		t.Errorf("expected weight 5, got %d", model.Weight.ValueInt64())
	}
}

func TestMemberImportValid(t *testing.T) {
	r := NewResource().(*memberResource)
	schemaResp := importSchema(t, r)
	ctx := context.Background()

	resp := &resource.ImportStateResponse{State: tfsdk.State{Schema: schemaResp.Schema, Raw: emptyMember(ctx, schemaResp)}}
	r.ImportState(ctx, resource.ImportStateRequest{ID: "lb-1/pool-2/mem-3"}, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("import failed: %v", resp.Diagnostics.Errors())
	}
	var lbID, poolID, id types.String
	resp.State.GetAttribute(ctx, path.Root("load_balancer_id"), &lbID)
	resp.State.GetAttribute(ctx, path.Root("pool_id"), &poolID)
	resp.State.GetAttribute(ctx, path.Root("id"), &id)
	if lbID.ValueString() != "lb-1" || poolID.ValueString() != "pool-2" || id.ValueString() != "mem-3" {
		t.Errorf("expected lb-1/pool-2/mem-3, got %s/%s/%s", lbID.ValueString(), poolID.ValueString(), id.ValueString())
	}
}

func TestMemberImportMalformed(t *testing.T) {
	r := NewResource().(*memberResource)
	schemaResp := importSchema(t, r)
	ctx := context.Background()

	for _, bad := range []string{"lb-1/pool-2", "lb-1", "", "lb-1//mem-3", "lb-1/pool-2/"} {
		resp := &resource.ImportStateResponse{State: tfsdk.State{Schema: schemaResp.Schema, Raw: emptyMember(ctx, schemaResp)}}
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

func emptyMember(ctx context.Context, schemaResp resource.SchemaResponse) tftypes.Value {
	tfType := schemaResp.Schema.Type().TerraformType(ctx)
	return tftypes.NewValue(tfType, map[string]tftypes.Value{
		"id":               tftypes.NewValue(tftypes.String, nil),
		"load_balancer_id": tftypes.NewValue(tftypes.String, nil),
		"pool_id":          tftypes.NewValue(tftypes.String, nil),
		"address":          tftypes.NewValue(tftypes.String, nil),
		"protocol_port":    tftypes.NewValue(tftypes.Number, nil),
		"name":             tftypes.NewValue(tftypes.String, nil),
		"weight":           tftypes.NewValue(tftypes.Number, nil),
		"subnet_id":        tftypes.NewValue(tftypes.String, nil),
		"cross_vpc":        tftypes.NewValue(tftypes.Bool, nil),
		"created_at":       tftypes.NewValue(tftypes.String, nil),
		"updated_at":       tftypes.NewValue(tftypes.String, nil),
		"timeouts":         tftypes.NewValue(tfType.(tftypes.Object).AttributeTypes["timeouts"], nil),
	})
}

// TestCrossVPCRequiresReplaceIf verifies the H3 fix: an imported member (prior
// state null) supplying cross_vpc for the first time is reconciled in place, not
// destroyed; a genuine change between two known values still forces replacement.
func TestCrossVPCRequiresReplaceIf(t *testing.T) {
	ctx := context.Background()

	cases := []struct {
		name      string
		state     types.Bool
		plan      types.Bool
		wantReplc bool
	}{
		{"imported null -> true: no replace", types.BoolNull(), types.BoolValue(true), false},
		{"imported null -> false: no replace", types.BoolNull(), types.BoolValue(false), false},
		{"false -> true: replace", types.BoolValue(false), types.BoolValue(true), true},
		{"true -> false: replace", types.BoolValue(true), types.BoolValue(false), true},
		{"true -> true: no replace", types.BoolValue(true), types.BoolValue(true), false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := planmodifier.BoolRequest{StateValue: tc.state, PlanValue: tc.plan}
			resp := &boolplanmodifier.RequiresReplaceIfFuncResponse{}
			requiresReplaceUnlessPriorNull(ctx, req, resp)
			if resp.RequiresReplace != tc.wantReplc {
				t.Errorf("got RequiresReplace=%v, want %v", resp.RequiresReplace, tc.wantReplc)
			}
		})
	}
}

func TestMemberMetadata(t *testing.T) {
	r := NewResource()
	resp := &resource.MetadataResponse{}
	r.Metadata(context.Background(), resource.MetadataRequest{ProviderTypeName: "frostmoln"}, resp)
	if resp.TypeName != "frostmoln_lb_member" {
		t.Errorf("expected frostmoln_lb_member, got %s", resp.TypeName)
	}
}

// fastMemberDeleteResource is the configured resource with the operation wait
// cut to milliseconds, the seam the async-delete tests drive.
func fastMemberDeleteResource(t *testing.T, c *client.Client) *memberResource {
	t.Helper()
	r := &memberResource{client: c}
	r.pollInterval = 5 * time.Millisecond
	r.pollTimeout = 100 * time.Millisecond
	return r
}

// TestDeleteWaitsForTheDeleteOperation: a member delete routes through
// provisioning and answers 202 with an Operation envelope BEFORE the platform
// has decided anything. The destroy is done when the operation says so, not
// when the 202 lands — the member may still be serving behind that envelope.
func TestDeleteWaitsForTheDeleteOperation(t *testing.T) {
	polled := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodDelete && r.URL.Path == "/v1/tenants/t-1/load-balancers/lb-1/pools/pool-1/members/mem-1":
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"operationId": "op-del", "status": "pending", "resourceType": "pool-member",
			})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-1/operations/op-del":
			polled++
			_ = json.NewEncoder(w).Encode(map[string]any{
				"operationId": "op-del", "status": "completed", "resourceType": "pool-member",
			})
		default:
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]string{"code": "NOT_FOUND", "message": "not found"})
		}
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key", client.WithHTTPClient(server.Client())) // pragma: allowlist secret
	c.SetTenantIDForTest("t-1")
	r := fastMemberDeleteResource(t, c)

	state := buildMemberState(t, sampleMemberModel())
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
// said why. Nothing was changed, the member still exists, and the diagnostic
// must carry the platform's own reason so the practitioner knows what to deal
// with before destroying again.
func TestDeleteOperationFailureRefusesAndKeepsState(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodDelete && r.URL.Path == "/v1/tenants/t-1/load-balancers/lb-1/pools/pool-1/members/mem-1":
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"operationId": "op-del-fail", "status": "pending", "resourceType": "pool-member",
			})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-1/operations/op-del-fail":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"operationId": "op-del-fail", "status": "failed", "resourceType": "pool-member",
				"error": "the pool must be drained before members leave",
			})
		default:
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]string{"code": "NOT_FOUND", "message": "not found"})
		}
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key", client.WithHTTPClient(server.Client())) // pragma: allowlist secret
	c.SetTenantIDForTest("t-1")
	r := fastMemberDeleteResource(t, c)

	state := buildMemberState(t, sampleMemberModel())
	resp := resource.DeleteResponse{State: state}
	r.Delete(context.Background(), resource.DeleteRequest{State: state}, &resp)

	if !resp.Diagnostics.HasError() {
		t.Fatal("a delete whose operation the platform refused must error, not report success")
	}
	if summary := resp.Diagnostics.Errors()[0].Summary(); !strings.Contains(summary, "Refused By The Platform") {
		t.Errorf("expected the refused-deletion summary, got %q", summary)
	}
	if detail := resp.Diagnostics.Errors()[0].Detail(); !strings.Contains(detail, "the pool must be drained before members leave") {
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
		case r.Method == http.MethodDelete && r.URL.Path == "/v1/tenants/t-1/load-balancers/lb-1/pools/pool-1/members/mem-1":
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
	r := fastMemberDeleteResource(t, c)

	state := buildMemberState(t, sampleMemberModel())
	resp := resource.DeleteResponse{State: state}
	r.Delete(context.Background(), resource.DeleteRequest{State: state}, &resp)

	if !resp.Diagnostics.HasError() {
		t.Fatal("an accepted delete whose operation cannot be watched must error, not silently succeed")
	}
	if summary := resp.Diagnostics.Errors()[0].Summary(); !strings.Contains(summary, "Outcome Is Unknown") {
		t.Errorf("expected the unknown-outcome classification, got %q", summary)
	}
}

// memberCreatePlanValue builds a create-plan for the address:port member the
// sweep matches pool members by.
func memberCreatePlanValue(ctx context.Context, schemaResp resource.SchemaResponse) tftypes.Value {
	tfType := schemaResp.Schema.Type().TerraformType(ctx)
	return tftypes.NewValue(tfType, map[string]tftypes.Value{
		"id":               tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"load_balancer_id": tftypes.NewValue(tftypes.String, "lb-1"),
		"pool_id":          tftypes.NewValue(tftypes.String, "pool-1"),
		"address":          tftypes.NewValue(tftypes.String, "10.0.9.7"),
		"protocol_port":    tftypes.NewValue(tftypes.Number, 8443),
		"name":             tftypes.NewValue(tftypes.String, nil),
		"weight":           tftypes.NewValue(tftypes.Number, tftypes.UnknownValue),
		"subnet_id":        tftypes.NewValue(tftypes.String, "subnet-1"),
		"cross_vpc":        tftypes.NewValue(tftypes.Bool, nil),
		"created_at":       tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"updated_at":       tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"timeouts":         tftypes.NewValue(tfType.(tftypes.Object).AttributeTypes["timeouts"], nil),
	})
}

// TestMemberCreateAdoptsAfterTimeout pins the Gate 3 discovery-adopt fallback
// for the name-less member: a 202 whose operation never completes resolves
// through the pool's member listing — the plan's address:port tuple matches
// exactly one member created after the floor — into an adopted, honestly-read
// state row and the shared adoption warning.
func TestMemberCreateAdoptsAfterTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/tenants/t-123/load-balancers/lb-1/pools/pool-1/members":
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"operationId": "op-adopt-1", "status": "pending", "resourceType": "member",
			})

		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-123/operations/op-adopt-1":
			// The saga never lands while the provider waits.
			_ = json.NewEncoder(w).Encode(map[string]any{
				"operationId": "op-adopt-1", "status": "pending", "resourceType": "member",
			})

		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-123/load-balancers/lb-1/pools/pool-1/members":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"members": []apiMember{{
					ID:           "mem-adopted-1",
					PoolID:       "pool-1",
					Address:      "10.0.9.7",
					ProtocolPort: 8443,
					Weight:       1,
					CreatedAt:    time.Now().UTC().Format(time.RFC3339), // after the floor
				}},
			})

		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-123/load-balancers/lb-1/pools/pool-1/members/mem-adopted-1":
			_ = json.NewEncoder(w).Encode(apiMember{
				ID:           "mem-adopted-1",
				PoolID:       "pool-1",
				Address:      "10.0.9.7",
				ProtocolPort: 8443,
				SubnetID:     "subnet-1",
				Weight:       1,
				CreatedAt:    "2025-06-01T12:00:00Z",
			})

		default:
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]string{"code": "NOT_FOUND", "message": "not found"})
		}
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key") // pragma: allowlist secret
	c.SetTenantIDForTest("t-123")
	r := fastMemberDeleteResource(t, c)
	schemaResp := importSchema(t, r)
	ctx := context.Background()

	resp := &resource.CreateResponse{State: tfsdk.State{Schema: schemaResp.Schema}}
	r.Create(ctx, resource.CreateRequest{
		Plan: tfsdk.Plan{Schema: schemaResp.Schema, Raw: memberCreatePlanValue(ctx, schemaResp)},
	}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected errors: %v", resp.Diagnostics)
	}

	warnings := resp.Diagnostics.Warnings()
	if len(warnings) != 1 || !strings.Contains(warnings[0].Summary(), "Was Adopted After The Apply Timed Out") {
		t.Fatalf("expected exactly one adoption warning, got %d warning(s)", len(warnings))
	}

	var state MemberModel
	resp.State.Get(ctx, &state)
	if state.ID.ValueString() != "mem-adopted-1" {
		t.Errorf("expected adopted id mem-adopted-1, got %s", state.ID.ValueString())
	}
}

// TestMemberCreateTimesOutVerifiedAbsent pins the absent arm of the member
// sweep: the pool holds NO member matching the plan's address:port tuple after
// the wait gives up, so the lookup is the verified absence — an error that is
// safe to re-apply, nothing recorded in state.
func TestMemberCreateTimesOutVerifiedAbsent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/tenants/t-123/load-balancers/lb-1/pools/pool-1/members":
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"operationId": "op-absent-1", "status": "pending", "resourceType": "member",
			})

		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-123/operations/op-absent-1":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"operationId": "op-absent-1", "status": "pending", "resourceType": "member",
			})

		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-123/load-balancers/lb-1/pools/pool-1/members":
			// The listing succeeds and matches nothing: verified absence.
			_ = json.NewEncoder(w).Encode(map[string]any{"members": []apiMember{}})

		default:
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]string{"code": "NOT_FOUND", "message": "not found"})
		}
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key") // pragma: allowlist secret
	c.SetTenantIDForTest("t-123")
	r := fastMemberDeleteResource(t, c)
	schemaResp := importSchema(t, r)
	ctx := context.Background()

	resp := &resource.CreateResponse{State: tfsdk.State{Schema: schemaResp.Schema}}
	r.Create(ctx, resource.CreateRequest{
		Plan: tfsdk.Plan{Schema: schemaResp.Schema, Raw: memberCreatePlanValue(ctx, schemaResp)},
	}, resp)

	if !resp.Diagnostics.HasError() {
		t.Fatal("expected the verified-absence error when no member matches the tuple")
	}
	if summary := resp.Diagnostics.Errors()[0].Summary(); !strings.Contains(summary, "Verified Absent") {
		t.Errorf("expected the Verified Absent summary, got %q", summary)
	}
	if !resp.State.Raw.IsNull() {
		t.Error("expected null state after a verified absence")
	}
}
