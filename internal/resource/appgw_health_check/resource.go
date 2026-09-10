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
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/scopedecl"
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
	Port               types.Int64  `tfsdk:"port"`
	ProxyProtocol      types.Bool   `tfsdk:"proxy_protocol"`
	IntervalSeconds    types.Int64  `tfsdk:"interval_seconds"`
	TimeoutSeconds     types.Int64  `tfsdk:"timeout_seconds"`
	HealthyThreshold   types.Int64  `tfsdk:"healthy_threshold"`
	UnhealthyThreshold types.Int64  `tfsdk:"unhealthy_threshold"`
}

type apiHealthCheck struct {
	ID             string `json:"id"`
	PoolID         string `json:"poolId"`
	Protocol       string `json:"protocol"`
	Path           string `json:"path,omitempty"`
	ExpectedStatus string `json:"expectedStatus"`

	// Port is the port PROBED when it is not the backend's own, and a POINTER
	// because null is a meaning here rather than an absence: null says "probe
	// the backend's own port". The server emits the key unconditionally.
	Port *int `json:"port"`

	// ProxyProtocol is on the PROBE connection, and is a different setting from
	// the POOL's field of the same name -- that one is about the connections
	// carrying traffic. No `omitempty`: the server emits the key on every read,
	// so a missing key would be a shape change rather than a false.
	ProxyProtocol bool `json:"proxyProtocol"`

	IntervalSeconds    int `json:"intervalSeconds"`
	TimeoutSeconds     int `json:"timeoutSeconds"`
	HealthyThreshold   int `json:"healthyThreshold"`
	UnhealthyThreshold int `json:"unhealthyThreshold"`
}

