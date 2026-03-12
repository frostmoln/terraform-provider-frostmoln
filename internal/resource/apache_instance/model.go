// Package apache_instance implements the frostmoln_apache_instance Terraform resource.
package apache_instance

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// ApacheInstanceModel is the Terraform state model for a managed Apache webserver instance.
type ApacheInstanceModel struct {
	ID            types.String `tfsdk:"id"`
	Name          types.String `tfsdk:"name"`
	EngineVersion types.String `tfsdk:"engine_version"`
	Flavor        types.String `tfsdk:"flavor"`
	StorageGB     types.Int64  `tfsdk:"storage_gb"`
	TLSEnabled    types.Bool   `tfsdk:"tls_enabled"`
	PHPEnabled    types.Bool   `tfsdk:"php_enabled"`
	PHPVersion    types.String `tfsdk:"php_version"`
	EngineConfig  types.String `tfsdk:"engine_config"`
	Status        types.String `tfsdk:"status"`
	PrivateIP     types.String `tfsdk:"private_ip"`
	Port          types.Int64  `tfsdk:"port"`
	CreatedAt     types.String `tfsdk:"created_at"`
	UpdatedAt     types.String `tfsdk:"updated_at"`
	TenantID      types.String `tfsdk:"tenant_id"`
}

// apiWebserverInstance is the API representation of a managed webserver instance.
type apiWebserverInstance struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	Engine        string `json:"engine"`
	EngineVersion string `json:"engineVersion"`
	Flavor        string `json:"flavor"`
	StorageGB     int    `json:"storageGb"`
	TLSEnabled    bool   `json:"tlsEnabled"`
	PHPEnabled    bool   `json:"phpEnabled"`
	PHPVersion    string `json:"phpVersion,omitempty"`
	EngineConfig  string `json:"engineConfig,omitempty"`
	Status        string `json:"status"`
	PrivateIP     string `json:"privateIp,omitempty"`
	Port          int    `json:"port,omitempty"`
	CreatedAt     string `json:"createdAt"`
	UpdatedAt     string `json:"updatedAt,omitempty"`
	TenantID      string `json:"tenantId,omitempty"`
}

// apiCreateWebserverInstanceRequest is the API request to create a managed webserver instance.
type apiCreateWebserverInstanceRequest struct {
	Name          string `json:"name"`
	Engine        string `json:"engine"`
	EngineVersion string `json:"engineVersion"`
	Flavor        string `json:"flavor"`
	StorageGB     int    `json:"storageGb"`
	TLSEnabled    *bool  `json:"tlsEnabled,omitempty"`
	PHPEnabled    *bool  `json:"phpEnabled,omitempty"`
	PHPVersion    string `json:"phpVersion,omitempty"`
	EngineConfig  string `json:"engineConfig,omitempty"`
}

// apiUpdateWebserverInstanceRequest is the API request to update a managed webserver instance.
type apiUpdateWebserverInstanceRequest struct {
	Name         *string `json:"name,omitempty"`
	Flavor       *string `json:"flavor,omitempty"`
	StorageGB    *int    `json:"storageGb,omitempty"`
	TLSEnabled   *bool   `json:"tlsEnabled,omitempty"`
	PHPEnabled   *bool   `json:"phpEnabled,omitempty"`
	PHPVersion   *string `json:"phpVersion,omitempty"`
	EngineConfig *string `json:"engineConfig,omitempty"`
}

// toCreateRequest converts the Terraform model to an API create request.
func (m *ApacheInstanceModel) toCreateRequest(_ context.Context, _ *diag.Diagnostics) apiCreateWebserverInstanceRequest {
	req := apiCreateWebserverInstanceRequest{
		Name:          m.Name.ValueString(),
		Engine:        "apache",
		EngineVersion: m.EngineVersion.ValueString(),
		Flavor:        m.Flavor.ValueString(),
		StorageGB:     int(m.StorageGB.ValueInt64()),
	}

	if !m.TLSEnabled.IsNull() && !m.TLSEnabled.IsUnknown() {
		v := m.TLSEnabled.ValueBool()
		req.TLSEnabled = &v
	}
	if !m.PHPEnabled.IsNull() && !m.PHPEnabled.IsUnknown() {
		v := m.PHPEnabled.ValueBool()
		req.PHPEnabled = &v
	}
	if !m.PHPVersion.IsNull() && !m.PHPVersion.IsUnknown() {
		req.PHPVersion = m.PHPVersion.ValueString()
	}
	if !m.EngineConfig.IsNull() && !m.EngineConfig.IsUnknown() {
		req.EngineConfig = m.EngineConfig.ValueString()
	}

	return req
}

// toUpdateRequest converts the Terraform model to an API update request, comparing with current state.
func (m *ApacheInstanceModel) toUpdateRequest(state *ApacheInstanceModel) apiUpdateWebserverInstanceRequest {
	req := apiUpdateWebserverInstanceRequest{}

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
	if !m.TLSEnabled.Equal(state.TLSEnabled) {
		v := m.TLSEnabled.ValueBool()
		req.TLSEnabled = &v
	}
	if !m.PHPEnabled.Equal(state.PHPEnabled) {
		v := m.PHPEnabled.ValueBool()
		req.PHPEnabled = &v
	}
	if !m.PHPVersion.Equal(state.PHPVersion) {
		v := m.PHPVersion.ValueString()
		req.PHPVersion = &v
	}
	if !m.EngineConfig.Equal(state.EngineConfig) {
		v := m.EngineConfig.ValueString()
		req.EngineConfig = &v
	}

	return req
}

// fromAPI populates the Terraform model from an API response.
func (m *ApacheInstanceModel) fromAPI(_ context.Context, inst *apiWebserverInstance, _ *diag.Diagnostics) {
	m.ID = types.StringValue(inst.ID)
	m.Name = types.StringValue(inst.Name)
	m.EngineVersion = types.StringValue(inst.EngineVersion)
	m.Flavor = types.StringValue(inst.Flavor)
	m.StorageGB = types.Int64Value(int64(inst.StorageGB))
	m.TLSEnabled = types.BoolValue(inst.TLSEnabled)
	m.PHPEnabled = types.BoolValue(inst.PHPEnabled)
	m.Status = types.StringValue(inst.Status)
	m.CreatedAt = types.StringValue(inst.CreatedAt)

	if inst.PHPVersion != "" {
		m.PHPVersion = types.StringValue(inst.PHPVersion)
	} else {
		m.PHPVersion = types.StringNull()
	}

	if inst.EngineConfig != "" {
		m.EngineConfig = types.StringValue(inst.EngineConfig)
	} else {
		m.EngineConfig = types.StringNull()
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
