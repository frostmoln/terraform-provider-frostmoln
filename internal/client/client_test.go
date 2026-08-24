package client

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// TestClientStampsVersionAndMaps426 covers the provider version gate (ADR-0088):
// the build version is stamped as X-FM-Provider-Version and a gateway 426
// surfaces as a clean, terminal-safe PROVIDER_UPGRADE_REQUIRED error.
func TestClientStampsVersionAndMaps426(t *testing.T) {
	var gotVersion string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotVersion = r.Header.Get(ProviderVersionHeader)
		w.WriteHeader(http.StatusUpgradeRequired)
		// Include an ANSI escape to prove the shared sanitizer strips it.
		_, _ = w.Write([]byte("{\"error\":{\"code\":\"PROVIDER_UPGRADE_REQUIRED\",\"message\":\"your provider (v0.1.0) is too old\x1b[31m; upgrade at https://registry.terraform.io/providers/frostmoln/frostmoln/latest\"}}"))
	}))
	t.Cleanup(server.Close)

	c := NewClient(server.URL, "test-key", WithClientVersion("v0.1.0"))
	_, err := c.do(context.Background(), http.MethodGet, "/v1/whatever", nil, nil, "")
	if err == nil {
		t.Fatal("expected an error on HTTP 426")
	}
	if gotVersion != "v0.1.0" {
		t.Errorf("X-FM-Provider-Version = %q, want v0.1.0", gotVersion)
	}
	apiErr, ok := err.(*APIError)
	if !ok {
		t.Fatalf("expected *APIError, got %T (%v)", err, err)
	}
	if apiErr.StatusCode != http.StatusUpgradeRequired || apiErr.Code != "PROVIDER_UPGRADE_REQUIRED" {
		t.Errorf("apiErr = %+v", apiErr)
	}
	if !strings.Contains(apiErr.Message, "registry.terraform.io") {
		t.Errorf("message missing the upgrade URL: %q", apiErr.Message)
	}
	if strings.ContainsAny(apiErr.Message, "\x1b") {
		t.Errorf("message leaked an ANSI escape: %q", apiErr.Message)
	}
}

func TestNewClient(t *testing.T) {
	c := NewClient("https://api.example.com", "test-key")
	if c.baseURL != "https://api.example.com" {
		t.Errorf("expected baseURL https://api.example.com, got %s", c.baseURL)
	}
	if c.apiKey != "test-key" { // pragma: allowlist secret
		t.Errorf("expected apiKey test-key, got %s", c.apiKey)
	}
}

func TestWithHTTPClient(t *testing.T) {
	custom := &http.Client{}
	c := NewClient("https://api.example.com", "key", WithHTTPClient(custom))
	if c.httpClient != custom {
		t.Error("expected custom HTTP client")
	}
}

func TestWithUserAgent(t *testing.T) {
	c := NewClient("https://api.example.com", "key", WithUserAgent("test/1.0"))
	if c.userAgent != "test/1.0" {
		t.Errorf("expected user agent test/1.0, got %s", c.userAgent)
	}
}

func TestConfigure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/me" {
			t.Errorf("expected path /v1/me, got %s", r.URL.Path)
		}
		if r.Header.Get("X-API-Key") != "test-key" { // pragma: allowlist secret
			t.Error("expected X-API-Key header")
		}
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(UserProfile{
			ID:       "user-123",
			TenantID: "tenant-456",
			Email:    "test@example.com",
		})
	}))
	defer server.Close()

	c := NewClient(server.URL, "test-key")
	err := c.Configure(context.Background())
	if err != nil {
		t.Fatalf("Configure failed: %v", err)
	}
	if c.TenantID() != "tenant-456" {
		t.Errorf("expected tenant ID tenant-456, got %s", c.TenantID())
	}
	if c.UserID() != "user-123" {
		t.Errorf("expected user ID user-123, got %s", c.UserID())
	}
}

func TestWithTenantIDBearerDistinctTenant(t *testing.T) {
	// On the OIDC/bearer path, a tenant_id override to a tenant other than the
	// /v1/me default is allowed (the gateway authorizes against the user's
	// accessible set); the override wins and userID is still resolved.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(UserProfile{ID: "user-123", TenantID: "default-tenant"})
	}))
	defer server.Close()

	// Fresh token (far-future expiry) → no refresh, so no token Source needed.
	c := NewClient(server.URL, "", WithTenantID("other-tenant"), WithTokenSource(TokenSourceConfig{
		AccessToken: "tok",
		ExpiresAt:   time.Now().Add(time.Hour).Unix(),
	}))
	if err := c.Configure(context.Background()); err != nil {
		t.Fatalf("Configure failed: %v", err)
	}
	if c.TenantID() != "other-tenant" {
		t.Errorf("expected override tenant other-tenant, got %s", c.TenantID())
	}
	if c.UserID() != "user-123" {
		t.Errorf("expected user ID user-123, got %s", c.UserID())
	}
	if got := c.TenantPath("/vpcs"); got != "/v1/tenants/other-tenant/vpcs" {
		t.Errorf("expected /v1/tenants/other-tenant/vpcs, got %s", got)
	}
}

func TestWithTenantIDAPIKeyMatchOK(t *testing.T) {
	// On the API-key path, a tenant_id equal to the key's own tenant is accepted.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(UserProfile{ID: "user-123", TenantID: "default-tenant"})
	}))
	defer server.Close()

	c := NewClient(server.URL, "test-key", WithTenantID("default-tenant"))
	if err := c.Configure(context.Background()); err != nil {
		t.Fatalf("Configure failed: %v", err)
	}
	if c.TenantID() != "default-tenant" {
		t.Errorf("expected default-tenant, got %s", c.TenantID())
	}
}

