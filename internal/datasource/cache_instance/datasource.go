// Package cache_instance implements the frostmoln_cache_instance Terraform data source.
package cache_instance

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"git.nl.cloud/NordicLight/terraform-provider-frostmoln/internal/client"
)

var _ datasource.DataSource = &cacheInstanceDataSource{}

// NewDataSource returns a new frostmoln_cache_instance data source factory.
func NewDataSource() datasource.DataSource {
	return &cacheInstanceDataSource{}
}

type cacheInstanceDataSource struct {
	client *client.Client
}

// cacheInstanceModel is the Terraform state model for a cache instance data source.
type cacheInstanceModel struct {
	ID              types.String `tfsdk:"id"`
	Name            types.String `tfsdk:"name"`
	Engine          types.String `tfsdk:"engine"`
	EngineVersion   types.String `tfsdk:"engine_version"`
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

// apiCacheInstance is the API representation of a managed cache instance.
type apiCacheInstance struct {
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

func (d *cacheInstanceDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_cache_instance"
}

func (d *cacheInstanceDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Look up a managed cache instance by ID.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Description: "The unique identifier of the cache instance.",
				Required:    true,
			},
			"name": schema.StringAttribute{
				Description: "The name of the cache instance.",
				Computed:    true,
			},
			"engine": schema.StringAttribute{
				Description: "The cache engine type (e.g. \"redis\", \"valkey\").",
				Computed:    true,
			},
			"engine_version": schema.StringAttribute{
				Description: "The engine version.",
				Computed:    true,
			},
			"flavor_id": schema.StringAttribute{
				Description: "The flavor/size of the cache instance.",
				Computed:    true,
			},
			"vpc_id": schema.StringAttribute{
				Description: "The VPC ID where the cache instance is deployed.",
				Computed:    true,
			},
			"subnet_id": schema.StringAttribute{
				Description: "The subnet ID where the cache instance is deployed.",
				Computed:    true,
			},
			"persistence_mode": schema.StringAttribute{
				Description: "The persistence mode of the cache instance.",
				Computed:    true,
			},
			"eviction_policy": schema.StringAttribute{
				Description: "The eviction policy of the cache instance.",
				Computed:    true,
			},
			"status": schema.StringAttribute{
				Description: "The current status of the cache instance.",
				Computed:    true,
			},
			"private_ip": schema.StringAttribute{
				Description: "The private IP address of the cache instance.",
				Computed:    true,
			},
			"port": schema.Int64Attribute{
				Description: "The port number the cache instance is listening on.",
				Computed:    true,
			},
			"admin_username": schema.StringAttribute{
				Description: "The admin username for the cache instance.",
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

func (d *cacheInstanceDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
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

func (d *cacheInstanceDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var state cacheInstanceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	apiResp, err := d.client.Get(ctx, d.client.TenantPath("/caches/"+state.ID.ValueString()), nil)
	if err != nil {
		resp.Diagnostics.AddError("Failed to read cache instance", err.Error())
		return
	}

	var inst apiCacheInstance
	if err := json.Unmarshal(apiResp.Body, &inst); err != nil {
		resp.Diagnostics.AddError("Failed to parse cache instance response", err.Error())
		return
	}

	state.ID = types.StringValue(inst.ID)
	state.Name = types.StringValue(inst.Name)
	state.Engine = types.StringValue(inst.Engine)
	state.EngineVersion = types.StringValue(inst.EngineVersion)
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
