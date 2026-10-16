// Package messaging_permission implements the frostmoln_messaging_permission
// Terraform resource: one application user's grant on one vhost of a managed
// messaging (LavinMQ) instance.
package messaging_permission

import (
	"github.com/hashicorp/terraform-plugin-framework/types"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/messagingobj"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/timeouts"
)

// permissionModel is the Terraform state of a grant.
type permissionModel struct {
	ID             types.String    `tfsdk:"id"`
	InstanceID     types.String    `tfsdk:"instance_id"`
	User           types.String    `tfsdk:"user"`
	Vhost          types.String    `tfsdk:"vhost"`
	Configure      types.String    `tfsdk:"configure"`
	Write          types.String    `tfsdk:"write"`
	Read           types.String    `tfsdk:"read"`
	Status         types.String    `tfsdk:"status"`
	FailureReason  types.String    `tfsdk:"failure_reason"`
	FailureMessage types.String    `tfsdk:"failure_message"`
	CreatedAt      types.String    `tfsdk:"created_at"`
	UpdatedAt      types.String    `tfsdk:"updated_at"`
	Timeouts       *timeouts.Model `tfsdk:"timeouts"`
}

// fromAPI records what the platform says about the grant. The regexes are
// what the BROKER was last given — after a failed replace that is the old
// grant, which is exactly what makes the next plan show the change again
// instead of converging on a grant that was never applied. user and vhost are
// the identity and are not taken from the response; get has already checked
// the response names exactly this grant.
func (m *permissionModel) fromAPI(p *messagingobj.Permission) {
	m.ID = types.StringValue(m.InstanceID.ValueString() + "/" + m.User.ValueString() + "/" + m.Vhost.ValueString())
	m.Configure = types.StringValue(p.Configure)
	m.Write = types.StringValue(p.Write)
	m.Read = types.StringValue(p.Read)
	m.Status = types.StringValue(p.Status)
	m.FailureReason = messagingobj.Optional(p.FailureReason)
	m.FailureMessage = messagingobj.Optional(p.FailureMessage)
	m.CreatedAt = types.StringValue(p.CreatedAt)
	m.UpdatedAt = types.StringValue(p.UpdatedAt)
}

func (m *permissionModel) request() messagingobj.SetPermissionRequest {
	return messagingobj.SetPermissionRequest{
		Configure: m.Configure.ValueString(),
		Write:     m.Write.ValueString(),
		Read:      m.Read.ValueString(),
	}
}

// samePermission reports whether the API answered for the grant asked for.
// EXACT, like the service's own lookup: names are case-sensitive.
func samePermission(user, vhost string, p *messagingobj.Permission) bool {
	return user == p.User && vhost == p.Vhost
}
