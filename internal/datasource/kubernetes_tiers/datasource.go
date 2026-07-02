// Package kubernetes_tiers implements the frostmoln_kubernetes_tiers Terraform data source.
package kubernetes_tiers

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

var _ datasource.DataSource = &kubernetesTiersDataSource{}

// NewDataSource returns a new frostmoln_kubernetes_tiers data source factory.
func NewDataSource() datasource.DataSource {
	return &kubernetesTiersDataSource{}
}

type kubernetesTiersDataSource struct {
	client *client.Client
}

// kubernetesTiersModel is the Terraform state model for the control-plane tiers list.
type kubernetesTiersModel struct {
	Tiers types.List `tfsdk:"tiers"`
}

// kubernetesTierItemModel represents a single control-plane tier in the list.
type kubernetesTierItemModel struct {
	Key               types.String `tfsdk:"key"`
	Name              types.String `tfsdk:"name"`
	ControlPlaneNodes types.Int64  `tfsdk:"control_plane_nodes"`
	HAEnabled         types.Bool   `tfsdk:"ha_enabled"`
	IsDefault         types.Bool   `tfsdk:"is_default"`
}

// apiKubernetesTier is the API representation of a control-plane tier.
type apiKubernetesTier struct {
	Key               string `json:"key"`
	Name              string `json:"name"`
	ControlPlaneNodes int    `json:"controlPlaneNodes"`
	HAEnabled         bool   `json:"haEnabled"`
	IsDefault         bool   `json:"isDefault"`
}

// apiKubernetesTierList is the API response for listing control-plane tiers.
type apiKubernetesTierList struct {
	Tiers []apiKubernetesTier `json:"tiers"`
}

var tierItemAttrTypes = map[string]attr.Type{
	"key":                 types.StringType,
	"name":                types.StringType,
	"control_plane_nodes": types.Int64Type,
	"ha_enabled":          types.BoolType,
	"is_default":          types.BoolType,
}

func (d *kubernetesTiersDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_kubernetes_tiers"
}

func (d *kubernetesTiersDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Lists available control-plane tiers for managed Kubernetes clusters.",
		Attributes: map[string]schema.Attribute{
			"tiers": schema.ListNestedAttribute{
				Description: "The list of available control-plane tiers.",
				Computed:    true,
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"key": schema.StringAttribute{
							Description: "The canonical tier key (used as `control_plane_tier` on the cluster resource).",
							Computed:    true,
						},
						"name": schema.StringAttribute{
							Description: "The human-readable tier name.",
							Computed:    true,
						},
						"control_plane_nodes": schema.Int64Attribute{
							Description: "The number of control-plane nodes in this tier.",
							Computed:    true,
						},
						"ha_enabled": schema.BoolAttribute{
							Description: "Whether this tier runs a highly available control plane.",
							Computed:    true,
						},
						"is_default": schema.BoolAttribute{
							Description: "Whether this tier is the default used when a cluster is created without an explicit tier.",
							Computed:    true,
						},
					},
				},
			},
		},
	}
}

func (d *kubernetesTiersDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
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

func (d *kubernetesTiersDataSource) Read(ctx context.Context, _ datasource.ReadRequest, resp *datasource.ReadResponse) {
	apiResp, err := d.client.Get(ctx, "/v1/kubernetes/tiers", nil)
	if err != nil {
		resp.Diagnostics.AddError("Failed to list Kubernetes control-plane tiers", err.Error())
		return
	}

	var list apiKubernetesTierList
	if err := json.Unmarshal(apiResp.Body, &list); err != nil {
		resp.Diagnostics.AddError("Failed to parse Kubernetes control-plane tiers response", err.Error())
		return
	}

	items := make([]kubernetesTierItemModel, 0, len(list.Tiers))
	for _, t := range list.Tiers {
		items = append(items, kubernetesTierItemModel{
			Key:               types.StringValue(t.Key),
			Name:              types.StringValue(t.Name),
			ControlPlaneNodes: types.Int64Value(int64(t.ControlPlaneNodes)),
			HAEnabled:         types.BoolValue(t.HAEnabled),
			IsDefault:         types.BoolValue(t.IsDefault),
		})
	}

	tiersList, diags := types.ListValueFrom(ctx, types.ObjectType{AttrTypes: tierItemAttrTypes}, items)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	state := kubernetesTiersModel{Tiers: tiersList}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}
