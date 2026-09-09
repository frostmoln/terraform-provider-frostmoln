package appgw_listener_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/knownvalue"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/acctest"
)

// The empty-plan proofs for the `tcp` listener, run by real Terraform against a
// scripted appgw rather than by replaying framework internals. They are the only
// place the whole chain — proposed new state, attribute plan modifiers,
// ValidateConfig, apply, refresh, re-plan — runs in the order Terraform actually
// uses, which is exactly where an Optional-versus-Computed mistake lives.
//
// TF_ACC-gated like every other acceptance test here, but they need no Frostmoln
// at all: the API is an httptest server in-process, so they are reproducible
// offline and deterministic. Run them with:
//
//	TF_ACC=1 go test ./internal/resource/appgw_listener/ -run TestAccListener
//
// terraform-plugin-testing runs a refresh-and-plan after every apply step and
// FAILS the step on a non-empty plan unless ExpectNonEmptyPlan is set. That
// built-in check IS the perpetual-diff assertion; the explicit PlanOnly steps
// below state it a second time, at a point where a diff could only come from the
// refresh.

// listenerAPI is a scripted listener surface: it stores what it was told and
// answers reads with the SHAPE the real server answers with, which is the half
// that matters here.
type listenerAPI struct {
	mu        sync.Mutex
	listeners map[string]map[string]any
	next      int
}

// present builds the read shape.
//
// 🔴 THE TWO SERVER BEHAVIOURS THIS WHOLE FILE EXISTS TO PIN:
//
//   - `portRangeEnd` is emitted UNCONDITIONALLY, as null for a single-port
//     listener (appgw's domain.Listener carries no `omitempty` on it, so the
//     document's `nullable: true` matches the bytes). Absent and null decode the
//     same on this side; what must not happen is a 0.
//   - A `tcp` listener reports tlsMinVersion and tlsCipherProfile as `""`
//     (appgw's presentListener), because it terminates no TLS. http and https
//     keep returning "1.2"/"intermediate" as they always have.
func (a *listenerAPI) present(l map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range l {
		out[k] = v
	}
	if _, ok := out["portRangeEnd"]; !ok {
		out["portRangeEnd"] = nil
	}
	if out["protocol"] == "tcp" {
		out["tlsMinVersion"] = ""
		out["tlsCipherProfile"] = ""
	} else {
		out["tlsMinVersion"] = firstNonEmpty(out["tlsMinVersion"], "1.2")
		out["tlsCipherProfile"] = firstNonEmpty(out["tlsCipherProfile"], "intermediate")
	}
	out["geoBlockMode"] = firstNonEmpty(out["geoBlockMode"], "off")
	out["enabled"] = true
	out["createdAt"] = "2026-09-01T00:00:00Z"
	return out
}

func firstNonEmpty(v any, def string) string {
	if s, ok := v.(string); ok && s != "" {
		return s
	}
	return def
}

func (a *listenerAPI) handler() http.Handler {
	const base = "/v1/tenants/t-1/application-gateways/agw-1/listeners"
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
			a.next++
			id := "lsn-" + string(rune('0'+a.next))
			body["id"] = id
			body["gatewayId"] = "agw-1"
			a.listeners[id] = body
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(a.present(body))

		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, base+"/"):
			l, ok := a.listeners[strings.TrimPrefix(r.URL.Path, base+"/")]
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				_ = json.NewEncoder(w).Encode(map[string]string{"code": "NOT_FOUND", "message": "gone"})
				return
			}
			_ = json.NewEncoder(w).Encode(a.present(l))

		case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, base+"/"):
			delete(a.listeners, strings.TrimPrefix(r.URL.Path, base+"/"))
			w.WriteHeader(http.StatusNoContent)

		default:
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]string{
				"code": "NOT_FOUND", "message": r.Method + " " + r.URL.Path,
			})
		}
	})
}

func startListenerAPI(t *testing.T) *listenerAPI {
	t.Helper()
	api := &listenerAPI{listeners: map[string]map[string]any{}}
	srv := httptest.NewServer(api.handler())
	t.Cleanup(srv.Close)
	t.Setenv("FROSTMOLN_API_ENDPOINT", srv.URL)
	t.Setenv("FROSTMOLN_API_KEY", "acc-test-key") // pragma: allowlist secret
	return api
}

