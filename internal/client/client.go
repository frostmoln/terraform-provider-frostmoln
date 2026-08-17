// Package client provides an HTTP client for the Frostmoln API.
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"

	"go.frostmoln.internal/oidc"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/clicreds"
)

// Client is the Frostmoln API client for the Terraform provider.
type Client struct {
	baseURL    string
	httpClient *http.Client
	apiKey     string
	// bearer is set (instead of apiKey) when authenticating from an fm CLI
	// OIDC session; it refreshes the access token automatically. nil for the
	// X-API-Key path.
	bearer    *bearerSource
	userAgent string
	// version is the provider build version stamped on requests as
	// X-FM-Provider-Version so the gateway can enforce a minimum supported
	// version. Empty sends no header (the gateway's gate fails open).
	version  string
	tenantID string
	// tenantOverridden marks tenantID as explicitly set via WithTenantID (the
	// provider-level tenant_id selector). When set, Configure keeps it instead
	// of adopting the default tenant from GET /v1/me.
	tenantOverridden bool
	userID           string
}

// ProviderVersionHeader carries the Frostmoln Terraform provider build version
// so the gateway can enforce a minimum supported version (mirrors the gateway's
// middleware constant; ADR-0088).
const ProviderVersionHeader = "X-FM-Provider-Version"

// providerUpgradeFallback is shown when the gateway rejects this provider as too
// old but its 426 body carried no message of its own.
const providerUpgradeFallback = "your Frostmoln Terraform provider is older than the minimum supported version; upgrade at https://registry.terraform.io/providers/frostmoln/frostmoln/latest"

// Option configures the client.
type Option func(*Client)

// NewClient creates a new API client. Authentication is either the X-API-Key
// path (pass a non-empty apiKey) or the OIDC bearer path (pass an empty apiKey
// plus WithTokenSource) — exactly one, never both.
func NewClient(baseURL, apiKey string, opts ...Option) *Client {
	c := &Client{
		baseURL: baseURL,
		httpClient: &http.Client{
			Timeout: 60 * time.Second,
			// Refuse a cross-host / https->http redirect: API requests carry the
			// X-API-Key or the OIDC bearer token, and the bearer-refresh POST (on
			// the same client) carries the single-use refresh token in its body —
			// Go resends the body and forwards custom headers across a same-/sub-
			// host redirect, so a malicious 3xx must not relay either elsewhere.
			CheckRedirect: oidc.RefuseUnsafeRedirect,
		},
		apiKey:    apiKey,
		userAgent: "terraform-provider-frostmoln/0.1.0",
	}

	for _, opt := range opts {
		opt(c)
	}

	// The bearer source refreshes via <baseURL>/v1/auth/cli-config, using the
	// (possibly customized) HTTP client; wire both after options are applied.
	if c.bearer != nil {
		c.bearer.apiEndpoint = c.baseURL
		c.bearer.httpClient = c.httpClient
		if c.bearer.now == nil {
			c.bearer.now = time.Now
		}
	}

	return c
}

// WithHTTPClient sets a custom HTTP client.
func WithHTTPClient(client *http.Client) Option {
	return func(c *Client) {
		c.httpClient = client
	}
}

// WithUserAgent sets the user agent string.
func WithUserAgent(ua string) Option {
	return func(c *Client) {
		c.userAgent = ua
	}
}

// WithClientVersion sets the provider build version stamped as
// X-FM-Provider-Version so the gateway can enforce a minimum supported version.
func WithClientVersion(version string) Option {
	return func(c *Client) {
		c.version = version
	}
}

// WithTenantID pre-sets the operating tenant, overriding the default tenant
// resolved from GET /v1/me. The provider's tenant_id attribute (or
// FROSTMOLN_TENANT_ID) uses this to target any tenant the credential is
// entitled to. Entitlement is enforced by the gateway (TENANT_ACCESS_DENIED),
// not here. An empty id is a no-op (keep the /v1/me default).
func WithTenantID(tenantID string) Option {
	return func(c *Client) {
		if tenantID != "" {
			c.tenantID = tenantID
			c.tenantOverridden = true
		}
	}
}

// UserProfile represents the response from GET /v1/me.
type UserProfile struct {
	ID       string `json:"id"`
	TenantID string `json:"tenantId"`
	Email    string `json:"email"`
	Name     string `json:"name"`
}

