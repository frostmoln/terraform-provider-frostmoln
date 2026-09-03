// Package appgw_backend_authorization implements the
// frostmoln_appgw_backend_authorization Terraform resource.
package appgw_backend_authorization

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/boolplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
)

var (
	_ resource.Resource                = &authorizationResource{}
	_ resource.ResourceWithImportState = &authorizationResource{}
	_ resource.ResourceWithConfigure   = &authorizationResource{}
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
}

type apiAuthorizationListResponse struct {
	Items []apiAuthorization `json:"items"`
}

type apiAuthorizeRequest struct {
	SecurityGroupID string `json:"securityGroupId"`
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
			"Every authorize and revoke is recorded in the gateway's audit trail.",
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
	// refresh silently turn a shared rule into one that looks exclusive.
	if a.Adopted {
		m.Adopted = types.BoolValue(true)
	}
	m.AuthorizedBy = types.StringValue(a.AuthorizedBy)
	m.CreatedAt = types.StringValue(a.CreatedAt)
}

func (r *authorizationResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan AuthorizationModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	apiResp, err := r.client.Post(ctx, r.client.TenantPath(fmt.Sprintf(
		"/application-gateways/%s/backend-pools/%s/backends/%s/authorize",
		plan.GatewayID.ValueString(), plan.PoolID.ValueString(), plan.BackendID.ValueString(),
	)),
		apiAuthorizeRequest{SecurityGroupID: plan.SecurityGroupID.ValueString()})
	if err != nil {
		resp.Diagnostics.AddError("Failed to Authorize Backend", err.Error())
		return
	}
	a, err := client.ParseResponse[apiAuthorization](apiResp)
	if err != nil {
		resp.Diagnostics.AddError("Failed to Parse Authorization Response", err.Error())
		return
	}
	poolID, backendID, adopt := plan.PoolID, plan.BackendID, plan.AdoptExisting
	plan.fromAPI(a)
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
// There is no get-by-id: the server registers POST (authorize), GET (list) and
// DELETE. Not finding it means the path was closed outside Terraform.
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
		if list.Items[i].ID == want {
			poolID, backendID := state.PoolID, state.BackendID
			adopt, wasAdopted := state.AdoptExisting, state.Adopted
			state.fromAPI(&list.Items[i])
			state.PoolID, state.BackendID = poolID, backendID
			state.AdoptExisting = adopt
			// Preserved, not refreshed: the listing cannot report it (no
			// column), so the server's silence would otherwise read as "this
			// rule is exclusive" for one that is shared.
			state.Adopted = wasAdopted
			resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
			return
		}
	}
	resp.State.RemoveResource(ctx)
}

// Update cannot be reached: every attribute carries RequiresReplace.
func (r *authorizationResource) Update(_ context.Context, _ resource.UpdateRequest, resp *resource.UpdateResponse) {
	resp.Diagnostics.AddError("Authorizations Cannot Be Updated In Place",
		"An authorization is a single ingress rule; changing what it permits means revoking it and "+
			"opening a new one. Every attribute of this resource forces a replacement.")
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
}
