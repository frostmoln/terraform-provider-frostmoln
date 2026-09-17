// Package load_balancer implements the frostmoln_load_balancer Terraform data
// source: the identity resolver for a load balancer — the lookup a
// configuration needs to reach a load balancer that Terraform did not create
// (a portal- or `fm`-provisioned one, or one whose UUID is better referenced
// than hardcoded).
//
// The shape is the house resolver shape (`frostmoln_postgres_instance`): `id`
// or `name`, exactly one. An id goes straight to the load-balancer read. A
// name lists the tenant's load balancers using the list service's EXACT
// `name` filter, sent server-side, and re-verifies the match client-side —
// the filter is the fast path, the re-verify is the contract. When the
// optional `vpc_id` input carries a value it rides the same request as the
// `vpcId` filter and is re-checked identically (the `frostmoln_subnet`
// data-source pattern: filter input on the lookup, observed value in the
// result).
//
// Two wire facts this package pins. The list envelope counts on `total` —
// {"loadBalancers":[…],"total":N,"limit":N,…} — NOT on `totalCount`: the load
// balancer list is the one service in the platform whose count key deviates
// (every sibling list answers `totalCount`). And the network service hides
// offer-internal load balancers from the customer REST plane: the
// guardManagedLB middleware 404s any id route that resolves to a load
// balancer carrying the reserved nlmeta tag, and filters them from the list.
// So a 404 on the id path can be a managed offer's load balancer rather than
// empty space — the not-found diagnostic says so.
//
// The schema deliberately carries NO `listeners` or `pools` collections: the
// child resources `frostmoln_lb_listener`, `frostmoln_lb_pool`,
// `frostmoln_lb_member` and `frostmoln_lb_health_monitor` own those
// collections (one collection, one owner — the Surface Contract doctrine), so
// this resolver's job is the id, and a listener or pool is referenced through
// the resource that manages it.
package load_balancer

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

var _ datasource.DataSource = &loadBalancerDataSource{}

// NewDataSource returns a new frostmoln_load_balancer data source factory.
func NewDataSource() datasource.DataSource {
	return &loadBalancerDataSource{}
}

type loadBalancerDataSource struct {
	client *client.Client
}

// loadBalancerModel is the Terraform state model. Attribute names mirror the
// frostmoln_load_balancer resource's (vip_address, public_ip_id, …), so a data
// source read can be fed straight into a reference without translating names.
type loadBalancerModel struct {
	ID                 types.String `tfsdk:"id"`
	Name               types.String `tfsdk:"name"`
	Description        types.String `tfsdk:"description"`
	VPCID              types.String `tfsdk:"vpc_id"`
	SubnetID           types.String `tfsdk:"subnet_id"`
	VIPAddress         types.String `tfsdk:"vip_address"`
	VIPPortID          types.String `tfsdk:"vip_port_id"`
	Scheme             types.String `tfsdk:"scheme"`
	PublicIPID         types.String `tfsdk:"public_ip_id"`
	PublicIPAddress    types.String `tfsdk:"public_ip_address"`
	Type               types.String `tfsdk:"type"`
	FlavorID           types.String `tfsdk:"flavor_id"`
	Status             types.String `tfsdk:"status"`
	ProvisioningStatus types.String `tfsdk:"provisioning_status"`
	OperatingStatus    types.String `tfsdk:"operating_status"`
	CreatedAt          types.String `tfsdk:"created_at"`
	UpdatedAt          types.String `tfsdk:"updated_at"`
	TenantID           types.String `tfsdk:"tenant_id"`
}

