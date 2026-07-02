// Package kubernetes_versions implements the frostmoln_kubernetes_versions Terraform data source.
package kubernetes_versions

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

var _ datasource.DataSource = &kubernetesVersionsDataSource{}

// NewDataSource returns a new frostmoln_kubernetes_versions data source factory.
func NewDataSource() datasource.DataSource {
	return &kubernetesVersionsDataSource{}
}

type kubernetesVersionsDataSource struct {
	client *client.Client
}

// kubernetesVersionsModel is the Terraform state model for the Kubernetes versions list.
type kubernetesVersionsModel struct {
	Versions types.List `tfsdk:"versions"`
}

// kubernetesVersionItemModel represents a single Kubernetes version in the list.
type kubernetesVersionItemModel struct {
	ID        types.String `tfsdk:"id"`
	Version   types.String `tfsdk:"version"`
	Status    types.String `tfsdk:"status"`
	IsDefault types.Bool   `tfsdk:"is_default"`
	EndOfLife types.String `tfsdk:"end_of_life"`
}

// apiKubernetesVersion is the API representation of a Kubernetes version.
type apiKubernetesVersion struct {
	ID        string `json:"id"`
	Version   string `json:"version"`
	Status    string `json:"status"`
	IsDefault bool   `json:"isDefault"`
	EndOfLife string `json:"endOfLife,omitempty"`
}

// apiKubernetesVersionList is the API response for listing Kubernetes versions.
type apiKubernetesVersionList struct {
	Versions []apiKubernetesVersion `json:"versions"`
}

var versionItemAttrTypes = map[string]attr.Type{
	"id":          types.StringType,
	"version":     types.StringType,
	"status":      types.StringType,
	"is_default":  types.BoolType,
	"end_of_life": types.StringType,
}

func (d *kubernetesVersionsDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_kubernetes_versions"
}

func (d *kubernetesVersionsDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Lists available Kubernetes versions for managed Kubernetes clusters.",
		Attributes: map[string]schema.Attribute{
			"versions": schema.ListNestedAttribute{
				Description: "The list of available Kubernetes versions.",
				Computed:    true,
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"id": schema.StringAttribute{
							Description: "The unique identifier of the version entry.",
							Computed:    true,
						},
						"version": schema.StringAttribute{
							Description: "The Kubernetes version string (e.g. \"1.35\").",
							Computed:    true,
						},
						"status": schema.StringAttribute{
							Description: "The support status of this version (\"current\", \"supported\", or \"deprecated\").",
							Computed:    true,
						},
						"is_default": schema.BoolAttribute{
							Description: "Whether this version is the default used when a cluster is created without an explicit version.",
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

func (d *kubernetesVersionsDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
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

func (d *kubernetesVersionsDataSource) Read(ctx context.Context, _ datasource.ReadRequest, resp *datasource.ReadResponse) {
	apiResp, err := d.client.Get(ctx, "/v1/kubernetes/versions", nil)
	if err != nil {
		resp.Diagnostics.AddError("Failed to list Kubernetes versions", err.Error())
		return
	}

	var list apiKubernetesVersionList
	if err := json.Unmarshal(apiResp.Body, &list); err != nil {
		resp.Diagnostics.AddError("Failed to parse Kubernetes versions response", err.Error())
		return
	}

	items := make([]kubernetesVersionItemModel, 0, len(list.Versions))
	for _, v := range list.Versions {
		item := kubernetesVersionItemModel{
			ID:        types.StringValue(v.ID),
			Version:   types.StringValue(v.Version),
			Status:    types.StringValue(v.Status),
			IsDefault: types.BoolValue(v.IsDefault),
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

	state := kubernetesVersionsModel{Versions: versionsList}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}
