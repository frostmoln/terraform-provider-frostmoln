// Package public_ip implements the frostmoln_public_ip Terraform data source.
package public_ip

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
)

var _ datasource.DataSource = &publicIPDataSource{}

// NewDataSource returns a new frostmoln_public_ip data source factory.
func NewDataSource() datasource.DataSource {
	return &publicIPDataSource{}
}

type publicIPDataSource struct {
	client *client.Client
}

type publicIPModel struct {
	ID         types.String `tfsdk:"id"`
	Address    types.String `tfsdk:"address"`
	Status     types.String `tfsdk:"status"`
	PrivateIP  types.String `tfsdk:"private_ip"`
	Attachment types.Object `tfsdk:"attachment"`
	Tags       types.Map    `tfsdk:"tags"`
	CreatedAt  types.String `tfsdk:"created_at"`
}

// attachmentAttrTypes mirrors the `frostmoln_public_ip` RESOURCE's attachment
// object. The two are kept identical by
// TestAttachmentShapeMatchesTheResource — a practitioner who moves an
// expression from the resource to this data source must not have to rewrite it.
// attachmentKindUnknown is synthesized by this provider, never sent by the
// platform: it is what "the platform reported no attachment for this address"
// becomes. See the resource package's AttachmentKindUnknown.
const attachmentKindUnknown = "unknown"

var attachmentAttrTypes = map[string]attr.Type{
	"kind":        types.StringType,
	"resource_id": types.StringType,
	"vpc_id":      types.StringType,
}

type apiPublicIP struct {
	ID        string                 `json:"id"`
	Address   string                 `json:"publicIpAddress"`
	Status    string                 `json:"status"`
	PortID    string                 `json:"portId,omitempty"`
	PrivateIP string                 `json:"fixedIpAddress,omitempty"`
	Tags      map[string]string      `json:"tags,omitempty"`
	CreatedAt string                 `json:"createdAt"`
	Att       *apiPublicIPAttachment `json:"attachment,omitempty"`
}

type apiPublicIPAttachment struct {
	Kind       string `json:"kind"`
	ResourceID string `json:"resourceId,omitempty"`
	VPCID      string `json:"vpcId,omitempty"`
}

type apiPublicIPList struct {
	PublicIPs []apiPublicIP `json:"publicIps"`
}

func (d *publicIPDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_public_ip"
}

func (d *publicIPDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Look up one of your tenant's public IPs by ID or by address, without managing " +
			"it. Looking one up BY ADDRESS is what lets a configuration name an address that " +
			"already exists — one already published in DNS or sitting in a partner's allow-list " +
			"— and hand it to `frostmoln_egress_gateway.public_ip_id` so a VPC egresses from it, " +
			"without importing the address into Terraform's management.\n\n" +
			"Reading a public IP does not attach it to anything, and this data source never " +
			"allocates one.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Description: "The public IP's identifier. Exactly one of `id` or `address` must be set.",
				Optional:    true,
				Computed:    true,
			},
			"address": schema.StringAttribute{
				Description: "The IPv4 address itself, e.g. \"203.0.113.10\". Exactly one of `id` or " +
					"`address` must be set. The lookup matches the whole address exactly and refuses " +
					"anything but a single result, so it can never silently resolve to a neighbouring " +
					"address.",
				Optional: true,
				Computed: true,
			},
			"status": schema.StringAttribute{
				Description: "The status of the public IP.",
				Computed:    true,
			},
			"private_ip": schema.StringAttribute{
				Description: "The private IP this address is attached to, when it is attached to an " +
					"instance. Null otherwise.",
				Computed: true,
			},
			"attachment": schema.SingleNestedAttribute{
				Description: "What is currently using this address. Never null — an address nothing is " +
					"using reports `kind = \"none\"`. Read this, not the absence of a private IP, to " +
					"tell whether the address is free: an address serving a VPC's outbound traffic has " +
					"no instance and no port, and giving such an address away cannot be undone.",
				Computed: true,
				Attributes: map[string]schema.Attribute{
					"kind": schema.StringAttribute{
						Description: "\"none\" (allocated, nothing using it), \"port\" (attached to an " +
							"instance or load balancer), \"egress_gateway\" (it is a VPC's outbound " +
							"source address) or \"unknown\". An address that is already \"port\" or " +
							"\"egress_gateway\" cannot be given to another egress gateway.\n\n" +
							"\"unknown\" means the platform did not report an attachment for this " +
							"address. Read it as \"not established\", never as \"free\": an address " +
							"serving a VPC's egress has no port either, so nothing here tells the two " +
							"apart. New kinds can appear without a provider upgrade and are passed " +
							"through unchanged.",
						Computed: true,
					},
					"resource_id": schema.StringAttribute{
						Description: "What holds the address: the network port for \"port\", the egress " +
							"gateway for \"egress_gateway\". Null for \"none\".",
						Computed: true,
					},
					"vpc_id": schema.StringAttribute{
						Description: "The VPC whose outbound traffic leaves from this address. Set only " +
							"for \"egress_gateway\".",
						Computed: true,
					},
				},
			},
			"tags": schema.MapAttribute{
				Description: "The tags on the public IP.",
				Computed:    true,
				ElementType: types.StringType,
			},
			"created_at": schema.StringAttribute{
				Description: "The timestamp when the public IP was allocated.",
				Computed:    true,
			},
		},
	}
}

