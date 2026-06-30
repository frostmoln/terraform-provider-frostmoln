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
	InstanceID types.String `tfsdk:"instance_id"`
	Tags       types.Map    `tfsdk:"tags"`
	Status     types.String `tfsdk:"status"`
	PrivateIP  types.String `tfsdk:"private_ip"`
	CreatedAt  types.String `tfsdk:"created_at"`
}

// apiFloatingIP is the API representation of a floating IP. The network service
// serializes the address as `floatingIpAddress` and the fixed IP as
// `fixedIpAddress`; the attached port is `portId` (there is no instanceId or
// region on the wire). See network/internal/domain/floating_ip.go.
type apiFloatingIP struct {
	ID        string            `json:"id"`
	Address   string            `json:"floatingIpAddress"`
	Status    string            `json:"status"`
	PortID    string            `json:"portId,omitempty"`
	PrivateIP string            `json:"fixedIpAddress,omitempty"`
	Tags      map[string]string `json:"tags,omitempty"`
	CreatedAt string            `json:"createdAt"`
}

// apiAllocateFloatingIPRequest is the API request to allocate a floating IP.
// The allocate routes through provisioning, which infers the external network
// from the project; region is not part of the contract (ADR-0022).
type apiAllocateFloatingIPRequest struct {
	Tags map[string]string `json:"tags,omitempty"`
}

// apiAssociateFloatingIPRequest is the API request to associate a floating IP.
// The provisioning associate handler requires a `portId` (binding:"required");
// the provider resolves it from the target instance's first network port.
type apiAssociateFloatingIPRequest struct {
	PortID string `json:"portId"`
}

// apiInstanceForPort is the subset of the instance read response used to resolve
// the Neutron port to associate a floating IP with (networks[].portId).
type apiInstanceForPort struct {
	Networks []struct {
		PortID string `json:"portId,omitempty"`
	} `json:"networks,omitempty"`
}

// apiUpdateFloatingIPRequest is the API request to update tags on a floating IP.
type apiUpdateFloatingIPRequest struct {
	Tags map[string]string `json:"tags,omitempty"`
}

// toAllocateRequest converts the Terraform model to an API allocate request.
func (m *FloatingIPModel) toAllocateRequest(ctx context.Context, diags *diag.Diagnostics) apiAllocateFloatingIPRequest {
	req := apiAllocateFloatingIPRequest{}

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
	m.Status = types.StringValue(fip.Status)
	m.CreatedAt = types.StringValue(fip.CreatedAt)

	// The backend never returns instanceId (only portId). instance_id is the
	// user's association input, so it is preserved as-is when the FIP is attached
	// (portId present) and cleared when the FIP is detached (no portId).
	if fip.PortID == "" {
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
