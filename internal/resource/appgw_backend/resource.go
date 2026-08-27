// Package appgw_backend implements the frostmoln_appgw_backend Terraform
// resource.
package appgw_backend

import (
	"context"
	"fmt"
	"net/netip"

	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
)

var (
	_ resource.Resource                   = &backendResource{}
	_ resource.ResourceWithImportState    = &backendResource{}
	_ resource.ResourceWithConfigure      = &backendResource{}
	_ resource.ResourceWithValidateConfig = &backendResource{}
)

// BackendModel is the Terraform state model for a backend.
type BackendModel struct {
	ID         types.String `tfsdk:"id"`
	GatewayID  types.String `tfsdk:"gateway_id"`
	PoolID     types.String `tfsdk:"pool_id"`
	SourceKind types.String `tfsdk:"source_kind"`
	SourceID   types.String `tfsdk:"source_id"`
	Address    types.String `tfsdk:"address"`
	Port       types.Int64  `tfsdk:"port"`
	Weight     types.Int64  `tfsdk:"weight"`
	Status     types.String `tfsdk:"status"`
	Enabled    types.Bool   `tfsdk:"enabled"`
	CreatedAt  types.String `tfsdk:"created_at"`
	UpdatedAt  types.String `tfsdk:"updated_at"`
}

type apiBackend struct {
	ID         string `json:"id"`
	PoolID     string `json:"poolId"`
	SourceKind string `json:"sourceKind"`
	SourceID   string `json:"sourceId,omitempty"`
	Address    string `json:"address"`
	Port       int    `json:"port"`
	Weight     int    `json:"weight"`
	Status     string `json:"status"`
	Enabled    bool   `json:"enabled"`
	CreatedAt  string `json:"createdAt"`
	UpdatedAt  string `json:"updatedAt,omitempty"`
}

type apiBackendListResponse struct {
	Backends []apiBackend `json:"backends"`
}

type apiCreateBackendRequest struct {
	SourceKind string `json:"sourceKind,omitempty"`
	SourceID   string `json:"sourceId,omitempty"`
	Address    string `json:"address,omitempty"`
	Port       int    `json:"port"`
	Weight     int    `json:"weight,omitempty"`
}

type backendResource struct {
	client *client.Client
}

// NewResource returns a new backend resource factory.
func NewResource() resource.Resource {
	return &backendResource{}
}

func (r *backendResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_appgw_backend"
}

func (r *backendResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	replaceStr := []planmodifier.String{stringplanmodifier.RequiresReplace()}
	replaceInt := []planmodifier.Int64{int64planmodifier.RequiresReplace()}

	resp.Schema = schema.Schema{
		Description: "Manages a backend in an Application Gateway pool: one destination inside your " +
			"VPC, named by its IP address.\n\n" +
			"Two things gate a backend, both deliberately:\n\n" +
			"1. **The address must lie inside the gateway's own VPC range.** Anything else is refused — " +
			"without that check the gateway would be an open proxy.\n" +
			"2. **Creating a backend does not open the path to it.** The platform never edits your " +
			"security groups implicitly. Use `frostmoln_appgw_backend_authorization` to add the one " +
			"ingress rule, which is audited.\n\n" +
			"The backend API has no update operation, so every attribute forces a new resource.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Description:   "The unique identifier of the backend.",
				Computed:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"gateway_id": schema.StringAttribute{
				Description:   "The Application Gateway this backend's pool belongs to.",
				Required:      true,
				PlanModifiers: replaceStr,
			},
			"pool_id": schema.StringAttribute{
				Description:   "The backend pool this backend belongs to.",
				Required:      true,
				PlanModifiers: replaceStr,
			},
			"source_kind": schema.StringAttribute{
				Description: "How the destination is named. `address` is the only value available: " +
					"`address` is an IP inside your VPC.\n\n" +
					"`instance` and `load_balancer` are reserved for naming a backend by an instance " +
					"or load balancer id and letting the platform resolve its address. The platform " +
					"**refuses** them today, so they are not offered here rather than being accepted " +
					"and failing partway through an apply that has already built the gateway.",
				Optional:      true,
				Computed:      true,
				Validators:    []validator.String{stringvalidator.OneOf("address")},
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown(), stringplanmodifier.RequiresReplace()},
			},
			"source_id": schema.StringAttribute{
				Description: "Reserved. It will name the instance or load balancer whose address the " +
					"platform resolves, once `source_kind` gains those values; it is refused today.",
				Optional:      true,
				Validators:    []validator.String{stringvalidator.LengthAtLeast(1)},
				PlanModifiers: replaceStr,
			},
			"address": schema.StringAttribute{
				Description: "The backend's IP address. It must lie inside the gateway's own VPC " +
					"range; anything else is refused.",
				Optional:      true,
				Computed:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown(), stringplanmodifier.RequiresReplace()},
			},
			"port": schema.Int64Attribute{
				Description:   "The port the backend listens on.",
				Required:      true,
				Validators:    []validator.Int64{int64validator.Between(1, 65535)},
				PlanModifiers: replaceInt,
			},
			"weight": schema.Int64Attribute{
				Description: "Relative share of traffic among the pool's backends. Omit to use the " +
					"platform default.",
				Optional: true,
				Computed: true,
				// AtLeast(1), not 0: the server coerces a 0 to its default, so a
				// configured 0 would come back as 1 and fail the apply with
				// "inconsistent result after apply".
				Validators:    []validator.Int64{int64validator.AtLeast(1)},
				PlanModifiers: []planmodifier.Int64{int64planmodifier.UseStateForUnknown(), int64planmodifier.RequiresReplace()},
			},
			"status": schema.StringAttribute{
				Description: "The backend's health as the gateway sees it.",
				Computed:    true,
			},
			"enabled": schema.BoolAttribute{
				Description: "Whether the backend is receiving traffic.",
				Computed:    true,
			},
			"created_at": schema.StringAttribute{
				Description:   "The creation timestamp.",
				Computed:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"updated_at": schema.StringAttribute{
				Description: "The last update timestamp.",
				Computed:    true,
			},
		},
	}
}

