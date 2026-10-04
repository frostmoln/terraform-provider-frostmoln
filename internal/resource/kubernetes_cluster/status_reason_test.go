package kubernetes_cluster

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
)

// errorServer answers every cluster and node-pool GET with the given rows,
// and 404s the tenant event stream so the poll falls back to its timer.
func errorServer(t *testing.T, cluster apiKubernetesCluster, pool apiNodePool) *client.Client {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/tenants/t-1/kubernetes-clusters/c-1":
			writeJSON(t, w, cluster)
		case "/v1/tenants/t-1/kubernetes-clusters/c-1/node-pools/np-1":
			writeJSON(t, w, pool)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
	c := client.NewClient(server.URL, "test-key", client.WithHTTPClient(server.Client()))
	c.SetTenantIDForTest("t-1")
	return c
}

// The pilot's failed create said only "entered error state: error"; the wait
// must name the phase, the failure class and the message when the API has them.
func TestPollErrorStateNamesWhy(t *testing.T) {
	failed := runningCluster()
	failed.Status = statusError
	failed.StatusReason = "InvalidState"
	failed.StatusMessage = "the API load balancer could not be registered"
	failed.FailedStep = "api_endpoint"
	pool := initialPool(statusError)
	pool.StatusReason = "QuotaExceeded"
	pool.StatusMessage = "instance quota exhausted"
	pool.FailedStep = "node_pool"
	r := testResource(errorServer(t, failed, pool))
	targets, errs := []string{statusRunning}, []string{statusError, statusDeleted}

	err := r.pollCluster(context.Background(), "c-1", targets, errs, time.Second)
	want := "Kubernetes cluster entered error state in phase api_endpoint (InvalidState): the API load balancer could not be registered"
	if err == nil || err.Error() != want {
		t.Errorf("cluster: got %v, want %q", err, want)
	}

	err = r.pollNodePool(context.Background(), "c-1", "np-1", []string{statusActive}, errs, time.Second)
	want = "Kubernetes cluster initial node pool entered error state in phase node_pool (QuotaExceeded): instance quota exhausted"
	if err == nil || err.Error() != want {
		t.Errorf("pool: got %v, want %q", err, want)
	}
}

// Without the detail fields the generic poller error is kept.
func TestPollErrorStateWithoutDetailKeepsGenericError(t *testing.T) {
	failed := runningCluster()
	failed.Status = statusError
	r := testResource(errorServer(t, failed, initialPool(statusError)))

	err := r.pollCluster(context.Background(), "c-1", []string{statusRunning}, []string{statusError}, time.Second)
	if err == nil || !strings.Contains(err.Error(), "kubernetes_cluster entered error state: error") {
		t.Errorf("got %v, want the generic error-state error", err)
	}
}

func TestFromAPIStatusDetail(t *testing.T) {
	c := runningCluster()
	var m KubernetesClusterModel
	m.fromAPI(&c)
	if !m.StatusReason.IsNull() || !m.StatusMessage.IsNull() || !m.FailedStep.IsNull() {
		t.Errorf("running cluster: want null detail, got %v %v %v", m.StatusReason, m.StatusMessage, m.FailedStep)
	}

	c.Status, c.StatusReason, c.StatusMessage, c.FailedStep = statusError, "InvalidState", "boom", "api_endpoint"
	m.fromAPI(&c)
	if m.StatusReason.ValueString() != "InvalidState" || m.StatusMessage.ValueString() != "boom" || m.FailedStep.ValueString() != "api_endpoint" {
		t.Errorf("error cluster: got %v %v %v", m.StatusReason, m.StatusMessage, m.FailedStep)
	}
}

func TestSetInitialNodePoolStatusDetail(t *testing.T) {
	var m KubernetesClusterModel
	pool := initialPool(statusActive)
	m.setInitialNodePool(&pool)
	if p := m.InitialNodePool; !p.StatusReason.IsNull() || !p.StatusMessage.IsNull() || !p.FailedStep.IsNull() {
		t.Errorf("active pool: want null detail, got %v %v %v", p.StatusReason, p.StatusMessage, p.FailedStep)
	}

	pool = initialPool(statusError)
	pool.StatusReason, pool.StatusMessage, pool.FailedStep = "QuotaExceeded", "instance quota exhausted", "node_pool"
	m.setInitialNodePool(&pool)
	if p := m.InitialNodePool; p.StatusReason.ValueString() != "QuotaExceeded" || p.StatusMessage.ValueString() != "instance quota exhausted" || p.FailedStep.ValueString() != "node_pool" {
		t.Errorf("error pool: got %v %v %v", p.StatusReason, p.StatusMessage, p.FailedStep)
	}
}
