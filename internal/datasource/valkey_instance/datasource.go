// Package valkey_instance implements the frostmoln_valkey_instance Terraform data source.
package valkey_instance

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
)

var _ datasource.DataSource = &valkeyInstanceDataSource{}

// NewDataSource returns a new frostmoln_valkey_instance data source factory.
func NewDataSource() datasource.DataSource {
	return &valkeyInstanceDataSource{}
}

type valkeyInstanceDataSource struct {
	client *client.Client
}

// valkeyInstanceModel is the Terraform state model for a Valkey instance data source.
type valkeyInstanceModel struct {
	ID              types.String `tfsdk:"id"`
	Name            types.String `tfsdk:"name"`
	Version         types.String `tfsdk:"version"`
	FlavorID        types.String `tfsdk:"flavor_id"`
	VPCID           types.String `tfsdk:"vpc_id"`
	SubnetID        types.String `tfsdk:"subnet_id"`
	PersistenceMode types.String `tfsdk:"persistence_mode"`
	EvictionPolicy  types.String `tfsdk:"eviction_policy"`
	Status          types.String `tfsdk:"status"`
	PrivateIP       types.String `tfsdk:"private_ip"`
	Port            types.Int64  `tfsdk:"port"`
	AdminUsername   types.String `tfsdk:"admin_username"`
	CreatedAt       types.String `tfsdk:"created_at"`
	UpdatedAt       types.String `tfsdk:"updated_at"`
}

// apiValkeyInstance is the API representation of a managed Valkey instance.
type apiValkeyInstance struct {
	ID              string `json:"id"`
	Name            string `json:"name"`
	Engine          string `json:"engine"`
	EngineVersion   string `json:"engineVersion"`
	FlavorID        string `json:"flavorId"`
	VPCID           string `json:"vpcId"`
	SubnetID        string `json:"subnetId"`
	PersistenceMode string `json:"persistenceMode"`
	EvictionPolicy  string `json:"evictionPolicy"`
	Status          string `json:"status"`
	PrivateIP       string `json:"privateIp,omitempty"`
	Port            int    `json:"port,omitempty"`
	AdminUsername   string `json:"adminUsername,omitempty"`
	CreatedAt       string `json:"createdAt"`
	UpdatedAt       string `json:"updatedAt,omitempty"`
}

func (d *valkeyInstanceDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_valkey_instance"
}

func (d *valkeyInstanceDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Look up a managed Valkey instance by ID.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Description: "The unique identifier of the Valkey instance.",
				Required:    true,
			},
			"name": schema.StringAttribute{
				Description: "The name of the Valkey instance.",
				Computed:    true,
			},
			"version": schema.StringAttribute{
				Description: "The Valkey version.",
				Computed:    true,
			},
			"flavor_id": schema.StringAttribute{
				Description: "The flavor/size of the Valkey instance.",
				Computed:    true,
			},
			"vpc_id": schema.StringAttribute{
				Description: "The VPC ID where the Valkey instance is deployed.",
				Computed:    true,
			},
			"subnet_id": schema.StringAttribute{
				Description: "The subnet ID where the Valkey instance is deployed.",
				Computed:    true,
			},
			"persistence_mode": schema.StringAttribute{
				Description: "The persistence mode of the Valkey instance.",
				Computed:    true,
			},
			"eviction_policy": schema.StringAttribute{
				Description: "The eviction policy of the Valkey instance.",
				Computed:    true,
			},
			"status": schema.StringAttribute{
				Description: "The current status of the Valkey instance.",
				Computed:    true,
			},
			"private_ip": schema.StringAttribute{
				Description: "The private IP address of the Valkey instance.",
				Computed:    true,
			},
			"port": schema.Int64Attribute{
				Description: "The port number the Valkey instance is listening on.",
				Computed:    true,
			},
			"admin_username": schema.StringAttribute{
				Description: "The admin username for the Valkey instance.",
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

func (d *valkeyInstanceDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
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

func (d *valkeyInstanceDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var state valkeyInstanceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	apiResp, err := d.client.Get(ctx, d.client.TenantPath("/caches/"+state.ID.ValueString()), nil)
	if err != nil {
		resp.Diagnostics.AddError("Failed to read Valkey instance", err.Error())
		return
	}

	var inst apiValkeyInstance
	if err := json.Unmarshal(apiResp.Body, &inst); err != nil {
		resp.Diagnostics.AddError("Failed to parse Valkey instance response", err.Error())
		return
	}

	state.ID = types.StringValue(inst.ID)
	state.Name = types.StringValue(inst.Name)
	state.Version = types.StringValue(inst.EngineVersion)
	state.FlavorID = types.StringValue(inst.FlavorID)
	state.VPCID = types.StringValue(inst.VPCID)
	state.SubnetID = types.StringValue(inst.SubnetID)
	state.PersistenceMode = types.StringValue(inst.PersistenceMode)
	state.EvictionPolicy = types.StringValue(inst.EvictionPolicy)
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

	if inst.AdminUsername != "" {
		state.AdminUsername = types.StringValue(inst.AdminUsername)
	} else {
		state.AdminUsername = types.StringNull()
	}

	if inst.UpdatedAt != "" {
		state.UpdatedAt = types.StringValue(inst.UpdatedAt)
	} else {
		state.UpdatedAt = types.StringNull()
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}
