package kubernetes_cluster

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/timeouts"
)

// `version` is changed IN PLACE (K8S-VERSION-UPGRADES-PLAN decision 16): a higher version is a
// POST .../upgrade, a lower one is refused at plan, and a cluster upgraded outside Terraform never
// plans a replacement. The provider does no minor arithmetic beyond ordering: which version is a
// valid target is the server's call (availableUpgrades and its 400), surfaced as a diagnostic.

const statusUpgrading = "upgrading"

// upgradeTimeout is how long an apply waits for an upgrade when timeouts.update is unset, from
// the moment the upgrade starts. Not the update default (30m): every node is drained with up to
// 30 minutes of PodDisruptionBudget waiting before it is replaced, so an upgrade takes hours.
const upgradeTimeout = 6 * time.Hour

// versionConfiguredKey (private state) records whether the configuration sets `version`, written
// by Create and Update. Only then does a read keep the recorded version below what the cluster
// runs (keepRecordedVersion); with it unset, state follows the cluster. Absent (a state from
// before this key) counts as configured: the warning, not a silent change.
const versionConfiguredKey = "version_configured"

type privateState interface {
	GetKey(context.Context, string) ([]byte, diag.Diagnostics)
	SetKey(context.Context, string, []byte) diag.Diagnostics
}

// nilPrivate: a unit test's hand-built response carries none (tftags.RecordDefaults does the same).
func nilPrivate(priv privateState) bool {
	v := reflect.ValueOf(priv)
	return !v.IsValid() || (v.Kind() == reflect.Pointer && v.IsNil())
}

func recordVersionConfigured(ctx context.Context, config tfsdk.Config, priv privateState, diags *diag.Diagnostics) {
	if nilPrivate(priv) {
		return
	}
	var v types.String
	diags.Append(config.GetAttribute(ctx, path.Root("version"), &v)...)
	flag := []byte("0")
	if !v.IsNull() {
		flag = []byte("1")
	}
	diags.Append(priv.SetKey(ctx, versionConfiguredKey, flag)...)
}

func versionConfigured(ctx context.Context, priv privateState, diags *diag.Diagnostics) bool {
	if nilPrivate(priv) {
		return true
	}
	b, d := priv.GetKey(ctx, versionConfiguredKey)
	diags.Append(d...)
	return string(b) != "0"
}

// upgradeBudget is timeouts.update when set, else upgradeTimeout.
func (r *kubernetesClusterResource) upgradeBudget(m *timeouts.Model) time.Duration {
	b, err := m.Resolve(timeouts.Uniform(upgradeTimeout))
	if err != nil {
		return upgradeTimeout
	}
	return b.Update
}

// upgradeEffects is what an upgrade does to the cluster, shared by the plan warning and the docs.
const upgradeEffects = "The control plane is upgraded first, then each node pool: every node is drained " +
	"(PodDisruptionBudgets are honoured for up to 30 minutes, then the remaining pods are deleted) and " +
	"replaced. On a single control-plane node the Kubernetes API is unavailable while the control plane " +
	"is replaced. Node pool changes are refused until the upgrade finishes. Kubernetes cannot be downgraded."

// compareMinor orders two "MAJOR.MINOR" versions numerically (1.9 < 1.10). ok is false when either
// is not in that form, and the caller then leaves the judgement to the server.
func compareMinor(a, b string) (int, bool) {
	pa, okA := parseMinor(a)
	pb, okB := parseMinor(b)
	if !okA || !okB {
		return 0, false
	}
	for i := range pa {
		if pa[i] != pb[i] {
			if pa[i] < pb[i] {
				return -1, true
			}
			return 1, true
		}
	}
	return 0, true
}

func parseMinor(v string) ([2]int, bool) {
	major, minor, ok := strings.Cut(v, ".")
	maj, err1 := strconv.Atoi(major)
	mnr, err2 := strconv.Atoi(minor)
	if !ok || err1 != nil || err2 != nil || maj < 0 || mnr < 0 {
		return [2]int{}, false
	}
	return [2]int{maj, mnr}, true
}

