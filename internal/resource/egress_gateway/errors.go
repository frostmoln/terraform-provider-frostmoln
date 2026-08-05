package egress_gateway

import (
	"errors"

	"github.com/hashicorp/terraform-plugin-framework/diag"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
)

// The network service's egress-gateway error codes. Each one means something a
// generic "Failed to create egress gateway: <code>: <message>" diagnostic
// cannot express — chiefly whether the practitioner should change their
// configuration, change nothing and retry, or import something that already
// exists.
const (
	errCodeModeUnavailable = "EGRESS_MODE_UNAVAILABLE"
	errCodeGatewayExists   = "EGRESS_GATEWAY_EXISTS"
	errCodeGatewayInUse    = "EGRESS_GATEWAY_IN_USE"
	errCodeLossNotAcked    = "EGRESS_GATEWAY_LOSS_NOT_ACKED"
	errCodePoolExhausted   = "EGRESS_POOL_EXHAUSTED"
)

// addEgressError appends the best diagnostic available for err. fallback is the
// summary used for an error the egress surface does not define (transport
// failures, auth, a code added after this build).
//
// The mapping is deliberately not a formatting flourish. Three of these codes
// are routinely mis-presented by clients that pass the raw envelope through:
//
//   - EGRESS_MODE_UNAVAILABLE is a 400, which reads as "your configuration is
//     wrong". It is not: `nat` is a valid mode that this region does not offer
//     yet, and the same configuration applies unchanged once it does.
//   - EGRESS_POOL_EXHAUSTED is a 503 platform inventory limit, TEMPORARY and
//     worth retrying. Presented as a quota error it sends the practitioner to
//     support to ask for something support cannot grant.
//   - EGRESS_GATEWAY_EXISTS means the object exists and Terraform does not know
//     about it — an import, not a second create.
func addEgressError(diags *diag.Diagnostics, fallback string, err error) {
	var apiErr *client.APIError
	if !errors.As(err, &apiErr) {
		diags.AddError(fallback, err.Error())
		return
	}

	switch apiErr.Code {
	case errCodeModeUnavailable:
		diags.AddError(
			"Egress gateway mode not offered in this region yet",
			"The requested mode is a valid mode this region cannot provision yet — this is not "+
				"invalid configuration, and nothing needs to be corrected for a region that does offer it.\n\n"+
				"`nat` is a per-region capability, and this region does not offer it yet. "+
				"Use mode = \"public_ip\" to give the VPC a dedicated public IPv4 as its source address "+
				"(this spends one address from your tenant's public IP quota), or keep this configuration "+
				"and apply it once the region offers NAT.\n\n"+
				"API said: "+apiErr.Message)
	case errCodeGatewayExists:
		diags.AddError(
			"VPC already has an egress gateway",
			"A VPC has at most one egress gateway, and this one already has a gateway in a different "+
				"mode — creating a second is not how the mode is changed.\n\n"+
				"If the existing gateway should be managed by this resource, import it and then set "+
				"`mode` to the value you want (the change is applied in place):\n\n"+
				"    terraform import frostmoln_egress_gateway.<name> <vpc id>\n\n"+
				"API said: "+apiErr.Message)
	case errCodeGatewayInUse:
		diags.AddError(
			"Egress gateway is still in use",
			"Public IPs in this VPC depend on the egress gateway — an associated public IP cannot exist "+
				"without one. Release or disassociate them (`frostmoln_public_ip`) before removing the "+
				"gateway.\n\nTo make Terraform do that ordering itself, put the dependency on the PUBLIC "+
				"IPs, pointing at the gateway:\n\n"+
				"    resource \"frostmoln_public_ip\" \"example\" {\n"+
				"      # ...\n"+
				"      depends_on = [frostmoln_egress_gateway.example]\n"+
				"    }\n\n"+
				"Terraform destroys dependents first, so this destroys the public IPs before the gateway "+
				"— and creates the gateway before them, which also matters: associating a public IP while "+
				"the VPC has no gateway makes the platform attach one implicitly, and a later "+
				"mode = \"nat\" create then fails with EGRESS_GATEWAY_EXISTS. A `depends_on` written the "+
				"other way round (on the gateway, listing the public IPs) reverses both orders and leaves "+
				"this delete failing exactly as it just did.\n\n"+
				"API said: "+apiErr.Message)
	case errCodePoolExhausted:
		diags.AddError(
			"No public IPv4 address is available in this region right now",
			"This is a TEMPORARY platform capacity limit, not your tenant's quota: the region's address "+
				"inventory is momentarily empty. Nothing was changed, and the same configuration succeeds "+
				"once an address frees — re-run `terraform apply`.\n\n"+
				"Where the region offers it, mode = \"nat\" needs no address at all and is not affected "+
				"by this limit.\n\n"+
				"API said: "+apiErr.Message)
	case errCodeLossNotAcked:
		diags.AddError(
			"Egress gateway connectivity loss was not acknowledged",
			"The API refused the change because it costs this VPC reachability. Set "+
				"`acknowledge_connectivity_loss = true` on the resource and apply that first.\n\n"+
				"API said: "+apiErr.Message)
	default:
		diags.AddError(fallback, apiErr.Error())
	}
}

// connectivityLossWarning is the consequence every removal and every mode change
// carries, in one place so the destroy refusal, the mode-change refusal and the
// schema documentation cannot drift apart. It is not only the internet: the
// platform DNS resolver and the managed-service path are reached over routes
// that exist only while the gateway does.
//
// This string is customer-facing (it renders into the Terraform Registry docs),
// so it names what a customer loses — DNS resolution, managed-service
// connectivity — and never the internal components that provide them.
const connectivityLossWarning = "Removing this gateway, or changing its mode, takes the VPC's outbound internet path " +
	"down — and with it platform DNS resolution and managed-service connectivity, which are reached " +
	"over routes that exist only while the gateway does. Instances in the VPC lose name resolution, " +
	"not just internet access."
