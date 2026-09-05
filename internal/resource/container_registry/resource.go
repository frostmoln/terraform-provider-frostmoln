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
)

var _ resource.Resource = &containerRegistryResource{}

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
			"tenant's namespace and the billable state that comes with it. Holding the container-registry " +
			"entitlement does not create a registry by itself.\n\n" +
			"Nothing about a registry is configurable, so this resource has no arguments and can never " +
			"produce a diff. Applying it against a tenant that already opted in (through the portal, the " +
			"fm CLI, or another Terraform state) ADOPTS the existing registry rather than failing.\n\n" +
			"DESTROYING THIS RESOURCE DOES NOT DELETE THE REGISTRY. There is no teardown endpoint: " +
			"registry teardown belongs to tenant closure. `terraform destroy` removes it from state and " +
			"warns; the namespace, its images and its billable state all survive.",
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
				Description: "The hard storage cap enforced on this namespace, in bytes. A push that would cross it is refused with a 403 whose message states the cap, the usage and the size that would not fit — as prose, and only once the push has failed. This attribute is the machine-readable form of the cap. Null for three different reasons with the same result: the registry reports no cap at all; the registry reports a hard cap of exactly 0, which refuses every push and is the one null that is worse than it looks, not better; or the quota read itself failed. Being disabled is not a fourth reason here — a registry that stops being enabled is removed from state rather than nulled. The platform re-checks and resets the cap on a later sweep, which repairs the first two; it never repairs a failed read, so that one can persist for as long as the outage lasts and the value can go null and back between refreshes with no configuration change. A cap of 0 and an unlimited -1 both arrive as null rather than as a value, so null — not a zero or negative number — is the only state to check for. Null means the limit was not reported, and never that it is unlimited.",
				Computed:    true,
			},
			"storage_used_bytes": schema.Int64Attribute{
				Description: "Storage currently counted against that cap, in bytes. OBSERVATIONAL: it changes with every push, so expect it to differ on each refresh. It counts the deduplicated bytes stored in this namespace as the registry measures them, which does not equal the sum of your local image sizes — do not use it for billing reconciliation.",
				Computed:    true,
			},
		},
	}
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

	// No request body: the namespace is derived from the verified tenant id, so
	// there is nothing a request could say that would change what is created.
	apiResp, err := r.client.Post(ctx, r.client.TenantPath(settingsPath), nil)
	if err != nil {
		if !isAlreadyEnabled(err) {
			// No special-casing for the entitlement refusal: the gateway's own
			// message already names the remedy ("contact support to request
			// access"), and rewording it here would only risk drifting from it.
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
		plan.fromAPI(r.client.TenantID(), settings)
		resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
		return
	}

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
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
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
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Update cannot be reached: every attribute is Computed, so no configuration
// change can produce an update plan. It is implemented only to satisfy the
// interface, and says so rather than silently succeeding.
func (r *containerRegistryResource) Update(_ context.Context, _ resource.UpdateRequest, resp *resource.UpdateResponse) {
	resp.Diagnostics.AddError(
		"Update Not Supported",
		"A container registry has nothing configurable, so there is nothing to update. "+
			"Reaching this is a provider bug; please report it.",
	)
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