// ValidateConfig mirrors the server's discriminated-union rules at plan time.
func (r *backendResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var cfg BackendModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	kind := "address"
	if !cfg.SourceKind.IsNull() && !cfg.SourceKind.IsUnknown() {
		kind = cfg.SourceKind.ValueString()
	}
	switch kind {
	case "address":
		if !cfg.SourceID.IsNull() {
			resp.Diagnostics.AddAttributeError(path.Root("source_id"),
				"source_id Is Not Available Yet",
				"A backend is named by its `address` today. source_id is reserved for naming one by "+
					"an instance or load balancer id, which the platform does not resolve yet.")
		}
		if cfg.Address.IsNull() {
			resp.Diagnostics.AddAttributeError(path.Root("address"),
				"address Is Required With source_kind = \"address\"",
				"Name the IP inside your VPC, or set source_kind to resolve one from an instance "+
					"or a load balancer.")
			return
		}
		if cfg.Address.IsUnknown() {
			return
		}
		// The server refuses loopback, link-local, multicast and the
		// unspecified address, and checks VPC membership it alone knows. What
		// can be judged locally is judged locally, so an obvious typo is caught
		// before anything is built.
		addr, err := netip.ParseAddr(cfg.Address.ValueString())
		if err != nil {
			resp.Diagnostics.AddAttributeError(path.Root("address"),
				"address Is Not a Valid IP Address", err.Error())
			return
		}
		if addr.IsLoopback() || addr.IsLinkLocalUnicast() || addr.IsMulticast() || addr.IsUnspecified() {
			resp.Diagnostics.AddAttributeError(path.Root("address"),
				"address Must Be a Routable Address Inside Your VPC",
				fmt.Sprintf("%s is a loopback, link-local, multicast or unspecified address. The "+
					"gateway forwards to it from its own appliance, so it must be an address that "+
					"exists in your VPC.", addr))
		}
	default:
		// Unreachable through the validator, and kept as a defence: if
		// source_kind ever gains a value here without the platform gaining the
		// resolution to go with it, this says so rather than sending a request
		// that 400s after the gateway and pool are already built.
		resp.Diagnostics.AddAttributeError(path.Root("source_kind"),
			fmt.Sprintf("source_kind = %q Is Not Available Yet", kind),
			"The platform resolves only an explicit `address` today.")
	}
}

