// Package container_registry implements the frostmoln_container_registry data
// source — a READ-ONLY view of the tenant's registry.
//
// It exists because the resource of the same name is the only other way to learn
// `endpoint` and `namespace`, and using it has a SIDE EFFECT: against a tenant
// that has not opted in, it creates the registry and the billable state that
// comes with it. A module that merely consumes a registry someone else owns —
// created in the portal, by `fm registry enable`, or in another Terraform state
// — needs a way to read it that cannot create one.
package container_registry

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
)

var _ datasource.DataSource = &containerRegistryDataSource{}

// settingsPath is the tenant-scoped route. Two-segment (`/registry/settings`),
// never a bare `/registry`: the api-gateway feature gate matches on an
// unanchored substring.
const settingsPath = "/registry/settings"

// NewDataSource returns a new frostmoln_container_registry data source factory.
func NewDataSource() datasource.DataSource {
	return &containerRegistryDataSource{}
}

type containerRegistryDataSource struct {
	client *client.Client
}

type containerRegistryModel struct {
	ID        types.String `tfsdk:"id"`
	Enabled   types.Bool   `tfsdk:"enabled"`
	Endpoint  types.String `tfsdk:"endpoint"`
	Namespace types.String `tfsdk:"namespace"`
}

// apiRegistrySettings mirrors the resource's decoding, including the pointer on
// Enabled: an absent field must not read as "no".
type apiRegistrySettings struct {
	Enabled   *bool  `json:"enabled"`
	Endpoint  string `json:"endpoint"`
	Namespace string `json:"namespace,omitempty"`
}

func (d *containerRegistryDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_container_registry"
}

func (d *containerRegistryDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Reads this tenant's container registry WITHOUT creating one.\n\n" +
			"Use this when the registry is managed elsewhere — the portal, `fm registry enable`, or " +
			"another Terraform state — and this configuration only needs its `endpoint` and " +
			"`namespace`. The `frostmoln_container_registry` RESOURCE would opt the tenant in and " +
			"start its billable storage; this never writes anything.\n\n" +
			"For a tenant that has not opted in, `enabled` is false and `namespace` is null. That is " +
			"the answer, not an error — check `enabled` before building an image reference.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Description: "The tenant id. A tenant has exactly one registry and its id is the tenant's.",
				Computed:    true,
			},
			"enabled": schema.BoolAttribute{
				Description: "Whether this tenant has opted in.",
				Computed:    true,
			},
			"endpoint": schema.StringAttribute{
				Description: "The host to `docker login` to. Present whether or not the registry is enabled.",
				Computed:    true,
			},
			"namespace": schema.StringAttribute{
				Description: "The tenant's namespace at that endpoint — the first path segment of every " +
					"image reference. Null while the registry is not enabled, because advertising a pull " +
					"path that 404s reads as a working registry.",
				Computed: true,
			},
		},
	}
}

func (d *containerRegistryDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
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

func (d *containerRegistryDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var state containerRegistryModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	apiResp, err := d.client.Get(ctx, d.client.TenantPath(settingsPath), nil)
	if err != nil {
		resp.Diagnostics.AddError("Failed to read the container registry", err.Error())
		return
	}

	settings, err := client.ParseResponse[apiRegistrySettings](apiResp)
	if err != nil {
		resp.Diagnostics.AddError("Failed to parse the container registry response", err.Error())
		return
	}
	if settings.Enabled == nil {
		resp.Diagnostics.AddError(
			"The container registry response did not say whether the registry is enabled",
			"The API answered without an `enabled` field, which the contract requires.",
		)
		return
	}

	state.ID = types.StringValue(d.client.TenantID())
	state.Enabled = types.BoolValue(*settings.Enabled)
	state.Endpoint = types.StringValue(settings.Endpoint)
	if settings.Namespace == "" {
		state.Namespace = types.StringNull()
	} else {
		state.Namespace = types.StringValue(settings.Namespace)
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}
