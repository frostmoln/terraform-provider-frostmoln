package appgw_backend_pool

import (
	"testing"
)

func TestPoolModelFromAPI(t *testing.T) {
	p := &apiPool{
		ID: "pool-1", GatewayID: "agw-1", Name: "api", Protocol: "https",
		Algorithm: "round_robin", SessionAffinity: "cookie", SessionCookieName: "FMSESSION",
		TLSVerifyBackend: true, TLSServerName: "api.internal",
		TimeoutConnectMS: 2000, TimeoutResponseMS: 30000,
		CreatedAt: "2026-08-01T00:00:00Z",
	}
	var m PoolModel
	m.fromAPI(p)
	if m.Protocol.ValueString() != "https" || !m.TLSVerifyBackend.ValueBool() {
		t.Fatalf("basic fields wrong: %+v", m)
	}
	if m.SessionCookieName.ValueString() != "FMSESSION" {
		t.Errorf("session_cookie_name = %v", m.SessionCookieName)
	}
}

// TestAbsentTLSFieldsBecomeNull keeps an http pool's plan clean: the server
// omits the TLS strings entirely, and reporting "" for them would give a
// permanent diff to anyone who never set one.
func TestAbsentTLSFieldsBecomeNull(t *testing.T) {
	var m PoolModel
	m.fromAPI(&apiPool{ID: "pool-1", Name: "web", Protocol: "http"})
	if !m.TLSCACertificate.IsNull() || !m.TLSServerName.IsNull() || !m.SessionCookieName.IsNull() {
		t.Fatalf("absent optional strings must be null: %+v", m)
	}
}
