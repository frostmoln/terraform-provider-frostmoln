// Package lb_health_monitor implements the frostmoln_lb_health_monitor Terraform resource.
package lb_health_monitor

import (
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
	CreatedAt      types.String `tfsdk:"created_at"`
	UpdatedAt      types.String `tfsdk:"updated_at"`
}

// apiHealthMonitor is the API representation of a health monitor.
type apiHealthMonitor struct {
	ID            string `json:"id"`
	PoolID        string `json:"poolId"`
	Type          string `json:"type"`
	Delay         int    `json:"delay"`
	Timeout       int    `json:"timeout"`
	MaxRetries    int    `json:"maxRetries"`
	HTTPMethod    string `json:"httpMethod,omitempty"`
	URLPath       string `json:"urlPath,omitempty"`
	ExpectedCodes string `json:"expectedCodes,omitempty"`
	CreatedAt     string `json:"createdAt"`
	UpdatedAt     string `json:"updatedAt,omitempty"`
}

// apiCreateHealthMonitorRequest is the API request to create a health monitor.
type apiCreateHealthMonitorRequest struct {
	Type          string `json:"type"`
	Delay         int    `json:"delay,omitempty"`
	Timeout       int    `json:"timeout,omitempty"`
	MaxRetries    int    `json:"maxRetries,omitempty"`
	HTTPMethod    string `json:"httpMethod,omitempty"`
	URLPath       string `json:"urlPath,omitempty"`
	ExpectedCodes string `json:"expectedCodes,omitempty"`
}

// apiUpdateHealthMonitorRequest is the API request to update a health monitor.
type apiUpdateHealthMonitorRequest struct {
	Delay         *int    `json:"delay,omitempty"`
	Timeout       *int    `json:"timeout,omitempty"`
	MaxRetries    *int    `json:"maxRetries,omitempty"`
	HTTPMethod    *string `json:"httpMethod,omitempty"`
	URLPath       *string `json:"urlPath,omitempty"`
	ExpectedCodes *string `json:"expectedCodes,omitempty"`
}

// toCreateRequest converts the Terraform model to an API create request.
func (m *HealthMonitorModel) toCreateRequest() apiCreateHealthMonitorRequest {
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

	return req
}

// toUpdateRequest converts the Terraform model to an API update request.
func (m *HealthMonitorModel) toUpdateRequest() apiUpdateHealthMonitorRequest {
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

	return req
}

// fromAPI populates the Terraform model from an API response. lbID is preserved
// from state/plan since the health monitor object does not carry it.
func (m *HealthMonitorModel) fromAPI(lbID string, hm *apiHealthMonitor) {
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
}