// Configure resolves the user ID (and, unless a tenant was pre-set via
// WithTenantID, the default tenant ID) by calling GET /v1/me. This must be
// called once during provider configuration.
func (c *Client) Configure(ctx context.Context) error {
	resp, err := c.Get(ctx, "/v1/me", nil)
	if err != nil {
		return fmt.Errorf("failed to authenticate: %w", err)
	}

	var profile UserProfile
	if err := json.Unmarshal(resp.Body, &profile); err != nil {
		return fmt.Errorf("failed to parse user profile: %w", err)
	}

	c.userID = profile.ID

	// Keep an explicit tenant_id override; otherwise adopt the caller's default
	// tenant. The override may target a tenant other than the /v1/me default, so
	// don't require profile.TenantID in that case.
	if !c.tenantOverridden {
		if profile.TenantID == "" {
			return fmt.Errorf("no tenant ID found for current user")
		}
		c.tenantID = profile.TenantID
		return nil
	}

	// An API key is bound to a single tenant: the gateway rejects any other
	// tenant in the request path with TENANT_ACCESS_DENIED (api-gateway
	// middleware/tenant.go). Catch that here — at configure time with a clear,
	// actionable message — instead of letting every resource operation fail 403
	// mid-apply. The OIDC / fm CLI session path (c.bearer != nil) can legitimately
	// target any tenant the user belongs to, so that is left to the gateway.
	if c.bearer == nil && profile.TenantID != "" && c.tenantID != profile.TenantID {
		return fmt.Errorf("tenant_id %q does not match this API key's tenant %q: an API key is bound to a single tenant. Omit tenant_id (or set it to the key's tenant), or authenticate with an fm CLI / OIDC session to manage multiple tenants", c.tenantID, profile.TenantID)
	}
	return nil
}

// Endpoint returns the base URL the client sends requests to. The provider uses
// it to explain a configure-time 404 caused by an endpoint missing the gateway's
// /api path prefix.
func (c *Client) Endpoint() string {
	return c.baseURL
}

// TenantID returns the resolved tenant ID.
func (c *Client) TenantID() string {
	return c.tenantID
}

// UserID returns the resolved user ID.
func (c *Client) UserID() string {
	return c.userID
}

// SetTenantIDForTest sets the tenant ID directly for testing purposes.
func (c *Client) SetTenantIDForTest(tenantID string) {
	c.tenantID = tenantID
}

// SetUserIDForTest sets the user ID directly for testing purposes.
func (c *Client) SetUserIDForTest(userID string) {
	c.userID = userID
}

// TenantPath builds a tenant-scoped API path. The tenant id is percent-escaped
// so a malformed tenant_id override stays a single, literal path segment the
// gateway rejects cleanly, rather than restructuring the URL via path cleaning.
func (c *Client) TenantPath(subpath string) string {
	return fmt.Sprintf("/v1/tenants/%s%s", url.PathEscape(c.tenantID), subpath)
}

// UserPath builds a user-scoped API path.
func (c *Client) UserPath(subpath string) string {
	return fmt.Sprintf("/v1/users/%s%s", c.userID, subpath)
}

// APIError represents an error returned by the API.
type APIError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	// Details is an OBJECT on the wire, never a string: servicekit's error type
	// carries map[string]any and every service emits through it — identity sends
	// {"reason":…,"min":…,"max":…} on a refused label and
	// {"unbillableTenantIds":[…]} on a refused close.
	//
	// It was declared as a string, which is worse than useless: encoding/json does
	// not abort on a type mismatch, it skips the field and returns the error at the
	// END, so the `err == nil` guard in Do() failed and the whole envelope was
	// discarded. A refused `frostmoln_api_key` name then surfaced in the plan
	// diagnostic as a raw JSON body, with the code lost — and IsNotFound/IsConflict
	// kept working only because they read StatusCode rather than Code.
	Details    map[string]any `json:"details,omitempty"`
	StatusCode int            `json:"-"`
	// RetryAfter is the server-suggested wait in seconds on a 429 (from the
	// rate-limit body's retry_after or the Retry-After header); 0 when absent.
	RetryAfter int `json:"retry_after,omitempty"`
}

