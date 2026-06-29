package clicreds

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// oidcServer is an httptest server that plays the gateway cli-config endpoint,
// OIDC discovery, and the token endpoint for the refresh grant — enough for the
// FileSource.Refresh tests, which drive the shared oidc client's
// RefreshViaGateway against it. (The OIDC protocol itself is tested in the
// shared go.frostmoln.internal/oidc module; this mock only supports the
// provider's locked write-back / peer-rotation tests.)
type oidcServer struct {
	*httptest.Server
	clientID     string
	gotForm      map[string]string
	gotUserAgent string        // User-Agent seen on the token request
	tokenStatus  int           // override token endpoint status; 0 = 200
	tokenBody    string        // override token endpoint body
	tokenBlock   chan struct{} // if non-nil, /token stalls until this channel is closed
	issuerOverri string        // override the issuer returned by cli-config
	tokenEPOverr string        // override the token_endpoint returned by discovery
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
			// issuer mirrors the requested issuer so the shared Discover's
			// issuer-match check passes.
			_ = json.NewEncoder(w).Encode(map[string]string{"issuer": s.URL, "token_endpoint": tokenEP})
		case "/token":
			if s.tokenBlock != nil {
				<-s.tokenBlock // stall the grant; the test closes this on return (before httptest Close)
				return
			}
			s.gotUserAgent = r.Header.Get("User-Agent")
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
