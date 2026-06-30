// Package images implements the fm_images Terraform data source.
package images

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
)

var _ datasource.DataSource = &imagesDataSource{}

// NewDataSource returns a new fm_images data source factory.
func NewDataSource() datasource.DataSource {
	return &imagesDataSource{}
}

type imagesDataSource struct {
	client *client.Client
}

// imagesModel is the Terraform state model for the images list.
type imagesModel struct {
	OSDistro  types.String `tfsdk:"os_distro"`
	NameRegex types.String `tfsdk:"name_regex"`
	Images    types.List   `tfsdk:"images"`
}

// imageItemModel represents a single image in the list.
type imageItemModel struct {
	ID        types.String `tfsdk:"id"`
	Name      types.String `tfsdk:"name"`
	OSDistro  types.String `tfsdk:"os_distro"`
	OSVersion types.String `tfsdk:"os_version"`
	MinDiskGB types.Int64  `tfsdk:"min_disk_gb"`
	MinRAMMB  types.Int64  `tfsdk:"min_ram_mb"`
	Status    types.String `tfsdk:"status"`
}

// apiImage is the API representation of an image.
type apiImage struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	OSDistro  string `json:"osDistro,omitempty"`
	OSVersion string `json:"osVersion,omitempty"`
	MinDiskGB int    `json:"minDisk"`
	MinRAMMB  int    `json:"minRam"`
	Status    string `json:"status"`
	CreatedAt string `json:"createdAt"`
}

// apiImageList is the API response for listing images.
type apiImageList struct {
	Images []apiImage `json:"images"`
}

var imageItemAttrTypes = map[string]attr.Type{
	"id":          types.StringType,
	"name":        types.StringType,
	"os_distro":   types.StringType,
	"os_version":  types.StringType,
	"min_disk_gb": types.Int64Type,
	"min_ram_mb":  types.Int64Type,
	"status":      types.StringType,
}

func (d *imagesDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_images"
}

func (d *imagesDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "List images with optional filters.",
		Attributes: map[string]schema.Attribute{
			"os_distro": schema.StringAttribute{
				Description: "Filter images by OS distribution.",
				Optional:    true,
			},
			"name_regex": schema.StringAttribute{
				Description: "Filter images by a regex pattern on the name.",
				Optional:    true,
			},
			"images": schema.ListNestedAttribute{
				Description: "The list of images matching the filters.",
				Computed:    true,
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"id": schema.StringAttribute{
							Description: "The unique identifier of the image.",
							Computed:    true,
						},
						"name": schema.StringAttribute{
							Description: "The name of the image.",
							Computed:    true,
						},
						"os_distro": schema.StringAttribute{
							Description: "The OS distribution of the image.",
							Computed:    true,
						},
						"os_version": schema.StringAttribute{
							Description: "The OS version of the image.",
							Computed:    true,
						},
						"min_disk_gb": schema.Int64Attribute{
							Description: "Minimum disk size in GB required by the image.",
							Computed:    true,
						},
						"min_ram_mb": schema.Int64Attribute{
							Description: "Minimum RAM in MB required by the image.",
							Computed:    true,
						},
						"status": schema.StringAttribute{
							Description: "The status of the image.",
							Computed:    true,
						},
					},
				},
			},
		},
	}
}

func (d *imagesDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
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

func (d *imagesDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var state imagesModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	apiResp, err := d.client.Get(ctx, "/v1/images", nil)
	if err != nil {
		resp.Diagnostics.AddError("Failed to list images", err.Error())
		return
	}

	var list apiImageList
	if err := json.Unmarshal(apiResp.Body, &list); err != nil {
		resp.Diagnostics.AddError("Failed to parse images response", err.Error())
		return
	}

	// Compile name regex if provided
	var nameRe *regexp.Regexp
	if !state.NameRegex.IsNull() && !state.NameRegex.IsUnknown() {
		var compileErr error
		nameRe, compileErr = regexp.Compile(state.NameRegex.ValueString())
		if compileErr != nil {
			resp.Diagnostics.AddError("Invalid name_regex", compileErr.Error())
			return
		}
	}

	osDistroFilter := ""
	if !state.OSDistro.IsNull() && !state.OSDistro.IsUnknown() {
		osDistroFilter = state.OSDistro.ValueString()
	}

	var items []imageItemModel
	for _, img := range list.Images {
		if osDistroFilter != "" && img.OSDistro != osDistroFilter {
			continue
		}
		if nameRe != nil && !nameRe.MatchString(img.Name) {
			continue
		}
		items = append(items, imageItemModel{
			ID:        types.StringValue(img.ID),
			Name:      types.StringValue(img.Name),
			OSDistro:  types.StringValue(img.OSDistro),
			OSVersion: types.StringValue(img.OSVersion),
			MinDiskGB: types.Int64Value(int64(img.MinDiskGB)),
			MinRAMMB:  types.Int64Value(int64(img.MinRAMMB)),
			Status:    types.StringValue(img.Status),
		})
	}

	imagesList, diags := types.ListValueFrom(ctx, types.ObjectType{AttrTypes: imageItemAttrTypes}, items)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	state.Images = imagesList
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}
