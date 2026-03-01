// Package security_group implements the fm_security_group Terraform resource.
package security_group

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// SecurityGroupModel is the Terraform state model for a security group.
type SecurityGroupModel struct {
	ID          types.String `tfsdk:"id"`
	Name        types.String `tfsdk:"name"`
	Description types.String `tfsdk:"description"`
	VPCID       types.String `tfsdk:"vpc_id"`
	Tags        types.Map    `tfsdk:"tags"`
	IsDefault   types.Bool   `tfsdk:"is_default"`
	CreatedAt   types.String `tfsdk:"created_at"`
}

// apiSecurityGroup is the API representation of a security group.
type apiSecurityGroup struct {
	ID          string            `json:"id"`
	Name        string            `json:"name"`
	Description string            `json:"description,omitempty"`
	VPCID       string            `json:"vpcId,omitempty"`
	IsDefault   bool              `json:"isDefault"`
	Tags        map[string]string `json:"tags,omitempty"`
	CreatedAt   string            `json:"createdAt"`
}

// apiCreateSecurityGroupRequest is the API request to create a security group.
type apiCreateSecurityGroupRequest struct {
	Name        string            `json:"name"`
	Description string            `json:"description,omitempty"`
	VPCID       string            `json:"vpcId,omitempty"`
	Tags        map[string]string `json:"tags,omitempty"`
}

// apiUpdateSecurityGroupRequest is the API request to update a security group.
type apiUpdateSecurityGroupRequest struct {
	Name        *string           `json:"name,omitempty"`
	Description *string           `json:"description,omitempty"`
	Tags        map[string]string `json:"tags,omitempty"`
}

// toCreateRequest converts the Terraform model to an API create request.
func (m *SecurityGroupModel) toCreateRequest(ctx context.Context, diags *diag.Diagnostics) apiCreateSecurityGroupRequest {
	req := apiCreateSecurityGroupRequest{
		Name: m.Name.ValueString(),
	}

	if !m.Description.IsNull() && !m.Description.IsUnknown() {
		req.Description = m.Description.ValueString()
	}

	if !m.VPCID.IsNull() && !m.VPCID.IsUnknown() {
		req.VPCID = m.VPCID.ValueString()
	}

	if !m.Tags.IsNull() && !m.Tags.IsUnknown() {
		tags := make(map[string]string)
		diags.Append(m.Tags.ElementsAs(ctx, &tags, false)...)
		req.Tags = tags
	}

	return req
}

// toUpdateRequest converts the Terraform model to an API update request.
func (m *SecurityGroupModel) toUpdateRequest(ctx context.Context, diags *diag.Diagnostics) apiUpdateSecurityGroupRequest {
	req := apiUpdateSecurityGroupRequest{}

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
func (m *SecurityGroupModel) fromAPI(ctx context.Context, sg *apiSecurityGroup, diags *diag.Diagnostics) {
	m.ID = types.StringValue(sg.ID)
	m.Name = types.StringValue(sg.Name)
	m.IsDefault = types.BoolValue(sg.IsDefault)
	m.CreatedAt = types.StringValue(sg.CreatedAt)

	if sg.Description != "" {
		m.Description = types.StringValue(sg.Description)
	} else if m.Description.IsNull() {
		m.Description = types.StringNull()
	} else {
		m.Description = types.StringValue("")
	}

	if sg.VPCID != "" {
		m.VPCID = types.StringValue(sg.VPCID)
	} else {
		m.VPCID = types.StringNull()
	}

	if len(sg.Tags) > 0 {
		tagsMap, d := types.MapValueFrom(ctx, types.StringType, sg.Tags)
		diags.Append(d...)
		m.Tags = tagsMap
	} else if m.Tags.IsNull() {
		m.Tags = types.MapNull(types.StringType)
	} else {
		m.Tags = types.MapNull(types.StringType)
	}
}
