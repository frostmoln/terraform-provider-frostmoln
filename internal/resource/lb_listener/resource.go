package lb_listener

import (
	"context"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/listvalidator"
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
	_ resource.Resource                = &listenerResource{}
	_ resource.ResourceWithImportState = &listenerResource{}
)

type listenerResource struct {
	client *client.Client
}

// NewResource returns a new listener resource factory.
func NewResource() resource.Resource {
	return &listenerResource{}
}

func (r *listenerResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_lb_listener"
}

func (r *listenerResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manages a listener on a Frostmoln load balancer.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Description: "The unique identifier of the listener.",
				Computed:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"load_balancer_id": schema.StringAttribute{
				Description: "The ID of the load balancer this listener belongs to. Changing this forces a new resource.",
				Required:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"name": schema.StringAttribute{
				Description: "The name of the listener.",
				Required:    true,
			},
			"protocol": schema.StringAttribute{
				Description: "The listener protocol: tcp, udp, sctp, http, https, or terminated_https.",
				Required:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
				Validators: []validator.String{
					stringvalidator.OneOf("tcp", "udp", "sctp", "http", "https", "terminated_https"),
				},
			},
			"protocol_port": schema.Int64Attribute{
				Description: "The port the listener accepts connections on. Changing this forces a new resource.",
				Required:    true,
				PlanModifiers: []planmodifier.Int64{
					int64planmodifier.RequiresReplace(),
				},
			},
			"allowed_cidrs": schema.ListAttribute{
				Description: "The CIDRs allowed to connect to this listener. This is deny-by-default: at least one CIDR is required. To allow all, set this explicitly to [\"0.0.0.0/0\"].",
				Required:    true,
				ElementType: types.StringType,
				Validators: []validator.List{
					listvalidator.SizeAtLeast(1),
				},
			},
			"insert_headers": schema.MapAttribute{
				Description: "Headers to insert into requests forwarded to backend members (HTTP/terminated_https listeners).",
				Optional:    true,
				ElementType: types.StringType,
			},
			"default_pool_id": schema.StringAttribute{
				Description: "The default pool ID requests are forwarded to.",
				Optional:    true,
			},
			"tls_certificate_id": schema.StringAttribute{
				Description: "The TLS certificate (secret) ID for terminated_https listeners.",
				Optional:    true,
			},
			"connection_limit": schema.Int64Attribute{
				Description: "The maximum number of concurrent connections allowed. If omitted, the backend default is used and reflected here.",
				Optional:    true,
				Computed:    true,
				PlanModifiers: []planmodifier.Int64{
					int64planmodifier.UseStateForUnknown(),
				},
			},
			"admin_state_up": schema.BoolAttribute{
				Description: "Whether the listener is administratively enabled.",
				Computed:    true,
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

func (r *listenerResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (r *listenerResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan ListenerModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	lbID := plan.LoadBalancerID.ValueString()
	createReq := plan.toCreateRequest(ctx, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}

	apiResp, err := r.client.Post(ctx, r.client.TenantPath(fmt.Sprintf("/load-balancers/%s/listeners", lbID)), createReq)
	if err != nil {
		resp.Diagnostics.AddError("Failed to Create Listener", err.Error())
		return
	}

	listener, err := client.ParseResponse[apiListener](apiResp)
	if err != nil {
		resp.Diagnostics.AddError("Failed to Parse Listener Response", err.Error())
		return
	}

	plan.fromAPI(ctx, listener, &resp.Diagnostics)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *listenerResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state ListenerModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	lbID := state.LoadBalancerID.ValueString()
	listenerID := state.ID.ValueString()

	apiResp, err := r.client.Get(ctx, r.client.TenantPath(fmt.Sprintf("/load-balancers/%s/listeners/%s", lbID, listenerID)), nil)
	if err != nil {
		if client.IsNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Failed to Read Listener", err.Error())
		return
	}

	listener, err := client.ParseResponse[apiListener](apiResp)
	if err != nil {
		resp.Diagnostics.AddError("Failed to Parse Listener Response", err.Error())
		return
	}

	state.fromAPI(ctx, listener, &resp.Diagnostics)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *listenerResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan ListenerModel
	var state ListenerModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	lbID := state.LoadBalancerID.ValueString()
	listenerID := state.ID.ValueString()
	updateReq := plan.toUpdateRequest(ctx, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}

	apiResp, err := r.client.Put(ctx, r.client.TenantPath(fmt.Sprintf("/load-balancers/%s/listeners/%s", lbID, listenerID)), updateReq)
	if err != nil {
		resp.Diagnostics.AddError("Failed to Update Listener", err.Error())
		return
	}

	listener, err := client.ParseResponse[apiListener](apiResp)
	if err != nil {
		resp.Diagnostics.AddError("Failed to Parse Listener Response", err.Error())
		return
	}

	plan.fromAPI(ctx, listener, &resp.Diagnostics)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *listenerResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state ListenerModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	lbID := state.LoadBalancerID.ValueString()
	listenerID := state.ID.ValueString()

	_, err := r.client.Delete(ctx, r.client.TenantPath(fmt.Sprintf("/load-balancers/%s/listeners/%s", lbID, listenerID)))
	if err != nil {
		if client.IsNotFound(err) {
			return
		}
		resp.Diagnostics.AddError("Failed to Delete Listener", err.Error())
		return
	}
}

func (r *listenerResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	// Import ID format: {load_balancer_id}/{listener_id}
	parts := strings.SplitN(req.ID, "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		resp.Diagnostics.AddError(
			"Invalid Import ID",
			fmt.Sprintf("Expected import ID format: {load_balancer_id}/{listener_id}, got: %s", req.ID),
		)
		return
	}

	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("load_balancer_id"), parts[0])...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), parts[1])...)
}
