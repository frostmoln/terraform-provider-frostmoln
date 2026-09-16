package tag_color

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/scopedecl"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/tagsettings"
)

var (
	_ resource.Resource                = &tagColorResource{}
	_ resource.ResourceWithImportState = &tagColorResource{}
	_ resource.ResourceWithModifyPlan  = &tagColorResource{}
)

// NewResource returns a new frostmoln_tag_color resource factory.
func NewResource() resource.Resource {
	return &tagColorResource{}
}

type tagColorResource struct {
	client *client.Client
}

func (r *tagColorResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_tag_color"
}

func (r *tagColorResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manages one tag colour rule of an organization: a tag key — and optionally one value of it — " +
			"shown in a chosen colour. Colour rules apply in every tenant of the organization, so a tag such as " +
			"`env=prod` looks the same everywhere in the portal. They are display only: nothing on the platform " +
			"reads a rule to decide anything." +
			"\n\n**Matching.** A rule without `value` matches any value of its `key`; a rule with a `value` (the " +
			"empty string included) matches exactly that value, and wins over the key-only rule for the same key. " +
			"Keys and values are compared case-sensitively, like the tags themselves." +
			"\n\n**One rule per key and value.** An organization holds at most one rule for each key and value — a " +
			"key-only rule and a rule for the empty value are different rules — and at most 200 rules in all. " +
			"Creating a rule the organization already has fails, naming the existing rule and the `terraform import` " +
			"command that brings it under management; the provider never adopts an existing rule silently. Changing " +
			"`key`, `value` or `color` updates the rule in place." +
			"\n\n**Organization.** Set `organization_id`, or leave it out to use the organization that owns the " +
			"provider's tenant, which the provider asks the platform for while planning, so the plan names it. It " +
			"never falls back to your account's home organization, which is not necessarily the one that owns the " +
			"tenant. The rule stays in the organization it was created in: removing `organization_id` from the " +
			"configuration later does not move it — if the provider's tenant then belongs to another organization, " +
			"the plan warns, naming both — and changing it deletes the rule and creates it in the other organization." +
			"\n\n**Permissions.** Creating, changing and deleting a rule needs an admin or owner role in the " +
			"organization; reading one needs any active membership. An API key needs `organizations:write` and " +
			"`organizations:read`, and reaches only the organization that owns the provider's tenant: an " +
			"`organization_id` naming another organization is refused." +
			"\n\n**Import.** By `<organization_id>/<rule_id>`, or by `<rule_id>` alone for a rule of the " +
			"organization that owns the provider's tenant. The `frostmoln_tag_colors` data source lists the rules " +
			"with their ids." +
			"\n\n" + scopedecl.Summary("frostmoln_tag_color"),
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Description: "The rule's id.",
				Computed:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"organization_id": schema.StringAttribute{
				Description: "The id of the organization the rule belongs to, in the canonical lowercase form the " +
					"platform reports. When omitted, the organization that owns the provider's tenant, as the platform " +
					"reports it when the rule is planned — never your account's home organization. Changing it deletes " +
					"the rule and creates it in the other organization; removing it from the configuration keeps the " +
					"rule where it is.",
				Optional: true,
				Computed: true,
				// UseStateForUnknown FIRST, then RequiresReplace — see CLAUDE.md
				// "Plan modifier order". Reversed, omitting organization_id (the
				// intended usage) would plan a replacement on every change.
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
					stringplanmodifier.RequiresReplace(),
				},
				Validators: []validator.String{tagsettings.OrganizationIDValidator()},
			},
			"key": schema.StringAttribute{
				Description: "The tag key the rule colours, matched case-sensitively: 1 to 255 characters, without " +
					"control or formatting characters. Keys starting with the platform's lower-case `frostmoln_` or " +
					"`frostmoln-` are refused; the check is case-sensitive, so `Frostmoln_Team` can be coloured. " +
					"Changing it retargets this rule in place.",
				Required:   true,
				Validators: []validator.String{tagsettings.KeyValidator()},
			},
			"value": schema.StringAttribute{
				Description: "The exact tag value the rule colours. Omit it for a rule matching ANY value of `key`; " +
					"`\"\"` is a different rule, matching only the empty value. At most 256 characters, without " +
					"control or formatting characters. Changing it retargets this rule in place.",
				Optional:   true,
				Validators: []validator.String{tagsettings.ValueValidator()},
			},
			"color": schema.StringAttribute{
				Description: "The background colour, as `#RRGGBB` (e.g. `#1f6feb`); the text colour is derived " +
					"from it. The platform stores it exactly as written, letter case included, so `#D73A49` and " +
					"`#d73a49` are different values.",
				Required:   true,
				Validators: []validator.String{tagsettings.ColorValidator()},
			},
			"created_at": schema.StringAttribute{
				Description: "When the rule was created (UTC).",
				Computed:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"updated_at": schema.StringAttribute{
				Description: "When the rule was last changed (UTC).",
				Computed:    true,
			},
		},
	}
}

