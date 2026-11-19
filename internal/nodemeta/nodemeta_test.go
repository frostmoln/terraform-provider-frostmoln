package nodemeta

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
)

// Cases mirror servicekit provcmd kubernetes_nodemeta_test.go: the server is the
// authority, this copy must refuse at least what it refuses and accept what it accepts.
func TestValidKey(t *testing.T) {
	for key, ok := range map[string]bool{
		"workload":                               true,
		"example.com/team":                       true,
		"dedicated":                              true,
		"a.b-c_d":                                true,
		"kubernetes.io/role":                     false,
		"node-role.kubernetes.io/worker":         false,
		"k8s.io/x":                               false,
		"sub.k8s.io/x":                           false,
		"frostmoln.se/x":                         false,
		"csi.frostmoln.cloud/x":                  false,
		"cilium.io/x":                            false,
		"notkubernetes.io/x":                     true, // suffix must be a whole domain label
		"Example.com/x":                          false,
		"-bad":                                   false,
		"":                                       false,
		strings.Repeat("a", 64):                  false,
		"example.com/" + strings.Repeat("a", 63): true,
	} {
		if err := ValidKey(key); (err == nil) != ok {
			t.Errorf("ValidKey(%q) = %v, want ok=%v", key, err, ok)
		}
	}
}

func TestValidValue(t *testing.T) {
	for v, ok := range map[string]bool{"": true, "sensors": true, "a.b_c-d": true, "-x": false, strings.Repeat("a", 64): false, "a b": false} {
		if err := ValidValue(v); (err == nil) != ok {
			t.Errorf("ValidValue(%q) = %v, want ok=%v", v, err, ok)
		}
	}
}

func TestFromAPI_EmptyKeepsUnsetNull(t *testing.T) {
	var d diag.Diagnostics
	var labels types.Map
	var taints types.Set
	l, ts := FromAPI(map[string]string{}, nil, labels, taints, &d)
	if d.HasError() || !l.IsNull() || !ts.IsNull() || l.ElementType(context.Background()) == nil || ts.ElementType(context.Background()) == nil {
		t.Fatalf("want typed nulls, got %v %v %v", l, ts, d)
	}
}

func TestFromAPI_RoundTrip(t *testing.T) {
	var d diag.Diagnostics
	l, ts := FromAPI(map[string]string{"workload": "sensors"},
		[]APITaint{{Key: "dedicated", Value: "sensors", Effect: "NoSchedule"}, {Key: "soft", Effect: "PreferNoSchedule"}},
		types.MapNull(types.StringType), types.SetNull(types.ObjectType{AttrTypes: TaintAttrTypes}), &d)
	if d.HasError() {
		t.Fatal(d)
	}
	gotL, gotT := ToAPI(context.Background(), l, ts, &d)
	if gotL["workload"] != "sensors" || len(gotT) != 2 {
		t.Fatalf("round trip lost data: %v %v", gotL, gotT)
	}
	for _, tt := range gotT {
		if tt.Key == "soft" && tt.Value != "" {
			t.Errorf("absent value must read back as null, not %q", tt.Value)
		}
	}
}

func conflict(reason, msg string) error {
	return fmt.Errorf("wrapped: %w", &client.APIError{StatusCode: 409, Code: "conflict", Message: msg, Details: map[string]any{"reason": reason}})
}

func TestRefusal_BranchesOnReasonNotMessage(t *testing.T) {
	for reason, want := range map[string]string{
		ReasonOnlyTainted:   "Only Tainted",
		ReasonRetryMismatch: "Failed Node Pool",
		ReasonFixed:         "Fixed",
	} {
		s, d, ok := Refusal(conflict(reason, "x"))
		if !ok || !strings.Contains(s, want) || !strings.Contains(d, "Underlying error") {
			t.Errorf("%s: got %q %v", reason, s, ok)
		}
	}
	// The message alone never decides.
	if _, _, ok := Refusal(conflict("", "would leave every worker node ... taint")); ok {
		t.Error("a conflict without details.reason must not map")
	}
	if _, _, ok := Refusal(errors.New("only_tainted_nodes")); ok {
		t.Error("a non-API error must not map")
	}
	if s, _, _ := Refusal(conflict(ReasonOnlyTainted, "")); !strings.Contains(s, "Tainted") {
		t.Error("only_tainted_nodes must map")
	}
}

func TestEffectValidator_InitialPoolSoftOnly(t *testing.T) {
	v := effectValidator{allowed: []string{"PreferNoSchedule"}}
	for effect, ok := range map[string]bool{"PreferNoSchedule": true, "NoSchedule": false, "NoExecute": false, "Bogus": false} {
		resp := &validator.StringResponse{}
		v.ValidateString(context.Background(), validator.StringRequest{Path: path.Root("effect"), ConfigValue: types.StringValue(effect)}, resp)
		if resp.Diagnostics.HasError() == ok {
			t.Errorf("effect %s: errors=%v, want ok=%v", effect, resp.Diagnostics, ok)
		}
	}
	resp := &validator.StringResponse{}
	v.ValidateString(context.Background(), validator.StringRequest{Path: path.Root("effect"), ConfigValue: types.StringValue("NoSchedule")}, resp)
	if !strings.Contains(resp.Diagnostics.Errors()[0].Detail(), "not accepted on the initial pool") {
		t.Errorf("want the server's reason, got %v", resp.Diagnostics)
	}
}

