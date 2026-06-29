// Package clicreds reads the fm CLI's ~/.fm/config.yaml as a fallback
// credential source for the Terraform provider, and persists OIDC tokens
// refreshed by the provider back to that file so an existing `fm` session stays
// valid. It mirrors the CLI config schema
// (go.frostmoln.internal/fm-cli/internal/config) rather than importing it —
// that package is internal to a different module.
package clicreds

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"gopkg.in/yaml.v3"

	"go.frostmoln.internal/oidc"
)

// Token is a refreshed token set the provider persists. RefreshToken is empty
// when the IdP did not rotate it (the caller keeps the previous one). ExpiresAt
// is the access-token expiry as an absolute Unix timestamp (seconds), 0 if
// unknown — distinct from the shared oidc.Token's relative expires_in wire shape.
type Token struct {
	AccessToken  string
	RefreshToken string
	ExpiresAt    int64
}

// ErrNotFound is returned by Resolve when no config file exists at the resolved
// path. The provider treats it as "no CLI credential available" and falls
// through to its missing-credentials error rather than failing hard.
var ErrNotFound = errors.New("fm CLI config file not found")

type cliCredentials struct {
	AccessToken  string `yaml:"access_token,omitempty"`
	RefreshToken string `yaml:"refresh_token,omitempty"`
	ExpiresAt    int64  `yaml:"expires_at,omitempty"`
	APIKey       string `yaml:"api_key,omitempty"`
}

type cliContext struct {
	APIEndpoint string         `yaml:"api_endpoint,omitempty"`
	Region      string         `yaml:"region,omitempty"`
	Credentials cliCredentials `yaml:"credentials"`
}

// cliConfig mirrors fm-cli's config.Config.
//
// ponytail: typed round-trip matches fm-cli's own Save(), which likewise drops
// unknown/future keys and comments. Switch to a yaml.Node round-trip only if
// the CLI grows fields the provider must preserve verbatim.
type cliConfig struct {
	APIEndpoint    string                `yaml:"api_endpoint,omitempty"`
	Region         string                `yaml:"region,omitempty"`
	Credentials    cliCredentials        `yaml:"credentials"`
	CurrentContext string                `yaml:"current_context,omitempty"`
	Contexts       map[string]cliContext `yaml:"contexts,omitempty"`
	Defaults       map[string]any        `yaml:"defaults,omitempty"`
}

// Options selects which config file and context Resolve reads.
type Options struct {
	// Path overrides the config file location; "" uses ~/.fm/config.yaml.
	Path string
	// Context overrides the context to read; "" uses the file's current_context.
	Context string
}

// Resolved is the credential resolved from the CLI config. Exactly one of
// APIKey (option B) or AccessToken (option C) is set when a usable credential
// is present; both are empty when the context exists but is logged out.
type Resolved struct {
	// APIEndpoint is the resolved context's api_endpoint (it includes the /api
	// suffix the CLI stores); "" when the file does not set one.
	APIEndpoint string

	APIKey       string
	AccessToken  string
	RefreshToken string
	ExpiresAt    int64

	// PermsWarning is non-empty when the config file is group/other-readable.
	PermsWarning string

	// Bearer refreshes the OIDC token (under the config lock, with write-back)
	// for the bearer path. It is non-nil only when AccessToken is set.
	Bearer *FileSource
}

