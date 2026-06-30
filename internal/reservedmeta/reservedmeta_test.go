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
