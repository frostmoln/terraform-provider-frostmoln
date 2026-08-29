package container_registry

import "testing"

// 🔴 THE DATA SOURCE CARRIES ITS OWN COPY OF storageAttrs.
//
// It has its own apiRegistrySettings and its own mapping helper — two packages,
// no shared internal type — and this is the copy a practitioner hits with
// `data "frostmoln_container_registry"`. The resource's table test cannot stand
// in for it, so the same cases are asserted here: an unreported cap must be
// NULL, never 0, because 0 would tell them their registry may hold nothing.
func TestStorageAttrsDistinguishesUnreportedFromZero(t *testing.T) {
	for name, tc := range map[string]struct {
		in                  apiRegistrySettings
		wantLimitNull       bool
		wantUsedNull        bool
		wantLimit, wantUsed int64
	}{
		"nothing reported":    {in: apiRegistrySettings{}, wantLimitNull: true, wantUsedNull: true},
		"cap and usage":       {in: apiRegistrySettings{StorageLimitBytes: 10 << 30, StorageUsedBytes: 3 << 30}, wantLimit: 10 << 30, wantUsed: 3 << 30},
		"cap with zero usage": {in: apiRegistrySettings{StorageLimitBytes: 10 << 30}, wantLimit: 10 << 30, wantUsed: 0},
		"usage but no cap":    {in: apiRegistrySettings{StorageUsedBytes: 5 << 20}, wantLimitNull: true, wantUsedNull: true},
		"unlimited sentinel":  {in: apiRegistrySettings{StorageLimitBytes: -1}, wantLimitNull: true, wantUsedNull: true},
	} {
		t.Run(name, func(t *testing.T) {
			limit, used := storageAttrs(&tc.in)
			if limit.IsNull() != tc.wantLimitNull {
				t.Errorf("limit null = %v, want %v", limit.IsNull(), tc.wantLimitNull)
			}
			if used.IsNull() != tc.wantUsedNull {
				t.Errorf("used null = %v, want %v", used.IsNull(), tc.wantUsedNull)
			}
			if !tc.wantLimitNull && limit.ValueInt64() != tc.wantLimit {
				t.Errorf("limit = %d, want %d", limit.ValueInt64(), tc.wantLimit)
			}
			if !tc.wantUsedNull && used.ValueInt64() != tc.wantUsed {
				t.Errorf("used = %d, want %d", used.ValueInt64(), tc.wantUsed)
			}
		})
	}
}
