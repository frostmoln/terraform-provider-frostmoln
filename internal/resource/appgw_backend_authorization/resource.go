// Package appgw_backend_authorization implements the
// frostmoln_appgw_backend_authorization Terraform resource.
package appgw_backend_authorization

import (
	"context"
	"fmt"
	"net/http"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/boolplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/pubid"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/scopedecl"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/tftags"
)

var (
	_ resource.Resource                = &authorizationResource{}
	_ resource.ResourceWithImportState = &authorizationResource{}
	_ resource.ResourceWithConfigure   = &authorizationResource{}
	_ resource.ResourceWithModifyPlan  = &authorizationResource{}
)

// AuthorizationModel is the Terraform state model for a backend authorization.
type AuthorizationModel struct {
	ID        types.String `tfsdk:"id"`
	GatewayID types.String `tfsdk:"gateway_id"`
	PoolID    types.String `tfsdk:"pool_id"`
	BackendID types.String `tfsdk:"backend_id"`

	SecurityGroupID types.String `tfsdk:"security_group_id"`
	AdoptExisting   types.Bool   `tfsdk:"adopt_existing"`

	Protocol     types.String `tfsdk:"protocol"`
	PortMin      types.Int64  `tfsdk:"port_min"`
	PortMax      types.Int64  `tfsdk:"port_max"`
	Adopted      types.Bool   `tfsdk:"adopted"`
	AuthorizedBy types.String `tfsdk:"authorized_by"`
	CreatedAt    types.String `tfsdk:"created_at"`

	// Tags / TagsAll are the authorization's customer resource tags (internal/tftags).
	Tags    types.Map `tfsdk:"tags"`
	TagsAll types.Map `tfsdk:"tags_all"`
}

type apiAuthorization struct {
	ID                    string `json:"id"`
	GatewayID             string `json:"gatewayId"`
	TargetSecurityGroupID string `json:"targetSecurityGroupId"`
	Protocol              string `json:"protocol"`
	PortMin               int    `json:"portMin"`
	PortMax               int    `json:"portMax"`
	Adopted               bool   `json:"adopted,omitempty"`
	AuthorizedBy          string `json:"authorizedBy,omitempty"`
	CreatedAt             string `json:"createdAt"`
	// Tags are the authorization's customer resource tags ({} when untagged).
	Tags map[string]string `json:"tags"`
}

type apiAuthorizationListResponse struct {
	Items []apiAuthorization `json:"items"`
}

type apiAuthorizeRequest struct {
	SecurityGroupID string `json:"securityGroupId"`
	// Tags is default_tags ∪ tags (tftags.ForCreate), omitted when empty; the API
	// merges the tenant's default tags in on the authorization's FIRST creation.
	// An authorize that adopts an existing rule keeps that rule's tags (Create
	// then writes the configured ones itself).
	Tags map[string]string `json:"tags,omitempty"`
}

// apiUpdateTagsRequest is PATCH .../authorizations/{id}: `tags` is the
// authorization's only editable field (any other is refused), a row-only write
// that changes no ingress rule. A POINTER so an empty map is sent as {}.
type apiUpdateTagsRequest struct {
	Tags *map[string]string `json:"tags,omitempty"`
}

type authorizationResource struct {
	client *client.Client
}

// NewResource returns a new backend authorization resource factory.
func NewResource() resource.Resource {
	return &authorizationResource{}
}

func (r *authorizationResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_appgw_backend_authorization"
}

