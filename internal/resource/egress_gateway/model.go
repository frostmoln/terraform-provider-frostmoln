// Package egress_gateway implements the frostmoln_egress_gateway Terraform resource.
package egress_gateway

import "github.com/hashicorp/terraform-plugin-framework/types"

// EgressGatewayModel is the Terraform state model for a VPC's outbound path.
type EgressGatewayModel struct {
	ID            types.String `tfsdk:"id"`
	VPCID         types.String `tfsdk:"vpc_id"`
	Mode          types.String `tfsdk:"mode"`
	SourceAddress types.String `tfsdk:"source_address"`
	Status        types.String `tfsdk:"status"`
	Origin        types.String `tfsdk:"origin"`
}

// apiEgressGateway is the API representation.
//
// Deliberately not router-shaped: no router id, no gateway info. Those have no
// meaning under a future shared-NAT mode, and once a field ships in a provider
// schema it is a compatibility obligation across every practitioner's state.
type apiEgressGateway struct {
	ID            string `json:"id"`
	VPCID         string `json:"vpcId"`
	TenantID      string `json:"tenantId"`
	Mode          string `json:"mode"`
	SourceAddress string `json:"sourceAddress,omitempty"`
	Status        string `json:"status"`
	Origin        string `json:"origin"`
}

// apiEgressGatewayList is the filtered-list response. A VPC with no gateway
// returns an empty list rather than 404, which is what lets Read treat "gone"
// as "removed from state" without conflating it with a transport error.
type apiEgressGatewayList struct {
	EgressGateways []apiEgressGateway `json:"egressGateways"`
	TotalCount     int                `json:"totalCount"`
}

type apiCreateEgressGatewayRequest struct {
	VPCID string `json:"vpcId"`
	Mode  string `json:"mode"`
}

type apiUpdateEgressGatewayRequest struct {
	Mode string `json:"mode"`
	// AcknowledgeConnectivityLoss is set by the provider on a mode change: the
	// plan output IS the practitioner's acknowledgement, shown before apply.
	AcknowledgeConnectivityLoss bool `json:"acknowledgeConnectivityLoss"`
}
