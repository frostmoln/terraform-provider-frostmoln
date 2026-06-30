// Package volume implements the fm_volume Terraform resource.
package volume

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/reservedmeta"
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
	AttachedTo  types.String `tfsdk:"attached_to"`
	DevicePath  types.String `tfsdk:"device_path"`
	CreatedAt   types.String `tfsdk:"created_at"`
}

// apiVolumeAttachment is one element of the volume's attachments[] array.
type apiVolumeAttachment struct {
	ID         string `json:"id"`
	VolumeID   string `json:"volumeId"`
	InstanceID string `json:"instanceId"`
	Device     string `json:"device"`
}

// apiVolume is the API representation of a volume. Field names match what the
// storage service serializes (see storage/internal/domain/volume.go): the size
// is `size` (int64 GB), user tags live under `metadata`, the snapshot source is
// `sourceSnapshotId`, attachment state is the `attachments[]` array, and there
// is no top-level region/attachedTo/devicePath.
type apiVolume struct {
	ID               string                `json:"id"`
	Name             string                `json:"name"`
	Description      string                `json:"description,omitempty"`
	Size             int64                 `json:"size"`
	VolumeType       string                `json:"volumeType"`
	Zone             string                `json:"availabilityZone,omitempty"`
	SourceSnapshotID string                `json:"sourceSnapshotId,omitempty"`
	Encrypted        bool                  `json:"encrypted"`
	Status           string                `json:"status"`
	IOPS             int                   `json:"iops,omitempty"`
	Throughput       int                   `json:"throughput,omitempty"`
	Attachments      []apiVolumeAttachment `json:"attachments,omitempty"`
	Metadata         map[string]string     `json:"metadata,omitempty"`
	CreatedAt        string                `json:"createdAt"`
}

// apiCreateVolumeRequest is the API request to create a volume.
type apiCreateVolumeRequest struct {
	Name             string            `json:"name"`
	Description      string            `json:"description,omitempty"`
	Size             int64             `json:"size"`
	VolumeType       string            `json:"volumeType,omitempty"`
	Zone             string            `json:"availabilityZone,omitempty"`
	SourceSnapshotID string            `json:"sourceSnapshotId,omitempty"`
	Encrypted        bool              `json:"encrypted"`
	Metadata         map[string]string `json:"metadata,omitempty"`
}

// apiUpdateVolumeRequest is the API request to update a volume.
type apiUpdateVolumeRequest struct {
	Name        *string           `json:"name,omitempty"`
	Description *string           `json:"description,omitempty"`
	Metadata    map[string]string `json:"metadata,omitempty"`
}

// apiResizeVolumeRequest is the API request to resize a volume.
type apiResizeVolumeRequest struct {
	NewSizeGB int `json:"newSizeGb"`
}

// toCreateRequest converts the Terraform model to an API create request.
func (m *VolumeModel) toCreateRequest(ctx context.Context, diags *diag.Diagnostics) apiCreateVolumeRequest {
	req := apiCreateVolumeRequest{
		Name:      m.Name.ValueString(),
		Size:      m.SizeGB.ValueInt64(),
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
		req.SourceSnapshotID = m.SnapshotID.ValueString()
	}
	if !m.Tags.IsNull() && !m.Tags.IsUnknown() {
		tags := make(map[string]string)
		diags.Append(m.Tags.ElementsAs(ctx, &tags, false)...)
		req.Metadata = tags
	}

	return req
}

// fromAPI populates the Terraform model from an API response.
func (m *VolumeModel) fromAPI(ctx context.Context, vol *apiVolume, diags *diag.Diagnostics) {
	m.ID = types.StringValue(vol.ID)
	m.Name = types.StringValue(vol.Name)
	m.SizeGB = types.Int64Value(vol.Size)
	m.VolumeType = types.StringValue(vol.VolumeType)
	m.Encrypted = types.BoolValue(vol.Encrypted)
	m.Status = types.StringValue(vol.Status)
	m.IOPS = types.Int64Value(int64(vol.IOPS))
	m.Throughput = types.Int64Value(int64(vol.Throughput))
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

	if vol.SourceSnapshotID != "" {
		m.SnapshotID = types.StringValue(vol.SourceSnapshotID)
	} else {
		m.SnapshotID = types.StringNull()
	}

	// attached_to / device_path are derived from the first attachment (the
	// backend exposes attachment state only via the attachments[] array).
	if len(vol.Attachments) > 0 {
		m.AttachedTo = types.StringValue(vol.Attachments[0].InstanceID)
		if vol.Attachments[0].Device != "" {
			m.DevicePath = types.StringValue(vol.Attachments[0].Device)
		} else {
			m.DevicePath = types.StringNull()
		}
	} else {
		m.AttachedTo = types.StringNull()
		m.DevicePath = types.StringNull()
	}

	// Backend stamps reserved metadata (bare request-id/customer-id/project-id +
	// the frostmoln_* namespace) onto every volume at create; the customer GET
	// returns it unfiltered. It is NOT a customer tag — filter it out, otherwise
	// a null/unset tags plan is overwritten on read-back ("inconsistent result
	// after apply"). Shared with the instance filter via reservedmeta.
	userTags := reservedmeta.FilterVolume(vol.Metadata)
	if len(userTags) > 0 {
		tagMap, d := types.MapValueFrom(ctx, types.StringType, userTags)
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
