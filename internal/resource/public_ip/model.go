// Package public_ip implements the fm_public_ip Terraform resource.
package public_ip

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// What a public IP can be attached to. One address serves one thing at a time,
// so these are mutually exclusive.
const (
	// AttachmentKindNone is an allocated address that nothing is using. It is
	// still yours, still counted against quota, and still billed.
	AttachmentKindNone = "none"

	// AttachmentKindPort is the familiar case: the address is attached to an
	// instance's (or load balancer's) network port and is that resource's
	// inbound address.
	AttachmentKindPort = "port"

	// AttachmentKindGateway means the address IS a VPC's outbound source
	// address — the whole VPC leaves the platform from it. Releasing it, or
	// attaching it to an instance, is refused while that lasts.
	AttachmentKindGateway = "gateway"

	// AttachmentKindUnknown is SYNTHESIZED BY THIS PROVIDER, never sent by the
	// platform. It is what an API response that carried no attachment object at
	// all becomes.
	//
	// The tempting reading of an absent object is "attached to nothing", and it
	// is wrong in the one direction that costs something irreversible: a
	// platform rolled back below the version that reports attachments still has
	// gateway-attached addresses, and would answer for one of them with no
	// object rather than with `gateway`. Calling that "none" reassures a
	// practitioner that an address is free at the exact moment it is not, and
	// releasing it is unrecoverable. So it says it does not know.
	AttachmentKindUnknown = "unknown"
)

// AttachmentAttrTypes is the object type of the `attachment` attribute. It is
// exported so the data source builds the identical object rather than a
// second, drifting copy.
var AttachmentAttrTypes = map[string]attr.Type{
	"kind":        types.StringType,
	"resource_id": types.StringType,
	"vpc_id":      types.StringType,
}

// PublicIPModel is the Terraform state model for a public IP.
type PublicIPModel struct {
	ID         types.String `tfsdk:"id"`
	Address    types.String `tfsdk:"address"`
	InstanceID types.String `tfsdk:"instance_id"`
	Tags       types.Map    `tfsdk:"tags"`
	Status     types.String `tfsdk:"status"`
	PrivateIP  types.String `tfsdk:"private_ip"`
	CreatedAt  types.String `tfsdk:"created_at"`

	// Attachment says what is holding this address. It is never null, so
	// `attachment.kind` is always readable in a configuration — an address
	// nothing holds reports "none", and one the platform did not report on
	// reports "unknown", rather than an absent object.
	Attachment types.Object `tfsdk:"attachment"`

	// AcknowledgeAddressLoss is the practitioner's explicit statement that they
	// accept losing this ADDRESS — not merely this resource.
	//
	// It exists because destroying an gateway-attached public IP is the one
	// irreversible action on this surface, and nothing else makes it visible.
	// Terraform renders an ordinary "will be destroyed" line, `instance_id` is
	// empty (an outbound source address has no instance), and the platform's own
	// refusal CANNOT fire on the path a correct configuration produces:
	// `frostmoln_gateway.public_ip_id` makes the gateway depend on this
	// resource, so Terraform destroys the gateway FIRST, the platform hands the
	// address back as an ordinary unattached address, and the release that
	// follows is — to the platform — the release of something idle. The address
	// returns to a shared pool and is re-issued to whoever asks next.
	//
	// So the acknowledgement is asked for here, from the recorded attachment,
	// before the request that cannot be taken back.
	AcknowledgeAddressLoss types.Bool `tfsdk:"acknowledge_address_loss"`
}

// IsGatewayBound reports whether the recorded attachment says this address is a
// VPC's outbound source.
func (m *PublicIPModel) IsGatewayBound() bool {
	return m.AttachmentKind() == AttachmentKindGateway
}

// AttachmentKind is the recorded attachment kind, or "" when no attachment has
// been recorded at all (a null or unknown object — a state row written before
// this attribute existed, or a plan value not yet resolved).
func (m *PublicIPModel) AttachmentKind() string {
	return m.attachmentString("kind")
}