func TestWithTenantIDAPIKeyMismatchErrors(t *testing.T) {
	// An API key is single-tenant: a tenant_id other than the key's tenant must
	// fail at Configure with a clear message, not silently mis-route.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(UserProfile{ID: "user-123", TenantID: "default-tenant"})
	}))
	defer server.Close()

	c := NewClient(server.URL, "test-key", WithTenantID("other-tenant"))
	err := c.Configure(context.Background())
	if err == nil {
		t.Fatal("expected error for API-key tenant mismatch")
	}
	if !strings.Contains(err.Error(), "single tenant") {
		t.Errorf("expected single-tenant message, got %v", err)
	}
}

func TestWithTenantIDEmptyKeepsDefault(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(UserProfile{ID: "user-123", TenantID: "default-tenant"})
	}))
	defer server.Close()

	// An empty override is a no-op: the /v1/me default tenant is adopted.
	c := NewClient(server.URL, "test-key", WithTenantID(""))
	if err := c.Configure(context.Background()); err != nil {
		t.Fatalf("Configure failed: %v", err)
	}
	if c.TenantID() != "default-tenant" {
		t.Errorf("expected default-tenant, got %s", c.TenantID())
	}
}

func TestConfigureNoTenantID(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(UserProfile{ID: "user-123"})
	}))
	defer server.Close()

	c := NewClient(server.URL, "test-key")
	err := c.Configure(context.Background())
	if err == nil {
		t.Fatal("expected error for missing tenant ID")
	}
}

func TestConfigureAuthError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"error": map[string]string{
				"code":    "AUTHENTICATION_REQUIRED",
				"message": "invalid api key",
			},
		})
	}))
	defer server.Close()

	c := NewClient(server.URL, "bad-key")
	err := c.Configure(context.Background())
	if err == nil {
		t.Fatal("expected error for auth failure")
	}
}

func TestTenantPath(t *testing.T) {
	c := NewClient("https://api.example.com", "key")
	c.tenantID = "t-123"
	path := c.TenantPath("/vpcs")
	if path != "/v1/tenants/t-123/vpcs" {
		t.Errorf("expected /v1/tenants/t-123/vpcs, got %s", path)
	}
}

func TestTenantPathEscapesMalformedID(t *testing.T) {
	c := NewClient("https://api.example.com", "key")
	c.tenantID = "a/../../v1/admin"
	// A malformed override must stay a single literal segment, not restructure
	// the URL via path cleaning.
	if got := c.TenantPath("/vpcs"); got != "/v1/tenants/a%2F..%2F..%2Fv1%2Fadmin/vpcs" {
		t.Errorf("expected escaped tenant segment, got %s", got)
	}
}

func TestUserPath(t *testing.T) {
	c := NewClient("https://api.example.com", "key")
	c.userID = "u-456"
	path := c.UserPath("/sshkeys")
	if path != "/v1/users/u-456/sshkeys" {
		t.Errorf("expected /v1/users/u-456/sshkeys, got %s", path)
	}
}

func TestGet(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("expected GET, got %s", r.Method)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}))
	defer server.Close()

	c := NewClient(server.URL, "key")
	resp, err := c.Get(context.Background(), "/test", nil)
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected status 200, got %d", resp.StatusCode)
	}
}

func TestPost(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if r.Header.Get("Content-Type") != "application/json" {
			t.Error("expected Content-Type application/json")
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":"new-123"}`))
	}))
	defer server.Close()

	c := NewClient(server.URL, "key")
	resp, err := c.Post(context.Background(), "/test", map[string]string{"name": "test"})
	if err != nil {
		t.Fatalf("Post failed: %v", err)
	}
	if resp.StatusCode != http.StatusCreated {
		t.Errorf("expected status 201, got %d", resp.StatusCode)
	}
}

func TestDelete(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			t.Errorf("expected DELETE, got %s", r.Method)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	c := NewClient(server.URL, "key")
	resp, err := c.Delete(context.Background(), "/test/123")
	if err != nil {
		t.Fatalf("Delete failed: %v", err)
	}
	if resp.StatusCode != http.StatusNoContent {
		t.Errorf("expected status 204, got %d", resp.StatusCode)
	}
}

func TestAPIErrorNestedFormat(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"error": map[string]string{
				"code":    "NOT_FOUND",
				"message": "resource not found",
			},
		})
	}))
	defer server.Close()

	c := NewClient(server.URL, "key")
	_, err := c.Get(context.Background(), "/test", nil)
	if err == nil {
		t.Fatal("expected error")
	}
	if !IsNotFound(err) {
		t.Errorf("expected not found error, got %v", err)
	}
}

func TestAPIErrorFlatFormat(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		// Written as wire JSON rather than encoded from APIError: a fixture
		// marshalled through the struct under test agrees with whatever field types
		// that struct happens to declare, so it can never catch one being wrong —
		// which is exactly how `details` stayed a string.
		_, _ = w.Write([]byte(`{"code":"VALIDATION_ERROR","message":"invalid input","details":{"field":"name"}}`))
	}))
	defer server.Close()

	c := NewClient(server.URL, "key")
	_, err := c.Get(context.Background(), "/test", nil)
	if err == nil {
		t.Fatal("expected error")
	}
	apiErr, ok := err.(*APIError)
	if !ok {
		t.Fatalf("expected *APIError, got %T", err)
	}
	if apiErr.Code != "VALIDATION_ERROR" {
		t.Errorf("expected code VALIDATION_ERROR, got %s", apiErr.Code)
	}
}

