package api_key

import (
	"context"
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
)

var (
	_ resource.Resource                = &apiKeyResource{}
	_ resource.ResourceWithImportState = &apiKeyResource{}
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
		Description: "Manages an API key in the Frostmoln platform.",
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
				Description: "The permission scopes granted to the API key, as `<service>:<action>` strings (e.g. `compute:read`, `storage:write`). " +
					"At least one is required, and a key can never exceed the permissions of the identity that created it. " +
					"Grant `:read` and `:write` — those are the actions the services enforce, and `:write` covers every mutation including start/stop/restart. " +
					"The global `*` wildcard is REJECTED (`WILDCARD_SCOPE_NOT_ALLOWED`) — keys are least-privilege — but a per-service wildcard such as `compute:*` is accepted. " +
					"The grantable set with a description of each is served by the platform: read it with the `frostmoln_api_key_scopes` data source, or `fm account api-key scopes`. " +
					"NOTE: identity also ACCEPTS the finer `service:resource:action` form here, but no service ENFORCES it as a key scope — a key holding only those applies cleanly and is then denied on every call. " +
					"For per-resource targets, constraints or explicit denies, attach an access policy with `frostmoln_iam_policy_attachment` instead.",
				Optional:    true,
				ElementType: types.StringType,
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
		},
	}
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

	apiResp, err := r.client.Post(ctx, r.client.TenantPath("/api-keys"), apiReq)
	if err != nil {
		resp.Diagnostics.AddError("Failed to create API key", err.Error())
		return
	}

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
	_, err = r.client.Patch(ctx, keyPath, updateReq)
	if err != nil {
		resp.Diagnostics.AddError("Failed to update API key", err.Error())
		return
	}

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
