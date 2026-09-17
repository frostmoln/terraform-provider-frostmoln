// Package kubernetes_cluster_kubeconfig implements the
// frostmoln_kubernetes_cluster_kubeconfig Terraform data source: it resolves
// a managed Kubernetes cluster with the same id-or-name belt as
// `frostmoln_kubernetes_cluster` and then fetches the cluster's kubeconfig
// from GET /kubernetes-clusters/{id}/kubeconfig.
//
// The kubeconfig is Vault-served credential material (ADR-0020): the client
// key and certificate live inside the YAML, and the server never persists
// them in its own database. The server refuses the read unless the cluster is
// serviceable — a cluster that is still provisioning cannot hand out a
// working credential, and an empty one betrays exactly that.
//
// The kubeconfig attribute is the FIRST Sensitive attribute on any data
// source in this provider: it appears redacted in CLI output, but it still
// lands in state, so the state must be protected like a kubeconfig file would
// be. The non-secret pairing (the CA cert hash) lives on
// `frostmoln_kubernetes_cluster`.
//
// The whole /kubernetes-clusters surface is gated twice: the service refuses
// every operation unless the signed auth context carries the `kubernetes`
// entitlement (ADR-0038), and API-key callers additionally need the
// `kubernetes:read` scope (ADR-0096) for GETs. An unentitled tenant is
// refused on every path of this data source.
//
// Deletes are SOFT: a completed delete sets status "deleted" and RETAINS the
// row, so both resolution paths here treat a "deleted" row as absent, exactly
// as the frostmoln_kubernetes_cluster resource's Read and the
// `frostmoln_kubernetes_cluster` data source do.
package kubernetes_cluster_kubeconfig

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
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

var _ datasource.DataSource = &kubernetesClusterKubeconfigDataSource{}

// statusDeleted is the soft-delete marker: the row is retained and answers
// 200 forever, so absence is a status value, not a 404.
const statusDeleted = "deleted"

// NewDataSource returns a new frostmoln_kubernetes_cluster_kubeconfig data
// source factory.
func NewDataSource() datasource.DataSource {
	return &kubernetesClusterKubeconfigDataSource{}
}

type kubernetesClusterKubeconfigDataSource struct {
	client *client.Client
}

// kubernetesClusterKubeconfigModel is the Terraform state model. `id` is the
// RESOLVED cluster id — mirroring the frostmoln_kubernetes_cluster data
// source, so a reference chain through one can continue through the other —
// and `name` is the cluster's verified name from the read surface, not a
// restatement of the configuration.
type kubernetesClusterKubeconfigModel struct {
	ID         types.String `tfsdk:"id"`
	Name       types.String `tfsdk:"name"`
	Endpoint   types.String `tfsdk:"endpoint"`
	Kubeconfig types.String `tfsdk:"kubeconfig"`
}

// apiCluster is the API representation of one managed Kubernetes cluster —
// the read surface this data source resolves through (kubernetes service
// domain.ManagedCluster). Only id/name/status matter on this path, but the
// shape is the same wire shape the cluster resources map.
type apiCluster struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Status string `json:"status"`
	// Endpoint rides the cluster row (the wire carries it, omit while the
	// platform has no address yet). It is NOT exported here — it feeds only
	// the cross-check against the kubeconfig response's own endpoint claim.
	Endpoint string `json:"endpoint,omitempty"`
}

// apiClusterList is the API response for listing a tenant's clusters. There
// is no name filter on this surface — the match is client-side.
type apiClusterList struct {
	Clusters   []apiCluster `json:"clusters"`
	TotalCount int          `json:"totalCount"`
}

// apiKubeconfig is the API response from the kubeconfig endpoint. The
// credential material is sourced from Vault at request time (ADR-0020); the
// service never persists private key material in its own database.
type apiKubeconfig struct {
	Endpoint   string `json:"endpoint"`
	Kubeconfig string `json:"kubeconfig"`
}

func (d *kubernetesClusterKubeconfigDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_kubernetes_cluster_kubeconfig"
}

func (d *kubernetesClusterKubeconfigDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Fetch the kubeconfig of a managed Kubernetes cluster, resolved by ID " +
			"or name. Exactly one of `id` or `name` must be specified.\n\n" +

			"This is the credential half of reaching a cluster that Terraform did not create: " +
			"a cluster provisioned in the portal or with `fm` can today be referenced only by a " +
			"hardcoded UUID, so look it up by name. The non-secret attributes of the same " +
			"cluster (endpoint, CIDRs, tier, the CA cert hash) live on " +
			"`frostmoln_kubernetes_cluster`.\n\n" +

			"A name lookup that matches nothing — or more than one cluster — fails the read, " +
			"and a soft-deleted cluster (the deleted row answers 200 with status `deleted` " +
			"forever) is reported as ABSENT on both paths. The service refuses the kubeconfig " +
			"read until the cluster is serviceable: an EMPTY kubeconfig on a 200 fails the " +
			"read instead of rendering empty credential material — the cluster may simply not " +
			"be serviceable yet.\n\n" +

			"The surface is gated twice: reads are refused for any tenant without the " +
			"`kubernetes` entitlement, and API-key callers additionally need the " +
			"`kubernetes:read` scope.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Description: "The unique identifier of the Kubernetes cluster. Exactly one of " +
					"id or name must be specified. In state this is the RESOLVED cluster's id.",
				Optional: true,
				Validators: []validator.String{
					validClusterIDValidator{},
				},
			},
			"name": schema.StringAttribute{
				Description: "The name of the Kubernetes cluster. Exactly one of id or name " +
					"must be specified. In state this is the resolved cluster's verified name.",
				Optional: true,
			},
			"endpoint": schema.StringAttribute{
				Description: "The Kubernetes API endpoint the kubeconfig is built against — " +
					"carried beside the blob so a configuration can compose a provider's host " +
					"input without re-deriving it from the YAML.",
				Computed: true,
			},
			// kubeconfig is SENSITIVE — and deliberately the FIRST Sensitive
			// attribute on any data source in this provider. The blob is
			// Vault-served credential material (the client key and certificate
			// live inside the YAML): it renders redacted in CLI output, yet it
			// still lands in state, so state security must be treated like the
			// security of a kubeconfig file. It is never secret-adjacent on the
			// non-secret cluster data source — that surface carries only the CA
			// cert hash.
			"kubeconfig": schema.StringAttribute{
				Description: "The raw kubeconfig YAML for the cluster, verbatim as the " +
					"platform serves it. Feed it into the `kubernetes` and `helm` providers' " +
					"kubeconfig inputs — the universal composition contract — without parsing " +
					"the client key/certificate out of the YAML. It is SECRET material: it " +
					"carries the cluster client's private key, it appears redacted in CLI " +
					"output, and it still lands in Terraform state — protect that state " +
					"accordingly. The non-secret pairing (the CA cert hash) lives on " +
					"`frostmoln_kubernetes_cluster`.",
				Computed:  true,
				Sensitive: true,
			},
		},
	}
}

func (d *kubernetesClusterKubeconfigDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
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

func (d *kubernetesClusterKubeconfigDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var cfg kubernetesClusterKubeconfigModel
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
		found, diags := d.resolveByID(ctx, cfg.ID.ValueString())
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

	kc, diags := d.fetchKubeconfig(ctx, cluster.ID)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	// TWO ENDPOINT CLAIMS, ONE CLUSTER. The cluster read and the kubeconfig
	// read each carry an endpoint, and this data source exports BOTH halves —
	// the address for the provider's `host`, the blob for its kubeconfig — so
	// a buggy or compromised answer that pairs one cluster's credential with a
	// DIFFERENT cluster's API address would make this the unwitting relay.
	// When both claims are non-empty they must agree; a mismatch is refused,
	// never rendered. (An empty side stays possible in non-serviceable states
	// where the platform genuinely has no address yet.)
	if cluster.Endpoint != "" && kc.Endpoint != "" && cluster.Endpoint != kc.Endpoint {
		resp.Diagnostics.AddError(
			"The kubeconfig does not belong to this cluster",
			fmt.Sprintf("The cluster %q reports endpoint %q, but the kubeconfig read answers "+
				"with endpoint %q. The two must name the SAME cluster's API address before "+
				"this provider exports them as a pair — refusing, because a pair whose "+
				"halves disagree points a configuration's credentials at the wrong cluster.",
				cluster.ID, cluster.Endpoint, kc.Endpoint),
		)
		return
	}

	cfg.ID = types.StringValue(cluster.ID)
	cfg.Name = types.StringValue(cluster.Name)
	cfg.Endpoint = stringFromWire(kc.Endpoint)
	cfg.Kubeconfig = types.StringValue(kc.Kubeconfig)
	resp.Diagnostics.Append(resp.State.Set(ctx, &cfg)...)
}

// resolveByID walks the id path: one cluster read, then identity and
// soft-delete checks — the same guards the frostmoln_kubernetes_cluster data
// source enforces, because a kubeconfig for the WRONG cluster (or for a
// deleted one) is not a credential this provider may hand out.
func (d *kubernetesClusterKubeconfigDataSource) resolveByID(ctx context.Context, id string) (*apiCluster, diag.Diagnostics) {
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
					"none this caller can see), so there is no kubeconfig to fetch. Correct "+
					"`id`, or look the cluster up by `name` instead.\n\n%s", id, err.Error()),
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
				"lookup refuses rather than hand out a kubeconfig no service promised for this "+
				"cluster. The path was %q.", id, pathStr),
		)
		return nil, diags
	}
	// Deletes are soft: the row answers 200 with status "deleted" forever —
	// treat it as absent rather than fetch credentials for a phantom.
	if cluster.Status == statusDeleted {
		diags.AddError(
			"The Kubernetes cluster does not exist",
			fmt.Sprintf("The id %q resolves to a cluster whose status is %q: it has been "+
				"deleted, and the platform retains the row without keeping a live cluster. "+
				"There is no kubeconfig to fetch. Correct `id`, or look the cluster up by "+
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
func (d *kubernetesClusterKubeconfigDataSource) resolveByName(ctx context.Context, name string) (*apiCluster, diag.Diagnostics) {
	var diags diag.Diagnostics

	var matches []apiCluster
	const pageSize = 100
	const maxPages = 20
	// searchExhausted records the loop hitting the page cap with a FULL final
	// page: the tenant may carry clusters beyond the search window, so a
	// zero-match verdict then is "not in the window we searched", never the
	// plain absence the not-found arm asserts (and for a kubeconfig lookup the
	// false "absent" reads as "no credential for this cluster", which is the
	// worst place to mislead).
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
			// so honoring it would hand out the credentials of a cluster that
			// no longer exists.
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
					"there may be clusters beyond it. Narrow with `id`.", maxPages, pageSize),
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
				"resolves to a DIFFERENT cluster, and a silent pick would hand out the "+
				"WRONG cluster's credentials. Disambiguate with `id`, or rename the "+
				"clusters.", strings.Join(ids, ", ")),
		)
		return nil, diags
	}
}

