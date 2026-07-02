package kubernetes_node_pool

import (
	"context"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64default"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
)

// Node-pool statuses (kubernetes service vocabulary). Deletes are SOFT: a
// completed delete sets status "deleted" and RETAINS the row, so GET on a
// deleted pool returns 200 with status "deleted" forever — never 404. Every
// poll/read path below treats status "deleted" as gone (404 is kept only as
// a fallback).
const (
	statusActive  = "active" // node-pool ready state (pools are never "running")
	statusError   = "error"
	statusDeleted = "deleted"
)

// createConflictAttempts bounds the create retry on a 409 invalid_state
// (cluster transiently not "running", e.g. scaling/updating). Other 409s
// (code "conflict": duplicate pool name) fail fast — they won't heal.
const createConflictAttempts = 5

// The backend limits pool names to 18 chars because the name becomes part of
// the node hostname (k8s-<clusterUUID>-<name>-<index>, 63-char cap) and to a
// lowercase DNS label (hostname + Kubernetes node-name charset). Mirror both
// at plan time so a bad name fails before the apply.
const maxNodePoolNameLen = 18

var nodePoolNameRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`)

var (
	_ resource.Resource                = &kubernetesNodePoolResource{}
	_ resource.ResourceWithImportState = &kubernetesNodePoolResource{}
)

// NewResource returns a new kubernetes_node_pool resource factory.
func NewResource() resource.Resource {
	return &kubernetesNodePoolResource{}
}

type kubernetesNodePoolResource struct {
	client       *client.Client
	pollInterval time.Duration
	pollTimeout  time.Duration
}

func (r *kubernetesNodePoolResource) getPollInterval() time.Duration {
	if r.pollInterval > 0 {
		return r.pollInterval
	}
	return 10 * time.Second
}

func (r *kubernetesNodePoolResource) getPollTimeout() time.Duration {
	if r.pollTimeout > 0 {
		return r.pollTimeout
	}
	return 30 * time.Minute
}

func (r *kubernetesNodePoolResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_kubernetes_node_pool"
}

func (r *kubernetesNodePoolResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manages an additional node pool on a managed Kubernetes cluster. " +
			"The cluster's INITIAL node pool is owned by the frostmoln_kubernetes_cluster " +
			"resource (its initial_node_pool block) and cannot be managed here. " +
			"A cluster must keep at least one node pool — deleting the last one is refused by the API.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Description: "The unique identifier of the node pool.",
				Computed:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"cluster_id": schema.StringAttribute{
				Description: "The ID of the Kubernetes cluster this pool belongs to.",
				Required:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"name": schema.StringAttribute{
				Description: "The name of the node pool: a lowercase DNS label (a-z, 0-9, non-leading/trailing " +
					"hyphens) of at most 18 characters — it becomes part of each node's hostname. Defaults to a " +
					"generated \"pool-<8 hex>\" name. Pools cannot be renamed — changing it REPLACES the pool. " +
					"Pool names are unique per cluster, so an explicitly named pool cannot use " +
					"`lifecycle { create_before_destroy = true }` — the replacement create conflicts with the " +
					"still-live pool.",
				Optional: true,
				Computed: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
					stringplanmodifier.UseStateForUnknown(),
				},
				Validators: []validator.String{
					stringvalidator.LengthAtMost(maxNodePoolNameLen),
					stringvalidator.RegexMatches(nodePoolNameRe,
						"must be a lowercase DNS label (a-z, 0-9, non-leading/trailing hyphens)"),
				},
			},
			"flavor_id": schema.StringAttribute{
				Description: "The node flavor ID (see the frostmoln_kubernetes_flavors data source). " +
					"Pools cannot change flavor — changing it REPLACES the pool.",
				Required: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"node_count": schema.Int64Attribute{
				Description: "The number of worker nodes in the pool (1-100). Scaled in-place. " +
					"Omitting it manages the pool at 1 node — an out-of-band scale will be reverted on the next apply.",
				Optional: true,
				Computed: true,
				Default:  int64default.StaticInt64(1),
				Validators: []validator.Int64{
					int64validator.Between(1, 100),
				},
			},
			"status": schema.StringAttribute{
				Description: "The current status of the node pool.",
				Computed:    true,
			},
			"created_at": schema.StringAttribute{
				Description: "The timestamp when the node pool was created.",
				Computed:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"updated_at": schema.StringAttribute{
				Description: "The timestamp when the node pool was last updated.",
				Computed:    true,
			},
		},
	}
}

func (r *kubernetesNodePoolResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	c, ok := req.ProviderData.(*client.Client)
	if !ok {
		resp.Diagnostics.AddError(
			"Unexpected Resource Configure Type",
			fmt.Sprintf("Expected *client.Client, got: %T", req.ProviderData),
		)
		return
	}
	r.client = c
}

// poolsPath and poolPath percent-escape the interpolated IDs so a malformed or
// hostile value from state/import cannot smuggle a "/" into the request path
// (same rationale as client.TenantPath). Escaping does NOT neutralize the
// dot-segments "." and ".." (unreserved characters, and the client joins paths
// with path.Join, which cleans them) — those are rejected at the import
// boundary in ImportState, the only place a fresh ID enters state.
func (r *kubernetesNodePoolResource) poolsPath(clusterID string) string {
	return r.client.TenantPath("/kubernetes-clusters/" + url.PathEscape(clusterID) + "/node-pools")
}

func (r *kubernetesNodePoolResource) poolPath(clusterID, poolID string) string {
	return r.poolsPath(clusterID) + "/" + url.PathEscape(poolID)
}

// getPool fetches a node pool by ID.
func (r *kubernetesNodePoolResource) getPool(ctx context.Context, clusterID, poolID string) (*apiNodePool, error) {
	resp, err := r.client.Get(ctx, r.poolPath(clusterID, poolID), nil)
	if err != nil {
		return nil, err
	}
	return client.ParseResponse[apiNodePool](resp)
}

// pollPool waits for the node pool to reach the target status. A soft-deleted
// pool keeps answering 200 with status "deleted", which the poller sees as a
// regular state; 404 maps to "deleted" as a fallback.
func (r *kubernetesNodePoolResource) pollPool(ctx context.Context, clusterID, poolID string, targets, errorStates []string) error {
	_, err := client.WaitForState(ctx, client.PollConfig{
		Interval:     r.getPollInterval(),
		Timeout:      r.getPollTimeout(),
		TargetStates: targets,
		ErrorStates:  errorStates,
		ResourceName: "kubernetes_node_pool",
		PollFunc: func(pollCtx context.Context) (string, error) {
			current, pollErr := r.getPool(pollCtx, clusterID, poolID)
			if pollErr != nil {
				if client.IsNotFound(pollErr) {
					return statusDeleted, nil
				}
				return "", pollErr
			}
			return current.Status, nil
		},
	})
	return err
}

// isRetryableCreateConflict reports whether a create error is the transient
// 409 the backend answers while the cluster is not exactly "running" (e.g.
// scaling/updating — code "invalid_state"). Only that known-transient code is
// retried; a duplicate-name conflict (code "conflict") — and any future 409
// code — is treated as permanent and fails fast.
func isRetryableCreateConflict(err error) bool {
	apiErr, ok := err.(*client.APIError)
	return ok && apiErr.StatusCode == 409 && apiErr.Code == "invalid_state"
}

func (r *kubernetesNodePoolResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan KubernetesNodePoolModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	clusterID := plan.ClusterID.ValueString()
	createPath := r.poolsPath(clusterID)
	createReq := plan.toCreateRequest()

	// Node-pool create requires the cluster to be exactly "running"; a create
	// racing a transient cluster state (scaling/updating) gets a 409
	// invalid_state — retry briefly instead of failing the apply.
	var apiResp *client.Response
	var err error
	for attempt := 0; attempt < createConflictAttempts; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				resp.Diagnostics.AddError("Failed to create Kubernetes node pool", ctx.Err().Error())
				return
			case <-time.After(r.getPollInterval()):
			}
		}
		apiResp, err = r.client.Post(ctx, createPath, createReq)
		if err == nil || !isRetryableCreateConflict(err) {
			break
		}
	}
	if err != nil {
		resp.Diagnostics.AddError("Failed to create Kubernetes node pool", err.Error())
		return
	}
	created, err := client.ParseResponse[apiNodePool](apiResp)
	if err != nil {
		resp.Diagnostics.AddError("Failed to parse Kubernetes node pool response", err.Error())
		return
	}

	// Save state immediately so the ID is tracked even if polling fails
	// (orphan guard).
	plan.fromAPI(created)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if err := r.pollPool(ctx, clusterID, created.ID, []string{statusActive}, []string{statusError, statusDeleted}); err != nil {
		resp.Diagnostics.AddError("Kubernetes node pool failed to reach active state", err.Error())
		return
	}

	final, err := r.getPool(ctx, clusterID, created.ID)
	if err != nil {
		resp.Diagnostics.AddError("Failed to read Kubernetes node pool after creation", err.Error())
		return
	}
	plan.fromAPI(final)
	// node_count is a KNOWN planned value; a concurrent out-of-band scale
	// landing between the active-poll and this final read must not make the
	// applied state diverge from the plan (Terraform core hard-errors
	// "inconsistent result after apply"). Keep the planned count; the
	// out-of-band value surfaces as normal drift on the next refresh.
	plan.NodeCount = types.Int64Value(int64(createReq.NodeCount))
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *kubernetesNodePoolResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state KubernetesNodePoolModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	clusterID := state.ClusterID.ValueString()
	poolID := state.ID.ValueString()
	if clusterID == "" || poolID == "" {
		resp.Diagnostics.AddError(
			"Missing node pool identity",
			"The state contains no cluster ID or node pool ID; the state may be corrupt.",
		)
		return
	}

	pool, err := r.getPool(ctx, clusterID, poolID)
	if err != nil {
		if client.IsNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Failed to read Kubernetes node pool", err.Error())
		return
	}
	// Deletes are soft: a deleted pool still answers 200 with status
	// "deleted" — treat it as gone.
	if pool.Status == statusDeleted {
		resp.State.RemoveResource(ctx)
		return
	}
	// The initial pool is owned by the frostmoln_kubernetes_cluster resource
	// (its initial_node_pool block); managing it here too would double-drive
	// the same pool. Only reachable via import of an initial pool's ID — a
	// pool this resource created is never initial.
	if pool.IsInitial {
		resp.Diagnostics.AddError(
			"Node pool is the cluster's initial pool",
			fmt.Sprintf("Node pool %s is the initial pool of cluster %s, which is managed by the "+
				"frostmoln_kubernetes_cluster resource (initial_node_pool block). It cannot be managed "+
				"as a frostmoln_kubernetes_node_pool.", poolID, clusterID),
		)
		return
	}

	state.fromAPI(pool)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *kubernetesNodePoolResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state KubernetesNodePoolModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	clusterID := state.ClusterID.ValueString()
	poolID := state.ID.ValueString()
	if clusterID == "" || poolID == "" {
		resp.Diagnostics.AddError(
			"Missing node pool identity",
			"The state contains no cluster ID or node pool ID; the state may be corrupt.",
		)
		return
	}

	// node_count is the only in-place-updatable attribute (everything else
	// RequiresReplace). The backend gates /scale on both the cluster and the
	// pool being serviceable: a soft-deleted pool is 404, any other
	// non-scalable state is 409 — both surface as-is.
	if !plan.NodeCount.Equal(state.NodeCount) {
		scaleReq := apiScaleNodePoolRequest{NodeCount: int(plan.NodeCount.ValueInt64())}
		if _, err := r.client.Post(ctx, r.poolPath(clusterID, poolID)+"/scale", scaleReq); err != nil {
			if client.IsNotFound(err) {
				resp.Diagnostics.AddError(
					"Node pool no longer exists",
					fmt.Sprintf("Node pool %s was deleted outside Terraform, so it cannot be scaled. "+
						"Remove it from state or re-create it (terraform apply).", poolID),
				)
				return
			}
			resp.Diagnostics.AddError("Failed to scale Kubernetes node pool", err.Error())
			return
		}
		if err := r.pollPool(ctx, clusterID, poolID, []string{statusActive}, []string{statusError, statusDeleted}); err != nil {
			resp.Diagnostics.AddError("Kubernetes node pool failed to reach active state after scaling", err.Error())
			return
		}
	}

	final, err := r.getPool(ctx, clusterID, poolID)
	if err != nil {
		resp.Diagnostics.AddError("Failed to read Kubernetes node pool after update", err.Error())
		return
	}
	planned := plan.NodeCount
	plan.fromAPI(final)
	// Same planned-value guard as Create: node_count is known in the plan, so
	// the final read must not adopt a concurrently-amended live count.
	plan.NodeCount = planned
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *kubernetesNodePoolResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state KubernetesNodePoolModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	clusterID := state.ClusterID.ValueString()
	poolID := state.ID.ValueString()
	if clusterID == "" || poolID == "" {
		resp.Diagnostics.AddError(
			"Missing node pool identity",
			"The state contains no cluster ID or node pool ID; the state may be corrupt.",
		)
		return
	}

	// Apply the soft-delete contract BEFORE issuing the DELETE: an already-
	// soft-deleted pool still answers the pre-delete GetByID on the backend, so
	// a stale-state DELETE would either 409 "last node pool" (live count is
	// already down) or flip the deleted row back to "deleting" (the resurrection
	// class the /scale gate closes; DELETE has no such gate).
	pool, err := r.getPool(ctx, clusterID, poolID)
	if err != nil {
		if client.IsNotFound(err) {
			return
		}
		resp.Diagnostics.AddError("Failed to read Kubernetes node pool before deletion", err.Error())
		return
	}
	if pool.Status == statusDeleted {
		return
	}

	// The backend refuses to delete a cluster's last node pool (409) — surface
	// that as-is: the practitioner must delete the cluster instead.
	if _, err := r.client.Delete(ctx, r.poolPath(clusterID, poolID)); err != nil {
		if client.IsNotFound(err) {
			return
		}
		resp.Diagnostics.AddError("Failed to delete Kubernetes node pool", err.Error())
		return
	}

	// Deletes are soft: poll until the row reports status "deleted" (404 is
	// only a fallback — the row is retained).
	if err := r.pollPool(ctx, clusterID, poolID, []string{statusDeleted}, []string{statusError}); err != nil {
		resp.Diagnostics.AddError("Kubernetes node pool failed to delete", err.Error())
	}
}

func (r *kubernetesNodePoolResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	// Import ID format: {cluster_id}/{pool_id}. The subsequent Read refuses an
	// initial pool (owned by the cluster resource).
	parts := strings.SplitN(req.ID, "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		resp.Diagnostics.AddError(
			"Invalid Import ID",
			fmt.Sprintf("Expected import ID format: {cluster_id}/{pool_id}, got: %s", req.ID),
		)
		return
	}
	// Dot-segments survive url.PathEscape (unreserved characters) and would be
	// collapsed by the client's path.Join, restructuring the request URL — an
	// import ID like "../.." would redirect requests at a different API object.
	// Reject them here, the trust boundary where a fresh ID enters state. A "/"
	// inside the pool-ID part is escaped to a dead single segment later, but
	// reject it too for a clear error instead of a confusing 404.
	for _, p := range parts {
		if p == "." || p == ".." || strings.Contains(p, "/") {
			resp.Diagnostics.AddError(
				"Invalid Import ID",
				fmt.Sprintf("%q is not a valid cluster or node pool ID: path segments like %q are not allowed.", req.ID, p),
			)
			return
		}
	}

	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("cluster_id"), parts[0])...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), parts[1])...)
}