func TestTaintsValidator_DuplicateKeyEffect(t *testing.T) {
	var d diag.Diagnostics
	_, ts := FromAPI(nil, []APITaint{{Key: "a", Value: "1", Effect: "NoSchedule"}, {Key: "a", Value: "2", Effect: "NoSchedule"}},
		types.MapNull(types.StringType), types.SetNull(types.ObjectType{AttrTypes: TaintAttrTypes}), &d)
	resp := &validator.SetResponse{}
	taintsValidator{}.ValidateSet(context.Background(), validator.SetRequest{Path: path.Root("taints"), ConfigValue: ts}, resp)
	if !resp.Diagnostics.HasError() {
		t.Fatal("same key+effect twice must be refused")
	}
}

// Unset and empty are the same set on the wire: moving between them must not
// replace a pool (or, on initial_node_pool, the cluster). A real change must.
func TestReplace_NullAndEmptyAreTheSameSet(t *testing.T) {
	ctx := context.Background()
	objType := types.ObjectType{AttrTypes: TaintAttrTypes}
	one := types.SetValueMust(objType, []attr.Value{types.ObjectValueMust(TaintAttrTypes, map[string]attr.Value{
		"key": types.StringValue("k"), "value": types.StringNull(), "effect": types.StringValue("NoSchedule"),
	})})
	for _, c := range []struct {
		state, plan types.Set
		want        bool
	}{
		{types.SetNull(objType), types.SetValueMust(objType, nil), false},
		{types.SetValueMust(objType, nil), types.SetNull(objType), false},
		{types.SetNull(objType), one, true},
		{one, types.SetNull(objType), true},
	} {
		resp := &planmodifier.SetResponse{PlanValue: c.plan}
		for _, m := range TaintsAttribute("pool", false).PlanModifiers {
			m.PlanModifySet(ctx, planmodifier.SetRequest{
				StateValue: c.state, PlanValue: c.plan, ConfigValue: c.plan,
				State: tfsdk.State{Raw: tftypes.NewValue(tftypes.Object{}, map[string]tftypes.Value{})},
				Plan:  tfsdk.Plan{Raw: tftypes.NewValue(tftypes.Object{}, map[string]tftypes.Value{})},
			}, resp)
		}
		if resp.RequiresReplace != c.want {
			t.Errorf("taints %v -> %v: replace=%v, want %v", c.state, c.plan, resp.RequiresReplace, c.want)
		}
	}
	labels := types.MapValueMust(types.StringType, map[string]attr.Value{"a": types.StringValue("1")})
	for _, c := range []struct {
		state, plan types.Map
		want        bool
	}{
		{types.MapNull(types.StringType), types.MapValueMust(types.StringType, nil), false},
		{types.MapNull(types.StringType), labels, true},
	} {
		resp := &planmodifier.MapResponse{PlanValue: c.plan}
		for _, m := range LabelsAttribute("pool").PlanModifiers {
			m.PlanModifyMap(ctx, planmodifier.MapRequest{
				StateValue: c.state, PlanValue: c.plan, ConfigValue: c.plan,
				State: tfsdk.State{Raw: tftypes.NewValue(tftypes.Object{}, map[string]tftypes.Value{})},
				Plan:  tfsdk.Plan{Raw: tftypes.NewValue(tftypes.Object{}, map[string]tftypes.Value{})},
			}, resp)
		}
		if resp.RequiresReplace != c.want {
			t.Errorf("labels %v -> %v: replace=%v, want %v", c.state, c.plan, resp.RequiresReplace, c.want)
		}
	}
}

func TestLabelsValidator_NullValueRefusedAtPlan(t *testing.T) {
	resp := &validator.MapResponse{}
	labelsValidator{}.ValidateMap(context.Background(), validator.MapRequest{
		Path:        path.Root("labels"),
		ConfigValue: types.MapValueMust(types.StringType, map[string]attr.Value{"a": types.StringNull()}),
	}, resp)
	if !resp.Diagnostics.HasError() {
		t.Fatal("a null label value must be refused at plan time")
	}
}

func TestValidKey_MoreServicekitCases(t *testing.T) {
	for key, ok := range map[string]bool{
		"a/b/c":                                   false,
		strings.Repeat("a", 254) + "/x":           false,
		"example.com/" + strings.Repeat("a", 64):  false,
		strings.Repeat("a.", 126) + "a/" + "name": true,
	} {
		if err := ValidKey(key); (err == nil) != ok {
			t.Errorf("ValidKey(%q...) = %v, want ok=%v", key[:min(len(key), 20)], err, ok)
		}
	}
	resp := &validator.StringResponse{}
	keyValidator{taint: true}.ValidateString(context.Background(), validator.StringRequest{
		Path: path.Root("key"), ConfigValue: types.StringValue("ToBeDeletedByClusterAutoscaler"),
	}, resp)
	if !resp.Diagnostics.HasError() {
		t.Error("the autoscaler taint key must be refused")
	}
}

func TestRefusal_NeedsCodeConflict(t *testing.T) {
	err := &client.APIError{StatusCode: 409, Code: "invalid_state", Details: map[string]any{"reason": ReasonOnlyTainted}}
	if _, _, ok := Refusal(err); ok {
		t.Error("only code conflict carries the node-meta reasons")
	}
}