type apiPutHealthCheckRequest struct {
	Protocol       string `json:"protocol,omitempty"`
	Path           string `json:"path,omitempty"`
	ExpectedStatus string `json:"expectedStatus,omitempty"`

	// `omitempty` on a pointer, so an unset port sends NO key -- which is what
	// returns the probe to the backend's own port. A `*int` holding 0 would be
	// a port the server refuses; only nil is "unset".
	Port *int `json:"port,omitempty"`

	// `omitempty` is CORRECT here, unlike on the response above: the server
	// reads an absent proxyProtocol as false, which is the same value, so the
	// two encodings mean one thing. A PUT states the whole check, so this is
	// never "leave it as it was".
	ProxyProtocol bool `json:"proxyProtocol,omitempty"`

	IntervalSeconds    int `json:"intervalSeconds,omitempty"`
	TimeoutSeconds     int `json:"timeoutSeconds,omitempty"`
	HealthyThreshold   int `json:"healthyThreshold,omitempty"`
	UnhealthyThreshold int `json:"unhealthyThreshold,omitempty"`
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
			"**`port` and `proxy_protocol` are the exceptions.** Deleting either from your " +
			"configuration really does return it to the platform default, and the plan says so: " +
			"`port` reverts to the backend's own port, `proxy_protocol` to `false`. They get there " +
			"differently — `port` is `Optional` and not `Computed`, because there is no platform " +
			"default to remember, while `proxy_protocol` carries a schema default of `false`, which " +
			"is what the server does with an omitted one.\n\n" +
			"Destroying this resource removes the check from the pool, which keeps running: from " +
			"the next configuration apply the gateway stops probing this pool's backends and treats " +
			"every enabled one as available. Do that when the backends decide their own " +
			"availability — a supervised service, or one behind its own load balancer. Otherwise " +
			"keep a check: without one, a backend that has stopped answering still receives its " +
			"share of traffic." +
			"\n\n" + scopedecl.Summary("frostmoln_appgw_health_check"),
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
					"`http` and `https` send a request and compare the response against " +
					"`expected_status`. `tcp` opens a connection to the port and closes it, so " +
					"`path` and `expected_status` do not apply and are refused with it.",
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
			"port": schema.Int64Attribute{
				Description: "Probe **this** port instead of the backend's own. Omit it to probe " +
					"the port each backend declares.\n\n" +
					"Useful when the service being fronted answers its health check somewhere else " +
					"— a mail server on 25 with a health endpoint on 8080. It is **required** for a " +
					"pool behind a ranged `tcp` listener (one with `port_range_end`): those backends " +
					"are forwarded to on whatever port the client used, so the probe has none to " +
					"dial.\n\n" +
					"Unlike every other attribute here it is not `Computed`: there is no platform " +
					"default to fall back to, so removing it from your configuration returns the " +
					"probe to the backend's own port rather than re-sending the last value.",
				// Optional ONLY -- no Computed, no Default. The server has no
				// default for this and reports `port: null` when it is unset, so
				// there is nothing to compute; a Default would have to be a
				// port, and the server refuses 0.
				//
				// 🔴 MARKED Computed, DELETING THIS LINE FROM A CONFIGURATION IS
				// A NO-OP -- Terraform carries a Computed attribute's prior value
				// into the proposed new state when the configuration is null, and
				// the framework marks such an attribute unknown only when the
				// proposed state DIFFERS from the prior one. Measured against
				// this schema with Computed added: "expected Update, got
				// action(s): [no-op]", with the old port still planned. The probe
				// keeps dialling 8080 while the configuration says it should be
				// dialling the backend's own port, and Terraform reports success
				// (TestAccHealthCheckRemovingThePortIsLegibleInThePlan).
				Optional:   true,
				Validators: []validator.Int64{int64validator.Between(1, 65535)},
			},
			"proxy_protocol": schema.BoolAttribute{
				Description: "Send the PROXY protocol v2 header on the **probe** connection, as the pool's " +
					"own `proxy_protocol` does on the connections carrying traffic. Defaults to `false`.\n\n" +
					"~> **It only means anything when you set `port`, and `false` does not mean \"no " +
					"header on probes\".** A probe with no `port` of its own dials the backend's own " +
					"address and port, so it inherits the pool's connection settings — this header " +
					"among them — and already carries it whenever the pool's `proxy_protocol` is on, " +
					"with this attribute left at `false`. Setting `port` opts the probe out of those " +
					"settings and takes the header with it; this attribute is how you put it back.\n\n" +
					"Set it when the port you probe expects the header, and leave it off when it does " +
					"not — the ordinary case for a health endpoint beside the real service. A server " +
					"that is not expecting the header reads it as the first bytes of your protocol, the " +
					"probe fails, and **every** backend in the pool is marked unhealthy while the " +
					"backends themselves are fine.\n\n" +
					"~> **The pool's `proxy_protocol` has to be on.** `true` on a pool that sends no " +
					"header is refused, and so is turning the pool's `proxy_protocol` off while this is " +
					"on. Terraform updates the pool before the check that references it, so a single " +
					"apply turning both off fails on the pool: turn this one off first, and the pool's " +
					"in a later apply.\n\n" +
					"~> **After `terraform import`, set this explicitly if the check has it on.** It " +
					"carries a `false` default, so a check whose probe header is already enabled — set " +
					"through the portal, the CLI or the API — plans `proxy_protocol = true -> false` " +
					"against a configuration that omits it, and the probe stops sending the header at " +
					"the next configuration apply. The plan says so; read it.",
				Optional: true,
				Computed: true,
				// 🔴 A schema Default, and NO UseStateForUnknown — the opposite of
				// every other Optional+Computed attribute here, on purpose.
				//
				// toRequest reads the PLAN, and for a null config on an
				// Optional+Computed+UseStateForUnknown attribute the plan value is
				// the STATE value. So deleting `proxy_protocol = true` from a
				// configuration would resolve back to true, the plan would show no
				// change, nothing would be sent, and the practitioner would believe
				// they had turned the header off while the probe kept sending it.
				// That is a silent no-op, not drift: nothing anywhere reports it.
				//
				// A Default is the escape because MarkComputedNilsAsUnknown
				// (terraform-plugin-framework, internal/fwserver/server_planresourcechange.go)
				// returns a default-bearing attribute UNTOUCHED, so the null config
				// plans false rather than the remembered true, and the plan reads
				// `proxy_protocol = true -> false`. Same shape as the pool's own
				// proxy_protocol, and honest for the same reason: the server's
				// default really is false, it echoes proxyProtocol unconditionally
				// on every read, and a PUT states the whole check — so the value
				// predicted from a null config is the value that comes back.
				//
				// Pinned by TestAccHealthCheckRemovingProxyProtocolIsLegibleInThePlan.
				Default: booldefault.StaticBool(false),
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
	validateTCPShape(&cfg, resp)
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

// validateTCPShape mirrors the server's refusal so a tcp probe carrying HTTP
// fields fails at PLAN rather than at apply. Only an explicitly-set value is
// refused, exactly as the server does it -- both attributes are Computed, so a
// value arriving from state is not something the practitioner wrote, and
// toRequest already drops those.
func validateTCPShape(cfg *HealthCheckModel, resp *resource.ValidateConfigResponse) {
	if cfg.Protocol.ValueString() != "tcp" {
		return
	}
	for _, f := range []struct {
		name string
		v    types.String
	}{
		{"path", cfg.Path},
		{"expected_status", cfg.ExpectedStatus},
	} {
		if f.v.IsNull() || f.v.IsUnknown() {
			continue
		}
		resp.Diagnostics.AddAttributeError(path.Root(f.name),
			f.name+" Does Not Apply To A tcp Probe",
			"A `tcp` probe opens a connection to the port and closes it. It sends no request "+
				"and reads no status, so `"+f.name+"` has nothing to act on and the server "+
				"refuses it. Remove it, or set `protocol` to `http` or `https`.")
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
	// nil is "probe the backend's own port", which is the same absence a
	// configuration states by omitting the attribute. Read as 0 it would be a
	// port, and one the schema itself refuses.
	if hc.Port == nil {
		m.Port = types.Int64Null()
	} else {
		m.Port = types.Int64Value(int64(*hc.Port))
	}
	m.ProxyProtocol = types.BoolValue(hc.ProxyProtocol)
	m.IntervalSeconds = types.Int64Value(int64(hc.IntervalSeconds))
	m.TimeoutSeconds = types.Int64Value(int64(hc.TimeoutSeconds))
	m.HealthyThreshold = types.Int64Value(int64(hc.HealthyThreshold))
	m.UnhealthyThreshold = types.Int64Value(int64(hc.UnhealthyThreshold))
}

func (m *HealthCheckModel) toRequest() apiPutHealthCheckRequest {
	req := apiPutHealthCheckRequest{
		Protocol:           str(m.Protocol),
		Path:               tcpDropped(m, m.Path),
		ExpectedStatus:     tcpDropped(m, m.ExpectedStatus),
		ProxyProtocol:      m.ProxyProtocol.ValueBool(),
		IntervalSeconds:    int(m.IntervalSeconds.ValueInt64()),
		TimeoutSeconds:     int(m.TimeoutSeconds.ValueInt64()),
		HealthyThreshold:   int(m.HealthyThreshold.ValueInt64()),
		UnhealthyThreshold: int(m.UnhealthyThreshold.ValueInt64()),
	}
	if !m.Port.IsNull() && !m.Port.IsUnknown() {
		v := int(m.Port.ValueInt64())
		req.Port = &v
	}
	return req
}

// 🔴 A tcp PROBE MUST NOT SEND BACK THE HTTP FIELDS THE SERVER GAVE IT, OR IT
// CAN BE CREATED AND THEN NEVER UPDATED.
//
// The server refuses an EXPLICIT `path`/`expectedStatus` on a tcp probe
// (appgw internal/domain/backendpool.go: "path and expectedStatus only apply to
// a http or https probe"), but its PutHealthCheck defaults both unconditionally
// -- storing `/` and `200-299` on a tcp probe and RETURNING them. Both
// attributes are Optional+Computed with UseStateForUnknown, so those defaults
// land in state on the first apply and are planned back on every later one.
//
// The sequence that broke: create a tcp probe with `path` omitted (accepted,
// 202), change `interval_seconds`, and the PUT now carries protocol=tcp with
// path="/" and expectedStatus="200-299" read out of state -- 400
// INVALID_REQUEST, with no in-place escape. Every change after a
// `terraform import`, and every switch of an existing http probe to tcp, hit
// the same wall.
//
// Dropping them here rather than clearing them in state is deliberate: state
// must keep recording what the server actually holds, and what the server holds
// on a tcp probe is those two defaults. This is the one place that knows the
// values are unsendable.
func tcpDropped(m *HealthCheckModel, v types.String) string {
	if m.Protocol.ValueString() == "tcp" {
		return ""
	}
	return str(v)
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

// Delete removes the health check from the pool.
//
// 🔴 THIS USED TO BE AN APOLOGY. The endpoint did not exist, so Delete dropped
// the resource from state, warned that the check was still probing, and told
// the practitioner to replace the pool to stop it -- which for a pool behind a
// tcp listener means deleting that listener first and closing its public port
// that instant. `DELETE .../backend-pools/{poolId}/health-check` shipped and
// removes the check from a pool that keeps running, so the real operation is
// what runs here now and the resource no longer lies about what a destroy did.
//
// A 404 is SUCCESS, not an error: the endpoint answers 404 when the pool has no
// check to remove, and "there is no check on this pool" is the state a destroy
// is asking for. It is also what a concurrent destroy, or a check removed
// outside Terraform, leaves behind -- erroring on it would wedge every
// subsequent destroy of the surrounding stack for a condition that is already
// the desired one. A missing POOL reaches this code as the same 404 and means
// the same thing: the check went with it.
func (r *healthCheckResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state HealthCheckModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	_, err := r.client.Delete(ctx, r.path(state.GatewayID.ValueString(), state.PoolID.ValueString()))
	if err != nil && !client.IsNotFound(err) {
		resp.Diagnostics.AddError("Failed to Delete Health Check", err.Error())
		return
	}
	// The pool keeps running with no probe, and that is a change in how it
	// behaves rather than only a change in what Terraform tracks. Said once,
	// here, because it is invisible everywhere else: every enabled backend now
	// receives traffic whether or not it is answering.
	resp.Diagnostics.AddWarning("Backends Are No Longer Probed",
		"The health check has been removed from the pool. From the gateway's next configuration "+
			"apply it stops probing this pool's backends and treats every enabled one as "+
			"available, so a backend that has stopped answering still receives its share of "+
			"traffic. That is the right outcome when the backends decide their own availability; "+
			"otherwise keep a check on the pool.")
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
