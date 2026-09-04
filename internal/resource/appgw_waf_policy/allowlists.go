package appgw_waf_policy

import (
	"context"
	"regexp"
	"sort"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// The method and media-type allow-lists: an OVERRIDE over a platform default
// the platform keeps current.
//
// ============================================================================
// THE TRI-STATE, AND WHY THIS PROVIDER SPELLS IT THIS WAY
// ============================================================================
//
// The server's PATCH models each list as a POINTER to a slice, and the three
// states are three different instructions:
//
//	absent  -> leave whatever is set alone
//	[]      -> CLEAR the override; run on the platform default again
//	a list  -> use exactly that list
//
// `[]` is the only spelling of "undo my narrowing" the API has. Omitting the
// field does NOT undo it. A client that cannot emit all three eventually
// strands a customer on a narrowing they removed from their configuration.
//
// Terraform has its own three states for an Optional list — absent (null), an
// empty list, and a list — and the tempting move is to map them one to one.
// THAT MAPPING IS WRONG HERE, and the reason is not taste:
//
//   - `allowed_methods = []` CANNOT ROUND-TRIP. Terraform core requires the
//     applied value of a non-computed attribute to equal the config value. The
//     server answers `[]` by deleting the override, and a policy with no
//     override reports the field ABSENT — so state comes back null, config says
//     `[]`, and every subsequent plan proposes `null -> []` forever. Marking the
//     attribute Computed does not rescue it: core still requires a non-null
//     config value to survive the apply unchanged. So `[]` is refused in
//     configuration (overrideNotEmpty, below) and named as the thing it would
//     have to mean.
//
//   - REMOVING THE ATTRIBUTE FROM HCL IS THEREFORE WHAT CLEARS THE OVERRIDE,
//     and that is also what Terraform already means by removing an Optional
//     attribute that has a server-side default: stop overriding, take the
//     default. So a null CONFIG with an override in state is the `[]` case on
//     the wire — the plan shows `["GET"] -> null`, the apply clears it, the read
//     back agrees, and the resource converges in one step.
//
// The rejected alternative is Optional+Computed with UseStateForUnknown, which
// maps null config -> wire-absent. It plans clean, and it is the worse bug the
// brief warns about: deleting the line from your configuration would then do
// NOTHING, silently and forever, with no diff to show it and no way left to
// express a reset at all.
//
// The remaining wire state, absent, is what an UNCHANGED attribute sends — the
// resource's Update only ever sends what actually differs from state, so a
// policy whose `mode` moved does not restate its allow-lists.
//
// ============================================================================
// AND NEVER THE OTHER DIRECTION
// ============================================================================
//
// The `effective_*` attributes are Computed and are never read back into a
// write. The platform default content-type list has been widened three times;
// a client that round-trips today's effective list into an override pins its
// tenant out of the next widening while appearing to change nothing.
const (
	// maxAllowedMethods and maxAllowedContentTypes are the server's own caps.
	// Checked at plan time so an oversized list is a diagnostic naming the
	// attribute rather than a 400 halfway through an apply.
	maxAllowedMethods      = 32
	maxAllowedContentTypes = 64
)

// httpMethodPattern bounds one method token, and it is a SHAPE the server
// enforces rather than a nicety: the entry is rendered into the managed
// ruleset's own configuration, so its characters are bounded there.
//
// Uppercase only, because the ruleset compares the request's method without
// transforming it — a lowercase entry is a control that can never match.
var httpMethodPattern = regexp.MustCompile(`^[A-Z][A-Z0-9-]{0,31}$`)

// mediaTypePattern bounds one media type: lowercase `type/subtype`, no
// parameters. The ruleset matches the type alone, so
// `application/json; charset=utf-8` would never match anything.
var mediaTypePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9!#$&^_.+-]{0,63}/[a-z0-9][a-z0-9!#$&^_.+-]{0,63}$`)

// refusedMethods are the four the managed ruleset refuses on every gateway, and
// the four an allow-list may never re-admit. The server refuses them by name;
// this provider says so at plan time, with the reason, because the alternative
// is an apply that fails on a value the practitioner believed was theirs to
// choose.
//
// 🔴 IT IS A DENY-LIST OF FOUR, NOT A CEILING OF SEVEN. Everything else is
// listable, the WebDAV and CalDAV verbs included — and those are refused UNTIL
// listed, so an application that speaks them has to say so here.
var refusedMethods = map[string]string{
	"TRACE":   "reflects the request back to the client (cross-site tracing)",
	"TRACK":   "is TRACE under another name",
	"CONNECT": "would make this gateway an open proxy",
	"DEBUG":   "exposes an application's own debug surface",
}

var (
	_ validator.List   = overrideNotEmpty{}
	_ validator.String = methodNotRefused{}
)

// overrideNotEmpty refuses `attr = []`.
//
// listvalidator.SizeAtLeast(1) makes the same check and tells the practitioner
// "list must contain at least 1 elements", which reads as "this attribute is
// mandatory" — the opposite of the truth, and useless for the person who wrote
// `[]` meaning "go back to the default". See the package comment above for why
// an empty list cannot be the spelling of that: it is the one value that cannot
// survive a round trip, so it is refused with the working spelling named.
type overrideNotEmpty struct{ what string }

func (v overrideNotEmpty) Description(context.Context) string {
	return "must not be an empty list; remove the attribute to run on the platform default"
}

func (v overrideNotEmpty) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}