func (r *authorizationResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	replaceStr := []planmodifier.String{stringplanmodifier.RequiresReplace()}

	resp.Schema = schema.Schema{
		Description: "Opens the path from an Application Gateway's appliance to a backend, by adding " +
			"one ingress rule to a security group you own.\n\n" +
			"Creating a backend does not make it reachable. The platform never edits your security " +
			"groups implicitly, so this is the explicit, audited step that does.\n\n" +
			"~> **The rule is per (security group, protocol, port), not per backend.** Two backends " +
			"behind the same security group and port **share one authorization**. If you declare " +
			"this resource for both, the second reports `adopted = true` and destroying either one " +
			"closes the path for both. Declare it **once per (security group, port)**, not once per " +
			"backend.\n\n" +
			"Every authorize and revoke is recorded in the gateway's audit trail." +
			"\n\n" + scopedecl.Summary("frostmoln_appgw_backend_authorization"),
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Description: "The authorization's identifier. This — not the backend — is what a " +
					"revoke is addressed by.",
				Computed:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"gateway_id": schema.StringAttribute{
				Description:   "The Application Gateway whose appliance is being let in.",
				Required:      true,
				PlanModifiers: replaceStr,
			},
			"pool_id": schema.StringAttribute{
				Description:   "The backend pool the backend belongs to.",
				Required:      true,
				PlanModifiers: replaceStr,
			},
			"backend_id": schema.StringAttribute{
				Description: "The backend whose protocol and port the rule is derived from. The " +
					"rule that results is not scoped to this backend — see the note above.",
				Required:      true,
				PlanModifiers: replaceStr,
			},
			"security_group_id": schema.StringAttribute{
				Description:   "The security group to add the ingress rule to. You must own it.",
				Required:      true,
				PlanModifiers: replaceStr,
			},
			"adopt_existing": schema.BoolAttribute{
				Description: "Permit this resource to take over an ingress rule that already exists " +
					"for the same (security group, protocol, port). Defaults to `false`.\n\n" +
					"~> **Adopting is destructive on destroy.** The platform keys one rule per " +
					"(group, protocol, port) and returns the SAME authorization id for a second " +
					"request, so an adopted resource shares its id with whatever created the rule " +
					"first. Destroying it — including the destroy half of a `create_before_destroy` " +
					"replacement — removes the rule for **every** backend behind that group and " +
					"port. Leave this `false` and declare one authorization per (group, port).",
				Optional:      true,
				Computed:      true,
				Default:       booldefault.StaticBool(false),
				PlanModifiers: []planmodifier.Bool{boolplanmodifier.RequiresReplace()},
			},
			"protocol": schema.StringAttribute{
				Description: "The protocol the rule permits, derived from the backend.",
				Computed:    true,
			},
			"port_min": schema.Int64Attribute{
				Description: "The first port the rule permits.",
				Computed:    true,
			},
			"port_max": schema.Int64Attribute{
				Description: "The last port the rule permits.",
				Computed:    true,
			},
			"adopted": schema.BoolAttribute{
				Description: "True when an equivalent rule already existed and was adopted rather " +
					"than created. A destroy still removes it, which is why declaring this resource " +
					"twice for one (security group, port) is a mistake.",
				Computed: true,
			},
			"authorized_by": schema.StringAttribute{
				Description: "Who opened the path.",
				Computed:    true,
			},
			"created_at": schema.StringAttribute{
				Description:   "When the path was opened.",
				Computed:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
		},
	}
	resp.Schema.Attributes["tags"] = tftags.OfferTagsAttribute("Resource tags on the authorization record: your " +
		"own key/value metadata for search and filtering, stored by the platform only and never placed on the " +
		"security-group rule. Changing them updates the authorization in place (every other attribute replaces " +
		"it) and changes no rule. An adopted rule (`adopt_existing`) keeps the tags it already carries and gets " +
		"these added.")
	resp.Schema.Attributes["tags_all"] = tftags.TagsAllAttribute()
}

func (r *authorizationResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	c, ok := req.ProviderData.(*client.Client)
	if !ok {
		resp.Diagnostics.AddError("Unexpected Provider Data",
			fmt.Sprintf("Expected *client.Client, got %T", req.ProviderData))
		return
	}
	r.client = c
}

