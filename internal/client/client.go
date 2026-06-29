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
	Code       string `json:"code"`
	Message    string `json:"message"`
	Details    string `json:"details,omitempty"`
	StatusCode int    `json:"-"`
}

func (e *APIError) Error() string {
	if e.Details != "" {
		return fmt.Sprintf("%s: %s (%s)", e.Code, e.Message, e.Details)
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

// IsNotFound returns true if the error is a 404 Not Found.
func IsNotFound(err error) bool {
	if apiErr, ok := err.(*APIError); ok {
		return apiErr.StatusCode == http.StatusNotFound
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
// from GET /v1/operations/{id} when polling. The field set matches the
// provisioning service's domain.Operation.
type Operation struct {
	OperationID  string `json:"operationId"`
	Status       string `json:"status"`
	ResourceType string `json:"resourceType"`
	ResourceID   string `json:"resourceId,omitempty"`
	Error        string `json:"error,omitempty"`
	Progress     int    `json:"progress"`
	CreatedAt    string `json:"createdAt"`
	CompletedAt  string `json:"completedAt,omitempty"`
}

// GetOperation fetches an async provisioning operation by ID.
//
// IMPORTANT: the operation endpoint is NOT tenant-scoped — it lives at
// /v1/operations/{id}, not under /v1/tenants/{tid}/...
func (c *Client) GetOperation(ctx context.Context, operationID string) (*Operation, error) {
	resp, err := c.Get(ctx, fmt.Sprintf("/v1/operations/%s", operationID), nil)
	if err != nil {
		return nil, err
	}
	return ParseResponse[Operation](resp)
}

// WaitForOperation polls GET /v1/operations/{id} until the operation reaches a
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
		// Try to parse nested error format first: {"error": {"code": ..., "message": ...}}
		var nested struct {
			Error APIError `json:"error"`
		}
		if err := json.Unmarshal(respBody, &nested); err == nil && nested.Error.Code != "" {
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

// ParseResponse unmarshals the response body into the given type.
func ParseResponse[T any](resp *Response) (*T, error) {
	var result T
	if err := json.Unmarshal(resp.Body, &result); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}
	return &result, nil
}
