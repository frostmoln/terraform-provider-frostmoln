// Package messaging_user implements the frostmoln_messaging_user Terraform
// resource: an AMQP-only LavinMQ application user on a managed messaging
// instance, with a platform-generated password.
package messaging_user

import (
	"github.com/hashicorp/terraform-plugin-framework/types"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/messagingobj"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/timeouts"
)

// userModel is the Terraform state of a user.
type userModel struct {
	ID             types.String    `tfsdk:"id"`
	InstanceID     types.String    `tfsdk:"instance_id"`
	Name           types.String    `tfsdk:"name"`
	Status         types.String    `tfsdk:"status"`
	FailureReason  types.String    `tfsdk:"failure_reason"`
	FailureMessage types.String    `tfsdk:"failure_message"`
	CreatedAt      types.String    `tfsdk:"created_at"`
	UpdatedAt      types.String    `tfsdk:"updated_at"`
	Password       types.String    `tfsdk:"password"`
	Timeouts       *timeouts.Model `tfsdk:"timeouts"`
}

// fromAPI records what the platform says about the user. The name is the
// resource's identity and is not taken from the response; get has already
// checked the response names exactly this user.
func (m *userModel) fromAPI(v *messagingobj.Object) {
	m.ID = types.StringValue(m.InstanceID.ValueString() + "/" + m.Name.ValueString())
	m.Status = types.StringValue(v.Status)
	m.FailureReason = messagingobj.Optional(v.FailureReason)
	m.FailureMessage = messagingobj.Optional(v.FailureMessage)
	m.CreatedAt = types.StringValue(v.CreatedAt)
	m.UpdatedAt = types.StringValue(v.UpdatedAt)
}

// sameName reports whether the API answered for the user that was asked for.
// EXACT: the service looks names up case-sensitively (only its uniqueness
// check folds case), and LavinMQ usernames are case-sensitive, so `Orders`
// and `orders` are different names, never two spellings of one object.
func sameName(want, got string) bool {
	return want == got
}
