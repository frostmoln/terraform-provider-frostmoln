// Package vpc implements the fm_vpc Terraform data source.
package vpc

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"git.nl.cloud/NordicLight/terraform-provider-frostmoln/internal/client"
)

var _ datasource.DataSource = &vpcDataSource{}

// NewDataSource returns a new fm_vpc data source factory.
func NewDataSource() datasource.DataSource {
	return &vpcDataSource{}
}

type vpcDataSource struct {
	client *client.Client
}

// vpcModel is the Terraform state model for a VPC data source.
type vpcModel struct {
	ID          types.String `tfsdk:"id"`
	Name        types.String `tfsdk:"name"`
	Description types.String `tfsdk:"description"`
	CIDR        types.String `tfsdk:"cidr"`
	Region      types.String `tfsdk:"region"`
	Status      types.String `tfsdk:"status"`
	IsDefault   types.Bool   `tfsdk:"is_default"`
	SubnetCount types.Int64  `tfsdk:"subnet_count"`
	Tags        types.Map    `tfsdk:"tags"`
	CreatedAt   types.String `tfsdk:"created_at"`
}

// apiVPC is the API representation of a VPC.
type apiVPC struct {
	ID          string            `json:"id"`
	Name        string            `json:"name"`
	Description string            `json:"description,omitempty"`
	CIDR        string            `json:"cidr"`
	Region      string            `json:"region"`
	Status      string            `json:"status"`
	IsDefault   bool              `json:"isDefault"`
	SubnetCount int               `json:"subnetCount"`
	Tags        map[string]string `json:"tags,omitempty"`
	CreatedAt   string            `json:"createdAt"`
}

// apiVPCList is the API response for listing VPCs.
type apiVPCList struct {
	VPCs []apiVPC `json:"vpcs"`
}

func (d *vpcDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_vpc"
}

func (d *vpcDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Look up a VPC by ID or name.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Description: "The unique identifier of the VPC. Exactly one of id or name must be specified.",
				Optional:    true,
				Computed:    true,
			},
			"name": schema.StringAttribute{
				Description: "The name of the VPC. Exactly one of id or name must be specified.",
				Optional:    true,
				Computed:    true,
			},
			"description": schema.StringAttribute{
				Description: "The description of the VPC.",
				Computed:    true,
			},
			"cidr": schema.StringAttribute{
				Description: "The CIDR block of the VPC.",
				Computed:    true,
			},
			"region": schema.StringAttribute{
				Description: "The region of the VPC.",
				Computed:    true,
			},
			"status": schema.StringAttribute{
				Description: "The status of the VPC.",
				Computed:    true,
			},
			"is_default": schema.BoolAttribute{
				Description: "Whether this is the default VPC.",
				Computed:    true,
			},
			"subnet_count": schema.Int64Attribute{
				Description: "The number of subnets in the VPC.",
				Computed:    true,
			},
			"tags": schema.MapAttribute{
				Description: "The tags associated with the VPC.",
				Computed:    true,
				ElementType: types.StringType,
			},
			"created_at": schema.StringAttribute{
				Description: "The timestamp when the VPC was created.",
				Computed:    true,
			},
		},
	}
}

func (d *vpcDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
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

func (d *vpcDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var state vpcModel
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
			"Exactly one of id or name must be specified.",
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

	// If ID is provided, try direct lookup first
	if idSet {
		apiResp, err := d.client.Get(ctx, d.client.TenantPath("/vpcs/"+state.ID.ValueString()), nil)
		if err != nil {
			resp.Diagnostics.AddError("Failed to read VPC", err.Error())
			return
		}

		var vpc apiVPC
		if err := json.Unmarshal(apiResp.Body, &vpc); err != nil {
			resp.Diagnostics.AddError("Failed to parse VPC response", err.Error())
			return
		}

		d.setVPCState(ctx, &state, &vpc, resp)
		return
	}

	// Filter by name: list all VPCs and find the match
	apiResp, err := d.client.Get(ctx, d.client.TenantPath("/vpcs"), nil)
	if err != nil {
		resp.Diagnostics.AddError("Failed to list VPCs", err.Error())
		return
	}

	var list apiVPCList
	if err := json.Unmarshal(apiResp.Body, &list); err != nil {
		resp.Diagnostics.AddError("Failed to parse VPCs response", err.Error())
		return
	}

	var found *apiVPC
	for i := range list.VPCs {
		if list.VPCs[i].Name == state.Name.ValueString() {
			found = &list.VPCs[i]
			break
		}
	}

	if found == nil {
		resp.Diagnostics.AddError("VPC not found", fmt.Sprintf("No VPC found with name %q.", state.Name.ValueString()))
		return
	}

	d.setVPCState(ctx, &state, found, resp)
}

func (d *vpcDataSource) setVPCState(ctx context.Context, state *vpcModel, vpc *apiVPC, resp *datasource.ReadResponse) {
	state.ID = types.StringValue(vpc.ID)
	state.Name = types.StringValue(vpc.Name)
	state.Description = types.StringValue(vpc.Description)
	state.CIDR = types.StringValue(vpc.CIDR)
	state.Region = types.StringValue(vpc.Region)
	state.Status = types.StringValue(vpc.Status)
	state.IsDefault = types.BoolValue(vpc.IsDefault)
	state.SubnetCount = types.Int64Value(int64(vpc.SubnetCount))
	state.CreatedAt = types.StringValue(vpc.CreatedAt)

	if len(vpc.Tags) > 0 {
		tagsMap, diags := types.MapValueFrom(ctx, types.StringType, vpc.Tags)
		resp.Diagnostics.Append(diags...)
		state.Tags = tagsMap
	} else {
		state.Tags = types.MapNull(types.StringType)
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
}
