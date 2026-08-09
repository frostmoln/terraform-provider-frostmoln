package gateway

import (
	"errors"

	"github.com/hashicorp/terraform-plugin-framework/diag"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
)

// The network service's gateway error codes. Each one means something a
// generic "Failed to create gateway: <code>: <message>" diagnostic
// cannot express — chiefly whether the practitioner should change their
// configuration, change nothing and retry, or import something that already
// exists.
const (
	errCodeModeUnavailable = "GATEWAY_MODE_UNAVAILABLE"
	errCodeGatewayExists   = "GATEWAY_EXISTS"
	errCodeGatewayInUse    = "GATEWAY_IN_USE"
	errCodeLossNotAcked    = "GATEWAY_LOSS_NOT_ACKED"
	errCodePoolExhausted   = "GATEWAY_POOL_EXHAUSTED"

	// errCodePublicIPUnavailable (409) means the named public IP exists and
	// belongs to this tenant, but is not free to become an outbound source
	// address — it is attached to an instance or load balancer, or already
	// serving another VPC's outbound path.
	errCodePublicIPUnavailable = "GATEWAY_PUBLIC_IP_UNAVAILABLE"

	// errCodePublicIPNotAllowed (400) has exactly ONE cause: `publicIpId` was
	errCodePublicIPNotAllowed = "GATEWAY_PUBLIC_IP_NOT_ALLOWED"
)

