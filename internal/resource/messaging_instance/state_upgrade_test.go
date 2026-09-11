package messaging_instance

import (
	"context"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

// TestUpgradeState_V0ToV1 guards the v0→v1 migration across the `engine` -> `type` rename.
//
// 🔴 THIS IS THE TEST FOR A BROKER-DESTROYING BUG, not a tidiness one. `type` carries a Default,
// so it is present in EVERY state file an older provider wrote. Without the upgrader the framework
// drops the unknown `engine` key and leaves `type` NULL; `terraform plan -refresh=false` — ordinary
// in CI, and the one path that never calls Read to repopulate it — then plans the Default against
// that null, RequiresReplace fires, and Terraform proposes to DESTROY AND RECREATE the instance.
// A persistent LavinMQ broker loses its queues for a rename.
func TestUpgradeState_V0ToV1(t *testing.T) {
	ctx := context.Background()
	r := &messagingInstanceResource{}

	up, ok := r.UpgradeState(ctx)[0]
	if !ok {
		t.Fatal("expected a v0 state upgrader — without one the rename destroys existing brokers")
	}
	if up.PriorSchema == nil {
		t.Fatal("expected PriorSchema for v0")
	}
	if _, ok := up.PriorSchema.Attributes["engine"]; !ok {
		t.Error("prior schema must carry the old `engine` attribute — that is what v0 state holds")
	}
	if _, ok := up.PriorSchema.Attributes["type"]; ok {
		t.Error("prior schema must not carry the new `type` attribute")
	}

	priorType := up.PriorSchema.Type().TerraformType(ctx)
	raw := map[string]tftypes.Value{}
	for name, at := range priorType.(tftypes.Object).AttributeTypes {
		raw[name] = tftypes.NewValue(at, nil)
	}
	raw["id"] = tftypes.NewValue(tftypes.String, "msg-123")
	raw["name"] = tftypes.NewValue(tftypes.String, "my-broker")
	raw["engine"] = tftypes.NewValue(tftypes.String, "lavinmq")
	priorVal := tftypes.NewValue(priorType, raw)

	var schemaResp resource.SchemaResponse
	r.Schema(ctx, resource.SchemaRequest{}, &schemaResp)

	req := resource.UpgradeStateRequest{State: &tfsdk.State{Schema: *up.PriorSchema, Raw: priorVal}}
	resp := &resource.UpgradeStateResponse{State: tfsdk.State{Schema: schemaResp.Schema}}

	up.StateUpgrader(ctx, req, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected errors: %v", resp.Diagnostics.Errors())
	}
	var model MessagingInstanceModel
	resp.State.Get(ctx, &model)
	if model.Type.ValueString() != "lavinmq" {
		t.Errorf("expected type lavinmq carried over from engine, got %q — a NULL here is the "+
			"forced-replacement path", model.Type.ValueString())
	}
	if model.ID.ValueString() != "msg-123" {
		t.Errorf("expected id carried through, got %s", model.ID.ValueString())
	}
	if model.Name.ValueString() != "my-broker" {
		t.Errorf("expected name carried through, got %s", model.Name.ValueString())
	}
}

// TestSchemaVersionIsBumped — a state upgrader registered against an UNBUMPED schema version is
// never invoked: the framework only upgrades when the stored version is lower than the schema's.
func TestSchemaVersionIsBumped(t *testing.T) {
	var schemaResp resource.SchemaResponse
	(&messagingInstanceResource{}).Schema(context.Background(), resource.SchemaRequest{}, &schemaResp)
	if schemaResp.Schema.Version != 1 {
		t.Errorf("schema Version = %d, want 1 — with version 0 the v0 upgrader never runs and the "+
			"rename still destroys brokers", schemaResp.Schema.Version)
	}
}
