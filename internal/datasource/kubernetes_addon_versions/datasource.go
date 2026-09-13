// Package kubernetes_addon_versions implements the frostmoln_kubernetes_addon_versions Terraform data source.
package kubernetes_addon_versions

import (
	"context"
	"fmt"
	"net/url"
	"regexp"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
)

// addonKeyRe is the API's addon path parameter grammar.
var addonKeyRe = regexp.MustCompile(`^[a-z0-9-]{1,64}$`)

var _ datasource.DataSource = &addonVersionsDataSource{}

// NewDataSource returns a new frostmoln_kubernetes_addon_versions data source factory.
func NewDataSource() datasource.DataSource {
	return &addonVersionsDataSource{}
}

type addonVersionsDataSource struct {
	client *client.Client
}

type addonVersionsModel struct {
	Addon    types.String `tfsdk:"addon"`
	Versions types.List   `tfsdk:"versions"`
}

type addonVersionItemModel struct {
	Version   types.String `tfsdk:"version"`
	Status    types.String `tfsdk:"status"`
	IsDefault types.Bool   `tfsdk:"is_default"`
}

type apiAddonVersion struct {
	Version   string `json:"version"`
	Status    string `json:"status"`
	IsDefault bool   `json:"isDefault"`
}

type apiAddonVersionList struct {
	Versions []apiAddonVersion `json:"versions"`
}

var versionAttrTypes = map[string]attr.Type{
	"version":    types.StringType,
	"status":     types.StringType,
	"is_default": types.BoolType,
}

func (d *addonVersionsDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_kubernetes_addon_versions"
}

func (d *addonVersionsDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Lists the versions of one cluster addon that can be pinned with the addon_versions attribute " +
			"of frostmoln_kubernetes_cluster. Versions that reached end of life are not listed; a cluster " +
			"already pinned to one reports it through the frostmoln_kubernetes_cluster_addons data source.",
		Attributes: map[string]schema.Attribute{
			"addon": schema.StringAttribute{
				Description: "The addon's catalog key (see the frostmoln_kubernetes_addons data source).",
				Required:    true,
				// The key is a URL path segment: ".." would path-join to another route
				// and silently list something that is not an addon's versions.
				Validators: []validator.String{
					stringvalidator.RegexMatches(addonKeyRe, "must be an addon catalog key: 1-64 lowercase letters, digits or hyphens"),
				},
			},
			"versions": schema.ListNestedAttribute{
				Description: "The pinnable versions, in the platform's order (newest-published last). Version " +
					"strings are opaque: do not sort or compare them — position is not recency, and a semver " +
					"sort misorders prereleases. Take the recommended version from is_default.",
				Computed: true,
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"version": schema.StringAttribute{
							Description: "The version string, to be used verbatim in addon_versions.",
							Computed:    true,
						},
						"status": schema.StringAttribute{
							Description: "The version's lifecycle status (for example current, supported or " +
								"deprecated). New values may appear.",
							Computed: true,
						},
						"is_default": schema.BoolAttribute{
							Description: "Whether this is the recommended version, which an addon selected without a pin gets.",
							Computed:    true,
						},
					},
				},
			},
		},
	}
}

func (d *addonVersionsDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
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

func (d *addonVersionsDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var state addonVersionsModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	addon := state.Addon.ValueString()
	apiResp, err := d.client.Get(ctx, "/v1/kubernetes/addons/"+url.PathEscape(addon)+"/versions", nil)
	if err != nil {
		resp.Diagnostics.AddError(fmt.Sprintf("Failed to list versions of Kubernetes addon %q", addon), err.Error())
		return
	}
	list, err := client.ParseResponse[apiAddonVersionList](apiResp)
	if err != nil {
		resp.Diagnostics.AddError("Failed to parse Kubernetes addon versions response", err.Error())
		return
	}

	// ponytail: server order, verbatim — the order IS the contract, never sort.
	items := make([]addonVersionItemModel, 0, len(list.Versions))
	for _, v := range list.Versions {
		items = append(items, addonVersionItemModel{
			Version:   types.StringValue(v.Version),
			Status:    types.StringValue(v.Status),
			IsDefault: types.BoolValue(v.IsDefault),
		})
	}
	versions, diags := types.ListValueFrom(ctx, types.ObjectType{AttrTypes: versionAttrTypes}, items)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	state.Versions = versions
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}
