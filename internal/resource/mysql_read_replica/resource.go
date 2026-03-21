package mysql_read_replica

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
)

var (
	_ resource.Resource                = &mysqlReadReplicaResource{}
	_ resource.ResourceWithImportState = &mysqlReadReplicaResource{}
)

// NewResource returns a new mysql_read_replica resource factory.
func NewResource() resource.Resource {
	return &mysqlReadReplicaResource{}
}

type mysqlReadReplicaResource struct {
	client       *client.Client
	pollInterval time.Duration
	pollTimeout  time.Duration
}

func (r *mysqlReadReplicaResource) getPollInterval() time.Duration {
	if r.pollInterval > 0 {
		return r.pollInterval
	}
	return 5 * time.Second
}

func (r *mysqlReadReplicaResource) getPollTimeout() time.Duration {
	if r.pollTimeout > 0 {
		return r.pollTimeout
	}
	return 15 * time.Minute
}

func (r *mysqlReadReplicaResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_mysql_read_replica"
}

func (r *mysqlReadReplicaResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manages a read replica of a managed MySQL instance. Read replicas are immutable after creation.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Description: "The unique identifier of the read replica.",
				Computed:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"instance_id": schema.StringAttribute{
				Description: "The ID of the primary MySQL instance to replicate.",
				Required:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"name": schema.StringAttribute{
				Description: "The name of the read replica.",
				Required:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"status": schema.StringAttribute{
				Description: "The current status of the read replica.",
				Computed:    true,
			},
			"private_ip": schema.StringAttribute{
				Description: "The private IP address of the read replica.",
				Computed:    true,
			},
			"port": schema.Int64Attribute{
				Description: "The port number the read replica is listening on.",
				Computed:    true,
				PlanModifiers: []planmodifier.Int64{
					int64planmodifier.UseStateForUnknown(),
				},
			},
			"replication_lag_bytes": schema.Int64Attribute{
				Description: "The replication lag in bytes between primary and replica.",
				Computed:    true,
			},
		},
	}
}

func (r *mysqlReadReplicaResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (r *mysqlReadReplicaResource) replicaPath(instanceID, replicaID string) string {
	if replicaID != "" {
		return r.client.TenantPath(fmt.Sprintf("/databases/%s/replicas/%s", instanceID, replicaID))
	}
	return r.client.TenantPath(fmt.Sprintf("/databases/%s/replicas", instanceID))
}

func (r *mysqlReadReplicaResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan MysqlReadReplicaModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	apiReq := plan.toCreateRequest(ctx, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}

	instanceID := plan.InstanceID.ValueString()

	apiResp, err := r.client.Post(ctx, r.replicaPath(instanceID, ""), apiReq)
	if err != nil {
		resp.Diagnostics.AddError("Failed to create MySQL read replica", err.Error())
		return
	}

	replica, err := client.ParseResponse[apiMysqlReadReplica](apiResp)
	if err != nil {
		resp.Diagnostics.AddError("Failed to parse MySQL read replica response", err.Error())
		return
	}

	plan.fromAPI(ctx, replica, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}

	// Save state immediately so the ID is tracked.
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// Poll until the replica reaches "running" status.
	_, err = client.WaitForState(ctx, client.PollConfig{
		Interval:     r.getPollInterval(),
		Timeout:      r.getPollTimeout(),
		TargetStates: []string{"running"},
		ErrorStates:  []string{"error", "failed"},
		ResourceName: "mysql_read_replica",
		PollFunc: func(pollCtx context.Context) (string, error) {
			pollResp, pollErr := r.client.Get(pollCtx, r.replicaPath(instanceID, replica.ID), nil)
			if pollErr != nil {
				return "", pollErr
			}
			current, parseErr := client.ParseResponse[apiMysqlReadReplica](pollResp)
			if parseErr != nil {
				return "", parseErr
			}
			return current.Status, nil
		},
	})
	if err != nil {
		resp.Diagnostics.AddError("MySQL read replica failed to reach running state", err.Error())
		return
	}

	// Refresh state after polling.
	readResp, err := r.client.Get(ctx, r.replicaPath(instanceID, replica.ID), nil)
	if err != nil {
		resp.Diagnostics.AddError("Failed to read MySQL read replica after creation", err.Error())
		return
	}
	finalReplica, err := client.ParseResponse[apiMysqlReadReplica](readResp)
	if err != nil {
		resp.Diagnostics.AddError("Failed to parse MySQL read replica response", err.Error())
		return
	}

	plan.fromAPI(ctx, finalReplica, &resp.Diagnostics)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *mysqlReadReplicaResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state MysqlReadReplicaModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	apiResp, err := r.client.Get(ctx, r.replicaPath(state.InstanceID.ValueString(), state.ID.ValueString()), nil)
	if err != nil {
		if client.IsNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Failed to read MySQL read replica", err.Error())
		return
	}

	replica, err := client.ParseResponse[apiMysqlReadReplica](apiResp)
	if err != nil {
		resp.Diagnostics.AddError("Failed to parse MySQL read replica response", err.Error())
		return
	}

	state.fromAPI(ctx, replica, &resp.Diagnostics)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *mysqlReadReplicaResource) Update(_ context.Context, _ resource.UpdateRequest, resp *resource.UpdateResponse) {
	resp.Diagnostics.AddError(
		"Update Not Supported",
		"MySQL read replicas are immutable and cannot be updated. All attribute changes require resource replacement.",
	)
}

func (r *mysqlReadReplicaResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state MysqlReadReplicaModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	instanceID := state.InstanceID.ValueString()
	replicaID := state.ID.ValueString()

	_, err := r.client.Delete(ctx, r.replicaPath(instanceID, replicaID))
	if err != nil {
		if client.IsNotFound(err) {
			return
		}
		resp.Diagnostics.AddError("Failed to delete MySQL read replica", err.Error())
		return
	}

	// Wait for the replica to be fully deleted (404 on GET).
	_, err = client.WaitForState(ctx, client.PollConfig{
		Interval:     r.getPollInterval(),
		Timeout:      r.getPollTimeout(),
		TargetStates: []string{"deleted"},
		ErrorStates:  []string{"error"},
		ResourceName: "mysql_read_replica",
		PollFunc: func(pollCtx context.Context) (string, error) {
			pollResp, pollErr := r.client.Get(pollCtx, r.replicaPath(instanceID, replicaID), nil)
			if pollErr != nil {
				if client.IsNotFound(pollErr) {
					return "deleted", nil
				}
				return "", pollErr
			}
			current, parseErr := client.ParseResponse[apiMysqlReadReplica](pollResp)
			if parseErr != nil {
				return "", parseErr
			}
			return current.Status, nil
		},
	})
	if err != nil {
		resp.Diagnostics.AddError("MySQL read replica failed to delete", err.Error())
	}
}

func (r *mysqlReadReplicaResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}
