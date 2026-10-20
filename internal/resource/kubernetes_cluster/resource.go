package kubernetes_cluster

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/mapvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/boolplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64default"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/mapplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/setplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/docs"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/pubid"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/scopedecl"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/tftags"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/timeouts"
)

// Cluster and node-pool statuses (kubernetes service vocabulary). Deletes are
// SOFT: a completed delete sets status "deleted" and RETAINS the row, so GET
// on a deleted cluster/pool returns 200 with status "deleted" forever — never
// 404. Every poll/read path below treats status "deleted" as gone (404 is
// kept only as a fallback).
const (
	statusRunning  = "running"
	statusActive   = "active" // node-pool ready state (pools are never "running")
	statusError    = "error"
	statusDeleted  = "deleted"
	statusDeleting = "deleting" // teardown in progress: the cluster's addons can no longer change
)

const kubeconfigFetchAttempts = 3

var (
	_ resource.ResourceWithModifyPlan  = &kubernetesClusterResource{}
	_ resource.Resource                = &kubernetesClusterResource{}
	_ resource.ResourceWithImportState = &kubernetesClusterResource{}
)

// NewResource returns a new kubernetes_cluster resource factory.
func NewResource() resource.Resource {
	return &kubernetesClusterResource{}
}

type kubernetesClusterResource struct {
	client       *client.Client
	pollInterval time.Duration
	pollTimeout  time.Duration
	// poolTagRetryInterval overrides patchPoolTags' wait between attempts (tests).
	poolTagRetryInterval time.Duration
}

func (r *kubernetesClusterResource) getPollInterval() time.Duration {
	if r.pollInterval > 0 {
		return r.pollInterval
	}
	return 10 * time.Second
}

// getPollTimeout is the DEFAULT wait budget — the timeouts block's fallback
// per verb. Create has always polled the cluster provision and the initial
// pool against 30m; the update's scale poll and the delete's status poll
// default to the same value so the resource keeps one number.
func (r *kubernetesClusterResource) getPollTimeout() time.Duration {
	if r.pollTimeout > 0 {
		return r.pollTimeout
	}
	return 30 * time.Minute
}

// resolveBudgets turns the configured timeouts block into effective budgets,
// falling back per verb to the same value this resource has always hardcoded.
// Routing the defaults through the accessor keeps the test-injection seam
// intact: a test that shrinks pollTimeout shrinks every wait that does not
// carry an explicit timeouts override, exactly as before.
func (r *kubernetesClusterResource) resolveBudgets(m *timeouts.Model) timeouts.Budgets {
	budgets, err := m.Resolve(timeouts.Uniform(r.getPollTimeout()))
	if err != nil {
		// Unreachable via HCL (the block validator rejects bad durations at
		// plan time); degrade to the defaults rather than fail a wait.
		return timeouts.Uniform(r.getPollTimeout())
	}
	return budgets
}

// ModifyPlan refuses `public_ip_id` on the plan that would CREATE a cluster with
// it — including the create half of a replacement, which Terraform plans as a
// SEPARATE PlanResourceChange with a null prior state, before anything is
// destroyed (verified against Terraform 1.16.1 with a minimal provider).
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
// 🔴 AND IT MUST NOT BE A ValidateConfig ERROR, which is what this was until
// 2026-09-06. Terraform validates the configuration on a DESTROY too, so a
// cluster created back when the attribute was supported, whose HCL still names
// it — exactly the population this refusal targets — could not be destroyed at
// all: `terraform destroy` aborted on the validator, and the message's own
// remedy (`ignore_changes`) does not suppress a validator. Keying the refusal on
// a null prior state keeps the pre-destroy protection above and leaves every
// operation on an EXISTING cluster (update, destroy) free; those get a warning.
func (r *kubernetesClusterResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	// tags_all: a default_tags change, or a managed key's drift, plans an in-place
	// update (it handles the destroy plan itself). The initial pool's pair first: the
	// cluster's half must then see a plan that writes the pool's tags as an update
	// too, which re-reads the cluster (its tags_all may have moved since the refresh).
	defaults := r.client.DefaultTags()
	tftags.PlanNestedTagsAll(ctx, defaults, req, resp, tftags.ScopedReader(req.Private, initialPoolTagsScope),
		path.Root("initial_node_pool").AtName("tags"), path.Root("initial_node_pool").AtName("tags_all"))
	planned := req
	planned.Plan = resp.Plan
	tftags.PlanTagsAll(ctx, defaults, planned, resp)
	tftags.WarnOfferTagBudget(ctx, defaults, req, resp)
	warnInitialPoolTags(ctx, defaults, req, &resp.Diagnostics)

	// Destroy: no plan to judge.
	if req.Plan.Raw.IsNull() {
		return
	}
	var plan KubernetesClusterModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	warnReplaceWithoutRetainedChoice(ctx, req, plan, &resp.Diagnostics)
	resp.Diagnostics.Append(r.checkConfiguredAddonVersions(ctx, req.Config, plan.AddonVersions)...)
	if resp.Diagnostics.HasError() {
		return
	}
	// Unknown (interpolated) values are skipped — the backend stays authoritative,
	// and a value that is not yet resolved cannot be judged here.
	if plan.PublicIPID.IsNull() || plan.PublicIPID.IsUnknown() || plan.PublicIPID.ValueString() == "" {
		return
	}
	if req.State.Raw.IsNull() {
		resp.Diagnostics.AddAttributeError(path.Root("public_ip_id"), publicIPIDRemovedSummary, publicIPIDRemovedDetail)
		return
	}
	resp.Diagnostics.AddAttributeWarning(
		path.Root("public_ip_id"),
		publicIPIDRemovedSummary,
		publicIPIDRemovedDetail+"\n\nThis cluster already exists and keeps the address it was created with, so "+
			"nothing here fails today — but any change that replaces the cluster will be refused at plan.",
	)
}

const publicIPIDRemovedSummary = "public_ip_id is no longer supported"

