package vpc_route

import (
	"context"
	"fmt"
	"net/netip"

	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
)

// canonicalPrefixValidator refuses a destination whose spelling differs from
// the one the API will return.
//
// WHY THIS EXISTS AND WHAT IT IS NOT. It is not input validation for its own
// sake — the service refuses a bad CIDR perfectly well. It is the fix for a
// convergence failure: the route table stores destinations MASKED and echoes
// them back canonicalized, so `FD00:1:0:0::/64` in a configuration reads back
// as `fd00:1::/64`. Terraform then either reports "Provider produced
// inconsistent result after apply" or shows a diff that no apply can settle.
//
// Rejecting the non-canonical spelling AT PLAN TIME, with the canonical form in
// the message, is the smallest fix, and the honest one: silently rewriting the
// practitioner's value would make the state disagree with the file they are
// looking at.
type canonicalPrefixValidator struct{}

func (v canonicalPrefixValidator) Description(_ context.Context) string {
	return "must be a CIDR block in canonical (masked) form"
}

func (v canonicalPrefixValidator) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}

func (v canonicalPrefixValidator) ValidateString(ctx context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	value := req.ConfigValue.ValueString()

	prefix, err := netip.ParsePrefix(value)
	if err != nil {
		resp.Diagnostics.AddAttributeError(
			req.Path,
			"Invalid destination",
			// NOT `::/0` as the example: this validator only checks the SHAPE, but
			// offering as an example a value the platform refuses sends the
			// practitioner to a plan-time success and an apply-time 400.
			fmt.Sprintf("%q is not a CIDR block, e.g. \"203.0.113.0/24\" or \"2001:db8::/32\".", value),
		)
		return
	}
	if canonical := prefix.Masked().String(); canonical != value {
		resp.Diagnostics.AddAttributeError(
			req.Path,
			"Destination is not in canonical form",
			fmt.Sprintf("The platform stores destinations masked and returns them canonicalized, so "+
				"%q would be read back as %q and this resource would never converge. Write it as %q.",
				value, canonical, canonical),
		)
	}
}

// canonicalAddressOrTokenValidator accepts the reserved `internet` token, and
// otherwise refuses only a non-canonical spelling of an address.
//
// IT DOES NOT VALIDATE THAT next_hop IS AN IP ADDRESS, and must never be made
// to. `internet` is a legal value on this surface — it is the ONLY way to write
// the exception route a forced tunnel needs, because the platform's own default
// route has no address a customer could name. Beyond that, which addresses are
// legal depends on the subnets attached to the VPC, which only the service
// knows; refusals come back as ROUTE_NEXT_HOP_* codes.
//
// The canonical-form check is here for the same convergence reason as the
// destination's: an IPv6 next hop written `FD00:1::A` reads back as `fd00:1::a`.
type canonicalAddressOrTokenValidator struct{}

func (v canonicalAddressOrTokenValidator) Description(_ context.Context) string {
	return "must be `internet`, or an IP address in canonical form"
}

func (v canonicalAddressOrTokenValidator) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}

func (v canonicalAddressOrTokenValidator) ValidateString(ctx context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	value := req.ConfigValue.ValueString()

	// Matched exactly and case-sensitively, as the service matches it.
	if value == NextHopInternet {
		return
	}

	addr, err := netip.ParseAddr(value)
	if err != nil {
		// NOT an error. The value may be a form this provider has no opinion
		// about; the service decides what a next hop may be, and inventing a
		// second opinion here is how a client refuses something the platform
		// would have accepted.
		return
	}
	if canonical := addr.String(); canonical != value {
		resp.Diagnostics.AddAttributeError(
			req.Path,
			"Next hop is not in canonical form",
			fmt.Sprintf("The platform returns addresses canonicalized, so %q would be read back as "+
				"%q and this resource would never converge. Write it as %q.", value, canonical, canonical),
		)
	}
}
