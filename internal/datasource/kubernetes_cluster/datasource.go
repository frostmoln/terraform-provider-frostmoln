// Package kubernetes_cluster implements the frostmoln_kubernetes_cluster
// Terraform data source: the name→id resolver for a managed Kubernetes
// cluster — the lookup a configuration needs to reach a cluster that
// Terraform did not create (a portal or `fm`-provisioned cluster is today
// referenced only by a hardcoded UUID or plumbed through
// terraform_remote_state). Beside its sibling kubeconfig data source it is
// the one-stop cross-provider composition enabler: this data source serves
// the cluster's non-secret attributes (endpoint, CIDRs, tier, region) that
// other references compose from, while the credential material is fetched
// separately by `frostmoln_kubernetes_cluster_kubeconfig`.
//
// The shape is the house resolver shape (`frostmoln_vpc`,
// `frostmoln_postgres_instance`): `id` or `name`, exactly one; an id goes
// straight to the cluster read, a name lists the tenant's clusters and
// matches client-side. A name resolver must do the match client-side: the
// cluster list service has no name filter (its list options are
// status/limit/offset).
//
// API-key callers need the `kubernetes:read` scope (ADR-0096) for GETs; a
// refused read fails loudly rather than rendering an empty row. (The
// `kubernetes` entitlement gate was retired 2026-09-23 — managed K8s is GA.)
//
// Deletes are SOFT: a completed delete sets status "deleted" and RETAINS the
// row, so GET on a deleted cluster answers 200 with status "deleted" forever —
// never 404. Both paths here treat a "deleted" row as absent, exactly as the
// frostmoln_kubernetes_cluster resource's Read does: a resolver that rendered
// a deleted row would resolve a phantom.
//
// Ambiguity and absence both fail the read. A resolver that silently picked
// the first of several same-named clusters would feed the wrong cluster's
// endpoint into a configuration — exactly the mistake this data source exists
// to prevent — so more than one match is an error that names the colliding
// ids, and zero matches is an error, never an empty row. (The list pages: the
// repository applies a default limit of 50, so one unfiltered request can
// miss clusters — Read walks the pages until the name is found or the list is
// exhausted.)
package kubernetes_cluster

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
)

var _ datasource.DataSource = &kubernetesClusterDataSource{}

// statusDeleted is the soft-delete marker: the row is retained and answers
// 200 forever, so absence is a status value, not a 404.
const statusDeleted = "deleted"

// NewDataSource returns a new frostmoln_kubernetes_cluster data source factory.
func NewDataSource() datasource.DataSource {
	return &kubernetesClusterDataSource{}
}

type kubernetesClusterDataSource struct {
	client *client.Client
}

// kubernetesClusterModel is the Terraform state model. Attribute names mirror
// the frostmoln_kubernetes_cluster resource's (version from the wire's
// kubernetesVersion, control_plane_tier, …), so a data source read can be fed
// straight into a reference without translating names.
type kubernetesClusterModel struct {
	ID               types.String `tfsdk:"id"`
	Name             types.String `tfsdk:"name"`
	Status           types.String `tfsdk:"status"`
	Version          types.String `tfsdk:"version"`
	ControlPlaneTier types.String `tfsdk:"control_plane_tier"`
	HAEnabled        types.Bool   `tfsdk:"ha_enabled"`
	Region           types.String `tfsdk:"region"`
	VPCID            types.String `tfsdk:"vpc_id"`
	SubnetID         types.String `tfsdk:"subnet_id"`
	PodCIDR          types.String `tfsdk:"pod_cidr"`
	ServiceCIDR      types.String `tfsdk:"service_cidr"`
	Endpoint         types.String `tfsdk:"endpoint"`
	LoadBalancerID   types.String `tfsdk:"load_balancer_id"`
	PublicIP         types.String `tfsdk:"public_ip"`
	CACertHash       types.String `tfsdk:"ca_cert_hash"`
	CreatedAt        types.String `tfsdk:"created_at"`
	UpdatedAt        types.String `tfsdk:"updated_at"`
	TenantID         types.String `tfsdk:"tenant_id"`
}

