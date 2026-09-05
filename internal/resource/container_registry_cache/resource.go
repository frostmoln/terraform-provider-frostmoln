package container_registry_cache

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/docs"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/writeonly"
)

var (
	_ resource.Resource                = &cacheResource{}
	_ resource.ResourceWithImportState = &cacheResource{}
)

// cachesPath is the tenant-scoped collection. It stays TWO-SEGMENT
// (`/registry/caches`, never a bare `/registry`): the api-gateway's feature gate
// matches with an unanchored substring over the whole path, and a bare
// `/registry` would capture a customer's own resources named "registry".
const cachesPath = "/registry/caches"

// The refusals this surface answers, discriminated by `details.reason`. The
// status alone is never enough: the same route answers two different 409s, and
// the cache cap is a 403 rather than a 409.
const (
	reasonNotEnabled          = "REGISTRY_NOT_ENABLED"
	reasonAlreadyExists       = "CACHE_ALREADY_EXISTS"
	reasonUpstreamNotAllowed  = "UPSTREAM_NOT_PERMITTED"
	reasonCredentialsRequired = "UPSTREAM_CREDENTIALS_REQUIRED"
)

// NewResource returns a new registry pull-through cache resource factory.
func NewResource() resource.Resource {
	return &cacheResource{}
}

type cacheResource struct {
	client *client.Client
}

func (r *cacheResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_container_registry_cache"
}

