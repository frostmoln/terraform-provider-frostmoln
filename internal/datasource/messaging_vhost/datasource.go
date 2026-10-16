// Package messaging_vhost implements the frostmoln_messaging_vhost Terraform
// data source.
package messaging_vhost

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/messagingobj"
)

var _ datasource.DataSource = &vhostDataSource{}

// NewDataSource returns a new frostmoln_messaging_vhost data source factory.
func NewDataSource() datasource.DataSource {
	return &vhostDataSource{}
}

type vhostDataSource struct {
	client *client.Client
}

type vhostModel struct {
	ID             types.String `tfsdk:"id"`
	InstanceID     types.String `tfsdk:"instance_id"`
	Name           types.String `tfsdk:"name"`
	Status         types.String `tfsdk:"status"`
	FailureReason  types.String `tfsdk:"failure_reason"`
	FailureMessage types.String `tfsdk:"failure_message"`
	CreatedAt      types.String `tfsdk:"created_at"`
	UpdatedAt      types.String `tfsdk:"updated_at"`
}

func (d *vhostDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_messaging_vhost"
}

func (d *vhostDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Look up a vhost on a managed messaging (LavinMQ) instance by name.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Description: "The vhost's id, `<instance_id>/<name>`.",
				Computed:    true,
			},
			"instance_id": schema.StringAttribute{
				Description: "The id of the messaging instance.",
				Required:    true,
				Validators:  messagingobj.InstanceIDValidators(),
			},
			"name": schema.StringAttribute{
				Description: "The vhost's name.",
				Required:    true,
				Validators:  messagingobj.NameValidators(),
			},
			"status": schema.StringAttribute{
				Description: "The vhost's status: `pending`, `ready`, `deleting` or `error`.",
				Computed:    true,
			},
			"failure_reason": schema.StringAttribute{
				Description: "The stable code of the vhost's last failed change, or null.",
				Computed:    true,
			},
			"failure_message": schema.StringAttribute{
				Description: "The last failed change's reason in words, or null.",
				Computed:    true,
			},
			"created_at": schema.StringAttribute{
				Description: "When the vhost was created.",
				Computed:    true,
			},
			"updated_at": schema.StringAttribute{
				Description: "When the vhost last changed.",
				Computed:    true,
			},
		},
	}
}

func (d *vhostDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
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

func (d *vhostDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	ctx = messagingobj.English(ctx)
	var m vhostModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	apiResp, err := d.client.Get(ctx, messagingobj.VhostPath(d.client, m.InstanceID.ValueString(), m.Name.ValueString()), nil)
	if err != nil {
		resp.Diagnostics.AddError("Failed to read the messaging vhost", err.Error())
		return
	}
	v, err := client.ParseResponse[messagingobj.Object](apiResp)
	if err != nil {
		resp.Diagnostics.AddError("Failed to parse the messaging vhost response", err.Error())
		return
	}
	if v.Name != m.Name.ValueString() {
		resp.Diagnostics.AddError("Unexpected messaging vhost in read response",
			fmt.Sprintf("Requested %q but the API answered with %q.", m.Name.ValueString(), v.Name))
		return
	}
	m.ID = types.StringValue(m.InstanceID.ValueString() + "/" + m.Name.ValueString())
	m.Status = types.StringValue(v.Status)
	m.FailureReason = messagingobj.Optional(v.FailureReason)
	m.FailureMessage = messagingobj.Optional(v.FailureMessage)
	m.CreatedAt = types.StringValue(v.CreatedAt)
	m.UpdatedAt = types.StringValue(v.UpdatedAt)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}
