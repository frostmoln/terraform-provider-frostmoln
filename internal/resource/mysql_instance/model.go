// Package mysql_instance implements the frostmoln_mysql_instance Terraform resource.
package mysql_instance

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// The backup policy the database service applies itself when backups are enabled without an
// explicit value (ADR-0085: the retention floor equals the 35-day COMPLIANCE object-lock window).
// Mirrors managedbackup.DefaultBackupSchedule / managedbackup.BackupRetentionMinDays in
// servicekit (the shared package both managed offers validate against as of servicekit v1.22.0) --
// if the platform ever moves either, this must move with it. The provider does not import
// servicekit, so this is a deliberate copy, not an oversight.
const (
	defaultBackupSchedule      = "0 2 * * *"
	defaultBackupRetentionDays = 35
)

// MysqlInstanceModel is the Terraform state model for a managed MySQL instance.
type MysqlInstanceModel struct {
	ID                  types.String `tfsdk:"id"`
	Name                types.String `tfsdk:"name"`
	Version             types.String `tfsdk:"version"`
	FlavorID            types.String `tfsdk:"flavor_id"`
	StorageGB           types.Int64  `tfsdk:"storage_gb"`
	VPCID               types.String `tfsdk:"vpc_id"`
	SubnetID            types.String `tfsdk:"subnet_id"`
	HAEnabled           types.Bool   `tfsdk:"ha_enabled"`
	BackupEnabled       types.Bool   `tfsdk:"backup_enabled"`
	BackupSchedule      types.String `tfsdk:"backup_schedule"`
	BackupRetentionDays types.Int64  `tfsdk:"backup_retention_days"`
	ParameterGroupID    types.String `tfsdk:"parameter_group_id"`
	Status              types.String `tfsdk:"status"`
	PrivateIP           types.String `tfsdk:"private_ip"`
	Port                types.Int64  `tfsdk:"port"`
	PublicIP            types.String `tfsdk:"public_ip"`
	AdminUsername       types.String `tfsdk:"admin_username"`
	CreatedAt           types.String `tfsdk:"created_at"`
	UpdatedAt           types.String `tfsdk:"updated_at"`
	TenantID            types.String `tfsdk:"tenant_id"`
}

// apiMysqlInstance is the API representation of a managed MySQL instance.
type apiMysqlInstance struct {
	ID                  string `json:"id"`
	Name                string `json:"name"`
	Engine              string `json:"engine"`
	EngineVersion       string `json:"engineVersion"`
	FlavorID            string `json:"flavorId"`
	StorageGB           int    `json:"storageGb"`
	VPCID               string `json:"vpcId"`
	SubnetID            string `json:"subnetId"`
	HAEnabled           bool   `json:"haEnabled"`
	BackupEnabled       bool   `json:"backupEnabled"`
	BackupSchedule      string `json:"backupSchedule,omitempty"`
	BackupRetentionDays int    `json:"backupRetentionDays,omitempty"`
	ParameterGroupID    string `json:"parameterGroupId,omitempty"`
	Status              string `json:"status"`
	PrivateIP           string `json:"privateIp,omitempty"`
	Port                int    `json:"port,omitempty"`
	PublicIP            string `json:"publicIp,omitempty"`
	AdminUsername       string `json:"adminUsername,omitempty"`
	CreatedAt           string `json:"createdAt"`
	UpdatedAt           string `json:"updatedAt,omitempty"`
	TenantID            string `json:"tenantId,omitempty"`
}

// apiCreateMysqlInstanceRequest is the API request to create a managed MySQL instance.
type apiCreateMysqlInstanceRequest struct {
	Name                string `json:"name"`
	Engine              string `json:"engine"`
	EngineVersion       string `json:"engineVersion"`
	FlavorID            string `json:"flavorId"`
	StorageGB           int    `json:"storageGb"`
	VPCID               string `json:"vpcId"`
	SubnetID            string `json:"subnetId"`
	HAEnabled           *bool  `json:"haEnabled,omitempty"`
	BackupEnabled       *bool  `json:"backupEnabled,omitempty"`
	BackupSchedule      string `json:"backupSchedule,omitempty"`
	BackupRetentionDays *int   `json:"backupRetentionDays,omitempty"`
	ParameterGroupID    string `json:"parameterGroupId,omitempty"`
}

