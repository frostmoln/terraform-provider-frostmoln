// Package appgw_config_apply implements the frostmoln_appgw_config_apply
// Terraform resource.
//
// 🔴 WITHOUT THIS RESOURCE, `terraform apply` DOES NOT MAKE ANYTHING HAPPEN.
//
// An Application Gateway is authored and then committed. Writing a listener, a
// route, a backend or a certificate records it and bumps the gateway's
// `config_generation`; it does NOT reach the appliance. The appliance keeps
// serving the configuration it last acknowledged (`config_revision`) until
// something dispatches an apply. That is deliberate — the portal has an Apply
// action and the CLI has `fm appgw apply` — but it left Terraform recording
// intent while reporting success, which is not what `terraform apply` means.
//
// Why this is a resource rather than something each child does after its own
// write:
//
//   - Intermediate states are WRONG states. Terraform orders by dependency, so a
//     listener is created before its routes. An apply dispatched there renders a
//     listener with no routes, and on an existing gateway that briefly serves
//     nothing for those hosts. A config edit would become a traffic event.
//   - The server refuses it anyway. Every authoring write bumps the generation
//     in its own transaction, and an apply whose generation moved during render
//     is rejected with "apply again to send the newer one". Per-child applies
//     would race each other.
//   - N children would mean N render/ship/validate/reload/self-probe cycles.
//
// So it takes the same shape as frostmoln_appgw_waf_policy_publication, which
// solves the identical ordering problem for the WAF: feed in a value from each
// child that moves when the child moves, and Terraform's own graph orders this
// last and re-runs it exactly when one of them changed. Nothing to remember
// beyond declaring it once, and a child that is not listed is visibly not
// listed.
//
// The publication can use a numeric `revision` because WAF rules carry one.
// Listeners, routes, pools, backends and certificates do NOT — they carry
// `updated_at`, a server-set timestamp — so this takes a map of STRINGS rather
// than pretending a revision exists. The timestamp has sub-second precision, so
// two sequential writes cannot collide on it.
package appgw_config_apply

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/hashicorp/terraform-plugin-framework-validators/mapvalidator"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
)

var (
	_ resource.Resource               = &applyResource{}
	_ resource.ResourceWithConfigure  = &applyResource{}
	_ resource.ResourceWithModifyPlan = &applyResource{}
)

// Apply waits. VARIABLES, not constants, so a test can shrink them: with the
// real values a test that exercises the never-converges path would run for
// hours, which means in practice it would not be written — and that is the path
// an operator actually hits when the appliance refuses a configuration.
//
// 🔴 THE CEILING MATCHES THE PLATFORM'S, and must keep matching it. provisioning
// polls the appliance for 2h (appgwApplyPollDeadline, itself pinned to the agent
// job's queue-side TTL) inside a 2h15m execution timeout, and appgw's reaper
// only abandons an attempt after 3h. A shorter ceiling here does not merely give
// up early: this resource writes no useful state on a wait failure, so the next
// run re-POSTs, the still-live attempt answers 409 CONFIG_APPLY_IN_FLIGHT, and
// the workspace is wedged until the reaper fires. apache_instance made the same
// call for the same mechanism and says so at configApplyTimeout.
var (
	applyPollInterval = 5 * time.Second
	applyTimeout      = 2 * time.Hour
)

// isTransientApplyConflict reports whether a dispatch 409 is one the server
// itself documents as self-clearing. Codes, never the bare status: appgw emits
// permanent 409s on this path too, and retrying those would spin to the ceiling.
//
//   - CONFIG_APPLY_IN_FLIGHT      "a race with their own earlier one, and it clears on its own"
//   - CONFIG_CHANGED_DURING_RENDER "RETRYABLE ... the next apply sends the newer one"
//   - GATEWAY_NOT_RUNNING          "the gateway will be running shortly"
//
// CONFIG_CHANGED_DURING_RENDER is very reachable: Terraform runs up to ten
// resources at once, so any child NOT listed in triggers landing mid-render
// refuses the apply.
func isTransientApplyConflict(err error) bool {
	var apiErr *client.APIError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != 409 {
		return false
	}
	switch apiErr.Code {
	case "CONFIG_APPLY_IN_FLIGHT", "CONFIG_CHANGED_DURING_RENDER", "GATEWAY_NOT_RUNNING":
		return true
	}
	return false
}