// Error renders code and message only. Details is machine-readable context for a
// caller that branches on it, not practitioner copy: spilling a decoded map into a
// diagnostic is the JSON blob this type exists to avoid.
func (e *APIError) Error() string {
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

// IsNotFound returns true if the error is a 404 Not Found.
func IsNotFound(err error) bool {
	if apiErr, ok := err.(*APIError); ok {
		return apiErr.StatusCode == http.StatusNotFound
	}
	return false
}

// IsAlreadyInDesiredState reports whether err is a 409 Conflict whose error code
// is the servicekit "conflict" code. The webserver expose/unexpose actions return
// this for "instance is already exposed" / "not exposed" / "an expose operation is
// already in progress" — all cases where the requested end state is already reached
// (or being reached), so an idempotent caller treats it as success. It deliberately
// does NOT match the other 409 those actions can return, "invalid_state" ("cannot
// expose an instance in <state> state"), which is a real precondition failure the
// caller must surface rather than swallow.
func IsAlreadyInDesiredState(err error) bool {
	if apiErr, ok := err.(*APIError); ok {
		return apiErr.StatusCode == http.StatusConflict && apiErr.Code == "conflict"
	}
	return false
}

// IsConflict reports whether err is a 409 Conflict, regardless of error code.
// It is deliberately broader than IsAlreadyInDesiredState (which matches only
// the "conflict"-code 409 that means the requested end state is already
// reached, an idempotent success): IsConflict is the predicate the read-replica
// delete uses to retry a transient 409, and a persistent 409 it does NOT swallow
// as success but surfaces as an error.
func IsConflict(err error) bool {
	if apiErr, ok := err.(*APIError); ok {
		return apiErr.StatusCode == http.StatusConflict
	}
	return false
}

// resizeTransientConflictMarkers are the message substrings of the two
// TRANSIENT 409s the managed-database resize path emits — a backup running, or
// a replica not yet running (typically mid-delete). Both clear on their own in
// minutes, so a resize is worth retrying. They are matched on stable substrings
// only, never the volatile replica id/status the backend interpolates.
//
// The message text is owned by the database service
// (database/internal/service/impl/instance.go, the 409s at ~:483 and ~:531); it
// is the only discriminator, because the resize path's permanent 409s share the
// code:"conflict"/status-409 wire shape. If either message is reworded there,
// this list must follow — a transient reword degrades safely (the resize
// surfaces the 409 as before the fix) but silently.
var resizeTransientConflictMarkers = []string{
	"a backup is in progress",
	"all replicas must be running to resize",
}

// IsTransientResizeConflict reports whether err is a 409 Conflict from the
// managed-database resize path that is TRANSIENT (worth retrying). Unlike
// IsConflict, it is deliberately default-deny: the resize path also emits
// PERMANENT 409s (wrong instance state, replica has no data volume) that would
// spin until timeout under a blind retry, so it matches only the two known
// transient messages. Anything else — including those permanent 409s — returns
// false and is surfaced immediately. This is the narrow predicate the resize
// retry uses, the reason DeleteWithConflictRetry's blanket IsConflict cannot be
// reused here (see its doc-comment).
func IsTransientResizeConflict(err error) bool {
	apiErr, ok := err.(*APIError)
	if !ok || apiErr.StatusCode != http.StatusConflict || apiErr.Code != "conflict" {
		return false
	}
	for _, marker := range resizeTransientConflictMarkers {
		if strings.Contains(apiErr.Message, marker) {
			return true
		}
	}
	return false
}

// OperationResponse represents an async operation accepted by the API (HTTP 202).
// Actions like volume detach, resize, and attach return this instead of the full resource.
type OperationResponse struct {
	OperationID  string `json:"operationId"`
	Status       string `json:"status"`
	ResourceType string `json:"resourceType"`
}

// Operation status values for async provisioning operations (matches the
// provisioning service's domain.OperationStatus).
const (
	OperationStatusPending   = "pending"
	OperationStatusRunning   = "running"
	OperationStatusCompleted = "completed"
	OperationStatusFailed    = "failed"
	OperationStatusCancelled = "cancelled"
)

// Operation represents an async provisioning operation. It is returned both as
// the 202 Accepted body from load-balancer create/delete (status "pending") and
// from GET /v1/tenants/{tid}/operations/{id} when polling. The field set matches the
// provisioning service's domain.Operation.
type Operation struct {
	OperationID  string `json:"operationId"`
	Status       string `json:"status"`
	ResourceType string `json:"resourceType"`
	ResourceID   string `json:"resourceId,omitempty"`
	Error        string `json:"error,omitempty"`
	// ErrorCode is the machine-readable half of a failure, in servicekit's wire
	// vocabulary (`quota_exceeded`, `not_found`, …). Empty when provisioning has
	// no canonical code for it.
	//
	// Error's text is customer-facing copy the service is free to reword or
	// redact (a message naming a resource by UUID is replaced wholesale), so it
	// is not something to branch on. This is — which is what a typed,
	// retryable-vs-permanent diagnostic would need.
	ErrorCode   string `json:"errorCode,omitempty"`
	Progress    int    `json:"progress"`
	CreatedAt   string `json:"createdAt"`
	CompletedAt string `json:"completedAt,omitempty"`
}

// GetOperation fetches an async provisioning operation by ID from the
// tenant-scoped route, falling back to the legacy untenanted one on 404.
//
// The tenant-scoped route is the one that enforces ownership: the legacy
// /v1/operations/{id} skips its check when the caller's signed tenant is empty,
// so a migrated client must not keep using it.
//
// WHY THE FALLBACK IS KEYED ON ROUTE_NOT_FOUND AND NOT ON THE 404 STATUS.
// Provisioning answers 404 with code NOT_FOUND for BOTH "no such operation" and
// "you do not own this one" — deliberately existence-oracle-free, so those two
// are indistinguishable and must stay that way. But "this gateway has no such
// route" is a different layer: the gateway itself answers ROUTE_NOT_FOUND before
// any service is consulted. Keying on the status alone conflates them, and a
// migrated client would then answer its own ownership denial by re-asking the
// route that does not check ownership — automatically, on every denial, handing
// exactly the org-invited caller this migration protects an unchecked read of any
// operation in the estate. It would also keep the legacy-read metric that gates
// the route's removal permanently non-zero, produced by the migrated clients
// themselves.
//
// The narrow fallback that remains covers the real transition case: the provider
// is a customer-pinned artifact, so a new provider against a gateway that has not
// yet published the scoped route is normal, not an ordering mistake — and
// WaitForState retries every poll error to the timeout, so without it that would
// surface as "timed out waiting for operation …" instead of a routing 404. Remove
// it with the legacy route (Ambix 019ff725-06f0).
//
// Deliberately an ALLOW-list of one code, not "any 404 that is not NOT_FOUND".
// The uncovered case is gateway-new / provisioning-old, where provisioning's own
// Gin answers a bare 404 with no code: that poll times out instead of falling
// back. It is transient, self-healing on rollout, and the operation still runs —
// whereas a deny-list would silently start retrying the ownership-denied route
// the day provisioning adds any other 404 code.
func (c *Client) GetOperation(ctx context.Context, operationID string) (*Operation, error) {
	// Escape the id for the same reason TenantPath escapes the tenant: the final
	// URL is assembled with path.Join, which CLEANS dot segments, so an id
	// containing "../" would otherwise collapse the scoped path back onto the
	// legacy route (or any other GET endpoint) carrying the caller's credential.
	suffix := url.PathEscape(operationID)
	resp, err := c.Get(ctx, c.TenantPath(fmt.Sprintf("/operations/%s", suffix)), nil)
	if err != nil && IsRouteNotFound(err) {
		resp, err = c.Get(ctx, fmt.Sprintf("/v1/operations/%s", suffix), nil)
	}
	if err != nil {
		return nil, err
	}
	return ParseResponse[Operation](resp)
}

// RouteNotFoundCode is the api-gateway's code for an unmatched path
// (internal/gateway/gateway.go). It predates the tenant-scoped operations route,
// so every gateway old enough to lack that route still answers with it.
const RouteNotFoundCode = "ROUTE_NOT_FOUND"

// IsRouteNotFound reports whether err is the api-gateway's "no route found for
// path" answer — the gateway does not serve this path at all. It is NOT the same
// as a 404 from the service behind the route, which for operations means either
// "no such operation" or "not yours" and must never be retried elsewhere.
func IsRouteNotFound(err error) bool {
	var apiErr *APIError
	return errors.As(err, &apiErr) &&
		apiErr.StatusCode == http.StatusNotFound &&
		apiErr.Code == RouteNotFoundCode
}

// WaitForOperation polls GET /v1/tenants/{tid}/operations/{id} until the
// operation reaches a
// terminal state. On "completed" it returns the operation (whose ResourceID is
// the affected resource's ID). On "failed"/"cancelled" it returns an error that
// includes the operation's error message. It reuses the generic WaitForState
// poller for interval/timeout/retry behavior.
func (c *Client) WaitForOperation(ctx context.Context, operationID string, interval, timeout time.Duration) (*Operation, error) {
	var lastOp *Operation
	_, err := WaitForState(ctx, PollConfig{
		Interval:     interval,
		Timeout:      timeout,
		TargetStates: []string{OperationStatusCompleted},
		ErrorStates:  []string{OperationStatusFailed, OperationStatusCancelled},
		ResourceName: fmt.Sprintf("operation %s", operationID),
		PollFunc: func(pollCtx context.Context) (string, error) {
			op, opErr := c.GetOperation(pollCtx, operationID)
			if opErr != nil {
				return "", opErr
			}
			lastOp = op
			return op.Status, nil
		},
	})
	if err != nil {
		if lastOp != nil && lastOp.Error != "" {
			return nil, fmt.Errorf("operation %s %s: %s", operationID, lastOp.Status, lastOp.Error)
		}
		return nil, err
	}
	return lastOp, nil
}

// Response represents an API response.
type Response struct {
	StatusCode int
	Headers    http.Header
	Body       []byte
}

// IsAccepted returns true if the response has HTTP status 202 Accepted.
func (r *Response) IsAccepted() bool {
	return r.StatusCode == http.StatusAccepted
}

// Do sends an API request and returns the response. For the X-API-Key path it
// is a single attempt. For the OIDC bearer path it refreshes the access token
// proactively (when near expiry) before the request, and reactively once on a
// 401 before replaying it — so an expired access token never surfaces as an
// auth error mid-apply.
func (c *Client) Do(ctx context.Context, method, reqPath string, query url.Values, body interface{}) (*Response, error) {
	for attempt := 0; ; attempt++ {
		resp, err := c.doWithAuth(ctx, method, reqPath, query, body)
		apiErr, ok := err.(*APIError)
		if !ok || apiErr.StatusCode != http.StatusTooManyRequests || attempt >= rateLimitRetries {
			return resp, err
		}
		// A SERVICE quota refusal is not a rate window and must NOT be retried
		// here. `details.cap` says which limit refused, and the remedies differ:
		// delete an image, wait for an import to finish, or nothing at all. This
		// loop's escalating backoff is sized for the gateway's per-minute window,
		// so retrying a quota refusal spends ~60s and five more requests to be
		// told the same thing — against the very component that is already at its
		// limit — and then fails the apply anyway. Hand it to the caller, which is
		// the only layer that knows what the cap means (see
		// resource/image.startImport, which waits out `concurrent_imports`).
		if _, hasCap := apiErr.Details["cap"]; hasCap {
			return resp, err
		}
		// The gateway rate-limits per key and says when to come back;
		// Terraform runs resource operations concurrently (parallel destroys
		// especially), so surfacing the 429 would fail an apply the server
		// explicitly asked us to retry.
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(rateLimitBackoff(attempt, apiErr.RetryAfter)):
		}
	}
}

