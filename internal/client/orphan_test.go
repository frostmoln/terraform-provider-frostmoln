package client

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestClassifyOperationFailure pins the single predicate the classified arms
// hang off: a terminal failed operation is a refusal (the platform decided
// no), anything unreadable or not-yet-terminal is unknown — silence is never
// read as either.
func TestClassifyOperationFailure(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   any
		want   OperationVerdict
	}{
		{"terminal failed is refused", http.StatusOK, map[string]any{
			"operationId": "op-1", "status": "failed", "error": "quota exceeded",
		}, OperationRefused},
		{"still running is unknown", http.StatusOK, map[string]any{
			"operationId": "op-1", "status": "running",
		}, OperationUnknown},
		{"cancelled is unknown", http.StatusOK, map[string]any{
			"operationId": "op-1", "status": "cancelled",
		}, OperationUnknown},
		{"completed is unknown-on-failure-path", http.StatusOK, map[string]any{
			"operationId": "op-1", "status": "completed",
		}, OperationUnknown},
		{"unreadable operation is unknown", http.StatusNotFound, map[string]string{
			"code": "NOT_FOUND", "message": "not found",
		}, OperationUnknown},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/v1/me" {
					_ = json.NewEncoder(w).Encode(map[string]string{"id": "u-1", "tenantId": "t-1"})
					return
				}
				if r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-1/operations/op-1" {
					_ = json.NewEncoder(w).Encode(tt.body)
					return
				}
				w.WriteHeader(http.StatusNotFound)
				_ = json.NewEncoder(w).Encode(map[string]string{"code": "NOT_FOUND", "message": "not found"})
			}))
			defer server.Close()

			c := NewClient(server.URL, "test-key") // pragma: allowlist secret
			if err := c.Configure(context.Background()); err != nil {
				t.Fatalf("client configure failed: %v", err)
			}
			if got := c.ClassifyOperationFailure(context.Background(), "op-1"); got != tt.want {
				t.Fatalf("ClassifyOperationFailure = %v, want %v", got, tt.want)
			}
		})
	}
}
