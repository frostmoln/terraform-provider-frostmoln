// Package appgw_waf_policy_attachment implements the
// frostmoln_appgw_waf_policy_attachment Terraform resource.
//
// WHERE a WAF policy applies is a separate act from WHAT it says, and this
// resource is the "where". A policy exists, is authored, is published and is
// rolled back without being attached to anything, and detaching one does not
// delete it -- so the attachment is its own object with its own lifecycle
// rather than a field on the policy.
//
// It is one resource for all three levels rather than three, because the levels
// differ only in which path they PUT to and which scope they accept. Three
// resources would be three copies of the same Read, the same drift handling and
// the same scope check, and the one that got a fix would not be the one a
// practitioner happened to use.
package appgw_waf_policy_attachment

import (
	"context"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/scopedecl"
)

var (
	_ resource.Resource                   = &attachmentResource{}
	_ resource.ResourceWithConfigure      = &attachmentResource{}
	_ resource.ResourceWithImportState    = &attachmentResource{}
	_ resource.ResourceWithValidateConfig = &attachmentResource{}
	_ resource.ResourceWithModifyPlan     = &attachmentResource{}
)

const (
	scopeGateway = "gateway"
	scopeOverlay = "overlay"

	// modeInherit is an AUTHORED value only. It is never a value this resource
	// reports as an effective mode -- see applyPolicy.
	modeInherit = "inherit"
)

// AttachmentModel is the Terraform state model for a WAF policy attachment.
type AttachmentModel struct {
	ID        types.String `tfsdk:"id"`
	GatewayID types.String `tfsdk:"gateway_id"`
	// ListenerID and RouteID together choose the LEVEL: neither is the
	// gateway, listener alone is a listener, and both is a route.
	ListenerID types.String `tfsdk:"listener_id"`
	RouteID    types.String `tfsdk:"route_id"`

	PolicyID types.String `tfsdk:"policy_id"`

	Scope         types.String `tfsdk:"scope"`
	EffectiveMode types.String `tfsdk:"effective_mode"`
}

// apiAttachRequest names the policy to attach.
//
// 🔴 THE FIELD IS policyId. The server binds one struct in all three attach
// handlers and declares it `binding:"required"`, so a body under any other name
// is a MISSING REQUIRED FIELD -- a 400 on every attach, at every level, every
// time. The plausible wrong answer is wafPolicyId, which is what this same
// referent is called when it is read back off a gateway, listener or route
// (apiAttachee below): there the attachee is the subject and the policy is one
// of its attributes, so the attribute is qualified; here the attachee is
// already named by the URL and the policy is the whole value being written.
//
// One field, because an attachment decides only WHERE a policy applies -- a
// mode or a setting riding along here would be a second, unaudited way to
// change what it does.
type apiAttachRequest struct {
	PolicyID string `json:"policyId"`
}

// apiPolicy is the slice of a WAF policy this resource reads: enough to check
// the scope fits the level, and to report what the attached policy actually
// does.
type apiPolicy struct {
	ID            string `json:"id"`
	Scope         string `json:"scope,omitempty"`
	Mode          string `json:"mode"`
	EffectiveMode string `json:"effectiveMode,omitempty"`
}

// apiAttachee is the slice of a gateway, listener or route that says which
// policy is attached to it. All three carry the field under the same name.
type apiAttachee struct {
	WAFPolicyID string `json:"wafPolicyId,omitempty"`
}

type attachmentResource struct {
	client *client.Client
}

// NewResource returns a new WAF policy attachment resource factory.
func NewResource() resource.Resource {
	return &attachmentResource{}
}

func (r *attachmentResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_appgw_waf_policy_attachment"
}

