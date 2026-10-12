package kubernetes_cluster

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/tftags"
)

// The cluster-delete volume choice (K8S-PVC-VOLUME-LIFECYCLE-PLAN §2.3, ADR-0031
// amendment 2026-10-11). Volumes whose PV reclaimPolicy is Delete (freshly reported)
// are deleted with the cluster. Retain and Unknown volumes are ASKED about: the delete
// must say keep or delete (retainedVolumes), and `delete` must echo the preflight's
// retainedSetToken, so a volume that turned Retain after the preflight is never
// deleted unseen. The server answers an undecided delete 409
// retained_volumes_decision_required, and this resource refuses before sending one.

const (
	retainedVolumesKeep   = "keep"
	retainedVolumesDelete = "delete"

	volumeDispositionDelete = "delete"

	codeRetainedVolumesDecisionRequired = "retained_volumes_decision_required"
	codeDeleteInProgress                = "delete_in_progress"
	codeTooManyClusterVolumes           = "too_many_cluster_volumes"
)

// apiClusterVolume / apiClusterVolumePlan are GET .../kubernetes-clusters/{id}/persistent-volumes.
type apiClusterVolume struct {
	VolumeID      string `json:"volumeId"`
	Name          string `json:"name"`
	SizeGB        int    `json:"sizeGb"`
	PVCNamespace  string `json:"pvcNamespace"`
	PVCName       string `json:"pvcName"`
	ReclaimPolicy string `json:"reclaimPolicy"`
	Disposition   string `json:"disposition"`
}

type apiClusterVolumePlan struct {
	Volumes          []apiClusterVolume `json:"volumes"`
	RetainedSetToken string             `json:"retainedSetToken"`
	ReportedAt       *string            `json:"reportedAt"`
}

// asked returns the volumes the delete must decide about. Anything not explicitly
// "delete" is asked about: an unknown future disposition never reads as permission.
func (p *apiClusterVolumePlan) asked() []apiClusterVolume {
	var out []apiClusterVolume
	for _, v := range p.Volumes {
		if v.Disposition != volumeDispositionDelete {
			out = append(out, v)
		}
	}
	return out
}

func (r *kubernetesClusterResource) listClusterVolumes(ctx context.Context, id string) (*apiClusterVolumePlan, error) {
	resp, err := r.client.Get(ctx, r.clusterPath(id)+"/persistent-volumes", nil)
	if err != nil {
		return nil, err
	}
	var plan apiClusterVolumePlan
	if err := json.Unmarshal(resp.Body, &plan); err != nil {
		return nil, fmt.Errorf("parse the cluster's volumes: %w", err)
	}
	return &plan, nil
}

// describeVolumes renders volumes for a diagnostic, one per line.
func describeVolumes(vols []apiClusterVolume) string {
	var b strings.Builder
	for _, v := range vols {
		fmt.Fprintf(&b, "\n  - %s", v.VolumeID)
		if v.Name != "" {
			fmt.Fprintf(&b, " (%s)", v.Name)
		}
		fmt.Fprintf(&b, ", %d GB, reclaimPolicy %s", v.SizeGB, v.ReclaimPolicy)
		if v.PVCName != "" {
			fmt.Fprintf(&b, ", PVC %s/%s", v.PVCNamespace, v.PVCName)
		}
	}
	return b.String()
}

const retainedVolumesRemedy = "\n\nSet retained_volumes_on_destroy = \"keep\" (they stay as ordinary volumes, " +
	"still billed) or \"delete\" (they are deleted with the cluster) on this resource, run `terraform apply` " +
	"so the choice is recorded in state, then destroy again. Nothing was deleted."

