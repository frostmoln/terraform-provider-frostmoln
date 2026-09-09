package appgw_backend_pool_test

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
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/acctest"
)

// The empty-plan proof for `proxy_protocol`, and the destroy/recreate proof for
// the pool now that PATCH exists.
//
// TF_ACC-gated, self-contained (httptest in-process):
//
//	TF_ACC=1 go test ./internal/resource/appgw_backend_pool/ -run TestAccBackendPool

// poolAPI is a scripted pool endpoint. It counts DELETEs, because a plan-level
// assertion that a change is an Update can be satisfied by accident and the
// request log cannot: a pool that was replaced shows a DELETE here.
type poolAPI struct {
	mu      sync.Mutex
	pool    map[string]any
	patches []map[string]any
	deletes int
	// patchCount drives updatedAt so the fake's timestamp MOVES on every PATCH,
	// as postgres's does.
	patchCount int
}

func (a *poolAPI) handler() http.Handler {
	const base = "/v1/tenants/t-1/application-gateways/agw-1/backend-pools"
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
			a.pool = map[string]any{
				"id": "pool-1", "gatewayId": "agw-1",
				"name":            body["name"],
				"protocol":        strOr(body["protocol"], "http"),
				"algorithm":       strOr(body["algorithm"], "round_robin"),
				"sessionAffinity": strOr(body["sessionAffinity"], "none"),
				// The free-text trio is echoed back as sent, empty included --
				// the server stores what it is given and returns it. Omitting
				// them here made every create that set one fail the framework's
				// consistency check.
				"sessionCookieName": strOr(body["sessionCookieName"], ""),
				"tlsCaCertificate":  strOr(body["tlsCaCertificate"], ""),
				"tlsServerName":     strOr(body["tlsServerName"], ""),
				"tlsVerifyBackend":  boolOr(body["tlsVerifyBackend"], true),
				"timeoutConnectMs":  numOr(body["timeoutConnectMs"], 2000),
				"timeoutResponseMs": numOr(body["timeoutResponseMs"], 30000),
				// Echoed UNCONDITIONALLY, and the server default is false.
				// That pair is what lets the Terraform attribute carry a schema
				// Default(false) and still round-trip exactly.
				"proxyProtocol": boolOr(body["proxyProtocol"], false),
				"createdAt":     "2026-09-01T00:00:00Z",
				"updatedAt":     "2026-09-01T00:00:00Z",
			}
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(a.pool)

		case r.Method == http.MethodPatch && strings.HasPrefix(r.URL.Path, base+"/"):
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			a.patches = append(a.patches, body)
			// An omitted field is left unchanged; that is the endpoint's
			// contract and the reason the provider sends a diff, not a snapshot.
			for k, v := range body {
				a.pool[k] = v
			}
			// 🔴 THE TIMESTAMP MOVES, BECAUSE THE REAL ONE DOES.
			//
			// postgres bumps updated_at on every PATCH and RETURNs it. A fake
			// that leaves it fixed cannot exercise the changing-computed-value
			// path at all, so it would sit green through someone "fixing a
			// perpetual diff" by adding UseStateForUnknown() to updated_at --
			// which would write a stale value into state and then fail the
			// consistency check on the next apply.
			a.patchCount++
			a.pool["updatedAt"] = fmt.Sprintf("2026-09-01T00:00:%02dZ", a.patchCount)
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(a.pool)

		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, base+"/"):
			if a.pool == nil {
				w.WriteHeader(http.StatusNotFound)
				_ = json.NewEncoder(w).Encode(map[string]string{"code": "NOT_FOUND", "message": "gone"})
				return
			}
			_ = json.NewEncoder(w).Encode(a.pool)

		case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, base+"/"):
			a.deletes++
			a.pool = nil
			w.WriteHeader(http.StatusNoContent)

		default:
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]string{
				"code": "NOT_FOUND", "message": r.Method + " " + r.URL.Path,
			})
		}
	})
}

func strOr(v any, def string) any {
	if s, ok := v.(string); ok && s != "" {
		return s
	}
	return def
}

func numOr(v any, def float64) any {
	if n, ok := v.(float64); ok && n != 0 {
		return n
	}
	return def
}

func boolOr(v any, def bool) any {
	if b, ok := v.(bool); ok {
		return b
	}
	return def
}

