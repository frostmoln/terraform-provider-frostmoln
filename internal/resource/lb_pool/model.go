// Package lb_pool implements the frostmoln_lb_pool Terraform resource.
package lb_pool

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// PoolModel is the Terraform state model for a load balancer pool.
type PoolModel struct {
	ID                 types.String             `tfsdk:"id"`
	LoadBalancerID     types.String             `tfsdk:"load_balancer_id"`
	ListenerID         types.String             `tfsdk:"listener_id"`
	Name               types.String             `tfsdk:"name"`
	Protocol           types.String             `tfsdk:"protocol"`
	LBAlgorithm        types.String             `tfsdk:"lb_algorithm"`
	ProxyProtocol      types.String             `tfsdk:"proxy_protocol"`
	SessionPersistence *SessionPersistenceModel `tfsdk:"session_persistence"`
	CreatedAt          types.String             `tfsdk:"created_at"`
	UpdatedAt          types.String             `tfsdk:"updated_at"`
}

// SessionPersistenceModel is the Terraform state model for pool session
// persistence configuration.
type SessionPersistenceModel struct {
	Type                   types.String `tfsdk:"type"`
	CookieName             types.String `tfsdk:"cookie_name"`
	PersistenceTimeout     types.Int64  `tfsdk:"persistence_timeout"`
	PersistenceGranularity types.String `tfsdk:"persistence_granularity"`
}

// apiSessionPersistence represents session persistence configuration. Matches
// the network service domain.SessionPersistence JSON shape.
type apiSessionPersistence struct {
	Type                   string `json:"type"`
	CookieName             string `json:"cookieName,omitempty"`
	PersistenceTimeout     int    `json:"persistenceTimeout,omitempty"`
	PersistenceGranularity string `json:"persistenceGranularity,omitempty"`
}

// apiPool is the API representation of a pool.
type apiPool struct {
	ID                 string                 `json:"id"`
	LoadBalancerID     string                 `json:"loadBalancerId"`
	ListenerID         string                 `json:"listenerId,omitempty"`
	Name               string                 `json:"name"`
	Protocol           string                 `json:"protocol"`
	LBAlgorithm        string                 `json:"lbAlgorithm"`
	ProxyProtocol      string                 `json:"proxyProtocol,omitempty"`
	SessionPersistence *apiSessionPersistence `json:"sessionPersistence,omitempty"`
	CreatedAt          string                 `json:"createdAt"`
	UpdatedAt          string                 `json:"updatedAt,omitempty"`
}

// apiCreatePoolRequest is the API request to create a pool.
type apiCreatePoolRequest struct {
	Name               string                 `json:"name"`
	Protocol           string                 `json:"protocol"`
	LBAlgorithm        string                 `json:"lbAlgorithm"`
	ProxyProtocol      string                 `json:"proxyProtocol,omitempty"`
	ListenerID         string                 `json:"listenerId,omitempty"`
	SessionPersistence *apiSessionPersistence `json:"sessionPersistence,omitempty"`
}

// apiUpdatePoolRequest is the API request to update a pool.
type apiUpdatePoolRequest struct {
	Name               *string                `json:"name,omitempty"`
	LBAlgorithm        *string                `json:"lbAlgorithm,omitempty"`
	SessionPersistence *apiSessionPersistence `json:"sessionPersistence,omitempty"`
}

// toAPISessionPersistence converts the TF nested model to the API shape, or nil
// when unset.
func (m *PoolModel) toAPISessionPersistence() *apiSessionPersistence {
	if m.SessionPersistence == nil {
		return nil
	}
	sp := &apiSessionPersistence{
		Type: m.SessionPersistence.Type.ValueString(),
	}
	if !m.SessionPersistence.CookieName.IsNull() && !m.SessionPersistence.CookieName.IsUnknown() {
		sp.CookieName = m.SessionPersistence.CookieName.ValueString()
	}
	if !m.SessionPersistence.PersistenceTimeout.IsNull() && !m.SessionPersistence.PersistenceTimeout.IsUnknown() {
		sp.PersistenceTimeout = int(m.SessionPersistence.PersistenceTimeout.ValueInt64())
	}
	if !m.SessionPersistence.PersistenceGranularity.IsNull() && !m.SessionPersistence.PersistenceGranularity.IsUnknown() {
		sp.PersistenceGranularity = m.SessionPersistence.PersistenceGranularity.ValueString()
	}
	return sp
}

