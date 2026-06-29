// Package nginx_instance implements the frostmoln_nginx_instance Terraform data source.
package nginx_instance

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
)

var _ datasource.DataSource = &nginxInstanceDataSource{}

// NewDataSource returns a new frostmoln_nginx_instance data source factory.
func NewDataSource() datasource.DataSource {
	return &nginxInstanceDataSource{}
}

type nginxInstanceDataSource struct {
	client *client.Client
}

// nginxInstanceModel is the Terraform state model for a Nginx instance data source.
type nginxInstanceModel struct {
	ID              types.String `tfsdk:"id"`
	Name            types.String `tfsdk:"name"`
	Version         types.String `tfsdk:"version"`
	Flavor          types.String `tfsdk:"flavor"`
	StorageGB       types.Int64  `tfsdk:"storage_gb"`
	TLSEnabled      types.Bool   `tfsdk:"tls_enabled"`
	WorkerProcesses types.Int64  `tfsdk:"worker_processes"`
	GzipEnabled     types.Bool   `tfsdk:"gzip_enabled"`
	TryFiles        types.String `tfsdk:"try_files"`
	ProxyPass       types.String `tfsdk:"proxy_pass"`
	Config          types.String `tfsdk:"config"`
	Status          types.String `tfsdk:"status"`
	PrivateIP       types.String `tfsdk:"private_ip"`
	Port            types.Int64  `tfsdk:"port"`
	CreatedAt       types.String `tfsdk:"created_at"`
	UpdatedAt       types.String `tfsdk:"updated_at"`
	TenantID        types.String `tfsdk:"tenant_id"`
}

// apiWebserverInstance is the API representation of a managed webserver instance.
type apiWebserverInstance struct {
	ID              string `json:"id"`
	Name            string `json:"name"`
	Engine          string `json:"engine"`
	EngineVersion   string `json:"engineVersion"`
	Flavor          string `json:"flavor"`
	StorageGB       int    `json:"storageGb"`
	TLSEnabled      bool   `json:"tlsEnabled"`
	WorkerProcesses int    `json:"workerProcesses,omitempty"`
	GzipEnabled     bool   `json:"gzipEnabled"`
	TryFiles        string `json:"tryFiles,omitempty"`
	ProxyPass       string `json:"proxyPass,omitempty"`
	EngineConfig    string `json:"engineConfig,omitempty"`
	Status          string `json:"status"`
	PrivateIP       string `json:"privateIp,omitempty"`
	Port            int    `json:"port,omitempty"`
	CreatedAt       string `json:"createdAt"`
	UpdatedAt       string `json:"updatedAt,omitempty"`
	TenantID        string `json:"tenantId,omitempty"`
}

func (d *nginxInstanceDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_nginx_instance"
}

func (d *nginxInstanceDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Look up a managed Nginx webserver instance by ID.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Description: "The unique identifier of the Nginx instance.",
				Required:    true,
			},
			"name": schema.StringAttribute{
				Description: "The name of the Nginx instance.",
				Computed:    true,
			},
			"version": schema.StringAttribute{
				Description: "The Nginx version.",
				Computed:    true,
			},
			"flavor": schema.StringAttribute{
				Description: "The flavor/size of the Nginx instance.",
				Computed:    true,
			},
			"storage_gb": schema.Int64Attribute{
				Description: "The storage size in gigabytes.",
				Computed:    true,
			},
			"tls_enabled": schema.BoolAttribute{
				Description: "Whether TLS is enabled.",
				Computed:    true,
			},
			"worker_processes": schema.Int64Attribute{
				Description: "The number of Nginx worker processes.",
				Computed:    true,
			},
			"gzip_enabled": schema.BoolAttribute{
				Description: "Whether gzip compression is enabled.",
				Computed:    true,
			},
			"try_files": schema.StringAttribute{
				Description: "The try_files directive value.",
				Computed:    true,
			},
			"proxy_pass": schema.StringAttribute{
				Description: "The proxy_pass upstream URL.",
				Computed:    true,
			},
			"config": schema.StringAttribute{
				Description: "Custom engine configuration.",
				Computed:    true,
			},
			"status": schema.StringAttribute{
				Description: "The current status of the Nginx instance.",
				Computed:    true,
			},
			"private_ip": schema.StringAttribute{
				Description: "The private IP address of the Nginx instance.",
				Computed:    true,
			},
			"port": schema.Int64Attribute{
				Description: "The port number the Nginx instance is listening on.",
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

func (d *nginxInstanceDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
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

func (d *nginxInstanceDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var state nginxInstanceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	apiResp, err := d.client.Get(ctx, d.client.TenantPath("/webservers/"+state.ID.ValueString()), nil)
	if err != nil {
		resp.Diagnostics.AddError("Failed to read Nginx instance", err.Error())
		return
	}

	var inst apiWebserverInstance
	if err := json.Unmarshal(apiResp.Body, &inst); err != nil {
		resp.Diagnostics.AddError("Failed to parse Nginx instance response", err.Error())
		return
	}

	state.ID = types.StringValue(inst.ID)
	state.Name = types.StringValue(inst.Name)
	state.Version = types.StringValue(inst.EngineVersion)
	state.Flavor = types.StringValue(inst.Flavor)
	state.StorageGB = types.Int64Value(int64(inst.StorageGB))
	state.TLSEnabled = types.BoolValue(inst.TLSEnabled)
	state.GzipEnabled = types.BoolValue(inst.GzipEnabled)
	state.Status = types.StringValue(inst.Status)
	state.CreatedAt = types.StringValue(inst.CreatedAt)

	if inst.WorkerProcesses > 0 {
		state.WorkerProcesses = types.Int64Value(int64(inst.WorkerProcesses))
	} else {
		state.WorkerProcesses = types.Int64Null()
	}

	if inst.TryFiles != "" {
		state.TryFiles = types.StringValue(inst.TryFiles)
	} else {
		state.TryFiles = types.StringNull()
	}

	if inst.ProxyPass != "" {
		state.ProxyPass = types.StringValue(inst.ProxyPass)
	} else {
		state.ProxyPass = types.StringNull()
	}

	if inst.EngineConfig != "" {
		state.Config = types.StringValue(inst.EngineConfig)
	} else {
		state.Config = types.StringNull()
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
