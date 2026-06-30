// Package apache_instance implements the frostmoln_apache_instance Terraform data source.
package apache_instance

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
)

var _ datasource.DataSource = &apacheInstanceDataSource{}

// NewDataSource returns a new frostmoln_apache_instance data source factory.
func NewDataSource() datasource.DataSource {
	return &apacheInstanceDataSource{}
}

type apacheInstanceDataSource struct {
	client *client.Client
}

// apacheInstanceModel is the Terraform state model for an Apache instance data source.
type apacheInstanceModel struct {
	ID         types.String `tfsdk:"id"`
	Name       types.String `tfsdk:"name"`
	Version    types.String `tfsdk:"version"`
	FlavorID   types.String `tfsdk:"flavor_id"`
	StorageGB  types.Int64  `tfsdk:"storage_gb"`
	VPCID      types.String `tfsdk:"vpc_id"`
	SubnetID   types.String `tfsdk:"subnet_id"`
	TLSEnabled types.Bool   `tfsdk:"tls_enabled"`
	PHPEnabled types.Bool   `tfsdk:"php_enabled"`
	PHPVersion types.String `tfsdk:"php_version"`
	Config     types.Map    `tfsdk:"config"`
	Status     types.String `tfsdk:"status"`
	PrivateIP  types.String `tfsdk:"private_ip"`
	Port       types.Int64  `tfsdk:"port"`
	CreatedAt  types.String `tfsdk:"created_at"`
	UpdatedAt  types.String `tfsdk:"updated_at"`
	TenantID   types.String `tfsdk:"tenant_id"`
}

// apiWebserverInstance is the API representation of a managed webserver instance.
// The flavor is `flavorId`, vpcId/subnetId are returned, and `engineConfig` is a
// JSON object (webserver/internal/domain/instance.go).
type apiWebserverInstance struct {
	ID            string            `json:"id"`
	Name          string            `json:"name"`
	Engine        string            `json:"engine"`
	EngineVersion string            `json:"engineVersion"`
	FlavorID      string            `json:"flavorId"`
	StorageGB     int               `json:"storageGb"`
	VPCID         string            `json:"vpcId"`
	SubnetID      string            `json:"subnetId"`
	TLSEnabled    bool              `json:"tlsEnabled"`
	PHPEnabled    bool              `json:"phpEnabled"`
	PHPVersion    string            `json:"phpVersion,omitempty"`
	EngineConfig  map[string]string `json:"engineConfig,omitempty"`
	Status        string            `json:"status"`
	PrivateIP     string            `json:"privateIp,omitempty"`
	Port          int               `json:"port,omitempty"`
	CreatedAt     string            `json:"createdAt"`
	UpdatedAt     string            `json:"updatedAt,omitempty"`
	TenantID      string            `json:"tenantId,omitempty"`
}

func (d *apacheInstanceDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_apache_instance"
}

func (d *apacheInstanceDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Look up a managed Apache webserver instance by ID.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Description: "The unique identifier of the Apache instance.",
				Required:    true,
			},
			"name": schema.StringAttribute{
				Description: "The name of the Apache instance.",
				Computed:    true,
			},
			"version": schema.StringAttribute{
				Description: "The Apache version.",
				Computed:    true,
			},
			"flavor_id": schema.StringAttribute{
				Description: "The flavor ID/size of the Apache instance.",
				Computed:    true,
			},
			"storage_gb": schema.Int64Attribute{
				Description: "The storage size in gigabytes.",
				Computed:    true,
			},
			"vpc_id": schema.StringAttribute{
				Description: "The VPC ID where the instance is deployed.",
				Computed:    true,
			},
			"subnet_id": schema.StringAttribute{
				Description: "The subnet ID where the instance is deployed.",
				Computed:    true,
			},
			"tls_enabled": schema.BoolAttribute{
				Description: "Whether TLS is enabled.",
				Computed:    true,
			},
			"php_enabled": schema.BoolAttribute{
				Description: "Whether PHP support is enabled.",
				Computed:    true,
			},
			"php_version": schema.StringAttribute{
				Description: "The PHP version.",
				Computed:    true,
			},
			"config": schema.MapAttribute{
				Description: "Engine-specific configuration as key/value pairs.",
				Computed:    true,
				ElementType: types.StringType,
			},
			"status": schema.StringAttribute{
				Description: "The current status of the Apache instance.",
				Computed:    true,
			},
			"private_ip": schema.StringAttribute{
				Description: "The private IP address of the Apache instance.",
				Computed:    true,
			},
			"port": schema.Int64Attribute{
				Description: "The port number the Apache instance is listening on.",
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
			"tenant_id": schema.StringAttribute{
				Description: "The tenant ID that owns this instance.",
				Computed:    true,
			},
		},
	}
}

func (d *apacheInstanceDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
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

func (d *apacheInstanceDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var state apacheInstanceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	apiResp, err := d.client.Get(ctx, d.client.TenantPath("/webservers/"+state.ID.ValueString()), nil)
	if err != nil {
		resp.Diagnostics.AddError("Failed to read Apache instance", err.Error())
		return
	}

	var inst apiWebserverInstance
	if err := json.Unmarshal(apiResp.Body, &inst); err != nil {
		resp.Diagnostics.AddError("Failed to parse Apache instance response", err.Error())
		return
	}

	state.ID = types.StringValue(inst.ID)
	state.Name = types.StringValue(inst.Name)
	state.Version = types.StringValue(inst.EngineVersion)
	state.FlavorID = types.StringValue(inst.FlavorID)
	state.StorageGB = types.Int64Value(int64(inst.StorageGB))
	state.VPCID = types.StringValue(inst.VPCID)
	state.SubnetID = types.StringValue(inst.SubnetID)
	state.TLSEnabled = types.BoolValue(inst.TLSEnabled)
	state.PHPEnabled = types.BoolValue(inst.PHPEnabled)
	state.Status = types.StringValue(inst.Status)
	state.CreatedAt = types.StringValue(inst.CreatedAt)

	if inst.PHPVersion != "" {
		state.PHPVersion = types.StringValue(inst.PHPVersion)
	} else {
		state.PHPVersion = types.StringNull()
	}

	if len(inst.EngineConfig) > 0 {
		cfgMap, d := types.MapValueFrom(ctx, types.StringType, inst.EngineConfig)
		resp.Diagnostics.Append(d...)
		state.Config = cfgMap
	} else {
		state.Config = types.MapNull(types.StringType)
	}

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

	if inst.UpdatedAt != "" {
		state.UpdatedAt = types.StringValue(inst.UpdatedAt)
	} else {
		state.UpdatedAt = types.StringNull()
	}

	if inst.TenantID != "" {
		state.TenantID = types.StringValue(inst.TenantID)
	} else {
		state.TenantID = types.StringNull()
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}
