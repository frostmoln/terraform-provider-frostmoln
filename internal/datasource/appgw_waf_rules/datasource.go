// Package appgw_waf_rules implements the frostmoln_appgw_waf_rules and
// frostmoln_appgw_waf_platform_rules data sources.
//
// 🔴 TWO DATA SOURCES, NOT ONE WITH A FILTER ARGUMENT, AND THE SPLIT IS THE
// POINT. A tenant's rules and the platform's rules are managed by completely
// different means -- one by frostmoln_appgw_waf_rule, the other by nobody --
// and a single data source would make it possible to iterate the wrong set by
// forgetting an argument.
//
// The tenant source filters to owner == "tenant". Without that filter, every
// emergency virtual patch the platform ships would appear in the list a
// customer iterates over, making `terraform plan` dirty for a change they did
// not make and cannot undo. The second time that happens they stop trusting
// plans, and a plan nobody trusts is worse than no plan.
package appgw_waf_rules

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
)

var (
	_ datasource.DataSource              = &rulesDataSource{}
	_ datasource.DataSourceWithConfigure = &rulesDataSource{}
)

// RulesModel is the data source's state.
type RulesModel struct {
	GatewayID types.String `tfsdk:"gateway_id"`
	PolicyID  types.String `tfsdk:"policy_id"`
	Source    types.String `tfsdk:"source"`
	Version   types.Int64  `tfsdk:"version"`
	Rules     []RuleModel  `tfsdk:"rules"`
}

// RuleModel is one rule in the policy.
type RuleModel struct {
	RuleKey     types.String `tfsdk:"rule_key"`
	Owner       types.String `tfsdk:"owner"`
	Kind        types.String `tfsdk:"kind"`
	Raw         types.String `tfsdk:"raw"`
	BuilderJSON types.String `tfsdk:"builder_json"`

	ManagedSecRuleID types.Int64  `tfsdk:"managed_secrule_id"`
	ManagedAction    types.String `tfsdk:"managed_action"`
	ManagedTarget    types.String `tfsdk:"managed_target"`

	SecRuleID   types.Int64  `tfsdk:"secrule_id"`
	Ordinal     types.Int64  `tfsdk:"ordinal"`
	Description types.String `tfsdk:"description"`
	Enabled     types.Bool   `tfsdk:"enabled"`

	OptedOut      types.Bool `tfsdk:"opted_out"`
	OptOutAllowed types.Bool `tfsdk:"opt_out_allowed"`

	Revision types.Int64 `tfsdk:"revision"`
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

	Revision int64 `json:"revision"`
}

type apiDraftResponse struct {
	Version struct {
		Version int `json:"version"`
	} `json:"version"`
	Rules []apiRule `json:"rules"`
}

type rulesDataSource struct {
	client *client.Client
	// wantOwner is the owner this data source reports. It is baked in at
	// construction rather than taken as an argument -- see the package comment.
	wantOwner string
	typeName  string
}

// NewTenantDataSource returns the tenant-owned rules data source.
func NewTenantDataSource() datasource.DataSource {
	return &rulesDataSource{wantOwner: "tenant", typeName: "_appgw_waf_rules"}
}

// NewPlatformDataSource returns the platform-owned rules data source.
func NewPlatformDataSource() datasource.DataSource {
	return &rulesDataSource{wantOwner: "platform", typeName: "_appgw_waf_platform_rules"}
}

func (d *rulesDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + d.typeName
}