// Resolve reads the CLI config and returns the credential for the selected
// context. It returns ErrNotFound when the file is absent.
func Resolve(opts Options) (*Resolved, error) {
	path := opts.Path
	if path == "" {
		p, err := defaultPath()
		if err != nil {
			return nil, err
		}
		path = p
	}

	fi, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("stat fm CLI config: %w", err)
	}

	cfg, err := readConfig(path)
	if err != nil {
		return nil, err
	}

	ctxName := opts.Context
	if ctxName == "" {
		ctxName = cfg.CurrentContext
	}

	var (
		creds    cliCredentials
		endpoint string
		chosen   string // the context name actually used; "" for a flat (context-less) config
	)
	if ctxName != "" {
		if c, ok := cfg.Contexts[ctxName]; ok {
			endpoint = c.APIEndpoint
			chosen = ctxName
			// fm-cli only adopts a context's credentials when they are
			// non-empty, else it keeps the top-level ones (config.go Load).
			if c.Credentials.AccessToken != "" || c.Credentials.APIKey != "" {
				creds = c.Credentials
			} else {
				creds = cfg.Credentials
			}
		}
	}
	if chosen == "" {
		// An explicitly-requested context that does not exist is an error; a
		// missing current_context just falls back to the flat top-level creds.
		if opts.Context != "" {
			return nil, fmt.Errorf("fm CLI context %q not found in %s", opts.Context, path)
		}
		creds = cfg.Credentials
		endpoint = cfg.APIEndpoint
	}
	if endpoint == "" {
		endpoint = cfg.APIEndpoint // context with no endpoint inherits the top-level one
	}

	res := &Resolved{
		APIEndpoint:  endpoint,
		APIKey:       creds.APIKey,
		AccessToken:  creds.AccessToken,
		RefreshToken: creds.RefreshToken,
		ExpiresAt:    creds.ExpiresAt,
	}
	if perm := fi.Mode().Perm(); perm&0o077 != 0 {
		res.PermsWarning = fmt.Sprintf(
			"fm CLI config %s is accessible to group/other (mode %#o); it holds credentials — run `chmod 600 %s`",
			path, perm, path,
		)
	}
	// Refresh/write-back only matters for the bearer path (api keys never rotate).
	if creds.AccessToken != "" && creds.APIKey == "" {
		res.Bearer = &FileSource{path: path, contextName: chosen}
	}
	return res, nil
}

// PersistError reports that an OIDC refresh succeeded but writing the rotated
// token pair back to the config file failed. The caller still holds a valid
// in-memory token (returned alongside this error) and can complete the current
// request, but the on-disk fm session is now stale.
type PersistError struct{ Err error }

func (e *PersistError) Error() string { return "persist refreshed token: " + e.Err.Error() }
func (e *PersistError) Unwrap() error { return e.Err }

// FileSource refreshes an fm-CLI OIDC bearer credential bound to a config file
// and context. Refresh performs the whole refresh as one locked
// read-modify-write so that aliased provider instances and a concurrent `fm`
// don't POST a stale (rotated, single-use) refresh token and trip Zitadel's
// reuse-detection.
type FileSource struct {
	path        string
	contextName string // "" for a flat (context-less) config
}

// Refresh exchanges the on-disk refresh token for a fresh token set and writes
// it back, all under the file lock. lastSeenRefresh is the refresh token the
// caller currently holds; if the on-disk token differs, a peer (another
// provider instance or `fm`) already rotated it, so Refresh adopts the fresher
// on-disk token and returns WITHOUT contacting the IdP — never re-POSTing a
// token a peer may have already invalidated.
//
// On a successful grant whose write-back fails, it returns the valid new token
// together with a *PersistError so the caller can proceed and warn.
func (s *FileSource) Refresh(ctx context.Context, httpClient *http.Client, apiEndpoint, lastSeenRefresh string) (*Token, error) {
	unlock, err := lockFile(s.path)
	if err != nil {
		return nil, err
	}
	defer unlock()

	cfg, err := readConfig(s.path)
	if err != nil {
		return nil, err
	}
	onDisk := s.credsLocked(cfg)

	// A peer already rotated the refresh token — adopt its fresh pair instead of
	// POSTing our stale one (which reuse-detection would reject, killing the
	// whole session).
	if onDisk.RefreshToken != "" && lastSeenRefresh != "" && onDisk.RefreshToken != lastSeenRefresh {
		return &Token{
			AccessToken:  onDisk.AccessToken,
			RefreshToken: onDisk.RefreshToken,
			ExpiresAt:    onDisk.ExpiresAt,
		}, nil
	}

	refreshToken := onDisk.RefreshToken
	if refreshToken == "" {
		refreshToken = lastSeenRefresh
	}
	// The shared module owns the cli-config->discovery->refresh flow + the
	// per-hop transport guard (ADR-0087). The injected httpClient sets
	// RefuseUnsafeRedirect (see client.NewClient); the shared client also
	// re-applies it defensively for a client that didn't.
	tok, err := (oidc.Client{HTTPClient: httpClient}).RefreshViaGateway(ctx, apiEndpoint, refreshToken)
	if err != nil {
		return nil, err
	}
	newRefresh := tok.RefreshToken
	if newRefresh == "" { // the IdP may or may not rotate the refresh token
		newRefresh = refreshToken
	}
	// Compute the absolute expiry ONCE (ExpiresAt() reads time.Now() per call),
	// so the in-memory token and the on-disk value are the same instant.
	expiresAt := tok.ExpiresAt()
	result := &Token{AccessToken: tok.AccessToken, RefreshToken: newRefresh, ExpiresAt: expiresAt}

	if err := s.applyLocked(cfg, tok.AccessToken, newRefresh, expiresAt); err != nil {
		return result, &PersistError{Err: err}
	}
	if err := writeConfig(s.path, cfg); err != nil {
		return result, &PersistError{Err: err}
	}
	return result, nil
}