// The NESTED envelope carrying a structured `details` OBJECT, which is what every
// service actually sends — identity emits {"reason","min","max"} on a refused label
// via servicekit's map[string]any.
//
// Declared as a string, this body did NOT parse: encoding/json skips a mismatched
// field and returns the error at the end, so `err == nil` was false, the nested
// branch was skipped, the flat branch found no top-level code, and the whole thing
// fell through to `ERROR: HTTP 400: {"error":{...}}` — the raw blob in a plan
// diagnostic, code lost. It reproduced on a `frostmoln_api_key` with a refused name.
//
// IsNotFound/IsConflict kept working throughout because they read StatusCode, which
// is why only the practitioner-facing half of the failure was visible.
func TestAPIErrorNestedWithObjectDetails(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"code":"INVALID_API_KEY_NAME","message":"name must be 2 to 100 characters after invisible characters are removed and the text is normalised","details":{"reason":"too_short","min":2,"max":100}}}`))
	}))
	defer server.Close()

	c := NewClient(server.URL, "key")
	_, err := c.Post(context.Background(), "/v1/api-keys", map[string]string{"name": "a"})
	if err == nil {
		t.Fatal("expected error")
	}
	apiErr, ok := err.(*APIError)
	if !ok {
		t.Fatalf("expected *APIError, got %T", err)
	}
	if apiErr.Code != "INVALID_API_KEY_NAME" {
		t.Errorf("expected code INVALID_API_KEY_NAME, got %q (raw-blob fallthrough?)", apiErr.Code)
	}
	if apiErr.Details["reason"] != "too_short" {
		t.Errorf("expected details.reason too_short, got %v", apiErr.Details["reason"])
	}
	// json decodes numbers into any as float64; the point is that they SURVIVE.
	if apiErr.Details["max"] != float64(100) {
		t.Errorf("expected details.max 100, got %v", apiErr.Details["max"])
	}
	// What the practitioner reads: the message alone, with no decoded map appended.
	want := "INVALID_API_KEY_NAME: name must be 2 to 100 characters after invisible characters are removed and the text is normalised"
	if apiErr.Error() != want {
		t.Errorf("Error() must not spill details into a diagnostic:\n got %q\nwant %q", apiErr.Error(), want)
	}
}

// identity's customer-register handler emits this same nested envelope with a
// STRING details (internal/handler/http/customer_register.go). The provider does not
// call that endpoint, but the shape proves the point: the branch must not care what
// details is. Before the `err == nil` guard came off, a string here parsed and an
// object did not — a decode that is correct only for the shapes it happens to have
// met is not a contract, it is luck.
func TestAPIErrorNestedSurvivesAnyDetailsShape(t *testing.T) {
	for _, body := range []string{
		`{"error":{"code":"INVALID_REQUEST","message":"invalid request body","details":"Key: 'x' failed"}}`,
		`{"error":{"code":"INVALID_REQUEST","message":"invalid request body","details":["a","b"]}}`,
		`{"error":{"code":"INVALID_REQUEST","message":"invalid request body","details":null}}`,
	} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(body))
		}))

		c := NewClient(server.URL, "key")
		_, err := c.Get(context.Background(), "/test", nil)
		server.Close()

		apiErr, ok := err.(*APIError)
		if !ok {
			t.Fatalf("expected *APIError, got %T for %s", err, body)
		}
		if apiErr.Code != "INVALID_REQUEST" {
			t.Errorf("raw-blob fallthrough on %s: code %q", body, apiErr.Code)
		}
		if apiErr.Message != "invalid request body" {
			t.Errorf("lost message on %s: %q", body, apiErr.Message)
		}
	}
}

func TestIsNotFound(t *testing.T) {
	if IsNotFound(nil) {
		t.Error("expected false for nil error")
	}
	if IsNotFound(&APIError{StatusCode: 500}) {
		t.Error("expected false for 500 error")
	}
	if !IsNotFound(&APIError{StatusCode: 404}) {
		t.Error("expected true for 404 error")
	}
}

func TestIsAlreadyInDesiredState(t *testing.T) {
	if IsAlreadyInDesiredState(nil) {
		t.Error("expected false for nil error")
	}
	// A 409 with the servicekit "conflict" code (already exposed / not exposed /
	// operation in progress) is the idempotent-success case.
	if !IsAlreadyInDesiredState(&APIError{StatusCode: 409, Code: "conflict"}) {
		t.Error("expected true for a 409 conflict-coded error")
	}
	// A 409 with the "invalid_state" code ("cannot expose an instance in <state>
	// state") is a REAL precondition failure and must NOT be swallowed.
	if IsAlreadyInDesiredState(&APIError{StatusCode: 409, Code: "invalid_state"}) {
		t.Error("expected false for a 409 invalid_state error (must surface)")
	}
	// A non-409 conflict code must not match either.
	if IsAlreadyInDesiredState(&APIError{StatusCode: 500, Code: "conflict"}) {
		t.Error("expected false for a non-409 status")
	}
}

func TestIsConflict(t *testing.T) {
	if IsConflict(nil) {
		t.Error("expected false for nil error")
	}
	// Any 409 matches, regardless of code — the replica-delete retry only cares
	// that the server said Conflict.
	if !IsConflict(&APIError{StatusCode: 409, Code: "conflict"}) {
		t.Error("expected true for a 409 conflict-coded error")
	}
	if !IsConflict(&APIError{StatusCode: 409, Code: "invalid_state"}) {
		t.Error("expected true for a 409 regardless of code")
	}
	if IsConflict(&APIError{StatusCode: 500, Code: "conflict"}) {
		t.Error("expected false for a non-409 status")
	}
}

func TestDeleteWithConflictRetry(t *testing.T) {
	t.Run("retries a 409 then succeeds", func(t *testing.T) {
		var calls atomic.Int32
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			if calls.Add(1) == 1 {
				w.WriteHeader(http.StatusConflict)
				_ = json.NewEncoder(w).Encode(map[string]string{"code": "conflict", "message": "resizing"})
				return
			}
			w.WriteHeader(http.StatusNoContent)
		}))
		defer server.Close()

		c := NewClient(server.URL, "test-key")
		resp, err := c.DeleteWithConflictRetry(context.Background(), "/v1/x", 2*time.Millisecond, 200*time.Millisecond)
		if err != nil {
			t.Fatalf("expected success after retry, got %v", err)
		}
		if resp.StatusCode != http.StatusNoContent {
			t.Errorf("expected 204, got %d", resp.StatusCode)
		}
		if got := calls.Load(); got < 2 {
			t.Errorf("expected >1 attempt, got %d", got)
		}
	})

	t.Run("surfaces the last 409 at the deadline", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusConflict)
			_ = json.NewEncoder(w).Encode(map[string]string{"code": "conflict", "message": "resizing"})
		}))
		defer server.Close()

		c := NewClient(server.URL, "test-key")
		_, err := c.DeleteWithConflictRetry(context.Background(), "/v1/x", 2*time.Millisecond, 20*time.Millisecond)
		if !IsConflict(err) {
			t.Fatalf("expected the persistent 409 to surface, got %v", err)
		}
	})

	t.Run("returns ctx error on cancellation mid-backoff", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusConflict)
			_ = json.NewEncoder(w).Encode(map[string]string{"code": "conflict", "message": "resizing"})
		}))
		defer server.Close()

		ctx, cancel := context.WithCancel(context.Background())
		// Cancel while the loop is parked in its 2s backoff after the first 409:
		// 100ms clears the (sub-ms) first round-trip but lands well inside the
		// sleep, so ctx.Done — not the deadline — is what returns.
		timer := time.AfterFunc(100*time.Millisecond, cancel)
		defer timer.Stop()

		c := NewClient(server.URL, "test-key")
		_, err := c.DeleteWithConflictRetry(ctx, "/v1/x", 2*time.Second, 10*time.Second)
		if err != context.Canceled {
			t.Fatalf("expected context.Canceled, got %v", err)
		}
	})
}

func TestIsTransientResizeConflict(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"non-api error", errors.New("boom"), false},
		{
			"transient: backup in progress",
			&APIError{StatusCode: 409, Code: "conflict", Message: "a backup is in progress for this instance; retry the resize after it completes"},
			true,
		},
		{
			"transient: replica not running",
			&APIError{StatusCode: 409, Code: "conflict", Message: "cannot resize: read replica 7f3a is stopped; all replicas must be running to resize (or delete it first)"},
			true,
		},
		{
			"permanent: wrong instance state",
			&APIError{StatusCode: 409, Code: "conflict", Message: "cannot resize instance in deleting state"},
			false,
		},
		{
			"permanent: replica has no data volume",
			&APIError{StatusCode: 409, Code: "conflict", Message: "cannot resize: read replica 7f3a has no data volume recorded"},
			false,
		},
		{
			"conflict message but not a 409",
			&APIError{StatusCode: 500, Code: "conflict", Message: "a backup is in progress"},
			false,
		},
		{
			"409 with a non-conflict code",
			&APIError{StatusCode: 409, Code: "invalid_state", Message: "a backup is in progress"},
			false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsTransientResizeConflict(tc.err); got != tc.want {
				t.Errorf("IsTransientResizeConflict(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

func TestPostWithConflictRetry(t *testing.T) {
	transientBody := map[string]string{"code": "conflict", "message": "cannot resize: read replica 7f3a is stopped; all replicas must be running to resize (or delete it first)"}
	permanentBody := map[string]string{"code": "conflict", "message": "cannot resize instance in deleting state"}

	t.Run("retries a transient 409 then succeeds", func(t *testing.T) {
		var calls atomic.Int32
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			if calls.Add(1) == 1 {
				w.WriteHeader(http.StatusConflict)
				_ = json.NewEncoder(w).Encode(transientBody)
				return
			}
			w.WriteHeader(http.StatusAccepted)
		}))
		defer server.Close()

		c := NewClient(server.URL, "test-key")
		resp, err := c.PostWithConflictRetry(context.Background(), "/v1/x", map[string]int{"storageGb": 15}, IsTransientResizeConflict, 2*time.Millisecond, 200*time.Millisecond)
		if err != nil {
			t.Fatalf("expected success after retry, got %v", err)
		}
		if resp.StatusCode != http.StatusAccepted {
			t.Errorf("expected 202, got %d", resp.StatusCode)
		}
		if got := calls.Load(); got < 2 {
			t.Errorf("expected >1 attempt, got %d", got)
		}
	})

	t.Run("surfaces a permanent 409 immediately without retrying", func(t *testing.T) {
		var calls atomic.Int32
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			calls.Add(1)
			w.WriteHeader(http.StatusConflict)
			_ = json.NewEncoder(w).Encode(permanentBody)
		}))
		defer server.Close()

		c := NewClient(server.URL, "test-key")
		// A generous timeout: if the predicate wrongly retried the permanent
		// 409, this budget would allow ~100 attempts before returning.
		_, err := c.PostWithConflictRetry(context.Background(), "/v1/x", nil, IsTransientResizeConflict, 2*time.Millisecond, 200*time.Millisecond)
		if !IsConflict(err) {
			t.Fatalf("expected the permanent 409 to surface, got %v", err)
		}
		if got := calls.Load(); got != 1 {
			t.Errorf("permanent 409 must not retry: expected exactly 1 attempt, got %d", got)
		}
	})

	t.Run("surfaces the last 409 when the transient conflict never clears", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusConflict)
			_ = json.NewEncoder(w).Encode(transientBody)
		}))
		defer server.Close()

		c := NewClient(server.URL, "test-key")
		_, err := c.PostWithConflictRetry(context.Background(), "/v1/x", nil, IsTransientResizeConflict, 2*time.Millisecond, 20*time.Millisecond)
		if !IsTransientResizeConflict(err) {
			t.Fatalf("expected the persistent transient 409 to surface, got %v", err)
		}
	})

	t.Run("returns ctx error on cancellation mid-backoff", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusConflict)
			_ = json.NewEncoder(w).Encode(transientBody)
		}))
		defer server.Close()

		ctx, cancel := context.WithCancel(context.Background())
		// Cancel while the loop is parked in its 2s backoff after the first 409:
		// 100ms clears the (sub-ms) first round-trip but lands well inside the
		// sleep, so ctx.Done — not the deadline — is what returns.
		timer := time.AfterFunc(100*time.Millisecond, cancel)
		defer timer.Stop()

		c := NewClient(server.URL, "test-key")
		_, err := c.PostWithConflictRetry(ctx, "/v1/x", nil, IsTransientResizeConflict, 2*time.Second, 10*time.Second)
		if err != context.Canceled {
			t.Fatalf("expected context.Canceled, got %v", err)
		}
	})
}

func TestParseResponse(t *testing.T) {
	type testType struct {
		Name string `json:"name"`
	}
	resp := &Response{Body: []byte(`{"name":"test"}`)}
	result, err := ParseResponse[testType](resp)
	if err != nil {
		t.Fatalf("ParseResponse failed: %v", err)
	}
	if result.Name != "test" {
		t.Errorf("expected name test, got %s", result.Name)
	}
}

func TestPatch(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPatch {
			t.Errorf("expected PATCH, got %s", r.Method)
		}
		if r.Header.Get("Content-Type") != "application/json" {
			t.Error("expected Content-Type application/json")
		}
		if r.URL.Path != "/v1/tenants/t-1/vpcs/vpc-1" {
			t.Errorf("expected path /v1/tenants/t-1/vpcs/vpc-1, got %s", r.URL.Path)
		}

		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["name"] != "updated" {
			t.Errorf("expected body name 'updated', got %q", body["name"])
		}

		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"vpc-1","name":"updated"}`))
	}))
	defer server.Close()

	c := NewClient(server.URL, "key")
	resp, err := c.Patch(context.Background(), "/v1/tenants/t-1/vpcs/vpc-1", map[string]string{"name": "updated"})
	if err != nil {
		t.Fatalf("Patch failed: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected status 200, got %d", resp.StatusCode)
	}
}

