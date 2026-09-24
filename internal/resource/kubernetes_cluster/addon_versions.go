package kubernetes_cluster

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
)

// addonKeyRe is the API's addon key grammar (UpdateClusterAddonsRequest.versions
// propertyNames), enforced on addon_versions keys at plan time.
var addonKeyRe = regexp.MustCompile(`^[a-z0-9-]{1,64}$`)

// changedPins is the `versions` delta of PUT .../addons: every pin the PLAN declares
// whose value differs from the one recorded in prior state (absent counts as
// different). Nil when nothing changed, so `versions` is omitted from the body.
//
// 🔴 VALUES COME FROM THE PLAN ONLY — the practitioner's configuration. Never from a
// response's `pinnedVersions` (the platform's FULL map, never even decoded: see
// apiKubernetesCluster) and never from prior state, which is used only to decide
// WHETHER a configured key changed. Replaying either would re-assert a pin for a key
// this configuration does not touch and override somebody else's concurrent change.
// A key present in state but gone from the plan sends nothing: the API has no unpin,
// so dropping a key means "stop managing that pin".
func changedPins(state, plan types.Map) map[string]string {
	if plan.IsNull() || plan.IsUnknown() {
		return nil
	}
	prior := state.Elements()
	var out map[string]string
	for key, v := range plan.Elements() {
		pin, ok := v.(types.String)
		if !ok || pin.IsNull() || pin.IsUnknown() {
			continue
		}
		if old, ok := prior[key].(types.String); ok && old.Equal(pin) {
			continue
		}
		if out == nil {
			out = map[string]string{}
		}
		out[key] = pin.ValueString()
	}
	return out
}

// addedAddonPins adds to the delta the configured pin of every addon this apply ADDS (in
// the plan's selection, not in state's). changedPins alone misses a RE-ADDED addon: its
// pin is still in state after someone removed the addon out of band, so the plan's pin
// compares equal and nothing is sent — and the platform installs the recommended version.
// Values come from the plan only, and an added addon the configuration does not pin
// sends nothing.
func addedAddonPins(pins map[string]string, stateAddons, planAddons types.Set, planPins types.Map) map[string]string {
	if planPins.IsNull() || planPins.IsUnknown() || planAddons.IsNull() || planAddons.IsUnknown() {
		return pins
	}
	had := map[string]bool{}
	if !stateAddons.IsNull() && !stateAddons.IsUnknown() {
		for _, k := range setToStringSlice(stateAddons) {
			had[k] = true
		}
	}
	configured := planPins.Elements()
	for _, k := range setToStringSlice(planAddons) {
		if had[k] {
			continue
		}
		pin, ok := configured[k].(types.String)
		if !ok || pin.IsNull() || pin.IsUnknown() {
			continue
		}
		if pins == nil {
			pins = map[string]string{}
		}
		pins[k] = pin.ValueString()
	}
	return pins
}

// checkAddonVersions refuses pins the API would 400: a pin needs an explicitly
// CONFIGURED `addons` (the PUT must carry the full selection, and taking it from a
// refreshed read would re-add an addon somebody else removed), and every pinned key
// must be in that selection. Unknown values are skipped — the backend stays
// authoritative for what cannot be judged yet.
func checkAddonVersions(configAddons types.Set, pins types.Map) diag.Diagnostics {
	var diags diag.Diagnostics
	if pins.IsNull() || pins.IsUnknown() || len(pins.Elements()) == 0 || configAddons.IsUnknown() {
		return diags
	}
	if configAddons.IsNull() {
		diags.AddAttributeError(path.Root("addon_versions"), "addon_versions requires addons",
			"Pinning an addon version needs the addon selection set explicitly: add an addons attribute "+
				"that includes every key in addon_versions.")
		return diags
	}
	selected := map[string]bool{}
	for _, k := range setToStringSlice(configAddons) {
		selected[k] = true
	}
	for key := range pins.Elements() {
		if !selected[key] {
			diags.AddAttributeError(path.Root("addon_versions").AtMapKey(key), "Pinned addon is not selected",
				fmt.Sprintf("addon_versions pins %q, which is not in addons. Pin only addons you select.", key))
		}
	}
	return diags
}

// checkConfiguredAddonVersions runs checkAddonVersions against the CONFIGURED addons.
// The config is read only when pins are set, so a request without a config (destroy,
// tests of unrelated paths) is never touched.
func (r *kubernetesClusterResource) checkConfiguredAddonVersions(ctx context.Context, config tfsdk.Config, pins types.Map) diag.Diagnostics {
	if pins.IsNull() || pins.IsUnknown() || len(pins.Elements()) == 0 {
		return nil
	}
	var configAddons types.Set
	diags := config.GetAttribute(ctx, path.Root("addons"), &configAddons)
	if diags.HasError() {
		return diags
	}
	return checkAddonVersions(configAddons, pins)
}

// retryableAddonsPut decides what putAddons re-sends: only a 409 `invalid_state` — the
// cluster is not running yet, or, for a request that REMOVES an addon, the platform is
// still applying its selection or finishing an earlier removal. Both clear on their own.
// 409 `conflict` is never retried: it also covers "the platform's records of this cluster
// disagree", which retrying cannot clear, and only the prose tells the two apart. A 5xx or
// a transport failure fails fast: an update error taints nothing, and its message says
// re-applying is safe.
func retryableAddonsPut(err error) bool {
	var apiErr *client.APIError
	return errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusConflict && apiErr.Code == "invalid_state"
}