// RequiresAddressLossAcknowledgement reports whether destroying this address
// would silently lose something the practitioner cannot get back, and so must
// not proceed on a plan approval alone.
//
// The default for an UNRECOGNISED kind is the whole point of the switch. A kind
// this build does not know is not the same as no answer: it is the platform
// stating positively that SOMETHING holds the address, in a vocabulary that
// grew after this provider was compiled — which the platform's own type
// documents as expected (`network/internal/domain/public_ip.go`: "a new kind
// does not change the schema"). Reading an unrecognised kind as "free" would
// make every future attachment kind silently destroyable by an old provider,
// which is exactly the failure this gate exists to prevent. So the unknown
// case fails CLOSED.
//
// The three that do not need it, and why:
//
//   - "none": nothing holds the address. Releasing it is the ordinary,
//     long-standing behaviour of this resource.
//   - "port": the address belongs to an instance or load balancer whose
//     association this same resource manages through `instance_id`. Destroying
//     the resource has always released it, the plan shows the instance losing
//     its address, and gating it now would break every existing configuration.
//   - "unknown" / "": the platform did not say. Refusing here would block
//     ordinary destroys against any build that does not report attachments,
//     which is a real breaking change made on a guess. It does not reassure
//     either — see AttachmentKindUnknown.
func (m *PublicIPModel) RequiresAddressLossAcknowledgement() bool {
	switch m.AttachmentKind() {
	case "", AttachmentKindNone, AttachmentKindPort, AttachmentKindUnknown:
		return false
	default:
		return true
	}
}

// AttachedVPCID is the VPC whose outbound path this address serves, or "".
func (m *PublicIPModel) AttachedVPCID() string {
	return m.attachmentString("vpc_id")
}

func (m *PublicIPModel) attachmentString(name string) string {
	if m.Attachment.IsNull() || m.Attachment.IsUnknown() {
		return ""
	}
	v, ok := m.Attachment.Attributes()[name]
	if !ok {
		return ""
	}
	s, ok := v.(types.String)
	if !ok || s.IsNull() || s.IsUnknown() {
		return ""
	}
	return s.ValueString()
}

// apiPublicIP is the API representation of a public IP. The network service
// serializes the address as `publicIpAddress` and the fixed IP as
// `fixedIpAddress`; the attached port is `portId` (there is no instanceId or
// region on the wire). See network/internal/domain/public_ip.go.
type apiPublicIP struct {
	ID        string            `json:"id"`
	Address   string            `json:"publicIpAddress"`
	Status    string            `json:"status"`
	PortID    string            `json:"portId,omitempty"`
	PrivateIP string            `json:"fixedIpAddress,omitempty"`
	Tags      map[string]string `json:"tags,omitempty"`
	CreatedAt string            `json:"createdAt"`

	// Attachment is what the address is being used for. It is a pointer
	// because an API that predates it omits the object entirely, which is a
	// different thing from "attached to nothing" — see AttachmentObject.
	Attachment *apiPublicIPAttachment `json:"attachment,omitempty"`
}

// apiPublicIPAttachment is the explicit statement of what holds the address.
// It exists because `portId` alone cannot tell an unattached address from a
// VPC's outbound source address: an gateway-attached public IP has no port, so a
// client inferring "available" from an empty portId offers to release an
// address a whole VPC is egressing from.
type apiPublicIPAttachment struct {
	Kind       string `json:"kind"`
	ResourceID string `json:"resourceId,omitempty"`
	VPCID      string `json:"vpcId,omitempty"`
}

// apiAllocatePublicIPRequest is the API request to allocate a public IP.
// The allocate routes through provisioning, which infers the external network
// from the project; region is not part of the contract (ADR-0022).
type apiAllocatePublicIPRequest struct {
	Tags map[string]string `json:"tags,omitempty"`
}

// apiAssociatePublicIPRequest is the API request to associate a public IP.
// The provisioning associate handler requires a `portId` (binding:"required");
// the provider resolves it from the target instance's first network port.
type apiAssociatePublicIPRequest struct {
	PortID string `json:"portId"`
}