// detailRetryAfterSeconds is where a service puts a 429's wait when the header
// cannot carry it. It is the same key servicekit-based services emit
// (compute/internal/domain/errors.go DetailRetryAfterSeconds), and it exists
// because `Retry-After` is not CORS-safelisted, so browser clients cannot read
// the header at all and services send the wait in the body instead.
const detailRetryAfterSeconds = "retryAfterSeconds"

// rateLimitBackoff computes the wait before 429-retry number attempt. The
// server's Retry-After hint alone is not enough: the limiter's minute window
// can stay exhausted long past its optimistic "retry after 1 seconds" while
// concurrent operations keep consuming the refill — so the wait escalates per
// attempt (raised to the server hint when that is larger) to ride out a full
// window rollover instead of burning all retries in the first seconds.
func rateLimitBackoff(attempt, retryAfterSeconds int) time.Duration {
	wait := time.Duration(retryAfterSeconds) * time.Second
	if escalated := rateLimitBaseWait << attempt; wait < escalated {
		wait = escalated
	}
	if wait > rateLimitMaxWait {
		wait = rateLimitMaxWait
	}
	return wait
}

// rateLimitRetries bounds the automatic 429 retry per request. Waits escalate
// rateLimitBaseWait<<attempt (2s, 4s, 8s, 16s, 30s — capped by
// rateLimitMaxWait), each raised to the server's Retry-After when that is
// larger: ~60s of total patience, enough to roll over the gateway's
// per-minute window.
const (
	rateLimitRetries  = 5
	rateLimitBaseWait = 2 * time.Second
	rateLimitMaxWait  = 30 * time.Second
)

