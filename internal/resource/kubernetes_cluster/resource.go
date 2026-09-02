package kubernetes_cluster

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/boolplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64default"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/setplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/docs"
)

// Cluster and node-pool statuses (kubernetes service vocabulary). Deletes are
// SOFT: a completed delete sets status "deleted" and RETAINS the row, so GET
// on a deleted cluster/pool returns 200 with status "deleted" forever — never
// 404. Every poll/read path below treats status "deleted" as gone (404 is
// kept only as a fallback).
const (
	statusRunning = "running"
	statusActive  = "active" // node-pool ready state (pools are never "running")
	statusError   = "error"
	statusDeleted = "deleted"
)

const kubeconfigFetchAttempts = 3

var (
	_ resource.ResourceWithValidateConfig = &kubernetesClusterResource{}
	_ resource.Resource                   = &kubernetesClusterResource{}
	_ resource.ResourceWithImportState    = &kubernetesClusterResource{}
)

// NewResource returns a new kubernetes_cluster resource factory.
func NewResource() resource.Resource {
	return &kubernetesClusterResource{}
}

type kubernetesClusterResource struct {
	client       *client.Client
	pollInterval time.Duration
	pollTimeout  time.Duration
}

func (r *kubernetesClusterResource) getPollInterval() time.Duration {
	if r.pollInterval > 0 {
		return r.pollInterval
	}
	return 10 * time.Second
}

func (r *kubernetesClusterResource) getPollTimeout() time.Duration {
	if r.pollTimeout > 0 {
		return r.pollTimeout
	}
	return 30 * time.Minute
}

// ValidateConfig refuses `public_ip_id` at PLAN time.
//
// 🔴 A DeprecationMessage IS NOT ENOUGH HERE, and the difference is a destroyed
// cluster. A deprecation is a plan-time WARNING: the plan succeeds, the apply
// starts, and the backend answers 400. That is survivable on a create. It is not
// survivable on a REPLACEMENT — `public_ip_id` carries RequiresReplace, so a
// replacement triggered by any other attribute (a version change, a vpc_id change,
// an import diff) destroys the cluster FIRST and then issues the create the
// backend refuses. The practitioner is left with no cluster and a configuration
// that cannot be applied until they edit it.
//
// Refusing in ValidateConfig moves that to `terraform plan`, before anything is
// destroyed, and matches what `fm-cli` does with --public-ip.
func (r *kubernetesClusterResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var cfg KubernetesClusterModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	// Unknown (interpolated) values are skipped — the backend stays authoritative,
	// and a value that is not yet resolved cannot be judged here.
	if cfg.PublicIPID.IsNull() || cfg.PublicIPID.IsUnknown() || cfg.PublicIPID.ValueString() == "" {
		return
	}
	resp.Diagnostics.AddAttributeError(
		path.Root("public_ip_id"),
		"public_ip_id is no longer supported",
		"The Kubernetes API endpoint takes no customer-provided address, and any value is "+
			"rejected by the API. Remove public_ip_id from this resource.\n\n"+
			"To expose a workload, create a Kubernetes Service of type LoadBalancer inside the "+
			"cluster; the platform provisions a load balancer for it and the address is chosen "+
			"there, not on the cluster.\n\n"+
			"If removing the attribute makes the plan want to REPLACE an existing cluster, add "+
			"lifecycle { ignore_changes = [public_ip_id] } as well — the existing cluster keeps "+
			"the address it was created with.",
	)
}

func (r *kubernetesClusterResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_kubernetes_cluster"
}

