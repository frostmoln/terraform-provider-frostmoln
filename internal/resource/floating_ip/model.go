// Package floating_ip implements the fm_floating_ip Terraform resource.
package floating_ip

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// FloatingIPModel is the Terraform state model for a floating IP.
type FloatingIPModel struct {
	ID         types.String `tfsdk:"id"`
	Address    types.String `tfsdk:"address"`
	Region     types.String `tfsdk:"region"`
	InstanceID types.String `tfsdk:"instance_id"`
	Tags       types.Map    `tfsdk:"tags"`
	Status     types.String `tfsdk:"status"`
	PrivateIP  types.String `tfsdk:"private_ip"`
	CreatedAt  types.String `tfsdk:"created_at"`
}

// apiFloatingIP is the API representation of a floating IP.
type apiFloatingIP struct {
	ID         string            `json:"id"`
	Address    string            `json:"address"`
	Region     string            `json:"region"`
	Status     string            `json:"status"`
	InstanceID string            `json:"instanceId,omitempty"`
	PrivateIP  string            `json:"privateIp,omitempty"`
	Tags       map[string]string `json:"tags,omitempty"`
	CreatedAt  string            `json:"createdAt"`
}

// apiAllocateFloatingIPRequest is the API request to allocate a floating IP.
type apiAllocateFloatingIPRequest struct {
	Region string            `json:"region,omitempty"`
	Tags   map[string]string `json:"tags,omitempty"`
}

// apiAssociateFloatingIPRequest is the API request to associate a floating IP.
type apiAssociateFloatingIPRequest struct {
	InstanceID string `json:"instanceId"`
}

// apiUpdateFloatingIPRequest is the API request to update tags on a floating IP.
type apiUpdateFloatingIPRequest struct {
	Tags map[string]string `json:"tags,omitempty"`
}

// toAllocateRequest converts the Terraform model to an API allocate request.
func (m *FloatingIPModel) toAllocateRequest(ctx context.Context, diags *diag.Diagnostics) apiAllocateFloatingIPRequest {
	req := apiAllocateFloatingIPRequest{}

	if !m.Region.IsNull() && !m.Region.IsUnknown() {
		req.Region = m.Region.ValueString()
	}

	if !m.Tags.IsNull() && !m.Tags.IsUnknown() {
		tags := make(map[string]string)
		diags.Append(m.Tags.ElementsAs(ctx, &tags, false)...)
		req.Tags = tags
	}

	return req
}

// fromAPI populates the Terraform model from an API response.
func (m *FloatingIPModel) fromAPI(ctx context.Context, fip *apiFloatingIP, diags *diag.Diagnostics) {
	m.ID = types.StringValue(fip.ID)
	m.Address = types.StringValue(fip.Address)
	m.Region = types.StringValue(fip.Region)
	m.Status = types.StringValue(fip.Status)
	m.CreatedAt = types.StringValue(fip.CreatedAt)

	if fip.InstanceID != "" {
		m.InstanceID = types.StringValue(fip.InstanceID)
	} else {
		m.InstanceID = types.StringNull()
	}

	if fip.PrivateIP != "" {
		m.PrivateIP = types.StringValue(fip.PrivateIP)
	} else {
		m.PrivateIP = types.StringNull()
	}

	if len(fip.Tags) > 0 {
		tagsMap, d := types.MapValueFrom(ctx, types.StringType, fip.Tags)
		diags.Append(d...)
		m.Tags = tagsMap
	} else if m.Tags.IsNull() {
		m.Tags = types.MapNull(types.StringType)
	} else {
		m.Tags = types.MapNull(types.StringType)
	}
}
