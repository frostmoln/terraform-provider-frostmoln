package planmod

import (
	"context"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

func TestInt64GrowOnly(t *testing.T) {
	m := Int64GrowOnly("GB")
	tests := []struct {
		name      string
		state     types.Int64
		plan      types.Int64
		wantError bool
	}{
		{"grow ok", types.Int64Value(50), types.Int64Value(100), false},
		{"unchanged ok", types.Int64Value(50), types.Int64Value(50), false},
		{"shrink rejected", types.Int64Value(100), types.Int64Value(50), true},
		{"create (null state) ok", types.Int64Null(), types.Int64Value(50), false},
		{"unknown plan ok", types.Int64Value(50), types.Int64Unknown(), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := &planmodifier.Int64Response{}
			m.PlanModifyInt64(context.Background(), planmodifier.Int64Request{
				Path:       path.Root("storage_gb"),
				StateValue: tt.state,
				PlanValue:  tt.plan,
			}, resp)
			if got := resp.Diagnostics.HasError(); got != tt.wantError {
				t.Errorf("HasError() = %v, want %v", got, tt.wantError)
			}
		})
	}
}

func TestStringErrorOnChange(t *testing.T) {
	m := StringErrorOnChange("flavor resize not supported")
	tests := []struct {
		name      string
		state     types.String
		plan      types.String
		wantError bool
	}{
		{"unchanged ok", types.StringValue("db.small"), types.StringValue("db.small"), false},
		{"changed rejected", types.StringValue("db.small"), types.StringValue("db.large"), true},
		{"create (null state) ok", types.StringNull(), types.StringValue("db.small"), false},
		{"unknown plan ok", types.StringValue("db.small"), types.StringUnknown(), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := &planmodifier.StringResponse{}
			m.PlanModifyString(context.Background(), planmodifier.StringRequest{
				Path:       path.Root("flavor_id"),
				StateValue: tt.state,
				PlanValue:  tt.plan,
			}, resp)
			if got := resp.Diagnostics.HasError(); got != tt.wantError {
				t.Errorf("HasError() = %v, want %v", got, tt.wantError)
			}
		})
	}
}

// stateRaw builds a prior-state stub whose Raw is null (create) or non-null (update);
// the *UseStateOrDefault modifiers branch on that to leave a create's value unknown.
func stateRaw(exists bool) tfsdk.State {
	objType := tftypes.Object{AttributeTypes: map[string]tftypes.Type{"x": tftypes.String}}
	if !exists {
		return tfsdk.State{Raw: tftypes.NewValue(objType, nil)}
	}
	return tfsdk.State{Raw: tftypes.NewValue(objType, map[string]tftypes.Value{
		"x": tftypes.NewValue(tftypes.String, "set"),
	})}
}

func TestStringUseStateOrDefault(t *testing.T) {
	m := StringUseStateOrDefault("0 2 * * *")
	tests := []struct {
		name        string
		stateExists bool
		state       types.String
		config      types.String
		plan        types.String
		want        types.String
	}{
		{"config value wins", true, types.StringValue("0 4 * * *"), types.StringValue("0 5 * * *"), types.StringValue("0 5 * * *"), types.StringValue("0 5 * * *")},
		{"omitted pins state", true, types.StringValue("0 4 * * *"), types.StringNull(), types.StringUnknown(), types.StringValue("0 4 * * *")},
		{"null state plans the default", true, types.StringNull(), types.StringNull(), types.StringUnknown(), types.StringValue("0 2 * * *")},
		{"create stays unknown", false, types.StringNull(), types.StringNull(), types.StringUnknown(), types.StringUnknown()},
		{"unknown config stays unknown", true, types.StringNull(), types.StringUnknown(), types.StringUnknown(), types.StringUnknown()},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := &planmodifier.StringResponse{PlanValue: tt.plan}
			m.PlanModifyString(context.Background(), planmodifier.StringRequest{
				Path:        path.Root("backup_schedule"),
				State:       stateRaw(tt.stateExists),
				StateValue:  tt.state,
				ConfigValue: tt.config,
				PlanValue:   tt.plan,
			}, resp)
			if !resp.PlanValue.Equal(tt.want) {
				t.Errorf("PlanValue = %s, want %s", resp.PlanValue, tt.want)
			}
		})
	}
}

func TestInt64UseStateOrDefault(t *testing.T) {
	m := Int64UseStateOrDefault(35)
	tests := []struct {
		name        string
		stateExists bool
		state       types.Int64
		config      types.Int64
		plan        types.Int64
		want        types.Int64
	}{
		{"config value wins", true, types.Int64Value(90), types.Int64Value(60), types.Int64Value(60), types.Int64Value(60)},
		// The case a schema Default gets wrong: dropping a raised retention from HCL must keep
		// the server's 90, not rewrite it to the floor (which would reap 55 days of backups).
		{"omitted pins state", true, types.Int64Value(90), types.Int64Null(), types.Int64Unknown(), types.Int64Value(90)},
		{"null state plans the default", true, types.Int64Null(), types.Int64Null(), types.Int64Unknown(), types.Int64Value(35)},
		{"create stays unknown", false, types.Int64Null(), types.Int64Null(), types.Int64Unknown(), types.Int64Unknown()},
		{"unknown config stays unknown", true, types.Int64Null(), types.Int64Unknown(), types.Int64Unknown(), types.Int64Unknown()},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := &planmodifier.Int64Response{PlanValue: tt.plan}
			m.PlanModifyInt64(context.Background(), planmodifier.Int64Request{
				Path:        path.Root("backup_retention_days"),
				State:       stateRaw(tt.stateExists),
				StateValue:  tt.state,
				ConfigValue: tt.config,
				PlanValue:   tt.plan,
			}, resp)
			if !resp.PlanValue.Equal(tt.want) {
				t.Errorf("PlanValue = %d, want %d", resp.PlanValue, tt.want)
			}
		})
	}
}
