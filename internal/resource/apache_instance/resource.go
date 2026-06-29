package apache_instance

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

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
)

var (
	_ resource.Resource                = &apacheInstanceResource{}
	_ resource.ResourceWithImportState = &apacheInstanceResource{}
)

// NewResource returns a new apache_instance resource factory.
func NewResource() resource.Resource {
	return &apacheInstanceResource{}
}

type apacheInstanceResource struct {
	client       *client.Client
	pollInterval time.Duration
	pollTimeout  time.Duration
}

func (r *apacheInstanceResource) getPollInterval() time.Duration {
	if r.pollInterval > 0 {
		return r.pollInterval
	}
	return 5 * time.Second
}

func (r *apacheInstanceResource) getPollTimeout() time.Duration {
	if r.pollTimeout > 0 {
		return r.pollTimeout
	}
	return 15 * time.Minute
}

func (r *apacheInstanceResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_apache_instance"
}

func (r *apacheInstanceResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manages a managed Apache webserver instance in the Frostmoln platform.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Description: "The unique identifier of the Apache instance.",
				Computed:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"name": schema.StringAttribute{
				Description: "The name of the Apache instance.",
				Required:    true,
			},
			"version": schema.StringAttribute{
				Description: "The Apache version (e.g. \"2.4\").",
				Required:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"flavor": schema.StringAttribute{
				Description: "The flavor/size for the webserver instance (e.g. \"web.gp1.small\", \"web.gp1.medium\").",
				Required:    true,
			},
			"storage_gb": schema.Int64Attribute{
				Description: "The storage size in gigabytes.",
				Required:    true,
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
				Description: "Whether PHP support is enabled.",
				Optional:    true,
				Computed:    true,
				PlanModifiers: []planmodifier.Bool{
					boolplanmodifier.UseStateForUnknown(),
				},
			},
			"php_version": schema.StringAttribute{
				Description: "The PHP version (e.g. \"8.2\", \"8.3\"). Only applicable when php_enabled is true.",
				Optional:    true,
			},
			"config": schema.StringAttribute{
				Description: "Custom engine configuration as a JSON string.",
				Optional:    true,
			},
			"status": schema.StringAttribute{
				Description: "The current status of the Apache instance.",
				Computed:    true,
			},
			"private_ip": schema.StringAttribute{
				Description: "The private IP address of the Apache instance.",
				Computed:    true,
			},
			"port": schema.Int64Attribute{
				Description: "The port number the Apache instance is listening on.",
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

func (r *apacheInstanceResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (r *apacheInstanceResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan ApacheInstanceModel
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
		resp.Diagnostics.AddError("Failed to create Apache instance", err.Error())
		return
	}

	inst, err := client.ParseResponse[apiWebserverInstance](apiResp)
	if err != nil {
		resp.Diagnostics.AddError("Failed to parse Apache instance response", err.Error())
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
		ResourceName: "apache_instance",
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
		resp.Diagnostics.AddError("Apache instance failed to reach running state", err.Error())
		return
	}

	// Refresh state after polling completes to get final status, IPs, etc.
	readResp, err := r.client.Get(ctx, r.client.TenantPath("/webservers/"+inst.ID), nil)
	if err != nil {
		resp.Diagnostics.AddError("Failed to read Apache instance after creation", err.Error())
		return
	}
	finalInst, err := client.ParseResponse[apiWebserverInstance](readResp)
	if err != nil {
		resp.Diagnostics.AddError("Failed to parse Apache instance response", err.Error())
		return
	}

	plan.fromAPI(ctx, finalInst, &resp.Diagnostics)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *apacheInstanceResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state ApacheInstanceModel
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
		resp.Diagnostics.AddError("Failed to read Apache instance", err.Error())
		return
	}

	inst, err := client.ParseResponse[apiWebserverInstance](apiResp)
	if err != nil {
		resp.Diagnostics.AddError("Failed to parse Apache instance response", err.Error())
		return
	}

	state.fromAPI(ctx, inst, &resp.Diagnostics)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *apacheInstanceResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan ApacheInstanceModel
	var state ApacheInstanceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	id := state.ID.ValueString()

	updateReq := plan.toUpdateRequest(&state)
	_, err := r.client.Put(ctx, r.client.TenantPath("/webservers/"+id), updateReq)
	if err != nil {
		resp.Diagnostics.AddError("Failed to update Apache instance", err.Error())
		return
	}

	// Poll until instance is back to "running" after the update.
	_, err = client.WaitForState(ctx, client.PollConfig{
		Interval:     r.getPollInterval(),
		Timeout:      r.getPollTimeout(),
		TargetStates: []string{"running"},
		ErrorStates:  []string{"error", "failed"},
		ResourceName: "apache_instance",
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
		resp.Diagnostics.AddError("Apache instance failed to reach running state after update", err.Error())
		return
	}

	// Refresh state from API.
	apiResp, err := r.client.Get(ctx, r.client.TenantPath("/webservers/"+id), nil)
	if err != nil {
		resp.Diagnostics.AddError("Failed to read Apache instance after update", err.Error())
		return
	}

	inst, err := client.ParseResponse[apiWebserverInstance](apiResp)
	if err != nil {
		resp.Diagnostics.AddError("Failed to parse Apache instance response", err.Error())
		return
	}

	plan.fromAPI(ctx, inst, &resp.Diagnostics)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *apacheInstanceResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state ApacheInstanceModel
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
		resp.Diagnostics.AddError("Failed to delete Apache instance", err.Error())
		return
	}

	// Wait for the instance to be fully deleted (404 on GET).
	_, err = client.WaitForState(ctx, client.PollConfig{
		Interval:     r.getPollInterval(),
		Timeout:      r.getPollTimeout(),
		TargetStates: []string{"deleted"},
		ErrorStates:  []string{"error"},
		ResourceName: "apache_instance",
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
		resp.Diagnostics.AddError("Apache instance failed to delete", err.Error())
	}
}

func (r *apacheInstanceResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}