func (r *cacheResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "A pull-through cache in this tenant's container registry — a namespace that " +
			"mirrors ONE public registry on demand. One upstream per cache: a tenant wanting three " +
			"upstreams creates three of these.\n\n" +
			"Pull through it with the SAME credential that reaches your repository namespace — " +
			"`docker login` is per host, so one credential spans both, and existing credentials are " +
			"extended onto a new cache automatically.\n\n" +
			"CACHED BYTES COUNT AGAINST YOUR STORAGE ALLOWANCE. A cache is your storage: one quota, " +
			"one meter, shared with your repository namespace. It fills on PULL, so it can consume " +
			"the allowance with no push from you.\n\n" +
			"THERE IS NO UPDATE ROUTE. Every argument here is replace-only, including `password`: " +
			"changing credentials means destroying the cache and creating it again. DESTROYING A " +
			"CACHE DELETES ITS CACHED IMAGES, and does not ask — every byte in it is a copy of " +
			"something still at the upstream, so a later pull simply re-fetches, but your stored-bytes " +
			"figure falls as the content goes.\n\n" +
			"The registry must already exist: a cache cannot be created before the tenant has opted " +
			"in with a `frostmoln_container_registry` resource.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Description: "The upstream key. A tenant holds at most one cache per upstream and the " +
					"API addresses a cache by that key, so the key is the identifier.",
				Computed: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"tenant_id": schema.StringAttribute{
				Description: "The owning tenant (server-set; the provider's tenant_id selects it).",
				Computed:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"upstream": schema.StringAttribute{
				Description: "The upstream to mirror, as a KEY from the server's catalog — never a URL, " +
					"which is not accepted in any form and has no field. Read the available keys from " +
					"the `frostmoln_container_registry_cache_upstreams` data source rather than from " +
					"any published list: the catalog is server-owned, rows are added and retired, and " +
					"a key copied into a configuration from documentation can be one the server no " +
					"longer offers. Changing it replaces the cache and deletes what it had cached.",
				Required: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
				Validators: []validator.String{
					// Length only. NEVER a OneOf over the upstream keys: the
					// catalog is served by the API precisely so no client holds a
					// copy, and a hardcoded set here would refuse — at plan time,
					// before the server is ever asked — a key the server accepts.
					stringvalidator.LengthBetween(1, 64),
				},
			},
			"username": schema.StringAttribute{
				Description: "Your own username at the upstream registry. Required for upstreams whose " +
					"catalog entry says `requires_credentials` (Docker Hub today — read the flag, do " +
					"not special-case the key); the server enforces it either way. WRITE-ONLY: no " +
					"endpoint returns it, so Terraform reports whatever it last wrote and cannot " +
					"detect a change made elsewhere. Changing it replaces the cache.",
				Optional: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
				Validators: []validator.String{
					stringvalidator.LengthBetween(1, 255),
					// The username/password PAIRING is enforced in Create, not
					// here: the password has two spellings (`password` and
					// `password_wo`) and AlsoRequires cannot say "one of two".
					// Create is also the only layer that sees a write-only value
					// at all.
				},
			},
			"password": schema.StringAttribute{
				Description: "Your own password or access token at the upstream registry. Required for " +
					"upstreams whose catalog entry says `requires_credentials`, together with `username`. " +
					"WRITE-ONLY AT THE API: no endpoint returns it and there is no update route, so " +
					"changing it REPLACES the cache and deletes what it had cached. NOT VALIDATED at " +
					"creation — a wrong password surfaces as a failed pull, never as an error from " +
					"`terraform apply`. " + docs.StateSecretNote +
					" Prefer `password_wo`, which carries the same value but is never written to state; " +
					"the two are mutually exclusive.",
				Optional:  true,
				Sensitive: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
				Validators: []validator.String{
					stringvalidator.LengthBetween(1, 1024),
					stringvalidator.ConflictsWith(path.MatchRoot("password_wo")),
					stringvalidator.PreferWriteOnlyAttribute(path.MatchRoot("password_wo")),
				},
			},
			"password_wo": schema.StringAttribute{
				Description: "Your own password or access token at the upstream registry, as a " +
					"[write-only argument](https://developer.hashicorp.com/terraform/language/resources/ephemeral/write-only): " +
					"the value reaches the provider on apply and is never written to the plan or to " +
					"state. Requires Terraform 1.11 or later. Mutually exclusive with `password`, and " +
					"`password_wo_version` is required whenever this one is set. Terraform cannot see a " +
					"write-only value, so it cannot detect a change to it: changing " +
					"`password_wo_version` is what makes the next apply send the current value, and it " +
					"does so by REPLACING the cache — deleting what it had cached — because the cache " +
					"API has no update route. Editing the password without touching the version does " +
					"nothing at all.",
				Optional:  true,
				Sensitive: true,
				WriteOnly: true,
				// No plan modifiers: a write-only attribute is null in prior
				// state, in the plan and in final state, so a RequiresReplace
				// here would compare null against null and never fire. The
				// version companion carries the replacement.
				Validators: []validator.String{
					// "" is not null, so it passes every null guard and reaches
					// the wire — where it is stored as a real (broken)
					// credential that no endpoint can read back to reveal.
					stringvalidator.LengthBetween(1, 1024),
					stringvalidator.AlsoRequires(path.MatchRoot("password_wo_version")),
				},
			},
			"password_wo_version": schema.StringAttribute{
				Description: "Change tracker for `password_wo`, required whenever that attribute is set. " +
					"Changing this value REPLACES the cache — deleting every image it had cached — and " +
					"sends the current `password_wo`, which is the same lifecycle a change to " +
					"`password` has, since the cache API has no update route. Leaving it alone leaves " +
					"the stored credential untouched however much the write-only value changes. Its " +
					"content is arbitrary — a counter or a date is typical — and unlike the password it " +
					"IS stored in state, so do not derive it from the password or from anything in it: " +
					"a digest of a secret is printed verbatim in `terraform plan` output, CI job logs " +
					"and PR plan comments, and a digest is an offline confirmation oracle. " +
					"`terraform import` leaves this unset, so the first apply against an imported cache " +
					"that uses `password_wo` plans a REPLACEMENT.",
				Optional: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
				Validators: []validator.String{
					stringvalidator.AlsoRequires(path.MatchRoot("password_wo")),
				},
			},
			"namespace": schema.StringAttribute{
				Description: "The namespace this cache occupies, `<tenant-namespace>-cache-<upstream>`. " +
					"Derived from the immutable tenant id and the upstream key, so it never changes " +
					"under a cache that is not being replaced.",
				Computed: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"pull_path": schema.StringAttribute{
				Description: "What to prefix an image with to pull it through this cache — the whole " +
					"point of the resource. `docker pull <pull_path>/<image>:<tag>`.",
				Computed: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"display": schema.StringAttribute{
				Description: "The upstream in human-readable form, naming the real host. The key is " +
					"opaque because it has to be safe in a namespace, so this is the only place a " +
					"person sees where the images come from. FALLS BACK TO THE BARE KEY once an " +
					"upstream is retired from the catalog — the cache keeps working and stays " +
					"deletable, so a display equal to the key is a retired upstream, not an error.",
				Computed: true,
			},
		},
	}
}

func (r *cacheResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	c, ok := req.ProviderData.(*client.Client)
	if !ok {
		resp.Diagnostics.AddError(
			"Unexpected Resource Configure Type",
			fmt.Sprintf("Expected *client.Client, got: %T", req.ProviderData),
		)
		return
	}
	r.client = c
}

func (r *cacheResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan ContainerRegistryCacheModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// A write-only attribute is null in the plan by construction; its value only
	// ever reaches the provider through the config. The read fails CLOSED on an
	// unknown or unpaired value rather than falling through to "no password
	// supplied", which would create a cache with no credentials and report it as
	// a success — and no endpoint can read the stored credential back to reveal
	// otherwise.
	passwordWO := cachePasswordWO.Read(ctx, req.Config, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	password := plan.Password.ValueString()
	if !passwordWO.IsNull() {
		password = passwordWO.ValueString()
	}

	upstream := plan.Upstream.ValueString()
	username := plan.Username.ValueString()
	// A credential is a PAIR, and half of one is accepted by the API and stored:
	// the failure then surfaces much later as a failed pull, on a cache that
	// cannot be repaired in place. This lives here rather than in an
	// AlsoRequires because the password has two spellings and because a
	// write-only value is invisible to every plan-time validator.
	if (username == "") != (password == "") {
		resp.Diagnostics.AddError(
			"Upstream credentials must be a username AND a password",
			"Only one half of the upstream credential was supplied. The registry accepts and stores "+
				"half a credential, and nothing reads it back — so the failure would surface later as "+
				"a failed pull through a cache that cannot be repaired in place, only replaced. Set "+
				"`username` together with `password` (or `password_wo` and `password_wo_version`), or "+
				"neither for an upstream that does not require credentials.",
		)
		return
	}

	apiResp, err := r.client.Post(ctx, r.client.TenantPath(cachesPath), apiCreateCacheRequest{
		Upstream: upstream,
		Username: username,
		Password: password,
	})
	if err != nil {
		addCreateRefusal(&resp.Diagnostics, upstream, err)
		return
	}

	cache, err := client.ParseResponse[apiCache](apiResp)
	if err != nil {
		// The cache exists and Terraform did not record it: the next apply will
		// meet CACHE_ALREADY_EXISTS. Name the remedy, because nothing else will.
		resp.Diagnostics.AddError(
			"Failed to parse the container registry cache response",
			"The cache was created but its response could not be read, so Terraform did not record it. "+
				"Import it with `terraform import <address> "+upstream+"`, or delete it with "+
				"`fm registry cache delete "+upstream+"` and apply again.\n\nError: "+err.Error(),
		)
		return
	}

	// Never let the response rewrite the identity of what was just created. A
	// body for a different upstream — a misroute collapsing to the collection, a
	// shape change — would put one cache's identity in another's state and the
	// next destroy would delete the wrong namespace, with its images.
	if cache.Upstream != upstream {
		resp.Diagnostics.AddError(
			"Unexpected container registry cache in the create response",
			fmt.Sprintf("Requested upstream %q but the API answered with %q. Nothing was recorded in "+
				"state; check `fm registry cache list` before applying again.", upstream, cache.Upstream),
		)
		return
	}
	if cache.PullPath == "" {
		// The pull path is the entire product of this resource. Recording a cache
		// without one reports a successful apply and then renders "/image:tag" in
		// whatever output consumes it.
		resp.Diagnostics.AddError(
			"The container registry cache was created without a pull path",
			fmt.Sprintf("The API accepted the request for upstream %q but reported no pull path for the "+
				"resulting cache, so there is nothing to pull through. The cache may exist: check "+
				"`fm registry cache list` and delete it before applying again.", upstream),
		)
		return
	}

	// Username and Password are carried across from the plan untouched: fromAPI
	// never writes them, because no response contains them.
	plan.fromAPI(r.client.TenantID(), cache)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *cacheResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state ContainerRegistryCacheModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	upstream := state.Upstream.ValueString()
	if upstream == "" {
		// Without this the match below would compare against "" and never hit,
		// silently removing a live cache from state and planning a create that
		// then fails with CACHE_ALREADY_EXISTS.
		resp.Diagnostics.AddError(
			"Container registry cache is missing its upstream",
			"Terraform cannot address this cache. Find it with `fm registry cache list` and re-import "+
				"it with `terraform import <address> <upstream>`.",
		)
		return
	}

	list, err := r.list(ctx)
	if err != nil {
		// REGISTRY_NOT_ENABLED is a statement about the REGISTRY, not about this
		// cache, so it must NOT drop the resource — the same fail-closed rule the
		// credential resource applies. Only the cache's absence from a listing the
		// server actually answered means THIS cache is gone.
		if isNotEnabled(err) {
			resp.Diagnostics.AddError(
				"This tenant's container registry is not enabled",
				"The tenant's caches cannot be read, because the tenant has no registry. Terraform is "+
					"keeping the cache in state rather than treating a registry-level refusal as a "+
					"deletion. Enable the registry with a `frostmoln_container_registry` resource (or "+
					"`fm registry enable`) and refresh again.\n\nError: "+err.Error(),
			)
			return
		}
		resp.Diagnostics.AddError("Failed to list the container registry caches", err.Error())
		return
	}

	found := findCache(list.Data, upstream)
	if found == nil {
		resp.State.RemoveResource(ctx)
		return
	}

	// Username and Password are NOT touched: fromAPI cannot write them, and
	// `state` already holds what was configured. Blanking them here would plan a
	// replace on the next refresh — destroying the cache and its images with no
	// configuration change at all.
	state.fromAPI(r.client.TenantID(), found)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Update cannot be reached: every configurable attribute is RequiresReplace,
// because the API has no update route for a cache.
func (r *cacheResource) Update(_ context.Context, _ resource.UpdateRequest, resp *resource.UpdateResponse) {
	resp.Diagnostics.AddError(
		"Update Not Supported",
		"A container registry cache is immutable: there is no update route, so changing the upstream "+
			"or the upstream credentials replaces the cache and deletes what it had cached. "+
			"Reaching this is a provider bug; please report it.",
	)
}

func (r *cacheResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state ContainerRegistryCacheModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	upstream := state.Upstream.ValueString()
	// The upstream is a REQUIRED query parameter. Without this guard the request
	// would go out without it, and a 400 would be reported as a plain failure
	// while a 204 on some future permissive build would delete something else.
	if upstream == "" {
		resp.Diagnostics.AddError(
			"Container registry cache is missing its upstream",
			"Terraform cannot address this cache, so it cannot delete it. Find it with "+
				"`fm registry cache list`, delete it there, then remove it from state with "+
				"`terraform state rm <address>`.",
		)
		return
	}

	query := url.Values{"upstream": []string{upstream}}
	if _, err := r.client.DeleteWithQuery(ctx, r.client.TenantPath(cachesPath), query); err != nil {
		// Only a genuine 404 means this cache is already gone. A
		// REGISTRY_NOT_ENABLED refusal is about the registry, and swallowing it
		// would print a successful destroy while the namespace, its cached images
		// and the tenant's stored upstream credentials all survived.
		if client.IsNotFound(err) {
			return
		}
		if isNotEnabled(err) {
			resp.Diagnostics.AddError(
				"The container registry cache was NOT deleted",
				fmt.Sprintf("The registry refused the request because this tenant's registry is not "+
					"currently enabled, so the %q cache was left in place — with its cached images and "+
					"the upstream credentials stored against it. Delete it with `fm registry cache "+
					"delete %s` once the registry is reachable, then remove it from state with "+
					"`terraform state rm <address>`.\n\nError: %s", upstream, upstream, err.Error()),
			)
			return
		}
		resp.Diagnostics.AddError("Failed to delete the container registry cache", err.Error())
	}
}

// ImportState adopts an existing cache by its upstream key.
//
// The key is the whole id: the operating tenant is a provider-level selector, so
// a tenant segment in the import id would be decorative — the provider would
// read the cache from whichever tenant it is configured for regardless of what
// the id said, which is worse than not accepting one.
//
// `username` and `password` CANNOT be recovered: no endpoint returns them. They
// import as null, so a configuration that sets either will plan a REPLACE on the
// first apply after the import — destroying the cache and its cached images. For
// an upstream that requires credentials there is no way around that; see the
// import example.
func (r *cacheResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	upstream := strings.TrimSpace(req.ID)
	if upstream == "" {
		resp.Diagnostics.AddError(
			"Invalid import ID",
			fmt.Sprintf("expected the cache's upstream key (for example \"ghcr\"), got %q", req.ID),
		)
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), upstream)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("upstream"), upstream)...)
}

