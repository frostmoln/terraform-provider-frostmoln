package postgres_instance

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/boolplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/objectplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/setplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/planmod"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/scopedecl"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/stateupgrade"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/timeouts"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/unenacted"
)

var (
	_ resource.Resource                 = &postgresInstanceResource{}
	_ resource.ResourceWithImportState  = &postgresInstanceResource{}
	_ resource.ResourceWithUpgradeState = &postgresInstanceResource{}
	_ resource.ResourceWithModifyPlan   = &postgresInstanceResource{}
)

// NewResource returns a new postgres_instance resource factory.
func NewResource() resource.Resource {
	return &postgresInstanceResource{}
}

// CLASS A (the 2026-09 convergence audit, finding A1): the database service
// stores an instance's parameter_group_id and echoes it back, and nothing
// applies it — the apply endpoint exists as route + handler but its
// implementation is a stub that logs "parameter apply initiated" and returns
// (database internal/service/impl/parameter.go:130-144), and zero
// provisioning files touch ParameterGroup (the same grep hits 20 files inside
// the database repo, so the check discriminates). A value set here survives
// create and update untouched by the running server on every surface — API,
// portal, fm, this provider. Refusing at plan (with a belt in
// Create and Update, because a configuration value that resolves only at
// apply slips past config validation) is the honest shape while the platform
// has no working apply path; RequiresReplace would merely destroy a live
// instance and re-create the same never-applied promise.
const parameterGroupRefusalTitle = "parameter_group_id is stored, never applied"

