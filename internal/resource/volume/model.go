// Package volume implements the fm_volume Terraform resource.
package volume

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// VolumeModel is the Terraform state model for a volume.
type VolumeModel struct {
	ID          types.String `tfsdk:"id"`
	Name        types.String `tfsdk:"name"`
	Description types.String `tfsdk:"description"`
	SizeGB      types.Int64  `tfsdk:"size_gb"`
	VolumeType  types.String `tfsdk:"volume_type"`
	Zone        types.String `tfsdk:"zone"`
	SnapshotID  types.String `tfsdk:"snapshot_id"`
	Encrypted   types.Bool   `tfsdk:"encrypted"`
	Tags        types.Map    `tfsdk:"tags"`
	Status      types.String `tfsdk:"status"`
	IOPS        types.Int64  `tfsdk:"iops"`
	Throughput  types.Int64  `tfsdk:"throughput"`
	Region      types.String `tfsdk:"region"`
	AttachedTo  types.String `tfsdk:"attached_to"`
	DevicePath  types.String `tfsdk:"device_path"`
	CreatedAt   types.String `tfsdk:"created_at"`
}

// apiVolume is the API representation of a volume.
type apiVolume struct {
	ID          string            `json:"id"`
	Name        string            `json:"name"`
	Description string            `json:"description,omitempty"`
	SizeGB      int               `json:"sizeGb"`
	VolumeType  string            `json:"volumeType"`
	Zone        string            `json:"availabilityZone,omitempty"`
	SnapshotID  string            `json:"snapshotId,omitempty"`
	Encrypted   bool              `json:"encrypted"`
	Status      string            `json:"status"`
	IOPS        int               `json:"iops"`
	Throughput  int               `json:"throughput"`
	Region      string            `json:"region"`
	AttachedTo  string            `json:"attachedTo,omitempty"`
	DevicePath  string            `json:"devicePath,omitempty"`
	Tags        map[string]string `json:"tags,omitempty"`
	CreatedAt   string            `json:"createdAt"`
}

// apiCreateVolumeRequest is the API request to create a volume.
type apiCreateVolumeRequest struct {
	Name        string            `json:"name"`
	Description string            `json:"description,omitempty"`
	SizeGB      int               `json:"sizeGb"`
	VolumeType  string            `json:"volumeType,omitempty"`
	Zone        string            `json:"availabilityZone,omitempty"`
	SnapshotID  string            `json:"snapshotId,omitempty"`
	Encrypted   bool              `json:"encrypted"`
	Tags        map[string]string `json:"tags,omitempty"`
}

// apiUpdateVolumeRequest is the API request to update a volume.
type apiUpdateVolumeRequest struct {
	Name        *string           `json:"name,omitempty"`
	Description *string           `json:"description,omitempty"`
	Tags        map[string]string `json:"tags,omitempty"`
}

// apiResizeVolumeRequest is the API request to resize a volume.
type apiResizeVolumeRequest struct {
	SizeGB int `json:"sizeGb"`
}

// toCreateRequest converts the Terraform model to an API create request.
func (m *VolumeModel) toCreateRequest(ctx context.Context, diags *diag.Diagnostics) apiCreateVolumeRequest {
	req := apiCreateVolumeRequest{
		Name:      m.Name.ValueString(),
		SizeGB:    int(m.SizeGB.ValueInt64()),
		Encrypted: m.Encrypted.ValueBool(),
	}

	if !m.Description.IsNull() && !m.Description.IsUnknown() {
		req.Description = m.Description.ValueString()
	}
	if !m.VolumeType.IsNull() && !m.VolumeType.IsUnknown() {
		req.VolumeType = m.VolumeType.ValueString()
	}
	if !m.Zone.IsNull() && !m.Zone.IsUnknown() {
		req.Zone = m.Zone.ValueString()
	}
	if !m.SnapshotID.IsNull() && !m.SnapshotID.IsUnknown() {
		req.SnapshotID = m.SnapshotID.ValueString()
	}
	if !m.Tags.IsNull() && !m.Tags.IsUnknown() {
		tags := make(map[string]string)
		diags.Append(m.Tags.ElementsAs(ctx, &tags, false)...)
		req.Tags = tags
	}

	return req
}

// fromAPI populates the Terraform model from an API response.
func (m *VolumeModel) fromAPI(ctx context.Context, vol *apiVolume, diags *diag.Diagnostics) {
	m.ID = types.StringValue(vol.ID)
	m.Name = types.StringValue(vol.Name)
	m.SizeGB = types.Int64Value(int64(vol.SizeGB))
	m.VolumeType = types.StringValue(vol.VolumeType)
	m.Encrypted = types.BoolValue(vol.Encrypted)
	m.Status = types.StringValue(vol.Status)
	m.IOPS = types.Int64Value(int64(vol.IOPS))
	m.Throughput = types.Int64Value(int64(vol.Throughput))
	m.Region = types.StringValue(vol.Region)
	m.CreatedAt = types.StringValue(vol.CreatedAt)

	if vol.Description != "" {
		m.Description = types.StringValue(vol.Description)
	} else if m.Description.IsNull() {
		// Keep null if it was null before
	} else {
		m.Description = types.StringValue("")
	}

	if vol.Zone != "" {
		m.Zone = types.StringValue(vol.Zone)
	} else {
		m.Zone = types.StringNull()
	}

	if vol.SnapshotID != "" {
		m.SnapshotID = types.StringValue(vol.SnapshotID)
	} else {
		m.SnapshotID = types.StringNull()
	}

	if vol.AttachedTo != "" {
		m.AttachedTo = types.StringValue(vol.AttachedTo)
	} else {
		m.AttachedTo = types.StringNull()
	}

	if vol.DevicePath != "" {
		m.DevicePath = types.StringValue(vol.DevicePath)
	} else {
		m.DevicePath = types.StringNull()
	}

	if len(vol.Tags) > 0 {
		tagMap, d := types.MapValueFrom(ctx, types.StringType, vol.Tags)
		diags.Append(d...)
		m.Tags = tagMap
	} else if !m.Tags.IsNull() {
		// If the model had tags set but API returned none, set empty map
		tagMap, d := types.MapValueFrom(ctx, types.StringType, map[string]string{})
		diags.Append(d...)
		m.Tags = tagMap
	} else {
		m.Tags = types.MapNull(types.StringType)
	}
}
