package messaging_instance

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/orphan"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/planmod"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/scopedecl"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/stateupgrade"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/timeouts"
)

var (
	_ resource.Resource                = &messagingInstanceResource{}
	_ resource.ResourceWithImportState = &messagingInstanceResource{}
)

// NewResource returns a new messaging_instance resource factory.
func NewResource() resource.Resource {
	return &messagingInstanceResource{}
}

type messagingInstanceResource struct {
	client       *client.Client
	pollInterval time.Duration
	pollTimeout  time.Duration
}

func (r *messagingInstanceResource) getPollInterval() time.Duration {
	if r.pollInterval > 0 {
		return r.pollInterval
	}
	return 5 * time.Second
}

func (r *messagingInstanceResource) getPollTimeout() time.Duration {
	if r.pollTimeout > 0 {
		return r.pollTimeout
	}
	return 15 * time.Minute
}

// resolveBudgets turns the configured timeouts block into effective budgets,
// falling back per verb to the same value this resource has always hardcoded.
// Routing the defaults through the accessor keeps the test-injection seam
// intact: a test that shrinks pollTimeout shrinks every wait that does not
// carry an explicit timeouts override, exactly as before.
func (r *messagingInstanceResource) resolveBudgets(m *timeouts.Model) timeouts.Budgets {
	budgets, err := m.Resolve(timeouts.Uniform(r.getPollTimeout()))
	if err != nil {
		// Unreachable via HCL (the block validator rejects bad durations at
		// plan time); degrade to the defaults rather than fail a wait.
		return timeouts.Uniform(r.getPollTimeout())
	}
	return budgets
}

func (r *messagingInstanceResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_messaging_instance"
}