// doWithAuth sends one request on the configured auth path, refreshing a
// stale bearer token once on a 401.
func (c *Client) doWithAuth(ctx context.Context, method, reqPath string, query url.Values, body interface{}) (*Response, error) {
	if c.bearer == nil {
		return c.do(ctx, method, reqPath, query, body, "")
	}

	refreshedProactively, err := c.bearer.ensureFresh(ctx)
	if err != nil {
		return nil, fmt.Errorf("refresh access token: %w", err)
	}
	used := c.bearer.token()
	resp, err := c.do(ctx, method, reqPath, query, body, used)
	if err == nil {
		return resp, nil
	}
	apiErr, ok := err.(*APIError)
	if !ok || apiErr.StatusCode != http.StatusUnauthorized {
		return nil, err
	}
	if refreshedProactively {
		// We just minted this token and it still 401s — that's not expiry
		// (audience/permission/clock), so another refresh won't help. Surface
		// the 401 instead of burning a second refresh.
		return nil, err
	}
	refreshed, rerr := c.bearer.refreshIfStale(ctx, used)
	if rerr != nil || !refreshed {
		// A dead refresh token (invalid_grant) can't be retried — surface the
		// actionable re-login diagnostic (the gateway's raw 401 body doesn't say
		// "run fm auth login"). Any other refresh failure leaves the original 401.
		if errors.Is(rerr, clicreds.ErrSessionExpired) {
			return nil, rerr
		}
		return nil, err
	}
	return c.do(ctx, method, reqPath, query, body, c.bearer.token())
}

