// Package flavor implements the fm_flavor Terraform data source.
package flavor

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

var _ datasource.DataSource = &flavorDataSource{}

// NewDataSource returns a new fm_flavor data source factory.
func NewDataSource() datasource.DataSource {
	return &flavorDataSource{}
}

type flavorDataSource struct {
	client *client.Client
}

// flavorModel is the Terraform state model for a single flavor lookup.
type flavorModel struct {
	ID             types.String  `tfsdk:"id"`
	Name           types.String  `tfsdk:"name"`
	VCPUs          types.Int64   `tfsdk:"vcpus"`
	RAMMB          types.Int64   `tfsdk:"ram_mb"`
	DiskGB         types.Int64   `tfsdk:"disk_gb"`
	Category       types.String  `tfsdk:"category"`
	Family         types.String  `tfsdk:"family"`
	Generation     types.Int64   `tfsdk:"generation"`
	Status         types.String  `tfsdk:"status"`
	SuccessorID    types.String  `tfsdk:"successor_id"`
	BaseVCPURatio  types.Float64 `tfsdk:"base_vcpu_ratio"`
	VCPUMultiplier types.Float64 `tfsdk:"vcpu_multiplier"`
}

// apiFlavor is the API representation of a flavor.
type apiFlavor struct {
	ID             string  `json:"id"`
	Name           string  `json:"name"`
	VCPUs          int     `json:"vcpus"`
	RAMMB          int     `json:"ramMb"`
	DiskGB         int     `json:"diskGb"`
	Category       string  `json:"category,omitempty"`
	Family         string  `json:"family"`
	Generation     int     `json:"generation"`
	Status         string  `json:"status"`
	SuccessorID    string  `json:"successorId"`
	BaseVCPURatio  float64 `json:"baseVcpuRatio"`
	VCPUMultiplier float64 `json:"vcpuMultiplier"`
}

// apiFlavorList is the API response for listing flavors.
type apiFlavorList struct {
	Flavors []apiFlavor `json:"flavors"`
}

func (d *flavorDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_flavor"
}

func (d *flavorDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Look up a single flavor by ID or name.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Description: "The unique identifier of the flavor. Exactly one of id or name must be specified.",
				Optional:    true,
				Computed:    true,
			},
			"name": schema.StringAttribute{
				Description: "The name of the flavor. Exactly one of id or name must be specified.",
				Optional:    true,
				Computed:    true,
			},
			"vcpus": schema.Int64Attribute{
				Description: "The number of virtual CPUs.",
				Computed:    true,
			},
			"ram_mb": schema.Int64Attribute{
				Description: "The amount of RAM in MB.",
				Computed:    true,
			},
			"disk_gb": schema.Int64Attribute{
				Description: "The disk size in GB.",
				Computed:    true,
			},
			"category": schema.StringAttribute{
				Description: "The category of the flavor.",
				Computed:    true,
			},
			"family": schema.StringAttribute{
				Optional:    true,
				Computed:    true,
				Description: "Filter by or computed flavor family (e.g. gp, co, mo, so)",
			},
			"generation": schema.Int64Attribute{
				Computed:    true,
				Description: "Flavor generation number",
			},
			"status": schema.StringAttribute{
				Optional:    true,
				Computed:    true,
				Description: "Filter by or computed flavor lifecycle status (preview, active, deprecated, retired)",
			},
			"successor_id": schema.StringAttribute{
				Computed:    true,
				Description: "ID of the successor flavor when deprecated or retired",
			},
			"base_vcpu_ratio": schema.Float64Attribute{
				Computed:    true,
				Description: "Base vCPU to physical CPU ratio",
			},
			"vcpu_multiplier": schema.Float64Attribute{
				Computed:    true,
				Description: "vCPU multiplier relative to base ratio",
			},
		},
	}
}

func (d *flavorDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
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

func (d *flavorDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var state flavorModel
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

	apiResp, err := d.client.Get(ctx, "/v1/flavors", nil)
	if err != nil {
		resp.Diagnostics.AddError("Failed to list flavors", err.Error())
		return
	}

	var list apiFlavorList
	if err := json.Unmarshal(apiResp.Body, &list); err != nil {
		resp.Diagnostics.AddError("Failed to parse flavors response", err.Error())
		return
	}

	var found *apiFlavor
	for i := range list.Flavors {
		f := &list.Flavors[i]
		if idSet && f.ID == state.ID.ValueString() {
			found = f
			break
		}
		if nameSet && f.Name == state.Name.ValueString() {
			found = f
			break
		}
	}

	if found == nil {
		if idSet {
			resp.Diagnostics.AddError("Flavor not found", fmt.Sprintf("No flavor found with ID %q.", state.ID.ValueString()))
		} else {
			resp.Diagnostics.AddError("Flavor not found", fmt.Sprintf("No flavor found with name %q.", state.Name.ValueString()))
		}
		return
	}

	state.ID = types.StringValue(found.ID)
	state.Name = types.StringValue(found.Name)
	state.VCPUs = types.Int64Value(int64(found.VCPUs))
	state.RAMMB = types.Int64Value(int64(found.RAMMB))
	state.DiskGB = types.Int64Value(int64(found.DiskGB))
	state.Category = types.StringValue(found.Category)
	state.Family = types.StringValue(found.Family)
	state.Generation = types.Int64Value(int64(found.Generation))
	state.Status = types.StringValue(found.Status)
	state.SuccessorID = types.StringValue(found.SuccessorID)
	state.BaseVCPURatio = types.Float64Value(found.BaseVCPURatio)
	state.VCPUMultiplier = types.Float64Value(found.VCPUMultiplier)

	if found.Status == "deprecated" {
		resp.Diagnostics.AddWarning(
			"Deprecated Flavor",
			fmt.Sprintf("Flavor %q is deprecated. Consider migrating to successor: %s", found.Name, found.SuccessorID),
		)
	}
	if found.Status == "retired" {
		resp.Diagnostics.AddError(
			"Retired Flavor",
			fmt.Sprintf("Flavor %q is retired and cannot be used for new instances.", found.Name),
		)
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}
