package client

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/clicreds"
)

// authServer plays the API endpoint plus the OIDC refresh endpoints. The API
// path /v1/test accepts only "Bearer <validToken>"; refreshes rotate the valid
// token to "new-access".
type authServer struct {
	*httptest.Server
	validToken    string
	refreshCalls  int64
	failRefresh   bool   // token endpoint returns invalid_grant (dead refresh token)
	transientRef  bool   // token endpoint returns 503 (transient, non-OAuth error)
	omitExpiresIn bool   // token endpoint rotates the pair but returns no expires_in
	gotRefresh    string // refresh_token seen on the most recent token POST
}

func newAuthServer(t *testing.T) *authServer {
	t.Helper()
	s := &authServer{validToken: "new-access"}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/auth/cli-config":
			_ = json.NewEncoder(w).Encode(map[string]string{"issuer": s.URL, "clientId": "cli"})
		case "/.well-known/openid-configuration":
			_ = json.NewEncoder(w).Encode(map[string]string{"token_endpoint": s.URL + "/token"})
		case "/token":
			atomic.AddInt64(&s.refreshCalls, 1)
			_ = r.ParseForm()
			s.gotRefresh = r.Form.Get("refresh_token")
			if s.transientRef {
				w.WriteHeader(http.StatusServiceUnavailable) // no OAuth body → plain error
				return
			}
			if s.failRefresh {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
				return
			}
			grant := map[string]any{
				"access_token":  "new-access",
				"refresh_token": "new-refresh",
				"expires_in":    1800,
			}
			if s.omitExpiresIn { // a grant that rotates the pair but reports no lifetime
				delete(grant, "expires_in")
			}
			_ = json.NewEncoder(w).Encode(grant)
		case "/v1/test":
			if r.Header.Get("Authorization") != "Bearer "+s.validToken {
				w.WriteHeader(http.StatusUnauthorized)
				_ = json.NewEncoder(w).Encode(map[string]any{
					"error": map[string]string{"code": "AUTHENTICATION_REQUIRED", "message": "expired"},
				})
				return
			}
			_, _ = w.Write([]byte(`{"ok":true}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(s.Close)
	return s
}

// fmConfigSource writes a temp fm config and resolves its bearer FileSource.
func fmConfigSource(t *testing.T, body, contextName string) (*clicreds.FileSource, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	r, err := clicreds.Resolve(clicreds.Options{Path: path, Context: contextName})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if r.Bearer == nil {
		t.Fatalf("expected a bearer FileSource for config: %s", body)
	}
	return r.Bearer, path
}

func diskAccessToken(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	// Cheap extraction without depending on clicreds internals.
	return string(data)
}

// bearerConfig is a single-context fm config with the given tokens.
func bearerConfig(access, refresh string) string {
	return "current_context: default\ncontexts:\n  default:\n    credentials:\n      access_token: " + access + "\n      refresh_token: " + refresh + "\n"
}

// bearerConfigExpires is bearerConfig with an explicit access-token expiry — what
// a peer (`fm` or another provider instance) actually writes when it rotates the
// token, and what the freshness gate keys adoption on.
func bearerConfigExpires(access, refresh string, expiresAt int64) string {
	return bearerConfig(access, refresh) + "      expires_at: " + strconv.FormatInt(expiresAt, 10) + "\n"
}

func TestBearerReactiveRefreshOn401(t *testing.T) {
	s := newAuthServer(t)
	src, path := fmConfigSource(t, bearerConfig("stale-access", "r0"), "")
	c := NewClient(s.URL, "", WithTokenSource(TokenSourceConfig{
		AccessToken:  "stale-access",
		RefreshToken: "r0",
		ExpiresAt:    time.Now().Add(time.Hour).Unix(), // fresh → no proactive; force the 401 path
		Source:       src,
	}))

	resp, err := c.Get(context.Background(), "/v1/test", nil)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 after refresh-retry, got %d", resp.StatusCode)
	}
	if atomic.LoadInt64(&s.refreshCalls) != 1 {
		t.Errorf("expected exactly one refresh, got %d", s.refreshCalls)
	}
	if c.bearer.token() != "new-access" {
		t.Errorf("client did not adopt the new token, got %q", c.bearer.token())
	}
	// The rotated pair was written back to disk.
	if got := diskAccessToken(t, path); !strings.Contains(got, "new-access") || !strings.Contains(got, "new-refresh") {
		t.Errorf("rotated pair not persisted to disk:\n%s", got)
	}
}

func TestBearerProactiveRefresh(t *testing.T) {
	s := newAuthServer(t)
	src, _ := fmConfigSource(t, bearerConfig("stale-access", "r0"), "")
	c := NewClient(s.URL, "", WithTokenSource(TokenSourceConfig{
		AccessToken:  "stale-access",
		RefreshToken: "r0",
		ExpiresAt:    time.Now().Add(-time.Minute).Unix(), // expired → proactive refresh
		Source:       src,
	}))

	resp, err := c.Get(context.Background(), "/v1/test", nil)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	// Proactive refresh fires before the request, so the API never 401s.
	if atomic.LoadInt64(&s.refreshCalls) != 1 {
		t.Errorf("expected one proactive refresh, got %d", s.refreshCalls)
	}
}

// TestBearerInvalidGrantSurfacesRelogin: on the reactive 401 path, a dead
// refresh token (invalid_grant) surfaces the actionable ErrSessionExpired
// re-login diagnostic — NOT the opaque original 401 (which says nothing about
// `fm auth login`). This is the dominant path for a mid-apply token expiry.
func TestBearerInvalidGrantSurfacesRelogin(t *testing.T) {
	s := newAuthServer(t)
	s.failRefresh = true
	src, _ := fmConfigSource(t, bearerConfig("stale-access", "r0"), "")
	c := NewClient(s.URL, "", WithTokenSource(TokenSourceConfig{
		AccessToken:  "stale-access",
		RefreshToken: "r0",
		ExpiresAt:    time.Now().Add(time.Hour).Unix(), // fresh → no proactive; force the reactive 401 path
		Source:       src,
	}))

	_, err := c.Get(context.Background(), "/v1/test", nil)
	if !errors.Is(err, clicreds.ErrSessionExpired) {
		t.Fatalf("expected ErrSessionExpired re-login prompt, got %T %v", err, err)
	}
}

// TestBearerTransientRefreshFailureSurfacesOriginal401: a NON-dead refresh
// failure (transient 5xx, not invalid_grant) is not a re-login signal — the
// original 401 surfaces unchanged.
func TestBearerTransientRefreshFailureSurfacesOriginal401(t *testing.T) {
	s := newAuthServer(t)
	s.transientRef = true
	src, _ := fmConfigSource(t, bearerConfig("stale-access", "r0"), "")
	c := NewClient(s.URL, "", WithTokenSource(TokenSourceConfig{
		AccessToken:  "stale-access",
		RefreshToken: "r0",
		ExpiresAt:    time.Now().Add(time.Hour).Unix(),
		Source:       src,
	}))

	_, err := c.Get(context.Background(), "/v1/test", nil)
	if errors.Is(err, clicreds.ErrSessionExpired) {
		t.Fatalf("a transient refresh failure must not surface ErrSessionExpired, got %v", err)
	}
	apiErr, ok := err.(*APIError)
	if !ok || apiErr.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected the original 401 to surface, got %T %v", err, err)
	}
}

func TestBearerSingleFlightRefresh(t *testing.T) {
	s := newAuthServer(t)
	src, _ := fmConfigSource(t, bearerConfig("stale-access", "r0"), "")
	c := NewClient(s.URL, "", WithTokenSource(TokenSourceConfig{
		AccessToken:  "stale-access",
		RefreshToken: "r0",
		ExpiresAt:    time.Now().Add(-time.Minute).Unix(), // expired → all goroutines want a refresh
		Source:       src,
	}))

	const n = 20
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := c.Get(context.Background(), "/v1/test", nil)
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent Get failed: %v", err)
		}
	}
	if got := atomic.LoadInt64(&s.refreshCalls); got != 1 {
		t.Errorf("expected single-flight refresh (1 call), got %d", got)
	}
}

func TestBearerAdoptsPeerRotation(t *testing.T) {
	// The on-disk token has a strictly later expiry than what the client holds in
	// memory (a peer — another provider instance or `fm` — rotated it forward).
	// The client must adopt the on-disk token and NOT POST its stale one (which
	// would trip Zitadel reuse-detection).
	s := newAuthServer(t)
	s.validToken = "peer-access" // the API only accepts the peer's fresh token
	peerExpiry := time.Now().Add(time.Hour).Unix()
	src, _ := fmConfigSource(t, bearerConfigExpires("peer-access", "peer-refresh", peerExpiry), "")
	c := NewClient(s.URL, "", WithTokenSource(TokenSourceConfig{
		AccessToken:  "stale-access",                      // what this instance loaded earlier
		RefreshToken: "r0",                                // != on-disk "peer-refresh"
		ExpiresAt:    time.Now().Add(-time.Minute).Unix(), // expired (older than peer) → triggers refresh, adopts peer
		Source:       src,
	}))

	resp, err := c.Get(context.Background(), "/v1/test", nil)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 using the adopted peer token, got %d", resp.StatusCode)
	}
	if got := atomic.LoadInt64(&s.refreshCalls); got != 0 {
		t.Errorf("expected NO IdP POST (adopt on-disk token), got %d refresh calls", got)
	}
	if c.bearer.token() != "peer-access" {
		t.Errorf("expected adopted peer token, got %q", c.bearer.token())
	}
}

func TestBearerPersistFailureWarnsButSucceeds(t *testing.T) {
	// Grant succeeds but write-back fails (the resolved context vanished). The
	// request must still complete with the valid in-memory token (M1), not fail.
	s := newAuthServer(t)
	cfg := "current_context: default\ncredentials:\n  access_token: top-access\n  refresh_token: top-refresh\n" +
		"contexts:\n  default:\n    credentials:\n      access_token: top-access\n      refresh_token: top-refresh\n" +
		"  extra:\n    credentials:\n      access_token: extra-access\n      refresh_token: top-refresh\n"
	src, path := fmConfigSource(t, cfg, "extra")
	// Delete the "extra" context so write-back has nowhere to land.
	if err := os.WriteFile(path, []byte("current_context: default\ncredentials:\n  access_token: top-access\n  refresh_token: top-refresh\n"), 0o600); err != nil {
		t.Fatalf("rewrite: %v", err)
	}
	c := NewClient(s.URL, "", WithTokenSource(TokenSourceConfig{
		AccessToken:  "stale-access",
		RefreshToken: "top-refresh", // == on-disk → real grant runs
		ExpiresAt:    time.Now().Add(time.Hour).Unix(),
		Source:       src,
	}))

	resp, err := c.Get(context.Background(), "/v1/test", nil)
	if err != nil {
		t.Fatalf("expected success despite write-back failure, got: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
}

// TestBearerZeroExpiryGrantStaysMonotonic is the HIGH (OAuth/Zitadel review)
// regression: a successful grant that omits expires_in must not zero the
// in-memory expiry. If it did, after a write-back failure the next refresh's
// expiry gate would adopt the stale on-disk token (which still carries a real,
// larger expiry) and the refresh after that would re-POST the consumed token →
// Zitadel reuse-detection revokes the whole family. The fix carries the prior
// expiry forward so the gate stays monotone and the live token is POSTed.
func TestBearerZeroExpiryGrantStaysMonotonic(t *testing.T) {
	s := newAuthServer(t)
	s.omitExpiresIn = true
	// Resolve "extra", then delete it so write-back fails (vanished context):
	// disk keeps the consumed refresh token R0 with its real on-disk expiry 5000.
	cfg := "current_context: default\ncredentials:\n  access_token: top-access\n  refresh_token: R0\n  expires_at: 5000\n" +
		"contexts:\n  default:\n    credentials:\n      access_token: top-access\n      refresh_token: R0\n      expires_at: 5000\n" +
		"  extra:\n    credentials:\n      access_token: extra-access\n      refresh_token: R0\n      expires_at: 5000\n"
	src, path := fmConfigSource(t, cfg, "extra")
	if err := os.WriteFile(path, []byte("current_context: default\ncredentials:\n  access_token: top-access\n  refresh_token: R0\n  expires_at: 5000\n"), 0o600); err != nil {
		t.Fatalf("rewrite: %v", err)
	}

	b := &bearerSource{
		access:      "stale-access",
		refresh:     "R0",
		expiresAt:   5000,
		apiEndpoint: s.URL,
		httpClient:  s.Client(),
		src:         src,
		now:         time.Now,
	}

	// First refresh: grant rotates R0 → new-refresh but omits expires_in; the
	// write-back then fails (extra context gone). refreshLocked tolerates the
	// PersistError and advances the in-memory token.
	b.mu.Lock()
	err := b.refreshLocked(context.Background())
	b.mu.Unlock()
	if err != nil {
		t.Fatalf("first refresh should proceed despite write-back failure, got: %v", err)
	}
	if b.refresh != "new-refresh" {
		t.Fatalf("in-memory refresh not advanced, got %q", b.refresh)
	}
	if b.expiresAt == 0 {
		t.Fatal("in-memory expiry was zeroed by an expires_in-less grant — the gate will adopt the stale on-disk token on the next refresh")
	}

	// Second refresh: the gate must NOT adopt the consumed on-disk R0 (expiry
	// 5000, not > the carried-forward 5000); it must POST the live new-refresh.
	b.mu.Lock()
	_ = b.refreshLocked(context.Background())
	b.mu.Unlock()
	if s.gotRefresh != "new-refresh" {
		t.Fatalf("second refresh POSTed %q, want the live new-refresh — the consumed on-disk R0 must never be re-POSTed", s.gotRefresh)
	}
}

func TestAPIKeyPathNotRefreshed(t *testing.T) {
	// The X-API-Key path has no bearer source, so a 401 is returned as-is.
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"error": map[string]string{"code": "AUTHENTICATION_REQUIRED", "message": "bad key"},
		})
	}))
	defer server.Close()

	c := NewClient(server.URL, "some-key")
	_, err := c.Get(context.Background(), "/v1/test", nil)
	if err == nil {
		t.Fatal("expected a 401 error")
	}
	if calls != 1 {
		t.Errorf("expected a single attempt (no refresh-retry), got %d", calls)
	}
}
