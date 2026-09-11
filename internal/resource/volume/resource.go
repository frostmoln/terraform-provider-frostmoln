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
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/orphan"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/scopedecl"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/timeouts"
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

	// pollInterval and pollTimeout bound the waits for the provisioning
	// operations a write starts. Fields rather than constants so a test can
	// drive the timeout in milliseconds; the defaults are the values this
	// resource has always hardcoded (same idiom as frostmoln_vpc).
	pollInterval time.Duration
	pollTimeout  time.Duration
}

func (r *volumeResource) getPollInterval() time.Duration {
	if r.pollInterval > 0 {
		return r.pollInterval
	}
	return 2 * time.Second
}

func (r *volumeResource) getPollTimeout() time.Duration {
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
func (r *volumeResource) resolveBudgets(m *timeouts.Model) timeouts.Budgets {
	budgets, err := m.Resolve(timeouts.Uniform(r.getPollTimeout()))
	if err != nil {
		// Unreachable via HCL (the block validator rejects bad durations at
		// plan time); degrade to the defaults rather than fail a wait.
		return timeouts.Uniform(r.getPollTimeout())
	}
	return budgets
}

func (r *volumeResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_volume"
}

func (r *volumeResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manages a block storage volume in the Frostmoln platform." + "\n\n" + scopedecl.Summary("frostmoln_volume"),
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
				Description: "The volume tier key (e.g. \"ssd\"). The set of selectable tiers is server-defined and may change without a provider release — read it from the frostmoln_volume_tiers data source (only tiers with status \"offered\" are accepted; a non-offered tier is rejected by the API). Defaults to the platform default tier when omitted.",
				Optional:    true,
				Computed:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
					stringplanmodifier.RequiresReplace(),
				},
			},
			"zone": schema.StringAttribute{
				Description: "The availability zone for the volume.",
				Optional:    true,
				Computed:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
					stringplanmodifier.RequiresReplace(),
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
				Description: "Whether the volume is encrypted. Volume encryption is not available yet — " +
					"setting this to true is rejected at apply time.",
				Optional: true,
				Computed: true,
				PlanModifiers: []planmodifier.Bool{
					boolplanmodifier.UseStateForUnknown(),
					boolplanmodifier.RequiresReplace(),
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
		Blocks: map[string]schema.Block{
			// Customer-tunable wait budgets: defaults keep the values this
			// resource has always hardcoded (5m per verb). A timeouts change
			// is an in-place no-op on real infrastructure — verified by the
			// Gate 2 smoke test (project-docs/product/TF-CONVERGENCE-WALL-PLAN.md).
			"timeouts": timeouts.Schema(),
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

// apiVolumeList is the volume family listing (GET /volumes) — only the
// envelope the orphan sweep reads; the volume items are the package's apiVolume.
type apiVolumeList struct {
	Volumes []apiVolume `json:"volumes"`
}

// adoptCreatedObject runs the create-timeout arm of the orphan contract for a
// volume whose provisioning operation outlived the wait (internal/orphan:
// ADOPT-AS-TRACKED). Found = a fresh read of what the platform HAS, adopted
// after the apply timed out — the shared warning is orphan's — written through
// the package's own GET + fromAPI + Set flow. Verified absence = the
// re-apply-safe error. Unreadable = the platform's last word plus the
// `fm storage volume list` hint. It never invites `terraform state rm`.
//
// waitErr may be a real wait failure or the synthetic "completed but returned
// no resource ID" note the completed-but-unnamed arm passes in — either way it
// is what the wait ended with, and the diagnostics must carry it.
func (r *volumeResource) adoptCreatedObject(ctx context.Context, plan *VolumeModel, floor time.Time, waitErr error, resp *resource.CreateResponse) {
	adoptedID := orphan.AdoptCreateOnTimeout(ctx, &resp.Diagnostics, orphan.CreateParams{
		ResourceName: "Volume",
		FMList:       "`fm storage volume list`",
		WaitErr:      waitErr,
		Resolve: func(sweepCtx context.Context) (string, error) {
			listResp, err := r.client.Get(sweepCtx, r.client.TenantPath("/volumes"), nil)
			if err != nil {
				return "", err
			}
			listing, err := client.ParseResponse[apiVolumeList](listResp)
			if err != nil {
				return "", fmt.Errorf("failed to parse volume list response: %w", err)
			}
			candidates := make([]orphan.Candidate, 0, len(listing.Volumes))
			for _, vol := range listing.Volumes {
				candidates = append(candidates, orphan.Candidate{ID: vol.ID, Name: vol.Name, CreatedAt: vol.CreatedAt})
			}
			return orphan.PickCreated(candidates, plan.Name.ValueString(), floor)
		},
	})
	if adoptedID == "" {
		return
	}

	// The honest read: the platform's response, not the configuration's
	// intent — the same GET + fromAPI + Set flow the ordinary create tail uses.
	getResp, err := r.client.Get(ctx, r.client.TenantPath("/volumes/"+adoptedID), nil)
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
	resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
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
	//
	// When the wait gives up without the platform having said either yes or no,
	// the orphan contract's create arm decides: a terminal refusal is reported
	// as nothing-created, and everything unknown goes through the name/list
	// sweep (ADOPT-AS-TRACKED) instead of an error that invites state surgery.
	// applyStarted is the created-at floor for that sweep: a volume that
	// existed before this apply is never adopted by mistake.
	applyStarted := time.Now().UTC()
	floor := applyStarted.Add(-time.Minute)

	// The customer's timeouts block, with defaults identical to the values
	// this resource has always hardcoded.
	budgets := r.resolveBudgets(plan.Timeouts)

	var volumeID string
	if apiResp.IsAccepted() {
		op, err := client.ParseResponse[client.Operation](apiResp)
		if err != nil {
			resp.Diagnostics.AddError("Failed to parse operation response", err.Error())
			return
		}
		done, waitErr := r.client.WaitForOperation(ctx, op.OperationID, r.getPollInterval(), budgets.Create)
		if waitErr != nil {
			if r.client.ClassifyOperationFailure(ctx, op.OperationID) == client.OperationRefused {
				orphan.AddCreateRefused(&resp.Diagnostics, "Volume", fmt.Sprintf("the volume %q", plan.Name.ValueString()), waitErr)
				return
			}
			// Unknown — the sweep decides: adopt, verified absence, or the
			// unreadable arm. Return either way; the helper wrote the state
			// row or the diagnostic.
			r.adoptCreatedObject(ctx, &plan, floor, waitErr, resp)
			return
		}
		volumeID = done.ResourceID
		if volumeID == "" {
			// Completed but the envelope named no object: the sweep decides.
			r.adoptCreatedObject(ctx, &plan, floor,
				fmt.Errorf("the volume create operation completed but returned no resource ID"), resp)
			return
		}
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

	// The customer's timeouts block, with defaults identical to the values
	// this resource has always hardcoded.
	budgets := r.resolveBudgets(plan.Timeouts)

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
		updateReq.Metadata = tags
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
			NewSizeGB: int(plan.SizeGB.ValueInt64()),
		}
		resizeResp, err := r.client.Post(ctx, r.client.TenantPath("/volumes/"+volumeID+"/resize"), resizeReq)
		if err != nil {
			resp.Diagnostics.AddError("Failed to resize volume", err.Error())
			return
		}

		// Resize is async (provisioning 202 + Operation): poll the operation to
		// completion (the workflow waits for the volume to return to available),
		// on the timeouts block's update budget.
		if resizeResp.IsAccepted() {
			op, err := client.ParseResponse[client.Operation](resizeResp)
			if err != nil {
				resp.Diagnostics.AddError("Failed to parse operation response", err.Error())
				return
			}
			if _, err := r.client.WaitForOperation(ctx, op.OperationID, r.getPollInterval(), budgets.Update); err != nil {
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
	// verified the volume is actually gone before dropping it from state, on
	// the timeouts block's delete budget.
	if apiResp.IsAccepted() {
		op, err := client.ParseResponse[client.Operation](apiResp)
		if err != nil {
			resp.Diagnostics.AddError("Failed to parse operation response", err.Error())
			return
		}
		subject := state.ID.ValueString()

		// Delete waits on the timeouts block's delete budget, and the outcome
		// is classified — a refused destroy says so and keeps the row, an
		// unknown one tells the practitioner to refresh before assuming either
		// way. Never silently, and never `terraform state rm`.
		budgets := r.resolveBudgets(state.Timeouts)
		if _, err := r.client.WaitForOperation(ctx, op.OperationID, r.getPollInterval(), budgets.Delete); err != nil {
			orphan.AddDeleteOutcome(&resp.Diagnostics,
				r.client.ClassifyOperationFailure(ctx, op.OperationID),
				"Volume", subject, err)
		}
	}
}

func (r *volumeResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}
