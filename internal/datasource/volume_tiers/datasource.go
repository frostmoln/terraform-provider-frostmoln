// Package volumetiers implements the frostmoln_volume_tiers Terraform data source.
package volumetiers

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
)

var _ datasource.DataSource = &volumeTiersDataSource{}

// NewDataSource returns a new frostmoln_volume_tiers data source factory.
func NewDataSource() datasource.DataSource {
	return &volumeTiersDataSource{}
}

type volumeTiersDataSource struct {
	client *client.Client
}

// volumeTiersModel is the Terraform state model for the volume-tier list.
type volumeTiersModel struct {
	VolumeTiers types.List `tfsdk:"volume_tiers"`
}

// volumeTierItemModel represents a single volume tier in the list.
type volumeTierItemModel struct {
	Key            types.String `tfsdk:"key"`
	Name           types.String `tfsdk:"name"`
	Description    types.String `tfsdk:"description"`
	Status         types.String `tfsdk:"status"`
	IsDefault      types.Bool   `tfsdk:"is_default"`
	PricingTierKey types.String `tfsdk:"pricing_tier_key"`
	SortOrder      types.Int64  `tfsdk:"sort_order"`
}

// apiVolumeTier is the API representation of a volume tier.
type apiVolumeTier struct {
	Key            string `json:"key"`
	Name           string `json:"name"`
	Description    string `json:"description"`
	Status         string `json:"status"`
	IsDefault      bool   `json:"isDefault"`
	PricingTierKey string `json:"pricingTierKey"`
	SortOrder      int64  `json:"sortOrder"`
}

// apiVolumeTierList is the API response for listing volume tiers.
type apiVolumeTierList struct {
	Data []apiVolumeTier `json:"data"`
}

var volumeTierItemAttrTypes = map[string]attr.Type{
	"key":              types.StringType,
	"name":             types.StringType,
	"description":      types.StringType,
	"status":           types.StringType,
	"is_default":       types.BoolType,
	"pricing_tier_key": types.StringType,
	"sort_order":       types.Int64Type,
}

func (d *volumeTiersDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_volume_tiers"
}

func (d *volumeTiersDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "List the available block-storage volume tiers. Only tiers with status \"offered\" can be selected when creating a volume; the offered set is server-defined and may change without a provider release.",
		Attributes: map[string]schema.Attribute{
			"volume_tiers": schema.ListNestedAttribute{
				Description: "The list of volume tiers.",
				Computed:    true,
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"key": schema.StringAttribute{
							Description: "The tier key — also the volume's volume_type value (e.g. \"ssd\").",
							Computed:    true,
						},
						"name": schema.StringAttribute{
							Description: "The display name of the tier.",
							Computed:    true,
						},
						"description": schema.StringAttribute{
							Description: "A human-readable description of the tier.",
							Computed:    true,
						},
						"status": schema.StringAttribute{
							Description: "The tier status (offered, unavailable, coming_soon). Only \"offered\" tiers are selectable when creating a volume.",
							Computed:    true,
						},
						"is_default": schema.BoolAttribute{
							Description: "Whether this tier is the default offered tier.",
							Computed:    true,
						},
						"pricing_tier_key": schema.StringAttribute{
							Description: "The billing pricing tier key this tier maps to.",
							Computed:    true,
						},
						"sort_order": schema.Int64Attribute{
							Description: "The display ordering of the tier.",
							Computed:    true,
						},
					},
				},
			},
		},
	}
}

func (d *volumeTiersDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
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

func (d *volumeTiersDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var state volumeTiersModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	apiResp, err := d.client.Get(ctx, "/v1/volume-tiers", nil)
	if err != nil {
		resp.Diagnostics.AddError("Failed to list volume tiers", err.Error())
		return
	}

	var list apiVolumeTierList
	if err := json.Unmarshal(apiResp.Body, &list); err != nil {
		resp.Diagnostics.AddError("Failed to parse volume tiers response", err.Error())
		return
	}

	items := make([]volumeTierItemModel, 0, len(list.Data))
	for _, t := range list.Data {
		items = append(items, volumeTierItemModel{
			Key:            types.StringValue(t.Key),
			Name:           types.StringValue(t.Name),
			Description:    types.StringValue(t.Description),
			Status:         types.StringValue(t.Status),
			IsDefault:      types.BoolValue(t.IsDefault),
			PricingTierKey: types.StringValue(t.PricingTierKey),
			SortOrder:      types.Int64Value(t.SortOrder),
		})
	}

	tiersList, diags := types.ListValueFrom(ctx, types.ObjectType{AttrTypes: volumeTierItemAttrTypes}, items)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	state.VolumeTiers = tiersList
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}
