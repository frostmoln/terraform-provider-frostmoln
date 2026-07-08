// Package iam_policy_attachment implements the frostmoln_iam_policy_attachment
// Terraform resource — a binding of an IAM access policy (ADR-0102) to a machine
// principal (API key or workload identity) or a group. The binding has no mutable
// fields: changing any attribute replaces it (detach + re-attach).
package iam_policy_attachment

import "github.com/hashicorp/terraform-plugin-framework/types"

// Attachee kinds (mirror the identity domain; validated server-side).
const (
	attacheeAPIKey           = "api_key" // pragma: allowlist secret -- attachee-kind identifier, not a credential
	attacheeWorkloadIdentity = "workload_identity"
	attacheeGroup            = "group"
)

// IAMPolicyAttachmentModel is the Terraform state model for a policy attachment.
// ID is a synthetic composite "policyId/attacheeType/attacheeId".
type IAMPolicyAttachmentModel struct {
	ID           types.String `tfsdk:"id"`
	PolicyID     types.String `tfsdk:"policy_id"`
	AttacheeType types.String `tfsdk:"attachee_type"`
	AttacheeID   types.String `tfsdk:"attachee_id"`
	AttachedAt   types.String `tfsdk:"attached_at"`
}

// apiAttachment is the identity representation of an attachment (list element).
type apiAttachment struct {
	PolicyID     string `json:"policyId"`
	AttacheeType string `json:"attacheeType"`
	AttacheeID   string `json:"attacheeId"`
	AttachedAt   string `json:"attachedAt"`
}

// apiAttachmentList is the keyset-paginated attachment list envelope.
type apiAttachmentList struct {
	Attachments []apiAttachment `json:"attachments"`
	NextCursor  string          `json:"nextCursor"`
}

// apiAttachRequest attaches a policy to a principal or group.
type apiAttachRequest struct {
	AttacheeType string `json:"attacheeType"`
	AttacheeID   string `json:"attacheeId"`
}