func (d *rulesDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	desc := "The **tenant-owned** rules in an Application Gateway WAF policy — the ones you " +
		"author with `frostmoln_appgw_waf_rule`.\n\n" +
		"Platform-owned rules are deliberately excluded. They are not yours to change, and " +
		"including them would make every emergency virtual patch the platform ships show up in a " +
		"list your configuration iterates over — dirtying `terraform plan` for a change you did " +
		"not make and cannot undo. Read those with `frostmoln_appgw_waf_platform_rules`."
	if d.wantOwner == "platform" {
		desc = "The **platform-owned** rules in an Application Gateway WAF policy: the gateway's own " +
			"self-protection, and any emergency virtual patch in force.\n\n" +
			"Read-only. You cannot create, edit or delete one. Where `opt_out_allowed` is true you " +
			"may turn it off for this gateway — which is the one write a tenant may make to a rule " +
			"they do not own, and is not expressible in Terraform today; use `fm appgw waf rule " +
			"opt-out` or the portal.\n\n" +
			"Worth reading in a configuration to assert on: a rule appearing here that you did not " +
			"expect is the platform having shipped a patch, which is exactly the thing to notice."
	}

	resp.Schema = schema.Schema{
		Description: desc,
		Attributes: map[string]schema.Attribute{
			"gateway_id": schema.StringAttribute{
				Description: "The Application Gateway the policy belongs to.",
				Required:    true,
			},
			"policy_id": schema.StringAttribute{
				Description: "The WAF policy to read.",
				Required:    true,
			},
			"source": schema.StringAttribute{
				Description: "Which ruleset to read: `draft` (default) is what is being edited, " +
					"`active` is what the gateway is enforcing. They differ whenever there are " +
					"unpublished changes, which is most of the time during an apply.",
				Optional: true,
			},
			"version": schema.Int64Attribute{
				Description: "The version number the returned rules came from.",
				Computed:    true,
			},
			"rules": schema.ListNestedAttribute{
				Description: "The rules, in evaluation order.",
				Computed:    true,
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"rule_key":           schema.StringAttribute{Description: "The rule's stable identity.", Computed: true},
						"owner":              schema.StringAttribute{Description: "`tenant` or `platform`.", Computed: true},
						"kind":               schema.StringAttribute{Description: "`builder`, `raw` or `managedOverride`.", Computed: true},
						"raw":                schema.StringAttribute{Description: "The SecLang text, for a raw rule.", Computed: true},
						"builder_json":       schema.StringAttribute{Description: "The structured body as JSON, for a builder rule.", Computed: true},
						"managed_secrule_id": schema.Int64Attribute{Description: "The managed rule an override applies to.", Computed: true},
						"managed_action":     schema.StringAttribute{Description: "`disable` or `updateTarget`.", Computed: true},
						"managed_target":     schema.StringAttribute{Description: "The replacement target.", Computed: true},
						"secrule_id":         schema.Int64Attribute{Description: "The SecRule id the platform assigned.", Computed: true},
						"ordinal":            schema.Int64Attribute{Description: "Evaluation order within the policy.", Computed: true},
						"description":        schema.StringAttribute{Description: "What the rule is for.", Computed: true},
						"enabled":            schema.BoolAttribute{Description: "Whether the rule is evaluated.", Computed: true},
						"opted_out": schema.BoolAttribute{
							Description: "For a platform rule: whether this tenant has turned it off.",
							Computed:    true,
						},
						"opt_out_allowed": schema.BoolAttribute{
							Description: "For a platform rule: whether it may be turned off at all. " +
								"Gateway self-protection rules may not.",
							Computed: true,
						},
						"revision": schema.Int64Attribute{
							Description: "The server-assigned write counter.",
							Computed:    true,
						},
					},
				},
			},
		},
	}
}

func (d *rulesDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	c, ok := req.ProviderData.(*client.Client)
	if !ok {
		resp.Diagnostics.AddError("Unexpected Provider Data",
			fmt.Sprintf("Expected *client.Client, got %T", req.ProviderData))
		return
	}
	d.client = c
}

func (d *rulesDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var cfg RulesModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}

	base := d.client.TenantPath(fmt.Sprintf("/application-gateways/%s/waf-policies/%s",
		cfg.GatewayID.ValueString(), cfg.PolicyID.ValueString()))
	sub := "/draft"
	switch cfg.Source.ValueString() {
	case "", "draft":
	case "active":
		sub = "/versions/active"
	default:
		resp.Diagnostics.AddError("Invalid source",
			fmt.Sprintf("source must be \"draft\" or \"active\", got %q", cfg.Source.ValueString()))
		return
	}

	apiResp, err := d.client.Get(ctx, base+sub, nil)
	if err != nil {
		resp.Diagnostics.AddError("Failed to Read WAF Rules", err.Error())
		return
	}
	draft, err := client.ParseResponse[apiDraftResponse](apiResp)
	if err != nil {
		resp.Diagnostics.AddError("Failed to Parse WAF Rules Response", err.Error())
		return
	}

	cfg.Version = types.Int64Value(int64(draft.Version.Version))
	cfg.Rules = make([]RuleModel, 0, len(draft.Rules))
	for _, r := range draft.Rules {
		if r.Owner != d.wantOwner {
			continue
		}
		builder := types.StringValue("")
		if len(r.Builder) > 0 {
			builder = types.StringValue(string(r.Builder))
		}
		cfg.Rules = append(cfg.Rules, RuleModel{
			RuleKey:          types.StringValue(r.RuleKey),
			Owner:            types.StringValue(r.Owner),
			Kind:             types.StringValue(r.Kind),
			Raw:              types.StringValue(r.Raw),
			BuilderJSON:      builder,
			ManagedSecRuleID: types.Int64Value(int64(r.ManagedSecRuleID)),
			ManagedAction:    types.StringValue(r.ManagedAction),
			ManagedTarget:    types.StringValue(r.ManagedTarget),
			SecRuleID:        types.Int64Value(int64(r.SecRuleID)),
			Ordinal:          types.Int64Value(int64(r.Ordinal)),
			Description:      types.StringValue(r.Description),
			Enabled:          types.BoolValue(r.Enabled),
			OptedOut:         types.BoolValue(r.OptedOut),
			OptOutAllowed:    types.BoolValue(r.OptOutAllowed),
			Revision:         types.Int64Value(r.Revision),
		})
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &cfg)...)
}