// planVersionChange is ModifyPlan's half: a lower configured version is warned about (and refused
// by Update, never a replacement), a higher one warns that this apply upgrades the cluster in place.
func planVersionChange(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse, plan KubernetesClusterModel) {
	diags := &resp.Diagnostics
	// A replacement (another attribute changed) creates a new cluster on the configured version:
	// neither an upgrade nor a refusal.
	if req.State.Raw.IsNull() || plan.Version.IsNull() || plan.Version.IsUnknown() || len(resp.RequiresReplace) > 0 {
		return
	}
	var prior types.String
	diags.Append(req.State.GetAttribute(ctx, path.Root("version"), &prior)...)
	if prior.IsNull() || prior.IsUnknown() {
		return
	}
	want, have := plan.Version.ValueString(), prior.ValueString()
	switch cmp, ok := compareMinor(want, have); {
	case !ok || cmp == 0:
	case cmp < 0:
		// A WARNING, not an error: ModifyPlan also runs in a destroy's plan, and an error here would
		// block destroying the cluster (TestNoErrorDiagnosticsWhilePlanning). Update refuses it
		// before changing anything (refuseDowngrade).
		diags.AddAttributeWarning(path.Root("version"), "Kubernetes Version Cannot Be Lowered",
			downgradeDetail(want, have)+" An apply of this plan is refused before it changes anything.")
	default:
		diags.AddAttributeWarning(path.Root("version"), "This Apply Upgrades The Cluster In Place",
			fmt.Sprintf("version %s -> %s upgrades the cluster without replacing it. %s The upgrade can take hours: "+
				"timeouts.update (default 6h for an upgrade) bounds how long this apply waits, not the upgrade itself.", have, want, upgradeEffects))
	}
}

func downgradeDetail(want, have string) string {
	return fmt.Sprintf("The cluster is on Kubernetes %s and the configuration says %s. Kubernetes cannot be downgraded, "+
		"and this resource never replaces a cluster to change its version. Set version = %q.",
		have, want, have)
}

// refuseDowngrade is Update's first check: a version below the recorded one, or below what the
// cluster runs (state can record less, after an upgrade outside Terraform: keepRecordedVersion),
// fails the apply before ANY change (name, tags, scale, add-ons) is made. The live read happens
// only when the version changes.
func (r *kubernetesClusterResource) refuseDowngrade(ctx context.Context, plan, state KubernetesClusterModel, diags *diag.Diagnostics) bool {
	if plan.Version.IsNull() || plan.Version.IsUnknown() || state.Version.IsNull() || state.Version.IsUnknown() ||
		plan.Version.Equal(state.Version) {
		return false
	}
	want, have := plan.Version.ValueString(), state.Version.ValueString()
	if cmp, ok := compareMinor(want, have); !ok || cmp > 0 {
		c, err := r.getCluster(ctx, state.ID.ValueString())
		if err != nil {
			diags.AddError("Failed to read Kubernetes cluster before upgrading it", err.Error())
			return true
		}
		have = c.KubernetesVersion
	}
	if cmp, ok := compareMinor(want, have); ok && cmp < 0 {
		diags.AddAttributeError(path.Root("version"), "Kubernetes Version Cannot Be Lowered",
			downgradeDetail(want, have)+" Nothing was changed.")
		return true
	}
	return false
}

// keepRecordedVersion stops a read from raising `version` above the value Terraform recorded:
// a cluster upgraded outside Terraform (or by an apply that timed out) would otherwise show a
// diff back DOWN to the configuration, which nothing can apply. It warns instead, and the next
// apply with the higher version in the configuration records it without touching the cluster.
func keepRecordedVersion(m *KubernetesClusterModel, recorded types.String, diags *diag.Diagnostics) {
	if recorded.IsNull() || recorded.IsUnknown() || m.Version.IsNull() {
		return
	}
	running := m.Version.ValueString()
	if cmp, ok := compareMinor(running, recorded.ValueString()); !ok || cmp <= 0 {
		return
	}
	m.Version = recorded
	diags.AddAttributeWarning(path.Root("version"), "Cluster Upgraded Outside Terraform",
		fmt.Sprintf("The cluster runs Kubernetes %s, but Terraform records %s (it was upgraded outside Terraform, or by "+
			"an apply that timed out). Terraform will neither downgrade nor replace it. Set version = %q in the "+
			"configuration; the next apply then records it without changing the cluster.",
			running, recorded.ValueString(), running))
}

