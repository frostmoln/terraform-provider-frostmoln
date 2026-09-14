// Package tenant_default_tags implements the frostmoln_tenant_default_tags
// resource: a tenant's one set of default tags, which the platform copies onto
// every taggable resource created in the tenant afterwards.
package tenant_default_tags

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/scopedecl"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/tagsettings"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/timeouts"
)

var (
	_ resource.Resource                = &tenantDefaultTagsResource{}
	_ resource.ResourceWithImportState = &tenantDefaultTagsResource{}
	_ resource.ResourceWithModifyPlan  = &tenantDefaultTagsResource{}
)

// NewResource returns a new frostmoln_tenant_default_tags resource factory.
func NewResource() resource.Resource {
	return &tenantDefaultTagsResource{}
}

type tenantDefaultTagsResource struct {
	client *client.Client
}

// TenantDefaultTagsModel is the Terraform state model.
type TenantDefaultTagsModel struct {
	ID       types.String `tfsdk:"id"`
	TenantID types.String `tfsdk:"tenant_id"`
	Tags     types.Map    `tfsdk:"tags"`
	// ApplyToExistingOnChange is a provider-side behaviour flag, not a platform
	// setting: it is carried in state and never read back from the platform.
	ApplyToExistingOnChange types.Bool `tfsdk:"apply_to_existing_on_change"`
	// Timeouts budgets the wait for the apply to existing resources: create and
	// update; destroy never applies, so its budget is never consulted.
	Timeouts *timeouts.Model `tfsdk:"timeouts"`
}

func (r *tenantDefaultTagsResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_tenant_default_tags"
}