// Model is the Terraform state model for a configuration apply.
type Model struct {
	ID        types.String `tfsdk:"id"`
	GatewayID types.String `tfsdk:"gateway_id"`

	// Triggers is the ordering input. See the package comment.
	Triggers types.Map `tfsdk:"triggers"`

	Revision  types.Int64  `tfsdk:"revision"`
	SHA256    types.String `tfsdk:"sha256"`
	AppliedAt types.String `tfsdk:"applied_at"`
	Status    types.String `tfsdk:"status"`
}

type apiApplyResult struct {
	OperationID string `json:"operationId"`
	Revision    int64  `json:"revision"`
	SHA256      string `json:"sha256"`
}

type apiGateway struct {
	ID               string  `json:"id"`
	ConfigGeneration int64   `json:"configGeneration"`
	ConfigRevision   *int64  `json:"configRevision,omitempty"`
	ConfigStatus     string  `json:"configStatus"`
	ConfigDetail     string  `json:"configDetail,omitempty"`
	ConfigAppliedAt  *string `json:"configAppliedAt,omitempty"`
}

type applyResource struct{ client *client.Client }

// NewResource returns the frostmoln_appgw_config_apply resource.
func NewResource() resource.Resource { return &applyResource{} }

func (r *applyResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_appgw_config_apply"
}

func (r *applyResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (r *applyResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Dispatches an Application Gateway's authored configuration to the appliance, " +
			"and waits for the appliance's verdict.\n\n" +
			"**Authoring is not applying.** A listener, route, backend, certificate or published " +
			"WAF version is recorded when you apply it, but the gateway keeps serving what it last " +
			"acknowledged until a configuration apply dispatches the change. Without this resource " +
			"in your configuration, `terraform apply` records your intent and the gateway does not " +
			"serve it.\n\n" +
			"Feed each child's `updated_at` into `triggers` — see the example. Because the " +
			"server sets those timestamps on every write, any change to a listener, route, pool, " +
			"backend or certificate changes an input here, so Terraform's own graph orders this " +
			"last and re-runs it exactly when something changed. This resource also re-plans on " +
			"its own whenever the gateway has authored changes it is not serving, which covers a " +
			"deleted child and an edit made outside Terraform.\n\n" +
			"~> This resource FAILS the apply if the appliance refuses the configuration, quoting " +
			"the proxy's own words. That is deliberate: a `terraform apply` that succeeds while the " +
			"gateway is serving something else is worse than one that fails.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Description:   "The gateway id and the applied revision, joined.",
				Computed:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"gateway_id": schema.StringAttribute{
				Description:   "The Application Gateway whose configuration to dispatch.",
				Required:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"triggers": schema.MapAttribute{
				Description: "A value from every child object that changes when the child changes, " +
					"keyed however you like. This is an ordering input, not something the server " +
					"reads: it is what makes Terraform run this after the children and re-run it " +
					"exactly when one of them moved. A child left out of this map is a child whose " +
					"change will not be dispatched.\n\n" +
					"Use each child's `updated_at`. Listeners, routes, backend pools, backends and " +
					"certificates carry a server-set timestamp rather than the numeric `revision` " +
					"that WAF rules have, which is why this is a map of strings — feed a WAF " +
					"publication in with `tostring(...)`.\n\n" +
					"~> `frostmoln_appgw_health_check` exposes NO such attribute, because the API " +
					"does not return one for it. A health-check change therefore moves nothing " +
					"here. It is still dispatched, one run late: the write bumps the gateway's " +
					"generation, and this resource re-plans on its own whenever the gateway has " +
					"authored changes it is not serving.",
				ElementType: types.StringType,
				Required:    true,
				Validators: []validator.Map{
					// An empty map means "never re-run": the resource would apply
					// once and then plan nothing for ever.
					mapvalidator.SizeAtLeast(1),
				},
			},
			"revision": schema.Int64Attribute{
				Description: "The configuration generation that was dispatched and acknowledged.",
				Computed:    true,
			},
			"sha256": schema.StringAttribute{
				Description: "Identifies the rendered bytes, so two applies that produced the same " +
					"configuration are visibly the same configuration.",
				Computed: true,
			},
			"status": schema.StringAttribute{
				Description: "The appliance's verdict on this apply. `applied` when it converged.",
				Computed:    true,
			},
			"applied_at": schema.StringAttribute{
				Description: "When the appliance acknowledged the configuration.",
				Computed:    true,
			},
		},
	}
}

