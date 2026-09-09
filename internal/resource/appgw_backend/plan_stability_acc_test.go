package appgw_backend_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/acctest"
)

// The empty-plan proof for `status_observed_at`.
//
//	TF_ACC=1 go test ./internal/resource/appgw_backend/ -run TestAccBackend

// backendAPI is a scripted pool-members endpoint. `observedAt` is the switch
// between the two shapes of the nullable field, both of which have to be plan
// stable: null (which is what EVERY backend reports today) and a timestamp
// (which is what they will report once the health ingest ships).
type backendAPI struct {
	mu         sync.Mutex
	backend    map[string]any
	observedAt any
}

func (a *backendAPI) handler() http.Handler {
	const base = "/v1/tenants/t-1/application-gateways/agw-1/backend-pools/pool-1/backends"
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a.mu.Lock()
		defer a.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")

		switch {
		case r.URL.Path == "/v1/me":
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "u-1", "tenantId": "t-1"})

		case r.Method == http.MethodPost && r.URL.Path == base:
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			a.backend = map[string]any{
				"id": "be-1", "poolId": "pool-1",
				"sourceKind": "address",
				"address":    body["address"],
				"port":       body["port"],
				"weight":     numOr(body["weight"], 1),
				// 🔴 `unknown` FOR EVERY BACKEND TODAY. The ingest that reports
				// observations has not shipped, so nothing writes either field.
				"status": "unknown",
				// Emitted unconditionally, null until something observes it.
				"statusObservedAt": a.observedAt,
				"enabled":          true,
				"createdAt":        "2026-09-01T00:00:00Z",
			}
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(a.backend)

		case r.Method == http.MethodGet && r.URL.Path == base:
			list := []any{}
			if a.backend != nil {
				a.backend["statusObservedAt"] = a.observedAt
				list = append(list, a.backend)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"backends": list, "totalCount": len(list)})

		case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, base+"/"):
			a.backend = nil
			w.WriteHeader(http.StatusNoContent)

		default:
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]string{
				"code": "NOT_FOUND", "message": r.Method + " " + r.URL.Path,
			})
		}
	})
}

func numOr(v any, def float64) any {
	if n, ok := v.(float64); ok && n != 0 {
		return n
	}
	return def
}

func startBackendAPI(t *testing.T) *backendAPI {
	t.Helper()
	api := &backendAPI{}
	srv := httptest.NewServer(api.handler())
	t.Cleanup(srv.Close)
	t.Setenv("FROSTMOLN_API_ENDPOINT", srv.URL)
	t.Setenv("FROSTMOLN_API_KEY", "acc-test-key") // pragma: allowlist secret
	return api
}

const backendConfig = `
resource "frostmoln_appgw_backend" "web" {
  gateway_id = "agw-1"
  pool_id    = "pool-1"
  address    = "10.0.1.10"
  port       = 25
}
`

// TestAccBackendStatusObservedAtIsNullUntilObserved is today's shape, and it is
// the one every backend has: nothing writes `status` or `status_observed_at`
// until the health-ingest workstream ships.
func TestAccBackendStatusObservedAtIsNullUntilObserved(t *testing.T) {
	startBackendAPI(t)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: acctest.TestAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: backendConfig,
				Check: resource.ComposeAggregateTestCheckFunc(
					// Null, not "": read as a string the zero value would be
					// indistinguishable from a real timestamp of "".
					resource.TestCheckNoResourceAttr("frostmoln_appgw_backend.web", "status_observed_at"),
					resource.TestCheckResourceAttr("frostmoln_appgw_backend.web", "status", "unknown"),
				),
			},
			{Config: backendConfig, PlanOnly: true},
			{Config: backendConfig},
		},
	})
}

// TestAccBackendStatusObservedAtIsPlanStableWhenObserved is tomorrow's shape.
//
// The value arrives from the gateway and changes on its own schedule, so a
// refresh that picks up a NEW timestamp must not produce a diff: it is Computed
// only, and a Computed attribute the configuration cannot set has nothing to
// disagree with. Marked Optional it would be a permanent diff against every
// configuration that -- necessarily -- never named it.
func TestAccBackendStatusObservedAtIsPlanStableWhenObserved(t *testing.T) {
	api := startBackendAPI(t)
	api.observedAt = "2026-09-01T12:00:00Z"

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: acctest.TestAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: backendConfig,
				Check: resource.TestCheckResourceAttr(
					"frostmoln_appgw_backend.web", "status_observed_at", "2026-09-01T12:00:00Z"),
			},
			{Config: backendConfig, PlanOnly: true},
			{Config: backendConfig},
		},
	})
}
