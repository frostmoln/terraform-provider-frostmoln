package scale_group

import (
	"context"
	"fmt"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64default"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/mapplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/setplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/orphan"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/scopedecl"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/timeouts"
)

var (
	_ resource.Resource                = &scaleGroupResource{}
	_ resource.ResourceWithImportState = &scaleGroupResource{}
)

// NewResource returns a new scale group resource factory.
func NewResource() resource.Resource {
	return &scaleGroupResource{}
}

type scaleGroupResource struct {
	client       *client.Client
	pollInterval time.Duration
	pollTimeout  time.Duration
}

func (r *scaleGroupResource) getPollInterval() time.Duration {
	if r.pollInterval > 0 {
		return r.pollInterval
	}
	return 5 * time.Second
}

func (r *scaleGroupResource) getPollTimeout() time.Duration {
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
func (r *scaleGroupResource) resolveBudgets(m *timeouts.Model) timeouts.Budgets {
	budgets, err := m.Resolve(timeouts.Uniform(r.getPollTimeout()))
	if err != nil {
		// Unreachable via HCL (the block validator rejects bad durations at
		// plan time); degrade to the defaults rather than fail a wait.
		return timeouts.Uniform(r.getPollTimeout())
	}
	return budgets
}

func (r *scaleGroupResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_scale_group"
}

func (r *scaleGroupResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manages an auto-scaling group in the Frostmoln platform." + "\n\n" + scopedecl.Summary("frostmoln_scale_group"),
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Description: "The unique identifier of the scale group.",
				Computed:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"name": schema.StringAttribute{
				Description: "The name of the scale group.",
				Required:    true,
			},
			"launch_template_id": schema.StringAttribute{
				Description: "The launch template ID used to create instances in this scale group.",
				Required:    true,
			},
			"min_size": schema.Int64Attribute{
				Description: "The minimum number of instances in the scale group.",
				Required:    true,
			},
			"max_size": schema.Int64Attribute{
				Description: "The maximum number of instances in the scale group.",
				Required:    true,
			},
			"desired_capacity": schema.Int64Attribute{
				Description: "The desired number of instances in the scale group.",
				Required:    true,
			},
			"current_size": schema.Int64Attribute{
				Description: "The current number of running instances in the scale group.",
				Computed:    true,
			},
			"status": schema.StringAttribute{
				Description: "The current status of the scale group.",
				Computed:    true,
			},
			"subnet_ids": schema.SetAttribute{
				Description: "The subnet IDs where instances will be launched. At least one is required.",
				Required:    true,
				ElementType: types.StringType,
				PlanModifiers: []planmodifier.Set{
					setplanmodifier.UseStateForUnknown(),
				},
			},
			"load_balancer_pool_ids": schema.SetAttribute{
				Description: "The load balancer pool IDs to attach to the scale group.",
				Optional:    true,
				ElementType: types.StringType,
				PlanModifiers: []planmodifier.Set{
					setplanmodifier.UseStateForUnknown(),
				},
			},
			"health_check_type": schema.StringAttribute{
				Description: "The type of health check to use: \"instance\", \"lb\", or \"both\".",
				Optional:    true,
				Computed:    true,
				Default:     stringdefault.StaticString("instance"),
			},
			"health_check_grace_period": schema.Int64Attribute{
				Description: "The number of seconds to wait before starting health checks on new instances.",
				Optional:    true,
				Computed:    true,
				Default:     int64default.StaticInt64(300),
			},
			"warmup_seconds": schema.Int64Attribute{
				Description: "The number of seconds to wait for a new instance to warm up before it receives traffic.",
				Optional:    true,
				Computed:    true,
				Default:     int64default.StaticInt64(0),
			},
			"cooldown_seconds": schema.Int64Attribute{
				Description: "The number of seconds after a scaling activity completes before another can start.",
				Optional:    true,
				Computed:    true,
				Default:     int64default.StaticInt64(300),
			},
			"termination_policy": schema.StringAttribute{
				Description: "The policy for selecting instances to terminate during scale-in (e.g. \"oldest_first\", \"newest_first\").",
				Optional:    true,
				Computed:    true,
				Default:     stringdefault.StaticString("oldest_first"),
			},
			"tags": schema.MapAttribute{
				Description: "Key-value tags for the scale group.",
				Optional:    true,
				ElementType: types.StringType,
				PlanModifiers: []planmodifier.Map{
					mapplanmodifier.UseStateForUnknown(),
				},
			},
			"created_at": schema.StringAttribute{
				Description: "The timestamp when the scale group was created.",
				Computed:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"updated_at": schema.StringAttribute{
				Description: "The timestamp when the scale group was last updated.",
				Computed:    true,
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

func (r *scaleGroupResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

// apiScaleGroupList is the scale-group family listing (GET /scale-groups) —
// only the envelope the orphan sweep reads. NOTE the wire key is `data`, not
// scaleGroups (verified against the scale service). Items are the package's
// apiScaleGroup.
type apiScaleGroupList struct {
	Data []apiScaleGroup `json:"data"`
}

// adoptCreatedObject runs the create-timeout arm of the orphan contract for a
// scale group whose provisioning operation outlived the wait (internal/orphan:
// ADOPT-AS-TRACKED). Found = a fresh read of what the platform HAS, adopted
// after the apply timed out — the shared warning is orphan's — written through
// the package's own GET + fromAPI + Set flow. Verified absence = the
// re-apply-safe error. Unreadable = the platform's last word plus the
// `fm scale-group list` hint. It never invites `terraform state rm`.
//
// waitErr may be a real wait failure or the synthetic "completed but returned
// no resource ID" note the completed-but-unnamed arm passes in — either way it
// is what the wait ended with, and the diagnostics must carry it.
func (r *scaleGroupResource) adoptCreatedObject(ctx context.Context, plan *ScaleGroupModel, floor time.Time, waitErr error, resp *resource.CreateResponse) {
	adoptedID := orphan.AdoptCreateOnTimeout(ctx, &resp.Diagnostics, orphan.CreateParams{
		ResourceName: "Scale Group",
		FMList:       "`fm scale-group list`",
		WaitErr:      waitErr,
		Resolve: func(sweepCtx context.Context) (string, error) {
			listResp, err := r.client.Get(sweepCtx, r.client.TenantPath("/scale-groups"), nil)
			if err != nil {
				return "", err
			}
			listing, err := client.ParseResponse[apiScaleGroupList](listResp)
			if err != nil {
				return "", fmt.Errorf("failed to parse scale group list response: %w", err)
			}
			candidates := make([]orphan.Candidate, 0, len(listing.Data))
			for _, sg := range listing.Data {
				candidates = append(candidates, orphan.Candidate{ID: sg.ID, Name: sg.Name, CreatedAt: sg.CreatedAt})
			}
			return orphan.PickCreated(candidates, plan.Name.ValueString(), floor)
		},
	})
	if adoptedID == "" {
		return
	}

	// The honest read: the platform's response, not the configuration's
	// intent — the same GET + fromAPI + Set flow the ordinary create tail uses
	// (which mirrors the post-operation read: status and current_size refresh).
	getResp, err := r.client.Get(ctx, r.client.TenantPath("/scale-groups/"+adoptedID), nil)
	if err != nil {
		resp.Diagnostics.AddError("Failed to read scale group after creation", err.Error())
		return
	}
	finalSG, err := client.ParseResponse[apiScaleGroup](getResp)
	if err != nil {
		resp.Diagnostics.AddError("Failed to parse scale group response", err.Error())
		return
	}
	plan.fromAPI(ctx, finalSG, &resp.Diagnostics)
	resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
}

func (r *scaleGroupResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan ScaleGroupModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	apiReq := plan.toCreateRequest(ctx, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}

	apiResp, err := r.client.Post(ctx, r.client.TenantPath("/scale-groups"), apiReq)
	if err != nil {
		resp.Diagnostics.AddError("Failed to create scale group", err.Error())
		return
	}

	// Scale-group create routes through provisioning, which returns 202 + an
	// Operation envelope (operationId only, NOT the scale group). Poll the
	// operation to completion, then read by its resolved resourceId. A 201 with
	// the scale-group body is still accepted for a synchronous backend.
	//
	// When the wait gives up without the platform having said either yes or no,
	// the orphan contract's create arm decides: a terminal refusal is reported
	// as nothing-created, and everything unknown goes through the name/list
	// sweep (ADOPT-AS-TRACKED) instead of an error that invites state surgery.
	// applyStarted is the created-at floor for that sweep.
	applyStarted := time.Now().UTC()
	floor := applyStarted.Add(-time.Minute)

	// The customer's timeouts block, with defaults identical to the values
	// this resource has always hardcoded.
	budgets := r.resolveBudgets(plan.Timeouts)

	var scaleGroupID string
	if apiResp.IsAccepted() {
		op, opErr := client.ParseResponse[client.Operation](apiResp)
		if opErr != nil {
			resp.Diagnostics.AddError("Failed to parse operation response", opErr.Error())
			return
		}
		done, waitErr := r.client.WaitForOperation(ctx, op.OperationID, r.getPollInterval(), budgets.Create)
		if waitErr != nil {
			if r.client.ClassifyOperationFailure(ctx, op.OperationID) == client.OperationRefused {
				orphan.AddCreateRefused(&resp.Diagnostics, "Scale Group", fmt.Sprintf("the scale group %q", plan.Name.ValueString()), waitErr)
				return
			}
			// Unknown — the sweep decides. Return either way; the helper
			// wrote the state row or the diagnostic.
			r.adoptCreatedObject(ctx, &plan, floor, waitErr, resp)
			return
		}
		scaleGroupID = done.ResourceID
		if scaleGroupID == "" {
			// Completed but the envelope named no object: the sweep decides.
			r.adoptCreatedObject(ctx, &plan, floor,
				fmt.Errorf("the scale group create operation completed but returned no resource ID"), resp)
			return
		}
	} else {
		sg, parseErr := client.ParseResponse[apiScaleGroup](apiResp)
		if parseErr != nil {
			resp.Diagnostics.AddError("Failed to parse scale group response", parseErr.Error())
			return
		}
		scaleGroupID = sg.ID
	}
	if scaleGroupID == "" {
		resp.Diagnostics.AddError(
			"Scale Group Operation Returned No Resource ID",
			"The scale group create operation completed but returned no resource ID. The scale group may exist in the backend without being tracked in Terraform state - check `fm scale-group list` and import it if necessary.",
		)
		return
	}

	// Refresh state after the operation completes to get final status and current_size.
	readResp, err := r.client.Get(ctx, r.client.TenantPath("/scale-groups/"+scaleGroupID), nil)
	if err != nil {
		resp.Diagnostics.AddError("Failed to read scale group after creation", err.Error())
		return
	}
	finalSG, err := client.ParseResponse[apiScaleGroup](readResp)
	if err != nil {
		resp.Diagnostics.AddError("Failed to parse scale group response", err.Error())
		return
	}

	plan.fromAPI(ctx, finalSG, &resp.Diagnostics)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *scaleGroupResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state ScaleGroupModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	apiResp, err := r.client.Get(ctx, r.client.TenantPath("/scale-groups/"+state.ID.ValueString()), nil)
	if err != nil {
		if client.IsNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Failed to read scale group", err.Error())
		return
	}

	sg, err := client.ParseResponse[apiScaleGroup](apiResp)
	if err != nil {
		resp.Diagnostics.AddError("Failed to parse scale group response", err.Error())
		return
	}

	state.fromAPI(ctx, sg, &resp.Diagnostics)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *scaleGroupResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan ScaleGroupModel
	var state ScaleGroupModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	id := state.ID.ValueString()

	updateReq := plan.toUpdateRequest(ctx, &state, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}

	_, err := r.client.Patch(ctx, r.client.TenantPath("/scale-groups/"+id), updateReq)
	if err != nil {
		resp.Diagnostics.AddError("Failed to update scale group", err.Error())
		return
	}

	// Refresh state from API.
	apiResp, err := r.client.Get(ctx, r.client.TenantPath("/scale-groups/"+id), nil)
	if err != nil {
		resp.Diagnostics.AddError("Failed to read scale group after update", err.Error())
		return
	}

	sg, err := client.ParseResponse[apiScaleGroup](apiResp)
	if err != nil {
		resp.Diagnostics.AddError("Failed to parse scale group response", err.Error())
		return
	}

	plan.fromAPI(ctx, sg, &resp.Diagnostics)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *scaleGroupResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state ScaleGroupModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	id := state.ID.ValueString()

	_, err := r.client.Delete(ctx, r.client.TenantPath("/scale-groups/"+id))
	if err != nil {
		if client.IsNotFound(err) {
			return
		}
		resp.Diagnostics.AddError("Failed to delete scale group", err.Error())
		return
	}

	// Wait for the scale group to be fully deleted (404 on GET), on the
	// timeouts block's delete budget.
	budgets := r.resolveBudgets(state.Timeouts)
	_, err = client.WaitForState(ctx, client.PollConfig{
		Interval:     r.getPollInterval(),
		Timeout:      budgets.Delete,
		TargetStates: []string{"deleted"},
		ErrorStates:  []string{"error"},
		ResourceName: "scale_group",
		PollFunc: func(pollCtx context.Context) (string, error) {
			pollResp, pollErr := r.client.Get(pollCtx, r.client.TenantPath("/scale-groups/"+id), nil)
			if pollErr != nil {
				if client.IsNotFound(pollErr) {
					return "deleted", nil
				}
				return "", pollErr
			}
			current, parseErr := client.ParseResponse[apiScaleGroup](pollResp)
			if parseErr != nil {
				return "", parseErr
			}
			return current.Status, nil
		},
	})
	if err != nil {
		resp.Diagnostics.AddError("Scale group failed to delete", err.Error())
	}
}

func (r *scaleGroupResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}
