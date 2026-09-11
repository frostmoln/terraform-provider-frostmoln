package lb_listener

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

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
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/orphan"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/scopedecl"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/timeouts"
)

var (
	_ resource.Resource                = &listenerResource{}
	_ resource.ResourceWithImportState = &listenerResource{}
)

type listenerResource struct {
	client *client.Client

	// pollInterval and pollTimeout bound the waits for the provisioning
	// operations a write starts. Fields rather than constants so a test can
	// drive the timeout in milliseconds; the timeouts block sits on top of
	// these defaults (see resolveBudgets).
	pollInterval time.Duration
	pollTimeout  time.Duration
}

// NewResource returns a new listener resource factory.
func NewResource() resource.Resource {
	return &listenerResource{}
}

func (r *listenerResource) getPollInterval() time.Duration {
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
func (r *listenerResource) getPollTimeout() time.Duration {
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
func (r *listenerResource) resolveBudgets(m *timeouts.Model) timeouts.Budgets {
	budgets, err := m.Resolve(timeouts.Uniform(r.getPollTimeout()))
	if err != nil {
		// Unreachable via HCL (the block validator rejects bad durations at
		// plan time); degrade to the defaults rather than fail a wait.
		return timeouts.Uniform(r.getPollTimeout())
	}
	return budgets
}

func (r *listenerResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_lb_listener"
}

func (r *listenerResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manages a listener on a Frostmoln load balancer." + "\n\n" + scopedecl.Summary("frostmoln_lb_listener"),
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
		Blocks: map[string]schema.Block{
			// Customer-tunable wait budgets: defaults keep the values this
			// resource has always hardcoded (5m per verb). A timeouts change
			// is an in-place no-op on real infrastructure — verified by the
			// Gate 2 smoke test (project-docs/product/TF-CONVERGENCE-WALL-PLAN.md).
			"timeouts": timeouts.Schema(),
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

	// the created-at floor for the discovery sweep
	applyStarted := time.Now().UTC()
	floor := applyStarted.Add(-time.Minute)
	apiResp, err := r.client.Post(ctx, r.client.TenantPath(fmt.Sprintf("/load-balancers/%s/listeners", lbID)), createReq)
	if err != nil {
		resp.Diagnostics.AddError("Failed to Create Listener", err.Error())
		return
	}

	// Listener create routes through provisioning → 202 + an Operation envelope
	// (operationId only). Poll the operation, then read by its resolved
	// resourceId. A non-202 body is parsed directly for a sync backend.
	budgets := r.resolveBudgets(plan.Timeouts)
	var listener *apiListener
	if apiResp.IsAccepted() {
		op, opErr := client.ParseResponse[client.Operation](apiResp)
		if opErr != nil {
			resp.Diagnostics.AddError("Failed to Parse Operation Response", opErr.Error())
			return
		}
		done, waitErr := r.client.WaitForOperation(ctx, op.OperationID, r.getPollInterval(), budgets.Create)
		if waitErr != nil {
			if r.client.ClassifyOperationFailure(ctx, op.OperationID) == client.OperationRefused {
				orphan.AddCreateRefused(&resp.Diagnostics, "Listener",
					fmt.Sprintf("the listener %q on load balancer %s", plan.Name.ValueString(), lbID), waitErr)
				return
			}
			// UNKNOWN: the saga may still land. The sweep decides honestly.
			r.adoptCreatedObject(ctx, plan, lbID, floor, waitErr, resp)
			return
		}
		if done.ResourceID == "" {
			// The operation COMPLETED without its resourceId (degraded
			// provisioning). The listener exists; the sweep resolves it by name.
			r.adoptCreatedObject(ctx, plan, lbID, floor,
				fmt.Errorf("the listener create operation completed but returned no resource ID"), resp)
			return
		}
		readResp, readErr := r.client.Get(ctx, r.client.TenantPath(fmt.Sprintf("/load-balancers/%s/listeners/%s", lbID, done.ResourceID)), nil)
		if readErr != nil {
			resp.Diagnostics.AddError("Failed to Read Listener After Creation", readErr.Error())
			return
		}
		listener, err = client.ParseResponse[apiListener](readResp)
		if err != nil {
			resp.Diagnostics.AddError("Failed to Parse Listener Response", err.Error())
			return
		}
	} else {
		listener, err = client.ParseResponse[apiListener](apiResp)
		if err != nil {
			resp.Diagnostics.AddError("Failed to Parse Listener Response", err.Error())
			return
		}
	}

	plan.fromAPI(ctx, listener, &resp.Diagnostics)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// adoptCreatedObject resolves an apply whose id the provider lost — a timed-out
// operation or a completed one that came back without a resourceId — by the
// parent load balancer's listener listing, and adopts the result honestly:
// found means a fresh read of what the platform HAS (not what the
// configuration asked for); absent means verified absence; unreadable names
// the platform's last word and the list path. Never `terraform state rm`
// (internal/orphan holds the copy).
func (r *listenerResource) adoptCreatedObject(ctx context.Context, plan ListenerModel, lbID string, floor time.Time, waitErr error, resp *resource.CreateResponse) {
	id := orphan.AdoptCreateOnTimeout(ctx, &resp.Diagnostics, orphan.CreateParams{
		ResourceName: "Listener",
		FMList:       "`fm lb listener list`",
		WaitErr:      waitErr,
		Resolve: func(ctx context.Context) (string, error) {
			apiResp, err := r.client.Get(ctx, r.client.TenantPath(fmt.Sprintf("/load-balancers/%s/listeners", lbID)), nil)
			if err != nil {
				return "", err
			}
			var list apiListenerList
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
	readResp, readErr := r.client.Get(ctx, r.client.TenantPath(fmt.Sprintf("/load-balancers/%s/listeners/%s", lbID, id)), nil)
	if readErr != nil {
		resp.Diagnostics.AddError("Failed to Read Listener After Adoption", readErr.Error())
		return
	}
	adopted, err := client.ParseResponse[apiListener](readResp)
	if err != nil {
		resp.Diagnostics.AddError("Failed to Parse Listener Response", err.Error())
		return
	}
	plan.fromAPI(ctx, adopted, &resp.Diagnostics)
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
	subject := fmt.Sprintf("%s/%s", lbID, listenerID)
	budgets := r.resolveBudgets(state.Timeouts)

	delResp, err := r.client.Delete(ctx, r.client.TenantPath(fmt.Sprintf("/load-balancers/%s/listeners/%s", lbID, listenerID)))
	if err != nil {
		if client.IsNotFound(err) {
			return
		}
		resp.Diagnostics.AddError("Failed to Delete Listener", err.Error())
		return
	}

	// A listener delete routes through provisioning, which answers 202 with an
	// Operation envelope BEFORE the platform has decided anything — and the
	// destroy of an async backend used to be reported as done the moment that
	// 202 landed, so a delete whose workflow later FAILED still dropped the
	// state row while the listener kept serving. Parse the envelope and wait
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
		orphan.AddDeleteOutcome(&resp.Diagnostics, client.OperationUnknown, "Listener", subject, unwatched)
		return
	}
	if _, waitErr := r.client.WaitForOperation(ctx, op.OperationID, r.getPollInterval(), budgets.Delete); waitErr != nil {
		orphan.AddDeleteOutcome(&resp.Diagnostics,
			r.client.ClassifyOperationFailure(ctx, op.OperationID),
			"Listener", subject, waitErr)
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
