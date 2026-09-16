// Package tagcolors implements the frostmoln_tag_colors data source: the tag
// colour rules of an organization.
package tagcolors

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/tagsettings"
)

var _ datasource.DataSource = &tagColorsDataSource{}

// NewDataSource returns a new frostmoln_tag_colors data source factory.
func NewDataSource() datasource.DataSource {
	return &tagColorsDataSource{}
}

type tagColorsDataSource struct {
	client *client.Client
}

type tagColorsModel struct {
	OrganizationID types.String `tfsdk:"organization_id"`
	Rules          types.List   `tfsdk:"rules"`
}

type ruleModel struct {
	ID    types.String `tfsdk:"id"`
	Key   types.String `tfsdk:"key"`
	Value types.String `tfsdk:"value"`
	Color types.String `tfsdk:"color"`
}

var ruleAttrTypes = map[string]attr.Type{
	"id":    types.StringType,
	"key":   types.StringType,
	"value": types.StringType,
	"color": types.StringType,
}

func (d *tagColorsDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_tag_colors"
}

func (d *tagColorsDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Lists the tag colour rules of an organization — the rules `frostmoln_tag_color` manages, " +
			"including ones created in the portal, with their ids for import. With `organization_id` omitted it " +
			"lists the rules of the organization that owns the provider's tenant, which any member of that " +
			"organization can read. Rules are ordered by key, the key-only rule first, then by value. An API key " +
			"needs `organizations:read`, and reaches only the organization that owns the provider's tenant: an " +
			"`organization_id` naming another organization is refused.",
		Attributes: map[string]schema.Attribute{
			"organization_id": schema.StringAttribute{
				Description: "The organization whose rules to list. When omitted, the organization that owns the " +
					"provider's tenant, and this attribute is set to its id as the platform reports it (null if the " +
					"platform does not report it).",
				Optional:   true,
				Computed:   true,
				Validators: []validator.String{tagsettings.OrganizationIDValidator()},
			},
			"rules": schema.ListNestedAttribute{
				Description: "The organization's colour rules.",
				Computed:    true,
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"id": schema.StringAttribute{
							Description: "The rule's id.",
							Computed:    true,
						},
						"key": schema.StringAttribute{
							Description: "The tag key the rule colours.",
							Computed:    true,
						},
						"value": schema.StringAttribute{
							Description: "The exact tag value the rule colours, or null for a rule matching any value " +
								"of `key`. The empty string is a different rule, matching only the empty value.",
							Computed: true,
						},
						"color": schema.StringAttribute{
							Description: "The background colour, `#RRGGBB`, letter case as stored.",
							Computed:    true,
						},
					},
				},
			},
		},
	}
}

func (d *tagColorsDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	c, ok := req.ProviderData.(*client.Client)
	if !ok {
		resp.Diagnostics.AddError(
			"Unexpected Data Source Configure Type",
			fmt.Sprintf("Expected *client.Client, got: %T", req.ProviderData),
		)
		return
	}
	d.client = c
}

func (d *tagColorsDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var cfg tagColorsModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}

	var (
		orgID string
		rules []tagsettings.Rule
		err   error
	)
	if !cfg.OrganizationID.IsNull() && !cfg.OrganizationID.IsUnknown() {
		orgID = cfg.OrganizationID.ValueString()
		rules, err = tagsettings.ListOrgRules(ctx, d.client, orgID)
	} else {
		// The tenant route answers the owning organization's rules to any member
		// of it, and names that organization — no second call, and no guess.
		orgID, rules, err = tagsettings.ListTenantRules(ctx, d.client)
	}
	if err != nil {
		resp.Diagnostics.AddError("Failed to list tag colour rules", err.Error())
		return
	}

	items := make([]ruleModel, 0, len(rules))
	for _, r := range rules {
		value := types.StringNull()
		if r.Value != nil {
			value = types.StringValue(*r.Value)
		}
		items = append(items, ruleModel{
			ID:    types.StringValue(r.ID),
			Key:   types.StringValue(r.Key),
			Value: value,
			Color: types.StringValue(r.Color),
		})
	}
	list, diags := types.ListValueFrom(ctx, types.ObjectType{AttrTypes: ruleAttrTypes}, items)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	state := tagColorsModel{Rules: list, OrganizationID: types.StringNull()}
	if orgID != "" {
		// The platform reports the canonical form already; normalising keeps the
		// value equal to what a frostmoln_tag_color records for the same org.
		if canonical, err := tagsettings.CanonicalUUID(orgID); err == nil {
			orgID = canonical
		}
		state.OrganizationID = types.StringValue(orgID)
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}
