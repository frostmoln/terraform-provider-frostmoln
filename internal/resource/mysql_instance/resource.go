package mysql_instance

import (
	"context"
	"fmt"
	"time"

	"github.com/hashicorp/terraform-plugin-framework-validators/boolvalidator"
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
	_ resource.Resource                 = &mysqlInstanceResource{}
	_ resource.ResourceWithImportState  = &mysqlInstanceResource{}
	_ resource.ResourceWithUpgradeState = &mysqlInstanceResource{}
)

// NewResource returns a new mysql_instance resource factory.
func NewResource() resource.Resource {
	return &mysqlInstanceResource{}
}

type mysqlInstanceResource struct {
	client       *client.Client
	pollInterval time.Duration
	pollTimeout  time.Duration
}

func (r *mysqlInstanceResource) getPollInterval() time.Duration {
	if r.pollInterval > 0 {
		return r.pollInterval
	}
	return 5 * time.Second
}

func (r *mysqlInstanceResource) getPollTimeout() time.Duration {
	if r.pollTimeout > 0 {
		return r.pollTimeout
	}
	return 15 * time.Minute
}

// resolveBudgets turns the configured timeouts block into effective budgets,
// falling back per verb to the same value this resource has always hardcoded
// (getPollTimeout's 15m). Routing the defaults through the accessor keeps the
// test-injection seam intact: a test that shrinks pollTimeout shrinks every
// wait that does not carry an explicit timeouts override, exactly as before.
func (r *mysqlInstanceResource) resolveBudgets(m *timeouts.Model) timeouts.Budgets {
	budgets, err := m.Resolve(timeouts.Uniform(r.getPollTimeout()))
	if err != nil {
		// Unreachable via HCL (the block validator rejects bad durations at
		// plan time); degrade to the defaults rather than fail a wait.
		return timeouts.Uniform(r.getPollTimeout())
	}
	return budgets
}

