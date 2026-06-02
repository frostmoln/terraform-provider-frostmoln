package client

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
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
		json.NewDecoder(r.Body).Decode(&body)
		if body["name"] != "updated" {
			t.Errorf("expected body name 'updated', got %q", body["name"])
		}

		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id":"vpc-1","name":"updated"}`))
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
		json.NewDecoder(r.Body).Decode(&body)
		if body["flavor"] != "m1.large" {
			t.Errorf("expected body flavor 'm1.large', got %q", body["flavor"])
		}

		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id":"i-1","flavor":"m1.large"}`))
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
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/v1/operations/op-1" {
			json.NewEncoder(w).Encode(Operation{
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
	op, err := c.GetOperation(context.Background(), "op-1")
	if err != nil {
		t.Fatalf("GetOperation failed: %v", err)
	}
	if op.ResourceID != "lb-1" || op.Status != "completed" {
		t.Errorf("unexpected operation: %+v", op)
	}
}

func TestWaitForOperationCompleted(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/v1/operations/op-2" {
			calls++
			status := "running"
			if calls >= 2 {
				status = "completed"
			}
			json.NewEncoder(w).Encode(Operation{OperationID: "op-2", Status: status, ResourceID: "lb-2"})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	c := NewClient(server.URL, "test-key") // pragma: allowlist secret
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
		if r.Method == http.MethodGet && r.URL.Path == "/v1/operations/op-3" {
			json.NewEncoder(w).Encode(Operation{OperationID: "op-3", Status: "failed", Error: "boom"})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	c := NewClient(server.URL, "test-key") // pragma: allowlist secret
	_, err := c.WaitForOperation(context.Background(), "op-3", 5*time.Millisecond, 2*time.Second)
	if err == nil {
		t.Fatal("expected error for failed operation")
	}
}
