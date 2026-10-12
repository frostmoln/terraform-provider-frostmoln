// Package postgres_backup implements the frostmoln_postgres_backup Terraform resource.
package postgres_backup

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/tftags"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/timeouts"
)

// backupTypeBase is the platform's own physical backup, taken for point-in-time
// recovery. It is never customer-creatable (`type` validates to "full"), it
// cannot be deleted, and it is restored by timestamp rather than by id — so
// this resource recognises one only to refuse it clearly.
const backupTypeBase = "base"

// PostgresBackupModel is the Terraform state model for a PostgreSQL backup.
type PostgresBackupModel struct {
	ID          types.String `tfsdk:"id"`
	InstanceID  types.String `tfsdk:"instance_id"`
	Name        types.String `tfsdk:"name"`
	Type        types.String `tfsdk:"type"`
	Status      types.String `tfsdk:"status"`
	SizeBytes   types.Int64  `tfsdk:"size_bytes"`
	StartedAt   types.String `tfsdk:"started_at"`
	CompletedAt types.String `tfsdk:"completed_at"`
	// Tags / TagsAll are the backup's customer resource tags (internal/tftags).
	Tags    types.Map `tfsdk:"tags"`
	TagsAll types.Map `tfsdk:"tags_all"`

	// Timeouts carries the customer-tunable wait budgets; a nil pointer is an
	// absent block, which resolves to the resource's hardcoded defaults.
	Timeouts *timeouts.Model `tfsdk:"timeouts"`
}

// apiPostgresBackup is the API representation of a PostgreSQL backup.
type apiPostgresBackup struct {
	ID          string `json:"id"`
	InstanceID  string `json:"instanceId"`
	Name        string `json:"name"`
	Type        string `json:"type"`
	Status      string `json:"status"`
	SizeBytes   int64  `json:"sizeBytes,omitempty"`
	StartedAt   string `json:"startedAt,omitempty"`
	CompletedAt string `json:"completedAt,omitempty"`
	// Tags are the backup's customer resource tags ({} when untagged).
	Tags map[string]string `json:"tags"`
}

// apiCreatePostgresBackupRequest is the API request to create a PostgreSQL backup.
type apiCreatePostgresBackupRequest struct {
	Name string `json:"name"`
	Type string `json:"type,omitempty"`
	// Tags is default_tags ∪ tags (tftags.ForCreate), omitted when empty; the API
	// merges the tenant's default tags in.
	Tags map[string]string `json:"tags,omitempty"`
}

// toCreateRequest converts the Terraform model to an API create request.
func (m *PostgresBackupModel) toCreateRequest(_ context.Context, _ *diag.Diagnostics) apiCreatePostgresBackupRequest {
	req := apiCreatePostgresBackupRequest{
		Name: m.Name.ValueString(),
	}

	if !m.Type.IsNull() && !m.Type.IsUnknown() {
		req.Type = m.Type.ValueString()
	}

	return req
}

// fromAPI populates the Terraform model from an API response.
func (m *PostgresBackupModel) fromAPI(ctx context.Context, backup *apiPostgresBackup, diags *diag.Diagnostics) {
	// Tags: `tags` keeps the keys the prior value (plan or state) names, `tags_all`
	// every customer key the platform holds.
	m.Tags, m.TagsAll = tftags.ReadBack(ctx, tftags.OfferCustomerTags(backup.Tags), m.Tags, diags)
	m.ID = types.StringValue(backup.ID)
	m.InstanceID = types.StringValue(backup.InstanceID)
	m.Name = types.StringValue(backup.Name)
	m.Status = types.StringValue(backup.Status)

	if backup.Type != "" {
		m.Type = types.StringValue(backup.Type)
	} else {
		m.Type = types.StringNull()
	}

	if backup.SizeBytes > 0 {
		m.SizeBytes = types.Int64Value(backup.SizeBytes)
	} else {
		m.SizeBytes = types.Int64Null()
	}

	if backup.StartedAt != "" {
		m.StartedAt = types.StringValue(backup.StartedAt)
	} else {
		m.StartedAt = types.StringNull()
	}

	if backup.CompletedAt != "" {
		m.CompletedAt = types.StringValue(backup.CompletedAt)
	} else {
		m.CompletedAt = types.StringNull()
	}
}

// apiUpdateTagsRequest is the tags-only PATCH of the backup: `tags` is the only field
// it takes (any other field is refused), a row-only write that starts no work. A
// POINTER so an empty map is sent as {} (clear).
type apiUpdateTagsRequest struct {
	Tags *map[string]string `json:"tags,omitempty"`
}
