package container_registry_retention

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/resourcevalidator"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/scopedecl"
)

var (
	_ resource.Resource                     = &retentionResource{}
	_ resource.ResourceWithImportState      = &retentionResource{}
	_ resource.ResourceWithConfigValidators = &retentionResource{}
	_ resource.ResourceWithModifyPlan       = &retentionResource{}
)

// retentionPath is the tenant-scoped retention route. Like /registry/settings
// it stays two-segment, so the gateway's feature gate matches it.
const retentionPath = "/registry/retention"

const (
	reasonNotEnabled = "REGISTRY_NOT_ENABLED"
	// reasonUnrecognized is answered on GET when the tenant's namespace holds a
	// policy whose rules this platform did not write. It is NOT an error for
	// Terraform: the policy exists, its rules are unknown, and the next PUT
	// replaces it.
	reasonUnrecognized = "RETENTION_POLICY_UNRECOGNIZED"
)

// NewResource returns a new container registry retention resource factory.
func NewResource() resource.Resource {
	return &retentionResource{}
}

type retentionResource struct {
	client *client.Client
}

func (r *retentionResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_container_registry_retention"
}

func (r *retentionResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manages the tag-retention policy on this tenant's container registry. A tenant has one " +
			"policy, and it applies to every repository in the tenant's registry namespace.\n\n" +
			"The policy runs nightly, between 00:00 and 06:00 UTC. Images are counted per repository: each repository " +
			"is evaluated separately. Pull-through caches are never touched; they keep their own " +
			"platform-managed cleanup. Storage freed by the policy shows in the registry's usage after " +
			"the nightly run, not when you apply.\n\n" +
			"At least one of `keep_last_tagged` and `delete_untagged_after_days` must be set. A rule " +
			"that is not set keeps everything in its class. Applying REPLACES the whole policy, so a " +
			"policy set elsewhere (the portal or the fm CLI) is overwritten. Destroying the resource " +
			"removes the policy, after which nothing is deleted.\n\n" +
			"Setting a policy is a deferred bulk delete, so it needs permission to delete images " +
			"(`registry:artifacts:delete`) as well as `registry:settings:update`. Images it deletes " +
			"cannot be recovered.\n\n" +
			"The container registry must be enabled first (`frostmoln_container_registry`)." +
			"\n\n" + scopedecl.Summary("frostmoln_container_registry_retention"),
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Description: "The tenant id. A tenant has exactly one retention policy and its id is the tenant's.",
				Computed:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"keep_last_tagged": schema.Int64Attribute{
				Description: "Keep the N most recently pushed tagged images in each repository; older tagged " +
					"images are deleted. Between 1 and 1000. When unset, tagged images are never deleted.",
				Optional:   true,
				Validators: []validator.Int64{int64validator.Between(1, 1000)},
			},
			"delete_untagged_after_days": schema.Int64Attribute{
				Description: "Delete untagged images whose last push is more than this many days old. " +
					"Between 1 and 365. When unset, untagged images are never deleted.",
				Optional:   true,
				Validators: []validator.Int64{int64validator.Between(1, 365)},
			},
		},
	}
}

// ConfigValidators requires at least one rule: the API refuses an empty policy,
// and refusing it at plan time says so before anything is sent.
func (r *retentionResource) ConfigValidators(_ context.Context) []resource.ConfigValidator {
	return []resource.ConfigValidator{
		resourcevalidator.AtLeastOneOf(
			path.MatchRoot("keep_last_tagged"),
			path.MatchRoot("delete_untagged_after_days"),
		),
	}
}

