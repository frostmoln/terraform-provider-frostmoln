// Package lb_member implements the frostmoln_lb_member Terraform resource.
package lb_member

import (
	"github.com/hashicorp/terraform-plugin-framework/types"
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
	CreatedAt      types.String `tfsdk:"created_at"`
	UpdatedAt      types.String `tfsdk:"updated_at"`
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
	CreatedAt    string `json:"createdAt"`
	UpdatedAt    string `json:"updatedAt,omitempty"`
}

// apiCreateMemberRequest is the API request to create a pool member.
type apiCreateMemberRequest struct {
	Name         string `json:"name,omitempty"`
	Address      string `json:"address"`
	ProtocolPort int    `json:"protocolPort"`
	SubnetID     string `json:"subnetId,omitempty"`
	Weight       int    `json:"weight,omitempty"`
	CrossVPC     bool   `json:"crossVpc,omitempty"`
}

// apiUpdateMemberRequest is the API request to update a pool member.
type apiUpdateMemberRequest struct {
	Name   *string `json:"name,omitempty"`
	Weight *int    `json:"weight,omitempty"`
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
