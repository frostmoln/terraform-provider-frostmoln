package postgres_instance

import (
	"context"
	"fmt"
	"time"

	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/boolplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/planmod"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/scopedecl"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/stateupgrade"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/timeouts"
)

var (
	_ resource.Resource                 = &postgresInstanceResource{}
	_ resource.ResourceWithImportState  = &postgresInstanceResource{}
	_ resource.ResourceWithUpgradeState = &postgresInstanceResource{}
)

// NewResource returns a new postgres_instance resource factory.
func NewResource() resource.Resource {
	return &postgresInstanceResource{}
}

type postgresInstanceResource struct {
	client       *client.Client
	pollInterval time.Duration
	pollTimeout  time.Duration
}

// resolveBudgets turns the configured timeouts block into effective budgets,
// falling back per verb to the same value this resource has always hardcoded
// (getPollTimeout's 30m). Routing the defaults through the accessor keeps the
// test-injection seam intact: a test that shrinks pollTimeout shrinks every
// wait that does not carry an explicit timeouts override, exactly as before.
func (r *postgresInstanceResource) resolveBudgets(m *timeouts.Model) timeouts.Budgets {
	budgets, err := m.Resolve(timeouts.Uniform(r.getPollTimeout()))
	if err != nil {
		// Unreachable via HCL (the block validator rejects bad durations at
		// plan time); degrade to the defaults rather than fail a wait.
		return timeouts.Uniform(r.getPollTimeout())
	}
	return budgets
}

func (r *postgresInstanceResource) getPollInterval() time.Duration {
	if r.pollInterval > 0 {
		return r.pollInterval
	}
	return 5 * time.Second
}

// getPollTimeout is the DEFAULT wait budget — the timeouts block's fallback
// per verb — and the transient-409 retry window on resize.
//
// Raised from 15 minutes on 2026-09-03, when the database-ha entitlement was removed
// and ha_enabled = true became reachable for every tenant. An HA instance is a TWO-VM
// provision with a replica seed between them, so it is not bounded by the same clock
// as a single node. 30 minutes matches kubernetes_node_pool, the platform's other
// multi-VM resource. A practitioner with a legitimately slower provision raises it
// per resource via the `timeouts` block instead of waiting on a provider release.
//
// The cliff this timeout used to sit on is GONE: Create now writes the instance id to
// state BEFORE waiting (see the 202 branch), so a timeout leaves a tracked instance the
// operator can destroy rather than an untracked one they must hunt for in the portal.
// What the timeout still decides is how long an apply blocks before giving up, which is
// all a timeout should decide.
func (r *postgresInstanceResource) getPollTimeout() time.Duration {
	if r.pollTimeout > 0 {
		return r.pollTimeout
	}
	return 30 * time.Minute
}

// pollRunning waits until the instance returns to "running" state. The wait
// budget is the timeouts block's update (or create, during Create) override.
func (r *postgresInstanceResource) pollRunning(ctx context.Context, id string, budget time.Duration) (string, error) {
	return client.WaitForState(ctx, client.PollConfig{
		Interval:     r.getPollInterval(),
		Timeout:      budget,
		TargetStates: []string{"running"},
		ErrorStates:  []string{"error", "failed"},
		ResourceName: "postgres_instance",
		PollFunc: func(pollCtx context.Context) (string, error) {
			pollResp, pollErr := r.client.Get(pollCtx, r.client.TenantPath("/databases/"+id), nil)
			if pollErr != nil {
				return "", pollErr
			}
			current, parseErr := client.ParseResponse[apiPostgresInstance](pollResp)
			if parseErr != nil {
				return "", parseErr
			}
			return current.Status, nil
		},
	})
}