const parameterGroupRefusalDetail = parameterGroupRefusalTitle +
	": a managed PostgreSQL instance records the parameter group reference, and nothing in " +
	"the platform applies it — the database service has no working parameter-apply workflow, so the " +
	"group's values never reach the running server on create or on update. The same inert reference is " +
	"reported by the portal and fm; Terraform refuses the attribute rather than report success for a " +
	"change it does not make.\n\n" +
	"Leave the attribute unset and manage parameter groups with the platform's parameter-group APIs " +
	"until a working apply path ships."

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
// schedule, when non-nil, rides ALONG WITH the resize rather than in a
// following PUT — on a point-in-time-recovery instance the platform re-checks
// the schedule against the new size's base-backup floor and refuses the resize
// outright if a customer-written one no longer passes, so a separate PUT would
// arrive after the failure it was meant to prevent.
func (r *postgresInstanceResource) resizeStorage(ctx context.Context, id string, storageGB int, schedule *string, budget time.Duration) error {
	body := apiResizePostgresInstanceRequest{StorageGB: storageGB, BackupSchedule: schedule}
	resp, err := r.client.PostWithConflictRetry(ctx, r.client.TenantPath("/databases/"+id+"/resize"), body, client.IsTransientResizeConflict, r.getPollInterval(), r.getPollTimeout())
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
			// backup_schedule and backup_retention_days are server-defaulted (ADR-0085: a
			// size-aware schedule the platform picks, and the 35-day object-lock floor) but
			// ONLY when backups are enabled --
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
			// RETENTION pins the server's value with planmod.Int64UseStateOrDefault: an
			// omitted attribute keeps whatever the instance already has, and a null prior
			// state plans the ADR-0085 floor, which is what a NULL column already MEANS to
			// the reaper (GREATEST(COALESCE(backup_retention_days, 35), 35)).
			//
			// THE SCHEDULE NO LONGER DOES, and this paragraph used to say it did. It read
			// that the null-state arm "heals a legacy row left at (backup_enabled = true,
			// schedule NULL)". Two things about that are now wrong. The platform's schedule
			// default became SIZE-AWARE, so there is no single literal to plan (see the
			// paragraph on backup_schedule below); and that legacy row is no longer
			// representable — ADR-0085 gave every offer a CHECK forbidding
			// (backup_enabled AND btrim(backup_schedule) = '') plus a one-time backfill
			// (database migration 000024), so the population this healed is empty and the
			// database, not this provider, is what keeps it empty.
			//
			// THE SCHEDULE DEFAULT IS THE SERVER'S TO PICK, AND IT IS SIZE-AWARE.
			// This used to pin `planmod.StringUseStateOrDefault("0 2 * * *")`,
			// which planned that literal whenever the config was null — and the
			// platform no longer has one schedule default. A
			// point-in-time-recovery instance's scheduled backup is a full base
			// backup, so the platform enforces a minimum interval that grows
			// with the volume and picks daily, weekly or twice-monthly
			// accordingly. Planning the daily literal against an instance the
			// platform schedules weekly is an "inconsistent result after apply"
			// on the create, and on the resize that moves an instance across a
			// threshold. The thresholds themselves are NEVER copied into this
			// provider: the platform owns the ladder and its refusal names the
			// floor for the size (the rule ADR-0015 sets for prices).
			//
			// It is REPLACED BY UseStateForUnknown, not by nothing.
			// MarkComputedNilsAsUnknown keys ONLY on the config value, so a
			// Computed attribute whose config is null is planned unknown on
			// every changing plan regardless of what state holds — with no
			// modifier at all, an omitted schedule went unknown on a plan whose
			// only change was the name, `toUpdateRequest` then skipped it as
			// unknown, and the enable PUT went out as
			// {"backupEnabled":true,"backupRetentionDays":35} with no schedule.
			// UseStateForUnknown pins a RECORDED value back (so an enable still
			// carries the schedule the instance already has) and bails on a null
			// prior state, leaving unknown exactly where the platform must
			// choose: a create, and a legacy row whose column was never written.
			// ModifyPlan adds the one case neither can see — a resize, where the
			// pinned value is about to become stale.
			//
			// NOT planmod.StringUseStateOrDefault: that substitutes a literal
			// for a null state, which is the ladder-duplicating behaviour above.
			"backup_schedule": schema.StringAttribute{
				Description: "Cron expression for the backup schedule. When omitted, the platform picks one for the instance's storage size and reports it back, so the value shows as \"known after apply\" on create and on any `storage_gb` change. Larger volumes need longer between scheduled backups; the platform owns that rule and its refusal names the floor for your size.",
				Optional:    true,
				Computed:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
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
			"pitr_enabled": schema.BoolAttribute{
				Description: "Whether point-in-time recovery is enabled: the platform continuously archives write-ahead log segments so the database can be restored to any second inside its restorable window, onto a NEW instance. PostgreSQL only, requires `backup_enabled`, and not available on highly-available instances.\n\n" +
					"    SET IT EXPLICITLY TO GET IT. The platform stamps point-in-time recovery at CREATE and the stamp is permanent: an instance created without it can never be given it, and the only remedy is to create another instance (restoring this one produces a capable target, see `restore_from`). Terraform therefore sends this field only when your configuration writes it — omit it and you get whatever the platform defaults to, which today is OFF. Write `pitr_enabled = true` on create if you want the feature.\n\n" +
					"    It can be turned off and on again in place on an instance that is capable of it (`pitr_capable`). Turning it off stops restores to earlier times; the backups already retained are kept, and billed, until their retention ends. Turning it back on within 24 hours of turning it (or `backup_enabled`) off is refused, and the window restarts from the next base backup rather than resuming.",
				Optional: true,
				Computed: true,
				PlanModifiers: []planmodifier.Bool{
					boolplanmodifier.UseStateForUnknown(),
				},
			},
			// The three attributes below and pitr_capable are Computed with NO
			// UseStateForUnknown, and ModifyPlan re-plans them unknown on any
			// apply. They are not slow-moving facts about the instance: it is
			// the platform's job to move them, continuously, while nobody is
			// applying anything. latest_restorable_time advances with every
			// archived segment (a busy database, every few minutes; an idle
			// one, on a ~10-minute liveness proof), earliest_restorable_time
			// advances as write-ahead log is reaped and as new base backups
			// supersede older ones, and pitr_archive_paused_reason appears and
			// clears on its own. Pinning any of them to state would plan a
			// value that is already stale by the time the apply reads it back,
			// which Terraform reports as "Provider produced inconsistent result
			// after apply" — an apply that fails for no reason but the clock.
			"pitr_capable": schema.BoolAttribute{
				Description: "Whether this instance CAN do point-in-time recovery, as opposed to whether it currently does (`pitr_enabled`). Fixed when the instance is created and never changes afterwards, so it is what tells \"turned off\" apart from \"cannot be turned on\". Shows as \"known after apply\" whenever anything else on the instance changes.",
				Computed:    true,
			},
			"pitr_archive_paused_reason": schema.StringAttribute{
				Description: "Why the platform has paused write-ahead log archiving for this instance, null when it is not paused. `tenant_cap` means the tenant's retained point-in-time-recovery storage has reached its cap — the window is broken until the next SCHEDULED base backup re-forms it, which on a weekly schedule can be up to a week. `platform_disabled` means the platform has archiving switched off fleet-wide. Any other value means archiving is paused for a reason this provider release does not know about. Read from the instance GET only, and shows as \"known after apply\" whenever anything else on the instance changes.",
				Computed:    true,
			},
			"earliest_restorable_time": schema.StringAttribute{
				Description: "The earliest instant this instance can be restored to (RFC 3339, UTC), null when there is no restorable window right now — point-in-time recovery is off, or the first base backup has not finished, or a gap restarted the window. Read from the instance GET only, and shows as \"known after apply\" whenever anything else on the instance changes, because the platform moves it as write-ahead log is reaped.",
				Computed:    true,
			},
			"latest_restorable_time": schema.StringAttribute{
				Description: "The latest instant this instance can be restored to (RFC 3339, UTC), null when there is no restorable window right now. It advances continuously while archiving runs, so it is ALWAYS \"known after apply\" on a changing instance and is a snapshot, never a promise: quote it, then restore against the platform's own check, which refuses a target outside the window and names both bounds. Read from the instance GET only.",
				Computed:    true,
			},
			"parameter_group_id": schema.StringAttribute{
				Description: "The ID of the parameter group to reference on the instance. The platform stores the reference but never applies it — its parameter-apply endpoint is an unimplemented stub, so the group's values never reach the running server on create or on update. Setting it is refused at plan time, with the constraint and remedy in the refusal terraform prints; leave it unset until the platform ships a working apply path.",
				Optional:    true,
				Validators: []validator.String{
					unenacted.String(parameterGroupRefusalTitle, parameterGroupRefusalDetail),
				},
			},
			"extensions": schema.SetAttribute{
				Description: "The set of PostgreSQL extension catalog names this instance has enabled (e.g. \"timescaledb\", \"pg_stat_statements\"). Names are PLATFORM CATALOG VALUES, never free-strings: only what the platform's extension catalog advertises can be enabled, and every name is checked against that live catalog at plan time — an unknown name, a name the platform does not offer for this instance's PostgreSQL version, or a deprecated name is refused at plan with the platform's own typed wording. When the catalog cannot be read at plan time the plan only WARNS and continues; the platform enforces the same check when the request is made and refuses with its typed wording. Enabling or disabling requires the database-extensions entitlement on the tenant (without it the platform refuses with feature_not_enabled) and is applied IN PLACE — the cost is per-extension, not per-operation: an extension that requires preload (timescaledb and pg_stat_statements in the catalog today) pays a restart window and the instance is unavailable while that apply runs; the apply for the rest is in place, with no restart. Highly available instances are refused by the platform until coordinated node-restart sequencing ships. Because the platform runs one operation at a time per instance, Terraform applies the diff SERIALIZED: the enables first, then the disables, each waited to its recorded verdict before the next starts; a plan with both waits twice. The platform records outcomes once, at the end: while an enable is running the instance's extension state shows the asked-for revision with no new entries, and a failed enable is recorded with the platform's reason. An enable or disable made outside Terraform (portal, fm) is refreshed into this attribute and removed or re-added on the next apply as ordinary configuration drift. Omitting the attribute keeps whatever the instance has; setting it to an explicit empty set (`[]`) disables every extension, one restart window for each preload-required one (the rest disable in place). Extensions are a PostgreSQL offer; MySQL instances have no extensions.",
				Optional:    true,
				Computed:    true,
				ElementType: types.StringType,
				PlanModifiers: []planmodifier.Set{
					setplanmodifier.UseStateForUnknown(),
				},
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
			// CREATE-ONLY, AND A CHANGE TO IT DESTROYS A DATABASE. Everything
			// unusual about this attribute follows from that.
			//
			// DELIBERATELY NOT Computed, and that is the whole design note.
			// Computed looks right — "an omitted value keeps what state
			// records" — and it is what the first draft of this did. But
			// MarkComputedNilsAsUnknown (framework v1.19.0) keys ONLY on the
			// config value: a Computed attribute with a null config is planned
			// UNKNOWN no matter what the prior state holds, and it has no
			// Default to exempt it. Two things then follow, both verified
			// against a real `terraform apply`:
			//
			//   - nothing can ever resolve that unknown, because the platform
			//     does not report how an instance came to exist, so `fromAPI`
			//     has nothing to write. Every ordinary create returned a state
			//     containing an unknown, which Terraform refuses outright
			//     ("All values must be known after apply") AFTER the database
			//     exists — tainting it, so the next apply destroys it;
			//   - and with a block recorded in state and absent from config,
			//     the proposed value is computed as null, so the marking pass
			//     fires over the WHOLE object on every plan. Every other
			//     Computed attribute went to "known after apply", the apply
			//     wrote the same values back, and the next plan showed the
			//     identical diff. Permanent, never converging.
			//
			// Optional alone has neither problem: the planned value is the
			// config value, always known.
			//
			// THE COST, STATED HONESTLY: removing the argument is then a single
			// in-place update that clears the record, not the no-op the §4.15
			// contract asked for. That no-op IS reachable — keep Computed, drop
			// UseStateForUnknown, and pin the prior value in ModifyPlan, which
			// runs after the marking pass and so overwrites the unknown. It is
			// Computed that buys no-op-on-removal, and only Computed: core
			// refuses to plan a non-computed attribute to anything but its
			// config value.
			//
			// This resource chooses the clearing update anyway. An immortal
			// record that no configuration change can remove — only
			// `terraform state rm` — is worse than a benign one-time diff that
			// converges, for a field the platform never reports and nothing
			// reads back. §4.15's bullet is amended to record the deviation.
			//
			// Import is unaffected either way: the API returns no such field, so
			// an imported instance records null and plans nothing.
			//
			// RequiresReplaceIf, not RequiresReplace, and it fires only when
			// BOTH the recorded value and the configured one are present and
			// differ. Plain RequiresReplace would compare a null against a
			// recorded block and destroy the restored database the first time
			// anyone tidied the configuration.
			"restore_from": schema.SingleNestedAttribute{
				Description: "Create this instance by restoring another one, instead of creating an empty database. The platform provisions a NEW instance from the source's backups; the source is untouched.\n\n" +
					"    The platform builds the target from the source's own shape, so the source must be a PostgreSQL instance and this resource's `version`, `flavor_id`, `vpc_id` and `subnet_id` must equal the source's. Terraform reads the source first and refuses with the mismatch named, rather than quietly creating a plain empty database instead. `storage_gb` may be larger than the source's and is grown in place after the restore; it may not be smaller.\n\n" +
					"    Create-only, and it records what Terraform asked for rather than anything the platform reports — there is no API field that says how an instance came to exist. Removing it afterwards clears that record in a single in-place update that changes nothing on the platform, and the plan then stays clean. Importing a restored instance records nothing, and plans nothing. Changing it to a DIFFERENT source or instant REPLACES this resource, which destroys the database it created — treat it as you would `vpc_id`. Adding it to an instance that already exists is refused: an instance cannot be un-created into a restore.",
				Optional: true,
				PlanModifiers: []planmodifier.Object{
					objectplanmodifier.RequiresReplaceIf(restoreSourceChanged,
						"replaces the instance when restore_from is changed from one non-null value to another",
						"replaces the instance when `restore_from` is changed from one non-null value to another"),
				},
				Attributes: map[string]schema.Attribute{
					"source_instance_id": schema.StringAttribute{
						Description: "The PostgreSQL instance to restore FROM. It must exist and be visible to this tenant; a source that answers 404 fails the create.",
						Required:    true,
						Validators: []validator.String{
							sourceInstanceIDValidator{},
						},
					},
					"point_in_time": schema.StringAttribute{
						Description: "Restore to this instant (RFC 3339, e.g. `2026-09-20T14:30:00Z`) instead of to a named backup. It must fall inside the source's restorable window — read `earliest_restorable_time` and `latest_restorable_time` off the source and note that the later bound advances continuously, so the platform, not this provider, is what checks it. Requires point-in-time recovery on the source.",
						Optional:    true,
						Validators: []validator.String{
							stringvalidator.ExactlyOneOf(path.MatchRelative().AtParent().AtName("backup_id")),
							rfc3339Validator{},
						},
					},
					"backup_id": schema.StringAttribute{
						Description: "Restore from this backup. It must be a backup of the source instance and must not be a `base` backup — base backups exist to serve point-in-time restores and the platform refuses one named here; use `point_in_time` instead.",
						Optional:    true,
					},
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

// ModifyPlan is where the declared `extensions` set meets the LIVE platform
// catalog: every declared name is checked against the catalog rows the
// platform serves right now (drift-safe by construction — no check, allowlist
// or copy of catalog rows is ever embedded in the provider). An unknown name,
// a name the catalog does not offer for this instance's PostgreSQL major, or
// a deprecated name is refused HERE, before anything is created or changed —
// including on the create half of a replacement, which Terraform plans as a
// separate PlanResourceChange with a null prior state. The platform re-checks
// everything at execution time, fail-closed.
func (r *postgresInstanceResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	// Destroy: no plan to judge.
	if req.Plan.Raw.IsNull() {
		return
	}
	var plan PostgresInstanceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(r.checkPlanExtensions(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	planPlatformRecomputedAttributes(ctx, req, resp)
}

// restoreSourceChanged is `restore_from`'s RequiresReplaceIf: a replacement is
// recorded ONLY when the instance already records a restore and the
// configuration now asks for a DIFFERENT one. Both of the other transitions are
// deliberately no-ops, and both would otherwise destroy a live database:
//
//   - config null against a recorded value — the practitioner tidied the block
//     away after the restore landed, or imported an instance the platform never
//     reports a restore for;
//   - a recorded null against a configured value — an instance that already
//     exists cannot be un-created into a restore, and the only honest answer is
//     to leave it alone. (Terraform would otherwise replace on the first apply
//     after someone adds the block to an existing resource by mistake.)
func restoreSourceChanged(_ context.Context, req planmodifier.ObjectRequest, resp *objectplanmodifier.RequiresReplaceIfFuncResponse) {
	if req.StateValue.IsNull() || req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	resp.RequiresReplace = !req.StateValue.Equal(req.ConfigValue)
}

// platformRecomputedAttributes are the attributes the PLATFORM moves on its own
// while nobody is applying anything, so a plan may never carry their prior value
// into an apply that is going to read a newer one back.
//
// Terraform's own default is the opposite: the proposed new state copies the
// prior value for a Computed attribute, the framework only marks NULL ones
// unknown, and a known planned value that the apply reads back differently is
// "Provider produced inconsistent result after apply" — a hard apply failure.
// For latest_restorable_time that is not an edge case, it is the normal case:
// it advances with every archived write-ahead log segment, so ANY apply that
// takes more than a moment would fail on the clock alone.
//
// backup_schedule joins them only on a storage_gb change, and for a different
// reason: the platform's minimum interval between scheduled backups grows with
// the volume, so a resize across a threshold makes the platform re-pick the
// schedule it chose for the old size. It is left pinned to state otherwise,
// which is what keeps an omitted attribute from rewriting a schedule the
// customer wrote.
var platformRecomputedAttributes = []string{
	"pitr_archive_paused_reason",
	"earliest_restorable_time",
	"latest_restorable_time",
}

// planPlatformRecomputedAttributes re-plans those attributes as unknown on any
// apply that is actually going to do something. A plan that changes nothing is
// left alone: Terraform runs no apply for it, so nothing can come back
// different, and marking them unknown there would invent a diff on every
// `terraform plan`.
//
// BELT AND BRACES over the framework's own pass, not a substitute for it:
// MarkComputedNilsAsUnknown already marks a Computed attribute with a null
// config unknown under the same "the plan differs from the prior state"
// condition, so for the four read-only attributes this repeats what the
// framework does. It is kept because it is what the unit test can drive — the
// framework's pass runs in PlanResourceChange, which a ModifyPlan test does not
// go through — and because the restore-create and resize arms below are NOT
// redundant: neither is something the framework can see.
func planPlatformRecomputedAttributes(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	// Destroy: nothing to compute.
	if req.Plan.Raw.IsNull() {
		return
	}

	// CREATE. The framework already plans every Computed attribute unknown, so
	// the PITR attributes need nothing — but a RESTORE has one case the
	// framework cannot see. backup_retention_days plans its documented default
	// on a create with no prior state, and a restore INHERITS the source's
	// retention instead. Planning 35 against a source retaining 90 days would
	// make the apply PUT the shorter window over a database whose backups the
	// customer chose to keep for longer, and make the reaper eligible to delete
	// everything past 35 days on its next tick. Unknown lets the inheritance
	// stand, and an explicitly configured value still wins, because a non-null
	// config value is never re-planned here.
	if req.State.Raw.IsNull() {
		var cfgRestore types.Object
		resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("restore_from"), &cfgRestore)...)
		if resp.Diagnostics.HasError() || decodeRestoreFrom(cfgRestore) == nil {
			return
		}
		for _, name := range []string{"backup_schedule", "backup_retention_days"} {
			var cfgValue attr.Value
			resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root(name), &cfgValue)...)
			if resp.Diagnostics.HasError() || !cfgValue.IsNull() {
				continue
			}
			if name == "backup_schedule" {
				resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root(name), types.StringUnknown())...)
			} else {
				resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root(name), types.Int64Unknown())...)
			}
		}
		return
	}

	// UPDATE. An instance cannot be un-created into a restore, so a block that
	// appears on one that already exists is refused HERE rather than recorded.
	//
	// Without this the plan is an ordinary in-place update, Update ignores
	// restore_from entirely (there is no restore path in Update), and state
	// then claims a restore that never happened. The damage is on the apply
	// AFTER that one: restore_from now holds a non-null value, so editing it
	// fires RequiresReplaceIf and Terraform destroys a live database to
	// "re-restore" it from a block that never did anything. A typo'd or
	// copy-pasted block is enough. The same reasoning
	// frostmoln_security_group's delete_default_egress uses for its plan-time
	// warning applies, only here the silent outcome is destructive.
	var cfgRestore, stateRestore types.Object
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("restore_from"), &cfgRestore)...)
	resp.Diagnostics.Append(req.State.GetAttribute(ctx, path.Root("restore_from"), &stateRestore)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if !cfgRestore.IsNull() && !cfgRestore.IsUnknown() && stateRestore.IsNull() {
		resp.Diagnostics.AddAttributeWarning(path.Root("restore_from"), restoreFromAddedTitle, restoreFromAddedDetail)
	}

	// A plan that changes nothing runs no apply, so nothing can come back
	// different; marking anything unknown there would invent a diff on every
	// `terraform plan`.
	if req.Plan.Raw.Equal(req.State.Raw) {
		return
	}

	for _, name := range platformRecomputedAttributes {
		resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root(name), types.StringUnknown())...)
	}
	// pitr_capable is stamped at create and the platform never changes it
	// afterwards, but an apply is where a pre-P8 deployment's silence would
	// first be read back, so it is re-planned with the rest rather than pinned
	// to a value nothing promised to keep.
	resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("pitr_capable"), types.BoolUnknown())...)

	// backup_schedule: unknown ONLY when the storage size is moving. The
	// platform's minimum interval between scheduled backups grows with the
	// volume, so a resize can make it re-pick the schedule it chose for the old
	// size — but only for a schedule the platform chose. A schedule the
	// customer wrote is in the config, and a non-null config value is never
	// re-planned here.
	var cfgSchedule types.String
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("backup_schedule"), &cfgSchedule)...)
	var planStorage, stateStorage types.Int64
	resp.Diagnostics.Append(req.Plan.GetAttribute(ctx, path.Root("storage_gb"), &planStorage)...)
	resp.Diagnostics.Append(req.State.GetAttribute(ctx, path.Root("storage_gb"), &stateStorage)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if cfgSchedule.IsNull() && !planStorage.Equal(stateStorage) {
		resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("backup_schedule"), types.StringUnknown())...)
	}
}

