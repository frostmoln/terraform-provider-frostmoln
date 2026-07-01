// Package instance_port_security_groups implements the
// frostmoln_instance_port_security_groups Terraform resource.
package instance_port_security_groups

import (
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// InstancePortSecurityGroupsModel is the Terraform state model for a single
// instance port's security-group set.
type InstancePortSecurityGroupsModel struct {
	ID             types.String `tfsdk:"id"`
	InstanceID     types.String `tfsdk:"instance_id"`
	PortID         types.String `tfsdk:"port_id"`
	SecurityGroups types.Set    `tfsdk:"security_groups"`
}

// apiSetInstancePortSecurityGroupsRequest is the body for
// PUT /instances/{id}/ports/{portId}/security-groups (replace semantics, Neutron
// SG UUIDs). ClearSecurityGroups must be true to clear all SGs on the port
// (empty list); an empty list without the flag is rejected by the backend as a
// probable dropped field.
type apiSetInstancePortSecurityGroupsRequest struct {
	SecurityGroupIDs    []string `json:"securityGroupIds"`
	ClearSecurityGroups bool     `json:"clearSecurityGroups,omitempty"`
}

// apiInstancePortSecurityGroups is one port's authoritative SG set by Neutron SG
// UUID (compute domain PortSecurityGroupsView).
type apiInstancePortSecurityGroups struct {
	PortID           string   `json:"portId"`
	SecurityGroupIDs []string `json:"securityGroupIds"`
}

// apiInstanceSecurityGroups is the authoritative applied SG view returned by
// GET /instances/{id}/security-groups (compute domain InstanceSecurityGroups).
// This resource reads its port back from the per-port Ports breakdown, which is
// populated regardless of the Uniform flag.
type apiInstanceSecurityGroups struct {
	SecurityGroupIDs []string                        `json:"securityGroupIds"`
	Uniform          bool                            `json:"uniform"`
	Ports            []apiInstancePortSecurityGroups `json:"ports"`
}

// findPort returns the SG set for portID, or nil if the port is not present in
// the instance's port breakdown (port detached or instance gone).
func (s *apiInstanceSecurityGroups) findPort(portID string) *apiInstancePortSecurityGroups {
	for i := range s.Ports {
		if s.Ports[i].PortID == portID {
			return &s.Ports[i]
		}
	}
	return nil
}
