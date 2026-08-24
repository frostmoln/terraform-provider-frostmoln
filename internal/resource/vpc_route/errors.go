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
// VPC being gone, the surface not being served, or THE PATH NOT BEING ROUTED AT
// ALL.
//
// THE ENVELOPE CHECK IS NOT BELT-AND-BRACES — it is what stops a routing
// failure from destroying state. Acting on this verdict DROPS THE RESOURCE, and
// until the PATH_NOT_ROUTED rename the gateway answered an unmatched path with this very
// code. So a gateway 404 on the VPC-routes path — a bad deploy, a config rule
// that lost the route, a mid-rollout window — reached `terraform destroy` as
// "your route no longer exists": it printed "Destroy complete", dropped the
// resource, and left the route installed in Neutron for nobody to manage.
//
// The rename fixed it going forward, but the provider is a CUSTOMER-PINNED
// artifact run against whatever gateway the estate has, so an older one still
// emits the collision and this guard has to survive it. The two are separable on
// the wire even when the code is identical: the gateway answers NESTED
// ({"error":{…}}), a service answers its own refusal FLAT. Requiring the flat
// shape means only network can produce this verdict.
//
// Deliberately NOT a version check — the provider cannot know the gateway's
// version at the point of the error, and the body shape is the thing that
// actually distinguishes the two layers.
func isRouteNotFound(err error) bool {
	var apiErr *client.APIError
	return errors.As(err, &apiErr) &&
		apiErr.StatusCode == http.StatusNotFound &&
		apiErr.Code == errCodeRouteNotFound &&
		apiErr.FlatEnvelope
}

// The diagnostic summaries addRouteError is called with, one per CRUD operation.
//
// CONSTANTS BECAUSE ONE OF THEM IS A DISCRIMINATOR, not only a fallback title.
// Since network v2.57.1 a DELETE can be refused with
// ROUTE_NEXT_HOP_UNREACHABLE — a removal is applied by writing the whole
// remaining set and Neutron revalidates all of it — so that code's remedy
// differs by operation, and the branch below reads `fallback` to pick it. A
// literal at the call site would let a reword silently drop the branch.
//
// A DEFINED TYPE SPLITS THE TWO JOBS. `fallback` was carrying both the
// diagnostic TITLE and the operation discriminator in one bare string, and copy
// reviewers edit titles freely — so a retitled call site, or a fourth one
// passing a literal, would silently take the create-shaped arm with no compiler
// complaint. The op is the control value; its title is derived.
type routeOp int

const (
	opCreate routeOp = iota
	opRead
	opDelete
)

func (o routeOp) summary() string {
	switch o {
	case opRead:
		return "Failed to Read VPC Routes"
	case opDelete:
		return "Failed to Delete VPC Route"
	default:
		return "Failed to Create VPC Route"
	}
}

// addRouteError turns a route refusal into a diagnostic whose remedy is
// actually followable.
//
// An unrecognised code falls through to the API's own rendering: a code this
// build has never seen is a fault to surface, not a refusal to reinterpret.//
// AND `invalid_input` DELIBERATELY GETS NO ARM, which is a divergence from the
// portal rather than an oversight. On a removal that code is a UNION: the
// service 400s a malformed `destination` the caller supplied, before any write,
// and the repository answers the same code when the remaining set is refused.
// The portal's destination comes from a listed row so only the second case is
// reachable there, and it overrides the copy. Here it comes from configuration
// or state, and the server's own sentence already names the destination.
func addRouteError(diags *diag.Diagnostics, op routeOp, err error) {
	var apiErr *client.APIError
	if !errors.As(err, &apiErr) {
		diags.AddError(op.summary(), err.Error())
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
		// TWO DIAGNOSTICS, PICKED BY OPERATION. On a DESTROY there is no next hop
		// in the configuration to correct and no dependency to add — the route is
		// being taken away. The refused next hop belongs to some OTHER route in
		// the set, which Neutron revalidated along with the removal, so the title
		// "the next hop is not reachable" reads as an accusation about the
		// resource being destroyed and the remedy is unfollowable.
		if op == opDelete {
			diags.AddError(
				"Another route in this VPC was refused while removing this one",
				"Removing a route rewrites the VPC's whole route set, and every route in it is "+
					"checked again — so the route refused here may be one that was already stored, "+
					"not the one being destroyed. A next hop must be an address on a subnet attached "+
					"to this VPC, or the `"+NextHopInternet+"` token.\n\n"+
					"The offending route may not be managed by Terraform at all, so read the VPC's "+
					"stored set rather than state: `fm network vpc route list <vpc_id>`, or the "+
					"routes panel in the portal. Correct or remove that route, then run the same "+
					"command again.\n\n"+apiErr.Message,
			)
			return
		}
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
		// A CLOSED LOOP ON A DESTROY, which is why it cannot share the copy below.
		// A removal writes the whole REMAINING set, and a set already over the
		// ceiling is still over it once one route goes — so the write is refused,
		// the route stays, and the next apply fails identically. "Remove a route
		// you no longer need" is the operation that just failed. This provider
		// removes one route per resource, so it cannot converge either.
		if op == opDelete {
			diags.AddError(
				"This VPC's route set is over the limit, and removing one route cannot get under it",
				"Every removal rewrites the VPC's whole route set, and a set already over the "+
					"limit is still over it once one route goes — so the write is refused and the "+
					"route stays. Removing them one at a time cannot converge, and this provider "+
					"removes one route per resource. Contact support to have the route set "+
					"replaced in a single write.\n\n"+apiErr.Message,
			)
			return
		}
		diags.AddError(
			"This VPC's route limit is reached",
			"The cap counts your own routes only; platform routes do not consume it. It is the same "+
				"for every VPC on the platform rather than a per-tenant allowance, and a default "+
				"route (0.0.0.0/0) counts as two — so remove a route "+
				"you no longer need — or contact support if the cap itself is too low.\n\n"+apiErr.Message,
		)
	case errCodeRoutesUnavailable, errCodeRouteInternetUnavailable:
		diags.AddError(
			"This deployment cannot serve that request",
			"This is a property of the deployment, not of your configuration, and retrying will not "+
				"change it.\n\n"+apiErr.Message,
		)
	default:
		diags.AddError(op.summary(), apiErr.Error())
	}
}
