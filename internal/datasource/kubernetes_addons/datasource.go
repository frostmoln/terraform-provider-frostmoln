// Package kubernetes_addons implements the frostmoln_kubernetes_addons Terraform data source.
package kubernetes_addons

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

var _ datasource.DataSource = &kubernetesAddonsDataSource{}

// NewDataSource returns a new frostmoln_kubernetes_addons data source factory.
func NewDataSource() datasource.DataSource {
	return &kubernetesAddonsDataSource{}
}

type kubernetesAddonsDataSource struct {
	client *client.Client
}

// kubernetesAddonsModel is the Terraform state model for the Kubernetes addons list.
type kubernetesAddonsModel struct {
	Addons types.List `tfsdk:"addons"`
}

// kubernetesAddonItemModel represents a single cluster addon in the catalog.
type kubernetesAddonItemModel struct {
	Key         types.String `tfsdk:"key"`
	Name        types.String `tfsdk:"name"`
	Description types.String `tfsdk:"description"`
	IsDefault   types.Bool   `tfsdk:"is_default"`
	Disabled    types.Bool   `tfsdk:"disabled"`
}

// apiKubernetesAddon is the API representation of a cluster-addon catalog entry.
type apiKubernetesAddon struct {
	Key         string `json:"key"`
	Name        string `json:"name"`
	Description string `json:"description"`
	IsDefault   bool   `json:"isDefault"`
	Disabled    bool   `json:"disabled"`
}

// apiKubernetesAddonList is the API response for listing cluster addons.
type apiKubernetesAddonList struct {
	Addons []apiKubernetesAddon `json:"addons"`
}

var addonItemAttrTypes = map[string]attr.Type{
	"key":         types.StringType,
	"name":        types.StringType,
	"description": types.StringType,
	"is_default":  types.BoolType,
	"disabled":    types.BoolType,
}

func (d *kubernetesAddonsDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_kubernetes_addons"
}

func (d *kubernetesAddonsDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Lists the available cluster addons that can be installed at Kubernetes cluster creation " +
			"(via the addons attribute of frostmoln_kubernetes_cluster).",
		Attributes: map[string]schema.Attribute{
			"addons": schema.ListNestedAttribute{
				Description: "The list of available cluster addons.",
				Computed:    true,
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"key": schema.StringAttribute{
							Description: "The addon's catalog key (the value used in the cluster's addons attribute, " +
								"e.g. \"external-secrets\").",
							Computed: true,
						},
						"name": schema.StringAttribute{
							Description: "The human-readable name of the addon.",
							Computed:    true,
						},
						"description": schema.StringAttribute{
							Description: "A description of what the addon provides.",
							Computed:    true,
						},
						"is_default": schema.BoolAttribute{
							Description: "Whether this addon is installed by default when a cluster is created without an explicit addons set.",
							Computed:    true,
						},
						"disabled": schema.BoolAttribute{
							Description: "Whether this addon is currently disabled (not installable). Disabled addons are rejected on cluster create.",
							Computed:    true,
						},
					},
				},
			},
		},
	}
}

func (d *kubernetesAddonsDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
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

func (d *kubernetesAddonsDataSource) Read(ctx context.Context, _ datasource.ReadRequest, resp *datasource.ReadResponse) {
	apiResp, err := d.client.Get(ctx, "/v1/kubernetes/addons", nil)
	if err != nil {
		resp.Diagnostics.AddError("Failed to list Kubernetes addons", err.Error())
		return
	}

	var list apiKubernetesAddonList
	if err := json.Unmarshal(apiResp.Body, &list); err != nil {
		resp.Diagnostics.AddError("Failed to parse Kubernetes addons response", err.Error())
		return
	}

	items := make([]kubernetesAddonItemModel, 0, len(list.Addons))
	for _, a := range list.Addons {
		items = append(items, kubernetesAddonItemModel{
			Key:         types.StringValue(a.Key),
			Name:        types.StringValue(a.Name),
			Description: types.StringValue(a.Description),
			IsDefault:   types.BoolValue(a.IsDefault),
			Disabled:    types.BoolValue(a.Disabled),
		})
	}

	addonsList, diags := types.ListValueFrom(ctx, types.ObjectType{AttrTypes: addonItemAttrTypes}, items)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	state := kubernetesAddonsModel{Addons: addonsList}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}
