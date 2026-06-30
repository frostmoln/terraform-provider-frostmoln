// Package stateupgrade holds reusable terraform-plugin-framework state
// upgraders shared across resources.
package stateupgrade

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

// RenameStringAttr returns a v0→v1 StateUpgrader that renames a single
// string attribute from oldName to newName, carrying every other attribute
// through unchanged.
//
// currentSchema is the resource's post-rename (v1) schema; the prior schema is
// derived from it by swapping newName back to oldName, so callers don't restate
// the whole attribute set. The result is built strictly against the resource's
// current object type (taken from resp.State at upgrade time): the renamed
// attribute takes the prior value, every other current attribute is copied from
// the prior state when present, and any current attribute the older state
// predates is null-filled. Because the result is constructed from the current
// type — not from the prior attribute set — it can never carry an extra or
// missing attribute, and the upgrader degrades gracefully if a later schema
// version adds attributes. Both attributes must be string-typed.
func RenameStringAttr(ctx context.Context, currentSchema schema.Schema, oldName, newName string) resource.StateUpgrader {
	priorAttrs := make(map[string]schema.Attribute, len(currentSchema.Attributes))
	for k, v := range currentSchema.Attributes {
		priorAttrs[k] = v
	}
	delete(priorAttrs, newName)
	priorAttrs[oldName] = schema.StringAttribute{Required: true}

	return resource.StateUpgrader{
		PriorSchema: &schema.Schema{Attributes: priorAttrs},
		StateUpgrader: func(ctx context.Context, req resource.UpgradeStateRequest, resp *resource.UpgradeStateResponse) {
			var prior map[string]tftypes.Value
			if err := req.State.Raw.As(&prior); err != nil {
				resp.Diagnostics.AddError("Failed to read prior state for upgrade", err.Error())
				return
			}
			renamed, ok := prior[oldName]
			if !ok {
				resp.Diagnostics.AddError("Prior state missing attribute", fmt.Sprintf("expected %q in prior state", oldName))
				return
			}

			objType, ok := resp.State.Schema.Type().TerraformType(ctx).(tftypes.Object)
			if !ok {
				resp.Diagnostics.AddError("Unexpected schema type", "resource schema is not an object")
				return
			}
			out := make(map[string]tftypes.Value, len(objType.AttributeTypes))
			for name, attrType := range objType.AttributeTypes {
				if name == newName {
					out[name] = renamed
				} else if v, present := prior[name]; present {
					out[name] = v
				} else {
					out[name] = tftypes.NewValue(attrType, nil)
				}
			}
			resp.State.Raw = tftypes.NewValue(objType, out)
		},
	}
}
