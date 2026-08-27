package application_gateway

import (
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/types"
)

func TestGatewayModelFromAPI(t *testing.T) {
	rev := int64(7)
	g := &apiGateway{
		ID: "agw-1", Name: "edge", TenantID: "t-1", Status: "running",
		FlavorID: "agw.gp1.small", Version: "1.0.0",
		VPCID: "vpc-1", SubnetID: "sub-1", PrivateIP: "10.0.0.5", VPCCIDR: "10.0.0.0/16",
		PublicIPMode: "allocated", PublicIP: "203.0.113.10",
		ConfigGeneration: 7, ConfigRevision: &rev, ConfigStatus: "applied",
		CreatedAt: "2026-08-01T00:00:00Z",
	}
	var m GatewayModel
	m.fromAPI(g)

	if m.ID.ValueString() != "agw-1" || m.Status.ValueString() != "running" {
		t.Fatalf("basic fields wrong: %+v", m)
	}
	if m.ConfigRevision.ValueInt64() != 7 {
		t.Errorf("config_revision = %v, want 7", m.ConfigRevision)
	}
}

// TestPublicIPIDIsNullForAPoolAllocatedGateway.
//
// 🔴 EMPTY MUST BECOME NULL, NOT "". public_ip_id is a plain Optional
// attribute, and the server deliberately leaves it empty for a pool-allocated
// gateway. Writing types.StringValue("") would make every subsequent plan show
// `public_ip_id: "" -> null` for ever, on the default configuration.
func TestPublicIPIDIsNullForAPoolAllocatedGateway(t *testing.T) {
	var m GatewayModel
	m.fromAPI(&apiGateway{ID: "agw-1", PublicIPMode: "allocated", PublicIP: "203.0.113.10"})
	if !m.PublicIPID.IsNull() {
		t.Fatalf("public_ip_id = %q for a pool-allocated gateway; it must be null so a plain "+
			"Optional attribute round-trips an absent configuration", m.PublicIPID.ValueString())
	}

	// A bring-your-own address DOES echo back, so the attribute round-trips
	// what the practitioner configured.
	m.fromAPI(&apiGateway{ID: "agw-2", PublicIPMode: "selected", PublicIPID: "pip-7"})
	if m.PublicIPID.ValueString() != "pip-7" {
		t.Fatalf("public_ip_id = %v for a bring-your-own gateway, want pip-7", m.PublicIPID)
	}
}

// TestConfigRevisionIsNullWhenNothingHasBeenApplied.
//
// Null and zero are different facts here: revision 0 is a real generation, and
// reporting 0 for "never applied" would make a gateway that has never served a
// configuration look like one serving generation 0.
func TestConfigRevisionIsNullWhenNothingHasBeenApplied(t *testing.T) {
	var m GatewayModel
	m.fromAPI(&apiGateway{ID: "agw-1", ConfigGeneration: 3, ConfigRevision: nil})
	if !m.ConfigRevision.IsNull() {
		t.Fatalf("config_revision = %v, want null when nothing has been applied", m.ConfigRevision)
	}

	zero := int64(0)
	m.fromAPI(&apiGateway{ID: "agw-1", ConfigRevision: &zero})
	if m.ConfigRevision.IsNull() || m.ConfigRevision.ValueInt64() != 0 {
		t.Fatalf("config_revision = %v, want a real 0", m.ConfigRevision)
	}
}

func TestOptionalString(t *testing.T) {
	if !optionalString("").IsNull() {
		t.Error(`optionalString("") must be null`)
	}
	if got := optionalString("x"); got != types.StringValue("x") {
		t.Errorf("optionalString(\"x\") = %v", got)
	}
}