func TestPut(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			t.Errorf("expected PUT, got %s", r.Method)
		}
		if r.Header.Get("Content-Type") != "application/json" {
			t.Error("expected Content-Type application/json")
		}
		if r.URL.Path != "/v1/tenants/t-1/instances/i-1" {
			t.Errorf("expected path /v1/tenants/t-1/instances/i-1, got %s", r.URL.Path)
		}

		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["flavor"] != "m1.large" {
			t.Errorf("expected body flavor 'm1.large', got %q", body["flavor"])
		}

		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"i-1","flavor":"m1.large"}`))
	}))
	defer server.Close()

	c := NewClient(server.URL, "key")
	resp, err := c.Put(context.Background(), "/v1/tenants/t-1/instances/i-1", map[string]string{"flavor": "m1.large"})
	if err != nil {
		t.Fatalf("Put failed: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected status 200, got %d", resp.StatusCode)
	}
}

func TestDeleteWithQuery(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			t.Errorf("expected DELETE, got %s", r.Method)
		}
		if r.URL.Path != "/v1/tenants/t-1/resources/r-1" {
			t.Errorf("expected path /v1/tenants/t-1/resources/r-1, got %s", r.URL.Path)
		}
		if r.URL.Query().Get("force") != "true" {
			t.Errorf("expected query param force=true, got %q", r.URL.Query().Get("force"))
		}
		if r.URL.Query().Get("cascade") != "yes" {
			t.Errorf("expected query param cascade=yes, got %q", r.URL.Query().Get("cascade"))
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	c := NewClient(server.URL, "key")
	query := make(map[string][]string)
	query["force"] = []string{"true"}
	query["cascade"] = []string{"yes"}
	resp, err := c.DeleteWithQuery(context.Background(), "/v1/tenants/t-1/resources/r-1", query)
	if err != nil {
		t.Fatalf("DeleteWithQuery failed: %v", err)
	}
	if resp.StatusCode != http.StatusNoContent {
		t.Errorf("expected status 204, got %d", resp.StatusCode)
	}
}

func TestSetTenantIDForTest(t *testing.T) {
	c := NewClient("https://api.example.com", "key")
	if c.TenantID() != "" {
		t.Errorf("expected empty tenant ID initially, got %s", c.TenantID())
	}
	c.SetTenantIDForTest("tenant-abc")
	if c.TenantID() != "tenant-abc" {
		t.Errorf("expected tenant ID tenant-abc, got %s", c.TenantID())
	}
	// Verify it works with TenantPath
	path := c.TenantPath("/vpcs")
	if path != "/v1/tenants/tenant-abc/vpcs" {
		t.Errorf("expected /v1/tenants/tenant-abc/vpcs, got %s", path)
	}
}

func TestSetUserIDForTest(t *testing.T) {
	c := NewClient("https://api.example.com", "key")
	if c.UserID() != "" {
		t.Errorf("expected empty user ID initially, got %s", c.UserID())
	}
	c.SetUserIDForTest("user-xyz")
	if c.UserID() != "user-xyz" {
		t.Errorf("expected user ID user-xyz, got %s", c.UserID())
	}
	// Verify it works with UserPath
	path := c.UserPath("/sshkeys")
	if path != "/v1/users/user-xyz/sshkeys" {
		t.Errorf("expected /v1/users/user-xyz/sshkeys, got %s", path)
	}
}

func TestDefaultPollConfig(t *testing.T) {
	cfg := DefaultPollConfig()
	if cfg.Interval != 2*time.Second {
		t.Errorf("expected interval 2s, got %v", cfg.Interval)
	}
	if cfg.Timeout != 5*time.Minute {
		t.Errorf("expected timeout 5m, got %v", cfg.Timeout)
	}
	if cfg.PollFunc != nil {
		t.Error("expected PollFunc to be nil by default")
	}
	if len(cfg.TargetStates) != 0 {
		t.Errorf("expected empty TargetStates, got %v", cfg.TargetStates)
	}
	if len(cfg.ErrorStates) != 0 {
		t.Errorf("expected empty ErrorStates, got %v", cfg.ErrorStates)
	}
	if cfg.ResourceName != "" {
		t.Errorf("expected empty ResourceName, got %s", cfg.ResourceName)
	}
}

func TestGetOperation(t *testing.T) {
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		if r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-1/operations/op-1" {
			_ = json.NewEncoder(w).Encode(Operation{
				OperationID:  "op-1",
				Status:       "completed",
				ResourceType: "load_balancer",
				ResourceID:   "lb-1",
			})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	c := NewClient(server.URL, "test-key") // pragma: allowlist secret
	c.SetTenantIDForTest("t-1")
	op, err := c.GetOperation(context.Background(), "op-1")
	if err != nil {
		t.Fatalf("GetOperation failed: %v", err)
	}
	if op.ResourceID != "lb-1" || op.Status != "completed" {
		t.Errorf("unexpected operation: %+v", op)
	}
	// The legacy untenanted route skips its ownership check when the caller's
	// signed tenant is empty. It is a transition-window fallback only, so a
	// working tenant-scoped read must never touch it.
	for _, p := range paths {
		if p == "/v1/operations/op-1" {
			t.Fatalf("read the legacy untenanted route despite a healthy scoped route: %v", paths)
		}
	}
}

// A new provider can be pointed at a gateway that has not yet published the
// tenant-scoped route — the provider is customer-pinned, so that ordering is
// normal. WaitForState retries every poll error to the timeout, so without this
// fallback the routing 404 would surface as "timed out waiting for operation".
// writeNotFound writes provisioning's 404 envelope. Its code is deliberately the
// same for "no such operation" and "not yours" — the route is
// existence-oracle-free.
func writeNotFound(w http.ResponseWriter) {
	w.WriteHeader(http.StatusNotFound)
	_, _ = w.Write([]byte(`{"error":{"code":"NOT_FOUND","message":"operation not found"}}`))
}

// writeRouteNotFound writes the api-gateway's answer for a path it does not serve.
func writeRouteNotFound(w http.ResponseWriter) {
	w.WriteHeader(http.StatusNotFound)
	_, _ = w.Write([]byte(`{"error":{"code":"ROUTE_NOT_FOUND","message":"no route found for path"}}`))
}

func TestGetOperationFallsBackWhenTheGatewayHasNoScopedRoute(t *testing.T) {
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		if r.Method == http.MethodGet && r.URL.Path == "/v1/operations/op-legacy" {
			_ = json.NewEncoder(w).Encode(Operation{OperationID: "op-legacy", Status: "completed", ResourceID: "vol-1"})
			return
		}
		writeRouteNotFound(w)
	}))
	defer server.Close()

	c := NewClient(server.URL, "test-key") // pragma: allowlist secret
	c.SetTenantIDForTest("t-1")
	op, err := c.GetOperation(context.Background(), "op-legacy")
	if err != nil {
		t.Fatalf("GetOperation failed: %v", err)
	}
	if op.ResourceID != "vol-1" {
		t.Errorf("expected resourceId vol-1, got %s", op.ResourceID)
	}
	want := []string{"/v1/tenants/t-1/operations/op-legacy", "/v1/operations/op-legacy"}
	if len(paths) != len(want) || paths[0] != want[0] || paths[1] != want[1] {
		t.Errorf("expected scoped-then-legacy, got %v", paths)
	}
}

// THE SECURITY-LOAD-BEARING CASE. The scoped route answers 404/NOT_FOUND both for
// an operation that does not exist and for one the caller does not own. Retrying
// the legacy route on that would answer our own ownership denial by re-asking the
// route that skips the ownership check — for an org-invited caller (empty signed
// tenant) that is an unchecked read of any operation in the estate. It would also
// keep the legacy-read metric that gates the route's removal permanently non-zero.
func TestGetOperationDoesNotFallBackOnAServiceNotFound(t *testing.T) {
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		writeNotFound(w)
	}))
	defer server.Close()

	c := NewClient(server.URL, "test-key") // pragma: allowlist secret
	c.SetTenantIDForTest("t-1")
	if _, err := c.GetOperation(context.Background(), "op-1"); err == nil {
		t.Fatal("expected an error when the operation is not found or not ours")
	}
	if len(paths) != 1 || paths[0] != "/v1/tenants/t-1/operations/op-1" {
		t.Errorf("expected exactly one scoped request and no legacy retry, got %v", paths)
	}
}

// THE INVARIANT THAT THE RENAME MADE ONLY PROSE. The fallback allow-lists
// exactly ONE code, and it is deliberately the OLD one: a gateway new enough to
// answer PATH_NOT_ROUTED is necessarily new enough to serve the tenant-scoped
// route, so it never needs the fallback — and the legacy route it would fall
// back to was removed from the catalog on 2026-08-14 anyway.
//
// Without this test a later "finish the rename" sweep could update the constant
// to PATH_NOT_ROUTED and leave the whole suite green. If the legacy route were
// ever restored, any catalog fault on the scoped route would then auto-retry the
// route that skips the ownership check — the exact escalation the allow-list of
// one exists to prevent.
func TestGetOperationDoesNotFallBackOnPathNotRouted(t *testing.T) {
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":{"code":"PATH_NOT_ROUTED","message":"no route found for path"}}`))
	}))
	defer server.Close()

	c := NewClient(server.URL, "test-key") // pragma: allowlist secret
	c.SetTenantIDForTest("t-1")
	if _, err := c.GetOperation(context.Background(), "op-1"); err == nil {
		t.Fatal("expected an error: a gateway answering PATH_NOT_ROUTED serves the scoped route, so there is nothing to fall back to")
	}
	if len(paths) != 1 || paths[0] != "/v1/tenants/t-1/operations/op-1" {
		t.Errorf("expected exactly one scoped request and no legacy retry, got %v", paths)
	}
}

