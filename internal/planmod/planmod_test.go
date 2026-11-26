package planmod

import (
	"context"
	"strings"
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
		name     string
		state    types.Int64
		plan     types.Int64
		wantWarn bool
	}{
		{"grow ok", types.Int64Value(50), types.Int64Value(100), false},
		{"unchanged ok", types.Int64Value(50), types.Int64Value(50), false},
		{"shrink warns", types.Int64Value(100), types.Int64Value(50), true},
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
			// NEVER an error: an error raised by a plan modifier also aborts
			// `terraform destroy` (the destroy plan's refresh phase runs attribute
			// plan modifiers against a real, non-null plan). The shrink is refused
			// in the resource's Update instead.
			if resp.Diagnostics.HasError() {
				t.Errorf("modifier raised an error diagnostic (blocks terraform destroy): %v", resp.Diagnostics.Errors())
			}
			if got := resp.Diagnostics.WarningsCount() > 0; got != tt.wantWarn {
				t.Errorf("hasWarning = %v, want %v", got, tt.wantWarn)
			}
		})
	}
}

func TestStringWarnOnChange(t *testing.T) {
	m := StringWarnOnChange("flavor resize not supported")
	tests := []struct {
		name     string
		state    types.String
		plan     types.String
		wantWarn bool
	}{
		{"unchanged ok", types.StringValue("db.small"), types.StringValue("db.small"), false},
		{"changed warns", types.StringValue("db.small"), types.StringValue("db.large"), true},
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
			// See TestInt64GrowOnly: an error here would block terraform destroy.
			if resp.Diagnostics.HasError() {
				t.Errorf("modifier raised an error diagnostic (blocks terraform destroy): %v", resp.Diagnostics.Errors())
			}
			if got := resp.Diagnostics.WarningsCount() > 0; got != tt.wantWarn {
				t.Errorf("hasWarning = %v, want %v", got, tt.wantWarn)
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

// sizeObj builds a state/plan Raw holding a requested size_gb (nil = null).
func sizeObj(sizeGB *int64) tftypes.Value {
	objType := tftypes.Object{AttributeTypes: map[string]tftypes.Type{"size_gb": tftypes.Number}}
	var v tftypes.Value
	if sizeGB == nil {
		v = tftypes.NewValue(tftypes.Number, tftypes.UnknownValue)
	} else {
		v = tftypes.NewValue(tftypes.Number, *sizeGB)
	}
	return tftypes.NewValue(objType, map[string]tftypes.Value{"size_gb": v})
}

func TestInt64SizeBytes(t *testing.T) {
	m := Int64SizeBytes("size_gb")
	gb := func(n int64) *int64 { return &n }
	nullObj := tftypes.NewValue(tftypes.Object{AttributeTypes: map[string]tftypes.Type{"size_gb": tftypes.Number}}, nil)
	const allocated = int64(94) << 30
	tests := []struct {
		name  string
		state tftypes.Value
		plan  tftypes.Value
		prior types.Int64
		want  types.Int64
	}{
		{"unchanged request keeps state", sizeObj(gb(100)), sizeObj(gb(100)), types.Int64Value(allocated), types.Int64Value(allocated)},
		{"resize is known after apply", sizeObj(gb(100)), sizeObj(gb(200)), types.Int64Value(allocated), types.Int64Unknown()},
		{"unknown request is known after apply", sizeObj(gb(100)), sizeObj(nil), types.Int64Value(allocated), types.Int64Unknown()},
		{"create is known after apply", nullObj, sizeObj(gb(100)), types.Int64Null(), types.Int64Unknown()},
		{"state predating size_bytes is known after apply", sizeObj(gb(100)), sizeObj(gb(100)), types.Int64Null(), types.Int64Unknown()},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := &planmodifier.Int64Response{PlanValue: types.Int64Unknown()}
			m.PlanModifyInt64(context.Background(), planmodifier.Int64Request{
				Path:       path.Root("size_bytes"),
				State:      tfsdk.State{Raw: tt.state},
				Plan:       tfsdk.Plan{Raw: tt.plan},
				StateValue: tt.prior,
				PlanValue:  types.Int64Unknown(),
			}, resp)
			if !resp.PlanValue.Equal(tt.want) {
				t.Errorf("plan = %v, want %v", resp.PlanValue, tt.want)
			}
		})
	}
}

// TestInt64SizeBytesOptionalComputedSize pins the redis/valkey shape: storage_gb
// is Optional+Computed, so with it left out of the configuration the plan this
// modifier sees holds it unknown (its own UseStateForUnknown has not run yet).
// A null config value keeps the server's size, so size_bytes keeps its state.
func TestInt64SizeBytesOptionalComputedSize(t *testing.T) {
	m := Int64SizeBytes("storage_gb", "flavor_id", "persistence_mode")
	objType := tftypes.Object{AttributeTypes: map[string]tftypes.Type{
		"storage_gb": tftypes.Number, "flavor_id": tftypes.String, "persistence_mode": tftypes.String,
	}}
	obj := func(storage, flavor, mode tftypes.Value) tftypes.Value {
		return tftypes.NewValue(objType, map[string]tftypes.Value{"storage_gb": storage, "flavor_id": flavor, "persistence_mode": mode})
	}
	num := func(n int64) tftypes.Value { return tftypes.NewValue(tftypes.Number, n) }
	str := func(s string) tftypes.Value { return tftypes.NewValue(tftypes.String, s) }
	unknownNum := tftypes.NewValue(tftypes.Number, tftypes.UnknownValue)
	unknownStr := tftypes.NewValue(tftypes.String, tftypes.UnknownValue)
	nullNum := tftypes.NewValue(tftypes.Number, nil)
	nullStr := tftypes.NewValue(tftypes.String, nil)
	const allocated = int64(10) << 30
	state := obj(num(10), str("cache.mo1.small"), str("rdb"))
	tests := []struct {
		name   string
		plan   tftypes.Value
		config tftypes.Value
		want   types.Int64
	}{
		{"storage_gb and persistence_mode unset keeps state",
			obj(unknownNum, str("cache.mo1.small"), unknownStr), obj(nullNum, str("cache.mo1.small"), nullStr), types.Int64Value(allocated)},
		{"storage_gb from an unresolved reference is known after apply",
			obj(unknownNum, str("cache.mo1.small"), str("rdb")), obj(unknownNum, str("cache.mo1.small"), str("rdb")), types.Int64Unknown()},
		{"a flavor change is known after apply",
			obj(unknownNum, str("cache.mo1.medium"), str("rdb")), obj(nullNum, str("cache.mo1.medium"), str("rdb")), types.Int64Unknown()},
		{"a configured resize is known after apply",
			obj(num(20), str("cache.mo1.small"), str("rdb")), obj(num(20), str("cache.mo1.small"), str("rdb")), types.Int64Unknown()},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := &planmodifier.Int64Response{PlanValue: types.Int64Unknown()}
			m.PlanModifyInt64(context.Background(), planmodifier.Int64Request{
				Path:       path.Root("size_bytes"),
				State:      tfsdk.State{Raw: state},
				Plan:       tfsdk.Plan{Raw: tt.plan},
				Config:     tfsdk.Config{Raw: tt.config},
				StateValue: types.Int64Value(allocated),
				PlanValue:  types.Int64Unknown(),
			}, resp)
			if !resp.PlanValue.Equal(tt.want) {
				t.Errorf("plan = %v, want %v", resp.PlanValue, tt.want)
			}
		})
	}
}

