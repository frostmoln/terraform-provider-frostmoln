// Package regions implements the frostmoln_regions Terraform data source.
package regions

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

var _ datasource.DataSource = &regionsDataSource{}

// NewDataSource returns a new frostmoln_regions data source factory.
func NewDataSource() datasource.DataSource {
	return &regionsDataSource{}
}

type regionsDataSource struct {
	client *client.Client
}

// regionsModel is the Terraform state model for the regions list.
type regionsModel struct {
	Regions types.List `tfsdk:"regions"`
}

// regionItemModel represents a single region in the list.
type regionItemModel struct {
	ID                types.String `tfsdk:"id"`
	Name              types.String `tfsdk:"name"`
	Description       types.String `tfsdk:"description"`
	Country           types.String `tfsdk:"country"`
	Status            types.String `tfsdk:"status"`
	IsDefault         types.Bool   `tfsdk:"is_default"`
	AvailabilityZones types.List   `tfsdk:"availability_zones"`
}

// azItemModel represents a single availability zone within a region.
type azItemModel struct {
	ID        types.String `tfsdk:"id"`
	Name      types.String `tfsdk:"name"`
	City      types.String `tfsdk:"city"`
	Status    types.String `tfsdk:"status"`
	IsDefault types.Bool   `tfsdk:"is_default"`
}

// apiAZ is the API representation of an availability zone.
type apiAZ struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	City      string `json:"city"`
	Status    string `json:"status"`
	IsDefault bool   `json:"isDefault"`
}

// apiRegion is the API representation of a region.
type apiRegion struct {
	ID                string  `json:"id"`
	Name              string  `json:"name"`
	Description       string  `json:"description"`
	Country           string  `json:"country"`
	Status            string  `json:"status"`
	IsDefault         bool    `json:"isDefault"`
	AvailabilityZones []apiAZ `json:"availabilityZones"`
}

// apiRegionList is the API response for listing regions.
type apiRegionList struct {
	Data []apiRegion `json:"data"`
}

var azItemAttrTypes = map[string]attr.Type{
	"id":         types.StringType,
	"name":       types.StringType,
	"city":       types.StringType,
	"status":     types.StringType,
	"is_default": types.BoolType,
}

var regionItemAttrTypes = map[string]attr.Type{
	"id":          types.StringType,
	"name":        types.StringType,
	"description": types.StringType,
	"country":     types.StringType,
	"status":      types.StringType,
	"is_default":  types.BoolType,
	"availability_zones": types.ListType{
		ElemType: types.ObjectType{AttrTypes: azItemAttrTypes},
	},
}

func (d *regionsDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_regions"
}

func (d *regionsDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "List the available regions and their availability zones.",
		Attributes: map[string]schema.Attribute{
			"regions": schema.ListNestedAttribute{
				Description: "The list of regions.",
				Computed:    true,
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"id": schema.StringAttribute{
							Description: "The unique identifier of the region.",
							Computed:    true,
						},
						"name": schema.StringAttribute{
							Description: "The display name of the region.",
							Computed:    true,
						},
						"description": schema.StringAttribute{
							Description: "A human-readable description of the region.",
							Computed:    true,
						},
						"country": schema.StringAttribute{
							Description: "The ISO country code of the region.",
							Computed:    true,
						},
						"status": schema.StringAttribute{
							Description: "The status of the region (active, maintenance, deprecated, offline).",
							Computed:    true,
						},
						"is_default": schema.BoolAttribute{
							Description: "Whether this region is the default region.",
							Computed:    true,
						},
						"availability_zones": schema.ListNestedAttribute{
							Description: "The availability zones within the region.",
							Computed:    true,
							NestedObject: schema.NestedAttributeObject{
								Attributes: map[string]schema.Attribute{
									"id": schema.StringAttribute{
										Description: "The unique identifier of the availability zone.",
										Computed:    true,
									},
									"name": schema.StringAttribute{
										Description: "The display name of the availability zone.",
										Computed:    true,
									},
									"city": schema.StringAttribute{
										Description: "The city where the availability zone is located.",
										Computed:    true,
									},
									"status": schema.StringAttribute{
										Description: "The status of the availability zone.",
										Computed:    true,
									},
									"is_default": schema.BoolAttribute{
										Description: "Whether this availability zone is the default for its region.",
										Computed:    true,
									},
								},
							},
						},
					},
				},
			},
		},
	}
}

func (d *regionsDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
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

func (d *regionsDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var state regionsModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	apiResp, err := d.client.Get(ctx, "/v1/regions", nil)
	if err != nil {
		resp.Diagnostics.AddError("Failed to list regions", err.Error())
		return
	}

	var list apiRegionList
	if err := json.Unmarshal(apiResp.Body, &list); err != nil {
		resp.Diagnostics.AddError("Failed to parse regions response", err.Error())
		return
	}

	items := make([]regionItemModel, 0, len(list.Data))
	for _, r := range list.Data {
		azItems := make([]azItemModel, 0, len(r.AvailabilityZones))
		for _, az := range r.AvailabilityZones {
			azItems = append(azItems, azItemModel{
				ID:        types.StringValue(az.ID),
				Name:      types.StringValue(az.Name),
				City:      types.StringValue(az.City),
				Status:    types.StringValue(az.Status),
				IsDefault: types.BoolValue(az.IsDefault),
			})
		}

		azList, diags := types.ListValueFrom(ctx, types.ObjectType{AttrTypes: azItemAttrTypes}, azItems)
		resp.Diagnostics.Append(diags...)
		if resp.Diagnostics.HasError() {
			return
		}

		items = append(items, regionItemModel{
			ID:                types.StringValue(r.ID),
			Name:              types.StringValue(r.Name),
			Description:       types.StringValue(r.Description),
			Country:           types.StringValue(r.Country),
			Status:            types.StringValue(r.Status),
			IsDefault:         types.BoolValue(r.IsDefault),
			AvailabilityZones: azList,
		})
	}

	regionsList, diags := types.ListValueFrom(ctx, types.ObjectType{AttrTypes: regionItemAttrTypes}, items)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	state.Regions = regionsList
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}
