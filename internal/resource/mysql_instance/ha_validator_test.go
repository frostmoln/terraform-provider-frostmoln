package mysql_instance

import (
	"context"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	fwresource "github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// TestHAEnabledIsRejectedAtPlanTime pins the MySQL half of the HA refusal.
//
// The API refuses engine=mysql with haEnabled (high availability needs a different mechanism end
// to end for MySQL). Without a schema validator that refusal lands at APPLY time, mid-graph, after
// the VPC and subnet in the same plan have already been created — so the customer gets a 400 and a
// half-built plan. The validator moves it to plan time, where it costs nothing.
//
// The attribute is deliberately KEPT rather than removed: removing it turns every existing config
// carrying it into an "Unsupported argument" parse error, which is a harder break.
func TestHAEnabledIsRejectedAtPlanTime(t *testing.T) {
	var schemaResp resource.SchemaResponse
	NewResource().(resource.ResourceWithConfigure).Schema(
		context.Background(), resource.SchemaRequest{}, &schemaResp,
	)

	attrDef, ok := schemaResp.Schema.Attributes["ha_enabled"].(fwresource.BoolAttribute)
	if !ok {
		t.Fatal("mysql_instance has no ha_enabled BoolAttribute; removing it outright breaks " +
			"every existing config with an 'Unsupported argument' parse error")
	}
	if len(attrDef.Validators) == 0 {
		t.Fatal("ha_enabled has no validator, so an unsupported MySQL HA request fails at APPLY " +
			"time mid-graph instead of at plan time")
	}
	if attrDef.DeprecationMessage == "" {
		t.Error("ha_enabled should carry a DeprecationMessage so `terraform plan` explains why")
	}

	run := func(v attr.Value) validator.BoolResponse {
		var resp validator.BoolResponse
		req := validator.BoolRequest{
			Path:        path.Root("ha_enabled"),
			ConfigValue: v.(types.Bool),
		}
		for _, val := range attrDef.Validators {
			val.ValidateBool(context.Background(), req, &resp)
		}
		return resp
	}

	if resp := run(types.BoolValue(true)); !resp.Diagnostics.HasError() {
		t.Error("ha_enabled = true was ACCEPTED for MySQL; the API rejects it, so the plan must too")
	}
	if resp := run(types.BoolValue(false)); resp.Diagnostics.HasError() {
		t.Errorf("ha_enabled = false was rejected: %v; only true is unsupported", resp.Diagnostics)
	}
	// An unset attribute must not error, or every MySQL config that never mentions HA breaks.
	if resp := run(types.BoolNull()); resp.Diagnostics.HasError() {
		t.Errorf("an unset ha_enabled was rejected: %v", resp.Diagnostics)
	}
}