// credsLocked returns the on-disk credentials for the resolved context (or the
// top-level credentials for a flat config). Caller holds the file lock.
func (s *FileSource) credsLocked(cfg *cliConfig) cliCredentials {
	if s.contextName != "" {
		if c, ok := cfg.Contexts[s.contextName]; ok &&
			(c.Credentials.AccessToken != "" || c.Credentials.APIKey != "") {
			return c.Credentials
		}
	}
	return cfg.Credentials
}

// applyLocked writes the rotated token pair into the resolved context and, when
// that context is the current one (or the config is flat), the top-level mirror
// fm-cli keeps (config.SetTokens writes both). It errors if neither was updated
// (e.g. the context was deleted underneath us) so a lost rotation is never
// silently reported as success.
func (s *FileSource) applyLocked(cfg *cliConfig, access, refresh string, expiresAt int64) error {
	wrote := false
	if s.contextName != "" {
		if c, ok := cfg.Contexts[s.contextName]; ok {
			c.Credentials.AccessToken = access
			c.Credentials.RefreshToken = refresh
			c.Credentials.ExpiresAt = expiresAt
			cfg.Contexts[s.contextName] = c
			wrote = true
		}
	}
	if s.contextName == "" || s.contextName == cfg.CurrentContext {
		cfg.Credentials.AccessToken = access
		cfg.Credentials.RefreshToken = refresh
		cfg.Credentials.ExpiresAt = expiresAt
		wrote = true
	}
	if !wrote {
		return fmt.Errorf("fm CLI context %q no longer exists in the config", s.contextName)
	}
	return nil
}

func defaultPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	return filepath.Join(home, ".fm", "config.yaml"), nil
}

func readConfig(path string) (*cliConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read fm CLI config: %w", err)
	}
	var cfg cliConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse fm CLI config %s: %w", path, err)
	}
	return &cfg, nil
}

// writeConfig writes cfg atomically: a temp file in the same directory is
// written, fsync'd and chmod'd 0600, then renamed over path. A crash or IO
// error mid-write leaves the original file intact rather than truncated — the
// file holds the only copy of the user's refresh token.
func writeConfig(path string, cfg *cliConfig) error {
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("serialize fm CLI config: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".config-*.yaml.tmp")
	if err != nil {
		return fmt.Errorf("create temp config: %w", err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }() // no-op once the rename succeeds

	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("chmod temp config: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write temp config: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("sync temp config: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp config: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("replace fm CLI config: %w", err)
	}
	return nil
}

// lockFile takes an advisory lock on path via an O_EXCL sibling lockfile,
// portable across platforms (no flock syscall). It retries briefly on
// contention and breaks a stale lock left by a crashed process.
//
// ponytail: O_EXCL lockfile, no dependency. A lock held longer than
// lockStaleAfter is assumed orphaned and broken — it MUST exceed the worst-case
// lock-hold, which now spans the refresh HTTP call (FileSource.Refresh holds
// the lock across the IdP round-trip), so a slow IdP isn't mistaken for a crash.
// The timings are vars only so tests can shrink them.
var (
	lockStaleAfter = 120 * time.Second
	lockTimeout    = 5 * time.Second
	lockRetry      = 50 * time.Millisecond
)

func lockFile(path string) (func(), error) {
	lock := path + ".lock"
	deadline := time.Now().Add(lockTimeout)
	for {
		f, err := os.OpenFile(lock, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			_ = f.Close()
			return func() { _ = os.Remove(lock) }, nil
		}
		if !os.IsExist(err) {
			return nil, fmt.Errorf("acquire config lock %s: %w", lock, err)
		}
		// Break a stale lock from a crashed writer.
		if fi, statErr := os.Stat(lock); statErr == nil && time.Since(fi.ModTime()) > lockStaleAfter {
			_ = os.Remove(lock)
			continue
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("fm CLI config %s is locked by another process", lock)
		}
		time.Sleep(lockRetry)
	}
}
