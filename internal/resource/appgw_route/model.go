// Package appgw_route implements the frostmoln_appgw_route Terraform resource.
package appgw_route

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
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

	// RequestHeadersSetNames / ResponseHeadersSetNames are the names-only
	// shape. Header values are secrets, and once the platform stops returning
	// them it reports only which headers a route sets. Both shapes decode here:
	// a response carries the value maps (the older server) or the name lists,
	// and headerMapFromAPI decides from whichever is present.
	RequestHeadersSetNames  []string `json:"requestHeadersSetNames,omitempty"`
	ResponseHeadersSetNames []string `json:"responseHeadersSetNames,omitempty"`

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

// fromAPI copies the server's view of a route into m.
//
// 🔴 THE TWO HEADER MAPS ARE RECONCILED AGAINST m, NOT OVERWRITTEN FROM THE
// RESPONSE. m holds the prior value on entry — prior state in Read, the plan in
// Create — and headerMapFromAPI decides what survives. Overwriting them from a
// server that returns only header NAMES writes null over the configured values:
// Create then fails with "inconsistent result after apply", and every refresh
// plans a REPLACEMENT of every header route, on every apply, forever. That is
// why this provider has to ship before the platform stops returning values.
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
	var drift headerNameDrift
	m.RequestHeadersSet, drift = headerMapFromAPI(ctx, m.RequestHeadersSet,
		rt.RequestHeadersSet, rt.RequestHeadersSetNames, diags)
	drift.warn(path.Root("request_headers_set"), diags)
	m.RequestHeadersRemove = optionalList(ctx, rt.RequestHeadersRemove, diags)
	m.ResponseHeadersSet, drift = headerMapFromAPI(ctx, m.ResponseHeadersSet,
		rt.ResponseHeadersSet, rt.ResponseHeadersSetNames, diags)
	drift.warn(path.Root("response_headers_set"), diags)

	m.WafPolicyID = types.StringValue(rt.WafPolicyID)
	m.Enabled = types.BoolValue(rt.Enabled)
	m.CreatedAt = types.StringValue(rt.CreatedAt)
	m.UpdatedAt = types.StringValue(rt.UpdatedAt)
}

// headerMapFromAPI decides what state holds for one header map, given its prior
// value and the server's response.
//
//   - The response carries VALUES (a server that still returns them): state
//     takes them, exactly as it always has, so a value that differs on the
//     server still shows as drift.
//   - The response carries neither values nor names: the route sets no such
//     headers, and state is null — the meaning an absent map has always had.
//   - The response carries only NAMES: the values cannot be read back, so the
//     prior value is kept for every name the server still reports and only the
//     SET OF NAMES is reconciled. Routes are immutable — the API has no route
//     update — so when the sets agree the prior values are the route's values,
//     and the refresh is a no-op.
//
// When the name sets disagree (import, where the prior value is null; or a
// platform-side change no API call can make), state must NOT equal the
// configuration, or the plan would adopt a route whose values nobody has seen.
// Each name the server reports without a prior value gets the empty string,
// which no configuration can hold — ValidateConfig and the server both refuse an
// empty header value — so the plan is guaranteed to show the replacement that
// re-creates the route with the configured values.
//
// This depends on the platform emitting the names in the same release that
// stops emitting the values. A server that emitted neither for a route that has
// headers would read as "no headers" and plan a replacement — the same outcome
// the provider before this change produced for every header route.
func headerMapFromAPI(ctx context.Context, prior types.Map, values map[string]string, names []string, diags *diag.Diagnostics) (types.Map, headerNameDrift) {
	if len(values) > 0 {
		return optionalMap(ctx, values, diags), headerNameDrift{}
	}
	if len(names) == 0 {
		return types.MapNull(types.StringType), headerNameDrift{}
	}

	kept := map[string]string{}
	if !prior.IsNull() && !prior.IsUnknown() {
		for name, el := range prior.Elements() {
			if s, ok := el.(types.String); ok && !s.IsNull() && !s.IsUnknown() {
				kept[name] = s.ValueString()
			}
		}
	}

	out := make(map[string]string, len(names))
	var drift headerNameDrift
	for _, name := range names {
		if _, dup := out[name]; dup {
			continue
		}
		if v, ok := kept[name]; ok {
			out[name] = v
			continue
		}
		out[name] = ""
		drift.unrecoverable = append(drift.unrecoverable, name)
	}
	for name := range kept {
		if _, ok := out[name]; !ok {
			drift.gone = append(drift.gone, name)
		}
	}
	sort.Strings(drift.unrecoverable)
	sort.Strings(drift.gone)
	return optionalMap(ctx, out, diags), drift
}

// headerNameDrift records how a names-only response differed from state. It
// carries header NAMES only, which the API itself returns; a value never enters
// it, because a value never enters a diagnostic.
type headerNameDrift struct {
	unrecoverable []string // reported by the server, no value in state
	gone          []string // in state, no longer reported by the server
}

func (d headerNameDrift) warn(at path.Path, diags *diag.Diagnostics) {
	if len(d.unrecoverable) == 0 && len(d.gone) == 0 {
		return
	}
	var parts []string
	if len(d.unrecoverable) > 0 {
		parts = append(parts, "set on the route but with no value in state: "+strings.Join(d.unrecoverable, ", "))
	}
	if len(d.gone) > 0 {
		parts = append(parts, "in state but no longer set on the route: "+strings.Join(d.gone, ", "))
	}
	diags.AddAttributeWarning(at, "Header Values Cannot Be Read Back",
		fmt.Sprintf("The platform returns only the names of this route's headers, not their values, "+
			"and the names do not match state (%s). The provider cannot recover a header value from the "+
			"API, so the next plan REPLACES this route, re-creating it with the values in your "+
			"configuration. After `terraform import` this is expected: an imported route with header "+
			"values always plans one replacement.", strings.Join(parts, "; ")))
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