func TestInt64GrowExceedsAllocation(t *testing.T) {
	m := Int64GrowExceedsAllocation()
	objType := tftypes.Object{AttributeTypes: map[string]tftypes.Type{"storage_gb": tftypes.Number, "size_bytes": tftypes.Number}}
	stateWith := func(sizeBytes any) tftypes.Value {
		return tftypes.NewValue(objType, map[string]tftypes.Value{
			"storage_gb": tftypes.NewValue(tftypes.Number, int64(100)),
			"size_bytes": tftypes.NewValue(tftypes.Number, sizeBytes),
		})
	}
	legacy := stateWith(int64(100) << 30) // 100 GiB = 107.37 GB
	tests := []struct {
		name     string
		state    tftypes.Value
		plan     types.Int64
		wantWarn bool
	}{
		{"a grow below the true size warns", legacy, types.Int64Value(105), true},
		{"a grow to the floor of the true size warns", legacy, types.Int64Value(107), true},
		{"a grow past the true size is fine", legacy, types.Int64Value(108), false},
		{"unchanged is fine", legacy, types.Int64Value(100), false},
		{"a shrink is left to Int64GrowOnly", legacy, types.Int64Value(50), false},
		{"a GB-native resource grows freely", stateWith(int64(94) << 30), types.Int64Value(101), false},
		{"no size_bytes in state is fine", stateWith(nil), types.Int64Value(105), false},
		{"an unknown plan is fine", legacy, types.Int64Unknown(), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := &planmodifier.Int64Response{}
			m.PlanModifyInt64(context.Background(), planmodifier.Int64Request{
				Path:       path.Root("storage_gb"),
				State:      tfsdk.State{Raw: tt.state},
				StateValue: types.Int64Value(100),
				PlanValue:  tt.plan,
			}, resp)
			if got := resp.Diagnostics.WarningsCount() > 0; got != tt.wantWarn {
				t.Errorf("warned = %v, want %v (%v)", got, tt.wantWarn, resp.Diagnostics)
			}
			if resp.Diagnostics.HasError() {
				t.Errorf("must never error: %v", resp.Diagnostics)
			}
		})
	}
	t.Run("the warning names the smallest accepted request", func(t *testing.T) {
		resp := &planmodifier.Int64Response{}
		m.PlanModifyInt64(context.Background(), planmodifier.Int64Request{
			Path: path.Root("storage_gb"), State: tfsdk.State{Raw: legacy},
			StateValue: types.Int64Value(100), PlanValue: types.Int64Value(105),
		}, resp)
		if d := resp.Diagnostics.Warnings()[0].Detail(); !strings.Contains(d, "107.37 GB") || !strings.Contains(d, "at least 108 GB") {
			t.Errorf("detail = %q", d)
		}
	})
}
