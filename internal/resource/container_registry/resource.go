package container_registry

import (
	"context"
	"errors"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/scopedecl"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/tftags"
)

var (
	_ resource.Resource               = &containerRegistryResource{}
	_ resource.ResourceWithModifyPlan = &containerRegistryResource{}
)

// settingsPath is the tenant-scoped opt-in route. It stays TWO-SEGMENT
// (`/registry/settings`, never a bare `/registry`): the api-gateway's feature
// gate matches with an unanchored substring, and a bare `/registry` would
// capture a customer's own resources named "registry".
const settingsPath = "/registry/settings"

// reasonAlreadyEnabled marks the 409 answered to a re-run opt-in. The registry
// answers two different 409s on this surface and they differ ONLY here — the
// top-level code is `conflict` for this one and `invalid_state` for a tenant
// with no registry — so branching on the status, or even on the code, is not
// enough.
const reasonAlreadyEnabled = "REGISTRY_ALREADY_ENABLED"

// NewResource returns a new container registry resource factory.
func NewResource() resource.Resource {
	return &containerRegistryResource{}
}

type containerRegistryResource struct {
	client *client.Client
}

func (r *containerRegistryResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_container_registry"
}

func (r *containerRegistryResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Enables this tenant's container registry — the explicit opt-in that creates the " +
			"tenant's namespace and the billable state that comes with it.\n\n" +
			"Nothing about the registry itself is configurable: its only argument is `tags`, the resource " +
			"tags on the registry, which update in place. Applying it against a tenant that already opted " +
			"in (through the portal, the fm CLI, or another Terraform state) ADOPTS the existing registry " +
			"rather than failing, and adds this configuration's tags to the ones it already holds.\n\n" +
			"DESTROYING THIS RESOURCE DOES NOT DELETE THE REGISTRY. There is no teardown endpoint: " +
			"registry teardown belongs to tenant closure. `terraform destroy` removes it from state and " +
			"warns; the namespace, its images and its billable state all survive." +
			"\n\n" + scopedecl.Summary("frostmoln_container_registry"),
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Description: "The tenant id. A tenant has exactly one registry and its id is the tenant's.",
				Computed:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"enabled": schema.BoolAttribute{
				Description: "Whether the registry exists. Always true once this resource is in state — a " +
					"registry that stops being enabled is treated as gone and removed from state on refresh.",
				Computed: true,
			},
			"endpoint": schema.StringAttribute{
				Description: "The host to `docker login` to.",
				Computed:    true,
			},
			"namespace": schema.StringAttribute{
				Description: "The tenant's namespace at that endpoint — the first path segment of every " +
					"image reference. Derived from the immutable tenant id and never changes, so a tenant " +
					"rename does not strand image references you have pinned.",
				Computed: true,
			},
			"storage_limit_bytes": schema.Int64Attribute{
				Description: "The SUM of the hard storage caps enforced across every namespace this tenant owns — the repository namespace and every pull-through cache. Read this before you render it: the registry caps PER NAMESPACE and there is no aggregate cap anywhere, so this is total headroom and not a single enforced threshold. A tenant holding a repository namespace and two caches, each capped at 10 GiB, reports 30 GiB here and can still be refused with 9 GiB of it unused — `frostmoln_container_registry_cache.used_bytes` is what says which namespace is close. The value also MOVES as caches are created and destroyed. A push that would cross ITS OWN namespace's cap is refused with a 403 whose message states the cap, the usage and the size that would not fit — as prose, and only once the push has failed. Null when any one namespace's cap was not usable, which is several causes with the same result: a namespace reports no cap at all; a namespace reports a hard cap of exactly 0, which refuses every push and is the one null that is worse than it looks, not better; or the quota read itself failed. Being disabled is not a reason here — a registry that stops being enabled is removed from state rather than nulled. The platform re-checks and resets caps on a later sweep, which repairs the first two; it never repairs a failed read, so that one can persist for as long as the outage lasts and the value can go null and back between refreshes with no configuration change. A cap of 0 and an unlimited -1 both arrive as null rather than as a value, so null — not a zero or negative number — is the only state to check for. Null means the limit was not reported, and never that it is unlimited.",
				Computed:    true,
			},
			"storage_used_bytes": schema.Int64Attribute{
				Description: "Storage stored ACROSS EVERY NAMESPACE this tenant owns — the repository namespace and every pull-through cache, summed. This is the figure the tenant is metered on: the platform's usage meter sums exactly this set, so this attribute and the invoice describe the same set of namespaces. Not, strictly, the same bytes at the same instant: the meter aborts a whole window on any unreadable quota, so a window can be lost while this reports normally. OBSERVATIONAL: it changes with every push and with every cached pull, so expect it to differ on each refresh — a cache fills on PULL, so it moves without anyone pushing. It counts deduplicated bytes per namespace and then adds the namespaces up, so the same layer held in two namespaces is counted twice, and that is also how it is billed. It does not equal the sum of your local image sizes. It is the registry's accounting rather than an audited ledger, so treat it as what you are metered on rather than as an invoice line — billing keeps its own copy of the same reading.",
				Computed:    true,
			},
		},
	}
	resp.Schema.Attributes["tags"] = tftags.OfferTagsAttribute("Resource tags on the registry: your own key/value " +
		"metadata for search and filtering, stored by the platform only. They are NOT image tags — the `:tag` of an " +
		"image you push is part of the image reference and is unrelated. Changing them updates the registry in place " +
		"and never touches its namespace, images or credentials.")
	resp.Schema.Attributes["tags_all"] = tftags.TagsAllAttribute()
}