func (m *AuthorizationModel) fromAPI(a *apiAuthorization) {
	m.ID = types.StringValue(a.ID)
	m.GatewayID = types.StringValue(a.GatewayID)
	m.SecurityGroupID = types.StringValue(a.TargetSecurityGroupID)
	m.Protocol = types.StringValue(a.Protocol)
	m.PortMin = types.Int64Value(int64(a.PortMin))
	m.PortMax = types.Int64Value(int64(a.PortMax))
	// 🔴 `adopted` IS NOT PERSISTED SERVER-SIDE. It is computed at authorize
	// time and returned on the 201 only; the table has no column for it, so
	// every later read reports false. Trust it on create, and do not let a
	// refresh silently turn a shared rule into one that looks exclusive: Read
	// restores the prior value over whatever this writes.
	//
	// Written on BOTH answers. `adopted` is Computed with no Default, so a
	// create plans it unknown, and writing only `true` left every create that
	// did not adopt returning that unknown — which Terraform refuses as an
	// invalid result and taints (Ambix 01a0cd73-3de9).
	m.Adopted = types.BoolValue(a.Adopted)
	m.AuthorizedBy = types.StringValue(a.AuthorizedBy)
	m.CreatedAt = types.StringValue(a.CreatedAt)
}

// tagsFromAPI renders the API's tags into `tags` (the keys the prior value names)
// and `tags_all` (every customer key). m.Tags must hold the prior value: the
// configuration on a create or update, the state on a refresh.
func (m *AuthorizationModel) tagsFromAPI(ctx context.Context, a *apiAuthorization, diags *diag.Diagnostics) {
	m.Tags, m.TagsAll = tftags.ReadBack(ctx, tftags.OfferCustomerTags(a.Tags), m.Tags, diags)
}

// authorizationPath is one authorization's member path.
func (r *authorizationResource) authorizationPath(gwID, id string) string {
	return r.client.TenantPath(fmt.Sprintf("/application-gateways/%s/authorizations/%s", gwID, id))
}

// find reads one authorization by listing the gateway's open paths (there is no
// GET by id). One missing from the list is reported as a 404.
func (r *authorizationResource) find(ctx context.Context, gwID, id string) (*apiAuthorization, error) {
	apiResp, err := r.client.Get(ctx, r.client.TenantPath(fmt.Sprintf(
		"/application-gateways/%s/authorizations", gwID,
	)), nil)
	if err != nil {
		return nil, err
	}
	list, err := client.ParseResponse[apiAuthorizationListResponse](apiResp)
	if err != nil {
		return nil, err
	}
	for i := range list.Items {
		if pubid.Equal(list.Items[i].ID, id) {
			return &list.Items[i], nil
		}
	}
	return nil, &client.APIError{StatusCode: http.StatusNotFound, Code: "NOT_FOUND", Message: "authorization " + id + " is not open"}
}