// toCreateRequest converts the Terraform model to an API create request.
func (m *PoolModel) toCreateRequest() apiCreatePoolRequest {
	req := apiCreatePoolRequest{
		Name:               m.Name.ValueString(),
		Protocol:           m.Protocol.ValueString(),
		LBAlgorithm:        m.LBAlgorithm.ValueString(),
		SessionPersistence: m.toAPISessionPersistence(),
	}

	if !m.ProxyProtocol.IsNull() && !m.ProxyProtocol.IsUnknown() {
		req.ProxyProtocol = m.ProxyProtocol.ValueString()
	}
	if !m.ListenerID.IsNull() && !m.ListenerID.IsUnknown() {
		req.ListenerID = m.ListenerID.ValueString()
	}

	return req
}

// toUpdateRequest converts the Terraform model to an API update request.
func (m *PoolModel) toUpdateRequest() apiUpdatePoolRequest {
	req := apiUpdatePoolRequest{
		SessionPersistence: m.toAPISessionPersistence(),
	}

	if !m.Name.IsNull() && !m.Name.IsUnknown() {
		name := m.Name.ValueString()
		req.Name = &name
	}
	if !m.LBAlgorithm.IsNull() && !m.LBAlgorithm.IsUnknown() {
		algo := m.LBAlgorithm.ValueString()
		req.LBAlgorithm = &algo
	}

	return req
}

// fromAPI populates the Terraform model from an API response.
func (m *PoolModel) fromAPI(_ context.Context, p *apiPool, _ *diag.Diagnostics) {
	m.ID = types.StringValue(p.ID)
	m.LoadBalancerID = types.StringValue(p.LoadBalancerID)
	m.Name = types.StringValue(p.Name)
	m.Protocol = types.StringValue(p.Protocol)
	m.LBAlgorithm = types.StringValue(p.LBAlgorithm)
	m.CreatedAt = types.StringValue(p.CreatedAt)

	if p.ListenerID != "" {
		m.ListenerID = types.StringValue(p.ListenerID)
	} else {
		m.ListenerID = types.StringNull()
	}

	if p.ProxyProtocol != "" {
		m.ProxyProtocol = types.StringValue(p.ProxyProtocol)
	} else if m.ProxyProtocol.IsNull() {
		m.ProxyProtocol = types.StringNull()
	}

	if p.SessionPersistence != nil && p.SessionPersistence.Type != "" {
		sp := &SessionPersistenceModel{
			Type: types.StringValue(p.SessionPersistence.Type),
		}
		if p.SessionPersistence.CookieName != "" {
			sp.CookieName = types.StringValue(p.SessionPersistence.CookieName)
		} else {
			sp.CookieName = types.StringNull()
		}
		if p.SessionPersistence.PersistenceTimeout != 0 {
			sp.PersistenceTimeout = types.Int64Value(int64(p.SessionPersistence.PersistenceTimeout))
		} else {
			sp.PersistenceTimeout = types.Int64Null()
		}
		if p.SessionPersistence.PersistenceGranularity != "" {
			sp.PersistenceGranularity = types.StringValue(p.SessionPersistence.PersistenceGranularity)
		} else {
			sp.PersistenceGranularity = types.StringNull()
		}
		m.SessionPersistence = sp
	} else {
		m.SessionPersistence = nil
	}

	if p.UpdatedAt != "" {
		m.UpdatedAt = types.StringValue(p.UpdatedAt)
	} else {
		m.UpdatedAt = types.StringNull()
	}
}
