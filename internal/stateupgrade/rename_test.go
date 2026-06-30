package stateupgrade

import (
	"context"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

type renameModel struct {
	ID       types.String `tfsdk:"id"`
	Name     types.String `tfsdk:"name"`
	FlavorID types.String `tfsdk:"flavor_id"`
}

func v1Schema() schema.Schema {
	return schema.Schema{
		Version: 1,
		Attributes: map[string]schema.Attribute{
			"id":        schema.StringAttribute{Computed: true},
			"name":      schema.StringAttribute{Required: true},
			"flavor_id": schema.StringAttribute{Required: true},
		},
	}
}

// TestRenameStringAttr_V0ToV1 proves the upgrader copies the prior `flavor`
// value into `flavor_id`, drops `flavor`, and carries the other attributes
// through unchanged.
func TestRenameStringAttr_V0ToV1(t *testing.T) {
	ctx := context.Background()
	up := RenameStringAttr(ctx, v1Schema(), "flavor", "flavor_id")

	if up.PriorSchema == nil {
		t.Fatal("expected a PriorSchema")
	}
	if _, ok := up.PriorSchema.Attributes["flavor"]; !ok {
		t.Error("prior schema must carry the old `flavor` attribute")
	}
	if _, ok := up.PriorSchema.Attributes["flavor_id"]; ok {
		t.Error("prior schema must not carry the new `flavor_id` attribute")
	}

	priorType := up.PriorSchema.Type().TerraformType(ctx)
	priorVal := tftypes.NewValue(priorType, map[string]tftypes.Value{
		"id":     tftypes.NewValue(tftypes.String, "db-123"),
		"name":   tftypes.NewValue(tftypes.String, "my-db"),
		"flavor": tftypes.NewValue(tftypes.String, "db.gp1.small"),
	})

	req := resource.UpgradeStateRequest{
		State: &tfsdk.State{Schema: *up.PriorSchema, Raw: priorVal},
	}
	resp := &resource.UpgradeStateResponse{State: tfsdk.State{Schema: v1Schema()}}

	up.StateUpgrader(ctx, req, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected errors: %v", resp.Diagnostics.Errors())
	}

	var got renameModel
	resp.State.Get(ctx, &got)
	if got.FlavorID.ValueString() != "db.gp1.small" {
		t.Errorf("expected flavor_id db.gp1.small, got %s", got.FlavorID.ValueString())
	}
	if got.ID.ValueString() != "db-123" {
		t.Errorf("expected id carried through, got %s", got.ID.ValueString())
	}
	if got.Name.ValueString() != "my-db" {
		t.Errorf("expected name carried through, got %s", got.Name.ValueString())
	}
}

// TestRenameStringAttr_NullFillsNewerAttrs proves the upgrader builds against
// the current object type: an attribute the prior state predates (here `region`,
// simulating a later schema version) is null-filled rather than causing a
// missing-key failure, so a stale 0→current path degrades gracefully.
func TestRenameStringAttr_NullFillsNewerAttrs(t *testing.T) {
	ctx := context.Background()

	current := schema.Schema{
		Version: 2,
		Attributes: map[string]schema.Attribute{
			"id":        schema.StringAttribute{Computed: true},
			"name":      schema.StringAttribute{Required: true},
			"flavor_id": schema.StringAttribute{Required: true},
			"region":    schema.StringAttribute{Optional: true},
		},
	}
	up := RenameStringAttr(ctx, current, "flavor", "flavor_id")

	// Prior state predates both `flavor_id` (it still has `flavor`) and `region`.
	priorSchema := schema.Schema{Attributes: map[string]schema.Attribute{
		"id":     schema.StringAttribute{Computed: true},
		"name":   schema.StringAttribute{Required: true},
		"flavor": schema.StringAttribute{Required: true},
	}}
	priorType := priorSchema.Type().TerraformType(ctx)
	priorVal := tftypes.NewValue(priorType, map[string]tftypes.Value{
		"id":     tftypes.NewValue(tftypes.String, "db-9"),
		"name":   tftypes.NewValue(tftypes.String, "n"),
		"flavor": tftypes.NewValue(tftypes.String, "db.gp1.small"),
	})

	req := resource.UpgradeStateRequest{State: &tfsdk.State{Schema: priorSchema, Raw: priorVal}}
	resp := &resource.UpgradeStateResponse{State: tfsdk.State{Schema: current}}
	up.StateUpgrader(ctx, req, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected errors: %v", resp.Diagnostics.Errors())
	}

	var got struct {
		ID       types.String `tfsdk:"id"`
		Name     types.String `tfsdk:"name"`
		FlavorID types.String `tfsdk:"flavor_id"`
		Region   types.String `tfsdk:"region"`
	}
	resp.State.Get(ctx, &got)
	if got.FlavorID.ValueString() != "db.gp1.small" {
		t.Errorf("expected flavor_id db.gp1.small, got %s", got.FlavorID.ValueString())
	}
	if !got.Region.IsNull() {
		t.Errorf("expected region null-filled, got %q", got.Region.ValueString())
	}
}
