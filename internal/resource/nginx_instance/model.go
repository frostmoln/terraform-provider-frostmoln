// Package nginx_instance implements the frostmoln_nginx_instance Terraform resource.
package nginx_instance

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// NginxInstanceModel is the Terraform state model for a managed Nginx webserver instance.
type NginxInstanceModel struct {
	ID         types.String `tfsdk:"id"`
	Name       types.String `tfsdk:"name"`
	Version    types.String `tfsdk:"version"`
	FlavorID   types.String `tfsdk:"flavor_id"`
	StorageGB  types.Int64  `tfsdk:"storage_gb"`
	VPCID      types.String `tfsdk:"vpc_id"`
	SubnetID   types.String `tfsdk:"subnet_id"`
	TLSEnabled types.Bool   `tfsdk:"tls_enabled"`
	Config     types.Map    `tfsdk:"config"`
	Status     types.String `tfsdk:"status"`
	PrivateIP  types.String `tfsdk:"private_ip"`
	Port       types.Int64  `tfsdk:"port"`
	CreatedAt  types.String `tfsdk:"created_at"`
	UpdatedAt  types.String `tfsdk:"updated_at"`
	TenantID   types.String `tfsdk:"tenant_id"`
}

// apiWebserverInstance is the API representation of a managed webserver instance.
// Field names match the webserver service (webserver/internal/domain/instance.go):
// the flavor is `flavorId`, vpcId/subnetId are returned, and `engineConfig` is a
// JSON object (not a string). The workerProcesses/gzipEnabled/tryFiles/proxyPass
// fields are NOT part of the contract (they only ever live inside engineConfig).
type apiWebserverInstance struct {
	ID            string            `json:"id"`
	Name          string            `json:"name"`
	Engine        string            `json:"engine"`
	EngineVersion string            `json:"engineVersion"`
	FlavorID      string            `json:"flavorId"`
	StorageGB     int               `json:"storageGb"`
	VPCID         string            `json:"vpcId"`
	SubnetID      string            `json:"subnetId"`
	TLSEnabled    bool              `json:"tlsEnabled"`
	EngineConfig  map[string]string `json:"engineConfig,omitempty"`
	Status        string            `json:"status"`
	PrivateIP     string            `json:"privateIp,omitempty"`
	Port          int               `json:"port,omitempty"`
	CreatedAt     string            `json:"createdAt"`
	UpdatedAt     string            `json:"updatedAt,omitempty"`
	TenantID      string            `json:"tenantId,omitempty"`
}

// apiCreateWebserverInstanceRequest is the API request to create a managed
// webserver instance. The webserver service requires flavorId, vpcId and
// subnetId; engineConfig is a JSON object.
type apiCreateWebserverInstanceRequest struct {
	Name          string            `json:"name"`
	Engine        string            `json:"engine"`
	EngineVersion string            `json:"engineVersion"`
	FlavorID      string            `json:"flavorId"`
	StorageGB     int               `json:"storageGb"`
	VPCID         string            `json:"vpcId"`
	SubnetID      string            `json:"subnetId"`
	TLSEnabled    *bool             `json:"tlsEnabled,omitempty"`
	EngineConfig  map[string]string `json:"engineConfig,omitempty"`
}

// apiUpdateWebserverInstanceRequest is the API request to update a managed webserver instance.
type apiUpdateWebserverInstanceRequest struct {
	Name         *string           `json:"name,omitempty"`
	FlavorID     *string           `json:"flavorId,omitempty"`
	StorageGB    *int              `json:"storageGb,omitempty"`
	TLSEnabled   *bool             `json:"tlsEnabled,omitempty"`
	EngineConfig map[string]string `json:"engineConfig,omitempty"`
}

// toCreateRequest converts the Terraform model to an API create request.
func (m *NginxInstanceModel) toCreateRequest(ctx context.Context, diags *diag.Diagnostics) apiCreateWebserverInstanceRequest {
	req := apiCreateWebserverInstanceRequest{
		Name:          m.Name.ValueString(),
		Engine:        "nginx",
		EngineVersion: m.Version.ValueString(),
		FlavorID:      m.FlavorID.ValueString(),
		StorageGB:     int(m.StorageGB.ValueInt64()),
		VPCID:         m.VPCID.ValueString(),
		SubnetID:      m.SubnetID.ValueString(),
	}

	if !m.TLSEnabled.IsNull() && !m.TLSEnabled.IsUnknown() {
		v := m.TLSEnabled.ValueBool()
		req.TLSEnabled = &v
	}
	if !m.Config.IsNull() && !m.Config.IsUnknown() {
		cfg := make(map[string]string)
		diags.Append(m.Config.ElementsAs(ctx, &cfg, false)...)
		req.EngineConfig = cfg
	}

	return req
}

// toUpdateRequest converts the Terraform model to an API update request, comparing with current state.
func (m *NginxInstanceModel) toUpdateRequest(ctx context.Context, state *NginxInstanceModel, diags *diag.Diagnostics) apiUpdateWebserverInstanceRequest {
	req := apiUpdateWebserverInstanceRequest{}

	if !m.Name.Equal(state.Name) {
		v := m.Name.ValueString()
		req.Name = &v
	}
	if !m.FlavorID.Equal(state.FlavorID) {
		v := m.FlavorID.ValueString()
		req.FlavorID = &v
	}
	if !m.StorageGB.Equal(state.StorageGB) {
		v := int(m.StorageGB.ValueInt64())
		req.StorageGB = &v
	}
	if !m.TLSEnabled.Equal(state.TLSEnabled) {
		v := m.TLSEnabled.ValueBool()
		req.TLSEnabled = &v
	}
	if !m.Config.Equal(state.Config) {
		cfg := make(map[string]string)
		if !m.Config.IsNull() && !m.Config.IsUnknown() {
			diags.Append(m.Config.ElementsAs(ctx, &cfg, false)...)
		}
		req.EngineConfig = cfg
	}

	return req
}

// fromAPI populates the Terraform model from an API response.
func (m *NginxInstanceModel) fromAPI(ctx context.Context, inst *apiWebserverInstance, diags *diag.Diagnostics) {
	m.ID = types.StringValue(inst.ID)
	m.Name = types.StringValue(inst.Name)
	m.Version = types.StringValue(inst.EngineVersion)
	m.FlavorID = types.StringValue(inst.FlavorID)
	m.StorageGB = types.Int64Value(int64(inst.StorageGB))
	m.VPCID = types.StringValue(inst.VPCID)
	m.SubnetID = types.StringValue(inst.SubnetID)
	m.TLSEnabled = types.BoolValue(inst.TLSEnabled)
	m.Status = types.StringValue(inst.Status)
	m.CreatedAt = types.StringValue(inst.CreatedAt)

	if len(inst.EngineConfig) > 0 {
		cfgMap, d := types.MapValueFrom(ctx, types.StringType, inst.EngineConfig)
		diags.Append(d...)
		m.Config = cfgMap
	} else {
		m.Config = types.MapNull(types.StringType)
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
