package client

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

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
		json.NewEncoder(w).Encode(UserProfile{
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

func TestConfigureNoTenantID(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		json.NewEncoder(w).Encode(UserProfile{ID: "user-123"})
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
		json.NewEncoder(w).Encode(map[string]interface{}{
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
		w.Write([]byte(`{"status":"ok"}`))
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
		w.Write([]byte(`{"id":"new-123"}`))
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
		json.NewEncoder(w).Encode(map[string]interface{}{
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
		json.NewEncoder(w).Encode(APIError{
			Code:    "VALIDATION_ERROR",
			Message: "invalid input",
			Details: "name is required",
		})
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
