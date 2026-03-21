// Package mysql_versions implements the frostmoln_mysql_versions Terraform data source.
package mysql_versions

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
)

var _ datasource.DataSource = &mysqlVersionsDataSource{}

// NewDataSource returns a new frostmoln_mysql_versions data source factory.
func NewDataSource() datasource.DataSource {
	return &mysqlVersionsDataSource{}
}

type mysqlVersionsDataSource struct {
	client *client.Client
}

// mysqlVersionsModel is the Terraform state model for the MySQL versions list.
type mysqlVersionsModel struct {
	Versions types.List `tfsdk:"versions"`
}

// mysqlVersionItemModel represents a single MySQL version in the list.
type mysqlVersionItemModel struct {
	Version   types.String `tfsdk:"version"`
	Status    types.String `tfsdk:"status"`
	EndOfLife types.String `tfsdk:"end_of_life"`
}

// apiMysqlVersion is the API representation of a MySQL version.
type apiMysqlVersion struct {
	Version   string `json:"version"`
	Status    string `json:"status"`
	EndOfLife string `json:"endOfLife,omitempty"`
}

// apiMysqlVersionList is the API response for listing MySQL versions.
type apiMysqlVersionList struct {
	Versions []apiMysqlVersion `json:"versions"`
}

var versionItemAttrTypes = map[string]attr.Type{
	"version":     types.StringType,
	"status":      types.StringType,
	"end_of_life": types.StringType,
}

func (d *mysqlVersionsDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_mysql_versions"
}

func (d *mysqlVersionsDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Lists available MySQL versions for managed database instances.",
		Attributes: map[string]schema.Attribute{
			"versions": schema.ListNestedAttribute{
				Description: "The list of available MySQL versions.",
				Computed:    true,
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"version": schema.StringAttribute{
							Description: "The MySQL version string (e.g. \"8.0\", \"8.4\", \"9.2\").",
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

func (d *mysqlVersionsDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
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

func (d *mysqlVersionsDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var state mysqlVersionsModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	query := url.Values{}
	query.Set("engine", "mysql")

	apiResp, err := d.client.Get(ctx, "/v1/databases/versions", query)
	if err != nil {
		resp.Diagnostics.AddError("Failed to list MySQL versions", err.Error())
		return
	}

	var list apiMysqlVersionList
	if err := json.Unmarshal(apiResp.Body, &list); err != nil {
		resp.Diagnostics.AddError("Failed to parse MySQL versions response", err.Error())
		return
	}

	var items []mysqlVersionItemModel
	for _, v := range list.Versions {
		item := mysqlVersionItemModel{
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