func (r *attachmentResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Attaches a Web Application Firewall policy to an Application Gateway, to one " +
			"of its listeners, or to one of its routes.\n\n" +
			"## Three levels, two scopes\n\n" +
			"With neither `listener_id` nor `route_id` this attaches to the **gateway**, which takes " +
			"a `gateway`-scoped policy — the one carrying the managed ruleset. With `listener_id`, " +
			"or with `listener_id` and `route_id`, it attaches an **overlay**-scoped policy to that " +
			"listener or route.\n\n" +
			"The scope has to match the level and the provider checks it before sending: attaching " +
			"an overlay to the gateway would leave the gateway with no managed protection at all, " +
			"and that is not a mistake worth discovering from a 400.\n\n" +
			"~> **A `tcp` listener cannot carry a policy, and neither can anything beneath it.** " +
			"The firewall inspects HTTP requests; a tcp listener forwards bytes and parses none, so " +
			"attaching a policy to one is refused. Its traffic is filtered by the listener's own " +
			"source-CIDR, geo, rate-limit and connection controls instead.\n\n" +
			"## Attaching is not authoring\n\n" +
			"The policy is a separate resource with its own rules and version history. Destroying " +
			"this attachment detaches the policy; it does not delete it, and the policy can be " +
			"attached again.\n\n" +
			"~> **Detaching the gateway policy changes every inheriting overlay.** An overlay whose " +
			"`mode` is `inherit` resolves against the gateway policy; with no gateway policy there " +
			"is nothing blocking to inherit, so those overlays fall back to detect and stop refusing " +
			"anything.\n\n" +
			"~> **Attaching is not applying.** The attachment reaches the appliance on the " +
			"gateway's next configuration apply — see `frostmoln_appgw_config_apply`." +
			"\n\n" + scopedecl.Summary("frostmoln_appgw_waf_policy_attachment"),
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Description: "The identifier of this attachment: the point it attaches to.",
				Computed:    true,
			},
			"gateway_id": schema.StringAttribute{
				Description:   "The Application Gateway.",
				Required:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"listener_id": schema.StringAttribute{
				Description: "Attach to this listener instead of the gateway. Requires an " +
					"`overlay`-scoped policy.",
				Optional:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"route_id": schema.StringAttribute{
				Description: "Attach to this route instead of the listener. Requires `listener_id` " +
					"(a route id is unique only within its listener) and an `overlay`-scoped policy.",
				Optional:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"policy_id": schema.StringAttribute{
				Description: "The WAF policy to attach. Changing it replaces the attachment in " +
					"place — the API's PUT is a replace, so there is no window with nothing attached.",
				Required: true,
			},
			"scope": schema.StringAttribute{
				Description: "The attached policy's scope, read back from it: `gateway` or `overlay`.",
				Computed:    true,
			},
			"effective_mode": schema.StringAttribute{
				Description: "What the attached policy itself resolves to — its `mode` with " +
					"`inherit` resolved.\n\n" +
					"Read this, never the policy's `mode`: an overlay set to `inherit` under a " +
					"blocking gateway policy reads `inherit` there and **blocks**. It is `null` " +
					"when that cannot be determined, and never the literal string `inherit`.\n\n" +
					"~> This is **this policy's** mode, not \"what is in force for traffic\". The " +
					"platform composes one artifact per gateway with the managed ruleset applied " +
					"across the whole gateway; an overlay adds rules to its listener or route and " +
					"never exempts it from that baseline. So a detecting overlay does not mean " +
					"traffic to that route is unprotected.",
				Computed: true,
			},
		},
	}
}

// ValidateConfig refuses a route named without its listener.
//
// A route id is unique only within a listener, so `route_id` alone addresses
// nothing -- and the path it would build is the listener collection, not a
// route.
func (r *attachmentResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var cfg AttachmentModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	hasRoute := !cfg.RouteID.IsNull() && !cfg.RouteID.IsUnknown()
	hasListener := !cfg.ListenerID.IsNull() && !cfg.ListenerID.IsUnknown()
	if hasRoute && !hasListener {
		resp.Diagnostics.AddAttributeError(path.Root("route_id"),
			"route_id Needs listener_id",
			"A route id is unique only within its listener, so a route on its own does not name "+
				"an attachment point. Set listener_id to the route's listener.")
	}
}