// validSourceInstanceID refuses an id that cannot safely be ONE path segment.
//
// urlPathEscapeSegments is url.PathEscape, which leaves "." and ".." intact,
// and the client joins with path.Join, which CLEANS — so ".." addresses a
// DIFFERENT resource than the one named, and "" addresses the LIST endpoint
// rather than an instance. Neither currently gets past the type check further
// down, but a guard that depends on a later check for its safety is not a
// guard. The same shape is carried by the postgres_instance DATA SOURCE
// (internal/datasource/postgres_instance/datasource.go), vpc_routes and
// security_group_rules; it is duplicated rather than shared because that is
// how the other three carry it.
func validSourceInstanceID(id string) error {
	if id == "" {
		return fmt.Errorf("a source instance ID is required")
	}
	if id == "." || id == ".." || strings.ContainsAny(id, `/\?#%`) {
		return fmt.Errorf("invalid source instance ID %q", id)
	}
	return nil
}

// sourceInstanceIDValidator carries validSourceInstanceID into plan time, so an
// unusable id fails the configuration before any request is built.
type sourceInstanceIDValidator struct{}

func (sourceInstanceIDValidator) Description(_ context.Context) string {
	return "value must be a usable database instance ID: non-empty, and a single URL path segment"
}

func (v sourceInstanceIDValidator) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}

func (sourceInstanceIDValidator) ValidateString(_ context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	if err := validSourceInstanceID(req.ConfigValue.ValueString()); err != nil {
		resp.Diagnostics.AddAttributeError(req.Path, "Invalid restore source instance ID",
			fmt.Sprintf("%s: an instance ID must be non-empty and must not contain a '/', a backslash, "+
				"'?', '#' or '%%', so it can only ever address the one instance named.", err.Error()))
	}
}

// rfc3339Validator refuses a point_in_time the platform could not parse, at
// plan time rather than after the apply has already created something.
type rfc3339Validator struct{}

func (rfc3339Validator) Description(_ context.Context) string {
	return "value must be an RFC 3339 timestamp, e.g. 2026-09-20T14:30:00Z"
}

func (v rfc3339Validator) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}

func (rfc3339Validator) ValidateString(_ context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	if _, err := time.Parse(time.RFC3339, req.ConfigValue.ValueString()); err != nil {
		resp.Diagnostics.AddAttributeError(req.Path, "Invalid point_in_time",
			fmt.Sprintf("%q is not an RFC 3339 timestamp: %s\n\nUse a form the platform parses, such as "+
				"2026-09-20T14:30:00Z or 2026-09-20T16:30:00+02:00. Copy the bounds from the source "+
				"instance's earliest_restorable_time and latest_restorable_time.",
				req.ConfigValue.ValueString(), err.Error()))
	}
}

