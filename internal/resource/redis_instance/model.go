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
	EngineVersion   types.String `tfsdk:"engine_version"`
	FlavorID        types.String `tfsdk:"flavor_id"`
	VPCID           types.String `tfsdk:"vpc_id"`
	SubnetID        types.String `tfsdk:"subnet_id"`
	PersistenceMode types.String `tfsdk:"persistence_mode"`
	EvictionPolicy  types.String `tfsdk:"eviction_policy"`
	Status          types.String `tfsdk:"status"`
	PrivateIP       types.String `tfsdk:"private_ip"`
	Port            types.Int64  `tfsdk:"port"`
	AdminUsername   types.String `tfsdk:"admin_username"`
	CreatedAt       types.String `tfsdk:"created_at"`
	UpdatedAt       types.String `tfsdk:"updated_at"`
}

// apiRedisInstance is the API representation of a managed Redis instance.
type apiRedisInstance struct {
	ID              string `json:"id"`
	Name            string `json:"name"`
	EngineVersion   string `json:"engineVersion"`
	FlavorID        string `json:"flavorId"`
	VPCID           string `json:"vpcId"`
	SubnetID        string `json:"subnetId"`
	PersistenceMode string `json:"persistenceMode"`
	EvictionPolicy  string `json:"evictionPolicy"`
	Status          string `json:"status"`
	PrivateIP       string `json:"privateIp,omitempty"`
	Port            int    `json:"port,omitempty"`
	AdminUsername   string `json:"adminUsername,omitempty"`
	CreatedAt       string `json:"createdAt"`
	UpdatedAt       string `json:"updatedAt,omitempty"`
}

// apiCreateRedisInstanceRequest is the API request to create a managed Redis instance.
type apiCreateRedisInstanceRequest struct {
	Name            string `json:"name"`
	EngineVersion   string `json:"engineVersion"`
	FlavorID        string `json:"flavorId"`
	VPCID           string `json:"vpcId"`
	SubnetID        string `json:"subnetId"`
	PersistenceMode string `json:"persistenceMode,omitempty"`
	EvictionPolicy  string `json:"evictionPolicy,omitempty"`
}

// apiUpdateRedisInstanceRequest is the API request to update a managed Redis instance.
type apiUpdateRedisInstanceRequest struct {
	Name            *string `json:"name,omitempty"`
	FlavorID        *string `json:"flavorId,omitempty"`
	PersistenceMode *string `json:"persistenceMode,omitempty"`
	EvictionPolicy  *string `json:"evictionPolicy,omitempty"`
}

// toCreateRequest converts the Terraform model to an API create request.
func (m *RedisInstanceModel) toCreateRequest(_ context.Context, _ *diag.Diagnostics) apiCreateRedisInstanceRequest {
	req := apiCreateRedisInstanceRequest{
		Name:          m.Name.ValueString(),
		EngineVersion: m.EngineVersion.ValueString(),
		FlavorID:      m.FlavorID.ValueString(),
		VPCID:         m.VPCID.ValueString(),
		SubnetID:      m.SubnetID.ValueString(),
	}

	if !m.PersistenceMode.IsNull() && !m.PersistenceMode.IsUnknown() {
		req.PersistenceMode = m.PersistenceMode.ValueString()
	}
	if !m.EvictionPolicy.IsNull() && !m.EvictionPolicy.IsUnknown() {
		req.EvictionPolicy = m.EvictionPolicy.ValueString()
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
	if !m.FlavorID.Equal(state.FlavorID) {
		v := m.FlavorID.ValueString()
		req.FlavorID = &v
	}
	if !m.PersistenceMode.Equal(state.PersistenceMode) {
		v := m.PersistenceMode.ValueString()
		req.PersistenceMode = &v
	}
	if !m.EvictionPolicy.Equal(state.EvictionPolicy) {
		v := m.EvictionPolicy.ValueString()
		req.EvictionPolicy = &v
	}

	return req
}

// fromAPI populates the Terraform model from an API response.
func (m *RedisInstanceModel) fromAPI(_ context.Context, inst *apiRedisInstance, _ *diag.Diagnostics) {
	m.ID = types.StringValue(inst.ID)
	m.Name = types.StringValue(inst.Name)
	m.EngineVersion = types.StringValue(inst.EngineVersion)
	m.FlavorID = types.StringValue(inst.FlavorID)
	m.VPCID = types.StringValue(inst.VPCID)
	m.SubnetID = types.StringValue(inst.SubnetID)
	m.PersistenceMode = types.StringValue(inst.PersistenceMode)
	m.EvictionPolicy = types.StringValue(inst.EvictionPolicy)
	m.Status = types.StringValue(inst.Status)
	m.CreatedAt = types.StringValue(inst.CreatedAt)

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
