package volume_attachment

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"

	"git.nl.cloud/NordicLight/terraform-provider-frostmoln/internal/client"
)

var (
	_ resource.Resource                = &volumeAttachmentResource{}
	_ resource.ResourceWithImportState = &volumeAttachmentResource{}
)

// NewResource returns a new volume attachment resource factory.
func NewResource() resource.Resource {
	return &volumeAttachmentResource{}
}

type volumeAttachmentResource struct {
	client *client.Client
}

func (r *volumeAttachmentResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_volume_attachment"
}

func (r *volumeAttachmentResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manages a volume attachment to a compute instance in the NordicLight platform.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Description: "The composite identifier of the attachment ({volume_id}/{instance_id}).",
				Computed:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"volume_id": schema.StringAttribute{
				Description: "The ID of the volume to attach.",
				Required:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"instance_id": schema.StringAttribute{
				Description: "The ID of the instance to attach the volume to.",
				Required:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"device_path": schema.StringAttribute{
				Description: "The device path on the instance (e.g., /dev/vdb). Optional on create, computed from response.",
				Optional:    true,
				Computed:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
					stringplanmodifier.UseStateForUnknown(),
				},
			},
		},
	}
}

func (r *volumeAttachmentResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (r *volumeAttachmentResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan VolumeAttachmentModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	volumeID := plan.VolumeID.ValueString()
	instanceID := plan.InstanceID.ValueString()
	attachReq := plan.toAttachRequest()

	_, err := r.client.Post(ctx, r.client.TenantPath("/volumes/"+volumeID+"/attach"), attachReq)
	if err != nil {
		resp.Diagnostics.AddError("Failed to attach volume", err.Error())
		return
	}

	// Wait for the volume to reach "in-use" state.
	_, err = client.WaitForState(ctx, client.PollConfig{
		Interval:     2 * time.Second,
		Timeout:      5 * time.Minute,
		TargetStates: []string{"in-use"},
		ErrorStates:  []string{"error"},
		ResourceName: "volume attachment",
		PollFunc: func(ctx context.Context) (string, error) {
			pollResp, err := r.client.Get(ctx, r.client.TenantPath("/volumes/"+volumeID), nil)
			if err != nil {
				return "", err
			}
			vol, err := client.ParseResponse[apiVolume](pollResp)
			if err != nil {
				return "", err
			}
			return vol.Status, nil
		},
	})
	if err != nil {
		resp.Diagnostics.AddError("Volume attachment failed", err.Error())
		return
	}

	// Read the final state.
	getResp, err := r.client.Get(ctx, r.client.TenantPath("/volumes/"+volumeID), nil)
	if err != nil {
		resp.Diagnostics.AddError("Failed to read volume after attach", err.Error())
		return
	}

	vol, err := client.ParseResponse[apiVolume](getResp)
	if err != nil {
		resp.Diagnostics.AddError("Failed to parse volume response", err.Error())
		return
	}

	if vol.AttachedTo != instanceID {
		resp.Diagnostics.AddError(
			"Volume attachment mismatch",
			fmt.Sprintf("Expected volume to be attached to %s, but found %s", instanceID, vol.AttachedTo),
		)
		return
	}

	plan.fromAPI(vol)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *volumeAttachmentResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state VolumeAttachmentModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	volumeID := state.VolumeID.ValueString()
	instanceID := state.InstanceID.ValueString()

	apiResp, err := r.client.Get(ctx, r.client.TenantPath("/volumes/"+volumeID), nil)
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

	// If the volume is no longer attached to the expected instance, remove the resource.
	if vol.AttachedTo != instanceID {
		resp.State.RemoveResource(ctx)
		return
	}

	state.fromAPI(vol)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *volumeAttachmentResource) Update(_ context.Context, _ resource.UpdateRequest, resp *resource.UpdateResponse) {
	resp.Diagnostics.AddError(
		"Update Not Supported",
		"Volume attachments cannot be updated. All attribute changes require resource replacement.",
	)
}

func (r *volumeAttachmentResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state VolumeAttachmentModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	volumeID := state.VolumeID.ValueString()

	detachReq := apiDetachRequest{Force: false}
	_, err := r.client.Post(ctx, r.client.TenantPath("/volumes/"+volumeID+"/detach"), detachReq)
	if err != nil {
		if client.IsNotFound(err) {
			return
		}
		resp.Diagnostics.AddError("Failed to detach volume", err.Error())
		return
	}

	// Wait for the volume to reach "available" state (detached).
	_, err = client.WaitForState(ctx, client.PollConfig{
		Interval:     2 * time.Second,
		Timeout:      5 * time.Minute,
		TargetStates: []string{"available"},
		ErrorStates:  []string{"error"},
		ResourceName: "volume detachment",
		PollFunc: func(ctx context.Context) (string, error) {
			pollResp, err := r.client.Get(ctx, r.client.TenantPath("/volumes/"+volumeID), nil)
			if err != nil {
				if client.IsNotFound(err) {
					// Volume was deleted externally, treat as success.
					return "available", nil
				}
				return "", err
			}
			vol, err := client.ParseResponse[apiVolume](pollResp)
			if err != nil {
				return "", err
			}
			return vol.Status, nil
		},
	})
	if err != nil {
		resp.Diagnostics.AddError("Volume detachment failed", err.Error())
	}
}

func (r *volumeAttachmentResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	parts := strings.SplitN(req.ID, "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		resp.Diagnostics.AddError(
			"Invalid Import ID",
			fmt.Sprintf("Expected import ID in the format {volume_id}/{instance_id}, got: %s", req.ID),
		)
		return
	}

	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("volume_id"), parts[0])...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("instance_id"), parts[1])...)
}
