package workload_identity_binding

import (
	"context"
	"errors"
	"fmt"
	"net/url"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/scopedecl"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/tftags"
)

var (
	_ resource.Resource                = &workloadIdentityBindingResource{}
	_ resource.ResourceWithImportState = &workloadIdentityBindingResource{}
	_ resource.ResourceWithModifyPlan  = &workloadIdentityBindingResource{}
	_ validator.List                   = emptyListNotAllowed{}
)

// emptyListNotAllowed rejects `scopes = []`. listvalidator.SizeAtLeast(1) does
// the same check, but its message ("list must contain at least 1 elements") reads
// as if the attribute were mandatory — the exact wrong thing to tell someone
// reaching for the policy-granted shape, and the intuitive spelling for it.
type emptyListNotAllowed struct{}

func (emptyListNotAllowed) Description(context.Context) string {
	return "must not be an empty list; omit the attribute for no scopes"
}

func (v emptyListNotAllowed) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}

func (emptyListNotAllowed) ValidateList(_ context.Context, req validator.ListRequest, resp *validator.ListResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() || len(req.ConfigValue.Elements()) > 0 {
		return
	}
	resp.Diagnostics.AddAttributeError(
		req.Path,
		"Empty scopes list",
		"An empty list is not a valid way to say \"no scopes\". Omit the attribute (or set it "+
			"to null) to author a policy-granted binding, whose authority comes from an access "+
			"policy attached with frostmoln_iam_policy_attachment.",
	)
}

// basePath is tenant-scoped, like every other resource in this provider. The
// untenanted form resolves the tenant from the caller's auth context — their
// HOME tenant — so under it the provider's tenant_id was silently ignored and a
// binding could never be created in another tenant. That mattered more here than
// elsewhere: a binding also requires its cluster to be in the same tenant, so
// the mismatch surfaced as an apply-time failure rather than a quiet misplace.
func basePath(c *client.Client) string {
	return c.TenantPath("/workload-identity/bindings")
}

// scopesRetiredErrorMessage renders the scopes retirement's 400 as the migration
// it is, instead of the bare "<code>: <message>" passthrough — same house shape
// as the attachment resource's BINDING_WOULD_HAVE_NO_GRANT branch. A plan that
// still carries `scopes` fails at APPLY with this (the plan itself only WARNS,
// via the schema DeprecationMessage — a warning alone must never be load-bearing).
func scopesRetiredErrorMessage(err error) (string, bool) {
	var apiErr *client.APIError
	if !errors.As(err, &apiErr) || apiErr.Code != "BINDING_SCOPES_RETIRED" {
		return "", false
	}
	return "Flat scopes are retired on workload-identity bindings (retire-scopes step 2, ADR-0102): " +
		"a binding's grant is its access policies, and the API now refuses every non-empty `scopes` " +
		"value with 400 BINDING_SCOPES_RETIRED.\n\n" +
		"Remove the `scopes` attribute from the configuration (the deprecation warning in the plan " +
		"naming it is expected) and grant the binding with an access policy attached using " +
		"frostmoln_iam_policy_attachment. For a pre-retirement binding that still carries scopes, " +
		"migrate it per binding: attach a policy carrying the same operations first, verify the " +
		"workload still passes, then apply the configuration with `scopes` removed — the removal " +
		"is guarded server-side (BINDING_WOULD_HAVE_NO_GRANT) so the binding is never left with " +
		"no grant at all.", true
}

// bindingPath returns the path for a single binding, escaping the id so it can
// only ever be a single path segment.
func bindingPath(c *client.Client, id string) string {
	return basePath(c) + "/" + url.PathEscape(id)
}

// NewResource returns a new workload identity binding resource factory.
func NewResource() resource.Resource {
	return &workloadIdentityBindingResource{}
}

type workloadIdentityBindingResource struct {
	client *client.Client
}

func (r *workloadIdentityBindingResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_workload_identity_binding"
}

