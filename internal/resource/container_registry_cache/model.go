// Package container_registry_cache implements the
// frostmoln_container_registry_cache Terraform resource — one pull-through
// cache in the tenant's container registry.
package container_registry_cache

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/tftags"
)

// ContainerRegistryCacheModel is the Terraform state model for one cache.
//
// Username and Password are WRITE-ONLY: they are the tenant's own credentials at
// the upstream registry and no endpoint returns them. They exist in this model
// only because Terraform must remember what was configured; nothing that reads
// from the API may write to them. See fromAPI.
type ContainerRegistryCacheModel struct {
	// ID is the upstream key. A tenant holds at most one cache per upstream and
	// the API addresses a cache by that key alone, so it IS the identifier —
	// there is no server-issued id to hold instead.
	ID types.String `tfsdk:"id"`
	// TenantID is server-set, like every other tenant_id in this provider: the
	// operating tenant is a PROVIDER-level selector (provider `tenant_id`,
	// FROSTMOLN_TENANT_ID, or the default from GET /v1/me) and client.TenantPath
	// builds the route from it. A per-resource argument would be ignored by the
	// client and would silently write to whichever tenant the provider selected.
	TenantID types.String `tfsdk:"tenant_id"`
	Upstream types.String `tfsdk:"upstream"`
	Username types.String `tfsdk:"username"`
	// Password is the legacy, state-written spelling. PasswordWO is the
	// write-only one: null in prior state, in the plan and in final state, so it
	// reaches the provider only through the CONFIG and only during an apply.
	// PasswordWOVersion is the practitioner-set change tracker that carries the
	// replacement PasswordWO cannot signal.
	Password          types.String `tfsdk:"password"`
	PasswordWO        types.String `tfsdk:"password_wo"`
	PasswordWOVersion types.String `tfsdk:"password_wo_version"`
	Namespace         types.String `tfsdk:"namespace"`
	PullPath          types.String `tfsdk:"pull_path"`
	Display           types.String `tfsdk:"display"`
	// UsedBytes is observational and MOVES on every cached pull. It gets no
	// UseStateForUnknown, unlike Namespace and PullPath: those are stable for the
	// life of the cache, this one is not, and pinning a stale reading would be
	// worse than showing it unknown until the read lands.
	UsedBytes types.Int64 `tfsdk:"used_bytes"`
	// Tags / TagsAll are the cache's customer resource tags (internal/tftags) —
	// never the image tags of what it mirrors.
	Tags    types.Map `tfsdk:"tags"`
	TagsAll types.Map `tfsdk:"tags_all"`
}

// apiCache is the API representation of a pull-through cache.
//
// It CARRIES a usage figure (server-side since 2026-09-06). This comment said
// the opposite, reasoning that a tenant's allowance is an aggregate. The
// aggregate half is right and `frostmoln_container_registry.storage_used_bytes`
// IS that aggregate — but the caps are enforced PER NAMESPACE, so a cache is
// also the unit that refuses a pull and the total cannot say which one is close.
//
// Do NOT sum used_bytes across caches and expect storage_used_bytes: both skip a
// namespace whose quota the server could not read. The settings attribute is the
// metered one.
//
// There is still no credential state: no endpoint reads an upstream credential
// back. Do not add fields the API does not send.
type apiCache struct {
	Upstream string `json:"upstream"`
	// Display FALLS BACK TO THE BARE KEY for a cache whose upstream has been
	// retired from the catalog. Such a cache still lists and stays deletable, so
	// nothing here may treat a display that equals the key as an error.
	Display   string `json:"display"`
	Namespace string `json:"namespace"`
	PullPath  string `json:"pullPath"`
	// UsedBytes is omitted by the server both for a cache that has mirrored
	// nothing and for a quota read it could not make, so a zero here is "no
	// number", never a measured empty.
	UsedBytes int64 `json:"usedBytes,omitempty"`
	// Tags are the cache's customer resource tags ({} when untagged). nil
	// together with TagsUnavailable when the registry could not read its tag
	// store; the cache itself is then still real and current.
	Tags            map[string]string `json:"tags"`
	TagsUnavailable bool              `json:"tagsUnavailable,omitempty"`
}

