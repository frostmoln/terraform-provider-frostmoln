package vpc_route

import (
	"errors"
	"net/http"

	"github.com/hashicorp/terraform-plugin-framework/diag"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
)

// The route surface's error codes.
//
// BRANCH ON THESE, NEVER ON THE MESSAGE. The taxonomy shipped before any client
// so that no client would ever have to parse English, and the two RESERVED
// refusals deliberately carry generic copy — the platform address they were
// measured against goes to the platform's own logs and is not disclosed.
//
// The roster lives in `domain.RouteErrorCodes` in the network service; these are
// the ones this resource distinguishes.
const (
	// errCodeRouteExists — this VPC already routes that destination.
	errCodeRouteExists = "ROUTE_EXISTS"
	// errCodeRouteDefaultProvidedByGateway — a default route via `internet` that
	// the VPC's gateway already provides.
	//
	// NOT errCodeRouteExists, and it must never be reported as one: the
	// colliding route is not in the tenant's set, is not listed by a read, and a
	// delete for it answers ROUTE_NOT_FOUND — so a practitioner told to "remove
	// the existing route" would find nothing to remove.
	errCodeRouteDefaultProvidedByGateway = "ROUTE_DEFAULT_PROVIDED_BY_GATEWAY"
	// errCodeRouteDestinationReserved — the destination falls inside a range the
	// platform routes for every VPC.
	errCodeRouteDestinationReserved = "ROUTE_DESTINATION_RESERVED"
	// errCodeRouteShadowsAttachedSubnet — the destination covers a subnet
	// attached to this VPC, which already delivers it directly.
	errCodeRouteShadowsAttachedSubnet = "ROUTE_SHADOWS_ATTACHED_SUBNET"
	// errCodeRouteNextHopReserved — the platform routes that address for every
	// VPC, so traffic to it leaves the VPC rather than reaching it.
	errCodeRouteNextHopReserved = "ROUTE_NEXT_HOP_RESERVED"
	// errCodeRouteNextHopUnreachable — the next hop is not a host address on a
	// subnet attached to this VPC.
	errCodeRouteNextHopUnreachable = "ROUTE_NEXT_HOP_UNREACHABLE"
	// errCodeRouteNoInternetGateway — `internet` was used on a VPC with no path
	// to the internet.
	//
	// ITS OWN CODE BECAUSE THE REMEDY IS A DIFFERENT RESOURCE. Terraform has to
	// tell "create frostmoln_gateway first" from "your input is malformed" to
	// order its graph, and cannot from a generic invalid_input.
	errCodeRouteNoInternetGateway = "ROUTE_NO_INTERNET_GATEWAY"
	// errCodeRouteQuotaExceeded — the per-VPC route cap, counting the tenant's
	// own routes only. A 403, never a 429.
	errCodeRouteQuotaExceeded = "ROUTE_QUOTA_EXCEEDED"
	// errCodeRouteNotFound — no route with that destination on this VPC.
	//
	// THE DISTINCTION THIS RESOURCE IS BUILT ON: a 404 carrying this code means
	// the ROUTE is gone and the resource should leave state. A 404 carrying the
	// plain `not_found` means the VPC is gone — parent drift — or that this
	// deployment does not serve the surface at all, which is emphatically not a
	// reason to drop anything from state. See routeAbsence.
	errCodeRouteNotFound = "ROUTE_NOT_FOUND"
	// errCodeRouteWriteConflict — THE ONLY TRANSIENT ROUTE CODE. Concurrent
	// writers kept changing the set; nothing was written. Every other refusal is
	// permanent and retrying one loops forever.
	errCodeRouteWriteConflict = "ROUTE_WRITE_CONFLICT"
	// errCodeRouteInternetUnavailable — this deployment configures no external
	// gateway address, so `internet` cannot be resolved here.
	errCodeRouteInternetUnavailable = "ROUTE_INTERNET_UNAVAILABLE"
	// errCodeRoutesUnavailable — this deployment does not offer route management
	// at all.
	errCodeRoutesUnavailable = "ROUTES_UNAVAILABLE"
)

// isRouteWriteConflict reports the ONE refusal that is safe to retry.
//
// Deliberately NOT client.IsConflict: this surface answers 409 for five other
// conditions, every one of them permanent, and a blanket conflict retry would
// spin on a reserved destination until the timeout and then report the timeout
// instead of the refusal.
func isRouteWriteConflict(err error) bool {
	var apiErr *client.APIError
	return errors.As(err, &apiErr) && apiErr.Code == errCodeRouteWriteConflict
}

