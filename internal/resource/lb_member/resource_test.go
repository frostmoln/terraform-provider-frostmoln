package lb_member

import (
	"context"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/boolplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
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
