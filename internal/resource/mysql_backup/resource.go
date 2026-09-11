package mysql_backup

import (
	"context"
	"fmt"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/scopedecl"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/timeouts"
)

var (
	_ resource.Resource                = &mysqlBackupResource{}
	_ resource.ResourceWithImportState = &mysqlBackupResource{}
)

// NewResource returns a new mysql_backup resource factory.
func NewResource() resource.Resource {
	return &mysqlBackupResource{}
}

type mysqlBackupResource struct {
	client       *client.Client
	pollInterval time.Duration
	pollTimeout  time.Duration
}

func (r *mysqlBackupResource) getPollInterval() time.Duration {
	if r.pollInterval > 0 {
		return r.pollInterval
	}
	return 5 * time.Second
}

func (r *mysqlBackupResource) getPollTimeout() time.Duration {
	if r.pollTimeout > 0 {
		return r.pollTimeout
	}
	return 30 * time.Minute
}

// resolveBudgets turns the configured timeouts block into effective budgets,
// falling back per verb to the same value this resource has always hardcoded.
// Routing the defaults through the accessor keeps the test-injection seam
// intact: a test that shrinks pollTimeout shrinks every wait that does not
// carry an explicit timeouts override, exactly as before. Only the create
// wait uses a budget — update is refused (backups are immutable) and delete
// is a plain DELETE with no wait.
func (r *mysqlBackupResource) resolveBudgets(m *timeouts.Model) timeouts.Budgets {
	budgets, err := m.Resolve(timeouts.Uniform(r.getPollTimeout()))
	if err != nil {
		// Unreachable via HCL (the block validator rejects bad durations at
		// plan time); degrade to the defaults rather than fail a wait.
		return timeouts.Uniform(r.getPollTimeout())
	}
	return budgets
}

func (r *mysqlBackupResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_mysql_backup"
}

func (r *mysqlBackupResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manages a backup of a managed MySQL instance. Backups are immutable after creation." + "\n\n" + scopedecl.Summary("frostmoln_mysql_backup"),
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Description: "The unique identifier of the backup.",
				Computed:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"instance_id": schema.StringAttribute{
				Description: "The ID of the MySQL instance to back up.",
				Required:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"name": schema.StringAttribute{
				Description: "The name of the backup.",
				Required:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"type": schema.StringAttribute{
				Description: "The type of backup: \"full\", \"incremental\", or \"binlog\". Defaults to \"full\".",
				Optional:    true,
				Computed:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
					stringplanmodifier.RequiresReplace(),
				},
			},
			"status": schema.StringAttribute{
				Description: "The current status of the backup.",
				Computed:    true,
			},
			"size_bytes": schema.Int64Attribute{
				Description: "The size of the backup in bytes.",
				Computed:    true,
				PlanModifiers: []planmodifier.Int64{
					int64planmodifier.UseStateForUnknown(),
				},
			},
			"started_at": schema.StringAttribute{
				Description: "The timestamp when the backup started.",
				Computed:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"completed_at": schema.StringAttribute{
				Description: "The timestamp when the backup completed.",
				Computed:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
		},
		Blocks: map[string]schema.Block{
			// Customer-tunable wait budgets: defaults keep the values this
			// resource has always hardcoded (30m per verb). Create is the
			// only verb with a wait to bound — the poll to "completed" —
			// while update is refused (backups are immutable after creation)
			// and delete is a plain DELETE answered synchronously, so
			// budgets.Update / budgets.Delete go unused. A timeouts change
			// is an in-place no-op on real infrastructure — verified by the
			// Gate 2 smoke test (project-docs/product/TF-CONVERGENCE-WALL-PLAN.md).
			"timeouts": timeouts.Schema(),
		},
	}
}

func (r *mysqlBackupResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (r *mysqlBackupResource) backupPath(instanceID, backupID string) string {
	if backupID != "" {
		return r.client.TenantPath(fmt.Sprintf("/databases/%s/backups/%s", instanceID, backupID))
	}
	return r.client.TenantPath(fmt.Sprintf("/databases/%s/backups", instanceID))
}

func (r *mysqlBackupResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan MysqlBackupModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	apiReq := plan.toCreateRequest(ctx, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}

	instanceID := plan.InstanceID.ValueString()

	// The customer's timeouts block, with defaults identical to the values
	// this resource has always hardcoded. Create is the only verb with a
	// wait to bound.
	budgets := r.resolveBudgets(plan.Timeouts)

	apiResp, err := r.client.Post(ctx, r.backupPath(instanceID, ""), apiReq)
	if err != nil {
		resp.Diagnostics.AddError("Failed to create MySQL backup", err.Error())
		return
	}

	backup, err := client.ParseResponse[apiMysqlBackup](apiResp)
	if err != nil {
		resp.Diagnostics.AddError("Failed to parse MySQL backup response", err.Error())
		return
	}

	plan.fromAPI(ctx, backup, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}

	// Save state immediately so the ID is tracked.
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// Poll until the backup reaches "completed" status, on the timeouts
	// block's create budget.
	_, err = client.WaitForState(ctx, client.PollConfig{
		Interval:     r.getPollInterval(),
		Timeout:      budgets.Create,
		TargetStates: []string{"completed", "available"},
		ErrorStates:  []string{"error", "failed"},
		ResourceName: "mysql_backup",
		PollFunc: func(pollCtx context.Context) (string, error) {
			pollResp, pollErr := r.client.Get(pollCtx, r.backupPath(instanceID, backup.ID), nil)
			if pollErr != nil {
				return "", pollErr
			}
			current, parseErr := client.ParseResponse[apiMysqlBackup](pollResp)
			if parseErr != nil {
				return "", parseErr
			}
			return current.Status, nil
		},
	})
	if err != nil {
		resp.Diagnostics.AddError("MySQL backup failed to complete", err.Error())
		return
	}

	// Refresh state after polling.
	readResp, err := r.client.Get(ctx, r.backupPath(instanceID, backup.ID), nil)
	if err != nil {
		resp.Diagnostics.AddError("Failed to read MySQL backup after creation", err.Error())
		return
	}
	finalBackup, err := client.ParseResponse[apiMysqlBackup](readResp)
	if err != nil {
		resp.Diagnostics.AddError("Failed to parse MySQL backup response", err.Error())
		return
	}

	plan.fromAPI(ctx, finalBackup, &resp.Diagnostics)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *mysqlBackupResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state MysqlBackupModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	apiResp, err := r.client.Get(ctx, r.backupPath(state.InstanceID.ValueString(), state.ID.ValueString()), nil)
	if err != nil {
		if client.IsNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Failed to read MySQL backup", err.Error())
		return
	}

	backup, err := client.ParseResponse[apiMysqlBackup](apiResp)
	if err != nil {
		resp.Diagnostics.AddError("Failed to parse MySQL backup response", err.Error())
		return
	}

	state.fromAPI(ctx, backup, &resp.Diagnostics)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *mysqlBackupResource) Update(_ context.Context, _ resource.UpdateRequest, resp *resource.UpdateResponse) {
	resp.Diagnostics.AddError(
		"Update Not Supported",
		"MySQL backups are immutable and cannot be updated. All attribute changes require resource replacement.",
	)
}

func (r *mysqlBackupResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state MysqlBackupModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	_, err := r.client.Delete(ctx, r.backupPath(state.InstanceID.ValueString(), state.ID.ValueString()))
	if err != nil {
		if client.IsNotFound(err) {
			return
		}
		resp.Diagnostics.AddError("Failed to delete MySQL backup", err.Error())
	}
}

func (r *mysqlBackupResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}
