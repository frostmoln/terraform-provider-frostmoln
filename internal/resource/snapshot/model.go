// Package snapshot implements the fm_snapshot Terraform resource.
package snapshot

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// SnapshotModel is the Terraform state model for a snapshot.
type SnapshotModel struct {
	ID          types.String `tfsdk:"id"`
	Name        types.String `tfsdk:"name"`
	Description types.String `tfsdk:"description"`
	VolumeID    types.String `tfsdk:"volume_id"`
	Tags        types.Map    `tfsdk:"tags"`
	Status      types.String `tfsdk:"status"`
	SizeGB      types.Int64  `tfsdk:"size_gb"`
	CreatedAt   types.String `tfsdk:"created_at"`
}

// apiSnapshot is the API representation of a snapshot. Field names match the
// storage service (storage/internal/domain/snapshot.go): size is `size` (int64),
// user tags live under `metadata`, and there is no region.
type apiSnapshot struct {
	ID          string            `json:"id"`
	Name        string            `json:"name"`
	Description string            `json:"description,omitempty"`
	VolumeID    string            `json:"volumeId"`
	Status      string            `json:"status"`
	Size        int64             `json:"size"`
	Metadata    map[string]string `json:"metadata,omitempty"`
	CreatedAt   string            `json:"createdAt"`
}

// apiCreateSnapshotRequest is the API request to create a snapshot. User tags
// are sent under `metadata` (the create handler reads CreateSnapshotRequest.Metadata).
type apiCreateSnapshotRequest struct {
	Name        string            `json:"name"`
	Description string            `json:"description,omitempty"`
	VolumeID    string            `json:"volumeId"`
	Metadata    map[string]string `json:"metadata,omitempty"`
}

// toCreateRequest converts the Terraform model to an API create request.
func (m *SnapshotModel) toCreateRequest(ctx context.Context, diags *diag.Diagnostics) apiCreateSnapshotRequest {
	req := apiCreateSnapshotRequest{
		Name:     m.Name.ValueString(),
		VolumeID: m.VolumeID.ValueString(),
	}

	if !m.Description.IsNull() && !m.Description.IsUnknown() {
		req.Description = m.Description.ValueString()
	}
	if !m.Tags.IsNull() && !m.Tags.IsUnknown() {
		tags := make(map[string]string)
		diags.Append(m.Tags.ElementsAs(ctx, &tags, false)...)
		req.Metadata = tags
	}

	return req
}

// fromAPI populates the Terraform model from an API response.
func (m *SnapshotModel) fromAPI(ctx context.Context, snap *apiSnapshot, diags *diag.Diagnostics) {
	m.ID = types.StringValue(snap.ID)
	m.Name = types.StringValue(snap.Name)
	m.VolumeID = types.StringValue(snap.VolumeID)
	m.Status = types.StringValue(snap.Status)
	m.SizeGB = types.Int64Value(snap.Size)
	m.CreatedAt = types.StringValue(snap.CreatedAt)

	if snap.Description != "" {
		m.Description = types.StringValue(snap.Description)
	} else if m.Description.IsNull() {
		// Keep null
	} else {
		m.Description = types.StringValue("")
	}

	if len(snap.Metadata) > 0 {
		tagMap, d := types.MapValueFrom(ctx, types.StringType, snap.Metadata)
		diags.Append(d...)
		m.Tags = tagMap
	} else if !m.Tags.IsNull() {
		tagMap, d := types.MapValueFrom(ctx, types.StringType, map[string]string{})
		diags.Append(d...)
		m.Tags = tagMap
	} else {
		m.Tags = types.MapNull(types.StringType)
	}
}