// resizeStorage grows the instance's storage online via POST /resize, then
// waits for the write's verdict. Grow-only: a shrink is refused in Update.
//
// The resize retries a TRANSIENT 409 (a mixed apply that also removes a replica
// can 409 the resize while the replica is mid-delete); a permanent 409 (wrong
// state) surfaces immediately via IsTransientResizeConflict's default-deny.
// The post-resize wait runs on the timeouts block's update budget; the
// transient-409 retry window itself stays provider-internal.
func (r *postgresInstanceResource) resizeStorage(ctx context.Context, id string, storageGB int, budget time.Duration) error {
	resp, err := r.client.PostWithConflictRetry(ctx, r.client.TenantPath("/databases/"+id+"/resize"), apiResizePostgresInstanceRequest{StorageGB: storageGB}, client.IsTransientResizeConflict, r.getPollInterval(), r.getPollTimeout())
	if err != nil {
		return err
	}
	return r.awaitResize(ctx, id, resp, budget)
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
func (r *postgresInstanceResource) awaitResize(ctx context.Context, id string, apiResp *client.Response, budget time.Duration) error {
	if !apiResp.IsAccepted() {
		_, fallbackErr := r.pollRunning(ctx, id, budget)
		return fallbackErr
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
			"still be running — check the instance status (portal, `fm db postgres instance list`) before retrying: %w", unknown)
	}
	if _, waitErr := r.client.WaitForOperation(ctx, op.OperationID, r.getPollInterval(), budget); waitErr != nil {
		return waitErr
	}
	_, runningErr := r.pollRunning(ctx, id, budget)
	return runningErr
}

func (r *postgresInstanceResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_postgres_instance"
}

