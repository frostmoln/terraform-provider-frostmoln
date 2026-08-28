// Package application_gateway implements the frostmoln_application_gateway
// Terraform resource.
package application_gateway

import (
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// GatewayModel is the Terraform state model for an Application Gateway.
type GatewayModel struct {
	ID       types.String `tfsdk:"id"`
	Name     types.String `tfsdk:"name"`
	FlavorID types.String `tfsdk:"flavor_id"`
	Version  types.String `tfsdk:"version"`
	VPCID    types.String `tfsdk:"vpc_id"`
	SubnetID types.String `tfsdk:"subnet_id"`

	PublicIPMode types.String `tfsdk:"public_ip_mode"`
	PublicIPID   types.String `tfsdk:"public_ip_id"`

	Status    types.String `tfsdk:"status"`
	PublicIP  types.String `tfsdk:"public_ip"`
	PrivateIP types.String `tfsdk:"private_ip"`
	VPCCIDR   types.String `tfsdk:"vpc_cidr"`

	ConfigGeneration types.Int64  `tfsdk:"config_generation"`
	ConfigRevision   types.Int64  `tfsdk:"config_revision"`
	ConfigStatus     types.String `tfsdk:"config_status"`
	ConfigDetail     types.String `tfsdk:"config_detail"`

	WafPolicyID types.String `tfsdk:"waf_policy_id"`

	CreatedAt types.String `tfsdk:"created_at"`
	UpdatedAt types.String `tfsdk:"updated_at"`
}

// apiGateway is the API representation of an Application Gateway.
type apiGateway struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	TenantID string `json:"tenantId"`
	Status   string `json:"status"`
	FlavorID string `json:"flavorId"`
	Version  string `json:"version"`

	VPCID     string `json:"vpcId"`
	SubnetID  string `json:"subnetId"`
	PrivateIP string `json:"privateIp,omitempty"`
	VPCCIDR   string `json:"vpcCidr,omitempty"`

	PublicIPMode string `json:"publicIpMode"`
	// PublicIPID is echoed back ONLY for a bring-your-own address. For a
	// pool-allocated one the server deliberately leaves it empty, which is what
	// lets this be a plain Optional attribute instead of Optional+Computed --
	// and Optional+Computed cannot express "the user removed this attribute".
	PublicIPID string `json:"publicIpId,omitempty"`
	PublicIP   string `json:"publicIp,omitempty"`

	WafPolicyID       string `json:"wafPolicyId,omitempty"`
	AppliedWafVersion int    `json:"appliedWafVersion"`

	ConfigGeneration int64  `json:"configGeneration"`
	ConfigStatus     string `json:"configStatus"`
	ConfigRevision   *int64 `json:"configRevision,omitempty"`
	ConfigDetail     string `json:"configDetail,omitempty"`

	CreatedAt string `json:"createdAt"`
	UpdatedAt string `json:"updatedAt,omitempty"`
}

// apiCreateGatewayRequest is the API create body.
type apiCreateGatewayRequest struct {
	Name         string `json:"name"`
	FlavorID     string `json:"flavorId"`
	VPCID        string `json:"vpcId"`
	SubnetID     string `json:"subnetId"`
	PublicIPMode string `json:"publicIpMode"`
	PublicIPID   string `json:"publicIpId,omitempty"`
}

// apiUpdateGatewayRequest is the API PATCH body. Only the name is mutable in
// place; everything else forces a replacement.
type apiUpdateGatewayRequest struct {
	Name *string `json:"name,omitempty"`
}

// apiCreateGatewayResponse is the 202 body: the row as created, plus the
// provisioning operation to poll.
type apiCreateGatewayResponse struct {
	Gateway     *apiGateway `json:"gateway"`
	OperationID string      `json:"operationId"`
}

// apiOperationResponse is a 202 with no resource body.
type apiOperationResponse struct {
	OperationID string `json:"operationId"`
}

// fromAPI copies the API representation into Terraform state.
func (m *GatewayModel) fromAPI(g *apiGateway) {
	m.ID = types.StringValue(g.ID)
	m.Name = types.StringValue(g.Name)
	m.FlavorID = types.StringValue(g.FlavorID)
	m.Version = types.StringValue(g.Version)
	m.VPCID = types.StringValue(g.VPCID)
	m.SubnetID = types.StringValue(g.SubnetID)
	m.PublicIPMode = types.StringValue(g.PublicIPMode)

	// 🔴 EMPTY MEANS NULL HERE, NOT "". public_ip_id is a plain Optional
	// attribute; writing types.StringValue("") for a pool-allocated gateway
	// would make every subsequent plan show `public_ip_id: "" -> null` forever.
	m.PublicIPID = optionalString(g.PublicIPID)

	m.Status = types.StringValue(g.Status)
	m.PublicIP = types.StringValue(g.PublicIP)
	m.PrivateIP = types.StringValue(g.PrivateIP)
	m.VPCCIDR = types.StringValue(g.VPCCIDR)

	m.ConfigGeneration = types.Int64Value(g.ConfigGeneration)
	if g.ConfigRevision != nil {
		m.ConfigRevision = types.Int64Value(*g.ConfigRevision)
	} else {
		// Null, not zero: "never applied" and "applied revision 0" are
		// different facts and revision 0 is a real value.
		m.ConfigRevision = types.Int64Null()
	}
	m.ConfigStatus = types.StringValue(g.ConfigStatus)
	m.ConfigDetail = types.StringValue(g.ConfigDetail)
	m.WafPolicyID = types.StringValue(g.WafPolicyID)

	m.CreatedAt = types.StringValue(g.CreatedAt)
	m.UpdatedAt = types.StringValue(g.UpdatedAt)
}

// optionalString maps "" to null for an attribute the practitioner may legally
// leave unset.
func optionalString(s string) types.String {
	if s == "" {
		return types.StringNull()
	}
	return types.StringValue(s)
}
