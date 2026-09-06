package kubernetes_cluster

import (
	"context"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

func addonSet(keys ...string) types.Set {
	elems := make([]attr.Value, 0, len(keys))
	for _, k := range keys {
		elems = append(elems, types.StringValue(k))
	}
	return types.SetValueMust(types.StringType, elems)
}

// liveRaw is a non-null tftypes.Value standing in for "this resource exists in state /
// is not being destroyed". The modifier only reads whether the whole-object Raw is null,
// so the object's shape is irrelevant here.
func liveRaw() tfsdk.State {
	return tfsdk.State{Raw: tftypes.NewValue(tftypes.Object{
		AttributeTypes: map[string]tftypes.Type{"id": tftypes.String},
	}, map[string]tftypes.Value{"id": tftypes.NewValue(tftypes.String, "cl-1")})}
}

func livePlanRaw() tfsdk.Plan {
	return tfsdk.Plan{Raw: liveRaw().Raw}
}

// wantReplace keeps the assertions one line each without pulling a matcher library
// into a repo that has none.
func wantReplace(t *testing.T, got, want bool, why string) {
	t.Helper()
	if got != want {
		t.Fatalf("RequiresReplace = %v, want %v: %s", got, want, why)
	}
}

func modifyAddons(t *testing.T, state, plan types.Set, destroying bool) bool {
	t.Helper()
	req := planmodifier.SetRequest{
		State:      liveRaw(),
		Plan:       livePlanRaw(),
		StateValue: state,
		PlanValue:  plan,
	}
	if destroying {
		req.Plan = tfsdk.Plan{Raw: tftypes.Value{}}
	}
	var resp planmodifier.SetResponse
	addonRemovalReplacer{}.PlanModifySet(context.Background(), req, &resp)
	return resp.RequiresReplace
}

// 🔴 THE ONE THAT MATTERS. Until the addons endpoint existed the attribute carried a
// plain RequiresReplace(), so ADDING an addon destroyed and rebuilt the customer's whole
// cluster — every workload on it — to perform an operation the API now does in place,
// in the background, with no outage.
func TestAddonPlanModifier_AddingAnAddonDoesNotReplaceTheCluster(t *testing.T) {
	wantReplace(t, modifyAddons(t, addonSet(), addonSet("external-dns"), false), false,
		"adding an addon is an in-place day-2 operation and must NOT destroy the cluster")
	wantReplace(t, modifyAddons(t, addonSet("external-dns"), addonSet("external-dns", "external-secrets"), false), false,
		"adding beside an existing addon must not replace either")
}

// Removing IS a replacement, and honestly so: nothing in the platform deletes the
// objects an addon already applied, so rebuilding really is the only way to be rid of
// one. The API refuses the removal outright, so a plan that promised an in-place update
// here would fail at apply time with a 400 instead.
func TestAddonPlanModifier_RemovingAnAddonReplacesTheCluster(t *testing.T) {
	wantReplace(t, modifyAddons(t, addonSet("external-dns"), addonSet(), false), true,
		"dropping the only addon is a removal")
	wantReplace(t, modifyAddons(t, addonSet("external-dns", "external-secrets"), addonSet("external-dns"), false), true,
		"dropping one of several is still a removal")
}

func TestAddonPlanModifier_NoChangeDoesNotReplace(t *testing.T) {
	wantReplace(t, modifyAddons(t, addonSet("external-dns"), addonSet("external-dns"), false), false,
		"an identical set is neither an addition nor a removal")
	wantReplace(t, modifyAddons(t, addonSet(), addonSet(), false), false,
		"two empty sets are not a removal")
}

// 🔴 THE ARM THAT WOULD REPLACE MOST OF THE ESTATE. `addons` is Optional+Computed, so a
// configuration that simply does not mention it presents a NULL or UNKNOWN plan value.
// Reading either as "the empty set, therefore everything was removed" would plan a
// destroy-and-rebuild for every cluster whose config omits addons — which is most of
// them — on the first apply after this provider version.
func TestAddonPlanModifier_NullOrUnknownPlanIsNotARemoval(t *testing.T) {
	wantReplace(t, modifyAddons(t, addonSet("external-dns"), types.SetNull(types.StringType), false), false,
		"an unconfigured attribute is not an emptied one")
	wantReplace(t, modifyAddons(t, addonSet("external-dns"), types.SetUnknown(types.StringType), false), false,
		"an unresolved computed value is not an emptied one")
	wantReplace(t, modifyAddons(t, types.SetNull(types.StringType), addonSet("external-dns"), false), false,
		"a null PRIOR value has nothing to remove")
}

// A destroy plan must not be turned into a replacement.
func TestAddonPlanModifier_DestroyIsNotAReplacement(t *testing.T) {
	wantReplace(t, modifyAddons(t, addonSet("external-dns"), addonSet(), true), false,
		"a destroy plan must never be upgraded to a replacement")
}