func (r *tagColorResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	c, ok := req.ProviderData.(*client.Client)
	if !ok {
		resp.Diagnostics.AddError(
			"Unexpected Resource Configure Type",
			fmt.Sprintf("Expected *client.Client, got: %T", req.ProviderData),
		)
		return
	}
	r.client = c
}

// resolveOrganization returns the organization a new rule is written to: the
// configured one, else the one that owns the provider's tenant as the platform
// reports it. ok is false when it has added an error.
func (r *tagColorResource) resolveOrganization(ctx context.Context, m *TagColorModel, addErr func(path.Path, string, string)) (string, bool) {
	if !m.OrganizationID.IsNull() && !m.OrganizationID.IsUnknown() {
		return m.OrganizationID.ValueString(), true
	}
	orgID, err := tagsettings.ResolveOrganizationID(ctx, r.client)
	if errors.Is(err, tagsettings.ErrOrganizationUnresolved) {
		addErr(path.Root("organization_id"), "Organization Not Resolved",
			tagsettings.UnresolvedOrganizationDetail(r.client.TenantID()))
		return "", false
	}
	if tagsettings.IsForbidden(err) {
		addErr(path.Root("organization_id"), "Failed to Resolve the Organization",
			fmt.Sprintf("organization_id is not set, so the provider asked the platform which organization owns "+
				"tenant %q, and the request was refused: %s. Reading tag colours needs an active membership of "+
				"that organization and, for an API key, the `organizations:read` scope — for this lookup AND for "+
				"every refresh of a rule, which reads it from its organization. Setting organization_id skips the "+
				"lookup but not those reads, so it will not make refresh work on its own: grant the key "+
				"`organizations:read`, and `organizations:write` to create, change or delete rules.",
				r.client.TenantID(), err))
		return "", false
	}
	if err != nil {
		addErr(path.Root("organization_id"), "Failed to Resolve the Organization",
			fmt.Sprintf("organization_id is not set, and asking the platform which organization owns tenant %q "+
				"failed: %s. Set organization_id explicitly, or fix the error and retry.", r.client.TenantID(), err))
		return "", false
	}
	return orgID, true
}

// ModifyPlan makes the target organization visible while planning when
// organization_id is not configured.
//
//   - Create: the organization that owns the provider's tenant is resolved now,
//     so the plan names it instead of "(known after apply)", and the create
//     writes to exactly that one. A failure is an error here — the create arm
//     only (prior state is null), which a destroy never reaches.
//   - Existing rule: it stays in the organization recorded in state (nothing
//     is moved or replaced), but when the provider's tenant now belongs to a
//     different organization the plan WARNS, naming both, so a configuration
//     repointed at another tenant does not silently keep managing rules of the
//     old organization. A failed lookup here is silent: a warning is a courtesy,
//     and this path also runs in a destroy's refresh plan.
func (r *tagColorResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.Plan.Raw.IsNull() || r.client == nil {
		return
	}
	var configured types.String
	if d := req.Config.GetAttribute(ctx, path.Root("organization_id"), &configured); d.HasError() || !configured.IsNull() {
		return
	}

	if req.State.Raw.IsNull() {
		m := TagColorModel{OrganizationID: types.StringNull()}
		orgID, ok := r.resolveOrganization(ctx, &m, resp.Diagnostics.AddAttributeError)
		if ok {
			resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("organization_id"), orgID)...)
		}
		return
	}

	var recorded types.String
	if d := req.State.GetAttribute(ctx, path.Root("organization_id"), &recorded); d.HasError() || recorded.IsNull() {
		return
	}
	current, err := tagsettings.ResolveOrganizationID(ctx, r.client)
	if err != nil || current == recorded.ValueString() {
		return
	}
	resp.Diagnostics.AddAttributeWarning(path.Root("organization_id"), "Rule Belongs to Another Organization",
		fmt.Sprintf("This rule is in organization %q (recorded in state), but the organization that owns the "+
			"provider's tenant is now %q. organization_id is not set, so the rule is not moved: it stays in %q and "+
			"this configuration keeps managing it there. Set organization_id = %q to say so explicitly, or %q to "+
			"move the rule (which deletes and re-creates it).",
			recorded.ValueString(), current, recorded.ValueString(), recorded.ValueString(), current))
}

