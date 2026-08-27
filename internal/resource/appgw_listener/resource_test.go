package appgw_listener

import (
	"context"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/diag"
)

func TestListenerModelFromAPI(t *testing.T) {
	l := &apiListener{
		ID: "lsn-1", GatewayID: "agw-1", Name: "https", Protocol: "https", Port: 443,
		DefaultCertificateID: "cert-1", SNICertificateIDs: []string{"cert-2"},
		TLSMinVersion: "1.2", TLSCipherProfile: "modern", RedirectToHTTPS: false,
		AllowedCIDRs: []string{"0.0.0.0/0"}, GeoBlockMode: "deny", GeoCountries: []string{"RU"},
		RateLimitRPS: 200, Enabled: true, CreatedAt: "2026-08-01T00:00:00Z",
	}
	var m ListenerModel
	var diags diag.Diagnostics
	m.fromAPI(context.Background(), l, &diags)
	if diags.HasError() {
		t.Fatalf("diagnostics: %v", diags)
	}
	if m.Port.ValueInt64() != 443 || m.Protocol.ValueString() != "https" {
		t.Fatalf("basic fields wrong: %+v", m)
	}
	if m.RateLimitRPS.ValueInt64() != 200 {
		t.Errorf("rate_limit_rps = %v, want 200", m.RateLimitRPS)
	}
}

// TestAbsentCollectionsBecomeNullNotEmpty.
//
// 🔴 THE SERVER OMITS THESE FIELDS ENTIRELY WHEN EMPTY (`omitempty`), so an
// empty list and an absent one are indistinguishable on the wire. Returning an
// empty list for an absent one makes a configuration that never mentioned
// denied_cidrs show `[] -> null` on every single plan, for ever.
func TestAbsentCollectionsBecomeNullNotEmpty(t *testing.T) {
	var m ListenerModel
	var diags diag.Diagnostics
	m.fromAPI(context.Background(), &apiListener{ID: "lsn-1", Protocol: "http", Port: 80}, &diags)
	if diags.HasError() {
		t.Fatalf("diagnostics: %v", diags)
	}
	for name, v := range map[string]bool{
		"sni_certificate_ids": m.SNICertificateIDs.IsNull(),
		"allowed_cidrs":       m.AllowedCIDRs.IsNull(),
		"denied_cidrs":        m.DeniedCIDRs.IsNull(),
		"geo_countries":       m.GeoCountries.IsNull(),
	} {
		if !v {
			t.Errorf("%s is an empty list; it must be null when the server omitted it", name)
		}
	}
}

// TestOmittedWhenZeroIntegersBecomeNull. The rate limits are `omitempty` on the
// wire, so 0 and absent are the same bytes; reporting 0 gives a permanent diff
// to anyone who never set one.
func TestOmittedWhenZeroIntegersBecomeNull(t *testing.T) {
	if !optionalInt(0).IsNull() {
		t.Error("optionalInt(0) must be null")
	}
	if optionalInt(5).ValueInt64() != 5 {
		t.Error("optionalInt(5) must be 5")
	}
}