// apiCacheUpstream is one row of the server's upstream catalog.
type apiCacheUpstream struct {
	Key                 string `json:"key"`
	Display             string `json:"display"`
	RequiresCredentials bool   `json:"requiresCredentials"`
}

// apiCacheList is the listing response. It is the ONLY read surface for a cache:
// there is no single-cache GET, so Read and ImportState both list and match on
// the upstream key.
//
// TotalCount is CHECKED against the rows rather than merely decoded. The route
// is unpaginated, so the two must agree — and a short listing is the dangerous
// failure here, because a cache missing from it is indistinguishable from a
// cache that was deleted, and Read would drop it from state and plan a create
// that then fails with CACHE_ALREADY_EXISTS.
type apiCacheList struct {
	Data       []apiCache         `json:"data"`
	TotalCount int64              `json:"totalCount"`
	Limit      int64              `json:"limit"`
	Upstreams  []apiCacheUpstream `json:"upstreams"`
}

// apiCreateCacheRequest is the API request to create a cache.
//
// Username and Password are omitempty: an upstream that does not require
// credentials must not be sent empty strings, which would be stored as a real
// (broken) credential pair rather than as "no credentials".
type apiCreateCacheRequest struct {
	Upstream string `json:"upstream"`
	Username string `json:"username,omitempty"`
	Password string `json:"password,omitempty"`
	// Tags is default_tags ∪ tags (tftags.ForCreate), omitted when empty; the API
	// merges the tenant's default tags in.
	Tags map[string]string `json:"tags,omitempty"`
}

// apiUpdateTagsRequest is the cache's tags-only PATCH (`?upstream=<key>`): a
// row-only write that never touches the cache's namespace, its cached images or
// its stored upstream credentials. A POINTER so an empty map is sent as {}
// (clear) — the route refuses a body without `tags`.
type apiUpdateTagsRequest struct {
	Tags *map[string]string `json:"tags"`
}

// readTags renders the response's tags into the model: `tags` keeps the keys
// the prior value (plan or state) names, `tags_all` every customer key the
// registry holds. When the registry could not read its tag store it keeps the
// prior values and warns; sent is what a create or update wrote, the fallback
// for a tags_all not yet known.
func (m *ContainerRegistryCacheModel) readTags(ctx context.Context, c *apiCache, sent map[string]string, diags *diag.Diagnostics) {
	if c.TagsUnavailable {
		m.Tags, m.TagsAll = tftags.KeepOfferTagsWhenUnavailable(ctx, "the container registry cache", m.Tags, m.TagsAll, sent, diags)
		return
	}
	m.Tags, m.TagsAll = tftags.ReadBack(ctx, tftags.OfferCustomerTags(c.Tags), m.Tags, diags)
}

// fromAPI populates the Terraform model from an API response.
//
// It NEVER touches Username, Password, PasswordWO or PasswordWOVersion. No endpoint returns them, so every
// value this function could write would be the empty one — blanking the
// practitioner's configured credentials in state. With RequiresReplace on both,
// that is not a cosmetic diff: the next plan would destroy the cache (and every
// image cached in it) and create it again, on every single refresh. The caller
// passes the prior state through instead, exactly as the credential resource
// carries its secret across a read.
//
// tenantID is the operating tenant; the cache body does not carry one.
func (m *ContainerRegistryCacheModel) fromAPI(tenantID string, c *apiCache) {
	m.ID = types.StringValue(c.Upstream)
	m.TenantID = types.StringValue(tenantID)
	m.Upstream = types.StringValue(c.Upstream)
	m.Namespace = types.StringValue(c.Namespace)
	m.PullPath = types.StringValue(c.PullPath)
	m.Display = types.StringValue(c.Display)
	// Zero becomes NULL, not 0. The server omits the field for an empty cache and
	// for a failed quota read alike, and a literal 0 in state would assert the
	// first — the same rule storage_used_bytes follows on the registry resource.
	if c.UsedBytes > 0 {
		m.UsedBytes = types.Int64Value(c.UsedBytes)
	} else {
		m.UsedBytes = types.Int64Null()
	}
}
