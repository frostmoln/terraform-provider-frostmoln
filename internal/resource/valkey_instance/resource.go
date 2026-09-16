package valkey_instance

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/orphan"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/planmod"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/scopedecl"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/timeouts"
)

var (
	_ resource.Resource                = &valkeyInstanceResource{}
	_ resource.ResourceWithImportState = &valkeyInstanceResource{}
	_ resource.ResourceWithModifyPlan  = &valkeyInstanceResource{}
)

// NewResource returns a new valkey_instance resource factory.
func NewResource() resource.Resource {
	return &valkeyInstanceResource{}
}

type valkeyInstanceResource struct {
	client       *client.Client
	pollInterval time.Duration
	pollTimeout  time.Duration
}

func (r *valkeyInstanceResource) getPollInterval() time.Duration {
	if r.pollInterval > 0 {
		return r.pollInterval
	}
	return 5 * time.Second
}

// getPollTimeout is the DEFAULT wait budget — the timeouts block's fallback
// per verb. Raised from 15m to 30m (audit D8): the saga's readiness step
// alone — apt + package install + init + the in-VM console probe on a slow
// mirror (managedServiceReadyTimeout, provisioning
// internal/activity/managed_readiness.go:20, consumed at
// cache_activities.go:742) is budgeted 20 minutes, inside a 30-minute
// CategoryLongRunning envelope, and it is the LAST leg of the create saga,
// behind the VM/volume/network steps. 15m gave up on a create the platform
// was still legitimately working — and this family keeps one HCL surface and
// one budget story with the database twins, both now at 30m.
//
// 30m sits exactly ON the platform's envelope, not under it: a saga that
// legitimately outruns the envelope is the platform's own failure to give a
// verdict on, and the platform's row is richer than the provider's generic
// timeout — a practitioner expecting those cases raises the budget via the
// timeouts block and keeps reading the platform's answer.
func (r *valkeyInstanceResource) getPollTimeout() time.Duration {
	if r.pollTimeout > 0 {
		return r.pollTimeout
	}
	return 30 * time.Minute
}

// resolveBudgets turns the configured timeouts block into effective budgets,
// falling back per verb to the same value this resource has always hardcoded
// (getPollTimeout's 30m — 15m before 2026-09-16). Routing the defaults through
// the accessor keeps the test-injection seam intact: a test that shrinks
// pollTimeout shrinks every wait that does not carry an explicit timeouts
// override, exactly as before.
func (r *valkeyInstanceResource) resolveBudgets(m *timeouts.Model) timeouts.Budgets {
	budgets, err := m.Resolve(timeouts.Uniform(r.getPollTimeout()))
	if err != nil {
		// Unreachable via HCL (the block validator rejects bad durations at
		// plan time); degrade to the defaults rather than fail a wait.
		return timeouts.Uniform(r.getPollTimeout())
	}
	return budgets
}

// waitRunning waits until the instance returns to "running" state. The budget
// is the timeouts block's update (or create, during Create) override.
func (r *valkeyInstanceResource) waitRunning(ctx context.Context, id string, budget time.Duration) error {
	_, err := client.WaitForState(ctx, client.PollConfig{
		Interval:     r.getPollInterval(),
		Timeout:      budget,
		TargetStates: []string{"running"},
		ErrorStates:  []string{"error", "failed"},
		ResourceName: "valkey_instance",
		PollFunc: func(pollCtx context.Context) (string, error) {
			pollResp, pollErr := r.client.Get(pollCtx, r.client.TenantPath("/caches/"+id), nil)
			if pollErr != nil {
				return "", pollErr
			}
			current, parseErr := client.ParseResponse[apiValkeyInstance](pollResp)
			if parseErr != nil {
				return "", parseErr
			}
			return current.Status, nil
		},
	})
	return err
}

