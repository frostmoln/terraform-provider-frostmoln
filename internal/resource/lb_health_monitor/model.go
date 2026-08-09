// Package lb_health_monitor implements the frostmoln_lb_health_monitor Terraform resource.
package lb_health_monitor

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// HealthMonitorModel is the Terraform state model for a pool health monitor.
// A pool has at most one health monitor (singleton), so there is no monitor-id
// path segment; the resource is identified by its parent pool.
type HealthMonitorModel struct {
	ID             types.String `tfsdk:"id"`
	LoadBalancerID types.String `tfsdk:"load_balancer_id"`
	PoolID         types.String `tfsdk:"pool_id"`
	Type           types.String `tfsdk:"type"`
	Delay          types.Int64  `tfsdk:"delay"`
	Timeout        types.Int64  `tfsdk:"timeout"`
	MaxRetries     types.Int64  `tfsdk:"max_retries"`
	URLPath        types.String `tfsdk:"url_path"`
	HTTPMethod     types.String `tfsdk:"http_method"`
	ExpectedCodes  types.String `tfsdk:"expected_codes"`
	Tags           types.Map    `tfsdk:"tags"`
	CreatedAt      types.String `tfsdk:"created_at"`
	UpdatedAt      types.String `tfsdk:"updated_at"`
}

// apiHealthMonitor is the API representation of a health monitor.
type apiHealthMonitor struct {
	ID            string            `json:"id"`
	PoolID        string            `json:"poolId"`
	Type          string            `json:"type"`
	Delay         int               `json:"delay"`
	Timeout       int               `json:"timeout"`
	MaxRetries    int               `json:"maxRetries"`
	HTTPMethod    string            `json:"httpMethod,omitempty"`
	URLPath       string            `json:"urlPath,omitempty"`
	ExpectedCodes string            `json:"expectedCodes,omitempty"`
	Tags          map[string]string `json:"tags,omitempty"`
	CreatedAt     string            `json:"createdAt"`
	UpdatedAt     string            `json:"updatedAt,omitempty"`
}

// apiCreateHealthMonitorRequest is the API request to create a health monitor.
type apiCreateHealthMonitorRequest struct {
	Type          string            `json:"type"`
	Delay         int               `json:"delay,omitempty"`
	Timeout       int               `json:"timeout,omitempty"`
	MaxRetries    int               `json:"maxRetries,omitempty"`
	HTTPMethod    string            `json:"httpMethod,omitempty"`
	URLPath       string            `json:"urlPath,omitempty"`
	ExpectedCodes string            `json:"expectedCodes,omitempty"`
	Tags          map[string]string `json:"tags,omitempty"`
}

// apiUpdateHealthMonitorRequest is the API request to update a health monitor.
//
// ClearTags carries the same absent-vs-empty contract as the pool's: the write
// is served by provisioning and forwarded to network over gRPC, where an empty
// map is indistinguishable from an absent one, so only the explicit flag
// empties the set. Sending both is a 400.
type apiUpdateHealthMonitorRequest struct {
	Delay         *int              `json:"delay,omitempty"`
	Timeout       *int              `json:"timeout,omitempty"`
	MaxRetries    *int              `json:"maxRetries,omitempty"`
	HTTPMethod    *string           `json:"httpMethod,omitempty"`
	URLPath       *string           `json:"urlPath,omitempty"`
	ExpectedCodes *string           `json:"expectedCodes,omitempty"`
	Tags          map[string]string `json:"tags,omitempty"`
	ClearTags     bool              `json:"clearTags,omitempty"`
}

// toCreateRequest converts the Terraform model to an API create request.
func (m *HealthMonitorModel) toCreateRequest(ctx context.Context, diags *diag.Diagnostics) apiCreateHealthMonitorRequest {
	req := apiCreateHealthMonitorRequest{
		Type:       m.Type.ValueString(),
		Delay:      int(m.Delay.ValueInt64()),
		Timeout:    int(m.Timeout.ValueInt64()),
		MaxRetries: int(m.MaxRetries.ValueInt64()),
	}

	if !m.HTTPMethod.IsNull() && !m.HTTPMethod.IsUnknown() {
		req.HTTPMethod = m.HTTPMethod.ValueString()
	}
	if !m.URLPath.IsNull() && !m.URLPath.IsUnknown() {
		req.URLPath = m.URLPath.ValueString()
	}
	if !m.ExpectedCodes.IsNull() && !m.ExpectedCodes.IsUnknown() {
		req.ExpectedCodes = m.ExpectedCodes.ValueString()
	}
	if !m.Tags.IsNull() && !m.Tags.IsUnknown() {
		tags := make(map[string]string)
		diags.Append(m.Tags.ElementsAs(ctx, &tags, false)...)
		req.Tags = tags
	}

	return req
}

