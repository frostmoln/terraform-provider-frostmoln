// Package container_registry implements the frostmoln_container_registry
// Terraform resource — the customer opt-in that creates the tenant's registry.
package container_registry

import (
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// ContainerRegistryModel is the Terraform state model for a tenant's registry.
//
// There is nothing configurable here: every attribute is Computed. The opt-in
// takes no request body, because the namespace is derived from the verified
// tenant id and nothing a request could say would change it.
type ContainerRegistryModel struct {
	// ID is the tenant id. The settings resource id IS the tenant — there is
	// exactly one registry per tenant and it has no identifier of its own.
	ID        types.String `tfsdk:"id"`
	Enabled   types.Bool   `tfsdk:"enabled"`
	Endpoint  types.String `tfsdk:"endpoint"`
	Namespace types.String `tfsdk:"namespace"`
	// StorageLimitBytes and StorageUsedBytes are null when the API omits them:
	// while the registry is disabled, and (for the limit) in the drift window
	// where the API has no cap to report. Null means "not reported" — never
	// "unlimited" and never zero, so nothing downstream should default them.
	StorageLimitBytes types.Int64 `tfsdk:"storage_limit_bytes"`
	StorageUsedBytes  types.Int64 `tfsdk:"storage_used_bytes"`
}

// apiRegistrySettings is the API representation of the tenant's registry state.
//
// Namespace is ABSENT while Enabled is false, deliberately: the name is
// derivable either way, but advertising a pull path that 404s reads as a
// working registry.
type apiRegistrySettings struct {
	// Enabled is a POINTER so that "the body did not say" is distinguishable
	// from "the answer is no". A plain bool decodes an absent field to false,
	// and Read treats false as gone — so a wrapper envelope, a field rename or a
	// partial 200 would silently remove the resource from state. The spec marks
	// `enabled` required; a response without it is a contract violation, and the
	// fail-closed direction is to say so rather than to act on the zero value.
	Enabled   *bool  `json:"enabled"`
	Endpoint  string `json:"endpoint"`
	Namespace string `json:"namespace,omitempty"`
	// Both are omitted by the API rather than sent as zero, so a plain int64 is
	// safe here: absent decodes to 0, and 0 is exactly the "nothing to report"
	// case storageAttrs maps to null.
	StorageLimitBytes int64 `json:"storageLimitBytes,omitempty"`
	StorageUsedBytes  int64 `json:"storageUsedBytes,omitempty"`
}

// isEnabled reports the registry's state. Callers must have rejected a nil
// Enabled first; this treats nil as false so no path acts on it accidentally.
func (s *apiRegistrySettings) isEnabled() bool {
	return s.Enabled != nil && *s.Enabled
}

// fromAPI populates the Terraform model from an API response. tenantID is the
// resource's identity; the settings body does not carry one.
func (m *ContainerRegistryModel) fromAPI(tenantID string, s *apiRegistrySettings) {
	m.ID = types.StringValue(tenantID)
	m.Enabled = types.BoolValue(s.isEnabled())
	m.Endpoint = types.StringValue(s.Endpoint)
	// Set BEFORE the namespace branch returns. Read rejects only !isEnabled(), so
	// an enabled body with no namespace reaches that return — and leaving the
	// assignment below it kept the PREVIOUS storage numbers in state next to a
	// nulled namespace.
	m.StorageLimitBytes, m.StorageUsedBytes = storageAttrs(s)
	if s.Namespace == "" {
		m.Namespace = types.StringNull()
		return
	}
	m.Namespace = types.StringValue(s.Namespace)
}

// storageAttrs maps the reported storage numbers onto Terraform values, with a
// non-positive value becoming NULL rather than 0.
//
// The distinction is the whole point: the API omits a cap it cannot report, and
// rendering that as 0 would tell a practitioner their registry may hold nothing.
//
// The data source carries its OWN copy of this, because it also carries its own
// apiRegistrySettings — two packages, no shared internal type. They CAN drift,
// so both are covered by the same table test rather than one standing in for
// the other.
func storageAttrs(s *apiRegistrySettings) (limit, used types.Int64) {
	limit, used = types.Int64Null(), types.Int64Null()
	if s.StorageLimitBytes > 0 {
		limit = types.Int64Value(s.StorageLimitBytes)
		// Usage is only meaningful against a cap, and it is legitimately zero on a
		// fresh registry — so it is reported whenever a cap was, zero included.
		used = types.Int64Value(s.StorageUsedBytes)
	}
	return limit, used
}
