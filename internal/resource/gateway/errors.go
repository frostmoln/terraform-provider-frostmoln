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
	errCodeGatewayExists = "GATEWAY_EXISTS"
	errCodeGatewayInUse  = "GATEWAY_IN_USE"
	errCodeLossNotAcked  = "GATEWAY_LOSS_NOT_ACKED"
	errCodePoolExhausted = "GATEWAY_POOL_EXHAUSTED"

	// errCodePublicIPUnavailable (409) means the named public IP exists and
	// belongs to this tenant, but is not free to become an outbound source
	// address — it is attached to an instance or load balancer, or already
	// serving another VPC's outbound path.
	errCodePublicIPUnavailable = "GATEWAY_PUBLIC_IP_UNAVAILABLE"

	// GATEWAY_MODE_UNAVAILABLE and GATEWAY_PUBLIC_IP_NOT_ALLOWED were mapped
	// here until ADR-0114. Both existed only to explain the withdrawn `nat`
	// mode, and network no longer defines either — the codes it can emit are
	// exactly the ones above plus the two acknowledgement codes. A branch for a
	// code the server cannot send is untestable against reality, and the prose
	// it carried described a mode no customer ever used.
)

// addGatewayError appends the best diagnostic available for err. fallback is the
// summary used for an error the gateway surface does not define (transport
// failures, auth, a code added after this build).
//
// The mapping is deliberately not a formatting flourish. These codes are
// routinely mis-presented by clients that pass the raw envelope through:
func addGatewayError(diags *diag.Diagnostics, fallback string, err error) {
	var apiErr *client.APIError
	if !errors.As(err, &apiErr) {
		diags.AddError(fallback, err.Error())
		return
	}

	switch apiErr.Code {
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