func (r *tenantDefaultTagsResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manages a tenant's default tags: the tags every taggable resource created in the tenant " +
			"starts with, whoever creates it — the portal, the `fm` CLI, the API or Terraform. The platform copies " +
			"them onto each resource when it is created; after that they are ordinary tags on the resource, no " +
			"longer linked to this setting." +
			"\n\n**Resources created afterwards.** Changing the defaults does not change existing resources, unless " +
			"`apply_to_existing_on_change` is set (below). A resource created shortly after a change may still " +
			"receive the previous set: the services that create resources can take up to 30 seconds to see it. In " +
			"one configuration, give the resources that must carry the defaults a `depends_on` on this resource, " +
			"and allow for that delay." +
			"\n\n**Existing resources.** With `apply_to_existing_on_change = true`, an apply that changes `tags` to " +
			"a non-empty set also adds the new defaults to the resources the tenant already has, and waits for it " +
			"(30 minutes by default; `timeouts.create` and `timeouts.update` change that). If the wait runs out while " +
			"the platform is still working, the apply succeeds with a warning that the apply to existing resources " +
			"is still running and continues in the background. If the platform cannot start it right now (it is " +
			"unavailable), the apply also succeeds, with a warning; run `fm tenant default-tags apply` later, since " +
			"a later `terraform apply` starts it again only when `tags` changes. If the platform refuses it (no " +
			"permission, the tenant is not provisioned yet, or the defaults are not valid), the apply fails after " +
			"saving the default tags, so a new resource is tainted and replaced by the next apply. It adds only the default keys a resource is missing: a key the resource already has " +
			"keeps its value, so changing a default's value does not reach resources that already carry the key. " +
			"Resources the platform manages (a managed security group, a load balancer or public IP a managed " +
			"service owns, and their load-balancer children) and the resources inside a managed service are " +
			"skipped, as is a resource the added tags would push over its tag limit; volumes the platform created " +
			"together with an instance (its boot volume), instance snapshots and images are not included. Only " +
			"resources that exist when the apply runs are stamped, so give the resources in the same configuration " +
			"that must get the defaults a `depends_on` on this resource. Resources that could not be updated are " +
			"reported as warnings. On a Terraform-managed resource " +
			"a stamped key appears in " +
			"`tags_all`, never in `tags`, and plans no diff — but provider versions before v0.62.0 remove it on the " +
			"resource's next apply, so upgrade every configuration that manages resources in the tenant first." +
			"\n\n**A resource's own tags win.** The defaults are merged into the create request's own tags, and a " +
			"key the request sets itself wins — including a key from the provider's `default_tags`, which the " +
			"provider sends with every create. On a Terraform-managed resource the tenant's defaults appear in " +
			"`tags_all`, never in `tags`, and are kept like any other key set outside Terraform." +
			"\n\n**One set per tenant.** Every tenant has exactly one set of default tags, empty until someone sets " +
			"it. This resource owns the whole set: every apply writes exactly `tags`, so a key added in the portal " +
			"is removed by the next apply, and destroying the resource clears the set. Manage it from one place: " +
			"there is no locking, and the last write wins. Creating the resource replaces whatever set the tenant " +
			"already has; the plan warns, naming the keys it will remove or change." +
			"\n\n**Rules.** A default is added to every kind of resource, so it has to be accepted by all of them — " +
			"the same rules as the provider's `default_tags`, checked at plan time: at most 10 tags; keys of 1 to " +
			"64 bytes made of letters, digits and `.` `_` `:` `-`, starting and ending with a letter or digit; " +
			"values of at most 255 bytes made of letters, digits, spaces and `+` `-` `.` `_` `:` `/` `@` `=` (or " +
			"empty). Keys the platform reserves are refused in any letter case: the prefixes `frostmoln_`, " +
			"`frostmoln-`, `os_`, `instance_` and `nova_`, and the keys `request-id`, `customer-id`, `project-id`, " +
			"`tenant-id`, `created-at`, `acl`, `storage-class`, `quota-bytes` and `cors-config`." +
			"\n\n**Permissions.** Changing the set needs an admin or owner role in the organization that owns the " +
			"tenant; reading it needs any active membership. An API key needs `organizations:write` and " +
			"`organizations:read`." +
			"\n\n**Tenant.** The resource manages the provider's tenant, like every other resource: select it " +
			"with the provider's `tenant_id`. If the provider is later pointed at another tenant, the plan " +
			"replaces the resource — clearing the old tenant's defaults — and warns, naming both tenants." +
			"\n\n**Import.** By tenant id, which must be the provider's tenant." +
			"\n\n" + scopedecl.Summary("frostmoln_tenant_default_tags"),
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Description: "The tenant's id: a tenant has exactly one set of default tags.",
				Computed:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"tenant_id": schema.StringAttribute{
				Description: "The tenant whose default tags this manages: the provider's tenant (its `tenant_id`, " +
					"else the credential's default), as for every other resource. If the provider is later pointed " +
					"at another tenant, the plan replaces this resource — clearing the old tenant's defaults and " +
					"setting the new tenant's — and warns, naming both.",
				Computed: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"tags": schema.MapAttribute{
				Description: "The complete set of default tags. Keys missing here are removed from the tenant's " +
					"defaults on apply, and `{}` keeps the set empty. See the rules above.",
				ElementType: types.StringType,
				Required:    true,
				Validators:  []validator.Map{tagsettings.DefaultTagsValidator()},
			},
			"apply_to_existing_on_change": schema.BoolAttribute{
				Description: "When true, an apply that changes `tags` to a non-empty set also adds the new defaults " +
					"to the tenant's existing resources and waits for it (30 minutes by default, see `timeouts`): only " +
					"the keys a resource is missing, never changing a value it already has. Setting or clearing this " +
					"flag alone changes " +
					"nothing and starts nothing, and destroying the resource never starts it. A run already in " +
					"progress is waited for, then the apply starts once. Default `false`.",
				Optional: true,
				Computed: true,
				Default:  booldefault.StaticBool(false),
			},
		},
		Blocks: map[string]schema.Block{
			// Budgets only the wait for the apply to existing resources. A
			// timeouts change is an in-place no-op on the platform.
			"timeouts": timeouts.Schema(),
		},
	}
}

// applyBudgets resolves the timeouts block onto the wait for the apply to
// existing resources, defaulting each verb to applyWaitTimeout.
func applyBudgets(m *timeouts.Model) timeouts.Budgets {
	defaults := timeouts.Uniform(applyWaitTimeout)
	budgets, err := m.Resolve(defaults)
	if err != nil {
		// Unreachable via HCL (the block validator refuses a bad duration at
		// plan time); degrade to the default rather than fail a wait.
		return defaults
	}
	return budgets
}

