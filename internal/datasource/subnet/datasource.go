// Package subnet implements the fm_subnet Terraform data source.
package subnet

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
)

var _ datasource.DataSource = &subnetDataSource{}

// NewDataSource returns a new fm_subnet data source factory.
func NewDataSource() datasource.DataSource {
	return &subnetDataSource{}
}

type subnetDataSource struct {
	client *client.Client
}

// subnetModel is the Terraform state model for a subnet data source.
type subnetModel struct {
	ID           types.String `tfsdk:"id"`
	Name         types.String `tfsdk:"name"`
	VPCID        types.String `tfsdk:"vpc_id"`
	Description  types.String `tfsdk:"description"`
	CIDR         types.String `tfsdk:"cidr"`
	Zone         types.String `tfsdk:"zone"`
	GatewayIP    types.String `tfsdk:"gateway_ip"`
	IsPublic     types.Bool   `tfsdk:"is_public"`
	Status       types.String `tfsdk:"status"`
	AvailableIPs types.Int64  `tfsdk:"available_ips"`
	Tags         types.Map    `tfsdk:"tags"`
	CreatedAt    types.String `tfsdk:"created_at"`
}

// apiSubnet is the API representation of a subnet.
type apiSubnet struct {
	ID           string            `json:"id"`
	Name         string            `json:"name"`
	VPCID        string            `json:"vpcId"`
	Description  string            `json:"description,omitempty"`
	CIDR         string            `json:"cidr"`
	Zone         string            `json:"zone,omitempty"`
	GatewayIP    string            `json:"gatewayIp,omitempty"`
	IsPublic     bool              `json:"isPublic"`
	Status       string            `json:"status"`
	AvailableIPs int               `json:"availableIps"`
	Tags         map[string]string `json:"tags,omitempty"`
	CreatedAt    string            `json:"createdAt"`
}

// apiSubnetList is the API response for listing subnets.
type apiSubnetList struct {
	Subnets []apiSubnet `json:"subnets"`
}

func (d *subnetDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_subnet"
}

func (d *subnetDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Look up a subnet by ID or name, optionally filtered by VPC.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Description: "The unique identifier of the subnet. At least one of id or name must be specified.",
				Optional:    true,
				Computed:    true,
			},
			"name": schema.StringAttribute{
				Description: "The name of the subnet. At least one of id or name must be specified.",
				Optional:    true,
				Computed:    true,
			},
			"vpc_id": schema.StringAttribute{
				Description: "Filter subnets by VPC ID. Used when looking up by name.",
				Optional:    true,
				Computed:    true,
			},
			"description": schema.StringAttribute{
				Description: "The description of the subnet.",
				Computed:    true,
			},
			"cidr": schema.StringAttribute{
				Description: "The CIDR block of the subnet.",
				Computed:    true,
			},
			"zone": schema.StringAttribute{
				Description: "The availability zone of the subnet.",
				Computed:    true,
			},
			"gateway_ip": schema.StringAttribute{
				Description: "The gateway IP address of the subnet.",
				Computed:    true,
			},
			"is_public": schema.BoolAttribute{
				Description: "Whether this is a public subnet.",
				Computed:    true,
			},
			"status": schema.StringAttribute{
				Description: "The status of the subnet.",
				Computed:    true,
			},
			"available_ips": schema.Int64Attribute{
				Description: "The number of available IP addresses in the subnet.",
				Computed:    true,
			},
			"tags": schema.MapAttribute{
				Description: "The tags associated with the subnet.",
				Computed:    true,
				ElementType: types.StringType,
			},
			"created_at": schema.StringAttribute{
				Description: "The timestamp when the subnet was created.",
				Computed:    true,
			},
		},
	}
}

func (d *subnetDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
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

func (d *subnetDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var state subnetModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	idSet := !state.ID.IsNull() && !state.ID.IsUnknown()
	nameSet := !state.Name.IsNull() && !state.Name.IsUnknown()

	if !idSet && !nameSet {
		resp.Diagnostics.AddAttributeError(
			path.Root("id"),
			"Missing Attribute",
			"At least one of id or name must be specified.",
		)
		return
	}
	if idSet && nameSet {
		resp.Diagnostics.AddAttributeError(
			path.Root("id"),
			"Conflicting Attributes",
			"Only one of id or name may be specified, not both.",
		)
		return
	}

	// If ID is provided, try direct lookup
	if idSet {
		apiResp, err := d.client.Get(ctx, d.client.TenantPath("/subnets/"+state.ID.ValueString()), nil)
		if err != nil {
			resp.Diagnostics.AddError("Failed to read subnet", err.Error())
			return
		}

		var sub apiSubnet
		if err := json.Unmarshal(apiResp.Body, &sub); err != nil {
			resp.Diagnostics.AddError("Failed to parse subnet response", err.Error())
			return
		}

		d.setSubnetState(ctx, &state, &sub, resp)
		return
	}

	// Filter by name: list all subnets and find the match
	apiResp, err := d.client.Get(ctx, d.client.TenantPath("/subnets"), nil)
	if err != nil {
		resp.Diagnostics.AddError("Failed to list subnets", err.Error())
		return
	}

	var list apiSubnetList
	if err := json.Unmarshal(apiResp.Body, &list); err != nil {
		resp.Diagnostics.AddError("Failed to parse subnets response", err.Error())
		return
	}

	vpcIDFilter := ""
	if !state.VPCID.IsNull() && !state.VPCID.IsUnknown() {
		vpcIDFilter = state.VPCID.ValueString()
	}

	var found *apiSubnet
	for i := range list.Subnets {
		s := &list.Subnets[i]
		if s.Name != state.Name.ValueString() {
			continue
		}
		if vpcIDFilter != "" && s.VPCID != vpcIDFilter {
			continue
		}
		found = s
		break
	}

	if found == nil {
		msg := fmt.Sprintf("No subnet found with name %q", state.Name.ValueString())
		if vpcIDFilter != "" {
			msg += fmt.Sprintf(" in VPC %q", vpcIDFilter)
		}
		msg += "."
		resp.Diagnostics.AddError("Subnet not found", msg)
		return
	}

	d.setSubnetState(ctx, &state, found, resp)
}

func (d *subnetDataSource) setSubnetState(ctx context.Context, state *subnetModel, sub *apiSubnet, resp *datasource.ReadResponse) {
	state.ID = types.StringValue(sub.ID)
	state.Name = types.StringValue(sub.Name)
	state.VPCID = types.StringValue(sub.VPCID)
	state.Description = types.StringValue(sub.Description)
	state.CIDR = types.StringValue(sub.CIDR)
	state.Zone = types.StringValue(sub.Zone)
	state.GatewayIP = types.StringValue(sub.GatewayIP)
	state.IsPublic = types.BoolValue(sub.IsPublic)
	state.Status = types.StringValue(sub.Status)
	state.AvailableIPs = types.Int64Value(int64(sub.AvailableIPs))
	state.CreatedAt = types.StringValue(sub.CreatedAt)

	if len(sub.Tags) > 0 {
		tagsMap, diags := types.MapValueFrom(ctx, types.StringType, sub.Tags)
		resp.Diagnostics.Append(diags...)
		state.Tags = tagsMap
	} else {
		state.Tags = types.MapNull(types.StringType)
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
}
