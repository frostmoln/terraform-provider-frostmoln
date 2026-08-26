package workload_identity_binding

import (
	"context"
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
			"Kubernetes (namespace, service account) to a least-privilege Frostmoln grant. " +
			"A pod running as that service account can exchange its projected token for a short-lived, " +
			"scoped Frostmoln credential. The grant is either flat `scopes` or an access policy attached " +
			"with `frostmoln_iam_policy_attachment` — a policy expresses far narrower least privilege " +
			"(per-resource targets, constraints, explicit denies), so prefer it for new bindings. " +
			"The binding is owned by the provider's selected tenant (`tenant_id`, else the " +
			"credential's default), and its `cluster_id` must belong to that same tenant.\n\n" +
			"~> **Upgrade note.** Before provider v0.38.0 this resource ignored `tenant_id` and " +
			"always acted on the credential's home tenant — a binding for a non-home cluster " +
			"failed at apply rather than landing in the wrong place. A configuration that sets " +
			"a non-home `tenant_id` and previously applied has its bindings in the HOME tenant: " +
			"the first plan after upgrading reads a 404, drops them from state and proposes a " +
			"create in the selected tenant. `terraform state rm` + re-`import` against the " +
			"selected tenant, or point `tenant_id` at the home tenant.",
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
				Description: "The least-privilege scopes granted to the workload (e.g. `compute:read`). " +
					"The grantable set, with a description of each, is served by the platform — read it " +
					"with the `frostmoln_api_key_scopes` data source or `fm account api-key scopes`. " +
					"Wildcards (`*` or `<resource>:*`) are rejected. Changing the scopes updates the " +
					"binding in place. Optional: omit it to author a **policy-granted** binding, whose " +
					"authority comes entirely from an access policy attached with " +
					"`frostmoln_iam_policy_attachment` (directly, or through a group). Write `null` or " +
					"omit the attribute for no scopes; an empty list is not a valid spelling.\n\n" +
					"Notes on the policy-granted path:\n" +
					"- The policy-granted path needs the `iam-policies` entitlement. Without it the " +
					"binding is still created, but `frostmoln_iam_policy` fails and the binding is left " +
					"inert.\n" +
					"- Until a policy is attached the binding is inert — the token exchange refuses it " +
					"rather than minting a credential that grants nothing.\n" +
					"- While a binding carries BOTH scopes and a policy, its scopes stay authoritative " +
					"and the policy's *additional* authority does not take effect. It goes live only " +
					"once `scopes` is dropped, so verify after the drop, not after the attach.\n" +
					"- Dropping `scopes` NARROWS the workload to whatever the policy allows, and the " +
					"API only checks that *some* grant survives — never that the policy covers what the " +
					"scopes covered. Enumerate the binding's effective access before dropping them.\n" +
					"- Removing the LAST grant is rejected outright. A binding whose only grant is an " +
					"attached policy must therefore be destroyed BEFORE that attachment; a plain " +
					"`terraform destroy` tries the attachment first and fails. Destroy the binding with " +
					"`-target` first — that removes its attachments with it.",
				Optional:    true,
				ElementType: types.StringType,
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

	apiResp, err := r.client.Post(ctx, basePath(r.client), apiReq)
	if err != nil {
		resp.Diagnostics.AddError("Failed to create workload identity binding", err.Error())
		return
	}

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
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *workloadIdentityBindingResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan WorkloadIdentityBindingModel
	var state WorkloadIdentityBindingModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// Only scopes are mutable; cluster_id/namespace/service_account force replace.
	updateReq := plan.toUpdateRequest(ctx, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}

	apiResp, err := r.client.Put(ctx, bindingPath(r.client, state.ID.ValueString()), updateReq)
	if err != nil {
		resp.Diagnostics.AddError("Failed to update workload identity binding", err.Error())
		return
	}

	// PUT returns the updated binding, so state is refreshed without a second GET.
	binding, err := client.ParseResponse[apiWorkloadIdentityBinding](apiResp)
	if err != nil {
		resp.Diagnostics.AddError("Failed to parse workload identity binding response", err.Error())
		return
	}

	// Preserve the planned scopes across the echo (see Create) — scopes is the
	// config-authoritative attribute the user just changed.
	plannedScopes := plan.Scopes
	plan.fromAPI(ctx, binding, &resp.Diagnostics)
	plan.Scopes = plannedScopes
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
}