// apiCluster is the API representation of one managed Kubernetes cluster —
// the same wire shape the frostmoln_kubernetes_cluster resource maps
// (kubernetes service domain.ManagedCluster).
//
// The addons array is deliberately NOT decoded here: one collection, one
// owner — the addon selection (and the per-addon pins behind it) is owned by
// the frostmoln_kubernetes_cluster_addons data source, so this read surface
// does not mirror it.
type apiCluster struct {
	ID                string `json:"id"`
	Name              string `json:"name"`
	TenantID          string `json:"tenantId,omitempty"`
	Status            string `json:"status"`
	KubernetesVersion string `json:"kubernetesVersion"`
	ControlPlaneTier  string `json:"controlPlaneTier"`
	HAEnabled         bool   `json:"haEnabled"`
	Region            string `json:"region"`
	VPCID             string `json:"vpcId"`
	SubnetID          string `json:"subnetId"`
	PodCIDR           string `json:"podCidr,omitempty"`
	ServiceCIDR       string `json:"serviceCidr,omitempty"`
	Endpoint          string `json:"endpoint,omitempty"`
	LoadBalancerID    string `json:"loadBalancerId,omitempty"`
	PublicIP          string `json:"publicIp,omitempty"`
	// CACertHash is a non-secret reference to the cluster CA: the server only
	// ever serves the hash, never the CA itself (the kubeconfig and its
	// private key material live in Vault, ADR-0020). A hash is safe to render
	// in plan output and state — the secret material is not on this read.
	CACertHash string `json:"caCertHash,omitempty"`
	CreatedAt  string `json:"createdAt"`
	UpdatedAt  string `json:"updatedAt,omitempty"`
}

// apiClusterList is the API response for listing a tenant's clusters. There
// is no name filter on this surface — the match is client-side.
type apiClusterList struct {
	Clusters   []apiCluster `json:"clusters"`
	TotalCount int          `json:"totalCount"`
}

func (d *kubernetesClusterDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_kubernetes_cluster"
}

func (d *kubernetesClusterDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Look up a managed Kubernetes cluster by ID or name. Exactly one of " +
			"`id` or `name` must be specified.\n\n" +

			"This is how a configuration reaches a cluster that Terraform did not create: a " +
			"cluster provisioned in the portal or with `fm` can today be referenced only by a " +
			"hardcoded UUID or plumbed through `terraform_remote_state` — both of which tie the " +
			"configuration to one deployment. Look it up by name instead and the reference " +
			"survives re-provisioning elsewhere; the endpoint and CIDRs feed other references, " +
			"while the credential material is fetched separately by " +
			"`frostmoln_kubernetes_cluster_kubeconfig`.\n\n" +

			"A name lookup that matches NOTHING fails the read, and so does one that matches " +
			"MORE THAN ONE cluster: the data source refuses to pick for you (the diagnostic " +
			"names the colliding ids). Deletes are soft — a deleted cluster's row answers 200 " +
			"with status `deleted` forever — and a deleted cluster is reported as ABSENT, on " +
			"the id path and in the name search, so a gone cluster can never resolve.\n\n" +

			"API-key callers need the `kubernetes:read` scope; a refused read fails on " +
			"every path rather than answering with an empty row.\n\n" +

			"The addons collection is deliberately NOT part of this schema: one collection, " +
			"one owner — the addon selection (and the per-addon applied versions) is read " +
			"with `frostmoln_kubernetes_cluster_addons`.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Description: "The unique identifier of the Kubernetes cluster. Exactly one of " +
					"id or name must be specified.",
				Optional: true,
				Validators: []validator.String{
					validClusterIDValidator{},
				},
			},
			"name": schema.StringAttribute{
				Description: "The name of the Kubernetes cluster. Exactly one of id or name " +
					"must be specified.",
				Optional: true,
			},
			"status": schema.StringAttribute{
				Description: "The current status of the cluster. On this read surface the " +
					"status is always a LIVE cluster's status: a soft-deleted cluster is " +
					"reported as absent rather than rendered, so `deleted` never lands in state.",
				Computed: true,
			},
			"version": schema.StringAttribute{
				Description: "The Kubernetes version (the wire's kubernetesVersion).",
				Computed:    true,
			},
			"control_plane_tier": schema.StringAttribute{
				Description: "The named control-plane plan (`development`, `production`, …) " +
					"that sizes the cluster's dedicated control plane — its HA shape, not a " +
					"customer tenancy.",
				Computed: true,
			},
			"ha_enabled": schema.BoolAttribute{
				Description: "Whether the dedicated control plane is high-availability.",
				Computed:    true,
			},
			"region": schema.StringAttribute{
				Description: "The region the cluster lives in.",
				Computed:    true,
			},
			"vpc_id": schema.StringAttribute{
				Description: "The VPC the cluster's nodes run in.",
				Computed:    true,
			},
			"subnet_id": schema.StringAttribute{
				Description: "The subnet the cluster's nodes run in.",
				Computed:    true,
			},
			"pod_cidr": schema.StringAttribute{
				Description: "The cluster's per-cluster pod CIDR, allocated from the managed " +
					"pool. Null while the platform has not allocated one.",
				Computed: true,
			},
			"service_cidr": schema.StringAttribute{
				Description: "The cluster's per-cluster service CIDR, allocated OUTSIDE the " +
					"infra `10.96.0.0/12` range to avoid colliding with platform services. " +
					"Null while the platform has not allocated one.",
				Computed: true,
			},
			"endpoint": schema.StringAttribute{
				Description: "The load-balancer VIP fronting the API server — the address " +
					"kube clients reach the cluster on. Null while provisioning has not " +
					"produced one.",
				Computed: true,
			},
			"load_balancer_id": schema.StringAttribute{
				Description: "The ID of the load balancer behind the API endpoint — the only " +
					"load balancer this surface reports.",
				Computed: true,
			},
			"public_ip": schema.StringAttribute{
				Description: "The public IP of the API endpoint, null when the endpoint is " +
					"not internet-reachable (an enclave cluster's private VIP has none).",
				Computed: true,
			},
			"ca_cert_hash": schema.StringAttribute{
				Description: "A NON-SECRET reference to the cluster CA (a hash). The server " +
					"only ever returns the hash, never the CA itself, so this attribute is not " +
					"secret and not sensitive. The actual credential material — the kubeconfig " +
					"with its client key — lives on `frostmoln_kubernetes_cluster_kubeconfig`, " +
					"not here.",
				Computed: true,
			},
			"created_at": schema.StringAttribute{
				Description: "The timestamp when the cluster was created.",
				Computed:    true,
			},
			"updated_at": schema.StringAttribute{
				Description: "The timestamp when the cluster was last updated, null when the " +
					"platform has not recorded one.",
				Computed: true,
			},
			"tenant_id": schema.StringAttribute{
				Description: "The tenant ID that owns this cluster.",
				Computed:    true,
			},
		},
	}
}