func (r *backendResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (r *backendResource) basePath(gwID, poolID string) string {
	return r.client.TenantPath(fmt.Sprintf(
		"/application-gateways/%s/backend-pools/%s/backends", gwID, poolID))
}

func (m *BackendModel) fromAPI(b *apiBackend) {
	m.ID = types.StringValue(b.ID)
	m.PoolID = types.StringValue(b.PoolID)
	m.SourceKind = types.StringValue(b.SourceKind)
	m.SourceID = optionalString(b.SourceID)
	m.Address = types.StringValue(b.Address)
	m.Port = types.Int64Value(int64(b.Port))
	m.Weight = types.Int64Value(int64(b.Weight))
	m.Status = types.StringValue(b.Status)
	m.Enabled = types.BoolValue(b.Enabled)
	m.CreatedAt = types.StringValue(b.CreatedAt)
	m.UpdatedAt = types.StringValue(b.UpdatedAt)
}

func (r *backendResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan BackendModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	createReq := apiCreateBackendRequest{
		SourceKind: str(plan.SourceKind),
		SourceID:   str(plan.SourceID),
		Address:    str(plan.Address),
		Port:       int(plan.Port.ValueInt64()),
		Weight:     int(plan.Weight.ValueInt64()),
	}
	apiResp, err := r.client.Post(ctx, r.basePath(plan.GatewayID.ValueString(), plan.PoolID.ValueString()), createReq)
	if err != nil {
		resp.Diagnostics.AddError("Failed to Create Backend", err.Error())
		return
	}
	b, err := client.ParseResponse[apiBackend](apiResp)
	if err != nil {
		resp.Diagnostics.AddError("Failed to Parse Backend Response", err.Error())
		return
	}
	gwID := plan.GatewayID
	plan.fromAPI(b)
	plan.GatewayID = gwID
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Read finds the backend by listing the pool.
//
// 🔴 THERE IS NO GET-BY-ID FOR A BACKEND. The server registers POST, GET (list)
// and DELETE only, so a refresh has to scan the pool. Not finding it is
// deletion-outside-Terraform, which is the same outcome a 404 would be.
func (r *backendResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state BackendModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	apiResp, err := r.client.Get(ctx, r.basePath(state.GatewayID.ValueString(), state.PoolID.ValueString()), nil)
	if err != nil {
		// A missing POOL means the backend is gone with it.
		if client.IsNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Failed to Read Backends", err.Error())
		return
	}
	list, err := client.ParseResponse[apiBackendListResponse](apiResp)
	if err != nil {
		resp.Diagnostics.AddError("Failed to Parse Backend Response", err.Error())
		return
	}
	want := state.ID.ValueString()
	for i := range list.Backends {
		if list.Backends[i].ID == want {
			gwID := state.GatewayID
			state.fromAPI(&list.Backends[i])
			state.GatewayID = gwID
			resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
			return
		}
	}
	resp.State.RemoveResource(ctx)
}

// Update cannot be reached: every attribute carries RequiresReplace.
func (r *backendResource) Update(_ context.Context, _ resource.UpdateRequest, resp *resource.UpdateResponse) {
	resp.Diagnostics.AddError("Backends Cannot Be Updated In Place",
		"The Application Gateway API has no backend update operation, so every attribute of this "+
			"resource forces a replacement. Reaching this code means an attribute was added to the "+
			"schema without RequiresReplace.")
}

func (r *backendResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state BackendModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	_, err := r.client.Delete(ctx, fmt.Sprintf("%s/%s",
		r.basePath(state.GatewayID.ValueString(), state.PoolID.ValueString()), state.ID.ValueString()))
	if err != nil && !client.IsNotFound(err) {
		resp.Diagnostics.AddError("Failed to Delete Backend", err.Error())
		return
	}
	// Removing a backend closes no ingress rule: the authorization is per
	// (security group, protocol, port) and may be shared. Saying so here is the
	// only place a practitioner destroying a backend will see it.
	resp.Diagnostics.AddWarning("Ingress Rule Not Closed",
		"Removing a backend does not revoke the authorization that lets the gateway reach it. "+
			"That rule is per (security group, protocol, port) and may be shared with other "+
			"backends; destroy the frostmoln_appgw_backend_authorization to close it.")
}

func (r *backendResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	parts, err := client.ParseImportID(req.ID, "gateway_id", "pool_id", "backend_id")
	if err != nil {
		resp.Diagnostics.AddError("Invalid Import ID", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("gateway_id"), parts[0])...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("pool_id"), parts[1])...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), parts[2])...)
}

func str(s types.String) string {
	if s.IsNull() || s.IsUnknown() {
		return ""
	}
	return s.ValueString()
}

func optionalString(s string) types.String {
	if s == "" {
		return types.StringNull()
	}
	return types.StringValue(s)
}
