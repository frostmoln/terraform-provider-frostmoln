// Package security_group_rule implements the fm_security_group_rule Terraform resource.
package security_group_rule

import (
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// SecurityGroupRuleModel is the Terraform state model for a security group rule.
type SecurityGroupRuleModel struct {
	ID              types.String `tfsdk:"id"`
	SecurityGroupID types.String `tfsdk:"security_group_id"`
	Direction       types.String `tfsdk:"direction"`
	Protocol        types.String `tfsdk:"protocol"`
	PortRangeMin    types.Int64  `tfsdk:"port_range_min"`
	PortRangeMax    types.Int64  `tfsdk:"port_range_max"`
	RemoteCIDR      types.String `tfsdk:"remote_cidr"`
	RemoteGroupID   types.String `tfsdk:"remote_group_id"`
	Description     types.String `tfsdk:"description"`
}

// apiSecurityGroupRule is the API representation of a security group rule.
type apiSecurityGroupRule struct {
	ID            string `json:"id"`
	Direction     string `json:"direction"`
	Protocol      string `json:"protocol"`
	PortRangeMin  *int   `json:"portRangeMin,omitempty"`
	PortRangeMax  *int   `json:"portRangeMax,omitempty"`
	RemoteCIDR    string `json:"remoteCidr,omitempty"`
	RemoteGroupID string `json:"remoteGroupId,omitempty"`
	Description   string `json:"description,omitempty"`
}

// apiSecurityGroupWithRules is the API response for a security group including its rules.
type apiSecurityGroupWithRules struct {
	ID    string                 `json:"id"`
	Rules []apiSecurityGroupRule `json:"rules,omitempty"`
}

// apiCreateSecurityGroupRuleRequest is the API request to create a security group rule.
type apiCreateSecurityGroupRuleRequest struct {
	Direction     string `json:"direction"`
	Protocol      string `json:"protocol"`
	PortRangeMin  *int   `json:"portRangeMin,omitempty"`
	PortRangeMax  *int   `json:"portRangeMax,omitempty"`
	RemoteCIDR    string `json:"remoteCidr,omitempty"`
	RemoteGroupID string `json:"remoteGroupId,omitempty"`
	Description   string `json:"description,omitempty"`
}

// toCreateRequest converts the Terraform model to an API create request.
func (m *SecurityGroupRuleModel) toCreateRequest() apiCreateSecurityGroupRuleRequest {
	req := apiCreateSecurityGroupRuleRequest{
		Direction: m.Direction.ValueString(),
		Protocol:  m.Protocol.ValueString(),
	}

	if !m.PortRangeMin.IsNull() && !m.PortRangeMin.IsUnknown() {
		v := int(m.PortRangeMin.ValueInt64())
		req.PortRangeMin = &v
	}

	if !m.PortRangeMax.IsNull() && !m.PortRangeMax.IsUnknown() {
		v := int(m.PortRangeMax.ValueInt64())
		req.PortRangeMax = &v
	}

	if !m.RemoteCIDR.IsNull() && !m.RemoteCIDR.IsUnknown() {
		req.RemoteCIDR = m.RemoteCIDR.ValueString()
	}

	if !m.RemoteGroupID.IsNull() && !m.RemoteGroupID.IsUnknown() {
		req.RemoteGroupID = m.RemoteGroupID.ValueString()
	}

	if !m.Description.IsNull() && !m.Description.IsUnknown() {
		req.Description = m.Description.ValueString()
	}

	return req
}

// fromAPI populates the Terraform model from an API response.
func (m *SecurityGroupRuleModel) fromAPI(sgID string, rule *apiSecurityGroupRule) {
	m.ID = types.StringValue(rule.ID)
	m.SecurityGroupID = types.StringValue(sgID)
	m.Direction = types.StringValue(rule.Direction)
	m.Protocol = types.StringValue(rule.Protocol)

	if rule.PortRangeMin != nil {
		m.PortRangeMin = types.Int64Value(int64(*rule.PortRangeMin))
	} else {
		m.PortRangeMin = types.Int64Null()
	}

	if rule.PortRangeMax != nil {
		m.PortRangeMax = types.Int64Value(int64(*rule.PortRangeMax))
	} else {
		m.PortRangeMax = types.Int64Null()
	}

	if rule.RemoteCIDR != "" {
		m.RemoteCIDR = types.StringValue(rule.RemoteCIDR)
	} else {
		m.RemoteCIDR = types.StringNull()
	}

	if rule.RemoteGroupID != "" {
		m.RemoteGroupID = types.StringValue(rule.RemoteGroupID)
	} else {
		m.RemoteGroupID = types.StringNull()
	}

	if rule.Description != "" {
		m.Description = types.StringValue(rule.Description)
	} else {
		m.Description = types.StringNull()
	}
}
