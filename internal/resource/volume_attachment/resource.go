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

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/orphan"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/scopedecl"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/timeouts"
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

	// pollInterval and pollTimeout bound the waits for the provisioning
	// operations (and, on a non-202 backend, the volume status polls) a write
	// starts. Fields rather than constants so a test can drive the timeout in
	// milliseconds; the timeouts block sits on top of these defaults (see
	// resolveBudgets).
	pollInterval time.Duration
	pollTimeout  time.Duration
}

func (r *volumeAttachmentResource) getPollInterval() time.Duration {
	if r.pollInterval > 0 {
		return r.pollInterval
	}
	return 2 * time.Second
}

// getPollTimeout is the DEFAULT wait budget — the timeouts block's fallback
// per verb. Both the attach and the detach have always waited on the volume
// against 5m; the operation waits default to the same value so the resource
// keeps one number.
func (r *volumeAttachmentResource) getPollTimeout() time.Duration {
	if r.pollTimeout > 0 {
		return r.pollTimeout
	}
	return 5 * time.Minute
}

// resolveBudgets turns the configured timeouts block into effective budgets,
// falling back per verb to the same value this resource has always hardcoded.
// Routing the defaults through the accessor keeps the test-injection seam
// intact: a test that shrinks pollTimeout shrinks every wait that does not
// carry an explicit timeouts override, exactly as before.
func (r *volumeAttachmentResource) resolveBudgets(m *timeouts.Model) timeouts.Budgets {
	budgets, err := m.Resolve(timeouts.Uniform(r.getPollTimeout()))
	if err != nil {
		// Unreachable via HCL (the block validator rejects bad durations at
		// plan time); degrade to the defaults rather than fail a wait.
		return timeouts.Uniform(r.getPollTimeout())
	}
	return budgets
}

func (r *volumeAttachmentResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_volume_attachment"
}