func (r *workloadIdentityBindingResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manages a Workload Identity Federation binding, mapping a managed " +
			"Kubernetes (namespace, service account) to a Frostmoln grant. " +
			"A pod running as that service account can exchange its projected token for a short-lived, " +
			"policy-scoped Frostmoln credential. The binding's grant is its access policies — attach " +
			"one with `frostmoln_iam_policy_attachment` (directly, or through a group). Flat `scopes` " +
			"are retired: a configuration that still sets them fails at apply with `BINDING_SCOPES_RETIRED`. " +
			"The binding is owned by the provider's selected tenant (`tenant_id`, else the " +
			"credential's default), and its `cluster_id` must belong to that same tenant.\n\n" +
			"~> **Upgrade note.** Before provider v0.37.9 this resource ignored `tenant_id` and " +
			"always acted on the credential's home tenant — a binding for a non-home cluster " +
			"failed at apply rather than landing in the wrong place. A configuration that sets " +
			"a non-home `tenant_id` and previously applied has its bindings in the HOME tenant: " +
			"the first plan after upgrading reads a 404, drops them from state and proposes a " +
			"create in the selected tenant. `terraform state rm` + re-`import` against the " +
			"selected tenant, or point `tenant_id` at the home tenant." +
			"\n\n" +
			"~> **Upgrade note (scopes retirement, retire-scopes step 2).** Flat `scopes` no longer " +
			"exist as a grant language. Remove the attribute from your configuration and grant the " +
			"binding with an access policy instead; the state normalizes to null (the server always " +
			"answers `scopes: []`). A carrying configuration fails at apply — see the `scopes` " +
			"attribute. To migrate an existing pre-retirement binding: attach a policy carrying the " +
			"same operations, verify the workload still passes, then apply the configuration with " +
			"`scopes` removed (that drop is the migration landing the server guards)." +
			"\n\n" + scopedecl.Summary("frostmoln_workload_identity_binding"),
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Description: "The unique identifier of the binding.",
				Computed:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"cluster_id": schema.StringAttribute{
				Description: "The managed cluster the binding applies to. The cluster must belong to the selected tenant (the provider's tenant_id).",
				Required:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"namespace": schema.StringAttribute{
				Description: "The Kubernetes namespace (DNS-1123 label).",
				Required:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"service_account": schema.StringAttribute{
				Description: "The Kubernetes service account name (DNS-1123 subdomain).",
				Required:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"scopes": schema.ListAttribute{
				Description: "RETIRED — flat scopes are no longer a grant language a binding can be " +
					"given. A create or update carrying any value is rejected by the API with 400 " +
					"BINDING_SCOPES_RETIRED and the apply fails; the deprecation warning in the plan " +
					"already names the migration. A binding's grant is its access policies: attach " +
					"one with `frostmoln_iam_policy_attachment` (directly, or through a group). " +
					"Remove the attribute from your configuration — state normalizes to null (the " +
					"server always answers `scopes: []`). An empty list remains an invalid spelling: " +
					"omit the attribute instead. Kept Optional (never Required) so a carrying " +
					"configuration migrates by deletion, not by replacement, and pre-retirement " +
					"state still reads.",
				Optional:    true,
				ElementType: types.StringType,
				DeprecationMessage: "scopes is retired: binding scopes are no longer a grant language " +
					"and any value is rejected by the API with 400 BINDING_SCOPES_RETIRED at apply. " +
					"Remove the attribute from your configuration and grant the binding with an access " +
					"policy attached using frostmoln_iam_policy_attachment.",
				Validators: []validator.List{
					// Rejects `scopes = []`. Omitted/null is the ONE spelling for
					// "no flat scopes", so state can be normalized to null in
					// fromAPI without an unconvergeable [] vs null diff.
					emptyListNotAllowed{},
				},
			},
			"tenant_id": schema.StringAttribute{
				Description: "The owning tenant (server-set; the provider's tenant_id selects it).",
				Computed:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"created_at": schema.StringAttribute{
				Description: "The timestamp when the binding was created.",
				Computed:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"updated_at": schema.StringAttribute{
				Description: "The timestamp when the binding was last updated.",
				Computed:    true,
			},
			"tags": tftags.OfferTagsAttribute("Resource tags on the binding: your own key/value metadata for search " +
				"and filtering (`?tag=key=value` in the API, `--tag` in the fm CLI). Labels only — the token exchange " +
				"never reads them, and a tags change (its own tags-only update) never touches the binding's scopes."),
			"tags_all": tftags.TagsAllAttribute(),
		},
	}
}

func (r *workloadIdentityBindingResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

// ModifyPlan warns before an apply that drops a live binding's scopes. Nothing
// downstream can catch this for the practitioner: the API only refuses the
// removal when NO grant would survive, so the far more common case — a policy
// remains, but a narrower one — succeeds silently and the workload starts 403ing
// at its next token mint. ADR-0102 requires that move to be measured per binding,
// so the plan is the last place to say so before it happens.
func (r *workloadIdentityBindingResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	// tags_all: unknown whenever the next write changes the platform's tag set,
	// including a change to the provider's default_tags.
	tftags.PlanTagsAll(ctx, r.client.DefaultTags(), req, resp)
	tftags.WarnOfferTagBudget(ctx, r.client.DefaultTags(), req, resp)
	if resp.Diagnostics.HasError() {
		return
	}

	// Create (no prior state) and destroy (no plan) are not this transition.
	if req.State.Raw.IsNull() || req.Plan.Raw.IsNull() {
		return
	}

	var state, plan WorkloadIdentityBindingModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if state.Scopes.IsNull() || !plan.Scopes.IsNull() {
		return
	}

	resp.Diagnostics.AddAttributeWarning(
		path.Root("scopes"),
		"Dropping a workload identity binding's scopes narrows it",
		"This binding currently grants its workload through flat scopes, and the plan removes "+
			"them. Its access afterwards is exactly what its attached access policies allow — "+
			"which is NOT checked against what the scopes granted. Anything the scopes covered "+
			"and the policy does not will start failing with 403 at the workload's next token "+
			"exchange.\n\n"+
			"Enumerate the binding's effective access before applying (ADR-0102 requires this "+
			"move to be measured per binding).\n\n"+
			"If no grant at all would survive, the apply is REJECTED rather than leaving a "+
			"deny-all binding. To remove a workload's authority entirely, destroy the binding — "+
			"emptying its scopes cannot express that.",
	)
}

func (r *workloadIdentityBindingResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan WorkloadIdentityBindingModel
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

	apiResp, err := r.client.Post(ctx, basePath(r.client), apiReq)
	if err != nil {
		if msg, retired := scopesRetiredErrorMessage(err); retired {
			resp.Diagnostics.AddError("Workload identity binding scopes are retired (BINDING_SCOPES_RETIRED)", msg)
			return
		}
		resp.Diagnostics.AddError("Failed to create workload identity binding", err.Error())
		return
	}
	tftags.RecordDefaults(ctx, resp.Private, defaults, &resp.Diagnostics)

	binding, err := client.ParseResponse[apiWorkloadIdentityBinding](apiResp)
	if err != nil {
		resp.Diagnostics.AddError("Failed to parse workload identity binding response", err.Error())
		return
	}

	// scopes is config-authoritative: keep the planned value so a server-side
	// reorder/dedup of the echoed scopes — or a `[]` echo where the plan said
	// null — can't trip "provider produced inconsistent result after apply".
	// Genuine server-side divergence surfaces as drift in Read, not here.
	plannedScopes := plan.Scopes
	plan.fromAPI(ctx, binding, &resp.Diagnostics)
	plan.Scopes = plannedScopes
	plan.tagsFromAPI(ctx, binding, &resp.Diagnostics)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *workloadIdentityBindingResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state WorkloadIdentityBindingModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	apiResp, err := r.client.Get(ctx, bindingPath(r.client, state.ID.ValueString()), nil)
	if err != nil {
		if client.IsNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Failed to read workload identity binding", err.Error())
		return
	}

	binding, err := client.ParseResponse[apiWorkloadIdentityBinding](apiResp)
	if err != nil {
		resp.Diagnostics.AddError("Failed to parse workload identity binding response", err.Error())
		return
	}

	state.fromAPI(ctx, binding, &resp.Diagnostics)
	state.tagsFromAPI(ctx, binding, &resp.Diagnostics)
	tftags.FinishRead(ctx, req.Private, resp.Private, r.client.DefaultTags(), &state.Tags, state.TagsAll, &resp.Diagnostics)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// get reads one binding.
func (r *workloadIdentityBindingResource) get(ctx context.Context, id string) (*apiWorkloadIdentityBinding, error) {
	apiResp, err := r.client.Get(ctx, bindingPath(r.client, id), nil)
	if err != nil {
		return nil, err
	}
	return client.ParseResponse[apiWorkloadIdentityBinding](apiResp)
}

func (r *workloadIdentityBindingResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan WorkloadIdentityBindingModel
	var state WorkloadIdentityBindingModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// Scopes and tags are mutable (cluster_id/namespace/service_account force
	// replace), each through its own verb: PUT for the scopes (scope cap, grant
	// guard), the tags-only PATCH for the tags (which touches nothing else).
	id := state.ID.ValueString()
	defaults := r.client.DefaultTags()
	tags, err := tftags.OfferTagsToWrite(ctx, defaults, plan.Tags, state.Tags, state.TagsAll, req.Private,
		func(ctx context.Context) (map[string]string, error) {
			cur, err := r.get(ctx, id)
			if err != nil {
				return nil, err
			}
			return tftags.OfferCustomerTags(cur.Tags), nil
		}, &resp.Diagnostics)
	if err != nil {
		resp.Diagnostics.AddError("Failed to Read Current Tags", tftags.CurrentTagsReadFailed+err.Error())
		return
	}
	if resp.Diagnostics.HasError() {
		return
	}

	// Each write answers with the binding as it now stands, so the last one is
	// the state; with no write at all (a default_tags change the platform
	// already matches) the binding is read.
	var binding *apiWorkloadIdentityBinding
	if !plan.Scopes.Equal(state.Scopes) {
		updateReq := plan.toUpdateRequest(ctx, &resp.Diagnostics)
		if resp.Diagnostics.HasError() {
			return
		}
		apiResp, err := r.client.Put(ctx, bindingPath(r.client, id), updateReq)
		if err != nil {
			if msg, retired := scopesRetiredErrorMessage(err); retired {
				resp.Diagnostics.AddError("Workload identity binding scopes are retired (BINDING_SCOPES_RETIRED)", msg)
				return
			}
			resp.Diagnostics.AddError("Failed to update workload identity binding", err.Error())
			return
		}
		if binding, err = client.ParseResponse[apiWorkloadIdentityBinding](apiResp); err != nil {
			resp.Diagnostics.AddError("Failed to parse workload identity binding response", err.Error())
			return
		}
	}
	if tags != nil {
		apiResp, err := r.client.Patch(ctx, bindingPath(r.client, id), apiUpdateTagsRequest{Tags: tags})
		if err != nil {
			resp.Diagnostics.AddError("Failed to update workload identity binding tags", err.Error())
			return
		}
		if binding, err = client.ParseResponse[apiWorkloadIdentityBinding](apiResp); err != nil {
			resp.Diagnostics.AddError("Failed to parse workload identity binding response", err.Error())
			return
		}
	}
	tftags.RecordDefaults(ctx, resp.Private, defaults, &resp.Diagnostics)
	if binding == nil {
		if binding, err = r.get(ctx, id); err != nil {
			resp.Diagnostics.AddError("Failed to read workload identity binding after update", err.Error())
			return
		}
	}

	// Preserve the planned scopes across the echo (see Create) — scopes is the
	// config-authoritative attribute the user just changed.
	plannedScopes := plan.Scopes
	plan.fromAPI(ctx, binding, &resp.Diagnostics)
	plan.Scopes = plannedScopes
	plan.tagsFromAPI(ctx, binding, &resp.Diagnostics)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *workloadIdentityBindingResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state WorkloadIdentityBindingModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	_, err := r.client.Delete(ctx, bindingPath(r.client, state.ID.ValueString()))
	if err != nil {
		if client.IsNotFound(err) {
			return
		}
		resp.Diagnostics.AddError("Failed to delete workload identity binding", err.Error())
	}
}

func (r *workloadIdentityBindingResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
	tftags.MarkImported(ctx, resp.Private, &resp.Diagnostics)
}