const publicIPIDRemovedDetail = "The Kubernetes API endpoint takes no customer-provided address, and any value is " +
	"rejected by the API. Remove public_ip_id from this resource.\n\n" +
	"To expose a workload, create a Kubernetes Service of type LoadBalancer inside the " +
	"cluster; the platform provisions a load balancer for it and the address is chosen " +
	"there, not on the cluster.\n\n" +
	"If removing the attribute makes the plan want to REPLACE an existing cluster, add " +
	"lifecycle { ignore_changes = [public_ip_id] } as well — the existing cluster keeps " +
	"the address it was created with."

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
			"Additional node pools are managed with the frostmoln_kubernetes_node_pool resource." +
			"\n\n" + scopedecl.Summary("frostmoln_kubernetes_cluster"),
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
				Description: "The set of cluster-addon catalog keys for this cluster (see the " +
					"frostmoln_kubernetes_addons data source for available keys). ADDING a key is applied " +
					"IN PLACE to a running cluster: the accepted change starts its own reconciliation, so it " +
					"reaches the cluster within minutes rather than immediately, and falls back to the " +
					"platform's own addon reconciliation period if that cannot be started. REMOVING a key is " +
					"ALSO applied in place and DELETES the " +
					"objects that addon installed; a cluster whose control plane predates the platform's " +
					"ability to remove an addon is refused with an error saying so, and must be recreated " +
					"to gain it. When you SET this attribute, note that it is refreshed from the API before " +
					"every plan, so an addon added outside Terraform (the portal, the fm CLI) appears in " +
					"state and will be REMOVED on the next apply, as ordinary configuration drift — " +
					"terraform plan shows the removal before anything happens. Leaving the attribute unset " +
					"keeps such an addon, since nothing in configuration asks for its removal. Removal leaves behind what the platform will not " +
					"delete for you: the addon's namespace, any CustomResourceDefinition, StorageClass or " +
					"CSIDriver it created, and anything you put in that namespace yourself. Leave it unset to apply the " +
					"platform default addons (the frostmoln_kubernetes_addons data source reports which are " +
					"defaulted); set it to an explicit empty set ([]) to select none. An addon's " +
					"upstream container images are pulled from a public registry when its pods start " +
					"rather than preloaded onto your nodes, so the addon needs your worker nodes to have " +
					"outbound HTTPS to that registry; where an addon has such a prerequisite its catalog " +
					"description states it (external-dns: registry.k8s.io, which redirects image " +
					"downloads to cloud-hosted backing stores on other domains, so allowing that " +
					"hostname alone is not enough if you filter egress by name). That reachability is a " +
					"property of your own VPC at the moment of the pull, and every new node pulls again, " +
					"so the platform does not validate it: applying a key your nodes cannot pull " +
					"succeeds here and leaves the addon's pods in ImagePullBackOff. Nothing about the " +
					"cluster itself depends on it.",
				Optional:    true,
				Computed:    true,
				ElementType: types.StringType,
				PlanModifiers: []planmodifier.Set{
					setplanmodifier.UseStateForUnknown(),
					// 🔴 NO RequiresReplace IN EITHER DIRECTION, AND THE REMOVAL ONE WAS
					// DELETED RATHER THAN NEVER WRITTEN. Both directions are now day-2
					// operations on PUT .../clusters/{id}/addons: an addition applies in
					// place, and a removal prunes the addon's objects in place. Until the
					// platform could prune, a removal genuinely did require rebuilding the
					// cluster, and the modifier that planned it was correct. It is not any
					// more — keeping it would destroy a customer's cluster, and every
					// workload on it, to perform an operation that now happens in the
					// background without an outage.
				},
			},
			"retained_volumes_on_destroy": schema.StringAttribute{
				Description: "What destroying this cluster does to its volumes whose PersistentVolume has " +
					"reclaimPolicy `Retain`, or `Unknown` (the cluster has not reported its volumes recently, which " +
					"is treated as Retain): `keep` (they stay as ordinary volumes, listed with your other volumes " +
					"and still billed) or `delete` (they are deleted with the cluster). Volumes with reclaimPolicy " +
					"`Delete` are always deleted with the cluster. Unknown " +
					"means the cluster has not reported the volume in the last 15 minutes, for example one created " +
					"moments before the destroy. There is NO default: when the " +
					"cluster has such volumes and this is unset, destroy fails and lists them, and nothing is " +
					"deleted. APPLY THE SETTING FIRST: the destroy reads it from state, so set it and " +
					"`terraform apply` before `terraform destroy` or before a change that REPLACES the cluster " +
					"(the plan warns when a replacement is planned without it). Changing it is an in-place update " +
					"that makes no API call. Null after import. With either value, the destroy also fails if the set " +
					"of retained volumes changed after the check, so a volume is never deleted unseen and a volume " +
					"shown as kept is never deleted. Destroying a " +
					"cluster that is already being deleted only waits: the first delete's choice stands.",
				Optional:   true,
				Validators: []validator.String{stringvalidator.OneOf(retainedVolumesKeep, retainedVolumesDelete)},
			},
			"addon_versions": schema.MapAttribute{
				Description: "Pins addons to published versions, keyed by addon key (at most 32). Every key must " +
					"also be in addons, which must be set explicitly. List an addon's pinnable versions with the " +
					"frostmoln_kubernetes_addon_versions data source and use one of those strings verbatim. " +
					"Pins given when the cluster is created are sent in the create request, so the cluster " +
					"starts on those versions and a pin the platform refuses when the request is made fails the " +
					"create before anything exists. " +
					"Changing a pin later is applied IN PLACE and reaches the cluster within minutes, on the " +
					"reconciliation the accepted change starts, falling back to the platform's own addon " +
					"reconciliation period if that cannot be started. " +
					"Only pins whose value differs from state are sent, so an addon not " +
					"pinned here keeps whatever version it is pinned to, including a pin set outside Terraform. " +
					"An addon being ADDED to addons without a pin gets its recommended version; an addon that " +
					"is already selected keeps its current pin. After an import, or whenever state records no " +
					"pins, every configured pin is sent. A refresh reads back the pin of each addon named here, " +
					"so a configured pin moved outside Terraform is reported as drift and the next apply " +
					"restores it; pins of addons not named here are never read into state. When the platform " +
					"cannot report pins (for example during a platform deployment) the refresh keeps the " +
					"recorded values and warns. Removing a key stops Terraform managing that pin — the addon " +
					"stays on its current version, as there is no unpin. Read each addon's pinned and applied " +
					"version with the frostmoln_kubernetes_cluster_addons data source.",
				Optional:    true,
				ElementType: types.StringType,
				Validators: []validator.Map{
					mapvalidator.SizeAtMost(32),
					mapvalidator.KeysAre(stringvalidator.RegexMatches(addonKeyRe,
						"must be an addon catalog key: 1-64 lowercase letters, digits or hyphens")),
					mapvalidator.ValueStringsAre(stringvalidator.LengthBetween(1, 128)),
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
					"status_reason": schema.StringAttribute{
						Description: "Why the initial node pool failed, as a failure class (e.g. `QuotaExceeded`). Set only when `status` is `error`.",
						Computed:    true,
					},
					"status_message": schema.StringAttribute{
						Description: "A human-readable description of why the initial node pool failed. Set only when `status` is `error`.",
						Computed:    true,
					},
					"failed_step": schema.StringAttribute{
						Description: "The provisioning phase the initial node pool failed in, e.g. `node_pool`. Set only when `status` is `error`, and may be absent even then.",
						Computed:    true,
					},
					"tags": schema.MapAttribute{
						Description: "Resource tags on the initial node pool: your own key/value metadata for search and " +
							"filtering, stored by the platform only. They are NOT Kubernetes node labels and never reach " +
							"a node. At create the platform gives the pool a copy of the cluster's merged tags (the " +
							"cluster's `tags`, the provider's `default_tags` and the tenant's default tags); the keys set " +
							"here are then written onto the pool, and the copied keys are kept as keys this configuration " +
							"does not manage. The provider's `default_tags` apply to the pool as to any resource, so a " +
							"cluster `tags` key that overrides a default stays the cluster's only: set it here too to keep " +
							"it on the pool. Later changes to the cluster's `tags` do not reach the pool. Changing them " +
							"updates the pool in place and touches no node. The same limits as the cluster's `tags` apply." +
							tftags.TagsNote,
						Optional:    true,
						ElementType: types.StringType,
						Validators:  []validator.Map{tftags.OfferTagsValidator()},
					},
					"tags_all": tftags.TagsAllAttribute(),
				},
			},
			"status": schema.StringAttribute{
				Description: "The current status of the cluster.",
				Computed:    true,
			},
			"status_reason": schema.StringAttribute{
				Description: "Why the cluster failed, as a failure class (e.g. `QuotaExceeded`). Set only when `status` is `error`.",
				Computed:    true,
			},
			"status_message": schema.StringAttribute{
				Description: "A human-readable description of why the cluster failed. Set only when `status` is `error`.",
				Computed:    true,
			},
			"failed_step": schema.StringAttribute{
				Description: "The provisioning phase the cluster failed in: one of `validation`, `control_plane`, `api_endpoint`, `addons`, `node_pool` or `provisioning`. Set only when `status` is `error`, and may be absent even then.",
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
				Description: "Always null: the id of the load balancer fronting the Kubernetes API is internal, has no public form, and the API no longer returns it.",
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
					"the cluster is running. " + docs.StateSecretNote + " Its server address is private to the " +
					"cluster's VPC, so a `kubernetes` or `helm` provider configured from it must run somewhere " +
					"with a route into that VPC. Because it is re-fetchable, state is " +
					"not the only place it can come from: if you do not need it in Terraform, leave the " +
					"attribute unreferenced and take it out of band with " +
					"`fm kubernetes cluster kubeconfig <cluster-id> --exec`, which writes a kubeconfig that " +
					"opens that route itself (it needs an API key with both `kubernetes:read` and " +
					"`sessions:write`, and the `fm` binary on PATH wherever the kubeconfig is used — " +
					"a CI runner will not have it by default).",
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
		Blocks: map[string]schema.Block{
			// Customer-tunable wait budgets: defaults keep the values this
			// resource has always hardcoded (30m per verb). Every status
			// poll is bounded by the matching budget — create covers the
			// cluster-create poll and the initial node pool's discovery +
			// active poll, update covers the scale polls, delete the
			// delete-status poll. There is no 202 envelope on these routes
			// (creates answer 201/204 and the waits are status polls), so
			// the budgets bound only the waits. The addons PUT is never
			// status-polled (converged by a background reconciliation — see
			// the no-poll note in Update); only its retryable 409 is bounded
			// by the update budget. The
			// kubeconfig fetch's retries stay paced by the provider-internal
			// poll interval. A timeouts change is an in-place no-op on real
			// infrastructure — verified by the Gate 2 smoke test
			// (project-docs/product/TF-CONVERGENCE-WALL-PLAN.md).
			"timeouts": timeouts.Schema(),
		},
	}
	resp.Schema.Attributes["tags"] = schema.MapAttribute{
		Description: "Resource tags on the cluster: your own key/value metadata for search and filtering " +
			"(`?tag=key=value` in the API, `--tag` in the fm CLI), stored by the platform only and never " +
			"applied to the cluster or its nodes. At most 32 keys including the tenant's default tags, " +
			"which the platform merges in at create; a key is 1-64 bytes matching " +
			"`^[a-zA-Z0-9]([a-zA-Z0-9._:/-]*[a-zA-Z0-9])?$`, a value at most 256 bytes; keys beginning " +
			"`frostmoln_` or `frostmoln-` (any case) are reserved. Changing them updates the cluster in " +
			"place and does not touch the running cluster. " +
			"🔴 THE INITIAL NODE POOL GETS A ONE-TIME COPY. At create, the platform copies the cluster's " +
			"merged tags (these, the provider's `default_tags` and the tenant's default tags) onto the " +
			"initial node pool, once. Later changes to this attribute do NOT reach that pool: manage its " +
			"tags with `initial_node_pool.tags` (all of them are shown in `initial_node_pool.tags_all`). " +
			"Pools you add with `frostmoln_kubernetes_node_pool` get no copy: tag them on that resource, " +
			"where the same tags can be set again if you want them to match." + tftags.TagsNote,
		Optional:    true,
		ElementType: types.StringType,
		Validators:  []validator.Map{tftags.OfferTagsValidator()},
		PlanModifiers: []planmodifier.Map{
			mapplanmodifier.UseStateForUnknown(),
		},
	}
	resp.Schema.Attributes["tags_all"] = tftags.TagsAllAttribute()
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
// a regular state; 404 maps to "deleted" as a fallback. The wait budget is the
// timeouts block's budget for the operation polling (create, update, delete).
func (r *kubernetesClusterResource) pollCluster(ctx context.Context, id string, targets, errorStates []string, budget time.Duration) error {
	var last *apiKubernetesCluster
	_, err := client.WaitForState(ctx, client.PollConfig{
		Interval:     r.getPollInterval(),
		Timeout:      budget,
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
			last = current
			return current.Status, nil
		},
	})
	if err != nil && last != nil && last.Status == statusError {
		if detail := client.ErrorStateDetail("Kubernetes cluster", last.StatusReason, last.StatusMessage, last.FailedStep); detail != nil {
			return detail
		}
	}
	return err
}

