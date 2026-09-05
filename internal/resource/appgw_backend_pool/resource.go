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

func (r *poolResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	replaceStr := []planmodifier.String{stringplanmodifier.RequiresReplace()}

	resp.Schema = schema.Schema{
		Description: "Manages a backend pool on a Frostmoln Application Gateway: a set of backends " +
			"sharing a protocol, a load-balancing algorithm and a health check.\n\n" +
			"The backend pool API has no update operation, so every attribute forces a new resource.",
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
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown(), stringplanmodifier.RequiresReplace()},
			},
			"algorithm": schema.StringAttribute{
				Description:   "How requests are distributed: `round_robin`, `least_connections` or `source_ip`.",
				Optional:      true,
				Computed:      true,
				Validators:    []validator.String{stringvalidator.OneOf("round_robin", "least_connections", "source_ip")},
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown(), stringplanmodifier.RequiresReplace()},
			},
			"session_affinity": schema.StringAttribute{
				Description:   "Pin a client to one backend: `none`, `cookie` or `source_ip`.",
				Optional:      true,
				Computed:      true,
				Validators:    []validator.String{stringvalidator.OneOf("none", "cookie", "source_ip")},
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown(), stringplanmodifier.RequiresReplace()},
			},
			"session_cookie_name": schema.StringAttribute{
				Description:   "The cookie used for affinity. Required when `session_affinity` is `cookie`.",
				Optional:      true,
				PlanModifiers: replaceStr,
			},
			"tls_verify_backend": schema.BoolAttribute{
				Description: "Verify the backend's certificate on an `https` pool.\n\n" +
					"Leave this unset to keep the platform default. It is deliberately **not** " +
					"defaulted to `false` here: sending an explicit `false` for a practitioner who " +
					"never mentioned it would silently disable certificate verification.",
				Optional:      true,
				Computed:      true,
				PlanModifiers: []planmodifier.Bool{boolplanmodifier.UseStateForUnknown(), boolplanmodifier.RequiresReplace()},
			},
			"tls_ca_certificate": schema.StringAttribute{
				Description:   "PEM CA bundle used to verify backend certificates.",
				Optional:      true,
				PlanModifiers: replaceStr,
			},
			"tls_server_name": schema.StringAttribute{
				Description:   "The server name presented to the backend (SNI) and verified against its certificate.",
				Optional:      true,
				PlanModifiers: replaceStr,
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
				PlanModifiers: []planmodifier.Int64{int64planmodifier.UseStateForUnknown(), int64planmodifier.RequiresReplace()},
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
				PlanModifiers: []planmodifier.Int64{int64planmodifier.UseStateForUnknown(), int64planmodifier.RequiresReplace()},
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

// Update cannot be reached: every attribute carries RequiresReplace.
func (r *poolResource) Update(_ context.Context, _ resource.UpdateRequest, resp *resource.UpdateResponse) {
	resp.Diagnostics.AddError("Backend Pools Cannot Be Updated In Place",
		"The Application Gateway API has no backend pool update operation, so every attribute of "+
			"this resource forces a replacement. Reaching this code means an attribute was added to "+
			"the schema without RequiresReplace.")
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
		resp.Diagnostics.AddError("Failed to Delete Backend Pool", err.Error())
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
