// Package database_types implements the frostmoln_database_types Terraform data source.
package database_types

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

var _ datasource.DataSource = &databaseTypesDataSource{}

// NewDataSource returns a new frostmoln_database_types data source factory.
func NewDataSource() datasource.DataSource {
	return &databaseTypesDataSource{}
}

type databaseTypesDataSource struct {
	client *client.Client
}

// databaseTypesModel is the Terraform state model for the database types list.
type databaseTypesModel struct {
	Types types.List `tfsdk:"types"`
}

// typeItemModel represents a single database type in the list.
type typeItemModel struct {
	Type     types.String `tfsdk:"type"`
	Versions types.List   `tfsdk:"versions"`
}

// versionItemModel represents a single version of a database type.
type versionItemModel struct {
	Version   types.String `tfsdk:"version"`
	Status    types.String `tfsdk:"status"`
	EndOfLife types.String `tfsdk:"end_of_life"`
	IsDefault types.Bool   `tfsdk:"is_default"`
}

// apiDatabaseVersion is the API representation of a database type version
// (database/internal/domain/version.go DatabaseVersion).
type apiDatabaseVersion struct {
	Version   string `json:"version"`
	Status    string `json:"status"`
	EndOfLife string `json:"endOfLife,omitempty"`
	IsDefault bool   `json:"isDefault"`
}

// apiDatabaseType is the API representation of a database type. The types
// endpoint serializes the type name under `type` and the versions as an
// array of objects (database/internal/service/interfaces.go TypeInfo).
type apiDatabaseType struct {
	Type     string               `json:"type"`
	Versions []apiDatabaseVersion `json:"versions,omitempty"`
}

// apiDatabaseTypeList is the API response for listing database types.
type apiDatabaseTypeList struct {
	Types []apiDatabaseType `json:"types"`
}

var versionItemAttrTypes = map[string]attr.Type{
	"version":     types.StringType,
	"status":      types.StringType,
	"end_of_life": types.StringType,
	"is_default":  types.BoolType,
}

var typeItemAttrTypes = map[string]attr.Type{
	"type":     types.StringType,
	"versions": types.ListType{ElemType: types.ObjectType{AttrTypes: versionItemAttrTypes}},
}

func (d *databaseTypesDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_database_types"
}

func (d *databaseTypesDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Lists all available database types and their versions for managed database instances.",
		Attributes: map[string]schema.Attribute{
			"types": schema.ListNestedAttribute{
				Description: "The list of available database types.",
				Computed:    true,
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"type": schema.StringAttribute{
							Description: "The type name (e.g. \"postgresql\", \"mysql\").",
							Computed:    true,
						},
						"versions": schema.ListNestedAttribute{
							Description: "The supported versions for this type.",
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

func (d *databaseTypesDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
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

func (d *databaseTypesDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var state databaseTypesModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	apiResp, err := d.client.Get(ctx, "/v1/databases/types", nil)
	if err != nil {
		resp.Diagnostics.AddError("Failed to list database types", err.Error())
		return
	}

	var list apiDatabaseTypeList
	if err := json.Unmarshal(apiResp.Body, &list); err != nil {
		resp.Diagnostics.AddError("Failed to parse database types response", err.Error())
		return
	}

	items := make([]typeItemModel, 0, len(list.Types))
	for _, e := range list.Types {
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

		items = append(items, typeItemModel{
			Type:     types.StringValue(e.Type),
			Versions: versionsList,
		})
	}

	typesList, diags := types.ListValueFrom(ctx, types.ObjectType{AttrTypes: typeItemAttrTypes}, items)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	state.Types = typesList
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}
