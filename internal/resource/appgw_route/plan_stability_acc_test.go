package appgw_route_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/acctest"
)

// The header-value proofs, run by real Terraform against a scripted appgw.
//
// TF_ACC-gated like every other acceptance test here, but they need no Frostmoln
// at all: the API is an httptest server in-process. Run them with:
//
//	TF_ACC=1 go test ./internal/resource/appgw_route/ -run TestAccRoute
//
// terraform-plugin-testing refreshes and re-plans after every apply step and
// FAILS the step on a non-empty plan, so every Config step below is itself a
// perpetual-diff assertion; the PlanOnly steps state it again at a point where a
// diff could only come from the refresh.

// routeAPI is a scripted route surface that answers in one of the two shapes the
// route API has:
//
//   - legacy: the header maps come back with their values (appgw today);
//   - sealed: only requestHeadersSetNames / responseHeadersSetNames come back,
//     and the value maps are absent (appgw once header values are secrets).
//
// Switching `sealed` between steps is the platform cutover as a practitioner
// meets it: state written by one shape, refreshed by the other.
type routeAPI struct {
	mu     sync.Mutex
	sealed bool
	routes map[string]map[string]any
	next   int
}

const routeBase = "/v1/tenants/t-1/application-gateways/agw-1/listeners/lsn-1/routes"

func (a *routeAPI) setSealed(v bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.sealed = v
}

func (a *routeAPI) present(rt map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range rt {
		out[k] = v
	}
	if out["pathMatchType"] == nil || out["pathMatchType"] == "" {
		out["pathMatchType"] = "prefix"
	}
	if out["action"] == nil || out["action"] == "" {
		out["action"] = "forward"
	}
	if _, ok := out["path"]; !ok {
		out["path"] = ""
	}
	out["enabled"] = true
	out["createdAt"] = "2026-09-01T00:00:00Z"
	if a.sealed {
		for _, pair := range [][2]string{
			{"requestHeadersSet", "requestHeadersSetNames"},
			{"responseHeadersSet", "responseHeadersSetNames"},
		} {
			m, _ := out[pair[0]].(map[string]any)
			delete(out, pair[0])
			if len(m) == 0 {
				continue
			}
			names := make([]string, 0, len(m))
			for k := range m {
				names = append(names, k)
			}
			sort.Strings(names)
			out[pair[1]] = names
		}
	}
	return out
}

func (a *routeAPI) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a.mu.Lock()
		defer a.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")

		switch {
		case r.URL.Path == "/v1/me":
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "u-1", "tenantId": "t-1"})

		case r.Method == http.MethodPost && r.URL.Path == routeBase:
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			a.next++
			id := "rt-" + strconv.Itoa(a.next)
			body["id"] = id
			body["listenerId"] = "lsn-1"
			if _, ok := body["priority"]; !ok {
				body["priority"] = 100 + 10*a.next
			}
			a.routes[id] = body
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(a.present(body))

		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, routeBase+"/"):
			rt, ok := a.routes[strings.TrimPrefix(r.URL.Path, routeBase+"/")]
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				_ = json.NewEncoder(w).Encode(map[string]string{"code": "NOT_FOUND", "message": "gone"})
				return
			}
			_ = json.NewEncoder(w).Encode(a.present(rt))

		case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, routeBase+"/"):
			delete(a.routes, strings.TrimPrefix(r.URL.Path, routeBase+"/"))
			w.WriteHeader(http.StatusNoContent)

		default:
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]string{
				"code": "NOT_FOUND", "message": r.Method + " " + r.URL.Path,
			})
		}
	})
}

func startRouteAPI(t *testing.T, sealed bool) *routeAPI {
	t.Helper()
	api := &routeAPI{sealed: sealed, routes: map[string]map[string]any{}}
	srv := httptest.NewServer(api.handler())
	t.Cleanup(srv.Close)
	t.Setenv("FROSTMOLN_API_ENDPOINT", srv.URL)
	t.Setenv("FROSTMOLN_API_KEY", "acc-test-key") // pragma: allowlist secret
	return api
}