func (r *authorizationResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan AuthorizationModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	defaults := r.client.DefaultTags()
	createTags := tftags.ForCreate(ctx, defaults, plan.Tags, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	apiResp, err := r.client.Post(ctx, r.client.TenantPath(fmt.Sprintf(
		"/application-gateways/%s/backend-pools/%s/backends/%s/authorize",
		plan.GatewayID.ValueString(), plan.PoolID.ValueString(), plan.BackendID.ValueString(),
	)),
		apiAuthorizeRequest{SecurityGroupID: plan.SecurityGroupID.ValueString(), Tags: createTags})
	if err != nil {
		resp.Diagnostics.AddError("Failed to Authorize Backend", err.Error())
		return
	}
	tftags.RecordDefaults(ctx, resp.Private, defaults, &resp.Diagnostics)
	a, err := client.ParseResponse[apiAuthorization](apiResp)
	if err != nil {
		resp.Diagnostics.AddError("Failed to Parse Authorization Response", err.Error())
		return
	}
	poolID, backendID, adopt := plan.PoolID, plan.BackendID, plan.AdoptExisting
	// An ADOPTED rule keeps its own tags: the authorize does not touch them. When
	// the adoption is wanted, the configured set is written onto it now (keys the
	// rule already carries that this configuration does not set are kept), so
	// the result is what the plan promised.
	if a.Adopted && adopt.ValueBool() {
		merged := tftags.OfferCustomerTags(a.Tags)
		for k, v := range createTags {
			merged[k] = v
		}
		if !tftags.Equal(merged, tftags.OfferCustomerTags(a.Tags)) {
			if _, err := r.client.Patch(ctx, r.authorizationPath(a.GatewayID, a.ID),
				apiUpdateTagsRequest{Tags: &merged}); err != nil {
				resp.Diagnostics.AddError("Failed to Tag The Adopted Authorization", err.Error())
				// Tracked anyway: the rule is open and adopted.
				plan.fromAPI(a)
				plan.PoolID, plan.BackendID, plan.AdoptExisting = poolID, backendID, adopt
				plan.tagsFromAPI(ctx, a, &resp.Diagnostics)
				resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
				return
			}
			a.Tags = merged
		}
	}
	plan.fromAPI(a)
	plan.tagsFromAPI(ctx, a, &resp.Diagnostics)
	// The authorization payload does not carry the pool, the backend or the
	// opt-in: the rule is scoped to none of them. They are Terraform's own
	// addressing and configuration, carried over.
	plan.PoolID, plan.BackendID, plan.AdoptExisting = poolID, backendID, adopt

	// 🔴 REFUSE AN UNASKED-FOR ADOPTION, DO NOT MERELY WARN.
	//
	// The platform keys one row per (gateway, security group, protocol, port)
	// and its upsert RETURNS THE EXISTING ROW'S ID. So a second authorization
	// for the same group and port is not a second rule — it is the same rule
	// under the same id, and destroying either resource deletes it for both.
	//
	// The case that makes a warning insufficient is `create_before_destroy`:
	// Terraform creates first (getting that shared id back), then destroys the
	// prior object BY THAT ID, deleting the rule it just "created". The apply is
	// green, state says the path is open, and the backend is unreachable. A
	// warning printed during the create is two steps too early to be read as a
	// prediction of that.
	//
	// State is written BEFORE the refusal so the resource is tracked: the
	// authorization row exists either way, and leaving Terraform unaware of it
	// would strand an open ingress rule with nothing to revoke it.
	if a.Adopted && !adopt.ValueBool() {
		resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
		resp.Diagnostics.AddError("An Equivalent Ingress Rule Already Exists",
			"An ingress rule for "+a.TargetSecurityGroupID+" on "+a.Protocol+" "+
				portRange(a.PortMin, a.PortMax)+" was already present, so this resource has adopted "+
				"it rather than creating one — and now shares its identity.\n\n"+
				"Destroying this resource would remove that rule for EVERY backend behind the same "+
				"security group and port, including any that another resource or another team "+
				"depends on.\n\n"+
				"Declare one authorization per (security group, port) — usually on the first "+
				"backend that needs it — and let the others rely on it. If taking over the "+
				"existing rule is what you intend, set adopt_existing = true.")
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// portRange renders a rule's port span for a diagnostic.
func portRange(min, max int) string {
	if min == max {
		return fmt.Sprintf("port %d", min)
	}
	return fmt.Sprintf("ports %d-%d", min, max)
}

// Read finds the authorization by listing the gateway's open paths.
//
// There is no get-by-id: the server registers POST (authorize), GET (list), PATCH (tags only)
// and DELETE. Not finding it means the path was closed outside Terraform.
func (r *authorizationResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state AuthorizationModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	apiResp, err := r.client.Get(ctx, r.client.TenantPath(fmt.Sprintf(
		"/application-gateways/%s/authorizations", state.GatewayID.ValueString(),
	)), nil)
	if err != nil {
		if client.IsNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Failed to Read Authorizations", err.Error())
		return
	}
	list, err := client.ParseResponse[apiAuthorizationListResponse](apiResp)
	if err != nil {
		resp.Diagnostics.AddError("Failed to Parse Authorization Response", err.Error())
		return
	}
	want := state.ID.ValueString()
	for i := range list.Items {
		if pubid.Equal(list.Items[i].ID, want) {
			poolID, backendID := state.PoolID, state.BackendID
			adopt, wasAdopted := state.AdoptExisting, state.Adopted
			state.fromAPI(&list.Items[i])
			state.PoolID, state.BackendID = poolID, backendID
			state.AdoptExisting = adopt
			// Preserved, not refreshed: the listing cannot report it (no
			// column), so the server's silence would otherwise read as "this
			// rule is exclusive" for one that is shared.
			state.Adopted = wasAdopted
			state.tagsFromAPI(ctx, &list.Items[i], &resp.Diagnostics)
			tftags.FinishRead(ctx, req.Private, resp.Private, r.client.DefaultTags(), &state.Tags, state.TagsAll, &resp.Diagnostics)
			resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
			return
		}
	}
	resp.State.RemoveResource(ctx)
}

// Update changes the authorization's tags — its only in-place attribute — with
// a tags-only PATCH, sent only when the platform's set would change (computed
// against the tags it holds right now, so a key added outside Terraform is
// kept). It changes no ingress rule.
func (r *authorizationResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state AuthorizationModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	gwID, id := state.GatewayID.ValueString(), state.ID.ValueString()
	defaults := r.client.DefaultTags()
	tags, err := tftags.OfferTagsToWrite(ctx, defaults, plan.Tags, state.Tags, state.TagsAll, req.Private,
		func(ctx context.Context) (map[string]string, error) {
			cur, err := r.find(ctx, gwID, id)
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
	if tags != nil {
		if _, err := r.client.Patch(ctx, r.authorizationPath(gwID, id), apiUpdateTagsRequest{Tags: tags}); err != nil {
			resp.Diagnostics.AddError("Failed to Update Authorization Tags", err.Error())
			return
		}
	}
	// The platform now holds the desired set, whether or not this update had to
	// write it; that also ends an import's pending state.
	tftags.RecordDefaults(ctx, resp.Private, defaults, &resp.Diagnostics)

	a, err := r.find(ctx, gwID, id)
	if err != nil {
		resp.Diagnostics.AddError("Failed to Read Authorization After Update", err.Error())
		return
	}
	poolID, backendID := plan.PoolID, plan.BackendID
	adopt, wasAdopted := plan.AdoptExisting, state.Adopted
	plan.fromAPI(a)
	plan.PoolID, plan.BackendID, plan.AdoptExisting, plan.Adopted = poolID, backendID, adopt, wasAdopted
	plan.tagsFromAPI(ctx, a, &resp.Diagnostics)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// ModifyPlan plans tags_all: a default_tags change, or a managed key's drift,
// plans an in-place update of the authorization's tags.
func (r *authorizationResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	tftags.PlanTagsAll(ctx, r.client.DefaultTags(), req, resp)
	tftags.WarnOfferTagBudget(ctx, r.client.DefaultTags(), req, resp)
}

func (r *authorizationResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state AuthorizationModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	_, err := r.client.Delete(ctx, r.client.TenantPath(fmt.Sprintf(
		"/application-gateways/%s/authorizations/%s",
		state.GatewayID.ValueString(), state.ID.ValueString(),
	)))
	if err != nil && !client.IsNotFound(err) {
		resp.Diagnostics.AddError("Failed to Revoke Authorization", err.Error())
	}
}

func (r *authorizationResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	parts, err := client.ParseImportID(req.ID, "gateway_id", "pool_id", "backend_id", "authorization_id")
	if err != nil {
		resp.Diagnostics.AddError("Invalid Import ID", err.Error())
		return
	}
	for i, p := range parts {
		if p == "" {
			resp.Diagnostics.AddError("Invalid Import ID",
				fmt.Sprintf("Segment %d of the import ID is empty: %s", i+1, req.ID))
			return
		}
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("gateway_id"), parts[0])...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("pool_id"), parts[1])...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("backend_id"), parts[2])...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), parts[3])...)
	// adopt_existing is configuration, never read back, and RequiresReplace: left
	// null by the import, its Default (false) planned a replacement of every
	// imported authorization. Importing an existing rule is not an adoption by a
	// create, so the default is the honest value.
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("adopt_existing"), false)...)
	tftags.MarkImported(ctx, resp.Private, &resp.Diagnostics)
}
