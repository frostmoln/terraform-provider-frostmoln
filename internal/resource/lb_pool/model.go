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
	Tags               types.Map                `tfsdk:"tags"`
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
	Tags               map[string]string      `json:"tags,omitempty"`
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
	Tags               map[string]string      `json:"tags,omitempty"`
}

// apiUpdatePoolRequest is the API request to update a pool.
//
// ClearTags is how tags are emptied, and `tags: {}` is NOT a substitute. Pool
// writes are served by provisioning, which forwards the map to the network
// service over gRPC; proto3 cannot tell an absent map from an empty one, so an
// empty map arrives as "leave the tags alone" and the request still succeeds.
// Provisioning refuses `tags` and `clearTags` together with a 400 rather than
// guessing, so exactly one is ever set.
type apiUpdatePoolRequest struct {
	Name               *string                `json:"name,omitempty"`
	LBAlgorithm        *string                `json:"lbAlgorithm,omitempty"`
	SessionPersistence *apiSessionPersistence `json:"sessionPersistence,omitempty"`
	Tags               map[string]string      `json:"tags,omitempty"`
	ClearTags          bool                   `json:"clearTags,omitempty"`
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
func (m *PoolModel) toCreateRequest(ctx context.Context, diags *diag.Diagnostics) apiCreatePoolRequest {
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
	if !m.Tags.IsNull() && !m.Tags.IsUnknown() {
		tags := make(map[string]string)
		diags.Append(m.Tags.ElementsAs(ctx, &tags, false)...)
		req.Tags = tags
	}

	return req
}

// toUpdateRequest converts the Terraform model to an API update request.
//
// prior is the tag set currently in state. Removing the `tags` block from the
// config leaves the planned map NULL, which as a plain omission would mean
// "leave the tags alone" — the resource would keep its tags forever and every
// plan would show the same pending removal. So a null/empty plan over a
// non-empty prior is sent as an explicit clearTags instead.
func (m *PoolModel) toUpdateRequest(ctx context.Context, prior types.Map, diags *diag.Diagnostics) apiUpdatePoolRequest {
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

// fromAPI populates the Terraform model from an API response.
func (m *PoolModel) fromAPI(ctx context.Context, p *apiPool, diags *diag.Diagnostics) {
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

	// An untagged pool reads back as null, not as an empty map — the same shape
	// the load balancer resource uses, so a config with no `tags` block stays
	// clean in plan instead of perpetually diffing null against {}.
	if len(p.Tags) > 0 {
		tagsMap, d := types.MapValueFrom(ctx, types.StringType, p.Tags)
		diags.Append(d...)
		m.Tags = tagsMap
	} else {
		m.Tags = types.MapNull(types.StringType)
	}
}
