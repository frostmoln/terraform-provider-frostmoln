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
	// Null when the API omits them. For the limit that is four causes with one
	// shape: disabled, no cap reported, a cap of exactly 0 that refuses every
	// push, or a failed quota read that nothing repairs. Null means "not
	// reported" — never "unlimited" and never zero.
	//
	// Disabled is a cause HERE and not on the resource, which removes a disabled
	// registry from state rather than nulling its attributes. A 0 and an
	// unlimited -1 both arrive as null rather than as a value — storageAttrs
	// nulls any non-positive before it reaches state.
	StorageLimitBytes types.Int64 `tfsdk:"storage_limit_bytes"`
	StorageUsedBytes  types.Int64 `tfsdk:"storage_used_bytes"`
}

// apiRegistrySettings mirrors the resource's decoding, including the pointer on
// Enabled: an absent field must not read as "no".
type apiRegistrySettings struct {
	Enabled           *bool  `json:"enabled"`
	Endpoint          string `json:"endpoint"`
	Namespace         string `json:"namespace,omitempty"`
	StorageLimitBytes int64  `json:"storageLimitBytes,omitempty"`
	StorageUsedBytes  int64  `json:"storageUsedBytes,omitempty"`
}

// storageAttrs maps the reported storage numbers onto Terraform values, with a
// non-positive value becoming NULL rather than 0 — the API omits a cap it cannot
// report, and 0 would read as "this registry may hold nothing". Mirrors the
// resource's helper of the same name; the two decode the same body.
func storageAttrs(s *apiRegistrySettings) (limit, used types.Int64) {
	limit, used = types.Int64Null(), types.Int64Null()
	if s.StorageLimitBytes > 0 {
		limit = types.Int64Value(s.StorageLimitBytes)
		// Legitimately zero on a fresh registry, so it is reported whenever a cap
		// was — zero included.
		used = types.Int64Value(s.StorageUsedBytes)
	}
	return limit, used
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
			"storage_limit_bytes": schema.Int64Attribute{
				Description: "The hard storage cap enforced on this namespace, in bytes. A push that would cross it is refused with a 403 whose message states the cap, the usage and the size that would not fit — as prose, and only once the push has failed. This attribute is the machine-readable form of the cap. Null for four different reasons with the same result: the registry is not enabled; the registry reports no cap at all; the registry reports a hard cap of exactly 0, which refuses every push and is the one null that is worse than it looks, not better; or the quota read itself failed. The platform re-checks and resets the cap on a later sweep, which repairs the middle two; it never repairs a failed read, so that one can persist for as long as the outage lasts and the value can go null and back between reads with no configuration change. A cap of 0 and an unlimited -1 both arrive as null rather than as a value, so null — not a zero or negative number — is the only state to check for. Null means the limit was not reported, and never that it is unlimited.",
				Computed:    true,
			},
			"storage_used_bytes": schema.Int64Attribute{
				Description: "Storage currently counted against that cap, in bytes. OBSERVATIONAL: it changes with every push, so expect it to differ on each read. It counts deduplicated stored bytes as the registry measures them, which does not equal the sum of your local image sizes — do not use it for billing reconciliation.",
				Computed:    true,
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
	state.StorageLimitBytes, state.StorageUsedBytes = storageAttrs(settings)

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}
