// Package kubernetes_flavors implements the frostmoln_kubernetes_flavors Terraform data source.
package kubernetes_flavors

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

var _ datasource.DataSource = &kubernetesFlavorsDataSource{}

// NewDataSource returns a new frostmoln_kubernetes_flavors data source factory.
func NewDataSource() datasource.DataSource {
	return &kubernetesFlavorsDataSource{}
}

type kubernetesFlavorsDataSource struct {
	client *client.Client
}

// kubernetesFlavorsModel is the Terraform state model for the node flavors list.
type kubernetesFlavorsModel struct {
	Flavors types.List `tfsdk:"flavors"`
}

// kubernetesFlavorItemModel represents a single node flavor in the list.
type kubernetesFlavorItemModel struct {
	ID          types.String `tfsdk:"id"`
	Name        types.String `tfsdk:"name"`
	Family      types.String `tfsdk:"family"`
	VCPUs       types.Int64  `tfsdk:"vcpus"`
	RAMMB       types.Int64  `tfsdk:"ram_mb"`
	DiskGB      types.Int64  `tfsdk:"disk_gb"`
	PricingTier types.String `tfsdk:"pricing_tier"`
}

// apiKubernetesFlavor is the API representation of a node flavor.
type apiKubernetesFlavor struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Family      string `json:"family"`
	VCPUs       int    `json:"vcpus"`
	RAMMB       int    `json:"ramMb"`
	DiskGB      int    `json:"diskGb"`
	PricingTier string `json:"pricingTier,omitempty"`
}

// apiKubernetesFlavorList is the API response for listing node flavors.
type apiKubernetesFlavorList struct {
	Flavors []apiKubernetesFlavor `json:"flavors"`
}

var flavorItemAttrTypes = map[string]attr.Type{
	"id":           types.StringType,
	"name":         types.StringType,
	"family":       types.StringType,
	"vcpus":        types.Int64Type,
	"ram_mb":       types.Int64Type,
	"disk_gb":      types.Int64Type,
	"pricing_tier": types.StringType,
}

func (d *kubernetesFlavorsDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_kubernetes_flavors"
}

func (d *kubernetesFlavorsDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Lists available node flavors for managed Kubernetes node pools.",
		Attributes: map[string]schema.Attribute{
			"flavors": schema.ListNestedAttribute{
				Description: "The list of available node flavors.",
				Computed:    true,
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"id": schema.StringAttribute{
							Description: "The unique identifier of the flavor (used as `flavor_id` on node pools).",
							Computed:    true,
						},
						"name": schema.StringAttribute{
							Description: "The human-readable flavor name.",
							Computed:    true,
						},
						"family": schema.StringAttribute{
							Description: "The flavor family (e.g. \"general-purpose\").",
							Computed:    true,
						},
						"vcpus": schema.Int64Attribute{
							Description: "The number of virtual CPUs per node.",
							Computed:    true,
						},
						"ram_mb": schema.Int64Attribute{
							Description: "The amount of RAM per node in MB.",
							Computed:    true,
						},
						"disk_gb": schema.Int64Attribute{
							Description: "The disk size per node in GB.",
							Computed:    true,
						},
						"pricing_tier": schema.StringAttribute{
							Description: "The billing pricing-tier key for this flavor. Prices are resolved by the billing service; the catalog never carries a price.",
							Computed:    true,
						},
					},
				},
			},
		},
	}
}

func (d *kubernetesFlavorsDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
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

func (d *kubernetesFlavorsDataSource) Read(ctx context.Context, _ datasource.ReadRequest, resp *datasource.ReadResponse) {
	apiResp, err := d.client.Get(ctx, "/v1/kubernetes/flavors", nil)
	if err != nil {
		resp.Diagnostics.AddError("Failed to list Kubernetes node flavors", err.Error())
		return
	}

	var list apiKubernetesFlavorList
	if err := json.Unmarshal(apiResp.Body, &list); err != nil {
		resp.Diagnostics.AddError("Failed to parse Kubernetes node flavors response", err.Error())
		return
	}

	items := make([]kubernetesFlavorItemModel, 0, len(list.Flavors))
	for _, f := range list.Flavors {
		item := kubernetesFlavorItemModel{
			ID:     types.StringValue(f.ID),
			Name:   types.StringValue(f.Name),
			Family: types.StringValue(f.Family),
			VCPUs:  types.Int64Value(int64(f.VCPUs)),
			RAMMB:  types.Int64Value(int64(f.RAMMB)),
			DiskGB: types.Int64Value(int64(f.DiskGB)),
		}
		if f.PricingTier != "" {
			item.PricingTier = types.StringValue(f.PricingTier)
		} else {
			item.PricingTier = types.StringNull()
		}
		items = append(items, item)
	}

	flavorsList, diags := types.ListValueFrom(ctx, types.ObjectType{AttrTypes: flavorItemAttrTypes}, items)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	state := kubernetesFlavorsModel{Flavors: flavorsList}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}
