package clicreds

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"go.frostmoln.internal/oidc"
)

const sampleConfig = `api_endpoint: https://api.frostmoln.cloud/api
region: sweden
current_context: default
credentials:
  access_token: top-access
  refresh_token: top-refresh
  expires_at: 111
contexts:
  default:
    api_endpoint: https://api.frostmoln.cloud/api
    region: sweden
    credentials:
      access_token: default-access
      refresh_token: default-refresh
      expires_at: 222
  staging:
    api_endpoint: https://api.staging.frostmoln.cloud/api
    region: sweden
    credentials:
      api_key: fmk_staging_key
defaults:
  output_format: table
`

func writeConfigFile(t *testing.T, content string, mode os.FileMode) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatalf("write config: %v", err)
	}
	// WriteFile honors mode only on create; chmod to be sure across umasks.
	if err := os.Chmod(path, mode); err != nil {
		t.Fatalf("chmod config: %v", err)
	}
	return path
}

func TestResolveNotFound(t *testing.T) {
	_, err := Resolve(Options{Path: filepath.Join(t.TempDir(), "nope.yaml")})
	if err != ErrNotFound {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestResolveCurrentContextBearer(t *testing.T) {
	path := writeConfigFile(t, sampleConfig, 0o600)
	r, err := Resolve(Options{Path: path})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if r.AccessToken != "default-access" || r.RefreshToken != "default-refresh" || r.ExpiresAt != 222 {
		t.Errorf("expected default context creds, got %+v", r)
	}
	if r.APIKey != "" {
		t.Errorf("expected no api key, got %q", r.APIKey)
	}
	if r.APIEndpoint != "https://api.frostmoln.cloud/api" {
		t.Errorf("expected /api endpoint, got %q", r.APIEndpoint)
	}
	if r.Bearer == nil {
		t.Error("expected a Bearer FileSource for the bearer path")
	}
}

func TestResolveContextOverrideAPIKey(t *testing.T) {
	path := writeConfigFile(t, sampleConfig, 0o600)
	r, err := Resolve(Options{Path: path, Context: "staging"})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if r.APIKey != "fmk_staging_key" { // pragma: allowlist secret
		t.Errorf("expected staging api key, got %q", r.APIKey)
	}
	if r.AccessToken != "" {
		t.Errorf("expected no access token, got %q", r.AccessToken)
	}
	if r.APIEndpoint != "https://api.staging.frostmoln.cloud/api" {
		t.Errorf("expected staging endpoint, got %q", r.APIEndpoint)
	}
	if r.Bearer != nil {
		t.Error("expected no Bearer for the api-key path (api keys never rotate)")
	}
}

func TestResolveMissingContextErrors(t *testing.T) {
	path := writeConfigFile(t, sampleConfig, 0o600)
	if _, err := Resolve(Options{Path: path, Context: "ghost"}); err == nil {
		t.Fatal("expected error for an explicitly-requested missing context")
	}
}

func TestResolveFlatConfig(t *testing.T) {
	flat := "api_endpoint: https://api.example.com/api\ncredentials:\n  api_key: fmk_flat\n"
	path := writeConfigFile(t, flat, 0o600)
	r, err := Resolve(Options{Path: path})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if r.APIKey != "fmk_flat" { // pragma: allowlist secret
		t.Errorf("expected flat api key, got %q", r.APIKey)
	}
	if r.APIEndpoint != "https://api.example.com/api" {
		t.Errorf("expected flat endpoint, got %q", r.APIEndpoint)
	}
}

func TestResolvePermsWarning(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX file modes not meaningful on Windows")
	}
	path := writeConfigFile(t, sampleConfig, 0o644)
	r, err := Resolve(Options{Path: path})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if r.PermsWarning == "" {
		t.Error("expected a perms warning for a 0644 config file")
	}

	tight := writeConfigFile(t, sampleConfig, 0o600)
	r2, err := Resolve(Options{Path: tight})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if r2.PermsWarning != "" {
		t.Errorf("expected no warning for 0600, got %q", r2.PermsWarning)
	}
}

func TestRefreshWritesBackPreservingFields(t *testing.T) {
	s := newOIDCServer(t)
	path := writeConfigFile(t, sampleConfig, 0o600)
	r, err := Resolve(Options{Path: path})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	// In-memory expiry == on-disk expiry, so the freshness gate does not adopt
	// and a real grant runs and rotates.
	tok, err := r.Bearer.Refresh(context.Background(), s.Client(), s.URL, r.RefreshToken, r.ExpiresAt)
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if tok.AccessToken != "fresh-access" {
		t.Errorf("expected rotated access token, got %q", tok.AccessToken)
	}
	// The mock's relative expires_in (1800s) maps to an absolute Unix timestamp
	// ~now+1800 — the conversion this migration introduced (oidc.Token.ExpiresAt()
	// -> clicreds.Token.ExpiresAt). The lower bound also catches a regression to
	// an unconverted/relative value.
	now := time.Now().Unix()
	if tok.ExpiresAt < now+1700 || tok.ExpiresAt > now+1800 {
		t.Errorf("ExpiresAt = %d, want ~now+1800 (%d)", tok.ExpiresAt, now+1800)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	var cfg cliConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("parse back: %v", err)
	}

	// The on-disk expiry must equal the returned one (computed once, not twice).
	if got := cfg.Contexts["default"].Credentials.ExpiresAt; got != tok.ExpiresAt {
		t.Errorf("on-disk expires_at %d != returned %d", got, tok.ExpiresAt)
	}
	if c := cfg.Contexts["default"].Credentials; c.AccessToken != "fresh-access" || c.RefreshToken != "fresh-refresh" {
		t.Errorf("default context creds not updated: %+v", c)
	}
	if cfg.Credentials.AccessToken != "fresh-access" || cfg.Credentials.RefreshToken != "fresh-refresh" {
		t.Errorf("top-level mirror not updated: %+v", cfg.Credentials)
	}
	if cfg.Contexts["staging"].Credentials.APIKey != "fmk_staging_key" { // pragma: allowlist secret
		t.Errorf("staging context clobbered: %+v", cfg.Contexts["staging"])
	}
	if cfg.Region != "sweden" || cfg.Defaults["output_format"] != "table" {
		t.Errorf("non-credential fields not preserved: region=%q defaults=%v", cfg.Region, cfg.Defaults)
	}
	if runtime.GOOS != "windows" {
		fi, _ := os.Stat(path)
		if fi.Mode().Perm() != 0o600 {
			t.Errorf("expected mode 0600 after write, got %#o", fi.Mode().Perm())
		}
	}
}

// TestRefreshInvalidGrantIsDead pins the cross-module contract: when the token
// endpoint rejects the refresh with invalid_grant, Refresh surfaces an error
// that IsRefreshTokenDead classifies as a dead refresh token (so refreshLocked
// can return the re-login diagnostic). A predicate-only unit test wouldn't catch
// the shared module ceasing to return a typed *OAuthError.
func TestRefreshInvalidGrantIsDead(t *testing.T) {
	s := newOIDCServer(t)
	s.tokenStatus = http.StatusBadRequest
	s.tokenBody = `{"error":"invalid_grant"}`
	path := writeConfigFile(t, sampleConfig, 0o600)
	r, err := Resolve(Options{Path: path})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	_, err = r.Bearer.Refresh(context.Background(), s.Client(), s.URL, r.RefreshToken, r.ExpiresAt)
	if !IsRefreshTokenDead(err) {
		t.Fatalf("expected a dead refresh token (invalid_grant), got %v", err)
	}
}

// TestRefreshSendsUserAgent: a UserAgent set on the source is stamped on the
// OIDC refresh request; the zero value sends none.
func TestRefreshSendsUserAgent(t *testing.T) {
	s := newOIDCServer(t)
	path := writeConfigFile(t, sampleConfig, 0o600)

	r, err := Resolve(Options{Path: path, UserAgent: "terraform-provider-frostmoln/9.9.9"})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if _, err := r.Bearer.Refresh(context.Background(), s.Client(), s.URL, r.RefreshToken, r.ExpiresAt); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if s.gotUserAgent != "terraform-provider-frostmoln/9.9.9" {
		t.Errorf("User-Agent = %q, want terraform-provider-frostmoln/9.9.9", s.gotUserAgent)
	}

	r2, _ := Resolve(Options{Path: writeConfigFile(t, sampleConfig, 0o600)}) // no UserAgent
	if _, err := r2.Bearer.Refresh(context.Background(), s.Client(), s.URL, r2.RefreshToken, r2.ExpiresAt); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if strings.HasPrefix(s.gotUserAgent, "terraform-provider-frostmoln/") {
		t.Errorf("empty UserAgent should send no provider UA, got %q", s.gotUserAgent)
	}
}

func TestRefreshNonCurrentContextLeavesTopLevel(t *testing.T) {
	// Refreshing a non-current context must NOT touch the top-level mirror
	// (which fm-cli keeps pointed at the current context).
	s := newOIDCServer(t)
	cfg2 := `current_context: default
credentials:
  access_token: cur-access
  refresh_token: cur-refresh
contexts:
  default:
    credentials:
      access_token: cur-access
      refresh_token: cur-refresh
  extra:
    credentials:
      access_token: extra-access
      refresh_token: extra-refresh
`
	path := writeConfigFile(t, cfg2, 0o600)
	r, err := Resolve(Options{Path: path, Context: "extra"})
	if err != nil {
		t.Fatalf("Resolve extra: %v", err)
	}
	if r.Bearer == nil {
		t.Fatal("expected Bearer for the extra context")
	}
	if _, err := r.Bearer.Refresh(context.Background(), s.Client(), s.URL, r.RefreshToken, r.ExpiresAt); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	data, _ := os.ReadFile(path)
	var got cliConfig
	_ = yaml.Unmarshal(data, &got)
	if got.Contexts["extra"].Credentials.AccessToken != "fresh-access" {
		t.Errorf("extra context not updated: %+v", got.Contexts["extra"].Credentials)
	}
	if got.Credentials.AccessToken != "cur-access" {
		t.Errorf("top-level mirror clobbered by non-current refresh: %q", got.Credentials.AccessToken)
	}
}

func TestRefreshAdoptsPeerRotationWithoutPOST(t *testing.T) {
	// A token endpoint that always 500s — proving Refresh does NOT POST when the
	// on-disk token has a strictly later expiry than the caller holds (a peer
	// rotated it forward).
	s := newOIDCServer(t)
	s.tokenStatus = http.StatusInternalServerError
	s.tokenBody = `{"error":"server_error"}`
	path := writeConfigFile(t, sampleConfig, 0o600)
	r, err := Resolve(Options{Path: path})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	// Caller's in-memory expiry (100) is older than the on-disk one (222) → the
	// freshness gate adopts the on-disk pair, no POST.
	tok, err := r.Bearer.Refresh(context.Background(), s.Client(), s.URL, "stale-old-refresh", 100)
	if err != nil {
		t.Fatalf("expected adoption without POST, got error: %v", err)
	}
	if tok.AccessToken != "default-access" || tok.RefreshToken != "default-refresh" {
		t.Errorf("expected adopted on-disk token, got %+v", tok)
	}
}

func TestRefreshVanishedContextReturnsPersistError(t *testing.T) {
	// Grant succeeds but the resolved context disappeared before write-back:
	// Refresh must return the valid token AND a *PersistError (never silent
	// success), so the caller can proceed-and-warn rather than lose the rotation.
	s := newOIDCServer(t)
	cfg := `current_context: default
credentials:
  access_token: top-access
  refresh_token: top-refresh
contexts:
  default:
    credentials:
      access_token: top-access
      refresh_token: top-refresh
  extra:
    credentials:
      access_token: extra-access
      refresh_token: top-refresh
`
	path := writeConfigFile(t, cfg, 0o600)
	r, err := Resolve(Options{Path: path, Context: "extra"})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	// Delete the "extra" context from the file before write-back.
	if err := os.WriteFile(path, []byte("current_context: default\ncredentials:\n  access_token: top-access\n  refresh_token: top-refresh\n"), 0o600); err != nil {
		t.Fatalf("rewrite: %v", err)
	}
	tok, err := r.Bearer.Refresh(context.Background(), s.Client(), s.URL, "top-refresh", r.ExpiresAt)
	var perr *PersistError
	if err == nil || !errors.As(err, &perr) {
		t.Fatalf("expected *PersistError, got %v", err)
	}
	if tok == nil || tok.AccessToken != "fresh-access" {
		t.Errorf("expected a valid token alongside the persist error, got %+v", tok)
	}
}

// TestRefreshAfterPersistFailurePOSTsLiveToken is the HIGH-1 regression: after a
// grant R->R' succeeds but the write-back fails, the in-memory token advances to
// R' while disk still holds the consumed R. The NEXT refresh must POST the live
// R' — never adopt+re-POST the stale on-disk R, which Zitadel reuse-detection
// would reject, revoking the whole token family and forcing `fm auth login`.
func TestRefreshAfterPersistFailurePOSTsLiveToken(t *testing.T) {
	s := newOIDCServer(t)
	// Resolve the "extra" context, then delete it from disk (the same
	// vanished-context mechanism TestRefreshVanishedContextReturnsPersistError /
	// TestBearerPersistFailureWarnsButSucceeds use): the grant succeeds but
	// write-back lands nowhere, so disk keeps the consumed refresh token.
	cfg := `current_context: default
credentials:
  access_token: top-access
  refresh_token: consumed-refresh
  expires_at: 100
contexts:
  default:
    credentials:
      access_token: top-access
      refresh_token: consumed-refresh
      expires_at: 100
  extra:
    credentials:
      access_token: extra-access
      refresh_token: consumed-refresh
      expires_at: 100
`
	path := writeConfigFile(t, cfg, 0o600)
	r, err := Resolve(Options{Path: path, Context: "extra"})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if err := os.WriteFile(path, []byte("current_context: default\ncredentials:\n  access_token: top-access\n  refresh_token: consumed-refresh\n  expires_at: 100\n"), 0o600); err != nil {
		t.Fatalf("rewrite: %v", err)
	}

	// First refresh: grant succeeds (POSTs the on-disk consumed-refresh), but the
	// write-back fails. Capture the live rotated token.
	tok, err := r.Bearer.Refresh(context.Background(), s.Client(), s.URL, "consumed-refresh", 100)
	var perr *PersistError
	if err == nil || !errors.As(err, &perr) {
		t.Fatalf("expected *PersistError from the failed write-back, got %v", err)
	}
	liveRefresh := tok.RefreshToken
	if liveRefresh != "fresh-refresh" {
		t.Fatalf("grant did not rotate the refresh token, got %q", liveRefresh)
	}

	// Second refresh: in-memory holds the live R' (and its newer expiry); disk
	// still holds the consumed R with the OLD expiry (100). The freshness gate
	// must NOT adopt the stale disk token, and must POST the live R'.
	if _, err := r.Bearer.Refresh(context.Background(), s.Client(), s.URL, liveRefresh, tok.ExpiresAt); err != nil {
		if !errors.As(err, &perr) { // a repeated write-back PersistError is expected and fine
			t.Fatalf("second refresh: unexpected error: %v", err)
		}
	}
	if got := s.gotForm["refresh_token"]; got != liveRefresh {
		t.Fatalf("second refresh POSTed %q, want the live %q — the consumed on-disk token must never be re-POSTed", got, liveRefresh)
	}
}

// TestRefreshBoundsLockHoldBelowStale is the MED-3/LOW-5 regression: a stalled
// IdP must not let the file lock outlive lockStaleAfter. The grant is bounded by
// lockMaxRefresh (< lockStaleAfter) even with a timeout-less HTTP client, so the
// lock is released before a peer treats it as orphaned (which would let both
// POST the same token → reuse-detection trip). Without the bound this test hangs
// until the go-test timeout.
func TestRefreshBoundsLockHoldBelowStale(t *testing.T) {
	oldMax, oldStale := lockMaxRefresh, lockStaleAfter
	lockMaxRefresh = 100 * time.Millisecond
	lockStaleAfter = 5 * time.Second
	defer func() { lockMaxRefresh, lockStaleAfter = oldMax, oldStale }()

	s := newOIDCServer(t)
	block := make(chan struct{})
	s.tokenBlock = block
	defer close(block) // release the stalled handler when the test returns, before httptest Close
	path := writeConfigFile(t, sampleConfig, 0o600)
	r, err := Resolve(Options{Path: path})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	start := time.Now()
	// A timeout-less client (LOW-5): only the lockMaxRefresh context bound stops
	// the stalled token endpoint from holding the lock until lockStaleAfter.
	_, err = r.Bearer.Refresh(context.Background(), &http.Client{}, s.URL, r.RefreshToken, r.ExpiresAt)
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("expected the stalled grant to fail, got nil")
	}
	if elapsed >= lockStaleAfter {
		t.Fatalf("refresh held the lock %v; must release before lockStaleAfter (%v)", elapsed, lockStaleAfter)
	}
	// Prove the named property directly: the lock is actually released (not just
	// that Refresh returned), so a peer can re-acquire it immediately.
	unlock, lerr := lockFile(path)
	if lerr != nil {
		t.Fatalf("lock not released after the bounded refresh: %v", lerr)
	}
	unlock()
}