func startPoolAPI(t *testing.T) *poolAPI {
	t.Helper()
	api := &poolAPI{}
	srv := httptest.NewServer(api.handler())
	t.Cleanup(srv.Close)
	t.Setenv("FROSTMOLN_API_ENDPOINT", srv.URL)
	t.Setenv("FROSTMOLN_API_KEY", "acc-test-key") // pragma: allowlist secret
	return api
}

func poolConfig(extra string) string {
	return `
resource "frostmoln_appgw_backend_pool" "mail" {
  gateway_id = "agw-1"
  name       = "mail"
` + extra + `
}
`
}

// TestAccBackendPoolProxyProtocolIsPlanStable. Unset it must read false, not
// "known after apply" and not null: the server's default IS false and it is
// echoed on every read, so the schema Default predicts the exact value that
// comes back.
func TestAccBackendPoolProxyProtocolIsPlanStable(t *testing.T) {
	startPoolAPI(t)

	unset := poolConfig("")
	on := poolConfig(`  proxy_protocol = true`)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: acctest.TestAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: unset,
				Check:  resource.TestCheckResourceAttr("frostmoln_appgw_backend_pool.mail", "proxy_protocol", "false"),
			},
			{Config: unset, PlanOnly: true},
			{
				Config: on,
				Check:  resource.TestCheckResourceAttr("frostmoln_appgw_backend_pool.mail", "proxy_protocol", "true"),
			},
			{Config: on, PlanOnly: true},
		},
	})
}

// TestAccBackendPoolRemovingProxyProtocolTurnsItOff is the proof that the
// schema Default is load-bearing rather than decorative.
//
// 🔴 WITHOUT IT, DELETING THE LINE FROM YOUR CONFIGURATION IS A NO-OP. A
// Computed attribute with a null configuration keeps its prior value in the
// proposed new state, and the framework marks such an attribute unknown only
// when the proposed state differs from the prior one -- so the plan comes back
// EMPTY and the pool goes on prepending the PROXY header while the configuration
// says it should not be. That is a bytes-on-the-wire difference: a backend that
// stopped expecting the header reads it as the first bytes of the protocol and
// every connection fails.
//
// With Default(false) the framework substitutes the server's own default over
// the null config, the plan reads `true -> false`, and the PATCH says so.
func TestAccBackendPoolRemovingProxyProtocolTurnsItOff(t *testing.T) {
	api := startPoolAPI(t)

	on := poolConfig(`  proxy_protocol = true`)
	unset := poolConfig("")

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: acctest.TestAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: on,
				Check:  resource.TestCheckResourceAttr("frostmoln_appgw_backend_pool.mail", "proxy_protocol", "true"),
			},
			{
				Config: unset,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction(
							"frostmoln_appgw_backend_pool.mail", plancheck.ResourceActionUpdate),
						plancheck.ExpectKnownValue("frostmoln_appgw_backend_pool.mail",
							tfjsonpath.New("proxy_protocol"), knownvalue.Bool(false)),
					},
				},
				Check: resource.TestCheckResourceAttr("frostmoln_appgw_backend_pool.mail", "proxy_protocol", "false"),
			},
			{Config: unset, PlanOnly: true},
		},
	})

	api.mu.Lock()
	defer api.mu.Unlock()
	last := api.patches[len(api.patches)-1]
	if last["proxyProtocol"] != false {
		t.Errorf("patch body = %v; turning it off must reach the wire as proxyProtocol=false", last)
	}
}

// TestAccBackendPoolProxyProtocolChangeIsInPlace is the destroy/recreate proof,
// and it is the whole reason the PATCH endpoint exists.
//
// 🔴 A REPLACEMENT HERE IS AN OUTAGE, AND OFTEN A PLAN TERRAFORM CANNOT EXECUTE.
// A pool is refused with `BACKEND_POOL_IN_USE` while anything forwards to it, and
// a pool is reachable from both sides now: an http route, or a `tcp` listener
// naming it directly. So "turn proxy_protocol on" planned as destroy/create means
// first destroying the tcp listener that holds the pool -- which CLOSES ITS PUBLIC
// PORT that instant -- to change one setting whose entire purpose is to be
// changed once the backend has learned to read the header.
//
// Two independent assertions, because either alone can be satisfied by accident:
// the plan must contain an Update and not a replacement, and the scripted API
// must have seen a PATCH and NO DELETE at all.
func TestAccBackendPoolProxyProtocolChangeIsInPlace(t *testing.T) {
	api := startPoolAPI(t)

	off := poolConfig(`  proxy_protocol = false`)
	on := poolConfig(`  proxy_protocol = true`)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: acctest.TestAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: off},
			{
				Config: on,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction(
							"frostmoln_appgw_backend_pool.mail", plancheck.ResourceActionUpdate),
					},
				},
				Check: resource.TestCheckResourceAttr("frostmoln_appgw_backend_pool.mail", "proxy_protocol", "true"),
			},
			{Config: on, PlanOnly: true},
		},
	})

	api.mu.Lock()
	defer api.mu.Unlock()
	if len(api.patches) == 0 {
		t.Fatal("no PATCH reached the API; the change did not go through the settings endpoint")
	}
	// One DELETE only: the harness's own teardown after the last step. A
	// replacement would have added another, mid-run.
	if api.deletes > 1 {
		t.Fatalf("the pool was deleted %d times; flipping proxy_protocol must not replace it",
			api.deletes)
	}
	last := api.patches[len(api.patches)-1]
	if len(last) != 1 || last["proxyProtocol"] != true {
		t.Errorf("patch body = %v; only the changed field belongs on the wire", last)
	}
}

