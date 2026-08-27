// Package appgw_route implements the frostmoln_appgw_route Terraform resource.
package appgw_route

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// RouteModel is the Terraform state model for an Application Gateway route.
type RouteModel struct {
	ID         types.String `tfsdk:"id"`
	GatewayID  types.String `tfsdk:"gateway_id"`
	ListenerID types.String `tfsdk:"listener_id"`
	Name       types.String `tfsdk:"name"`
	Priority   types.Int64  `tfsdk:"priority"`

	Host          types.String `tfsdk:"host"`
	PathMatchType types.String `tfsdk:"path_match_type"`
	Path          types.String `tfsdk:"path"`

	BackendPoolID types.String `tfsdk:"backend_pool_id"`
	Action        types.String `tfsdk:"action"`

	RewritePathPrefix    types.String `tfsdk:"rewrite_path_prefix"`
	RequestHeadersSet    types.Map    `tfsdk:"request_headers_set"`
	RequestHeadersRemove types.List   `tfsdk:"request_headers_remove"`
	ResponseHeadersSet   types.Map    `tfsdk:"response_headers_set"`

	WafPolicyID types.String `tfsdk:"waf_policy_id"`
	Enabled     types.Bool   `tfsdk:"enabled"`
	CreatedAt   types.String `tfsdk:"created_at"`
	UpdatedAt   types.String `tfsdk:"updated_at"`
}

type apiRoute struct {
	ID         string `json:"id"`
	ListenerID string `json:"listenerId"`
	Name       string `json:"name"`
	Priority   int    `json:"priority"`

	Host          string `json:"host,omitempty"`
	PathMatchType string `json:"pathMatchType"`
	Path          string `json:"path"`

	Action        string `json:"action"`
	BackendPoolID string `json:"backendPoolId,omitempty"`

	RewritePathPrefix    string            `json:"rewritePathPrefix,omitempty"`
	RequestHeadersSet    map[string]string `json:"requestHeadersSet,omitempty"`
	RequestHeadersRemove []string          `json:"requestHeadersRemove,omitempty"`
	ResponseHeadersSet   map[string]string `json:"responseHeadersSet,omitempty"`

	WafPolicyID string `json:"wafPolicyId,omitempty"`
	Enabled     bool   `json:"enabled"`
	CreatedAt   string `json:"createdAt"`
	UpdatedAt   string `json:"updatedAt,omitempty"`
}

type apiCreateRouteRequest struct {
	Name string `json:"name"`
	// Priority is a POINTER, and that is not cosmetic: omitting it makes the
	// server assign max+10 (last). Sending 0 would put every new route AHEAD of
	// everything already configured, silently reordering a live gateway.
	Priority *int `json:"priority,omitempty"`

	Host          string `json:"host,omitempty"`
	PathMatchType string `json:"pathMatchType,omitempty"`
	Path          string `json:"path,omitempty"`

	Action        string `json:"action,omitempty"`
	BackendPoolID string `json:"backendPoolId,omitempty"`

	RewritePathPrefix    string            `json:"rewritePathPrefix,omitempty"`
	RequestHeadersSet    map[string]string `json:"requestHeadersSet,omitempty"`
	RequestHeadersRemove []string          `json:"requestHeadersRemove,omitempty"`
	ResponseHeadersSet   map[string]string `json:"responseHeadersSet,omitempty"`
}

func (m *RouteModel) toCreateRequest(ctx context.Context, diags *diag.Diagnostics) apiCreateRouteRequest {
	req := apiCreateRouteRequest{
		Name:              m.Name.ValueString(),
		Host:              str(m.Host),
		PathMatchType:     str(m.PathMatchType),
		Path:              str(m.Path),
		Action:            str(m.Action),
		BackendPoolID:     str(m.BackendPoolID),
		RewritePathPrefix: str(m.RewritePathPrefix),
	}
	if !m.Priority.IsNull() && !m.Priority.IsUnknown() {
		p := int(m.Priority.ValueInt64())
		req.Priority = &p
	}
	req.RequestHeadersSet = mapOrNil(ctx, m.RequestHeadersSet, diags)
	req.ResponseHeadersSet = mapOrNil(ctx, m.ResponseHeadersSet, diags)
	if !m.RequestHeadersRemove.IsNull() && !m.RequestHeadersRemove.IsUnknown() {
		var out []string
		diags.Append(m.RequestHeadersRemove.ElementsAs(ctx, &out, false)...)
		req.RequestHeadersRemove = out
	}
	return req
}

func (m *RouteModel) fromAPI(ctx context.Context, rt *apiRoute, diags *diag.Diagnostics) {
	m.ID = types.StringValue(rt.ID)
	m.ListenerID = types.StringValue(rt.ListenerID)
	m.Name = types.StringValue(rt.Name)
	m.Priority = types.Int64Value(int64(rt.Priority))

	m.Host = optionalString(rt.Host)
	m.PathMatchType = types.StringValue(rt.PathMatchType)
	m.Path = types.StringValue(rt.Path)

	m.Action = types.StringValue(rt.Action)
	m.BackendPoolID = optionalString(rt.BackendPoolID)

	m.RewritePathPrefix = optionalString(rt.RewritePathPrefix)
	m.RequestHeadersSet = optionalMap(ctx, rt.RequestHeadersSet, diags)
	m.RequestHeadersRemove = optionalList(ctx, rt.RequestHeadersRemove, diags)
	m.ResponseHeadersSet = optionalMap(ctx, rt.ResponseHeadersSet, diags)

	m.WafPolicyID = types.StringValue(rt.WafPolicyID)
	m.Enabled = types.BoolValue(rt.Enabled)
	m.CreatedAt = types.StringValue(rt.CreatedAt)
	m.UpdatedAt = types.StringValue(rt.UpdatedAt)
}

func str(s types.String) string {
	if s.IsNull() || s.IsUnknown() {
		return ""
	}
	return s.ValueString()
}

func mapOrNil(ctx context.Context, m types.Map, diags *diag.Diagnostics) map[string]string {
	if m.IsNull() || m.IsUnknown() {
		return nil
	}
	out := map[string]string{}
	diags.Append(m.ElementsAs(ctx, &out, false)...)
	return out
}

// optionalMap and optionalList map an ABSENT collection to null rather than an
// empty one. The server omits these fields entirely when empty, so returning an
// empty collection would make a config that never mentioned them show a
// permanent `{} -> null` diff.
func optionalMap(ctx context.Context, v map[string]string, diags *diag.Diagnostics) types.Map {
	if len(v) == 0 {
		return types.MapNull(types.StringType)
	}
	m, d := types.MapValueFrom(ctx, types.StringType, v)
	diags.Append(d...)
	return m
}

func optionalList(ctx context.Context, v []string, diags *diag.Diagnostics) types.List {
	if len(v) == 0 {
		return types.ListNull(types.StringType)
	}
	l, d := types.ListValueFrom(ctx, types.StringType, v)
	diags.Append(d...)
	return l
}

func optionalString(s string) types.String {
	if s == "" {
		return types.StringNull()
	}
	return types.StringValue(s)
}