// pollNodePool waits for the initial node pool to reach the target status.
// The wait budget is the timeouts block's budget for the operation polling
// (create, update, delete).
func (r *kubernetesClusterResource) pollNodePool(ctx context.Context, clusterID, poolID string, targets, errorStates []string, budget time.Duration) error {
	poolPath := r.poolPath(clusterID, poolID)
	var last *apiNodePool
	_, err := client.WaitForState(ctx, client.PollConfig{
		Interval:     r.getPollInterval(),
		Timeout:      budget,
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
			last = current
			return current.Status, nil
		},
	})
	if err != nil && last != nil && last.Status == statusError {
		if detail := client.ErrorStateDetail("Kubernetes cluster initial node pool", last.StatusReason, last.StatusMessage, last.FailedStep); detail != nil {
			return detail
		}
	}
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

	// Apply-time half of the ModifyPlan refusal above: the plan-time check skips an
	// unknown (interpolated) value, so a create carrying one reaches here with the
	// value resolved. The API answers 400; say why instead.
	if !plan.PublicIPID.IsNull() && plan.PublicIPID.ValueString() != "" {
		resp.Diagnostics.AddAttributeError(path.Root("public_ip_id"), publicIPIDRemovedSummary, publicIPIDRemovedDetail)
		return
	}

	// The customer's timeouts block, with defaults identical to the values
	// this resource has always hardcoded.
	budgets := r.resolveBudgets(plan.Timeouts)

	// Apply-time half of the addon_versions check (a value unknown at plan is known now).
	resp.Diagnostics.Append(r.checkConfiguredAddonVersions(ctx, req.Config, plan.AddonVersions)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// The configured pins ride this request (toCreateRequest). The platform refuses a pin it
	// cannot honour with a 400 BEFORE anything is created, so a 201 means they were taken.
	defaults := r.client.DefaultTags()
	createReq := plan.toCreateRequest()
	createReq.Tags = tftags.ForCreate(ctx, defaults, plan.Tags, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	// The initial pool's tags are written AFTER the cluster exists, so a pool set the
	// platform would refuse must be refused here, before anything is created: past the
	// POST it would leave a provisioning cluster tainted. A lower bound (the tenant's
	// default tags count too), but every key it counts is certain to be there.
	poolOwn, _ := knownTags(ctx, plan.InitialNodePool.Tags, &resp.Diagnostics)
	if n := tftags.TooManyOfferTags(createReq.Tags, poolOwn); n > 0 && len(createReq.Tags) <= tftags.OfferMaxTags {
		resp.Diagnostics.AddAttributeError(path.Root("initial_node_pool").AtName("tags"), "Too Many Tags On The Initial Node Pool",
			fmt.Sprintf("The initial node pool would hold %d tags: the cluster's merged tags, which the platform copies onto "+
				"it, plus initial_node_pool.tags. The platform accepts at most %d. Nothing was created.", n, tftags.OfferMaxTags))
		return
	}
	apiResp, err := r.client.Post(ctx, r.client.TenantPath("/kubernetes-clusters"), createReq)
	if err != nil {
		resp.Diagnostics.AddError("Failed to create Kubernetes cluster", err.Error())
		return
	}
	tftags.RecordDefaults(ctx, resp.Private, defaults, &resp.Diagnostics)
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
	plan.tagsFromAPI(ctx, created, &resp.Diagnostics)
	plan.Kubeconfig = types.StringNull()
	// addon_versions keeps the configured pins (fromAPI never touches it): the create request
	// that just succeeded carried them, so state claims nothing the platform did not accept.
	// If a wait below fails, the resource is tainted and replaced with the same pins; after an
	// untaint, the refresh reads each pin back, so a pin the platform lost shows as drift.
	plan.InitialNodePool.ID = types.StringNull()
	plan.InitialNodePool.Status = types.StringNull()
	plan.InitialNodePool.StatusReason = types.StringNull()
	plan.InitialNodePool.StatusMessage = types.StringNull()
	plan.InitialNodePool.FailedStep = types.StringNull()
	plan.InitialNodePool.TagsAll = types.MapNull(types.StringType)
	if plan.InitialNodePool.Name.IsUnknown() {
		plan.InitialNodePool.Name = types.StringNull()
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	clusterID := created.ID

	// The initial pool's tags, right away rather than after the long wait below: the
	// pool row is written with the cluster, so it exists now, and a refusal (the tag
	// budget) fails in seconds instead of after a 30-minute create.
	if err := r.writeInitialPoolTagsAfterCreate(ctx, clusterID, defaults, plan.Tags, plan.InitialNodePool.Tags, resp.Private, &resp.Diagnostics); err != nil {
		resp.Diagnostics.AddError("Failed to set the initial node pool's tags", err.Error())
		return
	}
	if resp.Diagnostics.HasError() {
		return
	}

	if err := r.pollCluster(ctx, clusterID, []string{statusRunning}, []string{statusError, statusDeleted}, budgets.Create); err != nil {
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
	if err := r.pollNodePool(ctx, clusterID, pool.ID, []string{statusActive}, []string{statusError, statusDeleted}, budgets.Create); err != nil {
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
	plan.tagsFromAPI(ctx, final, &resp.Diagnostics)
	plan.setInitialNodePool(ctx, finalPool, plan.InitialNodePool.Tags, &resp.Diagnostics)
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
	state.tagsFromAPI(ctx, cluster, &resp.Diagnostics)
	tftags.FinishRead(ctx, req.Private, resp.Private, r.client.DefaultTags(), &state.Tags, state.TagsAll, &resp.Diagnostics)

	// Pins are read back only for keys state already records (see refreshPins); no pins,
	// no request.
	if len(state.AddonVersions.Elements()) > 0 {
		state.AddonVersions = r.refreshPins(ctx, id, state.AddonVersions, &resp.Diagnostics)
	}

	pool, err := r.findInitialNodePool(ctx, id)
	if err != nil {
		resp.Diagnostics.AddError("Failed to list the cluster's node pools", err.Error())
		return
	}
	if pool != nil {
		priorPoolTags := types.MapNull(types.StringType)
		if state.InitialNodePool != nil {
			priorPoolTags = state.InitialNodePool.Tags
		}
		state.setInitialNodePool(ctx, pool, priorPoolTags, &resp.Diagnostics)
		tftags.FinishRead(ctx, tftags.ScopedReader(req.Private, initialPoolTagsScope),
			tftags.ScopedWriter(resp.Private, initialPoolTagsScope), r.client.DefaultTags(),
			&state.InitialNodePool.Tags, state.InitialNodePool.TagsAll, &resp.Diagnostics)
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

	// retained_volumes_on_destroy alone: a setting only Delete reads. Recorded in state
	// with NO API call (not even the refresh below), so changing it never touches the
	// cluster.
	if r.onlyDestroyChoiceChanged(ctx, plan, state, req.Private, &resp.Diagnostics) {
		state.RetainedVolumesOnDestroy = plan.RetainedVolumesOnDestroy
		state.Timeouts = plan.Timeouts
		resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
		return
	}
	if resp.Diagnostics.HasError() {
		return
	}

	// The customer's timeouts block, with defaults identical to the values
	// this resource has always hardcoded. The update's polls run on the
	// update budget; the addons PUT's retries run against the same deadline.
	budgets := r.resolveBudgets(plan.Timeouts)
	deadline := time.Now().Add(budgets.Update)

	// NAME AND TAGS: one PUT .../kubernetes-clusters/{id}, carrying ONLY what changed
	// (the PUT has PATCH semantics: an omitted field is kept). Tags are sent when the
	// platform's set would change — computed against the tags the cluster holds right
	// now, read here rather than taken from state, so a key added outside Terraform
	// since the last refresh is kept (tftags.Prior.WithCurrent). A tags-only change
	// starts no work on the cluster.
	defaults := r.client.DefaultTags()
	var meta apiUpdateClusterRequest
	if !plan.Name.Equal(state.Name) {
		name := plan.Name.ValueString()
		meta.Name = &name
	}
	tags, err := r.tagsToWrite(ctx, id, defaults, plan, state, req.Private, &resp.Diagnostics)
	if err != nil {
		resp.Diagnostics.AddError("Failed to Read Current Tags", tftags.CurrentTagsReadFailed+err.Error())
		return
	}
	if resp.Diagnostics.HasError() {
		return
	}
	meta.Tags = tags
	if meta.Name != nil || meta.Tags != nil {
		if _, err := r.client.Put(ctx, r.clusterPath(id), meta); err != nil {
			if meta.Name != nil {
				resp.Diagnostics.AddError("Failed to update Kubernetes cluster name or tags", err.Error())
			} else {
				resp.Diagnostics.AddError("Failed to update Kubernetes cluster tags", err.Error())
			}
			return
		}
	}
	// The platform now holds the desired set, whether or not this update had to
	// write it; that also ends an import's pending state.
	tftags.RecordDefaults(ctx, resp.Private, defaults, &resp.Diagnostics)

	// THE INITIAL POOL'S TAGS: a tags-only PATCH of the pool, judged the same way — only
	// when the pool's set would change, against the tags it holds right now.
	if state.InitialNodePool != nil && plan.InitialNodePool != nil {
		if !r.writeInitialPoolTags(ctx, id, defaults, *plan.InitialNodePool, *state.InitialNodePool, req.Private, resp.Private, &resp.Diagnostics) {
			return
		}
	}

	// ADDONS — ADDITIONS AND REMOVALS, BOTH IN PLACE. Removing one used to plan a full
	// cluster REPLACEMENT, because the API was add-only and rebuilding really was the
	// only way to be rid of an addon. The API prunes now, so `requiresReplaceOnAddonRemoval`
	// is deleted: destroying a customer's cluster and every workload on it, to perform an
	// operation the platform does in the background without an outage, is not a plan any
	// practitioner should be shown again.
	//
	// 🔴 THE REMOVAL MUST BE NAMED, NOT INFERRED FROM THE OMISSION. The endpoint refuses a
	// selection short of the cluster's current one unless the request also NAMES the keys
	// it drops — deliberately, so a client working from a stale read cannot delete. So
	// `remove` is computed as state-minus-plan and sent alongside.
	//
	// 🔴 AND THAT MEANS AN ADDON ADDED OUT OF BAND WILL BE DELETED ON THE NEXT APPLY. Read
	// refreshes `state.Addons` from the API before every plan, so an addon added via the
	// portal or `fm` lands in state, is absent from config, and is therefore planned for
	// removal. That is ordinary Terraform — config is authoritative and drift is reverted
	// — and `terraform plan` prints it for approval before anything happens. It is NOT
	// safe "by construction", so the schema description says so in as many words.
	//
	// `-refresh=false` fails CLOSED rather than silently: a stale state yields a `remove`
	// that does not cover what the server computes, and the server refuses.
	//
	// 🔴 NULL IS GUARDED ALONGSIDE UNKNOWN, and they mean different things. Unknown is
	// "not resolved yet"; null is "the practitioner did not configure it". Neither is
	// "the empty set" — and setToStringSlice(null) produces `{"addons": []}`, which with a
	// matching `remove` would now DELETE every addon rather than being refused. The guard
	// mattered before as a 400; it matters more now that the same request would succeed.
	// Not reachable today (Optional+Computed with UseStateForUnknown cannot present a null
	// plan against a non-null state), which is exactly why it is worth keeping.
	//
	// PINS RIDE THE SAME PUT. `versions` is changedPins(state, plan): configured pins whose
	// value changed, never a response's `pinnedVersions` and never a prior-state value.
	// A pin-only change sends the configured selection unchanged (checkConfiguredAddonVersions
	// guarantees `addons` is configured, so plan.Addons is the configuration, not a read).
	//
	// A 409 `invalid_state` is retried against the update deadline: the cluster is not
	// running yet, or — only for a request that REMOVES an addon — the platform is still
	// applying its selection or finishing an earlier removal. Between tries the cluster is
	// re-read and an `error`/`deleting`/`deleted` cluster fails at once. A 5xx is NOT retried here: an
	// update error taints nothing, and its message says re-applying is safe. Any failure
	// leaves the prior state in place (the framework seeds Update's response with it), so
	// the next apply re-sends the same delta.
	resp.Diagnostics.Append(r.checkConfiguredAddonVersions(ctx, req.Config, plan.AddonVersions)...)
	if resp.Diagnostics.HasError() {
		return
	}
	addonsChanged := !plan.Addons.IsUnknown() && !plan.Addons.IsNull() && !plan.Addons.Equal(state.Addons)
	// An addon this apply ADDS also carries its configured pin, even when state already
	// records that value: a pin outlives an out-of-band removal of its addon in state, so
	// without this a re-added addon would get the platform's recommended version instead.
	pins := addedAddonPins(changedPins(state.AddonVersions, plan.AddonVersions), state.Addons, plan.Addons, plan.AddonVersions)
	if addonsChanged || pins != nil {
		body := apiUpdateClusterAddonsRequest{
			Addons:   setToStringSlice(plan.Addons),
			Remove:   removedAddons(state.Addons, plan.Addons),
			Versions: pins,
		}
		apiResp, err := r.putAddons(ctx, id, body, deadline)
		if err != nil {
			resp.Diagnostics.AddError("Failed to change the cluster's addons", addonsPutFailureDetail(err))
			return
		}
		addNotices(&resp.Diagnostics, apiResp)
		// NO pollCluster HERE, DELIBERATELY. The addon change does not move the
		// cluster's status — it is converged by a background reconciliation, and the
		// cluster stays `running` throughout — so polling for `running` would return
		// instantly and assert nothing. The final read below picks up the recorded
		// selection, which is what the API changed.
	}

	if plan.InitialNodePool != nil && state.InitialNodePool == nil {
		resp.Diagnostics.AddError(
			"Initial node pool missing from state",
			"This cluster's state has no initial node pool (none was found when it was read), so it cannot be "+
				"updated. Replace the cluster (terraform apply -replace=<this resource>).",
		)
		return
	}

	scaled := false
	if plan.InitialNodePool != nil && state.InitialNodePool != nil &&
		!plan.InitialNodePool.NodeCount.Equal(state.InitialNodePool.NodeCount) {
		// Re-verify the pool is live before scaling: the backend does not
		// gate /scale on the pool's own status, so scaling a stale ID from
		// state would resurrect a soft-deleted pool row.
		livePool, err := r.findInitialNodePool(ctx, id)
		if err != nil {
			resp.Diagnostics.AddError("Failed to verify the initial node pool before scaling", err.Error())
			return
		}
		if livePool == nil || !pubid.Equal(livePool.ID, state.InitialNodePool.ID.ValueString()) {
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
		if err := r.pollNodePool(ctx, id, livePool.ID, []string{statusActive}, []string{statusError, statusDeleted}, budgets.Update); err != nil {
			resp.Diagnostics.AddError("Initial node pool failed to reach active state after scaling", err.Error())
			return
		}
		if err := r.pollCluster(ctx, id, []string{statusRunning}, []string{statusError, statusDeleted}, budgets.Update); err != nil {
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
	plan.tagsFromAPI(ctx, final, &resp.Diagnostics)

	if scaled {
		pool, err := r.findInitialNodePool(ctx, id)
		if err != nil {
			resp.Diagnostics.AddError("Failed to list the cluster's node pools after update", err.Error())
			return
		}
		if pool != nil {
			plan.setInitialNodePool(ctx, pool, plan.InitialNodePool.Tags, &resp.Diagnostics)
		} else {
			plan.InitialNodePool = carryInitialPool(state.InitialNodePool, plan.InitialNodePool)
		}
	} else {
		// No scale: carry the prior pool state instead of a re-read, so a concurrent
		// out-of-band scale can't make the applied state diverge from a known planned
		// node_count. Only its tags are re-read, when the plan left tags_all to the
		// apply (a tag write, or any update: a key added outside Terraform since the
		// refresh comes back with the read).
		next := carryInitialPool(state.InitialNodePool, plan.InitialNodePool)
		if next != nil && plan.InitialNodePool != nil && plan.InitialNodePool.TagsAll.IsUnknown() {
			pool, err := r.findInitialNodePool(ctx, id)
			if err != nil {
				resp.Diagnostics.AddError("Failed to read the initial node pool's tags after update", err.Error())
				return
			}
			if pool != nil && pubid.Equal(pool.ID, next.ID.ValueString()) {
				next.Tags, next.TagsAll = tftags.ReadBack(ctx, poolCustomerTags(pool), plan.InitialNodePool.Tags, &resp.Diagnostics)
			}
		}
		plan.InitialNodePool = next
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

	// The volume preflight, and the choice for the volumes it asks about
	// (retained_volumes_on_destroy). No choice while there are such volumes = no
	// delete: the platform refuses it too, and nothing may be deleted unseen.
	if !r.deleteWithVolumeChoice(ctx, id, state.RetainedVolumesOnDestroy, &resp.Diagnostics) {
		return
	}

	// Deletes are soft: poll until the row reports status "deleted" (404 is
	// only a fallback — the row is retained). The wait runs on the timeouts
	// block's delete budget.
	budgets := r.resolveBudgets(state.Timeouts)
	if err := r.pollCluster(ctx, id, []string{statusDeleted}, []string{statusError}, budgets.Delete); err != nil {
		resp.Diagnostics.AddError("Kubernetes cluster failed to delete", err.Error())
	}
}

func (r *kubernetesClusterResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	// public_ip_id cannot be recovered on import (write-only on the API);
	// documented as a known limitation.
	if strings.Contains(req.ID, "/") {
		resp.Diagnostics.AddError(
			"Invalid import ID",
			fmt.Sprintf("%q is not a cluster ID: it must be the bare cluster id (k8s_… or a UUID), without path separators.", req.ID),
		)
		return
	}
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
	tftags.MarkImported(ctx, resp.Private, &resp.Diagnostics)
	tftags.MarkImported(ctx, tftags.ScopedWriter(resp.Private, initialPoolTagsScope), &resp.Diagnostics)
}

// carryInitialPool is the prior pool state with the configuration's `tags`, for an
// update that does not re-read the pool. tags_all keeps the prior value unless the
// caller re-reads it; a still-unknown planned value takes the prior one.
func carryInitialPool(prior, planned *InitialNodePoolModel) *InitialNodePoolModel {
	if prior == nil {
		return nil
	}
	next := *prior
	if planned != nil {
		next.Tags = planned.Tags
	}
	return &next
}

// writeInitialPoolTagsAfterCreate writes the initial pool's tags right after the
// cluster create. The pool holds the platform's copy of the cluster's merged tags.
// The provider's default_tags apply to the pool as to any taggable resource; the
// copy's other keys are kept as keys this configuration does not manage.
//
// The copy is already the desired set unless the pool configures keys of its own
// or a cluster tag overrode a default (the override is the cluster's; the pool gets
// the default) — both known here, so the common create makes no extra request.
func (r *kubernetesClusterResource) writeInitialPoolTagsAfterCreate(ctx context.Context, clusterID string, defaults tftags.Defaults,
	clusterTags, tags types.Map, private tftags.PrivateWriter, diags *diag.Diagnostics,
) error {
	if len(tags.Elements()) == 0 && !overridesADefault(ctx, defaults, clusterTags, diags) {
		tftags.RecordDefaults(ctx, tftags.ScopedWriter(private, initialPoolTagsScope), defaults, diags)
		return nil
	}
	pool, err := r.findInitialNodePool(ctx, clusterID)
	if err != nil {
		return err
	}
	if pool == nil {
		return fmt.Errorf("cluster %s has no live initial node pool", clusterID)
	}
	prior := tftags.Prior{
		Tags:        types.MapNull(types.StringType),
		TagsAll:     types.MapNull(types.StringType),
		DefaultKeys: defaults.Keys(),
	}.WithCurrent(poolCustomerTags(pool))
	want, changed := tftags.ForUpdate(ctx, defaults, tags, prior, diags)
	if diags.HasError() {
		return nil
	}
	if changed {
		if err := r.patchPoolTags(ctx, clusterID, pool.ID, want); err != nil {
			return err
		}
	}
	tftags.RecordDefaults(ctx, tftags.ScopedWriter(private, initialPoolTagsScope), defaults, diags)
	return nil
}

// patchPoolTags writes a pool's tags (a full replace), after re-checking the
// resolved map against the offer policy (the plan-time validator skips an unknown
// map). A transient failure (5xx, connection) is retried a few times: right after
// a create, a failure here taints a cluster that is already provisioning.
func (r *kubernetesClusterResource) patchPoolTags(ctx context.Context, clusterID, poolID string, tags map[string]string) error {
	if problems := tftags.OfferTagsProblems(tags); len(problems) > 0 {
		return fmt.Errorf("the initial node pool's tags break the tag policy: %s", strings.Join(problems, "; "))
	}
	var err error
	for attempt := 0; attempt < poolTagWriteAttempts; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return err
			case <-time.After(r.getPoolTagRetryInterval()):
			}
		}
		if _, err = r.client.Patch(ctx, r.poolPath(clusterID, poolID), apiUpdateNodePoolRequest{Tags: &tags}); err == nil || !isTransient(err) {
			return err
		}
	}
	return err
}

// poolTagWriteAttempts bounds patchPoolTags' retries.
const poolTagWriteAttempts = 4

func (r *kubernetesClusterResource) getPoolTagRetryInterval() time.Duration {
	if r.poolTagRetryInterval > 0 {
		return r.poolTagRetryInterval
	}
	return 2 * time.Second
}

// isTransient reports a failure worth retrying: a 5xx answer, or no answer at all.
func isTransient(err error) bool {
	var apiErr *client.APIError
	if errors.As(err, &apiErr) {
		return apiErr.StatusCode >= 500
	}
	return true
}

// knownTags returns a tag map's elements; ok is false when it is unknown.
func knownTags(ctx context.Context, m types.Map, diags *diag.Diagnostics) (map[string]string, bool) {
	out := map[string]string{}
	if m.IsUnknown() {
		return nil, false
	}
	if m.IsNull() {
		return out, true
	}
	for _, v := range m.Elements() {
		if v.IsUnknown() {
			return nil, false
		}
	}
	diags.Append(m.ElementsAs(ctx, &out, false)...)
	return out, true
}

// warnInitialPoolTags is the plan-time half for the initial pool's tags: the pool's
// tag budget (what it holds, or at create the copy it gets, plus default_tags and
// its own keys — the tenant's defaults count too), and, at create, a cluster key
// that overrides a default, which the pool will NOT keep. Warnings only: a plan
// error would also abort a destroy.
func warnInitialPoolTags(ctx context.Context, defaults tftags.Defaults, req resource.ModifyPlanRequest, diags *diag.Diagnostics) {
	if req.Plan.Raw.IsNull() || defaults.Unknown {
		return
	}
	var plan KubernetesClusterModel
	if d := req.Plan.Get(ctx, &plan); d.HasError() || plan.InitialNodePool == nil {
		return
	}
	poolPath := path.Root("initial_node_pool").AtName("tags")
	own, ok := knownTags(ctx, plan.InitialNodePool.Tags, diags)
	if !ok {
		return
	}
	var base map[string]string
	if req.State.Raw.IsNull() {
		cluster, ok := knownTags(ctx, plan.Tags, diags)
		if !ok {
			return
		}
		var over []string
		for k, v := range cluster {
			if d, isDefault := defaults.Tags[k]; isDefault && d != v {
				if _, poolSets := own[k]; !poolSets {
					over = append(over, k)
				}
			}
		}
		if len(over) > 0 {
			sort.Strings(over)
			diags.AddAttributeWarning(poolPath, "Cluster Tag Overrides A Default On The Initial Node Pool",
				fmt.Sprintf("The cluster's tags override the provider's default_tags for %s. The override is the "+
					"cluster's only: the initial node pool gets the default value. Set the key in "+
					"initial_node_pool.tags too to keep it on the pool.", strings.Join(over, ", ")))
		}
		base = cluster
	} else {
		// What the next write leaves: removed managed keys and dropped defaults do not count.
		var state KubernetesClusterModel
		if d := req.State.Get(ctx, &state); d.HasError() || state.InitialNodePool == nil {
			return
		}
		prior := tftags.PriorOf(ctx, state.InitialNodePool.Tags, state.InitialNodePool.TagsAll,
			tftags.ScopedReader(req.Private, initialPoolTagsScope), diags)
		desired, ok := tftags.Desired(ctx, defaults, plan.InitialNodePool.Tags, &prior, diags)
		if !ok {
			return
		}
		base = desired
	}
	if n := tftags.TooManyOfferTags(base, defaults.Tags, own); n > 0 {
		diags.AddAttributeWarning(poolPath, "Too Many Tags On The Initial Node Pool",
			fmt.Sprintf("The initial node pool would hold %d tags (the ones it holds or is copied from the cluster, "+
				"default_tags and initial_node_pool.tags); the platform accepts at most %d, the tenant's default "+
				"tags included, so the write will be refused.", n, tftags.OfferMaxTags))
	}
}

// overridesADefault reports whether the cluster's own tags set a default_tags key
// to another value.
func overridesADefault(ctx context.Context, defaults tftags.Defaults, clusterTags types.Map, diags *diag.Diagnostics) bool {
	own := map[string]string{}
	if clusterTags.IsNull() || clusterTags.IsUnknown() {
		return false
	}
	diags.Append(clusterTags.ElementsAs(ctx, &own, false)...)
	for k, v := range own {
		if d, ok := defaults.Tags[k]; ok && d != v {
			return true
		}
	}
	return false
}

// writeInitialPoolTags is the update half: judged against state first (the plan was
// made from it), and only when a write is due re-read against the pool's tags right
// now, so a key added outside Terraform since the last refresh is kept. Reports false
// when the update must stop (an error diagnostic was added).
func (r *kubernetesClusterResource) writeInitialPoolTags(ctx context.Context, clusterID string, defaults tftags.Defaults,
	plan, state InitialNodePoolModel, reqPrivate tftags.PrivateReader, respPrivate tftags.PrivateWriter, diags *diag.Diagnostics,
) bool {
	prior := tftags.PriorOf(ctx, state.Tags, state.TagsAll, tftags.ScopedReader(reqPrivate, initialPoolTagsScope), diags)
	if _, changed := tftags.ForUpdate(ctx, defaults, plan.Tags, prior, diags); changed && !diags.HasError() {
		pool, err := r.findInitialNodePool(ctx, clusterID)
		if err != nil {
			diags.AddError("Failed to Read Current Tags", tftags.CurrentTagsReadFailed+err.Error())
			return false
		}
		if pool == nil || !pubid.Equal(pool.ID, state.ID.ValueString()) {
			diags.AddError(
				"Initial node pool no longer exists",
				"The cluster's initial node pool was deleted or replaced outside Terraform, so its tags cannot "+
					"be updated. Replace the cluster (terraform apply -replace=<this resource>).",
			)
			return false
		}
		want, stillChanged := tftags.ForUpdate(ctx, defaults, plan.Tags, prior.WithCurrent(poolCustomerTags(pool)), diags)
		if diags.HasError() {
			return false
		}
		if stillChanged {
			if err := r.patchPoolTags(ctx, clusterID, pool.ID, want); err != nil {
				diags.AddError("Failed to update the initial node pool's tags", err.Error())
				return false
			}
		}
	}
	if diags.HasError() {
		return false
	}
	tftags.RecordDefaults(ctx, tftags.ScopedWriter(respPrivate, initialPoolTagsScope), defaults, diags)
	return true
}

// tagsToWrite returns the tag map an update must send, or nil when the platform's
// set needs no change. Judged first against state (the plan was made from it); only
// when that says a write is due does it read the tags the cluster holds right now
// and recompute against them, so a key added outside Terraform since the last
// refresh is kept — and an update that is not about tags costs no extra read.
func (r *kubernetesClusterResource) tagsToWrite(ctx context.Context, id string, defaults tftags.Defaults,
	plan, state KubernetesClusterModel, private tftags.PrivateReader, diags *diag.Diagnostics,
) (*map[string]string, error) {
	prior := tftags.PriorOf(ctx, state.Tags, state.TagsAll, private, diags)
	if _, changed := tftags.ForUpdate(ctx, defaults, plan.Tags, prior, diags); !changed {
		return nil, nil
	}
	current, err := r.currentTags(ctx, id)
	if err != nil {
		return nil, err
	}
	tags, changed := tftags.ForUpdate(ctx, defaults, plan.Tags, prior.WithCurrent(current), diags)
	if !changed {
		return nil, nil
	}
	return &tags, nil
}

// currentTags reads the tags the cluster holds right now. An update derives the
// keys it keeps — the ones this configuration does not manage — from this rather
// than from state, which is only as fresh as the last refresh.
func (r *kubernetesClusterResource) currentTags(ctx context.Context, id string) (map[string]string, error) {
	c, err := r.getCluster(ctx, id)
	if err != nil {
		return nil, err
	}
	return customerTags(c), nil
}
