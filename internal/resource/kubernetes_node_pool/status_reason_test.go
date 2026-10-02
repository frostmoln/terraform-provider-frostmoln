package kubernetes_node_pool

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestPollErrorStateNamesWhy(t *testing.T) {
	for _, tc := range []struct {
		name string
		pool apiNodePool
		want string
	}{
		{
			name: "with detail",
			pool: func() apiNodePool {
				p := testPool(statusError)
				p.StatusReason, p.StatusMessage, p.FailedStep = "QuotaExceeded", "instance quota exhausted", "node_pool"
				return p
			}(),
			want: "Kubernetes node pool entered error state in phase node_pool (QuotaExceeded): instance quota exhausted",
		},
		{name: "without detail", pool: testPool(statusError), want: "kubernetes_node_pool entered error state: error"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == poolPath {
					writeJSON(t, w, tc.pool)
					return
				}
				w.WriteHeader(http.StatusNotFound)
			}))
			defer server.Close()
			r := testResource(newTestClient(t, server))

			err := r.pollPool(context.Background(), "c-1", "np-2", []string{statusActive}, []string{statusError, statusDeleted}, time.Second)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("got %v, want %q", err, tc.want)
			}

			// The detail also lands in state on read.
			var m KubernetesNodePoolModel
			m.fromAPI(&tc.pool)
			if m.StatusReason.ValueString() != tc.pool.StatusReason || m.FailedStep.ValueString() != tc.pool.FailedStep ||
				m.StatusMessage.ValueString() != tc.pool.StatusMessage {
				t.Errorf("fromAPI: got %v %v %v", m.StatusReason, m.StatusMessage, m.FailedStep)
			}
		})
	}
}