type apiUpgradeRequest struct {
	KubernetesVersion string `json:"kubernetesVersion"`
}

type apiUpgradeAccepted struct {
	OperationID string `json:"operationId"`
}

// upgradeCluster takes the cluster to `target` and waits until it gets there or stops. A cluster
// already upgrading to `target` with no failure is waited on, not re-requested (an earlier apply
// timed out); one that stopped is resumed by the same request (the API treats it as a resume).
// The returned error is the diagnostic detail; nil means the cluster is on `target`.
//
// THE CLUSTER, NOT THE OPERATION, ENDS THE WAIT. Every way an upgrade ends is recorded on the
// cluster row (committed: running on the target; stopped: an upgradeFailure; a rejected or expired
// command: restored), while the operation can fail with no text, or not exist at all for a command
// refused before any workflow ran. The operation is read once, afterwards, for its error text.
func (r *kubernetesClusterResource) upgradeCluster(ctx context.Context, id, target string, deadline time.Time) error {
	c, err := r.getCluster(ctx, id)
	if err != nil {
		return fmt.Errorf("could not read the cluster before upgrading it: %w", err)
	}
	cmp, ok := compareMinor(target, c.KubernetesVersion)
	if ok && cmp < 0 {
		return errors.New(downgradeDetail(target, c.KubernetesVersion) + " The version was not changed.")
	}
	if ok && cmp == 0 {
		return nil // upgraded outside Terraform: record it, nothing to do
	}
	if c.Status == statusUpgrading && c.UpgradeFailure != "" && c.TargetKubernetesVersion != "" && c.TargetKubernetesVersion != target {
		ended := "stopped"
		if c.UpgradeFailure == upgradeFailureWindowEnded {
			ended = "paused until the next maintenance window"
		}
		return fmt.Errorf("an upgrade of this cluster to %s %s (upgradeFailure: %s), and only that version can resume it. "+
			"Set version = %q and apply again. Nothing was started", c.TargetKubernetesVersion, ended, c.UpgradeFailure, c.TargetKubernetesVersion)
	}

	opID := ""
	if attach := c.Status == statusUpgrading && c.TargetKubernetesVersion == target && c.UpgradeFailure == ""; !attach {
		resp, err := r.client.Post(ctx, r.clusterPath(id)+"/upgrade", apiUpgradeRequest{KubernetesVersion: target})
		if err != nil {
			return upgradeRefusal(err, c.KubernetesVersion, target)
		}
		// Unreadable 202: the wait below needs nothing from it.
		if accepted, err := client.ParseResponse[apiUpgradeAccepted](resp); err == nil {
			opID = accepted.OperationID
		}
	}

	var last *apiKubernetesCluster
	_, err = r.client.WaitForStateWoken(ctx, client.PollConfig{
		Timeout:      time.Until(deadline),
		TargetStates: []string{"settled"},
		ResourceName: "kubernetes_cluster upgrade",
		PollFunc: func(pollCtx context.Context) (string, error) {
			cur, err := r.getCluster(pollCtx, id)
			if err != nil {
				return "", err
			}
			last = cur
			if cur.Status == statusUpgrading && cur.UpgradeFailure == "" {
				return statusUpgrading, nil
			}
			return "settled", nil
		},
	}, r.getPollInterval())
	if err != nil {
		return fmt.Errorf("%w%s", upgradeStillRunning(target, err), r.operationDetail(ctx, opID))
	}
	if done, ok := compareMinor(last.KubernetesVersion, target); ok && done == 0 && last.UpgradeFailure == "" {
		return nil
	}
	return fmt.Errorf("%w%s", upgradeStopped(last, target), r.operationDetail(ctx, opID))
}