// TestAccBackendPoolTimeoutChangeIsInPlace covers the rest of the narrowing.
//
// Every setting the PATCH accepts stopped forcing a replacement, not just
// proxy_protocol: leaving a timeout replace-only would keep planning a
// destroy/create that 409s against any pool actually serving traffic.
func TestAccBackendPoolTimeoutChangeIsInPlace(t *testing.T) {
	api := startPoolAPI(t)

	before := poolConfig(`  timeout_response_ms = 30000`)
	after := poolConfig(`  timeout_response_ms = 60000`)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: acctest.TestAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: before},
			{
				Config: after,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction(
							"frostmoln_appgw_backend_pool.mail", plancheck.ResourceActionUpdate),
					},
				},
			},
			{Config: after, PlanOnly: true},
		},
	})

	api.mu.Lock()
	defer api.mu.Unlock()
	if api.deletes > 1 {
		t.Fatalf("the pool was deleted %d times; a timeout change must not replace it", api.deletes)
	}
}

// TestAccBackendPoolNameChangeStillReplaces is the other direction, and it is
// not vacuous: the PATCH body deliberately has NO `name`, so a rename is only
// expressible as a new pool. An in-place plan for it would be a plan the
// provider cannot execute.
func TestAccBackendPoolNameChangeStillReplaces(t *testing.T) {
	startPoolAPI(t)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: acctest.TestAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: poolConfig("")},
			{
				Config: `
resource "frostmoln_appgw_backend_pool" "mail" {
  gateway_id = "agw-1"
  name       = "mail-2"
}
`,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("frostmoln_appgw_backend_pool.mail",
							plancheck.ResourceActionDestroyBeforeCreate),
					},
				},
			},
		},
	})
}

// 🔴 CLEARING THE COOKIE NAME WHILE AFFINITY STAYS "cookie" IS CAUGHT.
//
// Deleting BOTH lines from a configuration does not turn affinity off:
// session_affinity is Optional+Computed with UseStateForUnknown, so it pins back
// to "cookie" from state, while session_cookie_name is Optional only and plans to
// null, which the patch sends as "" to clear. The server refuses that pair.
//
// ValidateConfig cannot see it — in the CONFIG session_affinity is null, so the
// coupling looks satisfied. The plan is the first place both resolved values
// exist together, which is why this lives in ModifyPlan.
//
// WARNED at plan, REFUSED at apply, and the split is deliberate rather than a
// compromise: an error diagnostic raised while planning also aborts
// `terraform destroy`, because a destroy plan runs a refresh phase whose planned
// state is not null. TestNoErrorDiagnosticsWhilePlanning pins that provider-wide.
// So the plan-time signal is a warning and Update carries the refusal, which a
// destroy never reaches. The error this asserts is Update's -- disabling it alone
// fails this test with "Step 2/2, expected an error but got none".
func TestAccBackendPoolClearingTheCookieNameUnderCookieAffinityIsRefused(t *testing.T) {
	startPoolAPI(t)

	withCookie := poolConfig(`  session_affinity    = "cookie"
  session_cookie_name = "sess"`)
	withoutBoth := poolConfig(`  timeout_response_ms = 30000`)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: acctest.TestAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: withCookie,
				Check: resource.TestCheckResourceAttr(
					"frostmoln_appgw_backend_pool.mail", "session_cookie_name", "sess"),
			},
			{
				Config:      withoutBoth,
				ExpectError: regexp.MustCompile(`session_cookie_name Is Required With session_affinity`),
			},
		},
	})
}