func (d *kubernetesClusterDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	c, ok := req.ProviderData.(*client.Client)
	if !ok {
		resp.Diagnostics.AddError(
			"Unexpected Data Source Configure Type",
			fmt.Sprintf("Expected *client.Client, got: %T", req.ProviderData),
		)
		return
	}
	d.client = c
}

func (d *kubernetesClusterDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var cfg kubernetesClusterModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}

	idSet := !cfg.ID.IsNull() && !cfg.ID.IsUnknown()
	nameSet := !cfg.Name.IsNull() && !cfg.Name.IsUnknown()

	if !idSet && !nameSet {
		resp.Diagnostics.AddError(
			"One of id or name must be specified",
			"Neither `id` nor `name` carries a value, so there is nothing to look up. "+
				"Specify exactly one of them.",
		)
		return
	}
	if idSet && nameSet {
		resp.Diagnostics.AddError(
			"Only one of id or name may be specified",
			fmt.Sprintf("Both `id` (%q) and `name` (%q) carry a value. Specify exactly one of "+
				"them.", cfg.ID.ValueString(), cfg.Name.ValueString()),
		)
		return
	}

	var cluster *apiCluster
	if idSet {
		found, diags := d.readByID(ctx, cfg.ID.ValueString())
		resp.Diagnostics.Append(diags...)
		if resp.Diagnostics.HasError() {
			return
		}
		cluster = found
	} else {
		found, diags := d.resolveByName(ctx, cfg.Name.ValueString())
		resp.Diagnostics.Append(diags...)
		if resp.Diagnostics.HasError() {
			return
		}
		cluster = found
	}

	setInstanceState(&cfg, cluster)
	resp.Diagnostics.Append(resp.State.Set(ctx, &cfg)...)
}