func (r *tenantDefaultTagsResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

// ModifyPlan ties the resource to the PROVIDER's tenant, as every other
// resource is tied, and makes what an apply will overwrite visible before
// anything is lost. It never raises an error diagnostic.
//
//   - Create: the plan names the provider's tenant, and if that tenant already
//     has default tags the plan WARNS, listing the keys the create will remove
//     or change (or that it could not read them).
//   - Update, provider now pointed at another tenant: the plan replaces the
//     resource — the destroy clears the old tenant's set, the create sets the
//     new one's — and warns, naming both, so the old tenant is never cleared
//     silently.
//   - Destroy of a set that is not the provider's tenant's: a warning naming it.
func (r *tenantDefaultTagsResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if r.client == nil || r.client.TenantID() == "" {
		return
	}
	provider := canonicalTenant(r.client.TenantID())

	var recorded types.String
	if !req.State.Raw.IsNull() {
		if d := req.State.GetAttribute(ctx, path.Root("tenant_id"), &recorded); d.HasError() {
			return
		}
	}

	if req.Plan.Raw.IsNull() {
		if !recorded.IsNull() && canonicalTenant(recorded.ValueString()) != provider {
			resp.Diagnostics.AddWarning("Destroy Clears Another Tenant's Default Tags",
				fmt.Sprintf("This destroy clears the default tags of tenant %q, which is not the provider's tenant "+
					"(%q): the resource was created for %q and still manages that tenant's set.",
					recorded.ValueString(), provider, recorded.ValueString()))
		}
		return
	}

	if req.State.Raw.IsNull() {
		resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("tenant_id"), provider)...)
		resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("id"), provider)...)
		var planned types.Map
		if d := req.Plan.GetAttribute(ctx, path.Root("tags"), &planned); d.HasError() {
			return
		}
		current, err := tagsettings.GetDefaultTags(ctx, r.client, provider)
		if err != nil {
			resp.Diagnostics.AddAttributeWarning(path.Root("tags"), "Current Default Tags Not Read",
				fmt.Sprintf("Could not read the default tags tenant %q has now (%s). Creating this resource "+
					"replaces the whole set, whatever it holds.", provider, err))
			return
		}
		if keys := plannedReplacements(current, planned); len(keys) > 0 {
			resp.Diagnostics.AddAttributeWarning(path.Root("tags"), "Existing Default Tags Will Be Replaced",
				fmt.Sprintf("Tenant %q already has default tags, and this resource owns the whole set, so creating "+
					"it removes or changes: %s. If they were set in the portal, add them to `tags` to keep them.",
					provider, strings.Join(keys, ", ")))
		}
		return
	}

	if recorded.IsNull() || canonicalTenant(recorded.ValueString()) == provider {
		return
	}
	resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("tenant_id"), provider)...)
	resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("id"), provider)...)
	resp.RequiresReplace = append(resp.RequiresReplace, path.Root("tenant_id"))
	resp.Diagnostics.AddAttributeWarning(path.Root("tenant_id"), "Default Tags Move to Another Tenant",
		fmt.Sprintf("The provider now points at tenant %q, but this resource manages the default tags of tenant "+
			"%q. The plan replaces it: the destroy CLEARS tenant %q's default tags, and the create sets tenant %q's. "+
			"If that is not what you want, point the provider back at %q, or remove the resource from state "+
			"(`terraform state rm`) to leave that tenant's defaults as they are.",
			provider, recorded.ValueString(), recorded.ValueString(), provider, recorded.ValueString()))
}

// canonicalTenant is a tenant id in the platform's canonical spelling, so an
// override written in another case is not mistaken for another tenant.
func canonicalTenant(id string) string {
	if c, err := tagsettings.CanonicalUUID(id); err == nil {
		return c
	}
	return id
}

// plannedReplacements lists, sorted, the current defaults a create will remove
// or change. With the planned set not yet known, every current key may be.
func plannedReplacements(current map[string]string, planned types.Map) []string {
	var out []string
	elems := planned.Elements()
	for k, v := range current {
		switch {
		case planned.IsUnknown():
			out = append(out, fmt.Sprintf("%s=%q (the new set is known after apply)", k, v))
		default:
			pv, ok := elems[k].(types.String)
			switch {
			case !ok:
				out = append(out, fmt.Sprintf("%s=%q", k, v))
			case pv.IsUnknown():
				out = append(out, fmt.Sprintf("%s=%q (the new value is known after apply)", k, v))
			case pv.ValueString() != v:
				out = append(out, fmt.Sprintf("%s=%q", k, v))
			}
		}
	}
	sort.Strings(out)
	return out
}

