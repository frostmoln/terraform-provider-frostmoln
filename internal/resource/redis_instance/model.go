// Package redis_instance implements the frostmoln_redis_instance Terraform resource.
package redis_instance

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// RedisInstanceModel is the Terraform state model for a managed Redis instance.
type RedisInstanceModel struct {
	ID              types.String `tfsdk:"id"`
	Name            types.String `tfsdk:"name"`
	Version         types.String `tfsdk:"version"`
	FlavorID        types.String `tfsdk:"flavor_id"`
	StorageGB       types.Int64  `tfsdk:"storage_gb"`
	VPCID           types.String `tfsdk:"vpc_id"`
	SubnetID        types.String `tfsdk:"subnet_id"`
	PersistenceMode types.String `tfsdk:"persistence_mode"`
	EvictionPolicy  types.String `tfsdk:"eviction_policy"`

	BackupEnabled       types.Bool   `tfsdk:"backup_enabled"`
	BackupSchedule      types.String `tfsdk:"backup_schedule"`
	BackupRetentionDays types.Int64  `tfsdk:"backup_retention_days"`

	Status        types.String `tfsdk:"status"`
	PrivateIP     types.String `tfsdk:"private_ip"`
	Port          types.Int64  `tfsdk:"port"`
	AdminUsername types.String `tfsdk:"admin_username"`
	CreatedAt     types.String `tfsdk:"created_at"`
	UpdatedAt     types.String `tfsdk:"updated_at"`
}

// apiRedisInstance is the API representation of a managed Redis instance.
type apiRedisInstance struct {
	ID              string `json:"id"`
	Name            string `json:"name"`
	EngineVersion   string `json:"engineVersion"`
	FlavorID        string `json:"flavorId"`
	StorageGB       int    `json:"storageGb"`
	VPCID           string `json:"vpcId"`
	SubnetID        string `json:"subnetId"`
	PersistenceMode string `json:"persistenceMode"`
	EvictionPolicy  string `json:"evictionPolicy"`

	BackupEnabled       bool   `json:"backupEnabled"`
	BackupSchedule      string `json:"backupSchedule,omitempty"`
	BackupRetentionDays int    `json:"backupRetentionDays,omitempty"`

	Status        string `json:"status"`
	PrivateIP     string `json:"privateIp,omitempty"`
	Port          int    `json:"port,omitempty"`
	AdminUsername string `json:"adminUsername,omitempty"`
	CreatedAt     string `json:"createdAt"`
	UpdatedAt     string `json:"updatedAt,omitempty"`
}

// apiCreateRedisInstanceRequest is the API request to create a managed Redis instance.
type apiCreateRedisInstanceRequest struct {
	Name            string `json:"name"`
	Engine          string `json:"engine"`
	EngineVersion   string `json:"engineVersion"`
	FlavorID        string `json:"flavorId"`
	StorageGB       int    `json:"storageGb,omitempty"`
	VPCID           string `json:"vpcId"`
	SubnetID        string `json:"subnetId"`
	PersistenceMode string `json:"persistenceMode,omitempty"`
	EvictionPolicy  string `json:"evictionPolicy,omitempty"`

	BackupEnabled       *bool  `json:"backupEnabled,omitempty"`
	BackupSchedule      string `json:"backupSchedule,omitempty"`
	BackupRetentionDays *int   `json:"backupRetentionDays,omitempty"`
}

// apiUpdateRedisInstanceRequest is the API request to update a managed Redis instance
// (PUT /caches/{id}). The backend accepts only these in-place fields — NOT flavor or storage:
// both flavor changes and storage grows go via POST /caches/{id}/resize.
type apiUpdateRedisInstanceRequest struct {
	Name            *string `json:"name,omitempty"`
	PersistenceMode *string `json:"persistenceMode,omitempty"`
	EvictionPolicy  *string `json:"evictionPolicy,omitempty"`

	BackupEnabled       *bool   `json:"backupEnabled,omitempty"`
	BackupSchedule      *string `json:"backupSchedule,omitempty"`
	BackupRetentionDays *int    `json:"backupRetentionDays,omitempty"`
}