func (r *kubernetesClusterResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		// NO gateway-ordering note. The cluster stopped being an attaching surface
		// when its API endpoint stopped taking a public address (public_ip_id is
		// refused now) — there is no attach left to order, and a note telling a
		// reader to sequence one would send them looking for a dependency that
		// cannot arise. See schemadoc.NotAttaching for the recorded reason.
		Description: "Manages a managed Kubernetes cluster in the Frostmoln platform. " +
			"The cluster owns its initial node pool (created embedded, scaled in-place). " +
			"Additional node pools are managed with the frostmoln_kubernetes_node_pool resource.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Description: "The unique identifier of the cluster.",
				Computed:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"name": schema.StringAttribute{
				Description: "The name of the cluster. Updatable in-place.",
				Required:    true,
			},
			"version": schema.StringAttribute{
				Description: "The Kubernetes version (e.g. \"1.35\"). Defaults to the platform default version. " +
					"Changing it currently REPLACES the cluster — in-place upgrade is not available yet.",
				Optional: true,
				Computed: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
					stringplanmodifier.RequiresReplace(),
				},
			},
			"control_plane_tier": schema.StringAttribute{
				Description: "The control-plane tier key (see the frostmoln_kubernetes_tiers data source for canonical keys). " +
					"Defaults to the platform default tier.",
				Optional: true,
				Computed: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
					stringplanmodifier.RequiresReplace(),
				},
			},
			"region": schema.StringAttribute{
				Description: "The region to create the cluster in. Defaults server-side.",
				Optional:    true,
				Computed:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
					stringplanmodifier.RequiresReplace(),
				},
			},
			"vpc_id": schema.StringAttribute{
				Description: "The VPC ID where the cluster nodes are deployed.",
				Required:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"subnet_id": schema.StringAttribute{
				Description: "The subnet ID where the cluster nodes are deployed.",
				Required:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"public_ip_id": schema.StringAttribute{
				DeprecationMessage: "public_ip_id is no longer supported and any value is rejected with a 400. " +
					"Remove it from your configuration. The Kubernetes API endpoint takes no customer-provided " +
					"address; expose workloads with a Service of type LoadBalancer, which gets its own.",
				Description: "REMOVED. Any value is rejected by the API with a 400, on every account — remove this " +
					"attribute from your configuration.\n\n" +
					"It supplied a bring-your-own address for the cluster API endpoint's public load balancer. " +
					"Clusters no longer have one: the API endpoint takes no address of yours, and workload " +
					"exposure is chosen per Service — create a Kubernetes Service of type LoadBalancer and the " +
					"platform provisions a load balancer for it.\n\n" +
					"An existing cluster created with a BYO public IP is unaffected and keeps its address; only " +
					"CREATE refuses the attribute.\n\n" +
					"🔴 REMOVING IT CAN PLAN A REPLACEMENT. This attribute forces replacement and is never read " +
					"back from the API, so deleting the line is itself the change that makes Terraform want to " +
					"destroy and recreate the cluster. Add `lifecycle { ignore_changes = [public_ip_id] }` " +
					"**as well as** removing it — the two are not alternatives, and only the pair is " +
					"non-destructive.",
				Optional: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"addons": schema.SetAttribute{
				Description: "The set of cluster-addon catalog keys to install at cluster creation (see the " +
					"frostmoln_kubernetes_addons data source for available keys). Addons are applied ONCE, at " +
					"cluster creation, from first-boot manifests — they cannot be changed on an existing cluster, so " +
					"changing this set REPLACES the cluster. Leave it unset to apply the platform default addons " +
					"(the frostmoln_kubernetes_addons data source reports which are defaulted); set it to an " +
					"explicit empty set ([]) to select none.",
				Optional:    true,
				Computed:    true,
				ElementType: types.StringType,
				PlanModifiers: []planmodifier.Set{
					setplanmodifier.UseStateForUnknown(),
					setplanmodifier.RequiresReplace(),
				},
			},
			"initial_node_pool": schema.SingleNestedAttribute{
				Description: "The cluster's initial node pool, owned by this resource.",
				Required:    true,
				Attributes: map[string]schema.Attribute{
					"id": schema.StringAttribute{
						Description: "The unique identifier of the initial node pool.",
						Computed:    true,
						PlanModifiers: []planmodifier.String{
							stringplanmodifier.UseStateForUnknown(),
						},
					},
					"name": schema.StringAttribute{
						Description: "The name of the initial node pool. Defaults to \"default\".",
						Optional:    true,
						Computed:    true,
						PlanModifiers: []planmodifier.String{
							stringplanmodifier.UseStateForUnknown(),
							stringplanmodifier.RequiresReplace(),
						},
					},
					"flavor_id": schema.StringAttribute{
						Description: "The node flavor ID (see the frostmoln_kubernetes_flavors data source).",
						Required:    true,
						PlanModifiers: []planmodifier.String{
							stringplanmodifier.RequiresReplace(),
						},
					},
					"node_count": schema.Int64Attribute{
						Description: "The number of worker nodes in the initial pool (1-100). Scaled in-place. " +
							"Omitting it manages the pool at 1 node — an out-of-band scale will be reverted on the next apply.",
						Optional: true,
						Computed: true,
						Default:  int64default.StaticInt64(1),
						Validators: []validator.Int64{
							int64validator.Between(1, 100),
						},
					},
					"status": schema.StringAttribute{
						Description: "The current status of the initial node pool.",
						Computed:    true,
					},
				},
			},
			"status": schema.StringAttribute{
				Description: "The current status of the cluster.",
				Computed:    true,
			},
			"ha_enabled": schema.BoolAttribute{
				Description: "Whether the control plane is highly available (derived from the control-plane tier).",
				Computed:    true,
				PlanModifiers: []planmodifier.Bool{
					boolplanmodifier.UseStateForUnknown(),
				},
			},
			"pod_cidr": schema.StringAttribute{
				Description: "The server-allocated pod network CIDR.",
				Computed:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"service_cidr": schema.StringAttribute{
				Description: "The server-allocated service network CIDR.",
				Computed:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"endpoint": schema.StringAttribute{
				Description: "The Kubernetes API endpoint URL.",
				Computed:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"load_balancer_id": schema.StringAttribute{
				Description: "The ID of the load balancer fronting the Kubernetes API.",
				Computed:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"public_ip": schema.StringAttribute{
				Description: "The public IP address of the cluster API endpoint.",
				Computed:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"ca_cert_hash": schema.StringAttribute{
				Description: "The cluster CA certificate hash.",
				Computed:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"kubeconfig": schema.StringAttribute{
				Description: "A kubeconfig for the cluster, re-fetched from the platform on every refresh while " +
					"the cluster is running. " + docs.StateSecretNote + " Because it is re-fetchable, state is " +
					"not the only place it can come from: if you do not need it in Terraform, leave the " +
					"attribute unreferenced and take it out of band with " +
					"`fm kubernetes cluster kubeconfig <cluster-id>`.",
				Computed:  true,
				Sensitive: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"created_at": schema.StringAttribute{
				Description: "The timestamp when the cluster was created.",
				Computed:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"updated_at": schema.StringAttribute{
				Description: "The timestamp when the cluster was last updated.",
				Computed:    true,
			},
			"tenant_id": schema.StringAttribute{
				Description: "The tenant ID that owns this cluster.",
				Computed:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
		},
	}
}

func (r *kubernetesClusterResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

// clusterPath and poolPath percent-escape the interpolated IDs so a malformed
// or hostile value from state/import stays a single literal path segment
// instead of restructuring the request URL via path cleaning (same rationale
// as client.TenantPath).
func (r *kubernetesClusterResource) clusterPath(id string) string {
	return r.client.TenantPath("/kubernetes-clusters/" + url.PathEscape(id))
}

func (r *kubernetesClusterResource) poolPath(clusterID, poolID string) string {
	return r.clusterPath(clusterID) + "/node-pools/" + url.PathEscape(poolID)
}

// getCluster fetches a cluster by ID.
func (r *kubernetesClusterResource) getCluster(ctx context.Context, id string) (*apiKubernetesCluster, error) {
	resp, err := r.client.Get(ctx, r.clusterPath(id), nil)
	if err != nil {
		return nil, err
	}
	return client.ParseResponse[apiKubernetesCluster](resp)
}

// findInitialNodePool lists the cluster's node pools and returns the live
// initial pool, or nil when none exists. The list includes soft-deleted rows,
// so deleted pools are filtered out here.
func (r *kubernetesClusterResource) findInitialNodePool(ctx context.Context, clusterID string) (*apiNodePool, error) {
	resp, err := r.client.Get(ctx, r.clusterPath(clusterID)+"/node-pools", nil)
	if err != nil {
		return nil, err
	}
	list, err := client.ParseResponse[apiNodePoolList](resp)
	if err != nil {
		return nil, err
	}
	for i := range list.NodePools {
		p := &list.NodePools[i]
		if p.IsInitial && p.Status != statusDeleted {
			return p, nil
		}
	}
	return nil, nil
}

// pollCluster waits for the cluster to reach the target status. A soft-deleted
// cluster keeps answering 200 with status "deleted", which the poller sees as
// a regular state; 404 maps to "deleted" as a fallback.
func (r *kubernetesClusterResource) pollCluster(ctx context.Context, id string, targets, errorStates []string) error {
	_, err := client.WaitForState(ctx, client.PollConfig{
		Interval:     r.getPollInterval(),
		Timeout:      r.getPollTimeout(),
		TargetStates: targets,
		ErrorStates:  errorStates,
		ResourceName: "kubernetes_cluster",
		PollFunc: func(pollCtx context.Context) (string, error) {
			current, pollErr := r.getCluster(pollCtx, id)
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

// pollNodePool waits for the initial node pool to reach the target status.
func (r *kubernetesClusterResource) pollNodePool(ctx context.Context, clusterID, poolID string, targets, errorStates []string) error {
	poolPath := r.poolPath(clusterID, poolID)
	_, err := client.WaitForState(ctx, client.PollConfig{
		Interval:     r.getPollInterval(),
		Timeout:      r.getPollTimeout(),
		TargetStates: targets,
		ErrorStates:  errorStates,
		ResourceName: "kubernetes_cluster initial node pool",
		PollFunc: func(pollCtx context.Context) (string, error) {
			resp, pollErr := r.client.Get(pollCtx, poolPath, nil)
			if pollErr != nil {
				if client.IsNotFound(pollErr) {
					return statusDeleted, nil
				}
				return "", pollErr
			}
			current, parseErr := client.ParseResponse[apiNodePool](resp)
			if parseErr != nil {
				return "", parseErr
			}
			return current.Status, nil
		},
	})
	return err
}

// fetchKubeconfig retrieves the cluster kubeconfig with a short bounded retry
// (tolerates a transient 5xx right after provisioning completes). 4xx errors
// are not retried — they won't heal within the retry window.
func (r *kubernetesClusterResource) fetchKubeconfig(ctx context.Context, id string) (string, error) {
	var lastErr error
	for attempt := 0; attempt < kubeconfigFetchAttempts; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			case <-time.After(r.getPollInterval()):
			}
		}
		resp, err := r.client.Get(ctx, r.clusterPath(id)+"/kubeconfig", nil)
		if err != nil {
			lastErr = err
			if apiErr, ok := err.(*client.APIError); ok && apiErr.StatusCode < 500 {
				break
			}
			continue
		}
		kc, err := client.ParseResponse[apiKubeconfig](resp)
		if err != nil {
			lastErr = err
			continue
		}
		return kc.Kubeconfig, nil
	}
	return "", lastErr
}

func (r *kubernetesClusterResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan KubernetesClusterModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	apiResp, err := r.client.Post(ctx, r.client.TenantPath("/kubernetes-clusters"), plan.toCreateRequest())
	if err != nil {
		resp.Diagnostics.AddError("Failed to create Kubernetes cluster", err.Error())
		return
	}
	created, err := client.ParseResponse[apiKubernetesCluster](apiResp)
	if err != nil {
		resp.Diagnostics.AddError("Failed to parse Kubernetes cluster response", err.Error())
		return
	}

	// Save state immediately so the ID is tracked even if polling fails
	// (orphan guard). The initial pool is not in the create response — its
	// computed attributes stay null until discovered below; unknown plan
	// values must not be persisted.
	plan.fromAPI(created)
	plan.Kubeconfig = types.StringNull()
	plan.InitialNodePool.ID = types.StringNull()
	plan.InitialNodePool.Status = types.StringNull()
	if plan.InitialNodePool.Name.IsUnknown() {
		plan.InitialNodePool.Name = types.StringNull()
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	clusterID := created.ID

	if err := r.pollCluster(ctx, clusterID, []string{statusRunning}, []string{statusError, statusDeleted}); err != nil {
		resp.Diagnostics.AddError("Kubernetes cluster failed to reach running state", err.Error())
		return
	}

	// The initial node pool is a separate row discovered via the pool list.
	// Poll it to "active" so a pool that finalizes asynchronously (control-
	// plane-gated create) can't fail silently after apply reports success.
	pool, err := r.findInitialNodePool(ctx, clusterID)
	if err != nil {
		resp.Diagnostics.AddError("Failed to discover the cluster's initial node pool", err.Error())
		return
	}
	if pool == nil {
		resp.Diagnostics.AddError(
			"Initial node pool not found",
			"The cluster reached running state but no live initial node pool exists.",
		)
		return
	}
	if err := r.pollNodePool(ctx, clusterID, pool.ID, []string{statusActive}, []string{statusError, statusDeleted}); err != nil {
		resp.Diagnostics.AddError("Initial node pool failed to reach active state", err.Error())
		return
	}

	// Kubeconfig fetch is best-effort at the very end of a long apply: a
	// persistent failure downgrades to a warning instead of failing the
	// 30-minute create at its last step; the next refresh retries.
	kubeconfig, kcErr := r.fetchKubeconfig(ctx, clusterID)

	final, err := r.getCluster(ctx, clusterID)
	if err != nil {
		resp.Diagnostics.AddError("Failed to read Kubernetes cluster after creation", err.Error())
		return
	}
	finalPool, err := r.findInitialNodePool(ctx, clusterID)
	if err != nil || finalPool == nil {
		finalPool = pool
	}

	plan.fromAPI(final)
	plan.setInitialNodePool(finalPool)
	if kcErr != nil {
		resp.Diagnostics.AddWarning(
			"Failed to fetch cluster kubeconfig",
			fmt.Sprintf("The cluster was created successfully but the kubeconfig could not be retrieved: %s. "+
				"It will be fetched on the next terraform refresh.", kcErr),
		)
		plan.Kubeconfig = types.StringNull()
	} else {
		plan.Kubeconfig = types.StringValue(kubeconfig)
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *kubernetesClusterResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state KubernetesClusterModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	id := state.ID.ValueString()
	if id == "" {
		resp.Diagnostics.AddError("Missing cluster ID", "The state contains no cluster ID; the state may be corrupt.")
		return
	}

	cluster, err := r.getCluster(ctx, id)
	if err != nil {
		if client.IsNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Failed to read Kubernetes cluster", err.Error())
		return
	}
	// Deletes are soft: a deleted cluster still answers 200 with status
	// "deleted" — treat it as gone.
	if cluster.Status == statusDeleted {
		resp.State.RemoveResource(ctx)
		return
	}

	// fromAPI leaves public_ip_id and kubeconfig untouched (preserved from
	// prior state).
	state.fromAPI(cluster)

	pool, err := r.findInitialNodePool(ctx, id)
	if err != nil {
		resp.Diagnostics.AddError("Failed to list the cluster's node pools", err.Error())
		return
	}
	if pool != nil {
		state.setInitialNodePool(pool)
	} else {
		// The backend only guards the LAST pool against deletion, so the
		// initial pool can be deleted out-of-band. Surface it loudly instead
		// of producing a silent perpetual diff.
		resp.Diagnostics.AddWarning(
			"Initial node pool deleted outside Terraform",
			fmt.Sprintf("Cluster %s has no live initial node pool. The initial pool cannot be re-created "+
				"in place — replace the cluster (terraform apply -replace=<this resource>).", id),
		)
	}

	if cluster.Status == statusRunning {
		// Refresh the kubeconfig only when the cluster can serve it (the
		// endpoint answers 409 otherwise). fetchKubeconfig retries transient
		// 5xx — an import Read has no prior value to keep, so a single
		// transient failure would fail ImportStateVerify. The prior value is
		// kept on any error; a 409 stays silent (expected transient),
		// anything else gets a warning so a persistently stale kubeconfig
		// doesn't go unnoticed.
		kubeconfig, kcErr := r.fetchKubeconfig(ctx, id)
		if kcErr == nil {
			state.Kubeconfig = types.StringValue(kubeconfig)
		} else {
			if apiErr, ok := kcErr.(*client.APIError); !ok || apiErr.StatusCode != 409 {
				resp.Diagnostics.AddWarning(
					"Failed to refresh cluster kubeconfig",
					fmt.Sprintf("Keeping the previously stored kubeconfig; the live one could not be retrieved: %s", kcErr),
				)
			}
		}
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *kubernetesClusterResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state KubernetesClusterModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	id := state.ID.ValueString()
	if id == "" {
		resp.Diagnostics.AddError("Missing cluster ID", "The state contains no cluster ID; the state may be corrupt.")
		return
	}

	if !plan.Name.Equal(state.Name) {
		name := plan.Name.ValueString()
		if _, err := r.client.Put(ctx, r.clusterPath(id), apiUpdateClusterRequest{Name: &name}); err != nil {
			resp.Diagnostics.AddError("Failed to rename Kubernetes cluster", err.Error())
			return
		}
	}

	scaled := false
	if !plan.InitialNodePool.NodeCount.Equal(state.InitialNodePool.NodeCount) {
		// Re-verify the pool is live before scaling: the backend does not
		// gate /scale on the pool's own status, so scaling a stale ID from
		// state would resurrect a soft-deleted pool row.
		livePool, err := r.findInitialNodePool(ctx, id)
		if err != nil {
			resp.Diagnostics.AddError("Failed to verify the initial node pool before scaling", err.Error())
			return
		}
		if livePool == nil || livePool.ID != state.InitialNodePool.ID.ValueString() {
			resp.Diagnostics.AddError(
				"Initial node pool no longer exists",
				"The cluster's initial node pool was deleted or replaced outside Terraform, so it cannot "+
					"be scaled. Replace the cluster (terraform apply -replace=<this resource>).",
			)
			return
		}
		scaleReq := apiScaleNodePoolRequest{NodeCount: int(plan.InitialNodePool.NodeCount.ValueInt64())}
		if _, err := r.client.Post(ctx, r.poolPath(id, livePool.ID)+"/scale", scaleReq); err != nil {
			resp.Diagnostics.AddError("Failed to scale the initial node pool", err.Error())
			return
		}
		if err := r.pollNodePool(ctx, id, livePool.ID, []string{statusActive}, []string{statusError, statusDeleted}); err != nil {
			resp.Diagnostics.AddError("Initial node pool failed to reach active state after scaling", err.Error())
			return
		}
		if err := r.pollCluster(ctx, id, []string{statusRunning}, []string{statusError, statusDeleted}); err != nil {
			resp.Diagnostics.AddError("Kubernetes cluster failed to return to running state after scaling", err.Error())
			return
		}
		scaled = true
	}

	final, err := r.getCluster(ctx, id)
	if err != nil {
		resp.Diagnostics.AddError("Failed to read Kubernetes cluster after update", err.Error())
		return
	}
	plan.fromAPI(final)

	if scaled {
		pool, err := r.findInitialNodePool(ctx, id)
		if err != nil {
			resp.Diagnostics.AddError("Failed to list the cluster's node pools after update", err.Error())
			return
		}
		if pool != nil {
			plan.setInitialNodePool(pool)
		} else {
			plan.InitialNodePool = state.InitialNodePool
		}
	} else {
		// No pool mutation: carry the prior pool state instead of a re-read,
		// so a concurrent out-of-band scale can't make the applied state
		// diverge from a known planned node_count.
		plan.InitialNodePool = state.InitialNodePool
	}
	// kubeconfig is carried from prior state by UseStateForUnknown; it only
	// changes on replacement.

	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *kubernetesClusterResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state KubernetesClusterModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	id := state.ID.ValueString()
	if id == "" {
		resp.Diagnostics.AddError("Missing cluster ID", "The state contains no cluster ID; the state may be corrupt.")
		return
	}

	if _, err := r.client.Delete(ctx, r.clusterPath(id)); err != nil {
		if client.IsNotFound(err) {
			return
		}
		resp.Diagnostics.AddError("Failed to delete Kubernetes cluster", err.Error())
		return
	}

	// Deletes are soft: poll until the row reports status "deleted" (404 is
	// only a fallback — the row is retained).
	if err := r.pollCluster(ctx, id, []string{statusDeleted}, []string{statusError}); err != nil {
		resp.Diagnostics.AddError("Kubernetes cluster failed to delete", err.Error())
	}
}

func (r *kubernetesClusterResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	// public_ip_id cannot be recovered on import (write-only on the API);
	// documented as a known limitation.
	if strings.Contains(req.ID, "/") {
		resp.Diagnostics.AddError(
			"Invalid import ID",
			fmt.Sprintf("%q is not a cluster ID: it must be the bare cluster UUID, without path separators.", req.ID),
		)
		return
	}
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}
