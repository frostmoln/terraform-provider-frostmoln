package kubernetes_cluster

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// requiresReplaceOnAddonRemoval forces cluster replacement when the plan DROPS an addon
// the cluster currently has, and allows an in-place update when it only ADDS.
//
// 🔴 THE ASYMMETRY IS THE API'S, NOT A PREFERENCE. `PUT .../kubernetes-clusters/{id}/addons`
// is ADD-ONLY: the platform applies a new addon to a running cluster, but nothing in it
// DELETES the objects an addon has already installed — the in-guest applier only creates
// and updates, and the seed bundle has no deletion primitive at all. So for a removal,
// destroying and rebuilding the cluster genuinely is the only way to be rid of it, and
// that is what the practitioner must be shown in the plan.
//
// 🔴 IT REPLACES A PLAIN RequiresReplace() OVER BOTH DIRECTIONS, WHICH WAS DESTRUCTIVE.
// Until the addons endpoint existed the whole attribute was create-time-only, so
// RequiresReplace() was correct. Left in place it would now destroy and rebuild a
// customer's entire Kubernetes cluster — every workload on it — to perform an operation
// the API does in place, in the background, without an outage. That is the single
// highest-consequence thing in this change, and it is why the modifier is a named type
// with its own test rather than an inline closure.
//
// 🔴 A NULL OR UNKNOWN PLAN IS NEVER A REMOVAL. `addons` is Optional+Computed, so a
// config that omits it plans NULL on the first refresh after this provider version and
// UNKNOWN before the value is resolved. Reading either as "the empty set, therefore
// everything was removed" would replace every cluster whose config does not mention
// addons — which is most of them. Both are left to UseStateForUnknown, which runs
// alongside this modifier and carries the prior value forward.
func requiresReplaceOnAddonRemoval() planmodifier.Set {
	return addonRemovalReplacer{}
}

type addonRemovalReplacer struct{}

func (addonRemovalReplacer) Description(context.Context) string {
	return "Removing an addon replaces the cluster; adding one is applied in place."
}

func (m addonRemovalReplacer) MarkdownDescription(ctx context.Context) string {
	return m.Description(ctx)
}

func (addonRemovalReplacer) PlanModifySet(
	ctx context.Context, req planmodifier.SetRequest, resp *planmodifier.SetResponse,
) {
	// No prior state (create) or the resource is being destroyed: nothing to compare
	// against, and neither is a removal.
	if req.State.Raw.IsNull() || req.Plan.Raw.IsNull() {
		return
	}
	// See the doc: null/unknown is "not configured", never "emptied".
	if req.PlanValue.IsNull() || req.PlanValue.IsUnknown() {
		return
	}
	if req.StateValue.IsNull() || req.StateValue.IsUnknown() {
		return
	}

	planned := make(map[string]struct{}, len(req.PlanValue.Elements()))
	for _, e := range req.PlanValue.Elements() {
		if s, ok := e.(types.String); ok {
			planned[s.ValueString()] = struct{}{}
		}
	}
	for _, e := range req.StateValue.Elements() {
		s, ok := e.(types.String)
		if !ok {
			continue
		}
		if _, kept := planned[s.ValueString()]; !kept {
			resp.RequiresReplace = true
			return
		}
	}
}
