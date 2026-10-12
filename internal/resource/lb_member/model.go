// Package lb_member implements the frostmoln_lb_member Terraform resource.
package lb_member

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/reservedmeta"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/tftags"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/timeouts"
)

// MemberModel is the Terraform state model for a load balancer pool member.
type MemberModel struct {
	ID             types.String `tfsdk:"id"`
	LoadBalancerID types.String `tfsdk:"load_balancer_id"`
	PoolID         types.String `tfsdk:"pool_id"`
	Address        types.String `tfsdk:"address"`
	ProtocolPort   types.Int64  `tfsdk:"protocol_port"`
	Name           types.String `tfsdk:"name"`
	Weight         types.Int64  `tfsdk:"weight"`
	SubnetID       types.String `tfsdk:"subnet_id"`
	CrossVPC       types.Bool   `tfsdk:"cross_vpc"`
	Tags           types.Map    `tfsdk:"tags"`
	TagsAll        types.Map    `tfsdk:"tags_all"`
	CreatedAt      types.String `tfsdk:"created_at"`
	UpdatedAt      types.String `tfsdk:"updated_at"`

	// Timeouts carries the customer-tunable wait budgets; a nil pointer is an
	// absent block, which resolves to the resource's hardcoded defaults.
	Timeouts *timeouts.Model `tfsdk:"timeouts"`
}

// apiMember is the API representation of a pool member.
type apiMember struct {
	ID           string `json:"id"`
	PoolID       string `json:"poolId"`
	Name         string `json:"name,omitempty"`
	Address      string `json:"address"`
	ProtocolPort int    `json:"protocolPort"`
	SubnetID     string `json:"subnetId,omitempty"`
	Weight       int    `json:"weight"`
	// Tags are the member's tags, omitted when it has none.
	Tags      map[string]string `json:"tags,omitempty"`
	CreatedAt string            `json:"createdAt"`
	UpdatedAt string            `json:"updatedAt,omitempty"`
}

// apiMemberList is the pool's member listing that the create-timeout adoption
// sweep reads (GET /load-balancers/{lb}/pools/{pool}/members → {"members":[…]});
// it wraps the item shape the package already has.
type apiMemberList struct {
	Items []apiMember `json:"members"`
}

// apiCreateMemberRequest is the API request to create a pool member.
type apiCreateMemberRequest struct {
	Name         string `json:"name,omitempty"`
	Address      string `json:"address"`
	ProtocolPort int    `json:"protocolPort"`
	SubnetID     string `json:"subnetId,omitempty"`
	Weight       int    `json:"weight,omitempty"`
	CrossVPC     bool   `json:"crossVpc,omitempty"`
	// Tags is default_tags ∪ tags (tftags.ForCreate), omitted when empty; the
	// platform merges the tenant's default tags in.
	Tags map[string]string `json:"tags,omitempty"`
}

// apiUpdateMemberRequest is the API request to update a pool member.
//
// ClearTags is how tags are emptied, and `tags: {}` is NOT a substitute: member
// writes are served by provisioning, which forwards the map to the network
// service over gRPC, where an empty map is indistinguishable from an absent one.
// Provisioning refuses `tags` and `clearTags` together, so exactly one is ever
// set (setTags).
type apiUpdateMemberRequest struct {
	Name      *string           `json:"name,omitempty"`
	Weight    *int              `json:"weight,omitempty"`
	Tags      map[string]string `json:"tags,omitempty"`
	ClearTags bool              `json:"clearTags,omitempty"`
}

// setTags puts the desired tag set on an update: a non-empty set as `tags`,
// emptying it as the explicit clearTags flag. Never both — that is a 400.
func (req *apiUpdateMemberRequest) setTags(tags map[string]string) {
	if len(tags) > 0 {
		req.Tags = tags
		return
	}
	req.ClearTags = true
}

// customerTags is the tag set the platform holds on the member, as Terraform
// sees it: platform-reserved keys filtered out. The read-back and the fresh read
// an update makes before it writes both use it.
func (a *apiMember) customerTags() map[string]string {
	return reservedmeta.FilterNetwork(a.Tags)
}

// tagsFromAPI renders the member's tags into `tags` (the keys the prior value
// names) and `tags_all` (every customer key). m.Tags must hold the prior value:
// the configuration on a create or update, the state on a refresh.
func (m *MemberModel) tagsFromAPI(ctx context.Context, mem *apiMember, diags *diag.Diagnostics) {
	m.Tags, m.TagsAll = tftags.ReadBack(ctx, mem.customerTags(), m.Tags, diags)
}

// toCreateRequest converts the Terraform model to an API create request.
func (m *MemberModel) toCreateRequest() apiCreateMemberRequest {
	req := apiCreateMemberRequest{
		Address:      m.Address.ValueString(),
		ProtocolPort: int(m.ProtocolPort.ValueInt64()),
	}

	if !m.Name.IsNull() && !m.Name.IsUnknown() {
		req.Name = m.Name.ValueString()
	}
	if !m.SubnetID.IsNull() && !m.SubnetID.IsUnknown() {
		req.SubnetID = m.SubnetID.ValueString()
	}
	if !m.Weight.IsNull() && !m.Weight.IsUnknown() {
		req.Weight = int(m.Weight.ValueInt64())
	}
	if !m.CrossVPC.IsNull() && !m.CrossVPC.IsUnknown() {
		req.CrossVPC = m.CrossVPC.ValueBool()
	}

	return req
}

// toUpdateRequest converts the Terraform model to an API update request.
// Only name and weight are mutable.
func (m *MemberModel) toUpdateRequest() apiUpdateMemberRequest {
	req := apiUpdateMemberRequest{}

	if !m.Name.IsNull() && !m.Name.IsUnknown() {
		name := m.Name.ValueString()
		req.Name = &name
	}
	if !m.Weight.IsNull() && !m.Weight.IsUnknown() {
		weight := int(m.Weight.ValueInt64())
		req.Weight = &weight
	}

	return req
}

// fromAPI populates the Terraform model from an API response. lbID is preserved
// from state/plan since the API member object does not carry the load balancer ID.
func (m *MemberModel) fromAPI(lbID string, mem *apiMember) {
	m.ID = types.StringValue(mem.ID)
	m.LoadBalancerID = types.StringValue(lbID)
	m.PoolID = types.StringValue(mem.PoolID)
	m.Address = types.StringValue(mem.Address)
	m.ProtocolPort = types.Int64Value(int64(mem.ProtocolPort))
	m.Weight = types.Int64Value(int64(mem.Weight))
	m.CreatedAt = types.StringValue(mem.CreatedAt)

	if mem.Name != "" {
		m.Name = types.StringValue(mem.Name)
	} else {
		m.Name = types.StringNull()
	}

	if mem.SubnetID != "" {
		m.SubnetID = types.StringValue(mem.SubnetID)
	} else {
		m.SubnetID = types.StringNull()
	}

	if mem.UpdatedAt != "" {
		m.UpdatedAt = types.StringValue(mem.UpdatedAt)
	} else {
		m.UpdatedAt = types.StringNull()
	}
}
