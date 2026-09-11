package lb_pool

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/orphan"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/scopedecl"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/timeouts"
)

var (
	_ resource.Resource                = &poolResource{}
	_ resource.ResourceWithImportState = &poolResource{}
)

type poolResource struct {
	client *client.Client

	// pollInterval and pollTimeout bound the waits for the provisioning
	// operations a write starts. Fields rather than constants so a test can
	// drive the timeout in milliseconds; the timeouts block sits on top of
	// these defaults (see resolveBudgets).
	pollInterval time.Duration
	pollTimeout  time.Duration
}

// NewResource returns a new pool resource factory.
func NewResource() resource.Resource {
	return &poolResource{}
}

func (r *poolResource) getPollInterval() time.Duration {
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
func (r *poolResource) getPollTimeout() time.Duration {
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
func (r *poolResource) resolveBudgets(m *timeouts.Model) timeouts.Budgets {
	budgets, err := m.Resolve(timeouts.Uniform(r.getPollTimeout()))
	if err != nil {
		// Unreachable via HCL (the block validator rejects bad durations at
		// plan time); degrade to the defaults rather than fail a wait.
		return timeouts.Uniform(r.getPollTimeout())
	}
	return budgets
}

func (r *poolResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_lb_pool"
}

func (r *poolResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manages a backend pool on a Frostmoln load balancer." + "\n\n" + scopedecl.Summary("frostmoln_lb_pool"),
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
				Description: "The load balancing algorithm: round_robin, least_connections, source_ip, or source_ip_port (source_ip_port is required on an l4 load balancer, which accepts no other algorithm).",
				Required:    true,
				Validators: []validator.String{
					stringvalidator.OneOf("round_robin", "least_connections", "source_ip", "source_ip_port"),
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
			"tags": schema.MapAttribute{
				Description: "Key-value tags for the pool. These are the pool's own tags, separate from the load balancer's.",
				ElementType: types.StringType,
				Optional:    true,
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
	createReq := plan.toCreateRequest(ctx, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}

	// the created-at floor for the discovery sweep
	applyStarted := time.Now().UTC()
	floor := applyStarted.Add(-time.Minute)
	apiResp, err := r.client.Post(ctx, r.client.TenantPath(fmt.Sprintf("/load-balancers/%s/pools", lbID)), createReq)
	if err != nil {
		resp.Diagnostics.AddError("Failed to Create Pool", err.Error())
		return
	}

	// Pool create routes through provisioning → 202 + an Operation envelope
	// (operationId only). Poll the operation, then read by its resolved
	// resourceId. A non-202 body is parsed directly for a sync backend.
	budgets := r.resolveBudgets(plan.Timeouts)
	var pool *apiPool
	if apiResp.IsAccepted() {
		op, opErr := client.ParseResponse[client.Operation](apiResp)
		if opErr != nil {
			resp.Diagnostics.AddError("Failed to Parse Operation Response", opErr.Error())
			return
		}
		done, waitErr := r.client.WaitForOperation(ctx, op.OperationID, r.getPollInterval(), budgets.Create)
		if waitErr != nil {
			if r.client.ClassifyOperationFailure(ctx, op.OperationID) == client.OperationRefused {
				orphan.AddCreateRefused(&resp.Diagnostics, "Pool",
					fmt.Sprintf("the pool %q on load balancer %s", plan.Name.ValueString(), lbID), waitErr)
				return
			}
			// UNKNOWN: the saga may still land. The sweep decides honestly.
			r.adoptCreatedObject(ctx, plan, lbID, floor, waitErr, resp)
			return
		}
		if done.ResourceID == "" {
			// The operation COMPLETED without its resourceId (degraded
			// provisioning). The pool exists; the sweep resolves it by name.
			r.adoptCreatedObject(ctx, plan, lbID, floor,
				fmt.Errorf("the pool create operation completed but returned no resource ID"), resp)
			return
		}
		readResp, readErr := r.client.Get(ctx, r.client.TenantPath(fmt.Sprintf("/load-balancers/%s/pools/%s", lbID, done.ResourceID)), nil)
		if readErr != nil {
			resp.Diagnostics.AddError("Failed to Read Pool After Creation", readErr.Error())
			return
		}
		pool, err = client.ParseResponse[apiPool](readResp)
		if err != nil {
			resp.Diagnostics.AddError("Failed to Parse Pool Response", err.Error())
			return
		}
	} else {
		pool, err = client.ParseResponse[apiPool](apiResp)
		if err != nil {
			resp.Diagnostics.AddError("Failed to Parse Pool Response", err.Error())
			return
		}
	}

	plan.fromAPI(ctx, pool, &resp.Diagnostics)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// adoptCreatedObject resolves an apply whose id the provider lost — a timed-out
// operation or a completed one that came back without a resourceId — by the
// parent load balancer's pool listing, and adopts the result honestly: found
// means a fresh read of what the platform HAS (not what the configuration
// asked for); absent means verified absence; unreadable names the platform's
// last word and the list path. Never `terraform state rm` (internal/orphan
// holds the copy).
func (r *poolResource) adoptCreatedObject(ctx context.Context, plan PoolModel, lbID string, floor time.Time, waitErr error, resp *resource.CreateResponse) {
	id := orphan.AdoptCreateOnTimeout(ctx, &resp.Diagnostics, orphan.CreateParams{
		ResourceName: "Pool",
		FMList:       "`fm lb pool list`",
		WaitErr:      waitErr,
		Resolve: func(ctx context.Context) (string, error) {
			apiResp, err := r.client.Get(ctx, r.client.TenantPath(fmt.Sprintf("/load-balancers/%s/pools", lbID)), nil)
			if err != nil {
				return "", err
			}
			var list apiPoolList
			if err := json.Unmarshal(apiResp.Body, &list); err != nil {
				return "", err
			}
			candidates := make([]orphan.Candidate, 0, len(list.Items))
			for _, it := range list.Items {
				candidates = append(candidates, orphan.Candidate{ID: it.ID, Name: it.Name, CreatedAt: it.CreatedAt})
			}
			return orphan.PickCreated(candidates, plan.Name.ValueString(), floor)
		},
	})
	if id == "" {
		return
	}
	// HONEST READ: the platform's response, not the configuration's intent.
	readResp, readErr := r.client.Get(ctx, r.client.TenantPath(fmt.Sprintf("/load-balancers/%s/pools/%s", lbID, id)), nil)
	if readErr != nil {
		resp.Diagnostics.AddError("Failed to Read Pool After Adoption", readErr.Error())
		return
	}
	adopted, err := client.ParseResponse[apiPool](readResp)
	if err != nil {
		resp.Diagnostics.AddError("Failed to Parse Pool Response", err.Error())
		return
	}
	plan.fromAPI(ctx, adopted, &resp.Diagnostics)
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
	poolPath := r.client.TenantPath(fmt.Sprintf("/load-balancers/%s/pools/%s", lbID, poolID))
	updateReq := plan.toUpdateRequest(ctx, state.Tags, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}

	apiResp, err := r.client.Put(ctx, poolPath, updateReq)
	if err != nil {
		resp.Diagnostics.AddError("Failed to Update Pool", err.Error())
		return
	}

	// Like create, a pool UPDATE routes through provisioning → 202 + an
	// Operation envelope, never the updated pool. Parsing that envelope as a
	// pool yields a zero-valued object, which would blank every attribute in
	// state; poll the operation and re-read instead. A non-202 body is parsed
	// directly for a sync backend.
	updateBudgets := r.resolveBudgets(plan.Timeouts)
	var pool *apiPool
	if apiResp.IsAccepted() {
		op, opErr := client.ParseResponse[client.Operation](apiResp)
		if opErr != nil {
			resp.Diagnostics.AddError("Failed to Parse Operation Response", opErr.Error())
			return
		}
		if _, waitErr := r.client.WaitForOperation(ctx, op.OperationID, r.getPollInterval(), updateBudgets.Update); waitErr != nil {
			resp.Diagnostics.AddError("Pool Update Failed", waitErr.Error())
			return
		}
		readResp, readErr := r.client.Get(ctx, poolPath, nil)
		if readErr != nil {
			resp.Diagnostics.AddError("Failed to Read Pool After Update", readErr.Error())
			return
		}
		pool, err = client.ParseResponse[apiPool](readResp)
		if err != nil {
			resp.Diagnostics.AddError("Failed to Parse Pool Response", err.Error())
			return
		}
	} else {
		pool, err = client.ParseResponse[apiPool](apiResp)
		if err != nil {
			resp.Diagnostics.AddError("Failed to Parse Pool Response", err.Error())
			return
		}
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
	subject := fmt.Sprintf("%s/%s", lbID, poolID)
	budgets := r.resolveBudgets(state.Timeouts)

	delResp, err := r.client.Delete(ctx, r.client.TenantPath(fmt.Sprintf("/load-balancers/%s/pools/%s", lbID, poolID)))
	if err != nil {
		if client.IsNotFound(err) {
			return
		}
		resp.Diagnostics.AddError("Failed to Delete Pool", err.Error())
		return
	}

	// A pool delete routes through provisioning, which answers 202 with an
	// Operation envelope BEFORE the platform has decided anything — and the
	// destroy of an async backend used to be reported as done the moment that
	// 202 landed, so a delete whose workflow later FAILED still dropped the
	// state row while the pool kept serving traffic. Parse the envelope and
	// wait for the workflow's verdict; a non-202 is a synchronous backend and
	// needs no watch.
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
		orphan.AddDeleteOutcome(&resp.Diagnostics, client.OperationUnknown, "Pool", subject, unwatched)
		return
	}
	if _, waitErr := r.client.WaitForOperation(ctx, op.OperationID, r.getPollInterval(), budgets.Delete); waitErr != nil {
		orphan.AddDeleteOutcome(&resp.Diagnostics,
			r.client.ClassifyOperationFailure(ctx, op.OperationID),
			"Pool", subject, waitErr)
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
