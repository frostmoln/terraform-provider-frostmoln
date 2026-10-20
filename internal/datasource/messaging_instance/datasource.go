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
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/tftags"
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
	Type            types.String `tfsdk:"type"`
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
	TLSEnabled      types.Bool   `tfsdk:"tls_enabled"`
	Hostname        types.String `tfsdk:"hostname"`
	PublicName      types.String `tfsdk:"public_name"`
	CreatedAt       types.String `tfsdk:"created_at"`
	UpdatedAt       types.String `tfsdk:"updated_at"`
	Tags            types.Map    `tfsdk:"tags"`
}

// apiMessagingInstance is the API representation of a managed messaging instance.
type apiMessagingInstance struct {
	ID              string `json:"id"`
	Name            string `json:"name"`
	Type            string `json:"type"`
	TypeVersion     string `json:"typeVersion"`
	FlavorID        string `json:"flavorId"`
	VPCID           string `json:"vpcId"`
	SubnetID        string `json:"subnetId"`
	PersistenceMode string `json:"persistenceMode"`
	Status          string `json:"status"`
	PrivateIP       string `json:"privateIp,omitempty"`
	Port            int    `json:"port,omitempty"`
	AMQPSPort       int    `json:"amqpsPort,omitempty"`
	ManagementPort  int    `json:"managementPort,omitempty"`
	TLSEnabled      bool   `json:"tlsEnabled"`
	Hostname        string `json:"hostname,omitempty"`
	PublicName      string `json:"publicName,omitempty"`
	CreatedAt       string `json:"createdAt"`
	UpdatedAt       string `json:"updatedAt,omitempty"`
	// Tags are the resource's customer resource tags ({} when untagged).
	Tags map[string]string `json:"tags"`
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
			"type": schema.StringAttribute{
				Description: "The messaging type (e.g. \"lavinmq\").",
				Computed:    true,
			},
			"version": schema.StringAttribute{
				Description: "The version.",
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
			"tls_enabled": schema.BoolAttribute{
				Description: "Whether the instance serves TLS: AMQPS on `amqps_port` and HTTPS management, with a certificate for " +
					"`hostname` from the Frostmoln customer CA (see the `frostmoln_ca_bundle` data source). Plaintext AMQP " +
					"and HTTP management stay served beside it. False on an instance without a certificate.",
				Computed: true,
			},
			"hostname": schema.StringAttribute{
				Description: "The instance's internal DNS name (`<label>.mq.<vpc-label>.<tenant-label>.<org-label>.fm.internal`, the label being the name at create lowercased, every run of characters other than `a-z0-9` turned into one `-`, `-` trimmed at both ends and cut to 63 characters), resolvable " +
					"inside its VPC; its TLS certificate is issued for this name. Fixed once assigned: a rename does not " +
					"move it. Null on an instance without a certificate (it gets none retroactively; " +
					"recreate it to get one).",
				Computed: true,
			},
			"public_name": schema.StringAttribute{
				Description: "The instance's public DNS name under `mq.svc.frostmoln.cloud`. Null unless it has a public IP.",
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
	resp.Schema.Attributes["tags"] = schema.MapAttribute{
		Description: "Every resource tag on the messaging instance — the same set as the resource's `tags_all`: your " +
			"own key/value metadata, never applied to the service itself. An empty map when it has none.",
		Computed:    true,
		ElementType: types.StringType,
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
	state.Type = types.StringValue(inst.Type)
	state.Version = types.StringValue(inst.TypeVersion)
	state.FlavorID = types.StringValue(inst.FlavorID)
	state.VPCID = types.StringValue(inst.VPCID)
	state.SubnetID = types.StringValue(inst.SubnetID)
	state.PersistenceMode = types.StringValue(inst.PersistenceMode)
	state.Status = types.StringValue(inst.Status)
	state.CreatedAt = types.StringValue(inst.CreatedAt)
	state.TLSEnabled = types.BoolValue(inst.TLSEnabled)
	state.Hostname = types.StringNull()
	if inst.Hostname != "" {
		state.Hostname = types.StringValue(inst.Hostname)
	}
	state.PublicName = types.StringNull()
	if inst.PublicName != "" {
		state.PublicName = types.StringValue(inst.PublicName)
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

	// The platform's reserved namespace is filtered as on the resource read-back,
	// so `tags` means the same thing on every surface.
	state.Tags = tftags.OfferTagsValue(inst.Tags)
	if inst.UpdatedAt != "" {
		state.UpdatedAt = types.StringValue(inst.UpdatedAt)
	} else {
		state.UpdatedAt = types.StringNull()
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}