// isRouteNotFound reports a 404 that means THE ROUTE is gone, as opposed to the
// VPC being gone or the surface not being served.
func isRouteNotFound(err error) bool {
	var apiErr *client.APIError
	return errors.As(err, &apiErr) &&
		apiErr.StatusCode == http.StatusNotFound &&
		apiErr.Code == errCodeRouteNotFound
}

// addRouteError turns a route refusal into a diagnostic whose remedy is
// actually followable.
//
// An unrecognised code falls through to the API's own rendering: a code this
// build has never seen is a fault to surface, not a refusal to reinterpret.
func addRouteError(diags *diag.Diagnostics, fallback string, err error) {
	var apiErr *client.APIError
	if !errors.As(err, &apiErr) {
		diags.AddError(fallback, err.Error())
		return
	}

	switch apiErr.Code {
	case errCodeRouteWriteConflict:
		diags.AddError(
			"VPC routes kept changing while this write was applied",
			"Other requests changed this VPC's route set while yours was being applied, and the "+
				"provider gave up retrying. Nothing was written. Run `terraform apply` again.\n\n"+
				apiErr.Message,
		)
	case errCodeRouteDefaultProvidedByGateway:
		diags.AddError(
			"This VPC's gateway already provides that default route",
			"The colliding route belongs to the platform: it is not in your route set, a read does "+
				"not list it, and there is nothing to remove or import. To route this VPC through "+
				"your own appliance, point the default route at an instance address instead of the "+
				"`"+NextHopInternet+"` token.\n\n"+apiErr.Message,
		)
	case errCodeRouteExists:
		diags.AddError(
			"This VPC already has a route to that destination",
			"A destination can have only one next hop. If the route was created outside Terraform, "+
				"import it with `terraform import frostmoln_vpc_route.<name> \"<vpc_id>/<destination>\"` "+
				"rather than creating it again.\n\n"+apiErr.Message,
		)
	case errCodeRouteNoInternetGateway:
		// NAMES BOTH CASES. The service emits this code when the VPC has NO
		// gateway and when it HAS one that sits on the platform service network
		// — an internal-connectivity path that reaches no internet (ADR-0103) —
		// and the code cannot separate them, which is why it is named for the
		// absent CAPABILITY rather than the absent object. Advising only
		// "create a gateway" would send the second group to a create that is
		// refused with GATEWAY_EXISTS.
		diags.AddError(
			"The `"+NextHopInternet+"` next hop needs an internet-facing gateway on this VPC",
			"If this VPC has no gateway, add a `frostmoln_gateway` for it and make this route depend "+
				"on that resource so Terraform creates them in order.\n\n"+
				"If it already has one, that gateway is an internal-connectivity path and reaches no "+
				"internet — set `next_hop` to an address on a subnet attached to this VPC instead of "+
				"the `"+NextHopInternet+"` token.\n\n"+apiErr.Message,
		)
	case errCodeRouteNextHopUnreachable:
		diags.AddError(
			"The next hop is not reachable inside this VPC",
			"A next hop must be a host address on a subnet attached to this VPC. If the subnet is "+
				"managed by Terraform too, make this route depend on it. To send the traffic out "+
				"the VPC's own gateway instead, use the `"+NextHopInternet+"` token.\n\n"+apiErr.Message,
		)
	case errCodeRouteDestinationReserved, errCodeRouteNextHopReserved:
		diags.AddError(
			"The platform reserves that address",
			"This is permanent — retrying will not change it, and the platform address involved is "+
				"deliberately not disclosed. Choose a destination inside your own address space, or "+
				"a next hop on a subnet attached to this VPC.\n\n"+apiErr.Message,
		)
	case errCodeRouteShadowsAttachedSubnet:
		diags.AddError(
			"That destination is already delivered directly",
			"The destination is, or falls inside, a subnet attached to this VPC, which reaches it "+
				"without a route.\n\n"+apiErr.Message,
		)
	case errCodeRouteQuotaExceeded:
		diags.AddError(
			"This VPC's route limit is reached",
			"The cap counts your own routes only; platform routes do not consume it. It is the same "+
				"for every VPC on the platform rather than a per-tenant allowance, so remove a route "+
				"you no longer need — or contact support if the cap itself is too low.\n\n"+apiErr.Message,
		)
	case errCodeRoutesUnavailable, errCodeRouteInternetUnavailable:
		diags.AddError(
			"This deployment cannot serve that request",
			"This is a property of the deployment, not of your configuration, and retrying will not "+
				"change it.\n\n"+apiErr.Message,
		)
	default:
		diags.AddError(fallback, apiErr.Error())
	}
}
