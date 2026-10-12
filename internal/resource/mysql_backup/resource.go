package mysql_backup

import (
	"context"
	"fmt"
	"time"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/scopedecl"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/tftags"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/timeouts"
)

var (
	_ resource.Resource                = &mysqlBackupResource{}
	_ resource.ResourceWithImportState = &mysqlBackupResource{}
	_ resource.ResourceWithModifyPlan  = &mysqlBackupResource{}
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

// getPollTimeout is the DEFAULT wait budget — the timeouts block's fallback
// per verb. Raised from 30m to 2h (audit D4): the platform's own polling
// ceiling for this workflow, backupPollDeadline (provisioning
// internal/workflow/backup_database.go:27), is 2 hours — matched to the agent
// job TTL plus the 2h backupPresignTTL so the workflow observes the agent's
// TRUE terminal state instead of declaring a live backup failed while its
// object and presigned URL are still usable. 30m gave up on a backup the
// platform still considers live. State is already written before the wait
// (adopt contract), so the 30m breach was never an orphan — only a wrong
// verdict, now closed too.
func (r *mysqlBackupResource) getPollTimeout() time.Duration {
	if r.pollTimeout > 0 {
		return r.pollTimeout
	}
	return 2 * time.Hour
}

// resolveBudgets turns the configured timeouts block into effective budgets,
// falling back per verb to the same value this resource has always hardcoded.
// Routing the defaults through the accessor keeps the test-injection seam
// intact: a test that shrinks pollTimeout shrinks every wait that does not
// carry an explicit timeouts override, exactly as before. Only the create
// wait uses a budget — an update writes tags only (no wait) and delete
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
		Description: "Manages a backup of a managed MySQL instance. Every attribute except `tags` (and `timeouts`) is fixed at creation; changing one replaces the backup." + "\n\n" + scopedecl.Summary("frostmoln_mysql_backup"),
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
				Description: "The type of backup. Only \"full\" is supported (every backup is a full dump). Defaults to \"full\".",
				Optional:    true,
				Computed:    true,
				Validators: []validator.String{
					stringvalidator.OneOf("full"),
				},
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
			// Customer-tunable wait budgets: default 2h per verb (30m before
			// 2026-09-16 — audit D4: the platform's own backupPollDeadline
			// for this workflow is 2h, agent job TTL + presigned-URL TTL).
			// Create is the
			// only verb with a wait to bound — the poll to "completed" —
			// while an update writes tags only, with no wait,
			// and delete is a plain DELETE answered synchronously, so
			// budgets.Update / budgets.Delete go unused. A timeouts change
			// is an in-place no-op on real infrastructure — verified by the
			// Gate 2 smoke test (project-docs/product/TF-CONVERGENCE-WALL-PLAN.md).
			"timeouts": timeouts.Schema(),
		},
	}
	resp.Schema.Attributes["tags"] = tftags.OfferTagsAttribute("Resource tags on the backup: your own key/value metadata for search and filtering, stored by the platform only. A backup's tags are its own, never copied from the instance. Changing them updates the backup in place.")
	resp.Schema.Attributes["tags_all"] = tftags.TagsAllAttribute()
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
	defaults := r.client.DefaultTags()
	apiReq.Tags = tftags.ForCreate(ctx, defaults, plan.Tags, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}

	instanceID := plan.InstanceID.ValueString()

	// The customer's timeouts block, falling back to this resource's
	// 2h defaults (see resolveBudgets and the block comment above). Create
	// is the only verb with a
	// wait to bound.
	budgets := r.resolveBudgets(plan.Timeouts)

	apiResp, err := r.client.Post(ctx, r.backupPath(instanceID, ""), apiReq)
	if err == nil {
		tftags.RecordDefaults(ctx, resp.Private, defaults, &resp.Diagnostics)
	}
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
	tftags.FinishRead(ctx, req.Private, resp.Private, r.client.DefaultTags(), &state.Tags, state.TagsAll, &resp.Diagnostics)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Update changes the backup's tags with a tags-only PATCH, sent only when the
// platform's set would change (computed against the tags the backup holds right
// now, so a key added outside Terraform is kept). It starts no work on the backup.
func (r *mysqlBackupResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state MysqlBackupModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	memberPath := r.backupPath(state.InstanceID.ValueString(), state.ID.ValueString())
	get := func(ctx context.Context) (*apiMysqlBackup, error) {
		apiResp, err := r.client.Get(ctx, memberPath, nil)
		if err != nil {
			return nil, err
		}
		return client.ParseResponse[apiMysqlBackup](apiResp)
	}
	defaults := r.client.DefaultTags()
	tags, err := tftags.OfferTagsToWrite(ctx, defaults, plan.Tags, state.Tags, state.TagsAll, req.Private,
		func(ctx context.Context) (map[string]string, error) {
			cur, err := get(ctx)
			if err != nil {
				return nil, err
			}
			return tftags.OfferCustomerTags(cur.Tags), nil
		}, &resp.Diagnostics)
	if err != nil {
		resp.Diagnostics.AddError("Failed to Read Current Tags", tftags.CurrentTagsReadFailed+err.Error())
		return
	}
	if resp.Diagnostics.HasError() {
		return
	}
	if tags != nil {
		if _, err := r.client.Patch(ctx, memberPath, apiUpdateTagsRequest{Tags: tags}); err != nil {
			resp.Diagnostics.AddError("Failed to update MySQL backup tags", err.Error())
			return
		}
	}
	// The platform now holds the desired set, whether or not this update had to
	// write it; that also ends an import's pending state.
	tftags.RecordDefaults(ctx, resp.Private, defaults, &resp.Diagnostics)

	current, err := get(ctx)
	if err != nil {
		resp.Diagnostics.AddError("Failed to read MySQL backup after update", err.Error())
		return
	}
	plan.fromAPI(ctx, current, &resp.Diagnostics)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
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
	tftags.MarkImported(ctx, resp.Private, &resp.Diagnostics)
}

// ModifyPlan plans tags_all: a default_tags change, or a managed key's drift,
// plans an in-place update of the backup's tags.
func (r *mysqlBackupResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	tftags.PlanTagsAll(ctx, r.client.DefaultTags(), req, resp)
	tftags.WarnOfferTagBudget(ctx, r.client.DefaultTags(), req, resp)
}