// operationDetail is the operation's own failure text, best effort: "" when there is no
// operation, it cannot be read, or it did not fail.
func (r *kubernetesClusterResource) operationDetail(ctx context.Context, opID string) string {
	if opID == "" {
		return ""
	}
	op, err := r.client.GetOperation(ctx, opID)
	if err != nil || op.Error == "" || (op.Status != client.OperationStatusFailed && op.Status != client.OperationStatusCancelled) {
		return ""
	}
	return fmt.Sprintf(" The upgrade operation %s %s: %s", opID, op.Status, op.Error)
}

func upgradeStillRunning(target string, err error) error {
	return fmt.Errorf("the upgrade to %s is still running on the platform and continues without Terraform (%v). "+
		"Run terraform apply again to wait for it; raise timeouts.update to wait longer in one apply", target, err)
}

// upgradeStopped explains an upgrade that ended without reaching the target. State keeps the
// version the API reports, so re-applying the same configuration resumes (or restarts) it.
func upgradeStopped(c *apiKubernetesCluster, target string) error {
	reason := c.UpgradeFailure
	if reason == "" {
		reason = "unknown"
	}
	var next string
	switch reason {
	case upgradeFailureWindowEnded:
		next = windowEndedLine(c) + " Apply again with the same version to do that."
	case "commit_not_converged", "upgrade_path_unavailable", "cluster_record_incomplete":
		next = "This needs Frostmoln support; contact them before applying again."
	case codeAddonNotVerified:
		next = "A selected add-on's pinned version is not verified on " + target + ": move the pin (addon_versions) first, then apply again."
	case "platform_images_not_converged", "rollout_in_progress":
		next = "This clears on its own: apply again later."
	default:
		if c.Status == statusUpgrading {
			next = "The cluster keeps serving. Apply again with the same version to resume the upgrade."
		} else {
			next = "Nothing runs " + target + " yet. Apply again to start a new upgrade."
		}
	}
	if reason == "cp_member_missing" && c.Status != statusUpgrading {
		next += " The control plane runs short of a member until Frostmoln repairs it."
	}
	ended := "stopped"
	if reason == upgradeFailureWindowEnded {
		ended = "paused" // a pause, never worded as a failure (decision 5)
	}
	return fmt.Errorf("the upgrade to %s %s (upgradeFailure: %s); the cluster is %s on %s. %s",
		target, ended, reason, c.Status, c.KubernetesVersion, next)
}

// upgradeFailureWindowEnded is a pause, not a failure (K8S-VERSION-UPGRADES-PLAN §3.1g decision 5):
// a platform (EOL-forced) upgrade did not fit in the cluster's maintenance window and continues, or
// starts, in the next one.
const upgradeFailureWindowEnded = "window_ended"

// windowEndedLine is decision 5's copy, by status. The target is the upgrade's own on an
// `upgrading` cluster and the offered next minor on a `running` one; never empty or invented.
func windowEndedLine(c *apiKubernetesCluster) string {
	target := windowEndedTarget(c)
	if target == "" {
		target = "the next Kubernetes version"
	}
	if c.Status == statusUpgrading {
		return "The upgrade to " + target + " continues in your next maintenance window. You can resume it now instead."
	}
	return "The upgrade to " + target + " did not fit in this maintenance window. It starts in your next window. " +
		"You can start it now instead."
}

// windowEndedTarget is the paused upgrade's version, or "" when none is known.
func windowEndedTarget(c *apiKubernetesCluster) string {
	if c.Status == statusUpgrading {
		return c.TargetKubernetesVersion
	}
	if len(c.AvailableUpgrades) > 0 {
		return c.AvailableUpgrades[0]
	}
	return ""
}

