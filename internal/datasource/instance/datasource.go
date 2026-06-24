// Package instance implements the fm_instance Terraform data source.
package instance

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
)

var _ datasource.DataSource = &instanceDataSource{}

// NewDataSource returns a new fm_instance data source factory.
func NewDataSource() datasource.DataSource {
	return &instanceDataSource{}
}

type instanceDataSource struct {
	client *client.Client
}

// instanceModel is the Terraform state model for an instance data source.
type instanceModel struct {
	ID         types.String `tfsdk:"id"`
	Name       types.String `tfsdk:"name"`
	FlavorID   types.String `tfsdk:"flavor_id"`
	FlavorName types.String `tfsdk:"flavor_name"`
	ImageID    types.String `tfsdk:"image_id"`
	ImageName  types.String `tfsdk:"image_name"`
	Region     types.String `tfsdk:"region"`
	Zone       types.String `tfsdk:"zone"`
	VPCID      types.String `tfsdk:"vpc_id"`
	SubnetID   types.String `tfsdk:"subnet_id"`
	PrivateIP  types.String `tfsdk:"private_ip"`
	PublicIP   types.String `tfsdk:"public_ip"`
	Status     types.String `tfsdk:"status"`
	Tags       types.Map    `tfsdk:"tags"`
	CreatedAt  types.String `tfsdk:"created_at"`
}

// apiInstance is the API representation of an instance.
type apiInstance struct {
	ID         string            `json:"id"`
	Name       string            `json:"name"`
	Status     string            `json:"status"`
	FlavorID   string            `json:"flavorId"`
	FlavorName string            `json:"flavorName,omitempty"`
	ImageID    string            `json:"imageId"`
	ImageName  string            `json:"imageName,omitempty"`
	Region     string            `json:"region"`
	Zone       string            `json:"availabilityZone,omitempty"`
	VPCID      string            `json:"vpcId,omitempty"`
	SubnetID   string            `json:"subnetId,omitempty"`
	PrivateIP  string            `json:"privateIp,omitempty"`
	PublicIP   string            `json:"publicIp,omitempty"`
	Tags       map[string]string `json:"tags,omitempty"`
	CreatedAt  string            `json:"createdAt"`
}

func (d *instanceDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_instance"
}

func (d *instanceDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Look up an instance by ID.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Description: "The unique identifier of the instance.",
				Required:    true,
			},
			"name": schema.StringAttribute{
				Description: "The name of the instance.",
				Computed:    true,
			},
			"flavor_id": schema.StringAttribute{
				Description: "The flavor ID of the instance.",
				Computed:    true,
			},
			"flavor_name": schema.StringAttribute{
				Description: "The flavor name of the instance.",
				Computed:    true,
			},
			"image_id": schema.StringAttribute{
				Description: "The image ID used to create the instance.",
				Computed:    true,
			},
			"image_name": schema.StringAttribute{
				Description: "The image name used to create the instance.",
				Computed:    true,
			},
			"region": schema.StringAttribute{
				Description: "The region of the instance.",
				Computed:    true,
			},
			"zone": schema.StringAttribute{
				Description: "The availability zone of the instance.",
				Computed:    true,
			},
			"vpc_id": schema.StringAttribute{
				Description: "The VPC ID of the instance.",
				Computed:    true,
			},
			"subnet_id": schema.StringAttribute{
				Description: "The subnet ID of the instance.",
				Computed:    true,
			},
			"private_ip": schema.StringAttribute{
				Description: "The private IP address of the instance.",
				Computed:    true,
			},
			"public_ip": schema.StringAttribute{
				Description: "The public IP address of the instance.",
				Computed:    true,
			},
			"status": schema.StringAttribute{
				Description: "The status of the instance.",
				Computed:    true,
			},
			"tags": schema.MapAttribute{
				Description: "The tags associated with the instance.",
				Computed:    true,
				ElementType: types.StringType,
			},
			"created_at": schema.StringAttribute{
				Description: "The timestamp when the instance was created.",
				Computed:    true,
			},
		},
	}
}

func (d *instanceDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
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

func (d *instanceDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var state instanceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	apiResp, err := d.client.Get(ctx, d.client.TenantPath("/instances/"+state.ID.ValueString()), nil)
	if err != nil {
		resp.Diagnostics.AddError("Failed to read instance", err.Error())
		return
	}

	var inst apiInstance
	if err := json.Unmarshal(apiResp.Body, &inst); err != nil {
		resp.Diagnostics.AddError("Failed to parse instance response", err.Error())
		return
	}

	state.ID = types.StringValue(inst.ID)
	state.Name = types.StringValue(inst.Name)
	state.FlavorID = types.StringValue(inst.FlavorID)
	state.FlavorName = types.StringValue(inst.FlavorName)
	state.ImageID = types.StringValue(inst.ImageID)
	state.ImageName = types.StringValue(inst.ImageName)
	state.Region = types.StringValue(inst.Region)
	state.Zone = types.StringValue(inst.Zone)
	state.VPCID = types.StringValue(inst.VPCID)
	state.SubnetID = types.StringValue(inst.SubnetID)
	state.PrivateIP = types.StringValue(inst.PrivateIP)
	state.PublicIP = types.StringValue(inst.PublicIP)
	state.Status = types.StringValue(inst.Status)
	state.CreatedAt = types.StringValue(inst.CreatedAt)

	if len(inst.Tags) > 0 {
		tagsMap, diags := types.MapValueFrom(ctx, types.StringType, inst.Tags)
		resp.Diagnostics.Append(diags...)
		state.Tags = tagsMap
	} else {
		state.Tags = types.MapNull(types.StringType)
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}
