// Package messaging_permission implements the frostmoln_messaging_permission
// Terraform data source.
package messaging_permission

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/messagingobj"
)

var _ datasource.DataSource = &permissionDataSource{}

// NewDataSource returns a new frostmoln_messaging_permission data source factory.
func NewDataSource() datasource.DataSource {
	return &permissionDataSource{}
}

type permissionDataSource struct {
	client *client.Client
}

type permissionModel struct {
	ID             types.String `tfsdk:"id"`
	InstanceID     types.String `tfsdk:"instance_id"`
	User           types.String `tfsdk:"user"`
	Vhost          types.String `tfsdk:"vhost"`
	Configure      types.String `tfsdk:"configure"`
	Write          types.String `tfsdk:"write"`
	Read           types.String `tfsdk:"read"`
	Status         types.String `tfsdk:"status"`
	FailureReason  types.String `tfsdk:"failure_reason"`
	FailureMessage types.String `tfsdk:"failure_message"`
	CreatedAt      types.String `tfsdk:"created_at"`
	UpdatedAt      types.String `tfsdk:"updated_at"`
}

func (d *permissionDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_messaging_permission"
}

func computed(desc string) schema.StringAttribute {
	return schema.StringAttribute{Description: desc, Computed: true}
}

func (d *permissionDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Look up one application user's permission on one vhost of a managed messaging (LavinMQ) instance.",
		Attributes: map[string]schema.Attribute{
			"id": computed("The permission's id, `<instance_id>/<user>/<vhost>`."),
			"instance_id": schema.StringAttribute{
				Description: "The id of the messaging instance.",
				Required:    true,
				Validators:  messagingobj.InstanceIDValidators(),
			},
			"user": schema.StringAttribute{
				Description: "The user's name.",
				Required:    true,
				Validators:  messagingobj.NameValidators(),
			},
			"vhost": schema.StringAttribute{
				Description: "The vhost's name.",
				Required:    true,
				Validators:  messagingobj.NameValidators(),
			},
			"configure":       computed("The configure regex the broker was last given: at most 255 bytes, never starting with `-`; `^$` grants nothing."),
			"write":           computed("The write regex the broker was last given: at most 255 bytes, never starting with `-`; `^$` grants nothing."),
			"read":            computed("The read regex the broker was last given: at most 255 bytes, never starting with `-`; `^$` grants nothing."),
			"status":          computed("The permission's status: `pending`, `ready`, `deleting` or `error`."),
			"failure_reason":  computed("The stable code of the permission's last failed change, or null."),
			"failure_message": computed("The last failed change's reason in words, or null."),
			"created_at":      computed("When the permission was created."),
			"updated_at":      computed("When the permission last changed."),
		},
	}
}

func (d *permissionDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
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

func (d *permissionDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	ctx = messagingobj.English(ctx)
	var m permissionModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	instanceID, user, vhost := m.InstanceID.ValueString(), m.User.ValueString(), m.Vhost.ValueString()
	apiResp, err := d.client.Get(ctx, messagingobj.PermissionPath(d.client, instanceID, user, vhost), nil)
	if err != nil {
		resp.Diagnostics.AddError("Failed to read the messaging permission", err.Error())
		return
	}
	p, err := client.ParseResponse[messagingobj.Permission](apiResp)
	if err != nil {
		resp.Diagnostics.AddError("Failed to parse the messaging permission response", err.Error())
		return
	}
	if p.User != user || p.Vhost != vhost {
		resp.Diagnostics.AddError("Unexpected messaging permission in read response",
			fmt.Sprintf("Requested %q on %q but the API answered with %q on %q.", user, vhost, p.User, p.Vhost))
		return
	}
	m.ID = types.StringValue(instanceID + "/" + user + "/" + vhost)
	m.Configure = types.StringValue(p.Configure)
	m.Write = types.StringValue(p.Write)
	m.Read = types.StringValue(p.Read)
	m.Status = types.StringValue(p.Status)
	m.FailureReason = messagingobj.Optional(p.FailureReason)
	m.FailureMessage = messagingobj.Optional(p.FailureMessage)
	m.CreatedAt = types.StringValue(p.CreatedAt)
	m.UpdatedAt = types.StringValue(p.UpdatedAt)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}
