package snapshot

import (
	"context"
	"fmt"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/mapplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
)

var (
	_ resource.Resource                = &snapshotResource{}
	_ resource.ResourceWithImportState = &snapshotResource{}
)

// NewResource returns a new snapshot resource factory.
func NewResource() resource.Resource {
	return &snapshotResource{}
}

type snapshotResource struct {
	client *client.Client
}

func (r *snapshotResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_snapshot"
}

func (r *snapshotResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manages a volume snapshot in the Frostmoln platform. Snapshots are immutable after creation.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Description: "The unique identifier of the snapshot.",
				Computed:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"name": schema.StringAttribute{
				Description: "The name of the snapshot.",
				Required:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"description": schema.StringAttribute{
				Description: "A human-readable description of the snapshot.",
				Optional:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"volume_id": schema.StringAttribute{
				Description: "The ID of the volume to create the snapshot from.",
				Required:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"tags": schema.MapAttribute{
				Description: "Key-value tags for the snapshot.",
				Optional:    true,
				ElementType: types.StringType,
				PlanModifiers: []planmodifier.Map{
					mapplanmodifier.RequiresReplace(),
				},
			},
			"status": schema.StringAttribute{
				Description: "The current status of the snapshot.",
				Computed:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"size_gb": schema.Int64Attribute{
				Description: "The size of the snapshot in gigabytes.",
				Computed:    true,
				PlanModifiers: []planmodifier.Int64{
					int64planmodifier.UseStateForUnknown(),
				},
			},
			"created_at": schema.StringAttribute{
				Description: "The timestamp when the snapshot was created.",
				Computed:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
		},
	}
}

func (r *snapshotResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (r *snapshotResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan SnapshotModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	apiReq := plan.toCreateRequest(ctx, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}

	// Volume snapshots are nested under their volume (ADR-0065).
	collectionPath := "/volumes/" + plan.VolumeID.ValueString() + "/snapshots"

	apiResp, err := r.client.Post(ctx, r.client.TenantPath(collectionPath), apiReq)
	if err != nil {
		resp.Diagnostics.AddError("Failed to create snapshot", err.Error())
		return
	}

	// Snapshot writes route through provisioning, which returns 202 with an
	// Operation envelope (operationId only, not the snapshot). Poll the operation
	// to completion, then read the snapshot by its resolved resourceId. A 201 with
	// the snapshot body is also accepted for a synchronous backend.
	var snapshotID string
	if apiResp.IsAccepted() {
		op, err := client.ParseResponse[client.Operation](apiResp)
		if err != nil {
			resp.Diagnostics.AddError("Failed to parse operation response", err.Error())
			return
		}
		done, err := r.client.WaitForOperation(ctx, op.OperationID, 2*time.Second, 10*time.Minute)
		if err != nil {
			resp.Diagnostics.AddError("Snapshot creation failed", err.Error())
			return
		}
		snapshotID = done.ResourceID
	} else {
		snap, err := client.ParseResponse[apiSnapshot](apiResp)
		if err != nil {
			resp.Diagnostics.AddError("Failed to parse snapshot response", err.Error())
			return
		}
		snapshotID = snap.ID
	}
	if snapshotID == "" {
		resp.Diagnostics.AddError(
			"Snapshot Operation Returned No Resource ID",
			"The snapshot create operation completed but returned no resource ID. The snapshot may exist in the backend without being tracked in Terraform state - check `fm storage snapshot list --volume "+plan.VolumeID.ValueString()+"` and import it if necessary.",
		)
		return
	}

	memberPath := collectionPath + "/" + snapshotID

	// Read the final state.
	getResp, err := r.client.Get(ctx, r.client.TenantPath(memberPath), nil)
	if err != nil {
		resp.Diagnostics.AddError("Failed to read snapshot after creation", err.Error())
		return
	}

	finalSnap, err := client.ParseResponse[apiSnapshot](getResp)
	if err != nil {
		resp.Diagnostics.AddError("Failed to parse snapshot response", err.Error())
		return
	}

	plan.fromAPI(ctx, finalSnap, &resp.Diagnostics)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *snapshotResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state SnapshotModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	apiResp, err := r.client.Get(ctx, r.client.TenantPath("/volumes/"+state.VolumeID.ValueString()+"/snapshots/"+state.ID.ValueString()), nil)
	if err != nil {
		if client.IsNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Failed to read snapshot", err.Error())
		return
	}

	snap, err := client.ParseResponse[apiSnapshot](apiResp)
	if err != nil {
		resp.Diagnostics.AddError("Failed to parse snapshot response", err.Error())
		return
	}

	state.fromAPI(ctx, snap, &resp.Diagnostics)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *snapshotResource) Update(_ context.Context, _ resource.UpdateRequest, resp *resource.UpdateResponse) {
	resp.Diagnostics.AddError(
		"Update Not Supported",
		"Snapshots are immutable and cannot be updated. All attribute changes require resource replacement.",
	)
}

func (r *snapshotResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state SnapshotModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	apiResp, err := r.client.Delete(ctx, r.client.TenantPath("/volumes/"+state.VolumeID.ValueString()+"/snapshots/"+state.ID.ValueString()))
	if err != nil {
		if client.IsNotFound(err) {
			return
		}
		resp.Diagnostics.AddError("Failed to delete snapshot", err.Error())
		return
	}

	// Async delete (provisioning 202 + Operation): poll until the workflow has
	// verified the snapshot is actually gone before dropping it from state.
	if apiResp.IsAccepted() {
		op, err := client.ParseResponse[client.Operation](apiResp)
		if err != nil {
			resp.Diagnostics.AddError("Failed to parse operation response", err.Error())
			return
		}
		if _, err := r.client.WaitForOperation(ctx, op.OperationID, 2*time.Second, 10*time.Minute); err != nil {
			resp.Diagnostics.AddError("Snapshot deletion failed", err.Error())
		}
	}
}

func (r *snapshotResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}
