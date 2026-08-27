// Package appgw_listener implements the frostmoln_appgw_listener Terraform
// resource.
package appgw_listener

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// ListenerModel is the Terraform state model for an Application Gateway listener.
type ListenerModel struct {
	ID        types.String `tfsdk:"id"`
	GatewayID types.String `tfsdk:"gateway_id"`
	Name      types.String `tfsdk:"name"`
	Protocol  types.String `tfsdk:"protocol"`
	Port      types.Int64  `tfsdk:"port"`

	DefaultCertificateID types.String `tfsdk:"default_certificate_id"`
	SNICertificateIDs    types.List   `tfsdk:"sni_certificate_ids"`
	TLSMinVersion        types.String `tfsdk:"tls_min_version"`
	TLSCipherProfile     types.String `tfsdk:"tls_cipher_profile"`
	RedirectToHTTPS      types.Bool   `tfsdk:"redirect_to_https"`

	AllowedCIDRs   types.List   `tfsdk:"allowed_cidrs"`
	DeniedCIDRs    types.List   `tfsdk:"denied_cidrs"`
	GeoBlockMode   types.String `tfsdk:"geo_block_mode"`
	GeoCountries   types.List   `tfsdk:"geo_countries"`
	RateLimitRPS   types.Int64  `tfsdk:"rate_limit_rps"`
	RateLimitBurst types.Int64  `tfsdk:"rate_limit_burst"`
	MaxConnections types.Int64  `tfsdk:"max_connections"`

	WafPolicyID types.String `tfsdk:"waf_policy_id"`
	Enabled     types.Bool   `tfsdk:"enabled"`
	CreatedAt   types.String `tfsdk:"created_at"`
	UpdatedAt   types.String `tfsdk:"updated_at"`
}

type apiListener struct {
	ID        string `json:"id"`
	GatewayID string `json:"gatewayId"`
	Name      string `json:"name"`
	Protocol  string `json:"protocol"`
	Port      int    `json:"port"`
	// PortRangeEnd belongs to the future tcp listener type. It is carried so
	// the field means the same thing the day it is accepted; an http or https
	// listener binds exactly one port and the server refuses it with a 400.
	PortRangeEnd *int `json:"portRangeEnd,omitempty"`

	DefaultCertificateID string   `json:"defaultCertificateId,omitempty"`
	SNICertificateIDs    []string `json:"sniCertificateIds,omitempty"`
	TLSMinVersion        string   `json:"tlsMinVersion"`
	TLSCipherProfile     string   `json:"tlsCipherProfile"`
	RedirectToHTTPS      bool     `json:"redirectToHttps"`

	AllowedCIDRs   []string `json:"allowedCidrs,omitempty"`
	DeniedCIDRs    []string `json:"deniedCidrs,omitempty"`
	GeoBlockMode   string   `json:"geoBlockMode"`
	GeoCountries   []string `json:"geoCountries,omitempty"`
	RateLimitRPS   int      `json:"rateLimitRps,omitempty"`
	RateLimitBurst int      `json:"rateLimitBurst,omitempty"`
	MaxConnections int      `json:"maxConnections,omitempty"`

	WafPolicyID string `json:"wafPolicyId,omitempty"`
	Enabled     bool   `json:"enabled"`
	CreatedAt   string `json:"createdAt"`
	UpdatedAt   string `json:"updatedAt,omitempty"`
}

type apiCreateListenerRequest struct {
	Name     string `json:"name"`
	Protocol string `json:"protocol"`
	Port     int    `json:"port"`

	DefaultCertificateID string   `json:"defaultCertificateId,omitempty"`
	SNICertificateIDs    []string `json:"sniCertificateIds,omitempty"`
	TLSMinVersion        string   `json:"tlsMinVersion,omitempty"`
	TLSCipherProfile     string   `json:"tlsCipherProfile,omitempty"`
	RedirectToHTTPS      bool     `json:"redirectToHttps"`

	AllowedCIDRs   []string `json:"allowedCidrs,omitempty"`
	DeniedCIDRs    []string `json:"deniedCidrs,omitempty"`
	GeoBlockMode   string   `json:"geoBlockMode,omitempty"`
	GeoCountries   []string `json:"geoCountries,omitempty"`
	RateLimitRPS   int      `json:"rateLimitRps,omitempty"`
	RateLimitBurst int      `json:"rateLimitBurst,omitempty"`
	MaxConnections int      `json:"maxConnections,omitempty"`
}

