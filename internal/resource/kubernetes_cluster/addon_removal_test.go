package kubernetes_cluster

import (
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

func addonSet(keys ...string) types.Set {
	elems := make([]attr.Value, 0, len(keys))
	for _, k := range keys {
		elems = append(elems, types.StringValue(k))
	}
	return types.SetValueMust(types.StringType, elems)
}

// 🔴 THIS FILE REPLACES addon_plan_modifier_test.go, WHOSE CENTRAL CASE WAS
// "RemovingAnAddonReplacesTheCluster". That behaviour is deleted: the API prunes in
// place now, so planning a full cluster replacement would destroy a customer's cluster
// and every workload on it to perform a background operation. What has to be pinned
// instead is that the removal is NAMED — the endpoint refuses a short selection that
// does not say what it drops, precisely so a client working from a stale read cannot
// delete.
func TestRemovedAddons_NamesWhatTheConfigurationDropped(t *testing.T) {
	got := removedAddons(addonSet("external-dns", "external-secrets"), addonSet("external-secrets"))
	if len(got) != 1 || got[0] != "external-dns" {
		t.Fatalf("removedAddons = %v, want [external-dns]", got)
	}
}

// A pure addition must carry NO removal at all — not an empty list, which would still
// serialise a `remove` key. `omitempty` plus a nil return is what keeps an add request
// structurally incapable of deleting.
func TestRemovedAddons_AnAdditionCarriesNoRemoval(t *testing.T) {
	if got := removedAddons(addonSet("external-secrets"), addonSet("external-secrets", "external-dns")); got != nil {
		t.Fatalf("removedAddons = %v, want nil so the field is omitted entirely", got)
	}
	if got := removedAddons(addonSet("external-dns"), addonSet("external-dns")); got != nil {
		t.Fatalf("an unchanged set produced %v, want nil", got)
	}
}

// 🔴 NULL AND UNKNOWN ARE NEVER "THE EMPTY SET", and reading either as one would compute
// a removal of EVERYTHING. Unknown is "not resolved yet"; null is "the practitioner did
// not configure it". Under the old modifier this mistake replaced the cluster; under the
// new code path it would DELETE every addon, so the guard matters more than it did.
func TestRemovedAddons_NullOrUnknownIsNeverARemoval(t *testing.T) {
	full := addonSet("external-dns")
	for name, args := range map[string][2]types.Set{
		"null plan":     {full, types.SetNull(types.StringType)},
		"unknown plan":  {full, types.SetUnknown(types.StringType)},
		"null state":    {types.SetNull(types.StringType), full},
		"unknown state": {types.SetUnknown(types.StringType), full},
	} {
		t.Run(name, func(t *testing.T) {
			if got := removedAddons(args[0], args[1]); got != nil {
				t.Fatalf("removedAddons = %v, want nil — this would have deleted every addon", got)
			}
		})
	}
}

// Emptying the set deliberately IS a removal of everything, and must be reported as such
// rather than confused with the null case above.
func TestRemovedAddons_AnExplicitEmptySetRemovesEverything(t *testing.T) {
	got := removedAddons(addonSet("external-dns", "external-secrets"), addonSet())
	if len(got) != 2 {
		t.Fatalf("removedAddons = %v, want both keys", got)
	}
}
