// Package image implements the fm_image Terraform data source.
package image

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
)

var _ datasource.DataSource = &imageDataSource{}

// NewDataSource returns a new fm_image data source factory.
func NewDataSource() datasource.DataSource {
	return &imageDataSource{}
}

type imageDataSource struct {
	client *client.Client
}

// imageModel is the Terraform state model for a single image lookup.
type imageModel struct {
	ID        types.String `tfsdk:"id"`
	Name      types.String `tfsdk:"name"`
	OSDistro  types.String `tfsdk:"os_distro"`
	OSVersion types.String `tfsdk:"os_version"`
	MinDiskGB types.Int64  `tfsdk:"min_disk_gb"`
	MinRAMMB  types.Int64  `tfsdk:"min_ram_mb"`
	Status    types.String `tfsdk:"status"`
	CreatedAt types.String `tfsdk:"created_at"`
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

func (d *imageDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_image"
}

func (d *imageDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Look up a single image by ID or name.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Description: "The unique identifier of the image. Exactly one of id or name must be specified.",
				Optional:    true,
				Computed:    true,
			},
			"name": schema.StringAttribute{
				Description: "The name of the image. Exactly one of id or name must be specified.",
				Optional:    true,
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
			"created_at": schema.StringAttribute{
				Description: "The timestamp when the image was created.",
				Computed:    true,
			},
		},
	}
}

func (d *imageDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
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

func (d *imageDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var state imageModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	idSet := !state.ID.IsNull() && !state.ID.IsUnknown()
	nameSet := !state.Name.IsNull() && !state.Name.IsUnknown()

	if !idSet && !nameSet {
		resp.Diagnostics.AddAttributeError(
			path.Root("id"),
			"Missing Attribute",
			"Exactly one of id or name must be specified.",
		)
		return
	}
	if idSet && nameSet {
		resp.Diagnostics.AddAttributeError(
			path.Root("id"),
			"Conflicting Attributes",
			"Only one of id or name may be specified, not both.",
		)
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

	var found *apiImage
	for i := range list.Images {
		img := &list.Images[i]
		if idSet && img.ID == state.ID.ValueString() {
			found = img
			break
		}
		if nameSet && img.Name == state.Name.ValueString() {
			found = img
			break
		}
	}

	if found == nil {
		if idSet {
			resp.Diagnostics.AddError("Image not found", fmt.Sprintf("No image found with ID %q.", state.ID.ValueString()))
		} else {
			resp.Diagnostics.AddError("Image not found", fmt.Sprintf("No image found with name %q.", state.Name.ValueString()))
		}
		return
	}

	state.ID = types.StringValue(found.ID)
	state.Name = types.StringValue(found.Name)
	state.OSDistro = types.StringValue(found.OSDistro)
	state.OSVersion = types.StringValue(found.OSVersion)
	state.MinDiskGB = types.Int64Value(int64(found.MinDiskGB))
	state.MinRAMMB = types.Int64Value(int64(found.MinRAMMB))
	state.Status = types.StringValue(found.Status)
	state.CreatedAt = types.StringValue(found.CreatedAt)

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}