// A "../" in the id must not collapse the scoped path back onto the legacy route:
// the URL is assembled with path.Join, which cleans dot segments.
func TestGetOperationEscapesTheOperationID(t *testing.T) {
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		writeNotFound(w)
	}))
	defer server.Close()

	c := NewClient(server.URL, "test-key") // pragma: allowlist secret
	c.SetTenantIDForTest("t-1")
	_, _ = c.GetOperation(context.Background(), "../../v1/operations/victim")

	if len(paths) != 1 {
		t.Fatalf("expected one request, got %v", paths)
	}
	if paths[0] == "/v1/operations/victim" {
		t.Fatalf("traversal collapsed onto the legacy route: %v", paths)
	}
	if !strings.HasPrefix(paths[0], "/v1/tenants/t-1/operations/") {
		t.Errorf("escaped id left the scoped path: %v", paths)
	}
}

func TestWaitForOperationCompleted(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-1/operations/op-2" {
			calls++
			status := "running"
			if calls >= 2 {
				status = "completed"
			}
			_ = json.NewEncoder(w).Encode(Operation{OperationID: "op-2", Status: status, ResourceID: "lb-2"})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	c := NewClient(server.URL, "test-key") // pragma: allowlist secret
	c.SetTenantIDForTest("t-1")
	op, err := c.WaitForOperation(context.Background(), "op-2", 5*time.Millisecond, 2*time.Second)
	if err != nil {
		t.Fatalf("WaitForOperation failed: %v", err)
	}
	if op.ResourceID != "lb-2" {
		t.Errorf("expected resourceId lb-2, got %s", op.ResourceID)
	}
}

