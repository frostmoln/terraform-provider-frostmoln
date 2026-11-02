package kubernetes_cluster_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/knownvalue"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/acctest"
)

// maintenance_window run by real Terraform against a scripted kubernetes API, so
// what is asserted is the plan Terraform really makes and the bodies it really sends.
// TF_ACC-gated but needs no Frostmoln:
//
//	TF_ACC=1 go test ./internal/resource/kubernetes_cluster/ -run TestAccMaintenanceWindowPlans

type windowAPI struct {
	mu      sync.Mutex
	cluster map[string]any
	pool    map[string]any
	refuse  bool
	bodies  []string // every POST and PUT to the cluster
}

func (a *windowAPI) setWindow(w map[string]any) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.cluster["maintenanceWindow"] = w
}

func (a *windowAPI) takeBodies() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	b := a.bodies
	a.bodies = nil
	return b
}

func (a *windowAPI) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a.mu.Lock()
		defer a.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		write := func(code int, v any) {
			w.WriteHeader(code)
			_ = json.NewEncoder(w).Encode(v)
		}
		var body map[string]json.RawMessage
		if r.Method == http.MethodPost || r.Method == http.MethodPut {
			_ = json.NewDecoder(r.Body).Decode(&body)
			raw, _ := json.Marshal(body)
			a.bodies = append(a.bodies, string(raw))
			if _, sent := body["maintenanceWindow"]; sent && a.refuse {
				write(http.StatusForbidden, map[string]string{
					"code": "insufficient_permission", "message": "needs kubernetes:kubernetes-clusters:update-maintenance-window",
				})
				return
			}
		}
		apply := func() {
			if v, ok := body["maintenanceWindow"]; ok {
				var next map[string]any
				_ = json.Unmarshal(v, &next)
				a.cluster["maintenanceWindow"] = next
			}
			if v, ok := body["name"]; ok {
				var next string
				_ = json.Unmarshal(v, &next)
				a.cluster["name"] = next
			}
		}
		switch p := r.URL.Path; {
		case p == "/v1/me":
			write(http.StatusOK, map[string]any{"id": "u-1", "tenantId": "t-1"})
		case r.Method == http.MethodPost && p == clusterBase:
			apply()
			write(http.StatusAccepted, a.cluster)
		case r.Method == http.MethodPut && p == clusterBase+"/c-1":
			apply()
			write(http.StatusOK, a.cluster)
		case r.Method == http.MethodGet && p == clusterBase+"/c-1":
			write(http.StatusOK, a.cluster)
		case r.Method == http.MethodGet && p == clusterBase+"/c-1/kubeconfig":
			write(http.StatusOK, map[string]any{"kubeconfig": "apiVersion: v1"})
		case r.Method == http.MethodGet && p == clusterBase+"/c-1/node-pools":
			write(http.StatusOK, map[string]any{"nodePools": []any{a.pool}})
		case r.Method == http.MethodGet && p == clusterBase+"/c-1/node-pools/np-1":
			write(http.StatusOK, a.pool)
		case r.Method == http.MethodGet && p == clusterBase+"/c-1/persistent-volumes":
			write(http.StatusOK, map[string]any{})
		case r.Method == http.MethodDelete && p == clusterBase+"/c-1":
			a.cluster["status"] = "deleted"
			a.pool["status"] = "deleted"
			w.WriteHeader(http.StatusAccepted)
		default:
			write(http.StatusNotFound, map[string]string{"code": "NOT_FOUND", "message": r.Method + " " + p})
		}
	})
}

func windowConfig(name, window string) string {
	return fmt.Sprintf(`
resource "frostmoln_kubernetes_cluster" "k" {
  name      = %q
  vpc_id    = "vpc-1"
  subnet_id = "sn-1"
  addons    = []
  %s

  initial_node_pool = {
    flavor_id = "f-1"
  }
}
`, name, window)
}

func windowHCL(days, start string) string {
	return fmt.Sprintf(`maintenance_window = {
    days           = %s
    start_time     = %q
    duration_hours = 6
    time_zone      = "Europe/Stockholm"
  }`, days, start)
}

