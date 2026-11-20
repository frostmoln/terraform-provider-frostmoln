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
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/acctest"
)

// `version` in place (K8S-VERSION-UPGRADES-PLAN decision 16), run by real Terraform against a
// scripted kubernetes API. TF_ACC-gated but needs no Frostmoln:
//
//	TF_ACC=1 go test ./internal/resource/kubernetes_cluster/ -run TestAccClusterVersion
//
// The proof against a really upgraded cluster waits for the prod canary (row 11).

type upgradeAPI struct {
	mu       sync.Mutex
	cluster  map[string]any
	pool     map[string]any
	ops      map[string]map[string]any
	upgrades []string // every POST .../upgrade body's kubernetesVersion
	puts     int      // PUT .../c-1 (name, tags)
	// answer scripts the next POST .../upgrade: "" commits at once; "stop" stops it past the
	// point of no return (pool_not_converged); "stop-quiet" the same with an operation that has
	// no error text; "lost" restores the cluster with no operation to read (a command refused
	// before any workflow ran); "slow" leaves it upgrading; or an HTTP status to refuse with.
	answer string
	// commitOnGet commits a "slow" upgrade on the next cluster GET.
	commitOnGet bool
}

func newUpgradeAPI(version string) *upgradeAPI {
	return &upgradeAPI{
		cluster: map[string]any{
			"id": "c-1", "name": "k", "tenantId": "t-1", "status": "running",
			"kubernetesVersion": version, "controlPlaneTier": "development", "region": "fbg",
			"vpcId": "vpc-1", "subnetId": "sn-1", "addons": []string{}, "createdAt": "2026-11-20T00:00:00Z",
		},
		pool: map[string]any{
			"id": "np-1", "clusterId": "c-1", "name": "default", "status": "active",
			"flavorId": "f-1", "nodeCount": 1, "isInitial": true, "createdAt": "2026-11-20T00:00:00Z",
		},
		ops: map[string]map[string]any{},
	}
}

// do runs f under the lock (PreConfig hooks).
func (a *upgradeAPI) do(f func()) func() {
	return func() {
		a.mu.Lock()
		defer a.mu.Unlock()
		f()
	}
}

func (a *upgradeAPI) commit(version string) {
	a.cluster["status"], a.cluster["kubernetesVersion"] = "running", version
	delete(a.cluster, "targetKubernetesVersion")
	delete(a.cluster, "upgradeFailure")
}

func (a *upgradeAPI) posted(want ...string) resource.TestCheckFunc {
	return func(*terraform.State) error {
		a.mu.Lock()
		defer a.mu.Unlock()
		got := a.upgrades
		a.upgrades = nil
		if fmt.Sprint(got) != fmt.Sprint(want) {
			return fmt.Errorf("upgrade requests = %v, want %v", got, want)
		}
		return nil
	}
}

const opsBase = "/v1/tenants/t-1/operations/"