// readExtensionsIntoModel refreshes the `extensions` attribute from the
// instance's recorded out-of-band ledger, so an enable or disable made in the
// portal or with fm surfaces at plan as ordinary configuration drift.
//
// A 404 on THIS read is treated the way the create-202 treats an older
// backend: as "this deployment predates (or misroutes) the extension route" —
// the attribute is left exactly as state has it. IsNotFound's flat-envelope
// strictness is deliberately NOT used here: that predicate protects reads
// whose 404 could be an instance-deletion verdict, but the instance body was
// just served on the same path — a 404 on the extension read can only be
// route-shaped (pre-feature service, misroute), and for every possible
// meaning of that, keeping the recorded state is the safe degradation. Every
// non-404 failure is a real read failure.
func (r *postgresInstanceResource) readExtensionsIntoModel(ctx context.Context, id string, m *PostgresInstanceModel, diags *diag.Diagnostics) {
	state, err := r.fetchExtensionState(ctx, urlPathEscapeSegments(id))
	if err != nil {
		var apiErr *client.APIError
		if errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusNotFound {
			return
		}
		// Fail-closed on purpose: this read feeds a DIFF surface, and
		// fabricating a blank or stale set here could plan removals. The
		// asymmetry with the catalog read's warn-degrade is deliberate —
		// that one is advisory and re-verified at enqueue; this one drives
		// what the next apply will disable.
		diags.AddError("Failed to read PostgreSQL extension state", err.Error())
		return
	}
	names := enabledExtensionNames(state)
	set, sdiags := types.SetValueFrom(ctx, types.StringType, names)
	diags.Append(sdiags...)
	if !diags.HasError() {
		m.Extensions = set
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

	// Belt for the plan-time unenacted validator (see the constants above): a
	// configuration value that resolves only at apply is still unknown when
	// the plan is validated, and Create is the last honest word before this
	// attribute would be POSTed. By apply every value is known.
	if !plan.ParameterGroupID.IsNull() {
		resp.Diagnostics.AddAttributeError(
			path.Root("parameter_group_id"), parameterGroupRefusalTitle, parameterGroupRefusalDetail,
		)
		return
	}

	// The CONFIGURATION, not the plan. `pitr_enabled` and `restore_from` are
	// both Optional+Computed, so the plan carries a value for each whether or
	// not the practitioner wrote one — and both rules below turn on what they
	// actually wrote, not on what the plan resolved to.
	var cfg PostgresInstanceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// The customer's timeouts block, with defaults identical to the values
	// this resource has always hardcoded.
	budgets := r.resolveBudgets(plan.Timeouts)

	if rf := decodeRestoreFrom(cfg.RestoreFrom); rf != nil {
		r.createFromRestore(ctx, rf, &plan, cfg, budgets, resp)
		return
	}

	apiReq := plan.toCreateRequest(ctx, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}

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

	r.finishCreate(ctx, instID, &plan, cfg, budgets, resp, resp.Diagnostics.AddError, false)
}

// finishCreate is the tail every create shares: wait for the instance to reach
// running, read its true state back by GET, enable the declared extensions, and
// record what the platform says.
//
// `fail` is how this tail reports a problem, and it is NOT always AddError.
// After a RESTORE the instance holds the customer's recovered data, and an
// error returned from Create leaves Terraform holding a TAINTED resource, which
// the next apply destroys and re-creates. Destroying a restored database to
// resolve a bookkeeping failure is the one outcome this path must never
// produce, so the restore caller passes a warning reporter instead and the
// resource stays in state with whatever the platform really has.
func (r *postgresInstanceResource) finishCreate(
	ctx context.Context,
	instID string,
	plan *PostgresInstanceModel,
	cfg PostgresInstanceModel,
	budgets timeouts.Budgets,
	resp *resource.CreateResponse,
	fail func(summary, detail string),
	preservePlanned bool,
) {
	// What the PLAN promised, kept before anything below refreshes the model
	// from the platform. See keepPlannedValues: a create that records a value
	// the plan did not promise is an apply ERROR, whatever this function's own
	// diagnostics say.
	planned := *plan

	// Poll until instance reaches "running" status.
	if _, err := client.WaitForState(ctx, client.PollConfig{
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
	}); err != nil {
		fail("PostgreSQL instance failed to reach running state", err.Error())
		return
	}

	// Refresh state after polling completes to get final status, IPs, etc.
	// This GET is also the ONLY place the restorable window and the archive
	// pause reason can come from: the create response does not carry them.
	finalInst, err := r.getInstance(ctx, instID)
	if err != nil {
		fail("Failed to read PostgreSQL instance after creation", err.Error())
		return
	}

	plan.fromAPI(ctx, finalInst, &resp.Diagnostics)

	// The declared set, applied to the freshly-running instance: enables
	// only — a new instance has no recorded extensions to disable. An
	// extension failure here does NOT fail the create: the instance exists,
	// and an errored create would TAINT it, i.e. destroy and re-create a live
	// database to retry what the next apply retries in place.
	//
	// WHAT STATE THEN RECORDS DEPENDS ON THE PATH, and the old wording here
	// ("the recorded ledger is what state keeps, so the next plan still shows
	// the remainder as a diff") was only ever true for an UNWRITTEN set:
	//   - `extensions` omitted → planned unknown → the recorded ledger is kept
	//     on both paths, the next plan shows the remainder, the next apply
	//     converges. The sentence was right about this case.
	//   - `extensions` WRITTEN → planned known. On an ordinary create the
	//     ledger is still recorded, and if it differs Terraform fails the
	//     apply — which is loud, and costs an empty database. On the RESTORE
	//     path keepPlannedValues keeps the asked-for set instead, because the
	//     alternative is that same failure destroying recovered data; state is
	//     then optimistic until the next REFRESH corrects it, which is what
	//     the warning below now says.
	if !plan.Extensions.IsNull() && !plan.Extensions.IsUnknown() {
		if enable, _ := extensionDiff(plan.Extensions, types.SetNull(types.StringType)); len(enable) > 0 {
			if err := r.applyExtensionDiff(ctx, instID, enable, nil, budgets.Create); err != nil {
				resp.Diagnostics.AddWarning(
					"PostgreSQL extensions were not enabled during create",
					"The instance was created and reached running state, but enabling its declared extensions failed: "+
						err.Error()+".\n\nNothing is lost, but the next plan has to REFRESH to see it: run "+
						"`terraform plan` (not `-refresh=false`) and the extensions that did not enable show as a "+
						"diff for the next apply to retry.",
				)
			}
		}
	}

	// Recorded truth into state: a create-time extension failure above leaves
	// the recorded ledger in state, which is what makes the next apply see
	// the remainder as an ordinary plan diff.
	//
	// Its diagnostics go through `fail`, NOT straight onto resp: this call
	// AddErrors on every non-404 failure, and on the restore path an error
	// here is a tainted resource — one transient 500 on a read-only extension
	// side channel would destroy the database the restore just recovered.
	var extDiags diag.Diagnostics
	r.readExtensionsIntoModel(ctx, instID, plan, &extDiags)
	for _, d := range extDiags.Errors() {
		fail(d.Summary(), d.Detail())
	}
	resp.Diagnostics.Append(extDiags.Warnings()...)

	if preservePlanned {
		keepPlannedValues(planned, plan)
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)

	// LAST, and against the CONFIG (O8). Everything above is already recorded,
	// so this cannot leave an untracked instance behind. This is also the only
	// thing that still reports a pitr_enabled the platform did not apply, now
	// that keepPlannedValues stops Terraform's own consistency check from
	// noticing it — and an explicit diagnostic naming the cause is a better
	// answer than a generic inconsistent-result error anyway.
	checkPITREnactment(cfg.PITREnabled, finalInst.PITREnabled, fail)
}

// An instance cannot be un-created into a restore, so `restore_from` appearing on
// one that already exists is refused — WARNED at plan and refused at apply, never
// errored while planning: a destroy plan still runs ModifyPlan against a non-null
// plan, and an error there would leave a practitioner unable to destroy
// (TestNoErrorDiagnosticsWhilePlanning, and the pilot-customer report behind it).
//
// Recording it instead would be worse than useless. Update has no restore path, so
// nothing would happen — but state would then hold a non-null restore_from, and the
// NEXT edit of it fires RequiresReplaceIf and destroys a live database to "re-restore"
// it from a block that never did anything. A copy-pasted block is enough.
const restoreFromAddedTitle = "restore_from cannot be added to an instance that already exists"

const restoreFromAddedDetail = "This instance was not created by a restore, and a restore is how an " +
	"instance comes into existence — there is nothing for Terraform to do with the argument here, and " +
	"recording it would arm a REPLACEMENT, and so the destruction of this database, the next time it " +
	"changed.\n\nTo restore this instance to a point in time, add a SEPARATE " +
	"frostmoln_postgres_instance resource whose restore_from names this one; the source is left " +
	"untouched. To keep this instance as it is, remove the argument."

// keepPlannedValues restores every attribute the PLAN carried as a known value,
// after the model has been refreshed from the platform.
//
// THIS IS WHAT MAKES THE WARNING PATHS ACTUALLY SAFE, and without it they are
// not. Terraform core compares the planned object with the applied one and
// raises "Provider produced inconsistent result after apply" as an ERROR for
// any attribute whose known planned value came back different — and an errored
// Create is TAINTED, so the next apply destroys the resource. On the restore
// path that is the recovered database. So a create that reports a failure as a
// warning and then writes the platform's true value has not avoided the taint
// at all: it has produced it by a second route.
//
// A planned value that was UNKNOWN accepts anything, so those are taken from
// the platform as usual — which is every Computed-only attribute on a create.
// A planned value that was KNOWN is what the practitioner wrote, and it stays.
// The divergence is not lost: the next refresh reads the platform's real value
// and the next plan shows it as an ordinary in-place update, which is exactly
// what the restore path's warning promises.
//
// A null planned value is KNOWN, not unknown, and is preserved for the same
// reason — core checks null against non-null too.
//
// SCOPED TO THE RESTORE PATH, deliberately. On an ordinary create the
// inconsistent-result error is doing useful work: it is how a platform that
// quietly altered a submitted value gets caught, and the taint it causes there
// destroys an EMPTY new database, which costs nothing. Applied everywhere, it
// would instead record `ha_enabled = true` against a platform that answered
// false, and the refresh-plus-RequiresReplace that followed would propose
// destroying a database that by then holds data. Narrow mechanism, narrow
// scope.
//
// WHAT IT COSTS, on the path it does run: state is briefly optimistic. The
// correction depends on a REFRESH — Read calls fromAPI unconditionally, so the
// next ordinary plan shows the divergence as an in-place update — and
// `terraform plan -refresh=false`, which is what many pipelines run, will not
// show it. The warning text says so.
func keepPlannedValues(planned PostgresInstanceModel, m *PostgresInstanceModel) {
	if !planned.Name.IsUnknown() {
		m.Name = planned.Name
	}
	if !planned.Version.IsUnknown() {
		m.Version = planned.Version
	}
	if !planned.FlavorID.IsUnknown() {
		m.FlavorID = planned.FlavorID
	}
	if !planned.StorageGB.IsUnknown() {
		m.StorageGB = planned.StorageGB
	}
	if !planned.VPCID.IsUnknown() {
		m.VPCID = planned.VPCID
	}
	if !planned.SubnetID.IsUnknown() {
		m.SubnetID = planned.SubnetID
	}
	if !planned.HAEnabled.IsUnknown() {
		m.HAEnabled = planned.HAEnabled
	}
	if !planned.BackupEnabled.IsUnknown() {
		m.BackupEnabled = planned.BackupEnabled
	}
	if !planned.BackupSchedule.IsUnknown() {
		m.BackupSchedule = planned.BackupSchedule
	}
	if !planned.BackupRetentionDays.IsUnknown() {
		m.BackupRetentionDays = planned.BackupRetentionDays
	}
	if !planned.ParameterGroupID.IsUnknown() {
		m.ParameterGroupID = planned.ParameterGroupID
	}
	if !planned.Extensions.IsUnknown() {
		m.Extensions = planned.Extensions
	}
	if !planned.PITREnabled.IsUnknown() {
		m.PITREnabled = planned.PITREnabled
	}
	// restore_from is Optional-only, so it is always known and always the
	// configured value; fromAPI never touches it, but keeping it here means the
	// rule has no exceptions to remember.
	if !planned.RestoreFrom.IsUnknown() {
		m.RestoreFrom = planned.RestoreFrom
	}
}

// checkPITREnactment compares what the configuration asked for against what the
// instance GET reports, and reports a mismatch.
//
// There is one deployment where they can differ, and it is worth naming in the
// diagnostic rather than leaving the practitioner to guess: a database service
// older than the release that introduced point-in-time recovery does not know
// the field, drops it silently, and answers 200 having done nothing. Terraform
// would otherwise record the value it asked for and report a database as
// protected when it is not — the one failure mode this whole feature must not
// have.
//
// A configuration that says nothing is checked against nothing: the platform's
// answer IS the value, and there is no promise to compare it with.
func checkPITREnactment(configured types.Bool, reported *bool, fail func(summary, detail string)) {
	if configured.IsNull() || configured.IsUnknown() {
		return
	}
	if reported != nil && *reported == configured.ValueBool() {
		return
	}
	got := "nothing at all"
	if reported != nil {
		got = fmt.Sprintf("%t", *reported)
	}
	fail("pitr_enabled was not applied",
		fmt.Sprintf("The configuration asks for pitr_enabled = %t, and reading the instance back reports %s.\n\n"+
			"The database service that answered is older than the release that added point-in-time recovery: it "+
			"does not know the field, discards it without complaint and answers success. Terraform refuses to "+
			"record a protection level the platform has not confirmed.\n\n"+
			"Wait for the platform to finish rolling out and apply again, or remove pitr_enabled from the "+
			"configuration to accept whatever the platform decides.",
			configured.ValueBool(), got))
}

// createFromRestore builds this instance by restoring another one.
//
// THE WHOLE FUNCTION IS ORGANISED AROUND ONE LINE: the POST. Before it, nothing
// exists and every problem is a plain error. After it, a database holding the
// customer's recovered data exists, and an error returned from Create TAINTS
// the resource — which makes the next apply destroy and re-create it. So
// everything after the POST reports warnings, keeps the resource, and leaves
// the remaining work to show up as an ordinary in-place update on the next plan.
func (r *postgresInstanceResource) createFromRestore(
	ctx context.Context,
	rf *restoreFrom,
	plan *PostgresInstanceModel,
	cfg PostgresInstanceModel,
	budgets timeouts.Budgets,
	resp *resource.CreateResponse,
) {
	// --- Before the POST. Nothing exists; refuse loudly. ---

	// The schema's ExactlyOneOf covers a written block, but it is not reached
	// for a value that only resolves at apply, and this is the last word before
	// a request that creates a billable database.
	if (rf.PointInTime == "") == (rf.BackupID == "") {
		resp.Diagnostics.AddAttributeError(path.Root("restore_from"),
			"restore_from needs exactly one of point_in_time and backup_id",
			"Set point_in_time to restore to an instant, or backup_id to restore a named backup — not both, "+
				"and not neither. The platform refuses the two together rather than choosing, because an "+
				"older release silently dropped the timestamp and restored the backup instead.")
		return
	}

	// The belt for the plan-time validator above: Create must never trust that a
	// validated configuration is the only thing that reaches it, and this id
	// goes straight into two request paths.
	if err := validSourceInstanceID(rf.SourceInstanceID); err != nil {
		resp.Diagnostics.AddAttributeError(path.Root("restore_from").AtName("source_instance_id"),
			"Invalid restore source instance ID", err.Error())
		return
	}

	if rf.PointInTime != "" {
		if _, tErr := time.Parse(time.RFC3339, rf.PointInTime); tErr != nil {
			resp.Diagnostics.AddAttributeError(path.Root("restore_from").AtName("point_in_time"),
				"Invalid point_in_time",
				fmt.Sprintf("%q is not an RFC 3339 timestamp: %s", rf.PointInTime, tErr.Error()))
			return
		}
	}
	// The platform caps the new instance's name at 63 characters and refuses
	// the restore otherwise — after the source read, at the POST. Saying so
	// here costs nothing and keeps the late failures on this path to the ones
	// only the platform can know about.
	if n := plan.Name.ValueString(); len(n) > restoreTargetNameMaxLen {
		resp.Diagnostics.AddAttributeError(path.Root("name"),
			"name is too long to restore into",
			fmt.Sprintf("The platform accepts at most %d characters for the name of a restore target, and "+
				"this one is %d. Shorten `name`.", restoreTargetNameMaxLen, len(n)))
		return
	}

	source, err := r.getInstance(ctx, urlPathEscapeSegments(rf.SourceInstanceID))
	if err != nil {
		if client.IsNotFound(err) {
			resp.Diagnostics.AddAttributeError(path.Root("restore_from").AtName("source_instance_id"),
				"The restore source does not exist",
				fmt.Sprintf("Instance %q answers 404 — there is no such database instance, or none this "+
					"tenant can see. Nothing was created: Terraform will not fall back to creating an "+
					"empty database when the source of a restore cannot be read.", rf.SourceInstanceID))
			return
		}
		resp.Diagnostics.AddError("Failed to read the restore source", err.Error())
		return
	}

	resp.Diagnostics.Append(checkRestoreSourceShape(rf.SourceInstanceID, source, cfg, plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// --- The POST. Past here, a database may exist. ---

	restorePath := r.client.TenantPath("/databases/" + urlPathEscapeSegments(rf.SourceInstanceID) + "/restore")
	// Anything created by this POST is newer than this instant. It is taken
	// BEFORE the request and slackened by two minutes for clock skew between
	// here and the platform; it is what stops the recovery below from adopting
	// an instance that merely shares the name.
	//
	// The slack is asymmetric in the safe direction. A platform clock BEHIND
	// ours only causes a refusal, and a refusal tells the practitioner to go
	// and import — recoverable. Only a platform clock AHEAD of ours could
	// adopt wrongly, and only for an instance created inside the window, which
	// means someone made a same-named PostgreSQL instance in the two minutes
	// before this POST. Tightening it trades that away for false refusals on
	// exactly the bad day when this path runs.
	postStartedAt := time.Now().UTC().Add(-2 * time.Minute)
	apiResp, err := r.client.Post(ctx, restorePath, rf.toRestoreRequest(plan.Name.ValueString()))

	var target *apiPostgresInstance
	switch {
	case err == nil:
		// The restore answers 202 with the NEW TARGET INSTANCE — not an
		// operation, unlike an ordinary create. There is no operation id to
		// watch; the target's own status is the progress.
		target, err = client.ParseResponse[apiPostgresInstance](apiResp)
		if err != nil {
			// The POST was ACCEPTED — a database exists — and its answer is
			// unreadable. Returning here would leave it untracked and billing,
			// and the practitioner's natural next move (apply again) would
			// restore a SECOND copy. Recover it the same way the ambiguous
			// branch does.
			recovered, lookupErr := r.findRestoreTarget(ctx, plan.Name.ValueString(), postStartedAt)
			if lookupErr != nil || recovered == nil {
				resp.Diagnostics.AddError("The restore was accepted but its response could not be read",
					fmt.Sprintf("The platform accepted the restore and a database now exists, but its answer "+
						"could not be parsed (%s) and looking the target up by name did not find it%s.\n\n"+
						"DO NOT apply again before checking: a second accepted restore is a second billable "+
						"database and a second full backup. Find the instance named %q (portal, or "+
						"`fm db postgres instance list`) and import it; apply again only if it does not exist.",
						err.Error(), lookupSuffix(lookupErr), plan.Name.ValueString()))
				return
			}
			target = recovered
		}
	case !isAmbiguousWriteFailure(err):
		// The platform answered and refused. Nothing was created, and its own
		// message says why — an unreachable point in time names both bounds of
		// the window, and a source that cannot be restored to a point in time
		// says which of the reasons applies.
		resp.Diagnostics.AddError("Failed to restore the PostgreSQL instance", err.Error())
		return
	default:
		// AMBIGUOUS: a timeout, a dropped connection, a gateway 5xx. The
		// restore may or may not have been accepted.
		//
		// NEVER RE-POST HERE. Each accepted restore is a new billable instance
		// AND an immediate full base backup into 35-day locked storage, so a
		// blind retry can double both and leave one of the two untracked
		// forever. Look the target up by the name we asked for instead.
		//
		// THAT RULE IS NOT YET TRUE END TO END, and saying so is better than
		// implying otherwise: the shared client retries a 429 automatically
		// (client.go's Do, rateLimitRetries = 5), and doWithAuth replays once
		// after refreshing a bearer token, so a restore POST can leave this
		// process more than once before the error below is ever seen. Whether
		// that can actually duplicate a restore depends on something nobody has
		// checked — whether api-gateway's rate limiter can answer 429 AFTER
		// forwarding, which a limiter in a proxy's request path normally cannot.
		// Unverified, tracked, and deliberately not asserted here. The failure
		// it would cause is contained rather than silent: two same-named
		// instances make findInstanceByName refuse, so the next apply stops
		// instead of compounding.
		found, lookupErr := r.findRestoreTarget(ctx, plan.Name.ValueString(), postStartedAt)
		if lookupErr != nil {
			resp.Diagnostics.AddError("The restore could not be confirmed",
				fmt.Sprintf("The restore request failed in a way that does not say whether the platform "+
					"accepted it (%s), and the target could not be confirmed by name either: %s.\n\n"+
					"Terraform will not retry the restore: a second accepted restore would be a second "+
					"billable database and a second full backup, one of them untracked. Check whether an "+
					"instance named %q exists (portal, or `fm db postgres instance list`). Import it if it "+
					"does; apply again if it does not.",
					err.Error(), lookupErr.Error(), plan.Name.ValueString()))
			return
		}
		if found == nil {
			resp.Diagnostics.AddError("Failed to restore the PostgreSQL instance",
				fmt.Sprintf("%s\n\nNo instance named %q exists, so the restore was not accepted and nothing "+
					"was created. Applying again is safe.", err.Error(), plan.Name.ValueString()))
			return
		}
		target = found
	}

	instID := target.ID
	if instID == "" {
		resp.Diagnostics.AddError("The restore returned no instance id",
			"The platform accepted the restore but its answer carries no instance id, so Terraform cannot "+
				"track the database it created. Find it in the portal (or with `fm db postgres instance "+
				"list`) and import it; do not apply again, which would restore a second copy.")
		return
	}

	// 🔴 `plan` IS THE PLANNED MODEL AND IS NEVER OVERWRITTEN BELOW.
	//
	// Every path from here reports a warning and keeps the resource, and that
	// only works while something still knows what the plan promised:
	// keepPlannedValues restores the planned-KNOWN values so the recorded state
	// matches the plan and Terraform's consistency check raises nothing. An
	// earlier version refreshed `plan` in place here and kept a copy for the
	// diff — which meant the give-up paths below were handed the model the
	// platform built, took THAT as their baseline, and preserved the target's
	// values instead of the practitioner's. The taint came back by the same
	// route the warning exists to avoid. One model, one meaning.
	//
	// State is written from a separate copy.
	recorded := *plan
	recorded.fromAPI(ctx, target, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	keepPlannedValues(*plan, &recorded)

	// 🔴 STATE FIRST. Everything below this line is recoverable; an untracked
	// restored database is not.
	//
	// fromAPI never touches restore_from — the platform does not report it —
	// so the configured value rides through into state, which is what records
	// that this instance was restored.
	resp.Diagnostics.Append(resp.State.Set(ctx, &recorded)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// From here on, a failure is a WARNING. See the function comment.
	warn := func(summary, detail string) {
		resp.Diagnostics.AddWarning(summary,
			detail+"\n\nThe restored database EXISTS and Terraform is tracking it — a failure here is not "+
				"allowed to fail the create, because Terraform marks a failed create as tainted and the "+
				"next apply would then destroy and re-create the database, losing the data this restore "+
				"recovered.\n\nState therefore records what your configuration ASKED for, so run "+
				"`terraform plan` and the difference shows as an ordinary in-place update. It needs the "+
				"REFRESH to show it: `terraform plan -refresh=false` compares against state alone and "+
				"will report no change.")
	}

	if _, err := r.pollRunning(ctx, instID, budgets.Create); err != nil {
		r.saveStateFromGet(ctx, instID, plan, resp)
		warn("The restored PostgreSQL instance did not reach running state", err.Error())
		checkPITREnactment(cfg.PITREnabled, target.PITREnabled, warn)
		return
	}

	// The differences the restore itself could not carry, in ONE update: the
	// platform builds the target from the SOURCE's shape, so a configuration
	// asking for more storage, or for different backup settings, is applied
	// afterwards.
	if err := r.applyRestoreDifferences(ctx, instID, plan, target, budgets); err != nil {
		r.saveStateFromGet(ctx, instID, plan, resp)
		warn("The restored PostgreSQL instance was not brought fully to its configuration", err.Error())
		// O8 holds on EVERY path, not only the one that succeeds: state now
		// records the pitr_enabled the plan asked for, so the platform's
		// disagreement has to be said out loud here too.
		checkPITREnactment(cfg.PITREnabled, latestPITREnabled(ctx, r, instID, target), warn)
		return
	}

	r.finishCreate(ctx, instID, plan, cfg, budgets, resp, warn, true)
}

// checkRestoreSourceShape refuses, before anything is created, a configuration
// the restore could never satisfy.
//
// The platform builds the target from the SOURCE's row — its type, version,
// flavor, storage, VPC, subnet and availability shape are copied, not taken
// from this configuration. For the attributes that cannot then be changed in
// place, a configuration that disagrees with the source is not a difference
// Terraform can converge: the apply would read back the source's value, which
// Terraform reports as "Provider produced inconsistent result after apply" —
// an ERRORED create, which taints the resource, which destroys the restored
// database on the next apply. Refusing here, while nothing exists, is the only
// point at which that is cheap.
//
// backup_enabled, backup_schedule, backup_retention_days and pitr_enabled are
// deliberately NOT checked: those the platform does change in place, and
// applyRestoreDifferences converges them.
func checkRestoreSourceShape(sourceID string, source *apiPostgresInstance, cfg PostgresInstanceModel, plan *PostgresInstanceModel) diag.Diagnostics {
	var diags diag.Diagnostics

	mismatch := func(attr, want, got string) {
		diags.AddAttributeError(path.Root(attr),
			fmt.Sprintf("%s does not match the restore source", attr),
			fmt.Sprintf("This configuration sets %s = %q, and the restore source %s has %q. A restore copies "+
				"that value from the source and the platform cannot change it afterwards, so the instance "+
				"could only ever come out as %q — which Terraform would report as an inconsistent result "+
				"AFTER creating the database.\n\nSet %s to %q, or restore from a different source.",
				attr, want, sourceID, got, got, attr, got))
	}

	if source.Type != offerTypePostgreSQL {
		diags.AddAttributeError(path.Root("restore_from").AtName("source_instance_id"),
			"The restore source is not a PostgreSQL instance",
			fmt.Sprintf("Instance %s is a %q instance. A frostmoln_postgres_instance can only be restored "+
				"from a PostgreSQL source; for a MySQL one use frostmoln_mysql_instance.", sourceID, source.Type))
	}
	if plan.Version.ValueString() != source.PostgresVersion {
		mismatch("version", plan.Version.ValueString(), source.PostgresVersion)
	}
	if plan.FlavorID.ValueString() != source.FlavorID {
		mismatch("flavor_id", plan.FlavorID.ValueString(), source.FlavorID)
	}
	if plan.VPCID.ValueString() != source.VPCID {
		mismatch("vpc_id", plan.VPCID.ValueString(), source.VPCID)
	}
	if plan.SubnetID.ValueString() != source.SubnetID {
		mismatch("subnet_id", plan.SubnetID.ValueString(), source.SubnetID)
	}
	// ha_enabled is Optional+Computed: omitted, it is unknown in the plan and
	// accepts whatever the source's shape produces. Only a value the
	// practitioner WROTE can contradict the source.
	//
	// Compared against the source's RAW haEnabled, while the platform stamps
	// the target with `source.HAEnabled && SupportsHA(source.Type)`
	// (database service/impl/backup.go:673). The two agree here because the
	// type gate above already refuses any source that is not PostgreSQL, which
	// supports HA — and mirroring the AND would put a second copy of the
	// platform's HA-capability rule in this provider, to no gain.
	if !cfg.HAEnabled.IsNull() && !cfg.HAEnabled.IsUnknown() && cfg.HAEnabled.ValueBool() != source.HAEnabled {
		mismatch("ha_enabled", fmt.Sprintf("%t", cfg.HAEnabled.ValueBool()), fmt.Sprintf("%t", source.HAEnabled))
	}
	// Storage is the one copied value that CAN be changed afterwards, and only
	// upwards: it is grown online and never shrunk. Int64GrowOnly compares a
	// plan against prior STATE and so does not run on a create at all, which is
	// why this is checked explicitly.
	if plan.StorageGB.ValueInt64() < int64(source.StorageGB) {
		diags.AddAttributeError(path.Root("storage_gb"),
			"storage_gb is smaller than the restore source's",
			fmt.Sprintf("This configuration asks for %d GB and the restore source %s has %d GB. A restore "+
				"starts at the source's size and storage is grown online, never shrunk, so a smaller size "+
				"is unreachable. Ask for at least %d GB.",
				plan.StorageGB.ValueInt64(), sourceID, source.StorageGB, source.StorageGB))
	}

	return diags
}

// applyRestoreDifferences converges the restored target onto the configuration,
// in ONE update: storage first (its own online-resize route), then everything
// PUT-able together.
func (r *postgresInstanceResource) applyRestoreDifferences(ctx context.Context, instID string, plan *PostgresInstanceModel, target *apiPostgresInstance, budgets timeouts.Budgets) error {
	if plan.StorageGB.ValueInt64() > int64(target.StorageGB) {
		if err := r.resizeStorage(ctx, instID, int(plan.StorageGB.ValueInt64()), nil, budgets.Create); err != nil {
			return fmt.Errorf("growing storage from %d GB to %d GB: %w", target.StorageGB, plan.StorageGB.ValueInt64(), err)
		}
	}

	// The target as the platform actually built it, as the "state" the diff is
	// taken against — the restore inherited the SOURCE's backup settings, so
	// this is the only honest baseline.
	//
	// From a GET, NOT from the restore's 202 body. The create/update/list
	// shapes carry less than the single-instance GET, and an absent
	// backupRetentionDays reads back through fromAPI as the 35-day floor — so
	// a target that really inherited 90 would baseline at 35, a config asking
	// for 35 would compare equal, nothing would be PUT, and the final read
	// would report 90 against a planned 35.
	baseline := target
	if fresh, err := r.getInstance(ctx, instID); err == nil {
		baseline = fresh
	}
	var current PostgresInstanceModel
	var ignored diag.Diagnostics
	current.fromAPI(ctx, baseline, &ignored)

	updateReq := plan.toUpdateRequest(&current)
	if !updateReq.hasChanges() {
		return nil
	}
	if _, err := r.client.Put(ctx, r.client.TenantPath("/databases/"+instID), updateReq); err != nil {
		return fmt.Errorf("applying the configured settings to the restored instance: %w", err)
	}
	if _, err := r.pollRunning(ctx, instID, budgets.Create); err != nil {
		return fmt.Errorf("waiting for the restored instance after applying its settings: %w", err)
	}
	return nil
}

// saveStateFromGet records what the instance REALLY is, best effort, on a path
// that is about to give up. It is how a half-converged restore stays a tracked
// resource with true attributes rather than one carrying the configuration's
// wishes; a failure to read is swallowed, because the caller is already
// reporting the real problem and state already holds the id.
func (r *postgresInstanceResource) saveStateFromGet(ctx context.Context, instID string, plan *PostgresInstanceModel, resp *resource.CreateResponse) {
	inst, err := r.getInstance(ctx, instID)
	if err != nil {
		return
	}
	planned := *plan
	var ignored diag.Diagnostics
	plan.fromAPI(ctx, inst, &ignored)
	if ignored.HasError() {
		return
	}
	// Unconditional, unlike finishCreate's: saveStateFromGet is reached ONLY
	// from the restore path's give-up arms, which is exactly where
	// keepPlannedValues applies. The whole point here is to keep the resource
	// tracked and the apply un-failed, and writing a value the plan did not
	// promise would fail it anyway.
	keepPlannedValues(planned, plan)
	resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
}

// lookupSuffix renders why a target lookup could not answer, for a diagnostic
// that has already said what the practitioner must check.
func lookupSuffix(err error) string {
	if err == nil {
		return ""
	}
	return " (" + err.Error() + ")"
}

// findRestoreTarget looks for the instance THIS restore created: a PostgreSQL
// instance of the expected name that did not exist before the POST.
//
// The creation-time bound is the whole point. Instance names are not unique per
// tenant, so a name match alone cannot tell "the target my lost POST created"
// from "an instance that already had this name" — an earlier manual recovery, a
// leftover, another workspace's. Adopting the wrong one puts a database
// Terraform did not create into state, applies this configuration's backup
// policy to it, and destroys it on the next `terraform destroy`. The lookup
// therefore REFUSES rather than adopt when it cannot show the candidate is new.
func (r *postgresInstanceResource) findRestoreTarget(ctx context.Context, name string, notBefore time.Time) (*apiPostgresInstance, error) {
	found, err := r.findInstanceByName(ctx, name)
	if err != nil || found == nil {
		return found, err
	}
	created, parseErr := time.Parse(time.RFC3339, found.CreatedAt)
	if parseErr != nil {
		return nil, fmt.Errorf("an instance named %q exists but its createdAt %q cannot be read, so it cannot be shown to be the one this restore created",
			name, found.CreatedAt)
	}
	if created.Before(notBefore) {
		return nil, fmt.Errorf("an instance named %q already existed before this restore was sent (created %s), so it is not the target and must not be adopted",
			name, found.CreatedAt)
	}
	return found, nil
}

// findInstanceByName resolves a PostgreSQL instance by name, for the one job of
// telling an accepted-but-unconfirmed write apart from one that never landed.
// It returns nil, nil for "no such instance", and an error when the question
// could not be answered — including when the name is AMBIGUOUS, because
// adopting the wrong one of two same-named instances is worse than admitting
// the lookup failed.
func (r *postgresInstanceResource) findInstanceByName(ctx context.Context, name string) (*apiPostgresInstance, error) {
	const pageSize = 100
	const maxPages = 20

	var matches []apiPostgresInstance
	for offset, page := 0, 0; page < maxPages; offset, page = offset+pageSize, page+1 {
		q := url.Values{}
		q.Set("limit", strconv.Itoa(pageSize))
		q.Set("offset", strconv.Itoa(offset))

		apiResp, err := r.client.Get(ctx, r.client.TenantPath("/databases"), q)
		if err != nil {
			return nil, err
		}
		list, err := client.ParseResponse[apiPostgresInstanceList](apiResp)
		if err != nil {
			return nil, err
		}
		for i := range list.Instances {
			if list.Instances[i].Name == name && list.Instances[i].Type == offerTypePostgreSQL {
				matches = append(matches, list.Instances[i])
			}
		}
		if len(list.Instances) < pageSize {
			break
		}
		if page == maxPages-1 {
			return nil, fmt.Errorf("the tenant's database list did not end within %d pages of %d, so %q cannot be shown to be absent", maxPages, pageSize, name)
		}
	}

	switch len(matches) {
	case 0:
		return nil, nil
	case 1:
		return &matches[0], nil
	default:
		ids := make([]string, 0, len(matches))
		for i := range matches {
			ids = append(ids, matches[i].ID)
		}
		return nil, fmt.Errorf("%d PostgreSQL instances are named %q (%s), so which one this restore created cannot be told apart",
			len(matches), name, strings.Join(ids, ", "))
	}
}

// isAmbiguousWriteFailure reports whether a failed write may still have been
// accepted by the service behind the gateway.
//
// A refusal the SERVICE rendered (4xx with its own code) is final: it decided,
// and it created nothing. A transport failure, a timeout, or a 5xx from
// anything in between is not a decision at all — the request may have reached
// the service and been accepted while the answer was lost. 429 is on the
// ambiguous side deliberately: the rate limiter usually refuses before the
// service sees anything, but "usually" is not what a billable, backup-creating
// write should be retried on.
func isAmbiguousWriteFailure(err error) bool {
	var apiErr *client.APIError
	if !errors.As(err, &apiErr) {
		return true
	}
	// A 503 the SERVICE itself rendered is a decision: it refused, and it
	// created nothing. The pre-GA restore gate is exactly that, and it is the
	// error a practitioner is most likely to meet before go-live — treating it
	// as ambiguous walked the tenant's whole database list and then said
	// "applying again is safe", which is advice, and wrong. FlatEnvelope is
	// the repo's own discriminator for "a servicekit-rendered refusal", the
	// same one IsNotFound uses; a 503 from anything in between does not carry
	// it and stays ambiguous.
	// 503 SPECIFICALLY, and that narrowness is load-bearing — do NOT widen this
	// to `>= 500 && FlatEnvelope`. The gate is the only 503 on this path and it
	// fires before any row is written, but a 500 rendered AFTER the instance
	// row was created carries a flat envelope just the same, and calling that
	// "nothing was created" would leave a billable database untracked.
	if apiErr.StatusCode == http.StatusServiceUnavailable && apiErr.FlatEnvelope {
		return false
	}
	return apiErr.StatusCode >= 500 ||
		apiErr.StatusCode == http.StatusRequestTimeout ||
		apiErr.StatusCode == http.StatusTooManyRequests
}

// withRetryAfter appends the ONE piece of machine-readable context the
// platform's own sentence cannot carry.
//
// This provider deliberately renders no `details` — for every other refusal on
// this surface the server's message already says everything the details repeat
// (pitr_out_of_window states both bounds in its text; the per-instance refusals
// carry per-reason prose). The re-enable cooldown is the exception: the message
// says point-in-time recovery "cannot be turned back on within 24 hours of
// turning it off" and never says of WHEN, while `details.retryAfter` holds the
// exact instant. Terraform is where that matters most — the practitioner's next
// move is to schedule the retry.
func withRetryAfter(err error) string {
	var apiErr *client.APIError
	if !errors.As(err, &apiErr) {
		return err.Error()
	}
	retryAfter, ok := apiErr.Details["retryAfter"].(string)
	if !ok || retryAfter == "" {
		return err.Error()
	}
	return fmt.Sprintf("%s (you can turn it back on from %s)", err.Error(), retryAfter)
}

// latestPITREnabled answers with the FRESHEST pitr_enabled the provider can get
// for the O8 read-back, falling back to what the restore returned when the
// instance cannot be re-read. The converging update may well have been the
// thing that set it, so the 202 body alone would report the pre-update value.
func latestPITREnabled(ctx context.Context, r *postgresInstanceResource, instID string, target *apiPostgresInstance) *bool {
	if fresh, err := r.getInstance(ctx, instID); err == nil {
		return fresh.PITREnabled
	}
	return target.PITREnabled
}

// getInstance reads one instance by id and parses it.
func (r *postgresInstanceResource) getInstance(ctx context.Context, instID string) (*apiPostgresInstance, error) {
	apiResp, err := r.client.Get(ctx, r.client.TenantPath("/databases/"+instID), nil)
	if err != nil {
		return nil, err
	}
	return client.ParseResponse[apiPostgresInstance](apiResp)
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
	if resp.Diagnostics.HasError() {
		return
	}

	// The extension ledger is a separate read: project the recorded enabled
	// entries into the attribute so out-of-band changes surface as drift.
	r.readExtensionsIntoModel(ctx, state.ID.ValueString(), &state, &resp.Diagnostics)

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *postgresInstanceResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan PostgresInstanceModel
	var state PostgresInstanceModel
	var cfg PostgresInstanceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// The apply half of the plan-time warning above: planning warns, applying
	// refuses. An Update error does not taint, so this is a clean failure that
	// leaves the instance exactly as it is.
	if !cfg.RestoreFrom.IsNull() && !cfg.RestoreFrom.IsUnknown() && state.RestoreFrom.IsNull() {
		resp.Diagnostics.AddAttributeError(path.Root("restore_from"), restoreFromAddedTitle, restoreFromAddedDetail)
		return
	}

	// Belt for the plan-time unenacted validator (see the constants above): by
	// apply every value is known, and the enforced-NULL plan value here is a
	// legitimate legacy drain (an id stored by an older provider clears from
	// the record on the PUT below). A non-null plan value at this point is the
	// unknown-at-plan escape hatch, and the refusal names the same constraint.
	if !plan.ParameterGroupID.IsNull() {
		resp.Diagnostics.AddAttributeError(
			path.Root("parameter_group_id"), parameterGroupRefusalTitle, parameterGroupRefusalDetail,
		)
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
		// Carry a schedule the practitioner CHANGED in the same plan: the
		// platform refuses a resize whose new size leaves a customer-written
		// schedule below the base-backup floor, and the PUT that would fix it
		// runs after this. A schedule the platform chose is re-picked by the
		// platform itself and must not be sent.
		var resizeSchedule *string
		if !plan.BackupSchedule.IsUnknown() && !plan.BackupSchedule.IsNull() &&
			!plan.BackupSchedule.Equal(state.BackupSchedule) && plan.BackupSchedule.ValueString() != "" {
			v := plan.BackupSchedule.ValueString()
			resizeSchedule = &v
		}
		if err := r.resizeStorage(ctx, id, int(plan.StorageGB.ValueInt64()), resizeSchedule, budgets.Update); err != nil {
			resp.Diagnostics.AddError("Failed to resize PostgreSQL instance storage", err.Error())
			return
		}
	}

	// In-place field updates (name, backups, parameter group) via PUT. Skip the
	// call entirely when nothing PUT-able changed (e.g. a storage-only resize).
	if updateReq := plan.toUpdateRequest(&state); updateReq.hasChanges() {
		if _, err := r.client.Put(ctx, r.client.TenantPath("/databases/"+id), updateReq); err != nil {
			resp.Diagnostics.AddError("Failed to update PostgreSQL instance", withRetryAfter(err))
			return
		}

		// Poll until instance is back to "running" after the update. The
		// extension ops below need a running instance (the platform refuses
		// extension requests on any other status), so they wait for this
		// first — a backup or restore in flight would refuse them instead,
		// and waiting here is honest both ways.
		if _, err := r.pollRunning(ctx, id, budgets.Update); err != nil {
			resp.Diagnostics.AddError("PostgreSQL instance failed to reach running state after update", err.Error())
			return
		}
	}

	// Enable/disable the declared diff, SERIALIZED (enables first, then
	// disables — the platform runs one operation per instance, so the second
	// batch always waits for the first's recorded verdict). The ledger is the
	// verdict and is re-read before every POST (reconcile-first), so a change
	// that already happened cannot be paid twice.
	enable, disable := extensionDiff(plan.Extensions, state.Extensions)
	if len(enable) > 0 || len(disable) > 0 {
		if err := r.applyExtensionDiff(ctx, id, enable, disable, budgets.Update); err != nil {
			verb := "enable"
			if len(enable) == 0 {
				verb = "disable"
			}
			resp.Diagnostics.AddError("Failed to "+verb+" PostgreSQL extensions", err.Error())
			return
		}
	}

	// Refresh state from API. This re-READ is the contract (O8), not a
	// convenience: the PUT's own response body carries no restorable window
	// and no archive pause reason, and it is also the only thing that can show
	// that a pitrEnabled the provider sent was dropped on the floor.
	inst, err := r.getInstance(ctx, id)
	if err != nil {
		resp.Diagnostics.AddError("Failed to read PostgreSQL instance after update", err.Error())
		return
	}

	plan.fromAPI(ctx, inst, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}

	// The recorded verdict is what state keeps — e.g. a failed enable names
	// the failure and shows as drift on the next plan, not as a phantom.
	r.readExtensionsIntoModel(ctx, id, &plan, &resp.Diagnostics)

	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)

	// An Update error does NOT taint, so this is a plain error: the resource
	// keeps the state written above and the next plan shows the difference.
	checkPITREnactment(cfg.PITREnabled, inst.PITREnabled, resp.Diagnostics.AddError)
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