// deleteWithVolumeChoice requests the cluster's delete and reports whether the caller
// must then poll for `deleted`. false means stop: the cluster is already gone (no
// diagnostic) or the delete was refused (a diagnostic was added).
//
//   - A cluster already `deleting` is a REPEAT delete: no preflight and no DELETE —
//     the first delete's volume choice stands — just the poll (true).
//   - Otherwise the volume preflight decides the parameters: none when nothing is
//     asked about, else retainedVolumes from retained_volumes_on_destroy (and the
//     retainedSetToken for delete). Unset while volumes are asked about = refused
//     here, before any DELETE.
func (r *kubernetesClusterResource) deleteWithVolumeChoice(ctx context.Context, id string, choiceAttr types.String, diags *diag.Diagnostics) bool {
	cluster, err := r.getCluster(ctx, id)
	switch {
	case client.IsNotFound(err):
		return false // already gone: nothing to delete, nothing to wait for
	case err != nil:
		diags.AddError("Failed to read Kubernetes cluster before deleting it", err.Error())
		return false
	case cluster.Status == statusDeleted:
		return false
	case cluster.Status == statusDeleting:
		return true
	}

	plan, err := r.listClusterVolumes(ctx, id)
	if err != nil {
		// The cluster exists (read above), so even a 404 here is a failure — a
		// platform that does not serve the volume check yet — never "gone".
		diags.AddError("Failed to read the Kubernetes cluster's volumes",
			"The cluster was not deleted: the volume check that decides what happens to its "+
				"PersistentVolume-backed volumes failed. Retry the destroy. "+err.Error())
		return false
	}
	asked := plan.asked()
	choice := ""
	if !choiceAttr.IsNull() && !choiceAttr.IsUnknown() {
		choice = choiceAttr.ValueString()
	}

	var query url.Values
	if len(asked) > 0 {
		if choice != retainedVolumesKeep && choice != retainedVolumesDelete {
			diags.AddError("Kubernetes cluster has retained volumes: retained_volumes_on_destroy is not set",
				fmt.Sprintf("Cluster %s has %d volume(s) whose PersistentVolume reclaimPolicy is Retain or Unknown "+
					"(Unknown: not reported by the cluster in the last 15 minutes; treated as Retain):%s%s",
					id, len(asked), describeVolumes(asked), retainedVolumesRemedy))
			return false
		}
		query = url.Values{"retainedVolumes": {choice}}
		if choice == retainedVolumesDelete {
			query.Set("retainedSetToken", plan.RetainedSetToken)
		}
	}

	if _, err := r.client.DeleteWithQuery(ctx, r.clusterPath(id), query); err != nil {
		if client.IsNotFound(err) {
			return false
		}
		if fresh, ok := retainedVolumesDecision(err); ok {
			diags.AddError("Kubernetes cluster's retained volumes changed",
				fmt.Sprintf("The platform refused the delete of cluster %s: its volumes with reclaimPolicy Retain "+
					"or Unknown are now:%s\n\nReview them, then destroy again. Nothing was deleted.",
					id, describeVolumes(fresh.asked())))
			return false
		}
		var apiErr *client.APIError
		if errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusConflict {
			switch apiErr.Code {
			case codeDeleteInProgress:
				// A delete started between the read above and this request; its choice stands.
				return true
			case codeTooManyClusterVolumes:
				diags.AddError("Too many Kubernetes cluster volumes to delete in one request",
					fmt.Sprintf("Cluster %s would delete %v volumes; the limit is %v. Nothing was deleted. Delete some "+
						"PVCs in the cluster first, or set retained_volumes_on_destroy = \"keep\" and apply.",
						id, apiErr.Details["count"], apiErr.Details["limit"]))
				return false
			}
		}
		diags.AddError("Failed to delete Kubernetes cluster", err.Error())
		return false
	}
	return true
}

