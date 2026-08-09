// Package gateway implements the frostmoln_gateway Terraform data source.
package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
)

var _ datasource.DataSource = &gatewayDataSource{}

// NewDataSource returns a new frostmoln_gateway data source factory.
func NewDataSource() datasource.DataSource {
	return &gatewayDataSource{}
}

type gatewayDataSource struct {
	client *client.Client
}

type gatewayModel struct {
	ID            types.String `tfsdk:"id"`
	VPCID         types.String `tfsdk:"vpc_id"`
	Mode          types.String `tfsdk:"mode"`
	SourceAddress types.String `tfsdk:"source_address"`
	Status        types.String `tfsdk:"status"`
	Origin        types.String `tfsdk:"origin"`
}

type apiGateway struct {
	ID            string `json:"id"`
	VPCID         string `json:"vpcId"`
	Mode          string `json:"mode"`
	SourceAddress string `json:"sourceAddress,omitempty"`
	Status        string `json:"status"`
	Origin        string `json:"origin"`
}

type apiGatewayList struct {
	Gateways   []apiGateway `json:"gateways"`
	TotalCount int          `json:"totalCount"`
}

func (d *gatewayDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_gateway"
}

func (d *gatewayDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Look up a VPC's gateway — its outbound internet path — without " +
			"managing it. Useful for reading the source address of a gateway created " +
			"elsewhere (chosen at VPC create, or attached by the platform when a public IP " +
			"was associated) so it can be fed to a partner allow-list.",
		Attributes: map[string]schema.Attribute{
			"vpc_id": schema.StringAttribute{
				Description: "The VPC whose gateway to look up. Required, and never empty: " +
					"the lookup is only ever asked for one specific VPC.",
				Required: true,
			},
			"id": schema.StringAttribute{
				Description: "The gateway's identifier.",
				Computed:    true,
			},
			"mode": schema.StringAttribute{
				Description: "How outbound traffic is addressed. \"public_ip\" — the VPC has " +
					"its own gateway, carrying both its outbound traffic and the inbound traffic " +
					"of any public IP attached to an instance in the VPC.",
				Computed: true,
			},
			"source_address": schema.StringAttribute{
				Description: "The public IPv4 address outbound traffic appears to come from. Null " +
					"while the gateway is detached or the address is not yet known.",
				Computed: true,
			},
			"status": schema.StringAttribute{
				Description: "Observed state, read from the cloud rather than from stored desired " +
					"state: \"active\" or \"detached\". What the platform observes depends on how the " +
					"mode is realised, so \"detached\" is a prompt to check the VPC's outbound path " +
					"rather than proof that anything is wrong.",
				Computed: true,
			},
			"origin": schema.StringAttribute{
				Description: "Who asked for the gateway: \"explicit\", \"implicit_public_ip\", " +
					"\"vpc_create\", or \"legacy\" — which means no stored record exists, so the " +
					"provenance is UNKNOWN (read it as \"unknown\", never as \"old\").",
				Computed: true,
			},
		},
	}
}

func (d *gatewayDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
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

func (d *gatewayDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var state gatewayModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// REFUSE AN EMPTY vpc_id, and never fall back to the unfiltered list.
	// `?vpcId=` present-but-empty is a 400 by design; the danger is the request
	// this guard prevents being made at all. Dropping the parameter returns the
	// TENANT-WIDE list, from which a caller takes element [0] — some unrelated
	// VPC's gateway — and the practitioner would then feed another tenant VPC's
	// source address into an allow-list, or point a managed resource at it.
	// `Required` already rejects null; an interpolation that resolves to "" is
	// what actually produces this, so it is checked explicitly.
	vpcID := state.VPCID.ValueString()
	if vpcID == "" {
		resp.Diagnostics.AddAttributeError(
			path.Root("vpc_id"),
			"Empty vpc_id",
			"vpc_id resolved to an empty string. It must name the VPC whose gateway you want: "+
				"an empty value would otherwise widen the lookup to every gateway in the tenant and "+
				"return an unrelated VPC's.",
		)
		return
	}

	q := url.Values{}
	q.Set("vpcId", vpcID)

	apiResp, err := d.client.Get(ctx, d.client.TenantPath("/gateways"), q)
	if err != nil {
		resp.Diagnostics.AddError("Failed to read gateway", err.Error())
		return
	}

	var list apiGatewayList
	if err := json.Unmarshal(apiResp.Body, &list); err != nil {
		resp.Diagnostics.AddError("Failed to parse gateway response", err.Error())
		return
	}

	if len(list.Gateways) == 0 {
		// An empty result is never a 404 here, and it covers two cases that
		// cannot be told apart from the response: the VPC has no gateway (it is
		// an isolated network), or the id names no VPC this tenant owns. Say
		// both, rather than asserting the first.
		resp.Diagnostics.AddError(
			"No gateway for this VPC",
			fmt.Sprintf("VPC %q has no gateway — it is an isolated network with no outbound "+
				"internet path, no platform DNS resolution and no managed-service connectivity. "+
				"(The same empty result is returned for a VPC id this tenant does not own, so check "+
				"the id too.) Use the `frostmoln_gateway` resource to give it one.", vpcID),
		)
		return
	}

	gw := list.Gateways[0]
	state.ID = types.StringValue(gw.ID)
	state.VPCID = types.StringValue(gw.VPCID)
	state.Mode = types.StringValue(gw.Mode)
	state.Status = types.StringValue(gw.Status)
	state.Origin = types.StringValue(gw.Origin)
	if gw.SourceAddress == "" {
		state.SourceAddress = types.StringNull()
	} else {
		state.SourceAddress = types.StringValue(gw.SourceAddress)
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}
