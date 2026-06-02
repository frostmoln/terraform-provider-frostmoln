package lb_pool

import (
	"context"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
)

var (
	_ resource.Resource                = &poolResource{}
	_ resource.ResourceWithImportState = &poolResource{}
)

type poolResource struct {
	client *client.Client
}

// NewResource returns a new pool resource factory.
func NewResource() resource.Resource {
	return &poolResource{}
}

func (r *poolResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_lb_pool"
}

func (r *poolResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manages a backend pool on a Frostmoln load balancer.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Description: "The unique identifier of the pool.",
				Computed:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"load_balancer_id": schema.StringAttribute{
				Description: "The ID of the load balancer this pool belongs to. Changing this forces a new resource.",
				Required:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"listener_id": schema.StringAttribute{
				Description: "The ID of the listener this pool is attached to. Changing this forces a new resource.",
				Optional:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"name": schema.StringAttribute{
				Description: "The name of the pool.",
				Required:    true,
			},
			"protocol": schema.StringAttribute{
				Description: "The pool protocol: tcp, udp, sctp, http, https, or terminated_https.",
				Required:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
				Validators: []validator.String{
					stringvalidator.OneOf("tcp", "udp", "sctp", "http", "https", "terminated_https"),
				},
			},
			"lb_algorithm": schema.StringAttribute{
				Description: "The load balancing algorithm: round_robin, least_connections, or source_ip.",
				Required:    true,
				Validators: []validator.String{
					stringvalidator.OneOf("round_robin", "least_connections", "source_ip"),
				},
			},
			"proxy_protocol": schema.StringAttribute{
				Description: "The PROXY protocol version sent to backend members: none (default), v1, or v2.",
				Optional:    true,
				Computed:    true,
				Default:     stringdefault.StaticString("none"),
				Validators: []validator.String{
					stringvalidator.OneOf("none", "v1", "v2"),
				},
			},
			"session_persistence": schema.SingleNestedAttribute{
				Description: "Session persistence configuration for the pool. Omit for no persistence.",
				Optional:    true,
				Attributes: map[string]schema.Attribute{
					"type": schema.StringAttribute{
						Description: "The session persistence type: SOURCE_IP, HTTP_COOKIE, or APP_COOKIE.",
						Required:    true,
						Validators: []validator.String{
							stringvalidator.OneOf("SOURCE_IP", "HTTP_COOKIE", "APP_COOKIE"),
						},
					},
					"cookie_name": schema.StringAttribute{
						Description: "The cookie name to use for persistence (required for APP_COOKIE).",
						Optional:    true,
					},
					"persistence_timeout": schema.Int64Attribute{
						Description: "The persistence timeout in seconds.",
						Optional:    true,
					},
					"persistence_granularity": schema.StringAttribute{
						Description: "The persistence granularity (netmask) for SOURCE_IP persistence.",
						Optional:    true,
					},
				},
			},
			"created_at": schema.StringAttribute{
				Description: "The creation timestamp.",
				Computed:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"updated_at": schema.StringAttribute{
				Description: "The last update timestamp.",
				Computed:    true,
			},
		},
	}
}

func (r *poolResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	c, ok := req.ProviderData.(*client.Client)
	if !ok {
		resp.Diagnostics.AddError(
			"Unexpected Provider Data",
			fmt.Sprintf("Expected *client.Client, got %T", req.ProviderData),
		)
		return
	}
	r.client = c
}

func (r *poolResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan PoolModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	lbID := plan.LoadBalancerID.ValueString()
	createReq := plan.toCreateRequest()

	apiResp, err := r.client.Post(ctx, r.client.TenantPath(fmt.Sprintf("/load-balancers/%s/pools", lbID)), createReq)
	if err != nil {
		resp.Diagnostics.AddError("Failed to Create Pool", err.Error())
		return
	}

	pool, err := client.ParseResponse[apiPool](apiResp)
	if err != nil {
		resp.Diagnostics.AddError("Failed to Parse Pool Response", err.Error())
		return
	}

	plan.fromAPI(ctx, pool, &resp.Diagnostics)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *poolResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state PoolModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	lbID := state.LoadBalancerID.ValueString()
	poolID := state.ID.ValueString()

	apiResp, err := r.client.Get(ctx, r.client.TenantPath(fmt.Sprintf("/load-balancers/%s/pools/%s", lbID, poolID)), nil)
	if err != nil {
		if client.IsNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Failed to Read Pool", err.Error())
		return
	}

	pool, err := client.ParseResponse[apiPool](apiResp)
	if err != nil {
		resp.Diagnostics.AddError("Failed to Parse Pool Response", err.Error())
		return
	}

	state.fromAPI(ctx, pool, &resp.Diagnostics)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *poolResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan PoolModel
	var state PoolModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	lbID := state.LoadBalancerID.ValueString()
	poolID := state.ID.ValueString()
	updateReq := plan.toUpdateRequest()

	apiResp, err := r.client.Put(ctx, r.client.TenantPath(fmt.Sprintf("/load-balancers/%s/pools/%s", lbID, poolID)), updateReq)
	if err != nil {
		resp.Diagnostics.AddError("Failed to Update Pool", err.Error())
		return
	}

	pool, err := client.ParseResponse[apiPool](apiResp)
	if err != nil {
		resp.Diagnostics.AddError("Failed to Parse Pool Response", err.Error())
		return
	}

	plan.fromAPI(ctx, pool, &resp.Diagnostics)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *poolResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state PoolModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	lbID := state.LoadBalancerID.ValueString()
	poolID := state.ID.ValueString()

	_, err := r.client.Delete(ctx, r.client.TenantPath(fmt.Sprintf("/load-balancers/%s/pools/%s", lbID, poolID)))
	if err != nil {
		if client.IsNotFound(err) {
			return
		}
		resp.Diagnostics.AddError("Failed to Delete Pool", err.Error())
		return
	}
}

func (r *poolResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	// Import ID format: {load_balancer_id}/{pool_id}
	parts := strings.SplitN(req.ID, "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		resp.Diagnostics.AddError(
			"Invalid Import ID",
			fmt.Sprintf("Expected import ID format: {load_balancer_id}/{pool_id}, got: %s", req.ID),
		)
		return
	}

	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("load_balancer_id"), parts[0])...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), parts[1])...)
}
