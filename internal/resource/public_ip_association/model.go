// Package public_ip_association implements the frostmoln_public_ip_association
// Terraform resource: the attachment of an ALREADY-ALLOCATED public IP to an
// instance, managed independently of the address itself.
package public_ip_association

import (
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/types"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/resource/public_ip"
)

// PublicIPAssociationModel is the Terraform state model for a public IP
// association.
type PublicIPAssociationModel struct {
	ID         types.String `tfsdk:"id"`
	PublicIPID types.String `tfsdk:"public_ip_id"`
	InstanceID types.String `tfsdk:"instance_id"`
	PortID     types.String `tfsdk:"port_id"`
}

// apiPublicIP is the SUBSET of the public IP read response this resource needs:
// what the address is bound to, in the platform's two ways of saying it.
//
// `portId` answers "is this address still on the port I bound?" and nothing
// else. It is NOT evidence about anything but ports: an address that has been
// re-purposed as a VPC's outbound source has no port either, so an empty
// `portId` alone cannot tell "nothing holds it" from "a whole VPC egresses
// through it". `attachment` is the platform's explicit statement of what holds
// it and is shipped unconditionally with `kind` always populated
// (network/internal/domain/public_ip.go), which is why both are read here.
type apiPublicIP struct {
	ID     string `json:"id"`
	PortID string `json:"portId,omitempty"`

	// Attachment is a pointer because an API that predates it omits the object
	// entirely — a different thing from "attached to nothing".
	Attachment *apiPublicIPAttachment `json:"attachment,omitempty"`
}

// apiPublicIPAttachment is what the platform says is holding the address.
type apiPublicIPAttachment struct {
	Kind       string `json:"kind"`
	ResourceID string `json:"resourceId,omitempty"`
	VPCID      string `json:"vpcId,omitempty"`
}

// attachmentKind is what holds the address, with the same reading
// frostmoln_public_ip applies (see public_ip.AttachmentObject): a response that
// carried no attachment object supports exactly ONE inference — a non-empty
// `portId` means a port — and becomes "unknown" otherwise, never "none".
func (f *apiPublicIP) attachmentKind() string {
	if f.Attachment == nil || f.Attachment.Kind == "" {
		if f.PortID != "" {
			return public_ip.AttachmentKindPort
		}
		return public_ip.AttachmentKindUnknown
	}
	return f.Attachment.Kind
}

// heldByNonPort reports whether the platform states POSITIVELY that something
// which is not a network port holds this address — a VPC gateway today, a kind
// added after this build tomorrow.
//
// It is what stops Delete reporting a clean destroy for an address whose port
// went away because it became a VPC's outbound source: "no port" is then true
// and "detached" is a lie. "unknown" is not in the set on purpose — the
// platform did not say, and warning on every already-detached destroy against a
// build that does not report attachments would be noise, not information.
func (f *apiPublicIP) heldByNonPort() bool {
	switch f.attachmentKind() {
	case public_ip.AttachmentKindNone, public_ip.AttachmentKindPort, public_ip.AttachmentKindUnknown:
		return false
	default:
		return true
	}
}

// holderDescription names what holds the address, for a diagnostic.
func (f *apiPublicIP) holderDescription() string {
	kind := f.attachmentKind()
	if kind == public_ip.AttachmentKindGateway {
		vpc := "(id not reported)"
		if f.Attachment != nil && f.Attachment.VPCID != "" {
			vpc = f.Attachment.VPCID
		}
		return fmt.Sprintf("a VPC gateway — it is the address VPC %s's outbound traffic leaves the "+
			"platform from", vpc)
	}
	if f.Attachment != nil && f.Attachment.ResourceID != "" {
		return fmt.Sprintf("%q (%s)", kind, f.Attachment.ResourceID)
	}
	return fmt.Sprintf("%q", kind)
}

// apiAssociateRequest is the API request to associate a public IP. The
// provisioning associate handler requires a `portId` (binding:"required"); the
// provider resolves it from the target instance's first network port.
type apiAssociateRequest struct {
	PortID string `json:"portId"`
}

// compositeID builds the composite resource ID from the public IP and instance
// IDs. An association has no id of its own on the platform — it is a property
// of the address — so the pair identifies it, the same shape
// frostmoln_volume_attachment uses.
func compositeID(publicIPID, instanceID string) string {
	return publicIPID + "/" + instanceID
}
