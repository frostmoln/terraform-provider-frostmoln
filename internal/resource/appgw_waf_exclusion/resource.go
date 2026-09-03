// Package appgw_waf_exclusion implements the frostmoln_appgw_waf_exclusion
// Terraform resource.
package appgw_waf_exclusion

import (
	"context"
	"fmt"
	"regexp"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
)

var ruleKeyPattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,62}[a-z0-9])?$`)

var (
	_ resource.Resource                   = &exclusionResource{}
	_ resource.ResourceWithImportState    = &exclusionResource{}
	_ resource.ResourceWithConfigure      = &exclusionResource{}
	_ resource.ResourceWithValidateConfig = &exclusionResource{}
)

// ExclusionModel is the Terraform state model for a WAF exclusion.
type ExclusionModel struct {
	GatewayID types.String `tfsdk:"gateway_id"`
	PolicyID  types.String `tfsdk:"policy_id"`
	RuleKey   types.String `tfsdk:"rule_key"`

	TargetSecRuleID types.Int64  `tfsdk:"target_secrule_id"`
	TargetTag       types.String `tfsdk:"target_tag"`

	Variable    types.String `tfsdk:"variable"`
	Selector    types.String `tfsdk:"selector"`
	PathPattern types.String `tfsdk:"path_pattern"`
	Description types.String `tfsdk:"description"`
	Ordinal     types.Int64  `tfsdk:"ordinal"`

	Revision  types.Int64  `tfsdk:"revision"`
	CreatedAt types.String `tfsdk:"created_at"`
	UpdatedAt types.String `tfsdk:"updated_at"`
}

type apiExclusion struct {
	RuleKey         string `json:"ruleKey"`
	TargetSecRuleID int    `json:"targetSecRuleId,omitempty"`
	TargetTag       string `json:"targetTag,omitempty"`
	Variable        string `json:"variable"`
	Selector        string `json:"selector,omitempty"`
	PathPattern     string `json:"pathPattern,omitempty"`
	Description     string `json:"description,omitempty"`
	Ordinal         int    `json:"ordinal"`
	Revision        int64  `json:"revision"`
	CreatedAt       string `json:"createdAt"`
	UpdatedAt       string `json:"updatedAt"`
}

type apiPutExclusionRequest struct {
	RuleKey         string `json:"ruleKey"`
	TargetSecRuleID int    `json:"targetSecRuleId,omitempty"`
	TargetTag       string `json:"targetTag,omitempty"`
	Variable        string `json:"variable"`
	Selector        string `json:"selector,omitempty"`
	PathPattern     string `json:"pathPattern,omitempty"`
	Description     string `json:"description,omitempty"`
	Ordinal         *int   `json:"ordinal,omitempty"`
}

type apiDraftResponse struct {
	Exclusions []apiExclusion `json:"exclusions"`
}

type exclusionResource struct {
	client *client.Client
}

// NewResource returns a new WAF exclusion resource factory.
func NewResource() resource.Resource {
	return &exclusionResource{}
}

func (r *exclusionResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_appgw_waf_exclusion"
}

func (r *exclusionResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Narrows a WAF rule so it stops matching a request shape you know is good — a " +
			"field that legitimately contains SQL, an upload endpoint, an API that posts XML.\n\n" +
			"Like a rule, an exclusion lands on the policy's **draft** and takes effect only when it " +
			"is published and the gateway applies its configuration.\n\n" +
			"Target exactly one of `target_secrule_id` or `target_tag`.",
		Attributes: map[string]schema.Attribute{
			"gateway_id": schema.StringAttribute{
				Description:   "The Application Gateway the policy belongs to.",
				Required:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"policy_id": schema.StringAttribute{
				Description:   "The WAF policy this exclusion belongs to.",
				Required:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"rule_key": schema.StringAttribute{
				Description: "Your stable name for this exclusion: 1-64 lowercase letters, digits " +
					"or hyphens, starting and ending with a letter or digit.",
				Required: true,
				Validators: []validator.String{
					stringvalidator.RegexMatches(ruleKeyPattern,
						"must be 1-64 lowercase letters, digits or hyphens, starting and ending "+
							"with a letter or digit"),
				},
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"target_secrule_id": schema.Int64Attribute{
				Description: "The single managed rule to narrow, by its SecRule id.",
				Optional:    true,
			},
			"target_tag": schema.StringAttribute{
				Description: "The tag whose rules to narrow, e.g. `attack-sqli`. At most 128 characters.",
				Optional:    true,
			},
			"variable": schema.StringAttribute{
				Description: "The variable to exclude from inspection, e.g. `ARGS` or `REQUEST_HEADERS`.",
				Required:    true,
			},
			"selector": schema.StringAttribute{
				Description: "A specific field within the variable, e.g. a parameter or header name. " +
					"Omit to exclude the whole variable, which is much broader — prefer naming the " +
					"one field that is a false positive.",
				Optional: true,
			},
			"path_pattern": schema.StringAttribute{
				Description: "Limit the exclusion to matching request paths, so it applies where the " +
					"false positive is rather than everywhere. At most 1024 characters.",
				Optional: true,
			},
			"description": schema.StringAttribute{
				Description: "Why this exclusion exists. At most 1024 characters.\n\n" +
					"Worth filling in: an exclusion is a hole in your firewall, and the next person " +
					"reviewing it needs to know whether it is still justified.",
				Optional: true,
			},
			"ordinal": schema.Int64Attribute{
				Description:   "Evaluation order within the policy.",
				Optional:      true,
				Computed:      true,
				PlanModifiers: []planmodifier.Int64{int64planmodifier.UseStateForUnknown()},
			},
			"revision": schema.Int64Attribute{
				Description: "A server-assigned counter, bumped on every write. Feed it into " +
					"`frostmoln_appgw_waf_policy_publication.rule_revisions` so a publish is ordered " +
					"after this exclusion without `depends_on`.",
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

// ValidateConfig enforces the exactly-one-target rule at plan time.
func (r *exclusionResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var cfg ExclusionModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if cfg.TargetSecRuleID.IsUnknown() || cfg.TargetTag.IsUnknown() {
		return
	}
	hasID := !cfg.TargetSecRuleID.IsNull()
	hasTag := !cfg.TargetTag.IsNull() && cfg.TargetTag.ValueString() != ""
	if hasID == hasTag {
		resp.Diagnostics.AddAttributeError(path.Root("target_secrule_id"),
			"Name Exactly One Of target_secrule_id Or target_tag",
			"Both or neither is meaningless: an exclusion narrows a specific rule, or every rule "+
				"carrying a tag.")
	}
	if !cfg.PathPattern.IsNull() && !cfg.PathPattern.IsUnknown() &&
		len(cfg.PathPattern.ValueString()) > 1024 {
		resp.Diagnostics.AddAttributeError(path.Root("path_pattern"),
			"path_pattern Is Too Long", "At most 1024 characters.")
	}
	if !cfg.TargetTag.IsNull() && !cfg.TargetTag.IsUnknown() && len(cfg.TargetTag.ValueString()) > 128 {
		resp.Diagnostics.AddAttributeError(path.Root("target_tag"),
			"target_tag Is Too Long", "At most 128 characters.")
	}
	if !cfg.Description.IsNull() && !cfg.Description.IsUnknown() &&
		len(cfg.Description.ValueString()) > 1024 {
		resp.Diagnostics.AddAttributeError(path.Root("description"),
			"description Is Too Long", "At most 1024 characters.")
	}
}

func (r *exclusionResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (m *ExclusionModel) fromAPI(e *apiExclusion) {
	m.RuleKey = types.StringValue(e.RuleKey)
	m.TargetSecRuleID = optionalInt(e.TargetSecRuleID)
	m.TargetTag = optionalString(e.TargetTag)
	m.Variable = types.StringValue(e.Variable)
	m.Selector = optionalString(e.Selector)
	m.PathPattern = optionalString(e.PathPattern)
	m.Description = optionalString(e.Description)
	m.Ordinal = types.Int64Value(int64(e.Ordinal))
	m.Revision = types.Int64Value(e.Revision)
	m.CreatedAt = types.StringValue(e.CreatedAt)
	m.UpdatedAt = types.StringValue(e.UpdatedAt)
}

func (r *exclusionResource) put(ctx context.Context, m *ExclusionModel) error {
	body := apiPutExclusionRequest{
		RuleKey:         m.RuleKey.ValueString(),
		TargetSecRuleID: int(m.TargetSecRuleID.ValueInt64()),
		TargetTag:       str(m.TargetTag),
		Variable:        m.Variable.ValueString(),
		Selector:        str(m.Selector),
		PathPattern:     str(m.PathPattern),
		Description:     str(m.Description),
	}
	if !m.Ordinal.IsNull() && !m.Ordinal.IsUnknown() {
		v := int(m.Ordinal.ValueInt64())
		body.Ordinal = &v
	}
	apiResp, err := r.client.Put(ctx, fmt.Sprintf("%s/exclusions/%s",
		r.policyPath(m.GatewayID.ValueString(), m.PolicyID.ValueString()), m.RuleKey.ValueString()), body)
	if err != nil {
		return err
	}
	e, err := client.ParseResponse[apiExclusion](apiResp)
	if err != nil {
		return err
	}
	gwID, policyID := m.GatewayID, m.PolicyID
	m.fromAPI(e)
	m.GatewayID, m.PolicyID = gwID, policyID
	return nil
}

func (r *exclusionResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan ExclusionModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.put(ctx, &plan); err != nil {
		resp.Diagnostics.AddError("Failed to Write WAF Exclusion", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *exclusionResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan ExclusionModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.put(ctx, &plan); err != nil {
		resp.Diagnostics.AddError("Failed to Write WAF Exclusion", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *exclusionResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state ExclusionModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	apiResp, err := r.client.Get(ctx, r.policyPath(
		state.GatewayID.ValueString(), state.PolicyID.ValueString(),
	)+"/draft", nil)
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
	for i := range draft.Exclusions {
		if draft.Exclusions[i].RuleKey == want {
			gwID, policyID := state.GatewayID, state.PolicyID
			state.fromAPI(&draft.Exclusions[i])
			state.GatewayID, state.PolicyID = gwID, policyID
			resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
			return
		}
	}
	resp.State.RemoveResource(ctx)
}

func (r *exclusionResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state ExclusionModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	_, err := r.client.Delete(ctx, fmt.Sprintf("%s/exclusions/%s",
		r.policyPath(state.GatewayID.ValueString(), state.PolicyID.ValueString()),
		state.RuleKey.ValueString()))
	if err != nil && !client.IsNotFound(err) {
		resp.Diagnostics.AddError("Failed to Delete WAF Exclusion", err.Error())
	}
}

func (r *exclusionResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	parts, err := client.ParseImportID(req.ID, "gateway_id", "policy_id", "rule_key")
	if err != nil {
		resp.Diagnostics.AddError("Invalid Import ID", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("gateway_id"), parts[0])...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("policy_id"), parts[1])...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("rule_key"), parts[2])...)
}

func (r *exclusionResource) policyPath(gwID, policyID string) string {
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
