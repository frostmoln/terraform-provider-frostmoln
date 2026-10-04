package kubernetes_cluster_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/acctest"
)

// The initial_node_pool block's status_reason / status_message / failed_step,
// run by real Terraform against a scripted kubernetes API. Like the appgw
// plan-stability tests it is TF_ACC-gated but needs no Frostmoln:
//
//	TF_ACC=1 go test ./internal/resource/kubernetes_cluster/ -run TestAccInitialPoolStatusDetail
//
// terraform-plugin-testing re-plans after every apply step and FAILS the step on
// a non-empty plan, so each Config step is itself a perpetual-diff assertion for
// the nested computed attributes.

type clusterAPI struct {
	mu   sync.Mutex
	pool map[string]any
}

const clusterBase = "/v1/tenants/t-1/kubernetes-clusters"

func (a *clusterAPI) setPool(kv map[string]any) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for k, v := range kv {
		if v == nil {
			delete(a.pool, k)
			continue
		}
		a.pool[k] = v
	}
}

func (a *clusterAPI) handler() http.Handler {
	cluster := map[string]any{
		"id": "c-1", "name": "k", "tenantId": "t-1", "status": "running",
		"kubernetesVersion": "1.35", "controlPlaneTier": "development", "region": "fbg",
		"vpcId": "vpc-1", "subnetId": "sn-1", "addons": []string{}, "createdAt": "2026-10-04T00:00:00Z",
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a.mu.Lock()
		defer a.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		write := func(code int, v any) {
			w.WriteHeader(code)
			_ = json.NewEncoder(w).Encode(v)
		}
		switch p := r.URL.Path; {
		case p == "/v1/me":
			write(http.StatusOK, map[string]any{"id": "u-1", "tenantId": "t-1"})
		case r.Method == http.MethodPost && p == clusterBase:
			write(http.StatusAccepted, cluster)
		case r.Method == http.MethodGet && p == clusterBase+"/c-1":
			write(http.StatusOK, cluster)
		case r.Method == http.MethodGet && p == clusterBase+"/c-1/kubeconfig":
			write(http.StatusOK, map[string]any{"kubeconfig": "apiVersion: v1"})
		case r.Method == http.MethodGet && p == clusterBase+"/c-1/node-pools":
			write(http.StatusOK, map[string]any{"nodePools": []any{a.pool}})
		case r.Method == http.MethodGet && p == clusterBase+"/c-1/node-pools/np-1":
			write(http.StatusOK, a.pool)
		case r.Method == http.MethodDelete && p == clusterBase+"/c-1":
			cluster["status"] = "deleted"
			a.pool["status"] = "deleted"
			w.WriteHeader(http.StatusAccepted)
		default:
			write(http.StatusNotFound, map[string]string{"code": "NOT_FOUND", "message": r.Method + " " + p})
		}
	})
}

func startClusterAPI(t *testing.T) *clusterAPI {
	t.Helper()
	api := &clusterAPI{pool: map[string]any{
		"id": "np-1", "clusterId": "c-1", "name": "default", "status": "active",
		"flavorId": "f-1", "nodeCount": 1, "isInitial": true, "createdAt": "2026-10-04T00:00:00Z",
	}}
	srv := httptest.NewServer(api.handler())
	t.Cleanup(srv.Close)
	t.Setenv("FROSTMOLN_API_ENDPOINT", srv.URL)
	t.Setenv("FROSTMOLN_API_KEY", "acc-test-key") // pragma: allowlist secret
	return api
}

const initialPoolAddr = "frostmoln_kubernetes_cluster.k"

const initialPoolConfig = `
resource "frostmoln_kubernetes_cluster" "k" {
  name      = "k"
  vpc_id    = "vpc-1"
  subnet_id = "sn-1"
  addons    = []

  initial_node_pool = {
    flavor_id = "f-1"
  }
}
`

func TestAccInitialPoolStatusDetail(t *testing.T) {
	api := startClusterAPI(t)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: acctest.TestAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: initialPoolConfig,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(initialPoolAddr, "initial_node_pool.status", "active"),
					resource.TestCheckNoResourceAttr(initialPoolAddr, "initial_node_pool.status_reason"),
					resource.TestCheckNoResourceAttr(initialPoolAddr, "initial_node_pool.status_message"),
					resource.TestCheckNoResourceAttr(initialPoolAddr, "initial_node_pool.failed_step"),
				),
			},
			// The pool fails out of band: the refresh records why, and the reason
			// alone (computed only) plans nothing.
			{
				PreConfig: func() {
					api.setPool(map[string]any{
						"status": "error", "statusReason": "QuotaExceeded",
						"statusMessage": "instance quota exhausted", "failedStep": "node_pool",
					})
				},
				Config: initialPoolConfig,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(initialPoolAddr, plancheck.ResourceActionNoop)},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(initialPoolAddr, "initial_node_pool.status", "error"),
					resource.TestCheckResourceAttr(initialPoolAddr, "initial_node_pool.status_reason", "QuotaExceeded"),
					resource.TestCheckResourceAttr(initialPoolAddr, "initial_node_pool.status_message", "instance quota exhausted"),
					resource.TestCheckResourceAttr(initialPoolAddr, "initial_node_pool.failed_step", "node_pool"),
				),
			},
			{Config: initialPoolConfig, PlanOnly: true},
			// Recovered: the reason clears on refresh, again with no diff.
			{
				PreConfig: func() {
					api.setPool(map[string]any{"status": "active", "statusReason": nil, "statusMessage": nil, "failedStep": nil})
				},
				Config: initialPoolConfig,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(initialPoolAddr, "initial_node_pool.status", "active"),
					resource.TestCheckNoResourceAttr(initialPoolAddr, "initial_node_pool.status_reason"),
				),
			},
		},
	})
}
