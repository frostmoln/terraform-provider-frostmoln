package lb_member

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/boolplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/orphan"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/scopedecl"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/timeouts"
)

var (
	_ resource.Resource                = &memberResource{}
	_ resource.ResourceWithImportState = &memberResource{}
)

type memberResource struct {
	client *client.Client

	// pollInterval and pollTimeout bound the waits for the provisioning
	// operations a write starts. Fields rather than constants so a test can
	// drive the timeout in milliseconds; the timeouts block sits on top of
	// these defaults (see resolveBudgets).
	pollInterval time.Duration
	pollTimeout  time.Duration
}

// NewResource returns a new pool member resource factory.
func NewResource() resource.Resource {
	return &memberResource{}
}

func (r *memberResource) getPollInterval() time.Duration {
	if r.pollInterval > 0 {
		return r.pollInterval
	}
	return 2 * time.Second
}

// getPollTimeout is the DEFAULT wait budget — the timeouts block's fallback
// per verb. Create has always polled the provisioning operation against 5m;
// the delete wait (an async destroy that used to be reported as done the
// moment its 202 landed) defaults to the same value so the family keeps one
// number.
func (r *memberResource) getPollTimeout() time.Duration {
	if r.pollTimeout > 0 {
		return r.pollTimeout
	}
	return 5 * time.Minute
}