// ModifyPlan is what makes this resource converge. Without it, it is a resource
// that fixes half the defect it exists for.
//
// 🔴 TWO FAILURES IT CATCHES THAT `triggers` CANNOT.
//
//   - A CHILD DELETION. Terraform orders a dependent's update BEFORE the
//     dependency's destroy, so removing a route updates this resource FIRST —
//     dispatching a generation that still contains the route — and only then
//     destroys it, bumping the generation again. Nothing dispatches that. And
//     the next plan is SILENT, because `triggers` in config now matches state:
//     the deleted route stays served for ever. Deleting a route is how a
//     customer takes something off the internet, so this is the defect this
//     resource exists to fix, running in reverse and permanently.
//   - AUTHORING OUTSIDE TERRAFORM, or a previous apply that ended `failed` or
//     `unknown` with unchanged children. `triggers` never moves, so Terraform
//     reports agreement while the gateway serves something else.
//
// Marking the outputs unknown on an ordinary update is separately REQUIRED, not
// cosmetic: Terraform's proposed-new plan reuses the prior state value for a
// computed attribute, so without this the plan carries the old revision, the
// apply returns a new one, and core rejects it with "Provider produced
// inconsistent result after apply" on the resource's primary path.
//
// Reading the gateway is a GET, so this stays a side-effect-free plan.
func (r *applyResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.Plan.Raw.IsNull() {
		return // destroy
	}
	if req.State.Raw.IsNull() {
		return // create: the plan already shows every output unknown
	}

	var plan Model
	if diags := req.Plan.Get(ctx, &plan); diags.HasError() {
		return
	}

	// An ordinary update: triggers moved, so the outputs must go unknown or core
	// rejects the apply.
	changed := true
	if !plan.Triggers.IsUnknown() {
		var state Model
		if diags := req.State.Get(ctx, &state); !diags.HasError() {
			changed = !plan.Triggers.Equal(state.Triggers)
		}
	}

	// Nothing moved in triggers — but the gateway may still be unconverged, from
	// a deleted child, an out-of-band edit, or a refused apply.
	if !changed {
		// The client is nil during the framework's early plan walks.
		if r.client == nil || plan.GatewayID.IsUnknown() || plan.GatewayID.IsNull() {
			return
		}
		gw, err := r.readGateway(ctx, plan.GatewayID.ValueString())
		if err != nil {
			return // a gateway we cannot read is not a plan-time decision
		}
		if gw.ConfigStatus == "applied" &&
			gw.ConfigRevision != nil && *gw.ConfigRevision == gw.ConfigGeneration {
			return // converged
		}
		ack := "nothing"
		if gw.ConfigRevision != nil {
			ack = fmt.Sprintf("revision %d", *gw.ConfigRevision)
		}
		resp.Diagnostics.AddWarning("Re-applying The Gateway Configuration",
			fmt.Sprintf("Generation %d has been authored but the appliance has acknowledged %s "+
				"(status %q). Nothing in `triggers` moved, so this is a child that was deleted, an "+
				"edit made outside Terraform, or an apply that did not succeed. Dispatching again.",
				gw.ConfigGeneration, ack, gw.ConfigStatus))
	}

	resp.Plan.SetAttribute(ctx, path.Root("id"), types.StringUnknown())
	resp.Plan.SetAttribute(ctx, path.Root("revision"), types.Int64Unknown())
	resp.Plan.SetAttribute(ctx, path.Root("sha256"), types.StringUnknown())
	resp.Plan.SetAttribute(ctx, path.Root("status"), types.StringUnknown())
	resp.Plan.SetAttribute(ctx, path.Root("applied_at"), types.StringUnknown())
}