// TestAccListenerTCPSinglePortIsPlanStable is the `port_range_end` proof.
//
// 🔴 THIS IS WHERE Computed WOULD HAVE FAILED. A single-port tcp listener is
// answered with `portRangeEnd: null`, and the configuration names no range.
// Marked Computed, the attribute plans as UNKNOWN from that null config and then
// resolves to null on apply — "known after apply" on every run for a value
// nobody set, and a hard "provider produced inconsistent result" wherever the
// unknown is pinned to state instead. Optional, it is null in config, null in
// plan and null in state, and there is no diff to have.
//
// The same argument covers `backend_pool_id` from the other side: it is SET
// here, echoed back, and must stay exactly what the configuration said.
func TestAccListenerTCPSinglePortIsPlanStable(t *testing.T) {
	startListenerAPI(t)

	const config = `
resource "frostmoln_appgw_listener" "smtp" {
  gateway_id      = "agw-1"
  name            = "smtp"
  protocol        = "tcp"
  port            = 25
  backend_pool_id = "pool-1"
}
`

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: acctest.TestAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: config,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("frostmoln_appgw_listener.smtp", "port", "25"),
					resource.TestCheckResourceAttr("frostmoln_appgw_listener.smtp", "backend_pool_id", "pool-1"),
					// null, not 0 and not "known after apply": this listener
					// binds one port and the server says so with a null.
					resource.TestCheckNoResourceAttr("frostmoln_appgw_listener.smtp", "port_range_end"),
					// 🔴 THE tcp TLS ECHO. The server answers "" for both,
					// which is in neither attribute's OneOf set. Landing in
					// state as "" would put a value there that the same schema
					// refuses in configuration.
					resource.TestCheckNoResourceAttr("frostmoln_appgw_listener.smtp", "tls_min_version"),
					resource.TestCheckNoResourceAttr("frostmoln_appgw_listener.smtp", "tls_cipher_profile"),
				),
			},
			{Config: config, PlanOnly: true},
			{Config: config},
		},
	})
}

// TestAccListenerTCPPortRangeIsPlanStable is the same proof with the range set:
// a value the configuration DID name has to survive the round trip unchanged.
func TestAccListenerTCPPortRangeIsPlanStable(t *testing.T) {
	startListenerAPI(t)

	const config = `
resource "frostmoln_appgw_listener" "ftp_data" {
  gateway_id      = "agw-1"
  name            = "ftp-data"
  protocol        = "tcp"
  port            = 8000
  port_range_end  = 8100
  backend_pool_id = "pool-1"
}
`

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: acctest.TestAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: config,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("frostmoln_appgw_listener.ftp_data", "port", "8000"),
					resource.TestCheckResourceAttr("frostmoln_appgw_listener.ftp_data", "port_range_end", "8100"),
				),
			},
			{Config: config, PlanOnly: true},
			{Config: config},
		},
	})
}

// TestAccListenerHTTPSIsStillPlanStable is the regression half.
//
// The TLS attributes are Optional+Computed and now map "" to null. On an https
// listener the server still returns "1.2"/"intermediate", so those values must
// still be adopted into state from a configuration that never named them — the
// behaviour that has been there since the first release and that the tcp change
// must not disturb. `backend_pool_id` and `port_range_end` must stay null here:
// the server refuses both on an L7 listener and omits them from the answer.
func TestAccListenerHTTPSIsStillPlanStable(t *testing.T) {
	startListenerAPI(t)

	const config = `
resource "frostmoln_appgw_listener" "https" {
  gateway_id = "agw-1"
  name       = "https"
  protocol   = "https"
  port       = 443
}
`

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: acctest.TestAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: config,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("frostmoln_appgw_listener.https", "tls_min_version", "1.2"),
					resource.TestCheckResourceAttr("frostmoln_appgw_listener.https", "tls_cipher_profile", "intermediate"),
					resource.TestCheckNoResourceAttr("frostmoln_appgw_listener.https", "port_range_end"),
					resource.TestCheckNoResourceAttr("frostmoln_appgw_listener.https", "backend_pool_id"),
				),
			},
			{Config: config, PlanOnly: true},
			{Config: config},
		},
	})
}

