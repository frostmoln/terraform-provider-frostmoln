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
	PHPEnabled types.Bool   `tfsdk:"php_enabled"`
	PHPVersion types.String `tfsdk:"php_version"`
	Config     types.Map    `tfsdk:"config"`
	Public     types.Bool   `tfsdk:"public"`
	PublicIP   types.String `tfsdk:"public_ip"`
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
	PHPEnabled    bool              `json:"phpEnabled"`
	PHPVersion    string            `json:"phpVersion,omitempty"`
	EngineConfig  map[string]string `json:"engineConfig,omitempty"`
	// Public is OBSERVED (ADR-0097): true when a Public IP is currently
	// associated to the instance's engine port. PublicIP is that FIP. Both are
	// set by the create saga (create with public=true) or the expose/unexpose
	// action, never written directly on the instance PUT.
	Public    bool   `json:"public"`
	PublicIP  string `json:"publicIp,omitempty"`
	Status    string `json:"status"`
	PrivateIP string `json:"privateIp,omitempty"`
	Port      int    `json:"port,omitempty"`
	CreatedAt string `json:"createdAt"`
	UpdatedAt string `json:"updatedAt,omitempty"`
	TenantID  string `json:"tenantId,omitempty"`
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
	PHPEnabled    *bool             `json:"phpEnabled,omitempty"`
	PHPVersion    string            `json:"phpVersion,omitempty"`
	EngineConfig  map[string]string `json:"engineConfig,omitempty"`
	// Public opts the instance into public exposure at create time (ADR-0097):
	// the create saga associates a Public IP and surfaces publicIp. Post-create,
	// exposure toggles via the POST /expose and /unexpose actions, never here.
	Public *bool `json:"public,omitempty"`
}

// apiUpdateWebserverInstanceRequest is the API request to update a managed
// webserver instance via PUT. It carries only in-place-updatable fields:
// storage_gb goes through POST /resize (grow-only) and flavor_id changes are
// rejected at plan time, so neither is sent here (the backend PUT handler
// deserializes only name/tlsEnabled/phpEnabled/phpVersion and drops the rest
// silently).
type apiUpdateWebserverInstanceRequest struct {
	// No PHP fields: php_enabled / php_version force a replacement (the API rejects a
	// PHP change on this PUT with 400), so they can never reach an update.
	//
	// No engineConfig either: this PUT rejects it with 400 ("use PUT /:id/config to
	// change engine configuration") because a write here would never be validated,
	// rendered or actuated on the VM. Config goes through apiUpdateEngineConfigRequest.
	Name       *string `json:"name,omitempty"`
	TLSEnabled *bool   `json:"tlsEnabled,omitempty"`
}

// hasChanges reports whether the update request carries any field to PUT.
func (r apiUpdateWebserverInstanceRequest) hasChanges() bool {
	return r.Name != nil || r.TLSEnabled != nil
}

// apiResizeWebserverInstanceRequest is the body for POST /webservers/{id}/resize.
// Storage grows online and cannot be shrunk (backend rejects a decrease).
type apiResizeWebserverInstanceRequest struct {
	StorageGB int `json:"storageGb"`
}

// apiUpdateEngineConfigRequest is the body for PUT /webservers/{id}/config — the only
// route that applies engine config. NO omitempty: an empty map is meaningful (it resets
// the engine to its boot defaults), and omitting the field would make a reset a silent
// no-op server-side.
type apiUpdateEngineConfigRequest struct {
	EngineConfig map[string]string `json:"engineConfig"`
}

// apiEngineConfigResponse is the PUT /config ack and the GET /config body. The apply is
// asynchronous: the ack reports configStatus "applying" and the runtime validator can
// still fail it, so the terminal state is reached by polling GET /config. configError
// carries the engine's rejection message on a failure. The stored engineConfig itself is
// not modelled here — state is refreshed from the instance read-back.
type apiEngineConfigResponse struct {
	ConfigVersion int64  `json:"configVersion"`
	ConfigStatus  string `json:"configStatus,omitempty"`
	ConfigError   string `json:"configError,omitempty"`
}

// maxConfigErrorBytes bounds how much of the platform's configError is echoed into a
// Terraform diagnostic.
const maxConfigErrorBytes = 512

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
	if !m.PHPEnabled.IsNull() && !m.PHPEnabled.IsUnknown() {
		v := m.PHPEnabled.ValueBool()
		req.PHPEnabled = &v
	}
	if !m.PHPVersion.IsNull() && !m.PHPVersion.IsUnknown() {
		req.PHPVersion = m.PHPVersion.ValueString()
	}
	if !m.Config.IsNull() && !m.Config.IsUnknown() {
		cfg := make(map[string]string)
		diags.Append(m.Config.ElementsAs(ctx, &cfg, false)...)
		req.EngineConfig = cfg
	}
	if !m.Public.IsNull() && !m.Public.IsUnknown() {
		v := m.Public.ValueBool()
		req.Public = &v
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
	if !m.TLSEnabled.Equal(state.TLSEnabled) {
		v := m.TLSEnabled.ValueBool()
		req.TLSEnabled = &v
	}

	return req
}

// engineConfigChange reports the engine config to apply, and whether it changed at all.
// An explicitly configured EMPTY map is a real change (it resets the engine to its boot
// defaults). A null or unknown plan value is NOT: `config` is Optional+Computed, so an
// attribute left out of the configuration keeps its prior value, and an unknown must never
// be read as "reset to defaults".
func (m *NginxInstanceModel) engineConfigChange(ctx context.Context, state *NginxInstanceModel, diags *diag.Diagnostics) (map[string]string, bool) {
	if m.Config.IsNull() || m.Config.IsUnknown() || m.Config.Equal(state.Config) {
		return nil, false
	}
	cfg := make(map[string]string, len(m.Config.Elements()))
	diags.Append(m.Config.ElementsAs(ctx, &cfg, false)...)
	return cfg, true
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
	m.PHPEnabled = types.BoolValue(inst.PHPEnabled)
	m.Public = types.BoolValue(inst.Public)
	m.Status = types.StringValue(inst.Status)
	m.CreatedAt = types.StringValue(inst.CreatedAt)

	if inst.PublicIP != "" {
		m.PublicIP = types.StringValue(inst.PublicIP)
	} else {
		m.PublicIP = types.StringNull()
	}

	if inst.PHPVersion != "" {
		m.PHPVersion = types.StringValue(inst.PHPVersion)
	} else {
		m.PHPVersion = types.StringNull()
	}

	switch {
	case len(inst.EngineConfig) > 0:
		cfgMap, d := types.MapValueFrom(ctx, types.StringType, inst.EngineConfig)
		diags.Append(d...)
		m.Config = cfgMap
	case !m.Config.IsNull() && !m.Config.IsUnknown() && len(m.Config.Elements()) == 0:
		// The instance read-back OMITS engineConfig when the stored config is empty, so an
		// explicit `config = {}` (reset to boot defaults) would otherwise flip to null on
		// every refresh and diff forever. Keep the configured empty map.
	default:
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
