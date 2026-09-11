package lb_health_monitor

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64default"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/orphan"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/scopedecl"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/timeouts"
)

var (
	_ resource.Resource                = &healthMonitorResource{}
	_ resource.ResourceWithImportState = &healthMonitorResource{}
)

type healthMonitorResource struct {
	client *client.Client

	// pollInterval and pollTimeout bound the waits for the provisioning
	// operations a write starts. Fields rather than constants so a test can
	// drive the timeout in milliseconds; the timeouts block sits on top of
	// these defaults (see resolveBudgets).
	pollInterval time.Duration
	pollTimeout  time.Duration
}

// NewResource returns a new health monitor resource factory.
func NewResource() resource.Resource {
	return &healthMonitorResource{}
}

func (r *healthMonitorResource) getPollInterval() time.Duration {
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
func (r *healthMonitorResource) getPollTimeout() time.Duration {
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
func (r *healthMonitorResource) resolveBudgets(m *timeouts.Model) timeouts.Budgets {
	budgets, err := m.Resolve(timeouts.Uniform(r.getPollTimeout()))
	if err != nil {
		// Unreachable via HCL (the block validator rejects bad durations at
		// plan time); degrade to the defaults rather than fail a wait.
		return timeouts.Uniform(r.getPollTimeout())
	}
	return budgets
}

func (r *healthMonitorResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_lb_health_monitor"
}

func (r *healthMonitorResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manages the health monitor of a Frostmoln load balancer pool. A pool has at most one health monitor (singleton)." + "\n\n" + scopedecl.Summary("frostmoln_lb_health_monitor"),
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Description: "The unique identifier of the health monitor.",
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
				Description: "The ID of the pool this health monitor belongs to. Changing this forces a new resource.",
				Required:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"type": schema.StringAttribute{
				Description: "The health check type: tcp, http, or https. Changing this forces a new resource.",
				Required:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
				Validators: []validator.String{
					stringvalidator.OneOf("tcp", "http", "https"),
				},
			},
			"delay": schema.Int64Attribute{
				Description: "The interval in seconds between health checks.",
				Optional:    true,
				Computed:    true,
				Default:     int64default.StaticInt64(5),
			},
			"timeout": schema.Int64Attribute{
				Description: "The time in seconds to wait for a health check response.",
				Optional:    true,
				Computed:    true,
				Default:     int64default.StaticInt64(3),
			},
			"max_retries": schema.Int64Attribute{
				Description: "The number of successful checks before a member is marked healthy.",
				Optional:    true,
				Computed:    true,
				Default:     int64default.StaticInt64(3),
			},
			"url_path": schema.StringAttribute{
				Description: "The HTTP path to probe (http/https monitors).",
				Optional:    true,
			},
			"http_method": schema.StringAttribute{
				Description: "The HTTP method used for the health check (http/https monitors).",
				Optional:    true,
			},
			"expected_codes": schema.StringAttribute{
				Description: "The HTTP status codes considered healthy (http/https monitors), e.g. \"200\" or \"200-299\".",
				Optional:    true,
			},
			"tags": schema.MapAttribute{
				Description: "Key-value tags for the health monitor. These are the monitor's own tags, separate from its pool's and the load balancer's.",
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

func (r *healthMonitorResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

// monitorPath builds the singleton health monitor path for a pool.
func (r *healthMonitorResource) monitorPath(lbID, poolID string) string {
	return r.client.TenantPath(fmt.Sprintf("/load-balancers/%s/pools/%s/healthmonitor", lbID, poolID))
}

func (r *healthMonitorResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan HealthMonitorModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	lbID := plan.LoadBalancerID.ValueString()
	poolID := plan.PoolID.ValueString()
	createReq := plan.toCreateRequest(ctx, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}

	// the created-at floor for the discovery sweep
	applyStarted := time.Now().UTC()
	floor := applyStarted.Add(-time.Minute)
	apiResp, err := r.client.Post(ctx, r.monitorPath(lbID, poolID), createReq)
	if err != nil {
		resp.Diagnostics.AddError("Failed to Create Health Monitor", err.Error())
		return
	}

	// Health-monitor create routes through provisioning → 202 + an Operation
	// envelope (operationId only). Poll the operation, then re-read the monitor
	// (one per pool — no id in the path). A non-202 body is parsed directly for a
	// sync backend.
	budgets := r.resolveBudgets(plan.Timeouts)
	var hm *apiHealthMonitor
	if apiResp.IsAccepted() {
		op, opErr := client.ParseResponse[client.Operation](apiResp)
		if opErr != nil {
			resp.Diagnostics.AddError("Failed to Parse Operation Response", opErr.Error())
			return
		}
		if _, waitErr := r.client.WaitForOperation(ctx, op.OperationID, r.getPollInterval(), budgets.Create); waitErr != nil {
			if r.client.ClassifyOperationFailure(ctx, op.OperationID) == client.OperationRefused {
				orphan.AddCreateRefused(&resp.Diagnostics, "Health Monitor",
					fmt.Sprintf("the health monitor on pool %s", poolID), waitErr)
				return
			}
			// UNKNOWN: the saga may still land. The sweep decides honestly.
			r.adoptCreatedObject(ctx, plan, lbID, poolID, floor, waitErr, resp)
			return
		}
		readResp, readErr := r.client.Get(ctx, r.monitorPath(lbID, poolID), nil)
		if readErr != nil {
			resp.Diagnostics.AddError("Failed to Read Health Monitor After Creation", readErr.Error())
			return
		}
		hm, err = client.ParseResponse[apiHealthMonitor](readResp)
		if err != nil {
			resp.Diagnostics.AddError("Failed to Parse Health Monitor Response", err.Error())
			return
		}
	} else {
		hm, err = client.ParseResponse[apiHealthMonitor](apiResp)
		if err != nil {
			resp.Diagnostics.AddError("Failed to Parse Health Monitor Response", err.Error())
			return
		}
	}

	plan.fromAPI(ctx, lbID, hm, &resp.Diagnostics)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// adoptCreatedObject resolves an apply whose watch the provider lost — the
// monitor has no family listing, so the sweep is the pool's singleton GET:
// 200 is found (adopt the row's id), 404 is verified absence, anything else
// names the platform's last word and the list path — and adopts the result
// honestly: a fresh read of what the platform HAS, never the configuration's
// intent. Never `terraform state rm` (internal/orphan holds the copy).
func (r *healthMonitorResource) adoptCreatedObject(ctx context.Context, plan HealthMonitorModel, lbID, poolID string, floor time.Time, waitErr error, resp *resource.CreateResponse) {
	id := orphan.AdoptCreateOnTimeout(ctx, &resp.Diagnostics, orphan.CreateParams{
		ResourceName: "Health Monitor",
		FMList:       "`fm lb pool list`",
		WaitErr:      waitErr,
		Resolve: func(ctx context.Context) (string, error) {
			apiResp, err := r.client.Get(ctx, r.monitorPath(lbID, poolID), nil)
			if err != nil {
				if client.IsNotFound(err) {
					// No monitor on the pool: the sweep just verified the
					// absence — the create never finished.
					return "", orphan.ErrAbsent
				}
				return "", err
			}
			hm, err := client.ParseResponse[apiHealthMonitor](apiResp)
			if err != nil {
				return "", err
			}
			// The singleton row is the only candidate, but a row that PREDATES
			// this apply is a pre-existing monitor — a colleague's or a prior
			// apply's — and adopting it would bind someone else's probe
			// semantics into state. Stamp parseable and before the floor →
			// refuse to guess (the pool holds a monitor this apply did not
			// make); unparseable stamps keep the candidate, per the shared
			// matcher's keep rule.
			if !floor.IsZero() {
				if created, perr := time.Parse(time.RFC3339, hm.CreatedAt); perr == nil && created.Before(floor) {
					return "", fmt.Errorf("%w: the pool's health monitor (%s, created %s) predates this apply",
						orphan.ErrAmbiguous, hm.ID, hm.CreatedAt)
				}
			}
			return hm.ID, nil
		},
	})
	if id == "" {
		return
	}
	// HONEST READ: the platform's response, not the configuration's intent.
	readResp, readErr := r.client.Get(ctx, r.monitorPath(lbID, poolID), nil)
	if readErr != nil {
		resp.Diagnostics.AddError("Failed to Read Health Monitor After Adoption", readErr.Error())
		return
	}
	adopted, err := client.ParseResponse[apiHealthMonitor](readResp)
	if err != nil {
		resp.Diagnostics.AddError("Failed to Parse Health Monitor Response", err.Error())
		return
	}
	plan.fromAPI(ctx, lbID, adopted, &resp.Diagnostics)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *healthMonitorResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state HealthMonitorModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	lbID := state.LoadBalancerID.ValueString()
	poolID := state.PoolID.ValueString()

	apiResp, err := r.client.Get(ctx, r.monitorPath(lbID, poolID), nil)
	if err != nil {
		if client.IsNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Failed to Read Health Monitor", err.Error())
		return
	}

	hm, err := client.ParseResponse[apiHealthMonitor](apiResp)
	if err != nil {
		resp.Diagnostics.AddError("Failed to Parse Health Monitor Response", err.Error())
		return
	}

	state.fromAPI(ctx, lbID, hm, &resp.Diagnostics)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *healthMonitorResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan HealthMonitorModel
	var state HealthMonitorModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	lbID := state.LoadBalancerID.ValueString()
	poolID := state.PoolID.ValueString()
	updateReq := plan.toUpdateRequest(ctx, state.Tags, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}

	apiResp, err := r.client.Put(ctx, r.monitorPath(lbID, poolID), updateReq)
	if err != nil {
		resp.Diagnostics.AddError("Failed to Update Health Monitor", err.Error())
		return
	}

	// Like create, a health-monitor UPDATE routes through provisioning → 202 +
	// an Operation envelope, never the updated monitor. Parsing that envelope as
	// a monitor yields a zero-valued object, which would blank every attribute
	// in state; poll the operation and re-read instead.
	budgets := r.resolveBudgets(plan.Timeouts)
	var hm *apiHealthMonitor
	if apiResp.IsAccepted() {
		op, opErr := client.ParseResponse[client.Operation](apiResp)
		if opErr != nil {
			resp.Diagnostics.AddError("Failed to Parse Operation Response", opErr.Error())
			return
		}
		if _, waitErr := r.client.WaitForOperation(ctx, op.OperationID, r.getPollInterval(), budgets.Update); waitErr != nil {
			resp.Diagnostics.AddError("Health Monitor Update Failed", waitErr.Error())
			return
		}
		readResp, readErr := r.client.Get(ctx, r.monitorPath(lbID, poolID), nil)
		if readErr != nil {
			resp.Diagnostics.AddError("Failed to Read Health Monitor After Update", readErr.Error())
			return
		}
		hm, err = client.ParseResponse[apiHealthMonitor](readResp)
		if err != nil {
			resp.Diagnostics.AddError("Failed to Parse Health Monitor Response", err.Error())
			return
		}
	} else {
		hm, err = client.ParseResponse[apiHealthMonitor](apiResp)
		if err != nil {
			resp.Diagnostics.AddError("Failed to Parse Health Monitor Response", err.Error())
			return
		}
	}

	plan.fromAPI(ctx, lbID, hm, &resp.Diagnostics)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *healthMonitorResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state HealthMonitorModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	lbID := state.LoadBalancerID.ValueString()
	poolID := state.PoolID.ValueString()
	subject := fmt.Sprintf("%s/%s", lbID, poolID)
	budgets := r.resolveBudgets(state.Timeouts)

	delResp, err := r.client.Delete(ctx, r.monitorPath(lbID, poolID))
	if err != nil {
		if client.IsNotFound(err) {
			return
		}
		resp.Diagnostics.AddError("Failed to Delete Health Monitor", err.Error())
		return
	}

	// A health-monitor delete routes through provisioning, which answers 202
	// with an Operation envelope BEFORE the platform has decided anything —
	// and the destroy of an async backend used to be reported as done the
	// moment that 202 landed, so a delete whose workflow later FAILED still
	// dropped the state row while the monitor kept running. Parse the envelope
	// and wait for the workflow's verdict; a non-202 is a synchronous backend
	// and needs no watch.
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
		orphan.AddDeleteOutcome(&resp.Diagnostics, client.OperationUnknown, "Health Monitor", subject, unwatched)
		return
	}
	if _, waitErr := r.client.WaitForOperation(ctx, op.OperationID, r.getPollInterval(), budgets.Delete); waitErr != nil {
		orphan.AddDeleteOutcome(&resp.Diagnostics,
			r.client.ClassifyOperationFailure(ctx, op.OperationID),
			"Health Monitor", subject, waitErr)
	}
}

func (r *healthMonitorResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
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
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("pool_id"), parts[1])...)
}