func TestWaitForOperationFailed(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-1/operations/op-3" {
			_ = json.NewEncoder(w).Encode(Operation{OperationID: "op-3", Status: "failed", Error: "boom"})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	c := NewClient(server.URL, "test-key") // pragma: allowlist secret
	c.SetTenantIDForTest("t-1")
	_, err := c.WaitForOperation(context.Background(), "op-3", 5*time.Millisecond, 2*time.Second)
	if err == nil {
		t.Fatal("expected error for failed operation")
	}
}

func TestDoRetries429WithRetryAfter(t *testing.T) {
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		if calls == 1 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusTooManyRequests)
			// The gateway's rate-limit shape: "error" is a string, not an object.
			_, _ = w.Write([]byte(`{"error":"rate limit exceeded","message":"Too many requests. Please retry after 1 seconds.","retry_after":1}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()

	c := NewClient(server.URL, "test-key")
	resp, err := c.Get(context.Background(), "/v1/things", nil)
	if err != nil {
		t.Fatalf("expected the 429 to be retried to success, got: %s", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 after retry, got %d", resp.StatusCode)
	}
	if calls != 2 {
		t.Fatalf("expected exactly 2 requests (429 then 200), got %d", calls)
	}
}

func TestDoStops429RetryOnContextCancel(t *testing.T) {
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.Header().Set("Retry-After", "1")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":"rate limit exceeded","message":"still limited"}`))
	}))
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	c := NewClient(server.URL, "test-key")
	_, err := c.Get(ctx, "/v1/things", nil)
	if err == nil {
		t.Fatal("expected an error when the context expires during the 429 backoff")
	}
	if calls != 1 {
		t.Fatalf("expected exactly 1 request before the context expired, got %d", calls)
	}
}

