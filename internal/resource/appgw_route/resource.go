package appgw_route

import (
	"context"
	"fmt"
	"regexp"
	"sort"

	"github.com/hashicorp/terraform-plugin-framework-validators/listvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/mapvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"

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
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/appgwvalidate"

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
				Description: "Headers to set on requests forwarded to the backend. Names are " +
					"HTTP tokens and additionally may not contain `#` or an apostrophe, " +
					"which the gateway cannot render. Values must be non-empty, at most 1024 bytes, and free of control characters. Checked at plan time.",
				Optional:      true,
				ElementType:   types.StringType,
				Validators:    []validator.Map{mapvalidator.SizeAtLeast(1)},
				PlanModifiers: []planmodifier.Map{mapplanmodifier.RequiresReplace()},
			},
			"request_headers_remove": schema.ListAttribute{
				Description: "Headers to strip from requests before forwarding. This is also " +
					"how you remove a header: setting one to an empty string is refused. " +
					"Names follow the same rule as `request_headers_set`.",
				Optional:    true,
				ElementType: types.StringType,
				// SizeAtLeast(1): the server omits an empty collection entirely,
				// so `= []` plans as empty and applies as null. Omit the
				// attribute instead; they mean the same thing here.
				Validators:    []validator.List{listvalidator.SizeAtLeast(1)},
				PlanModifiers: []planmodifier.List{listplanmodifier.RequiresReplace()},
			},
			"response_headers_set": schema.MapAttribute{
				Description: "Headers to set on responses returned to the client. Same name " +
					"and value rules as `request_headers_set`, checked at plan time.",
				Optional:      true,
				ElementType:   types.StringType,
				Validators:    []validator.Map{mapvalidator.SizeAtLeast(1)},
				PlanModifiers: []planmodifier.Map{mapplanmodifier.RequiresReplace()},
			},
			"waf_policy_id": schema.StringAttribute{
				Description: "The WAF policy applied to this route, if any — an `overlay`-scoped " +
					"policy. Read-only here: attach one with " +
					"`frostmoln_appgw_waf_policy_attachment`, which is where the attachment's " +
					"lifecycle lives.",
				Computed: true,
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

// ValidateConfig catches the REGEX and HEADER problems at PLAN time.
//
// Named narrowly on purpose. It does NOT cover everything the server refuses --
// `host` (RFC 1123 with an optional single `*.`), `path` on a non-regex route,
// and the `rewrite_path_prefix` + `path_match_type` coupling are all checked
// server-side and not here. A comment promising "what the server would refuse"
// would be read as a guarantee and is a list that silently goes stale.
//
// Worth doing here rather than leaving to the server: a bad value discovered at
// apply time has usually already created the gateway and the listener, and the
// practitioner then has to unpick a half-built stack over a typo.
//
// 🔴 THE HEADER CHECKS RUN FOR EVERY ROUTE, NOT ONLY REGEX ONES. They used to
// sit below the `path_match_type != "regex"` return, so on the overwhelming
// majority of routes -- every prefix and exact one -- this function looked at
// the path and then stopped, and the header maps were never examined at all.
// Plan-time validation that only runs on one match type is not plan-time
// validation; it is a regex check wearing its name.
func (r *routeResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var cfg RouteModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}

	validateHeaderMap(ctx, cfg.RequestHeadersSet, path.Root("request_headers_set"), resp)
	validateHeaderMap(ctx, cfg.ResponseHeadersSet, path.Root("response_headers_set"), resp)
	validateHeaderNames(ctx, cfg.RequestHeadersRemove, path.Root("request_headers_remove"), resp)

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

// validateHeaderMap checks a name/value map the way the server does.
//
// 🔴 A DIAGNOSTIC NAMES THE HEADER AND NEVER THE VALUE. The server refuses this
// way for a stated reason -- a header value is where a tenant's upstream
// credential lives -- and a Terraform diagnostic is a worse place to leak one
// than an API error: it lands in plan output, in CI logs, and in whatever
// captures them.
func validateHeaderMap(_ context.Context, m types.Map, at path.Path, resp *resource.ValidateConfigResponse) {
	if m.IsNull() || m.IsUnknown() {
		return
	}

	// 🔴 ELEMENT BY ELEMENT, NOT ElementsAs -- AND THE REASON IS THE BUG THIS
	// FUNCTION EXISTS TO CLOSE.
	//
	// ElementsAs converts the WHOLE map and errors on any unknown element, so a
	// single value interpolated from another resource made this function return
	// having checked nothing. That is not a rare config; it is the ordinary one:
	//
	//     request_headers_set = {
	//       "X Forwarded"   = "1"                       # the server 400s on this
	//       "Authorization" = frostmoln_secret.up.value # unknown at plan
	//     }
	//
	// Plan clean, apply fails, gateway and listener already created -- exactly
	// the half-built stack this validation is for.
	//
	// A map KEY can never be unknown: Terraform cannot produce a known map with
	// an unknown key. So every name here is checkable even when a value is not,
	// and only the individual unknown VALUE is deferred to the server.
	for _, name := range sortedKeys(m.Elements()) {
		checkHeaderName(name, at.AtMapKey(name), resp)

		el := m.Elements()[name]
		if el.IsUnknown() || el.IsNull() {
			continue
		}
		v, ok := el.(types.String)
		if !ok {
			continue
		}
		checkHeaderValue(name, v.ValueString(), at.AtMapKey(name), resp)
	}
}

// checkHeaderValue applies the server's value rules to one known value.
func checkHeaderValue(name, value string, at path.Path, resp *resource.ValidateConfigResponse) {
	switch {
	case value == "":
		resp.Diagnostics.AddAttributeError(at, "Header Value Must Not Be Empty",
			fmt.Sprintf("The value of %q is empty. The gateway refuses this: an empty "+
				"pair of quotes collapses and leaves a directive the proxy will not "+
				"parse. To REMOVE a header, list it in request_headers_remove instead.",
				name))
	case len(value) > appgwvalidate.MaxHeaderValueLength:
		resp.Diagnostics.AddAttributeError(at, "Header Value Is Too Long",
			fmt.Sprintf("The value of %q is %d bytes; the gateway accepts %d or fewer. "+
				"The bound is on BYTES, so a non-ASCII value reaches it sooner than its "+
				"character count suggests.", name, len(value), appgwvalidate.MaxHeaderValueLength))
	default:
		for _, r := range value {
			if r < 0x20 || r == 0x7f {
				resp.Diagnostics.AddAttributeError(at, "Header Value Contains a Control Character",
					fmt.Sprintf("The value of %q contains a control character. A newline "+
						"ends the directive whatever quoting is in force, so the gateway "+
						"refuses it.", name))
				break
			}
		}
	}
}

// sortedKeys gives the diagnostics a deterministic order. Ranging a Go map
// would emit two bad headers in a different order on each run, which makes plan
// output irreproducible in a CI diff.
func sortedKeys(m map[string]attr.Value) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// validateHeaderNames checks a list of bare header names (the remove list).
func validateHeaderNames(_ context.Context, l types.List, at path.Path, resp *resource.ValidateConfigResponse) {
	if l.IsNull() || l.IsUnknown() {
		return
	}
	// Element by element for the same reason as the map -- but here an unknown
	// element genuinely IS an unknown name, so only the known ones are
	// salvageable. Checking those still beats checking none.
	for i, el := range l.Elements() {
		if el.IsUnknown() || el.IsNull() {
			continue
		}
		v, ok := el.(types.String)
		if !ok {
			continue
		}
		checkHeaderName(v.ValueString(), at.AtListIndex(i), resp)
	}
}

func checkHeaderName(name string, at path.Path, resp *resource.ValidateConfigResponse) {
	switch {
	case name == "":
		resp.Diagnostics.AddAttributeError(at, "Header Name Must Not Be Empty",
			"A header name must not be empty.")
	case !appgwvalidate.Token.MatchString(name):
		resp.Diagnostics.AddAttributeError(at, "Invalid Header Name",
			fmt.Sprintf("%q is not a valid header name. HTTP allows letters, digits and "+
				"!#$%%&'*+-.^_`|~ — a space or a colon is what usually causes this.", name))
	case appgwvalidate.HasUnrenderable(name):
		resp.Diagnostics.AddAttributeError(at, "Header Name the Gateway Cannot Render",
			fmt.Sprintf("The header name %q contains '#' or an apostrophe. Both are legal "+
				"HTTP but the gateway cannot render them: it would refuse its whole "+
				"configuration and go on serving the previous one.", name))
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
