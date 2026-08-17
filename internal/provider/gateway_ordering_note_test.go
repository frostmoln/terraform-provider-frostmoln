package provider

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/schemadoc"
)

// publicIPShaped matches an attribute name through which a configuration might
// hand the platform a public IP. It is the tripwire's candidate rule, not its
// answer: everything it matches must be either a documented attaching surface or
// an explicit exemption with a reason.
var publicIPShaped = regexp.MustCompile(`(^|_)public_ip_id$`)

// resourceSchemas renders every registered resource's schema once.
func resourceSchemas(t *testing.T) map[string]schema.Schema {
	t.Helper()

	ctx := context.Background()
	p, ok := New("test")().(*FrostmolnProvider)
	if !ok {
		t.Fatal("expected New to return a *FrostmolnProvider")
	}

	out := map[string]schema.Schema{}
	for _, newResource := range p.Resources(ctx) {
		r := newResource()

		var mdResp resource.MetadataResponse
		r.Metadata(ctx, resource.MetadataRequest{ProviderTypeName: "frostmoln"}, &mdResp)
		var schemaResp resource.SchemaResponse
		r.Schema(ctx, resource.SchemaRequest{}, &schemaResp)

		out[mdResp.TypeName] = schemaResp.Schema
	}
	return out
}

// TestEveryAttachingSurfaceCarriesTheGatewayOrderingNote asserts the documented
// set is actually documented, and that each page names ITSELF as where the
// `depends_on` goes.
//
// The self-naming half is the one that matters. The first practitioner to hit
// this was told to put the dependency on a `frostmoln_public_ip` while his
// attachment was made by a `frostmoln_public_ip_association`, and moved it across
// by hand — a note present but naming the wrong resource is the exact defect.
func TestEveryAttachingSurfaceCarriesTheGatewayOrderingNote(t *testing.T) {
	t.Parallel()

	schemas := resourceSchemas(t)

	for _, surface := range schemadoc.AttachingSurfaces {
		s, ok := schemas[surface.TypeName]
		if !ok {
			t.Errorf("%s is a documented attaching surface but is not a registered resource — "+
				"the list is stale, which means it is no longer guarding anything", surface.TypeName)
			continue
		}

		note := schemadoc.GatewayOrderingNote(surface.TypeName)

		t.Run(surface.TypeName, func(t *testing.T) {
			switch surface.Placement {
			case schemadoc.OnResource:
				if !strings.Contains(s.Description, note) {
					t.Errorf("%s attaches an address, so its RESOURCE description must carry the "+
						"gateway-ordering note naming itself", surface.TypeName)
				}
				// The attribute a reader arrives at must at least point at it —
				// the note is on the resource precisely because the attachment
				// can happen without this attribute being set.
				if surface.Attribute != "" {
					attr, ok := s.Attributes[surface.Attribute]
					if !ok {
						t.Fatalf("%s has no attribute %q — the list is stale",
							surface.TypeName, surface.Attribute)
					}
					if !strings.Contains(strings.ToLower(attr.GetDescription()), "ordering note on this resource") {
						t.Errorf("%s.%s is where a reader looks for this; it must point at the note on "+
							"the resource:\n%s", surface.TypeName, surface.Attribute, attr.GetDescription())
					}
				}

			case schemadoc.OnAttribute:
				attr, ok := s.Attributes[surface.Attribute]
				if !ok {
					t.Fatalf("%s has no attribute %q — the list is stale",
						surface.TypeName, surface.Attribute)
				}
				if !strings.Contains(attr.GetDescription(), note) {
					t.Errorf("%s.%s attaches an address, so it must carry the gateway-ordering note "+
						"naming %s:\n%s", surface.TypeName, surface.Attribute, surface.TypeName,
						attr.GetDescription())
				}
			}
		})
	}
}

// TestNoUndocumentedPublicIPAttribute is the half that keeps working when nobody
// remembers schemadoc exists.
//
// The previous shape of this guard was an allow-list: it checked that listed
// surfaces still carried the note, and `continue`d past everything else. That
// fails OPEN on the rot it was written for — a new resource with a
// `public_ip_id` ships, nobody adds a row, and the suite stays green. Two of the
// six surfaces we have were missed exactly that way and were found by review
// rather than by this test.
//
// So the direction is inverted: every public-IP-shaped attribute in the provider
// must be accounted for, and "not a surface" has to be written down with a
// reason. Adding one costs a sentence; adding one silently is not possible.
func TestNoUndocumentedPublicIPAttribute(t *testing.T) {
	t.Parallel()

	documented := map[string]bool{}
	for _, s := range schemadoc.AttachingSurfaces {
		if s.Attribute != "" {
			documented[s.TypeName+"."+s.Attribute] = true
		}
	}

	for typeName, s := range resourceSchemas(t) {
		for name, attr := range s.Attributes {
			if !publicIPShaped.MatchString(name) {
				continue
			}
			// Computed-only attributes report an address; they never ask for one.
			if attr.IsComputed() && !attr.IsOptional() && !attr.IsRequired() {
				continue
			}
			key := typeName + "." + name
			if documented[key] {
				continue
			}
			if reason, exempt := schemadoc.NotAttaching[key]; exempt {
				if strings.TrimSpace(reason) == "" {
					t.Errorf("%s is exempted with an empty reason — the exemption list is where the "+
						"thinking is supposed to survive", key)
				}
				continue
			}
			t.Errorf("%s is a configurable public-IP attribute that is neither a documented attaching "+
				"surface nor an explicit exemption.\n\nIf attaching it can put an address on a port in "+
				"the customer's VPC, add it to schemadoc.AttachingSurfaces and render the ordering "+
				"note — that attach makes the platform create a gateway the configuration did not ask "+
				"for, and holds it open against `terraform destroy`. If it cannot, add it to "+
				"schemadoc.NotAttaching with the reason.", key)
		}
	}
}