// fetchKubeconfig reads the cluster's kubeconfig. The cluster was verified
// live moments ago, so a failure here — including a 404 on the sub-resource —
// is NOT a verdict that the cluster is gone; it never takes the "does not
// exist" arm. An empty kubeconfig on a 200 is an error, never rendered
// silently: empty credential material would pass through to a provider block
// as if it were a working credential.
func (d *kubernetesClusterKubeconfigDataSource) fetchKubeconfig(ctx context.Context, clusterID string) (*apiKubeconfig, diag.Diagnostics) {
	var diags diag.Diagnostics

	pathStr, pathErr := clusterPath(d.client, clusterID)
	if pathErr != nil {
		diags.AddError("Invalid Cluster ID", pathErr.Error())
		return nil, diags
	}

	apiResp, err := d.client.Get(ctx, pathStr+"/kubeconfig", nil)
	if err != nil {
		// A 403 here is the ADR-0096 scope gate in the NEGATIVE ("this cannot
		// succeed, ever"), not a transient refusal — retrying it burns stuck
		// plans, so it gets its own sentence while staying in the generic arm
		// (it is NOT a verdict that the cluster is gone).
		if apiErr := (*client.APIError)(nil); errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusForbidden {
			diags.AddError(
				"Failed to Read the Cluster Kubeconfig",
				fmt.Sprintf("%s\n\nThis is a refusal to AUTHORIZE the read, not a fault: "+
					"the kubeconfig surface checks the caller's kubernetes API-key scope, "+
					"and an API key without the kubernetes:read scope refuses kubeconfig "+
					"reads permanently. Re-run with a key carrying that scope.", err.Error()),
			)
			return nil, diags
		}
		diags.AddError("Failed to Read the Cluster Kubeconfig", err.Error())
		return nil, diags
	}

	var kc apiKubeconfig
	if err := json.Unmarshal(apiResp.Body, &kc); err != nil {
		diags.AddError("Failed to Parse the Cluster Kubeconfig Response", err.Error())
		return nil, diags
	}
	if kc.Kubeconfig == "" {
		diags.AddError(
			"The kubeconfig came back empty",
			fmt.Sprintf("The cluster %q answered the kubeconfig read with HTTP 200 but carries "+
				"no kubeconfig material. An empty credential blob is never rendered silently, "+
				"because it would reach a provider block as if it were a working credential. "+
				"The cluster may not be serviceable yet — the server refuses the kubeconfig of "+
				"a cluster that cannot serve it, and an empty answer reports the same story. "+
				"Wait for the cluster to become serviceable and read again.", clusterID),
		)
		return nil, diags
	}
	return &kc, diags
}

// clusterPath builds one cluster-scoped path behind the same guard the id
// carries at plan time — Read must never trust that a validated configuration
// is the only thing that reaches it.
func clusterPath(c *client.Client, id string) (string, error) {
	if err := validClusterID(id); err != nil {
		return "", err
	}
	return c.TenantPath(fmt.Sprintf("/kubernetes-clusters/%s", id)), nil
}

// validClusterID refuses an id that cannot safely be one path segment. The
// same guard shape the sibling data sources carry: a "." or ".." id does not
// stay one path segment (the client joins with path.Join, which CLEANS), and
// the cleaned URL addresses a DIFFERENT resource.
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

// stringFromWire maps an empty optional string to null, not "".
func stringFromWire(v string) types.String {
	if v == "" {
		return types.StringNull()
	}
	return types.StringValue(v)
}
