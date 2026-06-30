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
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/stateupgrade"
)

var (
	_ resource.Resource                 = &nginxInstanceResource{}
	_ resource.ResourceWithImportState  = &nginxInstanceResource{}
	_ resource.ResourceWithUpgradeState = &nginxInstanceResource{}
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
			},
			"storage_gb": schema.Int64Attribute{
				Description: "The storage size in gigabytes.",
				Required:    true,
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
			"config": schema.MapAttribute{
				Description: "Engine-specific configuration as key/value pairs (sent as the engineConfig object).",
				Optional:    true,
				Computed:    true,
				ElementType: types.StringType,
				PlanModifiers: []planmodifier.Map{
					mapplanmodifier.UseStateForUnknown(),
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

	apiReq := plan.toCreateRequest(ctx, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}

	apiResp, err := r.client.Post(ctx, r.client.TenantPath("/webservers"), apiReq)
	if err != nil {
		resp.Diagnostics.AddError("Failed to create Nginx instance", err.Error())
		return
	}

	inst, err := client.ParseResponse[apiWebserverInstance](apiResp)
	if err != nil {
		resp.Diagnostics.AddError("Failed to parse Nginx instance response", err.Error())
		return
	}

	plan.fromAPI(ctx, inst, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}

	// Save state immediately so the ID is tracked, even if polling fails.
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// Poll until instance reaches "running" status.
	_, err = client.WaitForState(ctx, client.PollConfig{
		Interval:     r.getPollInterval(),
		Timeout:      r.getPollTimeout(),
		TargetStates: []string{"running"},
		ErrorStates:  []string{"error", "failed"},
		ResourceName: "nginx_instance",
		PollFunc: func(pollCtx context.Context) (string, error) {
			pollResp, pollErr := r.client.Get(pollCtx, r.client.TenantPath("/webservers/"+inst.ID), nil)
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

	// Refresh state after polling completes to get final status, IPs, etc.
	readResp, err := r.client.Get(ctx, r.client.TenantPath("/webservers/"+inst.ID), nil)
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

	updateReq := plan.toUpdateRequest(ctx, &state, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	_, err := r.client.Put(ctx, r.client.TenantPath("/webservers/"+id), updateReq)
	if err != nil {
		resp.Diagnostics.AddError("Failed to update Nginx instance", err.Error())
		return
	}

	// Poll until instance is back to "running" after the update.
	_, err = client.WaitForState(ctx, client.PollConfig{
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
	if err != nil {
		resp.Diagnostics.AddError("Nginx instance failed to reach running state after update", err.Error())
		return
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
// and carries every other attribute through unchanged. `flavor` is in-place
// updatable (not RequiresReplace), so without this the first post-upgrade plan
// would show a spurious update rather than a destroy; the upgrader keeps the
// upgrade a clean no-op.
func (r *nginxInstanceResource) UpgradeState(ctx context.Context) map[int64]resource.StateUpgrader {
	schemaResp := resource.SchemaResponse{}
	r.Schema(ctx, resource.SchemaRequest{}, &schemaResp)
	return map[int64]resource.StateUpgrader{
		0: stateupgrade.RenameStringAttr(ctx, schemaResp.Schema, "flavor", "flavor_id"),
	}
}
