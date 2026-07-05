package nginx_instance

import (
	"context"
	"fmt"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/boolplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/mapplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/planmod"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/stateupgrade"
)

var (
	_ resource.Resource                 = &nginxInstanceResource{}
	_ resource.ResourceWithImportState  = &nginxInstanceResource{}
	_ resource.ResourceWithUpgradeState = &nginxInstanceResource{}
	_ resource.ResourceWithModifyPlan   = &nginxInstanceResource{}
)

// NewResource returns a new nginx_instance resource factory.
func NewResource() resource.Resource {
	return &nginxInstanceResource{}
}

type nginxInstanceResource struct {
	client       *client.Client
	pollInterval time.Duration
	pollTimeout  time.Duration
}

func (r *nginxInstanceResource) getPollInterval() time.Duration {
	if r.pollInterval > 0 {
		return r.pollInterval
	}
	return 5 * time.Second
}

func (r *nginxInstanceResource) getPollTimeout() time.Duration {
	if r.pollTimeout > 0 {
		return r.pollTimeout
	}
	return 15 * time.Minute
}

// pollRunning waits until the instance returns to "running" state.
func (r *nginxInstanceResource) pollRunning(ctx context.Context, id string) (string, error) {
	return client.WaitForState(ctx, client.PollConfig{
		Interval:     r.getPollInterval(),
		Timeout:      r.getPollTimeout(),
		TargetStates: []string{"running"},
		ErrorStates:  []string{"error", "failed"},
		ResourceName: "nginx_instance",
		PollFunc: func(pollCtx context.Context) (string, error) {
			pollResp, pollErr := r.client.Get(pollCtx, r.client.TenantPath("/webservers/"+id), nil)
			if pollErr != nil {
				return "", pollErr
			}
			current, parseErr := client.ParseResponse[apiWebserverInstance](pollResp)
			if parseErr != nil {
				return "", parseErr
			}
			return current.Status, nil
		},
	})
}

// resizeStorage grows the instance's storage online via POST /resize, then waits
// for it to return to "running". Grow-only: a shrink is rejected at plan time.
func (r *nginxInstanceResource) resizeStorage(ctx context.Context, id string, storageGB int) error {
	_, err := r.client.Post(ctx, r.client.TenantPath("/webservers/"+id+"/resize"), apiResizeWebserverInstanceRequest{StorageGB: storageGB})
	if err != nil {
		return err
	}
	_, err = r.pollRunning(ctx, id)
	return err
}

// setExposure toggles the instance's public exposure via the action endpoints
// (POST /expose | /unexpose), then waits for the returned async operation to
// finish. The expose/unexpose actions are idempotent from the caller's side:
// the backend returns 409 when the instance is already in the desired state, so
// a 409 is treated as success rather than an error. The instance status stays
// "running" throughout (ADR-0097), so completion is tracked via the operation,
// not an instance state transition.
func (r *nginxInstanceResource) setExposure(ctx context.Context, id string, desired bool) error {
	action := "unexpose"
	if desired {
		action = "expose"
	}
	resp, err := r.client.Post(ctx, r.client.TenantPath("/webservers/"+id+"/"+action), nil)
	if err != nil {
		if client.IsAlreadyInDesiredState(err) {
			// Already exposed / already not exposed / an operation already in
			// progress — the requested end state is (being) reached. A 409
			// invalid_state ("cannot expose an instance in <state> state") is NOT
			// swallowed by this helper and surfaces as a real error.
			return nil
		}
		return err
	}
	op, err := client.ParseResponse[client.OperationResponse](resp)
	if err != nil {
		return err
	}
	if op.OperationID == "" {
		// Synchronous acknowledgement with no operation to poll.
		return nil
	}
	_, err = r.client.WaitForOperation(ctx, op.OperationID, r.getPollInterval(), r.getPollTimeout())
	return err
}

