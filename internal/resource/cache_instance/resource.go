package cache_instance

import (
	"context"
	"fmt"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"

	"git.nl.cloud/NordicLight/terraform-provider-frostmoln/internal/client"
)

var (
	_ resource.Resource                = &cacheInstanceResource{}
	_ resource.ResourceWithImportState = &cacheInstanceResource{}
)

// NewResource returns a new cache_instance resource factory.
func NewResource() resource.Resource {
	return &cacheInstanceResource{}
}

type cacheInstanceResource struct {
	client       *client.Client
	pollInterval time.Duration
	pollTimeout  time.Duration
}

func (r *cacheInstanceResource) getPollInterval() time.Duration {
	if r.pollInterval > 0 {
		return r.pollInterval
	}
	return 5 * time.Second
}

func (r *cacheInstanceResource) getPollTimeout() time.Duration {
	if r.pollTimeout > 0 {
		return r.pollTimeout
	}
	return 15 * time.Minute
}

func (r *cacheInstanceResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_cache_instance"
}

func (r *cacheInstanceResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manages a managed cache instance in the NordicLight platform.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Description: "The unique identifier of the cache instance.",
				Computed:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"name": schema.StringAttribute{
				Description: "The name of the cache instance.",
				Required:    true,
			},
			"engine": schema.StringAttribute{
				Description: "The cache engine type (e.g. \"redis\", \"valkey\").",
				Required:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"engine_version": schema.StringAttribute{
				Description: "The engine version (e.g. \"7.2\", \"7.4\").",
				Required:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"flavor_id": schema.StringAttribute{
				Description: "The flavor/size for the cache instance (e.g. \"cache.small\", \"cache.medium\").",
				Required:    true,
			},
			"vpc_id": schema.StringAttribute{
				Description: "The VPC ID where the cache instance will be deployed.",
				Required:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"subnet_id": schema.StringAttribute{
				Description: "The subnet ID where the cache instance will be deployed.",
				Required:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"persistence_mode": schema.StringAttribute{
				Description: "The persistence mode for the cache instance (\"rdb\", \"aof\", or \"none\"). Defaults to \"rdb\".",
				Optional:    true,
				Computed:    true,
				Default:     stringdefault.StaticString("rdb"),
			},
			"eviction_policy": schema.StringAttribute{
				Description: "The eviction policy for the cache instance (e.g. \"noeviction\", \"allkeys-lru\"). Defaults to \"noeviction\".",
				Optional:    true,
				Computed:    true,
				Default:     stringdefault.StaticString("noeviction"),
			},
			"status": schema.StringAttribute{
				Description: "The current status of the cache instance.",
				Computed:    true,
			},
			"private_ip": schema.StringAttribute{
				Description: "The private IP address of the cache instance.",
				Computed:    true,
			},
			"port": schema.Int64Attribute{
				Description: "The port number the cache instance is listening on.",
				Computed:    true,
				PlanModifiers: []planmodifier.Int64{
					int64planmodifier.UseStateForUnknown(),
				},
			},
			"admin_username": schema.StringAttribute{
				Description: "The admin username for the cache instance.",
				Computed:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
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
	}
}

func (r *cacheInstanceResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (r *cacheInstanceResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan CacheInstanceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	apiReq := plan.toCreateRequest(ctx, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}

	apiResp, err := r.client.Post(ctx, r.client.TenantPath("/caches"), apiReq)
	if err != nil {
		resp.Diagnostics.AddError("Failed to create cache instance", err.Error())
		return
	}

	inst, err := client.ParseResponse[apiCacheInstance](apiResp)
	if err != nil {
		resp.Diagnostics.AddError("Failed to parse cache instance response", err.Error())
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
		ResourceName: "cache_instance",
		PollFunc: func(pollCtx context.Context) (string, error) {
			pollResp, pollErr := r.client.Get(pollCtx, r.client.TenantPath("/caches/"+inst.ID), nil)
			if pollErr != nil {
				return "", pollErr
			}
			current, parseErr := client.ParseResponse[apiCacheInstance](pollResp)
			if parseErr != nil {
				return "", parseErr
			}
			return current.Status, nil
		},
	})
	if err != nil {
		resp.Diagnostics.AddError("Cache instance failed to reach running state", err.Error())
		return
	}

	// Refresh state after polling completes to get final status, IPs, etc.
	readResp, err := r.client.Get(ctx, r.client.TenantPath("/caches/"+inst.ID), nil)
	if err != nil {
		resp.Diagnostics.AddError("Failed to read cache instance after creation", err.Error())
		return
	}
	finalInst, err := client.ParseResponse[apiCacheInstance](readResp)
	if err != nil {
		resp.Diagnostics.AddError("Failed to parse cache instance response", err.Error())
		return
	}

	plan.fromAPI(ctx, finalInst, &resp.Diagnostics)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *cacheInstanceResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state CacheInstanceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	apiResp, err := r.client.Get(ctx, r.client.TenantPath("/caches/"+state.ID.ValueString()), nil)
	if err != nil {
		if client.IsNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Failed to read cache instance", err.Error())
		return
	}

	inst, err := client.ParseResponse[apiCacheInstance](apiResp)
	if err != nil {
		resp.Diagnostics.AddError("Failed to parse cache instance response", err.Error())
		return
	}

	state.fromAPI(ctx, inst, &resp.Diagnostics)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *cacheInstanceResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan CacheInstanceModel
	var state CacheInstanceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	id := state.ID.ValueString()

	updateReq := plan.toUpdateRequest(&state)
	_, err := r.client.Put(ctx, r.client.TenantPath("/caches/"+id), updateReq)
	if err != nil {
		resp.Diagnostics.AddError("Failed to update cache instance", err.Error())
		return
	}

	// Poll until instance is back to "running" after the update.
	_, err = client.WaitForState(ctx, client.PollConfig{
		Interval:     r.getPollInterval(),
		Timeout:      r.getPollTimeout(),
		TargetStates: []string{"running"},
		ErrorStates:  []string{"error", "failed"},
		ResourceName: "cache_instance",
		PollFunc: func(pollCtx context.Context) (string, error) {
			pollResp, pollErr := r.client.Get(pollCtx, r.client.TenantPath("/caches/"+id), nil)
			if pollErr != nil {
				return "", pollErr
			}
			current, parseErr := client.ParseResponse[apiCacheInstance](pollResp)
			if parseErr != nil {
				return "", parseErr
			}
			return current.Status, nil
		},
	})
	if err != nil {
		resp.Diagnostics.AddError("Cache instance failed to reach running state after update", err.Error())
		return
	}

	// Refresh state from API.
	apiResp, err := r.client.Get(ctx, r.client.TenantPath("/caches/"+id), nil)
	if err != nil {
		resp.Diagnostics.AddError("Failed to read cache instance after update", err.Error())
		return
	}

	inst, err := client.ParseResponse[apiCacheInstance](apiResp)
	if err != nil {
		resp.Diagnostics.AddError("Failed to parse cache instance response", err.Error())
		return
	}

	plan.fromAPI(ctx, inst, &resp.Diagnostics)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *cacheInstanceResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state CacheInstanceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	id := state.ID.ValueString()

	_, err := r.client.Delete(ctx, r.client.TenantPath("/caches/"+id))
	if err != nil {
		if client.IsNotFound(err) {
			return
		}
		resp.Diagnostics.AddError("Failed to delete cache instance", err.Error())
		return
	}

	// Wait for the instance to be fully deleted (404 on GET).
	_, err = client.WaitForState(ctx, client.PollConfig{
		Interval:     r.getPollInterval(),
		Timeout:      r.getPollTimeout(),
		TargetStates: []string{"deleted"},
		ErrorStates:  []string{"error"},
		ResourceName: "cache_instance",
		PollFunc: func(pollCtx context.Context) (string, error) {
			pollResp, pollErr := r.client.Get(pollCtx, r.client.TenantPath("/caches/"+id), nil)
			if pollErr != nil {
				if client.IsNotFound(pollErr) {
					return "deleted", nil
				}
				return "", pollErr
			}
			current, parseErr := client.ParseResponse[apiCacheInstance](pollResp)
			if parseErr != nil {
				return "", parseErr
			}
			return current.Status, nil
		},
	})
	if err != nil {
		resp.Diagnostics.AddError("Cache instance failed to delete", err.Error())
	}
}

func (r *cacheInstanceResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}