// cachePasswordWO is the write-only triple for the upstream password.
//
// ExactlyOne is FALSE: unlike the certificate's private key, upstream
// credentials are genuinely optional — most upstreams need none, and the ones
// that do are named by the server's own `requires_credentials` flag rather than
// by anything this provider may assert. The floor that DOES exist here — a
// username and a password together or neither — is enforced in Create, where a
// write-only value is visible at all.
var cachePasswordWO = writeonly.Attr{ // pragma: allowlist secret
	WO:      "password_wo",
	Version: "password_wo_version",
	Legacy:  "password",
	Subject: "the pull-through cache",
}

// list fetches the tenant's caches. It is the only read surface: there is no
// single-cache GET.
func (r *cacheResource) list(ctx context.Context) (*apiCacheList, error) {
	apiResp, err := r.client.Get(ctx, r.client.TenantPath(cachesPath), nil)
	if err != nil {
		return nil, err
	}
	list, err := client.ParseResponse[apiCacheList](apiResp)
	if err != nil {
		return nil, err
	}
	// The count the server reports must match the rows it sent. This route is
	// unpaginated, so the two agree or the listing is short — and a short listing
	// is invisible here: a cache missing from it looks exactly like a cache that
	// was deleted, so Read would drop a live cache from state and plan a create
	// that fails with CACHE_ALREADY_EXISTS. Refuse rather than act on a view that
	// may be incomplete.
	if list.TotalCount != int64(len(list.Data)) {
		return nil, fmt.Errorf(
			"the container registry cache listing is inconsistent: the API reported %d caches but returned %d",
			list.TotalCount, len(list.Data),
		)
	}
	return list, nil
}