// apiLoadBalancer is the API representation of one load balancer — the wire
// shape the LOAD-BALANCERS surface serves (the domain's LoadBalancer minus the
// children): listeners and pools stay on the wire but are deliberately NOT
// carried here, for the same one-collection-one-owner reason the schema omits
// them; the child resources read and manage them.
type apiLoadBalancer struct {
	ID                 string `json:"id"`
	Name               string `json:"name"`
	Description        string `json:"description,omitempty"`
	VPCID              string `json:"vpcId"`
	SubnetID           string `json:"subnetId"`
	VIPAddress         string `json:"vipAddress,omitempty"`
	VIPPortID          string `json:"vipPortId,omitempty"`
	Scheme             string `json:"scheme"`
	PublicIPID         string `json:"publicIpId,omitempty"`
	PublicIPAddress    string `json:"publicIpAddress,omitempty"`
	Type               string `json:"type,omitempty"`
	FlavorID           string `json:"flavorId,omitempty"`
	Status             string `json:"status"`
	ProvisioningStatus string `json:"provisioningStatus"`
	OperatingStatus    string `json:"operatingStatus"`
	CreatedAt          string `json:"createdAt"`
	UpdatedAt          string `json:"updatedAt"`
	TenantID           string `json:"tenantId"`
}

// apiLoadBalancerList is the list envelope. Its count key is `total` — NOT
// `totalCount`, which every other service in the platform answers. Reading a
// count from a key that is not there silently yields 0, so the key is spelled
// here exactly as the handler emits it and pinned by TestListEnvelopeCountsOnTotalNotTotalCount.
type apiLoadBalancerList struct {
	LoadBalancers []apiLoadBalancer `json:"loadBalancers"`
	Total         int               `json:"total"`
	Limit         int               `json:"limit"`
	Marker        string            `json:"marker,omitempty"`
	NextMarker    string            `json:"nextMarker,omitempty"`
}

func (d *loadBalancerDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_load_balancer"
}

func (d *loadBalancerDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Look up a load balancer by ID or name. Exactly one of `id` or `name` " +
			"must be specified.\n\n" +

			"This is how a configuration reaches a load balancer that Terraform did not create: " +
			"a load balancer provisioned in the portal or with `fm` can today be referenced only " +
			"by a hardcoded UUID or plumbed through `terraform_remote_state`. Look it up by name " +
			"instead and the reference survives re-provisioning elsewhere. The optional `vpc_id` " +
			"narrows a name lookup (the `vpcId` filter rides the request) and is echoed back " +
			"from the read.\n\n" +

			"A name lookup that matches NOTHING fails the read, and so does one that matches " +
			"MORE THAN ONE load balancer: the data source refuses to pick for you (the diagnostic " +
			"names the colliding ids). There is no `listeners` or `pools` attribute here: those " +
			"collections belong to the child resources `frostmoln_lb_listener`, " +
			"`frostmoln_lb_pool`, `frostmoln_lb_member` and `frostmoln_lb_health_monitor`, so this " +
			"resolver's job is the id the child resources are addressed by.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Description: "The unique identifier of the load balancer. Exactly one of " +
					"id or name must be specified.",
				Optional: true,
				Validators: []validator.String{
					validLoadBalancerIDValidator{},
				},
			},
			"name": schema.StringAttribute{
				Description: "The name of the load balancer. Exactly one of id or name " +
					"must be specified.",
				Optional: true,
			},
			"vpc_id": schema.StringAttribute{
				Description: "The VPC the load balancer serves. As an INPUT it narrows a name " +
					"lookup (sent as the list's `vpcId` filter); in the result it is the observed " +
					"VPC of the resolved load balancer.",
				Optional: true,
				Computed: true,
			},
			"description": schema.StringAttribute{
				Description: "The description of the load balancer, null when it has none.",
				Computed:    true,
			},
			"subnet_id": schema.StringAttribute{
				Description: "The subnet ID the load balancer's VIP lives on.",
				Computed:    true,
			},
			"vip_address": schema.StringAttribute{
				Description: "The virtual IP address of the load balancer, null when the " +
					"platform has not allocated one yet.",
				Computed: true,
			},
			"vip_port_id": schema.StringAttribute{
				Description: "The neutron port ID behind the VIP, null until the port exists.",
				Computed:    true,
			},
			"scheme": schema.StringAttribute{
				Description: "The reachability scheme, `internal` or `public` — derived by the " +
					"read path from whether a public IP is attached to the VIP port, so the " +
					"value is platform-verified, not a restatement of the configuration.",
				Computed: true,
			},
			"public_ip_id": schema.StringAttribute{
				Description: "The public IP attached to the load balancer, null when the load " +
					"balancer is reachable only from inside its VPC.",
				Computed: true,
			},
			"public_ip_address": schema.StringAttribute{
				Description: "The public IP address attached to the load balancer, null when " +
					"the load balancer is not publicly reachable.",
				Computed: true,
			},
			"type": schema.StringAttribute{
				Description: "The load-balancer type, `l4` or `l7` — null only if the platform " +
					"serves a row without one (the server normalises an unset type to `l7`).",
				Computed: true,
			},
			"flavor_id": schema.StringAttribute{
				Description: "The flavor the load balancer runs on, null for the platform's " +
					"default flavor.",
				Computed: true,
			},
			"status": schema.StringAttribute{
				Description: "The current lifecycle status of the load balancer, derived from " +
					"the two status axes below.",
				Computed: true,
			},
			"provisioning_status": schema.StringAttribute{
				Description: "The control-plane provisioning status, verbatim from the wire.",
				Computed:    true,
			},
			"operating_status": schema.StringAttribute{
				Description: "The data-plane operating status, verbatim from the wire.",
				Computed:    true,
			},
			"created_at": schema.StringAttribute{
				Description: "The timestamp when the load balancer was created.",
				Computed:    true,
			},
			"updated_at": schema.StringAttribute{
				Description: "The timestamp when the load balancer was last updated, null when " +
					"the platform has not recorded one.",
				Computed: true,
			},
			"tenant_id": schema.StringAttribute{
				Description: "The tenant ID that owns this load balancer.",
				Computed:    true,
			},
		},
	}
}

