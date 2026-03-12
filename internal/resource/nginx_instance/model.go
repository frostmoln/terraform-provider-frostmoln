// Package nginx_instance implements the frostmoln_nginx_instance Terraform resource.
package nginx_instance

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// NginxInstanceModel is the Terraform state model for a managed Nginx webserver instance.
type NginxInstanceModel struct {
	ID              types.String `tfsdk:"id"`
	Name            types.String `tfsdk:"name"`
	EngineVersion   types.String `tfsdk:"engine_version"`
	Flavor          types.String `tfsdk:"flavor"`
	StorageGB       types.Int64  `tfsdk:"storage_gb"`
	TLSEnabled      types.Bool   `tfsdk:"tls_enabled"`
	WorkerProcesses types.Int64  `tfsdk:"worker_processes"`
	GzipEnabled     types.Bool   `tfsdk:"gzip_enabled"`
	TryFiles        types.String `tfsdk:"try_files"`
	ProxyPass       types.String `tfsdk:"proxy_pass"`
	EngineConfig    types.String `tfsdk:"engine_config"`
	Status          types.String `tfsdk:"status"`
	PrivateIP       types.String `tfsdk:"private_ip"`
	Port            types.Int64  `tfsdk:"port"`
	CreatedAt       types.String `tfsdk:"created_at"`
	UpdatedAt       types.String `tfsdk:"updated_at"`
	TenantID        types.String `tfsdk:"tenant_id"`
}

// apiWebserverInstance is the API representation of a managed webserver instance.
type apiWebserverInstance struct {
	ID              string `json:"id"`
	Name            string `json:"name"`
	Engine          string `json:"engine"`
	EngineVersion   string `json:"engineVersion"`
	Flavor          string `json:"flavor"`
	StorageGB       int    `json:"storageGb"`
	TLSEnabled      bool   `json:"tlsEnabled"`
	WorkerProcesses int    `json:"workerProcesses,omitempty"`
	GzipEnabled     bool   `json:"gzipEnabled"`
	TryFiles        string `json:"tryFiles,omitempty"`
	ProxyPass       string `json:"proxyPass,omitempty"`
	EngineConfig    string `json:"engineConfig,omitempty"`
	Status          string `json:"status"`
	PrivateIP       string `json:"privateIp,omitempty"`
	Port            int    `json:"port,omitempty"`
	CreatedAt       string `json:"createdAt"`
	UpdatedAt       string `json:"updatedAt,omitempty"`
	TenantID        string `json:"tenantId,omitempty"`
}

// apiCreateWebserverInstanceRequest is the API request to create a managed webserver instance.
type apiCreateWebserverInstanceRequest struct {
	Name            string `json:"name"`
	Engine          string `json:"engine"`
	EngineVersion   string `json:"engineVersion"`
	Flavor          string `json:"flavor"`
	StorageGB       int    `json:"storageGb"`
	TLSEnabled      *bool  `json:"tlsEnabled,omitempty"`
	WorkerProcesses *int   `json:"workerProcesses,omitempty"`
	GzipEnabled     *bool  `json:"gzipEnabled,omitempty"`
	TryFiles        string `json:"tryFiles,omitempty"`
	ProxyPass       string `json:"proxyPass,omitempty"`
	EngineConfig    string `json:"engineConfig,omitempty"`
}

// apiUpdateWebserverInstanceRequest is the API request to update a managed webserver instance.
type apiUpdateWebserverInstanceRequest struct {
	Name            *string `json:"name,omitempty"`
	Flavor          *string `json:"flavor,omitempty"`
	StorageGB       *int    `json:"storageGb,omitempty"`
	TLSEnabled      *bool   `json:"tlsEnabled,omitempty"`
	WorkerProcesses *int    `json:"workerProcesses,omitempty"`
	GzipEnabled     *bool   `json:"gzipEnabled,omitempty"`
	TryFiles        *string `json:"tryFiles,omitempty"`
	ProxyPass       *string `json:"proxyPass,omitempty"`
	EngineConfig    *string `json:"engineConfig,omitempty"`
}

