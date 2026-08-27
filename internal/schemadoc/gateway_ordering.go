// Package schemadoc holds schema-description prose that more than one resource
// package must render identically.
//
// It exists because the alternative is worse in a specific way: a note owned by
// whichever resource happened to need it first, then imported by unrelated
// domains. GatewayOrderingNote began in `internal/resource/public_ip` and within
// a week was wanted by the load balancer, the managed Kubernetes cluster and two
// managed web servers — Octavia and RKE2 reaching into the address resource for a
// string. A leaf package keeps every future call site a leaf import rather than a
// new edge between resource packages.
//
// Prose belonging to ONE domain stays in that domain: `public_ip` keeps
// `MutualExclusivityNote`, a statement about two public-IP resources and nothing
// else.
package schemadoc

import (
	"fmt"
	"sort"
	"strings"
)

// Placement says where on a resource's page the note belongs.
type Placement int

const (
	// OnResource renders the note in the resource description. The default, and
	// the right choice whenever more than one attribute can cause the attachment
	// — and whenever the attachment happens with NO attribute set, which is the
	// common case: a public-scheme load balancer or a public cluster ingress
	// allocates its own address when the configuration names none. Keying the
	// note to a bring-your-own-address attribute hides it from exactly the
	// practitioners who never set one.
	//
	// It also renders better. A multi-paragraph note inside an attribute
	// description terminates the generated Optional/Required bullet list, so the
	// block detaches from its bullet and splits the list in two — on the cluster
	// page that would separate an attribute from the one that gates it.
	OnResource Placement = iota

	// OnAttribute renders it in one attribute's description, for a surface where
	// a single attribute IS the attachment and is where a reader looks.
	OnAttribute
)

// AttachingSurface is one place a Terraform configuration can put a public IP
// onto a port inside the customer's own VPC.
//
// MEMBERSHIP IS ONE QUESTION: does this resource's attach run through network's
// associate path? That path attaches the VPC router's gateway when the VPC has
// none (`ensureVPCRouterGateway`, network public_ip_service.go) and records it as
// the platform's own, and every address it attaches counts toward the
// GATEWAY_IN_USE refusal on that gateway's delete.
//
// It is deliberately NOT "an address the practitioner named". Who allocates the
// address changes nothing about the hazard — which is exactly how
// `frostmoln_nginx_instance` and `frostmoln_apache_instance` were missed on the
// first pass: their addresses are platform-allocated, so a rule phrased around
// bring-your-own attributes silently excluded them while the teardown failed for
// their users just the same.
//
// A resource that only REPORTS an address it never attaches — a Computed
// `public_ip` with no configurable toggle, as on the managed databases — is not a
// surface. There is nothing to order.
type AttachingSurface struct {
	// TypeName is the `frostmoln_`-prefixed resource type.
	TypeName string

	// Attribute is where the note renders when Placement is OnAttribute. When
	// Placement is OnResource it is the attribute a reader most likely arrives
	// at, and the drift guard checks that it carries a pointer to the note.
	Attribute string

	// When is the condition under which this resource attaches an address,
	// rendered into the note's first sentence. Empty means it always does.
	When string

	// Placement is where the note goes.
	Placement Placement

	// Why is the attachment in customer terms, with the code path that proves
	// it. Rendered nowhere: it is here so that adding a row requires answering
	// the membership question rather than pattern-matching an attribute name.
	Why string
}

