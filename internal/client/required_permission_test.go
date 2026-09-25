package client

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The org-role refusal diagnostic (Human-axis IAM P2): the two permission-denial
// envelopes carry required_permission; a plan diagnostic that stops at "your
// role does not grant permission" leaves which-grant and what-now to guesswork.
// These tests pin BOTH wire shapes landing on the field, the suffix appearing
// only for the two permission-negative codes, and the withholding of
// server-sent nonsense — the same discipline the CLI's remedy applies.

func testJSONServer(t *testing.T, status int, body map[string]any) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(body)
	}))
	t.Cleanup(server.Close)
	return server
}

func TestAPIError_DecodesRequiredPermission_BothEnvelopes(t *testing.T) {
	t.Run("nested gateway enforce 403", func(t *testing.T) {
		server := testJSONServer(t, http.StatusForbidden, map[string]any{
			"error": map[string]any{
				"code":                "INSUFFICIENT_PERMISSIONS",
				"message":             "your role does not grant permission to perform this action",
				"required_permission": "compute:instances:create",
			},
		})

		c := NewClient(server.URL, "key")
		_, err := c.Get(context.Background(), "/test", nil)
		if err == nil {
			t.Fatal("expected error")
		}
		apiErr, ok := err.(*APIError)
		if !ok {
			t.Fatalf("expected *APIError, got %T", err)
		}
		if apiErr.RequiredPermission != "compute:instances:create" {
			t.Errorf("nested envelope must decode required_permission, got %q", apiErr.RequiredPermission)
		}
	})

	t.Run("flat servicekit human deny", func(t *testing.T) {
		server := testJSONServer(t, http.StatusForbidden, map[string]any{
			"code":                "insufficient_permission",
			"message":             "your role does not grant permission to perform this action",
			"required_permission": "storage:volumes:delete",
		})

		c := NewClient(server.URL, "key")
		_, err := c.Get(context.Background(), "/test", nil)
		if err == nil {
			t.Fatal("expected error")
		}
		apiErr, ok := err.(*APIError)
		if !ok {
			t.Fatalf("expected *APIError, got %T", err)
		}
		if apiErr.RequiredPermission != "storage:volumes:delete" {
			t.Errorf("flat envelope must decode required_permission, got %q", apiErr.RequiredPermission)
		}
	})
}

func TestAPIError_ErrorSuffix(t *testing.T) {
	cases := map[string]struct {
		err      *APIError
		want     string
		wantNone bool
	}{
		"gateway code with permission": {
			err:  &APIError{Code: "INSUFFICIENT_PERMISSIONS", Message: "denied", RequiredPermission: "compute:instances:create"},
			want: " (required permission: compute:instances:create)",
		},
		"servicekit code with permission": {
			err:  &APIError{Code: "insufficient_permission", Message: "denied", RequiredPermission: "storage:volumes:delete"},
			want: " (required permission: storage:volumes:delete)",
		},
		"unclassified marker withheld": {
			err:      &APIError{Code: "INSUFFICIENT_PERMISSIONS", Message: "denied", RequiredPermission: "unclassified"},
			wantNone: true,
		},
		"path echo withheld": {
			err:      &APIError{Code: "INSUFFICIENT_PERMISSIONS", Message: "denied", RequiredPermission: "/api/v1/tenants/x/instances"},
			wantNone: true,
		},
		"bidi escape withheld": {
			err:      &APIError{Code: "INSUFFICIENT_PERMISSIONS", Message: "denied", RequiredPermission: "compute:instances:crea\u202Ere"},
			wantNone: true,
		},
		"empty withheld": {
			err:      &APIError{Code: "insufficient_permission", Message: "denied"},
			wantNone: true,
		},
		"other code never suffixed": {
			err:      &APIError{Code: "FEATURE_NOT_ENABLED", Message: "denied", RequiredPermission: "compute:instances:create"},
			wantNone: true,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := tc.err.Error()
			if tc.wantNone {
				if strings.Contains(got, "required permission") {
					t.Errorf("must keep the bare line, got %q", got)
				}
			} else if !strings.Contains(got, tc.want) {
				t.Errorf("want the demanded permission appended, got %q", got)
			}
		})
	}
}
