// Package launch_template implements the frostmoln_launch_template Terraform resource.
package launch_template

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// LaunchTemplateModel is the Terraform state model for a launch template.
type LaunchTemplateModel struct {
	ID               types.String `tfsdk:"id"`
	Name             types.String `tfsdk:"name"`
	FlavorID         types.String `tfsdk:"flavor_id"`
	ImageID          types.String `tfsdk:"image_id"`
	VPCID            types.String `tfsdk:"vpc_id"`
	SSHKeyIDs        types.Set    `tfsdk:"ssh_key_ids"`
	SecurityGroupIDs types.Set    `tfsdk:"security_group_ids"`
	UserData         types.String `tfsdk:"user_data"`
	Metadata         types.Map    `tfsdk:"metadata"`
	Tags             types.Map    `tfsdk:"tags"`
	CreatedAt        types.String `tfsdk:"created_at"`
	UpdatedAt        types.String `tfsdk:"updated_at"`
}

// apiLaunchTemplate is the API representation of a launch template.
type apiLaunchTemplate struct {
	ID               string            `json:"id"`
	Name             string            `json:"name"`
	FlavorID         string            `json:"flavorId"`
	ImageID          string            `json:"imageId"`
	VPCID            string            `json:"vpcId"`
	SSHKeyIDs        []string          `json:"sshKeyIds,omitempty"`
	SecurityGroupIDs []string          `json:"securityGroupIds,omitempty"`
	UserData         string            `json:"userData,omitempty"`
	Metadata         map[string]string `json:"metadata,omitempty"`
	Tags             map[string]string `json:"tags,omitempty"`
	CreatedAt        string            `json:"createdAt"`
	UpdatedAt        string            `json:"updatedAt,omitempty"`
}

// apiCreateLaunchTemplateRequest is the API request to create a launch template.
type apiCreateLaunchTemplateRequest struct {
	Name             string            `json:"name"`
	FlavorID         string            `json:"flavorId"`
	ImageID          string            `json:"imageId"`
	VPCID            string            `json:"vpcId"`
	SSHKeyIDs        []string          `json:"sshKeyIds,omitempty"`
	SecurityGroupIDs []string          `json:"securityGroupIds,omitempty"`
	UserData         string            `json:"userData,omitempty"`
	Metadata         map[string]string `json:"metadata,omitempty"`
	Tags             map[string]string `json:"tags,omitempty"`
}

// apiUpdateLaunchTemplateRequest is the API request to update a launch template.
type apiUpdateLaunchTemplateRequest struct {
	Name             *string           `json:"name,omitempty"`
	FlavorID         *string           `json:"flavorId,omitempty"`
	ImageID          *string           `json:"imageId,omitempty"`
	VPCID            *string           `json:"vpcId,omitempty"`
	SSHKeyIDs        []string          `json:"sshKeyIds,omitempty"`
	SecurityGroupIDs []string          `json:"securityGroupIds,omitempty"`
	UserData         *string           `json:"userData,omitempty"`
	Metadata         map[string]string `json:"metadata,omitempty"`
	Tags             map[string]string `json:"tags,omitempty"`
}

// toCreateRequest converts the Terraform model to an API create request.
func (m *LaunchTemplateModel) toCreateRequest(ctx context.Context, diags *diag.Diagnostics) apiCreateLaunchTemplateRequest {
	req := apiCreateLaunchTemplateRequest{
		Name:     m.Name.ValueString(),
		FlavorID: m.FlavorID.ValueString(),
		ImageID:  m.ImageID.ValueString(),
		VPCID:    m.VPCID.ValueString(),
	}

	if !m.SSHKeyIDs.IsNull() && !m.SSHKeyIDs.IsUnknown() {
		var ids []string
		diags.Append(m.SSHKeyIDs.ElementsAs(ctx, &ids, false)...)
		req.SSHKeyIDs = ids
	}

	if !m.SecurityGroupIDs.IsNull() && !m.SecurityGroupIDs.IsUnknown() {
		var ids []string
		diags.Append(m.SecurityGroupIDs.ElementsAs(ctx, &ids, false)...)
		req.SecurityGroupIDs = ids
	}

	if !m.UserData.IsNull() && !m.UserData.IsUnknown() {
		req.UserData = m.UserData.ValueString()
	}

	if !m.Metadata.IsNull() && !m.Metadata.IsUnknown() {
		meta := make(map[string]string)
		diags.Append(m.Metadata.ElementsAs(ctx, &meta, false)...)
		req.Metadata = meta
	}

	if !m.Tags.IsNull() && !m.Tags.IsUnknown() {
		tags := make(map[string]string)
		diags.Append(m.Tags.ElementsAs(ctx, &tags, false)...)
		req.Tags = tags
	}

	return req
}