// AttachingSurfaces is the single source of the set — rendered into the note's
// own prose, and asserted against the live provider schema by
// internal/provider/gateway_ordering_note_test.go.
//
// ONE list, because the previous shape had three: the note enumerated the
// resources in its own text, the guard listed them in a table, and the call
// sites were a third. The guard built its expectation by CALLING the note, so
// the enumeration was equal to itself on both sides and could never fail — a new
// surface would have shipped with every page still asserting the old set.
var AttachingSurfaces = []AttachingSurface{
	{
		TypeName: "frostmoln_public_ip", Attribute: "instance_id",
		When: "when `instance_id` names an instance", Placement: OnAttribute,
		Why: "associates this address with the instance's first network port",
	},
	{
		TypeName: "frostmoln_public_ip_association", Attribute: "",
		When: "", Placement: OnResource,
		Why: "the resource IS the attachment",
	},
	{
		TypeName: "frostmoln_load_balancer", Attribute: "public_ip_id",
		When: "when `scheme` is `public`", Placement: OnResource,
		Why: "the saga attaches the address to the VIP port in the vpc_id/subnet_id this resource names (provisioning create_load_balancer.go -> AssociatePublicIPResource)",
	},
	{
		TypeName: "frostmoln_nginx_instance", Attribute: "public",
		When: "when `public` is true", Placement: OnResource,
		Why: "the expose saga attaches a platform-allocated address to the instance's engine port, in the vpc_id/subnet_id this resource names (provisioning expose_webserver.go -> AssociatePublicIPResource)",
	},
	{
		TypeName: "frostmoln_apache_instance", Attribute: "public",
		When: "when `public` is true", Placement: OnResource,
		Why: "as frostmoln_nginx_instance — the same expose saga",
	},
	{
		TypeName: "frostmoln_application_gateway", Attribute: "public_ip_id",
		// ALWAYS, not only for a bring-your-own address. An Application Gateway
		// is an ingress product: it gets a public address in both public_ip_mode
		// values, and the pool-allocated case is the DEFAULT — so a note keyed
		// to the bring-your-own attribute would hide the hazard from almost
		// everyone who hits it. This is the failure that missed nginx and apache
		// on the first pass.
		When: "", Placement: OnResource,
		Why: "the create saga attaches the address to the appliance's port in the vpc_id/subnet_id " +
			"this resource names, in both public_ip_mode values (provisioning appgw_public_ip.go " +
			"-> AssociatePublicIPResource)",
	},
}

// NotAttaching records attributes that LOOK like a surface to the drift guard's
// name-shape scan but are not, each with the reason.
//
// This is the half of the guard that keeps working when nobody remembers this
// file exists: the scan fails on any public-IP-shaped attribute that is neither
// noted nor listed here, so a new one cannot ship silently — someone has to write
// down why it is exempt.
var NotAttaching = map[string]string{
	"frostmoln_gateway.public_ip_id": "This IS the gateway. It has no ordering problem with itself, and a " +
		"depends_on here would be the cycle the note warns about.",
	"frostmoln_public_ip_association.public_ip_id": "The whole resource is the attachment and carries the " +
		"note; a second copy on the attribute would render it twice on one page.",
	"frostmoln_kubernetes_cluster.public_ip_id": "It attaches NOTHING any more. It was re-keyed here from " +
		"`ingress_public_ip_id` when the worker ingress load balancer was retired, on the grounds that the API " +
		"endpoint's own address remained — that address is now gone too. Every new cluster's apiserver is a " +
		"private VIP with no public address (kubernetes, ADR-0017 amended 2026-08-27), the API refuses any " +
		"value with a 400, and no create can associate one. The attribute is retained, deprecated, purely so a " +
		"stale configuration is told so rather than having the value silently dropped. Do NOT restore the " +
		"ordering note: there is no attach to order.",
}

// surfacesOtherThan renders the set for the note's prose, excluding the page it
// is rendering on — a note telling a load-balancer reader "this also applies to
// the load balancer" wastes the one sentence they were going to read.
func surfacesOtherThan(typeName string) string {
	parts := make([]string, 0, len(AttachingSurfaces))
	for _, s := range AttachingSurfaces {
		if s.TypeName == typeName {
			continue
		}
		if s.Attribute == "" {
			parts = append(parts, fmt.Sprintf("`%s`", s.TypeName))
			continue
		}
		parts = append(parts, fmt.Sprintf("`%s` (`%s`)", s.TypeName, s.Attribute))
	}
	sort.Strings(parts)
	switch len(parts) {
	case 0:
		return ""
	case 1:
		return parts[0]
	}
	last := len(parts) - 1
	return strings.Join(parts[:last], ", ") + " and " + parts[last]
}

// isAddressResource reports whether the page being rendered is one where the
// practitioner is holding a `frostmoln_public_ip` — the only pages where the
// cycle warning and the allocation-versus-attachment distinction describe a
// configuration that can exist.
//
// They cannot on the others: an address bound to a load-balancer VIP or a
// cluster ingress VIP is refused as a gateway source address
// (GATEWAY_PUBLIC_IP_UNAVAILABLE), so the cycle is unbuildable there — and a
// warning about an impossible configuration is not merely inert, it invites
// being read as the different, real constraint next to it.
func isAddressResource(typeName string) bool {
	return typeName == "frostmoln_public_ip" || typeName == "frostmoln_public_ip_association"
}