func (a *upgradeAPI) handler() http.Handler {
	refusals := map[string][3]any{
		"501": {http.StatusNotImplemented, "not_implemented", "kubernetes upgrades are not available"},
		"400": {http.StatusBadRequest, "invalid_input", "unsupported kubernetes upgrade target"},
		"409": {http.StatusConflict, "pool_not_active", "a node pool of this cluster is not active"},
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
			write(http.StatusAccepted, a.cluster)
		case r.Method == http.MethodPost && p == clusterBase+"/c-1/upgrade":
			var body struct {
				KubernetesVersion string `json:"kubernetesVersion"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			a.upgrades = append(a.upgrades, body.KubernetesVersion)
			if ref, ok := refusals[a.answer]; ok {
				write(ref[0].(int), map[string]any{"code": ref[1], "message": ref[2]})
				return
			}
			opID := fmt.Sprintf("upgrade-k8s-enclave-cluster-c-1@%d", len(a.ops)+1)
			op := map[string]any{"operationId": opID, "status": "completed", "resourceType": "kubernetes_cluster"}
			switch a.answer {
			case "stop", "stop-quiet":
				a.cluster["status"], a.cluster["targetKubernetesVersion"] = "upgrading", body.KubernetesVersion
				a.cluster["upgradeFailure"] = "pool_not_converged"
				op["status"] = "failed"
				if a.answer == "stop" {
					op["error"] = "a node pool did not reach the target version"
				}
			case "lost":
				a.cluster["upgradeFailure"] = "upgrade_path_unavailable"
				op = nil
			case "slow":
				a.cluster["status"], a.cluster["targetKubernetesVersion"] = "upgrading", body.KubernetesVersion
				delete(a.cluster, "upgradeFailure")
				op["status"] = "running"
			default:
				a.commit(body.KubernetesVersion)
			}
			if op != nil {
				a.ops[opID] = op
			}
			write(http.StatusAccepted, map[string]any{
				"operationId": opID, "status": "upgrading",
				"kubernetesVersion": a.cluster["kubernetesVersion"], "targetKubernetesVersion": body.KubernetesVersion,
			})
		case r.Method == http.MethodGet && strings.HasPrefix(p, opsBase):
			if op, ok := a.ops[strings.TrimPrefix(p, opsBase)]; ok {
				write(http.StatusOK, op)
				return
			}
			write(http.StatusNotFound, map[string]string{"code": "NOT_FOUND", "message": "no operation"})
		case r.Method == http.MethodGet && p == clusterBase+"/c-1":
			if a.commitOnGet {
				a.commitOnGet = false
				a.commit(a.cluster["targetKubernetesVersion"].(string))
			}
			write(http.StatusOK, a.cluster)
		case r.Method == http.MethodPut && p == clusterBase+"/c-1":
			a.puts++
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			if n, ok := body["name"].(string); ok {
				a.cluster["name"] = n
			}
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

func versionConfig(version string) string { return clusterConfig("k", version, "") }

// clusterConfig: an empty version leaves the attribute unset; extra is appended HCL.
func clusterConfig(name, version, extra string) string {
	v := ""
	if version != "" {
		v = fmt.Sprintf("version = %q", version)
	}
	return fmt.Sprintf(`
resource "frostmoln_kubernetes_cluster" "k" {
  name      = %q
  %s
  vpc_id    = "vpc-1"
  subnet_id = "sn-1"
  addons    = []
  initial_node_pool = {
    flavor_id = "f-1"
  }
  %s
}
`, name, v, extra)
}

func serveUpgradeAPI(t *testing.T, api *upgradeAPI) {
	srv := httptest.NewServer(api.handler())
	t.Cleanup(srv.Close)
	t.Setenv("FROSTMOLN_API_ENDPOINT", srv.URL)
	t.Setenv("FROSTMOLN_API_KEY", "acc-test-key") // pragma: allowlist secret
}

const versionAddr = "frostmoln_kubernetes_cluster.k"

func inPlace() resource.ConfigPlanChecks {
	return resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(versionAddr, plancheck.ResourceActionUpdate)}}
}

func noChange() resource.ConfigPlanChecks {
	return resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(versionAddr, plancheck.ResourceActionNoop)}}
}

// errs matches diagnostic text across Terraform's line wrapping.
func errs(words string) *regexp.Regexp {
	return regexp.MustCompile(`(?s)` + strings.ReplaceAll(regexp.QuoteMeta(words), " ", `\s+`))
}

// 1.35 -> 1.36 upgrades in place; every refusal is a diagnostic that leaves the cluster and
// state where they were; a stopped upgrade fails the apply with its reason, state keeps what the
// cluster runs, and applying the same configuration again resumes it; an apply that runs out of
// time is picked up by the next one without a second request.
func TestAccClusterVersionUpgradesInPlace(t *testing.T) {
	api := newUpgradeAPI("1.35")
	serveUpgradeAPI(t, api)
	script := func(answer string) func() { return api.do(func() { api.answer = answer }) }
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: acctest.TestAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: versionConfig("1.35"), Check: api.posted()},
			{
				Config: versionConfig("1.36"), ConfigPlanChecks: inPlace(),
				Check: resource.ComposeAggregateTestCheckFunc(api.posted("1.36"),
					resource.TestCheckResourceAttr(versionAddr, "version", "1.36"),
					resource.TestCheckResourceAttr(versionAddr, "id", "c-1")),
			},
			{PreConfig: script("501"), Config: versionConfig("1.37"), ConfigPlanChecks: inPlace(), ExpectError: errs("not available to this tenant yet")},
			{PreConfig: script("400"), Config: versionConfig("1.37"), ExpectError: errs("1.37 is not an upgrade target for this cluster (unsupported kubernetes upgrade target)")},
			{PreConfig: script("409"), Config: versionConfig("1.37"), ExpectError: errs("repair or delete it, then apply again. This request started nothing")},
			// The refusals changed nothing: the version still reads 1.36 against a 1.36 config.
			{Config: versionConfig("1.36"), ConfigPlanChecks: noChange(), Check: api.posted("1.37", "1.37", "1.37")},
			{PreConfig: script("stop"), Config: versionConfig("1.37"), ExpectError: errs("upgradeFailure: pool_not_converged); the cluster is upgrading on 1.36. The cluster keeps serving. Apply again with the same version to resume the upgrade. The upgrade operation")},
			// A stopped upgrade resumes only with its own version, and nothing is sent otherwise.
			{Config: versionConfig("1.38"), ExpectError: errs(`only that version can resume it. Set version = "1.37"`)},
			// State kept what the cluster runs, so the same configuration plans the same request: a resume.
			{
				PreConfig: script(""), Config: versionConfig("1.37"), ConfigPlanChecks: inPlace(),
				Check: resource.ComposeAggregateTestCheckFunc(api.posted("1.37", "1.37"),
					resource.TestCheckResourceAttr(versionAddr, "version", "1.37")),
			},
			// An operation that fails with no text, and a command that never became an operation,
			// still end at once: the cluster row is what ends the wait.
			{PreConfig: script("stop-quiet"), Config: versionConfig("1.38"), ExpectError: errs("upgradeFailure: pool_not_converged)")},
			{
				PreConfig: api.do(func() { api.answer = "lost"; api.commit("1.37") }), Config: versionConfig("1.38"),
				ExpectError: errs("upgradeFailure: upgrade_path_unavailable); the cluster is running on 1.37. This needs Frostmoln support"),
			},
			// An apply that runs out of time leaves state on what the cluster runs; the next apply
			// waits on the running upgrade instead of requesting it again.
			{
				PreConfig: api.do(func() { api.answer = "slow"; api.commit("1.37") }), Config: clusterConfig("k", "1.38", `timeouts { update = "1s" }`),
				ExpectError: errs("is still running on the platform"),
			},
			{
				PreConfig: api.do(func() { api.commitOnGet = true }), Config: versionConfig("1.38"), ConfigPlanChecks: inPlace(),
				Check: resource.ComposeAggregateTestCheckFunc(api.posted("1.38", "1.38", "1.38"),
					resource.TestCheckResourceAttr(versionAddr, "version", "1.38")),
			},
			{Config: versionConfig("1.38"), PlanOnly: true},
		},
	})
}

// A config at 1.35 against a cluster upgraded to 1.36 outside Terraform plans no replacement and
// no change (the refresh warns); writing 1.36 then records it without an upgrade request; a lower
// config is refused before anything (here a rename) is written; with version unset, state follows
// the cluster.
func TestAccClusterVersionUpgradedOutsideTerraform(t *testing.T) {
	api := newUpgradeAPI("1.35")
	serveUpgradeAPI(t, api)
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: acctest.TestAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: versionConfig("1.35")},
			{
				PreConfig: api.do(func() { api.cluster["kubernetesVersion"] = "1.36" }),
				Config:    versionConfig("1.35"), ConfigPlanChecks: noChange(),
				Check: resource.ComposeAggregateTestCheckFunc(api.posted(),
					resource.TestCheckResourceAttr(versionAddr, "version", "1.35")),
			},
			{
				Config: versionConfig("1.36"), ConfigPlanChecks: inPlace(),
				Check: resource.ComposeAggregateTestCheckFunc(api.posted(),
					resource.TestCheckResourceAttr(versionAddr, "version", "1.36")),
			},
			{
				Config: clusterConfig("renamed", "1.35", ""), ConfigPlanChecks: inPlace(),
				ExpectError: errs("Kubernetes Version Cannot Be Lowered"),
			},
			{
				Config: versionConfig("1.36"), ConfigPlanChecks: noChange(),
				Check: func(*terraform.State) error {
					api.mu.Lock()
					defer api.mu.Unlock()
					if api.puts != 0 || api.cluster["name"] != "k" {
						return fmt.Errorf("the refused apply wrote: %d PUTs, name %v", api.puts, api.cluster["name"])
					}
					return nil
				},
			},
			// Recorded 1.36, cluster upgraded to 1.37 outside, config 1.36: nothing to do.
			{PreConfig: api.do(func() { api.cluster["kubernetesVersion"] = "1.37" }), Config: versionConfig("1.36"), ConfigPlanChecks: noChange()},
			// The version unchanged, other changes apply; the recorded version stays (and warns).
			{
				Config: clusterConfig("renamed", "1.36", ""), ConfigPlanChecks: inPlace(),
				Check: resource.ComposeAggregateTestCheckFunc(api.posted(), resource.TestCheckResourceAttr(versionAddr, "version", "1.36")),
			},
			// Above state but below what the cluster runs (state 1.36, cluster 1.38, config 1.37), with a
			// rename: refused before the rename is written.
			{
				PreConfig: api.do(func() { api.cluster["kubernetesVersion"] = "1.38" }),
				Config:    clusterConfig("renamed2", "1.37", ""), ConfigPlanChecks: inPlace(),
				ExpectError: errs("The cluster is on Kubernetes 1.38 and the configuration says 1.37"),
			},
			{
				Config: clusterConfig("renamed", "1.36", ""), ConfigPlanChecks: noChange(),
				Check: func(*terraform.State) error {
					api.mu.Lock()
					defer api.mu.Unlock()
					if api.cluster["name"] != "renamed" {
						return fmt.Errorf("the refused apply renamed the cluster to %v", api.cluster["name"])
					}
					return nil
				},
			},
			// version unset: no diff, no request. The next APPLY records it unset (the planned value
			// is still the recorded one, so that apply keeps it); every refresh after follows the cluster.
			{Config: clusterConfig("renamed", "", ""), ConfigPlanChecks: noChange(), Check: api.posted()},
			{
				PreConfig: api.do(func() { api.cluster["kubernetesVersion"] = "1.38" }),
				Config:    clusterConfig("k2", "", ""), ConfigPlanChecks: inPlace(),
				Check: resource.ComposeAggregateTestCheckFunc(api.posted(), resource.TestCheckResourceAttr(versionAddr, "version", "1.36")),
			},
			// That apply recorded version as unset: from here on a refresh follows the cluster.
			{
				PreConfig: api.do(func() { api.cluster["kubernetesVersion"] = "1.39" }),
				Config:    clusterConfig("k2", "", ""), ConfigPlanChecks: noChange(),
				Check: resource.ComposeAggregateTestCheckFunc(api.posted(), resource.TestCheckResourceAttr(versionAddr, "version", "1.39")),
			},
		},
	})
}