// ModifyPlan decides between "unknown" and "unchanged" for effective_mode.
//
// 🔴 ON TODAY'S SCHEMA, NEITHER BRANCH CHANGES A PLAN. Both write the value the
// framework has already put there. That is not a reason to delete them -- each
// guards a different, plausible edit that WOULD change a plan, and each one's
// trigger is named below. It is a reason not to describe them as rescues: a
// hazard that does not reproduce gets the guard deleted by the next reader who
// goes looking for it.
//
// The one fact both arguments turn on is what MarkComputedNilsAsUnknown keys
// on. It is the CONFIG value, not the planned value (verified against
// terraform-plugin-framework v1.19.0: the transform returns early only for a
// non-null config value, a non-Computed attribute, or one carrying a schema
// Default -- it never inspects the value in the plan). id, scope and
// effective_mode are all Computed-only, absent from configuration and
// Default-less, so on any plan where the transform runs, all three arrive here
// already unknown. CLAUDE.md states the same rule for the whole provider.
//
// 🔴 THE UNKNOWN BRANCH GUARDS AGAINST UseStateForUnknown BEING ADDED HERE.
// Re-pointing an attachment at another policy is the one non-replacing edit
// this resource has, and it is exactly what changes the mode in force -- so the
// plan must not promise the old policy's mode. Today it does not: policy_id
// differs, the transform runs, and effective_mode is unknown before this method
// is reached. Deleting this branch changes nothing.
//
// What changes everything is one line in the schema. This provider adds
// `UseStateForUnknown()` to Computed attributes as a matter of course -- 343 of
// them across 83 files, codified in CLAUDE.md -- and it is the obvious reach for
// anyone wanting to quiet effective_mode's "(known after apply)" churn. That
// modifier copies the state value over an unknown planned value, and schema
// modifiers run BEFORE this method. So with it added, effective_mode reaches
// this branch pinned to the PRIOR policy's mode, this SetAttribute is the only
// thing that restores unknown, and without it the run ends:
//
//	Provider produced inconsistent result after apply: .effective_mode:
//	was cty.StringVal("detect"), but now cty.StringVal("block")
//
// It is not dead code; do not delete it.
//
// 🔴 THE UNCHANGED BRANCH GUARDS AGAINST A NEW NON-REPLACING ATTRIBUTE. The
// diff it prevents is real in shape: where the server does not resolve an
// inheriting overlay, effective_mode is null, and an unknown planted over that
// null is an update reported forever, with an empty re-PUT of an attachment
// nobody asked to move behind it. An unchanged policy_id reaches no Update, so
// nothing can contradict the value the refresh found.
//
// Today that cannot happen, because the transform is GATED. In
// fwserver.PlanResourceChange it runs under
//
//	if !resp.PlannedState.Raw.IsNull() &&
//		!resp.PlannedState.Raw.Equal(req.PriorState.Raw) {
//
// -- so only on a plan that ALREADY differs from prior, not on every plan. (The
// compared value is the planned state after the framework's own defaulting and
// write-only nulling passes, which are no-ops on this schema; the first schema
// Default added here would change what that comparison sees.) On THIS schema
// that gate and this branch are mutually exclusive. A Computed-only attribute
// keeps its prior value in a proposed new state, so it can never be the source
// of the difference; gateway_id, listener_id and route_id carry RequiresReplace
// and hit the replacement early-return below; and policy_id, the only attribute
// left that could differ, takes the unknown branch. So no plan Terraform core
// can produce reaches this line with the states differing, the transform never
// ran, and the value written is the value already there. (Nothing ENFORCES
// that -- the RPC will plan whatever proposed state it is handed, and
// plan_rpc_test.go constructs the excluded shape on purpose to isolate the
// ordering.)
//
// 🔴 IT GOES LIVE THE MOMENT A NON-REPLACING ATTRIBUTE THAT IS NOT policy_id IS
// ADDED TO THE SCHEMA. That single change is what lets the gate fire on a plan
// that still reaches this branch, and from then on an unresolved effective_mode
// arrives here unknown and diffs forever unless it is pinned back. Until then
// the branch still holds the shape against the over-broad fix -- an
// unconditional SetUnknown reads reasonable and costs exactly that perpetual
// diff. It is not dead code; do not delete it.
//
// The ordering both branches depend on is established, not assumed:
// TestAttachmentPlanRPCMarksComputedNilsUnknownBeforeModifyPlan drives the real
// RPC and shows the transform running BEFORE this method, so a value written
// here survives to the planned state.
//
// 🔴 AND THE CONDITION IS policy_id, NOT `inherit`. The cross-resource hazard
// the POLICY resource has to handle -- the gateway policy flipping under an
// unchanged overlay -- cannot produce an inconsistent result here, because an
// attachment with an unchanged policy_id plans no apply at all.
//
// scope is deliberately left alone: attach REFUSES a policy whose scope does
// not fit the level, so an update that would change it fails with that
// diagnostic rather than an inconsistent result.
func (r *attachmentResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	// Create (no prior state) and destroy (no plan) have nothing to decide: on a
	// create the framework already marks every Computed attribute whose config
	// value is null and which carries no schema Default as unknown -- which on
	// this schema is all three of them.
	if req.State.Raw.IsNull() || req.Plan.Raw.IsNull() {
		return
	}
	var plan, state AttachmentModel
	if d := req.Plan.Get(ctx, &plan); d.HasError() {
		return
	}
	if d := req.State.Get(ctx, &state); d.HasError() {
		return
	}
	// A replacement is a NEW attachment, and pinning a computed value to the
	// old one's state would describe the object being destroyed. Every
	// attribute that forces one is checked, so adding another cannot quietly
	// skip this.
	if !plan.GatewayID.Equal(state.GatewayID) || !plan.ListenerID.Equal(state.ListenerID) ||
		!plan.RouteID.Equal(state.RouteID) {
		return
	}
	if !plan.PolicyID.Equal(state.PolicyID) {
		resp.Plan.SetAttribute(ctx, path.Root("effective_mode"), types.StringUnknown())
		return
	}
	resp.Plan.SetAttribute(ctx, path.Root("effective_mode"), state.EffectiveMode)
}

