// Package lb_listener implements the frostmoln_lb_listener Terraform resource.
package lb_listener

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/reservedmeta"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/tftags"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/timeouts"
)

// ListenerModel is the Terraform state model for a load balancer listener.
type ListenerModel struct {
	ID               types.String `tfsdk:"id"`
	LoadBalancerID   types.String `tfsdk:"load_balancer_id"`
	Name             types.String `tfsdk:"name"`
	Protocol         types.String `tfsdk:"protocol"`
	ProtocolPort     types.Int64  `tfsdk:"protocol_port"`
	AllowedCIDRs     types.List   `tfsdk:"allowed_cidrs"`
	InsertHeaders    types.Map    `tfsdk:"insert_headers"`
	DefaultPoolID    types.String `tfsdk:"default_pool_id"`
	TLSCertificateID types.String `tfsdk:"tls_certificate_id"`
	ConnectionLimit  types.Int64  `tfsdk:"connection_limit"`
	AdminStateUp     types.Bool   `tfsdk:"admin_state_up"`
	Tags             types.Map    `tfsdk:"tags"`
	TagsAll          types.Map    `tfsdk:"tags_all"`
	CreatedAt        types.String `tfsdk:"created_at"`
	UpdatedAt        types.String `tfsdk:"updated_at"`

	// Timeouts carries the customer-tunable wait budgets; a nil pointer is an
	// absent block, which resolves to the resource's hardcoded defaults.
	Timeouts *timeouts.Model `tfsdk:"timeouts"`
}

// apiListener is the API representation of a listener.
type apiListener struct {
	ID               string            `json:"id"`
	LoadBalancerID   string            `json:"loadBalancerId"`
	Name             string            `json:"name"`
	Protocol         string            `json:"protocol"`
	ProtocolPort     int               `json:"protocolPort"`
	DefaultPoolID    string            `json:"defaultPoolId,omitempty"`
	ConnectionLimit  int               `json:"connectionLimit,omitempty"`
	AllowedCIDRs     []string          `json:"allowedCidrs,omitempty"`
	InsertHeaders    map[string]string `json:"insertHeaders,omitempty"`
	TLSCertificateID string            `json:"tlsCertificateId,omitempty"`
	AdminStateUp     bool              `json:"adminStateUp"`
	Tags             map[string]string `json:"tags,omitempty"`
	CreatedAt        string            `json:"createdAt"`
	UpdatedAt        string            `json:"updatedAt,omitempty"`
}

// apiListenerList is the listener listing of the parent load balancer that the
// create-timeout adoption sweep reads (GET /load-balancers/{lb}/listeners →
// {"listeners":[…]}); it wraps the item shape the package already has.
type apiListenerList struct {
	Items []apiListener `json:"listeners"`
}

// apiCreateListenerRequest is the API request to create a listener.
type apiCreateListenerRequest struct {
	Name             string            `json:"name"`
	Protocol         string            `json:"protocol"`
	ProtocolPort     int               `json:"protocolPort"`
	DefaultPoolID    string            `json:"defaultPoolId,omitempty"`
	ConnectionLimit  int               `json:"connectionLimit,omitempty"`
	AllowedCIDRs     []string          `json:"allowedCidrs,omitempty"`
	InsertHeaders    map[string]string `json:"insertHeaders,omitempty"`
	TLSCertificateID string            `json:"tlsCertificateId,omitempty"`
	// Tags is default_tags ∪ tags (tftags.ForCreate), omitted when empty; the
	// platform merges the tenant's default tags in.
	Tags map[string]string `json:"tags,omitempty"`
}

// apiUpdateListenerRequest is the API request to update a listener.
//
// ClearTags is how tags are emptied, and `tags: {}` is NOT a substitute.
// Listener writes are served by provisioning, which forwards the map to the
// network service over gRPC; proto3 cannot tell an absent map from an empty
// one, so an empty map arrives as "leave the tags alone" and the request still
// succeeds. Provisioning refuses `tags` and `clearTags` together with a 400, so
// exactly one is ever set (setTags).
type apiUpdateListenerRequest struct {
	Name            *string           `json:"name,omitempty"`
	DefaultPoolID   *string           `json:"defaultPoolId,omitempty"`
	ConnectionLimit *int              `json:"connectionLimit,omitempty"`
	AllowedCIDRs    []string          `json:"allowedCidrs,omitempty"`
	InsertHeaders   map[string]string `json:"insertHeaders,omitempty"`
	Tags            map[string]string `json:"tags,omitempty"`
	ClearTags       bool              `json:"clearTags,omitempty"`
}