// addGatewayError appends the best diagnostic available for err. fallback is the
// summary used for an error the gateway surface does not define (transport
// failures, auth, a code added after this build).
//
// The mapping is deliberately not a formatting flourish. Three of these codes
// are routinely mis-presented by clients that pass the raw envelope through:
//
//   - GATEWAY_MODE_UNAVAILABLE is a 400, which reads as "fix your syntax". It is
func addGatewayError(diags *diag.Diagnostics, fallback string, err error) {
	var apiErr *client.APIError
	if !errors.As(err, &apiErr) {
		diags.AddError(fallback, err.Error())
		return
	}

	switch apiErr.Code {
	case errCodeModeUnavailable:
		diags.AddError(
			"Gateway mode is not available",
			"The platform declined to provision the mode named — this is a product statement, not a "+
				"syntax error.\n\n"+
				"If it is `nat`: that mode has been WITHDRAWN and will not come back. It sent the VPC's "+
				"outbound traffic through an address shared with other VPCs, and a VPC on it could not "+
				"give any of its instances a public IP. Use mode = \"public_ip\", which gives the VPC "+
				"its own outbound gateway — name a `public_ip_id` to egress from a public IP of your "+
				"own, or leave it out and the gateway gets an address the platform draws for it. This "+
				"provider refuses `nat` before any request is sent, so reaching this diagnostic means "+
				"`mode` came from an expression that was not known at validate time.\n\n"+
				"`public_ip` is the only mode there is. Nothing else is a mode waiting to be shipped, "+
				"and a VPC that should have NO outbound path at all is this resource NOT DECLARED "+
				"rather than a different value.\n\n"+
				"API said: "+apiErr.Message)
	case errCodeGatewayExists:
		diags.AddError(
			"VPC already has a gateway",
			"A VPC has at most one gateway, and this one already has a gateway in a different "+
				"mode — creating a second is not how the mode is changed.\n\n"+
				"If the existing gateway should be managed by this resource, import it and then set "+
				"`mode` to the value you want (the change is applied in place):\n\n"+
				"    terraform import frostmoln_gateway.<name> <vpc id>\n\n"+
				"API said: "+apiErr.Message)
	case errCodeGatewayInUse:
		diags.AddError(
			"Gateway is still in use",
			"Something in this VPC still depends on the gateway — an address associated here "+
				"cannot exist without one.\n\n"+
				"DETACH FIRST, and only consider releasing after that. Detaching a public IP from its "+
				"instance — clearing `instance_id` on the `frostmoln_public_ip`, or "+
				"`fm network public-ip detach` — clears this refusal, costs nothing, and KEEPS THE "+
				"ADDRESS: it stays yours, so a partner allow-list entry or a DNS record naming it goes "+
				"on matching. Releasing the address instead (destroying the `frostmoln_public_ip`) also "+
				"clears the refusal, but it is PERMANENT: the address returns to a shared pool that "+
				"hands it out at random, and every allow-list and DNS record naming it silently stops "+
				"matching. Nothing here needs an address released.\n\n"+
				"If nothing in your own public IP list accounts for this, do not go looking for "+
				"something to release: the holder is a platform-managed resource inside this VPC — "+
				"typically a managed-service load balancer or control plane — which is not in your "+
				"listing by construction. Delete that resource, or contact support if you cannot "+
				"identify it. The API message below says which of the two cases this is.\n\n"+
				"Where the public IPs ARE being destroyed too — a whole stack coming down — the "+
				"ordering can be Terraform's job rather than yours. Put the dependency on the PUBLIC "+
				"IPs, pointing at the gateway:\n\n"+
				"    resource \"frostmoln_public_ip\" \"example\" {\n"+
				"      # ...\n"+
				"      depends_on = [frostmoln_gateway.example]\n"+
				"    }\n\n"+
				"That changes ORDER only; it releases nothing that was not already being destroyed. "+
				"Terraform destroys dependents first, so this destroys the public IPs before the gateway "+
				"— and creates the gateway before them, which also matters: associating a public IP while "+
				"the VPC has no gateway makes the platform attach one implicitly, and the explicit "+
				"gateway create then fails with GATEWAY_EXISTS. A `depends_on` written the "+
				"other way round (on the gateway, listing the public IPs) reverses both orders and leaves "+
				"this delete failing exactly as it just did.\n\n"+
				"API said: "+apiErr.Message)
	case errCodePoolExhausted:
		diags.AddError(
			"No public IPv4 address is available in this region right now",
			"This is a TEMPORARY platform capacity limit, not your tenant's quota: the region's address "+
				"inventory is momentarily empty. Nothing was changed, and the same configuration succeeds "+
				"once an address frees — re-run `terraform apply`.\n\n"+
				"Only a VPC that needs NO outbound path at all escapes this limit, and that is the "+
				"absence of this resource rather than a different mode.\n\n"+
				"API said: "+apiErr.Message)
	case errCodePublicIPUnavailable:
		diags.AddError(
			"That public IP is not free to be this VPC's outbound source address",
			"The address named in `public_ip_id` is yours, but something else is holding it: it is "+
				"attached to an instance or a load balancer, or it is already the outbound source address "+
				"of another VPC. One address serves one thing at a time.\n\n"+
				"Nothing was changed — this VPC's outbound path is exactly as it was.\n\n"+
				"Detach the address from whatever holds it (the `frostmoln_public_ip` resource's "+
				"`instance_id`, or the other VPC's gateway) and apply again, or point "+
				"`public_ip_id` at a public IP that is not in use.\n\n"+
				"Omitting the attribute entirely is a different outcome, not a shortcut to the same "+
				"one: the gateway then gets an address the PLATFORM draws for it. That address is not a "+
				"public IP of yours — it has no id, it is not in your public IP list, and nothing pins "+
				"it, so it can change if the gateway is rebuilt. Take it when nothing outside the VPC "+
				"needs to know the source address; name a `public_ip_id` when something does.\n\n"+
				"API said: "+apiErr.Message)
	case errCodePublicIPNotAllowed:
		diags.AddError(
			"public_ip_id cannot be used with the withdrawn mode = \"nat\"",
			"`mode` resolved to \"nat\" while `public_ip_id` named an address. That mode egresses from "+
				"an address SHARED with other VPCs, so the named address would carry none of this VPC's "+
				"outbound traffic — the platform refuses the pair rather than dropping the field, because "+
				"silently ignoring it would leave you giving a partner an address to allow-list that "+
				"your traffic never arrives from.\n\n"+
				"Nothing was changed.\n\n"+
				"This provider refuses \"nat\" before an apply. It could not here because `mode` came "+
				"from a value that was not known at validate time — a module output, or another "+
				"resource's attribute. Check what that expression resolves to and set "+
				"`mode = \"public_ip\"`: \"nat\" has been withdrawn, so it is not a mode to fall back "+
				"to by dropping `public_ip_id`.\n\n"+
				"API said: "+apiErr.Message)
	case errCodeLossNotAcked:
		diags.AddError(
			"Gateway connectivity loss was not acknowledged",
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
