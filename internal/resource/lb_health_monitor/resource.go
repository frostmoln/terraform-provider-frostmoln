package lb_health_monitor

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64default"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
)

var (
	_ resource.Resource                = &healthMonitorResource{}
	_ resource.ResourceWithImportState = &healthMonitorResource{}
)

type healthMonitorResource struct {
	client *client.Client
}

// NewResource returns a new health monitor resource factory.
func NewResource() resource.Resource {
	return &healthMonitorResource{}
}

func (r *healthMonitorResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_lb_health_monitor"
}

func (r *healthMonitorResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manages the health monitor of a Frostmoln load balancer pool. A pool has at most one health monitor (singleton).",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Description: "The unique identifier of the health monitor.",
				Computed:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"load_balancer_id": schema.StringAttribute{
				Description: "The ID of the load balancer the pool belongs to. Changing this forces a new resource.",
				Required:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"pool_id": schema.StringAttribute{
				Description: "The ID of the pool this health monitor belongs to. Changing this forces a new resource.",
				Required:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"type": schema.StringAttribute{
				Description: "The health check type: tcp, http, or https. Changing this forces a new resource.",
				Required:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
				Validators: []validator.String{
					stringvalidator.OneOf("tcp", "http", "https"),
				},
			},
			"delay": schema.Int64Attribute{
				Description: "The interval in seconds between health checks.",
				Optional:    true,
				Computed:    true,
				Default:     int64default.StaticInt64(5),
			},
			"timeout": schema.Int64Attribute{
				Description: "The time in seconds to wait for a health check response.",
				Optional:    true,
				Computed:    true,
				Default:     int64default.StaticInt64(3),
			},
			"max_retries": schema.Int64Attribute{
				Description: "The number of successful checks before a member is marked healthy.",
				Optional:    true,
				Computed:    true,
				Default:     int64default.StaticInt64(3),
			},
			"url_path": schema.StringAttribute{
				Description: "The HTTP path to probe (http/https monitors).",
				Optional:    true,
			},
			"http_method": schema.StringAttribute{
				Description: "The HTTP method used for the health check (http/https monitors).",
				Optional:    true,
			},
			"expected_codes": schema.StringAttribute{
				Description: "The HTTP status codes considered healthy (http/https monitors), e.g. \"200\" or \"200-299\".",
				Optional:    true,
			},
			"created_at": schema.StringAttribute{
				Description: "The creation timestamp.",
				Computed:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"updated_at": schema.StringAttribute{
				Description: "The last update timestamp.",
				Computed:    true,
			},
		},
	}
}

func (r *healthMonitorResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	c, ok := req.ProviderData.(*client.Client)
	if !ok {
		resp.Diagnostics.AddError(
			"Unexpected Provider Data",
			fmt.Sprintf("Expected *client.Client, got %T", req.ProviderData),
		)
		return
	}
	r.client = c
}

// monitorPath builds the singleton health monitor path for a pool.
func (r *healthMonitorResource) monitorPath(lbID, poolID string) string {
	return r.client.TenantPath(fmt.Sprintf("/load-balancers/%s/pools/%s/healthmonitor", lbID, poolID))
}

func (r *healthMonitorResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan HealthMonitorModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	lbID := plan.LoadBalancerID.ValueString()
	poolID := plan.PoolID.ValueString()
	createReq := plan.toCreateRequest()

	apiResp, err := r.client.Post(ctx, r.monitorPath(lbID, poolID), createReq)
	if err != nil {
		resp.Diagnostics.AddError("Failed to Create Health Monitor", err.Error())
		return
	}

	// Health-monitor create routes through provisioning → 202 + an Operation
	// envelope (operationId only). Poll the operation, then re-read the monitor
	// (one per pool — no id in the path). A non-202 body is parsed directly for a
	// sync backend.
	var hm *apiHealthMonitor
	if apiResp.IsAccepted() {
		op, opErr := client.ParseResponse[client.Operation](apiResp)
		if opErr != nil {
			resp.Diagnostics.AddError("Failed to Parse Operation Response", opErr.Error())
			return
		}
		if _, waitErr := r.client.WaitForOperation(ctx, op.OperationID, 2*time.Second, 5*time.Minute); waitErr != nil {
			resp.Diagnostics.AddError("Health Monitor Creation Failed", waitErr.Error())
			return
		}
		readResp, readErr := r.client.Get(ctx, r.monitorPath(lbID, poolID), nil)
		if readErr != nil {
			resp.Diagnostics.AddError("Failed to Read Health Monitor After Creation", readErr.Error())
			return
		}
		hm, err = client.ParseResponse[apiHealthMonitor](readResp)
		if err != nil {
			resp.Diagnostics.AddError("Failed to Parse Health Monitor Response", err.Error())
			return
		}
	} else {
		hm, err = client.ParseResponse[apiHealthMonitor](apiResp)
		if err != nil {
			resp.Diagnostics.AddError("Failed to Parse Health Monitor Response", err.Error())
			return
		}
	}

	plan.fromAPI(lbID, hm)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *healthMonitorResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state HealthMonitorModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	lbID := state.LoadBalancerID.ValueString()
	poolID := state.PoolID.ValueString()

	apiResp, err := r.client.Get(ctx, r.monitorPath(lbID, poolID), nil)
	if err != nil {
		if client.IsNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Failed to Read Health Monitor", err.Error())
		return
	}

	hm, err := client.ParseResponse[apiHealthMonitor](apiResp)
	if err != nil {
		resp.Diagnostics.AddError("Failed to Parse Health Monitor Response", err.Error())
		return
	}

	state.fromAPI(lbID, hm)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *healthMonitorResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan HealthMonitorModel
	var state HealthMonitorModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	lbID := state.LoadBalancerID.ValueString()
	poolID := state.PoolID.ValueString()
	updateReq := plan.toUpdateRequest()

	apiResp, err := r.client.Put(ctx, r.monitorPath(lbID, poolID), updateReq)
	if err != nil {
		resp.Diagnostics.AddError("Failed to Update Health Monitor", err.Error())
		return
	}

	hm, err := client.ParseResponse[apiHealthMonitor](apiResp)
	if err != nil {
		resp.Diagnostics.AddError("Failed to Parse Health Monitor Response", err.Error())
		return
	}

	plan.fromAPI(lbID, hm)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *healthMonitorResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state HealthMonitorModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	lbID := state.LoadBalancerID.ValueString()
	poolID := state.PoolID.ValueString()

	_, err := r.client.Delete(ctx, r.monitorPath(lbID, poolID))
	if err != nil {
		if client.IsNotFound(err) {
			return
		}
		resp.Diagnostics.AddError("Failed to Delete Health Monitor", err.Error())
		return
	}
}

func (r *healthMonitorResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	// Import ID format: {load_balancer_id}/{pool_id}
	parts := strings.SplitN(req.ID, "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		resp.Diagnostics.AddError(
			"Invalid Import ID",
			fmt.Sprintf("Expected import ID format: {load_balancer_id}/{pool_id}, got: %s", req.ID),
		)
		return
	}

	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("load_balancer_id"), parts[0])...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("pool_id"), parts[1])...)
}
