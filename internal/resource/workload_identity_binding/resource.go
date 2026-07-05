package workload_identity_binding

import (
	"context"
	"fmt"
	"net/url"

	"github.com/hashicorp/terraform-plugin-framework-validators/listvalidator"
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
)

// basePath is NOT tenant-scoped: the binding endpoints resolve the tenant from
// the API key, so we must not use client.TenantPath here (it would inject
// /v1/tenants/{id}/, which these routes reject).
const basePath = "/v1/workload-identity/bindings"

// bindingPath returns the path for a single binding, escaping the id so it can
// only ever be a single path segment.
func bindingPath(id string) string {
	return basePath + "/" + url.PathEscape(id)
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
		Description: "Manages a Workload Identity Federation binding (ADR-0095), mapping a managed " +
			"Kubernetes (namespace, service account) to a set of least-privilege Frostmoln scopes. " +
			"A pod running as that service account can exchange its projected token for a short-lived, " +
			"scoped Frostmoln credential. The binding is owned by the tenant resolved from the " +
			"provider credential (API key or OIDC session), not by a provider `tenant_id` override.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Description: "The unique identifier of the binding.",
				Computed:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"cluster_id": schema.StringAttribute{
				Description: "The managed cluster the binding applies to. The cluster must belong to the caller's tenant.",
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
					"At least one is required; wildcards (`*` or `<resource>:*`) are rejected. Changing " +
					"the scopes updates the binding in place.",
				Required:    true,
				ElementType: types.StringType,
				Validators: []validator.List{
					listvalidator.SizeAtLeast(1),
				},
			},
			"tenant_id": schema.StringAttribute{
				Description: "The owning tenant (server-set from the auth context).",
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

	apiResp, err := r.client.Post(ctx, basePath, apiReq)
	if err != nil {
		resp.Diagnostics.AddError("Failed to create workload identity binding", err.Error())
		return
	}

	binding, err := client.ParseResponse[apiWorkloadIdentityBinding](apiResp)
	if err != nil {
		resp.Diagnostics.AddError("Failed to parse workload identity binding response", err.Error())
		return
	}

	// scopes is a Required (config-authoritative) attribute: keep the planned
	// value so a hypothetical server-side reorder/dedup of the echoed scopes
	// can't trip "provider produced inconsistent result after apply". Genuine
	// server-side divergence surfaces as drift in Read, not here.
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

	apiResp, err := r.client.Get(ctx, bindingPath(state.ID.ValueString()), nil)
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

	apiResp, err := r.client.Put(ctx, bindingPath(state.ID.ValueString()), updateReq)
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
	// config-authoritative Required attribute the user just changed.
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

	_, err := r.client.Delete(ctx, bindingPath(state.ID.ValueString()))
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
