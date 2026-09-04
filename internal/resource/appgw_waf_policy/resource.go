// Package appgw_waf_policy implements the frostmoln_appgw_waf_policy Terraform
// resource.
package appgw_waf_policy

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/listvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
)

var (
	_ resource.Resource                   = &policyResource{}
	_ resource.ResourceWithImportState    = &policyResource{}
	_ resource.ResourceWithConfigure      = &policyResource{}
	_ resource.ResourceWithValidateConfig = &policyResource{}
	_ resource.ResourceWithModifyPlan     = &policyResource{}
)

// WAF policy scopes and modes.
//
// A GATEWAY-scoped policy attaches to the gateway and carries the managed (CRS)
// ruleset. An OVERLAY-scoped policy attaches to a listener or a route, is
// compiled WITHOUT the managed ruleset, and may carry only builder and raw
// rules -- so none of the CRS dials apply to it.
const (
	scopeGateway = "gateway"
	scopeOverlay = "overlay"

	modeDetect = "detect"
	modeBlock  = "block"
	// modeInherit takes the mode from the GATEWAY policy. Valid ONLY on an
	// overlay-scoped policy -- the server enforces that with a database check
	// -- and it is what an overlay defaults to.
	modeInherit = "inherit"
)

// Request body inspection bounds.
//
// 🔴 A FRAME CAP, NOT A MEMORY BUDGET. The body reaches the inspection engine
// over SPOE, whose frame size bounds what crosses in one piece, so the ceiling
// does not rise with a larger gateway. The provider previously advertised
// 128 KiB - 512 MiB; every value in that range is now a 400.
const (
	minRequestBodyLimitBytes = 4096
	maxRequestBodyLimitBytes = 40960
)

// PolicyModel is the Terraform state model for a WAF policy.
type PolicyModel struct {
	ID        types.String `tfsdk:"id"`
	GatewayID types.String `tfsdk:"gateway_id"`
	Name      types.String `tfsdk:"name"`

	Scope types.String `tfsdk:"scope"`

	Mode types.String `tfsdk:"mode"`
	// EffectiveMode is `mode` with `inherit` RESOLVED: server-computed and
	// read-only. It is what the engine actually does, and it is the only
	// honest input to "is this policy blocking?".
	EffectiveMode         types.String `tfsdk:"effective_mode"`
	ParanoiaLevel         types.Int64  `tfsdk:"paranoia_level"`
	AnomalyScoreThreshold types.Int64  `tfsdk:"anomaly_score_threshold"`
	CRSVersion            types.String `tfsdk:"managed_ruleset_version"`
	FailMode              types.String `tfsdk:"fail_mode"`
	RequestBodyLimitBytes types.Int64  `tfsdk:"request_body_limit_bytes"`

	// The two allow-list OVERRIDES. Null means this policy carries none and
	// runs on the platform default; see allowlists.go for the tri-state this
	// maps onto and why an empty list is refused.
	AllowedMethods             types.List `tfsdk:"allowed_methods"`
	AllowedRequestContentTypes types.List `tfsdk:"allowed_request_content_types"`
	// The lists that will actually compile: server-computed and read-only, the
	// override where one is set and the platform default otherwise.
	//
	// 🔴 NEVER FEED ONE OF THESE BACK INTO A WRITE. The platform default has
	// been widened three times; a configuration that copies today's effective
	// list into the override above pins the tenant out of the next widening
	// while appearing to change nothing.
	EffectiveAllowedMethods             types.List `tfsdk:"effective_allowed_methods"`
	EffectiveAllowedRequestContentTypes types.List `tfsdk:"effective_allowed_request_content_types"`

	ActiveVersion types.Int64 `tfsdk:"active_version"`
	DraftVersion  types.Int64 `tfsdk:"draft_version"`

	CreatedAt types.String `tfsdk:"created_at"`
	UpdatedAt types.String `tfsdk:"updated_at"`
}

type apiPolicy struct {
	ID        string `json:"id"`
	GatewayID string `json:"gatewayId"`
	Name      string `json:"name"`

	// Scope is omitted by the server when it is the default; treat an absent
	// value as "gateway", which is what the server defaults it to.
	Scope string `json:"scope,omitempty"`

	Mode string `json:"mode"`
	// EffectiveMode is READ-ONLY: it appears on a response and is never sent
	// on a write, so there is no field for it on either request type below.
	EffectiveMode         string `json:"effectiveMode,omitempty"`
	ParanoiaLevel         int    `json:"paranoiaLevel"`
	AnomalyScoreThreshold int    `json:"anomalyScoreThreshold"`
	CRSVersion            string `json:"crsVersion"`
	FailMode              string `json:"failMode"`
	RequestBodyLimitBytes int    `json:"requestBodyLimitBytes"`

	// Absent when this policy carries no override -- and absent on an overlay
	// policy, which cannot carry one. A nil slice is that absence; an empty
	// slice would be the server sending an empty list, which it does not.
	AllowedMethods             []string `json:"allowedMethods,omitempty"`
	AllowedRequestContentTypes []string `json:"allowedRequestContentTypes,omitempty"`
	// Server-computed, read-only, and absent on an overlay policy -- which does
	// not decide these lists, so stamping the platform default there would
	// claim a route enforces something it has no say in.
	EffectiveAllowedMethods             []string `json:"effectiveAllowedMethods,omitempty"`
	EffectiveAllowedRequestContentTypes []string `json:"effectiveAllowedRequestContentTypes,omitempty"`

	ActiveVersion *int `json:"activeVersion,omitempty"`
	DraftVersion  *int `json:"draftVersion,omitempty"`

	ConfigGeneration int64  `json:"configGeneration"`
	CreatedAt        string `json:"createdAt"`
	UpdatedAt        string `json:"updatedAt,omitempty"`
}