// toUpdateRequest converts the Terraform model to an API update request.
//
// prior is the tag set currently in state; a null/empty plan over a non-empty
// prior means the config dropped the `tags` block, which must go out as an
// explicit clearTags rather than as an omission the backend reads as "leave
// them alone". See apiUpdateHealthMonitorRequest.
func (m *HealthMonitorModel) toUpdateRequest(ctx context.Context, prior types.Map, diags *diag.Diagnostics) apiUpdateHealthMonitorRequest {
	req := apiUpdateHealthMonitorRequest{}

	if !m.Delay.IsNull() && !m.Delay.IsUnknown() {
		v := int(m.Delay.ValueInt64())
		req.Delay = &v
	}
	if !m.Timeout.IsNull() && !m.Timeout.IsUnknown() {
		v := int(m.Timeout.ValueInt64())
		req.Timeout = &v
	}
	if !m.MaxRetries.IsNull() && !m.MaxRetries.IsUnknown() {
		v := int(m.MaxRetries.ValueInt64())
		req.MaxRetries = &v
	}
	if !m.HTTPMethod.IsNull() && !m.HTTPMethod.IsUnknown() {
		v := m.HTTPMethod.ValueString()
		req.HTTPMethod = &v
	}
	if !m.URLPath.IsNull() && !m.URLPath.IsUnknown() {
		v := m.URLPath.ValueString()
		req.URLPath = &v
	}
	if !m.ExpectedCodes.IsNull() && !m.ExpectedCodes.IsUnknown() {
		v := m.ExpectedCodes.ValueString()
		req.ExpectedCodes = &v
	}
	if !m.Tags.IsNull() && !m.Tags.IsUnknown() {
		tags := make(map[string]string)
		diags.Append(m.Tags.ElementsAs(ctx, &tags, false)...)
		if len(tags) > 0 {
			req.Tags = tags
		} else if len(prior.Elements()) > 0 {
			req.ClearTags = true
		}
	} else if m.Tags.IsNull() && len(prior.Elements()) > 0 {
		req.ClearTags = true
	}

	return req
}

// fromAPI populates the Terraform model from an API response. lbID is preserved
// from state/plan since the health monitor object does not carry it.
func (m *HealthMonitorModel) fromAPI(ctx context.Context, lbID string, hm *apiHealthMonitor, diags *diag.Diagnostics) {
	m.ID = types.StringValue(hm.ID)
	m.LoadBalancerID = types.StringValue(lbID)
	m.PoolID = types.StringValue(hm.PoolID)
	m.Type = types.StringValue(hm.Type)
	m.Delay = types.Int64Value(int64(hm.Delay))
	m.Timeout = types.Int64Value(int64(hm.Timeout))
	m.MaxRetries = types.Int64Value(int64(hm.MaxRetries))
	m.CreatedAt = types.StringValue(hm.CreatedAt)

	if hm.HTTPMethod != "" {
		m.HTTPMethod = types.StringValue(hm.HTTPMethod)
	} else {
		m.HTTPMethod = types.StringNull()
	}

	if hm.URLPath != "" {
		m.URLPath = types.StringValue(hm.URLPath)
	} else {
		m.URLPath = types.StringNull()
	}

	if hm.ExpectedCodes != "" {
		m.ExpectedCodes = types.StringValue(hm.ExpectedCodes)
	} else {
		m.ExpectedCodes = types.StringNull()
	}

	if hm.UpdatedAt != "" {
		m.UpdatedAt = types.StringValue(hm.UpdatedAt)
	} else {
		m.UpdatedAt = types.StringNull()
	}

	// An untagged monitor reads back as null, not as an empty map, so a config
	// with no `tags` block does not diff forever.
	if len(hm.Tags) > 0 {
		tagsMap, d := types.MapValueFrom(ctx, types.StringType, hm.Tags)
		diags.Append(d...)
		m.Tags = tagsMap
	} else {
		m.Tags = types.MapNull(types.StringType)
	}
}