// describeTarget renders a rule's key and value for a diagnostic.
func describeTarget(req tagsettings.RuleRequest) string {
	if req.Value == nil {
		return fmt.Sprintf("key %q (any value)", req.Key)
	}
	return fmt.Sprintf("key %q with value %q", req.Key, *req.Value)
}

// existsOnCreateDetail is the refusal when the organization already has a rule
// for this key and value. It names the rule and the import that brings it under
// management — the provider does not adopt it, because a create that silently
// took over an existing rule would let two configurations believe they each own
// it, and a destroy of either would delete the other's.
func existsOnCreateDetail(orgID, existingID string, req tagsettings.RuleRequest) string {
	lead := fmt.Sprintf("Organization %q already has a tag colour rule for %s", orgID, describeTarget(req))
	if existingID == "" {
		return lead + ". An organization holds one rule per key and value. " + sameApplyHint + " Otherwise this " +
			"configuration describes that existing rule: find its id with the frostmoln_tag_colors data source and " +
			"import it as <organization_id>/<rule_id> to manage it here, or delete it first."
	}
	importID := orgID + "/" + existingID
	return fmt.Sprintf("%s: rule %q. An organization holds one rule per key and value. %s Otherwise this "+
		"configuration describes that existing rule. To manage it here, import it instead of creating it:\n\n"+
		"  terraform import <this resource's address> %s\n\n"+
		"or add an `import` block with id = %q. The next apply then sets its colour. The provider does not "+
		"adopt an existing rule on its own.", lead, existingID, sameApplyHint, importID, importID)
}

// sameApplyHint covers the collision that is not a real duplicate: another
// resource in the same configuration is moving off this key and value in the
// same apply — a renamed resource without a `moved` block (planned as a destroy
// and a create, with no ordering between them), or a retarget — and simply had
// not run yet. Importing would then take over a rule that is about to change.
const sameApplyHint = "If another resource in this configuration is moving off this key and value in the same " +
	"apply — a rename without a `moved` block, or a retarget of another rule — re-run the apply instead of " +
	"importing: the other change has run by then."

