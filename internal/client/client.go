// Package client provides an HTTP client for the Frostmoln API.
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"time"
)

// Client is the Frostmoln API client for the Terraform provider.
type Client struct {
	baseURL    string
	httpClient *http.Client
	apiKey     string
	userAgent  string
	tenantID   string
	userID     string
}

// Option configures the client.
type Option func(*Client)

// NewClient creates a new API client.
func NewClient(baseURL, apiKey string, opts ...Option) *Client {
	c := &Client{
		baseURL: baseURL,
		httpClient: &http.Client{
			Timeout: 60 * time.Second,
		},
		apiKey:    apiKey,
		userAgent: "terraform-provider-frostmoln/0.1.0",
	}

	for _, opt := range opts {
		opt(c)
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

// UserProfile represents the response from GET /v1/me.
type UserProfile struct {
	ID       string `json:"id"`
	TenantID string `json:"tenantId"`
	Email    string `json:"email"`
	Name     string `json:"name"`
}

// Configure resolves the tenant ID and user ID by calling GET /v1/me.
// This must be called once during provider configuration.
func (c *Client) Configure(ctx context.Context) error {
	resp, err := c.Get(ctx, "/v1/me", nil)
	if err != nil {
		return fmt.Errorf("failed to authenticate: %w", err)
	}

	var profile UserProfile
	if err := json.Unmarshal(resp.Body, &profile); err != nil {
		return fmt.Errorf("failed to parse user profile: %w", err)
	}

	if profile.TenantID == "" {
		return fmt.Errorf("no tenant ID found for current user")
	}

	c.tenantID = profile.TenantID
	c.userID = profile.ID
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

// TenantPath builds a tenant-scoped API path.
func (c *Client) TenantPath(subpath string) string {
	return fmt.Sprintf("/v1/tenants/%s%s", c.tenantID, subpath)
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

// Do sends an API request and returns the response.
func (c *Client) Do(ctx context.Context, method, reqPath string, query url.Values, body interface{}) (*Response, error) {
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
	httpReq.Header.Set("Accept", "application/json")
	if body != nil {
		httpReq.Header.Set("Content-Type", "application/json")
	}
	httpReq.Header.Set("X-API-Key", c.apiKey)

	httpResp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer httpResp.Body.Close()

	respBody, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	if httpResp.StatusCode >= 400 {
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
