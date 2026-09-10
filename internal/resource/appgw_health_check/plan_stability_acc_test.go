package appgw_health_check_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/knownvalue"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/acctest"
)

// The empty-plan proofs for the health check's new `port`, plus the proof that
// a destroy now REMOVES the check instead of apologising for not being able to.
//
// TF_ACC-gated, but self-contained: the API is an httptest server in-process.
//
//	TF_ACC=1 go test ./internal/resource/appgw_health_check/ -run TestAccHealthCheck

// healthCheckAPI is a scripted PUT/GET/DELETE health-check endpoint. It applies
// the endpoint's own rule -- the PUT states the WHOLE check, so an omitted field
// returns to its default, `port` included -- because that rule is exactly what
// makes `port` Optional rather than Optional+Computed.
type healthCheckAPI struct {
	mu      sync.Mutex
	check   map[string]any
	deletes int

	// poolProxyProtocol is the POOL's own proxyProtocol, which this endpoint
	// CONSULTS: appgw refuses a probe header on a pool that sends none, because
	// there would be nothing to send. There is no pool resource in this test, so
	// the setting is a field a test sets before it applies. False by default, as
	// the platform's is.
	poolProxyProtocol bool
}

func (a *healthCheckAPI) handler() http.Handler {
	const path = "/v1/tenants/t-1/application-gateways/agw-1/backend-pools/pool-1/health-check"
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a.mu.Lock()
		defer a.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")

		switch {
		case r.URL.Path == "/v1/me":
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "u-1", "tenantId": "t-1"})

		case r.Method == http.MethodPut && r.URL.Path == path:
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			// 🔴 THE FAKE MODELS THE SERVER'S REFUSAL, NOT JUST ITS DEFAULTS.
			//
			// appgw refuses an EXPLICIT path/expectedStatus on a tcp probe
			// (internal/domain/backendpool.go) while defaulting both
			// unconditionally just below. Without this arm the fake accepts
			// anything, and the round-trip test cannot fail against a provider
			// that echoes the two defaults straight back -- which is exactly
			// the defect it exists to catch.
			if str(body["protocol"]) == "tcp" &&
				(str(body["path"]) != "" || str(body["expectedStatus"]) != "") {
				w.WriteHeader(http.StatusBadRequest)
				_ = json.NewEncoder(w).Encode(map[string]string{
					"code": "INVALID_REQUEST",
					"message": "path and expectedStatus only apply to a http or https probe; " +
						"a tcp probe opens a connection to the port and closes it, and has " +
						"no status to compare",
				})
				return
			}
			// 🔴 THE FAKE MODELS THE CROSS-RESOURCE REFUSAL TOO. appgw rejects
			// `proxyProtocol: true` when the POOL's own proxyProtocol is off
			// (internal/domain/backendpool.go). A fake that stored it anyway would
			// let the provider look correct on a configuration the platform will
			// not accept.
			if body["proxyProtocol"] == true && !a.poolProxyProtocol {
				w.WriteHeader(http.StatusBadRequest)
				_ = json.NewEncoder(w).Encode(map[string]string{
					"code": "INVALID_REQUEST",
					"message": "proxyProtocol requires the pool's own proxyProtocol to be enabled; " +
						"the pool sends no PROXY header, so there would be none to send on the probe",
				})
				return
			}
			a.check = map[string]any{
				"id": "hc-1", "poolId": "pool-1",
				"protocol":       or(body["protocol"], "http"),
				"path":           or(body["path"], "/"),
				"expectedStatus": or(body["expectedStatus"], "200-299"),
				// 🔴 THE WHOLE POINT. An omitted `port` reverts to the
				// backend's own port and is answered as null -- there is no
				// remembered previous value, unlike every other field here,
				// which is why the attribute is not Computed.
				"port": body["port"],
				// 🔴 THE SAME WHOLE-CHECK RULE AS `port`, AND THE REASON THE
				// ATTRIBUTE CARRIES A SCHEMA DEFAULT RATHER THAN
				// UseStateForUnknown. An omitted proxyProtocol is stored FALSE --
				// it is not left as it was -- so a fake that echoed only what it
				// was sent, or remembered the previous value, could not fail
				// against a provider that quietly re-sends `true` for ever.
				// Always present in the response, never omitted, exactly as the
				// server emits it.
				"proxyProtocol":      body["proxyProtocol"] == true,
				"intervalSeconds":    orNum(body["intervalSeconds"], 10),
				"timeoutSeconds":     orNum(body["timeoutSeconds"], 5),
				"healthyThreshold":   orNum(body["healthyThreshold"], 2),
				"unhealthyThreshold": orNum(body["unhealthyThreshold"], 3),
			}
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(a.check)

		case r.Method == http.MethodGet && r.URL.Path == path:
			if a.check == nil {
				w.WriteHeader(http.StatusNotFound)
				_ = json.NewEncoder(w).Encode(map[string]string{"code": "NOT_FOUND", "message": "no check"})
				return
			}
			_ = json.NewEncoder(w).Encode(a.check)

		case r.Method == http.MethodDelete && r.URL.Path == path:
			a.deletes++
			if a.check == nil {
				w.WriteHeader(http.StatusNotFound)
				_ = json.NewEncoder(w).Encode(map[string]string{"code": "NOT_FOUND", "message": "no check"})
				return
			}
			a.check = nil
			w.WriteHeader(http.StatusNoContent)

		default:
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]string{
				"code": "NOT_FOUND", "message": r.Method + " " + r.URL.Path,
			})
		}
	})
}