func (r *tagColorResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan TagColorModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	orgID, ok := r.resolveOrganization(ctx, &plan, resp.Diagnostics.AddAttributeError)
	if !ok {
		return
	}

	body := plan.toRequest()
	apiResp, err := r.client.Post(ctx, tagsettings.OrgRulesPath(orgID), body)
	if err != nil {
		if existingID, exists := tagsettings.RuleExistsID(err); exists {
			resp.Diagnostics.AddError("Tag Colour Rule Already Exists", existsOnCreateDetail(orgID, existingID, body))
			return
		}
		resp.Diagnostics.AddError("Failed to create tag colour rule", err.Error())
		return
	}

	rule, err := client.ParseResponse[tagsettings.Rule](apiResp)
	if err != nil {
		resp.Diagnostics.AddError("Failed to parse tag colour rule response", err.Error())
		return
	}

	plan.fromAPI(rule)
	plan.OrganizationID = types.StringValue(orgID)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *tagColorResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state TagColorModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	apiResp, err := r.client.Get(ctx, tagsettings.OrgRulePath(state.OrganizationID.ValueString(), state.ID.ValueString()), nil)
	if err != nil {
		// The flat 404 is the service's verdict that the rule is not in this
		// organization (deleted, or never there) — stop managing it. A routing
		// 404 is nested and stays an error (client.IsNotFound).
		if client.IsNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Failed to read tag colour rule", err.Error())
		return
	}

	rule, err := client.ParseResponse[tagsettings.Rule](apiResp)
	if err != nil {
		resp.Diagnostics.AddError("Failed to parse tag colour rule response", err.Error())
		return
	}

	state.fromAPI(rule)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Update replaces the rule in place. The API's PUT is a full replace of key,
// value and colour — retargeting a rule to another key or value is an ordinary
// update there — so none of the three forces a replacement.
func (r *tagColorResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state TagColorModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// organization_id cannot differ from state here: a change replaces.
	orgID := state.OrganizationID.ValueString()
	body := plan.toRequest()
	apiResp, err := r.client.Put(ctx, tagsettings.OrgRulePath(orgID, state.ID.ValueString()), body)
	if err != nil {
		if existingID, exists := tagsettings.RuleExistsID(err); exists {
			other := "another rule"
			if existingID != "" {
				other = fmt.Sprintf("rule %q", existingID)
			}
			resp.Diagnostics.AddError("Tag Colour Rule Already Exists",
				fmt.Sprintf("Organization %q already has a tag colour rule for %s (%s), so this rule cannot be "+
					"changed to target it: an organization holds one rule per key and value. %s Otherwise delete "+
					"or retarget %s first, or manage that rule instead of this one.",
					orgID, describeTarget(body), other, sameApplyHint, other))
			return
		}
		resp.Diagnostics.AddError("Failed to update tag colour rule", err.Error())
		return
	}

	rule, err := client.ParseResponse[tagsettings.Rule](apiResp)
	if err != nil {
		resp.Diagnostics.AddError("Failed to parse tag colour rule response", err.Error())
		return
	}

	plan.fromAPI(rule)
	plan.OrganizationID = state.OrganizationID
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *tagColorResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state TagColorModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	_, err := r.client.Delete(ctx, tagsettings.OrgRulePath(state.OrganizationID.ValueString(), state.ID.ValueString()))
	if err != nil {
		if client.IsNotFound(err) {
			return
		}
		resp.Diagnostics.AddError("Failed to delete tag colour rule", err.Error())
	}
}

// ImportState accepts `<organization_id>/<rule_id>`, or `<rule_id>` alone for a
// rule of the organization that owns the provider's tenant. Both segments are
// checked before they reach a request path.
func (r *tagColorResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	var orgID, ruleID string
	if strings.Contains(req.ID, "/") {
		parts, err := client.ParseImportID(req.ID, "organization_id", "rule_id")
		if err != nil {
			resp.Diagnostics.AddError("Invalid import ID", err.Error())
			return
		}
		orgID, ruleID = parts[0], parts[1]
		// Any spelling uuid.Parse accepts, stored canonical — the form a
		// configuration must use — so importing `ORG/rule` in upper case does not
		// plan a replacement against a lower-case organization_id.
		canonical, err := tagsettings.CanonicalUUID(orgID)
		if err != nil {
			resp.Diagnostics.AddError("Invalid import ID",
				tagsettings.CheckOrganizationID(orgID)+" Expected <organization_id>/<rule_id>.")
			return
		}
		orgID = canonical
	} else {
		parts, err := client.ParseImportID(req.ID, "rule_id")
		if err != nil {
			resp.Diagnostics.AddError("Invalid import ID", err.Error())
			return
		}
		ruleID = parts[0]
	}
	if problem := tagsettings.CheckRuleID(ruleID); problem != "" {
		resp.Diagnostics.AddError("Invalid import ID", problem+" Expected <organization_id>/<rule_id> or <rule_id>.")
		return
	}
	ruleID, _ = tagsettings.CanonicalUUID(ruleID)
	if orgID == "" {
		var m TagColorModel
		m.OrganizationID = types.StringNull()
		var ok bool
		orgID, ok = r.resolveOrganization(ctx, &m, func(_ path.Path, summary, detail string) {
			resp.Diagnostics.AddError(summary, detail+" To import a rule of another organization, use "+
				"<organization_id>/<rule_id>.")
		})
		if !ok {
			return
		}
	}

	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), ruleID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("organization_id"), orgID)...)
}
