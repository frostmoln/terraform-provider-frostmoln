package clicreds

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// oidcServer is an httptest server that plays the gateway cli-config endpoint,
// OIDC discovery, and the token endpoint for the refresh grant.
type oidcServer struct {
	*httptest.Server
	clientID     string
	gotForm      map[string]string
	tokenStatus  int    // override token endpoint status; 0 = 200
	tokenBody    string // override token endpoint body
	issuerOverri string // override the issuer returned by cli-config
	tokenEPOverr string // override the token_endpoint returned by discovery
}

func newOIDCServer(t *testing.T) *oidcServer {
	t.Helper()
	s := &oidcServer{clientID: "cli-client"}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/auth/cli-config":
			issuer := s.URL
			if s.issuerOverri != "" {
				issuer = s.issuerOverri
			}
			_ = json.NewEncoder(w).Encode(map[string]string{"issuer": issuer, "clientId": s.clientID})
		case "/.well-known/openid-configuration":
			tokenEP := s.URL + "/token"
			if s.tokenEPOverr != "" {
				tokenEP = s.tokenEPOverr
			}
			_ = json.NewEncoder(w).Encode(map[string]string{"issuer": s.URL, "token_endpoint": tokenEP})
		case "/token":
			_ = r.ParseForm()
			s.gotForm = map[string]string{}
			for k := range r.Form {
				s.gotForm[k] = r.Form.Get(k)
			}
			if s.tokenStatus != 0 {
				w.WriteHeader(s.tokenStatus)
				_, _ = w.Write([]byte(s.tokenBody))
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token":  "fresh-access",
				"refresh_token": "fresh-refresh",
				"expires_in":    1800,
			})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(s.Close)
	return s
}

func TestRefreshRoundTrip(t *testing.T) {
	s := newOIDCServer(t)
	tok, err := Refresh(context.Background(), s.Client(), s.URL, "old-refresh")
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if tok.AccessToken != "fresh-access" || tok.RefreshToken != "fresh-refresh" {
		t.Errorf("unexpected token: %+v", tok)
	}
	if tok.ExpiresAt <= time.Now().Unix() {
		t.Errorf("expected a future expiry, got %d", tok.ExpiresAt)
	}
	// The grant carried the right parameters.
	if s.gotForm["grant_type"] != "refresh_token" || s.gotForm["refresh_token"] != "old-refresh" ||
		s.gotForm["client_id"] != "cli-client" {
		t.Errorf("unexpected refresh form: %+v", s.gotForm)
	}
	if !strings.Contains(s.gotForm["scope"], "offline_access") {
		t.Errorf("expected offline_access scope, got %q", s.gotForm["scope"])
	}
}

func TestRefreshNoRotation(t *testing.T) {
	s := newOIDCServer(t)
	s.tokenStatus = http.StatusOK
	s.tokenBody = `{"access_token":"a","expires_in":1800}` // no refresh_token
	tok, err := Refresh(context.Background(), s.Client(), s.URL, "old-refresh")
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if tok.RefreshToken != "" {
		t.Errorf("expected empty refresh token on no-rotation, got %q", tok.RefreshToken)
	}
}

func TestRefreshOAuthErrorHidesToken(t *testing.T) {
	s := newOIDCServer(t)
	s.tokenStatus = http.StatusBadRequest
	s.tokenBody = `{"error":"invalid_grant","error_description":"token expired"}`
	_, err := Refresh(context.Background(), s.Client(), s.URL, "super-secret-refresh")
	if err == nil {
		t.Fatal("expected error for invalid_grant")
	}
	if !strings.Contains(err.Error(), "invalid_grant") {
		t.Errorf("expected invalid_grant in error, got %v", err)
	}
	if strings.Contains(err.Error(), "super-secret-refresh") {
		t.Errorf("error leaked the refresh token: %v", err)
	}
}

func TestRefreshMissingClientID(t *testing.T) {
	s := newOIDCServer(t)
	s.clientID = ""
	if _, err := Refresh(context.Background(), s.Client(), s.URL, "r"); err == nil {
		t.Fatal("expected error when cli-config has no clientId")
	}
}

func TestRefreshRejectsInsecureTokenEndpoint(t *testing.T) {
	s := newOIDCServer(t)
	// Point discovery at a non-loopback http token endpoint.
	s.tokenEPOverr = "http://evil.example.com/token"
	_, err := Refresh(context.Background(), s.Client(), s.URL, "r")
	if err == nil || !strings.Contains(err.Error(), "insecure") {
		t.Fatalf("expected insecure-endpoint rejection, got %v", err)
	}
}

func TestSecureURL(t *testing.T) {
	cases := map[string]bool{
		"https://api.frostmoln.cloud/token": true,
		"http://127.0.0.1:8080/token":       true,
		"http://localhost/token":            true,
		"http://example.com/token":          false,
		"ftp://example.com":                 false,
		"://nonsense":                       false,
	}
	for raw, want := range cases {
		if got := SecureURL(raw); got != want {
			t.Errorf("SecureURL(%q) = %v, want %v", raw, got, want)
		}
	}
}