// toCreateRequest converts the Terraform model to an API create request.
func (m *NginxInstanceModel) toCreateRequest(_ context.Context, _ *diag.Diagnostics) apiCreateWebserverInstanceRequest {
	req := apiCreateWebserverInstanceRequest{
		Name:          m.Name.ValueString(),
		Engine:        "nginx",
		EngineVersion: m.EngineVersion.ValueString(),
		Flavor:        m.Flavor.ValueString(),
		StorageGB:     int(m.StorageGB.ValueInt64()),
	}

	if !m.TLSEnabled.IsNull() && !m.TLSEnabled.IsUnknown() {
		v := m.TLSEnabled.ValueBool()
		req.TLSEnabled = &v
	}
	if !m.WorkerProcesses.IsNull() && !m.WorkerProcesses.IsUnknown() {
		v := int(m.WorkerProcesses.ValueInt64())
		req.WorkerProcesses = &v
	}
	if !m.GzipEnabled.IsNull() && !m.GzipEnabled.IsUnknown() {
		v := m.GzipEnabled.ValueBool()
		req.GzipEnabled = &v
	}
	if !m.TryFiles.IsNull() && !m.TryFiles.IsUnknown() {
		req.TryFiles = m.TryFiles.ValueString()
	}
	if !m.ProxyPass.IsNull() && !m.ProxyPass.IsUnknown() {
		req.ProxyPass = m.ProxyPass.ValueString()
	}
	if !m.EngineConfig.IsNull() && !m.EngineConfig.IsUnknown() {
		req.EngineConfig = m.EngineConfig.ValueString()
	}

	return req
}

// toUpdateRequest converts the Terraform model to an API update request, comparing with current state.
func (m *NginxInstanceModel) toUpdateRequest(state *NginxInstanceModel) apiUpdateWebserverInstanceRequest {
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
	if !m.WorkerProcesses.Equal(state.WorkerProcesses) {
		v := int(m.WorkerProcesses.ValueInt64())
		req.WorkerProcesses = &v
	}
	if !m.GzipEnabled.Equal(state.GzipEnabled) {
		v := m.GzipEnabled.ValueBool()
		req.GzipEnabled = &v
	}
	if !m.TryFiles.Equal(state.TryFiles) {
		v := m.TryFiles.ValueString()
		req.TryFiles = &v
	}
	if !m.ProxyPass.Equal(state.ProxyPass) {
		v := m.ProxyPass.ValueString()
		req.ProxyPass = &v
	}
	if !m.EngineConfig.Equal(state.EngineConfig) {
		v := m.EngineConfig.ValueString()
		req.EngineConfig = &v
	}

	return req
}

// fromAPI populates the Terraform model from an API response.
func (m *NginxInstanceModel) fromAPI(_ context.Context, inst *apiWebserverInstance, _ *diag.Diagnostics) {
	m.ID = types.StringValue(inst.ID)
	m.Name = types.StringValue(inst.Name)
	m.EngineVersion = types.StringValue(inst.EngineVersion)
	m.Flavor = types.StringValue(inst.Flavor)
	m.StorageGB = types.Int64Value(int64(inst.StorageGB))
	m.TLSEnabled = types.BoolValue(inst.TLSEnabled)
	m.GzipEnabled = types.BoolValue(inst.GzipEnabled)
	m.Status = types.StringValue(inst.Status)
	m.CreatedAt = types.StringValue(inst.CreatedAt)

	if inst.WorkerProcesses > 0 {
		m.WorkerProcesses = types.Int64Value(int64(inst.WorkerProcesses))
	} else {
		m.WorkerProcesses = types.Int64Null()
	}

	if inst.TryFiles != "" {
		m.TryFiles = types.StringValue(inst.TryFiles)
	} else {
		m.TryFiles = types.StringNull()
	}

	if inst.ProxyPass != "" {
		m.ProxyPass = types.StringValue(inst.ProxyPass)
	} else {
		m.ProxyPass = types.StringNull()
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