func str(v any) string {
	s, _ := v.(string)
	return s
}

func or(v any, def string) any {
	if s, ok := v.(string); ok && s != "" {
		return s
	}
	return def
}

func orNum(v any, def float64) any {
	if n, ok := v.(float64); ok && n != 0 {
		return n
	}
	return def
}

func startHealthCheckAPI(t *testing.T) *healthCheckAPI {
	t.Helper()
	api := &healthCheckAPI{}
	srv := httptest.NewServer(api.handler())
	t.Cleanup(srv.Close)
	t.Setenv("FROSTMOLN_API_ENDPOINT", srv.URL)
	t.Setenv("FROSTMOLN_API_KEY", "acc-test-key") // pragma: allowlist secret
	return api
}

const hcWithoutPort = `
resource "frostmoln_appgw_health_check" "web" {
  gateway_id = "agw-1"
  pool_id    = "pool-1"
  protocol   = "http"
  path       = "/healthz"
}
`

const hcWithPort = `
resource "frostmoln_appgw_health_check" "web" {
  gateway_id = "agw-1"
  pool_id    = "pool-1"
  protocol   = "http"
  path       = "/healthz"
  port       = 8080
}
`

// TestAccHealthCheckPortIsPlanStable covers both halves: unset stays null, set
// stays what the configuration said.
func TestAccHealthCheckPortIsPlanStable(t *testing.T) {
	startHealthCheckAPI(t)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: acctest.TestAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: hcWithoutPort,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckNoResourceAttr("frostmoln_appgw_health_check.web", "port"),
					// The Optional+Computed neighbours still adopt their
					// server-chosen defaults, which is the behaviour `port`
					// deliberately does NOT get.
					resource.TestCheckResourceAttr("frostmoln_appgw_health_check.web", "interval_seconds", "10"),
				),
			},
			{Config: hcWithoutPort, PlanOnly: true},
			{
				Config: hcWithPort,
				Check:  resource.TestCheckResourceAttr("frostmoln_appgw_health_check.web", "port", "8080"),
			},
			{Config: hcWithPort, PlanOnly: true},
		},
	})
}

// TestAccHealthCheckRemovingThePortIsLegibleInThePlan is the assertion that
// separates Optional from Optional+Computed here, and an empty-plan test cannot
// make it.
//
// 🔴 Computed, DELETING `port` FROM THE CONFIGURATION WOULD BE A NO-OP. Terraform
// carries a Computed attribute's prior value into the proposed new state when the
// configuration is null, and the framework only marks such an attribute unknown
// when the proposed state DIFFERS from the prior one -- so the plan comes back
// empty, Terraform reports success, and the probe goes on dialling 8080 while
// the configuration says it should be dialling the backend's own port. Optional,
// the plan reads `port: 8080 -> null`, the PUT omits it, and the server reverts.
//
// The same shape is why the attribute has no schema Default: a Default(0) would
// be a port, and the server refuses 0.
func TestAccHealthCheckRemovingThePortIsLegibleInThePlan(t *testing.T) {
	startHealthCheckAPI(t)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: acctest.TestAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: hcWithPort,
				Check:  resource.TestCheckResourceAttr("frostmoln_appgw_health_check.web", "port", "8080"),
			},
			{
				Config: hcWithoutPort,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction(
							"frostmoln_appgw_health_check.web", plancheck.ResourceActionUpdate),
						plancheck.ExpectKnownValue("frostmoln_appgw_health_check.web",
							tfjsonpath.New("port"), knownvalue.Null()),
					},
				},
				Check: resource.TestCheckNoResourceAttr("frostmoln_appgw_health_check.web", "port"),
			},
			{Config: hcWithoutPort, PlanOnly: true},
		},
	})
}