// pollRunning waits until the instance returns to "running" state. The wait
// budget is the timeouts block's update (or create, during Create) override.
func (r *mysqlInstanceResource) pollRunning(ctx context.Context, id string, budget time.Duration) (string, error) {
	return client.WaitForState(ctx, client.PollConfig{
		Interval:     r.getPollInterval(),
		Timeout:      budget,
		TargetStates: []string{"running"},
		ErrorStates:  []string{"error", "failed"},
		ResourceName: "mysql_instance",
		PollFunc: func(pollCtx context.Context) (string, error) {
			pollResp, pollErr := r.client.Get(pollCtx, r.client.TenantPath("/databases/"+id), nil)
			if pollErr != nil {
				return "", pollErr
			}
			current, parseErr := client.ParseResponse[apiMysqlInstance](pollResp)
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
func (r *mysqlInstanceResource) resizeStorage(ctx context.Context, id string, storageGB int, budget time.Duration) error {
	resp, err := r.client.PostWithConflictRetry(ctx, r.client.TenantPath("/databases/"+id+"/resize"), apiResizeMysqlInstanceRequest{StorageGB: storageGB}, client.IsTransientResizeConflict, r.getPollInterval(), r.getPollTimeout())
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
func (r *mysqlInstanceResource) awaitResize(ctx context.Context, id string, apiResp *client.Response, budget time.Duration) error {
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
			"still be running — check the instance status (portal, `fm db mysql instance list`) before retrying: %w", unknown)
	}
	if _, waitErr := r.client.WaitForOperation(ctx, op.OperationID, r.getPollInterval(), budget); waitErr != nil {
		return waitErr
	}
	_, runningErr := r.pollRunning(ctx, id, budget)
	return runningErr
}

func (r *mysqlInstanceResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_mysql_instance"
}

func (r *mysqlInstanceResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		// v1: the HCL attribute `flavor` was renamed to `flavor_id` to match the
		// flagship frostmoln_instance and the cache/messaging offers (the wire tag
		// was always flavorId). See UpgradeState for the v0→v1 migration.
		Version:     1,
		Description: "Manages a managed MySQL database instance in the Frostmoln platform." + "\n\n" + scopedecl.Summary("frostmoln_mysql_instance"),
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Description: "The unique identifier of the MySQL instance.",
				Computed:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"name": schema.StringAttribute{
				Description: "The name of the MySQL instance.",
				Required:    true,
			},
			"version": schema.StringAttribute{
				Description: "The MySQL version (e.g. \"8.0\", \"8.4\", \"9.2\").",
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
			// NOT SUPPORTED FOR MySQL. High availability needs a different mechanism end to
			// end here -- semi-synchronous replication or Group Replication, with different
			// promote and fence semantics -- so the API refuses type=mysql with haEnabled.
			//
			// The attribute is KEPT rather than removed: removing it turns every existing config
			// that carries it into an "Unsupported argument" error at parse time, which is a
			// harder break than a validator. The validator moves the refusal from apply time
			// (mid-graph, after the VPC and subnet in the same plan are already created) to plan
			// time, where it costs nothing.
			//
			// Note this DOES newly fail `terraform plan` for a config already carrying
			// ha_enabled = true -- a value the API accepted for months and the platform never
			// honoured. That is deliberate and is called out in the changelog.
			"ha_enabled": schema.BoolAttribute{
				Description: "Not supported for MySQL. High availability is available for " +
					"PostgreSQL only; setting this to true is rejected.",
				DeprecationMessage: "High availability is not available for MySQL. This attribute " +
					"is rejected when set to true and will be removed in a future major version.",
				Optional: true,
				Computed: true,
				Validators: []validator.Bool{
					// Equals(false), not None(): None is a COMPOSITION validator meaning "none of
					// these sub-validators may pass". What we want is a value constraint.
					boolvalidator.Equals(false),
				},
				PlanModifiers: []planmodifier.Bool{
					boolplanmodifier.UseStateForUnknown(),
					boolplanmodifier.RequiresReplace(),
				},
			},
			"ha_status": schema.StringAttribute{
				Description: "Availability state of the instance. Always \"disabled\" for MySQL, " +
					"which does not support high availability.",
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
				Description: "The current status of the MySQL instance.",
				Computed:    true,
			},
			"private_ip": schema.StringAttribute{
				Description: "The private IP address of the MySQL instance.",
				Computed:    true,
			},
			"port": schema.Int64Attribute{
				Description: "The port number the MySQL instance is listening on.",
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
				Description: "The admin username for the MySQL instance.",
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
			// resource has always hardcoded (15m per verb). A timeouts change
			// is an in-place no-op on real infrastructure — verified by the
			// Gate 2 smoke test (project-docs/product/TF-CONVERGENCE-WALL-PLAN.md).
			"timeouts": timeouts.Schema(),
		},
	}
}

func (r *mysqlInstanceResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (r *mysqlInstanceResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan MysqlInstanceModel
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
		resp.Diagnostics.AddError("Failed to create MySQL instance", err.Error())
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
			resp.Diagnostics.AddError("Failed to parse MySQL instance operation response", err.Error())
			return
		}
		// 🔴 STATE BEFORE THE WAIT — the postgres_instance precedent
		// (postgres_instance/resource.go) applied to mysql. MySQL's create 202
		// carries resourceId exactly like PostgreSQL's, but until now mysql set
		// state only AFTER the operation wait, so a wait that timed out orphaned
		// a running, billing instance with no id in state: unrefreshable,
		// undestroyable, unimportable, and the next apply either re-created it
		// or 409'd on the duplicate name.
		//
		// The operation poll cannot supply the id earlier: provisioning fills an
		// operation's ResourceID from the workflow RESULT, so a PENDING operation
		// has none. DEGRADES CLEANLY against a database service that sends no
		// resourceId: the branch is skipped and the behaviour is what it was.
		//
		// A failure to pre-record is NOT fatal and adds no diagnostic: the wait
		// below is the real work, and turning a best-effort bookkeeping read
		// into a create failure would trade a rare orphan for a common one.
		if op.ResourceID != "" {
			if earlyResp, earlyErr := r.client.Get(ctx,
				r.client.TenantPath("/databases/"+op.ResourceID), nil); earlyErr == nil {
				if earlyInst, parseErr := client.ParseResponse[apiMysqlInstance](earlyResp); parseErr == nil {
					early := plan
					early.fromAPI(ctx, earlyInst, &resp.Diagnostics)
					if !resp.Diagnostics.HasError() {
						resp.Diagnostics.Append(resp.State.Set(ctx, &early)...)
					}
				}
			}
			if resp.Diagnostics.HasError() {
				return
			}
		}

		done, err := r.client.WaitForOperation(ctx, op.OperationID, r.getPollInterval(), budgets.Create)
		if err != nil {
			resp.Diagnostics.AddError("MySQL instance creation failed", err.Error())
			return
		}
		instID = done.ResourceID
		if instID == "" {
			resp.Diagnostics.AddError(
				"MySQL instance operation returned no resource ID",
				"The create operation completed but returned no resource ID. The instance may "+
					"exist in the backend without being tracked in Terraform state.",
			)
			return
		}

		// Persist state immediately so the ID is tracked, even if the
		// poll-to-running or final read below fails. (The operation wait itself
		// is already covered above by the state-before-the-wait read; this
		// second wait runs against a tracked id, so a failure here leaves a
		// refreshable state row.)
		readResp, err := r.client.Get(ctx, r.client.TenantPath("/databases/"+instID), nil)
		if err != nil {
			resp.Diagnostics.AddError("Failed to read MySQL instance after creation", err.Error())
			return
		}
		inst, err := client.ParseResponse[apiMysqlInstance](readResp)
		if err != nil {
			resp.Diagnostics.AddError("Failed to parse MySQL instance response", err.Error())
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
		inst, err := client.ParseResponse[apiMysqlInstance](apiResp)
		if err != nil {
			resp.Diagnostics.AddError("Failed to parse MySQL instance response", err.Error())
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
		ResourceName: "mysql_instance",
		PollFunc: func(pollCtx context.Context) (string, error) {
			pollResp, pollErr := r.client.Get(pollCtx, r.client.TenantPath("/databases/"+instID), nil)
			if pollErr != nil {
				return "", pollErr
			}
			current, parseErr := client.ParseResponse[apiMysqlInstance](pollResp)
			if parseErr != nil {
				return "", parseErr
			}
			return current.Status, nil
		},
	})
	if err != nil {
		resp.Diagnostics.AddError("MySQL instance failed to reach running state", err.Error())
		return
	}

	// Refresh state after polling completes to get final status, IPs, etc.
	readResp, err := r.client.Get(ctx, r.client.TenantPath("/databases/"+instID), nil)
	if err != nil {
		resp.Diagnostics.AddError("Failed to read MySQL instance after creation", err.Error())
		return
	}
	finalInst, err := client.ParseResponse[apiMysqlInstance](readResp)
	if err != nil {
		resp.Diagnostics.AddError("Failed to parse MySQL instance response", err.Error())
		return
	}

	plan.fromAPI(ctx, finalInst, &resp.Diagnostics)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *mysqlInstanceResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state MysqlInstanceModel
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
		resp.Diagnostics.AddError("Failed to read MySQL instance", err.Error())
		return
	}

	inst, err := client.ParseResponse[apiMysqlInstance](apiResp)
	if err != nil {
		resp.Diagnostics.AddError("Failed to parse MySQL instance response", err.Error())
		return
	}

	state.fromAPI(ctx, inst, &resp.Diagnostics)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *mysqlInstanceResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan MysqlInstanceModel
	var state MysqlInstanceModel
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
	// The customer's timeouts block, with defaults identical to the values
	// this resource has always hardcoded.
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
			resp.Diagnostics.AddError("Failed to resize MySQL instance storage", err.Error())
			return
		}
	}

	// In-place field updates (name, backups, parameter group) via PUT. Skip the
	// call entirely when nothing PUT-able changed (e.g. a storage-only resize).
	if updateReq := plan.toUpdateRequest(&state); updateReq.hasChanges() {
		if _, err := r.client.Put(ctx, r.client.TenantPath("/databases/"+id), updateReq); err != nil {
			resp.Diagnostics.AddError("Failed to update MySQL instance", err.Error())
			return
		}

		// Poll until instance is back to "running" after the update.
		if _, err := r.pollRunning(ctx, id, budgets.Update); err != nil {
			resp.Diagnostics.AddError("MySQL instance failed to reach running state after update", err.Error())
			return
		}
	}

	// Refresh state from API.
	apiResp, err := r.client.Get(ctx, r.client.TenantPath("/databases/"+id), nil)
	if err != nil {
		resp.Diagnostics.AddError("Failed to read MySQL instance after update", err.Error())
		return
	}

	inst, err := client.ParseResponse[apiMysqlInstance](apiResp)
	if err != nil {
		resp.Diagnostics.AddError("Failed to parse MySQL instance response", err.Error())
		return
	}

	plan.fromAPI(ctx, inst, &resp.Diagnostics)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *mysqlInstanceResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state MysqlInstanceModel
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
		resp.Diagnostics.AddError("Failed to delete MySQL instance", err.Error())
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
		ResourceName: "mysql_instance",
		PollFunc: func(pollCtx context.Context) (string, error) {
			pollResp, pollErr := r.client.Get(pollCtx, r.client.TenantPath("/databases/"+id), nil)
			if pollErr != nil {
				if client.IsNotFound(pollErr) {
					return "deleted", nil
				}
				return "", pollErr
			}
			current, parseErr := client.ParseResponse[apiMysqlInstance](pollResp)
			if parseErr != nil {
				return "", parseErr
			}
			return current.Status, nil
		},
	})
	if err != nil {
		resp.Diagnostics.AddError("MySQL instance failed to delete", err.Error())
	}
}

func (r *mysqlInstanceResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}

// UpgradeState migrates prior state across the HCL-surface rename:
//   - v0→v1: the attribute `flavor` was renamed to `flavor_id`. The wire tag was
//     always flavorId, so the migration is purely local: it copies the prior
//     `flavor` value into `flavor_id` and carries every other attribute through
//     unchanged. `flavor` is in-place updatable (not RequiresReplace), so without
//     this the first post-upgrade plan would show a spurious update rather than a
//     destroy; the upgrader keeps the upgrade a clean no-op.
func (r *mysqlInstanceResource) UpgradeState(ctx context.Context) map[int64]resource.StateUpgrader {
	schemaResp := resource.SchemaResponse{}
	r.Schema(ctx, resource.SchemaRequest{}, &schemaResp)
	return map[int64]resource.StateUpgrader{
		0: stateupgrade.RenameStringAttr(ctx, schemaResp.Schema, "flavor", "flavor_id"),
	}
}
