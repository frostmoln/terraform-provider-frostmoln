// Package postgres_backup implements the frostmoln_postgres_backup Terraform resource.
package postgres_backup

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

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
}

// apiCreatePostgresBackupRequest is the API request to create a PostgreSQL backup.
type apiCreatePostgresBackupRequest struct {
	Name string `json:"name"`
	Type string `json:"type,omitempty"`
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
func (m *PostgresBackupModel) fromAPI(_ context.Context, backup *apiPostgresBackup, _ *diag.Diagnostics) {
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