// do sends a single API request with no refresh-retry. bearerToken is the
// access token to send on the bearer path (ignored for the X-API-Key path).
func (c *Client) do(ctx context.Context, method, reqPath string, query url.Values, body interface{}, bearerToken string) (*Response, error) {
	u, err := url.Parse(c.baseURL)
	if err != nil {
		return nil, fmt.Errorf("invalid base URL: %w", err)
	}
	u.Path = path.Join(u.Path, reqPath)
	if query != nil {
		u.RawQuery = query.Encode()
	}

	var bodyReader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal request body: %w", err)
		}
		bodyReader = bytes.NewReader(data)
	}

	httpReq, err := http.NewRequestWithContext(ctx, method, u.String(), bodyReader)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	httpReq.Header.Set("User-Agent", c.userAgent)
	if c.version != "" {
		httpReq.Header.Set(ProviderVersionHeader, c.version)
	}
	httpReq.Header.Set("Accept", "application/json")
	if body != nil {
		httpReq.Header.Set("Content-Type", "application/json")
	}
	if c.bearer != nil {
		httpReq.Header.Set("Authorization", "Bearer "+bearerToken)
	} else if c.apiKey != "" {
		httpReq.Header.Set("X-API-Key", c.apiKey)
	}

	httpResp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer func() { _ = httpResp.Body.Close() }()

	respBody, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	if httpResp.StatusCode >= 400 {
		// A 426 is the gateway's minimum-provider-version floor. Surface a clean,
		// terminal-safe upgrade message (the gateway body is untrusted; the shared
		// helper strips control/ANSI bytes) so `terraform plan/apply` prints a
		// single actionable line (ADR-0088).
		if httpResp.StatusCode == http.StatusUpgradeRequired {
			var gw struct {
				Error struct {
					Message string `json:"message"`
				} `json:"error"`
			}
			_ = json.Unmarshal(respBody, &gw)
			return nil, &APIError{
				Code:       "PROVIDER_UPGRADE_REQUIRED",
				Message:    oidc.NewUpgradeRequiredError(gw.Error.Message, providerUpgradeFallback).Error(),
				StatusCode: httpResp.StatusCode,
			}
		}
		// The gateway's rate-limit body is its own shape ({"error":"rate
		// limit exceeded","message":...,"retry_after":N} — "error" is a
		// string, not an object) so parse it before the generic formats; Do
		// retries these using the server-suggested wait.
		if httpResp.StatusCode == http.StatusTooManyRequests {
			// A SERVICE's 429 comes first, and this ordering is the whole point:
			// the gateway shape below has no `details`, so parsing it first
			// flattened every service 429 into Code="RATE_LIMITED" with a nil
			// Details — discarding the `cap` discriminator that says WHICH limit
			// refused and therefore what the remedy is. compute has more than one
			// 429 (`mint_rate`, `mint_unavailable`, `concurrent_imports`) and they
			// do not share a remedy: one clears by waiting, one by deleting an
			// image, one not at all. A caller that cannot tell them apart cannot
			// do the right thing, and image_resource.startImport is one such
			// caller. Recognised by a non-empty `code`, which the gateway's own
			// body does not carry.
			var svc APIError
			_ = json.Unmarshal(respBody, &svc)
			if svc.Code != "" {
				svc.StatusCode = httpResp.StatusCode
				if svc.RetryAfter <= 0 {
					if s, ok := svc.Details[detailRetryAfterSeconds].(float64); ok {
						svc.RetryAfter = int(s)
					} else {
						svc.RetryAfter, _ = strconv.Atoi(httpResp.Header.Get("Retry-After"))
					}
				}
				return nil, &svc
			}
			var rl struct {
				Message    string `json:"message"`
				RetryAfter int    `json:"retry_after"`
			}
			_ = json.Unmarshal(respBody, &rl)
			if rl.RetryAfter <= 0 {
				rl.RetryAfter, _ = strconv.Atoi(httpResp.Header.Get("Retry-After"))
			}
			if rl.Message == "" {
				rl.Message = string(respBody)
			}
			return nil, &APIError{
				Code:       "RATE_LIMITED",
				Message:    rl.Message,
				StatusCode: httpResp.StatusCode,
				RetryAfter: rl.RetryAfter,
			}
		}
		// Try to parse nested error format first: {"error": {"code": ..., "message": ...}}
		var nested struct {
			Error APIError `json:"error"`
		}
		// Deliberately NOT gated on the unmarshal error. encoding/json fills every
		// field it can and reports a type mismatch only at the END, so a single
		// unexpected shape anywhere in the envelope used to discard a code and
		// message it had already decoded perfectly — that is how an object `details`
		// turned every refusal into a raw JSON blob in a plan diagnostic. A body
		// that is not this envelope leaves Code empty and falls through exactly as
		// before, so the guard costs nothing and this branch is now immune to
		// whatever shape a future `details` (or any other added key) arrives in.
		_ = json.Unmarshal(respBody, &nested)
		if nested.Error.Code != "" {
			nested.Error.StatusCode = httpResp.StatusCode
			return nil, &nested.Error
		}
		// Fall back to flat error format: {"code": ..., "message": ...}
		var apiErr APIError
		if err := json.Unmarshal(respBody, &apiErr); err == nil && apiErr.Code != "" {
			apiErr.StatusCode = httpResp.StatusCode
			return nil, &apiErr
		}
		return nil, &APIError{
			Code:       "ERROR",
			Message:    fmt.Sprintf("HTTP %d: %s", httpResp.StatusCode, string(respBody)),
			StatusCode: httpResp.StatusCode,
		}
	}

	return &Response{
		StatusCode: httpResp.StatusCode,
		Headers:    httpResp.Header,
		Body:       respBody,
	}, nil
}

