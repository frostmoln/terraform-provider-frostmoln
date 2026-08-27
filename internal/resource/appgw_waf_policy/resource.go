// Package appgw_waf_policy implements the frostmoln_appgw_waf_policy Terraform
// resource.
package appgw_waf_policy

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
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

var (
	_ resource.Resource                = &policyResource{}
	_ resource.ResourceWithImportState = &policyResource{}
	_ resource.ResourceWithConfigure   = &policyResource{}
)

// PolicyModel is the Terraform state model for a WAF policy.
type PolicyModel struct {
	ID        types.String `tfsdk:"id"`
	GatewayID types.String `tfsdk:"gateway_id"`
	Name      types.String `tfsdk:"name"`

	Mode                  types.String `tfsdk:"mode"`
	ParanoiaLevel         types.Int64  `tfsdk:"paranoia_level"`
	AnomalyScoreThreshold types.Int64  `tfsdk:"anomaly_score_threshold"`
	CRSVersion            types.String `tfsdk:"managed_ruleset_version"`
	FailMode              types.String `tfsdk:"fail_mode"`
	RequestBodyLimitBytes types.Int64  `tfsdk:"request_body_limit_bytes"`

	ActiveVersion types.Int64 `tfsdk:"active_version"`
	DraftVersion  types.Int64 `tfsdk:"draft_version"`

	CreatedAt types.String `tfsdk:"created_at"`
	UpdatedAt types.String `tfsdk:"updated_at"`
}

type apiPolicy struct {
	ID        string `json:"id"`
	GatewayID string `json:"gatewayId"`
	Name      string `json:"name"`

	Mode                  string `json:"mode"`
	ParanoiaLevel         int    `json:"paranoiaLevel"`
	AnomalyScoreThreshold int    `json:"anomalyScoreThreshold"`
	CRSVersion            string `json:"crsVersion"`
	FailMode              string `json:"failMode"`
	RequestBodyLimitBytes int    `json:"requestBodyLimitBytes"`

	ActiveVersion *int `json:"activeVersion,omitempty"`
	DraftVersion  *int `json:"draftVersion,omitempty"`

	ConfigGeneration int64  `json:"configGeneration"`
	CreatedAt        string `json:"createdAt"`
	UpdatedAt        string `json:"updatedAt,omitempty"`
}

type apiCreatePolicyRequest struct {
	Name                  string `json:"name"`
	Mode                  string `json:"mode,omitempty"`
	ParanoiaLevel         int    `json:"paranoiaLevel,omitempty"`
	AnomalyScoreThreshold int    `json:"anomalyScoreThreshold,omitempty"`
	CRSVersion            string `json:"crsVersion,omitempty"`
	FailMode              string `json:"failMode,omitempty"`
	RequestBodyLimitBytes int    `json:"requestBodyLimitBytes,omitempty"`
}

type apiUpdatePolicyRequest struct {
	Mode                  *string `json:"mode,omitempty"`
	ParanoiaLevel         *int    `json:"paranoiaLevel,omitempty"`
	AnomalyScoreThreshold *int    `json:"anomalyScoreThreshold,omitempty"`
	CRSVersion            *string `json:"crsVersion,omitempty"`
	FailMode              *string `json:"failMode,omitempty"`
	RequestBodyLimitBytes *int    `json:"requestBodyLimitBytes,omitempty"`
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
			"mode": schema.StringAttribute{
				Description: "`detect` records what the ruleset WOULD have done and blocks nothing; " +
					"`block` refuses matching requests.\n\n" +
					"`detect` is the safe place to start and the safe place to return to. Move to " +
					"`block` after a dry-run has shown you what would newly be refused.",
				Optional:      true,
				Computed:      true,
				Validators:    []validator.String{stringvalidator.OneOf("detect", "block")},
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"paranoia_level": schema.Int64Attribute{
				Description: "How aggressively the managed ruleset matches, 1-4. Higher catches " +
					"more and produces more false positives.",
				Optional:      true,
				Computed:      true,
				Validators:    []validator.Int64{int64validator.Between(1, 4)},
				PlanModifiers: []planmodifier.Int64{int64planmodifier.UseStateForUnknown()},
			},
			"anomaly_score_threshold": schema.Int64Attribute{
				Description:   "The accumulated anomaly score at which a request is refused.",
				Optional:      true,
				Computed:      true,
				Validators:    []validator.Int64{int64validator.Between(1, 1000)},
				PlanModifiers: []planmodifier.Int64{int64planmodifier.UseStateForUnknown()},
			},
			"managed_ruleset_version": schema.StringAttribute{
				Description: "The managed ruleset version in force.\n\n" +
					"The server accepts only the version the running build ships, and migrating a " +
					"stored pin is the platform's job — so leave this unset unless you have a " +
					"specific reason, and read it rather than writing it.",
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
				Description: "How much of a request body is inspected, in bytes. Between 128 KiB " +
					"and 512 MiB. Anything beyond the limit is not examined.",
				Optional:      true,
				Computed:      true,
				Validators:    []validator.Int64{int64validator.Between(128*1024, 512*1024*1024)},
				PlanModifiers: []planmodifier.Int64{int64planmodifier.UseStateForUnknown()},
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
	m.Mode = types.StringValue(p.Mode)
	m.ParanoiaLevel = types.Int64Value(int64(p.ParanoiaLevel))
	m.AnomalyScoreThreshold = types.Int64Value(int64(p.AnomalyScoreThreshold))
	m.CRSVersion = types.StringValue(p.CRSVersion)
	m.FailMode = types.StringValue(p.FailMode)
	m.RequestBodyLimitBytes = types.Int64Value(int64(p.RequestBodyLimitBytes))
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
	apiResp, err := r.client.Post(ctx, r.client.TenantPath(fmt.Sprintf(
		"/application-gateways/%s/waf-policies", plan.GatewayID.ValueString())),
		apiCreatePolicyRequest{
			Name:                  plan.Name.ValueString(),
			Mode:                  str(plan.Mode),
			ParanoiaLevel:         int(plan.ParanoiaLevel.ValueInt64()),
			AnomalyScoreThreshold: int(plan.AnomalyScoreThreshold.ValueInt64()),
			CRSVersion:            str(plan.CRSVersion),
			FailMode:              str(plan.FailMode),
			RequestBodyLimitBytes: int(plan.RequestBodyLimitBytes.ValueInt64()),
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

	if body.Mode != nil || body.ParanoiaLevel != nil || body.AnomalyScoreThreshold != nil ||
		body.FailMode != nil || body.RequestBodyLimitBytes != nil {
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
