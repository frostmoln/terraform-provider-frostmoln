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

// apiVolume is a subset of the volume API response used to verify attachment state.
type apiVolume struct {
	ID         string `json:"id"`
	AttachedTo string `json:"attachedTo,omitempty"`
	DevicePath string `json:"devicePath,omitempty"`
	Status     string `json:"status"`
}

// apiAttachRequest is the API request to attach a volume.
type apiAttachRequest struct {
	InstanceID string `json:"instanceId"`
	DevicePath string `json:"devicePath,omitempty"`
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
		req.DevicePath = m.DevicePath.ValueString()
	}
	return req
}

// fromAPI populates the Terraform model from the volume API response.
func (m *VolumeAttachmentModel) fromAPI(vol *apiVolume) {
	m.ID = types.StringValue(compositeID(vol.ID, vol.AttachedTo))
	m.VolumeID = types.StringValue(vol.ID)
	m.InstanceID = types.StringValue(vol.AttachedTo)
	if vol.DevicePath != "" {
		m.DevicePath = types.StringValue(vol.DevicePath)
	} else if m.DevicePath.IsNull() {
		// Keep null
	} else {
		m.DevicePath = types.StringNull()
	}
}