// tenantOf is the tenant a plan or state addresses: tenant_id, else the
// provider's tenant.
func (r *tenantDefaultTagsResource) tenantOf(m *TenantDefaultTagsModel) string {
	if !m.TenantID.IsNull() && !m.TenantID.IsUnknown() && m.TenantID.ValueString() != "" {
		return m.TenantID.ValueString()
	}
	return r.client.TenantID()
}

// desired reads the planned tag set. A null element cannot get here (the
// validator refuses it at plan time), and an unknown one is known by apply.
func desired(ctx context.Context, m *TenantDefaultTagsModel) (map[string]string, error) {
	out := map[string]string{}
	if m.Tags.IsNull() || m.Tags.IsUnknown() {
		return out, nil
	}
	if d := m.Tags.ElementsAs(ctx, &out, false); d.HasError() {
		return nil, fmt.Errorf("tags: %v", d.Errors())
	}
	return out, nil
}

func (m *TenantDefaultTagsModel) fromAPI(ctx context.Context, tenantID string, tags map[string]string) error {
	v, d := types.MapValueFrom(ctx, types.StringType, tags)
	if d.HasError() {
		return fmt.Errorf("tags: %v", d.Errors())
	}
	m.ID = types.StringValue(tenantID)
	m.TenantID = types.StringValue(tenantID)
	m.Tags = v
	return nil
}

// replacedKeysWarning names the keys a create overwrote: a set the tenant
// already had, typically from the portal. The create proceeds — this resource
// adopts the tenant's one set (scopedecl: adopt-as-managed) — but not silently.
func replacedKeysWarning(tenantID string, previous, now map[string]string) (string, bool) {
	var changed []string
	for k, v := range previous {
		if nv, ok := now[k]; !ok || nv != v {
			changed = append(changed, fmt.Sprintf("%s=%q", k, v))
		}
	}
	if len(changed) == 0 {
		return "", false
	}
	sort.Strings(changed)
	return fmt.Sprintf("Tenant %q already had default tags, and this resource now owns the whole set, so the "+
		"apply replaced them. Removed or changed: %s. If they were set in the portal, add them to `tags` to keep "+
		"them.", tenantID, strings.Join(changed, ", ")), true
}

func (r *tenantDefaultTagsResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan TenantDefaultTagsModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	tenantID := r.tenantOf(&plan)
	tags, err := desired(ctx, &plan)
	if err != nil {
		resp.Diagnostics.AddError("Invalid default tags", err.Error())
		return
	}

	// Read the set this create is about to replace, to say what it replaced
	// (the plan warned already; the set may have changed since). A failed read
	// does not stop the create — the write below is authoritative and fails on
	// its own if the caller may not make it — but it is said, not skipped.
	previous, prevErr := tagsettings.GetDefaultTags(ctx, r.client, tenantID)

	stored, err := tagsettings.PutDefaultTags(ctx, r.client, tenantID, tags)
	if err != nil {
		resp.Diagnostics.AddError("Failed to set tenant default tags", err.Error())
		return
	}
	if prevErr != nil {
		resp.Diagnostics.AddWarning("Previous Default Tags Not Read",
			fmt.Sprintf("Could not read the default tags tenant %q had before this create (%s). The create "+
				"replaced the whole set, so any defaults it held are gone unless they are in `tags`.", tenantID, prevErr))
	} else if msg, replaced := replacedKeysWarning(tenantID, previous, stored); replaced {
		resp.Diagnostics.AddWarning("Existing Default Tags Replaced", msg)
	}

	if err := plan.fromAPI(ctx, tenantID, stored); err != nil {
		resp.Diagnostics.AddError("Failed to record tenant default tags", err.Error())
		return
	}
	// State FIRST: the defaults were written, whatever the apply below does. An
	// apply that fails is an error raised after this, so the create is tainted
	// with its state kept, and the replacement runs the apply again.
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if plan.ApplyToExistingOnChange.ValueBool() && len(stored) > 0 {
		r.applyToExisting(ctx, tenantID, applyBudgets(plan.Timeouts).Create, &resp.Diagnostics)
	}
}