func (v overrideNotEmpty) ValidateList(_ context.Context, req validator.ListRequest, resp *validator.ListResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() || len(req.ConfigValue.Elements()) > 0 {
		return
	}
	resp.Diagnostics.AddAttributeError(req.Path,
		"An Empty List Cannot Say \"Use The Platform Default\"",
		"This attribute is an OVERRIDE over a platform default that is kept current for you, and "+
			"an empty list is not a narrowing to no "+v.what+" — the platform has no such setting.\n\n"+
			"REMOVE THE ATTRIBUTE (or set it to null) to clear the override and go back to the "+
			"platform default. That is what Terraform already means by dropping an optional "+
			"attribute, the plan shows it as a change, and the apply clears it.\n\n"+
			"An empty list is refused rather than accepted because it could not be kept: a policy "+
			"carrying no override reports none, so the value would read back as null and every "+
			"later plan would propose the same empty list again.")
}

// methodNotRefused refuses the four methods no policy may re-admit.
type methodNotRefused struct{}

func (methodNotRefused) Description(context.Context) string {
	return "must not name a method the managed ruleset refuses on every gateway"
}

func (v methodNotRefused) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}

func (methodNotRefused) ValidateString(_ context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	why, refused := refusedMethods[req.ConfigValue.ValueString()]
	if !refused {
		return
	}
	resp.Diagnostics.AddAttributeError(req.Path,
		req.ConfigValue.ValueString()+" Is Refused On Every Gateway",
		req.ConfigValue.ValueString()+" "+why+", so the managed ruleset refuses it everywhere and "+
			"this list cannot re-admit it. The whole set is "+allRefusedMethods()+".\n\n"+
			"Every other method token is yours to list, including the WebDAV and CalDAV verbs "+
			"(PROPFIND, PROPPATCH, MKCOL, COPY, MOVE, LOCK, UNLOCK, REPORT, SEARCH), which the "+
			"platform default does not carry and which are refused until you name them.")
}

// allRefusedMethods names the whole deny-list, so someone who tried one learns
// the set in a single plan rather than one apply at a time.
func allRefusedMethods() string {
	out := make([]string, 0, len(refusedMethods))
	for m := range refusedMethods {
		out = append(out, m)
	}
	sort.Strings(out)
	return strings.Join(out, ", ")
}

// optionalStringList renders a server-supplied list, using NULL for absent.
//
// Null and an empty list are different answers and both are reachable: absent
// means "this policy carries no override" (or, for an effective list, "this
// policy does not decide it"), while an empty list would mean the server sent
// one. Collapsing absent to `[]` would make a policy running on the platform
// default look like one that allows nothing.
func optionalStringList(ss []string) types.List {
	if ss == nil {
		return types.ListNull(types.StringType)
	}
	elems := make([]attr.Value, 0, len(ss))
	for _, s := range ss {
		elems = append(elems, types.StringValue(s))
	}
	return types.ListValueMust(types.StringType, elems)
}

// stringSliceFrom reads a list attribute, returning nil for null or unknown.
//
// Nil is what the caller turns into either "omit the field" (create) or "clear
// the override" (update); the two are distinguished at the call site, not here,
// because only the call site knows whether there is an override to clear.
func stringSliceFrom(ctx context.Context, l types.List) ([]string, diag.Diagnostics) {
	if l.IsNull() || l.IsUnknown() {
		return nil, nil
	}
	var out []string
	return out, l.ElementsAs(ctx, &out, false)
}

// allowListDelta renders one allow-list attribute into the PATCH body, and it
// is where Terraform's two config states become the server's three wire states.
//
// It writes nothing when the planned value equals state -- an update sends only
// what changed -- so an unrelated edit never restates an allow-list. Otherwise:
// a planned list is sent as itself, and a planned NULL against an override in
// state is sent as `[]`, the API's only spelling of "clear it".
//
// An UNKNOWN plan value is left alone rather than guessed at. It cannot reach
// Update in practice (the framework resolves the plan before apply), and the
// alternative -- treating unknown as null -- would clear an override because a
// value had not been computed yet.
func allowListDelta(ctx context.Context, plan, state types.List, out **[]string) diag.Diagnostics {
	if plan.IsUnknown() || plan.Equal(state) {
		return nil
	}
	v, diags := stringSliceFrom(ctx, plan)
	if diags.HasError() {
		return diags
	}
	if v == nil {
		v = []string{}
	}
	*out = &v
	return diags
}
