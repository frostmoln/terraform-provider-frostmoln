package iam_policy

import (
	"context"
	"fmt"
	"net/url"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
)

var (
	_ resource.Resource                = &iamPolicyResource{}
	_ resource.ResourceWithImportState = &iamPolicyResource{}
)

// basePath is tenant-scoped, exactly like every other resource in this provider.
// It must be: the untenanted /v1/iam/policies form resolves the tenant from the
// caller's auth context, which is the OIDC token's HOME tenant — so under that
// form the provider's tenant_id was silently ignored and every policy landed in
// the caller's home tenant. The /v1/tenants/{id}/ form makes the gateway
// re-scope the auth context to the selected tenant before identity sees it.
func basePath(c *client.Client) string {
	return c.TenantPath("/iam/policies")
}

// policyPath returns the path for a single policy, escaping the id so it can only
// ever be one path segment.
func policyPath(c *client.Client, id string) string {
	return basePath(c) + "/" + url.PathEscape(id)
}

// NewResource returns a new IAM policy resource factory.
func NewResource() resource.Resource {
	return &iamPolicyResource{}
}

type iamPolicyResource struct {
	client *client.Client
}

func (r *iamPolicyResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_iam_policy"
}

func (r *iamPolicyResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manages a reusable IAM access policy. A policy is a set of " +
			"allow/deny rules over operations, targets (FRNs) and constraints; attach it to an " +
			"API key, workload identity, or group with `frostmoln_iam_policy_attachment`. The " +
			"policy is owned by the provider's selected tenant (`tenant_id`, else the " +
			"credential's default). Authoring requires an fm CLI / OIDC session — a machine " +
			"token may not author or attach a policy.\n\n" +
			"~> **Upgrade note.** Before provider v0.38.0 this resource ignored `tenant_id` and " +
			"always acted on the credential's home tenant. If your configuration sets a " +
			"non-home `tenant_id`, existing policies live in the HOME tenant: the first plan " +
			"after upgrading reads a 404, drops them from state and proposes a create in the " +
			"selected tenant, leaving the originals orphaned and no longer managed. Either " +
			"`terraform state rm` + re-`import` them against the selected tenant, or point " +
			"`tenant_id` at the home tenant to keep managing them where they are.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Description: "The unique identifier of the policy.",
				Computed:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"name": schema.StringAttribute{
				Description: "The name of the policy.",
				Required:    true,
			},
			"description": schema.StringAttribute{
				Description: "A description of the policy.",
				Optional:    true,
			},
			"document": schema.StringAttribute{
				Description: "The access-policy document as a JSON string " +
					"(`{\"schemaVersion\":\"1\",\"rules\":[...]}`). Compose it with the " +
					"`frostmoln_iam_policy_document` data source rather than hand-writing it. " +
					"The server validates operations against the append-only catalog, requires " +
					"the region segment of every target FRN to be `*` (region-scoped targets are " +
					"not supported yet), and rejects a malformed document. A hand-written or " +
					"`file()`-loaded document is only checked at apply time; the data source " +
					"catches these at plan time.",
				Required: true,
			},
			"tenant_id": schema.StringAttribute{
				Description: "The owning tenant (server-set; the provider's tenant_id selects it).",
				Computed:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"authored_by": schema.StringAttribute{
				Description: "The user that authored the policy (server-set).",
				Computed:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"version": schema.Int64Attribute{
				Description: "The policy version, incremented by the server on each update.",
				Computed:    true,
			},
			"created_at": schema.StringAttribute{
				Description: "The timestamp when the policy was created.",
				Computed:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"updated_at": schema.StringAttribute{
				Description: "The timestamp when the policy was last updated.",
				Computed:    true,
			},
		},
	}
}

func (r *iamPolicyResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (r *iamPolicyResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan IAMPolicyModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	apiReq := plan.toCreateRequest(&resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}

	apiResp, err := r.client.Post(ctx, basePath(r.client), apiReq)
	if err != nil {
		resp.Diagnostics.AddError("Failed to create IAM policy", err.Error())
		return
	}

	p, err := client.ParseResponse[apiPolicy](apiResp)
	if err != nil {
		resp.Diagnostics.AddError("Failed to parse IAM policy response", err.Error())
		return
	}

	plan.fromAPI(p)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *iamPolicyResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state IAMPolicyModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	apiResp, err := r.client.Get(ctx, policyPath(r.client, state.ID.ValueString()), nil)
	if err != nil {
		if client.IsNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Failed to read IAM policy", err.Error())
		return
	}

	p, err := client.ParseResponse[apiPolicy](apiResp)
	if err != nil {
		resp.Diagnostics.AddError("Failed to parse IAM policy response", err.Error())
		return
	}

	state.fromAPI(p)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *iamPolicyResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan IAMPolicyModel
	var state IAMPolicyModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	apiReq := plan.toUpdateRequest(&resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}

	apiResp, err := r.client.Patch(ctx, policyPath(r.client, state.ID.ValueString()), apiReq)
	if err != nil {
		resp.Diagnostics.AddError("Failed to update IAM policy", err.Error())
		return
	}

	p, err := client.ParseResponse[apiPolicy](apiResp)
	if err != nil {
		resp.Diagnostics.AddError("Failed to parse IAM policy response", err.Error())
		return
	}

	plan.fromAPI(p)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *iamPolicyResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state IAMPolicyModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	_, err := r.client.Delete(ctx, policyPath(r.client, state.ID.ValueString()))
	if err != nil {
		if client.IsNotFound(err) {
			return
		}
		resp.Diagnostics.AddError("Failed to delete IAM policy", err.Error())
	}
}

func (r *iamPolicyResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}