func (d *publicIPDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
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

func (d *publicIPDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var state publicIPModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// An empty string counts as NOT set, not as a lookup key. Both selectors
	// are typically interpolated, and an interpolation that resolves to "" would
	// otherwise become `?publicIpAddress=` — a filter the API ignores, leaving
	// the tenant-wide list from which a caller takes an arbitrary element and
	// feeds an unrelated address into an allow-list or an egress gateway.
	idSet := !state.ID.IsNull() && !state.ID.IsUnknown() && state.ID.ValueString() != ""
	addressSet := !state.Address.IsNull() && !state.Address.IsUnknown() && state.Address.ValueString() != ""

	switch {
	case !idSet && !addressSet:
		resp.Diagnostics.AddAttributeError(
			path.Root("id"),
			"Missing Attribute",
			"Exactly one of `id` or `address` must be specified, and neither may be an empty string. "+
				"Without one of them there is nothing to look up, and a lookup with no selector would "+
				"return an arbitrary one of your tenant's addresses.",
		)
		return
	case idSet && addressSet:
		resp.Diagnostics.AddAttributeError(
			path.Root("id"),
			"Conflicting Attributes",
			"Only one of `id` or `address` may be specified, not both. If they name different public "+
				"IPs there is no answer that is not a guess.",
		)
		return
	}

	if idSet {
		apiResp, err := d.client.Get(ctx, d.client.TenantPath("/public-ips/"+url.PathEscape(state.ID.ValueString())), nil)
		if err != nil {
			if client.IsNotFound(err) {
				resp.Diagnostics.AddError(
					"Public IP not found",
					fmt.Sprintf("No public IP with id %q exists in this tenant. The same answer is given "+
						"for an address owned by a different tenant, so check that the provider's "+
						"credentials are scoped to the tenant that owns it.", state.ID.ValueString()),
				)
				return
			}
			resp.Diagnostics.AddError("Failed to read public IP", err.Error())
			return
		}

		var pip apiPublicIP
		if err := json.Unmarshal(apiResp.Body, &pip); err != nil {
			resp.Diagnostics.AddError("Failed to parse public IP response", err.Error())
			return
		}
		d.setState(ctx, &state, &pip, resp)
		return
	}

	address := state.Address.ValueString()
	q := url.Values{}
	q.Set("publicIpAddress", address)

	apiResp, err := d.client.Get(ctx, d.client.TenantPath("/public-ips"), q)
	if err != nil {
		resp.Diagnostics.AddError("Failed to list public IPs", err.Error())
		return
	}

	var list apiPublicIPList
	if err := json.Unmarshal(apiResp.Body, &list); err != nil {
		resp.Diagnostics.AddError("Failed to parse public IPs response", err.Error())
		return
	}

	// Re-check the address in the client. The filter is the server's, but what
	// this data source promises is an EXACT address, and a server that widened
	// or ignored the filter would otherwise resolve to a neighbouring address
	// that a practitioner then publishes or pins a VPC's egress to.
	var matches []apiPublicIP
	for _, pip := range list.PublicIPs {
		if pip.Address == address {
			matches = append(matches, pip)
		}
	}

	switch len(matches) {
	case 0:
		resp.Diagnostics.AddError(
			"Public IP not found",
			fmt.Sprintf("This tenant has no public IP with the address %q. The same answer is given for "+
				"an address that exists but belongs to another tenant, and for one the platform "+
				"reserves for its own use, so check the address and that the provider's credentials are "+
				"scoped to the tenant that owns it.", address),
		)
		return
	case 1:
	default:
		resp.Diagnostics.AddError(
			"Multiple public IPs match that address",
			fmt.Sprintf("The lookup for %q returned %d public IPs. An address identifies at most one, so "+
				"this is not a result to choose from — picking one would bind the configuration to an "+
				"arbitrary object. Look the address up by `id` instead.", address, len(matches)),
		)
		return
	}

	d.setState(ctx, &state, &matches[0], resp)
}

func (d *publicIPDataSource) setState(ctx context.Context, state *publicIPModel, pip *apiPublicIP, resp *datasource.ReadResponse) {
	state.ID = types.StringValue(pip.ID)
	state.Address = types.StringValue(pip.Address)
	state.Status = types.StringValue(pip.Status)
	state.CreatedAt = types.StringValue(pip.CreatedAt)
	state.PrivateIP = nullIfEmpty(pip.PrivateIP)
	state.Attachment = attachmentObject(pip, &resp.Diagnostics)

	if len(pip.Tags) > 0 {
		tagsMap, d := types.MapValueFrom(ctx, types.StringType, pip.Tags)
		resp.Diagnostics.Append(d...)
		state.Tags = tagsMap
	} else {
		state.Tags = types.MapNull(types.StringType)
	}
	if resp.Diagnostics.HasError() {
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
}

// attachmentObject renders the attachment, always non-null — see the resource's
// AttachmentObject, which this mirrors exactly.
//
// A response with no `attachment` is one that does not report attachments, and
// only ONE inference is drawn from it: a non-empty `portId` is positive
// evidence of a port. No port is NOT evidence of no attachment (an egress
// source address never has one), so that becomes "unknown", never "none".
// Reading it as "none" would tell a practitioner an address is free at the one
// moment giving it away cannot be undone.
func attachmentObject(pip *apiPublicIP, diags *diag.Diagnostics) types.Object {
	att := pip.Att
	if att == nil {
		att = &apiPublicIPAttachment{Kind: attachmentKindUnknown}
		if pip.PortID != "" {
			att = &apiPublicIPAttachment{Kind: "port", ResourceID: pip.PortID}
		}
	}

	kind := att.Kind
	if kind == "" {
		kind = attachmentKindUnknown
	}

	obj, d := types.ObjectValue(attachmentAttrTypes, map[string]attr.Value{
		"kind":        types.StringValue(kind),
		"resource_id": nullIfEmpty(att.ResourceID),
		"vpc_id":      nullIfEmpty(att.VPCID),
	})
	diags.Append(d...)
	return obj
}

func nullIfEmpty(s string) types.String {
	if s == "" {
		return types.StringNull()
	}
	return types.StringValue(s)
}
