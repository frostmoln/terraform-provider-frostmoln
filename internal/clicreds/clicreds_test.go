package clicreds

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
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
	// On-disk refresh token == lastSeen, so a real grant runs and rotates.
	tok, err := r.Bearer.Refresh(context.Background(), s.Client(), s.URL, r.RefreshToken)
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if tok.AccessToken != "fresh-access" {
		t.Errorf("expected rotated access token, got %q", tok.AccessToken)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	var cfg cliConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("parse back: %v", err)
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
	if _, err := r.Bearer.Refresh(context.Background(), s.Client(), s.URL, r.RefreshToken); err != nil {
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
	// on-disk refresh token differs from the caller's (a peer already rotated).
	s := newOIDCServer(t)
	s.tokenStatus = http.StatusInternalServerError
	s.tokenBody = `{"error":"server_error"}`
	path := writeConfigFile(t, sampleConfig, 0o600)
	r, err := Resolve(Options{Path: path})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	// Caller holds an older token than what's on disk → adopt disk, no POST.
	tok, err := r.Bearer.Refresh(context.Background(), s.Client(), s.URL, "stale-old-refresh")
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
	tok, err := r.Bearer.Refresh(context.Background(), s.Client(), s.URL, "top-refresh")
	var perr *PersistError
	if err == nil || !errors.As(err, &perr) {
		t.Fatalf("expected *PersistError, got %v", err)
	}
	if tok == nil || tok.AccessToken != "fresh-access" {
		t.Errorf("expected a valid token alongside the persist error, got %+v", tok)
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
