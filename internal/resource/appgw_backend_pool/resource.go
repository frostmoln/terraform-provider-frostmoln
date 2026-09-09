// Package appgw_backend_pool implements the frostmoln_appgw_backend_pool
// Terraform resource.
package appgw_backend_pool

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/path"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/boolplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/appgwvalidate"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
)

var (
	_ resource.Resource                   = &poolResource{}
	_ resource.ResourceWithImportState    = &poolResource{}
	_ resource.ResourceWithConfigure      = &poolResource{}
	_ resource.ResourceWithValidateConfig = &poolResource{}
	_ resource.ResourceWithModifyPlan     = &poolResource{}
)

// PoolModel is the Terraform state model for a backend pool.
type PoolModel struct {
	ID        types.String `tfsdk:"id"`
	GatewayID types.String `tfsdk:"gateway_id"`
	Name      types.String `tfsdk:"name"`
	Protocol  types.String `tfsdk:"protocol"`
	Algorithm types.String `tfsdk:"algorithm"`

	SessionAffinity   types.String `tfsdk:"session_affinity"`
	SessionCookieName types.String `tfsdk:"session_cookie_name"`

	TLSVerifyBackend types.Bool   `tfsdk:"tls_verify_backend"`
	TLSCACertificate types.String `tfsdk:"tls_ca_certificate"`
	TLSServerName    types.String `tfsdk:"tls_server_name"`

	TimeoutConnectMS  types.Int64 `tfsdk:"timeout_connect_ms"`
	TimeoutResponseMS types.Int64 `tfsdk:"timeout_response_ms"`

	ProxyProtocol types.Bool `tfsdk:"proxy_protocol"`

	CreatedAt types.String `tfsdk:"created_at"`
	UpdatedAt types.String `tfsdk:"updated_at"`
}

type apiPool struct {
	ID        string `json:"id"`
	GatewayID string `json:"gatewayId"`
	Name      string `json:"name"`
	Protocol  string `json:"protocol"`
	Algorithm string `json:"algorithm"`

	SessionAffinity   string `json:"sessionAffinity"`
	SessionCookieName string `json:"sessionCookieName,omitempty"`

	TLSVerifyBackend bool   `json:"tlsVerifyBackend"`
	TLSCACertificate string `json:"tlsCaCertificate,omitempty"`
	TLSServerName    string `json:"tlsServerName,omitempty"`

	TimeoutConnectMS  int `json:"timeoutConnectMs"`
	TimeoutResponseMS int `json:"timeoutResponseMs"`

	// ProxyProtocol is echoed UNCONDITIONALLY (no `omitempty` server-side), so
	// false on the wire is a real false and not an omission. That is what lets
	// it be a plain bool here and still round-trip.
	ProxyProtocol bool `json:"proxyProtocol"`

	CreatedAt string `json:"createdAt"`
	UpdatedAt string `json:"updatedAt,omitempty"`
}

type apiCreatePoolRequest struct {
	Name      string `json:"name"`
	Protocol  string `json:"protocol,omitempty"`
	Algorithm string `json:"algorithm,omitempty"`

	SessionAffinity   string `json:"sessionAffinity,omitempty"`
	SessionCookieName string `json:"sessionCookieName,omitempty"`

	// 🔴 A POINTER, and this one is a security property rather than a
	// convenience. A plain bool would serialize false whenever the practitioner
	// did not mention it, which on an https pool means silently turning OFF
	// backend certificate verification.
	TLSVerifyBackend *bool  `json:"tlsVerifyBackend,omitempty"`
	TLSCACertificate string `json:"tlsCaCertificate,omitempty"`
	TLSServerName    string `json:"tlsServerName,omitempty"`

	TimeoutConnectMS  int `json:"timeoutConnectMs,omitempty"`
	TimeoutResponseMS int `json:"timeoutResponseMs,omitempty"`

	// ProxyProtocol needs no pointer, unlike TLSVerifyBackend above: the server
	// default is FALSE, which is also the zero value, so `omitempty` dropping a
	// false says exactly what a false would have said. The security asymmetry
	// runs the other way from tls_verify_backend -- an accidental false here
	// leaves the backend seeing the gateway's address, while an accidental true
	// breaks every connection to a backend that is not parsing the header.
	ProxyProtocol bool `json:"proxyProtocol,omitempty"`
}

