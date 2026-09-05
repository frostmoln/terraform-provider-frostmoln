// Package container_registry_cache_upstreams implements the
// frostmoln_container_registry_cache_upstreams Terraform data source — the
// server's catalog of upstreams a pull-through cache may front.
//
// IT EXISTS SO THAT NOTHING IN THIS PROVIDER HOLDS A COPY OF THAT CATALOG. The
// set is server-owned: rows are added and retired without a provider release, so
// a list hardcoded in a validator, an enum or a docs page would refuse a key the
// server accepts and offer a key it no longer does — and the customer meets that
// as a 400 they cannot act on. `frostmoln_container_registry_cache` therefore
// validates only the LENGTH of its `upstream`, and a practitioner reads the real
// keys from here, including to drive a `for_each`.
package container_registry_cache_upstreams

import (
	"context"
	"errors"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
)

var _ datasource.DataSource = &cacheUpstreamsDataSource{}

// cachesPath is the tenant-scoped collection that serves the catalog. It stays
// TWO-SEGMENT (`/registry/caches`, never a bare `/registry`): the api-gateway's
// feature gate matches on an unanchored substring of the whole path, so a bare
// `/registry` would capture a customer's own resources named "registry".
//
// The catalog is platform-wide but is served on the TENANT route, alongside the
// tenant's own caches and their cap — so reading it needs an enabled registry.
const cachesPath = "/registry/caches"

// reasonNotEnabled marks the 409 answered to a tenant that has not opted in. The
// same route answers a different 409, so `details.reason` is the discriminator,
// not the status and not the code. The registry packages keep per-package copies
// of this constant rather than share an internal type, as the repositories data
// source does.
const reasonNotEnabled = "REGISTRY_NOT_ENABLED"

// NewDataSource returns a new frostmoln_container_registry_cache_upstreams data
// source factory.
func NewDataSource() datasource.DataSource {
	return &cacheUpstreamsDataSource{}
}

type cacheUpstreamsDataSource struct {
	client *client.Client
}

// cacheUpstreamsModel is the Terraform state model for the catalog.
type cacheUpstreamsModel struct {
	Upstreams  types.List  `tfsdk:"upstreams"`
	CacheLimit types.Int64 `tfsdk:"cache_limit"`
}

// cacheUpstreamItemModel represents one catalog row.
type cacheUpstreamItemModel struct {
	Key                 types.String `tfsdk:"key"`
	Display             types.String `tfsdk:"display"`
	RequiresCredentials types.Bool   `tfsdk:"requires_credentials"`
}

// apiCacheUpstream is the API representation of one catalog row.
type apiCacheUpstream struct {
	Key                 string `json:"key"`
	Display             string `json:"display"`
	RequiresCredentials bool   `json:"requiresCredentials"`
}

// apiCacheList is the listing response. Only `upstreams` and `limit` are read
// here — the tenant's own caches belong to the resource.
type apiCacheList struct {
	Limit     int64              `json:"limit"`
	Upstreams []apiCacheUpstream `json:"upstreams"`
}

var cacheUpstreamItemAttrTypes = map[string]attr.Type{
	"key":                  types.StringType,
	"display":              types.StringType,
	"requires_credentials": types.BoolType,
}

func (d *cacheUpstreamsDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_container_registry_cache_upstreams"
}

