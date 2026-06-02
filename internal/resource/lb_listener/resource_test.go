package lb_listener

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