const routeAddr = "frostmoln_appgw_route.api"

const routeConfig = `
resource "frostmoln_appgw_route" "api" {
  gateway_id      = "agw-1"
  listener_id     = "lsn-1"
  name            = "api"
  path            = "/v1"
  backend_pool_id = "pool-1"

  request_headers_set = {
    "Authorization" = "Bearer upstream-token"
    "X-Env"         = "staging"
  }
  response_headers_set = {
    "Cache-Control" = "no-store"
  }
}
`

func routeImportID(s *terraform.State) (string, error) {
	rs, ok := s.RootModule().Resources[routeAddr]
	if !ok {
		return "", fmt.Errorf("%s not in state", routeAddr)
	}
	return "agw-1/lsn-1/" + rs.Primary.ID, nil
}

// 🔴 THE CASE THIS CHANGE EXISTS FOR. A route with header values, created and
// refreshed against a server that returns only the names, must plan EMPTY.
//
// Before the change the create read-back wrote null over the configured maps
// ("Provider produced inconsistent result after apply"), and every refresh after
// that planned a replacement of the route — forever, on every apply.
func TestAccRouteHeaderValuesArePlanStableAgainstANamesOnlyServer(t *testing.T) {
	startRouteAPI(t, true)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: acctest.TestAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: routeConfig,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(routeAddr, "request_headers_set.Authorization", "Bearer upstream-token"),
					resource.TestCheckResourceAttr(routeAddr, "request_headers_set.X-Env", "staging"),
					resource.TestCheckResourceAttr(routeAddr, "response_headers_set.Cache-Control", "no-store"),
				),
			},
			{Config: routeConfig, PlanOnly: true},
			{Config: routeConfig},
		},
	})
}

// The cutover as an existing practitioner meets it: state written while the
// server still returned values, then refreshed after it stopped. The plan must
// stay empty — this is every header route already under management on the day
// the platform changes shape.
func TestAccRouteHeaderValuesArePlanStableAcrossTheServerCutover(t *testing.T) {
	api := startRouteAPI(t, false)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: acctest.TestAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: routeConfig},
			{
				PreConfig: func() { api.setSealed(true) },
				Config:    routeConfig,
				PlanOnly:  true,
			},
			{
				Config: routeConfig,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(routeAddr, plancheck.ResourceActionNoop)},
				},
				Check: resource.TestCheckResourceAttr(routeAddr, "request_headers_set.Authorization", "Bearer upstream-token"),
			},
		},
	})
}

// Import against a server that still returns values is clean, as it always was.
func TestAccRouteImportAgainstALegacyServerIsClean(t *testing.T) {
	startRouteAPI(t, false)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: acctest.TestAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: routeConfig},
			{
				Config:            routeConfig,
				ResourceName:      routeAddr,
				ImportState:       true,
				ImportStateKind:   resource.ImportBlockWithID,
				ImportStateIdFunc: routeImportID,
			},
		},
	})
}

// Import against a names-only server CANNOT recover a value — the API has none
// to give — so the import plans a REPLACEMENT, which re-creates the route with
// the configured values. The alternative, a clean import holding values nobody
// has seen, would put state out of step with the platform with no diff to show
// it.
func TestAccRouteImportAgainstANamesOnlyServerPlansAReplacement(t *testing.T) {
	startRouteAPI(t, true)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: acctest.TestAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: routeConfig},
			{
				Config:             routeConfig,
				ResourceName:       routeAddr,
				ImportState:        true,
				ImportStateKind:    resource.ImportBlockWithID,
				ImportStateIdFunc:  routeImportID,
				ExpectNonEmptyPlan: true,
				ImportPlanChecks: resource.ImportPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction(routeAddr, plancheck.ResourceActionReplace),
					},
				},
			},
		},
	})
}
