// Package messaging_instance implements the frostmoln_messaging_instance Terraform data source.
package messaging_instance

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
)

var _ datasource.DataSource = &messagingInstanceDataSource{}

// NewDataSource returns a new frostmoln_messaging_instance data source factory.
func NewDataSource() datasource.DataSource {
	return &messagingInstanceDataSource{}
}

type messagingInstanceDataSource struct {
	client *client.Client
}

// messagingInstanceModel is the Terraform state model for a messaging instance data source.
type messagingInstanceModel struct {
	ID              types.String `tfsdk:"id"`
	Name            types.String `tfsdk:"name"`
	Engine          types.String `tfsdk:"engine"`
	Version         types.String `tfsdk:"version"`
	FlavorID        types.String `tfsdk:"flavor_id"`
	VPCID           types.String `tfsdk:"vpc_id"`
	SubnetID        types.String `tfsdk:"subnet_id"`
	PersistenceMode types.String `tfsdk:"persistence_mode"`
	Status          types.String `tfsdk:"status"`
	PrivateIP       types.String `tfsdk:"private_ip"`
	Port            types.Int64  `tfsdk:"port"`
	AMQPSPort       types.Int64  `tfsdk:"amqps_port"`
	ManagementPort  types.Int64  `tfsdk:"management_port"`
	CreatedAt       types.String `tfsdk:"created_at"`
	UpdatedAt       types.String `tfsdk:"updated_at"`
}

// apiMessagingInstance is the API representation of a managed messaging instance.
type apiMessagingInstance struct {
	ID              string `json:"id"`
	Name            string `json:"name"`
	Engine          string `json:"engine"`
	EngineVersion   string `json:"engineVersion"`
	FlavorID        string `json:"flavorId"`
	VPCID           string `json:"vpcId"`
	SubnetID        string `json:"subnetId"`
	PersistenceMode string `json:"persistenceMode"`
	Status          string `json:"status"`
	PrivateIP       string `json:"privateIp,omitempty"`
	Port            int    `json:"port,omitempty"`
	AMQPSPort       int    `json:"amqpsPort,omitempty"`
	ManagementPort  int    `json:"managementPort,omitempty"`
	CreatedAt       string `json:"createdAt"`
	UpdatedAt       string `json:"updatedAt,omitempty"`
}

func (d *messagingInstanceDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_messaging_instance"
}

func (d *messagingInstanceDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Look up a managed messaging (LavinMQ) instance by ID.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Description: "The unique identifier of the messaging instance.",
				Required:    true,
			},
			"name": schema.StringAttribute{
				Description: "The name of the messaging instance.",
				Computed:    true,
			},
			"engine": schema.StringAttribute{
				Description: "The messaging engine type (e.g. \"lavinmq\").",
				Computed:    true,
			},
			"version": schema.StringAttribute{
				Description: "The engine version.",
				Computed:    true,
			},
			"flavor_id": schema.StringAttribute{
				Description: "The flavor/size of the messaging instance.",
				Computed:    true,
			},
			"vpc_id": schema.StringAttribute{
				Description: "The VPC ID where the messaging instance is deployed.",
				Computed:    true,
			},
			"subnet_id": schema.StringAttribute{
				Description: "The subnet ID where the messaging instance is deployed.",
				Computed:    true,
			},
			"persistence_mode": schema.StringAttribute{
				Description: "The persistence mode of the messaging instance.",
				Computed:    true,
			},
			"status": schema.StringAttribute{
				Description: "The current status of the messaging instance.",
				Computed:    true,
			},
			"private_ip": schema.StringAttribute{
				Description: "The private IP address of the messaging instance.",
				Computed:    true,
			},
			"port": schema.Int64Attribute{
				Description: "The AMQP port number the messaging instance is listening on.",
				Computed:    true,
			},
			"amqps_port": schema.Int64Attribute{
				Description: "The AMQPS (TLS) port number the messaging instance is listening on.",
				Computed:    true,
			},
			"management_port": schema.Int64Attribute{
				Description: "The HTTP management/API port number the messaging instance is listening on.",
				Computed:    true,
			},
			"created_at": schema.StringAttribute{
				Description: "The timestamp when the instance was created.",
				Computed:    true,
			},
			"updated_at": schema.StringAttribute{
				Description: "The timestamp when the instance was last updated.",
				Computed:    true,
			},
		},
	}
}

func (d *messagingInstanceDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
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

func (d *messagingInstanceDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var state messagingInstanceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	apiResp, err := d.client.Get(ctx, d.client.TenantPath("/messaging/"+state.ID.ValueString()), nil)
	if err != nil {
		resp.Diagnostics.AddError("Failed to read messaging instance", err.Error())
		return
	}

	var inst apiMessagingInstance
	if err := json.Unmarshal(apiResp.Body, &inst); err != nil {
		resp.Diagnostics.AddError("Failed to parse messaging instance response", err.Error())
		return
	}

	state.ID = types.StringValue(inst.ID)
	state.Name = types.StringValue(inst.Name)
	state.Engine = types.StringValue(inst.Engine)
	state.Version = types.StringValue(inst.EngineVersion)
	state.FlavorID = types.StringValue(inst.FlavorID)
	state.VPCID = types.StringValue(inst.VPCID)
	state.SubnetID = types.StringValue(inst.SubnetID)
	state.PersistenceMode = types.StringValue(inst.PersistenceMode)
	state.Status = types.StringValue(inst.Status)
	state.CreatedAt = types.StringValue(inst.CreatedAt)

	if inst.PrivateIP != "" {
		state.PrivateIP = types.StringValue(inst.PrivateIP)
	} else {
		state.PrivateIP = types.StringNull()
	}

	if inst.Port > 0 {
		state.Port = types.Int64Value(int64(inst.Port))
	} else {
		state.Port = types.Int64Null()
	}

	if inst.AMQPSPort > 0 {
		state.AMQPSPort = types.Int64Value(int64(inst.AMQPSPort))
	} else {
		state.AMQPSPort = types.Int64Null()
	}

	if inst.ManagementPort > 0 {
		state.ManagementPort = types.Int64Value(int64(inst.ManagementPort))
	} else {
		state.ManagementPort = types.Int64Null()
	}

	if inst.UpdatedAt != "" {
		state.UpdatedAt = types.StringValue(inst.UpdatedAt)
	} else {
		state.UpdatedAt = types.StringNull()
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}
