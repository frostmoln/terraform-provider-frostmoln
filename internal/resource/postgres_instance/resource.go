package postgres_instance

import (
	"context"
	"fmt"
	"time"

	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/boolplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/stateupgrade"
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

func (r *postgresInstanceResource) getPollInterval() time.Duration {
	if r.pollInterval > 0 {
		return r.pollInterval
	}
	return 5 * time.Second
}

func (r *postgresInstanceResource) getPollTimeout() time.Duration {
	if r.pollTimeout > 0 {
		return r.pollTimeout
	}
	return 15 * time.Minute
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
		Description: "Manages a managed PostgreSQL database instance in the Frostmoln platform.",
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
			},
			"storage_gb": schema.Int64Attribute{
				Description: "The storage size in gigabytes.",
				Required:    true,
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
					boolplanmodifier.RequiresReplace(),
					boolplanmodifier.UseStateForUnknown(),
				},
			},
			"backup_enabled": schema.BoolAttribute{
				Description: "Whether automated backups are enabled.",
				Optional:    true,
				Computed:    true,
				PlanModifiers: []planmodifier.Bool{
					boolplanmodifier.UseStateForUnknown(),
				},
			},
			"backup_schedule": schema.StringAttribute{
				Description: "Cron expression for the backup schedule (e.g. \"0 2 * * *\").",
				Optional:    true,
			},
			"backup_retention_days": schema.Int64Attribute{
				Description: "Number of days to retain backups. Minimum 35 (backups are immutably object-locked for 35 days, ADR-0085); maximum 90.",
				Optional:    true,
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
			"floating_ip": schema.StringAttribute{
				Description: "The floating (public) IP address, if assigned.",
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
		done, err := r.client.WaitForOperation(ctx, op.OperationID, r.getPollInterval(), r.getPollTimeout())
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
		Timeout:      r.getPollTimeout(),
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

	updateReq := plan.toUpdateRequest(&state)
	_, err := r.client.Put(ctx, r.client.TenantPath("/databases/"+id), updateReq)
	if err != nil {
		resp.Diagnostics.AddError("Failed to update PostgreSQL instance", err.Error())
		return
	}

	// Poll until instance is back to "running" after the update.
	_, err = client.WaitForState(ctx, client.PollConfig{
		Interval:     r.getPollInterval(),
		Timeout:      r.getPollTimeout(),
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
	if err != nil {
		resp.Diagnostics.AddError("PostgreSQL instance failed to reach running state after update", err.Error())
		return
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

	// Wait for the instance to be fully deleted (404 on GET).
	_, err = client.WaitForState(ctx, client.PollConfig{
		Interval:     r.getPollInterval(),
		Timeout:      r.getPollTimeout(),
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

// UpgradeState migrates v0 state (HCL attribute `flavor`) to v1 (`flavor_id`).
// The rename is HCL-surface only -- the wire tag was always flavorId -- so the
// migration is purely local: it copies the prior `flavor` value into `flavor_id`
// and carries every other attribute through unchanged. `flavor` is in-place
// updatable (not RequiresReplace), so without this the first post-upgrade plan
// would show a spurious update rather than a destroy; the upgrader keeps the
// upgrade a clean no-op.
func (r *postgresInstanceResource) UpgradeState(ctx context.Context) map[int64]resource.StateUpgrader {
	schemaResp := resource.SchemaResponse{}
	r.Schema(ctx, resource.SchemaRequest{}, &schemaResp)
	return map[int64]resource.StateUpgrader{
		0: stateupgrade.RenameStringAttr(ctx, schemaResp.Schema, "flavor", "flavor_id"),
	}
}