func TestDoParses429RetryAfterHeader(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", "7")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":"rate limit exceeded","message":"still limited"}`))
	}))
	defer server.Close()

	c := NewClient(server.URL, "test-key")
	// Bypass Do's retry loop: a single attempt, so the parsed error surfaces.
	_, err := c.do(context.Background(), http.MethodGet, "/v1/things", nil, nil, "")
	apiErr, ok := err.(*APIError)
	if !ok || apiErr.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("expected a 429 APIError, got: %v", err)
	}
	if apiErr.RetryAfter != 7 {
		t.Fatalf("expected RetryAfter 7 from the Retry-After header, got %d", apiErr.RetryAfter)
	}
}

func TestRateLimitBackoff(t *testing.T) {
	cases := []struct {
		attempt, serverSecs int
		want                time.Duration
	}{
		{0, 0, 2 * time.Second},    // no hint: base
		{0, 5, 5 * time.Second},    // server hint above base wins
		{1, 1, 4 * time.Second},    // escalation above a small hint wins
		{3, 0, 16 * time.Second},   // exponential growth
		{4, 0, 30 * time.Second},   // capped
		{2, 120, 30 * time.Second}, // hostile/huge server value capped
	}
	for _, tc := range cases {
		if got := rateLimitBackoff(tc.attempt, tc.serverSecs); got != tc.want {
			t.Errorf("rateLimitBackoff(%d, %d) = %s, want %s", tc.attempt, tc.serverSecs, got, tc.want)
		}
	}
}

// A SERVICE's 429 must keep its own code and details. The gateway's rate-limit
// body has neither, and parsing that shape first flattened every service 429
// into RATE_LIMITED with a nil Details — which silently disabled every caller
// that branches on `details.cap` to decide what the refusal even means.
//
// End to end through Do deliberately: a hand-built APIError proves nothing about
// what the wire produces, and that is exactly how this defect survived a review.
func TestServiceRateLimitKeepsItsCodeAndDetails(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"code":"rate_limited",` +
			`"message":"this tenant already has 1 image import(s) running (limit 1); wait for one to finish",` +
			`"details":{"cap":"concurrent_imports","limit":1,"used":1,"retryAfterSeconds":30}}`))
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "k")
	_, err := c.Do(context.Background(), http.MethodPost, "/v1/images/img-1/import", nil, nil)

	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("want an *APIError, got %v", err)
	}
	if apiErr.Code != "rate_limited" {
		t.Errorf("Code = %q, want the service's own code", apiErr.Code)
	}
	if got, _ := apiErr.Details["cap"].(string); got != "concurrent_imports" {
		t.Errorf("details.cap = %q, want concurrent_imports — the discriminator was dropped", got)
	}
	// The body's wait must be picked up when no header carries it, or Do's
	// backoff falls back to its own escalation and ignores what the server said.
	if apiErr.RetryAfter != 30 {
		t.Errorf("RetryAfter = %d, want 30 from details.retryAfterSeconds", apiErr.RetryAfter)
	}
}