// pollPublicExposed waits until the instance read-back reports public==true. The
// create-with-public=true saga stamps exposure in a second write AFTER
// status=running (webserver syncer), so a create must wait for that write to land
// before its final read, otherwise the read could see public=false while the plan
// had public=true. It keeps polling while the instance is still "running" (not yet
// exposed) and errors out if the instance drops into an error state.
func (r *nginxInstanceResource) pollPublicExposed(ctx context.Context, id string) error {
	_, err := client.WaitForState(ctx, client.PollConfig{
		Interval:     r.getPollInterval(),
		Timeout:      r.getPollTimeout(),
		TargetStates: []string{"exposed"},
		ErrorStates:  []string{"error", "failed"},
		ResourceName: "nginx_instance exposure",
		PollFunc: func(pollCtx context.Context) (string, error) {
			pollResp, pollErr := r.client.Get(pollCtx, r.client.TenantPath("/webservers/"+id), nil)
			if pollErr != nil {
				return "", pollErr
			}
			current, parseErr := client.ParseResponse[apiWebserverInstance](pollResp)
			if parseErr != nil {
				return "", parseErr
			}
			if current.Public {
				return "exposed", nil
			}
			// Not yet exposed: surface a genuine error status, else keep polling.
			return current.Status, nil
		},
	})
	return err
}

// ModifyPlan marks public_ip as unknown when the desired public value is
// changing on an existing instance. Exposure is action-based (expose/unexpose),
// so the Floating IP is (re)assigned or released during Update; without this the
// public_ip attribute's UseStateForUnknown would pin the plan to the stale value
// and Terraform would reject the post-apply value as an inconsistent result.
func (r *nginxInstanceResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	// Only the in-place update case matters (create marks computed attrs unknown
	// already; destroy has no plan).
	if req.Plan.Raw.IsNull() || req.State.Raw.IsNull() {
		return
	}
	var plan, state NginxInstanceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if !plan.Public.Equal(state.Public) {
		resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("public_ip"), types.StringUnknown())...)
	}
}

func (r *nginxInstanceResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_nginx_instance"
}