type apiCreatePolicyRequest struct {
	Name                  string `json:"name"`
	Scope                 string `json:"scope,omitempty"`
	Mode                  string `json:"mode,omitempty"`
	ParanoiaLevel         int    `json:"paranoiaLevel,omitempty"`
	AnomalyScoreThreshold int    `json:"anomalyScoreThreshold,omitempty"`
	CRSVersion            string `json:"crsVersion,omitempty"`
	FailMode              string `json:"failMode,omitempty"`
	RequestBodyLimitBytes int    `json:"requestBodyLimitBytes,omitempty"`
	// Plain slices, NOT pointers: a create has no override to clear, so it has
	// only two states -- omit for the platform default, or send a narrowing.
	// The tri-state belongs to the PATCH below.
	AllowedMethods             []string `json:"allowedMethods,omitempty"`
	AllowedRequestContentTypes []string `json:"allowedRequestContentTypes,omitempty"`
}

// apiUpdatePolicyRequest has NO scope and NO effectiveMode, and that mirrors the
// server exactly: its update body carries mode, paranoiaLevel,
// anomalyScoreThreshold, crsVersion, failMode, requestBodyLimitBytes and the two
// allow-list overrides, and nothing else. So scope is IMMUTABLE after creation
// -- confirmed, not assumed -- which is what makes RequiresReplace on the scope
// attribute correct rather than merely cautious. effectiveMode and the
// effective* lists are server-computed; a client that sent one would be
// asserting something it does not decide.
type apiUpdatePolicyRequest struct {
	Mode                  *string `json:"mode,omitempty"`
	ParanoiaLevel         *int    `json:"paranoiaLevel,omitempty"`
	AnomalyScoreThreshold *int    `json:"anomalyScoreThreshold,omitempty"`
	CRSVersion            *string `json:"crsVersion,omitempty"`
	FailMode              *string `json:"failMode,omitempty"`
	RequestBodyLimitBytes *int    `json:"requestBodyLimitBytes,omitempty"`

	// 🔴 POINTERS TO SLICES, AND THAT IS THE WHOLE POINT. `omitempty` omits a
	// NIL POINTER and marshals a pointer to an empty slice as `[]`, so this one
	// type expresses all three of the server's states: absent (leave alone),
	// `[]` (clear the override, return to the platform default) and a list.
	// A plain []string could not: `omitempty` would erase the `[]` that is the
	// only spelling of a reset the API has. See allowlists.go.
	AllowedMethods             *[]string `json:"allowedMethods,omitempty"`
	AllowedRequestContentTypes *[]string `json:"allowedRequestContentTypes,omitempty"`
}

type policyResource struct {
	client *client.Client
}

// NewResource returns a new WAF policy resource factory.
func NewResource() resource.Resource {
	return &policyResource{}
}

func (r *policyResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_appgw_waf_policy"
}