// TestGatewayOrderingNoteNamesTheOtherSurfaces pins the enumeration inside the
// note against the list it is generated from — and, more importantly, that the
// note does NOT list the page it is rendering on.
//
// Not a tautology despite both sides deriving from AttachingSurfaces: the note
// builds prose from the list while this asserts the resulting text mentions each
// OTHER surface and omits self. The previous shape hard-coded the enumeration in
// the note's own string, so it agreed with itself by construction and could not
// have failed when a fifth surface appeared.
func TestGatewayOrderingNoteNamesTheOtherSurfaces(t *testing.T) {
	t.Parallel()

	for _, surface := range schemadoc.AttachingSurfaces {
		note := schemadoc.GatewayOrderingNote(surface.TypeName)

		t.Run(surface.TypeName, func(t *testing.T) {
			for _, other := range schemadoc.AttachingSurfaces {
				if other.TypeName == surface.TypeName {
					continue
				}
				if !strings.Contains(note, other.TypeName) {
					t.Errorf("the note rendered for %s must name %s as another surface that needs the "+
						"same line — a practitioner holding both otherwise fixes one and keeps the "+
						"failure", surface.TypeName, other.TypeName)
				}
			}
			// "this one" is what the note says instead; naming itself in the
			// list of OTHERS is the redundancy the per-resource rendering exists
			// to remove.
			marker := fmt.Sprintf("`%s` (", surface.TypeName)
			if strings.Contains(note, marker) {
				t.Errorf("the note rendered for %s lists %s among the OTHER attaching surfaces",
					surface.TypeName, surface.TypeName)
			}
		})
	}
}

// TestCycleWarningOnlyWhereTheCycleIsBuildable.
//
// The warning tells the reader never to hang the dependency on an address that a
// `frostmoln_gateway.public_ip_id` names. That configuration cannot exist on a
// load balancer or a cluster page: an address bound to a VIP is refused as a
// gateway source address (GATEWAY_PUBLIC_IP_UNAVAILABLE), so the cycle is
// unbuildable there. Warning about an impossible configuration is not merely
// wasted words — next to the real constraint on those pages it invites being
// read as that constraint.
func TestCycleWarningOnlyWhereTheCycleIsBuildable(t *testing.T) {
	t.Parallel()

	for _, surface := range schemadoc.AttachingSurfaces {
		note := schemadoc.GatewayOrderingNote(surface.TypeName)
		addressResource := surface.TypeName == "frostmoln_public_ip" ||
			surface.TypeName == "frostmoln_public_ip_association"

		if got := strings.Contains(note, "Cycle:"); got != addressResource {
			t.Errorf("%s: cycle warning present=%v, want %v — it belongs only where the practitioner "+
				"holds a frostmoln_public_ip that a gateway can name", surface.TypeName, got, addressResource)
		}
	}
}

// TestGatewayOrderingNoteStatesWhatItCannotGetWrong holds the claims that took
// three expert reviews and a trace through the network service to get right.
//
// Deliberately NOT asserted: that the note contains its own interpolated
// argument. The function builds it, so that check can never fail — an earlier
// version of this file made exactly that mistake.
func TestGatewayOrderingNoteStatesWhatItCannotGetWrong(t *testing.T) {
	t.Parallel()

	note := schemadoc.GatewayOrderingNote("frostmoln_public_ip_association")
	for _, want := range []string{
		"depends_on = [frostmoln_gateway.<name>]",
		"GATEWAY_IN_USE",
		"GATEWAY_EXISTS",
		"ADOPTS",       // the create side does not always fail
		"concurrently", // ...and neither order is guaranteed
		"acknowledge_connectivity_loss",
		"module.<name>",
		"data \"frostmoln_gateway\"", // a data source cannot carry the order
	} {
		if !strings.Contains(note, want) {
			t.Errorf("the gateway-ordering note must contain %q", want)
		}
	}
}
