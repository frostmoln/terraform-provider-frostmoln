package volume

import (
	"context"
	"fmt"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/boolplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
)

var (
	_ resource.Resource                = &volumeResource{}
	_ resource.ResourceWithImportState = &volumeResource{}
)

// NewResource returns a new volume resource factory.
func NewResource() resource.Resource {
	return &volumeResource{}
}

type volumeResource struct {
	client *client.Client
}

func (r *volumeResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_volume"
}

func (r *volumeResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manages a block storage volume in the Frostmoln platform.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Description: "The unique identifier of the volume.",
				Computed:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"name": schema.StringAttribute{
				Description: "The name of the volume.",
				Required:    true,
			},
			"description": schema.StringAttribute{
				Description: "A human-readable description of the volume.",
				Optional:    true,
			},
			"size_gb": schema.Int64Attribute{
				Description: "The size of the volume in gigabytes. Can be increased after creation (resize).",
				Required:    true,
			},
			"volume_type": schema.StringAttribute{
				Description: "The type of volume. Valid values: ssd, hdd, nvme, premium.",
				Optional:    true,
				Computed:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"zone": schema.StringAttribute{
				Description: "The availability zone for the volume.",
				Optional:    true,
				Computed:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"snapshot_id": schema.StringAttribute{
				Description: "The snapshot ID to create the volume from.",
				Optional:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"encrypted": schema.BoolAttribute{
				Description: "Whether the volume is encrypted.",
				Optional:    true,
				Computed:    true,
				PlanModifiers: []planmodifier.Bool{
					boolplanmodifier.RequiresReplace(),
					boolplanmodifier.UseStateForUnknown(),
				},
			},
			"tags": schema.MapAttribute{
				Description: "Key-value tags for the volume.",
				Optional:    true,
				ElementType: types.StringType,
			},
			"status": schema.StringAttribute{
				Description: "The current status of the volume.",
				Computed:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"iops": schema.Int64Attribute{
				Description: "The provisioned IOPS of the volume.",
				Computed:    true,
				PlanModifiers: []planmodifier.Int64{
					int64planmodifier.UseStateForUnknown(),
				},
			},
			"throughput": schema.Int64Attribute{
				Description: "The throughput of the volume in MB/s.",
				Computed:    true,
				PlanModifiers: []planmodifier.Int64{
					int64planmodifier.UseStateForUnknown(),
				},
			},
			"region": schema.StringAttribute{
				Description: "The region where the volume is located.",
				Computed:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"attached_to": schema.StringAttribute{
				Description: "The instance ID the volume is attached to, if any.",
				Computed:    true,
			},
			"device_path": schema.StringAttribute{
				Description: "The device path on the instance, if attached.",
				Computed:    true,
			},
			"created_at": schema.StringAttribute{
				Description: "The timestamp when the volume was created.",
				Computed:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
		},
	}
}

func (r *volumeResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (r *volumeResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan VolumeModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	apiReq := plan.toCreateRequest(ctx, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}

	apiResp, err := r.client.Post(ctx, r.client.TenantPath("/volumes"), apiReq)
	if err != nil {
		resp.Diagnostics.AddError("Failed to create volume", err.Error())
		return
	}

	// Volume create routes through provisioning, which returns 202 with an
	// Operation envelope (operationId only, NOT the volume). Poll the operation to
	// completion (the workflow waits for the volume to reach available before
	// completing), then read by its resolved resourceId. A 201 with the volume
	// body is still accepted for a synchronous backend. Mirrors the snapshot +
	// load_balancer resources.
	var volumeID string
	if apiResp.IsAccepted() {
		op, err := client.ParseResponse[client.Operation](apiResp)
		if err != nil {
			resp.Diagnostics.AddError("Failed to parse operation response", err.Error())
			return
		}
		done, err := r.client.WaitForOperation(ctx, op.OperationID, 2*time.Second, 5*time.Minute)
		if err != nil {
			resp.Diagnostics.AddError("Volume creation failed", err.Error())
			return
		}
		volumeID = done.ResourceID
	} else {
		vol, err := client.ParseResponse[apiVolume](apiResp)
		if err != nil {
			resp.Diagnostics.AddError("Failed to parse volume response", err.Error())
			return
		}
		volumeID = vol.ID
	}
	if volumeID == "" {
		resp.Diagnostics.AddError(
			"Volume Operation Returned No Resource ID",
			"The volume create operation completed but returned no resource ID. The volume may exist in the backend without being tracked in Terraform state - check `fm storage volume list` and import it if necessary.",
		)
		return
	}

	// Read the final state.
	getResp, err := r.client.Get(ctx, r.client.TenantPath("/volumes/"+volumeID), nil)
	if err != nil {
		resp.Diagnostics.AddError("Failed to read volume after creation", err.Error())
		return
	}

	finalVol, err := client.ParseResponse[apiVolume](getResp)
	if err != nil {
		resp.Diagnostics.AddError("Failed to parse volume response", err.Error())
		return
	}

	plan.fromAPI(ctx, finalVol, &resp.Diagnostics)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *volumeResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state VolumeModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	apiResp, err := r.client.Get(ctx, r.client.TenantPath("/volumes/"+state.ID.ValueString()), nil)
	if err != nil {
		if client.IsNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Failed to read volume", err.Error())
		return
	}

	vol, err := client.ParseResponse[apiVolume](apiResp)
	if err != nil {
		resp.Diagnostics.AddError("Failed to parse volume response", err.Error())
		return
	}

	state.fromAPI(ctx, vol, &resp.Diagnostics)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *volumeResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan VolumeModel
	var state VolumeModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	volumeID := state.ID.ValueString()
	needsPatch := false

	var updateReq apiUpdateVolumeRequest

	// Check if name changed.
	if !plan.Name.Equal(state.Name) {
		name := plan.Name.ValueString()
		updateReq.Name = &name
		needsPatch = true
	}

	// Check if description changed.
	if !plan.Description.Equal(state.Description) {
		desc := plan.Description.ValueString()
		updateReq.Description = &desc
		needsPatch = true
	}

	// Check if tags changed.
	if !plan.Tags.Equal(state.Tags) {
		tags := make(map[string]string)
		if !plan.Tags.IsNull() && !plan.Tags.IsUnknown() {
			resp.Diagnostics.Append(plan.Tags.ElementsAs(ctx, &tags, false)...)
			if resp.Diagnostics.HasError() {
				return
			}
		}
		updateReq.Tags = tags
		needsPatch = true
	}

	if needsPatch {
		_, err := r.client.Patch(ctx, r.client.TenantPath("/volumes/"+volumeID), updateReq)
		if err != nil {
			resp.Diagnostics.AddError("Failed to update volume", err.Error())
			return
		}
	}

	// Check if size_gb increased (resize).
	if plan.SizeGB.ValueInt64() > state.SizeGB.ValueInt64() {
		resizeReq := apiResizeVolumeRequest{
			SizeGB: int(plan.SizeGB.ValueInt64()),
		}
		resizeResp, err := r.client.Post(ctx, r.client.TenantPath("/volumes/"+volumeID+"/resize"), resizeReq)
		if err != nil {
			resp.Diagnostics.AddError("Failed to resize volume", err.Error())
			return
		}

		// Resize is async (provisioning 202 + Operation): poll the operation to
		// completion (the workflow waits for the volume to return to available).
		if resizeResp.IsAccepted() {
			op, err := client.ParseResponse[client.Operation](resizeResp)
			if err != nil {
				resp.Diagnostics.AddError("Failed to parse operation response", err.Error())
				return
			}
			if _, err := r.client.WaitForOperation(ctx, op.OperationID, 2*time.Second, 5*time.Minute); err != nil {
				resp.Diagnostics.AddError("Volume resize failed", err.Error())
				return
			}
		}
	}

	// Read the final state.
	getResp, err := r.client.Get(ctx, r.client.TenantPath("/volumes/"+volumeID), nil)
	if err != nil {
		resp.Diagnostics.AddError("Failed to read volume after update", err.Error())
		return
	}

	vol, err := client.ParseResponse[apiVolume](getResp)
	if err != nil {
		resp.Diagnostics.AddError("Failed to parse volume response", err.Error())
		return
	}

	plan.fromAPI(ctx, vol, &resp.Diagnostics)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *volumeResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state VolumeModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	apiResp, err := r.client.Delete(ctx, r.client.TenantPath("/volumes/"+state.ID.ValueString()))
	if err != nil {
		if client.IsNotFound(err) {
			return
		}
		resp.Diagnostics.AddError("Failed to delete volume", err.Error())
		return
	}

	// Async delete (provisioning 202 + Operation): poll until the workflow has
	// verified the volume is actually gone before dropping it from state.
	if apiResp.IsAccepted() {
		op, err := client.ParseResponse[client.Operation](apiResp)
		if err != nil {
			resp.Diagnostics.AddError("Failed to parse operation response", err.Error())
			return
		}
		if _, err := r.client.WaitForOperation(ctx, op.OperationID, 2*time.Second, 5*time.Minute); err != nil {
			resp.Diagnostics.AddError("Volume deletion failed", err.Error())
		}
	}
}

func (r *volumeResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}
