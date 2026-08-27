package appgw_route

import (
	"context"
	"fmt"
	"regexp"

	"github.com/hashicorp/terraform-plugin-framework-validators/listvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/mapvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/listplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/mapplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
)

var (
	_ resource.Resource                   = &routeResource{}
	_ resource.ResourceWithImportState    = &routeResource{}
	_ resource.ResourceWithConfigure      = &routeResource{}
	_ resource.ResourceWithValidateConfig = &routeResource{}
)

type routeResource struct {
	client *client.Client
}

// NewResource returns a new Application Gateway route resource factory.
func NewResource() resource.Resource {
	return &routeResource{}
}

func (r *routeResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_appgw_route"
}

func (r *routeResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	replaceStr := []planmodifier.String{stringplanmodifier.RequiresReplace()}

	resp.Schema = schema.Schema{
		Description: "Manages a route on an Application Gateway listener: a host and path match that " +
			"forwards to a backend pool.\n\n" +
			"**Priority is explicit and lower wins.** There is no implicit longest-prefix rule, " +
			"because that would make one route's effect depend on another route's contents — adding a " +
			"route would silently reorder the others while the plan showed no change. Leave `priority` " +
			"unset and the server places the route last.\n\n" +
			"The route API has no update operation, so every attribute forces a new resource.\n\n" +
			"A route is authored, not live: it starts serving on the gateway's next configuration apply.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Description:   "The unique identifier of the route.",
				Computed:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"gateway_id": schema.StringAttribute{
				Description:   "The Application Gateway this route belongs to.",
				Required:      true,
				PlanModifiers: replaceStr,
			},
			"listener_id": schema.StringAttribute{
				Description:   "The listener this route hangs off.",
				Required:      true,
				PlanModifiers: replaceStr,
			},
			"name": schema.StringAttribute{
				Description:   "The name of the route.",
				Required:      true,
				PlanModifiers: replaceStr,
			},
			"priority": schema.Int64Attribute{
				Description: "Evaluation priority; **lower wins**. Omit to have the server place this " +
					"route last, which is the safe default — a route given priority 0 by accident would " +
					"take precedence over everything already configured.",
				Optional:      true,
				Computed:      true,
				PlanModifiers: []planmodifier.Int64{int64planmodifier.UseStateForUnknown(), int64planmodifier.RequiresReplace()},
			},
			"host": schema.StringAttribute{
				Description:   "The host to match, e.g. `api.example.com` or `*.example.com`. Omit to match any host.",
				Optional:      true,
				Validators:    []validator.String{stringvalidator.LengthAtLeast(1)},
				PlanModifiers: replaceStr,
			},
			"path_match_type": schema.StringAttribute{
				Description:   "How `path` is matched: `prefix` (default), `exact` or `regex` (RE2).",
				Optional:      true,
				Computed:      true,
				Validators:    []validator.String{stringvalidator.OneOf("prefix", "exact", "regex")},
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown(), stringplanmodifier.RequiresReplace()},
			},
			"path": schema.StringAttribute{
				Description:   "The path to match. Required when `path_match_type` is `regex`.",
				Optional:      true,
				Computed:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown(), stringplanmodifier.RequiresReplace()},
			},
			"backend_pool_id": schema.StringAttribute{
				Description:   "The backend pool to forward matching requests to.",
				Required:      true,
				PlanModifiers: replaceStr,
			},
			"action": schema.StringAttribute{
				Description:   "What the route does. Only `forward` is available.",
				Optional:      true,
				Computed:      true,
				Validators:    []validator.String{stringvalidator.OneOf("forward")},
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown(), stringplanmodifier.RequiresReplace()},
			},
			"rewrite_path_prefix": schema.StringAttribute{
				Description:   "Replace the matched path prefix with this before forwarding.",
				Optional:      true,
				PlanModifiers: replaceStr,
			},
			"request_headers_set": schema.MapAttribute{
				Description:   "Headers to set on requests forwarded to the backend.",
				Optional:      true,
				ElementType:   types.StringType,
				Validators:    []validator.Map{mapvalidator.SizeAtLeast(1)},
				PlanModifiers: []planmodifier.Map{mapplanmodifier.RequiresReplace()},
			},
			"request_headers_remove": schema.ListAttribute{
				Description: "Headers to strip from requests before forwarding.",
				Optional:    true,
				ElementType: types.StringType,
				// SizeAtLeast(1): the server omits an empty collection entirely,
				// so `= []` plans as empty and applies as null. Omit the
				// attribute instead; they mean the same thing here.
				Validators:    []validator.List{listvalidator.SizeAtLeast(1)},
				PlanModifiers: []planmodifier.List{listplanmodifier.RequiresReplace()},
			},
			"response_headers_set": schema.MapAttribute{
				Description:   "Headers to set on responses returned to the client.",
				Optional:      true,
				ElementType:   types.StringType,
				Validators:    []validator.Map{mapvalidator.SizeAtLeast(1)},
				PlanModifiers: []planmodifier.Map{mapplanmodifier.RequiresReplace()},
			},
			"waf_policy_id": schema.StringAttribute{
				Description: "The WAF policy applied to this route, if any.",
				Computed:    true,
			},
			"enabled": schema.BoolAttribute{
				Description: "Whether the route is serving.",
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

// ValidateConfig catches the regex problems at PLAN time.
//
// Worth doing here rather than leaving to the server: a bad pattern discovered
// at apply time has usually already created the gateway and the listener, and
// the practitioner then has to unpick a half-built stack over a typo.
func (r *routeResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var cfg RouteModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if cfg.PathMatchType.IsUnknown() || cfg.PathMatchType.ValueString() != "regex" {
		return
	}
	if cfg.Path.IsNull() {
		resp.Diagnostics.AddAttributeError(path.Root("path"),
			"path Is Required With path_match_type = \"regex\"",
			"A regex route matches on its pattern, so there must be one.")
		return
	}
	if cfg.Path.IsUnknown() {
		return
	}
	// The server compiles with Go's regexp (RE2), which is what makes
	// catastrophic backtracking inexpressible. Compiling here with the same
	// engine means a pattern accepted at plan time is accepted at apply time.
	if _, err := regexp.Compile(cfg.Path.ValueString()); err != nil {
		resp.Diagnostics.AddAttributeError(path.Root("path"),
			"path Is Not a Valid Regular Expression",
			fmt.Sprintf("The gateway compiles route patterns with RE2: %s", err))
	}
}

func (r *routeResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (r *routeResource) basePath(gwID, listenerID string) string {
	return r.client.TenantPath(fmt.Sprintf("/application-gateways/%s/listeners/%s/routes", gwID, listenerID))
}

func (r *routeResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan RouteModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	createReq := plan.toCreateRequest(ctx, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	apiResp, err := r.client.Post(ctx,
		r.basePath(plan.GatewayID.ValueString(), plan.ListenerID.ValueString()), createReq)
	if err != nil {
		resp.Diagnostics.AddError("Failed to Create Route", err.Error())
		return
	}
	rt, err := client.ParseResponse[apiRoute](apiResp)
	if err != nil {
		resp.Diagnostics.AddError("Failed to Parse Route Response", err.Error())
		return
	}
	gwID := plan.GatewayID
	plan.fromAPI(ctx, rt, &resp.Diagnostics)
	// fromAPI does not set gateway_id: the route payload does not carry it (a
	// route belongs to a listener), so it is carried over from the plan.
	plan.GatewayID = gwID
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *routeResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state RouteModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	apiResp, err := r.client.Get(ctx, fmt.Sprintf("%s/%s",
		r.basePath(state.GatewayID.ValueString(), state.ListenerID.ValueString()), state.ID.ValueString()), nil)
	if err != nil {
		if client.IsNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Failed to Read Route", err.Error())
		return
	}
	rt, err := client.ParseResponse[apiRoute](apiResp)
	if err != nil {
		resp.Diagnostics.AddError("Failed to Parse Route Response", err.Error())
		return
	}
	gwID := state.GatewayID
	state.fromAPI(ctx, rt, &resp.Diagnostics)
	state.GatewayID = gwID
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Update cannot be reached: every attribute carries RequiresReplace.
func (r *routeResource) Update(_ context.Context, _ resource.UpdateRequest, resp *resource.UpdateResponse) {
	resp.Diagnostics.AddError("Routes Cannot Be Updated In Place",
		"The Application Gateway API has no route update operation, so every attribute of this "+
			"resource forces a replacement. Reaching this code means an attribute was added to the "+
			"schema without RequiresReplace.")
}

func (r *routeResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state RouteModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	_, err := r.client.Delete(ctx, fmt.Sprintf("%s/%s",
		r.basePath(state.GatewayID.ValueString(), state.ListenerID.ValueString()), state.ID.ValueString()))
	if err != nil && !client.IsNotFound(err) {
		resp.Diagnostics.AddError("Failed to Delete Route", err.Error())
	}
}

func (r *routeResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	parts, err := client.ParseImportID(req.ID, "gateway_id", "listener_id", "route_id")
	if err != nil {
		resp.Diagnostics.AddError("Invalid Import ID", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("gateway_id"), parts[0])...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("listener_id"), parts[1])...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), parts[2])...)
}