func (r *policyResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manages a Web Application Firewall policy on a Frostmoln Application Gateway.\n\n" +
			"## The firewall is composed, not singular\n\n" +
			"A `gateway`-scoped policy attaches to the gateway and carries the **managed ruleset**, " +
			"along with the dials that tune it — `paranoia_level`, `anomaly_score_threshold`, " +
			"`managed_ruleset_version`. An `overlay`-scoped policy attaches to one listener or one " +
			"route, is compiled **without** the managed ruleset, and carries only your own rules — " +
			"so none of those dials apply to it. Attach either with " +
			"`frostmoln_appgw_waf_policy_attachment`.\n\n" +
			"An overlay defaults to `mode = \"inherit\"`: it takes the gateway policy's mode, so it " +
			"**blocks when the gateway policy blocks**. Read `effective_mode`, never `mode`, to " +
			"decide whether a policy is refusing requests.\n\n" +
			"~> **These settings are AUTHORED, not enforced.** Changing `mode` (or any other " +
			"setting) writes the policy immediately, but what the gateway inspects with is the last " +
			"**published** version's snapshot. A policy can read `block` here while the gateway is " +
			"still only logging, until a `frostmoln_appgw_waf_policy_publication` publishes and the " +
			"gateway applies its configuration.\n\n" +
			"Settings are part of what a dry-run is taken against, so changing one invalidates the " +
			"current dry-run and the next publish needs a fresh one. That is deliberate: the dry-run " +
			"must describe the ruleset that is about to go live, settings included.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Description:   "The unique identifier of the WAF policy.",
				Computed:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"gateway_id": schema.StringAttribute{
				Description:   "The Application Gateway this policy belongs to.",
				Required:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"name": schema.StringAttribute{
				Description: "The name of the policy. The API has no rename, so changing this " +
					"forces a new resource.",
				Required:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"scope": schema.StringAttribute{
				Description: "`gateway` (the default) or `overlay`.\n\n" +
					"A `gateway` policy carries the managed ruleset and the dials that tune it. An " +
					"`overlay` policy carries only your own rules and attaches to a listener or a " +
					"route; `mode = \"inherit\"` is available to it, and the managed-ruleset dials " +
					"are not.\n\n" +
					"Changing this forces a new resource: scope decides what the policy is compiled " +
					"from, and the API has no field for it on an update.",
				Optional: true,
				Computed: true,
				Validators: []validator.String{
					stringvalidator.OneOf(scopeGateway, scopeOverlay),
				},
				// UseStateForUnknown BEFORE RequiresReplace. Reversed, the
				// framework's unknown-from-null-config plan value would be
				// compared against state on every update and record a
				// replacement — destroying the policy, its rules and its whole
				// version history for an unrelated change.
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
					stringplanmodifier.RequiresReplace(),
				},
			},
			"mode": schema.StringAttribute{
				Description: "`detect` records what the ruleset WOULD have done and blocks nothing; " +
					"`block` refuses matching requests; `inherit` takes the mode from the **gateway** " +
					"policy and is valid only on an `overlay`-scoped policy, where it is the " +
					"default.\n\n" +
					"`detect` is the safe place to start and the safe place to return to. Move to " +
					"`block` after a dry-run has shown you what would newly be refused.\n\n" +
					"~> `inherit` is **not** the cautious setting. Under a blocking gateway policy an " +
					"inheriting overlay blocks. Read `effective_mode` for what is actually in force.",
				Optional:      true,
				Computed:      true,
				Validators:    []validator.String{stringvalidator.OneOf(modeDetect, modeBlock, modeInherit)},
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"effective_mode": schema.StringAttribute{
				Description: "`mode` with `inherit` resolved — what the engine actually does. " +
					"Server-computed and read-only.\n\n" +
					"This is the attribute to key on. An overlay whose `mode` is `inherit` under a " +
					"blocking gateway policy has `effective_mode = \"block\"`, and a check written " +
					"against `mode` would report it as not blocking while its users' requests are " +
					"being refused.\n\n" +
					"It is **`null`** when the mode in force cannot be determined — the policy " +
					"inherits and the server did not resolve it. It is never the literal string " +
					"`inherit`: resolving that needs the gateway policy's mode, and this provider " +
					"surfaces an absent value rather than guessing one. Use `try()` or `coalesce()` " +
					"if your configuration must tolerate that.",
				Computed: true,
			},
			"paranoia_level": schema.Int64Attribute{
				Description: "How aggressively the managed ruleset matches, 1-4. Higher catches " +
					"more and produces more false positives.\n\n" +
					"`gateway`-scoped policies only: an overlay is not compiled with the managed " +
					"ruleset, so this has nothing to act on and the server refuses it.",
				Optional:      true,
				Computed:      true,
				Validators:    []validator.Int64{int64validator.Between(1, 4)},
				PlanModifiers: []planmodifier.Int64{int64planmodifier.UseStateForUnknown()},
			},
			"anomaly_score_threshold": schema.Int64Attribute{
				Description: "The accumulated anomaly score at which a request is refused.\n\n" +
					"`gateway`-scoped policies only, for the same reason as `paranoia_level`.",
				Optional:      true,
				Computed:      true,
				Validators:    []validator.Int64{int64validator.Between(1, 1000)},
				PlanModifiers: []planmodifier.Int64{int64planmodifier.UseStateForUnknown()},
			},
			"managed_ruleset_version": schema.StringAttribute{
				Description: "The managed ruleset version in force.\n\n" +
					"The server accepts only the version the running build ships, and migrating a " +
					"stored pin is the platform's job — so leave this unset unless you have a " +
					"specific reason, and read it rather than writing it.\n\n" +
					"`gateway`-scoped policies only.",
				Optional:      true,
				Computed:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"fail_mode": schema.StringAttribute{
				Description: "What happens when the inspection engine is unavailable: `open` passes " +
					"traffic through uninspected, `closed` refuses it. An explicit choice, never an " +
					"accident.",
				Optional:      true,
				Computed:      true,
				Validators:    []validator.String{stringvalidator.OneOf("open", "closed")},
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"request_body_limit_bytes": schema.Int64Attribute{
				Description: "How much of a request body is inspected, in bytes. Between 4096 and " +
					"40960. Anything beyond the limit is not examined.\n\n" +
					"The ceiling is the inspection engine's frame size, not a memory budget, so it " +
					"does not rise with a larger gateway flavor.",
				Optional:      true,
				Computed:      true,
				Validators:    []validator.Int64{int64validator.Between(minRequestBodyLimitBytes, maxRequestBodyLimitBytes)},
				PlanModifiers: []planmodifier.Int64{int64planmodifier.UseStateForUnknown()},
			},
			"allowed_methods": schema.ListAttribute{
				Description: "The HTTP methods the managed ruleset accepts, as an **override** of " +
					"the platform default.\n\n" +
					"Omit it unless you are narrowing: absent means the platform default, which is " +
					"kept current for you. **Removing this attribute from your configuration clears " +
					"the override** and returns the policy to that default — that is the plan you " +
					"will see (`[\"GET\"]` to `null`) and what the apply does. An empty list is " +
					"refused: it is not a narrowing to no methods, and it could not be kept.\n\n" +
					"Entries are uppercase method tokens. `TRACE`, `TRACK`, `CONNECT` and `DEBUG` " +
					"are refused on every gateway and may never be listed — it is a deny-list of " +
					"four, not a ceiling of seven. Everything else is yours to name, including the " +
					"WebDAV and CalDAV verbs (`PROPFIND`, `PROPPATCH`, `MKCOL`, `COPY`, `MOVE`, " +
					"`LOCK`, `UNLOCK`, `REPORT`, `SEARCH`), which the platform default does not " +
					"carry and which are therefore refused until you list them.\n\n" +
					"~> A method outside the list is refused with **403, independently of " +
					"`anomaly_score_threshold`** — at 5, at 100 and at 1000. Raising the threshold " +
					"to quiet false positives cannot switch this control off. A policy in `detect` " +
					"mode still logs and serves: this is a refusal, not a bypass of the mode.\n\n" +
					"Read `effective_allowed_methods` to see the list actually in force. " +
					"`gateway`-scoped policies only.",
				Optional:    true,
				ElementType: types.StringType,
				Validators: []validator.List{
					overrideNotEmpty{what: "methods"},
					listvalidator.SizeAtMost(maxAllowedMethods),
					listvalidator.UniqueValues(),
					listvalidator.ValueStringsAre(
						stringvalidator.RegexMatches(httpMethodPattern,
							"must be an uppercase HTTP method token such as GET or PROPFIND"),
						methodNotRefused{},
					),
				},
			},
			"allowed_request_content_types": schema.ListAttribute{
				Description: "The request `Content-Type` values the managed ruleset accepts, as an " +
					"**override** of the platform default.\n\n" +
					"Omit it unless you are changing the set. **Removing this attribute from your " +
					"configuration clears the override** and returns the policy to the platform " +
					"default; an empty list is refused, for the same reason as `allowed_methods`.\n\n" +
					"Unlike `allowed_methods` this may **add** types as well as remove them: a " +
					"vendor media type such as `application/vnd.acme.v2+json` is ordinary, and the " +
					"alternative is turning the whole content-type check off.\n\n" +
					"Write each entry lowercase as `type/subtype` with no parameters — " +
					"`application/json`, never `application/json; charset=utf-8`. The type is " +
					"compared without its parameters and without regard to case, and the header is " +
					"tested with a substring match. A request with no `Content-Type` at all is not " +
					"affected.\n\n" +
					"~> A body whose type is outside the list is refused with **403, independently " +
					"of `anomaly_score_threshold`**, exactly as for `allowed_methods`.\n\n" +
					"The platform default has been widened three times, so read " +
					"`effective_allowed_request_content_types` rather than keeping a copy of it. " +
					"`gateway`-scoped policies only.",
				Optional:    true,
				ElementType: types.StringType,
				Validators: []validator.List{
					overrideNotEmpty{what: "media types"},
					listvalidator.SizeAtMost(maxAllowedContentTypes),
					listvalidator.UniqueValues(),
					listvalidator.ValueStringsAre(
						stringvalidator.RegexMatches(mediaTypePattern,
							"must be a lowercase media type written as type/subtype with no parameters"),
					),
				},
			},
			"effective_allowed_methods": schema.ListAttribute{
				Description: "The method list that will actually compile: `allowed_methods` where " +
					"you set one, the platform default otherwise. Server-computed and read-only.\n\n" +
					"Render this if you render only one — it saves you carrying a copy of a default " +
					"that moves.\n\n" +
					"~> **Never feed this back into `allowed_methods`.** Copying it turns a default " +
					"that is maintained for you into a frozen override, and the change is invisible " +
					"in the plan the day you do it.\n\n" +
					"`null` on a listener- or route-scoped policy, which does not decide it.",
				Computed:    true,
				ElementType: types.StringType,
			},
			"effective_allowed_request_content_types": schema.ListAttribute{
				Description: "The media-type list that will actually compile. Same rule as " +
					"`effective_allowed_methods`, and the same warning with more force: this " +
					"default has been widened three times, so a configuration that copies it into " +
					"`allowed_request_content_types` pins the tenant out of the next widening.\n\n" +
					"`null` on a listener- or route-scoped policy.",
				Computed:    true,
				ElementType: types.StringType,
			},
			"active_version": schema.Int64Attribute{
				Description: "The version being ENFORCED. Null until something has been published.",
				Computed:    true,
			},
			"draft_version": schema.Int64Attribute{
				Description: "The version being EDITED. Rule and exclusion writes land here.",
				Computed:    true,
			},
			"created_at": schema.StringAttribute{
				Description:   "The creation timestamp.",
				Computed:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"updated_at": schema.StringAttribute{Description: "The last update timestamp.", Computed: true},
		},
	}
}

// ValidateConfig refuses at PLAN time what the server refuses at write time.
//
// Both rules are the composition's, not this provider's:
//
//   - mode = "inherit" is legal only on an overlay. The server holds a database
//     check saying so, and a gateway policy has nothing above it to inherit
//     from.
//   - the managed-ruleset dials belong to a gateway policy. An overlay is
//     compiled without that ruleset, so they have nothing to act on.
//
// Catching them here means the practitioner reads the reason in a plan, rather
// than an apply failing halfway with a 400 that names a JSON field.
func (r *policyResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var cfg PolicyModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() || cfg.Scope.IsUnknown() {
		return
	}
	// A null scope is a GATEWAY policy: the server defaults it. So "inherit"
	// with no scope written down is refused too, rather than quietly sent.
	scope := scopeGateway
	if !cfg.Scope.IsNull() {
		scope = cfg.Scope.ValueString()
	}

	if !cfg.Mode.IsNull() && !cfg.Mode.IsUnknown() &&
		cfg.Mode.ValueString() == modeInherit && scope != scopeOverlay {
		resp.Diagnostics.AddAttributeError(path.Root("mode"),
			`mode = "inherit" Needs scope = "overlay"`,
			"An inheriting policy takes its mode from the gateway policy, and a gateway-scoped "+
				"policy has nothing to inherit from. Set scope = \"overlay\" if this policy attaches "+
				"to a listener or a route, or use \"detect\" or \"block\" here.")
	}

	if scope != scopeOverlay {
		return
	}
	const dialDetail = "An overlay policy is compiled WITHOUT the managed ruleset, so this setting has " +
		"nothing to act on and the server refuses it. Set it on the gateway-scoped policy " +
		"instead; an overlay carries your own builder and raw rules only."
	// The allow-lists are refused for a related but distinct reason, and saying
	// the right one matters: they are not a dial on a ruleset the overlay
	// lacks, they are a gateway-wide decision. The server refuses them at BOTH
	// doors -- create and update -- so a plan-time refusal here costs nothing
	// and replaces a 400 that names a JSON field halfway through an apply.
	const allowListDetail = "The gateway runs ONE managed ruleset and reads this list from the " +
		"gateway-scoped policy alone, so a value here would be stored and never applied -- which " +
		"is why the server refuses it at create and at update alike. Set it on the gateway-scoped " +
		"policy; every listener and route behind that gateway is covered by it.\n\n" +
		"An overlay's own effective list is reported as null for the same reason: it does not " +
		"decide one."
	for _, setting := range []struct {
		attr   string
		set    bool
		detail string
	}{
		{"paranoia_level", !cfg.ParanoiaLevel.IsNull() && !cfg.ParanoiaLevel.IsUnknown(), dialDetail},
		{"anomaly_score_threshold", !cfg.AnomalyScoreThreshold.IsNull() && !cfg.AnomalyScoreThreshold.IsUnknown(), dialDetail},
		{"managed_ruleset_version", !cfg.CRSVersion.IsNull() && !cfg.CRSVersion.IsUnknown(), dialDetail},
		{"allowed_methods", !cfg.AllowedMethods.IsNull() && !cfg.AllowedMethods.IsUnknown(), allowListDetail},
		{"allowed_request_content_types", !cfg.AllowedRequestContentTypes.IsNull() && !cfg.AllowedRequestContentTypes.IsUnknown(), allowListDetail},
	} {
		if !setting.set {
			continue
		}
		resp.Diagnostics.AddAttributeError(path.Root(setting.attr),
			setting.attr+` Belongs To A gateway-scoped Policy`, setting.detail)
	}
}

// ModifyPlan decides, for each server-computed effective_* attribute, between
// "unknown" and "unchanged" -- and BOTH answers are load-bearing.
//
// 🔴 WITHOUT THE UNKNOWN BRANCH, SETTING allowed_methods IS A GUARANTEED APPLY
// FAILURE. effective_* are Computed-only, so the framework proposes their PRIOR
// STATE value, and Terraform treats a computed attribute whose applied value
// differs from a KNOWN planned value as "Provider produced inconsistent result
// after apply" -- an error, not a warning. Narrowing allowed_methods changes
// effective_allowed_methods by definition, so every such apply would fail on
// the value it had just correctly written.
//
// 🔴 WITHOUT THE UNCHANGED BRANCH, EVERY OVERLAY POLICY DIFFS FOREVER. An
// overlay decides none of these three, so the server sends none of them and
// they are permanently null -- and MarkComputedNilsAsUnknown turns a null
// computed attribute into "(known after apply)" on every plan. Terraform then
// schedules an update, the apply sends an empty PATCH, the read returns null
// again, and the next plan says the same thing. A plan that shows a diff when
// nothing changed is a bug, so the value is pinned back to what the refresh
// found.
//
// 🔴 AND THAT PINNING IS NOT UseStateForUnknown, WHICH WOULD LIE HERE. The
// schema modifier cannot see WHY a value is unknown, so it would pin the old
// effective list across the very apply that changes it -- the first failure
// above. The condition is what makes pinning honest: it fires only when nothing
// in this configuration decides the value, and the state it pins to is the
// REFRESHED state, so an effective value that moved on its own (the gateway
// policy's mode flipped, the platform default was widened) has already been
// read back before this runs.
func (r *policyResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	// Create (no prior state) and destroy (no plan) have nothing to decide: the
	// framework already marks every computed attribute unknown on a create.
	if req.State.Raw.IsNull() || req.Plan.Raw.IsNull() {
		return
	}
	var plan, state PolicyModel
	if d := req.Plan.Get(ctx, &plan); d.HasError() {
		return
	}
	if d := req.State.Get(ctx, &state); d.HasError() {
		return
	}
	// A replacement is a NEW policy, and pinning a computed value to the old
	// one's state would describe the object being destroyed. Every attribute
	// that forces one is checked, so adding another cannot quietly skip this.
	if !plan.GatewayID.Equal(state.GatewayID) || !plan.Name.Equal(state.Name) ||
		!plan.Scope.Equal(state.Scope) {
		return
	}

	// 🔴 AN INHERITING POLICY DOES NOT DECIDE ITS OWN EFFECTIVE MODE, so the
	// "did this policy's mode change" test cannot speak for it.
	//
	// The first case below asks whether THIS policy's `mode` moved. For an
	// overlay at `inherit` the mode in force is decided by the GATEWAY policy,
	// which is a different resource with no dependency edge to this one. One
	// apply that flips the gateway policy detect -> block and touches any
	// attribute of an attached overlay (fail_mode, say) leaves this policy's
	// `mode` equal, so that test alone pins effective_mode to the
	// refreshed-but-now-stale "detect"; the gateway policy applies first, this
	// Update reads back "block", and Terraform fails the apply with
	//   Provider produced inconsistent result after apply: .effective_mode:
	//   was cty.StringVal("detect"), but now cty.StringVal("block")
	//
	// 🔴 AND `inherit` ALONE IS NOT THE CONDITION -- THE APPLY HAPPENING IS.
	// Going unknown on every inheriting overlay would put "(known after apply)"
	// on one that is not being changed at all, and that is the perpetual diff
	// the pin exists to kill: the plan reports an update, the apply sends an
	// empty PATCH, the read returns the same value, forever. A plan that
	// changes no settable attribute reaches no Update, so nothing can
	// contradict the pin there. Where an update IS coming, unknown costs one
	// extra "(known after apply)" line on a resource already being changed.
	switch {
	case !plan.Mode.Equal(state.Mode):
		resp.Plan.SetAttribute(ctx, path.Root("effective_mode"), types.StringUnknown())
	case plan.Mode.ValueString() == modeInherit && planReachesUpdate(req):
		resp.Plan.SetAttribute(ctx, path.Root("effective_mode"), types.StringUnknown())
	default:
		resp.Plan.SetAttribute(ctx, path.Root("effective_mode"), state.EffectiveMode)
	}
	for _, eff := range []struct {
		attr    string
		changed bool
		known   types.List
	}{
		{"effective_allowed_methods",
			!plan.AllowedMethods.Equal(state.AllowedMethods), state.EffectiveAllowedMethods},
		{"effective_allowed_request_content_types",
			!plan.AllowedRequestContentTypes.Equal(state.AllowedRequestContentTypes),
			state.EffectiveAllowedRequestContentTypes},
	} {
		if eff.changed {
			resp.Plan.SetAttribute(ctx, path.Root(eff.attr), types.ListUnknown(types.StringType))
			continue
		}
		resp.Plan.SetAttribute(ctx, path.Root(eff.attr), eff.known)
	}
}

// planReachesUpdate reports whether this plan will actually call Update, which
// is the only situation in which a pinned computed value can be contradicted.
//
// It compares every attribute a practitioner can set, taken off the SCHEMA
// rather than from a hand-written list, so an attribute added later cannot
// quietly fall outside it. Computed-only attributes are skipped deliberately:
// they are the values this decides, and the framework has already turned the
// null ones into unknowns, so including them would answer "changed" on every
// plan and make the function useless.
//
// A raw value that will not decode answers "an update is coming": that costs an
// unknown, where the other answer would be a pin nothing has checked.
func planReachesUpdate(req resource.ModifyPlanRequest) bool {
	var planAttrs, stateAttrs map[string]tftypes.Value
	if err := req.Plan.Raw.As(&planAttrs); err != nil {
		return true
	}
	if err := req.State.Raw.As(&stateAttrs); err != nil {
		return true
	}
	for name, a := range req.Plan.Schema.GetAttributes() {
		if !a.IsOptional() && !a.IsRequired() {
			continue
		}
		if !planAttrs[name].Equal(stateAttrs[name]) {
			return true
		}
	}
	return false
}

func (r *policyResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	c, ok := req.ProviderData.(*client.Client)
	if !ok {
		resp.Diagnostics.AddError("Unexpected Provider Data",
			fmt.Sprintf("Expected *client.Client, got %T", req.ProviderData))
		return
	}
	r.client = c
}

func (m *PolicyModel) fromAPI(p *apiPolicy) {
	m.ID = types.StringValue(p.ID)
	m.GatewayID = types.StringValue(p.GatewayID)
	m.Name = types.StringValue(p.Name)
	// An absent scope is "gateway": the server omits the default. Reporting it
	// as null would make a config that never mentioned scope show a permanent
	// diff, and a config that set it to "gateway" plan a replacement.
	m.Scope = types.StringValue(scopeOrDefault(p.Scope))
	m.Mode = types.StringValue(p.Mode)
	// NULL when the mode in force cannot be determined -- never the authored
	// token, which would be a wrong answer dressed as a right one.
	m.EffectiveMode = effectiveModeValue(p.Mode, p.EffectiveMode)
	m.ParanoiaLevel = types.Int64Value(int64(p.ParanoiaLevel))
	m.AnomalyScoreThreshold = types.Int64Value(int64(p.AnomalyScoreThreshold))
	m.CRSVersion = types.StringValue(p.CRSVersion)
	m.FailMode = types.StringValue(p.FailMode)
	m.RequestBodyLimitBytes = types.Int64Value(int64(p.RequestBodyLimitBytes))
	// Absent stays NULL on all four. For the overrides that is "this policy
	// carries none, so it runs on the platform default"; for the effective
	// lists it is "this policy does not decide them" (an overlay). Rendering
	// either as [] would say "allows nothing", which is a different claim.
	m.AllowedMethods = optionalStringList(p.AllowedMethods)
	m.AllowedRequestContentTypes = optionalStringList(p.AllowedRequestContentTypes)
	m.EffectiveAllowedMethods = optionalStringList(p.EffectiveAllowedMethods)
	m.EffectiveAllowedRequestContentTypes = optionalStringList(p.EffectiveAllowedRequestContentTypes)
	// Null rather than 0: "nothing published yet" and "version 0" are
	// different, and version 0 does not exist.
	m.ActiveVersion = optionalInt(p.ActiveVersion)
	m.DraftVersion = optionalInt(p.DraftVersion)
	m.CreatedAt = types.StringValue(p.CreatedAt)
	m.UpdatedAt = types.StringValue(p.UpdatedAt)
}

func (r *policyResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan PolicyModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	// A null override is simply OMITTED here. A create has nothing to clear --
	// a brand-new policy carries no override -- so the create body has two
	// states, not three, and `[]` would be the same request as omitting it.
	methods, d := stringSliceFrom(ctx, plan.AllowedMethods)
	resp.Diagnostics.Append(d...)
	contentTypes, d := stringSliceFrom(ctx, plan.AllowedRequestContentTypes)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}

	apiResp, err := r.client.Post(ctx, r.client.TenantPath(fmt.Sprintf(
		"/application-gateways/%s/waf-policies", plan.GatewayID.ValueString(),
	)),
		apiCreatePolicyRequest{
			Name:                       plan.Name.ValueString(),
			Scope:                      str(plan.Scope),
			Mode:                       str(plan.Mode),
			ParanoiaLevel:              int(plan.ParanoiaLevel.ValueInt64()),
			AnomalyScoreThreshold:      int(plan.AnomalyScoreThreshold.ValueInt64()),
			CRSVersion:                 str(plan.CRSVersion),
			FailMode:                   str(plan.FailMode),
			RequestBodyLimitBytes:      int(plan.RequestBodyLimitBytes.ValueInt64()),
			AllowedMethods:             methods,
			AllowedRequestContentTypes: contentTypes,
		})
	if err != nil {
		resp.Diagnostics.AddError("Failed to Create WAF Policy", err.Error())
		return
	}
	p, err := client.ParseResponse[apiPolicy](apiResp)
	if err != nil {
		resp.Diagnostics.AddError("Failed to Parse WAF Policy Response", err.Error())
		return
	}
	plan.fromAPI(p)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *policyResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state PolicyModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	apiResp, err := r.client.Get(ctx, r.policyPath(state.GatewayID.ValueString(), state.ID.ValueString()), nil)
	if err != nil {
		if client.IsNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Failed to Read WAF Policy", err.Error())
		return
	}
	p, err := client.ParseResponse[apiPolicy](apiResp)
	if err != nil {
		resp.Diagnostics.AddError("Failed to Parse WAF Policy Response", err.Error())
		return
	}
	state.fromAPI(p)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Update sends only the settings that actually changed.
//
// Sending a value that is already set is not harmless: the server validates a
// SUPPLIED managed-ruleset version against the running build while deliberately
// exempting a STORED one, so re-sending an unchanged pin turns that exemption
// off and can fail an apply that changed nothing about it.
func (r *policyResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state PolicyModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	body := apiUpdatePolicyRequest{}
	if !plan.Mode.Equal(state.Mode) {
		v := plan.Mode.ValueString()
		body.Mode = &v
	}
	if !plan.ParanoiaLevel.Equal(state.ParanoiaLevel) {
		v := int(plan.ParanoiaLevel.ValueInt64())
		body.ParanoiaLevel = &v
	}
	if !plan.AnomalyScoreThreshold.Equal(state.AnomalyScoreThreshold) {
		v := int(plan.AnomalyScoreThreshold.ValueInt64())
		body.AnomalyScoreThreshold = &v
	}
	if !plan.CRSVersion.Equal(state.CRSVersion) {
		v := plan.CRSVersion.ValueString()
		body.CRSVersion = &v
	}
	if !plan.FailMode.Equal(state.FailMode) {
		v := plan.FailMode.ValueString()
		body.FailMode = &v
	}
	if !plan.RequestBodyLimitBytes.Equal(state.RequestBodyLimitBytes) {
		v := int(plan.RequestBodyLimitBytes.ValueInt64())
		body.RequestBodyLimitBytes = &v
	}
	// 🔴 THE TRI-STATE, AND THE ONE PLACE ALL THREE WIRE STATES ARE PRODUCED.
	//
	//   plan == state          -> the pointer stays nil, the field is ABSENT,
	//                             and the server leaves the override alone.
	//   plan is NULL, state is a list
	//                          -> the practitioner REMOVED the attribute from
	//                             their configuration, which means "stop
	//                             overriding". That is `[]` on the wire: the
	//                             only spelling of a reset the API has.
	//   plan is a list         -> send exactly that list.
	//
	// Omitting the field in the middle case would be the silent failure the
	// whole design is against: the plan would show the attribute going to null,
	// the apply would report success, and the narrowing would still be in force
	// -- with the next refresh reading it back and proposing the same removal
	// forever. See allowlists.go.
	resp.Diagnostics.Append(allowListDelta(ctx, plan.AllowedMethods, state.AllowedMethods,
		&body.AllowedMethods)...)
	resp.Diagnostics.Append(allowListDelta(ctx, plan.AllowedRequestContentTypes,
		state.AllowedRequestContentTypes, &body.AllowedRequestContentTypes)...)
	if resp.Diagnostics.HasError() {
		return
	}

	apiResp, err := r.client.Patch(ctx, r.policyPath(state.GatewayID.ValueString(), state.ID.ValueString()), body)
	if err != nil {
		resp.Diagnostics.AddError("Failed to Update WAF Policy", err.Error())
		return
	}
	p, err := client.ParseResponse[apiPolicy](apiResp)
	if err != nil {
		resp.Diagnostics.AddError("Failed to Parse WAF Policy Response", err.Error())
		return
	}
	plan.fromAPI(p)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)

	// 🔴 SAY WHAT THE POLICY NOW DOES, NOT WHAT WAS WRITTEN. Setting an overlay
	// to "inherit" under a blocking gateway starts refusing requests, and a
	// practitioner reading only their own diff sees the word "inherit" and
	// takes it for a relaxation.
	if body.Mode != nil && plan.Mode.ValueString() == modeInherit {
		inForce := "not something the server resolved, so it is unknown from here"
		if !plan.EffectiveMode.IsNull() {
			inForce = fmt.Sprintf("currently %q", plan.EffectiveMode.ValueString())
		}
		resp.Diagnostics.AddWarning("This Policy Now Takes The Gateway Policy's Mode",
			fmt.Sprintf("mode = \"inherit\" resolves against the gateway policy, which is %s — so "+
				"that is what this policy does. Changing the gateway policy's mode changes this "+
				"one with it; read effective_mode rather than mode to see what is in force.",
				inForce))
	}

	if body.Mode != nil || body.ParanoiaLevel != nil || body.AnomalyScoreThreshold != nil ||
		body.FailMode != nil || body.RequestBodyLimitBytes != nil ||
		body.AllowedMethods != nil || body.AllowedRequestContentTypes != nil {
		resp.Diagnostics.AddWarning("Settings Changed But Not Yet Enforced",
			"These settings are part of the ruleset, so this change is not in force until it is "+
				"published and the gateway's configuration is applied. Until then the gateway keeps "+
				"inspecting with the previously published version.")
	}
}

func (r *policyResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state PolicyModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	_, err := r.client.Delete(ctx, r.policyPath(state.GatewayID.ValueString(), state.ID.ValueString()))
	if err != nil && !client.IsNotFound(err) {
		resp.Diagnostics.AddError("Failed to Delete WAF Policy", err.Error())
	}
}

func (r *policyResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	parts, err := client.ParseImportID(req.ID, "gateway_id", "policy_id")
	if err != nil {
		resp.Diagnostics.AddError("Invalid Import ID", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("gateway_id"), parts[0])...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), parts[1])...)
}

