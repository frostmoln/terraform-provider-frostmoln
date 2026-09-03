// Package appgw_health_check implements the frostmoln_appgw_health_check
// Terraform resource.
package appgw_health_check

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
)

var (
	_ resource.Resource                   = &healthCheckResource{}
	_ resource.ResourceWithImportState    = &healthCheckResource{}
	_ resource.ResourceWithConfigure      = &healthCheckResource{}
	_ resource.ResourceWithValidateConfig = &healthCheckResource{}
)

// HealthCheckModel is the Terraform state model for a pool's health check.
type HealthCheckModel struct {
	ID                 types.String `tfsdk:"id"`
	GatewayID          types.String `tfsdk:"gateway_id"`
	PoolID             types.String `tfsdk:"pool_id"`
	Protocol           types.String `tfsdk:"protocol"`
	Path               types.String `tfsdk:"path"`
	ExpectedStatus     types.String `tfsdk:"expected_status"`
	IntervalSeconds    types.Int64  `tfsdk:"interval_seconds"`
	TimeoutSeconds     types.Int64  `tfsdk:"timeout_seconds"`
	HealthyThreshold   types.Int64  `tfsdk:"healthy_threshold"`
	UnhealthyThreshold types.Int64  `tfsdk:"unhealthy_threshold"`
}

type apiHealthCheck struct {
	ID                 string `json:"id"`
	PoolID             string `json:"poolId"`
	Protocol           string `json:"protocol"`
	Path               string `json:"path,omitempty"`
	ExpectedStatus     string `json:"expectedStatus"`
	IntervalSeconds    int    `json:"intervalSeconds"`
	TimeoutSeconds     int    `json:"timeoutSeconds"`
	HealthyThreshold   int    `json:"healthyThreshold"`
	UnhealthyThreshold int    `json:"unhealthyThreshold"`
}

type apiPutHealthCheckRequest struct {
	Protocol           string `json:"protocol,omitempty"`
	Path               string `json:"path,omitempty"`
	ExpectedStatus     string `json:"expectedStatus,omitempty"`
	IntervalSeconds    int    `json:"intervalSeconds,omitempty"`
	TimeoutSeconds     int    `json:"timeoutSeconds,omitempty"`
	HealthyThreshold   int    `json:"healthyThreshold,omitempty"`
	UnhealthyThreshold int    `json:"unhealthyThreshold,omitempty"`
}

type healthCheckResource struct {
	client *client.Client
}

// NewResource returns a new health check resource factory.
func NewResource() resource.Resource {
	return &healthCheckResource{}
}

func (r *healthCheckResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_appgw_health_check"
}