// TestRefreshRetriesTransientThenSucceeds is the MED-4 happy path: a single
// transient token-endpoint blip (a bare gateway 503, no OAuth body) is retried
// once and the refresh recovers. Exactly one retry — two /token hits. The
// in-memory expiry equals the on-disk one, so the freshness gate does not adopt
// and a real grant runs.
func TestRefreshRetriesTransientThenSucceeds(t *testing.T) {
	old := refreshBackoff
	refreshBackoff = time.Millisecond
	defer func() { refreshBackoff = old }()

	s := newOIDCServer(t)
	s.tokenStatus = http.StatusServiceUnavailable
	s.tokenFailN = 1 // 503 once (bare body -> "token endpoint HTTP 503"), then succeed
	path := writeConfigFile(t, sampleConfig, 0o600)
	r, err := Resolve(Options{Path: path})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	tok, err := r.Bearer.Refresh(context.Background(), s.Client(), s.URL, r.RefreshToken, r.ExpiresAt)
	if err != nil {
		t.Fatalf("expected the retry to recover, got %v", err)
	}
	if tok.AccessToken != "fresh-access" {
		t.Errorf("expected rotated token after retry, got %q", tok.AccessToken)
	}
	if s.tokenHits != 2 {
		t.Errorf("expected exactly one retry (2 token hits), got %d", s.tokenHits)
	}
}

