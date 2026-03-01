// Package flavors implements the fm_flavors Terraform data source.
package flavors

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"git.nl.cloud/NordicLight/terraform-provider-frostmoln/internal/client"
)

var _ datasource.DataSource = &flavorsDataSource{}

// NewDataSource returns a new fm_flavors data source factory.
func NewDataSource() datasource.DataSource {
	return &flavorsDataSource{}
}

type flavorsDataSource struct {
	client *client.Client
}

// flavorsModel is the Terraform state model for the flavors list.
type flavorsModel struct {
	Category types.String `tfsdk:"category"`
	Flavors  types.List   `tfsdk:"flavors"`
}

// flavorItemModel represents a single flavor in the list.
type flavorItemModel struct {
	ID       types.String `tfsdk:"id"`
	Name     types.String `tfsdk:"name"`
	VCPUs    types.Int64  `tfsdk:"vcpus"`
	RAMMB    types.Int64  `tfsdk:"ram_mb"`
	DiskGB   types.Int64  `tfsdk:"disk_gb"`
	Category types.String `tfsdk:"category"`
}

// apiFlavor is the API representation of a flavor.
type apiFlavor struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	VCPUs    int    `json:"vcpus"`
	RAMMB    int    `json:"ramMb"`
	DiskGB   int    `json:"diskGb"`
	Category string `json:"category,omitempty"`
}

// apiFlavorList is the API response for listing flavors.
type apiFlavorList struct {
	Flavors []apiFlavor `json:"flavors"`
}

var flavorItemAttrTypes = map[string]attr.Type{
	"id":       types.StringType,
	"name":     types.StringType,
	"vcpus":    types.Int64Type,
	"ram_mb":   types.Int64Type,
	"disk_gb":  types.Int64Type,
	"category": types.StringType,
}

func (d *flavorsDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_flavors"
}

func (d *flavorsDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "List flavors with optional category filter.",
		Attributes: map[string]schema.Attribute{
			"category": schema.StringAttribute{
				Description: "Filter flavors by category.",
				Optional:    true,
			},
			"flavors": schema.ListNestedAttribute{
				Description: "The list of flavors matching the filters.",
				Computed:    true,
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"id": schema.StringAttribute{
							Description: "The unique identifier of the flavor.",
							Computed:    true,
						},
						"name": schema.StringAttribute{
							Description: "The name of the flavor.",
							Computed:    true,
						},
						"vcpus": schema.Int64Attribute{
							Description: "The number of virtual CPUs.",
							Computed:    true,
						},
						"ram_mb": schema.Int64Attribute{
							Description: "The amount of RAM in MB.",
							Computed:    true,
						},
						"disk_gb": schema.Int64Attribute{
							Description: "The disk size in GB.",
							Computed:    true,
						},
						"category": schema.StringAttribute{
							Description: "The category of the flavor.",
							Computed:    true,
						},
					},
				},
			},
		},
	}
}

func (d *flavorsDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
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

func (d *flavorsDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var state flavorsModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	apiResp, err := d.client.Get(ctx, "/v1/flavors", nil)
	if err != nil {
		resp.Diagnostics.AddError("Failed to list flavors", err.Error())
		return
	}

	var list apiFlavorList
	if err := json.Unmarshal(apiResp.Body, &list); err != nil {
		resp.Diagnostics.AddError("Failed to parse flavors response", err.Error())
		return
	}

	categoryFilter := ""
	if !state.Category.IsNull() && !state.Category.IsUnknown() {
		categoryFilter = state.Category.ValueString()
	}

	var items []flavorItemModel
	for _, f := range list.Flavors {
		if categoryFilter != "" && f.Category != categoryFilter {
			continue
		}
		items = append(items, flavorItemModel{
			ID:       types.StringValue(f.ID),
			Name:     types.StringValue(f.Name),
			VCPUs:    types.Int64Value(int64(f.VCPUs)),
			RAMMB:    types.Int64Value(int64(f.RAMMB)),
			DiskGB:   types.Int64Value(int64(f.DiskGB)),
			Category: types.StringValue(f.Category),
		})
	}

	flavorsList, diags := types.ListValueFrom(ctx, types.ObjectType{AttrTypes: flavorItemAttrTypes}, items)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	state.Flavors = flavorsList
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}
