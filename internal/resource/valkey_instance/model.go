// Package valkey_instance implements the frostmoln_valkey_instance Terraform resource.
package valkey_instance

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/tftags"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/timeouts"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/tlsattr"
)

// ValkeyInstanceModel is the Terraform state model for a managed Valkey instance.
type ValkeyInstanceModel struct {
	ID              types.String `tfsdk:"id"`
	Name            types.String `tfsdk:"name"`
	Version         types.String `tfsdk:"version"`
	FlavorID        types.String `tfsdk:"flavor_id"`
	StorageGB       types.Int64  `tfsdk:"storage_gb"`
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
	// TLS (MANAGED-OFFER-TLS-AND-INTERNAL-DNS-PLAN §8, D1, Phase 4d). RequireTLS is set at
	// create (server default), fixed afterwards (see tlsattr). TLSEnabled, Hostname and
	// TLSPort are read-only and fixed once the create completes (a cache has no standby,
	// and a restore is a new instance). With require_tls the plaintext Port is null.
	RequireTLS types.Bool   `tfsdk:"require_tls"`
	TLSEnabled types.Bool   `tfsdk:"tls_enabled"`
	Hostname   types.String `tfsdk:"hostname"`
	TLSPort    types.Int64  `tfsdk:"tls_port"`
	// Tags / TagsAll are the instance's customer resource tags (internal/tftags).
	Tags    types.Map `tfsdk:"tags"`
	TagsAll types.Map `tfsdk:"tags_all"`
	// Timeouts is the customer-tunable `timeouts` block. fromAPI never touches
	// it: the pointer the practitioner configured rides through every model
	// copy unchanged, which keeps the block stable in state across refreshes.
	Timeouts *timeouts.Model `tfsdk:"timeouts"`
}

// apiValkeyInstance is the API representation of a managed Valkey instance.
type apiValkeyInstance struct {
	ID              string `json:"id"`
	Name            string `json:"name"`
	Type            string `json:"type"`
	TypeVersion     string `json:"typeVersion"`
	FlavorID        string `json:"flavorId"`
	StorageGB       int    `json:"storageGb"`
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
	// TLS (Phase 4d): requireTls and tlsEnabled are always sent by a TLS-aware cache
	// service; absent (an older service, an instance built before TLS) reads false. port is
	// absent when requireTls (6379 closed); tlsPort and hostname only while TLS is served.
	RequireTLS bool   `json:"requireTls"`
	TLSEnabled bool   `json:"tlsEnabled"`
	Hostname   string `json:"hostname,omitempty"`
	TLSPort    int    `json:"tlsPort,omitempty"`
	// Tags are the instance's customer resource tags ({} when untagged).
	Tags map[string]string `json:"tags"`
}

// apiCreateValkeyInstanceRequest is the API request to create a managed Valkey instance.
type apiCreateValkeyInstanceRequest struct {
	Name            string `json:"name"`
	Type            string `json:"type"`
	TypeVersion     string `json:"typeVersion"`
	FlavorID        string `json:"flavorId"`
	StorageGB       int    `json:"storageGb,omitempty"`
	VPCID           string `json:"vpcId"`
	SubnetID        string `json:"subnetId"`
	PersistenceMode string `json:"persistenceMode,omitempty"`
	EvictionPolicy  string `json:"evictionPolicy,omitempty"`
	// RequireTLS is sent only when configured. Omitted, the service applies its default:
	// TLS required once TLS for managed caches is available in the deployment, the pre-TLS
	// create while it is not (where an explicit true is REQUIRE_TLS_UNAVAILABLE).
	RequireTLS *bool `json:"requireTls,omitempty"`
	// Tags is default_tags ∪ tags (tftags.ForCreate), omitted when empty; the API
	// merges the tenant's default tags in.
	Tags map[string]string `json:"tags,omitempty"`
}

// apiUpdateValkeyInstanceRequest is the API request to update a managed Valkey instance
// (PUT /caches/{id}). The backend accepts only these in-place fields — NOT flavor or storage:
// both flavor changes and storage grows go via POST /caches/{id}/resize.
type apiUpdateValkeyInstanceRequest struct {
	Name            *string `json:"name,omitempty"`
	PersistenceMode *string `json:"persistenceMode,omitempty"`
	EvictionPolicy  *string `json:"evictionPolicy,omitempty"`
}

// hasChanges reports whether any in-place-updatable field is set, so Update can skip an empty PUT
// when only storage changed (which routes through /resize instead).
func (r apiUpdateValkeyInstanceRequest) hasChanges() bool {
	return r.Name != nil || r.PersistenceMode != nil || r.EvictionPolicy != nil
}