// TestAccListenerDroppingThePortRangeIsLegibleInThePlan is the assertion that
// separates Optional from Optional+Computed on this attribute, and it is the
// one an empty-plan test cannot make.
//
// 🔴 A NO-OP PLAN CANNOT SEE THE DIFFERENCE. The framework marks Computed
// attributes with a null configuration as unknown only when the proposed new
// state DIFFERS from the prior state (fwserver.PlanResourceChange), so on a plan
// with nothing to do the Computed variant is never marked and both spellings
// produce the same empty plan. The difference appears the moment something else
// in the resource changes -- and the change that matters here is the
// practitioner deleting `port_range_end` from their configuration.
//
// Optional, the plan reads `port_range_end: 8100 -> null`: a 101-port listener
// is about to become a one-port listener, and the plan says so before the port
// range is torn down.
//
// Computed, MEASURED against this same schema: the planned value comes back as
// the OLD number rather than null, because Terraform carries a Computed
// attribute's prior value into the proposed new state when the configuration is
// null. Nothing else changed, so the proposed state equals the prior state, the
// framework never marks it unknown, and the plan is EMPTY -- deleting the line
// does nothing and Terraform reports success while the listener goes on binding
// all 101 ports. (The health check's `port` is the same trap and reports it more
// bluntly: "expected Update, got action(s): [no-op]".)
//
// The listener's ports are opened and closed WITHOUT a configuration apply, so
// this is not a delayed disagreement: it is a public port space that stays open
// against a configuration that says it should not be.
func TestAccListenerDroppingThePortRangeIsLegibleInThePlan(t *testing.T) {
	startListenerAPI(t)

	const ranged = `
resource "frostmoln_appgw_listener" "ftp_data" {
  gateway_id      = "agw-1"
  name            = "ftp-data"
  protocol        = "tcp"
  port            = 8000
  port_range_end  = 8100
  backend_pool_id = "pool-1"
}
`
	const single = `
resource "frostmoln_appgw_listener" "ftp_data" {
  gateway_id      = "agw-1"
  name            = "ftp-data"
  protocol        = "tcp"
  port            = 8000
  backend_pool_id = "pool-1"
}
`

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: acctest.TestAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: ranged,
				Check: resource.TestCheckResourceAttr(
					"frostmoln_appgw_listener.ftp_data", "port_range_end", "8100"),
			},
			{
				Config: single,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectKnownValue(
							"frostmoln_appgw_listener.ftp_data",
							tfjsonpath.New("port_range_end"),
							knownvalue.Null(),
						),
					},
				},
				Check: resource.TestCheckNoResourceAttr(
					"frostmoln_appgw_listener.ftp_data", "port_range_end"),
			},
			{Config: single, PlanOnly: true},
		},
	})
}

// TestAccListenerHTTPSBackendPoolIDStaysKnownNull is the same assertion for
// `backend_pool_id`, from the side where the attribute is never set at all.
//
// An http or https listener has no pool of its own -- each route names one --
// so the server omits the field and the answer is null for the life of the
// resource. The port change is there only to make the proposed new state differ
// from the prior state, which is the condition under which a Computed attribute
// with a null configuration would be marked unknown. It must stay a known null:
// "known after apply" on an attribute this protocol cannot even carry is the
// provider claiming a value is coming that never will.
func TestAccListenerHTTPSBackendPoolIDStaysKnownNull(t *testing.T) {
	startListenerAPI(t)

	cfg := func(port int) string {
		return `
resource "frostmoln_appgw_listener" "https" {
  gateway_id = "agw-1"
  name       = "https"
  protocol   = "https"
  port       = ` + strconv.Itoa(port) + `
}
`
	}

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: acctest.TestAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: cfg(443)},
			{
				Config: cfg(8443),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectKnownValue(
							"frostmoln_appgw_listener.https",
							tfjsonpath.New("backend_pool_id"),
							knownvalue.Null(),
						),
						plancheck.ExpectKnownValue(
							"frostmoln_appgw_listener.https",
							tfjsonpath.New("port_range_end"),
							knownvalue.Null(),
						),
					},
				},
			},
			{Config: cfg(8443), PlanOnly: true},
		},
	})
}