func (r *containerRegistryResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (r *containerRegistryResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan ContainerRegistryModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// The body carries the tags and nothing else: the namespace is derived from
	// the verified tenant id, so there is nothing a request could say that would
	// change what is created. Without tags it stays empty, as it always was.
	defaults := r.client.DefaultTags()
	sent := tftags.ForCreate(ctx, defaults, plan.Tags, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	var body any
	if sent != nil {
		body = apiEnableRequest{Tags: sent}
	}
	apiResp, err := r.client.Post(ctx, r.client.TenantPath(settingsPath), body)
	if err != nil {
		if !isAlreadyEnabled(err) {
			// No special-casing for other refusals: the gateway's own message
			// already names the remedy, and rewording it here would only risk
			// drifting from it.
			resp.Diagnostics.AddError("Failed to enable the container registry", err.Error())
			return
		}
		// The tenant opted in elsewhere. Adopting is the only defensible
		// behaviour: the opt-in is idempotent by design, it is the same registry
		// either way, and failing here would leave a tenant that clicked the
		// button in the portal permanently unable to manage it from Terraform.
		settings, readErr := r.read(ctx)
		if readErr != nil {
			resp.Diagnostics.AddError(
				"Container registry is already enabled but could not be read",
				"The tenant's registry already exists, so it was adopted rather than created — but reading "+
					"its current state failed, so Terraform cannot record it.\n\nError: "+readErr.Error(),
			)
			return
		}
		if d := assertUsable(settings); d != nil {
			resp.Diagnostics.Append(d)
			return
		}
		settings, sent = r.adoptTags(ctx, settings, sent, &resp.Diagnostics)
		if resp.Diagnostics.HasError() {
			return
		}
		tftags.RecordDefaults(ctx, resp.Private, defaults, &resp.Diagnostics)
		plan.fromAPI(r.client.TenantID(), settings)
		plan.readTags(ctx, settings, sent, &resp.Diagnostics)
		resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
		return
	}
	tftags.RecordDefaults(ctx, resp.Private, defaults, &resp.Diagnostics)

	settings, err := client.ParseResponse[apiRegistrySettings](apiResp)
	if err != nil {
		resp.Diagnostics.AddError("Failed to parse container registry response", err.Error())
		return
	}

	if d := assertUsable(settings); d != nil {
		resp.Diagnostics.Append(d)
		return
	}
	plan.fromAPI(r.client.TenantID(), settings)
	plan.readTags(ctx, settings, sent, &resp.Diagnostics)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// adoptTags puts the configured tags on a registry this Create ADOPTED: the
// opt-in that would have carried them was refused as already done. They are
// added to the tags the registry already holds — set in the portal, by the fm
// CLI, or merged from the tenant's defaults when it was enabled — never in
// place of them, the same rule every later update follows. It returns the
// settings to record and the set written (nil when no write was due).
func (r *containerRegistryResource) adoptTags(ctx context.Context, settings *apiRegistrySettings, desired map[string]string, diags *diag.Diagnostics) (*apiRegistrySettings, map[string]string) {
	if len(desired) == 0 {
		return settings, nil
	}
	if settings.TagsUnavailable {
		diags.AddError("Failed to tag the adopted container registry",
			"This tenant's registry already existed, so it was adopted — but its current tags could not be "+
				"read, so the configured tags were not written and nothing was recorded in state. Apply "+
				"again shortly.\n\nError: "+tftags.ErrOfferTagsUnavailable.Error())
		return nil, nil
	}
	current := tftags.OfferCustomerTags(settings.Tags)
	merged := make(map[string]string, len(current)+len(desired))
	for k, v := range current {
		merged[k] = v
	}
	for k, v := range desired {
		merged[k] = v
	}
	if tftags.Equal(merged, current) {
		return settings, nil
	}
	apiResp, err := r.client.Patch(ctx, r.client.TenantPath(settingsPath), apiUpdateTagsRequest{Tags: &merged})
	if err != nil {
		diags.AddError("Failed to tag the adopted container registry",
			"This tenant's registry already existed, so it was adopted — but writing the configured tags "+
				"failed, so nothing was recorded in state. Apply again.\n\nError: "+err.Error())
		return nil, nil
	}
	updated, err := client.ParseResponse[apiRegistrySettings](apiResp)
	if err != nil {
		diags.AddError("Failed to parse container registry response", err.Error())
		return nil, nil
	}
	if d := assertUsable(updated); d != nil {
		diags.Append(d)
		return nil, nil
	}
	return updated, merged
}

// assertUsable refuses to record a create that did not produce a usable
// registry. Without it a response that decoded to enabled:false — a race with a
// teardown, an envelope drift — would be written to state and reported as a
// successful apply, and the example's image-prefix output would silently render
// as "<endpoint>/".
func assertUsable(s *apiRegistrySettings) diag.Diagnostic {
	if s.Enabled == nil {
		return diag.NewErrorDiagnostic(
			"The container registry response did not say whether the registry is enabled",
			"The API answered without an `enabled` field, which the contract requires. Terraform will "+
				"not record a registry it cannot confirm exists.",
		)
	}
	if !s.isEnabled() || s.Namespace == "" {
		return diag.NewErrorDiagnostic(
			"The container registry was not created",
			"The API accepted the request but then reported no enabled registry with a namespace for "+
				"this tenant. Nothing was recorded in state. Check the registry's status with "+
				"`fm registry status` and apply again.",
		)
	}
	return nil
}

func (r *containerRegistryResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state ContainerRegistryModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	settings, err := r.read(ctx)
	if err != nil {
		if client.IsNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Failed to read container registry", err.Error())
		return
	}

	// `enabled: false` is the ANSWER for a tenant with no registry, not an
	// absence of one — the route answers 200 either way. So the disabled state,
	// not a 404, is what "gone" looks like here.
	//
	// A MISSING `enabled` is not that answer, and must never be read as one:
	// removing the resource is destructive to the plan, so an unreadable body
	// fails loudly instead.
	if settings.Enabled == nil {
		resp.Diagnostics.AddError(
			"The container registry response did not say whether the registry is enabled",
			"The API answered without an `enabled` field, which the contract requires. Terraform is "+
				"keeping the resource in state rather than treating an unreadable answer as a deletion.",
		)
		return
	}
	if !settings.isEnabled() {
		resp.State.RemoveResource(ctx)
		return
	}

	state.fromAPI(r.client.TenantID(), settings)
	state.readTags(ctx, settings, nil, &resp.Diagnostics)
	if !settings.TagsUnavailable {
		tftags.FinishRead(ctx, req.Private, resp.Private, r.client.DefaultTags(), &state.Tags, state.TagsAll, &resp.Diagnostics)
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Update writes the tags — the only thing about a registry that changes in
// place. The write is a tags-only PATCH of the settings (a row-only write: the
// namespace, its images and its credentials are untouched), sent only when the
// platform's set would change, and computed against the tags the registry holds
// right now so a key added outside Terraform is kept.
func (r *containerRegistryResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state ContainerRegistryModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	defaults := r.client.DefaultTags()
	tags, tagsErr := tftags.OfferTagsToWrite(ctx, defaults, plan.Tags, state.Tags, state.TagsAll, req.Private,
		func(ctx context.Context) (map[string]string, error) {
			current, err := r.read(ctx)
			if err != nil {
				return nil, err
			}
			if current.TagsUnavailable {
				return nil, tftags.ErrOfferTagsUnavailable
			}
			return tftags.OfferCustomerTags(current.Tags), nil
		}, &resp.Diagnostics)
	if tagsErr != nil {
		resp.Diagnostics.AddError("Failed to Read Current Tags", tftags.CurrentTagsReadFailed+tagsErr.Error())
		return
	}
	if resp.Diagnostics.HasError() {
		return
	}

	var settings *apiRegistrySettings
	var sent map[string]string
	if tags != nil {
		apiResp, err := r.client.Patch(ctx, r.client.TenantPath(settingsPath), apiUpdateTagsRequest{Tags: tags})
		if err != nil {
			resp.Diagnostics.AddError("Failed to update container registry tags", err.Error())
			return
		}
		if settings, err = client.ParseResponse[apiRegistrySettings](apiResp); err != nil {
			resp.Diagnostics.AddError("Failed to parse container registry response", err.Error())
			return
		}
		sent = *tags
	} else {
		var err error
		if settings, err = r.read(ctx); err != nil {
			resp.Diagnostics.AddError("Failed to read container registry", err.Error())
			return
		}
	}
	// The platform now holds the desired set, whether or not this update had to
	// write it.
	tftags.RecordDefaults(ctx, resp.Private, defaults, &resp.Diagnostics)

	if d := assertUsable(settings); d != nil {
		resp.Diagnostics.Append(d)
		return
	}
	plan.fromAPI(r.client.TenantID(), settings)
	plan.readTags(ctx, settings, sent, &resp.Diagnostics)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// ModifyPlan plans tags_all: a default_tags change, or a managed key's drift,
// plans an in-place update of the registry's tags.
func (r *containerRegistryResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	tftags.PlanTagsAll(ctx, r.client.DefaultTags(), req, resp)
	tftags.WarnOfferTagBudget(ctx, r.client.DefaultTags(), req, resp)
}

// Delete removes the resource from state WITHOUT deleting anything, and says so.
//
// There is no teardown endpoint to call: the deprovision RPC exists but has no
// caller and refuses outright for a namespace still holding repositories,
// because the repository purge is unbuilt. Silently returning success would be
// the worse failure — `terraform destroy` would report a destroy that did not
// happen, and the tenant would keep being billed for it.
func (r *containerRegistryResource) Delete(_ context.Context, _ resource.DeleteRequest, resp *resource.DeleteResponse) {
	resp.Diagnostics.AddWarning(
		"The Container Registry Was Removed From State But Still Exists",
		"Terraform has forgotten this registry, but nothing was deleted: the platform has no registry "+
			"teardown endpoint, and teardown belongs to tenant closure.\n\n"+
			"The namespace, every image in it, and its billable state all survive. Credentials minted "+
			"against it keep working unless they were revoked separately.\n\n"+
			"Re-adding the resource will adopt the same registry.",
	)
}

// read fetches the tenant's current registry state.
func (r *containerRegistryResource) read(ctx context.Context) (*apiRegistrySettings, error) {
	apiResp, err := r.client.Get(ctx, r.client.TenantPath(settingsPath), nil)
	if err != nil {
		return nil, err
	}
	return client.ParseResponse[apiRegistrySettings](apiResp)
}

// isAlreadyEnabled reports whether err is the 409 for a re-run opt-in.
func isAlreadyEnabled(err error) bool {
	var apiErr *client.APIError
	if !errors.As(err, &apiErr) {
		return false
	}
	reason, _ := apiErr.Details["reason"].(string)
	return reason == reasonAlreadyEnabled
}
