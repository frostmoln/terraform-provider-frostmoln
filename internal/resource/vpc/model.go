// Package vpc implements the fm_vpc Terraform resource.
package vpc

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// VPCModel is the Terraform state model for a VPC.
type VPCModel struct {
	ID          types.String `tfsdk:"id"`
	Name        types.String `tfsdk:"name"`
	Description types.String `tfsdk:"description"`
	CIDR        types.String `tfsdk:"cidr"`
	Tags        types.Map    `tfsdk:"tags"`
	Status      types.String `tfsdk:"status"`
	IsDefault   types.Bool   `tfsdk:"is_default"`
	SubnetCount types.Int64  `tfsdk:"subnet_count"`
	CreatedAt   types.String `tfsdk:"created_at"`
	UpdatedAt   types.String `tfsdk:"updated_at"`
}

// apiVPC is the API representation of a VPC.
type apiVPC struct {
	ID          string            `json:"id"`
	Name        string            `json:"name"`
	Description string            `json:"description,omitempty"`
	CIDR        string            `json:"cidrBlock"`
	Status      string            `json:"status"`
	IsDefault   bool              `json:"isDefault"`
	SubnetCount int               `json:"subnetCount"`
	Tags        map[string]string `json:"tags,omitempty"`
	CreatedAt   string            `json:"createdAt"`
	UpdatedAt   string            `json:"updatedAt,omitempty"`
}

// apiCreateVPCRequest is the API request to create a VPC.
type apiCreateVPCRequest struct {
	Name        string            `json:"name"`
	Description string            `json:"description,omitempty"`
	CIDR        string            `json:"cidrBlock"`
	Tags        map[string]string `json:"tags,omitempty"`
}

// apiUpdateVPCRequest is the API request to update a VPC.
type apiUpdateVPCRequest struct {
	Name        *string           `json:"name,omitempty"`
	Description *string           `json:"description,omitempty"`
	Tags        map[string]string `json:"tags,omitempty"`
}

// toCreateRequest converts the Terraform model to an API create request.
func (m *VPCModel) toCreateRequest(ctx context.Context, diags *diag.Diagnostics) apiCreateVPCRequest {
	req := apiCreateVPCRequest{
		Name: m.Name.ValueString(),
		CIDR: m.CIDR.ValueString(),
	}

	if !m.Description.IsNull() && !m.Description.IsUnknown() {
		req.Description = m.Description.ValueString()
	}

	if !m.Tags.IsNull() && !m.Tags.IsUnknown() {
		tags := make(map[string]string)
		diags.Append(m.Tags.ElementsAs(ctx, &tags, false)...)
		req.Tags = tags
	}

	return req
}

// toUpdateRequest converts the Terraform model to an API update request.
func (m *VPCModel) toUpdateRequest(ctx context.Context, diags *diag.Diagnostics) apiUpdateVPCRequest {
	req := apiUpdateVPCRequest{}

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
func (m *VPCModel) fromAPI(ctx context.Context, vpc *apiVPC, diags *diag.Diagnostics) {
	m.ID = types.StringValue(vpc.ID)
	m.Name = types.StringValue(vpc.Name)
	m.CIDR = types.StringValue(vpc.CIDR)
	m.Status = types.StringValue(vpc.Status)
	m.IsDefault = types.BoolValue(vpc.IsDefault)
	m.SubnetCount = types.Int64Value(int64(vpc.SubnetCount))
	m.CreatedAt = types.StringValue(vpc.CreatedAt)

	if vpc.Description != "" {
		m.Description = types.StringValue(vpc.Description)
	} else if m.Description.IsNull() {
		m.Description = types.StringNull()
	} else {
		m.Description = types.StringValue("")
	}

	if vpc.UpdatedAt != "" {
		m.UpdatedAt = types.StringValue(vpc.UpdatedAt)
	} else {
		m.UpdatedAt = types.StringNull()
	}

	if len(vpc.Tags) > 0 {
		tagsMap, d := types.MapValueFrom(ctx, types.StringType, vpc.Tags)
		diags.Append(d...)
		m.Tags = tagsMap
	} else if m.Tags.IsNull() {
		m.Tags = types.MapNull(types.StringType)
	} else {
		m.Tags = types.MapNull(types.StringType)
	}
}