// The gateway's own shape has no `code`, so it must still land on the legacy
// branch — this is the fallback the ordering above must not break.
func TestGatewayRateLimitStillParses(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":"rate limit exceeded","message":"too many requests","retry_after":7}`))
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "k")
	_, err := c.Do(context.Background(), http.MethodGet, "/v1/images", nil, nil)

	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("want an *APIError, got %v", err)
	}
	if apiErr.Code != "RATE_LIMITED" || apiErr.RetryAfter != 7 {
		t.Errorf("gateway 429 parsed as code=%q retry_after=%d, want RATE_LIMITED/7", apiErr.Code, apiErr.RetryAfter)
	}
}

// A quota refusal must cost ONE request, not six. Do's escalating backoff is
// sized for the gateway's per-minute window; spending it on a cap that clears
// when an import finishes means ~60s of extra load aimed at the component that
// is already at its limit, and the same refusal at the end of it.
func TestServiceQuotaRefusalIsNotRetried(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"code":"rate_limited","message":"at the cap",` +
			`"details":{"cap":"concurrent_imports","limit":1,"used":1}}`))
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "k")
	start := time.Now()
	_, err := c.Do(context.Background(), http.MethodPost, "/v1/images/img-1/import", nil, nil)
	if err == nil {
		t.Fatal("expected the refusal to surface")
	}
	if calls != 1 {
		t.Errorf("%d requests for one quota refusal, want 1", calls)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("refusal took %s — the rate-limit backoff is still running for a quota cap", elapsed)
	}
}

// FlatEnvelope is a security-relevant marker with exactly one setter, so pin it
// directly rather than only through vpc_route's behaviour. The nested case is
// the one that matters: it is what a gateway routing failure looks like, and a
// mutant that sets the marker there re-arms the state-drop bug.
func TestAPIErrorFlatEnvelopeMarksOnlyFlatRefusalBodies(t *testing.T) {
	cases := []struct {
		name     string
		status   int
		body     string
		wantCode string
		wantFlat bool
	}{
		{"flat service refusal", http.StatusNotFound, `{"code":"ROUTE_NOT_FOUND","message":"no route with that destination"}`, "ROUTE_NOT_FOUND", true},
		{"nested gateway refusal", http.StatusNotFound, `{"error":{"code":"ROUTE_NOT_FOUND","message":"no route found for path"}}`, "ROUTE_NOT_FOUND", false},
		{"nested service refusal (provisioning shape)", http.StatusNotFound, `{"error":{"code":"NOT_FOUND","message":"operation not found"}}`, "NOT_FOUND", false},
		{"flat refusal with an object details", http.StatusConflict, `{"code":"ROUTE_WRITE_CONFLICT","message":"busy","details":{"cap":5}}`, "ROUTE_WRITE_CONFLICT", true},
		{"unparseable body", http.StatusNotFound, `404 page not found`, "ERROR", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()

			c := NewClient(server.URL, "test-key") // pragma: allowlist secret
			_, err := c.Get(context.Background(), "/v1/anything", nil)

			var apiErr *APIError
			if !errors.As(err, &apiErr) {
				t.Fatalf("want *APIError, got %v", err)
			}
			if apiErr.Code != tc.wantCode {
				t.Errorf("Code = %q, want %q", apiErr.Code, tc.wantCode)
			}
			if apiErr.FlatEnvelope != tc.wantFlat {
				t.Errorf("FlatEnvelope = %v, want %v — this decides whether vpc_route drops the resource from state", apiErr.FlatEnvelope, tc.wantFlat)
			}
		})
	}
}
