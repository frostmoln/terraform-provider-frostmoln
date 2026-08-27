package appgw_backend

import (
	"testing"
)

func TestBackendModelFromAPI(t *testing.T) {
	b := &apiBackend{
		ID: "b-1", PoolID: "pool-1", SourceKind: "instance", SourceID: "i-1",
		Address: "10.0.1.10", Port: 8080, Weight: 1, Status: "healthy", Enabled: true,
		CreatedAt: "2026-08-01T00:00:00Z",
	}
	var m BackendModel
	m.fromAPI(b)
	if m.Port.ValueInt64() != 8080 || m.SourceID.ValueString() != "i-1" {
		t.Fatalf("basic fields wrong: %+v", m)
	}
	// The platform-resolved address is reported, which is what makes
	// source_kind = "instance" usable without the practitioner knowing the IP.
	if m.Address.ValueString() != "10.0.1.10" {
		t.Errorf("address = %v, want the resolved 10.0.1.10", m.Address)
	}
}

// TestAbsentSourceIDBecomesNull. For an address-kind backend the server omits
// sourceId, and reporting "" would give a permanent diff on the default kind.
func TestAbsentSourceIDBecomesNull(t *testing.T) {
	var m BackendModel
	m.fromAPI(&apiBackend{ID: "b-1", SourceKind: "address", Address: "10.0.1.10", Port: 80})
	if !m.SourceID.IsNull() {
		t.Fatalf("source_id = %v for an address backend, want null", m.SourceID)
	}
}