// apiUpdatePoolRequest is the PATCH body: EVERY field is a pointer and every
// one is `omitempty`, because on this endpoint an omitted field is left
// unchanged and an explicit `null` is REFUSED. A non-pointer field would send
// its zero value for everything the practitioner did not touch -- clearing the
// TLS server name and setting both timeouts to 0 on a pool nobody asked to
// change.
//
// `name` is deliberately absent: the server does not accept a rename here.
type apiUpdatePoolRequest struct {
	Protocol        *string `json:"protocol,omitempty"`
	Algorithm       *string `json:"algorithm,omitempty"`
	SessionAffinity *string `json:"sessionAffinity,omitempty"`

	// The three free-text fields accept "" and it CLEARS them, which is how a
	// removed attribute is expressed. The three enum fields above do NOT accept
	// "" -- they are omitted to be left alone.
	SessionCookieName *string `json:"sessionCookieName,omitempty"`
	TLSCACertificate  *string `json:"tlsCaCertificate,omitempty"`
	TLSServerName     *string `json:"tlsServerName,omitempty"`

	TLSVerifyBackend  *bool `json:"tlsVerifyBackend,omitempty"`
	TimeoutConnectMS  *int  `json:"timeoutConnectMs,omitempty"`
	TimeoutResponseMS *int  `json:"timeoutResponseMs,omitempty"`
	ProxyProtocol     *bool `json:"proxyProtocol,omitempty"`
}

// isEmpty reports whether this patch would change nothing, so the call can be
// skipped rather than sent as an empty body.
func (r *apiUpdatePoolRequest) isEmpty() bool {
	return r.Protocol == nil && r.Algorithm == nil && r.SessionAffinity == nil &&
		r.SessionCookieName == nil && r.TLSCACertificate == nil && r.TLSServerName == nil &&
		r.TLSVerifyBackend == nil && r.TimeoutConnectMS == nil && r.TimeoutResponseMS == nil &&
		r.ProxyProtocol == nil
}

type poolResource struct {
	client *client.Client
}

// NewResource returns a new backend pool resource factory.
func NewResource() resource.Resource {
	return &poolResource{}
}

func (r *poolResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_appgw_backend_pool"
}

