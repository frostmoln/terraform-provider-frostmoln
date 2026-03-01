// Package instance implements the fm_instance Terraform resource.
package instance

import (
	"context"
	"crypto/sha256"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// InstanceModel is the Terraform state model for a compute instance.
type InstanceModel struct {
	ID             types.String `tfsdk:"id"`
	Name           types.String `tfsdk:"name"`
	FlavorID       types.String `tfsdk:"flavor_id"`
	ImageID        types.String `tfsdk:"image_id"`
	Region         types.String `tfsdk:"region"`
	Zone           types.String `tfsdk:"zone"`
	VPCID          types.String `tfsdk:"vpc_id"`
	SubnetID       types.String `tfsdk:"subnet_id"`
	SecurityGroups types.Set    `tfsdk:"security_groups"`
	SSHKeyNames    types.Set    `tfsdk:"ssh_key_names"`
	UserData       types.String `tfsdk:"user_data"`
	UserDataHash   types.String `tfsdk:"user_data_hash"`
	Tags           types.Map    `tfsdk:"tags"`
	Status         types.String `tfsdk:"status"`
	FlavorName     types.String `tfsdk:"flavor_name"`
	ImageName      types.String `tfsdk:"image_name"`
	PrivateIP      types.String `tfsdk:"private_ip"`
	PublicIP       types.String `tfsdk:"public_ip"`
	CreatedAt      types.String `tfsdk:"created_at"`
}

// apiInstance is the API representation of a compute instance.
type apiInstance struct {
	ID             string            `json:"id"`
	Name           string            `json:"name"`
	Status         string            `json:"status"`
	FlavorID       string            `json:"flavorId"`
	FlavorName     string            `json:"flavorName,omitempty"`
	ImageID        string            `json:"imageId"`
	ImageName      string            `json:"imageName,omitempty"`
	Region         string            `json:"region"`
	Zone           string            `json:"zone,omitempty"`
	VPCID          string            `json:"vpcId,omitempty"`
	SubnetID       string            `json:"subnetId,omitempty"`
	PrivateIP      string            `json:"privateIp,omitempty"`
	PublicIP       string            `json:"publicIp,omitempty"`
	SecurityGroups []string          `json:"securityGroups,omitempty"`
	SSHKeyNames    []string          `json:"sshKeyNames,omitempty"`
	Tags           map[string]string `json:"tags,omitempty"`
	CreatedAt      string            `json:"createdAt"`
}

// apiCreateInstanceRequest is the API request to create an instance.
type apiCreateInstanceRequest struct {
	Name           string            `json:"name"`
	FlavorID       string            `json:"flavorId"`
	ImageID        string            `json:"imageId"`
	Region         string            `json:"region,omitempty"`
	Zone           string            `json:"zone,omitempty"`
	VPCID          string            `json:"vpcId,omitempty"`
	SubnetID       string            `json:"subnetId,omitempty"`
	SecurityGroups []string          `json:"securityGroups,omitempty"`
	SSHKeyNames    []string          `json:"sshKeyNames,omitempty"`
	UserData       string            `json:"userData,omitempty"`
	Tags           map[string]string `json:"tags,omitempty"`
}

// apiUpdateInstanceRequest is the API request to update an instance.
type apiUpdateInstanceRequest struct {
	Name           *string           `json:"name,omitempty"`
	SecurityGroups []string          `json:"securityGroups,omitempty"`
	Tags           map[string]string `json:"tags,omitempty"`
}

// apiInstanceActionRequest is the API request to perform an action on an instance.
type apiInstanceActionRequest struct {
	Action   string `json:"action"`
	FlavorID string `json:"flavorId,omitempty"`
}

// computeUserDataHash returns the SHA256 hash of user data.
func computeUserDataHash(userData string) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(userData)))
}

// toCreateRequest converts the Terraform model to an API create request.
func (m *InstanceModel) toCreateRequest(ctx context.Context, diags *diag.Diagnostics) apiCreateInstanceRequest {
	req := apiCreateInstanceRequest{
		Name:     m.Name.ValueString(),
		FlavorID: m.FlavorID.ValueString(),
		ImageID:  m.ImageID.ValueString(),
	}

	if !m.Region.IsNull() && !m.Region.IsUnknown() {
		req.Region = m.Region.ValueString()
	}
	if !m.Zone.IsNull() && !m.Zone.IsUnknown() {
		req.Zone = m.Zone.ValueString()
	}
	if !m.VPCID.IsNull() && !m.VPCID.IsUnknown() {
		req.VPCID = m.VPCID.ValueString()
	}
	if !m.SubnetID.IsNull() && !m.SubnetID.IsUnknown() {
		req.SubnetID = m.SubnetID.ValueString()
	}

	if !m.SecurityGroups.IsNull() && !m.SecurityGroups.IsUnknown() {
		var sgs []string
		diags.Append(m.SecurityGroups.ElementsAs(ctx, &sgs, false)...)
		req.SecurityGroups = sgs
	}

	if !m.SSHKeyNames.IsNull() && !m.SSHKeyNames.IsUnknown() {
		var keys []string
		diags.Append(m.SSHKeyNames.ElementsAs(ctx, &keys, false)...)
		req.SSHKeyNames = keys
	}

	if !m.UserData.IsNull() && !m.UserData.IsUnknown() {
		req.UserData = m.UserData.ValueString()
	}

	if !m.Tags.IsNull() && !m.Tags.IsUnknown() {
		tags := make(map[string]string)
		diags.Append(m.Tags.ElementsAs(ctx, &tags, false)...)
		req.Tags = tags
	}

	return req
}