// warnWindowEnded is the refresh's notice of a paused platform upgrade. A WARNING, never an error:
// Terraform did not start that run, and a refresh, plan or unrelated apply must not fail over it.
// Raising `version` to the target in the configuration resumes (or starts) it at once; with no
// known target the hint is left out, so it never advises a request the server would refuse.
func warnWindowEnded(c *apiKubernetesCluster, diags *diag.Diagnostics) {
	if c.UpgradeFailure != upgradeFailureWindowEnded {
		return
	}
	detail := windowEndedLine(c)
	if target := windowEndedTarget(c); target != "" {
		detail += fmt.Sprintf(" To do that with Terraform, set version = %q and apply.", target)
	}
	diags.AddAttributeWarning(path.Root("version"), "Kubernetes Upgrade Paused Until The Next Maintenance Window", detail)
}

// upgradeHints adds what to do to each refusal the upgrade route can give; the server's own
// message is always shown too.
var upgradeHints = map[string]string{
	"not_running":             "Only a running cluster can be upgraded.",
	"upgrade_in_progress":     "Another upgrade of this cluster is running. Apply again once it has finished.",
	"running_version_unknown": "The platform does not know yet which version the cluster runs. Apply again after its next maintenance update.",
	"pool_behind":             "A node pool runs an older minor than the control plane: apply its pending update first.",
	"pool_not_active":         "A node pool is failed or busy: repair or delete it, then apply again.",
	"rollout_in_progress":     "A platform update of the cluster is running. Apply again once it has finished.",
	"target_halted":           "Upgrades to this version are on hold on the platform side. Apply again later.",
	"too_many_pools":          "An upgrade takes at most 100 node pools.",
	"invalid_state":           "The cluster changed while the request was judged. Apply again.",
}

func upgradeRefusal(err error, have, target string) error {
	var apiErr *client.APIError
	if !errors.As(err, &apiErr) {
		return fmt.Errorf("the upgrade request failed: %w. It may or may not have started; applying again is safe "+
			"(an upgrade already running to this version is waited on, not requested twice)", err)
	}
	keep := fmt.Sprintf("Nothing was started and the cluster stays on %s.", have)
	switch apiErr.StatusCode {
	case http.StatusNotImplemented:
		return fmt.Errorf("upgrades are not available to this tenant yet (%s). %s Keep version = %q until they are",
			apiErr.Message, keep, have)
	case http.StatusBadRequest:
		if apiErr.Code == codeAddonNotVerified {
			return addonsNotVerified(apiErr, target, keep)
		}
		return fmt.Errorf("%s is not an upgrade target for this cluster (%s). A cluster moves one minor version at a "+
			"time, to a version the platform offers for upgrades. %s", target, apiErr.Message, keep)
	case http.StatusConflict:
		return fmt.Errorf("%s %s This request started nothing", apiErr.Error(), upgradeHints[apiErr.Code])
	case http.StatusServiceUnavailable:
		return fmt.Errorf("%s. This is temporary: apply again. %s", apiErr.Error(), keep)
	case http.StatusForbidden:
		if upgradeDenyCodes[apiErr.Code] {
			named := "" // not apiErr.Error() below: it echoes the demand again, 2-part shapes included
			if client.IsOperationDemand(apiErr.RequiredPermission) {
				named = " (required permission: " + apiErr.RequiredPermission + ")"
			}
			return fmt.Errorf("the version change to %s was not applied: this credential may not upgrade the cluster%s. "+
				"%s %s\n\n%s: %s", target, named, upgradeAccess, keep, apiErr.Code, apiErr.Message)
		}
	}
	// Every other status and code, a gate's 403 (a suspended account) included: the server's own message.
	return fmt.Errorf("%s. %s", apiErr.Error(), keep)
}

// codeAddonNotVerified is the upgrade route's 400 for a selected add-on pinned to a version not
// verified on the target (a fresh start only). It is not a bad target, so it never gets that text.
const codeAddonNotVerified = "addon_not_verified"

// addonsNotVerifiedError is that 400's diagnostic; Update shows Summary as the diagnostic's summary.
type addonsNotVerifiedError struct {
	Summary string
	detail  string
}

func (e *addonsNotVerifiedError) Error() string { return e.detail }

type apiUnverifiedAddon struct {
	Key           string `json:"key"`
	Version       string `json:"version"`
	MoveToVersion string `json:"moveToVersion"`
}

