// Package database_engines implements the frostmoln_database_engines Terraform data source.
package database_engines

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

var _ datasource.DataSource = &databaseEnginesDataSource{}

// NewDataSource returns a new frostmoln_database_engines data source factory.
func NewDataSource() datasource.DataSource {
	return &databaseEnginesDataSource{}
}

type databaseEnginesDataSource struct {
	client *client.Client
}

// databaseEnginesModel is the Terraform state model for the database engines list.
type databaseEnginesModel struct {
	Engines types.List `tfsdk:"engines"`
}

// databaseEngineItemModel represents a single database engine in the list.
type databaseEngineItemModel struct {
	Name        types.String `tfsdk:"name"`
	Description types.String `tfsdk:"description"`
	Versions    types.List   `tfsdk:"versions"`
}

// apiDatabaseEngine is the API representation of a database engine.
type apiDatabaseEngine struct {
	Name        string   `json:"name"`
	Description string   `json:"description,omitempty"`
	Versions    []string `json:"versions,omitempty"`
}

// apiDatabaseEngineList is the API response for listing database engines.
type apiDatabaseEngineList struct {
	Engines []apiDatabaseEngine `json:"engines"`
}

var engineItemAttrTypes = map[string]attr.Type{
	"name":        types.StringType,
	"description": types.StringType,
	"versions":    types.ListType{ElemType: types.StringType},
}

func (d *databaseEnginesDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_database_engines"
}

func (d *databaseEnginesDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Lists all available database engines for managed database instances.",
		Attributes: map[string]schema.Attribute{
			"engines": schema.ListNestedAttribute{
				Description: "The list of available database engines.",
				Computed:    true,
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"name": schema.StringAttribute{
							Description: "The engine name (e.g. \"postgresql\", \"mysql\").",
							Computed:    true,
						},
						"description": schema.StringAttribute{
							Description: "A human-readable description of the engine.",
							Computed:    true,
						},
						"versions": schema.ListAttribute{
							Description: "The list of supported version strings for this engine.",
							Computed:    true,
							ElementType: types.StringType,
						},
					},
				},
			},
		},
	}
}

func (d *databaseEnginesDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
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

func (d *databaseEnginesDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var state databaseEnginesModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	apiResp, err := d.client.Get(ctx, "/v1/databases/engines", nil)
	if err != nil {
		resp.Diagnostics.AddError("Failed to list database engines", err.Error())
		return
	}

	var list apiDatabaseEngineList
	if err := json.Unmarshal(apiResp.Body, &list); err != nil {
		resp.Diagnostics.AddError("Failed to parse database engines response", err.Error())
		return
	}

	var items []databaseEngineItemModel
	for _, e := range list.Engines {
		item := databaseEngineItemModel{
			Name: types.StringValue(e.Name),
		}

		if e.Description != "" {
			item.Description = types.StringValue(e.Description)
		} else {
			item.Description = types.StringNull()
		}

		if len(e.Versions) > 0 {
			versionList, diags := types.ListValueFrom(ctx, types.StringType, e.Versions)
			resp.Diagnostics.Append(diags...)
			if resp.Diagnostics.HasError() {
				return
			}
			item.Versions = versionList
		} else {
			item.Versions = types.ListNull(types.StringType)
		}

		items = append(items, item)
	}

	enginesList, diags := types.ListValueFrom(ctx, types.ObjectType{AttrTypes: engineItemAttrTypes}, items)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	state.Engines = enginesList
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}