// findCache returns the cache for upstream, or nil when the tenant holds none.
func findCache(caches []apiCache, upstream string) *apiCache {
	for i := range caches {
		if caches[i].Upstream == upstream {
			return &caches[i]
		}
	}
	return nil
}

// addCreateRefusal translates the refusals whose server message cannot name the
// Terraform-level remedy — the server does not know the caller is Terraform.
// Every branch keys on `details.reason` or on the error code, never on the
// message text.
func addCreateRefusal(diags interface{ AddError(string, string) }, upstream string, err error) {
	switch {
	case isNotEnabled(err):
		diags.AddError(
			"This tenant's container registry is not enabled",
			"A pull-through cache cannot be created before the registry exists. Add a "+
				"`frostmoln_container_registry` resource and let it create one — Terraform will order "+
				"the two if the cache references it, for example via `depends_on`.\n\nError: "+err.Error(),
		)
	case hasReason(err, reasonAlreadyExists):
		diags.AddError(
			fmt.Sprintf("This tenant already has a cache for the %q upstream", upstream),
			"One cache per upstream, and there is nothing to update — so this is not a diff Terraform "+
				"can resolve. Adopt the existing cache with `terraform import <address> "+upstream+"` "+
				"(its `username` and `password` cannot be imported: no endpoint returns them), or "+
				"delete it with `fm registry cache delete "+upstream+"` and apply again — which "+
				"deletes what it had cached.\n\nError: "+err.Error(),
		)
	case hasReason(err, reasonUpstreamNotAllowed):
		diags.AddError(
			fmt.Sprintf("%q is not an upstream this platform offers", upstream),
			"The upstream catalog is server-owned: rows are added and retired without a provider "+
				"release, so a key taken from documentation or from an older configuration can be one "+
				"the server no longer offers. Read the current keys from the "+
				"`frostmoln_container_registry_cache_upstreams` data source."+
				permittedSuffix(err)+"\n\nError: "+err.Error(),
		)
	case hasReason(err, reasonCredentialsRequired):
		diags.AddError(
			fmt.Sprintf("The %q upstream requires your own credentials", upstream),
			"This upstream is refused without credentials, because an anonymous mirror of it draws on "+
				"an allowance shared with other customers. Set `username` and `password` to your own "+
				"account at that upstream. They are write-only and cannot be changed in place: a later "+
				"change replaces the cache.\n\nError: "+err.Error(),
		)
	case isCacheCapReached(err):
		diags.AddError(
			"This tenant holds the maximum number of container registry caches",
			"Each cache is a namespace with its own storage, so the count is capped. The current cap "+
				"is the `cache_limit` attribute of the `frostmoln_container_registry_cache_upstreams` "+
				"data source. Remove a cache you no longer pull through — deleting it deletes what it "+
				"had cached — and apply again.\n\nError: "+err.Error(),
		)
	default:
		diags.AddError("Failed to create the container registry cache", err.Error())
	}
}