// TestAccHealthCheckDestroyRemovesTheCheck is the proof that the destroy is a
// real one.
//
// 🔴 THIS RESOURCE USED TO APOLOGISE. With no delete route, Delete dropped the
// resource from state, warned that the check was still probing, and told the
// practitioner to replace the pool to stop it. terraform-plugin-testing runs a
// CheckDestroy after the last step; asserting through the scripted server that
// the check is really gone is what an apology could never satisfy.
func TestAccHealthCheckDestroyRemovesTheCheck(t *testing.T) {
	api := startHealthCheckAPI(t)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: acctest.TestAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: hcWithPort},
		},
	})

	api.mu.Lock()
	defer api.mu.Unlock()
	if api.deletes == 0 {
		t.Fatal("terraform destroy never called DELETE on the health check; the check is still " +
			"probing and nothing in state says so")
	}
	if api.check != nil {
		t.Fatal("the health check survived the destroy")
	}
}

const hcTCP = `
resource "frostmoln_appgw_health_check" "mail" {
  gateway_id       = "agw-1"
  pool_id          = "pool-1"
  protocol         = "tcp"
  port             = 8080
  interval_seconds = 10
}
`

const hcTCPChanged = `
resource "frostmoln_appgw_health_check" "mail" {
  gateway_id       = "agw-1"
  pool_id          = "pool-1"
  protocol         = "tcp"
  port             = 8080
  interval_seconds = 20
}
`

// 🔴 A tcp PROBE HAS TO SURVIVE A SECOND APPLY.
//
// This is a ROUND-TRIP assertion, and it has to be: a test that only checks
// "an explicit path on a tcp probe is refused" passes against the broken
// provider, because the break is not in what the practitioner wrote -- it is in
// what the provider reads back and then sends again.
//
// The server stores `path: "/"` and `expectedStatus: "200-299"` on a tcp probe
// even though it refuses them as input. Both attributes are Optional+Computed
// with UseStateForUnknown, so those two defaults land in state on the first
// apply and are planned back on every subsequent one. Before the fix, step two
// here failed with:
//
//	Error: Failed to Update Health Check
//	API error 400: path and expectedStatus only apply to a http or https probe
//
// and there was no in-place escape -- the resource could be created and never
// changed again. The same wall stood in front of every change after a
// `terraform import`, and every switch of an existing http probe to tcp.
func TestAccHealthCheckTCPProbeSurvivesAnUpdate(t *testing.T) {
	startHealthCheckAPI(t)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: acctest.TestAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: hcTCP,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("frostmoln_appgw_health_check.mail", "protocol", "tcp"),
					// State records what the server actually holds, defaults
					// included. Clearing them here would hide the defect rather
					// than fix it -- the next read would put them straight back.
					resource.TestCheckResourceAttr("frostmoln_appgw_health_check.mail", "path", "/"),
				),
			},
			{Config: hcTCP, PlanOnly: true},
			{
				Config: hcTCPChanged,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction(
							"frostmoln_appgw_health_check.mail", plancheck.ResourceActionUpdate),
					},
				},
				Check: resource.TestCheckResourceAttr(
					"frostmoln_appgw_health_check.mail", "interval_seconds", "20"),
			},
		},
	})
}

const hcProbePortWithProxy = `
resource "frostmoln_appgw_health_check" "mail" {
  gateway_id       = "agw-1"
  pool_id          = "pool-1"
  protocol         = "tcp"
  port             = 8080
  proxy_protocol   = true
  interval_seconds = 10
}
`