// readByID walks the id path: one cluster read, then identity and soft-delete
// checks. The identity guard refuses a 200 that does not carry the requested
// id — whatever answered is not the cluster read this provider builds its
// contract on. A row whose status is "deleted" is treated as absent: deletes
// are SOFT, and a deleted cluster's row answers 200 forever, so honoring it
// would resolve a phantom.
func (d *kubernetesClusterDataSource) readByID(ctx context.Context, id string) (*apiCluster, diag.Diagnostics) {
	var diags diag.Diagnostics

	pathStr, pathErr := clusterPath(d.client, id)
	if pathErr != nil {
		diags.AddError("Invalid Cluster ID", pathErr.Error())
		return nil, diags
	}

	apiResp, err := d.client.Get(ctx, pathStr, nil)
	if err != nil {
		if client.IsNotFound(err) {
			diags.AddError(
				"The Kubernetes cluster does not exist",
				fmt.Sprintf("The id %q answers 404 — there is no such Kubernetes cluster (or "+
					"none this caller can see), so there is nothing to resolve. Correct `id`, "+
					"or look the cluster up by `name` instead.\n\n%s", id, err.Error()),
			)
			return nil, diags
		}
		diags.AddError("Failed to Read Kubernetes Cluster", err.Error())
		return nil, diags
	}

	var cluster apiCluster
	if err := json.Unmarshal(apiResp.Body, &cluster); err != nil {
		diags.AddError("Failed to Parse Kubernetes Cluster Response", err.Error())
		return nil, diags
	}
	if cluster.ID != id {
		diags.AddError(
			"This cluster read did not identify the requested cluster",
			fmt.Sprintf("The response does not carry the id this path asks for (%q). Whatever "+
				"answered is not the cluster read this provider builds its contract on, so the "+
				"lookup refuses rather than render a row no service promised. The path was %q.",
				id, pathStr),
		)
		return nil, diags
	}
	// Deletes are soft: a completed delete sets status "deleted" and keeps the
	// row answering 200 forever (the frostmoln_kubernetes_cluster resource's
	// Read carries the same rule). A deleted cluster does not exist for
	// resolution purposes — say so instead of rendering the retained row.
	if cluster.Status == statusDeleted {
		diags.AddError(
			"The Kubernetes cluster does not exist",
			fmt.Sprintf("The id %q resolves to a cluster whose status is %q: it has been "+
				"deleted, and the platform retains the row without keeping a live cluster. "+
				"There is nothing to resolve. Correct `id`, or look the cluster up by "+
				"`name` instead.", id, cluster.Status),
		)
		return nil, diags
	}
	return &cluster, diags
}

// resolveByName walks the list path. The list has no name filter and pages at
// a repository default limit of 50, so Read walks the pages until the name is
// found or the list is exhausted; more than one match is an error that names
// the colliding ids. A soft-deleted row can never match.
func (d *kubernetesClusterDataSource) resolveByName(ctx context.Context, name string) (*apiCluster, diag.Diagnostics) {
	var diags diag.Diagnostics

	var matches []apiCluster
	const pageSize = 100
	const maxPages = 20
	// searchExhausted records the loop hitting the page cap with a FULL final
	// page: the tenant may carry clusters beyond the search window, so a
	// zero-match verdict then is "not in the window we searched", never the
	// plain absence the not-found arm asserts.
	searchExhausted := false
	for offset, page := 0, 0; page < maxPages; offset, page = offset+pageSize, page+1 {
		q := url.Values{}
		q.Set("limit", strconv.Itoa(pageSize))
		q.Set("offset", strconv.Itoa(offset))

		apiResp, err := d.client.Get(ctx, d.client.TenantPath("/kubernetes-clusters"), q)
		if err != nil {
			diags.AddError("Failed to List Kubernetes Clusters", err.Error())
			return nil, diags
		}

		var list apiClusterList
		if err := json.Unmarshal(apiResp.Body, &list); err != nil {
			diags.AddError("Failed to Parse Kubernetes Clusters Response", err.Error())
			return nil, diags
		}

		for i := range list.Clusters {
			// A soft-deleted row can never count as a match here — the same
			// rule the id path enforces: the deleted row is retained forever,
			// so honoring it would resolve a cluster that no longer exists.
			if list.Clusters[i].Name == name && list.Clusters[i].Status != statusDeleted {
				matches = append(matches, list.Clusters[i])
			}
		}
		if len(list.Clusters) < pageSize {
			break
		}
		if page == maxPages-1 {
			searchExhausted = true
		}
	}

	switch len(matches) {
	case 0:
		if searchExhausted {
			diags.AddError(
				fmt.Sprintf("No Kubernetes cluster named %q in the searched window", name),
				fmt.Sprintf("The tenant's cluster list did not terminate within the search "+
					"window (%d pages of %d), so the lookup cannot claim the name is absent — "+
					"there may be clusters beyond it. Narrow with `id`, or ask the tenant to "+
					"rename clusters to a unique name.", maxPages, pageSize),
			)
			return nil, diags
		}
		diags.AddError(
			"No Kubernetes cluster with this name",
			fmt.Sprintf("No cluster named %q resolves to a live Kubernetes cluster in "+
				"this tenant. Check the spelling of `name`; a cluster that has been "+
				"deleted is reported as absent and cannot be resolved.", name),
		)
		return nil, diags
	case 1:
		return &matches[0], diags
	default:
		ids := make([]string, 0, len(matches))
		for i := range matches {
			ids = append(ids, matches[i].ID)
		}
		diags.AddError(
			fmt.Sprintf("%d Kubernetes clusters share the name %q", len(matches), name),
			fmt.Sprintf("The tenant's clusters carry more than one live cluster with "+
				"this name (%s). The data source refuses to pick one for you: every match "+
				"resolves to a DIFFERENT cluster, and a silent pick would feed the wrong "+
				"cluster's endpoint into your configuration. Disambiguate with `id`, or rename "+
				"the clusters.", strings.Join(ids, ", ")),
		)
		return nil, diags
	}
}

