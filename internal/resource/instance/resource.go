package instance

import (
	"context"
	"fmt"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/mapplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/setplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
)

var (
	_ resource.Resource                = &instanceResource{}
	_ resource.ResourceWithImportState = &instanceResource{}
)

// NewResource returns a new instance resource factory.
func NewResource() resource.Resource {
	return &instanceResource{}
}

type instanceResource struct {
	client       *client.Client
	pollInterval time.Duration // overridable for tests; defaults to 5s
	pollTimeout  time.Duration // overridable for tests; defaults to 10m
}

func (r *instanceResource) getPollInterval() time.Duration {
	if r.pollInterval > 0 {
		return r.pollInterval
	}
	return 5 * time.Second
}

func (r *instanceResource) getPollTimeout() time.Duration {
	if r.pollTimeout > 0 {
		return r.pollTimeout
	}
	return 10 * time.Minute
}

func (r *instanceResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_instance"
}

func (r *instanceResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manages a compute instance in the Frostmoln platform.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Description: "The unique identifier of the instance.",
				Computed:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"name": schema.StringAttribute{
				Description: "The name of the instance.",
				Required:    true,
			},
			"flavor_id": schema.StringAttribute{
				Description: "The flavor ID for the instance. Changing this triggers a resize workflow (stop, resize, start).",
				Required:    true,
			},
			"image_id": schema.StringAttribute{
				Description: "The image ID to use for the instance.",
				Required:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"zone": schema.StringAttribute{
				Description: "The availability zone for the instance.",
				Optional:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"vpc_id": schema.StringAttribute{
				Description: "The VPC ID for the instance.",
				Optional:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"subnet_id": schema.StringAttribute{
				Description: "The subnet ID for the instance.",
				Optional:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"security_groups": schema.SetAttribute{
				// The compute update API has no security-group field — SGs can only
				// be set at create — so a change forces replacement (like ssh_key_names).
				Description: "The security group IDs attached to the instance. Changing this forces a new instance (the API does not support changing security groups in place).",
				Optional:    true,
				ElementType: types.StringType,
				PlanModifiers: []planmodifier.Set{
					setplanmodifier.RequiresReplace(),
				},
			},
			"ssh_key_names": schema.SetAttribute{
				Description: "The SSH key names to inject into the instance.",
				Optional:    true,
				ElementType: types.StringType,
				PlanModifiers: []planmodifier.Set{
					setplanmodifier.RequiresReplace(),
				},
			},
			"user_data": schema.StringAttribute{
				Description: "User data to provide to the instance at launch. This is write-only; the API does not return it. A SHA256 hash is stored in state for change detection.",
				Optional:    true,
				Sensitive:   true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"console_password": schema.StringAttribute{
				Description: "Password for the default OS user, usable only at the VNC console; SSH stays key-only. Changing forces replacement.",
				Optional:    true,
				Sensitive:   true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"user_data_hash": schema.StringAttribute{
				Description: "SHA256 hash of the user data, used for change detection.",
				Computed:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"tags": schema.MapAttribute{
				Description: "Key-value tags for the instance.",
				Optional:    true,
				ElementType: types.StringType,
				PlanModifiers: []planmodifier.Map{
					mapplanmodifier.UseStateForUnknown(),
				},
			},
			"status": schema.StringAttribute{
				Description: "The current status of the instance.",
				Computed:    true,
			},
			"flavor_name": schema.StringAttribute{
				Description: "The name of the instance flavor.",
				Computed:    true,
			},
			"image_name": schema.StringAttribute{
				Description: "The name of the image used to create the instance.",
				Computed:    true,
			},
			"private_ip": schema.StringAttribute{
				Description: "The private IP address of the instance.",
				Computed:    true,
			},
			"public_ip": schema.StringAttribute{
				Description: "The public IP address of the instance, if assigned.",
				Computed:    true,
			},
			"created_at": schema.StringAttribute{
				Description: "The timestamp when the instance was created.",
				Computed:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
		},
	}
}

func (r *instanceResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (r *instanceResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan InstanceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	apiReq := plan.toCreateRequest(ctx, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}

	apiResp, err := r.client.Post(ctx, r.client.TenantPath("/instances"), apiReq)
	if err != nil {
		resp.Diagnostics.AddError("Failed to create instance", err.Error())
		return
	}

	// Instance create routes through provisioning, which returns 202 + an Operation
	// envelope (operationId only, NOT the instance). Poll the operation to
	// completion (the workflow waits for the instance to reach running before
	// completing), then read by its resolved resourceId. A 201 with the instance
	// body is still accepted for a synchronous backend. Mirrors the volume +
	// snapshot + load_balancer resources.
	var instanceID string
	if apiResp.IsAccepted() {
		op, opErr := client.ParseResponse[client.Operation](apiResp)
		if opErr != nil {
			resp.Diagnostics.AddError("Failed to parse operation response", opErr.Error())
			return
		}
		done, waitErr := r.client.WaitForOperation(ctx, op.OperationID, r.getPollInterval(), r.getPollTimeout())
		if waitErr != nil {
			resp.Diagnostics.AddError("Instance failed to reach running state", waitErr.Error())
			return
		}
		instanceID = done.ResourceID
	} else {
		inst, parseErr := client.ParseResponse[apiInstance](apiResp)
		if parseErr != nil {
			resp.Diagnostics.AddError("Failed to parse instance response", parseErr.Error())
			return
		}
		instanceID = inst.ID
	}
	if instanceID == "" {
		resp.Diagnostics.AddError(
			"Instance Operation Returned No Resource ID",
			"The instance create operation completed but returned no resource ID. The instance may exist in the backend without being tracked in Terraform state - check `fm compute instance list` and import it if necessary.",
		)
		return
	}

	// Store user_data hash before fromAPI (which doesn't touch user_data fields)
	if !plan.UserData.IsNull() && !plan.UserData.IsUnknown() {
		plan.UserDataHash = types.StringValue(computeUserDataHash(plan.UserData.ValueString()))
	} else {
		plan.UserDataHash = types.StringNull()
	}

	// Read the final state (the operation completion means the instance is running).
	readResp, err := r.client.Get(ctx, r.client.TenantPath("/instances/"+instanceID), nil)
	if err != nil {
		resp.Diagnostics.AddError("Failed to read instance after creation", err.Error())
		return
	}
	finalInst, err := client.ParseResponse[apiInstance](readResp)
	if err != nil {
		resp.Diagnostics.AddError("Failed to parse instance response", err.Error())
		return
	}

	plan.fromAPI(ctx, finalInst, &resp.Diagnostics)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *instanceResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state InstanceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// Preserve write-only fields before refreshing from API.
	savedUserData := state.UserData
	savedUserDataHash := state.UserDataHash

	apiResp, err := r.client.Get(ctx, r.client.TenantPath("/instances/"+state.ID.ValueString()), nil)
	if err != nil {
		if client.IsNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Failed to read instance", err.Error())
		return
	}

	inst, err := client.ParseResponse[apiInstance](apiResp)
	if err != nil {
		resp.Diagnostics.AddError("Failed to parse instance response", err.Error())
		return
	}

	state.fromAPI(ctx, inst, &resp.Diagnostics)

	// Restore write-only fields that the API doesn't return.
	state.UserData = savedUserData
	state.UserDataHash = savedUserDataHash

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *instanceResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan InstanceModel
	var state InstanceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	id := state.ID.ValueString()

	// Preserve write-only fields.
	plan.UserData = state.UserData
	plan.UserDataHash = state.UserDataHash

	// Check if flavor_id changed (resize workflow).
	if !plan.FlavorID.Equal(state.FlavorID) {
		if err := r.resizeInstance(ctx, id, plan.FlavorID.ValueString()); err != nil {
			resp.Diagnostics.AddError("Failed to resize instance", err.Error())
			return
		}
	}

	// Only name + tags are updatable in place (security_groups is RequiresReplace —
	// the compute update API has no SG field).
	nameChanged := !plan.Name.Equal(state.Name)
	tagsChanged := !plan.Tags.Equal(state.Tags)

	if nameChanged || tagsChanged {
		updateReq := plan.toUpdateRequest(ctx, &resp.Diagnostics)
		if resp.Diagnostics.HasError() {
			return
		}
		_, err := r.client.Patch(ctx, r.client.TenantPath("/instances/"+id), updateReq)
		if err != nil {
			resp.Diagnostics.AddError("Failed to update instance", err.Error())
			return
		}
	}

	// Refresh state from API.
	apiResp, err := r.client.Get(ctx, r.client.TenantPath("/instances/"+id), nil)
	if err != nil {
		resp.Diagnostics.AddError("Failed to read instance after update", err.Error())
		return
	}

	inst, err := client.ParseResponse[apiInstance](apiResp)
	if err != nil {
		resp.Diagnostics.AddError("Failed to parse instance response", err.Error())
		return
	}

	plan.fromAPI(ctx, inst, &resp.Diagnostics)

	// Restore write-only fields.
	plan.UserData = state.UserData
	plan.UserDataHash = state.UserDataHash

	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// resizeInstance performs the stop -> resize -> start workflow.
func (r *instanceResource) resizeInstance(ctx context.Context, id, newFlavorID string) error {
	base := r.client.TenantPath(fmt.Sprintf("/instances/%s", id))

	// 1. Stop the instance (discrete route; the backend has no /action endpoint).
	_, err := r.client.Post(ctx, base+"/stop", nil)
	if err != nil {
		return fmt.Errorf("failed to stop instance for resize: %w", err)
	}

	// 2. Wait for stopped state.
	_, err = client.WaitForState(ctx, client.PollConfig{
		Interval:     r.getPollInterval(),
		Timeout:      r.getPollTimeout(),
		TargetStates: []string{"stopped"},
		ErrorStates:  []string{"error"},
		ResourceName: "instance",
		PollFunc: func(pollCtx context.Context) (string, error) {
			pollResp, pollErr := r.client.Get(pollCtx, r.client.TenantPath("/instances/"+id), nil)
			if pollErr != nil {
				return "", pollErr
			}
			inst, parseErr := client.ParseResponse[apiInstance](pollResp)
			if parseErr != nil {
				return "", parseErr
			}
			return inst.Status, nil
		},
	})
	if err != nil {
		return fmt.Errorf("instance failed to reach stopped state: %w", err)
	}

	// 3. Resize the instance.
	_, err = r.client.Post(ctx, base+"/resize", apiResizeInstanceRequest{FlavorID: newFlavorID})
	if err != nil {
		return fmt.Errorf("failed to resize instance: %w", err)
	}

	// 4. Start the instance.
	_, err = r.client.Post(ctx, base+"/start", nil)
	if err != nil {
		return fmt.Errorf("failed to start instance after resize: %w", err)
	}

	// 5. Wait for running state.
	_, err = client.WaitForState(ctx, client.PollConfig{
		Interval:     r.getPollInterval(),
		Timeout:      r.getPollTimeout(),
		TargetStates: []string{"running"},
		ErrorStates:  []string{"error"},
		ResourceName: "instance",
		PollFunc: func(pollCtx context.Context) (string, error) {
			pollResp, pollErr := r.client.Get(pollCtx, r.client.TenantPath("/instances/"+id), nil)
			if pollErr != nil {
				return "", pollErr
			}
			inst, parseErr := client.ParseResponse[apiInstance](pollResp)
			if parseErr != nil {
				return "", parseErr
			}
			return inst.Status, nil
		},
	})
	if err != nil {
		return fmt.Errorf("instance failed to reach running state after resize: %w", err)
	}

	return nil
}

func (r *instanceResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state InstanceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	id := state.ID.ValueString()

	_, err := r.client.Delete(ctx, r.client.TenantPath("/instances/"+id))
	if err != nil {
		if client.IsNotFound(err) {
			return
		}
		resp.Diagnostics.AddError("Failed to delete instance", err.Error())
		return
	}

	// Wait for the instance to be fully deleted (404 on GET).
	_, err = client.WaitForState(ctx, client.PollConfig{
		Interval:     r.getPollInterval(),
		Timeout:      r.getPollTimeout(),
		TargetStates: []string{"deleted"},
		ErrorStates:  []string{"error"},
		ResourceName: "instance",
		PollFunc: func(pollCtx context.Context) (string, error) {
			pollResp, pollErr := r.client.Get(pollCtx, r.client.TenantPath("/instances/"+id), nil)
			if pollErr != nil {
				if client.IsNotFound(pollErr) {
					return "deleted", nil
				}
				return "", pollErr
			}
			inst, parseErr := client.ParseResponse[apiInstance](pollResp)
			if parseErr != nil {
				return "", parseErr
			}
			return inst.Status, nil
		},
	})
	if err != nil {
		resp.Diagnostics.AddError("Instance failed to delete", err.Error())
	}
}

func (r *instanceResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}