func TestAccMaintenanceWindowPlans(t *testing.T) {
	api := &windowAPI{
		cluster: map[string]any{
			"id": "c-1", "name": "k", "tenantId": "t-1", "status": "running",
			"kubernetesVersion": "1.35", "controlPlaneTier": "development", "region": "fbg",
			"vpcId": "vpc-1", "subnetId": "sn-1", "addons": []string{}, "createdAt": "2026-10-04T00:00:00Z",
			"maintenanceWindow": map[string]any{
				"days": []string{"mon", "thu"}, "startTime": "00:00", "durationHours": 6, "timeZone": "Europe/Stockholm",
			},
		},
		pool: map[string]any{
			"id": "np-1", "clusterId": "c-1", "name": "default", "status": "active",
			"flavorId": "f-1", "nodeCount": 1, "isInitial": true, "createdAt": "2026-10-04T00:00:00Z",
		},
	}
	srv := httptest.NewServer(api.handler())
	t.Cleanup(srv.Close)
	t.Setenv("FROSTMOLN_API_ENDPOINT", srv.URL)
	t.Setenv("FROSTMOLN_API_KEY", "acc-test-key") // pragma: allowlist secret

	const addr = "frostmoln_kubernetes_cluster.k"
	noop := resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(addr, plancheck.ResourceActionNoop)}}
	update := resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(addr, plancheck.ResourceActionUpdate)}}
	// Each step's writes, and whether any of them carried the field.
	wrote := func(wantBodies int, wantField bool) resource.TestCheckFunc {
		return func(*terraform.State) error {
			bodies := api.takeBodies()
			if len(bodies) != wantBodies {
				return fmt.Errorf("writes = %d, want %d: %v", len(bodies), wantBodies, bodies)
			}
			for _, b := range bodies {
				if strings.Contains(b, "maintenanceWindow") != wantField {
					return fmt.Errorf("maintenanceWindow in %s, want %v", b, wantField)
				}
			}
			return nil
		}
	}

	stored, stored2 := windowHCL(`["thu", "mon"]`, "00:00"), windowHCL(`["wed"]`, "03:00")
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: acctest.TestAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			// Create without the attribute: omitted, and the server's value does not diff.
			{
				Config: windowConfig("k", ""),
				Check: resource.ComposeAggregateTestCheckFunc(
					wrote(1, false),
					resource.TestCheckResourceAttr(addr, "maintenance_window.start_time", "00:00"),
					resource.TestCheckResourceAttr(addr, "maintenance_window.days.#", "2"),
				),
			},
			{Config: windowConfig("k", ""), PlanOnly: true},
			// An unrelated update with the attribute unset does not send it.
			// And the plan shows the stored value, not "known after apply".
			{
				Config: windowConfig("k2", ""),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction(addr, plancheck.ResourceActionUpdate),
					plancheck.ExpectKnownValue(addr, tfjsonpath.New("maintenance_window").AtMapKey("start_time"), knownvalue.StringExact("00:00")),
				}},
				Check: wrote(1, false),
			},
			// Restating the stored value, in another order, plans nothing.
			{Config: windowConfig("k2", stored), ConfigPlanChecks: noop, Check: wrote(0, false)},
			// With a rename: the field stays out.
			{Config: windowConfig("k3", stored), ConfigPlanChecks: update, Check: wrote(1, false)},
			// A real change, and nothing else, runs and sends it.
			{
				Config: windowConfig("k3", stored2), ConfigPlanChecks: update,
				Check: resource.ComposeAggregateTestCheckFunc(
					wrote(1, true),
					resource.TestCheckResourceAttr(addr, "maintenance_window.start_time", "03:00"),
				),
			},
			// Changed out of band with the attribute set: a change is planned.
			{
				PreConfig: func() {
					api.setWindow(map[string]any{"days": []string{"fri"}, "startTime": "05:00", "durationHours": 6, "timeZone": "Europe/Stockholm"})
				},
				Config: windowConfig("k3", stored2), ConfigPlanChecks: update, Check: wrote(1, true),
			},
			// Removing it from the configuration keeps the window, with no change.
			{Config: windowConfig("k3", ""), ConfigPlanChecks: noop, Check: wrote(0, false)},
			// Changed out of band with the attribute unset: no diff, state follows.
			{
				PreConfig: func() {
					api.setWindow(map[string]any{"days": []string{"sat"}, "startTime": "06:00", "durationHours": 7, "timeZone": "Europe/Oslo"})
				},
				Config: windowConfig("k3", ""), ConfigPlanChecks: noop,
				Check: resource.ComposeAggregateTestCheckFunc(
					wrote(0, false),
					resource.TestCheckResourceAttr(addr, "maintenance_window.time_zone", "Europe/Oslo"),
				),
			},
			// Import reads it.
			{ResourceName: addr, ImportState: true, ImportStateVerify: true, ImportStateVerifyIgnore: []string{"public_ip_id", "tags", "tags_all", "kubeconfig", "initial_node_pool.tags", "initial_node_pool.tags_all"}},
			// A caller without the operation: a rename still works, a window change gets a readable refusal.
			{
				PreConfig: func() {
					api.mu.Lock()
					defer api.mu.Unlock()
					api.refuse = true
					api.bodies = nil
				},
				Config: windowConfig("k4", ""), ConfigPlanChecks: update, Check: wrote(1, false),
			},
			{
				Config:      windowConfig("k4", stored2),
				ExpectError: regexp.MustCompile(`Not Allowed To Set The Maintenance Window`),
			},
			// Invalid values stop at the plan.
			{Config: windowConfig("k4", `maintenance_window = { days = ["mon"] }`), ExpectError: regexp.MustCompile(`start_time`)},
			{Config: windowConfig("k4", windowHCL(`["Mon"]`, "03:00")), ExpectError: regexp.MustCompile(`(?s)must be one of`)},
			{Config: windowConfig("k4", windowHCL(`["mon"]`, "3am")), ExpectError: regexp.MustCompile(`HH:MM`)},
			// The post-test destroy runs on the last configuration, so it must be valid.
			{Config: windowConfig("k4", ""), PlanOnly: true},
		},
	})
}