// hasChanges reports whether any in-place-updatable field is set, so Update can skip an empty PUT
// when only storage changed (which routes through /resize instead).
func (r apiUpdateRedisInstanceRequest) hasChanges() bool {
	return r.Name != nil || r.PersistenceMode != nil || r.EvictionPolicy != nil ||
		r.BackupEnabled != nil || r.BackupSchedule != nil || r.BackupRetentionDays != nil
}

// apiResizeRedisInstanceRequest is the API request to resize a managed Redis instance
// (POST /caches/{id}/resize): a storage grow (StorageGB) OR a flavor change (FlavorID), never both
// in one request (the backend rejects both). Storage is grow-only (the provider rejects a shrink
// client-side); a flavor change is a Nova resize that RESTARTS the VM.
type apiResizeRedisInstanceRequest struct {
	StorageGB int    `json:"storageGb,omitempty"`
	FlavorID  string `json:"flavorId,omitempty"`
}

// toCreateRequest converts the Terraform model to an API create request.
func (m *RedisInstanceModel) toCreateRequest(_ context.Context, _ *diag.Diagnostics) apiCreateRedisInstanceRequest {
	req := apiCreateRedisInstanceRequest{
		Name:          m.Name.ValueString(),
		Engine:        "redis",
		EngineVersion: m.Version.ValueString(),
		FlavorID:      m.FlavorID.ValueString(),
		VPCID:         m.VPCID.ValueString(),
		SubnetID:      m.SubnetID.ValueString(),
	}

	if !m.StorageGB.IsNull() && !m.StorageGB.IsUnknown() {
		req.StorageGB = int(m.StorageGB.ValueInt64())
	}
	if !m.PersistenceMode.IsNull() && !m.PersistenceMode.IsUnknown() {
		req.PersistenceMode = m.PersistenceMode.ValueString()
	}
	if !m.EvictionPolicy.IsNull() && !m.EvictionPolicy.IsUnknown() {
		req.EvictionPolicy = m.EvictionPolicy.ValueString()
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

	return req
}

// toUpdateRequest converts the Terraform model to an API update request, comparing with current state.
func (m *RedisInstanceModel) toUpdateRequest(state *RedisInstanceModel) apiUpdateRedisInstanceRequest {
	req := apiUpdateRedisInstanceRequest{}

	if !m.Name.Equal(state.Name) {
		v := m.Name.ValueString()
		req.Name = &v
	}
	if !m.PersistenceMode.Equal(state.PersistenceMode) {
		v := m.PersistenceMode.ValueString()
		req.PersistenceMode = &v
	}
	if !m.EvictionPolicy.Equal(state.EvictionPolicy) {
		v := m.EvictionPolicy.ValueString()
		req.EvictionPolicy = &v
	}
	// Guard on !IsUnknown so a still-unresolved Computed plan value is never serialized as
	// &"" / &0 (ValueString/ValueInt64 return the zero value for unknown), which would fire a
	// spurious no-op PUT — mirrors the toCreateRequest guards.
	if !m.BackupEnabled.IsUnknown() && !m.BackupEnabled.Equal(state.BackupEnabled) {
		v := m.BackupEnabled.ValueBool()
		req.BackupEnabled = &v
	}
	if !m.BackupSchedule.IsUnknown() && !m.BackupSchedule.Equal(state.BackupSchedule) {
		v := m.BackupSchedule.ValueString()
		req.BackupSchedule = &v
	}
	if !m.BackupRetentionDays.IsUnknown() && !m.BackupRetentionDays.Equal(state.BackupRetentionDays) {
		v := int(m.BackupRetentionDays.ValueInt64())
		req.BackupRetentionDays = &v
	}

	return req
}

// fromAPI populates the Terraform model from an API response.
func (m *RedisInstanceModel) fromAPI(_ context.Context, inst *apiRedisInstance, _ *diag.Diagnostics) {
	m.ID = types.StringValue(inst.ID)
	m.Name = types.StringValue(inst.Name)
	m.Version = types.StringValue(inst.EngineVersion)
	m.FlavorID = types.StringValue(inst.FlavorID)
	m.StorageGB = types.Int64Value(int64(inst.StorageGB))
	m.VPCID = types.StringValue(inst.VPCID)
	m.SubnetID = types.StringValue(inst.SubnetID)
	m.PersistenceMode = types.StringValue(inst.PersistenceMode)
	m.EvictionPolicy = types.StringValue(inst.EvictionPolicy)
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
}
