// Package postgres_instance implements the frostmoln_postgres_instance Terraform resource.
package postgres_instance

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// PostgresInstanceModel is the Terraform state model for a managed PostgreSQL instance.
type PostgresInstanceModel struct {
	ID                  types.String `tfsdk:"id"`
	Name                types.String `tfsdk:"name"`
	Version             types.String `tfsdk:"version"`
	Flavor              types.String `tfsdk:"flavor"`
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
	FloatingIP          types.String `tfsdk:"floating_ip"`
	AdminUsername       types.String `tfsdk:"admin_username"`
	CreatedAt           types.String `tfsdk:"created_at"`
	UpdatedAt           types.String `tfsdk:"updated_at"`
	TenantID            types.String `tfsdk:"tenant_id"`
}

// apiPostgresInstance is the API representation of a managed PostgreSQL instance.
type apiPostgresInstance struct {
	ID                  string `json:"id"`
	Name                string `json:"name"`
	PostgresVersion     string `json:"postgresVersion"`
	Flavor              string `json:"flavor"`
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
	FloatingIP          string `json:"floatingIp,omitempty"`
	AdminUsername       string `json:"adminUsername,omitempty"`
	CreatedAt           string `json:"createdAt"`
	UpdatedAt           string `json:"updatedAt,omitempty"`
	TenantID            string `json:"tenantId,omitempty"`
}

// apiCreatePostgresInstanceRequest is the API request to create a managed PostgreSQL instance.
type apiCreatePostgresInstanceRequest struct {
	Name                string `json:"name"`
	PostgresVersion     string `json:"postgresVersion"`
	Flavor              string `json:"flavor"`
	StorageGB           int    `json:"storageGb"`
	VPCID               string `json:"vpcId"`
	SubnetID            string `json:"subnetId"`
	HAEnabled           *bool  `json:"haEnabled,omitempty"`
	BackupEnabled       *bool  `json:"backupEnabled,omitempty"`
	BackupSchedule      string `json:"backupSchedule,omitempty"`
	BackupRetentionDays *int   `json:"backupRetentionDays,omitempty"`
	ParameterGroupID    string `json:"parameterGroupId,omitempty"`
}

// apiUpdatePostgresInstanceRequest is the API request to update a managed PostgreSQL instance.
type apiUpdatePostgresInstanceRequest struct {
	Name                *string `json:"name,omitempty"`
	Flavor              *string `json:"flavor,omitempty"`
	StorageGB           *int    `json:"storageGb,omitempty"`
	BackupEnabled       *bool   `json:"backupEnabled,omitempty"`
	BackupSchedule      *string `json:"backupSchedule,omitempty"`
	BackupRetentionDays *int    `json:"backupRetentionDays,omitempty"`
	ParameterGroupID    *string `json:"parameterGroupId,omitempty"`
}

// toCreateRequest converts the Terraform model to an API create request.
func (m *PostgresInstanceModel) toCreateRequest(_ context.Context, _ *diag.Diagnostics) apiCreatePostgresInstanceRequest {
	req := apiCreatePostgresInstanceRequest{
		Name:            m.Name.ValueString(),
		PostgresVersion: m.Version.ValueString(),
		Flavor:          m.Flavor.ValueString(),
		StorageGB:       int(m.StorageGB.ValueInt64()),
		VPCID:           m.VPCID.ValueString(),
		SubnetID:        m.SubnetID.ValueString(),
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
func (m *PostgresInstanceModel) toUpdateRequest(state *PostgresInstanceModel) apiUpdatePostgresInstanceRequest {
	req := apiUpdatePostgresInstanceRequest{}

	if !m.Name.Equal(state.Name) {
		v := m.Name.ValueString()
		req.Name = &v
	}
	if !m.Flavor.Equal(state.Flavor) {
		v := m.Flavor.ValueString()
		req.Flavor = &v
	}
	if !m.StorageGB.Equal(state.StorageGB) {
		v := int(m.StorageGB.ValueInt64())
		req.StorageGB = &v
	}
	if !m.BackupEnabled.Equal(state.BackupEnabled) {
		v := m.BackupEnabled.ValueBool()
		req.BackupEnabled = &v
	}
	if !m.BackupSchedule.Equal(state.BackupSchedule) {
		v := m.BackupSchedule.ValueString()
		req.BackupSchedule = &v
	}
	if !m.BackupRetentionDays.Equal(state.BackupRetentionDays) {
		v := int(m.BackupRetentionDays.ValueInt64())
		req.BackupRetentionDays = &v
	}
	if !m.ParameterGroupID.Equal(state.ParameterGroupID) {
		v := m.ParameterGroupID.ValueString()
		req.ParameterGroupID = &v
	}

	return req
}

// fromAPI populates the Terraform model from an API response.
func (m *PostgresInstanceModel) fromAPI(_ context.Context, inst *apiPostgresInstance, _ *diag.Diagnostics) {
	m.ID = types.StringValue(inst.ID)
	m.Name = types.StringValue(inst.Name)
	m.Version = types.StringValue(inst.PostgresVersion)
	m.Flavor = types.StringValue(inst.Flavor)
	m.StorageGB = types.Int64Value(int64(inst.StorageGB))
	m.VPCID = types.StringValue(inst.VPCID)
	m.SubnetID = types.StringValue(inst.SubnetID)
	m.HAEnabled = types.BoolValue(inst.HAEnabled)
	m.BackupEnabled = types.BoolValue(inst.BackupEnabled)
	m.Status = types.StringValue(inst.Status)
	m.CreatedAt = types.StringValue(inst.CreatedAt)

	if inst.BackupSchedule != "" {
		m.BackupSchedule = types.StringValue(inst.BackupSchedule)
	} else {
		m.BackupSchedule = types.StringNull()
	}

	if inst.BackupRetentionDays > 0 {
		m.BackupRetentionDays = types.Int64Value(int64(inst.BackupRetentionDays))
	} else {
		m.BackupRetentionDays = types.Int64Null()
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

	if inst.FloatingIP != "" {
		m.FloatingIP = types.StringValue(inst.FloatingIP)
	} else {
		m.FloatingIP = types.StringNull()
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
