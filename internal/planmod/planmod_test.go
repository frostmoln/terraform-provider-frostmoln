package planmod

import (
	"context"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
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
