// Package kubernetes_cluster_addons implements the frostmoln_kubernetes_cluster_addons Terraform data source.
package kubernetes_cluster_addons

import (
	"context"
	"fmt"
	"net/http"
	"net/url"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
)

var _ datasource.DataSource = &clusterAddonsDataSource{}

// NewDataSource returns a new frostmoln_kubernetes_cluster_addons data source factory.
func NewDataSource() datasource.DataSource {
	return &clusterAddonsDataSource{}
}

type clusterAddonsDataSource struct {
	client *client.Client
}

type clusterAddonsModel struct {
	ClusterID types.String `tfsdk:"cluster_id"`
	Addons    types.List   `tfsdk:"addons"`
}

type clusterAddonItemModel struct {
	Key            types.String `tfsdk:"key"`
	PinnedVersion  types.String `tfsdk:"pinned_version"`
	AppliedVersion types.String `tfsdk:"applied_version"`
	State          types.String `tfsdk:"state"`
	PinnedStatus   types.String `tfsdk:"pinned_status"`
}

type apiClusterAddonState struct {
	Key            string `json:"key"`
	PinnedVersion  string `json:"pinnedVersion"`
	AppliedVersion string `json:"appliedVersion"`
	State          string `json:"state"`
	PinnedStatus   string `json:"pinnedStatus"`
}

type apiClusterAddonStateList struct {
	Addons []apiClusterAddonState `json:"addons"`
}

var addonStateAttrTypes = map[string]attr.Type{
	"key":             types.StringType,
	"pinned_version":  types.StringType,
	"applied_version": types.StringType,
	"state":           types.StringType,
	"pinned_status":   types.StringType,
}

// noVersionData maps the statuses that mean "the platform has no version answer for
// this cluster right now" to the reason shown. They are warnings, not errors: a
// configuration reading this beside a cluster being created must still plan.
var noVersionData = map[int]string{
	http.StatusBadRequest:         "This cluster's control plane does not record addon versions (a legacy control plane whose addons were fixed at create).",
	http.StatusConflict:           "The cluster has no recorded addon state yet; it is recorded once the cluster finishes being created.",
	http.StatusNotImplemented:     "A platform deployment is in progress; addon versions are available again once it finishes.",
	http.StatusServiceUnavailable: "The platform could not be reached, so no version is reported rather than a guessed one.",
}

func (d *clusterAddonsDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_kubernetes_cluster_addons"
}

func (d *clusterAddonsDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Reads, per addon of one Kubernetes cluster, the version it is pinned to beside the version the " +
			"platform last confirmed it applied. This is the platform's record, not a read inside the cluster. " +
			"When no version data is available (a cluster still being created, a legacy control plane, a " +
			"deployment in progress or the platform unreachable) the read succeeds with a warning and addons is null.",
		Attributes: map[string]schema.Attribute{
			"cluster_id": schema.StringAttribute{
				Description: "The ID of the Kubernetes cluster.",
				Required:    true,
				// The ID is a URL path segment: "." and ".." would path-join to another
				// route instead of naming a cluster.
				Validators: []validator.String{
					stringvalidator.LengthAtLeast(1),
					stringvalidator.NoneOf(".", ".."),
				},
			},
			"addons": schema.ListNestedAttribute{
				Description: "The cluster's addons in the platform's order: its current selection first, then any " +
					"addon it still owes a removal for. Never sorted.",
				Computed: true,
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"key": schema.StringAttribute{
							Description: "The addon's catalog key.",
							Computed:    true,
						},
						"pinned_version": schema.StringAttribute{
							Description: "The version the cluster is pinned to. Empty for an addon with nothing to pin.",
							Computed:    true,
						},
						"applied_version": schema.StringAttribute{
							Description: "The version the platform last confirmed it applied. It lags pinned_version " +
								"while a change converges, and is empty when nothing is confirmed yet.",
							Computed: true,
						},
						"state": schema.StringAttribute{
							Description: "How the pin and the applied version relate (for example converged, " +
								"converging, removing or unsupported). New values may appear.",
							Computed: true,
						},
						"pinned_status": schema.StringAttribute{
							Description: "The lifecycle status of pinned_version now (for example current, " +
								"supported, deprecated or eol) — how a pin that went stale is noticed. Empty when " +
								"there is no pin or the version is no longer published. New values may appear.",
							Computed: true,
						},
					},
				},
			},
		},
	}
}

func (d *clusterAddonsDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
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

func (d *clusterAddonsDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var state clusterAddonsModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	objType := types.ObjectType{AttrTypes: addonStateAttrTypes}

	id := state.ClusterID.ValueString()
	apiResp, err := d.client.Get(ctx, d.client.TenantPath("/kubernetes-clusters/"+url.PathEscape(id)+"/addons"), nil)
	if err != nil {
		// Never synthesized from the cluster's `addons` list: that records a request
		// and carries no version at all.
		if apiErr, ok := err.(*client.APIError); ok {
			if reason, known := noVersionData[apiErr.StatusCode]; known {
				resp.Diagnostics.AddWarning("No addon version data for Kubernetes cluster",
					fmt.Sprintf("%s\n\nPlatform response: %s", reason, err))
				state.Addons = types.ListNull(objType)
				resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
				return
			}
		}
		resp.Diagnostics.AddError("Failed to read Kubernetes cluster addons", err.Error())
		return
	}
	list, err := client.ParseResponse[apiClusterAddonStateList](apiResp)
	if err != nil {
		resp.Diagnostics.AddError("Failed to parse Kubernetes cluster addons response", err.Error())
		return
	}

	// ponytail: platform order and open vocabularies, both verbatim — never sorted, never validated.
	items := make([]clusterAddonItemModel, 0, len(list.Addons))
	for _, a := range list.Addons {
		items = append(items, clusterAddonItemModel{
			Key:            types.StringValue(a.Key),
			PinnedVersion:  types.StringValue(a.PinnedVersion),
			AppliedVersion: types.StringValue(a.AppliedVersion),
			State:          types.StringValue(a.State),
			PinnedStatus:   types.StringValue(a.PinnedStatus),
		})
	}
	addons, diags := types.ListValueFrom(ctx, objType, items)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	state.Addons = addons
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}