// TestRefreshRetriesTemporarilyUnavailable covers the typed retryable branch: an
// explicit temporarily_unavailable OAuth error means the IdP did not process the
// request, so it is retried once.
func TestRefreshRetriesTemporarilyUnavailable(t *testing.T) {
	old := refreshBackoff
	refreshBackoff = time.Millisecond
	defer func() { refreshBackoff = old }()

	s := newOIDCServer(t)
	s.tokenStatus = http.StatusServiceUnavailable
	s.tokenBody = `{"error":"temporarily_unavailable"}`
	s.tokenFailN = 1
	path := writeConfigFile(t, sampleConfig, 0o600)
	r, err := Resolve(Options{Path: path})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if _, err := r.Bearer.Refresh(context.Background(), s.Client(), s.URL, r.RefreshToken, r.ExpiresAt); err != nil {
		t.Fatalf("expected temporarily_unavailable to be retried, got %v", err)
	}
	if s.tokenHits != 2 {
		t.Errorf("expected exactly one retry (2 token hits), got %d", s.tokenHits)
	}
}

// TestRefreshDoesNotRetryServerError pins the consumed-token guard: server_error
// is ambiguous (the grant may have partially consumed the refresh token), so it
// must NOT be retried — a re-POST could trip Zitadel reuse-detection. One hit.
func TestRefreshDoesNotRetryServerError(t *testing.T) {
	old := refreshBackoff
	refreshBackoff = time.Millisecond
	defer func() { refreshBackoff = old }()

	s := newOIDCServer(t)
	s.tokenStatus = http.StatusInternalServerError
	s.tokenBody = `{"error":"server_error"}`
	path := writeConfigFile(t, sampleConfig, 0o600)
	r, err := Resolve(Options{Path: path})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if _, err := r.Bearer.Refresh(context.Background(), s.Client(), s.URL, r.RefreshToken, r.ExpiresAt); err == nil {
		t.Fatal("expected server_error to be terminal, got nil")
	}
	if s.tokenHits != 1 {
		t.Errorf("server_error must NOT be retried; got %d token hits", s.tokenHits)
	}
}

