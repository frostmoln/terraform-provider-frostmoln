package client

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/hashicorp/terraform-plugin-log/tflog"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/clicreds"
)

// refreshSkew is how far before expiry the client proactively refreshes a
// bearer token, so a request never goes out with a token that expires mid-flight.
const refreshSkew = 60 * time.Second

// TokenSourceConfig is the initial OIDC bearer state for WithTokenSource.
type TokenSourceConfig struct {
	AccessToken  string
	RefreshToken string
	ExpiresAt    int64 // access-token expiry as a Unix timestamp (seconds), 0 if unknown
	// Source performs the locked refresh + write-back against the origin (the
	// fm CLI config). Required for the bearer path.
	Source *clicreds.FileSource
}

// WithTokenSource configures the client to authenticate with an OIDC bearer
// token, refreshing it automatically (proactively near expiry and reactively on
// a 401) and persisting the rotated pair. Used for the fm-CLI-session auth path;
// mutually exclusive with the X-API-Key path (NewClient is called with an empty
// apiKey).
func WithTokenSource(cfg TokenSourceConfig) Option {
	return func(c *Client) {
		c.bearer = &bearerSource{
			access:    cfg.AccessToken,
			refresh:   cfg.RefreshToken,
			expiresAt: cfg.ExpiresAt,
			src:       cfg.Source,
			now:       time.Now,
		}
	}
}

// bearerSource holds a refreshable OIDC bearer token. All token reads and
// refreshes are serialized by mu, so concurrent Terraform resource operations
// sharing one client don't race or stampede the IdP (single-flight refresh via
// refreshIfStale). Cross-process / aliased-instance safety is handled one level
// down by clicreds.FileSource, which refreshes under a file lock.
type bearerSource struct {
	mu        sync.Mutex
	access    string
	refresh   string
	expiresAt int64

	apiEndpoint string // = client baseURL (the CLI endpoint, with /api), for cli-config discovery
	httpClient  *http.Client
	src         *clicreds.FileSource
	now         func() time.Time
}

// token returns the current access token.
func (b *bearerSource) token() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.access
}

// ensureFresh refreshes the token if it is expired or within refreshSkew of
// expiry. A zero expiry (unknown) is left to the reactive 401 path. It reports
// whether a refresh was performed.
func (b *bearerSource) ensureFresh(ctx context.Context) (bool, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.expiresAt == 0 {
		return false, nil
	}
	if b.now().Add(refreshSkew).Unix() < b.expiresAt {
		return false, nil
	}
	return true, b.refreshLocked(ctx)
}

// refreshIfStale refreshes only if the access token still equals used; if
// another goroutine already refreshed it, it reports success without a second
// refresh. Returns whether a usable token is now available.
func (b *bearerSource) refreshIfStale(ctx context.Context, used string) (bool, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.access != used {
		return true, nil
	}
	if err := b.refreshLocked(ctx); err != nil {
		return false, err
	}
	return true, nil
}

// refreshLocked performs the OIDC refresh + write-back via the file source.
// Caller holds mu. A write-back failure after a successful grant does NOT fail
// the request — the in-memory token is valid — but is surfaced as a warning,
// because the on-disk fm session is now stale.
func (b *bearerSource) refreshLocked(ctx context.Context) error {
	tok, err := b.src.Refresh(ctx, b.httpClient, b.apiEndpoint, b.refresh)
	var perr *clicreds.PersistError
	if err != nil && !errors.As(err, &perr) {
		return err // grant or lock failure — no usable token
	}
	// Grant succeeded; tok is valid even when perr != nil (write-back failed).
	b.access = tok.AccessToken
	b.refresh = tok.RefreshToken
	b.expiresAt = tok.ExpiresAt
	if perr != nil {
		tflog.Warn(ctx, "refreshed the Frostmoln access token but could not write it back to the fm CLI config; your fm session may need 'fm auth login'", map[string]any{
			"error": perr.Unwrap().Error(),
		})
	}
	return nil
}