// putAddons sends PUT .../addons until it succeeds, fails for good, or the deadline
// passes. The deadline is the caller's ONE deadline for the whole operation, not a fresh
// budget for this request.
//
// 🔴 THE CALLER BUILDS THE BODY ONCE AND EVERY ATTEMPT RE-SENDS IT UNCHANGED. The cluster
// read between attempts exists only to stop at once on a cluster that went to `error`,
// `deleting` or `deleted` (a 404 counts as deleted, as in pollCluster); nothing it returns may reach the
// request. A read that FAILS (the platform unreachable) proves nothing, so it keeps retrying.
func (r *kubernetesClusterResource) putAddons(ctx context.Context, id string, body apiUpdateClusterAddonsRequest, deadline time.Time) (*client.Response, error) {
	for {
		apiResp, err := r.client.Put(ctx, r.clusterPath(id)+"/addons", body)
		if err == nil {
			return apiResp, nil
		}
		// The operation itself was cancelled: stop, whatever the error chain says.
		if ctx.Err() != nil {
			return nil, err
		}
		if !retryableAddonsPut(err) || !time.Now().Before(deadline) {
			return nil, err
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(r.getPollInterval()):
		}
		current, getErr := r.getCluster(ctx, id)
		if client.IsNotFound(getErr) {
			return nil, fmt.Errorf("the cluster no longer exists, so its addons cannot be changed: %w", err)
		}
		if getErr != nil {
			continue
		}
		switch current.Status {
		case statusDeleted, statusDeleting, statusError:
			return nil, fmt.Errorf("the cluster is %s, so its addons cannot be changed: %w", current.Status, err)
		}
	}
}

// addonsPutFailureDetail explains a failed PUT .../addons. A 500 or 503 may mean the
// selection WAS applied (a 503 can mean the selection changed while the version pin was
// not recorded, mid-deployment); re-sending the identical request is the platform's own
// remedy, and an update error taints nothing, so re-applying is safe.
func addonsPutFailureDetail(err error) string {
	detail := err.Error()
	if apiErr, ok := err.(*client.APIError); ok && (apiErr.StatusCode == http.StatusInternalServerError || apiErr.StatusCode == http.StatusServiceUnavailable) {
		detail += "\n\nThe addon selection may already have been applied even though this request failed " +
			"(for example the selection changed but a version pin was not recorded while a platform " +
			"deployment is in progress). Re-applying is safe: it re-sends the identical request, which " +
			"is idempotent."
	}
	return detail
}

// addNotices surfaces a successful PUT's `notices` as warnings — never errors, and an
// unknown code is shown by its message (open vocabulary). A 2xx whose body cannot be
// parsed is still a success: it warns that notices could not be read rather than failing
// a change the platform accepted.
func addNotices(diags *diag.Diagnostics, apiResp *client.Response) {
	updated, err := client.ParseResponse[apiKubernetesCluster](apiResp)
	if err != nil {
		diags.AddWarning("Addon change accepted; notices could not be read",
			"The platform accepted the addon change, but its response could not be read, so any notices "+
				"it carried are not shown: "+err.Error())
		return
	}
	for _, n := range updated.Notices {
		msg := n.Message
		if msg == "" {
			msg = n.Code
		}
		diags.AddWarning("Kubernetes cluster addon notice", msg)
	}
}

// apiAddonPin is the part of one GET .../addons entry a refresh reads.
type apiAddonPin struct {
	Key           string `json:"key"`
	PinnedVersion string `json:"pinnedVersion"`
}

type apiAddonPinList struct {
	Addons []apiAddonPin `json:"addons"`
}

// refreshPins reads back the pin of each addon that addon_versions ALREADY names, so a
// configured pin moved outside Terraform shows as drift and the next apply restores it.
//
// It never ADDS a key: an unconfigured pin must not enter state. It skips an empty
// pinnedVersion (nothing to pin; the recorded value stays). On any failure — 409 mid-create,
// 501 deployment, 503 outage, an unreadable body — it keeps the
// recorded values and warns; a refresh never fails on it. Callers skip it when no pin is
// recorded, so a cluster without pins costs no extra request. Request values still come only
// from the plan (changedPins): this changes what state compares against, never what is sent.
func (r *kubernetesClusterResource) refreshPins(ctx context.Context, id string, pins types.Map, diags *diag.Diagnostics) types.Map {
	apiResp, err := r.client.Get(ctx, r.clusterPath(id)+"/addons", nil)
	var list *apiAddonPinList
	if err == nil {
		list, err = client.ParseResponse[apiAddonPinList](apiResp)
	}
	if err != nil {
		diags.AddWarning("Could not refresh addon version pins",
			"Keeping the recorded addon_versions: the platform could not report this cluster's pins right now. "+
				"Drift in a pinned version is not detected until it can. "+err.Error())
		return pins
	}
	elems := pins.Elements()
	for _, a := range list.Addons {
		if _, configured := elems[a.Key]; configured && a.PinnedVersion != "" {
			elems[a.Key] = types.StringValue(a.PinnedVersion)
		}
	}
	refreshed, d := types.MapValue(types.StringType, elems)
	diags.Append(d...)
	if d.HasError() {
		return pins
	}
	return refreshed
}
