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
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/orphan"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/scopedecl"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/timeouts"
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

	// pollInterval and pollTimeout bound the waits for the provisioning
	// operations a write starts. Fields rather than constants so a test can
	// drive the timeout in milliseconds; the defaults are the values this
	// resource has always hardcoded (same idiom as frostmoln_vpc).
	pollInterval time.Duration
	pollTimeout  time.Duration
}

func (r *snapshotResource) getPollInterval() time.Duration {
	if r.pollInterval > 0 {
		return r.pollInterval
	}
	return 2 * time.Second
}

func (r *snapshotResource) getPollTimeout() time.Duration {
	if r.pollTimeout > 0 {
		return r.pollTimeout
	}
	return 10 * time.Minute
}

// resolveBudgets turns the configured timeouts block into effective budgets,
// falling back per verb to the same value this resource has always hardcoded.
// Routing the defaults through the accessor keeps the test-injection seam
// intact: a test that shrinks pollTimeout shrinks every wait that does not
// carry an explicit timeouts override, exactly as before.
func (r *snapshotResource) resolveBudgets(m *timeouts.Model) timeouts.Budgets {
	budgets, err := m.Resolve(timeouts.Uniform(r.getPollTimeout()))
	if err != nil {
		// Unreachable via HCL (the block validator rejects bad durations at
		// plan time); degrade to the defaults rather than fail a wait.
		return timeouts.Uniform(r.getPollTimeout())
	}
	return budgets
}

func (r *snapshotResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_snapshot"
}

func (r *snapshotResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manages a volume snapshot in the Frostmoln platform. Snapshots are immutable after creation." + "\n\n" + scopedecl.Summary("frostmoln_snapshot"),
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
		Blocks: map[string]schema.Block{
			// Customer-tunable wait budgets: defaults keep the values this
			// resource has always hardcoded (10m per verb). A timeouts change
			// is an in-place no-op on real infrastructure — verified by the
			// Gate 2 smoke test (project-docs/product/TF-CONVERGENCE-WALL-PLAN.md).
			"timeouts": timeouts.Schema(),
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

// apiSnapshotList is the snapshot family listing
// (GET /volumes/{volume}/snapshots) — the listing is ALREADY narrowed to the
// one volume the apply snapped, which is the pre-sweep narrowing; the window
// (name + created-at floor) does the rest. Items are the package's apiSnapshot.
type apiSnapshotList struct {
	Snapshots []apiSnapshot `json:"snapshots"`
}

// adoptCreatedObject runs the create-timeout arm of the orphan contract for a
// snapshot whose provisioning operation outlived the wait (internal/orphan:
// ADOPT-AS-TRACKED). Found = a fresh read of what the platform HAS, adopted
// after the apply timed out — the shared warning is orphan's — written through
// the package's own GET + fromAPI + Set flow. Verified absence = the
// re-apply-safe error. Unreadable = the platform's last word plus the
// `fm storage snapshot list` hint. It never invites `terraform state rm`.
//
// waitErr may be a real wait failure or the synthetic "completed but returned
// no resource ID" note the completed-but-unnamed arm passes in — either way it
// is what the wait ended with, and the diagnostics must carry it.
func (r *snapshotResource) adoptCreatedObject(ctx context.Context, plan *SnapshotModel, floor time.Time, waitErr error, resp *resource.CreateResponse) {
	adoptedID := orphan.AdoptCreateOnTimeout(ctx, &resp.Diagnostics, orphan.CreateParams{
		ResourceName: "Snapshot",
		FMList:       "`fm storage snapshot list`",
		WaitErr:      waitErr,
		Resolve: func(sweepCtx context.Context) (string, error) {
			collectionPath := "/volumes/" + plan.VolumeID.ValueString() + "/snapshots"
			listResp, err := r.client.Get(sweepCtx, r.client.TenantPath(collectionPath), nil)
			if err != nil {
				return "", err
			}
			listing, err := client.ParseResponse[apiSnapshotList](listResp)
			if err != nil {
				return "", fmt.Errorf("failed to parse snapshot list response: %w", err)
			}
			candidates := make([]orphan.Candidate, 0, len(listing.Snapshots))
			for _, snap := range listing.Snapshots {
				candidates = append(candidates, orphan.Candidate{ID: snap.ID, Name: snap.Name, CreatedAt: snap.CreatedAt})
			}
			return orphan.PickCreated(candidates, plan.Name.ValueString(), floor)
		},
	})
	if adoptedID == "" {
		return
	}

	// The honest read: the platform's response, not the configuration's
	// intent — the same GET + fromAPI + Set flow the ordinary create tail uses.
	memberPath := "/volumes/" + plan.VolumeID.ValueString() + "/snapshots/" + adoptedID
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
	resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
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
	//
	// When the wait gives up without the platform having said either yes or no,
	// the orphan contract's create arm decides: a terminal refusal is reported
	// as nothing-created, and everything unknown goes through the snapshot's own
	// family listing (ADOPT-AS-TRACKED) instead of an error that invites state
	// surgery. applyStarted is the created-at floor for that sweep.
	applyStarted := time.Now().UTC()
	floor := applyStarted.Add(-time.Minute)

	// The customer's timeouts block, with defaults identical to the values
	// this resource has always hardcoded.
	budgets := r.resolveBudgets(plan.Timeouts)

	var snapshotID string
	if apiResp.IsAccepted() {
		op, err := client.ParseResponse[client.Operation](apiResp)
		if err != nil {
			resp.Diagnostics.AddError("Failed to parse operation response", err.Error())
			return
		}
		done, waitErr := r.client.WaitForOperation(ctx, op.OperationID, r.getPollInterval(), budgets.Create)
		if waitErr != nil {
			if r.client.ClassifyOperationFailure(ctx, op.OperationID) == client.OperationRefused {
				orphan.AddCreateRefused(&resp.Diagnostics, "Snapshot", fmt.Sprintf("the snapshot %q", plan.Name.ValueString()), waitErr)
				return
			}
			// Unknown — the sweep decides. Return either way; the helper
			// wrote the state row or the diagnostic.
			r.adoptCreatedObject(ctx, &plan, floor, waitErr, resp)
			return
		}
		snapshotID = done.ResourceID
		if snapshotID == "" {
			// Completed but the envelope named no object: the sweep decides.
			r.adoptCreatedObject(ctx, &plan, floor,
				fmt.Errorf("the snapshot create operation completed but returned no resource ID"), resp)
			return
		}
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
		subject := state.ID.ValueString()

		// Delete waits on the timeouts block's delete budget, classified like
		// the rest of the surface: the row stays in both arms, and the copy
		// tells the practitioner which arm they are in.
		budgets := r.resolveBudgets(state.Timeouts)
		if _, err := r.client.WaitForOperation(ctx, op.OperationID, r.getPollInterval(), budgets.Delete); err != nil {
			orphan.AddDeleteOutcome(&resp.Diagnostics,
				r.client.ClassifyOperationFailure(ctx, op.OperationID),
				"Snapshot", subject, err)
		}
	}
}

func (r *snapshotResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}
