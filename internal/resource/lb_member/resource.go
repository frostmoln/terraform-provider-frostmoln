package lb_member

import (
	"context"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/boolplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
)

var (
	_ resource.Resource                = &memberResource{}
	_ resource.ResourceWithImportState = &memberResource{}
)

type memberResource struct {
	client *client.Client
}

// NewResource returns a new pool member resource factory.
func NewResource() resource.Resource {
	return &memberResource{}
}

func (r *memberResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_lb_member"
}

// requiresReplaceUnlessPriorNull forces replacement when cross_vpc changes
// between two known values, but NOT when the prior state value is null. A null
// prior value means the member was freshly imported (cross_vpc is write-only and
// cannot be recovered from the API), so supplying the ack flag for the first time
// must reconcile state in place rather than destroy a live backend member.
func requiresReplaceUnlessPriorNull(_ context.Context, req planmodifier.BoolRequest, resp *boolplanmodifier.RequiresReplaceIfFuncResponse) {
	if req.StateValue.IsNull() {
		resp.RequiresReplace = false
		return
	}
	resp.RequiresReplace = !req.StateValue.Equal(req.PlanValue)
}

func (r *memberResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manages a backend member of a Frostmoln load balancer pool.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Description: "The unique identifier of the member.",
				Computed:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"load_balancer_id": schema.StringAttribute{
				Description: "The ID of the load balancer the pool belongs to. Changing this forces a new resource.",
				Required:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"pool_id": schema.StringAttribute{
				Description: "The ID of the pool this member belongs to. Changing this forces a new resource.",
				Required:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"address": schema.StringAttribute{
				Description: "The IP address of the backend member. Changing this forces a new resource.",
				Required:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"protocol_port": schema.Int64Attribute{
				Description: "The port the backend member listens on. Changing this forces a new resource.",
				Required:    true,
				PlanModifiers: []planmodifier.Int64{
					int64planmodifier.RequiresReplace(),
				},
			},
			"name": schema.StringAttribute{
				Description: "The name of the member.",
				Optional:    true,
			},
			"weight": schema.Int64Attribute{
				Description: "The weight of the member for weighted load balancing.",
				Optional:    true,
				Computed:    true,
			},
			"subnet_id": schema.StringAttribute{
				Description: "The subnet ID the member resides in. Changing this forces a new resource.",
				Optional:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"cross_vpc": schema.BoolAttribute{
				Description: "Whether the member is in a different VPC than the load balancer. Write-only acknowledgement flag; preserved in state but never returned by the API. Changing this between two known values forces a new resource. On import this flag cannot be recovered from the API, so it is left null and reconciled (not destroyed) on the first apply that supplies it.",
				Optional:    true,
				PlanModifiers: []planmodifier.Bool{
					boolplanmodifier.RequiresReplaceIf(
						requiresReplaceUnlessPriorNull,
						"If the value changes between two known values, Terraform will destroy and recreate the member.",
						"If the value changes between two known values, Terraform will destroy and recreate the member.",
					),
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

func (r *memberResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (r *memberResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan MemberModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	lbID := plan.LoadBalancerID.ValueString()
	poolID := plan.PoolID.ValueString()
	crossVPC := plan.CrossVPC
	createReq := plan.toCreateRequest()

	apiResp, err := r.client.Post(ctx, r.client.TenantPath(fmt.Sprintf("/load-balancers/%s/pools/%s/members", lbID, poolID)), createReq)
	if err != nil {
		resp.Diagnostics.AddError("Failed to Create Member", err.Error())
		return
	}

	member, err := client.ParseResponse[apiMember](apiResp)
	if err != nil {
		resp.Diagnostics.AddError("Failed to Parse Member Response", err.Error())
		return
	}

	plan.fromAPI(lbID, member)
	plan.CrossVPC = crossVPC // write-only, not returned by API
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *memberResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state MemberModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	lbID := state.LoadBalancerID.ValueString()
	poolID := state.PoolID.ValueString()
	memberID := state.ID.ValueString()
	crossVPC := state.CrossVPC

	apiResp, err := r.client.Get(ctx, r.client.TenantPath(fmt.Sprintf("/load-balancers/%s/pools/%s/members/%s", lbID, poolID, memberID)), nil)
	if err != nil {
		if client.IsNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Failed to Read Member", err.Error())
		return
	}

	member, err := client.ParseResponse[apiMember](apiResp)
	if err != nil {
		resp.Diagnostics.AddError("Failed to Parse Member Response", err.Error())
		return
	}

	state.fromAPI(lbID, member)
	state.CrossVPC = crossVPC // write-only, preserved from prior state
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *memberResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan MemberModel
	var state MemberModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	lbID := state.LoadBalancerID.ValueString()
	poolID := state.PoolID.ValueString()
	memberID := state.ID.ValueString()
	crossVPC := plan.CrossVPC
	updateReq := plan.toUpdateRequest()

	apiResp, err := r.client.Put(ctx, r.client.TenantPath(fmt.Sprintf("/load-balancers/%s/pools/%s/members/%s", lbID, poolID, memberID)), updateReq)
	if err != nil {
		resp.Diagnostics.AddError("Failed to Update Member", err.Error())
		return
	}

	member, err := client.ParseResponse[apiMember](apiResp)
	if err != nil {
		resp.Diagnostics.AddError("Failed to Parse Member Response", err.Error())
		return
	}

	plan.fromAPI(lbID, member)
	plan.CrossVPC = crossVPC // write-only, preserved
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *memberResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state MemberModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	lbID := state.LoadBalancerID.ValueString()
	poolID := state.PoolID.ValueString()
	memberID := state.ID.ValueString()

	_, err := r.client.Delete(ctx, r.client.TenantPath(fmt.Sprintf("/load-balancers/%s/pools/%s/members/%s", lbID, poolID, memberID)))
	if err != nil {
		if client.IsNotFound(err) {
			return
		}
		resp.Diagnostics.AddError("Failed to Delete Member", err.Error())
		return
	}
}

func (r *memberResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	// Import ID format: {load_balancer_id}/{pool_id}/{member_id}
	parts := strings.SplitN(req.ID, "/", 3)
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		resp.Diagnostics.AddError(
			"Invalid Import ID",
			fmt.Sprintf("Expected import ID format: {load_balancer_id}/{pool_id}/{member_id}, got: %s", req.ID),
		)
		return
	}

	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("load_balancer_id"), parts[0])...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("pool_id"), parts[1])...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), parts[2])...)
	// cross_vpc is write-only and cannot be recovered on import.
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("cross_vpc"), types.BoolNull())...)
}