func (d *loadBalancerDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
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

func (d *loadBalancerDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var cfg loadBalancerModel
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

	vpcIDFilter := ""
	if !cfg.VPCID.IsNull() && !cfg.VPCID.IsUnknown() {
		vpcIDFilter = cfg.VPCID.ValueString()
	}

	var lb *apiLoadBalancer
	if idSet {
		found, diags := d.readByID(ctx, cfg.ID.ValueString())
		resp.Diagnostics.Append(diags...)
		if resp.Diagnostics.HasError() {
			return
		}
		lb = found
	} else {
		found, diags := d.resolveByName(ctx, cfg.Name.ValueString(), vpcIDFilter)
		resp.Diagnostics.Append(diags...)
		if resp.Diagnostics.HasError() {
			return
		}
		lb = found
	}

	setInstanceState(&cfg, lb)
	resp.Diagnostics.Append(resp.State.Set(ctx, &cfg)...)
}

// readByID walks the id path: one load-balancer read, then the identity guard.
// A 404 here is one cause among more than it looks: the guardManagedLB
// middleware answers the SAME 404 for an offer-internal (platform-managed) load
// balancer it hides from this plane as the service answers for a genuinely
// absent one, so the diagnostic says both things rather than promising which.
// A 200 that does not carry the requested id is not the load-balancer read
// this provider builds its contract on, whatever answered.
func (d *loadBalancerDataSource) readByID(ctx context.Context, id string) (*apiLoadBalancer, diag.Diagnostics) {
	var diags diag.Diagnostics

	pathStr, pathErr := loadBalancerPath(d.client, id)
	if pathErr != nil {
		diags.AddError("Invalid Load Balancer ID", pathErr.Error())
		return nil, diags
	}

	apiResp, err := d.client.Get(ctx, pathStr, nil)
	if err != nil {
		if client.IsNotFound(err) {
			diags.AddError(
				"The load balancer does not exist",
				fmt.Sprintf("The id %q answers 404 — there is no such load balancer (or none "+
					"this caller can see). The platform also hides offer-internal load balancers "+
					"from this plane, so the 404 may be a managed offer's rather than empty "+
					"space. Correct `id`, or look the load balancer up by `name` instead.\n\n%s",
					id, err.Error()),
			)
			return nil, diags
		}
		diags.AddError("Failed to Read Load Balancer", err.Error())
		return nil, diags
	}

	var lb apiLoadBalancer
	if err := json.Unmarshal(apiResp.Body, &lb); err != nil {
		diags.AddError("Failed to Parse Load Balancer Response", err.Error())
		return nil, diags
	}
	if lb.ID != id {
		diags.AddError(
			"This load balancer read did not identify the requested load balancer",
			fmt.Sprintf("The response does not carry the id this path asks for (%q). Whatever "+
				"answered is not the load-balancer read this provider builds its contract on, so "+
				"the lookup refuses rather than render a row no service promised. The path was %q.",
				id, pathStr),
		)
		return nil, diags
	}
	return &lb, diags
}

// resolveByName walks the list path. The exact `name` filter is the wire's own
// (`?name=`), and the optional `vpcId` filter rides the same request; but the
// filters are the FAST PATH, never the contract — Read re-verifies every row
// it answers with client-side, the way `frostmoln_subnet` does with its un-
// filtered list. The list pages on a marker, so Read walks the pages until the
// list is exhausted; more than one match is an error that names the colliding
// ids.
func (d *loadBalancerDataSource) resolveByName(ctx context.Context, name, vpcID string) (*apiLoadBalancer, diag.Diagnostics) {
	var diags diag.Diagnostics

	var matches []apiLoadBalancer
	const pageSize = 100
	const maxPages = 20
	marker := ""
	// searchExhausted records the walk hitting the page cap with a NEXT MARKER
	// still pending: the tenant may carry load balancers beyond the search
	// window, so a zero-match verdict then is "not in the window we searched",
	// never the plain absence the not-found arm asserts.
	searchExhausted := false
	for page := 0; page < maxPages; page++ {
		q := url.Values{}
		q.Set("limit", strconv.Itoa(pageSize))
		q.Set("name", name)
		if vpcID != "" {
			q.Set("vpcId", vpcID)
		}
		if marker != "" {
			q.Set("marker", marker)
		}

		apiResp, err := d.client.Get(ctx, d.client.TenantPath("/load-balancers"), q)
		if err != nil {
			diags.AddError("Failed to List Load Balancers", err.Error())
			return nil, diags
		}

		var list apiLoadBalancerList
		if err := json.Unmarshal(apiResp.Body, &list); err != nil {
			diags.AddError("Failed to Parse Load Balancers Response", err.Error())
			return nil, diags
		}

		for i := range list.LoadBalancers {
			lb := &list.LoadBalancers[i]
			if lb.Name != name {
				continue
			}
			if vpcID != "" && lb.VPCID != vpcID {
				continue
			}
			matches = append(matches, *lb)
		}
		if len(list.LoadBalancers) < pageSize || list.NextMarker == "" {
			break
		}
		if page == maxPages-1 {
			// The loop is about to end on the cap, and the server still has
			// more: the window, not the tenant, is what was searched.
			searchExhausted = true
		}
		marker = list.NextMarker
	}

	switch len(matches) {
	case 0:
		if searchExhausted {
			diags.AddError(
				fmt.Sprintf("No load balancer named %q in the searched window", name),
				fmt.Sprintf("The tenant's load-balancer list did not terminate within the "+
					"search window (%d pages), so the lookup cannot claim the name is "+
					"absent — there may be balancers beyond it. Narrow via `id`, or with "+
					"`vpc_id` when the lookup can be pinned to one VPC.", maxPages),
			)
			return nil, diags
		}
		detail := fmt.Sprintf("No load balancer named %q resolves in this tenant. Check the "+
			"spelling of `name`.", name)
		if vpcID != "" {
			detail = fmt.Sprintf("No load balancer named %q resolves in VPC %q of this tenant. "+
				"Check the spelling of `name`, that `vpc_id` names the right VPC, or drop "+
				"`vpc_id` to search the whole tenant.", name, vpcID)
		}
		diags.AddError(
			"No load balancer with this name",
			detail,
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
			fmt.Sprintf("%d load balancers share the name %q", len(matches), name),
			fmt.Sprintf("The tenant's load balancers carry more than one with "+
				"this name (%s). The data source refuses to pick one for you: every match "+
				"resolves to a DIFFERENT load balancer, and a silent pick would feed the wrong "+
				"VIP into your configuration. Disambiguate with `id`, or rename the load "+
				"balancers.", strings.Join(ids, ", ")),
		)
		return nil, diags
	}
}

// loadBalancerPath builds one load-balancer read path behind the same guard
// the id carries at plan time — Read must never trust that a validated
// configuration is the only thing that reaches it.
func loadBalancerPath(c *client.Client, id string) (string, error) {
	if err := validLoadBalancerID(id); err != nil {
		return "", err
	}
	return c.TenantPath(fmt.Sprintf("/load-balancers/%s", id)), nil
}

// validLoadBalancerID refuses an id that cannot safely be one path segment. The
// same guard shape the postgres_instance resolver carries: a "." or ".." id
// does not stay one path segment (the client joins with path.Join, which
// CLEANS), and the cleaned URL addresses a DIFFERENT resource.
func validLoadBalancerID(id string) error {
	if id == "" {
		return fmt.Errorf("a load balancer ID is required")
	}
	if id == "." || id == ".." || strings.ContainsAny(id, `/\?#%`) {
		return fmt.Errorf("invalid load balancer ID %q", id)
	}
	return nil
}

// validLoadBalancerIDValidator carries validLoadBalancerID into plan time, so a
// configuration with an unusable id fails before any request is built.
type validLoadBalancerIDValidator struct{}

func (v validLoadBalancerIDValidator) Description(_ context.Context) string {
	return "value must be a usable load balancer ID: non-empty, and a single URL path segment"
}

func (v validLoadBalancerIDValidator) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}

