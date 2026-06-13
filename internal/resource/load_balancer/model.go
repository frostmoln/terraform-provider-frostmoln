// Package load_balancer implements the frostmoln_load_balancer Terraform resource.
package load_balancer

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// LoadBalancerModel is the Terraform state model for a load balancer.
type LoadBalancerModel struct {
	ID                 types.String `tfsdk:"id"`
	Name               types.String `tfsdk:"name"`
	VPCID              types.String `tfsdk:"vpc_id"`
	SubnetID           types.String `tfsdk:"subnet_id"`
	Description        types.String `tfsdk:"description"`
	VIPAddress         types.String `tfsdk:"vip_address"`
	Scheme             types.String `tfsdk:"scheme"`
	FloatingIPID       types.String `tfsdk:"floating_ip_id"`
	FloatingIPAddress  types.String `tfsdk:"floating_ip_address"`
	Provider           types.String `tfsdk:"provider_type"`
	FlavorID           types.String `tfsdk:"flavor_id"`
	Tags               types.Map    `tfsdk:"tags"`
	VIPPortID          types.String `tfsdk:"vip_port_id"`
	Status             types.String `tfsdk:"status"`
	ProvisioningStatus types.String `tfsdk:"provisioning_status"`
	OperatingStatus    types.String `tfsdk:"operating_status"`
	CreatedAt          types.String `tfsdk:"created_at"`
	UpdatedAt          types.String `tfsdk:"updated_at"`
}

// apiLoadBalancer is the API representation of a load balancer.
type apiLoadBalancer struct {
	ID                 string            `json:"id"`
	Name               string            `json:"name"`
	Description        string            `json:"description,omitempty"`
	VPCID              string            `json:"vpcId"`
	SubnetID           string            `json:"subnetId"`
	VIPAddress         string            `json:"vipAddress,omitempty"`
	VIPPortID          string            `json:"vipPortId,omitempty"`
	Scheme             string            `json:"scheme,omitempty"`
	FloatingIPID       string            `json:"floatingIpId,omitempty"`
	FloatingIPAddress  string            `json:"floatingIpAddress,omitempty"`
	Provider           string            `json:"provider,omitempty"`
	FlavorID           string            `json:"flavorId,omitempty"`
	Status             string            `json:"status"`
	ProvisioningStatus string            `json:"provisioningStatus"`
	OperatingStatus    string            `json:"operatingStatus"`
	Tags               map[string]string `json:"tags,omitempty"`
	CreatedAt          string            `json:"createdAt"`
	UpdatedAt          string            `json:"updatedAt,omitempty"`
}

// apiCreateLoadBalancerRequest is the API request to create a load balancer.
type apiCreateLoadBalancerRequest struct {
	Name         string            `json:"name"`
	Description  string            `json:"description,omitempty"`
	VPCID        string            `json:"vpcId"`
	SubnetID     string            `json:"subnetId"`
	VIPAddress   string            `json:"vipAddress,omitempty"`
	Scheme       string            `json:"scheme,omitempty"`
	FloatingIPID string            `json:"floatingIpId,omitempty"`
	Provider     string            `json:"provider,omitempty"`
	FlavorID     string            `json:"flavorId,omitempty"`
	Tags         map[string]string `json:"tags,omitempty"`
}

// apiUpdateLoadBalancerRequest is the API request to update a load balancer.
// Only name, description, and tags are mutable.
type apiUpdateLoadBalancerRequest struct {
	Name        *string           `json:"name,omitempty"`
	Description *string           `json:"description,omitempty"`
	Tags        map[string]string `json:"tags,omitempty"`
}

// toCreateRequest converts the Terraform model to an API create request.
func (m *LoadBalancerModel) toCreateRequest(ctx context.Context, diags *diag.Diagnostics) apiCreateLoadBalancerRequest {
	req := apiCreateLoadBalancerRequest{
		Name:     m.Name.ValueString(),
		VPCID:    m.VPCID.ValueString(),
		SubnetID: m.SubnetID.ValueString(),
	}

	if !m.Description.IsNull() && !m.Description.IsUnknown() {
		req.Description = m.Description.ValueString()
	}
	if !m.VIPAddress.IsNull() && !m.VIPAddress.IsUnknown() {
		req.VIPAddress = m.VIPAddress.ValueString()
	}
	if !m.Scheme.IsNull() && !m.Scheme.IsUnknown() {
		req.Scheme = m.Scheme.ValueString()
	}
	if !m.FloatingIPID.IsNull() && !m.FloatingIPID.IsUnknown() {
		req.FloatingIPID = m.FloatingIPID.ValueString()
	}
	if !m.Provider.IsNull() && !m.Provider.IsUnknown() {
		req.Provider = m.Provider.ValueString()
	}
	if !m.FlavorID.IsNull() && !m.FlavorID.IsUnknown() {
		req.FlavorID = m.FlavorID.ValueString()
	}
	if !m.Tags.IsNull() && !m.Tags.IsUnknown() {
		tags := make(map[string]string)
		diags.Append(m.Tags.ElementsAs(ctx, &tags, false)...)
		req.Tags = tags
	}

	return req
}

