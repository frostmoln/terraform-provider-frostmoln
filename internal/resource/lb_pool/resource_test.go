package lb_pool

import (
	"context"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
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
	req := m.toCreateRequest()
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

	up := m.toUpdateRequest()
	if up.SessionPersistence == nil || up.SessionPersistence.Type != "APP_COOKIE" {
		t.Errorf("expected session_persistence in update request")
	}

	empty := &PoolModel{Name: types.StringValue("x"), Protocol: types.StringValue("tcp"), LBAlgorithm: types.StringValue("round_robin")}
	if empty.toCreateRequest().SessionPersistence != nil {
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
		"created_at":          tftypes.NewValue(tftypes.String, nil),
		"updated_at":          tftypes.NewValue(tftypes.String, nil),
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