func (r *nginxInstanceResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		// v1: the HCL attribute flavor was renamed to flavor_id to match the
		// flagship frostmoln_instance and the cache/messaging offers (the wire
		// tag was always flavorId). See UpgradeState for the v0->v1 migration.
		Version:     1,
		Description: "Manages a managed Nginx webserver instance in the Frostmoln platform.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Description: "The unique identifier of the Nginx instance.",
				Computed:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"name": schema.StringAttribute{
				Description: "The name of the Nginx instance.",
				Required:    true,
			},
			"version": schema.StringAttribute{
				Description: "The Nginx version (e.g. \"1.24\", \"1.26\").",
				Required:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"flavor_id": schema.StringAttribute{
				Description: "The flavor ID/size for the webserver instance (e.g. \"web.gp1.small\", \"web.gp1.medium\").",
				Required:    true,
				PlanModifiers: []planmodifier.String{
					planmod.StringErrorOnChange("Changing flavor_id (flavor resize) is not yet supported for managed webserver instances. Keep the original flavor_id, or destroy and recreate the instance to change it."),
				},
			},
			"storage_gb": schema.Int64Attribute{
				Description: "The storage size in gigabytes. Can be increased in place (online resize); decreasing it is not supported.",
				Required:    true,
				PlanModifiers: []planmodifier.Int64{
					planmod.Int64GrowOnly("GB"),
				},
			},
			"vpc_id": schema.StringAttribute{
				Description: "The VPC ID where the webserver instance will be deployed.",
				Required:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"subnet_id": schema.StringAttribute{
				Description: "The subnet ID where the webserver instance will be deployed.",
				Required:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"tls_enabled": schema.BoolAttribute{
				Description: "Whether TLS is enabled for the webserver.",
				Optional:    true,
				Computed:    true,
				PlanModifiers: []planmodifier.Bool{
					boolplanmodifier.UseStateForUnknown(),
				},
			},
			"php_enabled": schema.BoolAttribute{
				Description: "Whether PHP-FPM support is enabled for the webserver. Can be toggled in place; " +
					"the instance is reconfigured (and briefly reloads) on change.",
				Optional: true,
				Computed: true,
				PlanModifiers: []planmodifier.Bool{
					boolplanmodifier.UseStateForUnknown(),
				},
			},
			"php_version": schema.StringAttribute{
				Description: "The PHP version to run (e.g. \"8.1\", \"8.2\", \"8.3\"). Only applicable when " +
					"php_enabled is true; the platform selects a supported default when omitted.",
				Optional: true,
				Computed: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"config": schema.MapAttribute{
				Description: "Engine-specific configuration applied to the webserver, as key/value pairs " +
					"(sent as the engineConfig object). Keys must be from the platform's curated allowlist " +
					"(e.g. gzip, securityHeaders, clientMaxBodySize, spaFallback); unknown keys are rejected. " +
					"Changing this reconfigures the running instance in place.",
				Optional:    true,
				Computed:    true,
				ElementType: types.StringType,
				PlanModifiers: []planmodifier.Map{
					mapplanmodifier.UseStateForUnknown(),
				},
			},
			"public": schema.BoolAttribute{
				Description: "Whether the instance is publicly exposed (ADR-0097): when true a Floating IP is " +
					"associated to the instance's engine port so the deployed site is reachable on the public " +
					"internet, and public_ip is populated. Set at create to expose immediately; toggling it " +
					"afterwards runs the platform's expose (true) or unexpose (false) action.",
				Optional: true,
				Computed: true,
				PlanModifiers: []planmodifier.Bool{
					boolplanmodifier.UseStateForUnknown(),
				},
			},
			"public_ip": schema.StringAttribute{
				Description: "The public Floating IP address associated with the instance while it is exposed (empty when not public).",
				Computed:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"status": schema.StringAttribute{
				Description: "The current status of the Nginx instance.",
				Computed:    true,
			},
			"private_ip": schema.StringAttribute{
				Description: "The private IP address of the Nginx instance.",
				Computed:    true,
			},
			"port": schema.Int64Attribute{
				Description: "The port number the Nginx instance is listening on.",
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

func (r *nginxInstanceResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (r *nginxInstanceResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan NginxInstanceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// Capture whether the plan requested public exposure at create BEFORE fromAPI
	// (called below) overwrites plan.Public with the read-back value.
	requestedPublic := !plan.Public.IsNull() && !plan.Public.IsUnknown() && plan.Public.ValueBool()

	apiReq := plan.toCreateRequest(ctx, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}

	apiResp, err := r.client.Post(ctx, r.client.TenantPath("/webservers"), apiReq)
	if err != nil {
		resp.Diagnostics.AddError("Failed to create Nginx instance", err.Error())
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
			resp.Diagnostics.AddError("Failed to parse Nginx instance operation response", err.Error())
			return
		}
		done, err := r.client.WaitForOperation(ctx, op.OperationID, r.getPollInterval(), r.getPollTimeout())
		if err != nil {
			resp.Diagnostics.AddError("Nginx instance creation failed", err.Error())
			return
		}
		instID = done.ResourceID
		if instID == "" {
			resp.Diagnostics.AddError(
				"Nginx instance operation returned no resource ID",
				"The create operation completed but returned no resource ID. The instance may "+
					"exist in the backend without being tracked in Terraform state.",
			)
			return
		}

		// Persist state immediately so the ID is tracked, even if the
		// poll-to-running or final read below fails. The 202 path has no
		// create body, so fill the computed attributes from a GET of the
		// freshly-created instance.
		readResp, err := r.client.Get(ctx, r.client.TenantPath("/webservers/"+instID), nil)
		if err != nil {
			resp.Diagnostics.AddError("Failed to read Nginx instance after creation", err.Error())
			return
		}
		inst, err := client.ParseResponse[apiWebserverInstance](readResp)
		if err != nil {
			resp.Diagnostics.AddError("Failed to parse Nginx instance response", err.Error())
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
		inst, err := client.ParseResponse[apiWebserverInstance](apiResp)
		if err != nil {
			resp.Diagnostics.AddError("Failed to parse Nginx instance response", err.Error())
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
		ResourceName: "nginx_instance",
		PollFunc: func(pollCtx context.Context) (string, error) {
			pollResp, pollErr := r.client.Get(pollCtx, r.client.TenantPath("/webservers/"+instID), nil)
			if pollErr != nil {
				return "", pollErr
			}
			current, parseErr := client.ParseResponse[apiWebserverInstance](pollResp)
			if parseErr != nil {
				return "", parseErr
			}
			return current.Status, nil
		},
	})
	if err != nil {
		resp.Diagnostics.AddError("Nginx instance failed to reach running state", err.Error())
		return
	}

	// When the plan requested public=true, exposure is stamped by the create
	// saga in a SEPARATE write AFTER status=running (webserver syncer), so the
	// final read below can land before public is set and read public=false while
	// the plan says true — an inconsistent-result failure. Wait for the read-back
	// public to become true first. Gated on requestedPublic so the common
	// public-unset / public=false path is not delayed.
	if requestedPublic {
		if err := r.pollPublicExposed(ctx, instID); err != nil {
			resp.Diagnostics.AddError("Nginx instance failed to become publicly exposed", err.Error())
			return
		}
	}

	// Refresh state after polling completes to get final status, IPs, etc.
	readResp, err := r.client.Get(ctx, r.client.TenantPath("/webservers/"+instID), nil)
	if err != nil {
		resp.Diagnostics.AddError("Failed to read Nginx instance after creation", err.Error())
		return
	}
	finalInst, err := client.ParseResponse[apiWebserverInstance](readResp)
	if err != nil {
		resp.Diagnostics.AddError("Failed to parse Nginx instance response", err.Error())
		return
	}

	plan.fromAPI(ctx, finalInst, &resp.Diagnostics)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *nginxInstanceResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state NginxInstanceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	apiResp, err := r.client.Get(ctx, r.client.TenantPath("/webservers/"+state.ID.ValueString()), nil)
	if err != nil {
		if client.IsNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Failed to read Nginx instance", err.Error())
		return
	}

	inst, err := client.ParseResponse[apiWebserverInstance](apiResp)
	if err != nil {
		resp.Diagnostics.AddError("Failed to parse Nginx instance response", err.Error())
		return
	}

	state.fromAPI(ctx, inst, &resp.Diagnostics)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *nginxInstanceResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan NginxInstanceModel
	var state NginxInstanceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	id := state.ID.ValueString()

	// Storage grow goes through POST /resize (online, grow-only). A shrink is
	// rejected at plan time (storage_gb GrowOnly modifier), so in the normal case
	// only an increase reaches here. Re-check at apply as a defensive backstop:
	// the plan-time modifier is skipped when storage_gb is an unknown
	// (interpolated) value, so a shrink can slip through to apply with known
	// values here — fail with a clear message rather than a silent no-op.
	switch {
	case plan.StorageGB.ValueInt64() < state.StorageGB.ValueInt64():
		resp.Diagnostics.AddError(
			"Storage cannot be shrunk",
			fmt.Sprintf("storage_gb can only be increased (currently %d GB, requested %d GB); storage is grown online and cannot be shrunk.",
				state.StorageGB.ValueInt64(), plan.StorageGB.ValueInt64()),
		)
		return
	case plan.StorageGB.ValueInt64() > state.StorageGB.ValueInt64():
		if err := r.resizeStorage(ctx, id, int(plan.StorageGB.ValueInt64())); err != nil {
			resp.Diagnostics.AddError("Failed to resize Nginx instance storage", err.Error())
			return
		}
	}

	// In-place field updates (name, TLS, config) via PUT. Skip the call entirely
	// when nothing PUT-able changed (e.g. a storage-only resize).
	updateReq := plan.toUpdateRequest(ctx, &state, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	if updateReq.hasChanges() {
		if _, err := r.client.Put(ctx, r.client.TenantPath("/webservers/"+id), updateReq); err != nil {
			resp.Diagnostics.AddError("Failed to update Nginx instance", err.Error())
			return
		}

		// Poll until instance is back to "running" after the update.
		if _, err := r.pollRunning(ctx, id); err != nil {
			resp.Diagnostics.AddError("Nginx instance failed to reach running state after update", err.Error())
			return
		}
	}

	// Public exposure is action-based, not a PUT field: when the desired `public`
	// value changes, expose (true) or unexpose (false) and wait for the operation.
	if !plan.Public.Equal(state.Public) {
		if err := r.setExposure(ctx, id, plan.Public.ValueBool()); err != nil {
			resp.Diagnostics.AddError("Failed to change Nginx instance public exposure", err.Error())
			return
		}
	}

	// Refresh state from API.
	apiResp, err := r.client.Get(ctx, r.client.TenantPath("/webservers/"+id), nil)
	if err != nil {
		resp.Diagnostics.AddError("Failed to read Nginx instance after update", err.Error())
		return
	}

	inst, err := client.ParseResponse[apiWebserverInstance](apiResp)
	if err != nil {
		resp.Diagnostics.AddError("Failed to parse Nginx instance response", err.Error())
		return
	}

	plan.fromAPI(ctx, inst, &resp.Diagnostics)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *nginxInstanceResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state NginxInstanceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	id := state.ID.ValueString()

	_, err := r.client.Delete(ctx, r.client.TenantPath("/webservers/"+id))
	if err != nil {
		if client.IsNotFound(err) {
			return
		}
		resp.Diagnostics.AddError("Failed to delete Nginx instance", err.Error())
		return
	}

	// Wait for the instance to be fully deleted (404 on GET).
	_, err = client.WaitForState(ctx, client.PollConfig{
		Interval:     r.getPollInterval(),
		Timeout:      r.getPollTimeout(),
		TargetStates: []string{"deleted"},
		ErrorStates:  []string{"error"},
		ResourceName: "nginx_instance",
		PollFunc: func(pollCtx context.Context) (string, error) {
			pollResp, pollErr := r.client.Get(pollCtx, r.client.TenantPath("/webservers/"+id), nil)
			if pollErr != nil {
				if client.IsNotFound(pollErr) {
					return "deleted", nil
				}
				return "", pollErr
			}
			current, parseErr := client.ParseResponse[apiWebserverInstance](pollResp)
			if parseErr != nil {
				return "", parseErr
			}
			return current.Status, nil
		},
	})
	if err != nil {
		resp.Diagnostics.AddError("Nginx instance failed to delete", err.Error())
	}
}

func (r *nginxInstanceResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}

// UpgradeState migrates v0 state (HCL attribute `flavor`) to v1 (`flavor_id`).
// The rename is HCL-surface only -- the wire tag was always flavorId -- so the
// migration is purely local: it copies the prior `flavor` value into `flavor_id`
// and carries every other attribute through unchanged. `flavor_id` is not
// RequiresReplace (flavor changes are now rejected at plan time), so without
// this the first post-upgrade plan would show a spurious diff rather than a
// destroy; copying the same value keeps the upgrade a clean no-op.
func (r *nginxInstanceResource) UpgradeState(ctx context.Context) map[int64]resource.StateUpgrader {
	schemaResp := resource.SchemaResponse{}
	r.Schema(ctx, resource.SchemaRequest{}, &schemaResp)
	return map[int64]resource.StateUpgrader{
		0: stateupgrade.RenameStringAttr(ctx, schemaResp.Schema, "flavor", "flavor_id"),
	}
}
