package reservedmeta

import (
	"reflect"
	"testing"
)

func TestIsReservedVolume(t *testing.T) {
	cases := []struct {
		key  string
		want bool
	}{
		// bare backend-stamped volume keys
		{"request-id", true},
		{"customer-id", true},
		{"project-id", true},
		// prefixed managed namespace
		{"frostmoln_type", true},
		{"frostmoln_id", true},
		{"frostmoln-managed", true},
		// user keys
		{"env", false},
		{"team", false},
		{"request_id", false}, // underscore, not the bare hyphen key
		{"", false},
	}
	for _, c := range cases {
		if got := IsReservedVolume(c.key); got != c.want {
			t.Errorf("IsReservedVolume(%q) = %v, want %v", c.key, got, c.want)
		}
	}
}

func TestIsReservedInstance(t *testing.T) {
	cases := []struct {
		key  string
		want bool
	}{
		// only the frostmoln_ prefix is reserved on compute
		{"frostmoln_type", true},
		{"frostmoln_id", true},
		// the bare *-id keys are NOT reserved on instances — compute neither
		// stamps nor reserves them, so a customer may legally use them.
		{"customer-id", false},
		{"request-id", false},
		{"project-id", false},
		{"frostmoln-managed", false}, // hyphen prefix is volume-only
		{"env", false},
		{"", false},
	}
	for _, c := range cases {
		if got := IsReservedInstance(c.key); got != c.want {
			t.Errorf("IsReservedInstance(%q) = %v, want %v", c.key, got, c.want)
		}
	}
}

func TestFilterVolume(t *testing.T) {
	in := map[string]string{
		"env":            "prod",
		"team":           "backend",
		"request-id":     "r1",
		"customer-id":    "c1",
		"project-id":     "p1",
		"frostmoln_type": "managed",
	}
	got := FilterVolume(in)
	want := map[string]string{"env": "prod", "team": "backend"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("FilterVolume() = %v, want %v", got, want)
	}
	// must not mutate input
	if len(in) != 6 {
		t.Errorf("FilterVolume mutated input: %v", in)
	}
}

func TestFilterInstance(t *testing.T) {
	in := map[string]string{
		"env":            "prod",
		"customer-id":    "c1", // legal customer tag on an instance — must survive
		"frostmoln_type": "managed",
		"frostmoln_id":   "i1",
	}
	got := FilterInstance(in)
	want := map[string]string{"env": "prod", "customer-id": "c1"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("FilterInstance() = %v, want %v", got, want)
	}
	if len(in) != 4 {
		t.Errorf("FilterInstance mutated input: %v", in)
	}
}

// The network predicate is NARROWER than the volume one on purpose, and the
// cases below are the ones that would be wrong if someone reused FilterVolume
// for network resources: network reserves the frostmoln_ prefix and nothing
// else, so the bare *-id keys and the frostmoln- hyphen spelling are legal
// CUSTOMER tags there and must survive read-back.
func TestIsReservedNetwork(t *testing.T) {
	reserved := []string{
		"frostmoln_type",
		"frostmoln_managed_by",
		"frostmoln_cluster_id",
		// The key a named list already missed — the reason the predicate is a
		// prefix rather than three names (network nlmeta.ReservedTagPrefix).
		"frostmoln_enclave_key",
		"frostmoln_",
	}
	for _, k := range reserved {
		if !IsReservedNetwork(k) {
			t.Errorf("IsReservedNetwork(%q) = false, want true", k)
		}
	}

	notReserved := []string{
		"env", "team", "cost-centre",
		// Reserved for VOLUMES, not for network. Filtering these here would
		// drop a legal customer tag — the bug this package's header warns of.
		"request-id", "customer-id", "project-id",
		"frostmoln-type",
		// Near-misses that are the customer's own.
		"frostmolnish", "frostmoln", "my_frostmoln_type",
	}
	for _, k := range notReserved {
		if IsReservedNetwork(k) {
			t.Errorf("IsReservedNetwork(%q) = true, want false", k)
		}
	}
}

func TestFilterNetwork(t *testing.T) {
	got := FilterNetwork(map[string]string{
		"env":                   "prod",
		"customer-id":           "cust-1",
		"frostmoln-type":        "mine",
		"frostmoln_type":        "kubernetes-control-plane",
		"frostmoln_enclave_key": "org-7",
	})
	want := map[string]string{
		"env":            "prod",
		"customer-id":    "cust-1",
		"frostmoln-type": "mine",
	}
	if len(got) != len(want) {
		t.Fatalf("FilterNetwork() = %v, want %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("FilterNetwork()[%q] = %q, want %q", k, got[k], v)
		}
	}

	// The whole point: an all-reserved map filters to empty, which is what lets
	// the caller fall through to MapNull instead of writing a key into state.
	if len(FilterNetwork(map[string]string{"frostmoln_type": "x"})) != 0 {
		t.Error("a map of only reserved keys must filter to empty")
	}
}