// toUpdateRequest converts the Terraform model to an API update request.
func (m *LoadBalancerModel) toUpdateRequest(ctx context.Context, diags *diag.Diagnostics) apiUpdateLoadBalancerRequest {
	req := apiUpdateLoadBalancerRequest{}

	if !m.Name.IsNull() && !m.Name.IsUnknown() {
		name := m.Name.ValueString()
		req.Name = &name
	}
	if !m.Description.IsNull() && !m.Description.IsUnknown() {
		desc := m.Description.ValueString()
		req.Description = &desc
	} else if m.Description.IsNull() {
		empty := ""
		req.Description = &empty
	}
	if !m.Tags.IsNull() && !m.Tags.IsUnknown() {
		tags := make(map[string]string)
		diags.Append(m.Tags.ElementsAs(ctx, &tags, false)...)
		req.Tags = tags
	}

	return req
}

// fromAPI populates the Terraform model from an API response.
func (m *LoadBalancerModel) fromAPI(ctx context.Context, lb *apiLoadBalancer, diags *diag.Diagnostics) {
	m.ID = types.StringValue(lb.ID)
	m.Name = types.StringValue(lb.Name)
	m.VPCID = types.StringValue(lb.VPCID)
	m.SubnetID = types.StringValue(lb.SubnetID)
	m.Status = types.StringValue(lb.Status)
	m.ProvisioningStatus = types.StringValue(lb.ProvisioningStatus)
	m.OperatingStatus = types.StringValue(lb.OperatingStatus)
	m.CreatedAt = types.StringValue(lb.CreatedAt)

	if lb.Description != "" {
		m.Description = types.StringValue(lb.Description)
	} else if m.Description.IsNull() {
		m.Description = types.StringNull()
	} else {
		m.Description = types.StringValue("")
	}

	if lb.VIPAddress != "" {
		m.VIPAddress = types.StringValue(lb.VIPAddress)
	} else {
		m.VIPAddress = types.StringNull()
	}

	if lb.VIPPortID != "" {
		m.VIPPortID = types.StringValue(lb.VIPPortID)
	} else {
		m.VIPPortID = types.StringNull()
	}

	// scheme is computed (default internal) and always populated on read.
	if lb.Scheme != "" {
		m.Scheme = types.StringValue(lb.Scheme)
	} else {
		m.Scheme = types.StringValue("internal")
	}

	// floating_ip_id is the bring-your-own FIP (config); reflect the attached
	// FIP from the read. floating_ip_address is computed.
	if lb.FloatingIPID != "" {
		m.FloatingIPID = types.StringValue(lb.FloatingIPID)
	} else {
		m.FloatingIPID = types.StringNull()
	}
	if lb.FloatingIPAddress != "" {
		m.FloatingIPAddress = types.StringValue(lb.FloatingIPAddress)
	} else {
		m.FloatingIPAddress = types.StringNull()
	}

	if lb.Provider != "" {
		m.Provider = types.StringValue(lb.Provider)
	} else if m.Provider.IsNull() {
		m.Provider = types.StringNull()
	}

	if lb.FlavorID != "" {
		m.FlavorID = types.StringValue(lb.FlavorID)
	} else if m.FlavorID.IsNull() {
		m.FlavorID = types.StringNull()
	} else {
		m.FlavorID = types.StringNull()
	}

	if lb.UpdatedAt != "" {
		m.UpdatedAt = types.StringValue(lb.UpdatedAt)
	} else {
		m.UpdatedAt = types.StringNull()
	}

	if len(lb.Tags) > 0 {
		tagsMap, d := types.MapValueFrom(ctx, types.StringType, lb.Tags)
		diags.Append(d...)
		m.Tags = tagsMap
	} else {
		m.Tags = types.MapNull(types.StringType)
	}
}