func (r *postgresInstanceResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		// v1: the HCL attribute flavor was renamed to flavor_id to match the
		// flagship frostmoln_instance and the cache/messaging offers (the wire
		// tag was always flavorId). See UpgradeState for the v0->v1 migration.
		Version:     1,
		Description: "Manages a managed PostgreSQL database instance in the Frostmoln platform." + "\n\n" + scopedecl.Summary("frostmoln_postgres_instance"),
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Description: "The unique identifier of the PostgreSQL instance.",
				Computed:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"name": schema.StringAttribute{
				Description: "The name of the PostgreSQL instance.",
				Required:    true,
			},
			"version": schema.StringAttribute{
				Description: "The PostgreSQL version (e.g. \"15\", \"16\").",
				Required:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"flavor_id": schema.StringAttribute{
				Description: "The flavor ID/size for the database instance (e.g. \"db.gp1.small\", \"db.gp1.medium\").",
				Required:    true,
				PlanModifiers: []planmodifier.String{
					planmod.StringWarnOnChange("Changing flavor_id (flavor resize) is not yet supported for managed database instances. Keep the original flavor_id, or destroy and recreate the instance to change it."),
				},
			},
			"storage_gb": schema.Int64Attribute{
				Description: "The storage size in gigabytes. Can be increased in place (online resize); decreasing it is not supported.",
				Required:    true,
				PlanModifiers: []planmodifier.Int64{
					planmod.Int64GrowOnly("GB"),
				},
			},
			"vpc_id": schema.StringAttribute{
				Description: "The VPC ID where the database instance will be deployed.",
				Required:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"subnet_id": schema.StringAttribute{
				Description: "The subnet ID where the database instance will be deployed.",
				Required:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"ha_enabled": schema.BoolAttribute{
				Description: "Whether high availability is enabled with a standby replica.",
				Optional:    true,
				Computed:    true,
				PlanModifiers: []planmodifier.Bool{
					boolplanmodifier.UseStateForUnknown(),
					boolplanmodifier.RequiresReplace(),
				},
			},
			"ha_status": schema.StringAttribute{
				Description: "Availability state of the instance: disabled, provisioning, healthy, " +
					"degraded, failing_over or no_standby. This is what the platform actually has, " +
					"as opposed to ha_enabled, which records what was requested at create time. An " +
					"instance created before high availability was built reports ha_enabled = true " +
					"with ha_status = no_standby.",
				Computed: true,
			},
			"backup_enabled": schema.BoolAttribute{
				Description: "Whether automated backups are enabled.",
				Optional:    true,
				Computed:    true,
				PlanModifiers: []planmodifier.Bool{
					boolplanmodifier.UseStateForUnknown(),
				},
			},
			// backup_schedule and backup_retention_days are server-defaulted (ADR-0085:
			// "0 2 * * *" and the 35-day object-lock floor) but ONLY when backups are enabled --
			// the database backend gates both defaults on backup_enabled, and both columns are
			// nullable. Optional-without-Computed therefore failed the apply outright for
			// `backup_enabled = true` with the attribute omitted: config is null, the server
			// echoes its default, and Terraform reports "Provider produced inconsistent result
			// after apply". Both are Computed for that reason.
			//
			// They deliberately do NOT carry a schema Default, which is what redis_instance uses
			// for the schedule. TransformDefaults applies a default whenever the CONFIG value is
			// null, irrespective of prior state, so a Default of 35 would rewrite a
			// deliberately-raised retention back down the moment the practitioner drops the
			// attribute from HCL -- and the retention reaper's window is
			// GREATEST(COALESCE(backup_retention_days, 35), 35) and is NOT gated on
			// backup_enabled, so a 90 -> 35 rewrite makes every backup older than 35 days
			// reapable on the next sweep tick. Silent backup loss, where the Optional-only schema
			// at least failed loudly (it sent 0, which the server rejects).
			//
			// planmod.*UseStateOrDefault pins the server's value instead: an omitted attribute
			// keeps whatever the instance already has and is never sent. Plain UseStateForUnknown
			// is not enough -- it copies a NULL prior state straight into the plan, and state
			// written before these attributes became Computed holds exactly that, so under
			// `-refresh=false` (no Read, nothing normalized) the plan would carry null while the
			// apply reads back a real value: an inconsistent-result error AFTER the PUT landed.
			// The null-state arm plans the documented default instead, which also heals a legacy
			// row left at (backup_enabled = true, schedule NULL) -- a row the sweep's due-list
			// skips entirely, so it silently takes no backups at all.
			"backup_schedule": schema.StringAttribute{
				Description: "Cron expression for the backup schedule. Defaults to \"0 2 * * *\" server-side.",
				Optional:    true,
				Computed:    true,
				PlanModifiers: []planmodifier.String{
					planmod.StringUseStateOrDefault(defaultBackupSchedule),
				},
				Validators: []validator.String{
					// "" is not "unset": it is dropped by omitempty, stored as NULL, and read back
					// as the default -- an inconsistent-result error at apply. The backend has no
					// "clear the schedule" operation either (an Update re-applies the default while
					// backups are on); backup_enabled = false is the only off switch.
					stringvalidator.LengthAtLeast(1),
				},
			},
			"backup_retention_days": schema.Int64Attribute{
				Description: "Number of days to retain backups. Minimum 35 (backups are immutably object-locked for 35 days); maximum 90. Defaults to 35 server-side.",
				Optional:    true,
				Computed:    true,
				PlanModifiers: []planmodifier.Int64{
					planmod.Int64UseStateOrDefault(defaultBackupRetentionDays),
				},
				Validators: []validator.Int64{
					int64validator.Between(35, 90),
				},
			},
			"parameter_group_id": schema.StringAttribute{
				Description: "The ID of the parameter group to apply to the instance.",
				Optional:    true,
			},
			"status": schema.StringAttribute{
				Description: "The current status of the PostgreSQL instance.",
				Computed:    true,
			},
			"private_ip": schema.StringAttribute{
				Description: "The private IP address of the PostgreSQL instance.",
				Computed:    true,
			},
			"port": schema.Int64Attribute{
				Description: "The port number the PostgreSQL instance is listening on.",
				Computed:    true,
				PlanModifiers: []planmodifier.Int64{
					int64planmodifier.UseStateForUnknown(),
				},
			},
			"public_ip": schema.StringAttribute{
				Description: "The public IP address, if assigned.",
				Computed:    true,
			},
			"admin_username": schema.StringAttribute{
				Description: "The admin username for the PostgreSQL instance.",
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
			"tenant_id": schema.StringAttribute{
				Description: "The tenant ID that owns this instance.",
				Computed:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
		},
		Blocks: map[string]schema.Block{
			// Customer-tunable wait budgets: defaults keep the values this
			// resource has always hardcoded (30m per verb). A timeouts change
			// is an in-place no-op on real infrastructure — verified by the
			// Gate 2 smoke test (project-docs/product/TF-CONVERGENCE-WALL-PLAN.md).
			"timeouts": timeouts.Schema(),
		},
	}
}

func (r *postgresInstanceResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (r *postgresInstanceResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan PostgresInstanceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	apiReq := plan.toCreateRequest(ctx, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}

	// The customer's timeouts block, with defaults identical to the values
	// this resource has always hardcoded.
	budgets := r.resolveBudgets(plan.Timeouts)

	apiResp, err := r.client.Post(ctx, r.client.TenantPath("/databases"), apiReq)
	if err != nil {
		resp.Diagnostics.AddError("Failed to create PostgreSQL instance", err.Error())
		return
	}

	// The backend may answer either synchronously (201 with the instance body)
	// or asynchronously (202 with an Operation). Tolerate both: resolve the
	// instance ID from whichever shape we got, then run the existing
	// poll-to-running + state refresh below against that ID.
	var instID string
	if apiResp.IsAccepted() {
		op, err := client.ParseResponse[client.Operation](apiResp)
		if err != nil {
			resp.Diagnostics.AddError("Failed to parse PostgreSQL instance operation response", err.Error())
			return
		}
		// 🔴 STATE BEFORE THE WAIT. THIS IS WHAT STOPS A TIMEOUT ORPHANING A BILLABLE INSTANCE.
		//
		// The wait below can take half an hour, and until this existed it ran with NOTHING in
		// state: on a timeout Create returned the error before any Set, so Terraform held no id
		// for an instance that was already running and billing. It could not be refreshed,
		// destroyed or imported, and the next apply either re-created it or 409'd on the
		// duplicate name. The only recovery was for a human to find it in the portal.
		//
		// The id is available now because the create 202 carries it (database service, the
		// `resourceId` field). It could not come from the operation poll: provisioning fills an
		// operation's ResourceID from the workflow RESULT, so a PENDING operation has none.
		//
		// DEGRADES CLEANLY against an older database service, which sends no resourceId: the
		// branch is skipped and the behaviour is exactly what it was.
		//
		// Setting state from a CREATING instance is not novel -- the 201 branch below already
		// does precisely that from the create body. What is in state after this point is a real
		// instance with a real id, whose computed attributes are refreshed once the wait ends.
		if op.ResourceID != "" {
			if earlyResp, earlyErr := r.client.Get(ctx,
				r.client.TenantPath("/databases/"+op.ResourceID), nil); earlyErr == nil {
				if earlyInst, parseErr := client.ParseResponse[apiPostgresInstance](earlyResp); parseErr == nil {
					early := plan
					early.fromAPI(ctx, earlyInst, &resp.Diagnostics)
					if !resp.Diagnostics.HasError() {
						resp.Diagnostics.Append(resp.State.Set(ctx, &early)...)
					}
				}
			}
			// A failure to pre-record is NOT fatal and adds no diagnostic: the wait below is the
			// real work, and turning a best-effort bookkeeping read into a create failure would
			// trade a rare orphan for a common one.
			if resp.Diagnostics.HasError() {
				return
			}
		}

		done, err := r.client.WaitForOperation(ctx, op.OperationID, r.getPollInterval(), budgets.Create)
		if err != nil {
			resp.Diagnostics.AddError("PostgreSQL instance creation failed", err.Error())
			return
		}
		instID = done.ResourceID
		if instID == "" {
			resp.Diagnostics.AddError(
				"PostgreSQL instance operation returned no resource ID",
				"The create operation completed but returned no resource ID. The instance may "+
					"exist in the backend without being tracked in Terraform state.",
			)
			return
		}

		// Persist state immediately so the ID is tracked, even if the
		// poll-to-running or final read below fails. The 202 path has no
		// create body, so fill the computed attributes from a GET of the
		// freshly-created instance.
		readResp, err := r.client.Get(ctx, r.client.TenantPath("/databases/"+instID), nil)
		if err != nil {
			resp.Diagnostics.AddError("Failed to read PostgreSQL instance after creation", err.Error())
			return
		}
		inst, err := client.ParseResponse[apiPostgresInstance](readResp)
		if err != nil {
			resp.Diagnostics.AddError("Failed to parse PostgreSQL instance response", err.Error())
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
		inst, err := client.ParseResponse[apiPostgresInstance](apiResp)
		if err != nil {
			resp.Diagnostics.AddError("Failed to parse PostgreSQL instance response", err.Error())
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
		ResourceName: "postgres_instance",
		PollFunc: func(pollCtx context.Context) (string, error) {
			pollResp, pollErr := r.client.Get(pollCtx, r.client.TenantPath("/databases/"+instID), nil)
			if pollErr != nil {
				return "", pollErr
			}
			current, parseErr := client.ParseResponse[apiPostgresInstance](pollResp)
			if parseErr != nil {
				return "", parseErr
			}
			return current.Status, nil
		},
	})
	if err != nil {
		resp.Diagnostics.AddError("PostgreSQL instance failed to reach running state", err.Error())
		return
	}

	// Refresh state after polling completes to get final status, IPs, etc.
	readResp, err := r.client.Get(ctx, r.client.TenantPath("/databases/"+instID), nil)
	if err != nil {
		resp.Diagnostics.AddError("Failed to read PostgreSQL instance after creation", err.Error())
		return
	}
	finalInst, err := client.ParseResponse[apiPostgresInstance](readResp)
	if err != nil {
		resp.Diagnostics.AddError("Failed to parse PostgreSQL instance response", err.Error())
		return
	}

	plan.fromAPI(ctx, finalInst, &resp.Diagnostics)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *postgresInstanceResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state PostgresInstanceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	apiResp, err := r.client.Get(ctx, r.client.TenantPath("/databases/"+state.ID.ValueString()), nil)
	if err != nil {
		if client.IsNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Failed to read PostgreSQL instance", err.Error())
		return
	}

	inst, err := client.ParseResponse[apiPostgresInstance](apiResp)
	if err != nil {
		resp.Diagnostics.AddError("Failed to parse PostgreSQL instance response", err.Error())
		return
	}

	state.fromAPI(ctx, inst, &resp.Diagnostics)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *postgresInstanceResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan PostgresInstanceModel
	var state PostgresInstanceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	id := state.ID.ValueString()

	// flavor_id cannot change in place — the platform has no flavor-resize path and the
	// PUT below would silently drop it. The plan-time modifier only WARNS (an error there
	// would also block `terraform destroy`; see planmod.StringWarnOnChange), so the change
	// is refused HERE. Unknown values are skipped: they carry no comparable value.
	// An empty prior value carries nothing to compare against either — it means the
	// API returned no flavorId on the last read, and trapping every future update
	// behind a refusal naming `""` would be worse than letting the change through.
	if !plan.FlavorID.IsUnknown() && !state.FlavorID.IsUnknown() && state.FlavorID.ValueString() != "" &&
		!plan.FlavorID.Equal(state.FlavorID) {
		resp.Diagnostics.AddError(
			"flavor_id cannot be changed",
			fmt.Sprintf("Changing flavor_id (flavor resize) is not yet supported for managed database instances (currently %q, requested %q). Keep the original flavor_id, or destroy and recreate the instance to change it.",
				state.FlavorID.ValueString(), plan.FlavorID.ValueString()),
		)
		return
	}

	// Storage grow goes through POST /resize (online, grow-only). A shrink is only
	// WARNED about at plan time (storage_gb GrowOnly modifier — an error there would
	// also block `terraform destroy`), so this is where it is actually refused: fail
	// with a clear message rather than a silent no-op.
	budgets := r.resolveBudgets(plan.Timeouts)
	switch {
	case plan.StorageGB.ValueInt64() < state.StorageGB.ValueInt64():
		resp.Diagnostics.AddError(
			"Storage cannot be shrunk",
			fmt.Sprintf("storage_gb can only be increased (currently %d GB, requested %d GB); storage is grown online and cannot be shrunk.",
				state.StorageGB.ValueInt64(), plan.StorageGB.ValueInt64()),
		)
		return
	case plan.StorageGB.ValueInt64() > state.StorageGB.ValueInt64():
		if err := r.resizeStorage(ctx, id, int(plan.StorageGB.ValueInt64()), budgets.Update); err != nil {
			resp.Diagnostics.AddError("Failed to resize PostgreSQL instance storage", err.Error())
			return
		}
	}

	// In-place field updates (name, backups, parameter group) via PUT. Skip the
	// call entirely when nothing PUT-able changed (e.g. a storage-only resize).
	if updateReq := plan.toUpdateRequest(&state); updateReq.hasChanges() {
		if _, err := r.client.Put(ctx, r.client.TenantPath("/databases/"+id), updateReq); err != nil {
			resp.Diagnostics.AddError("Failed to update PostgreSQL instance", err.Error())
			return
		}

		// Poll until instance is back to "running" after the update.
		if _, err := r.pollRunning(ctx, id, budgets.Update); err != nil {
			resp.Diagnostics.AddError("PostgreSQL instance failed to reach running state after update", err.Error())
			return
		}
	}

	// Refresh state from API.
	apiResp, err := r.client.Get(ctx, r.client.TenantPath("/databases/"+id), nil)
	if err != nil {
		resp.Diagnostics.AddError("Failed to read PostgreSQL instance after update", err.Error())
		return
	}

	inst, err := client.ParseResponse[apiPostgresInstance](apiResp)
	if err != nil {
		resp.Diagnostics.AddError("Failed to parse PostgreSQL instance response", err.Error())
		return
	}

	plan.fromAPI(ctx, inst, &resp.Diagnostics)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *postgresInstanceResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state PostgresInstanceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	id := state.ID.ValueString()

	_, err := r.client.Delete(ctx, r.client.TenantPath("/databases/"+id))
	if err != nil {
		if client.IsNotFound(err) {
			return
		}
		resp.Diagnostics.AddError("Failed to delete PostgreSQL instance", err.Error())
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
		ResourceName: "postgres_instance",
		PollFunc: func(pollCtx context.Context) (string, error) {
			pollResp, pollErr := r.client.Get(pollCtx, r.client.TenantPath("/databases/"+id), nil)
			if pollErr != nil {
				if client.IsNotFound(pollErr) {
					return "deleted", nil
				}
				return "", pollErr
			}
			current, parseErr := client.ParseResponse[apiPostgresInstance](pollResp)
			if parseErr != nil {
				return "", parseErr
			}
			return current.Status, nil
		},
	})
	if err != nil {
		resp.Diagnostics.AddError("PostgreSQL instance failed to delete", err.Error())
	}
}

func (r *postgresInstanceResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}

// UpgradeState migrates prior state across the HCL-surface rename:
//   - v0->v1: the attribute `flavor` was renamed to `flavor_id`. The wire tag was
//     always flavorId, so the migration is purely local: it copies the prior
//     `flavor` value into `flavor_id` and carries every other attribute through
//     unchanged. `flavor` is in-place updatable (not RequiresReplace), so without
//     this the first post-upgrade plan would show a spurious update rather than a
//     destroy; the upgrader keeps the upgrade a clean no-op.
func (r *postgresInstanceResource) UpgradeState(ctx context.Context) map[int64]resource.StateUpgrader {
	schemaResp := resource.SchemaResponse{}
	r.Schema(ctx, resource.SchemaRequest{}, &schemaResp)
	return map[int64]resource.StateUpgrader{
		0: stateupgrade.RenameStringAttr(ctx, schemaResp.Schema, "flavor", "flavor_id"),
	}
}
