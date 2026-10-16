// Package messaging_user implements the frostmoln_messaging_user Terraform
// data source.
package messaging_user

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/messagingobj"
)

var _ datasource.DataSource = &userDataSource{}

// NewDataSource returns a new frostmoln_messaging_user data source factory.
func NewDataSource() datasource.DataSource {
	return &userDataSource{}
}

type userDataSource struct {
	client *client.Client
}

type userModel struct {
	ID             types.String `tfsdk:"id"`
	InstanceID     types.String `tfsdk:"instance_id"`
	Name           types.String `tfsdk:"name"`
	Status         types.String `tfsdk:"status"`
	FailureReason  types.String `tfsdk:"failure_reason"`
	FailureMessage types.String `tfsdk:"failure_message"`
	CreatedAt      types.String `tfsdk:"created_at"`
	UpdatedAt      types.String `tfsdk:"updated_at"`
}

func (d *userDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_messaging_user"
}

func (d *userDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Look up an application user on a managed messaging (LavinMQ) instance by name. It does not expose the " +
			"password: manage the user with `frostmoln_messaging_user` to read it.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Description: "The user's id, `<instance_id>/<name>`.",
				Computed:    true,
			},
			"instance_id": schema.StringAttribute{
				Description: "The id of the messaging instance.",
				Required:    true,
				Validators:  messagingobj.InstanceIDValidators(),
			},
			"name": schema.StringAttribute{
				Description: "The user's name.",
				Required:    true,
				Validators:  messagingobj.NameValidators(),
			},
			"status": schema.StringAttribute{
				Description: "The user's status: `pending`, `ready`, `deleting` or `error`.",
				Computed:    true,
			},
			"failure_reason": schema.StringAttribute{
				Description: "The stable code of the user's last failed change, or null.",
				Computed:    true,
			},
			"failure_message": schema.StringAttribute{
				Description: "The last failed change's reason in words, or null.",
				Computed:    true,
			},
			"created_at": schema.StringAttribute{
				Description: "When the user was created.",
				Computed:    true,
			},
			"updated_at": schema.StringAttribute{
				Description: "When the user last changed.",
				Computed:    true,
			},
		},
	}
}

func (d *userDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
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

func (d *userDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	ctx = messagingobj.English(ctx)
	var m userModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	apiResp, err := d.client.Get(ctx, messagingobj.UserPath(d.client, m.InstanceID.ValueString(), m.Name.ValueString()), nil)
	if err != nil {
		resp.Diagnostics.AddError("Failed to read the messaging user", err.Error())
		return
	}
	v, err := client.ParseResponse[messagingobj.Object](apiResp)
	if err != nil {
		resp.Diagnostics.AddError("Failed to parse the messaging user response", err.Error())
		return
	}
	if v.Name != m.Name.ValueString() {
		resp.Diagnostics.AddError("Unexpected messaging user in read response",
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
