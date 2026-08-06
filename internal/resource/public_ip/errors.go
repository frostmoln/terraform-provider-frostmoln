package public_ip

import (
	"errors"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/diag"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
)

// errCodeInUseByEgressGateway (409) is the platform refusing to release — or to
// attach to an instance — an address that is a VPC's outbound source address.
//
// Passed through raw it reads as an obscure conflict on an address that, by
// every other signal the practitioner has, looks unused: it has no instance and
// no port, because an egress source address is not an instance's address. The
// refusal is the platform preventing a whole VPC from losing its internet path,
// its DNS resolution and its managed-service connectivity, and it says so.
const errCodeInUseByEgressGateway = "PUBLIC_IP_IN_USE_BY_EGRESS_GATEWAY"

// The refusal reaches this provider through two different mechanisms — an error
// envelope on a synchronous rejection, an operation's error string on the
// asynchronous path — and both must read the same. One text, so they cannot
// drift apart.
const inUseByEgressGatewaySummary = "This public IP is a VPC's outbound source address"

const inUseByEgressGatewayDetail = "The platform refused the change because this address is not idle: a VPC's outbound " +
	"traffic leaves the platform from it. It has no instance and no port for exactly that " +
	"reason, which is why it can look unused.\n\n" +
	"Nothing was changed. Releasing it, or attaching it to an instance, would take that VPC's " +
	"internet path down and with it platform DNS resolution and managed-service connectivity " +
	"for every instance in the VPC — and the ADDRESS itself would be gone for good: it returns " +
	"to a shared regional pool and is re-issued to whoever asks for one next.\n\n" +
	"Change the VPC's egress gateway first (`frostmoln_egress_gateway`): point its " +
	"`public_ip_id` at a different address, or set `mode = \"nat\"`, which needs no address at " +
	"all. Terraform sequences that for you when the gateway's `public_ip_id` refers to this " +
	"resource — the gateway is then changed or destroyed before this address is.\n\n"

// addPublicIPError appends the best diagnostic available for a SYNCHRONOUS
// failure — one carried by the HTTP response itself. fallback is the summary
// used for anything this surface does not define.
func addPublicIPError(diags *diag.Diagnostics, fallback string, err error) {
	var apiErr *client.APIError
	if !errors.As(err, &apiErr) {
		diags.AddError(fallback, err.Error())
		return
	}

	if apiErr.Code != errCodeInUseByEgressGateway {
		diags.AddError(fallback, apiErr.Error())
		return
	}

	diags.AddError(inUseByEgressGatewaySummary, inUseByEgressGatewayDetail+"API said: "+apiErr.Message)
}

// addPublicIPOperationError appends the best diagnostic available for an
// ASYNCHRONOUS failure — one reported by a provisioning operation rather than
// by the HTTP response that started it.
//
// It matches on the error TEXT, not on a typed code, because on that path there
// is no typed code to match: the write endpoints answer 202 with an operation
// id, the real outcome lands on the operation, and WaitForOperation surfaces
// the operation's error message as a plain string. A substring match is weaker
// than a code comparison, so the fallback carries the operation's message
// verbatim — a missed match costs presentation, never information.
func addPublicIPOperationError(diags *diag.Diagnostics, fallback string, err error) {
	if strings.Contains(err.Error(), errCodeInUseByEgressGateway) {
		diags.AddError(inUseByEgressGatewaySummary, inUseByEgressGatewayDetail+"The platform said: "+err.Error())
		return
	}
	diags.AddError(fallback, err.Error())
}