// Get sends a GET request.
func (c *Client) Get(ctx context.Context, path string, query url.Values) (*Response, error) {
	return c.Do(ctx, http.MethodGet, path, query, nil)
}

// Post sends a POST request.
func (c *Client) Post(ctx context.Context, path string, body interface{}) (*Response, error) {
	return c.Do(ctx, http.MethodPost, path, nil, body)
}

// Patch sends a PATCH request.
func (c *Client) Patch(ctx context.Context, path string, body interface{}) (*Response, error) {
	return c.Do(ctx, http.MethodPatch, path, nil, body)
}

// Put sends a PUT request.
func (c *Client) Put(ctx context.Context, path string, body interface{}) (*Response, error) {
	return c.Do(ctx, http.MethodPut, path, nil, body)
}

// Delete sends a DELETE request.
func (c *Client) Delete(ctx context.Context, path string) (*Response, error) {
	return c.Do(ctx, http.MethodDelete, path, nil, nil)
}

// DeleteWithQuery sends a DELETE request with query parameters.
func (c *Client) DeleteWithQuery(ctx context.Context, path string, query url.Values) (*Response, error) {
	return c.Do(ctx, http.MethodDelete, path, query, nil)
}

// DeleteWithConflictRetry sends a DELETE and retries while the server returns a
// 409 Conflict, up to timeout, sleeping interval between attempts. It exists for
// the managed-database read-replica delete path: the backend 409s a replica
// delete while the primary instance is mid-resize ("cannot delete read replica
// while the primary instance is resizing"), a transient state that clears in
// minutes. A mixed `terraform apply` that grows the primary storage AND removes
// a replica in the same plan runs the two concurrently (no dependency edge), so
// surfacing that 409 would hard-fail an apply that self-heals on re-run.
//
// Retrying on ANY 409 is safe ONLY because the replica DELETE endpoint returns
// no other 409. Do NOT reuse this on paths that can 409 permanently (e.g. the
// instance resize path's wrong-state 409) — it would spin until timeout. Unlike
// IsAlreadyInDesiredState (a 409 that means the delete is effectively already
// done), a delete has NOT happened here, so a 409 that never clears is returned
// as an error for the caller to surface, never swallowed as success.
func (c *Client) DeleteWithConflictRetry(ctx context.Context, path string, interval, timeout time.Duration) (*Response, error) {
	deadline := time.Now().Add(timeout)
	for {
		resp, err := c.Delete(ctx, path)
		if err == nil || !IsConflict(err) {
			return resp, err
		}
		if !time.Now().Before(deadline) {
			// The conflict never cleared within the budget — surface the last
			// 409 (more actionable than a bare deadline error), don't hang.
			return resp, err
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(interval):
		}
	}
}