func (m *ListenerModel) toCreateRequest(ctx context.Context, diags *diag.Diagnostics) apiCreateListenerRequest {
	req := apiCreateListenerRequest{
		Name:            m.Name.ValueString(),
		Protocol:        m.Protocol.ValueString(),
		Port:            int(m.Port.ValueInt64()),
		RedirectToHTTPS: m.RedirectToHTTPS.ValueBool(),
	}
	if v := stringOrEmpty(m.DefaultCertificateID); v != "" {
		req.DefaultCertificateID = v
	}
	if v := stringOrEmpty(m.TLSMinVersion); v != "" {
		req.TLSMinVersion = v
	}
	if v := stringOrEmpty(m.TLSCipherProfile); v != "" {
		req.TLSCipherProfile = v
	}
	if v := stringOrEmpty(m.GeoBlockMode); v != "" {
		req.GeoBlockMode = v
	}
	req.SNICertificateIDs = listOrNil(ctx, m.SNICertificateIDs, diags)
	req.AllowedCIDRs = listOrNil(ctx, m.AllowedCIDRs, diags)
	req.DeniedCIDRs = listOrNil(ctx, m.DeniedCIDRs, diags)
	req.GeoCountries = listOrNil(ctx, m.GeoCountries, diags)
	req.RateLimitRPS = int(m.RateLimitRPS.ValueInt64())
	req.RateLimitBurst = int(m.RateLimitBurst.ValueInt64())
	req.MaxConnections = int(m.MaxConnections.ValueInt64())
	return req
}

func (m *ListenerModel) fromAPI(ctx context.Context, l *apiListener, diags *diag.Diagnostics) {
	m.ID = types.StringValue(l.ID)
	m.GatewayID = types.StringValue(l.GatewayID)
	m.Name = types.StringValue(l.Name)
	m.Protocol = types.StringValue(l.Protocol)
	m.Port = types.Int64Value(int64(l.Port))

	m.DefaultCertificateID = optionalString(l.DefaultCertificateID)
	m.SNICertificateIDs = optionalList(ctx, l.SNICertificateIDs, diags)
	m.TLSMinVersion = types.StringValue(l.TLSMinVersion)
	m.TLSCipherProfile = types.StringValue(l.TLSCipherProfile)
	m.RedirectToHTTPS = types.BoolValue(l.RedirectToHTTPS)

	m.AllowedCIDRs = optionalList(ctx, l.AllowedCIDRs, diags)
	m.DeniedCIDRs = optionalList(ctx, l.DeniedCIDRs, diags)
	m.GeoBlockMode = types.StringValue(l.GeoBlockMode)
	m.GeoCountries = optionalList(ctx, l.GeoCountries, diags)
	m.RateLimitRPS = optionalInt(l.RateLimitRPS)
	m.RateLimitBurst = optionalInt(l.RateLimitBurst)
	m.MaxConnections = optionalInt(l.MaxConnections)

	m.WafPolicyID = types.StringValue(l.WafPolicyID)
	m.Enabled = types.BoolValue(l.Enabled)
	m.CreatedAt = types.StringValue(l.CreatedAt)
	m.UpdatedAt = types.StringValue(l.UpdatedAt)
}

func stringOrEmpty(s types.String) string {
	if s.IsNull() || s.IsUnknown() {
		return ""
	}
	return s.ValueString()
}

func listOrNil(ctx context.Context, l types.List, diags *diag.Diagnostics) []string {
	if l.IsNull() || l.IsUnknown() {
		return nil
	}
	var out []string
	diags.Append(l.ElementsAs(ctx, &out, false)...)
	return out
}

// optionalList maps an ABSENT collection to null rather than an empty list.
//
// The two are different in Terraform, and conflating them produces a plan that
// never converges: a config with no `denied_cidrs` shows `[] -> null` on every
// run because the server omits the field entirely when it is empty.
func optionalList(ctx context.Context, v []string, diags *diag.Diagnostics) types.List {
	if len(v) == 0 {
		return types.ListNull(types.StringType)
	}
	l, d := types.ListValueFrom(ctx, types.StringType, v)
	diags.Append(d...)
	return l
}

// optionalInt maps the server's omitted-when-zero integers to null.
//
// The server tags these `omitempty`, so 0 and absent are indistinguishable on
// the wire. Reporting 0 for an unset rate limit would make a config that never
// mentioned it show a permanent diff.
func optionalInt(n int) types.Int64 {
	if n == 0 {
		return types.Int64Null()
	}
	return types.Int64Value(int64(n))
}

func optionalString(s string) types.String {
	if s == "" {
		return types.StringNull()
	}
	return types.StringValue(s)
}
