// Package tlsattrtest checks a resource schema's TLS attributes (internal/tlsattr) the
// way the framework plans them: by replaying each attribute's own plan modifiers.
package tlsattrtest

import (
	"context"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

// existing returns non-null raw state/plan objects: the UseStateForUnknown and
// RequiresReplace families return early on a resource being created or destroyed.
func existing() (tfsdk.State, tfsdk.Plan) {
	s := schema.Schema{Attributes: map[string]schema.Attribute{"id": schema.StringAttribute{Computed: true}}}
	obj := tftypes.NewValue(tftypes.Object{AttributeTypes: map[string]tftypes.Type{"id": tftypes.String}},
		map[string]tftypes.Value{"id": tftypes.NewValue(tftypes.String, "x")})
	return tfsdk.State{Schema: s, Raw: obj}, tfsdk.Plan{Schema: s, Raw: obj}
}

// ReplayBool runs a bool attribute's plan modifiers on an update, in order, and returns
// the planned value and whether a replacement was recorded. A null config plans unknown
// first, as the framework does for a Computed attribute.
func ReplayBool(t *testing.T, mods []planmodifier.Bool, name string, state, config types.Bool) (types.Bool, bool) {
	t.Helper()
	st, pl := existing()
	plan := config
	if config.IsNull() {
		plan = types.BoolUnknown()
	}
	req := planmodifier.BoolRequest{Path: path.Root(name), State: st, Plan: pl, StateValue: state, PlanValue: plan, ConfigValue: config}
	replace := false
	for _, m := range mods {
		r := &planmodifier.BoolResponse{PlanValue: req.PlanValue}
		m.PlanModifyBool(context.Background(), req, r)
		if r.Diagnostics.HasError() {
			t.Fatalf("%s: plan modifier error: %v", name, r.Diagnostics)
		}
		req.PlanValue = r.PlanValue
		replace = replace || r.RequiresReplace
	}
	return req.PlanValue, replace
}

func replayString(t *testing.T, mods []planmodifier.String, name string, state types.String) types.String {
	t.Helper()
	st, pl := existing()
	req := planmodifier.StringRequest{Path: path.Root(name), State: st, Plan: pl, StateValue: state, PlanValue: types.StringUnknown(), ConfigValue: types.StringNull()}
	for _, m := range mods {
		r := &planmodifier.StringResponse{PlanValue: req.PlanValue}
		m.PlanModifyString(context.Background(), req, r)
		req.PlanValue = r.PlanValue
	}
	return req.PlanValue
}

func replayInt64(t *testing.T, mods []planmodifier.Int64, name string, state types.Int64) types.Int64 {
	t.Helper()
	st, pl := existing()
	req := planmodifier.Int64Request{Path: path.Root(name), State: st, Plan: pl, StateValue: state, PlanValue: types.Int64Unknown(), ConfigValue: types.Int64Null()}
	for _, m := range mods {
		r := &planmodifier.Int64Response{PlanValue: req.PlanValue}
		m.PlanModifyInt64(context.Background(), req, r)
		req.PlanValue = r.PlanValue
	}
	return req.PlanValue
}

// AssertRequireTLS checks require_tls: Optional+Computed without a schema Default, a
// real change replaces, an omission keeps the prior value, and a state that predates the
// attribute (null) never replaces and plans unknown, never null.
func AssertRequireTLS(t *testing.T, attrs map[string]schema.Attribute) {
	t.Helper()
	a, ok := attrs["require_tls"].(schema.BoolAttribute)
	if !ok {
		t.Fatal("require_tls must be a BoolAttribute")
	}
	if !a.Optional || !a.Computed || a.Default != nil {
		t.Fatal("require_tls must be Optional+Computed without a schema Default (the server owns the default)")
	}
	for _, tc := range []struct {
		name          string
		state, config types.Bool
		replace       bool
	}{
		{"legacy null, omitted", types.BoolNull(), types.BoolNull(), false},
		{"legacy null, configured true", types.BoolNull(), types.BoolValue(true), false},
		{"legacy null, configured false", types.BoolNull(), types.BoolValue(false), false},
		{"omitted against true", types.BoolValue(true), types.BoolNull(), false},
		{"omitted against false", types.BoolValue(false), types.BoolNull(), false},
		{"configured equal", types.BoolValue(true), types.BoolValue(true), false},
		{"true to false", types.BoolValue(true), types.BoolValue(false), true},
		{"false to true", types.BoolValue(false), types.BoolValue(true), true},
	} {
		planned, replace := ReplayBool(t, a.PlanModifiers, "require_tls", tc.state, tc.config)
		if replace != tc.replace {
			t.Errorf("require_tls %s: replace = %v, want %v", tc.name, replace, tc.replace)
		}
		if tc.config.IsNull() && tc.state.IsNull() && !planned.IsUnknown() {
			t.Errorf("require_tls %s: must plan unknown, got %v", tc.name, planned)
		}
		if tc.config.IsNull() && !tc.state.IsNull() && !planned.Equal(tc.state) {
			t.Errorf("require_tls %s: must plan the prior value, got %v", tc.name, planned)
		}
	}
}

// AssertPinnedComputed checks a computed-only TLS attribute that is fixed once known
// (hostname; tls_enabled and tls_port where nothing can change them after the create):
// a null prior (state that predates it) plans unknown, a known prior is kept.
func AssertPinnedComputed(t *testing.T, attrs map[string]schema.Attribute, name string) {
	t.Helper()
	a, ok := attrs[name]
	if !ok {
		t.Fatalf("attribute %q missing", name)
	}
	if !a.IsComputed() || a.IsOptional() || a.IsRequired() {
		t.Errorf("%s must be computed only", name)
	}
	switch v := a.(type) {
	case schema.BoolAttribute:
		for _, prior := range []types.Bool{types.BoolNull(), types.BoolValue(true), types.BoolValue(false)} {
			planned, replace := ReplayBool(t, v.PlanModifiers, name, prior, types.BoolNull())
			if replace || (prior.IsNull() && !planned.IsUnknown()) || (!prior.IsNull() && !planned.Equal(prior)) {
				t.Errorf("%s: prior %v planned %v (replace %v)", name, prior, planned, replace)
			}
		}
	case schema.StringAttribute:
		for _, prior := range []types.String{types.StringNull(), types.StringValue("x.web.v.t.o.fm.internal")} {
			planned := replayString(t, v.PlanModifiers, name, prior)
			if (prior.IsNull() && !planned.IsUnknown()) || (!prior.IsNull() && !planned.Equal(prior)) {
				t.Errorf("%s: prior %v planned %v", name, prior, planned)
			}
		}
	case schema.Int64Attribute:
		for _, prior := range []types.Int64{types.Int64Null(), types.Int64Value(6380)} {
			planned := replayInt64(t, v.PlanModifiers, name, prior)
			if (prior.IsNull() && !planned.IsUnknown()) || (!prior.IsNull() && !planned.Equal(prior)) {
				t.Errorf("%s: prior %v planned %v", name, prior, planned)
			}
		}
	default:
		t.Fatalf("%s: unexpected attribute type %T", name, a)
	}
}