// PostWithConflictRetry sends a POST and retries while retryable(err) is true,
// up to timeout, sleeping interval between attempts. It exists for the managed-
// database instance resize path (POST /databases/{id}/resize): a mixed
// `terraform apply` that grows the primary storage AND removes a read replica in
// the same plan runs the two concurrently (no dependency edge), so the resize
// can 409 transiently while the replica is mid-delete — the mirror of the
// replica-delete 409 DeleteWithConflictRetry handles.
//
// Unlike DeleteWithConflictRetry, the retry predicate is a parameter rather than
// a hardcoded IsConflict: the resize path emits PERMANENT 409s (wrong instance
// state, replica with no data volume) alongside the transient ones, and blindly
// retrying those would spin until timeout. Callers pass IsTransientResizeConflict
// so only the two transient messages retry; every other error (including a
// permanent 409) returns on the first attempt.
//
// Idempotency: a resize 409 is always rejected before the resize workflow
// starts — either pre-CAS (backup in progress) or with the instance reverted
// resizing→running before the call returns (a replica not yet running) — so no
// partial resize is ever in flight and re-POSTing is safe. Once the call
// succeeds (202/200) the loop stops. A transient 409 that never clears within
// the budget surfaces the last 409 (more actionable than a bare deadline error),
// never swallowed as success.
func (c *Client) PostWithConflictRetry(ctx context.Context, path string, body any, retryable func(error) bool, interval, timeout time.Duration) (*Response, error) {
	deadline := time.Now().Add(timeout)
	for {
		resp, err := c.Post(ctx, path, body)
		if err == nil || !retryable(err) {
			return resp, err
		}
		if !time.Now().Before(deadline) {
			return resp, err
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(interval):
		}
	}
}

// ParseResponse unmarshals the response body into the given type.
func ParseResponse[T any](resp *Response) (*T, error) {
	var result T
	if err := json.Unmarshal(resp.Body, &result); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}
	return &result, nil
}
