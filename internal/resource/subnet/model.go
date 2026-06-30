// Package subnet implements the fm_subnet Terraform resource.
package subnet

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// SubnetModel is the Terraform state model for a subnet.
type SubnetModel struct {
	ID           types.String `tfsdk:"id"`
	Name         types.String `tfsdk:"name"`
	Description  types.String `tfsdk:"description"`
	CIDR         types.String `tfsdk:"cidr"`
	VPCID        types.String `tfsdk:"vpc_id"`
	Zone         types.String `tfsdk:"zone"`
	GatewayIP    types.String `tfsdk:"gateway_ip"`
	DNSServers   types.List   `tfsdk:"dns_servers"`
	Tags         types.Map    `tfsdk:"tags"`
	Status       types.String `tfsdk:"status"`
	AvailableIPs types.Int64  `tfsdk:"available_ips"`
	CreatedAt    types.String `tfsdk:"created_at"`
}

// apiSubnet is the API representation of a subnet.
type apiSubnet struct {
	ID           string            `json:"id"`
	Name         string            `json:"name"`
	Description  string            `json:"description,omitempty"`
	CIDR         string            `json:"cidrBlock"`
	VPCID        string            `json:"vpcId"`
	Zone         string            `json:"availabilityZone,omitempty"`
	GatewayIP    string            `json:"gatewayIp,omitempty"`
	DNSServers   []string          `json:"dnsServers,omitempty"`
	Status       string            `json:"status"`
	AvailableIPs int               `json:"availableIpCount"`
	Tags         map[string]string `json:"tags,omitempty"`
	CreatedAt    string            `json:"createdAt"`
}

// apiCreateSubnetRequest is the API request to create a subnet. The create
// routes through provisioning, which reads the DNS servers under
// `dnsNameservers` (provisioning/internal/handler/http/network_handler.go);
// the read response uses `dnsServers` (apiSubnet).
type apiCreateSubnetRequest struct {
	Name        string            `json:"name"`
	Description string            `json:"description,omitempty"`
	CIDR        string            `json:"cidrBlock"`
	VPCID       string            `json:"vpcId"`
	Zone        string            `json:"availabilityZone,omitempty"`
	GatewayIP   string            `json:"gatewayIp,omitempty"`
	DNSServers  []string          `json:"dnsNameservers,omitempty"`
	Tags        map[string]string `json:"tags,omitempty"`
}

// apiUpdateSubnetRequest is the API request to update a subnet (tags only).
type apiUpdateSubnetRequest struct {
	Tags map[string]string `json:"tags,omitempty"`
}

// toCreateRequest converts the Terraform model to an API create request.
func (m *SubnetModel) toCreateRequest(ctx context.Context, diags *diag.Diagnostics) apiCreateSubnetRequest {
	req := apiCreateSubnetRequest{
		Name:  m.Name.ValueString(),
		CIDR:  m.CIDR.ValueString(),
		VPCID: m.VPCID.ValueString(),
	}

	if !m.Description.IsNull() && !m.Description.IsUnknown() {
		req.Description = m.Description.ValueString()
	}

	if !m.Zone.IsNull() && !m.Zone.IsUnknown() {
		req.Zone = m.Zone.ValueString()
	}

	if !m.GatewayIP.IsNull() && !m.GatewayIP.IsUnknown() {
		req.GatewayIP = m.GatewayIP.ValueString()
	}

	if !m.DNSServers.IsNull() && !m.DNSServers.IsUnknown() {
		var servers []string
		diags.Append(m.DNSServers.ElementsAs(ctx, &servers, false)...)
		req.DNSServers = servers
	}

	if !m.Tags.IsNull() && !m.Tags.IsUnknown() {
		tags := make(map[string]string)
		diags.Append(m.Tags.ElementsAs(ctx, &tags, false)...)
		req.Tags = tags
	}

	return req
}

// toUpdateRequest converts the Terraform model to an API update request (tags only).
func (m *SubnetModel) toUpdateRequest(ctx context.Context, diags *diag.Diagnostics) apiUpdateSubnetRequest {
	req := apiUpdateSubnetRequest{}

	if !m.Tags.IsNull() && !m.Tags.IsUnknown() {
		tags := make(map[string]string)
		diags.Append(m.Tags.ElementsAs(ctx, &tags, false)...)
		req.Tags = tags
	}

	return req
}

// fromAPI populates the Terraform model from an API response.
func (m *SubnetModel) fromAPI(ctx context.Context, subnet *apiSubnet, diags *diag.Diagnostics) {
	m.ID = types.StringValue(subnet.ID)
	m.Name = types.StringValue(subnet.Name)
	m.CIDR = types.StringValue(subnet.CIDR)
	m.VPCID = types.StringValue(subnet.VPCID)
	m.Status = types.StringValue(subnet.Status)
	m.AvailableIPs = types.Int64Value(int64(subnet.AvailableIPs))
	m.CreatedAt = types.StringValue(subnet.CreatedAt)

	if subnet.Description != "" {
		m.Description = types.StringValue(subnet.Description)
	} else if m.Description.IsNull() {
		m.Description = types.StringNull()
	} else {
		m.Description = types.StringValue("")
	}

	if subnet.Zone != "" {
		m.Zone = types.StringValue(subnet.Zone)
	} else {
		m.Zone = types.StringNull()
	}

	if subnet.GatewayIP != "" {
		m.GatewayIP = types.StringValue(subnet.GatewayIP)
	} else {
		m.GatewayIP = types.StringNull()
	}

	if len(subnet.DNSServers) > 0 {
		dnsServers, d := types.ListValueFrom(ctx, types.StringType, subnet.DNSServers)
		diags.Append(d...)
		m.DNSServers = dnsServers
	} else if m.DNSServers.IsNull() {
		m.DNSServers = types.ListNull(types.StringType)
	} else {
		m.DNSServers = types.ListNull(types.StringType)
	}

	if len(subnet.Tags) > 0 {
		tagsMap, d := types.MapValueFrom(ctx, types.StringType, subnet.Tags)
		diags.Append(d...)
		m.Tags = tagsMap
	} else if m.Tags.IsNull() {
		m.Tags = types.MapNull(types.StringType)
	} else {
		m.Tags = types.MapNull(types.StringType)
	}
}
