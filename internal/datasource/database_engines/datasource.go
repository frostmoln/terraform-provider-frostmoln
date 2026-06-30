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

// engineItemModel represents a single database engine in the list.
type engineItemModel struct {
	Engine   types.String `tfsdk:"engine"`
	Versions types.List   `tfsdk:"versions"`
}

// versionItemModel represents a single version of a database engine.
type versionItemModel struct {
	Version   types.String `tfsdk:"version"`
	Status    types.String `tfsdk:"status"`
	EndOfLife types.String `tfsdk:"end_of_life"`
	IsDefault types.Bool   `tfsdk:"is_default"`
}

// apiDatabaseVersion is the API representation of a database engine version
// (database/internal/domain/version.go DatabaseVersion).
type apiDatabaseVersion struct {
	Version   string `json:"version"`
	Status    string `json:"status"`
	EndOfLife string `json:"endOfLife,omitempty"`
	IsDefault bool   `json:"isDefault"`
}

// apiDatabaseEngine is the API representation of a database engine. The engines
// endpoint serializes the engine name under `engine` and the versions as an
// array of objects (database/internal/service/interfaces.go EngineInfo).
type apiDatabaseEngine struct {
	Engine   string               `json:"engine"`
	Versions []apiDatabaseVersion `json:"versions,omitempty"`
}

// apiDatabaseEngineList is the API response for listing database engines.
type apiDatabaseEngineList struct {
	Engines []apiDatabaseEngine `json:"engines"`
}

var versionItemAttrTypes = map[string]attr.Type{
	"version":     types.StringType,
	"status":      types.StringType,
	"end_of_life": types.StringType,
	"is_default":  types.BoolType,
}

var engineItemAttrTypes = map[string]attr.Type{
	"engine":   types.StringType,
	"versions": types.ListType{ElemType: types.ObjectType{AttrTypes: versionItemAttrTypes}},
}

func (d *databaseEnginesDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_database_engines"
}

func (d *databaseEnginesDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Lists all available database engines and their versions for managed database instances.",
		Attributes: map[string]schema.Attribute{
			"engines": schema.ListNestedAttribute{
				Description: "The list of available database engines.",
				Computed:    true,
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"engine": schema.StringAttribute{
							Description: "The engine name (e.g. \"postgresql\", \"mysql\").",
							Computed:    true,
						},
						"versions": schema.ListNestedAttribute{
							Description: "The supported versions for this engine.",
							Computed:    true,
							NestedObject: schema.NestedAttributeObject{
								Attributes: map[string]schema.Attribute{
									"version": schema.StringAttribute{
										Description: "The version string (e.g. \"16\").",
										Computed:    true,
									},
									"status": schema.StringAttribute{
										Description: "The version lifecycle status (current/supported/deprecated/eol/innovation).",
										Computed:    true,
									},
									"end_of_life": schema.StringAttribute{
										Description: "The end-of-life date for this version.",
										Computed:    true,
									},
									"is_default": schema.BoolAttribute{
										Description: "Whether this is the recommended default version.",
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

	items := make([]engineItemModel, 0, len(list.Engines))
	for _, e := range list.Engines {
		versionItems := make([]versionItemModel, 0, len(e.Versions))
		for _, v := range e.Versions {
			item := versionItemModel{
				Version:   types.StringValue(v.Version),
				Status:    types.StringValue(v.Status),
				IsDefault: types.BoolValue(v.IsDefault),
			}
			if v.EndOfLife != "" {
				item.EndOfLife = types.StringValue(v.EndOfLife)
			} else {
				item.EndOfLife = types.StringNull()
			}
			versionItems = append(versionItems, item)
		}

		versionsList, diags := types.ListValueFrom(ctx, types.ObjectType{AttrTypes: versionItemAttrTypes}, versionItems)
		resp.Diagnostics.Append(diags...)
		if resp.Diagnostics.HasError() {
			return
		}

		items = append(items, engineItemModel{
			Engine:   types.StringValue(e.Engine),
			Versions: versionsList,
		})
	}

	enginesList, diags := types.ListValueFrom(ctx, types.ObjectType{AttrTypes: engineItemAttrTypes}, items)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	state.Engines = enginesList
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}
