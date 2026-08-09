// Package gateway implements the frostmoln_gateway Terraform resource.
package gateway

import (
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// Gateway modes, as the network service's enum spells them.
const (
	// ModePublicIP is the only mode a configuration can set. The VPC gets its
	// own outbound gateway; naming a `public_ip_id`
	// makes that address one of the tenant's own — stable, and the address to
	// hand a partner for an allow-list.
	ModePublicIP = "public_ip"
)

// OriginExplicitPublicIP is the `origin` the platform records for a gateway
// whose source address is a public IP the tenant chose, rather than one the
// platform drew on their behalf. It is documented on the `origin` attribute and
// named here so the schema text and this package agree on the spelling.
const OriginExplicitPublicIP = "explicit_public_ip"

// GatewayModel is the Terraform state model for a VPC's outbound path.
type GatewayModel struct {
	ID            types.String `tfsdk:"id"`
	VPCID         types.String `tfsdk:"vpc_id"`
	Mode          types.String `tfsdk:"mode"`
	SourceAddress types.String `tfsdk:"source_address"`
	Status        types.String `tfsdk:"status"`
	Origin        types.String `tfsdk:"origin"`

	// PublicIPID names the tenant's own public IP that this gateway egresses
	// from. Optional and Computed, and the two cases are NOT the same product:
	//
	//   - Named: a real public IP of the tenant's. It has an id, it is listed,
	//     it draws on their public IP quota, it is metered, and it is PINNED as
	//     this gateway's source address.
	//   - Omitted: the platform draws the gateway an address of its own
	//     (ADR-0114). No id comes back, so this attribute stays NULL. It is not
	//     a public IP resource of the tenant's in any sense — not listed, not
	//     quota-drawn, not metered, not on an invoice — and nothing pins it, so
	//     it may change if the gateway is rebuilt.
	//
	// Computed is therefore about the NAMED case (the API echoes the id back),
	// never about the platform handing back an allocation for the omitted one.
	//
	// It is NOT a replacement trigger. Re-pointing a gateway at a different
	// address is applied in place — the platform re-addresses the existing
	// path — and a RequiresReplace here would instead destroy the VPC's only
	// outbound path and build a new one, which is the exact outage this
	// feature exists to prevent.
	PublicIPID types.String `tfsdk:"public_ip_id"`

	// AcknowledgeConnectivityLoss is the practitioner's explicit statement that
	// they accept what removing — or re-addressing — this gateway costs. It is
	// config, not a computed value, because the API refuses both operations
	// without it and Terraform has nowhere else to express intent: `terraform
	// destroy` and a `mode` change are both plain plan approvals, and neither
	// tells the practitioner that DNS resolution and managed-service
	// connectivity go down with the internet path.
	AcknowledgeConnectivityLoss types.Bool `tfsdk:"acknowledge_connectivity_loss"`
}

// apiGateway is the API representation.
//
// Deliberately not router-shaped: no router id, no gateway info. Once a field
// ships in a provider schema it is a compatibility obligation across every
// practitioner's state, and the gateway is addressed by its own id and its
// VPC's — never by the objects that realise it.
type apiGateway struct {
	ID       string `json:"id"`
	VPCID    string `json:"vpcId"`
	TenantID string `json:"tenantId"`
	Mode     string `json:"mode"`
	// SourceAddress is absent while the gateway is detached or the address is
	// not yet known — which is why it maps to a NULL Terraform value rather
	// than "" (see applyToModel).
	SourceAddress string `json:"sourceAddress,omitempty"`
	Status        string `json:"status"`
	Origin        string `json:"origin"`
	// PublicIPID is present ONLY where the tenant named an address. It is absent
	// on a withdrawn-`nat` gateway, and absent under `public_ip` for every
	// gateway running on a platform-drawn address — there is no public IP
	// resource for the API to name, so it omits the field rather than inventing
	// one. Absent maps to a NULL Terraform value, never "".
	PublicIPID string `json:"publicIpId,omitempty"`
}

// apiGatewayList is the filtered-list response. A VPC with no gateway
// returns an empty list rather than 404, which is what lets Read treat "gone"
// as "removed from state" without conflating it with a transport error.
type apiGatewayList struct {
	Gateways   []apiGateway `json:"gateways"`
	TotalCount int          `json:"totalCount"`
}

type apiCreateGatewayRequest struct {
	VPCID string `json:"vpcId"`
	Mode  string `json:"mode"`
	// PublicIPID is omitted when the practitioner did not choose an address.
	// Omitted is NOT the same as empty: the platform reads an absent field as
	// "draw the gateway an address yourself", which is what makes
	// `mode = "public_ip"` keep working unchanged for every configuration
	// written before this attribute existed. What it does NOT do is create a
	// public IP for the tenant — that address has no id, is not listed, and is
	// not pinned (ADR-0114); only a named id buys a tenant-owned, stable one.
	PublicIPID string `json:"publicIpId,omitempty"`
}

type apiUpdateGatewayRequest struct {
	Mode string `json:"mode"`
	// PublicIPID is omitted unless the practitioner named an address. See the
	// create request: omitted means "the platform draws the address", not
	// "detach".
	PublicIPID string `json:"publicIpId,omitempty"`
	// AcknowledgeConnectivityLoss carries the practitioner's acknowledgement
	// from the resource's attribute of the same name. The provider never
	// hardcodes it: a mode change re-attaches the VPC's only path off-net — the
	// move a gateway left on the withdrawn `nat` mode has to make — dropping
	// in-flight connections along with DNS and managed-service reachability, so
	// the intent has to be stated in the configuration rather than inferred
	// from the fact that an apply was approved.
	AcknowledgeConnectivityLoss bool `json:"acknowledgeConnectivityLoss"`
}

// noIDDetail explains why an identity-less state row is refused rather than
// used to build a request. It is shared by the Update and Delete guards.
const noIDDetail = "This resource's state row has no `id`, so there is nothing to address. A request built " +
	"from it would go to the gateway COLLECTION instead — the empty id is cleaned straight " +
	"out of the path — and the collection answers as if this gateway did not exist, which the " +
	"provider would record as \"already gone\" for a gateway that is still up and still spending " +
	"an address. Nothing has been changed.\n\n" +
	"Re-import the gateway before retrying:\n\n" +
	"    terraform import frostmoln_gateway.<name> <vpc id>"

// applyToModel copies an API gateway onto the Terraform model.
//
// It refuses two responses rather than writing them to state:
//
//   - One with no `id` or no `vpcId`. Both are required by the API contract, so
//     an empty one means the body was not the object that was asked for. Storing
//     it is worse than failing: every later request builds its path from the id,
//     and "/gateways/" with an empty id is cleaned back to the COLLECTION
//     path, so the DELETE 404s, IsNotFound reads that as "already gone", and the
//     provider drops a live gateway out of state.
//   - One that names a DIFFERENT gateway or VPC than the state row already
//     bound to. Silently re-pointing the row at whatever came back is how a
//     resource ends up PATCHing or DELETING another VPC's internet path; the two
//     ids belong in a diagnostic, not in state.
//
// The one legitimate id mismatch is the import path: GET /gateways/{id}
// accepts the VPC's id as well, so a row imported by VPC id holds that id until
// the first read rebinds it to the gateway's own id.
//
// sourceAddress maps to null when absent: the API omits it while the gateway is
// detached or the address is not yet known, and "" is a value a practitioner
// could interpolate into a firewall rule.
func applyToModel(m *GatewayModel, gw *apiGateway) diag.Diagnostics {
	var diags diag.Diagnostics

	if gw.ID == "" || gw.VPCID == "" {
		diags.AddError(
			"Incomplete gateway response",
			fmt.Sprintf("The API returned a gateway with no %s. Both are required on this "+
				"resource, so this response cannot be written to state — a state row without them "+
				"addresses the whole collection on the next request, and a gateway that is still up "+
				"would be recorded as gone. Nothing has been changed.",
				missingIdentityFields(gw)),
		)
		return diags
	}

	// Never rebind. An id that matches the response's vpcId is the import path
	// (see the doc comment), not a mismatch.
	if id := m.ID.ValueString(); id != "" && id != gw.ID && id != gw.VPCID {
		diags.AddError(
			"Gateway identity does not match state",
			fmt.Sprintf("This resource tracks gateway %q, but the API answered with gateway %q "+
				"(VPC %q). The provider will not re-point the resource at a different gateway: doing so "+
				"would put a later mode change or destroy on an object nobody asked it to touch. "+
				"Nothing has been changed.\n\nRemove the resource from state and re-import the gateway "+
				"you mean to manage.", id, gw.ID, gw.VPCID),
		)
	}
	if vpcID := m.VPCID.ValueString(); vpcID != "" && vpcID != gw.VPCID {
		diags.AddError(
			"Gateway belongs to a different VPC",
			fmt.Sprintf("This resource tracks the gateway of VPC %q, but the API answered with "+
				"the gateway of VPC %q. The provider will not re-point the resource at another VPC's "+
				"outbound path: a later mode change or destroy would then take THAT VPC's internet, DNS "+
				"and managed-service connectivity down. Nothing has been changed.\n\nCheck that `vpc_id` "+
				"still names the VPC you mean and that the API key is scoped to its tenant.",
				vpcID, gw.VPCID),
		)
	}
	if diags.HasError() {
		return diags
	}

	m.ID = types.StringValue(gw.ID)
	m.VPCID = types.StringValue(gw.VPCID)
	m.Mode = types.StringValue(gw.Mode)
	m.Status = types.StringValue(gw.Status)
	m.Origin = types.StringValue(gw.Origin)

	if gw.PublicIPID == "" {
		m.PublicIPID = types.StringNull()
	} else {
		m.PublicIPID = types.StringValue(gw.PublicIPID)
	}

	if gw.SourceAddress == "" {
		m.SourceAddress = types.StringNull()
		return diags
	}
	m.SourceAddress = types.StringValue(gw.SourceAddress)
	return diags
}

// checkRequestedPublicIP refuses a response that bound a DIFFERENT public IP
// than the one the practitioner named.
//
// Terraform core would catch the divergence too — a known planned value that
// the apply contradicts is "Provider produced inconsistent result after apply"
// — but it reports it as a provider bug and says nothing about what is now
// true in the cloud. The consequence here is specific and worth naming: the
// VPC is egressing from an address the configuration does not mention, so the
// address the practitioner published to a partner is not the address their
// traffic arrives from, and the address they did name may still be attached to
// something else.
//
// requested is the plan value. Null or unknown means the practitioner asked
// the platform to choose, so anything it chose is by definition correct.
func checkRequestedPublicIP(requested types.String, gw *apiGateway) diag.Diagnostics {
	var diags diag.Diagnostics

	if requested.IsNull() || requested.IsUnknown() {
		return diags
	}
	want := requested.ValueString()
	if want == "" || want == gw.PublicIPID {
		return diags
	}

	got := gw.PublicIPID
	if got == "" {
		got = "no public IP at all (the platform reported none)"
	} else {
		got = fmt.Sprintf("public IP %q", got)
	}
	diags.AddError(
		"Gateway was not attached to the requested public IP",
		fmt.Sprintf("The configuration names public IP %q as this VPC's outbound source address, but the "+
			"platform answered with %s. Terraform has NOT recorded the requested value as if it had "+
			"been applied.\n\nThe VPC is egressing from an address the configuration does not name: "+
			"anything you published for a partner to allow-list, or in DNS, may no longer match the "+
			"address your traffic arrives from. Check the gateway and the public IP "+
			"(`fm network gateway show` / `fm network public-ip list`), then re-apply.",
			want, got),
	)
	return diags
}

// missingIdentityFields names which of the two required identity fields the
// response left empty, so the diagnostic says which one rather than "one of".
func missingIdentityFields(gw *apiGateway) string {
	switch {
	case gw.ID == "" && gw.VPCID == "":
		return "`id` and no `vpcId`"
	case gw.ID == "":
		return "`id`"
	default:
		return "`vpcId`"
	}
}