func (r *messagingInstanceResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		// v1: the attribute `engine` was renamed to `type`. See UpgradeState — without the
		// migration this rename DESTROYS brokers, because `type` carries a Default and
		// RequiresReplace.
		Version:     1,
		Description: "Manages a managed messaging (LavinMQ) instance in the Frostmoln platform." + "\n\n" + scopedecl.Summary("frostmoln_messaging_instance"),
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Description: "The unique identifier of the messaging instance.",
				Computed:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"name": schema.StringAttribute{
				Description: "The name of the messaging instance.",
				Required:    true,
			},
			"type": schema.StringAttribute{
				Description: "The messaging type. Only \"lavinmq\" is currently supported. Defaults to \"lavinmq\".",
				Optional:    true,
				Computed:    true,
				Default:     stringdefault.StaticString("lavinmq"),
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"version": schema.StringAttribute{
				Description: "The version (e.g. \"2.3\"). Defaults to the recommended version when omitted.",
				Optional:    true,
				Computed:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
					stringplanmodifier.RequiresReplace(),
				},
			},
			"flavor_id": schema.StringAttribute{
				Description: "The flavor/size for the messaging instance (e.g. \"mq.gp1.small\", \"mq.gp1.medium\").",
				Required:    true,
				PlanModifiers: []planmodifier.String{
					planmod.StringWarnOnChange("Changing flavor_id (flavor resize) is not yet supported for managed messaging instances. Keep the original flavor_id, or destroy and recreate the instance to change it."),
				},
			},
			"vpc_id": schema.StringAttribute{
				Description: "The VPC ID where the messaging instance will be deployed.",
				Required:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"subnet_id": schema.StringAttribute{
				Description: "The subnet ID where the messaging instance will be deployed.",
				Required:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"persistence_mode": schema.StringAttribute{
				Description: "The persistence mode for the messaging instance (\"none\" or \"persistent\"). Defaults to \"persistent\".",
				Optional:    true,
				Computed:    true,
				Default:     stringdefault.StaticString("persistent"),
			},
			"status": schema.StringAttribute{
				Description: "The current status of the messaging instance.",
				Computed:    true,
			},
			"private_ip": schema.StringAttribute{
				Description: "The private IP address of the messaging instance.",
				Computed:    true,
			},
			"port": schema.Int64Attribute{
				Description: "The AMQP port number the messaging instance is listening on.",
				Computed:    true,
				PlanModifiers: []planmodifier.Int64{
					int64planmodifier.UseStateForUnknown(),
				},
			},
			"amqps_port": schema.Int64Attribute{
				Description: "The AMQPS (TLS) port number the messaging instance is listening on.",
				Computed:    true,
				PlanModifiers: []planmodifier.Int64{
					int64planmodifier.UseStateForUnknown(),
				},
			},
			"management_port": schema.Int64Attribute{
				Description: "The HTTP management/API port number the messaging instance is listening on.",
				Computed:    true,
				PlanModifiers: []planmodifier.Int64{
					int64planmodifier.UseStateForUnknown(),
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
		},
		Blocks: map[string]schema.Block{
			// Customer-tunable wait budgets: defaults keep the values this
			// resource has always hardcoded (15m per verb). A timeouts change
			// is an in-place no-op on real infrastructure — verified by the
			// Gate 2 smoke test (project-docs/product/TF-CONVERGENCE-WALL-PLAN.md).
			"timeouts": timeouts.Schema(),
		},
	}
}

// UpgradeState migrates prior state across the HCL-surface rename `engine` -> `type`.
//
// 🔴 WITHOUT THIS THE RENAME DESTROYS BROKERS, and not on some exotic path. `type` carries a
// Default, so it is present in EVERY state file written by an older provider. On upgrade the
// framework drops the unknown `engine` key and leaves `type` NULL. With refresh on, Read
// repopulates it and the plan is clean — but `terraform plan -refresh=false`, which is ordinary
// in CI, never calls Read: the Default plans "lavinmq" against a null prior value, RequiresReplace
// fires, and Terraform proposes to destroy and recreate the instance. A persistent LavinMQ broker
// loses its queues for a rename.
//
// This is the same shape, and the same fix, as load_balancer's provider_type -> type migration and
// mysql_instance's flavor -> flavor_id. It does NOT reintroduce a deprecated alias: this is the
// only code that reads the pre-rename DATA, and the request path, the response path and the schema
// are all canonical-only.
func (r *messagingInstanceResource) UpgradeState(ctx context.Context) map[int64]resource.StateUpgrader {
	schemaResp := resource.SchemaResponse{}
	r.Schema(ctx, resource.SchemaRequest{}, &schemaResp)
	return map[int64]resource.StateUpgrader{
		0: stateupgrade.RenameStringAttr(ctx, schemaResp.Schema, "engine", "type"),
	}
}

func (r *messagingInstanceResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (r *messagingInstanceResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan MessagingInstanceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	apiReq := plan.toCreateRequest(ctx, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}

	// The customer's timeouts block, with defaults identical to the values
	// this resource has always hardcoded.
	budgets := r.resolveBudgets(plan.Timeouts)

	// The created-at floor and subject for the discovery sweep (below): the
	// instance this apply produces must have been created by THIS apply, and
	// the refused arm names what the platform said no to.
	applyStarted := time.Now().UTC()
	floor := applyStarted.Add(-time.Minute)
	subject := fmt.Sprintf("the messaging instance %q", plan.Name.ValueString())

	apiResp, err := r.client.Post(ctx, r.client.TenantPath("/messaging"), apiReq)
	if err != nil {
		resp.Diagnostics.AddError("Failed to create messaging instance", err.Error())
		return
	}

	// Messaging create routes through provisioning, which returns 202 + an Operation
	// envelope (operationId only, NOT the instance). Poll the operation to
	// completion (the workflow waits for the instance to reach running before
	// completing), then read by its resolved resourceId. A 201 with the instance
	// body is still accepted for a synchronous backend. Mirrors the compute
	// instance + volume + load_balancer resources.
	var instanceID string
	// adopted records that instanceID came from the discovery sweep, not the
	// operation: the follow-up read then owes the sweep's contract extra
	// diligence (identity verify, and never un-tracking the found object).
	adopted := false
	if apiResp.IsAccepted() {
		op, opErr := client.ParseResponse[client.Operation](apiResp)
		if opErr != nil {
			resp.Diagnostics.AddError("Failed to parse operation response", opErr.Error())
			return
		}
		done, waitErr := r.client.WaitForOperation(ctx, op.OperationID, r.getPollInterval(), budgets.Create)
		if waitErr != nil {
			if ctx.Err() != nil {
				// The practitioner cancelled this apply — do not spend the
				// classification or the sweep on a dead context, and do not
				// mislabel a cancellation as an unresolvable fate.
				resp.Diagnostics.AddError("Messaging instance creation failed", waitErr.Error())
				return
			}
			if r.client.ClassifyOperationFailure(ctx, op.OperationID) == client.OperationRefused {
				orphan.AddCreateRefused(&resp.Diagnostics, "Messaging Instance", subject, waitErr)
				return
			}
			// UNKNOWN: the saga may still land. The sweep decides honestly.
			instanceID = r.adoptCreatedInstance(ctx, plan, floor, waitErr, resp)
			adopted = instanceID != ""
			if !adopted {
				return
			}
		} else {
			instanceID = done.ResourceID
			if instanceID == "" {
				// The operation COMPLETED without its resourceId (degraded
				// provisioning). The instance exists; the sweep resolves it
				// by name.
				instanceID = r.adoptCreatedInstance(ctx, plan, floor,
					fmt.Errorf("the create operation completed but returned no resource ID"), resp)
				adopted = instanceID != ""
				if !adopted {
					return
				}
			}
		}
	} else {
		inst, parseErr := client.ParseResponse[apiMessagingInstance](apiResp)
		if parseErr != nil {
			resp.Diagnostics.AddError("Failed to parse messaging instance response", parseErr.Error())
			return
		}
		instanceID = inst.ID
		// Legacy 201 (a synchronous backend, or a pre-202-rollout messaging build):
		// the instance comes back as "creating". Persist the row FIRST — a poll
		// that never reaches running must leave the created instance tracked,
		// not orphaned (the same D2 class the 202 arm's contract closes) —
		// then poll to running so `apply` blocks to completion exactly like
		// the 202 operation path does.
		if instanceID != "" {
			plan.fromAPI(ctx, inst, &resp.Diagnostics)
			if resp.Diagnostics.HasError() {
				return
			}
			resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
			if resp.Diagnostics.HasError() {
				return
			}
			if _, waitErr := client.WaitForState(ctx, client.PollConfig{
				Interval:     r.getPollInterval(),
				Timeout:      budgets.Create,
				TargetStates: []string{"running"},
				ErrorStates:  []string{"error", "failed"},
				ResourceName: "messaging_instance",
				PollFunc: func(pollCtx context.Context) (string, error) {
					pollResp, pollErr := r.client.Get(pollCtx, r.client.TenantPath("/messaging/"+instanceID), nil)
					if pollErr != nil {
						return "", pollErr
					}
					current, parseErr := client.ParseResponse[apiMessagingInstance](pollResp)
					if parseErr != nil {
						return "", parseErr
					}
					return current.Status, nil
				},
			}); waitErr != nil {
				resp.Diagnostics.AddError("Messaging instance failed to reach running state", waitErr.Error())
				return
			}
		}
	}
	if instanceID == "" {
		resp.Diagnostics.AddError(
			"Messaging Instance Operation Returned No Resource ID",
			"The messaging instance create operation completed but returned no resource ID. The instance may exist in the backend without being tracked in Terraform state - check `fm messaging instance list` and import it if necessary.",
		)
		return
	}

	// Read the final state (the operation completion means the instance is running).
	readResp, err := r.client.Get(ctx, r.client.TenantPath("/messaging/"+instanceID), nil)
	if err != nil {
		if adopted {
			// The sweep FOUND the instance — a failed follow-up read must
			// not un-track it. Write the minimal tracked row (the id is
			// what destroy and refresh need) and say why.
			plan.ID = types.StringValue(instanceID)
			resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
			resp.Diagnostics.AddError("Adopted Messaging Instance Could Not Be Read",
				fmt.Sprintf("The discovery sweep adopted instance %s, but the follow-up read failed: %s. "+
					"The instance is tracked in state; refresh once the platform responds.", instanceID, err.Error()))
			return
		}
		resp.Diagnostics.AddError("Failed to read messaging instance after creation", err.Error())
		return
	}
	finalInst, err := client.ParseResponse[apiMessagingInstance](readResp)
	if err != nil {
		resp.Diagnostics.AddError("Failed to parse messaging instance response", err.Error())
		return
	}
	if adopted && (finalInst.Name != plan.Name.ValueString() || (finalInst.Type != "" && finalInst.Type != plan.Type.ValueString())) {
		// The sweep is name+floor matched, but the honest read is the last
		// word: anything read outside this apply's name/type is not this
		// apply's instance (adopting the wrong object outranks adopting
		// none — and this row would be destroyable).
		resp.Diagnostics.AddError("Adopted Object Does Not Match This Apply",
			fmt.Sprintf("The discovery sweep matched instance %s for name %q, but the platform's read "+
				"returned name %q type %q. Nothing was recorded in state; identify the instance with "+
				"`fm messaging instance list` and `terraform import` it if it is yours.",
				instanceID, plan.Name.ValueString(), finalInst.Name, finalInst.Type))
		return
	}

	plan.fromAPI(ctx, finalInst, &resp.Diagnostics)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// adoptCreatedInstance is the create-timeout arm of the orphan contract
// (internal/orphan) for this resource family: the wait gave up while the saga
// may still be running, or completed without its resourceId, and the family
// listing resolves the instance this apply produced — matched on name, created
// after this apply started, and of this apply's type so a same-named sibling
// type can never be adopted by mistake. Found means adopt (the caller's
// final read-and-persist is the honest read); absent means the sweep verified
// the absence, so re-applying is safe; unreadable names the platform's last
// word plus the list path. Never `terraform state rm` — it is the one action
// that re-orphans a live, billing instance. Returns the adopted id, empty when
// the diagnostic already says everything (the caller must return).
func (r *messagingInstanceResource) adoptCreatedInstance(ctx context.Context, plan MessagingInstanceModel, floor time.Time, waitErr error, resp *resource.CreateResponse) string {
	wantType := ""
	if !plan.Type.IsNull() && !plan.Type.IsUnknown() {
		wantType = plan.Type.ValueString()
	}
	return orphan.AdoptCreateOnTimeout(ctx, &resp.Diagnostics, orphan.CreateParams{
		ResourceName: "Messaging Instance",
		FMList:       "`fm messaging instance list`",
		WaitErr:      waitErr,
		Resolve: func(ctx context.Context) (string, error) {
			apiResp, err := r.client.Get(ctx, r.client.TenantPath("/messaging"), nil)
			if err != nil {
				return "", err
			}
			var list apiMessagingInstanceList
			if err := json.Unmarshal(apiResp.Body, &list); err != nil {
				return "", err
			}
			candidates := make([]orphan.Candidate, 0, len(list.Instances))
			for _, it := range list.Instances {
				// Fail closed on the discriminator (the type carries a schema
				// Default, so wantType is always known here): a row with no
				// type or a different type is never adopted — adopting the
				// wrong object outranks adopting none.
				if it.Type != wantType {
					continue
				}
				candidates = append(candidates, orphan.Candidate{ID: it.ID, Name: it.Name, CreatedAt: it.CreatedAt})
			}
			return orphan.PickCreated(candidates, plan.Name.ValueString(), floor)
		},
	})
}

func (r *messagingInstanceResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state MessagingInstanceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	apiResp, err := r.client.Get(ctx, r.client.TenantPath("/messaging/"+state.ID.ValueString()), nil)
	if err != nil {
		if client.IsNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Failed to read messaging instance", err.Error())
		return
	}

	inst, err := client.ParseResponse[apiMessagingInstance](apiResp)
	if err != nil {
		resp.Diagnostics.AddError("Failed to parse messaging instance response", err.Error())
		return
	}

	state.fromAPI(ctx, inst, &resp.Diagnostics)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *messagingInstanceResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan MessagingInstanceModel
	var state MessagingInstanceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	id := state.ID.ValueString()

	// flavor_id cannot change in place — the platform has no flavor-resize path and the
	// PUT below would silently drop it. The plan-time modifier only WARNS (an error there
	// would also block `terraform destroy`; see planmod.StringWarnOnChange), so the change
	// is refused HERE. Unknown values are skipped: they carry no comparable value.
	// An empty prior value carries nothing to compare against either — it means the
	// API returned no flavorId on the last read, and trapping every future update
	// behind a refusal naming `""` would be worse than letting the change through.
	if !plan.FlavorID.IsUnknown() && !state.FlavorID.IsUnknown() && state.FlavorID.ValueString() != "" &&
		!plan.FlavorID.Equal(state.FlavorID) {
		resp.Diagnostics.AddError(
			"flavor_id cannot be changed",
			fmt.Sprintf("Changing flavor_id (flavor resize) is not yet supported for managed messaging instances (currently %q, requested %q). Keep the original flavor_id, or destroy and recreate the instance to change it.",
				state.FlavorID.ValueString(), plan.FlavorID.ValueString()),
		)
		return
	}

	// In-place field updates (name, persistence mode) via PUT. flavor_id changes
	// are refused above, so nothing here can change size. Skip the call
	// when nothing PUT-able changed.
	if updateReq := plan.toUpdateRequest(&state); updateReq.hasChanges() {
		if _, err := r.client.Put(ctx, r.client.TenantPath("/messaging/"+id), updateReq); err != nil {
			resp.Diagnostics.AddError("Failed to update messaging instance", err.Error())
			return
		}

		// Poll until instance is back to "running" after the update, on the
		// timeouts block's update budget.
		_, err := client.WaitForState(ctx, client.PollConfig{
			Interval:     r.getPollInterval(),
			Timeout:      r.resolveBudgets(plan.Timeouts).Update,
			TargetStates: []string{"running"},
			ErrorStates:  []string{"error", "failed"},
			ResourceName: "messaging_instance",
			PollFunc: func(pollCtx context.Context) (string, error) {
				pollResp, pollErr := r.client.Get(pollCtx, r.client.TenantPath("/messaging/"+id), nil)
				if pollErr != nil {
					return "", pollErr
				}
				current, parseErr := client.ParseResponse[apiMessagingInstance](pollResp)
				if parseErr != nil {
					return "", parseErr
				}
				return current.Status, nil
			},
		})
		if err != nil {
			resp.Diagnostics.AddError("Messaging instance failed to reach running state after update", err.Error())
			return
		}
	}

	// Refresh state from API.
	apiResp, err := r.client.Get(ctx, r.client.TenantPath("/messaging/"+id), nil)
	if err != nil {
		resp.Diagnostics.AddError("Failed to read messaging instance after update", err.Error())
		return
	}

	inst, err := client.ParseResponse[apiMessagingInstance](apiResp)
	if err != nil {
		resp.Diagnostics.AddError("Failed to parse messaging instance response", err.Error())
		return
	}

	plan.fromAPI(ctx, inst, &resp.Diagnostics)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *messagingInstanceResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state MessagingInstanceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	id := state.ID.ValueString()

	_, err := r.client.Delete(ctx, r.client.TenantPath("/messaging/"+id))
	if err != nil {
		if client.IsNotFound(err) {
			return
		}
		resp.Diagnostics.AddError("Failed to delete messaging instance", err.Error())
		return
	}

	// Wait for the instance to be fully deleted (404 on GET), on the
	// timeouts block's delete budget.
	_, err = client.WaitForState(ctx, client.PollConfig{
		Interval:     r.getPollInterval(),
		Timeout:      r.resolveBudgets(state.Timeouts).Delete,
		TargetStates: []string{"deleted"},
		ErrorStates:  []string{"error"},
		ResourceName: "messaging_instance",
		PollFunc: func(pollCtx context.Context) (string, error) {
			pollResp, pollErr := r.client.Get(pollCtx, r.client.TenantPath("/messaging/"+id), nil)
			if pollErr != nil {
				if client.IsNotFound(pollErr) {
					return "deleted", nil
				}
				return "", pollErr
			}
			current, parseErr := client.ParseResponse[apiMessagingInstance](pollResp)
			if parseErr != nil {
				return "", parseErr
			}
			return current.Status, nil
		},
	})
	if err != nil {
		resp.Diagnostics.AddError("Messaging instance failed to delete", err.Error())
	}
}

func (r *messagingInstanceResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}
