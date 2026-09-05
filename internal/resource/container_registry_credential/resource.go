package container_registry_credential

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/docs"
)

var _ resource.Resource = &credentialResource{}

// credentialsPath is the tenant-scoped collection. It stays TWO-SEGMENT
// (`/registry/credentials`, never a bare `/registry`): the api-gateway's feature
// gate matches with an unanchored substring, and a bare `/registry` would
// capture a customer's own resources named "registry".
const credentialsPath = "/registry/credentials"

// reasonNotEnabled marks the 409 answered to a tenant that has not opted in. It
// is NOT distinguishable by status or by code alone — the opt-in route answers a
// different 409 on this same surface — so this is the discriminator.
const reasonNotEnabled = "REGISTRY_NOT_ENABLED"

// NewResource returns a new registry credential resource factory.
func NewResource() resource.Resource {
	return &credentialResource{}
}

type credentialResource struct {
	client *client.Client
}

func (r *credentialResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_container_registry_credential"
}

func (r *credentialResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Mints a container registry credential — the username and secret used to " +
			"`docker login` to the registry endpoint.\n\n" +
			"THE SECRET IS RETURNED ONCE, AT CREATION, and there is no rotation endpoint. Terraform " +
			"persists it to state (so keep your state encrypted and access-controlled) and never " +
			"expects it on read. A secret lost outside Terraform is recovered by revoking the " +
			"credential and minting a new one — `terraform taint` or changing `name`/`capability` " +
			"both do exactly that.\n\n" +
			"Credentials are immutable: there is no update route, so changing any argument replaces " +
			"the credential and issues a new secret. Update your downstream consumers.\n\n" +
			"This resource cannot be imported: an imported credential's `secret` could never be " +
			"populated, and every subsequent plan would show it as unknown. Revoke and mint instead.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Description: "The credential's id.",
				Computed:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"name": schema.StringAttribute{
				Description: "Your own label for the credential. It is not part of the credential's " +
					"username. Changing it replaces the credential and issues a new secret.",
				Required: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
				Validators: []validator.String{
					stringvalidator.LengthBetween(1, 128),
				},
			},
			"capability": schema.StringAttribute{
				Description: "What the credential may do at the registry endpoint: `pull` or `push` " +
					"(push implies pull). Changing it replaces the credential and issues a new secret.",
				Optional: true,
				Computed: true,
				Default:  stringdefault.StaticString("pull"),
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
				Validators: []validator.String{
					stringvalidator.OneOf("pull", "push"),
				},
			},
			"username": schema.StringAttribute{
				Description: "The `docker login` username, including its `robot$` prefix.",
				Computed:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"secret": schema.StringAttribute{
				Description: "The `docker login` password. Returned only when the credential is first " +
					"created, and it cannot be fetched again. " + docs.StateSecretNote,
				Computed:  true,
				Sensitive: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"disabled": schema.BoolAttribute{
				Description: "True while the tenant's registry is suspended. It is a property of the " +
					"registry, not something done to this credential.",
				Computed: true,
			},
			"endpoint": schema.StringAttribute{
				Description: "The host to `docker login` to.",
				Computed:    true,
			},
			"namespaces": schema.ListAttribute{
				Description: "The namespaces this credential reaches — the tenant's repository namespace " +
					"and each of its pull-through caches (`frostmoln_container_registry_cache`). One " +
					"credential covers all of them, because `docker login` is per host. A cache created " +
					"AFTER this credential is extended onto it by the platform, so pulling through a new " +
					"cache needs no new credential and no replacement of this one — but the list is read " +
					"at refresh, so it lags a cache created in the same apply until the next one.",
				Computed:    true,
				ElementType: types.StringType,
			},
			"created_at": schema.StringAttribute{
				Description: "When the credential was minted.",
				Computed:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
		},
	}
}

func (r *credentialResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (r *credentialResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan ContainerRegistryCredentialModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	apiResp, err := r.client.Post(ctx, r.client.TenantPath(credentialsPath), apiCreateCredentialRequest{
		Name:       plan.Name.ValueString(),
		Capability: plan.Capability.ValueString(),
	})
	if err != nil {
		addCreateRefusal(&resp.Diagnostics, err)
		return
	}

	cred, err := client.ParseResponse[apiCredential](apiResp)
	if err != nil {
		// The credential was minted and its secret is in a response we could not
		// decode. Say so: the practitioner has an orphan to revoke, and no other
		// signal that one exists.
		resp.Diagnostics.AddError(
			"Failed to parse the container registry credential response",
			"The credential was created but its response could not be read, so Terraform did not record "+
				"it — and its secret is now unrecoverable. Find it with `fm registry credential list` and "+
				"revoke it before applying again.\n\nError: "+err.Error(),
		)
		return
	}

	if cred.Secret == "" {
		// Recording a credential whose secret is empty would be worse than
		// failing: every later plan would show it as known-and-empty, and the
		// only copy of the real secret is already gone.
		resp.Diagnostics.AddError(
			"The container registry credential was created without a secret",
			fmt.Sprintf("The API returned credential %d but no secret, and a secret is returned only at "+
				"creation. Revoke it with `fm registry credential delete %d` and apply again.", cred.ID, cred.ID),
		)
		return
	}

	resp.Diagnostics.Append(plan.fromAPI(ctx, cred)...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *credentialResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state ContainerRegistryCredentialModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	id := state.ID.ValueString()
	if id == "" {
		resp.Diagnostics.AddError(
			"Container registry credential is missing its id",
			"Terraform cannot address this credential. Find it with `fm registry credential list`, "+
				"revoke it, and remove it from state with `terraform state rm <address>`.",
		)
		return
	}

	apiResp, err := r.client.Get(ctx, r.client.TenantPath(credentialsPath+"/"+url.PathEscape(id)), nil)
	if err != nil {
		if client.IsNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		// REGISTRY_NOT_ENABLED is a statement about the REGISTRY, not about this
		// credential, so it must NOT drop the resource — the same rule the
		// fail-closed contract applies to a gateway misroute, and for a sharper
		// reason here: what state holds is the only copy of the secret, and
		// nothing can reissue it. Only a genuine 404 is the service saying THIS
		// credential is gone.
		resp.Diagnostics.AddError("Failed to read the container registry credential", err.Error())
		return
	}

	found, err := client.ParseResponse[apiCredential](apiResp)
	if err != nil {
		resp.Diagnostics.AddError("Failed to parse the container registry credential response", err.Error())
		return
	}

	// Never let a response rewrite the identity of the resource being read: a
	// body that is not the requested credential (a misrouted path collapsing to
	// the collection, a shape change) would otherwise blank the id and strand
	// the credential — with its secret, which nothing can reissue.
	if got := strconv.FormatInt(found.ID, 10); got != id {
		resp.Diagnostics.AddError(
			"Unexpected container registry credential in read response",
			fmt.Sprintf("Requested %q but the API answered with %q.", id, got),
		)
		return
	}

	// `state` already carries the secret, and fromAPI deliberately does not
	// touch Secret when the response has none — which is every read. That single
	// rule is what preserves it; a second copy-it-back step here would be
	// redundant and, being unreachable, could not be tested.
	resp.Diagnostics.Append(state.fromAPI(ctx, found)...)
	if resp.Diagnostics.HasError() {
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Update cannot be reached: every settable attribute is RequiresReplace, because
// the API has no update route.
func (r *credentialResource) Update(_ context.Context, _ resource.UpdateRequest, resp *resource.UpdateResponse) {
	resp.Diagnostics.AddError(
		"Update Not Supported",
		"Container registry credentials are immutable. Every attribute change replaces the credential "+
			"and issues a new secret.",
	)
}

func (r *credentialResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state ContainerRegistryCredentialModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	id := state.ID.ValueString()
	// Without this guard the request would be a DELETE on the COLLECTION path,
	// which answers 404 and would be swallowed below as "already gone" —
	// reporting a successful destroy while the credential stays live and usable.
	if id == "" {
		resp.Diagnostics.AddError(
			"Container registry credential is missing its id",
			"Terraform cannot address this credential, so it cannot revoke it. Find it with "+
				"`fm registry credential list` and revoke it there, then remove it from state with "+
				"`terraform state rm <address>`.",
		)
		return
	}

	if _, err := r.client.Delete(ctx, r.client.TenantPath(credentialsPath+"/"+url.PathEscape(id))); err != nil {
		// Only a genuine 404 means the credential is already gone. A
		// REGISTRY_NOT_ENABLED refusal is about the registry, and swallowing it
		// would print a successful destroy while this credential stayed live and
		// usable at the endpoint the moment the registry state cleared.
		if client.IsNotFound(err) {
			return
		}
		if isNotEnabled(err) {
			resp.Diagnostics.AddError(
				"The container registry credential was NOT revoked",
				fmt.Sprintf("The registry refused the request because this tenant's registry is not "+
					"currently enabled, so credential %s was left in place and still works. Revoke it "+
					"with `fm registry credential delete %s` once the registry is reachable, then remove "+
					"it from state with `terraform state rm <address>`.\n\nError: %s", id, id, err.Error()),
			)
			return
		}
		resp.Diagnostics.AddError("Failed to revoke the container registry credential", err.Error())
	}
}

// addCreateRefusal translates the two refusals whose server message does not
// name the Terraform-level remedy. The entitlement refusal is deliberately not
// among them: the gateway's own copy already says to contact support.
func addCreateRefusal(diags interface{ AddError(string, string) }, err error) {
	switch {
	case isNotEnabled(err):
		diags.AddError(
			"This tenant's container registry is not enabled",
			"A credential cannot be minted before the registry exists. Add a "+
				"`frostmoln_container_registry` resource and let it create one — Terraform will order "+
				"the two if the credential references it, for example via `depends_on`.\n\nError: "+err.Error(),
		)
	case isCredentialCapReached(err):
		diags.AddError(
			"This tenant holds the maximum number of container registry credentials",
			"Registry credentials do not expire, so the count is capped. Revoke one you no longer use "+
				"— remove its resource from the configuration, or use `fm registry credential delete "+
				"<id>` for one Terraform does not manage — and apply again.\n\nError: "+err.Error(),
		)
	default:
		diags.AddError("Failed to create the container registry credential", err.Error())
	}
}

// isNotEnabled reports whether err is the refusal issued to a tenant that has
// not opted in.
func isNotEnabled(err error) bool {
	var apiErr *client.APIError
	if !errors.As(err, &apiErr) {
		return false
	}
	reason, _ := apiErr.Details["reason"].(string)
	return reason == reasonNotEnabled
}

// isCredentialCapReached reports whether err is the credential cap. It is a 403
// carrying the platform's `quota_exceeded` code, NOT a 409 — branching on the
// status alone never sees it.
func isCredentialCapReached(err error) bool {
	var apiErr *client.APIError
	if !errors.As(err, &apiErr) {
		return false
	}
	return apiErr.Code == "quota_exceeded" && apiErr.StatusCode == http.StatusForbidden
}