// clusterPath builds one cluster read path behind the same guard the id
// carries at plan time — Read must never trust that a validated configuration
// is the only thing that reaches it.
func clusterPath(c *client.Client, id string) (string, error) {
	if err := validClusterID(id); err != nil {
		return "", err
	}
	return c.TenantPath(fmt.Sprintf("/kubernetes-clusters/%s", id)), nil
}

// validClusterID refuses an id that cannot safely be one path segment. The
// same guard shape the postgres_instance data source carries: a "." or ".."
// id does not stay one path segment (the client joins with path.Join, which
// CLEANS), and the cleaned URL addresses a DIFFERENT resource.
func validClusterID(id string) error {
	if id == "" {
		return fmt.Errorf("a cluster ID is required")
	}
	if id == "." || id == ".." || strings.ContainsAny(id, `/\?#%`) {
		return fmt.Errorf("invalid cluster ID %q", id)
	}
	return nil
}

// validClusterIDValidator carries validClusterID into plan time, so a
// configuration with an unusable id fails before any request is built.
type validClusterIDValidator struct{}

func (v validClusterIDValidator) Description(_ context.Context) string {
	return "value must be a usable Kubernetes cluster ID: non-empty, and a single URL path segment"
}

func (v validClusterIDValidator) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}

func (v validClusterIDValidator) ValidateString(_ context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if req.ConfigValue.IsUnknown() || req.ConfigValue.IsNull() {
		return
	}
	if err := validClusterID(req.ConfigValue.ValueString()); err != nil {
		resp.Diagnostics.AddAttributeError(
			req.Path,
			"Invalid Cluster ID",
			fmt.Sprintf("%s: %s", err.Error(), "a Kubernetes cluster ID must be non-empty and "+
				"must not contain a '/', a backslash, '?', '#' or '%', so it can only ever "+
				"address the one cluster named."),
		)
	}
}

// setInstanceState maps one verified cluster onto the model. Absent-optionals
// are null, never "" or 0 — a check block must be able to tell "no value"
// apart from "empty value".
func setInstanceState(state *kubernetesClusterModel, cluster *apiCluster) {
	state.ID = types.StringValue(cluster.ID)
	state.Name = types.StringValue(cluster.Name)
	state.Status = types.StringValue(cluster.Status)
	state.Version = stringFromWire(cluster.KubernetesVersion)
	state.ControlPlaneTier = stringFromWire(cluster.ControlPlaneTier)
	state.HAEnabled = types.BoolValue(cluster.HAEnabled)
	state.Region = stringFromWire(cluster.Region)
	state.VPCID = types.StringValue(cluster.VPCID)
	state.SubnetID = types.StringValue(cluster.SubnetID)
	state.PodCIDR = stringFromWire(cluster.PodCIDR)
	state.ServiceCIDR = stringFromWire(cluster.ServiceCIDR)
	state.Endpoint = stringFromWire(cluster.Endpoint)
	state.LoadBalancerID = stringFromWire(cluster.LoadBalancerID)
	state.PublicIP = stringFromWire(cluster.PublicIP)
	state.CACertHash = stringFromWire(cluster.CACertHash)
	state.CreatedAt = types.StringValue(cluster.CreatedAt)
	state.UpdatedAt = stringFromWire(cluster.UpdatedAt)
	state.TenantID = stringFromWire(cluster.TenantID)
}

// stringFromWire maps an empty optional string to null, not "".
func stringFromWire(v string) types.String {
	if v == "" {
		return types.StringNull()
	}
	return types.StringValue(v)
}