// toUpdateRequest converts the Terraform model to an API update request, comparing with current state.
func (m *LaunchTemplateModel) toUpdateRequest(ctx context.Context, state *LaunchTemplateModel, diags *diag.Diagnostics) apiUpdateLaunchTemplateRequest {
	req := apiUpdateLaunchTemplateRequest{}

	if !m.Name.Equal(state.Name) {
		v := m.Name.ValueString()
		req.Name = &v
	}
	if !m.FlavorID.Equal(state.FlavorID) {
		v := m.FlavorID.ValueString()
		req.FlavorID = &v
	}
	if !m.ImageID.Equal(state.ImageID) {
		v := m.ImageID.ValueString()
		req.ImageID = &v
	}
	if !m.VPCID.Equal(state.VPCID) {
		v := m.VPCID.ValueString()
		req.VPCID = &v
	}

	if !m.SSHKeyIDs.Equal(state.SSHKeyIDs) {
		if !m.SSHKeyIDs.IsNull() && !m.SSHKeyIDs.IsUnknown() {
			var ids []string
			diags.Append(m.SSHKeyIDs.ElementsAs(ctx, &ids, false)...)
			req.SSHKeyIDs = ids
		} else {
			req.SSHKeyIDs = []string{}
		}
	}

	if !m.SecurityGroupIDs.Equal(state.SecurityGroupIDs) {
		if !m.SecurityGroupIDs.IsNull() && !m.SecurityGroupIDs.IsUnknown() {
			var ids []string
			diags.Append(m.SecurityGroupIDs.ElementsAs(ctx, &ids, false)...)
			req.SecurityGroupIDs = ids
		} else {
			req.SecurityGroupIDs = []string{}
		}
	}

	if !m.UserData.Equal(state.UserData) {
		if !m.UserData.IsNull() && !m.UserData.IsUnknown() {
			v := m.UserData.ValueString()
			req.UserData = &v
		}
	}

	if !m.Metadata.Equal(state.Metadata) {
		if !m.Metadata.IsNull() && !m.Metadata.IsUnknown() {
			meta := make(map[string]string)
			diags.Append(m.Metadata.ElementsAs(ctx, &meta, false)...)
			req.Metadata = meta
		} else {
			req.Metadata = map[string]string{}
		}
	}

	if !m.Tags.Equal(state.Tags) {
		if !m.Tags.IsNull() && !m.Tags.IsUnknown() {
			tags := make(map[string]string)
			diags.Append(m.Tags.ElementsAs(ctx, &tags, false)...)
			req.Tags = tags
		} else {
			req.Tags = map[string]string{}
		}
	}

	return req
}

// fromAPI populates the Terraform model from an API response.
func (m *LaunchTemplateModel) fromAPI(ctx context.Context, lt *apiLaunchTemplate, diags *diag.Diagnostics) {
	m.ID = types.StringValue(lt.ID)
	m.Name = types.StringValue(lt.Name)
	m.FlavorID = types.StringValue(lt.FlavorID)
	m.ImageID = types.StringValue(lt.ImageID)
	m.VPCID = types.StringValue(lt.VPCID)
	m.CreatedAt = types.StringValue(lt.CreatedAt)

	if lt.UpdatedAt != "" {
		m.UpdatedAt = types.StringValue(lt.UpdatedAt)
	} else {
		m.UpdatedAt = types.StringNull()
	}

	// SSH key IDs
	if len(lt.SSHKeyIDs) > 0 {
		keySet, d := types.SetValueFrom(ctx, types.StringType, lt.SSHKeyIDs)
		diags.Append(d...)
		m.SSHKeyIDs = keySet
	} else if !m.SSHKeyIDs.IsNull() {
		keySet, d := types.SetValueFrom(ctx, types.StringType, []string{})
		diags.Append(d...)
		m.SSHKeyIDs = keySet
	} else {
		m.SSHKeyIDs = types.SetNull(types.StringType)
	}

	// Security group IDs
	if len(lt.SecurityGroupIDs) > 0 {
		sgSet, d := types.SetValueFrom(ctx, types.StringType, lt.SecurityGroupIDs)
		diags.Append(d...)
		m.SecurityGroupIDs = sgSet
	} else if !m.SecurityGroupIDs.IsNull() {
		sgSet, d := types.SetValueFrom(ctx, types.StringType, []string{})
		diags.Append(d...)
		m.SecurityGroupIDs = sgSet
	} else {
		m.SecurityGroupIDs = types.SetNull(types.StringType)
	}

	// Metadata
	if len(lt.Metadata) > 0 {
		metaMap, d := types.MapValueFrom(ctx, types.StringType, lt.Metadata)
		diags.Append(d...)
		m.Metadata = metaMap
	} else if !m.Metadata.IsNull() {
		metaMap, d := types.MapValueFrom(ctx, types.StringType, map[string]string{})
		diags.Append(d...)
		m.Metadata = metaMap
	} else {
		m.Metadata = types.MapNull(types.StringType)
	}

	// Tags
	if len(lt.Tags) > 0 {
		tagMap, d := types.MapValueFrom(ctx, types.StringType, lt.Tags)
		diags.Append(d...)
		m.Tags = tagMap
	} else if !m.Tags.IsNull() {
		tagMap, d := types.MapValueFrom(ctx, types.StringType, map[string]string{})
		diags.Append(d...)
		m.Tags = tagMap
	} else {
		m.Tags = types.MapNull(types.StringType)
	}

	// user_data is not returned by the API; preserved from state in Read/Update.
}