// awaitResize watches a resize write to its verdict, absorbing the resize gap
// (Ambix 01a03e62): a 202 answer carries an Operation — the saga is still
// running — and USED to be discarded (`if _, err := Post`) while a bare
// status-poll decided; that poll could satisfy itself on the still-current
// `running` before the saga moved the instance to `resizing` and never saw a
// resize that FAILED. The database and cache/webserver/messaging services
// synchronously CAS `running`→`resizing` before answering 202 (verified
// 2026-09-07), so the first poll now sees `resizing` — but the operation, not
// the status, is what carries the resize's verdict, so the 202 is polled to
// completion. The 200 {"status":"resizing"} answer is the legacy synchronous
// ack — the only case for the status-poll fallback.
func (r *valkeyInstanceResource) awaitResize(ctx context.Context, id string, apiResp *client.Response, budget time.Duration) error {
	if !apiResp.IsAccepted() {
		return r.waitRunning(ctx, id, budget)
	}
	op, opErr := client.ParseResponse[client.OperationResponse](apiResp)
	if opErr != nil || op.OperationID == "" {
		// The saga was accepted and its operation cannot be watched from here.
		// That is classified — NOT a success and NOT a failure: the resize may
		// still complete, and retrying blind can hit 409 RESIZE-backed states.
		unknown := opErr
		if unknown == nil {
			unknown = fmt.Errorf("the resize was accepted but returned no operation id")
		}
		return fmt.Errorf("resize was accepted but its outcome could not be tracked; the resize may "+
			"still be running — check the instance status (portal, `fm cache valkey instance list`) before retrying: %w", unknown)
	}
	if _, waitErr := r.client.WaitForOperation(ctx, op.OperationID, r.getPollInterval(), budget); waitErr != nil {
		return waitErr
	}
	return r.waitRunning(ctx, id, budget)
}

func (r *valkeyInstanceResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_valkey_instance"
}