// apiInstanceForPort is the subset of the instance read response used to resolve
// the Neutron port to associate a public IP with (networks[].portId).
type apiInstanceForPort struct {
	Networks []struct {
		PortID string `json:"portId,omitempty"`
	} `json:"networks,omitempty"`
}

// apiUpdatePublicIPRequest is the API request to update tags on a public IP.
type apiUpdatePublicIPRequest struct {
	Tags map[string]string `json:"tags,omitempty"`
}

// toAllocateRequest converts the Terraform model to an API allocate request.
func (m *PublicIPModel) toAllocateRequest(ctx context.Context, diags *diag.Diagnostics) apiAllocatePublicIPRequest {
	req := apiAllocatePublicIPRequest{}

	if !m.Tags.IsNull() && !m.Tags.IsUnknown() {
		tags := make(map[string]string)
		diags.Append(m.Tags.ElementsAs(ctx, &tags, false)...)
		req.Tags = tags
	}

	return req
}

// fromAPI populates the Terraform model from an API response.
func (m *PublicIPModel) fromAPI(ctx context.Context, fip *apiPublicIP, diags *diag.Diagnostics) {
	m.ID = types.StringValue(fip.ID)
	m.Address = types.StringValue(fip.Address)
	m.Status = types.StringValue(fip.Status)
	m.CreatedAt = types.StringValue(fip.CreatedAt)

	// The backend never returns instanceId (only portId). instance_id is the
	// user's association input, so it is preserved as-is when the FIP is attached
	// (portId present) and cleared when the FIP is detached (no portId).
	if fip.PortID == "" {
		m.InstanceID = types.StringNull()
	}

	if fip.PrivateIP != "" {
		m.PrivateIP = types.StringValue(fip.PrivateIP)
	} else {
		m.PrivateIP = types.StringNull()
	}

	m.Attachment = AttachmentObject(fip, diags)

	if len(fip.Tags) > 0 {
		tagsMap, d := types.MapValueFrom(ctx, types.StringType, fip.Tags)
		diags.Append(d...)
		m.Tags = tagsMap
	} else if m.Tags.IsNull() {
		m.Tags = types.MapNull(types.StringType)
	} else {
		m.Tags = types.MapNull(types.StringType)
	}
}

// AttachmentObject renders the attachment as a Terraform object, always
// non-null so `attachment.kind` is readable in every configuration.
//
// An API response with no `attachment` at all is one that does not report
// attachments. Only ONE inference is drawn from that: a non-empty `portId` is
// positive evidence the address is attached to a port, which is what `portId`
// meant before the object existed. The absence of a port is NOT evidence of the
// absence of an attachment — an outbound source address has never had one — so
// that case becomes "unknown", not "none". Getting this backwards is how a
// practitioner is reassured that a VPC's outbound source address is idle.
//
// A present object whose `kind` is empty is likewise not a claim that the
// address is free; it is an answer that said nothing.
func AttachmentObject(fip *apiPublicIP, diags *diag.Diagnostics) types.Object {
	att := fip.Attachment
	if att == nil {
		att = &apiPublicIPAttachment{Kind: AttachmentKindUnknown}
		if fip.PortID != "" {
			att = &apiPublicIPAttachment{Kind: AttachmentKindPort, ResourceID: fip.PortID}
		}
	}

	kind := att.Kind
	if kind == "" {
		kind = AttachmentKindUnknown
	}

	obj, d := types.ObjectValue(AttachmentAttrTypes, map[string]attr.Value{
		"kind":        types.StringValue(kind),
		"resource_id": nullIfEmpty(att.ResourceID),
		"vpc_id":      nullIfEmpty(att.VPCID),
	})
	diags.Append(d...)
	return obj
}

// nullIfEmpty keeps "" out of state: an absent id is nothing, and "" is a value
// a practitioner could interpolate somewhere it would be silently accepted.
func nullIfEmpty(s string) types.String {
	if s == "" {
		return types.StringNull()
	}
	return types.StringValue(s)
}