func (v validLoadBalancerIDValidator) ValidateString(_ context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if req.ConfigValue.IsUnknown() || req.ConfigValue.IsNull() {
		return
	}
	if err := validLoadBalancerID(req.ConfigValue.ValueString()); err != nil {
		resp.Diagnostics.AddAttributeError(
			req.Path,
			"Invalid Load Balancer ID",
			fmt.Sprintf("%s: %s", err.Error(), "a load balancer ID must be non-empty and "+
				"must not contain a '/', a backslash, '?', '#' or '%', so it can only ever "+
				"address the one load balancer named."),
		)
	}
}

// setInstanceState maps one verified load balancer onto the model. Fields the
// wire marks omitempty are null when absent — a check block must be able to
// tell "no value" apart from "empty value". The `vpc_id` input becomes the
// observed VPC here, exactly as frostmoln_subnet's does.
func setInstanceState(state *loadBalancerModel, lb *apiLoadBalancer) {
	state.ID = types.StringValue(lb.ID)
	state.Name = types.StringValue(lb.Name)
	state.Description = stringFromWire(lb.Description)
	state.VPCID = types.StringValue(lb.VPCID)
	state.SubnetID = types.StringValue(lb.SubnetID)
	state.VIPAddress = stringFromWire(lb.VIPAddress)
	state.VIPPortID = stringFromWire(lb.VIPPortID)
	state.Scheme = types.StringValue(lb.Scheme)
	state.PublicIPID = stringFromWire(lb.PublicIPID)
	state.PublicIPAddress = stringFromWire(lb.PublicIPAddress)
	state.Type = stringFromWire(lb.Type)
	state.FlavorID = stringFromWire(lb.FlavorID)
	state.Status = types.StringValue(lb.Status)
	state.ProvisioningStatus = types.StringValue(lb.ProvisioningStatus)
	state.OperatingStatus = types.StringValue(lb.OperatingStatus)
	state.CreatedAt = types.StringValue(lb.CreatedAt)
	state.UpdatedAt = stringFromWire(lb.UpdatedAt)
	state.TenantID = types.StringValue(lb.TenantID)
}

// stringFromWire maps an empty optional string to null, not "".
func stringFromWire(v string) types.String {
	if v == "" {
		return types.StringNull()
	}
	return types.StringValue(v)
}
