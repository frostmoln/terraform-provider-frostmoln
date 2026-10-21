package api_key

import (
	"context"
	"errors"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/docs"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/scopedecl"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/tftags"
)

var (
	_ resource.Resource                = &apiKeyResource{}
	_ resource.ResourceWithImportState = &apiKeyResource{}
	_ resource.ResourceWithModifyPlan  = &apiKeyResource{}
)

// NewResource returns a new API key resource factory.
func NewResource() resource.Resource {
	return &apiKeyResource{}
}

type apiKeyResource struct {
	client *client.Client
}

func (r *apiKeyResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_api_key"
}

func (r *apiKeyResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manages an API key in the Frostmoln platform." +
			"\n\n" +
			"~> **Upgrade note (scopes retirement).** Flat `scopes` no longer exist as a grant " +
			"language an API key can be given: the API refuses every non-empty value on create " +
			"and update with 400 `API_KEY_SCOPES_RETIRED`. Remove the attribute from your " +
			"configuration and grant the key with an access policy attached using " +
			"`frostmoln_iam_policy_attachment` instead. A migrated key's stored scopes stay " +
			"while `scopes` is omitted (unchanged server-side); to retire them too, set " +
			"`scopes = []` once — the API's clear arm — after the replacement policy is " +
			"attached and verified." +
			"\n\n" + scopedecl.Summary("frostmoln_api_key"),
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Description: "The unique identifier of the API key.",
				Computed:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"name": schema.StringAttribute{
				Description: "The name of the API key.",
				Required:    true,
			},
			"description": schema.StringAttribute{
				Description: "A description of the API key.",
				Optional:    true,
			},
			"scopes": schema.ListAttribute{
				// Deliberately NO emptyListNotAllowed validator and NO
				// ModifyPlan drop-warning, unlike the workload-identity binding.
				// There the PUT is a full replacement, so a configuration that
				// drops `scopes` EMPTIES the grant — hence the warning and the
				// refusal of `[]`. Here the PATCH reads an omitted scopes field
				// as UNCHANGED server-side: dropping the attribute narrows
				// nothing, and `scopes = []` is not an invalid spelling at all
				// but the API's one explicit clear arm. See the ModifyPlan note.
				Description: "RETIRED — flat scopes are no longer a grant language an API key can be " +
					"given. A create or update carrying any value is rejected by the API with 400 " +
					"API_KEY_SCOPES_RETIRED and the apply fails; the deprecation warning in the plan " +
					"already names the migration. " +
					"Remove the attribute from your configuration to stop sending scopes — on a " +
					"PATCH an omitted field is read server-side as UNCHANGED, so a migrated key's " +
					"stored scopes persist until you clear them explicitly by setting `scopes = []` " +
					"once (the API's clear arm; zero-scope creates are legal). " +
					"Grant the key with an access policy attached using `frostmoln_iam_policy_attachment` instead. " +
					"Kept Optional (never Required) so a carrying configuration migrates by deletion, " +
					"not by replacement, and pre-retirement state still reads.",
				Optional:    true,
				ElementType: types.StringType,
				DeprecationMessage: "scopes is retired: API-key scopes are no longer a grant language " +
					"and any value is rejected by the API with 400 API_KEY_SCOPES_RETIRED at apply. " +
					"Remove the attribute from your configuration and grant the key with an access " +
					"policy attached using frostmoln_iam_policy_attachment.",
			},
			"expires_at": schema.StringAttribute{
				Description: "The expiration for the API key. Accepts a bare date (`YYYY-MM-DD`, interpreted as the end of that day in UTC — e.g. `2027-01-01` becomes `2027-01-01T23:59:59Z`) or a full RFC3339 timestamp. The expiry must be at most 2 years in the future — the server rejects a later value with `API_KEY_LIFETIME_EXCEEDS_MAX`. Note: the fm CLI's `--expires` uses the operator's local timezone for the end-of-day, so the same bare date may resolve to an instant up to a day apart between the two tools; this provider uses UTC so plans are reproducible across machines. Omit to use the server default (~1 year), which is then reflected in state. Once set, this cannot be changed (changing it replaces the key).",
				Optional:    true,
				// Computed so the server's default (~1y) can populate state when
				// omitted without a "provider produced inconsistent result" error.
				Computed: true,
				PlanModifiers: []planmodifier.String{
					// Suppress a spurious diff (and RequiresReplace) when a bare
					// date equals the canonical state instant — e.g. after import.
					expiresAtSemanticEquality{},
					stringplanmodifier.UseStateForUnknown(),
					stringplanmodifier.RequiresReplace(),
				},
			},
			"rate_limit": schema.Int64Attribute{
				Description: "The rate limit for the API key (requests per minute). Omit to use the server default, which is then reflected in state.",
				Optional:    true,
				// Computed so the server-applied default populates state when
				// omitted (otherwise TF reports an inconsistent result).
				Computed: true,
				PlanModifiers: []planmodifier.Int64{
					int64planmodifier.UseStateForUnknown(),
				},
			},
			"key": schema.StringAttribute{
				Description: "The API key value. Only available after creation; not returned on subsequent reads. " +
					docs.StateSecretNote,
				Computed:  true,
				Sensitive: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"key_prefix": schema.StringAttribute{
				Description: "The prefix of the API key for identification.",
				Computed:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"status": schema.StringAttribute{
				Description: "The current status of the API key.",
				Computed:    true,
				// Keep the prior value on plan so a read-only, server-owned field
				// doesn't show a spurious "known after apply" update (e.g. after
				// import); a genuine status change is still picked up on refresh.
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"created_at": schema.StringAttribute{
				Description: "The timestamp when the API key was created.",
				Computed:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"tags": tftags.OfferTagsAttribute("Resource tags on the API key: your own key/value metadata for search " +
				"and filtering (`?tag=key=value` in the API, `--tag` in the fm CLI). Labels only — they change no " +
				"scope, expiry or rate limit, and a tags-only change is accepted even on a revoked or expired key."),
			"tags_all": tftags.TagsAllAttribute(),
		},
	}
}

// scopesRetiredErrorMessage renders the API-key scopes retirement's 400 as the
// migration it is, instead of the bare "<code>: <message>" passthrough — the
// api-key twin of the workload-identity binding's BINDING_SCOPES_RETIRED branch.
// A create or PATCH that still carries a non-empty `scopes` fails at APPLY with
// this (the plan itself only WARNS, via the schema DeprecationMessage — a
// warning alone must never be load-bearing). Unlike the binding, the refusal is
// about the ARGUMENT only: a migrated key's stored scopes keep working, and the
// PATCH's clear arm (`scopes = []`) is how Terraform retires them.
func scopesRetiredErrorMessage(err error) (string, bool) {
	var apiErr *client.APIError
	if !errors.As(err, &apiErr) || apiErr.Code != "API_KEY_SCOPES_RETIRED" {
		return "", false
	}
	return "Flat scopes are retired on API keys: the API now refuses every non-empty " +
		"`scopes` value on create and update with 400 API_KEY_SCOPES_RETIRED.\n\n" +
		"Grant the key with an access policy attached using frostmoln_iam_policy_attachment " +
		"instead: attach a policy carrying the operations the key actually used, verify the key " +
		"still authenticates and is authorized as before, then apply the configuration with " +
		"`scopes` removed. An omitted `scopes` attribute sends no scopes at all, and the server " +
		"leaves a migrated key's stored scopes UNCHANGED; to clear a migrated key's stored scopes " +
		"explicitly, set `scopes = []` once — the API's clear arm — then remove the attribute " +
		"(keeping `[]` is harmless: it clears again).", true
}

// ModifyPlan plans tags_all: a default_tags change, or a managed key's drift,
// plans an in-place update of the key's tags.
//
// Deliberately NO drop-direction scopes warning, unlike the workload-identity
// binding — the PATCH/PUT asymmetry. There the PUT carries `scopes` as a FULL
// replacement, so a configuration that silently drops the attribute empties the
// binding's grant, and the plan is the last place to say so. Here the PATCH
// reads an omitted scopes field as UNCHANGED server-side: a configuration that
// drops the attribute narrows nothing, so a drop-warning would cry wolf on every
// deprecation migration. Narrowing happens only when the practitioner spells
// `scopes = []` — a deliberate, visible act (the API's clear arm), not a silent
// side effect of deleting a line.
func (r *apiKeyResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	tftags.PlanTagsAll(ctx, r.client.DefaultTags(), req, resp)
	tftags.WarnOfferTagBudget(ctx, r.client.DefaultTags(), req, resp)
}

func (r *apiKeyResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (r *apiKeyResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan APIKeyModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	apiReq := plan.toCreateRequest(ctx, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	defaults := r.client.DefaultTags()
	apiReq.Tags = tftags.ForCreate(ctx, defaults, plan.Tags, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}

	apiResp, err := r.client.Post(ctx, r.client.TenantPath("/api-keys"), apiReq)
	if err != nil {
		if msg, retired := scopesRetiredErrorMessage(err); retired {
			resp.Diagnostics.AddError("API key scopes are retired (API_KEY_SCOPES_RETIRED)", msg)
			return
		}
		resp.Diagnostics.AddError("Failed to create API key", err.Error())
		return
	}
	tftags.RecordDefaults(ctx, resp.Private, defaults, &resp.Diagnostics)

	// Create returns an envelope {apiKey: {...}, key: "..."}, unlike Read which
	// returns a bare key object.
	created, err := client.ParseResponse[apiCreateAPIKeyResponse](apiResp)
	if err != nil {
		resp.Diagnostics.AddError("Failed to parse API key response", err.Error())
		return
	}
	key := &created.APIKey

	// Save the key value from the create response (only available once).
	if created.Key != "" {
		plan.Key = types.StringValue(created.Key)
	} else {
		plan.Key = types.StringNull()
	}

	plan.fromAPI(ctx, key, &resp.Diagnostics)
	plan.tagsFromAPI(ctx, key, &resp.Diagnostics)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *apiKeyResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state APIKeyModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// Preserve the key value from state (API does not return it on reads).
	savedKey := state.Key

	keyPath, err := r.apiKeyPath(state.ID.ValueString(), "")
	if err != nil {
		resp.Diagnostics.AddError("Invalid API key ID", err.Error())
		return
	}
	apiResp, err := r.client.Get(ctx, keyPath, nil)
	if err != nil {
		if client.IsNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Failed to read API key", err.Error())
		return
	}

	key, err := client.ParseResponse[apiAPIKey](apiResp)
	if err != nil {
		resp.Diagnostics.AddError("Failed to parse API key response", err.Error())
		return
	}

	state.fromAPI(ctx, key, &resp.Diagnostics)
	state.tagsFromAPI(ctx, key, &resp.Diagnostics)
	tftags.FinishRead(ctx, req.Private, resp.Private, r.client.DefaultTags(), &state.Tags, state.TagsAll, &resp.Diagnostics)

	// Restore the key value from prior state.
	state.Key = savedKey

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *apiKeyResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan APIKeyModel
	var state APIKeyModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	id := state.ID.ValueString()

	// Preserve the key value from state.
	plan.Key = state.Key

	updateReq := plan.toUpdateRequest(ctx, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}

	keyPath, err := r.apiKeyPath(id, "")
	if err != nil {
		resp.Diagnostics.AddError("Invalid API key ID", err.Error())
		return
	}
	// The tag set to write, or nil when the platform's needs no change —
	// computed against the tags the key holds right now, so a key added outside
	// Terraform since the last refresh is kept.
	defaults := r.client.DefaultTags()
	tags, err := tftags.OfferTagsToWrite(ctx, defaults, plan.Tags, state.Tags, state.TagsAll, req.Private,
		func(ctx context.Context) (map[string]string, error) {
			cur, err := r.client.Get(ctx, keyPath, nil)
			if err != nil {
				return nil, err
			}
			key, err := client.ParseResponse[apiAPIKey](cur)
			if err != nil {
				return nil, err
			}
			return tftags.OfferCustomerTags(key.Tags), nil
		}, &resp.Diagnostics)
	if err != nil {
		resp.Diagnostics.AddError("Failed to Read Current Tags", tftags.CurrentTagsReadFailed+err.Error())
		return
	}
	if resp.Diagnostics.HasError() {
		return
	}
	switch {
	case onlyTagsDiffer(&plan, &state) && tags != nil:
		// Tags alone: the API accepts this on a revoked or expired key too.
		_, err = r.client.Patch(ctx, keyPath, apiUpdateAPIKeyRequest{Tags: tags})
	case onlyTagsDiffer(&plan, &state):
		// Nothing to write (a default_tags change the platform already matches).
	default:
		updateReq.Tags = tags
		_, err = r.client.Patch(ctx, keyPath, updateReq)
	}
	if err != nil {
		if msg, retired := scopesRetiredErrorMessage(err); retired {
			resp.Diagnostics.AddError("API key scopes are retired (API_KEY_SCOPES_RETIRED)", msg)
			return
		}
		resp.Diagnostics.AddError("Failed to update API key", err.Error())
		return
	}
	tftags.RecordDefaults(ctx, resp.Private, defaults, &resp.Diagnostics)

	// Refresh state from API.
	apiResp, err := r.client.Get(ctx, keyPath, nil)
	if err != nil {
		resp.Diagnostics.AddError("Failed to read API key after update", err.Error())
		return
	}

	key, err := client.ParseResponse[apiAPIKey](apiResp)
	if err != nil {
		resp.Diagnostics.AddError("Failed to parse API key response", err.Error())
		return
	}

	plan.fromAPI(ctx, key, &resp.Diagnostics)
	plan.tagsFromAPI(ctx, key, &resp.Diagnostics)

	// Restore the key value from state.
	plan.Key = state.Key

	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *apiKeyResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state APIKeyModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	keyPath, err := r.apiKeyPath(state.ID.ValueString(), "")
	if err != nil {
		resp.Diagnostics.AddError("Invalid API key ID", err.Error())
		return
	}
	_, err = r.client.Delete(ctx, keyPath)
	if err != nil {
		if client.IsNotFound(err) {
			return
		}
		resp.Diagnostics.AddError("Failed to delete API key", err.Error())
	}
}

func (r *apiKeyResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	// NOT ImportStatePassthroughID: that writes the practitioner's string
	// straight into `id`, and `id` becomes a URL path segment. See apiKeyPath.
	parts, err := client.ParseImportID(req.ID, "api_key_id")
	if err != nil {
		resp.Diagnostics.AddError("Invalid import ID", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), parts[0])...)
	tftags.MarkImported(ctx, resp.Private, &resp.Diagnostics)
}

// apiKeyPath builds the tenant-scoped path for one API key, optionally with a
// sub-action suffix such as "/revoke".
//
// 🔴 THE ID IS VALIDATED, AND THE DIFFERENCE IS A DESTROYED RESOURCE. Client.do
// finishes URL assembly with path.Join, which CLEANS dot segments, and
// TenantPath escapes only the TENANT — the subpath is interpolated raw.
// url.PathEscape would not help anyway: "." and ".." are unreserved, so there is
// nothing for it to escape. `id` is Computed, so the practitioner-controlled
// entry points are `terraform import` and a supplied or edited state file (a
// shared module, a CI pipeline that builds an import block, tampered remote
// state). With an id of "../instances/<uuid>" the DELETE that Terraform issues
// on destroy lands on /api/v1/tenants/{t}/instances/<uuid> — a registered route,
// same verb, authorized by the practitioner's own credential — and the plan says
// "api key" throughout. Three more dot segments reach /api/v1/me, identity's
// self-service account delete.
//
// Moving these routes under /v1/tenants/{tid}/ only added segments to climb; it
// did not close this. Refusing the value does, which is what ParseImportID's
// single-segment form is for — it rejects "", ".", ".." and anything containing
// a slash. One builder for every id-bearing call so a new sub-action cannot skip
// the guard. Same shape as fm-cli's account.apiKeyPath.
func (r *apiKeyResource) apiKeyPath(id, suffix string) (string, error) {
	if _, err := client.ParseImportID(id, "api_key_id"); err != nil {
		return "", err
	}
	return r.client.TenantPath("/api-keys/" + id + suffix), nil
}