// resolveBudgets turns the configured timeouts block into effective budgets,
// falling back per verb to the same value this resource has always hardcoded.
// Routing the defaults through the accessor keeps the test-injection seam
// intact: a test that shrinks pollTimeout shrinks every wait that does not
// carry an explicit timeouts override, exactly as before.
func (r *memberResource) resolveBudgets(m *timeouts.Model) timeouts.Budgets {
	budgets, err := m.Resolve(timeouts.Uniform(r.getPollTimeout()))
	if err != nil {
		// Unreachable via HCL (the block validator rejects bad durations at
		// plan time); degrade to the defaults rather than fail a wait.
		return timeouts.Uniform(r.getPollTimeout())
	}
	return budgets
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
		Description: "Manages a backend member of a Frostmoln load balancer pool." + "\n\n" + scopedecl.Summary("frostmoln_lb_member"),
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
				Description: "Whether the member is in a different VPC than the load balancer. Acknowledgement flag: recorded in state but never returned by the API. Changing this between two known values forces a new resource. On import this flag cannot be recovered from the API, so it is left null and reconciled (not destroyed) on the first apply that supplies it.",
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
		Blocks: map[string]schema.Block{
			// Customer-tunable wait budgets: defaults keep the values this
			// resource has always hardcoded (5m per verb). A timeouts change
			// is an in-place no-op on real infrastructure — verified by the
			// Gate 2 smoke test (project-docs/product/TF-CONVERGENCE-WALL-PLAN.md).
			"timeouts": timeouts.Schema(),
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

	// the created-at floor for the discovery sweep
	applyStarted := time.Now().UTC()
	floor := applyStarted.Add(-time.Minute)
	apiResp, err := r.client.Post(ctx, r.client.TenantPath(fmt.Sprintf("/load-balancers/%s/pools/%s/members", lbID, poolID)), createReq)
	if err != nil {
		resp.Diagnostics.AddError("Failed to Create Member", err.Error())
		return
	}

	// Member create routes through provisioning → 202 + an Operation envelope
	// (operationId only). Poll the operation, then read by its resolved
	// resourceId. A non-202 body is parsed directly for a sync backend.
	budgets := r.resolveBudgets(plan.Timeouts)
	var member *apiMember
	if apiResp.IsAccepted() {
		op, opErr := client.ParseResponse[client.Operation](apiResp)
		if opErr != nil {
			resp.Diagnostics.AddError("Failed to Parse Operation Response", opErr.Error())
			return
		}
		done, waitErr := r.client.WaitForOperation(ctx, op.OperationID, r.getPollInterval(), budgets.Create)
		if waitErr != nil {
			if r.client.ClassifyOperationFailure(ctx, op.OperationID) == client.OperationRefused {
				orphan.AddCreateRefused(&resp.Diagnostics, "Member",
					fmt.Sprintf("the member %s:%d on pool %s", plan.Address.ValueString(), plan.ProtocolPort.ValueInt64(), poolID), waitErr)
				return
			}
			// UNKNOWN: the saga may still land. The sweep decides honestly.
			r.adoptCreatedObject(ctx, plan, lbID, poolID, floor, waitErr, resp)
			return
		}
		if done.ResourceID == "" {
			// The operation COMPLETED without its resourceId (degraded
			// provisioning). The member exists; the sweep resolves it by the
			// address:port tuple the plan states.
			r.adoptCreatedObject(ctx, plan, lbID, poolID, floor,
				fmt.Errorf("the member create operation completed but returned no resource ID"), resp)
			return
		}
		readResp, readErr := r.client.Get(ctx, r.client.TenantPath(fmt.Sprintf("/load-balancers/%s/pools/%s/members/%s", lbID, poolID, done.ResourceID)), nil)
		if readErr != nil {
			resp.Diagnostics.AddError("Failed to Read Member After Creation", readErr.Error())
			return
		}
		member, err = client.ParseResponse[apiMember](readResp)
		if err != nil {
			resp.Diagnostics.AddError("Failed to Parse Member Response", err.Error())
			return
		}
	} else {
		member, err = client.ParseResponse[apiMember](apiResp)
		if err != nil {
			resp.Diagnostics.AddError("Failed to Parse Member Response", err.Error())
			return
		}
	}

	plan.fromAPI(lbID, member)
	plan.CrossVPC = crossVPC // write-only, not returned by API
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// adoptCreatedObject resolves an apply whose id the provider lost — a timed-out
// operation or a completed one that came back without a resourceId — by the
// pool's member listing (members carry no unique name: the address:port tuple
// the plan states is the matcher, with the created-at floor as backstop), and
// adopts the result honestly: found means a fresh read of what the platform
// HAS (not what the configuration asked for); absent means verified absence;
// unreadable names the platform's last word and the list path. Never
// `terraform state rm` (internal/orphan holds the copy).
func (r *memberResource) adoptCreatedObject(ctx context.Context, plan MemberModel, lbID, poolID string, floor time.Time, waitErr error, resp *resource.CreateResponse) {
	address := plan.Address.ValueString()
	protocolPort := int(plan.ProtocolPort.ValueInt64())
	id := orphan.AdoptCreateOnTimeout(ctx, &resp.Diagnostics, orphan.CreateParams{
		ResourceName: "Member",
		FMList:       "`fm lb member list`",
		WaitErr:      waitErr,
		Resolve: func(ctx context.Context) (string, error) {
			apiResp, err := r.client.Get(ctx, r.client.TenantPath(fmt.Sprintf("/load-balancers/%s/pools/%s/members", lbID, poolID)), nil)
			if err != nil {
				return "", err
			}
			var list apiMemberList
			if err := json.Unmarshal(apiResp.Body, &list); err != nil {
				return "", err
			}
			candidates := make([]orphan.Candidate, 0, len(list.Items))
			for _, it := range list.Items {
				if it.Address == address && it.ProtocolPort == protocolPort {
					candidates = append(candidates, orphan.Candidate{ID: it.ID, CreatedAt: it.CreatedAt})
				}
			}
			return orphan.PickCreated(candidates, "", floor)
		},
	})
	if id == "" {
		return
	}
	// HONEST READ: the platform's response, not the configuration's intent —
	// the same member read the completed-operation path uses.
	readResp, readErr := r.client.Get(ctx, r.client.TenantPath(fmt.Sprintf("/load-balancers/%s/pools/%s/members/%s", lbID, poolID, id)), nil)
	if readErr != nil {
		resp.Diagnostics.AddError("Failed to Read Member After Adoption", readErr.Error())
		return
	}
	adopted, err := client.ParseResponse[apiMember](readResp)
	if err != nil {
		resp.Diagnostics.AddError("Failed to Parse Member Response", err.Error())
		return
	}
	plan.fromAPI(lbID, adopted)
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
	subject := fmt.Sprintf("%s/%s/%s", lbID, poolID, memberID)
	budgets := r.resolveBudgets(state.Timeouts)

	delResp, err := r.client.Delete(ctx, r.client.TenantPath(fmt.Sprintf("/load-balancers/%s/pools/%s/members/%s", lbID, poolID, memberID)))
	if err != nil {
		if client.IsNotFound(err) {
			return
		}
		resp.Diagnostics.AddError("Failed to Delete Member", err.Error())
		return
	}

	// A member delete routes through provisioning, which answers 202 with an
	// Operation envelope BEFORE the platform has decided anything — and the
	// destroy of an async backend used to be reported as done the moment that
	// 202 landed, so a delete whose workflow later FAILED still dropped the
	// state row while the member kept serving. Parse the envelope and wait
	// for the workflow's verdict; a non-202 is a synchronous backend and needs
	// no watch.
	if !delResp.IsAccepted() {
		return
	}
	op, opErr := client.ParseResponse[client.Operation](delResp)
	if opErr != nil || op.OperationID == "" {
		// The destroy was accepted and its workflow cannot be watched from
		// here. That is classified — NOT a success and NOT a verified absence.
		unwatched := opErr
		if unwatched == nil {
			unwatched = fmt.Errorf("the delete was accepted but returned no operation id")
		}
		orphan.AddDeleteOutcome(&resp.Diagnostics, client.OperationUnknown, "Member", subject, unwatched)
		return
	}
	if _, waitErr := r.client.WaitForOperation(ctx, op.OperationID, r.getPollInterval(), budgets.Delete); waitErr != nil {
		orphan.AddDeleteOutcome(&resp.Diagnostics,
			r.client.ClassifyOperationFailure(ctx, op.OperationID),
			"Member", subject, waitErr)
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
