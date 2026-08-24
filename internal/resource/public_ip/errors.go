package public_ip

import (
	"errors"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/diag"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
)

// errCodeInUseByGateway (409) is the platform refusing to release — or to
// attach to an instance — an address that is a VPC's outbound source address.
//
// Passed through raw it reads as an obscure conflict on an address that, by
// every other signal the practitioner has, looks unused: it has no instance and
// no port, because an outbound source address is not an instance's address. The
// refusal is the platform preventing a whole VPC from losing its internet path,
// its DNS resolution and its managed-service connectivity, and it says so.
const errCodeInUseByGateway = "PUBLIC_IP_IN_USE_BY_GATEWAY"

// The refusal reaches this provider through two different mechanisms — an error
// envelope on a synchronous rejection, an operation's error string on the
// asynchronous path — and both must read the same. One text, so they cannot
// drift apart.
const inUseByGatewaySummary = "This public IP is a VPC's outbound source address"

const inUseByGatewayDetail = "The platform refused the change because this address is not idle: a VPC's outbound " +
	"traffic leaves the platform from it. It has no instance and no port for exactly that " +
	"reason, which is why it can look unused.\n\n" +
	"Nothing was changed. Releasing it, or attaching it to an instance, would take that VPC's " +
	"internet path down and with it platform DNS resolution and managed-service connectivity " +
	"for every instance in the VPC — and the ADDRESS itself would be gone for good: it returns " +
	"to a shared regional pool and is re-issued to whoever asks for one next.\n\n" +
	"Change the VPC's gateway first (`frostmoln_gateway`): point its " +
	"`public_ip_id` at a different address, or remove the gateway entirely if the VPC needs no " +
	"outbound path at all. Terraform sequences that for you when the gateway's `public_ip_id` refers to this " +
	"resource — the gateway is then changed or destroyed before this address is.\n\n"

// AddAPIError appends the best diagnostic available for a SYNCHRONOUS
// failure — one carried by the HTTP response itself. fallback is the summary
// used for anything this surface does not define.
//
// Exported because `frostmoln_public_ip_association` writes to the same
// endpoints and gets the same refusals; one copy of the gateway text, so the
// two resources cannot describe the identical refusal differently.
func AddAPIError(diags *diag.Diagnostics, fallback string, err error) {
	var apiErr *client.APIError
	if !errors.As(err, &apiErr) {
		diags.AddError(fallback, err.Error())
		return
	}

	if apiErr.Code != errCodeInUseByGateway {
		diags.AddError(fallback, apiErr.Error())
		return
	}

	diags.AddError(inUseByGatewaySummary, inUseByGatewayDetail+"API said: "+apiErr.Message)
}

// AddOperationError appends the best diagnostic available for an
// ASYNCHRONOUS failure — one reported by a provisioning operation rather than
// by the HTTP response that started it.
//
// It matches on the error TEXT, not on a typed code, because on that path there
// is no typed code to match: the write endpoints answer 202 with an operation
// id, the real outcome lands on the operation, and WaitForOperation surfaces
// the operation's error message as a plain string. A substring match is weaker
// than a code comparison, so the fallback carries the operation's message
// verbatim — a missed match costs presentation, never information.
func AddOperationError(diags *diag.Diagnostics, fallback string, err error) {
	AddOperationErrorContext(diags, fallback, "", err)
}

// AddOperationErrorContext is AddOperationError with practitioner context put
// in FRONT of the operation's verbatim message: what was being attempted, and
// what is true now that it failed.
//
// The gateway translation still wins when it matches — that text already says
// both, and says them better than a caller-supplied line could.
func AddOperationErrorContext(diags *diag.Diagnostics, fallback, context string, err error) {
	if strings.Contains(err.Error(), errCodeInUseByGateway) {
		diags.AddError(inUseByGatewaySummary, inUseByGatewayDetail+"The platform said: "+err.Error())
		return
	}
	if context == "" {
		diags.AddError(fallback, err.Error())
		return
	}
	diags.AddError(fallback, context+"\n\nThe platform said: "+err.Error())
}

// AddPortResolutionError renders a failed instance-port lookup as a diagnostic
// that still names the INSTANCE.
//
// InstancePortIDs deliberately returns the client's error unwrapped on a 404 —
// client.IsNotFound is a bare type assertion, so a `%w` prefix would defeat the
// "the instance is gone" branch that reads it. The instance id is therefore
// re-attached HERE, at the point the error becomes practitioner copy, instead of
// in the error value: "not found: not found" tells nobody which instance.
func AddPortResolutionError(diags *diag.Diagnostics, instanceID string, err error) {
	const summary = "Failed to Resolve Instance Port"
	if client.IsNotFound(err) {
		diags.AddError(summary, fmt.Sprintf(
			"Instance %s was not found, so the network port to attach the address to could not be "+
				"resolved. Nothing was changed.", instanceID,
		))
		return
	}
	diags.AddError(summary, fmt.Sprintf(
		"Could not resolve the network port of instance %s: %s", instanceID, err.Error(),
	))
}