// apiResizeValkeyInstanceRequest is the API request to resize a managed Valkey instance
// (POST /caches/{id}/resize): a storage grow (StorageGB) OR a flavor change (FlavorID), never both
// in one request (the backend rejects both). Storage is grow-only (the provider rejects a shrink
// client-side); a flavor change is a Nova resize that RESTARTS the VM.
type apiResizeValkeyInstanceRequest struct {
	StorageGB int    `json:"storageGb,omitempty"`
	FlavorID  string `json:"flavorId,omitempty"`
}

// toCreateRequest converts the Terraform model to an API create request.
func (m *ValkeyInstanceModel) toCreateRequest(_ context.Context, _ *diag.Diagnostics) apiCreateValkeyInstanceRequest {
	req := apiCreateValkeyInstanceRequest{
		Name:        m.Name.ValueString(),
		Type:        "valkey",
		TypeVersion: m.Version.ValueString(),
		FlavorID:    m.FlavorID.ValueString(),
		VPCID:       m.VPCID.ValueString(),
		SubnetID:    m.SubnetID.ValueString(),
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

	// Sent ONLY when configured, so the platform default applies to an omission.
	req.RequireTLS = tlsattr.RequireTLSForCreate(m.RequireTLS)

	return req
}

// toUpdateRequest converts the Terraform model to an API update request, comparing with current state.
func (m *ValkeyInstanceModel) toUpdateRequest(state *ValkeyInstanceModel) apiUpdateValkeyInstanceRequest {
	req := apiUpdateValkeyInstanceRequest{}

	if !m.Name.Equal(state.Name) {
		v := m.Name.ValueString()
		req.Name = &v
	}
	// Both guards carry IsUnknown (mirroring the Backup* pattern): an
	// unknown plan value must never serialize as &"" — the Update belt runs
	// ahead of this and refuses known changes; this keeps the wire writer's
	// predicate identical to the belt's.
	if !m.PersistenceMode.IsUnknown() && !m.PersistenceMode.Equal(state.PersistenceMode) {
		v := m.PersistenceMode.ValueString()
		req.PersistenceMode = &v
	}
	if !m.EvictionPolicy.IsUnknown() && !m.EvictionPolicy.Equal(state.EvictionPolicy) {
		v := m.EvictionPolicy.ValueString()
		req.EvictionPolicy = &v
	}

	return req
}

// fromAPI populates the Terraform model from an API response.
func (m *ValkeyInstanceModel) fromAPI(ctx context.Context, inst *apiValkeyInstance, diags *diag.Diagnostics) {
	// Tags: `tags` keeps the keys the prior value (plan or state) names, `tags_all`
	// every customer key the platform holds.
	m.Tags, m.TagsAll = tftags.ReadBack(ctx, tftags.OfferCustomerTags(inst.Tags), m.Tags, diags)
	m.ID = types.StringValue(inst.ID)
	m.Name = types.StringValue(inst.Name)
	m.Version = types.StringValue(inst.TypeVersion)
	m.FlavorID = types.StringValue(inst.FlavorID)
	m.StorageGB = types.Int64Value(int64(inst.StorageGB))
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

	// TLS, as the service reports it. port above is null when the cache requires TLS (the
	// plaintext 6379 is closed and not sent); tls_port is the TLS listener while it serves.
	m.RequireTLS = types.BoolValue(inst.RequireTLS)
	m.TLSEnabled = types.BoolValue(inst.TLSEnabled)
	m.Hostname = tlsattr.StringFromWire(inst.Hostname)
	m.TLSPort = tlsattr.Int64FromWire(inst.TLSPort)
}

// apiInstanceRef is the slice of the family listing's item shape that the
// create-timeout orphan sweep (internal/orphan) matches on: identity, the
// type discriminator that keeps a same-named sibling type out of the sweep,
// and the created-at stamp the apply floor is compared against.
type apiInstanceRef struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Type      string `json:"type"`
	CreatedAt string `json:"createdAt"`
}

// apiValkeyInstanceList is the GET /caches listing response.
type apiValkeyInstanceList struct {
	Instances []apiInstanceRef `json:"instances"`
}

// apiUpdateTagsRequest is the tags-only PUT of the instance: carrying `tags` alone it is
// a row-only write — no command, no restart, no configuration change. A POINTER
// so an empty map is sent as {} (clear).
type apiUpdateTagsRequest struct {
	Tags *map[string]string `json:"tags,omitempty"`
}