// apiUpdateMysqlInstanceRequest is the API request to update a managed MySQL
// instance via PUT. It carries only in-place-updatable fields: storage_gb goes
// through POST /resize (grow-only) and flavor_id changes are rejected at plan
// time, so neither is sent here (the backend PUT handler has no storage field
// and drops flavor changes silently).
type apiUpdateMysqlInstanceRequest struct {
	Name                *string `json:"name,omitempty"`
	BackupEnabled       *bool   `json:"backupEnabled,omitempty"`
	BackupSchedule      *string `json:"backupSchedule,omitempty"`
	BackupRetentionDays *int    `json:"backupRetentionDays,omitempty"`
	ParameterGroupID    *string `json:"parameterGroupId,omitempty"`
}

// hasChanges reports whether the update request carries any field to PUT.
func (r apiUpdateMysqlInstanceRequest) hasChanges() bool {
	return r.Name != nil || r.BackupEnabled != nil || r.BackupSchedule != nil ||
		r.BackupRetentionDays != nil || r.ParameterGroupID != nil
}

// apiResizeMysqlInstanceRequest is the body for POST /databases/{id}/resize.
// Storage grows online and cannot be shrunk (backend rejects a decrease).
type apiResizeMysqlInstanceRequest struct {
	StorageGB int `json:"storageGb"`
}

// toCreateRequest converts the Terraform model to an API create request.
func (m *MysqlInstanceModel) toCreateRequest(_ context.Context, _ *diag.Diagnostics) apiCreateMysqlInstanceRequest {
	req := apiCreateMysqlInstanceRequest{
		Name:          m.Name.ValueString(),
		Engine:        "mysql",
		EngineVersion: m.Version.ValueString(),
		FlavorID:      m.FlavorID.ValueString(),
		StorageGB:     int(m.StorageGB.ValueInt64()),
		VPCID:         m.VPCID.ValueString(),
		SubnetID:      m.SubnetID.ValueString(),
	}

	if !m.HAEnabled.IsNull() && !m.HAEnabled.IsUnknown() {
		v := m.HAEnabled.ValueBool()
		req.HAEnabled = &v
	}
	if !m.BackupEnabled.IsNull() && !m.BackupEnabled.IsUnknown() {
		v := m.BackupEnabled.ValueBool()
		req.BackupEnabled = &v
	}
	if !m.BackupSchedule.IsNull() && !m.BackupSchedule.IsUnknown() {
		req.BackupSchedule = m.BackupSchedule.ValueString()
	}
	if !m.BackupRetentionDays.IsNull() && !m.BackupRetentionDays.IsUnknown() {
		v := int(m.BackupRetentionDays.ValueInt64())
		req.BackupRetentionDays = &v
	}
	if !m.ParameterGroupID.IsNull() && !m.ParameterGroupID.IsUnknown() {
		req.ParameterGroupID = m.ParameterGroupID.ValueString()
	}

	return req
}

// toUpdateRequest converts the Terraform model to an API update request, comparing with current state.
func (m *MysqlInstanceModel) toUpdateRequest(state *MysqlInstanceModel) apiUpdateMysqlInstanceRequest {
	req := apiUpdateMysqlInstanceRequest{}

	if !m.Name.Equal(state.Name) {
		v := m.Name.ValueString()
		req.Name = &v
	}
	if !m.BackupEnabled.Equal(state.BackupEnabled) {
		v := m.BackupEnabled.ValueBool()
		req.BackupEnabled = &v
	}
	// An enable carries the backup policy explicitly even when neither value changed. Both are
	// pinned to state by the plan modifier, so on an omitted attribute plan == state and the
	// diff-only rule would send nothing -- leaving the server to fill the NULL columns from its
	// own constants. Those happen to equal ours today, but a divergence would surface as an
	// inconsistent-result error on every enable, and a schedule the server does NOT write (the
	// pre-ADR-0085 path) leaves a row that silently takes no backups. Sending the planned values
	// makes the outcome ours to guarantee.
	enabling := m.BackupEnabled.ValueBool() && !state.BackupEnabled.ValueBool()
	if enabling || !m.BackupSchedule.Equal(state.BackupSchedule) {
		v := m.BackupSchedule.ValueString()
		if v != "" {
			req.BackupSchedule = &v
		}
	}
	if enabling || !m.BackupRetentionDays.Equal(state.BackupRetentionDays) {
		v := int(m.BackupRetentionDays.ValueInt64())
		if v > 0 {
			req.BackupRetentionDays = &v
		}
	}
	if !m.ParameterGroupID.Equal(state.ParameterGroupID) {
		v := m.ParameterGroupID.ValueString()
		req.ParameterGroupID = &v
	}

	return req
}