// unverifiedAddons reads details.addons in the platform's order (never sorted). Only an entry with
// no non-empty string key is skipped; a missing version or moveToVersion still lists the add-on.
// Versions are opaque: shown, never compared. Every server string goes through client.Printable.
func unverifiedAddons(details map[string]any) []apiUnverifiedAddon {
	raw, ok := details["addons"].([]any)
	if !ok {
		return nil
	}
	out := []apiUnverifiedAddon{}
	for _, e := range raw {
		m, ok := e.(map[string]any)
		if !ok {
			continue
		}
		str := func(k string) string { v, _ := m[k].(string); return client.Printable(v) }
		a := apiUnverifiedAddon{Key: str("key"), Version: str("version"), MoveToVersion: str("moveToVersion")}
		if a.Key == "" {
			continue
		}
		out = append(out, a)
	}
	return out
}

// addonsNotVerified names each add-on and where its pin can move. With no readable addons the
// platform could not judge them against the target, which needs support (the API contract).
func addonsNotVerified(apiErr *client.APIError, target, keep string) error {
	e := &addonsNotVerifiedError{Summary: "Add-ons Not Verified On Kubernetes " + target}
	var b strings.Builder
	b.WriteString(client.Printable(apiErr.Message))
	addons := unverifiedAddons(apiErr.Details)
	if len(addons) == 0 {
		fmt.Fprintf(&b, "\n\nThe platform could not check the cluster's add-ons against %s: contact Frostmoln support "+
			"before applying again. %s", target, keep)
		e.detail = b.String()
		return e
	}
	fmt.Fprintf(&b, "\n\nThese add-ons are pinned to a version not verified on %s:", target)
	movable, stuck := false, false
	for _, a := range addons {
		fmt.Fprintf(&b, "\n  - %s", a.Key)
		if a.Version != "" {
			fmt.Fprintf(&b, " (pinned %s)", a.Version)
		}
		if a.MoveToVersion != "" {
			fmt.Fprintf(&b, ": move to %s", a.MoveToVersion)
			movable = true
		} else {
			stuck = true
			b.WriteString(": no verified version to move to; contact Frostmoln support")
		}
	}
	b.WriteString("\n\n")
	switch {
	case movable && stuck:
		b.WriteString("Set each movable add-on's pin in addon_versions (the add-on must also be listed in addons) to " +
			"the version named, and contact Frostmoln support about the others before applying again. ")
	case movable:
		b.WriteString("Set each add-on's pin in addon_versions (the add-on must also be listed in addons) to the " +
			"version named, then apply again. ")
	}
	b.WriteString(keep)
	e.detail = b.String()
	return e
}

// upgradeAccess says who may upgrade, in the attribute text and the 403 diagnostic alike. It names
// no role as the only way: a group document or custom role grants the operations too. Provisioning
// refuses an access-policy API key or workload identity on every command path today (servicekit
// provcmd), so the usual "add it to the key's policy" advice would mislead.
const upgradeAccess = "Upgrading needs kubernetes:kubernetes-clusters:upgrade and kubernetes:kubernetes-clusters:create " +
	"on the cluster: the Owner and Admin roles hold them, and a group document or custom role that names them grants " +
	"them too. An access-policy API key cannot start or resume an upgrade today: run that apply signed in with " +
	"`fm auth login`, with no API key in the provider's api_key, FROSTMOLN_API_KEY or the fm CLI configuration. " +
	"Never copy an fm sign-in into CI or other automation."

// upgradeDenyCodes are the IAM and scope denials, plus kubernetes' refusal of a policy-granted
// principal (command_authorization.go), which is what an access-policy API key whose policy does
// grant the operations gets. Not "forbidden": on this route kubernetes sends it for a credential
// that cannot manage the enclave (requireEnclaveOrgClaim), whose own message says what to do. A
// gate's 403 (a suspended account) carries another code too.
var upgradeDenyCodes = map[string]bool{
	"insufficient_scope": true, "INSUFFICIENT_SCOPE": true,
	"insufficient_permission": true, "INSUFFICIENT_PERMISSIONS": true,
	"policy_granted_not_supported": true,
}