// toCreateRequest converts the Terraform model to an API create request.
func (m *ListenerModel) toCreateRequest(ctx context.Context, diags *diag.Diagnostics) apiCreateListenerRequest {
	req := apiCreateListenerRequest{
		Name:         m.Name.ValueString(),
		Protocol:     m.Protocol.ValueString(),
		ProtocolPort: int(m.ProtocolPort.ValueInt64()),
	}

	if !m.DefaultPoolID.IsNull() && !m.DefaultPoolID.IsUnknown() {
		req.DefaultPoolID = m.DefaultPoolID.ValueString()
	}
	if !m.TLSCertificateID.IsNull() && !m.TLSCertificateID.IsUnknown() {
		req.TLSCertificateID = m.TLSCertificateID.ValueString()
	}
	if !m.ConnectionLimit.IsNull() && !m.ConnectionLimit.IsUnknown() {
		req.ConnectionLimit = int(m.ConnectionLimit.ValueInt64())
	}
	if !m.AllowedCIDRs.IsNull() && !m.AllowedCIDRs.IsUnknown() {
		var cidrs []string
		diags.Append(m.AllowedCIDRs.ElementsAs(ctx, &cidrs, false)...)
		req.AllowedCIDRs = cidrs
	}
	if !m.InsertHeaders.IsNull() && !m.InsertHeaders.IsUnknown() {
		headers := make(map[string]string)
		diags.Append(m.InsertHeaders.ElementsAs(ctx, &headers, false)...)
		req.InsertHeaders = headers
	}

	return req
}

// toUpdateRequest converts the Terraform model to an API update request.
func (m *ListenerModel) toUpdateRequest(ctx context.Context, diags *diag.Diagnostics) apiUpdateListenerRequest {
	req := apiUpdateListenerRequest{}

	if !m.Name.IsNull() && !m.Name.IsUnknown() {
		name := m.Name.ValueString()
		req.Name = &name
	}
	if !m.DefaultPoolID.IsNull() && !m.DefaultPoolID.IsUnknown() {
		pool := m.DefaultPoolID.ValueString()
		req.DefaultPoolID = &pool
	}
	if !m.ConnectionLimit.IsNull() && !m.ConnectionLimit.IsUnknown() {
		cl := int(m.ConnectionLimit.ValueInt64())
		req.ConnectionLimit = &cl
	}
	if !m.AllowedCIDRs.IsNull() && !m.AllowedCIDRs.IsUnknown() {
		var cidrs []string
		diags.Append(m.AllowedCIDRs.ElementsAs(ctx, &cidrs, false)...)
		req.AllowedCIDRs = cidrs
	}
	if !m.InsertHeaders.IsNull() && !m.InsertHeaders.IsUnknown() {
		headers := make(map[string]string)
		diags.Append(m.InsertHeaders.ElementsAs(ctx, &headers, false)...)
		req.InsertHeaders = headers
	}

	return req
}

// fromAPI populates the Terraform model from an API response.
func (m *ListenerModel) fromAPI(ctx context.Context, l *apiListener, diags *diag.Diagnostics) {
	m.ID = types.StringValue(l.ID)
	m.LoadBalancerID = types.StringValue(l.LoadBalancerID)
	m.Name = types.StringValue(l.Name)
	m.Protocol = types.StringValue(l.Protocol)
	m.ProtocolPort = types.Int64Value(int64(l.ProtocolPort))
	m.AdminStateUp = types.BoolValue(l.AdminStateUp)
	m.CreatedAt = types.StringValue(l.CreatedAt)

	if l.DefaultPoolID != "" {
		m.DefaultPoolID = types.StringValue(l.DefaultPoolID)
	} else {
		m.DefaultPoolID = types.StringNull()
	}

	if l.TLSCertificateID != "" {
		m.TLSCertificateID = types.StringValue(l.TLSCertificateID)
	} else {
		m.TLSCertificateID = types.StringNull()
	}

	// connection_limit is Optional+Computed: always reflect the backend value so
	// a backend-defaulted limit doesn't churn. Octavia uses -1 to mean unlimited.
	m.ConnectionLimit = types.Int64Value(int64(l.ConnectionLimit))

	if len(l.AllowedCIDRs) > 0 {
		cidrs, d := types.ListValueFrom(ctx, types.StringType, l.AllowedCIDRs)
		diags.Append(d...)
		m.AllowedCIDRs = cidrs
	} else {
		empty, d := types.ListValueFrom(ctx, types.StringType, []string{})
		diags.Append(d...)
		m.AllowedCIDRs = empty
	}

	if len(l.InsertHeaders) > 0 {
		headers, d := types.MapValueFrom(ctx, types.StringType, l.InsertHeaders)
		diags.Append(d...)
		m.InsertHeaders = headers
	} else {
		m.InsertHeaders = types.MapNull(types.StringType)
	}

	if l.UpdatedAt != "" {
		m.UpdatedAt = types.StringValue(l.UpdatedAt)
	} else {
		m.UpdatedAt = types.StringNull()
	}

	// Platform-owned frostmoln_* keys are filtered first (network refuses them on
	// every customer write, so no config can converge on one).
	m.Tags, m.TagsAll = tftags.ReadBack(ctx, l.customerTags(), m.Tags, diags)
}

// setTags puts the desired tag set on an update (tftags.ForUpdate's result).
// A non-empty set is sent as `tags`; emptying a set the platform still holds
// (changed) goes out as the explicit clearTags flag, because an empty map is
// lost on the gRPC hop. Never both — that is a 400.
func (req *apiUpdateListenerRequest) setTags(tags map[string]string, changed bool) {
	switch {
	case len(tags) > 0:
		req.Tags = tags
	case changed:
		req.ClearTags = true
	}
}

// customerTags is the tag set the platform holds on the listener, as Terraform
// sees it: platform-reserved keys filtered out. The read-back and the fresh read
// an update makes before it writes both use it.
func (a *apiListener) customerTags() map[string]string {
	return reservedmeta.FilterNetwork(a.Tags)
}