func (r *attachmentResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

// level reports which of the three attachment points this model names.
func (m *AttachmentModel) level() string {
	switch {
	case !m.RouteID.IsNull() && m.RouteID.ValueString() != "":
		return "route"
	case !m.ListenerID.IsNull() && m.ListenerID.ValueString() != "":
		return "listener"
	default:
		return "gateway"
	}
}

// wantScope is the policy scope this level accepts.
func (m *AttachmentModel) wantScope() string {
	if m.level() == "gateway" {
		return scopeGateway
	}
	return scopeOverlay
}

// describe names the attachment point in a sentence.
func (m *AttachmentModel) describe() string {
	switch m.level() {
	case "route":
		return fmt.Sprintf("route %s on listener %s", m.RouteID.ValueString(), m.ListenerID.ValueString())
	case "listener":
		return "listener " + m.ListenerID.ValueString()
	default:
		return "gateway " + m.GatewayID.ValueString()
	}
}

// attachPath is the endpoint that carries the attachment, and parentPath is the
// object that reports it back. They differ by exactly the `/waf-policy` suffix.
func (m *AttachmentModel) parentPath(c *client.Client) string {
	base := "/application-gateways/" + m.GatewayID.ValueString()
	switch m.level() {
	case "route":
		base += "/listeners/" + m.ListenerID.ValueString() + "/routes/" + m.RouteID.ValueString()
	case "listener":
		base += "/listeners/" + m.ListenerID.ValueString()
	}
	return c.TenantPath(base)
}

func (m *AttachmentModel) attachPath(c *client.Client) string {
	return m.parentPath(c) + "/waf-policy"
}

func (m *AttachmentModel) composeID() string {
	parts := []string{m.GatewayID.ValueString()}
	if m.level() != "gateway" {
		parts = append(parts, m.ListenerID.ValueString())
	}
	if m.level() == "route" {
		parts = append(parts, m.RouteID.ValueString())
	}
	return strings.Join(parts, "/")
}

// readPolicy fetches the policy being attached, so the scope can be checked
// against the level and the effective mode reported.
func (r *attachmentResource) readPolicy(ctx context.Context, m *AttachmentModel) (*apiPolicy, error) {
	apiResp, err := r.client.Get(ctx, r.client.TenantPath(fmt.Sprintf(
		"/application-gateways/%s/waf-policies/%s",
		m.GatewayID.ValueString(), m.PolicyID.ValueString(),
	)), nil)
	if err != nil {
		return nil, err
	}
	return client.ParseResponse[apiPolicy](apiResp)
}

// applyPolicy records what the attached policy is and what it does.
func (m *AttachmentModel) applyPolicy(p *apiPolicy) {
	scope := p.Scope
	if scope == "" {
		scope = scopeGateway
	}
	m.Scope = types.StringValue(scope)
	// 🔴 THE EFFECTIVE MODE, NEVER THE AUTHORED ONE, AND NULL RATHER THAN A
	// GUESS. An overlay in "inherit" under a blocking gateway is blocking, and
	// reporting "inherit" here would let a configuration assert it is not.
	// Where the server did not resolve it, the answer lives on the GATEWAY
	// policy and is not ours to invent: null says so, and `inherit` in a field
	// documented as "inherit resolved" would not.
	if p.EffectiveMode != "" {
		m.EffectiveMode = types.StringValue(p.EffectiveMode)
	} else if p.Mode != "" && p.Mode != modeInherit {
		m.EffectiveMode = types.StringValue(p.Mode)
	} else {
		m.EffectiveMode = types.StringNull()
	}
}

// attach performs the PUT after checking the scope fits the level.
func (r *attachmentResource) attach(ctx context.Context, m *AttachmentModel, diags interface {
	AddError(summary, detail string)
},
) {
	p, err := r.readPolicy(ctx, m)
	if err != nil {
		diags.AddError("Failed to Read The WAF Policy Being Attached", err.Error())
		return
	}
	m.applyPolicy(p)

	if got := m.Scope.ValueString(); got != m.wantScope() {
		diags.AddError("The Policy's Scope Does Not Fit This Attachment Point",
			fmt.Sprintf("%s is a %s-scoped policy, and %s takes a %s-scoped one.\n\n%s",
				m.PolicyID.ValueString(), got, m.describe(), m.wantScope(), scopeMismatchHint(got)))
		return
	}

	// Attach answers 202 with the policy; detach answers 204. Neither is 200,
	// and neither body is needed: the policy was just read above to check its
	// scope. The client's error path is status >= 400, so both pass through as
	// the successes they are.
	if _, err := r.client.Put(ctx, m.attachPath(r.client),
		apiAttachRequest{PolicyID: m.PolicyID.ValueString()}); err != nil {
		diags.AddError("Failed to Attach The WAF Policy", err.Error())
		return
	}
	m.ID = types.StringValue(m.composeID())
}

// scopeMismatchHint says what to do about a scope that does not fit, and what
// getting it wrong would have cost.
func scopeMismatchHint(have string) string {
	if have == scopeGateway {
		return "A gateway policy carries the managed ruleset, which an overlay is not compiled " +
			"with. Create a policy with scope = \"overlay\" for this listener or route."
	}
	return "An overlay policy carries no managed ruleset, so attaching it to the gateway would " +
		"leave the gateway with no managed protection. Create a policy with scope = \"gateway\"."
}

func (r *attachmentResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan AttachmentModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	r.attach(ctx, &plan, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Update replaces the attachment in place. The API's PUT is a replace, so
// pointing the same level at a different policy never leaves a window with
// nothing attached -- which detach-then-attach would.
func (r *attachmentResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan AttachmentModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	r.attach(ctx, &plan, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Read asks the ATTACHEE which policy it carries, not the policy which
// attachees it has.
//
// The gateway, the listener and the route all report `wafPolicyId`, and that is
// the fact this resource manages. If the attachment point is gone, so is this;
// if it now carries a different policy, something detached or re-attached out
// of band and the next plan must show that rather than report agreement.
func (r *attachmentResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state AttachmentModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	apiResp, err := r.client.Get(ctx, state.parentPath(r.client), nil)
	if err != nil {
		if client.IsNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Failed to Read The WAF Attachment Point", err.Error())
		return
	}
	parent, err := client.ParseResponse[apiAttachee](apiResp)
	if err != nil {
		resp.Diagnostics.AddError("Failed to Parse The WAF Attachment Point", err.Error())
		return
	}
	if parent.WAFPolicyID == "" {
		// Nothing is attached any more. Removing it from state makes the next
		// plan re-attach, which is the direction that restores the protection
		// the configuration asks for.
		resp.Diagnostics.AddWarning("No WAF Policy Is Attached Any More",
			fmt.Sprintf("%s carries no WAF policy. It was detached outside this configuration; "+
				"the next plan will re-attach %s.", state.describe(), state.PolicyID.ValueString()))
		resp.State.RemoveResource(ctx)
		return
	}
	state.PolicyID = types.StringValue(parent.WAFPolicyID)
	state.ID = types.StringValue(state.composeID())

	// Refresh what the attached policy does, so a gateway policy moving to
	// block shows up on the overlays that inherit from it.
	if p, err := r.readPolicy(ctx, &state); err == nil {
		state.applyPolicy(p)
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *attachmentResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state AttachmentModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	_, err := r.client.Delete(ctx, state.attachPath(r.client))
	if err != nil && !client.IsNotFound(err) {
		resp.Diagnostics.AddError("Failed to Detach The WAF Policy", err.Error())
		return
	}
	if state.level() == "gateway" {
		resp.Diagnostics.AddWarning("Inheriting Overlays No Longer Block",
			"The gateway's WAF policy has been detached. Any overlay policy with "+
				"mode = \"inherit\" resolves against it, so with nothing there to inherit a "+
				"blocking mode from those overlays fall back to detect and stop refusing "+
				"requests. The policies themselves are untouched.")
	}
}

// ImportState takes the attachment POINT, because that is what this resource
// is: which policy is attached there is read back from the server.
//
//	{gateway_id}
//	{gateway_id}/{listener_id}
//	{gateway_id}/{listener_id}/{route_id}
func (r *attachmentResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	// The segment count chooses the level, so each arity is parsed with the
	// exact-count parser that refuses dot segments -- an import id becomes a
	// URL path segment, and ".." there addresses the PARENT with the same
	// method.
	var parts []string
	var err error
	switch strings.Count(req.ID, "/") {
	case 0:
		parts, err = client.ParseImportID(req.ID, "gateway_id")
	case 1:
		parts, err = client.ParseImportID(req.ID, "gateway_id", "listener_id")
	case 2:
		parts, err = client.ParseImportID(req.ID, "gateway_id", "listener_id", "route_id")
	default:
		err = fmt.Errorf("expected import ID format {gateway_id}, {gateway_id}/{listener_id} or "+
			"{gateway_id}/{listener_id}/{route_id}, got %q", req.ID)
	}
	if err != nil {
		resp.Diagnostics.AddError("Invalid Import ID", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("gateway_id"), parts[0])...)
	if len(parts) > 1 {
		resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("listener_id"), parts[1])...)
	}
	if len(parts) > 2 {
		resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("route_id"), parts[2])...)
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
}