func (r *healthCheckResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manages the health check on an Application Gateway backend pool.\n\n" +
			"A pool has at most one health check, so this resource is keyed on the pool rather than " +
			"on an id of its own.\n\n" +
			"The endpoint is a PUT, so each write sends the whole check. Removing an attribute " +
			"from your configuration does **not** reset it, though: the value is remembered from " +
			"state and re-sent, so the plan shows no change. To return an attribute to the " +
			"platform default, set it explicitly to that default.\n\n" +
			"~> **The API has no delete for a health check.** Destroying this resource removes it " +
			"from state and warns; the check itself goes away with its pool. Removing a check from a " +
			"pool that keeps running is not expressible today.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Description:   "The unique identifier of the health check.",
				Computed:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"gateway_id": schema.StringAttribute{
				Description:   "The Application Gateway this pool belongs to.",
				Required:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"pool_id": schema.StringAttribute{
				Description:   "The backend pool this health check belongs to.",
				Required:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"protocol": schema.StringAttribute{
				Description: "How the backend is probed: `http`, `https` or `tcp`.\n\n" +
					"Note this enum is NOT the listener's: a `tcp` probe is available here and a " +
					"`tcp` listener is not.",
				Optional:      true,
				Computed:      true,
				Validators:    []validator.String{stringvalidator.OneOf("http", "https", "tcp")},
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"path": schema.StringAttribute{
				Description:   "The path to request, for an `http` or `https` probe.",
				Optional:      true,
				Computed:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"expected_status": schema.StringAttribute{
				Description:   "The status treated as healthy, e.g. `200` or `200-299`.",
				Optional:      true,
				Computed:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"interval_seconds": schema.Int64Attribute{
				Description: "Seconds between probes.",
				Optional:    true,
				Computed:    true,
				// AtLeast(1), not 0: the server treats 0 as absent — either by
				// `omitempty` on the wire or by coercing it to a default — so a
				// configured 0 comes back as something else and the apply fails
				// with "inconsistent result after apply". Refusing it at plan
				// time is both cheaper and truthful.
				Validators:    []validator.Int64{int64validator.AtLeast(1)},
				PlanModifiers: []planmodifier.Int64{int64planmodifier.UseStateForUnknown()},
			},
			"timeout_seconds": schema.Int64Attribute{
				Description: "Seconds a probe may take. Must be strictly less than `interval_seconds`.",
				Optional:    true,
				Computed:    true,
				// AtLeast(1), not 0: the server treats 0 as absent — either by
				// `omitempty` on the wire or by coercing it to a default — so a
				// configured 0 comes back as something else and the apply fails
				// with "inconsistent result after apply". Refusing it at plan
				// time is both cheaper and truthful.
				Validators:    []validator.Int64{int64validator.AtLeast(1)},
				PlanModifiers: []planmodifier.Int64{int64planmodifier.UseStateForUnknown()},
			},
			"healthy_threshold": schema.Int64Attribute{
				Description: "Consecutive successes before a backend is considered healthy.",
				Optional:    true,
				Computed:    true,
				// AtLeast(1), not 0: the server treats 0 as absent — either by
				// `omitempty` on the wire or by coercing it to a default — so a
				// configured 0 comes back as something else and the apply fails
				// with "inconsistent result after apply". Refusing it at plan
				// time is both cheaper and truthful.
				Validators:    []validator.Int64{int64validator.AtLeast(1)},
				PlanModifiers: []planmodifier.Int64{int64planmodifier.UseStateForUnknown()},
			},
			"unhealthy_threshold": schema.Int64Attribute{
				Description: "Consecutive failures before a backend is taken out of rotation.",
				Optional:    true,
				Computed:    true,
				// AtLeast(1), not 0: the server treats 0 as absent — either by
				// `omitempty` on the wire or by coercing it to a default — so a
				// configured 0 comes back as something else and the apply fails
				// with "inconsistent result after apply". Refusing it at plan
				// time is both cheaper and truthful.
				Validators:    []validator.Int64{int64validator.AtLeast(1)},
				PlanModifiers: []planmodifier.Int64{int64planmodifier.UseStateForUnknown()},
			},
		},
	}
}

// ValidateConfig catches the one cross-field rule locally: a probe that cannot
// finish before the next one starts is a misconfiguration, not a tuning choice.
func (r *healthCheckResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var cfg HealthCheckModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if cfg.TimeoutSeconds.IsNull() || cfg.TimeoutSeconds.IsUnknown() ||
		cfg.IntervalSeconds.IsNull() || cfg.IntervalSeconds.IsUnknown() {
		return
	}
	to, iv := cfg.TimeoutSeconds.ValueInt64(), cfg.IntervalSeconds.ValueInt64()
	if to > 0 && iv > 0 && to >= iv {
		resp.Diagnostics.AddAttributeError(path.Root("timeout_seconds"),
			"timeout_seconds Must Be Less Than interval_seconds",
			fmt.Sprintf("A probe allowed %ds cannot finish before the next one starts every %ds.", to, iv))
	}
}

func (r *healthCheckResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	c, ok := req.ProviderData.(*client.Client)
	if !ok {
		resp.Diagnostics.AddError("Unexpected Provider Data",
			fmt.Sprintf("Expected *client.Client, got %T", req.ProviderData))
		return
	}
	r.client = c
}

func (r *healthCheckResource) path(gwID, poolID string) string {
	return r.client.TenantPath(fmt.Sprintf(
		"/application-gateways/%s/backend-pools/%s/health-check", gwID, poolID,
	))
}

func (m *HealthCheckModel) fromAPI(hc *apiHealthCheck) {
	m.ID = types.StringValue(hc.ID)
	m.PoolID = types.StringValue(hc.PoolID)
	m.Protocol = types.StringValue(hc.Protocol)
	m.Path = types.StringValue(hc.Path)
	m.ExpectedStatus = types.StringValue(hc.ExpectedStatus)
	m.IntervalSeconds = types.Int64Value(int64(hc.IntervalSeconds))
	m.TimeoutSeconds = types.Int64Value(int64(hc.TimeoutSeconds))
	m.HealthyThreshold = types.Int64Value(int64(hc.HealthyThreshold))
	m.UnhealthyThreshold = types.Int64Value(int64(hc.UnhealthyThreshold))
}

func (m *HealthCheckModel) toRequest() apiPutHealthCheckRequest {
	return apiPutHealthCheckRequest{
		Protocol:           str(m.Protocol),
		Path:               str(m.Path),
		ExpectedStatus:     str(m.ExpectedStatus),
		IntervalSeconds:    int(m.IntervalSeconds.ValueInt64()),
		TimeoutSeconds:     int(m.TimeoutSeconds.ValueInt64()),
		HealthyThreshold:   int(m.HealthyThreshold.ValueInt64()),
		UnhealthyThreshold: int(m.UnhealthyThreshold.ValueInt64()),
	}
}

// Create and Update are the SAME call: the endpoint is a PUT keyed on the pool,
// so there is no distinction between the first write and a later one.
func (r *healthCheckResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan HealthCheckModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.put(ctx, &plan); err != nil {
		resp.Diagnostics.AddError("Failed to Create Health Check", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *healthCheckResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan HealthCheckModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.put(ctx, &plan); err != nil {
		resp.Diagnostics.AddError("Failed to Update Health Check", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *healthCheckResource) put(ctx context.Context, m *HealthCheckModel) error {
	apiResp, err := r.client.Put(ctx, r.path(m.GatewayID.ValueString(), m.PoolID.ValueString()), m.toRequest())
	if err != nil {
		return err
	}
	hc, err := client.ParseResponse[apiHealthCheck](apiResp)
	if err != nil {
		return err
	}
	gwID := m.GatewayID
	m.fromAPI(hc)
	m.GatewayID = gwID
	return nil
}

func (r *healthCheckResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state HealthCheckModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	apiResp, err := r.client.Get(ctx, r.path(state.GatewayID.ValueString(), state.PoolID.ValueString()), nil)
	if err != nil {
		if client.IsNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Failed to Read Health Check", err.Error())
		return
	}
	hc, err := client.ParseResponse[apiHealthCheck](apiResp)
	if err != nil {
		resp.Diagnostics.AddError("Failed to Parse Health Check Response", err.Error())
		return
	}
	gwID := state.GatewayID
	state.fromAPI(hc)
	state.GatewayID = gwID
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Delete removes the resource from state and says what it could not do.
//
// 🔴 THE API HAS NO DELETE FOR A HEALTH CHECK -- the router registers GET and
// PUT on this path and nothing else. Three options, and this is the least bad:
//
//   - Erroring would wedge `terraform destroy` for the whole stack, including
//     the pool that is about to take the check with it.
//   - Silently removing it from state is how a provider claims a change
//     happened when it did not.
//   - Removing it and SAYING so is truthful. When the pool is being destroyed
//     in the same run -- the overwhelmingly common case -- the check really is
//     gone, and the warning costs a line. When the pool survives, the check
//     survives too, and the practitioner is told rather than left to discover
//     it from a probe that keeps running.
//
// Removing a check from a pool that keeps running is a genuine API gap.
func (r *healthCheckResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state HealthCheckModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	// If the pool is already gone, the check went with it and there is nothing
	// to warn about.
	if _, err := r.client.Get(ctx, r.client.TenantPath(fmt.Sprintf(
		"/application-gateways/%s/backend-pools/%s",
		state.GatewayID.ValueString(), state.PoolID.ValueString(),
	)), nil); err != nil {
		if client.IsNotFound(err) {
			return
		}
		// Neither branch fired here before, so a 500 or a timeout dropped the
		// resource from state in complete silence — precisely the "claims a
		// change happened when it did not" this function's comment rejects.
		resp.Diagnostics.AddWarning("Could Not Confirm Whether The Health Check Is Gone",
			"The pool could not be read, so this provider cannot tell whether the health check "+
				"went away with it: "+err.Error()+"\n\nThe resource has been removed from state. "+
				"If the pool still exists, the check is still probing your backends.")
	} else {
		resp.Diagnostics.AddWarning("Health Check Removed From State But Not From The Pool",
			"The Application Gateway API has no operation to remove a health check from a pool "+
				"that still exists, so this resource has been dropped from state while the check "+
				"itself keeps running. It will go away when the pool does. To stop probing sooner, "+
				"replace the pool.")
	}
}

func (r *healthCheckResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	parts, err := client.ParseImportID(req.ID, "gateway_id", "pool_id")
	if err != nil {
		resp.Diagnostics.AddError("Invalid Import ID", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("gateway_id"), parts[0])...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("pool_id"), parts[1])...)
}

func str(s types.String) string {
	if s.IsNull() || s.IsUnknown() {
		return ""
	}
	return s.ValueString()
}