func whenClause(typeName string) string {
	for _, s := range AttachingSurfaces {
		if s.TypeName == typeName && s.When != "" {
			return " " + s.When
		}
	}
	return ""
}

// GatewayOrderingNote states the one dependency on these surfaces that Terraform
// CANNOT derive: an ATTACHED address depends on its VPC having a gateway, and no
// attribute of any attaching resource refers to `frostmoln_gateway`, so the two
// are unordered and run concurrently.
//
// It is rendered per resource because the `depends_on` belongs on whichever
// resource makes the ATTACHMENT, and that is the one thing a reader cannot work
// out from a snippet naming a different one. The first practitioner to hit this
// had to: the GATEWAY_IN_USE diagnostic named `frostmoln_public_ip`, their
// attachment was a `frostmoln_public_ip_association`, and the destroy kept
// failing until they moved the dependency across by hand.
//
// Both failures are stated as the service actually behaves, which is NOT what
// this provider said before. `gatewayService.Create` treats a create that names
// no `public_ip_id` as an idempotent replay (`publicIPMatches` reads an unnamed
// address as "whatever it has"), so it ADOPTS a gateway the platform attached
// rather than refusing it — GATEWAY_EXISTS is reached only by a create that pins
// an address. A note promising an error that does not come is worse than no note:
// the reader concludes the ordering does not matter, and keeps the silent
// outcome, which is a VPC egressing from an address they never chose.
func GatewayOrderingNote(typeName string) string {
	note := "~> **Terraform cannot see that an ATTACHED address depends on the VPC's gateway.** This " +
		"resource attaches one" + whenClause(typeName) + ", and an address reaches the outside world " +
		"only through a gateway — but nothing that attaches an address refers to `frostmoln_gateway`, " +
		"so nothing orders the two. Terraform runs them concurrently and either can win.\n\n" +
		"On teardown the gateway can go first, and its delete is then refused (\"Gateway is still in " +
		"use\", `GATEWAY_IN_USE`) because something in the VPC still depends on it — the failure that " +
		"stops a `terraform destroy` half way through. On create the attachment can land first, and " +
		"the platform then attaches a gateway ITSELF to carry it: a `frostmoln_gateway` that names a " +
		"`public_ip_id` is refused after that (\"VPC already has a gateway\", `GATEWAY_EXISTS`), and " +
		"one that names none is not refused at all — it quietly ADOPTS the gateway the platform made, " +
		"leaving the VPC egressing from whatever address that gateway already had rather than one " +
		"this configuration names, with `origin` reading `implicit_public_ip`.\n\n" +
		"Where the same configuration manages the gateway, state the ordering yourself: put " +
		"`depends_on = [frostmoln_gateway.<name>]` on the resource that makes the ATTACHMENT — this " +
		"one. Where the gateway is in another module, the dependency is on the module itself: " +
		"`depends_on = [module.<name>]`. This resource is not the only one that needs it: " +
		surfacesOtherThan(typeName) + " attach addresses too, and each takes the line on itself.\n\n" +
		"It works only where the gateway is a `frostmoln_gateway` RESOURCE in the same configuration. " +
		"A `data \"frostmoln_gateway\"` cannot carry the order — a data source is read, never created " +
		"or destroyed — so depending on one defers a read and sequences nothing."

	if isAddressResource(typeName) {
		note += "\n\nThe ATTACHMENT is the place for it because an address that is merely allocated " +
			"depends on nothing — and because an address resolved through the `frostmoln_public_ip` " +
			"data source has no allocation resource to hang it on at all.\n\n" +
			"**Never put it on an address that a `frostmoln_gateway.public_ip_id` names** — the " +
			"gateway already depends on that address, so a dependency back the other way is a " +
			"plan-time `Cycle:` error, and that reference already sequences the two correctly " +
			"without help."
	}

	note += "\n\nDo not write it the other way about — on the gateway, listing what attaches. " +
		"`depends_on` orders the resource it is written on, so that reverses both orders and turns a " +
		"race that sometimes passed into a teardown that fails every time.\n\n" +
		"It changes ORDER only: nothing is created and nothing is released. It does not arm the " +
		"gateway's own destroy either — without `acknowledge_connectivity_loss` the teardown stops at " +
		"that refusal instead, and never reaches the ordering at all. And if a gateway was already " +
		"adopted, nothing needs importing or rebuilding: it is in state already — add the ordering so " +
		"it cannot recur, then give the gateway the address you meant with `public_ip_id`, which is " +
		"applied in place."

	return note
}