func (r *retentionResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (r *retentionResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan RetentionModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	// A policy set elsewhere (portal, fm CLI) is about to be replaced. Say so.
	// Any other GET failure is left to the PUT, which reports it with a remedy.
	switch existing, err := r.get(ctx); {
	case hasReason(err, reasonUnrecognized):
		resp.Diagnostics.AddWarning("Replacing an existing retention policy",
			"This tenant already had a retention policy with rules Terraform cannot express. It has been "+
				"replaced by "+describe(plan)+".")
	case err == nil && existing.Configured != nil && *existing.Configured:
		var prev RetentionModel
		prev.fromAPI(r.client.TenantID(), existing)
		resp.Diagnostics.AddWarning("Replacing an existing retention policy",
			"This tenant already had a retention policy ("+describe(prev)+"), set outside this "+
				"configuration. It has been replaced by "+describe(plan)+".")
	}
	resp.Diagnostics.Append(r.put(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *retentionResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state RetentionModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if d := r.tenantGuard(state.ID); d != nil {
		resp.Diagnostics.Append(d)
		return
	}
	resp.Diagnostics.Append(r.put(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// put replaces the policy with m's rules and loads the server's answer into m.
func (r *retentionResource) put(ctx context.Context, m *RetentionModel) diag.Diagnostics {
	var diags diag.Diagnostics
	apiResp, err := r.client.Put(ctx, r.client.TenantPath(retentionPath), m.toAPI())
	if err != nil {
		if hasReason(err, reasonNotEnabled) {
			diags.AddError(
				"The container registry is not enabled for this tenant",
				"A retention policy needs an enabled registry. Enable it with the "+
					"`frostmoln_container_registry` resource (and reference it so it is created first), "+
					"or with `fm registry enable`, and apply again.\n\nError: "+err.Error(),
			)
			return diags
		}
		var apiErr *client.APIError
		if errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusForbidden {
			diags.AddError(
				"Not permitted to set the container registry retention policy",
				"Setting a retention policy is a deferred bulk delete, so the API key needs permission "+
					"to delete images (`registry:artifacts:delete`) as well as `registry:settings:update`."+
					"\n\nError: "+err.Error(),
			)
			return diags
		}
		diags.AddError("Failed to set the container registry retention policy", err.Error())
		return diags
	}
	policy, err := client.ParseResponse[apiRetentionPolicy](apiResp)
	if err != nil {
		diags.AddError("Failed to parse the container registry retention policy response", err.Error())
		return diags
	}
	if policy.Configured == nil || !*policy.Configured {
		diags.AddError(
			"The retention policy was not recorded",
			"The API accepted the request but did not report a configured policy. Nothing was recorded "+
				"in state; check the policy with `fm registry retention get` and apply again.",
		)
		return diags
	}
	m.fromAPI(r.client.TenantID(), policy)
	return diags
}

func (r *retentionResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state RetentionModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if d := r.tenantGuard(state.ID); d != nil {
		resp.Diagnostics.Append(d)
		return
	}

	policy, err := r.get(ctx)
	if err != nil {
		switch {
		case hasReason(err, reasonUnrecognized):
			// Configured, with rules we cannot express. Keep it and null both
			// rules: the plan then shows a diff and the next apply PUTs over it.
			// Failing the refresh here would block every plan for the tenant.
			state.ID = types.StringValue(r.client.TenantID())
			state.KeepLastTagged = types.Int64Null()
			state.DeleteUntaggedAfterDays = types.Int64Null()
			resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
		case hasReason(err, reasonNotEnabled):
			// No registry, so no policy.
			resp.State.RemoveResource(ctx)
		default:
			resp.Diagnostics.AddError("Failed to read the container registry retention policy", err.Error())
		}
		return
	}
	// A missing `configured` is a contract violation, never "none": removing
	// the resource is destructive to the plan, so fail loudly instead.
	if policy.Configured == nil {
		resp.Diagnostics.AddError(
			"The retention policy response did not say whether a policy is configured",
			"The API answered without a `configured` field, which the contract requires. Terraform is "+
				"keeping the resource in state rather than treating an unreadable answer as a deletion.",
		)
		return
	}
	if !*policy.Configured {
		resp.State.RemoveResource(ctx)
		return
	}
	state.fromAPI(r.client.TenantID(), policy)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Delete removes the policy. The API answers 204 even when none is set, and a
// tenant with no registry has no policy to remove.
func (r *retentionResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state RetentionModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if d := r.tenantGuard(state.ID); d != nil {
		resp.Diagnostics.Append(d)
		return
	}
	if _, err := r.client.Delete(ctx, r.client.TenantPath(retentionPath)); err != nil && !hasReason(err, reasonNotEnabled) {
		resp.Diagnostics.AddError("Failed to remove the container registry retention policy", err.Error())
	}
}

// ModifyPlan warns when a plan makes the policy delete MORE than it does now:
// a new policy, a rule newly set, or a rule lowered. Deleted images cannot be
// recovered, and the effect lands at the next nightly run, not at apply.
func (r *retentionResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.Plan.Raw.IsNull() {
		return // destroy
	}
	var plan RetentionModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	// Only the rules matter: `id` is always unknown on create.
	if resp.Diagnostics.HasError() || plan.KeepLastTagged.IsUnknown() || plan.DeleteUntaggedAfterDays.IsUnknown() {
		return
	}
	before := "no policy"
	if !req.State.Raw.IsNull() {
		var state RetentionModel
		resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
		if resp.Diagnostics.HasError() {
			return
		}
		if !tightens(state.KeepLastTagged, plan.KeepLastTagged) &&
			!tightens(state.DeleteUntaggedAfterDays, plan.DeleteUntaggedAfterDays) {
			return
		}
		before = describe(state)
	}
	resp.Diagnostics.AddWarning(
		"This retention policy change deletes more images",
		"Before: "+before+".\nAfter: "+describe(plan)+".\n\nIt takes effect at the next nightly run "+
			"between 00:00 and 06:00 UTC, in every repository of this tenant's registry. Deleted images cannot be recovered.",
	)
}

// tightens reports whether a rule moves from old to new in the deleting
// direction: newly set, or lowered. Both rules delete more as they go down.
func tightens(prev, next types.Int64) bool {
	return !next.IsNull() && (prev.IsNull() || next.ValueInt64() < prev.ValueInt64())
}

// describe renders a policy's rules for a diagnostic.
func describe(m RetentionModel) string {
	rule := func(v types.Int64, unset string) string {
		if v.IsNull() || v.IsUnknown() {
			return unset
		}
		return fmt.Sprint(v.ValueInt64())
	}
	return "keep_last_tagged = " + rule(m.KeepLastTagged, "unset (no tagged image is deleted)") +
		", delete_untagged_after_days = " + rule(m.DeleteUntaggedAfterDays, "unset (no untagged image is deleted)")
}

// tenantGuard refuses a state id that is not the provider's own tenant: an
// import of another id, or credentials switched under existing state, would
// otherwise read or write this tenant's policy under that id. Tenant ids are
// UUIDs, so case is not significant.
func (r *retentionResource) tenantGuard(id types.String) diag.Diagnostic {
	if v := id.ValueString(); v != "" && !strings.EqualFold(v, r.client.TenantID()) {
		return diag.NewErrorDiagnostic(
			"Retention policy belongs to another tenant",
			fmt.Sprintf("The id %q is not the provider's tenant (%q). A retention policy can only be "+
				"managed with a provider configured for its own tenant.", v, r.client.TenantID()),
		)
	}
	return nil
}

// get reads the tenant's current policy.
func (r *retentionResource) get(ctx context.Context) (*apiRetentionPolicy, error) {
	apiResp, err := r.client.Get(ctx, r.client.TenantPath(retentionPath), nil)
	if err != nil {
		return nil, err
	}
	return client.ParseResponse[apiRetentionPolicy](apiResp)
}

// ImportState takes the tenant id, the resource's only identity.
func (r *retentionResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
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
