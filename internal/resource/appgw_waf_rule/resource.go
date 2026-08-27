// Package appgw_waf_rule implements the frostmoln_appgw_waf_rule Terraform
// resource.
package appgw_waf_rule

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
)

// ruleKeyPattern mirrors the server's own rule-key regexp. Matching it here
// means a bad key is a plan-time error naming the attribute, not a 400 partway
// through an apply that has already built the gateway.
var ruleKeyPattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,62}[a-z0-9])?$`)

var (
	_ resource.Resource                   = &ruleResource{}
	_ resource.ResourceWithImportState    = &ruleResource{}
	_ resource.ResourceWithConfigure      = &ruleResource{}
	_ resource.ResourceWithValidateConfig = &ruleResource{}
)

// RuleModel is the Terraform state model for a WAF rule.
type RuleModel struct {
	GatewayID types.String `tfsdk:"gateway_id"`
	PolicyID  types.String `tfsdk:"policy_id"`
	RuleKey   types.String `tfsdk:"rule_key"`
	Kind      types.String `tfsdk:"kind"`

	Builder types.String `tfsdk:"builder_json"`
	Raw     types.String `tfsdk:"raw"`

	ManagedSecRuleID types.Int64  `tfsdk:"managed_secrule_id"`
	ManagedAction    types.String `tfsdk:"managed_action"`
	ManagedTarget    types.String `tfsdk:"managed_target"`

	Ordinal     types.Int64  `tfsdk:"ordinal"`
	Description types.String `tfsdk:"description"`
	Enabled     types.Bool   `tfsdk:"enabled"`

	SecRuleID types.Int64  `tfsdk:"secrule_id"`
	Owner     types.String `tfsdk:"owner"`
	Revision  types.Int64  `tfsdk:"revision"`
	CreatedAt types.String `tfsdk:"created_at"`
	UpdatedAt types.String `tfsdk:"updated_at"`
}

type apiRule struct {
	RuleKey string          `json:"ruleKey"`
	Owner   string          `json:"owner"`
	Kind    string          `json:"kind"`
	Builder json.RawMessage `json:"builder,omitempty"`
	Raw     string          `json:"raw,omitempty"`

	ManagedSecRuleID int    `json:"managedSecRuleId,omitempty"`
	ManagedAction    string `json:"managedAction,omitempty"`
	ManagedTarget    string `json:"managedTarget,omitempty"`

	SecRuleID   int    `json:"secRuleId"`
	Ordinal     int    `json:"ordinal"`
	Description string `json:"description,omitempty"`
	Enabled     bool   `json:"enabled"`

	OptedOut      bool `json:"optedOut"`
	OptOutAllowed bool `json:"optOutAllowed"`

	Revision  int64  `json:"revision"`
	CreatedAt string `json:"createdAt"`
	UpdatedAt string `json:"updatedAt"`
}

type apiPutRuleRequest struct {
	RuleKey string          `json:"ruleKey"`
	Kind    string          `json:"kind"`
	Builder json.RawMessage `json:"builder,omitempty"`
	Raw     string          `json:"raw,omitempty"`

	ManagedSecRuleID int    `json:"managedSecRuleId,omitempty"`
	ManagedAction    string `json:"managedAction,omitempty"`
	ManagedTarget    string `json:"managedTarget,omitempty"`

	Ordinal     *int   `json:"ordinal,omitempty"`
	Description string `json:"description,omitempty"`
	Enabled     *bool  `json:"enabled,omitempty"`
}

type apiDraftResponse struct {
	Rules []apiRule `json:"rules"`
}

type ruleResource struct {
	client *client.Client
}

// NewResource returns a new WAF rule resource factory.
func NewResource() resource.Resource {
	return &ruleResource{}
}

func (r *ruleResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_appgw_waf_rule"
}

func (r *ruleResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manages one rule in an Application Gateway WAF policy's **draft**.\n\n" +
			"Writing a rule changes nothing a request sees. It lands on the draft, and takes effect " +
			"when a `frostmoln_appgw_waf_policy_publication` publishes it and the gateway applies " +
			"its configuration.\n\n" +
			"`rule_key` is the rule's identity everywhere: it is this resource's id, the anchor a " +
			"diff is keyed on, and what a report points at. Changing it replaces the rule.\n\n" +
			"This resource manages **tenant-owned** rules only. Platform rules cannot be created, " +
			"edited or deleted; read them with the `frostmoln_appgw_waf_platform_rules` data source.",
		Attributes: map[string]schema.Attribute{
			"gateway_id": schema.StringAttribute{
				Description:   "The Application Gateway the policy belongs to.",
				Required:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"policy_id": schema.StringAttribute{
				Description:   "The WAF policy this rule belongs to.",
				Required:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"rule_key": schema.StringAttribute{
				Description: "Your stable name for this rule: 1-64 lowercase letters, digits or " +
					"hyphens, starting and ending with a letter or digit.",
				Required: true,
				Validators: []validator.String{
					stringvalidator.RegexMatches(ruleKeyPattern,
						"must be 1-64 lowercase letters, digits or hyphens, starting and ending "+
							"with a letter or digit"),
				},
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"kind": schema.StringAttribute{
				Description: "Which authoring surface this rule uses:\n\n" +
					"* `builder` — a structured rule, given as `builder_json`.\n" +
					"* `raw` — SecLang text, given as `raw`. **Not usable from Terraform today** — " +
					"see the `raw` attribute.\n" +
					"* `managedOverride` — change a platform-managed rule, via " +
					"`managed_secrule_id` and `managed_action`.\n\n" +
					"Exactly one payload belongs to each kind.",
				Required:   true,
				Validators: []validator.String{stringvalidator.OneOf("builder", "raw", "managedOverride")},
			},
			"builder_json": schema.StringAttribute{
				Description: "A structured rule as a JSON document, for `kind = \"builder\"`. Use " +
					"`jsonencode({ conditions = [...], action = {...}, phase = 2 })`.\n\n" +
					"A string rather than a nested block because the condition list is an ordered " +
					"sequence whose shape the server owns; encoding it as JSON keeps this resource " +
					"from having to re-declare — and drift from — that schema.",
				Optional: true,
			},
			"raw": schema.StringAttribute{
				Description: "SecLang rule text, for `kind = \"raw\"`.\n\n" +
					"~> **Not usable from Terraform today.** The platform requires the text to carry " +
					"the SecRule id it allocates *during the same write* (`id:4000000`, and so on), " +
					"and a configuration cannot reference its own computed `secrule_id`. Until the " +
					"platform injects that id, author rules with `kind = \"builder\"` or " +
					"`kind = \"managedOverride\"`.\n\n" +
					"The accepted subset is also narrower than SecLang: exactly one directive per " +
					"rule (no `chain`), an allow-listed set of actions and transformations, RE2 " +
					"regular expressions only, phase 1 or 2, and at most 8192 characters.",
				Optional: true,
			},
			"managed_secrule_id": schema.Int64Attribute{
				Description: "The managed rule to override, for `kind = \"managedOverride\"`.",
				Optional:    true,
			},
			"managed_action": schema.StringAttribute{
				Description: "What to do with the managed rule: `disable` or `updateTarget`.",
				Optional:    true,
				Validators:  []validator.String{stringvalidator.OneOf("disable", "updateTarget")},
			},
			"managed_target": schema.StringAttribute{
				Description: "The new target, for `managed_action = \"updateTarget\"`.",
				Optional:    true,
			},
			"ordinal": schema.Int64Attribute{
				Description: "Evaluation order within the policy.",
				Optional:    true,
				Computed:    true,
				PlanModifiers: []planmodifier.Int64{
					int64planmodifier.UseStateForUnknown(),
				},
			},
			"description": schema.StringAttribute{
				Description: "What this rule is for. At most 1024 characters.",
				Optional:    true,
			},
			"enabled": schema.BoolAttribute{
				Description: "Whether the rule is evaluated.",
				Optional:    true,
				Computed:    true,
				Default:     booldefault.StaticBool(true),
			},
			"secrule_id": schema.Int64Attribute{
				Description: "The SecRule id the platform assigned. Derived from a reserved " +
					"customer range and then fixed, so it never moves. Never supplied by you: a " +
					"chosen id could collide with the managed ruleset.",
				Computed: true,
			},
			"owner": schema.StringAttribute{
				Description: "Always `tenant` for a rule managed by this resource.",
				Computed:    true,
			},
			"revision": schema.Int64Attribute{
				Description: "A server-assigned counter, bumped on every write.\n\n" +
					"**This is what orders a publish after your rule changes without `depends_on`.** " +
					"Feed it into `frostmoln_appgw_waf_policy_publication.rule_revisions`: any rule " +
					"change changes an input to the publication, so Terraform's own graph runs the " +
					"publish last, and re-runs it exactly when something changed.",
				Computed: true,
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

// ValidateConfig enforces the discriminated union at plan time, exactly as the
// server does at write time.
func (r *ruleResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var cfg RuleModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() || cfg.Kind.IsUnknown() {
		return
	}
	has := func(s types.String) bool { return !s.IsNull() && !s.IsUnknown() }
	hasInt := func(n types.Int64) bool { return !n.IsNull() && !n.IsUnknown() }

	switch cfg.Kind.ValueString() {
	case "builder":
		if !has(cfg.Builder) {
			resp.Diagnostics.AddAttributeError(path.Root("builder_json"),
				"builder_json Is Required With kind = \"builder\"", "A builder rule needs its body.")
		} else if cfg.Builder.ValueString() != "" && !json.Valid([]byte(cfg.Builder.ValueString())) {
			resp.Diagnostics.AddAttributeError(path.Root("builder_json"),
				"builder_json Is Not Valid JSON",
				"Use jsonencode({ conditions = [...], action = {...}, phase = 2 }).")
		}
		if has(cfg.Raw) || hasInt(cfg.ManagedSecRuleID) {
			resp.Diagnostics.AddAttributeError(path.Root("kind"),
				"kind = \"builder\" Must Not Carry raw Or managed_* Fields",
				"Exactly one payload belongs to each kind.")
		}
	case "raw":
		if !has(cfg.Raw) || strings.TrimSpace(cfg.Raw.ValueString()) == "" {
			resp.Diagnostics.AddAttributeError(path.Root("raw"),
				"raw Is Required With kind = \"raw\"", "A raw rule needs its SecLang text.")
		} else if len(cfg.Raw.ValueString()) > 8192 {
			resp.Diagnostics.AddAttributeError(path.Root("raw"),
				"raw Is Too Long",
				fmt.Sprintf("A raw rule may be at most 8192 characters; this one is %d. "+
					"Split it into several rules.", len(cfg.Raw.ValueString())))
		}
		if has(cfg.Builder) || hasInt(cfg.ManagedSecRuleID) {
			resp.Diagnostics.AddAttributeError(path.Root("kind"),
				"kind = \"raw\" Must Not Carry builder_json Or managed_* Fields",
				"Exactly one payload belongs to each kind.")
		}
	case "managedOverride":
		if !hasInt(cfg.ManagedSecRuleID) {
			resp.Diagnostics.AddAttributeError(path.Root("managed_secrule_id"),
				"managed_secrule_id Is Required With kind = \"managedOverride\"",
				"Name the managed rule this override applies to.")
		}
		if cfg.ManagedAction.IsNull() {
			resp.Diagnostics.AddAttributeError(path.Root("managed_action"),
				"managed_action Is Required With kind = \"managedOverride\"",
				"Choose disable or updateTarget.")
		}
		if has(cfg.Builder) || has(cfg.Raw) {
			resp.Diagnostics.AddAttributeError(path.Root("kind"),
				"kind = \"managedOverride\" Must Not Carry builder_json Or raw",
				"Exactly one payload belongs to each kind.")
		}
	}
	if has(cfg.Description) && len(cfg.Description.ValueString()) > 1024 {
		resp.Diagnostics.AddAttributeError(path.Root("description"),
			"description Is Too Long", "At most 1024 characters.")
	}
}

func (r *ruleResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (m *RuleModel) fromAPI(a *apiRule) {
	m.RuleKey = types.StringValue(a.RuleKey)
	m.Owner = types.StringValue(a.Owner)
	m.Kind = types.StringValue(a.Kind)
	// 🔴 builder_json IS DELIBERATELY NOT SET FROM THE REPLY, and this is the
	// same rule appgw_certificate applies to chain_pem.
	//
	// The server does not echo the bytes it was sent: it binds into a struct and
	// re-marshals, so key order becomes Go field order and `omitempty` drops
	// selector/negated/transformations. Terraform's jsonencode emits keys
	// LEXICALLY SORTED and keeps every key written. For a non-Computed attribute
	// that mismatch is "Provider produced inconsistent result after apply" — a
	// hard error, on create, every time a builder rule is used.
	//
	// Preserving the configured value is correct rather than merely convenient:
	// the two documents are semantically identical, and the one the practitioner
	// wrote is the one their configuration has to keep matching.
	m.Raw = optionalString(a.Raw)
	m.ManagedSecRuleID = optionalInt(a.ManagedSecRuleID)
	m.ManagedAction = optionalString(a.ManagedAction)
	m.ManagedTarget = optionalString(a.ManagedTarget)
	m.SecRuleID = types.Int64Value(int64(a.SecRuleID))
	m.Ordinal = types.Int64Value(int64(a.Ordinal))
	m.Description = optionalString(a.Description)
	m.Enabled = types.BoolValue(a.Enabled)
	m.Revision = types.Int64Value(a.Revision)
	m.CreatedAt = types.StringValue(a.CreatedAt)
	m.UpdatedAt = types.StringValue(a.UpdatedAt)
}

func (m *RuleModel) toRequest() apiPutRuleRequest {
	req := apiPutRuleRequest{
		RuleKey:          m.RuleKey.ValueString(),
		Kind:             m.Kind.ValueString(),
		Raw:              str(m.Raw),
		ManagedSecRuleID: int(m.ManagedSecRuleID.ValueInt64()),
		ManagedAction:    str(m.ManagedAction),
		ManagedTarget:    str(m.ManagedTarget),
		Description:      str(m.Description),
	}
	if s := str(m.Builder); s != "" {
		req.Builder = json.RawMessage(s)
	}
	if !m.Ordinal.IsNull() && !m.Ordinal.IsUnknown() {
		v := int(m.Ordinal.ValueInt64())
		req.Ordinal = &v
	}
	if !m.Enabled.IsNull() && !m.Enabled.IsUnknown() {
		v := m.Enabled.ValueBool()
		req.Enabled = &v
	}
	return req
}

// Create and Update are the same PUT: the rule key IS the identity, so a
// declarative client sends desired state without knowing whether the row
// already exists.
func (r *ruleResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan RuleModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.put(ctx, &plan); err != nil {
		resp.Diagnostics.AddError("Failed to Write WAF Rule", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *ruleResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan RuleModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.put(ctx, &plan); err != nil {
		resp.Diagnostics.AddError("Failed to Write WAF Rule", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *ruleResource) put(ctx context.Context, m *RuleModel) error {
	apiResp, err := r.client.Put(ctx, fmt.Sprintf("%s/rules/%s",
		r.policyPath(m.GatewayID.ValueString(), m.PolicyID.ValueString()), m.RuleKey.ValueString()),
		m.toRequest())
	if err != nil {
		return err
	}
	a, err := client.ParseResponse[apiRule](apiResp)
	if err != nil {
		return err
	}
	gwID, policyID := m.GatewayID, m.PolicyID
	m.fromAPI(a)
	m.GatewayID, m.PolicyID = gwID, policyID
	return nil
}

// Read finds the rule in the policy's DRAFT.
//
// The draft is what this resource manages: a rule that has been published still
// lives in the draft too, and reading the published version instead would make
// an unpublished edit invisible to `terraform plan`.
func (r *ruleResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state RuleModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	apiResp, err := r.client.Get(ctx, r.policyPath(
		state.GatewayID.ValueString(), state.PolicyID.ValueString())+"/draft", nil)
	if err != nil {
		if client.IsNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Failed to Read WAF Draft", err.Error())
		return
	}
	draft, err := client.ParseResponse[apiDraftResponse](apiResp)
	if err != nil {
		resp.Diagnostics.AddError("Failed to Parse WAF Draft Response", err.Error())
		return
	}
	want := state.RuleKey.ValueString()
	for i := range draft.Rules {
		if draft.Rules[i].RuleKey == want {
			// 🔴 A PLATFORM RULE IS NOT THIS RESOURCE'S TO MANAGE. The draft
			// carries both owners and the match is on key alone, so importing a
			// platform rule's key would succeed and put a security control the
			// tenant does not own into state as one they "manage". The server
			// then refuses every subsequent write, but the state file has
			// already misrepresented ownership.
			if draft.Rules[i].Owner != "tenant" {
				resp.Diagnostics.AddError("This Rule Key Belongs To A Platform-Owned Rule",
					fmt.Sprintf("%q is owned by the platform: it is gateway self-protection or an "+
						"emergency virtual patch, and it cannot be created, edited or deleted by a "+
						"tenant.\n\nRead platform rules with the "+
						"frostmoln_appgw_waf_platform_rules data source. Where one may be turned "+
						"off for your gateway it reports opt_out_allowed = true; use `fm appgw waf "+
						"rule opt-out` or the portal to do so.", want))
				return
			}
			gwID, policyID := state.GatewayID, state.PolicyID
			state.fromAPI(&draft.Rules[i])
			state.GatewayID, state.PolicyID = gwID, policyID
			resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
			return
		}
	}
	resp.State.RemoveResource(ctx)
}

func (r *ruleResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state RuleModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	_, err := r.client.Delete(ctx, fmt.Sprintf("%s/rules/%s",
		r.policyPath(state.GatewayID.ValueString(), state.PolicyID.ValueString()),
		state.RuleKey.ValueString()))
	if err != nil && !client.IsNotFound(err) {
		resp.Diagnostics.AddError("Failed to Delete WAF Rule", err.Error())
	}
}

func (r *ruleResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	parts, err := client.ParseImportID(req.ID, "gateway_id", "policy_id", "rule_key")
	if err != nil {
		resp.Diagnostics.AddError("Invalid Import ID", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("gateway_id"), parts[0])...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("policy_id"), parts[1])...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("rule_key"), parts[2])...)
}

func (r *ruleResource) policyPath(gwID, policyID string) string {
	return r.client.TenantPath(fmt.Sprintf("/application-gateways/%s/waf-policies/%s", gwID, policyID))
}

func str(s types.String) string {
	if s.IsNull() || s.IsUnknown() {
		return ""
	}
	return s.ValueString()
}

func optionalString(s string) types.String {
	if s == "" {
		return types.StringNull()
	}
	return types.StringValue(s)
}

func optionalInt(n int) types.Int64 {
	if n == 0 {
		return types.Int64Null()
	}
	return types.Int64Value(int64(n))
}