// fromAPI populates the Terraform model from an API response.
func (m *MysqlInstanceModel) fromAPI(_ context.Context, inst *apiMysqlInstance, _ *diag.Diagnostics) {
	m.ID = types.StringValue(inst.ID)
	m.Name = types.StringValue(inst.Name)
	m.Version = types.StringValue(inst.EngineVersion)
	m.FlavorID = types.StringValue(inst.FlavorID)
	m.StorageGB = types.Int64Value(int64(inst.StorageGB))
	m.VPCID = types.StringValue(inst.VPCID)
	m.SubnetID = types.StringValue(inst.SubnetID)
	m.HAEnabled = types.BoolValue(inst.HAEnabled)
	m.BackupEnabled = types.BoolValue(inst.BackupEnabled)
	m.Status = types.StringValue(inst.Status)
	m.CreatedAt = types.StringValue(inst.CreatedAt)

	// Both backup fields are absent from the response whenever the backend column is NULL --
	// every instance created with backups off, since the server applies its defaults only when
	// backup_enabled is true. Reading that back as null would leave the plan carrying null while
	// the apply reads a real value the moment backups are enabled, so the absent value is mapped
	// to what the server itself would apply. That substitution is only sound where the NULL
	// column and the literal are genuinely equivalent, which differs per field:
	//
	//   retention -- equivalent unconditionally. The reaper reads the column as
	//     GREATEST(COALESCE(backup_retention_days, 35), 35) and provisioning's sweep floors a
	//     zero the same way, so NULL already MEANS 35. Sub-floor legacy values (written before
	//     the floor existed; no migration backfilled them) are floored here too -- the server
	//     clamps them on the next update, and reporting the pre-clamp number would mismatch.
	//
	//   schedule -- equivalent ONLY while backups are off. With backups ON, a NULL schedule is
	//     not "the default": the sweep's due-list requires `backup_schedule <> ''`, so such a row
	//     takes no backups at all and appears in no overdue metric. That row is reachable (an
	//     Update carried no defaults-on-enable before the ADR-0085 work, and nothing backfilled
	//     the column). Substituting the default there would report a silently-unbacked-up
	//     instance as healthy, so it is left null and the plan modifier surfaces it as a diff.
	switch {
	case inst.BackupSchedule != "":
		m.BackupSchedule = types.StringValue(inst.BackupSchedule)
	case inst.BackupEnabled:
		m.BackupSchedule = types.StringNull()
	default:
		m.BackupSchedule = types.StringValue(defaultBackupSchedule)
	}

	if inst.BackupRetentionDays > defaultBackupRetentionDays {
		m.BackupRetentionDays = types.Int64Value(int64(inst.BackupRetentionDays))
	} else {
		m.BackupRetentionDays = types.Int64Value(defaultBackupRetentionDays)
	}

	if inst.ParameterGroupID != "" {
		m.ParameterGroupID = types.StringValue(inst.ParameterGroupID)
	} else {
		m.ParameterGroupID = types.StringNull()
	}

	if inst.PrivateIP != "" {
		m.PrivateIP = types.StringValue(inst.PrivateIP)
	} else {
		m.PrivateIP = types.StringNull()
	}

	if inst.Port > 0 {
		m.Port = types.Int64Value(int64(inst.Port))
	} else {
		m.Port = types.Int64Null()
	}

	if inst.PublicIP != "" {
		m.PublicIP = types.StringValue(inst.PublicIP)
	} else {
		m.PublicIP = types.StringNull()
	}

	if inst.AdminUsername != "" {
		m.AdminUsername = types.StringValue(inst.AdminUsername)
	} else {
		m.AdminUsername = types.StringNull()
	}

	if inst.UpdatedAt != "" {
		m.UpdatedAt = types.StringValue(inst.UpdatedAt)
	} else {
		m.UpdatedAt = types.StringNull()
	}

	if inst.TenantID != "" {
		m.TenantID = types.StringValue(inst.TenantID)
	} else {
		m.TenantID = types.StringNull()
	}
}
