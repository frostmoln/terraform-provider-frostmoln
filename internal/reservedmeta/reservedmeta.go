// Package reservedmeta identifies platform-owned instance/volume metadata keys
// that the backend injects (tenant boundary, billing attribution, provenance, the
// frostmoln_* managed namespace) and that customers cannot set. They must be
// filtered out of the Terraform `tags` attribute on read-back, or a null/unset
// tags plan is overwritten on apply ("inconsistent result after apply").
//
// The reserved set differs per backend, so the predicates are split — using the
// volume (union) set on instances would silently drop a legal customer instance
// tag named customer-id/request-id/project-id, reintroducing the very bug this
// package fixes. Keep each predicate in sync with its backend definition.
package reservedmeta

import "strings"

// IsReservedVolume reports whether a key is platform-owned for storage resources
// (volumes, snapshots). Mirrors the backend isReservedVolumeMetadataKey
// (storage/internal/service/impl/volume.go and the identical
// provisioning/internal/activity/activities.go): the bare keys request-id,
// customer-id, project-id plus the frostmoln_/frostmoln- prefixes.
func IsReservedVolume(k string) bool {
	switch k {
	case "request-id", "customer-id", "project-id":
		return true
	}
	return strings.HasPrefix(k, "frostmoln_") || strings.HasPrefix(k, "frostmoln-")
}

// IsReservedInstance reports whether a key is platform-owned for compute
// resources (instances). Mirrors compute's ServiceReservedPrefixes
// (compute/internal/service/impl/instance.go): only the frostmoln_ prefix.
// Compute neither stamps nor reserves the bare *-id keys, so they are NOT
// filtered here — doing so would drop a legal customer tag.
func IsReservedInstance(k string) bool {
	return strings.HasPrefix(k, "frostmoln_")
}

// FilterVolume returns a copy of m with storage-reserved keys removed.
func FilterVolume(m map[string]string) map[string]string {
	return filter(m, IsReservedVolume)
}

// FilterInstance returns a copy of m with compute-reserved keys removed.
func FilterInstance(m map[string]string) map[string]string {
	return filter(m, IsReservedInstance)
}

func filter(m map[string]string, reserved func(string) bool) map[string]string {
	out := make(map[string]string, len(m))
	for k, v := range m {
		if reserved(k) {
			continue
		}
		out[k] = v
	}
	return out
}