// Schema.
//
// 🔴 THE POOL IS NO LONGER REPLACE-ON-EVERYTHING, AND THAT IS A DELIBERATE
// NARROWING. Every attribute here carried RequiresReplace because the server
// registered POST, GET and DELETE and nothing else. `PATCH
// .../backend-pools/{poolId}` shipped and takes every setting below, so a
// change to one is now an in-place update.
//
// Keeping the old modifiers would not merely have been pessimistic, it would
// have produced plans TERRAFORM CANNOT EXECUTE: a pool is refused with
// `409 BACKEND_POOL_IN_USE` while anything forwards to it, and a pool is now
// reachable from both sides -- an http route, or a `tcp` listener naming it
// directly. So "change a timeout" planned as destroy/create, and the destroy
// 409s against any pool that is actually serving. The one way out was to delete
// the tcp listener first, which CLOSES ITS PUBLIC PORT that instant: an outage
// to change one number.
//
// `name` keeps RequiresReplace, because the PATCH body deliberately has no
// `name` -- a rename has its own uniqueness failure and no effect on what the
// appliance does.
func (r *poolResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	replaceStr := []planmodifier.String{stringplanmodifier.RequiresReplace()}
	keepStr := []planmodifier.String{stringplanmodifier.UseStateForUnknown()}

	resp.Schema = schema.Schema{
		Description: "Manages a backend pool on a Frostmoln Application Gateway: a set of backends " +
			"sharing a protocol, a load-balancing algorithm and a health check.\n\n" +
			"Every setting below is changed in place. Only `name` forces a new resource — the API " +
			"has no rename — and replacing a pool is refused with `BACKEND_POOL_IN_USE` while " +
			"anything still forwards to it, so destroy the `frostmoln_appgw_route` or the `tcp` " +
			"`frostmoln_appgw_listener` that holds it first.\n\n" +
			"Changes are authored, not live: they reach the appliance on the gateway's next " +
			"configuration apply.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Description:   "The unique identifier of the backend pool.",
				Computed:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"gateway_id": schema.StringAttribute{
				Description:   "The Application Gateway this pool belongs to.",
				Required:      true,
				PlanModifiers: replaceStr,
			},
			"name": schema.StringAttribute{
				Description:   "The name of the pool.",
				Required:      true,
				PlanModifiers: replaceStr,
			},
			"protocol": schema.StringAttribute{
				Description: "How the gateway talks to the backends: `http` or `https`. " +
					"`https` re-encrypts traffic on the way to the backend.",
				Optional:      true,
				Computed:      true,
				Validators:    []validator.String{stringvalidator.OneOf("http", "https")},
				PlanModifiers: keepStr,
			},
			"algorithm": schema.StringAttribute{
				Description:   "How requests are distributed: `round_robin`, `least_connections` or `source_ip`.",
				Optional:      true,
				Computed:      true,
				Validators:    []validator.String{stringvalidator.OneOf("round_robin", "least_connections", "source_ip")},
				PlanModifiers: keepStr,
			},
			"session_affinity": schema.StringAttribute{
				Description:   "Pin a client to one backend: `none`, `cookie` or `source_ip`.",
				Optional:      true,
				Computed:      true,
				Validators:    []validator.String{stringvalidator.OneOf("none", "cookie", "source_ip")},
				PlanModifiers: keepStr,
			},
			"session_cookie_name": schema.StringAttribute{
				Description: "The cookie used for affinity. Required when `session_affinity` is " +
					"`cookie`. Removing it from your configuration clears it on the pool.",
				Optional: true,
			},
			"tls_verify_backend": schema.BoolAttribute{
				Description: "Verify the backend's certificate on an `https` pool.\n\n" +
					"Leave this unset to keep the platform default. It is deliberately **not** " +
					"defaulted to `false` here: sending an explicit `false` for a practitioner who " +
					"never mentioned it would silently disable certificate verification.",
				Optional:      true,
				Computed:      true,
				PlanModifiers: []planmodifier.Bool{boolplanmodifier.UseStateForUnknown()},
			},
			"tls_ca_certificate": schema.StringAttribute{
				Description: "PEM CA bundle used to verify backend certificates. Removing it from " +
					"your configuration clears it on the pool.",
				Optional: true,
			},
			"tls_server_name": schema.StringAttribute{
				Description: "The server name presented to the backend (SNI) and verified against " +
					"its certificate. Removing it from your configuration clears it on the pool.",
				Optional: true,
			},
			"timeout_connect_ms": schema.Int64Attribute{
				Description: "Connect timeout in milliseconds.",
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
			"timeout_response_ms": schema.Int64Attribute{
				Description: "Response timeout in milliseconds.",
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
			"proxy_protocol": schema.BoolAttribute{
				Description: "Prepend a PROXY protocol v2 header to every connection to this pool's " +
					"backends, so they see the real client address instead of the gateway's. " +
					"Defaults to `false`.\n\n" +
					"Worth turning on for a mail server behind a `tcp` listener: without it every " +
					"connection appears to come from the gateway, which makes spam scoring, rate " +
					"limiting and abuse logging on that server useless. There is no `X-Forwarded-For` " +
					"equivalent at layer 4 — that is an HTTP header.\n\n" +
					"~> **Turning this on changes the bytes the backend receives.** Turn on the " +
					"backend's own PROXY-protocol option FIRST — Postfix's " +
					"`smtpd_upstream_proxy_protocol`, Dovecot's `haproxy_trusted_networks`, nginx's " +
					"`proxy_protocol` on the listen line — then set this, then apply the gateway " +
					"configuration. A server that is not expecting the header reads it as the first " +
					"bytes of your protocol and every connection fails. Turning it off is the same " +
					"change in reverse.\n\n" +
					"~> **Restrict the port to the gateway before turning this on.** A backend that " +
					"accepts a PROXY header trusts whoever sends it: anything that can reach that " +
					"port can then claim any source address it likes, which inverts the reason to " +
					"enable this — an attacker chooses whose reputation to burn and whose rate " +
					"limit to spend. Of the three options above only Dovecot's is itself a trust " +
					"list; nginx needs `set_real_ip_from` alongside `proxy_protocol`, and Postfix " +
					"has none, so its PROXY-enabled service must sit on a port reachable only from " +
					"the gateway. Authorize the backend so the ingress rule is scoped to the " +
					"gateway's security group, and do not open that port more widely.\n\n" +
					"~> **After `terraform import`, set this explicitly if the pool has it on.** " +
					"It carries a `false` default, so a pool whose header is already enabled — set " +
					"through the portal, the CLI or the API — plans " +
					"`proxy_protocol = true -> false` against a configuration that omits it. That " +
					"is the change described above, in the direction that breaks every connection " +
					"to a backend now expecting the header. The plan says so; read it.",
				Optional: true,
				Computed: true,
				// A schema Default rather than the UseStateForUnknown dance the
				// other Optional+Computed attributes here use, and it is honest
				// rather than a shortcut: the server's default really is false,
				// and it echoes proxyProtocol on every read unconditionally --
				// so the value the provider predicts from a null config is the
				// value that comes back, always. (A Default also takes the
				// attribute out of the unknown-from-null-config path entirely:
				// MarkComputedNilsAsUnknown leaves a default-bearing attribute
				// alone.) Contrast tls_verify_backend directly above, whose
				// server default is TRUE: a Default(false) there would silently
				// disable backend certificate verification for anyone who never
				// mentioned it.
				Default: booldefault.StaticBool(false),
			},
			"created_at": schema.StringAttribute{
				Description:   "The creation timestamp.",
				Computed:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"updated_at": schema.StringAttribute{
				Description: "The last update timestamp.",
				Computed:    true,
			},
		},
	}
}

// ValidateConfig mirrors the server's cookie rules at plan time.
//
// 🔴 IT CHECKED THE COUPLING AND NOT THE NAME, WHILE SAYING IT MIRRORED THE
// RULE. The server applies three more: the RFC token set, a 64-byte bound, and
// a refusal of `#` and an apostrophe. The last is the one that matters most and
// is the least obvious — the cookie name is rendered as a BARE, whitespace-
// separated argument (`cookie <name> insert indirect nocache httponly`), so a
// `#` truncates the line and leaves a cookie directive with no mode, and an
// apostrophe opens strong quoting mid-word. Either way the proxy refuses its
// WHOLE configuration and the appliance sticks at its previous revision.
//
// The server's own comment records that this is the SECOND caller of that
// shared rule, and that it was "found by review rather than by the class being
// closed properly the first time". The provider had the same split: the route
// headers were validated here and the cookie name was not.
func (r *poolResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var cfg PoolModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if !cfg.SessionAffinity.IsUnknown() && cfg.SessionAffinity.ValueString() == "cookie" &&
		cfg.SessionCookieName.IsNull() {
		resp.Diagnostics.AddAttributeError(path.Root("session_cookie_name"),
			"session_cookie_name Is Required With session_affinity = \"cookie\"",
			"Cookie affinity needs the name of the cookie to pin on.")
	}
	validateCookieName(cfg.SessionCookieName, resp)
	// Verification and a CA bundle only mean anything when the gateway speaks
	// TLS to the backend. Silently ignoring them would let a practitioner
	// believe a plaintext pool was verified.
	if !cfg.Protocol.IsUnknown() && !cfg.Protocol.IsNull() && cfg.Protocol.ValueString() != "https" {
		if !cfg.TLSCACertificate.IsNull() || !cfg.TLSServerName.IsNull() {
			resp.Diagnostics.AddAttributeWarning(path.Root("tls_ca_certificate"),
				"Backend TLS Settings Have No Effect On An http Pool",
				"tls_ca_certificate and tls_server_name apply to the connection from the gateway to "+
					"the backend, which is plaintext on an http pool. Set protocol = \"https\" to "+
					"re-encrypt, or remove these attributes.")
		}
	}
}

// ModifyPlan re-runs the cookie coupling against the PLANNED values, and WARNS
// rather than refusing.
//
// 🔴 ValidateConfig CANNOT SEE THIS CASE, AND IT BECAME REACHABLE WHEN THE POOL
// LEARNED TO PATCH.
//
// State has session_affinity = "cookie" and a cookie name. The practitioner
// deletes BOTH lines. session_affinity is Optional+Computed with
// UseStateForUnknown, so it pins straight back to "cookie"; session_cookie_name
// is Optional only, so it plans to null and the patch sends "" to CLEAR it. The
// server merges that onto the stored row and refuses the pair with a 400.
//
// ValidateConfig reads the CONFIG, where session_affinity is null — so the
// coupling looks satisfied. The plan is the first place both resolved values
// exist together.
//
// A WARNING, not an error, and `req.Plan.Raw.IsNull()` is NOT a sufficient guard
// for one: a destroy plan still runs a refresh phase that computes an ordinary
// NON-null plan and runs ModifyPlan against it, so an error here aborts
// `terraform destroy` too — leaving a practitioner whose pool has drifted with
// no way out but to edit the HCL. TestNoErrorDiagnosticsWhilePlanning pins the
// rule provider-wide; the refusal lives in Update, which a destroy never reaches.
func (r *poolResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.Plan.Raw.IsNull() {
		return
	}
	var plan PoolModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if !cookieAffinityWithoutAName(&plan) {
		return
	}
	resp.Diagnostics.AddAttributeWarning(path.Root("session_cookie_name"),
		"session_cookie_name Is Required With session_affinity = \"cookie\"",
		cookieCouplingDetail)
}

// cookieAffinityWithoutAName is the coupling itself, shared by the plan-time
// warning and the apply-time refusal so the two cannot drift apart.
func cookieAffinityWithoutAName(m *PoolModel) bool {
	if m.SessionAffinity.IsUnknown() || m.SessionAffinity.ValueString() != "cookie" {
		return false
	}
	return !m.SessionCookieName.IsUnknown() && m.SessionCookieName.IsNull()
}

const cookieCouplingDetail = "This pool keeps session_affinity = \"cookie\" — removing the " +
	"attribute from your configuration does not clear it, because the server's value is carried " +
	"forward — but session_cookie_name is being cleared. Cookie affinity needs the name of the " +
	"cookie to pin on. Set session_affinity to \"none\" or \"source_ip\" to turn affinity off, " +
	"or keep session_cookie_name."

// validateCookieName applies the server's three name rules to a known value.
// An unknown one is deferred, like any other value Terraform cannot see yet.
func validateCookieName(n types.String, resp *resource.ValidateConfigResponse) {
	if n.IsNull() || n.IsUnknown() {
		return
	}
	at := path.Root("session_cookie_name")
	name := n.ValueString()
	switch {
	case len(name) > appgwvalidate.MaxCookieNameLength:
		resp.Diagnostics.AddAttributeError(at, "session_cookie_name Is Too Long",
			fmt.Sprintf("It is %d bytes; the gateway accepts %d or fewer.",
				len(name), appgwvalidate.MaxCookieNameLength))
	case !appgwvalidate.Token.MatchString(name):
		resp.Diagnostics.AddAttributeError(at, "Invalid session_cookie_name",
			fmt.Sprintf("%q is not a valid cookie name. It allows letters, digits and "+
				"!#$%%&'*+-.^_`|~ — a space, a colon or an equals sign is what usually "+
				"causes this.", name))
	case appgwvalidate.HasUnrenderable(name):
		resp.Diagnostics.AddAttributeError(at, "session_cookie_name the Gateway Cannot Render",
			fmt.Sprintf("%q contains '#' or an apostrophe. Both are legal in a cookie name "+
				"but the gateway renders it as a bare argument, so either one makes the "+
				"proxy refuse its whole configuration and keep serving the previous one.",
				name))
	}
}

func (r *poolResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (m *PoolModel) fromAPI(p *apiPool) {
	m.ID = types.StringValue(p.ID)
	m.GatewayID = types.StringValue(p.GatewayID)
	m.Name = types.StringValue(p.Name)
	m.Protocol = types.StringValue(p.Protocol)
	m.Algorithm = types.StringValue(p.Algorithm)
	m.SessionAffinity = types.StringValue(p.SessionAffinity)
	m.SessionCookieName = optionalString(p.SessionCookieName)
	m.TLSVerifyBackend = types.BoolValue(p.TLSVerifyBackend)
	m.TLSCACertificate = optionalString(p.TLSCACertificate)
	m.TLSServerName = optionalString(p.TLSServerName)
	m.TimeoutConnectMS = types.Int64Value(int64(p.TimeoutConnectMS))
	m.TimeoutResponseMS = types.Int64Value(int64(p.TimeoutResponseMS))
	m.ProxyProtocol = types.BoolValue(p.ProxyProtocol)
	m.CreatedAt = types.StringValue(p.CreatedAt)
	m.UpdatedAt = types.StringValue(p.UpdatedAt)
}

func (r *poolResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan PoolModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	createReq := apiCreatePoolRequest{
		Name:              plan.Name.ValueString(),
		Protocol:          str(plan.Protocol),
		Algorithm:         str(plan.Algorithm),
		SessionAffinity:   str(plan.SessionAffinity),
		SessionCookieName: str(plan.SessionCookieName),
		TLSCACertificate:  str(plan.TLSCACertificate),
		TLSServerName:     str(plan.TLSServerName),
		TimeoutConnectMS:  int(plan.TimeoutConnectMS.ValueInt64()),
		TimeoutResponseMS: int(plan.TimeoutResponseMS.ValueInt64()),
		ProxyProtocol:     plan.ProxyProtocol.ValueBool(),
	}
	if !plan.TLSVerifyBackend.IsNull() && !plan.TLSVerifyBackend.IsUnknown() {
		v := plan.TLSVerifyBackend.ValueBool()
		createReq.TLSVerifyBackend = &v
	}

	apiResp, err := r.client.Post(ctx, r.client.TenantPath(fmt.Sprintf(
		"/application-gateways/%s/backend-pools", plan.GatewayID.ValueString(),
	)), createReq)
	if err != nil {
		resp.Diagnostics.AddError("Failed to Create Backend Pool", err.Error())
		return
	}
	p, err := client.ParseResponse[apiPool](apiResp)
	if err != nil {
		resp.Diagnostics.AddError("Failed to Parse Backend Pool Response", err.Error())
		return
	}
	plan.fromAPI(p)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *poolResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state PoolModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	apiResp, err := r.client.Get(ctx, r.client.TenantPath(fmt.Sprintf(
		"/application-gateways/%s/backend-pools/%s",
		state.GatewayID.ValueString(), state.ID.ValueString(),
	)), nil)
	if err != nil {
		if client.IsNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Failed to Read Backend Pool", err.Error())
		return
	}
	p, err := client.ParseResponse[apiPool](apiResp)
	if err != nil {
		resp.Diagnostics.AddError("Failed to Parse Backend Pool Response", err.Error())
		return
	}
	state.fromAPI(p)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Update PATCHes the pool's settings.
//
// 🔴 IT SENDS ONLY WHAT CHANGED, AND THAT IS THE ENDPOINT'S CONTRACT RATHER
// THAN AN OPTIMISATION. On this PATCH an omitted field is left unchanged, an
// explicit `null` is REFUSED, and the three enum fields cannot be set to "".
// The result is validated as a whole and not the patch, so a body that echoed
// every current value would also be accepted -- but it would make the plan and
// the wire disagree about what the practitioner asked to change, and a refusal
// would then name a field nobody touched.
//
// The clearing direction is why the free-text fields are handled separately: on
// them "" CLEARS the value, and dropping tls_server_name from a configuration
// has to reach the server as `""` rather than as an omission, which would leave
// it in place while the plan showed it going away.
func (r *poolResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state PoolModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	// The refusal ModifyPlan could only warn about. It lives here because a
	// destroy never reaches Update, so an error is safe -- see ModifyPlan's
	// comment and TestNoErrorDiagnosticsWhilePlanning.
	if cookieAffinityWithoutAName(&plan) {
		resp.Diagnostics.AddAttributeError(path.Root("session_cookie_name"),
			"session_cookie_name Is Required With session_affinity = \"cookie\"",
			cookieCouplingDetail)
		return
	}

	patch := buildPatch(&plan, &state)
	if patch.isEmpty() {
		// Nothing on the wire to change. Terraform does not normally call
		// Update in this case; carrying the plan through rather than sending an
		// empty body keeps it a no-op if it ever does.
		resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
		return
	}

	apiResp, err := r.client.Patch(ctx, r.client.TenantPath(fmt.Sprintf(
		"/application-gateways/%s/backend-pools/%s",
		state.GatewayID.ValueString(), state.ID.ValueString(),
	)), patch)
	if err != nil {
		resp.Diagnostics.AddError("Failed to Update Backend Pool", err.Error())
		return
	}
	p, err := client.ParseResponse[apiPool](apiResp)
	if err != nil {
		resp.Diagnostics.AddError("Failed to Parse Backend Pool Response", err.Error())
		return
	}
	plan.fromAPI(p)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// buildPatch is the plan-versus-state diff, with the two field classes the
// endpoint distinguishes.
//
// The ENUM and numeric fields are Optional+Computed: a practitioner who drops
// one from configuration gets the state value back through
// UseStateForUnknown, so "removed from config" never reaches here as a change
// and there is nothing to clear -- which matches the server, where those three
// enums refuse "".
//
// The FREE-TEXT fields are Optional-only: dropping one from configuration
// really is a null plan against a non-null state, and the server clears them
// with "".
func buildPatch(plan, state *PoolModel) *apiUpdatePoolRequest {
	patch := &apiUpdatePoolRequest{}

	setStr := func(dst **string, planV, stateV types.String) {
		if planV.IsUnknown() || planV.Equal(stateV) {
			return
		}
		v := planV.ValueString()
		*dst = &v
	}
	// A null plan value on an enum would be a "" the server refuses, so those
	// three go through a variant that only ever sends a known, non-null value.
	setEnum := func(dst **string, planV, stateV types.String) {
		if planV.IsUnknown() || planV.IsNull() || planV.Equal(stateV) {
			return
		}
		v := planV.ValueString()
		*dst = &v
	}
	setBool := func(dst **bool, planV, stateV types.Bool) {
		if planV.IsUnknown() || planV.IsNull() || planV.Equal(stateV) {
			return
		}
		v := planV.ValueBool()
		*dst = &v
	}
	setInt := func(dst **int, planV, stateV types.Int64) {
		if planV.IsUnknown() || planV.IsNull() || planV.Equal(stateV) {
			return
		}
		v := int(planV.ValueInt64())
		*dst = &v
	}

	setEnum(&patch.Protocol, plan.Protocol, state.Protocol)
	setEnum(&patch.Algorithm, plan.Algorithm, state.Algorithm)
	setEnum(&patch.SessionAffinity, plan.SessionAffinity, state.SessionAffinity)

	setStr(&patch.SessionCookieName, plan.SessionCookieName, state.SessionCookieName)
	setStr(&patch.TLSCACertificate, plan.TLSCACertificate, state.TLSCACertificate)
	setStr(&patch.TLSServerName, plan.TLSServerName, state.TLSServerName)

	setBool(&patch.TLSVerifyBackend, plan.TLSVerifyBackend, state.TLSVerifyBackend)
	setBool(&patch.ProxyProtocol, plan.ProxyProtocol, state.ProxyProtocol)
	setInt(&patch.TimeoutConnectMS, plan.TimeoutConnectMS, state.TimeoutConnectMS)
	setInt(&patch.TimeoutResponseMS, plan.TimeoutResponseMS, state.TimeoutResponseMS)

	return patch
}

func (r *poolResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state PoolModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	_, err := r.client.Delete(ctx, r.client.TenantPath(fmt.Sprintf(
		"/application-gateways/%s/backend-pools/%s",
		state.GatewayID.ValueString(), state.ID.ValueString(),
	)))
	if err != nil && !client.IsNotFound(err) {
		resp.Diagnostics.AddError("Failed to Delete Backend Pool",
			err.Error()+"\n\nA pool is refused with BACKEND_POOL_IN_USE while anything still "+
				"forwards to it, and it is reachable from both sides: an http "+
				"frostmoln_appgw_route, or a tcp frostmoln_appgw_listener naming it directly. "+
				"Destroy that first. Note this is a DESTROY -- to change a pool SETTING the "+
				"resource is updated in place and the pool keeps serving.")
	}
}

func (r *poolResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	parts, err := client.ParseImportID(req.ID, "gateway_id", "pool_id")
	if err != nil {
		resp.Diagnostics.AddError("Invalid Import ID", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("gateway_id"), parts[0])...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), parts[1])...)
}

func str(s types.String) string {
	if s.IsNull() || s.IsUnknown() {
		return ""
	}
	return s.ValueString()
}

func optionalString(s string) types.String {
	if s == "" {
		return types.StringNull()
	}
	return types.StringValue(s)
}