// TestRefreshDoesNotRetryInvalidGrant: a dead refresh token (invalid_grant) is
// terminal — retrying would re-POST a token reuse-detection already flagged.
func TestRefreshDoesNotRetryInvalidGrant(t *testing.T) {
	old := refreshBackoff
	refreshBackoff = time.Millisecond
	defer func() { refreshBackoff = old }()

	s := newOIDCServer(t)
	s.tokenStatus = http.StatusBadRequest
	s.tokenBody = `{"error":"invalid_grant"}`
	path := writeConfigFile(t, sampleConfig, 0o600)
	r, err := Resolve(Options{Path: path})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	_, err = r.Bearer.Refresh(context.Background(), s.Client(), s.URL, r.RefreshToken, r.ExpiresAt)
	if !IsRefreshTokenDead(err) {
		t.Fatalf("expected a dead refresh token, got %v", err)
	}
	if s.tokenHits != 1 {
		t.Errorf("invalid_grant must NOT be retried; got %d token hits", s.tokenHits)
	}
}

// TestIsRetryable pins the classification directly — notably the context-error
// exclusion (a grant cancelled / past lockMaxRefresh must NOT loop-retry), which
// the HTTP-driven tests above do not reach.
func TestIsRetryable(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"temporarily_unavailable", &oidc.OAuthError{Code: "temporarily_unavailable"}, true},
		{"server_error", &oidc.OAuthError{Code: "server_error"}, false},
		{"invalid_grant", &oidc.OAuthError{Code: "invalid_grant"}, false},
		{"pre-response transport", errors.New("dial tcp: connection refused"), true},
		// Post-consumption residuals (bare 5xx, missing-token 2xx, decode failure)
		// are retried BY DESIGN — safe via the monotonic-benefit invariant; see the
		// IsRetryable doc. Pinned so the behavior is deliberate, not incidental.
		{"bare 5xx (post-consumption residual)", errors.New("token endpoint HTTP 502"), true},
		// A 426 version gate is deterministic — fail fast, do not burn a retry.
		{"version gate 426", &oidc.UpgradeRequiredError{Msg: "upgrade fm"}, false},
		{"context canceled", context.Canceled, false},
		{"wrapped deadline", fmt.Errorf("grant: %w", context.DeadlineExceeded), false},
		{"nil", nil, false},
	}
	for _, tt := range tests {
		if got := IsRetryable(tt.err); got != tt.want {
			t.Errorf("IsRetryable(%s) = %v, want %v", tt.name, got, tt.want)
		}
	}
}

func TestLockFileExclusive(t *testing.T) {
	// Shrink the timeout so the contended path fails fast.
	old := lockTimeout
	lockTimeout = 150 * time.Millisecond
	defer func() { lockTimeout = old }()

	path := filepath.Join(t.TempDir(), "config.yaml")
	unlock, err := lockFile(path)
	if err != nil {
		t.Fatalf("first lock: %v", err)
	}
	// Second acquisition must time out while the first is held.
	if _, err := lockFile(path); err == nil {
		t.Error("expected second lock to fail while held")
	}
	unlock()
	// After release it must succeed again.
	unlock2, err := lockFile(path)
	if err != nil {
		t.Fatalf("lock after release: %v", err)
	}
	unlock2()
}