func (r *valkeyInstanceResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manages a managed Valkey instance in the Frostmoln platform." + "\n\n" + scopedecl.Summary("frostmoln_valkey_instance"),
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Description: "The unique identifier of the Valkey instance.",
				Computed:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"name": schema.StringAttribute{
				Description: "The name of the Valkey instance.",
				Required:    true,
			},
			"version": schema.StringAttribute{
				Description: "The Valkey version (e.g. \"8.1\").",
				Required:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"flavor_id": schema.StringAttribute{
				Description: "The flavor/size for the Valkey instance (e.g. \"cache.gp1.small\", \"cache.gp1.medium\"). Changing this triggers an in-place flavor resize, which RESTARTS the instance (brief downtime) — unlike an online storage grow. Cannot be changed together with storage_gb in the same apply.",
				Required:    true,
			},
			"storage_gb": schema.Int64Attribute{
				Description: "The storage size in gigabytes (defaults to 10 if unset). Can only be increased (grow-only); volumes cannot be shrunk.",
				Optional:    true,
				Computed:    true,
				PlanModifiers: []planmodifier.Int64{
					int64planmodifier.UseStateForUnknown(),
				},
			},
			"vpc_id": schema.StringAttribute{
				Description: "The VPC ID where the Valkey instance will be deployed.",
				Required:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"subnet_id": schema.StringAttribute{
				Description: "The subnet ID where the Valkey instance will be deployed.",
				Required:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"persistence_mode": schema.StringAttribute{
				Description: "The persistence mode for the Valkey instance (\"rdb\", \"aof\", \"rdb+aof\", or \"none\"). Defaults to \"rdb\", rendered into the instance at creation only — changing it later is refused, because no platform workflow re-renders a running instance (replace the instance to change it).",
				Optional:    true,
				Computed:    true,
				// NOT a schema Default: TransformDefaults substitutes a default
				// whenever the CONFIG value is null, irrespective of prior
				// state — an instance whose render used a non-default mode and
				// whose HCL later omits the line would plan the default against
				// the state value and hit the refusal below, mis-attributed to
				// "the configuration requests rdb". The modifier pins the
				// state value (what the create render actually installed) and
				// only plans the default for a null state (a fresh create).
				// See internal/planmod and the frostmoln_secret precedent.
				PlanModifiers: []planmodifier.String{
					planmod.StringUseStateOrDefault("rdb"),
				},
			},
			"eviction_policy": schema.StringAttribute{
				Description: "The eviction policy for the Valkey instance (e.g. \"noeviction\", \"allkeys-lru\"). Defaults to \"noeviction\", rendered into the instance at creation only — changing it later is refused, because no platform workflow re-renders a running instance (replace the instance to change it).",
				Optional:    true,
				Computed:    true,
				// Same reason as persistence_mode above.
				PlanModifiers: []planmodifier.String{
					planmod.StringUseStateOrDefault("noeviction"),
				},
			},
			"status": schema.StringAttribute{
				Description: "The current status of the Valkey instance.",
				Computed:    true,
			},
			"private_ip": schema.StringAttribute{
				Description: "The private IP address of the Valkey instance.",
				Computed:    true,
			},
			"port": schema.Int64Attribute{
				Description: "The port number the Valkey instance is listening on.",
				Computed:    true,
				PlanModifiers: []planmodifier.Int64{
					int64planmodifier.UseStateForUnknown(),
				},
			},
			"admin_username": schema.StringAttribute{
				Description: "The admin username for the Valkey instance.",
				Computed:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"created_at": schema.StringAttribute{
				Description: "The timestamp when the instance was created.",
				Computed:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"updated_at": schema.StringAttribute{
				Description: "The timestamp when the instance was last updated.",
				Computed:    true,
			},
		},
		Blocks: map[string]schema.Block{
			// Customer-tunable wait budgets: default 30m per verb (15m before
			// 2026-09-16 — audit D8: the platform's readiness step alone is
			// budgeted 20m, inside a 30m CategoryLongRunning saga envelope).
			// A timeouts change
			// is an in-place no-op on real infrastructure — verified by the
			// Gate 2 smoke test (project-docs/product/TF-CONVERGENCE-WALL-PLAN.md).
			"timeouts": timeouts.Schema(),
		},
	}
}

func (r *valkeyInstanceResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	c, ok := req.ProviderData.(*client.Client)
	if !ok {
		resp.Diagnostics.AddError(
			"Unexpected Resource Configure Type",
			fmt.Sprintf("Expected *client.Client, got: %T", req.ProviderData),
		)
		return
	}
	r.client = c
}

func (r *valkeyInstanceResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan ValkeyInstanceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	apiReq := plan.toCreateRequest(ctx, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}

	// The customer's timeouts block, falling back to this resource's
	// defaults (see resolveBudgets above for what each default is now).
	budgets := r.resolveBudgets(plan.Timeouts)

	// The created-at floor and subject for the discovery sweep (below): the
	// instance this apply produces must have been created by THIS apply, and
	// the refused arm names what the platform said no to.
	applyStarted := time.Now().UTC()
	floor := applyStarted.Add(-time.Minute)
	subject := fmt.Sprintf("the Valkey instance %q", plan.Name.ValueString())

	apiResp, err := r.client.Post(ctx, r.client.TenantPath("/caches"), apiReq)
	if err != nil {
		resp.Diagnostics.AddError("Failed to create Valkey instance", err.Error())
		return
	}

	// The backend may answer either synchronously (201 with the instance body)
	// or asynchronously (202 with an Operation). Tolerate both: resolve the
	// instance ID from whichever shape we got, then run the existing
	// poll-to-running + state refresh below against that ID.
	var instID string
	// adopted records that instID came from the discovery sweep, not the
	// operation: the follow-up read then owes the sweep's contract extra
	// diligence (identity verify, and never un-tracking the found object).
	adopted := false
	if apiResp.IsAccepted() {
		op, err := client.ParseResponse[client.Operation](apiResp)
		if err != nil {
			resp.Diagnostics.AddError("Failed to parse Valkey instance operation response", err.Error())
			return
		}
		done, err := r.client.WaitForOperation(ctx, op.OperationID, r.getPollInterval(), budgets.Create)
		if err != nil {
			if ctx.Err() != nil {
				// The practitioner cancelled this apply — do not spend the
				// classification or the sweep on a dead context, and do not
				// mislabel a cancellation as an unresolvable fate.
				resp.Diagnostics.AddError("Valkey instance creation failed", err.Error())
				return
			}
			if r.client.ClassifyOperationFailure(ctx, op.OperationID) == client.OperationRefused {
				orphan.AddCreateRefused(&resp.Diagnostics, "Valkey Instance", subject, err)
				return
			}
			// UNKNOWN: the saga may still land. The sweep decides honestly.
			instID = r.adoptCreatedInstance(ctx, plan, floor, err, resp)
			adopted = instID != ""
			if !adopted {
				return
			}
		} else {
			instID = done.ResourceID
			if instID == "" {
				// The operation COMPLETED without its resourceId (degraded
				// provisioning). The instance exists; the sweep resolves it
				// by name.
				instID = r.adoptCreatedInstance(ctx, plan, floor,
					fmt.Errorf("the create operation completed but returned no resource ID"), resp)
				adopted = instID != ""
				if !adopted {
					return
				}
			}
		}

		// Persist state immediately so the ID is tracked, even if the
		// poll-to-running or final read below fails. The 202 path has no
		// create body, so fill the computed attributes from a GET of the
		// freshly-created instance.
		readResp, err := r.client.Get(ctx, r.client.TenantPath("/caches/"+instID), nil)
		if err != nil {
			if adopted {
				// The sweep FOUND the instance — a failed follow-up read must
				// not un-track it. Write the minimal tracked row (the id is
				// what destroy and refresh need) and say why.
				plan.ID = types.StringValue(instID)
				resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
				resp.Diagnostics.AddError("Adopted Valkey Instance Could Not Be Read",
					fmt.Sprintf("The discovery sweep adopted instance %s, but the follow-up read failed: %s. "+
						"The instance is tracked in state; refresh once the platform responds.", instID, err.Error()))
				return
			}
			resp.Diagnostics.AddError("Failed to read Valkey instance after creation", err.Error())
			return
		}
		inst, err := client.ParseResponse[apiValkeyInstance](readResp)
		if err != nil {
			resp.Diagnostics.AddError("Failed to parse Valkey instance response", err.Error())
			return
		}
		if adopted && (inst.Name != plan.Name.ValueString() || (inst.Type != "" && inst.Type != "valkey")) {
			// The sweep is name+floor matched, but the honest read is the last
			// word: anything read outside this apply's name/type family is not
			// this apply's instance (adopting the wrong object outranks
			// adopting none — and this row would be destroyable).
			resp.Diagnostics.AddError("Adopted Object Does Not Match This Apply",
				fmt.Sprintf("The discovery sweep matched instance %s for name %q, but the platform's read "+
					"returned name %q type %q. Nothing was recorded in state; identify the instance with "+
					"`fm cache instance list` and `terraform import` it if it is yours.", instID, plan.Name.ValueString(), inst.Name, inst.Type))
			return
		}
		plan.fromAPI(ctx, inst, &resp.Diagnostics)
		if resp.Diagnostics.HasError() {
			return
		}
		resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
		if resp.Diagnostics.HasError() {
			return
		}
	} else {
		inst, err := client.ParseResponse[apiValkeyInstance](apiResp)
		if err != nil {
			resp.Diagnostics.AddError("Failed to parse Valkey instance response", err.Error())
			return
		}
		instID = inst.ID

		plan.fromAPI(ctx, inst, &resp.Diagnostics)
		if resp.Diagnostics.HasError() {
			return
		}

		// Save state immediately so the ID is tracked, even if polling fails.
		resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
		if resp.Diagnostics.HasError() {
			return
		}
	}

	// Poll until instance reaches "running" status.
	_, err = client.WaitForState(ctx, client.PollConfig{
		Interval:     r.getPollInterval(),
		Timeout:      budgets.Create,
		TargetStates: []string{"running"},
		ErrorStates:  []string{"error", "failed"},
		ResourceName: "valkey_instance",
		PollFunc: func(pollCtx context.Context) (string, error) {
			pollResp, pollErr := r.client.Get(pollCtx, r.client.TenantPath("/caches/"+instID), nil)
			if pollErr != nil {
				return "", pollErr
			}
			current, parseErr := client.ParseResponse[apiValkeyInstance](pollResp)
			if parseErr != nil {
				return "", parseErr
			}
			return current.Status, nil
		},
	})
	if err != nil {
		resp.Diagnostics.AddError("Valkey instance failed to reach running state", err.Error())
		return
	}

	// Refresh state after polling completes to get final status, IPs, etc.
	readResp, err := r.client.Get(ctx, r.client.TenantPath("/caches/"+instID), nil)
	if err != nil {
		resp.Diagnostics.AddError("Failed to read Valkey instance after creation", err.Error())
		return
	}
	finalInst, err := client.ParseResponse[apiValkeyInstance](readResp)
	if err != nil {
		resp.Diagnostics.AddError("Failed to parse Valkey instance response", err.Error())
		return
	}

	plan.fromAPI(ctx, finalInst, &resp.Diagnostics)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// adoptCreatedInstance is the create-timeout arm of the orphan contract
// (internal/orphan) for this resource family: the wait gave up while the saga
// may still be running, or completed without its resourceId, and the family
// listing resolves the instance this apply produced — matched on name, created
// after this apply started, and of this apply's type so a same-named sibling
// type can never be adopted by mistake. Found means adopt (the caller's
// read-and-persist path is the honest read); absent means the sweep verified
// the absence, so re-applying is safe; unreadable names the platform's last
// word plus the list path. Never `terraform state rm` — it is the one action
// that re-orphans a live, billing instance. Returns the adopted id, empty when
// the diagnostic already says everything (the caller must return).
func (r *valkeyInstanceResource) adoptCreatedInstance(ctx context.Context, plan ValkeyInstanceModel, floor time.Time, waitErr error, resp *resource.CreateResponse) string {
	return orphan.AdoptCreateOnTimeout(ctx, &resp.Diagnostics, orphan.CreateParams{
		ResourceName: "Valkey Instance",
		FMList:       "`fm cache instance list`",
		WaitErr:      waitErr,
		Resolve: func(ctx context.Context) (string, error) {
			apiResp, err := r.client.Get(ctx, r.client.TenantPath("/caches"), nil)
			if err != nil {
				return "", err
			}
			var list apiValkeyInstanceList
			if err := json.Unmarshal(apiResp.Body, &list); err != nil {
				return "", err
			}
			candidates := make([]orphan.Candidate, 0, len(list.Instances))
			for _, it := range list.Instances {
				// Fail closed on the discriminator: a row with no type (or a
				// sibling-family row) is never adopted — adopting the wrong
				// object outranks adopting none.
				if it.Type != "valkey" {
					continue
				}
				candidates = append(candidates, orphan.Candidate{ID: it.ID, Name: it.Name, CreatedAt: it.CreatedAt})
			}
			return orphan.PickCreated(candidates, plan.Name.ValueString(), floor)
		},
	})
}

func (r *valkeyInstanceResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state ValkeyInstanceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	apiResp, err := r.client.Get(ctx, r.client.TenantPath("/caches/"+state.ID.ValueString()), nil)
	if err != nil {
		if client.IsNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Failed to read Valkey instance", err.Error())
		return
	}

	inst, err := client.ParseResponse[apiValkeyInstance](apiResp)
	if err != nil {
		resp.Diagnostics.AddError("Failed to parse Valkey instance response", err.Error())
		return
	}

	state.fromAPI(ctx, inst, &resp.Diagnostics)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *valkeyInstanceResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan ValkeyInstanceModel
	var state ValkeyInstanceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// Create-rendered cache settings (class A2 of the 2026-09 convergence
	// audit): the create render (cloud-init, provisioning's CreateCacheVM) is
	// the only path a persistence/eviction value ever takes — the PUT below
	// validates, stores and echoes it while the running Valkey keeps its
	// previous behaviour. Warned about at plan; refused HERE, before any
	// request, rather than applied as a change it does not make.
	if refused := refusedCacheConfigChanges(plan, state); len(refused) > 0 {
		addCacheConfigRefusals(&resp.Diagnostics, refused)
		return
	}

	id := state.ID.ValueString()

	budgets := r.resolveBudgets(plan.Timeouts)

	// Storage grows via POST /caches/{id}/resize — the PUT below cannot change storage. Grow-only:
	// Cinder volumes cannot shrink, so reject a decrease with a clear error rather than a silent
	// no-op / perpetual diff.
	if !plan.StorageGB.IsNull() && !plan.StorageGB.IsUnknown() && plan.StorageGB.ValueInt64() != state.StorageGB.ValueInt64() {
		newSize := plan.StorageGB.ValueInt64()
		cur := state.StorageGB.ValueInt64()
		if newSize < cur {
			resp.Diagnostics.AddError("Storage cannot be shrunk",
				fmt.Sprintf("storage_gb can only be increased (current %d GB, requested %d GB); volumes cannot shrink.", cur, newSize))
			return
		}
		apiResp, postErr := r.client.Post(ctx, r.client.TenantPath("/caches/"+id+"/resize"), apiResizeValkeyInstanceRequest{StorageGB: int(newSize)})
		if postErr != nil {
			resp.Diagnostics.AddError("Failed to resize Valkey storage", postErr.Error())
			return
		}
		if err := r.awaitResize(ctx, id, apiResp, budgets.Update); err != nil {
			resp.Diagnostics.AddError("Valkey instance failed to reach running state after storage resize", err.Error())
			return
		}
	}

	// Flavor changes via POST /caches/{id}/resize (a Nova resize — the VM RESTARTS, brief downtime).
	// Mutually exclusive with a storage resize at the backend, so it is a SEPARATE request; if a
	// single apply changed both, the storage resize above already returned the instance to running.
	if !plan.FlavorID.IsNull() && !plan.FlavorID.IsUnknown() && plan.FlavorID.ValueString() != state.FlavorID.ValueString() {
		apiResp, postErr := r.client.Post(ctx, r.client.TenantPath("/caches/"+id+"/resize"), apiResizeValkeyInstanceRequest{FlavorID: plan.FlavorID.ValueString()})
		if postErr != nil {
			resp.Diagnostics.AddError("Failed to resize Valkey flavor", postErr.Error())
			return
		}
		if err := r.awaitResize(ctx, id, apiResp, budgets.Update); err != nil {
			resp.Diagnostics.AddError("Valkey instance failed to reach running state after flavor resize", err.Error())
			return
		}
	}

	// In-place field updates (name/persistence/eviction) via PUT — skip an empty PUT when only
	// storage/flavor changed.
	updateReq := plan.toUpdateRequest(&state)
	if updateReq.hasChanges() {
		if _, err := r.client.Put(ctx, r.client.TenantPath("/caches/"+id), updateReq); err != nil {
			resp.Diagnostics.AddError("Failed to update Valkey instance", err.Error())
			return
		}
		if err := r.waitRunning(ctx, id, budgets.Update); err != nil {
			resp.Diagnostics.AddError("Valkey instance failed to reach running state after update", err.Error())
			return
		}
	}

	// Refresh state from API.
	apiResp, err := r.client.Get(ctx, r.client.TenantPath("/caches/"+id), nil)
	if err != nil {
		resp.Diagnostics.AddError("Failed to read Valkey instance after update", err.Error())
		return
	}

	inst, err := client.ParseResponse[apiValkeyInstance](apiResp)
	if err != nil {
		resp.Diagnostics.AddError("Failed to parse Valkey instance response", err.Error())
		return
	}

	plan.fromAPI(ctx, inst, &resp.Diagnostics)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *valkeyInstanceResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state ValkeyInstanceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	id := state.ID.ValueString()

	_, err := r.client.Delete(ctx, r.client.TenantPath("/caches/"+id))
	if err != nil {
		if client.IsNotFound(err) {
			return
		}
		resp.Diagnostics.AddError("Failed to delete Valkey instance", err.Error())
		return
	}

	// Wait for the instance to be fully deleted (404 on GET), on the
	// timeouts block's delete budget.
	budgets := r.resolveBudgets(state.Timeouts)
	_, err = client.WaitForState(ctx, client.PollConfig{
		Interval:     r.getPollInterval(),
		Timeout:      budgets.Delete,
		TargetStates: []string{"deleted"},
		ErrorStates:  []string{"error"},
		ResourceName: "valkey_instance",
		PollFunc: func(pollCtx context.Context) (string, error) {
			pollResp, pollErr := r.client.Get(pollCtx, r.client.TenantPath("/caches/"+id), nil)
			if pollErr != nil {
				if client.IsNotFound(pollErr) {
					return "deleted", nil
				}
				return "", pollErr
			}
			current, parseErr := client.ParseResponse[apiValkeyInstance](pollResp)
			if parseErr != nil {
				return "", parseErr
			}
			return current.Status, nil
		},
	})
	if err != nil {
		resp.Diagnostics.AddError("Valkey instance failed to delete", err.Error())
	}
}

func (r *valkeyInstanceResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}

// cacheConfigAttr is one setting the platform renders into a Valkey instance
// only at creation, with the values on both sides so the diagnostic can say
// what is held and what was requested.
type cacheConfigAttr struct{ name, current, requested string }

// refusedCacheConfigChanges lists the create-rendered settings a plan
// changes (class A2 of the 2026-09 convergence audit).
//
// The cache service's PUT answers: it validates via IsValidPersistenceMode /
// IsValidEvictionPolicy, assigns onto the instance row and returns — with no
// provisioning dispatch on that path, and CreateCache is a documented no-op
// after the create render (cache_activities.go:745-748), so the values reach
// the running Valkey ONLY through the create render. Applying the change
// would store and echo it honestly — and change nothing on the server.
// Refusing is the honest answer while the platform has no config-update
// path. A flavor/flavor resize does NOT re-render either (resize_cache has
// no in-guest reconfigure step) — a resize does not launder the refusal
// away, and a resize combined with a config change is refused whole.
func refusedCacheConfigChanges(plan, state ValkeyInstanceModel) []cacheConfigAttr {
	var refused []cacheConfigAttr
	if !plan.PersistenceMode.IsUnknown() && !plan.PersistenceMode.Equal(state.PersistenceMode) {
		refused = append(refused, cacheConfigAttr{
			"persistence_mode",
			state.PersistenceMode.ValueString(),
			plan.PersistenceMode.ValueString(),
		})
	}
	if !plan.EvictionPolicy.IsUnknown() && !plan.EvictionPolicy.Equal(state.EvictionPolicy) {
		refused = append(refused, cacheConfigAttr{
			"eviction_policy",
			state.EvictionPolicy.ValueString(),
			plan.EvictionPolicy.ValueString(),
		})
	}
	return refused
}

// cacheConfigRefusalDetail carries the remedy. Recreating is the one shape
// that works — the values are honoured in the create render — and it is
// named deliberately rather than left to inference.
const cacheConfigRefusalDetail = "The Frostmoln cache platform renders a managed Valkey instance's " +
	"configuration only at creation: these settings reach the running server in the create render, " +
	"and no workflow re-renders it later. A change of %[1]s is validated, stored and echoed back " +
	"while the running server keeps its current behaviour — this instance has %[1]s = %[2]q and the " +
	"configuration requests %[3]q.\n\n" +
	"To hold a different %[1]s, replace the instance deliberately (destroy and re-create): the new " +
	"instance is rendered with the requested value. The change is refused rather than applied until " +
	"the platform gains a config-update path."

func addCacheConfigRefusals(diags *diag.Diagnostics, refused []cacheConfigAttr) {
	for _, a := range refused {
		diags.AddAttributeError(
			path.Root(a.name),
			a.name+" cannot be changed on a running Valkey instance",
			fmt.Sprintf(cacheConfigRefusalDetail, a.name, a.current, a.requested),
		)
	}
}

// warnCacheConfigRefusals is the plan-time half of addCacheConfigRefusals. It
// must stay a WARNING: an error raised while planning also aborts `terraform
// destroy`, because the destroy plan's refresh phase computes an ordinary
// (non-null) plan and runs ModifyPlan against it. The refusal itself is in
// Update, which a destroy never reaches. See internal/planmod for the same
// reasoning on the shared modifiers.
func warnCacheConfigRefusals(diags *diag.Diagnostics, refused []cacheConfigAttr) {
	for _, a := range refused {
		diags.AddAttributeWarning(
			path.Root(a.name),
			a.name+" cannot be changed on a running Valkey instance",
			fmt.Sprintf(cacheConfigRefusalDetail, a.name, a.current, a.requested)+
				"\n\nThis apply will fail; a destroy is unaffected.",
		)
	}
}

// ModifyPlan surfaces the create-rendered settings a plan would change, as a
// WARNING; Update refuses them. Create (no prior state) and destroy (no
// plan) skip comparison entirely — on create the values ARE rendered, and a
// destroy must stay unblocked (see warnCacheConfigRefusals above).
func (r *valkeyInstanceResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.State.Raw.IsNull() || req.Plan.Raw.IsNull() {
		return // Create or destroy: nothing to compare.
	}

	var plan, state ValkeyInstanceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	warnCacheConfigRefusals(&resp.Diagnostics, refusedCacheConfigChanges(plan, state))
}