func (r *policyResource) policyPath(gwID, policyID string) string {
	return r.client.TenantPath(fmt.Sprintf("/application-gateways/%s/waf-policies/%s", gwID, policyID))
}

// scopeOrDefault treats an absent scope as gateway, which is what the server
// defaults it to.
func scopeOrDefault(scope string) string {
	if scope == "" {
		return scopeGateway
	}
	return scope
}

// effectiveMode returns the mode actually in force, or "" when that cannot be
// known from this policy alone.
//
// 🔴 `mode` alone is the AUTHORED value. For an inheriting overlay it is the
// string "inherit", so a check written as `mode == "block"` is FALSE for a
// policy that is refusing requests right now. That is what effective_mode is
// for.
//
// 🔴 NEVER INVENT A RESOLUTION -- SURFACE UNKNOWN. When the server omits
// effectiveMode, an authored "inherit" resolves to nothing this provider can
// compute: the answer lives on the GATEWAY policy. Returning the authored token
// would put the literal string "inherit" into an attribute whose documented
// contract is "mode with inherit resolved", so
// `self.effective_mode == "block" ? 1 : 0` would take the wrong branch
// confidently instead of surfacing an absent value the practitioner can see and
// handle. An authored detect or block needs no resolution and is returned as
// itself; only inherit can be unknown.
func effectiveMode(mode, computed string) string {
	if computed != "" {
		return computed
	}
	if mode == modeInherit {
		return ""
	}
	return mode
}

// effectiveModeValue renders the effective mode as a Terraform value, using
// NULL for "cannot be determined".
//
// Null is the honest Terraform spelling of unknown-at-rest: it is what `try()`
// and `coalesce()` are built to handle, and unlike the empty string it cannot
// be mistaken for a mode. This is dormant against today's server -- it always
// sends effectiveMode on a policy -- and it is one server change from live.
func effectiveModeValue(mode, computed string) types.String {
	if eff := effectiveMode(mode, computed); eff != "" {
		return types.StringValue(eff)
	}
	return types.StringNull()
}

func str(s types.String) string {
	if s.IsNull() || s.IsUnknown() {
		return ""
	}
	return s.ValueString()
}

func optionalInt(n *int) types.Int64 {
	if n == nil {
		return types.Int64Null()
	}
	return types.Int64Value(int64(*n))
}