func (r *volumeAttachmentResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manages a volume attachment to a compute instance in the Frostmoln platform." + "\n\n" + scopedecl.Summary("frostmoln_volume_attachment"),
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
					stringplanmodifier.UseStateForUnknown(),
					stringplanmodifier.RequiresReplace(),
				},
			},
		},
		Blocks: map[string]schema.Block{
			// Customer-tunable wait budgets: defaults keep the values this
			// resource has always hardcoded (5m per verb). A timeouts change
			// is an in-place no-op on real infrastructure — verified by the
			// Gate 2 smoke test (project-docs/product/TF-CONVERGENCE-WALL-PLAN.md).
			"timeouts": timeouts.Schema(),
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

// pollVolumeStatus waits for the volume to reach the wanted state. This is
// the FALLBACK branch for a non-202 backend (a synchronous enactor); on the
// 202 path the provisioning operation IS the verdict and this poll is not
// run. A volume 404 altogether answers "available" — the volume was deleted
// externally, which is the detach's desired end state.
func (r *volumeAttachmentResource) pollVolumeStatus(ctx context.Context, volumeID, target string, budget time.Duration) error {
	_, err := client.WaitForState(ctx, client.PollConfig{
		Interval:     r.getPollInterval(),
		Timeout:      budget,
		TargetStates: []string{target},
		ErrorStates:  []string{"error"},
		ResourceName: "volume attachment",
		PollFunc: func(ctx context.Context) (string, error) {
			pollResp, err := r.client.Get(ctx, r.client.TenantPath("/volumes/"+volumeID), nil)
			if err != nil {
				if client.IsNotFound(err) {
					// Volume was deleted externally, treat as success.
					return target, nil
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
	return err
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
	subject := fmt.Sprintf("%s/%s", volumeID, instanceID)
	budgets := r.resolveBudgets(plan.Timeouts)

	attachResp, err := r.client.Post(ctx, r.client.TenantPath("/volumes/"+volumeID+"/attach"), attachReq)
	if err != nil {
		resp.Diagnostics.AddError("Failed to attach volume", err.Error())
		return
	}

	// The attach routes through provisioning → 202 + an Operation envelope,
	// which this resource DISCARDED for its whole life: the wait ran against
	// the VOLUME status while the operation carrying the workflow's actual
	// verdict (and its error prose and errorCode) was dropped on the floor. A
	// 202 is an intent receipt, so poll it; the attach-match read below stays
	// as the verify step. A non-202 body is a synchronous backend and falls
	// back to the volume status poll.
	if attachResp.IsAccepted() {
		op, opErr := client.ParseResponse[client.Operation](attachResp)
		if opErr != nil {
			resp.Diagnostics.AddError(
				"Volume Attach Was Accepted But Could Not Be Tracked",
				fmt.Sprintf("The platform accepted the attach of volume %s to instance %s and returned an "+
					"operation this provider could not read, so it has not been watched to completion. "+
					"Nothing has been recorded in Terraform state.\n\nCheck whether the attach landed — "+
					"`fm storage volume list` — and `terraform import frostmoln_volume_attachment.<name> %s` "+
					"if it did. Do NOT run `terraform state rm`.\n\nThe response could not be parsed with: %s",
					volumeID, instanceID, subject, opErr.Error()),
			)
			return
		}
		if _, waitErr := r.client.WaitForOperation(ctx, op.OperationID, r.getPollInterval(), budgets.Create); waitErr != nil {
			if r.client.ClassifyOperationFailure(ctx, op.OperationID) == client.OperationRefused {
				resp.Diagnostics.AddError(
					"Volume Attach Was Refused By The Platform",
					fmt.Sprintf("The platform refused to attach volume %s to instance %s: nothing was "+
						"attached and nothing has been recorded in Terraform state, so applying again is "+
						"safe once the reason below is dealt with.\n\nThe platform said: %s",
						volumeID, instanceID, waitErr.Error()),
				)
				return
			}
			// Unknown: the attach may still land. The read below decides
			// honestly — it finds the attachment only if the volume now
			// carries it.
			resp.Diagnostics.AddWarning(
				"Volume Attach Took Longer Than The Provider Waited",
				fmt.Sprintf("The wait for the attach of volume %s to instance %s gave up; the operation "+
					"may still be running. Reading the volume back to see whether the attachment landed.\n\n"+
					"The wait gave up with: %s", volumeID, instanceID, waitErr.Error()),
			)
		}
	} else if err := r.pollVolumeStatus(ctx, volumeID, "in-use", budgets.Create); err != nil {
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

	att := vol.findAttachment(instanceID)
	if att == nil {
		resp.Diagnostics.AddError(
			"Volume attachment mismatch",
			fmt.Sprintf("Expected volume %s to be attached to instance %s, but no matching attachment was found "+
				"(or the attach is still in flight — check `fm storage volume list` and import the "+
				"attachment if it landed: `terraform import frostmoln_volume_attachment.<name> %s`)",
				volumeID, instanceID, subject),
		)
		return
	}

	plan.fromAttachment(volumeID, att)
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
	att := vol.findAttachment(instanceID)
	if att == nil {
		resp.State.RemoveResource(ctx)
		return
	}

	state.fromAttachment(volumeID, att)
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
	instanceID := state.InstanceID.ValueString()
	subject := fmt.Sprintf("%s/%s", volumeID, instanceID)
	budgets := r.resolveBudgets(state.Timeouts)

	detachReq := apiDetachRequest{Force: false}
	detachResp, err := r.client.Post(ctx, r.client.TenantPath("/volumes/"+volumeID+"/detach"), detachReq)
	if err != nil {
		if client.IsNotFound(err) {
			// The volume is gone: there is no attachment to detach, which is
			// the desired end state (verified absence — the classified form of
			// "nothing to do").
			return
		}
		resp.Diagnostics.AddError("Failed to detach volume", err.Error())
		return
	}

	// The detach routes through provisioning → 202 + an Operation envelope,
	// which this resource DISCARDED for its whole life. Returning on the 202
	// would drop the row while the workflow is still running, so wait for its
	// verdict — classified, never silently. A non-202 is a synchronous backend:
	// fall back to the volume status poll, where a volume 404 altogether is
	// the detach's desired end state.
	if detachResp.IsAccepted() {
		op, opErr := client.ParseResponse[client.Operation](detachResp)
		if opErr != nil || op.OperationID == "" {
			// The destroy was accepted and its workflow cannot be watched
			// from here. That is classified — NOT a success and NOT a
			// verified absence.
			unwatched := opErr
			if unwatched == nil {
				unwatched = fmt.Errorf("the detach was accepted but returned no operation id")
			}
			orphan.AddDeleteOutcome(&resp.Diagnostics, client.OperationUnknown, "Volume Attachment", subject, unwatched)
			return
		}
		if _, waitErr := r.client.WaitForOperation(ctx, op.OperationID, r.getPollInterval(), budgets.Delete); waitErr != nil {
			orphan.AddDeleteOutcome(&resp.Diagnostics,
				r.client.ClassifyOperationFailure(ctx, op.OperationID),
				"Volume Attachment", subject, waitErr)
		}
		return
	}
	if err := r.pollVolumeStatus(ctx, volumeID, "available", budgets.Delete); err != nil {
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