func (r *applyResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan Model
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	r.dispatch(ctx, &plan, &resp.Diagnostics)
	// 🔴 STATE IS WRITTEN EVEN ON FAILURE. The apply WAS dispatched; losing that
	// record means the next run re-POSTs into a live attempt and collects 409
	// CONFIG_APPLY_IN_FLIGHT until the reaper fires hours later. Terraform keeps
	// the state and marks the resource tainted, which is the honest outcome.
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Update is Create: an apply is an act, not a mutable object. Any change to
// `revisions` means a child moved, which is exactly when the appliance needs to
// be told again.
func (r *applyResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan Model
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	r.dispatch(ctx, &plan, &resp.Diagnostics)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Read refreshes the appliance's current verdict.
//
// 🔴 IT DOES NOT REMOVE THE RESOURCE WHEN THE GATEWAY HAS MOVED ON. A newer
// generation authored outside Terraform is not this resource ceasing to exist —
// it is the gateway having unapplied changes, which the gateway resource warns
// about. Removing state here would make Terraform re-dispatch on a plan that
// showed no change.
func (r *applyResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state Model
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	gw, err := r.readGateway(ctx, state.GatewayID.ValueString())
	if err != nil {
		// ONLY a 404 means the gateway is gone. A 500, a restart, an expired
		// token or a TLS blip during refresh must not drop this from state:
		// doing so re-dispatches on the next run, and if the previous attempt is
		// still in flight that collects a 409 until the reaper fires.
		if client.IsNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Failed to Read The Application Gateway", err.Error())
		return
	}
	state.Status = types.StringValue(gw.ConfigStatus)
	if gw.ConfigRevision != nil {
		state.Revision = types.Int64Value(*gw.ConfigRevision)
	}
	if gw.ConfigAppliedAt != nil {
		state.AppliedAt = types.StringValue(*gw.ConfigAppliedAt)
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Delete forgets the apply. It cannot un-apply: the appliance is serving the
// configuration, and removing this resource from the configuration does not
// mean "go back to serving the previous one" — it means "Terraform no longer
// dispatches for this gateway". Destroying the children is what removes the
// configuration, and that needs its own apply.
func (r *applyResource) Delete(_ context.Context, _ resource.DeleteRequest, resp *resource.DeleteResponse) {
	// Silently succeeding would imply the configuration stopped being served.
	// The sibling publication warns for the identical situation.
	resp.Diagnostics.AddWarning("The Gateway Keeps Serving This Configuration",
		"Removing frostmoln_appgw_config_apply does not un-apply anything — the appliance goes on "+
			"serving the configuration it last acknowledged. It means Terraform will no longer "+
			"dispatch for this gateway, so later changes to its listeners, routes, backends or "+
			"certificates will be recorded and never take effect. Destroy the children (or the "+
			"gateway) to remove the configuration.")
}

func (r *applyResource) gatewayPath(id string) string {
	return r.client.TenantPath(fmt.Sprintf("/application-gateways/%s", id))
}

func (r *applyResource) readGateway(ctx context.Context, id string) (*apiGateway, error) {
	apiResp, err := r.client.Get(ctx, r.gatewayPath(id), nil)
	if err != nil {
		return nil, err
	}
	return client.ParseResponse[apiGateway](apiResp)
}

// dispatch posts the apply and waits for the appliance to answer.
func (r *applyResource) dispatch(ctx context.Context, m *Model, diags *diag.Diagnostics) {
	gwID := m.GatewayID.ValueString()

	apiResp, err := r.client.PostWithConflictRetry(ctx, r.gatewayPath(gwID)+"/config/apply", nil,
		isTransientApplyConflict, applyPollInterval, applyTimeout)
	if err != nil {
		diags.AddError("Failed to Dispatch The Gateway Configuration", err.Error())
		return
	}
	res, err := client.ParseResponse[apiApplyResult](apiResp)
	if err != nil {
		diags.AddError("Failed to Read The Apply Result", err.Error())
		return
	}

	m.Revision = types.Int64Value(res.Revision)
	m.SHA256 = types.StringValue(res.SHA256)
	m.ID = types.StringValue(fmt.Sprintf("%s:%d", gwID, res.Revision))
	m.Status = types.StringValue("applying")
	m.AppliedAt = types.StringNull()

	// 🔴 THE WAIT IS THE POINT. A 202 means the bundle was dispatched, not that
	// the gateway is serving it: the appliance validates the rendered config,
	// swaps it atomically, reloads and probes itself before answering. Returning
	// here would be the same lie this resource exists to fix, one step further
	// along.
	gw, superseded, err := r.waitForVerdict(ctx, gwID, res.Revision)
	if err != nil {
		// State is written by the caller even on this path: the apply WAS
		// dispatched, and losing that means the next run re-POSTs into a live
		// attempt and collects a 409 until the reaper fires.
		diags.AddError("The Gateway Did Not Apply This Configuration", err.Error())
		return
	}

	m.Status = types.StringValue(gw.ConfigStatus)
	if gw.ConfigRevision != nil {
		// Record what the appliance ACKNOWLEDGED, not what we asked for.
		m.Revision = types.Int64Value(*gw.ConfigRevision)
		m.ID = types.StringValue(fmt.Sprintf("%s:%d", gwID, *gw.ConfigRevision))
	}
	if gw.ConfigAppliedAt != nil {
		m.AppliedAt = types.StringValue(*gw.ConfigAppliedAt)
	}
	if superseded {
		diags.AddWarning("Another Client Applied A Newer Configuration",
			fmt.Sprintf("Revision %d was dispatched, but the appliance acknowledged revision %d — "+
				"a portal edit, `fm appgw apply`, or another workspace applied while this one was "+
				"in flight. The gateway renders its CURRENT authored state, so that newer revision "+
				"carries this configuration forward; it also carries whatever else was authored. "+
				"Re-run a plan if you need to see agreement.",
				res.Revision, *gw.ConfigRevision))
	}
}

// waitForVerdict polls the gateway until the appliance has answered for this
// revision. The bool reports that a NEWER revision was acknowledged.
//
// Uses the shared poller rather than a loop of its own: WaitForState tolerates a
// transient GET failure to the deadline and names the last poll error in its
// timeout, which a hand-rolled loop turning one bad read into a fatal error does
// not.
func (r *applyResource) waitForVerdict(ctx context.Context, gwID string, revision int64) (*apiGateway, bool, error) {
	var (
		last        apiGateway
		superseded  bool
		terminalErr error
	)

	_, err := client.WaitForState(ctx, client.PollConfig{
		Interval:     applyPollInterval,
		Timeout:      applyTimeout,
		TargetStates: []string{"applied"},
		ErrorStates:  []string{"failed", "unknown"},
		ResourceName: "application gateway configuration apply",
		PollFunc: func(pollCtx context.Context) (string, error) {
			gw, err := r.readGateway(pollCtx, gwID)
			if err != nil {
				return "", err
			}
			last = *gw

			switch gw.ConfigStatus {
			case "applied":
				switch {
				case gw.ConfigRevision == nil || *gw.ConfigRevision < revision:
					// A previous apply's terminal state. Keep polling rather
					// than resolve on someone else's older outcome.
					return "", nil
				case *gw.ConfigRevision > revision:
					// The gateway renders its CURRENT authored state, so a newer
					// acknowledged revision is a SUPERSET of what we dispatched
					// — convergence, not failure. Reported as a warning so it is
					// not silent, unlike apache_instance, whose per-instance
					// config blob is replaced rather than accumulated and so
					// must fail closed.
					superseded = true
				}
				return "applied", nil

			case "failed":
				// The appliance's OWN words — the proxy validator's output or
				// the self-probe's. A customer who wrote a rule the proxy will
				// not load needs the proxy's sentence, not ours.
				detail := gw.ConfigDetail
				if detail == "" {
					detail = "The appliance gave no detail."
				}
				terminalErr = fmt.Errorf(
					"revision %d was refused and the gateway is still serving its previous "+
						"configuration:\n\n%s", revision, detail)
				return "failed", nil

			case "unknown":
				// TERMINAL, and deliberately weaker than `failed`: appgw's
				// reaper writes it when no verdict arrived, and unlike a refusal
				// it establishes NO guarantee about what the appliance is
				// serving. Nothing will ever move it, so waiting for the ceiling
				// would burn two hours to print the wrong message.
				terminalErr = fmt.Errorf(
					"revision %d was dispatched but no verdict ever arrived, so the attempt was "+
						"abandoned. Unlike a refusal this says NOTHING about what the gateway is "+
						"serving — it may or may not have loaded this configuration; read the "+
						"gateway's config_status and re-apply", revision)
				return "unknown", nil
			}
			return gw.ConfigStatus, nil
		},
	})
	if terminalErr != nil {
		return nil, false, terminalErr
	}
	if err != nil {
		// Name the revision and what the appliance had acknowledged: a bare
		// deadline sends practitioners to `terraform state rm`, which is the one
		// thing that must not happen here — the apply is dispatched and recorded.
		ack := "nothing"
		if last.ConfigRevision != nil {
			ack = fmt.Sprintf("revision %d", *last.ConfigRevision)
		}
		return nil, false, fmt.Errorf(
			"revision %d was dispatched but the appliance had not answered (last status %q, "+
				"acknowledged %s). The change may still land; read the gateway's config_status "+
				"before re-running: %w", revision, last.ConfigStatus, ack, err)
	}
	return &last, superseded, nil
}