// warnReplaceWithoutRetainedChoice warns when this plan REPLACES the cluster while
// retained_volumes_on_destroy is unset in state: the destroy half reads the setting
// from state, so it would fail if the cluster has Retain/Unknown volumes. The
// attributes compared are exactly those carrying RequiresReplace (the framework does
// not hand a resource's ModifyPlan the attribute modifiers' replace paths).
func warnReplaceWithoutRetainedChoice(ctx context.Context, req resource.ModifyPlanRequest, plan KubernetesClusterModel, diags *diag.Diagnostics) {
	if req.State.Raw.IsNull() {
		return
	}
	var state KubernetesClusterModel
	if d := req.State.Get(ctx, &state); d.HasError() {
		return
	}
	if !state.RetainedVolumesOnDestroy.IsNull() || !plannedReplacement(plan, state) {
		return
	}
	diags.AddAttributeWarning(path.Root("retained_volumes_on_destroy"),
		"Replacing this cluster: set retained_volumes_on_destroy first",
		"This plan replaces the Kubernetes cluster, and retained_volumes_on_destroy is not set in state. If the "+
			"cluster has volumes with PersistentVolume reclaimPolicy Retain or Unknown (not reported in the last 15 "+
			"minutes), the destroy half of the replacement fails and lists them. Set retained_volumes_on_destroy "+
			"(\"keep\" or \"delete\") alone and apply it before the change that replaces the cluster.")
}

// plannedReplacement reports whether any RequiresReplace attribute changes. An
// unknown planned value counts: RequiresReplace replaces on it too.
func plannedReplacement(plan, state KubernetesClusterModel) bool {
	changed := func(p, s types.String) bool { return p.IsUnknown() || !p.Equal(s) }
	if changed(plan.Version, state.Version) || changed(plan.ControlPlaneTier, state.ControlPlaneTier) ||
		changed(plan.Region, state.Region) || changed(plan.VPCID, state.VPCID) ||
		changed(plan.SubnetID, state.SubnetID) || changed(plan.PublicIPID, state.PublicIPID) {
		return true
	}
	if plan.InitialNodePool == nil || state.InitialNodePool == nil {
		return plan.InitialNodePool != state.InitialNodePool
	}
	return changed(plan.InitialNodePool.Name, state.InitialNodePool.Name) ||
		changed(plan.InitialNodePool.FlavorID, state.InitialNodePool.FlavorID)
}

// retainedVolumesDecision extracts the fresh plan from a 409
// retained_volumes_decision_required.
func retainedVolumesDecision(err error) (*apiClusterVolumePlan, bool) {
	var apiErr *client.APIError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusConflict ||
		apiErr.Code != codeRetainedVolumesDecisionRequired {
		return nil, false
	}
	plan := &apiClusterVolumePlan{}
	if apiErr.Details != nil {
		if raw, mErr := json.Marshal(apiErr.Details); mErr == nil {
			_ = json.Unmarshal(raw, plan)
		}
	}
	return plan, true
}

// onlyDestroyChoiceChanged reports whether this update changes retained_volumes_on_destroy
// and NOTHING the Update would act on (name, tags, addons, pins, node count). It makes
// no API call: the tag judgement is the state-only first pass tagsToWrite also starts
// with.
func (r *kubernetesClusterResource) onlyDestroyChoiceChanged(ctx context.Context, plan, state KubernetesClusterModel,
	private tftags.PrivateReader, diags *diag.Diagnostics) bool {
	if plan.RetainedVolumesOnDestroy.Equal(state.RetainedVolumesOnDestroy) {
		return false
	}
	if !plan.Name.Equal(state.Name) {
		return false
	}
	if !plan.Addons.IsUnknown() && !plan.Addons.IsNull() && !plan.Addons.Equal(state.Addons) {
		return false
	}
	if addedAddonPins(changedPins(state.AddonVersions, plan.AddonVersions), state.Addons, plan.Addons, plan.AddonVersions) != nil {
		return false
	}
	if plan.InitialNodePool == nil || state.InitialNodePool == nil ||
		!plan.InitialNodePool.NodeCount.Equal(state.InitialNodePool.NodeCount) {
		return false
	}
	prior := tftags.PriorOf(ctx, state.Tags, state.TagsAll, private, diags)
	if _, changed := tftags.ForUpdate(ctx, r.client.DefaultTags(), plan.Tags, prior, diags); changed {
		return false
	}
	return !diags.HasError()
}