func (d *cacheUpstreamsDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "The upstreams a `frostmoln_container_registry_cache` may front, and how many " +
			"caches this tenant may hold.\n\n" +
			"THE CATALOG IS SERVER-OWNED AND THIS IS THE ONLY PLACE TO READ IT. Rows are added and " +
			"retired without a provider release, so no list published in documentation is " +
			"authoritative — drive a `for_each` from this data source rather than from a literal set " +
			"of keys, and expect a key that worked last quarter to be gone.\n\n" +
			"A cache whose upstream has since been retired keeps working and stays deletable, but it " +
			"will NOT have a row here — so a lookup from a cache's `upstream` into this list must " +
			"tolerate a miss.\n\n" +
			"A tenant that has NOT enabled its registry is an error here, not an empty catalog: an " +
			"empty list would make \"this tenant has no registry\" and \"this platform offers no " +
			"upstreams\" indistinguishable, and a `for_each` reading the second would quietly " +
			"destroy every cache in the configuration.",
		Attributes: map[string]schema.Attribute{
			"upstreams": schema.ListNestedAttribute{
				Description: "Every upstream currently on offer.",
				Computed:    true,
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"key": schema.StringAttribute{
							Description: "The value to set as `upstream` on a " +
								"`frostmoln_container_registry_cache`, and the `-cache-<key>` suffix of " +
								"the namespace it occupies. A key, never a URL.",
							Computed: true,
						},
						"display": schema.StringAttribute{
							Description: "The upstream in human-readable form, naming the real host " +
								"(for example \"GitHub Container Registry (ghcr.io)\"). The key is " +
								"opaque because it has to be safe in a namespace, so this is the only " +
								"place a person sees where the images come from.",
							Computed: true,
						},
						"requires_credentials": schema.BoolAttribute{
							Description: "Whether this upstream demands your own credentials, because an " +
								"anonymous mirror of it draws on an allowance shared with other " +
								"customers. When true, a cache for this upstream must set `username` " +
								"and `password`. Read the flag rather than special-casing a key: it is " +
								"a property of the row and the server enforces it either way.",
							Computed: true,
						},
					},
				},
			},
			"cache_limit": schema.Int64Attribute{
				Description: "How many caches this tenant may hold in total. Served so a configuration " +
					"can be written against the real cap instead of a number copied from " +
					"documentation. Exceeding it is refused at apply time with a 403.",
				Computed: true,
			},
		},
	}
}

func (d *cacheUpstreamsDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
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

func (d *cacheUpstreamsDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var state cacheUpstreamsModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	apiResp, err := d.client.Get(ctx, d.client.TenantPath(cachesPath), nil)
	if err != nil {
		if isNotEnabled(err) {
			// Deliberately an error rather than an empty catalog — see the schema
			// description. It names the remedy because the server's own message
			// cannot: it does not know the practitioner is in Terraform.
			resp.Diagnostics.AddError(
				"This tenant's container registry is not enabled",
				"There is no cache catalog to read, because the tenant has not opted in. Enable the "+
					"registry with a `frostmoln_container_registry` resource (or `fm registry enable`) "+
					"and read it again — and if this configuration creates the registry itself, make "+
					"the data source depend on that resource so the two are ordered.\n\nError: "+err.Error(),
			)
			return
		}
		resp.Diagnostics.AddError("Failed to read the container registry cache upstreams", err.Error())
		return
	}

	list, err := client.ParseResponse[apiCacheList](apiResp)
	if err != nil {
		resp.Diagnostics.AddError("Failed to parse the container registry cache upstreams response", err.Error())
		return
	}

	// An EMPTY catalog is refused rather than returned. The allow-list is closed
	// and never empty in practice, so an empty one is a shape change or a
	// misroute — and it is the shape that does damage: a `for_each` over it plans
	// the destruction of every cache in the configuration, which deletes their
	// cached images, and reports it as an ordinary diff.
	if len(list.Upstreams) == 0 {
		resp.Diagnostics.AddError(
			"The container registry cache upstream catalog came back empty",
			"The platform's upstream allow-list is closed and never empty, so an empty catalog is an "+
				"unreadable answer rather than a real one. Refusing rather than returning a list that "+
				"would plan the destruction of every cache driven from it.",
		)
		return
	}

	// Built with a length, not appended to from nil: a nil slice renders as a
	// NULL list, which reads to a configuration as "unknown" rather than "none".
	items := make([]cacheUpstreamItemModel, 0, len(list.Upstreams))
	for _, u := range list.Upstreams {
		items = append(items, cacheUpstreamItemModel{
			Key:                 types.StringValue(u.Key),
			Display:             types.StringValue(u.Display),
			RequiresCredentials: types.BoolValue(u.RequiresCredentials),
		})
	}

	upstreams, diags := types.ListValueFrom(ctx, types.ObjectType{AttrTypes: cacheUpstreamItemAttrTypes}, items)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	state.Upstreams = upstreams
	state.CacheLimit = types.Int64Value(list.Limit)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// isNotEnabled reports whether err is the refusal issued to a tenant that has
// not opted in.
func isNotEnabled(err error) bool {
	var apiErr *client.APIError
	if !errors.As(err, &apiErr) {
		return false
	}
	reason, _ := apiErr.Details["reason"].(string)
	return reason == reasonNotEnabled
}
