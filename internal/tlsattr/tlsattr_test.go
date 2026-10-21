package tlsattr

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

// ReplayRequireTLS runs require_tls's plan modifiers in order, as the framework does, on
// an update of an existing resource, and reports the planned value and whether a
// replacement was recorded. Exported to the package's tests only.
func replay(t *testing.T, mods []planmodifier.Bool, state, config types.Bool) (types.Bool, bool) {
	t.Helper()
	s := schema.Schema{Attributes: map[string]schema.Attribute{"require_tls": schema.BoolAttribute{Optional: true, Computed: true}}}
	obj := tftypes.NewValue(tftypes.Object{AttributeTypes: map[string]tftypes.Type{"require_tls": tftypes.Bool}},
		map[string]tftypes.Value{"require_tls": tftypes.NewValue(tftypes.Bool, nil)})
	// The framework plans an Optional+Computed attribute with a null config as unknown.
	plan := config
	if config.IsNull() {
		plan = types.BoolUnknown()
	}
	req := planmodifier.BoolRequest{
		Path: path.Root("require_tls"), State: tfsdk.State{Schema: s, Raw: obj}, Plan: tfsdk.Plan{Schema: s, Raw: obj},
		StateValue: state, PlanValue: plan, ConfigValue: config,
	}
	replace := false
	for _, m := range mods {
		r := &planmodifier.BoolResponse{PlanValue: req.PlanValue}
		m.PlanModifyBool(context.Background(), req, r)
		if r.Diagnostics.HasError() {
			t.Fatalf("plan modifier error: %v", r.Diagnostics)
		}
		req.PlanValue = r.PlanValue
		replace = replace || r.RequiresReplace
	}
	return req.PlanValue, replace
}

func TestRequireTLSPlan(t *testing.T) {
	for _, tc := range []struct {
		name          string
		state, config types.Bool
		wantReplace   bool
	}{
		// Legacy state (written before the attribute existed), planned with -refresh=false:
		// never a replacement.
		{"legacy null state, omitted", types.BoolNull(), types.BoolNull(), false},
		{"legacy null state, configured true", types.BoolNull(), types.BoolValue(true), false},
		{"legacy null state, configured false", types.BoolNull(), types.BoolValue(false), false},
		{"omitted against true", types.BoolValue(true), types.BoolNull(), false},
		{"omitted against false", types.BoolValue(false), types.BoolNull(), false},
		{"configured equal", types.BoolValue(false), types.BoolValue(false), false},
		{"true to false", types.BoolValue(true), types.BoolValue(false), true},
		{"false to true", types.BoolValue(false), types.BoolValue(true), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			planned, replace := replay(t, RequireTLSPlanModifiers(), tc.state, tc.config)
			if replace != tc.wantReplace {
				t.Errorf("replace = %v, want %v", replace, tc.wantReplace)
			}
			switch {
			case !tc.config.IsNull() && !planned.Equal(tc.config):
				t.Errorf("a configured value must plan as configured, got %v", planned)
			case tc.config.IsNull() && tc.state.IsNull() && !planned.IsUnknown():
				t.Errorf("legacy null state, omitted: must plan unknown (never null), got %v", planned)
			case tc.config.IsNull() && !tc.state.IsNull() && !planned.Equal(tc.state):
				t.Errorf("omitted: must plan the prior value %v, got %v", tc.state, planned)
			}
		})
	}
}

func TestCheckRequireTLSUnchanged(t *testing.T) {
	read := func(v bool, err error, calls *int) func(context.Context) (bool, error) {
		return func(context.Context) (bool, error) { *calls++; return v, err }
	}
	for _, tc := range []struct {
		name        string
		plan, state types.Bool
		held        bool
		readErr     error
		wantCalls   int
		wantErr     string
	}{
		{"known prior: never re-read", types.BoolValue(true), types.BoolValue(true), false, nil, 0, ""},
		{"omitted: nothing to check", types.BoolUnknown(), types.BoolNull(), false, nil, 0, ""},
		{"null plan: nothing to check", types.BoolNull(), types.BoolNull(), false, nil, 0, ""},
		{"legacy, configured match", types.BoolValue(true), types.BoolNull(), true, nil, 1, ""},
		{"legacy, configured mismatch", types.BoolValue(true), types.BoolNull(), false, nil, 1, "require_tls = false"},
		{"legacy, read fails", types.BoolValue(false), types.BoolNull(), false, errors.New("boom"), 1, "Failed to read MySQL instance"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			var diags diag.Diagnostics
			CheckRequireTLSUnchanged(context.Background(), tc.plan, tc.state, "MySQL instance", read(tc.held, tc.readErr, &calls), &diags)
			if calls != tc.wantCalls {
				t.Errorf("reads = %d, want %d", calls, tc.wantCalls)
			}
			if tc.wantErr == "" {
				if diags.HasError() {
					t.Errorf("unexpected error: %v", diags)
				}
				return
			}
			if !diags.HasError() {
				t.Fatal("want an error")
			}
			e := diags.Errors()[0]
			if !strings.Contains(e.Summary()+e.Detail(), tc.wantErr) {
				t.Errorf("error %q / %q lacks %q", e.Summary(), e.Detail(), tc.wantErr)
			}
			if tc.readErr == nil {
				if wp, ok := e.(diag.DiagnosticWithPath); !ok || !wp.Path().Equal(path.Root("require_tls")) {
					t.Errorf("the refusal must be an attribute error on require_tls, got %#v", e)
				}
			}
		})
	}
}

func TestRequireTLSForCreate(t *testing.T) {
	if RequireTLSForCreate(types.BoolNull()) != nil || RequireTLSForCreate(types.BoolUnknown()) != nil {
		t.Error("an omitted or unknown require_tls must not be sent")
	}
	if v := RequireTLSForCreate(types.BoolValue(false)); v == nil || *v {
		t.Error("a configured false must be sent as false")
	}
	if v := RequireTLSForCreate(types.BoolValue(true)); v == nil || !*v {
		t.Error("a configured true must be sent as true")
	}
}

func TestFromWire(t *testing.T) {
	if !StringFromWire("").IsNull() || StringFromWire("h.x").ValueString() != "h.x" {
		t.Error("StringFromWire")
	}
	if !Int64FromWire(0).IsNull() || Int64FromWire(6380).ValueInt64() != 6380 {
		t.Error("Int64FromWire")
	}
}
