// Package postgres_versions implements the frostmoln_postgres_versions Terraform data source.
package postgres_versions

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

var _ datasource.DataSource = &postgresVersionsDataSource{}

// NewDataSource returns a new frostmoln_postgres_versions data source factory.
func NewDataSource() datasource.DataSource {
	return &postgresVersionsDataSource{}
}

type postgresVersionsDataSource struct {
	client *client.Client
}

// postgresVersionsModel is the Terraform state model for the postgres versions list.
type postgresVersionsModel struct {
	Versions types.List `tfsdk:"versions"`
}

// postgresVersionItemModel represents a single PostgreSQL version in the list.
type postgresVersionItemModel struct {
	Version   types.String `tfsdk:"version"`
	Status    types.String `tfsdk:"status"`
	EndOfLife types.String `tfsdk:"end_of_life"`
}

// apiPostgresVersion is the API representation of a PostgreSQL version.
type apiPostgresVersion struct {
	Version   string `json:"version"`
	Status    string `json:"status"`
	EndOfLife string `json:"endOfLife,omitempty"`
}

// apiPostgresVersionList is the API response for listing PostgreSQL versions.
type apiPostgresVersionList struct {
	Versions []apiPostgresVersion `json:"versions"`
}

var versionItemAttrTypes = map[string]attr.Type{
	"version":     types.StringType,
	"status":      types.StringType,
	"end_of_life": types.StringType,
}

func (d *postgresVersionsDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_postgres_versions"
}

func (d *postgresVersionsDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Lists available PostgreSQL versions for managed database instances.",
		Attributes: map[string]schema.Attribute{
			"versions": schema.ListNestedAttribute{
				Description: "The list of available PostgreSQL versions.",
				Computed:    true,
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"version": schema.StringAttribute{
							Description: "The PostgreSQL version string (e.g. \"15\", \"16\").",
							Computed:    true,
						},
						"status": schema.StringAttribute{
							Description: "The support status of this version (e.g. \"supported\", \"deprecated\").",
							Computed:    true,
						},
						"end_of_life": schema.StringAttribute{
							Description: "The end-of-life date for this version, if known.",
							Computed:    true,
						},
					},
				},
			},
		},
	}
}

func (d *postgresVersionsDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
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

func (d *postgresVersionsDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var state postgresVersionsModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	apiResp, err := d.client.Get(ctx, "/v1/databases/versions", nil)
	if err != nil {
		resp.Diagnostics.AddError("Failed to list PostgreSQL versions", err.Error())
		return
	}

	var list apiPostgresVersionList
	if err := json.Unmarshal(apiResp.Body, &list); err != nil {
		resp.Diagnostics.AddError("Failed to parse PostgreSQL versions response", err.Error())
		return
	}

	var items []postgresVersionItemModel
	for _, v := range list.Versions {
		item := postgresVersionItemModel{
			Version: types.StringValue(v.Version),
			Status:  types.StringValue(v.Status),
		}
		if v.EndOfLife != "" {
			item.EndOfLife = types.StringValue(v.EndOfLife)
		} else {
			item.EndOfLife = types.StringNull()
		}
		items = append(items, item)
	}

	versionsList, diags := types.ListValueFrom(ctx, types.ObjectType{AttrTypes: versionItemAttrTypes}, items)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	state.Versions = versionsList
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}
