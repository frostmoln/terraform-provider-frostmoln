// Package volume_attachment implements the fm_volume_attachment Terraform resource.
package volume_attachment

import (
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// VolumeAttachmentModel is the Terraform state model for a volume attachment.
type VolumeAttachmentModel struct {
	ID         types.String `tfsdk:"id"`
	VolumeID   types.String `tfsdk:"volume_id"`
	InstanceID types.String `tfsdk:"instance_id"`
	DevicePath types.String `tfsdk:"device_path"`
}

// apiVolumeAttachment is one element of the volume's attachments[] array
// (storage/internal/domain/volume.go VolumeAttachment).
type apiVolumeAttachment struct {
	ID         string `json:"id"`
	VolumeID   string `json:"volumeId"`
	InstanceID string `json:"instanceId"`
	Device     string `json:"device"`
}

// apiVolume is a subset of the volume API response used to verify attachment
// state. The backend exposes attachments only via the attachments[] array;
// there are no top-level attachedTo/devicePath scalars.
type apiVolume struct {
	ID          string                `json:"id"`
	Status      string                `json:"status"`
	Attachments []apiVolumeAttachment `json:"attachments,omitempty"`
}

// findAttachment returns the attachment for the given instance, or nil.
func (v *apiVolume) findAttachment(instanceID string) *apiVolumeAttachment {
	for i := range v.Attachments {
		if v.Attachments[i].InstanceID == instanceID {
			return &v.Attachments[i]
		}
	}
	return nil
}

// apiAttachRequest is the API request to attach a volume. The storage attach
// handler expects `device` (not devicePath).
type apiAttachRequest struct {
	InstanceID string `json:"instanceId"`
	Device     string `json:"device,omitempty"`
}

// apiDetachRequest is the API request to detach a volume.
type apiDetachRequest struct {
	Force bool `json:"force"`
}

// compositeID builds the composite resource ID from volume and instance IDs.
func compositeID(volumeID, instanceID string) string {
	return volumeID + "/" + instanceID
}

// toAttachRequest converts the Terraform model to an API attach request.
func (m *VolumeAttachmentModel) toAttachRequest() apiAttachRequest {
	req := apiAttachRequest{
		InstanceID: m.InstanceID.ValueString(),
	}
	if !m.DevicePath.IsNull() && !m.DevicePath.IsUnknown() {
		req.Device = m.DevicePath.ValueString()
	}
	return req
}

// fromAttachment populates the Terraform model from a matched attachment.
func (m *VolumeAttachmentModel) fromAttachment(volumeID string, att *apiVolumeAttachment) {
	m.ID = types.StringValue(compositeID(volumeID, att.InstanceID))
	m.VolumeID = types.StringValue(volumeID)
	m.InstanceID = types.StringValue(att.InstanceID)
	if att.Device != "" {
		m.DevicePath = types.StringValue(att.Device)
	} else if m.DevicePath.IsNull() {
		// Keep null
	} else {
		m.DevicePath = types.StringNull()
	}
}