// permittedSuffix renders the keys the server said it does permit, when it sent
// them. It reads `details.permitted` rather than any list held here — that is
// the whole point: the catalog is the server's, and this is the one place a
// refusal can name it without a client-side copy.
func permittedSuffix(err error) string {
	var apiErr *client.APIError
	if !errors.As(err, &apiErr) {
		return ""
	}
	raw, ok := apiErr.Details["permitted"].([]any)
	if !ok {
		return ""
	}
	// Built with a length rather than appended to from nil, and skipping
	// non-strings rather than rendering Go's %v for them: the list is rendered
	// verbatim to a practitioner who will paste a key out of it.
	keys := make([]string, 0, len(raw))
	for _, k := range raw {
		if s, ok := k.(string); ok && s != "" {
			keys = append(keys, s)
		}
	}
	if len(keys) == 0 {
		return ""
	}
	sort.Strings(keys)
	return "\n\nThe server currently permits: " + strings.Join(keys, ", ") + "."
}

// hasReason reports whether err carries the given `details.reason`.
func hasReason(err error, reason string) bool {
	var apiErr *client.APIError
	if !errors.As(err, &apiErr) {
		return false
	}
	got, _ := apiErr.Details["reason"].(string)
	return got == reason
}

// isNotEnabled reports whether err is the refusal issued to a tenant that has
// not opted in.
func isNotEnabled(err error) bool {
	return hasReason(err, reasonNotEnabled)
}

// isCacheCapReached reports whether err is the cache cap. It is a 403 carrying
// the platform's `quota_exceeded` code, NOT a 409 — branching on the status
// alone never sees it, and branching on 409 alone catches the wrong refusal.
func isCacheCapReached(err error) bool {
	var apiErr *client.APIError
	if !errors.As(err, &apiErr) {
		return false
	}
	return apiErr.Code == "quota_exceeded" && apiErr.StatusCode == http.StatusForbidden
}
