// Package snapshot implements the fm_snapshot Terraform resource.
package snapshot

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/reservedmeta"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/tftags"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/timeouts"
)

// SnapshotModel is the Terraform state model for a snapshot.
type SnapshotModel struct {
	ID          types.String `tfsdk:"id"`
	Name        types.String `tfsdk:"name"`
	Description types.String `tfsdk:"description"`
	VolumeID    types.String `tfsdk:"volume_id"`
	Tags        types.Map    `tfsdk:"tags"`
	TagsAll     types.Map    `tfsdk:"tags_all"`
	Status      types.String `tfsdk:"status"`
	SizeGB      types.Int64  `tfsdk:"size_gb"`
	CreatedAt   types.String `tfsdk:"created_at"`

	// Timeouts carries the customer-tunable wait budgets; a nil pointer is an
	// absent block, which resolves to the resource's hardcoded defaults.
	Timeouts *timeouts.Model `tfsdk:"timeouts"`
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

// apiUpdateSnapshotRequest is the in-place update of a snapshot's tags
// (PUT /tenants/{t}/volumes/{v}/snapshots/{s}, served by storage since v1.23.0).
//
// Metadata carries no omitempty. storage acts on it whenever it is non-nil: it
// drops any reserved key from the incoming map, re-stamps the snapshot's
// existing reserved keys (customer-id above all — ownership and metering
// resolve through it), and Cinder applies the result as a REPLACE. So {} clears
// the customer's tags and leaves the platform's in place. Name and description
// could ride along too, but this resource replaces on them, so they are not
// sent.
type apiUpdateSnapshotRequest struct {
	Metadata map[string]string `json:"metadata"`
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
	// Tags are set by Create, which merges the provider's default_tags into
	// them (tftags.ForCreate).

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
	m.Tags, m.TagsAll = tftags.ReadBack(ctx, snap.customerTags(), m.Tags, diags)
}

// customerTags is the tag set the platform holds on the object, as Terraform
// sees it: platform-reserved keys filtered out. The read-back and the fresh
// read an update makes before it writes (tftags.Prior.WithCurrent) both use
// it, so the two cannot disagree about what counts as a tag.
func (a *apiSnapshot) customerTags() map[string]string {
	return reservedmeta.FilterVolume(a.Metadata)
}