func (r *tenantDefaultTagsResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state TenantDefaultTagsModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	tenantID := r.tenantOf(&state)
	tags, err := tagsettings.GetDefaultTags(ctx, r.client, tenantID)
	if err != nil {
		if client.IsNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Failed to read tenant default tags", err.Error())
		return
	}
	if err := state.fromAPI(ctx, tenantID, tags); err != nil {
		resp.Diagnostics.AddError("Failed to record tenant default tags", err.Error())
		return
	}
	// The flag is the configuration's, never the platform's: a refresh keeps it.
	// Null — after an import, or in state written by a provider that predates
	// the attribute — is its default, so neither plans a flag-only update.
	if state.ApplyToExistingOnChange.IsNull() || state.ApplyToExistingOnChange.IsUnknown() {
		state.ApplyToExistingOnChange = types.BoolValue(false)
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *tenantDefaultTagsResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state TenantDefaultTagsModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	// tenant_id cannot differ from state here: a change replaces.
	tenantID := r.tenantOf(&state)

	// A plan that changes only apply_to_existing_on_change (D8) writes nothing
	// and starts nothing: the platform already holds exactly these tags (the
	// refresh read them), and the flag is not a platform setting.
	if plan.Tags.Equal(state.Tags) {
		plan.ID, plan.TenantID, plan.Tags = state.ID, state.TenantID, state.Tags
		resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
		return
	}

	tags, err := desired(ctx, &plan)
	if err != nil {
		resp.Diagnostics.AddError("Invalid default tags", err.Error())
		return
	}
	stored, err := tagsettings.PutDefaultTags(ctx, r.client, tenantID, tags)
	if err != nil {
		resp.Diagnostics.AddError("Failed to set tenant default tags", err.Error())
		return
	}
	if err := plan.fromAPI(ctx, tenantID, stored); err != nil {
		resp.Diagnostics.AddError("Failed to record tenant default tags", err.Error())
		return
	}
	// State first, as in Create: the new defaults are written whatever the apply
	// below does. An update error taints nothing, so a failed apply is not
	// retried by the next plan — the error says how to run it again.
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if plan.ApplyToExistingOnChange.ValueBool() && len(stored) > 0 {
		r.applyToExisting(ctx, tenantID, applyBudgets(plan.Timeouts).Update, &resp.Diagnostics)
	}
}

// Delete clears the set: a tenant always has one, so "destroy" means "no
// defaults", written as {"tags": {}}. It never applies anything to existing
// resources, whatever apply_to_existing_on_change says: clearing the defaults
// removes no tag from any resource.
func (r *tenantDefaultTagsResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state TenantDefaultTagsModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if _, err := tagsettings.PutDefaultTags(ctx, r.client, r.tenantOf(&state), map[string]string{}); err != nil {
		if client.IsNotFound(err) {
			return
		}
		resp.Diagnostics.AddError("Failed to clear tenant default tags", err.Error())
	}
}

// ImportState takes the tenant id, which must be the provider's tenant.
func (r *tenantDefaultTagsResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	parts, err := client.ParseImportID(req.ID, "tenant_id")
	if err != nil {
		resp.Diagnostics.AddError("Invalid import ID", err.Error())
		return
	}
	tenantID, err := tagsettings.CanonicalUUID(parts[0])
	if err != nil {
		resp.Diagnostics.AddError("Invalid import ID", fmt.Sprintf("%q is not a tenant id (a UUID).", parts[0]))
		return
	}
	if r.client != nil && tenantID != canonicalTenant(r.client.TenantID()) {
		resp.Diagnostics.AddError("Import ID Is Not the Provider's Tenant",
			fmt.Sprintf("%q is not the provider's tenant (%q). This resource manages the default tags of the "+
				"provider's tenant, like every other resource manages its own: point the provider at %q (its "+
				"`tenant_id`) to import that tenant's defaults.", tenantID, r.client.TenantID(), tenantID))
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), tenantID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("tenant_id"), tenantID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("apply_to_existing_on_change"), false)...)
}
