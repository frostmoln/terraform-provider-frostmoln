// Package snapshot implements the fm_snapshot Terraform resource.
package snapshot

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/reservedmeta"
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

	// description is Optional-only, so a null plan MUST read back null. The snapshot
	// backend currently honors the user description (CreateVolumeSnapshot passes it
	// through), so this is defensive — but preserving null keeps the Optional contract
	// robust if it ever stamps a default like the volume path does (provisioning
	// CreateVolume), which would otherwise trip "inconsistent result after apply".
	if m.Description.IsNull() {
		m.Description = types.StringNull()
	} else if snap.Description != "" {
		m.Description = types.StringValue(snap.Description)
	} else {
		m.Description = types.StringValue("")
	}

	// A Cinder snapshot inherits its source volume's metadata, which the backend
	// stamps with reserved keys (bare *-id + frostmoln_*). They are NOT customer
	// tags — filter them out (same storage set as volumes), otherwise a null/unset
	// tags plan reads back the system keys ("inconsistent result after apply").
	userTags := reservedmeta.FilterVolume(snap.Metadata)
	if len(userTags) > 0 {
		tagMap, d := types.MapValueFrom(ctx, types.StringType, userTags)
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
