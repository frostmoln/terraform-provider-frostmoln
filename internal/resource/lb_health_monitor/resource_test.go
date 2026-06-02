package lb_health_monitor

import (
	"context"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
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
	model.fromAPI("lb-1", hm)
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
	req := m.toUpdateRequest()
	if req.ExpectedCodes == nil {
		t.Fatalf("expected ExpectedCodes in update request, got nil")
	}
	if *req.ExpectedCodes != "200,202" {
		t.Errorf("expected 200,202, got %s", *req.ExpectedCodes)
	}

	// Null expected_codes should be omitted.
	empty := &HealthMonitorModel{ExpectedCodes: types.StringNull()}
	if empty.toUpdateRequest().ExpectedCodes != nil {
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
		"created_at":       tftypes.NewValue(tftypes.String, nil),
		"updated_at":       tftypes.NewValue(tftypes.String, nil),
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