// toUpdateRequest converts the Terraform model to an API update request.
func (m *InstanceModel) toUpdateRequest(ctx context.Context, diags *diag.Diagnostics) apiUpdateInstanceRequest {
	req := apiUpdateInstanceRequest{}

	if !m.Name.IsNull() && !m.Name.IsUnknown() {
		name := m.Name.ValueString()
		req.Name = &name
	}

	if !m.SecurityGroups.IsNull() && !m.SecurityGroups.IsUnknown() {
		var sgs []string
		diags.Append(m.SecurityGroups.ElementsAs(ctx, &sgs, false)...)
		req.SecurityGroups = sgs
	}

	if !m.Tags.IsNull() && !m.Tags.IsUnknown() {
		tags := make(map[string]string)
		diags.Append(m.Tags.ElementsAs(ctx, &tags, false)...)
		req.Tags = tags
	}

	return req
}

// fromAPI populates the Terraform model from an API response.
// It preserves the user_data field from state since the API does not return it.
func (m *InstanceModel) fromAPI(ctx context.Context, inst *apiInstance, diags *diag.Diagnostics) {
	m.ID = types.StringValue(inst.ID)
	m.Name = types.StringValue(inst.Name)
	m.Status = types.StringValue(inst.Status)
	m.FlavorID = types.StringValue(inst.FlavorID)
	m.ImageID = types.StringValue(inst.ImageID)
	m.Region = types.StringValue(inst.Region)
	m.CreatedAt = types.StringValue(inst.CreatedAt)

	if inst.FlavorName != "" {
		m.FlavorName = types.StringValue(inst.FlavorName)
	} else {
		m.FlavorName = types.StringNull()
	}

	if inst.ImageName != "" {
		m.ImageName = types.StringValue(inst.ImageName)
	} else {
		m.ImageName = types.StringNull()
	}

	if inst.Zone != "" {
		m.Zone = types.StringValue(inst.Zone)
	} else if m.Zone.IsNull() {
		m.Zone = types.StringNull()
	} else {
		m.Zone = types.StringNull()
	}

	if inst.VPCID != "" {
		m.VPCID = types.StringValue(inst.VPCID)
	} else if m.VPCID.IsNull() {
		m.VPCID = types.StringNull()
	} else {
		m.VPCID = types.StringNull()
	}

	if inst.SubnetID != "" {
		m.SubnetID = types.StringValue(inst.SubnetID)
	} else if m.SubnetID.IsNull() {
		m.SubnetID = types.StringNull()
	} else {
		m.SubnetID = types.StringNull()
	}

	if inst.PrivateIP != "" {
		m.PrivateIP = types.StringValue(inst.PrivateIP)
	} else {
		m.PrivateIP = types.StringNull()
	}

	if inst.PublicIP != "" {
		m.PublicIP = types.StringValue(inst.PublicIP)
	} else {
		m.PublicIP = types.StringNull()
	}

	// Security groups
	if len(inst.SecurityGroups) > 0 {
		sgSet, d := types.SetValueFrom(ctx, types.StringType, inst.SecurityGroups)
		diags.Append(d...)
		m.SecurityGroups = sgSet
	} else if !m.SecurityGroups.IsNull() {
		sgSet, d := types.SetValueFrom(ctx, types.StringType, []string{})
		diags.Append(d...)
		m.SecurityGroups = sgSet
	} else {
		m.SecurityGroups = types.SetNull(types.StringType)
	}

	// SSH key names
	if len(inst.SSHKeyNames) > 0 {
		keySet, d := types.SetValueFrom(ctx, types.StringType, inst.SSHKeyNames)
		diags.Append(d...)
		m.SSHKeyNames = keySet
	} else if !m.SSHKeyNames.IsNull() {
		keySet, d := types.SetValueFrom(ctx, types.StringType, []string{})
		diags.Append(d...)
		m.SSHKeyNames = keySet
	} else {
		m.SSHKeyNames = types.SetNull(types.StringType)
	}

	// Tags
	if len(inst.Tags) > 0 {
		tagMap, d := types.MapValueFrom(ctx, types.StringType, inst.Tags)
		diags.Append(d...)
		m.Tags = tagMap
	} else if !m.Tags.IsNull() {
		tagMap, d := types.MapValueFrom(ctx, types.StringType, map[string]string{})
		diags.Append(d...)
		m.Tags = tagMap
	} else {
		m.Tags = types.MapNull(types.StringType)
	}

	// user_data and user_data_hash are NOT set here because the API doesn't return them.
	// They are preserved from the existing state in the Read method.
}