// The same check with the `proxy_protocol` LINE DELETED -- not set to false.
// The distinction is the entire subject of the removal test below.
const hcProbePortNoProxy = `
resource "frostmoln_appgw_health_check" "mail" {
  gateway_id       = "agw-1"
  pool_id          = "pool-1"
  protocol         = "tcp"
  port             = 8080
  interval_seconds = 10
}
`

// TestAccHealthCheckProxyProtocolIsPlanStable covers both halves: unset settles
// on the server's `false` and stays there, set stays what the configuration
// said.
func TestAccHealthCheckProxyProtocolIsPlanStable(t *testing.T) {
	api := startHealthCheckAPI(t)
	api.poolProxyProtocol = true

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: acctest.TestAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: hcProbePortNoProxy,
				Check: resource.TestCheckResourceAttr(
					"frostmoln_appgw_health_check.mail", "proxy_protocol", "false"),
			},
			{Config: hcProbePortNoProxy, PlanOnly: true},
			{
				Config: hcProbePortWithProxy,
				Check: resource.TestCheckResourceAttr(
					"frostmoln_appgw_health_check.mail", "proxy_protocol", "true"),
			},
			{Config: hcProbePortWithProxy, PlanOnly: true},
		},
	})
}

// TestAccHealthCheckRemovingProxyProtocolIsLegibleInThePlan is the assertion
// that discriminates, and no empty-plan or import test can make it.
//
// 🔴 WITH UseStateForUnknown INSTEAD OF A Default, DELETING `proxy_protocol`
// FROM THE CONFIGURATION IS A SILENT NO-OP. toRequest reads the PLAN, and for a
// null config on an Optional+Computed+UseStateForUnknown attribute the plan
// value is the STATE value -- so the removed `true` resolves back to true, the
// plan shows no change, no PUT is sent, and the practitioner is told the apply
// succeeded while the probe goes on prepending a PROXY header to a port that
// never asked for one. Nothing reports it: it is not drift, it is a change that
// was never attempted.
//
// A schema Default is the escape. MarkComputedNilsAsUnknown
// (terraform-plugin-framework, internal/fwserver/server_planresourcechange.go)
// returns a default-bearing attribute untouched, so the null config plans
// `false` and the plan reads `proxy_protocol = true -> false`. Against
// UseStateForUnknown this test fails with
// "expected Update, got action(s): [no-op]".
//
// The step-two Check is the other half: the fake stores an omitted
// proxyProtocol as FALSE, as the PUT's whole-check rule requires, so a provider
// that planned false and then re-sent true would be caught by the read-back
// even if the plan somehow read correctly.
func TestAccHealthCheckRemovingProxyProtocolIsLegibleInThePlan(t *testing.T) {
	api := startHealthCheckAPI(t)
	api.poolProxyProtocol = true

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: acctest.TestAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: hcProbePortWithProxy,
				Check: resource.TestCheckResourceAttr(
					"frostmoln_appgw_health_check.mail", "proxy_protocol", "true"),
			},
			{
				Config: hcProbePortNoProxy,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction(
							"frostmoln_appgw_health_check.mail", plancheck.ResourceActionUpdate),
						plancheck.ExpectKnownValue("frostmoln_appgw_health_check.mail",
							tfjsonpath.New("proxy_protocol"), knownvalue.Bool(false)),
					},
				},
				Check: resource.TestCheckResourceAttr(
					"frostmoln_appgw_health_check.mail", "proxy_protocol", "false"),
			},
			{Config: hcProbePortNoProxy, PlanOnly: true},
		},
	})
}

// TestAccHealthCheckProxyProtocolNeedsThePoolsHeader pins the cross-resource
// refusal the provider CANNOT check locally: it never reads the pool, so the
// only place this rule exists is the server, and the only honest thing to do
// with it is surface it. Turning it into a plan-time validator would mean
// guessing at a value this resource does not hold.
func TestAccHealthCheckProxyProtocolNeedsThePoolsHeader(t *testing.T) {
	startHealthCheckAPI(t) // poolProxyProtocol stays false

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: acctest.TestAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config:      hcProbePortWithProxy,
				ExpectError: regexp.MustCompile(`proxyProtocol requires the pool's own proxyProtocol`),
			},
		},
	})
}
