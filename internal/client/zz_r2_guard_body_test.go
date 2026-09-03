package client

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// Bodies copied VERBATIM from the provisioning guard's actual responses,
// dumped by TestZZDumpRefusalBodies in provisioning/internal/handler/http.
func TestZZ_R2_ProvisioningGuardBodies(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
	}{
		{"guard 404 subnet", 404, `{"code":"not_found","message":"subnet not found"}`},
		{"guard 404 public ip", 404, `{"code":"not_found","message":"public IP not found"}`},
		{"guard 404 router", 404, `{"code":"not_found","message":"router not found"}`},
		{"guard 503", 503, `{"error":{"code":"SERVICE_UNAVAILABLE","message":"ownership could not be verified, retry"}}`},
		{"guard 400", 400, `{"error":{"code":"TENANT_NOT_PROVISIONED","message":"your tenant is still being set up; please try again in a moment"}}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json; charset=utf-8")
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()

			c := NewClient(srv.URL, "tok")
			_, err := c.Delete(context.Background(), "/v1/tenants/t1/subnets/s1")
			if err == nil {
				t.Fatalf("expected an error")
			}
			t.Logf("RESULT %s: err=%v IsNotFound=%v IsConflict=%v IsAlreadyInDesiredState=%v IsLegacyUnroutedPath=%v IsTransientResizeConflict=%v",
				tc.name, err, IsNotFound(err), IsConflict(err), IsAlreadyInDesiredState(err),
				IsLegacyUnroutedPath(err), IsTransientResizeConflict(err))
		})
	}
}
