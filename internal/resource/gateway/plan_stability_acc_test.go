package gateway_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/acctest"
)

// These are the end-to-end proofs for `public_ip_id`, run by real Terraform
// against a scripted API rather than by replaying framework internals. They are
// the only place the whole chain — proposed new state, attribute plan
// modifiers, ModifyPlan, apply, refresh, re-plan — is exercised in the order
// Terraform actually uses, which is exactly where a Computed attribute's
// perpetual diff and an accidental replacement both live.
//
// They are TF_ACC-gated like every other acceptance test here, but they need no
// Frostmoln at all: the API is an httptest server in-process, so they are
// reproducible offline and deterministic. Run them with:
//
//	TF_ACC=1 go test ./internal/resource/gateway/ -run TestAccEgressGateway
//
// terraform-plugin-testing runs a refresh-and-plan after every apply step and
// FAILS the step on a non-empty plan unless ExpectNonEmptyPlan is set. That
// built-in check is the perpetual-diff assertion; the explicit plancheck below
// is the destroy/recreate assertion.

// gatewayAPI is a scripted gateway API: one VPC, one gateway, and
// whatever public IP the gateway was last told to use.
type gatewayAPI struct {
	mu sync.Mutex

	publicIPID string
	mode       string
	address    string

	// reportsUnnamedPublicIP makes the scripted platform answer with a
	// publicIpId the CONFIGURATION never named — the `implicit_public_ip`
	// shape, where the gateway exists because a public IP was associated with an
	// instance and the platform attached one to carry it.
	//
	// Off (the default) is what an unnamed `mode = "public_ip"` create really
	// does: the platform draws the gateway an address of its own and reports NO
	// publicIpId, because there is no public IP resource to name (ADR-0114). The
	// two settings are the two halves of the Optional+Computed trap — a NULL in
	// state facing an unknown in the plan, and a known value in state the
	// configuration never mentions — and both have to stay plan-stable.
	reportsUnnamedPublicIP bool

	// patchBodies records every PATCH body, so a test can assert what went on
	// the wire without a second mock.
	patchBodies []map[string]any
	// deletes counts gateway DELETEs. A re-pointed gateway that was destroyed
	// and recreated would show one here even though the plan looked in-place.
	deletes int
}

func (a *gatewayAPI) gateway() map[string]any {
	gw := map[string]any{
		"id":       "gw-1",
		"vpcId":    "vpc-1",
		"tenantId": "t-1",
		"mode":     a.mode,
		"status":   "active",
		"origin":   "explicit",
	}
	if a.address != "" {
		gw["sourceAddress"] = a.address
	}
	if a.publicIPID != "" {
		gw["publicIpId"] = a.publicIPID
		gw["origin"] = "explicit_public_ip"
	}
	return gw
}

// addressFor keeps the fiction honest: each public IP has its own address, so a
// re-point really does change source_address, the way the platform's does.
func addressFor(publicIPID string) string {
	switch publicIPID {
	case "":
		return "198.51.100.1"
	case "pip-1":
		return "198.51.100.11"
	default:
		return "198.51.100.22"
	}
}

func (a *gatewayAPI) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a.mu.Lock()
		defer a.mu.Unlock()

		w.Header().Set("Content-Type", "application/json")

		switch {
		case r.URL.Path == "/v1/me":
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "u-1", "tenantId": "t-1"})

		case r.Method == http.MethodPost && r.URL.Path == "/v1/tenants/t-1/gateways":
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			a.mode, _ = body["mode"].(string)
			a.publicIPID, _ = body["publicIpId"].(string)
			a.address = addressFor(a.publicIPID)
			// An omitted publicIpId under public_ip means "the platform draws
			// the address itself", and it reports NO publicIpId back — the
			// address is not a public IP resource, so there is nothing to name.
			// The opt-in below is the other real shape: a gateway the platform
			// attached around an address the configuration never mentioned.
			if a.publicIPID == "" && a.mode == "public_ip" && a.reportsUnnamedPublicIP {
				a.publicIPID = "pip-implicit"
				a.address = addressFor(a.publicIPID)
			}
			_ = json.NewEncoder(w).Encode(a.gateway())

		case r.Method == http.MethodPatch && strings.HasPrefix(r.URL.Path, "/v1/tenants/t-1/gateways/"):
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			a.patchBodies = append(a.patchBodies, body)
			a.mode, _ = body["mode"].(string)
			if pip, ok := body["publicIpId"].(string); ok && pip != "" {
				a.publicIPID = pip
			}
			if a.mode != "public_ip" {
				a.publicIPID = ""
			}
			a.address = addressFor(a.publicIPID)
			_ = json.NewEncoder(w).Encode(a.gateway())

		case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/v1/tenants/t-1/gateways/"):
			a.deletes++
			a.mode, a.publicIPID, a.address = "", "", ""
			w.WriteHeader(http.StatusNoContent)

		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-1/gateways":
			if a.mode == "" {
				_ = json.NewEncoder(w).Encode(map[string]any{"gateways": []any{}, "totalCount": 0})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"gateways": []any{a.gateway()}, "totalCount": 1,
			})

		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v1/tenants/t-1/gateways/"):
			if a.mode == "" {
				w.WriteHeader(http.StatusNotFound)
				_ = json.NewEncoder(w).Encode(map[string]any{
					"error": map[string]string{"code": "NOT_FOUND", "message": "no gateway"},
				})
				return
			}
			_ = json.NewEncoder(w).Encode(a.gateway())

		default:
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"error": map[string]string{"code": "NOT_FOUND", "message": r.Method + " " + r.URL.Path},
			})
		}
	})
}

func startGatewayAPI(t *testing.T) *gatewayAPI {
	t.Helper()
	api := &gatewayAPI{}
	srv := httptest.NewServer(api.handler())
	t.Cleanup(srv.Close)
	t.Setenv("FROSTMOLN_API_ENDPOINT", srv.URL)
	t.Setenv("FROSTMOLN_API_KEY", "acc-test-key") // pragma: allowlist secret
	return api
}

// TestAccEgressGatewayOmittedPublicIPIDIsPlanStable is the perpetual-diff
// proof for the case the plan calls out: `mode = "public_ip"` with no
// `public_ip_id`.
//
// The platform draws the gateway an address of its own and reports NO
// publicIpId (ADR-0114) — no id, no public IP resource — so an Optional+Computed
// attribute ends up holding a NULL in state. Null-versus-unknown is a diff on
// every single run, and it is the failure mode that survives a careless
// UseStateForUnknown.
//
// Every apply step here is followed by terraform-plugin-testing's own
// refresh-and-plan, which fails the step on a non-empty plan — so the third
// step, a no-op re-apply of the identical configuration, asserts stability
// across a full second cycle rather than just once.
func TestAccEgressGatewayOmittedPublicIPIDIsPlanStable(t *testing.T) {
	startGatewayAPI(t)

	// The acknowledgement is present only so the harness can tear the gateway
	// down at the end of the test; it is Optional-only and plays no part in
	// how `public_ip_id` is planned.
	const config = `
resource "frostmoln_gateway" "test" {
  vpc_id                        = "vpc-1"
  mode                          = "public_ip"
  acknowledge_connectivity_loss = true
}
`

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: acctest.TestAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: config,
				Check: resource.ComposeAggregateTestCheckFunc(
					// No public IP resource exists, so the attribute is NULL —
					// not "" — and the gateway still has a source address.
					resource.TestCheckNoResourceAttr("frostmoln_gateway.test", "public_ip_id"),
					resource.TestCheckResourceAttr("frostmoln_gateway.test", "source_address", "198.51.100.1"),
					resource.TestCheckResourceAttr("frostmoln_gateway.test", "origin", "explicit"),
				),
			},
			{
				// The explicit statement of the invariant: planning the same
				// configuration again produces NO actions at all.
				Config:   config,
				PlanOnly: true,
			},
			{
				// And a second full cycle, whose built-in post-apply plan check
				// would fail on any residual diff.
				Config: config,
			},
		},
	})
}

// TestAccEgressGatewayUnnamedPublicIPIDIsPlanStable is the other half of the
// perpetual-diff proof: the platform answers with a public IP id the
// CONFIGURATION never named, so an Optional+Computed attribute holds a known
// value with nothing in the config to match it against. Pinned known and wrong,
// that is "Provider produced inconsistent result after apply"; re-planned as
// unknown on every run, it is a permanent diff.
//
// It is a real shape, not a hypothetical: a gateway the platform attached
// because a public IP was associated with an instance in the VPC reports that
// address (`origin` = "implicit_public_ip"), and importing it leaves exactly
// this state against a configuration that names no address. An UNNAMED create
// does NOT produce it — that path gets a platform-drawn address with no id at
// all, which is the test above.
func TestAccEgressGatewayUnnamedPublicIPIDIsPlanStable(t *testing.T) {
	api := startGatewayAPI(t)
	api.reportsUnnamedPublicIP = true

	const config = `
resource "frostmoln_gateway" "test" {
  vpc_id                        = "vpc-1"
  mode                          = "public_ip"
  acknowledge_connectivity_loss = true
}
`

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: acctest.TestAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: config,
				Check: resource.TestCheckResourceAttr(
					"frostmoln_gateway.test", "public_ip_id", "pip-implicit"),
			},
			{
				Config:   config,
				PlanOnly: true,
			},
		},
	})
}

// TestAccEgressGatewayPublicIPIDChangeIsInPlace is the destroy/recreate proof.
//
// A RequiresReplace on this attribute would destroy the VPC's only outbound
// path and build a new one — taking internet, platform DNS resolution and
// managed-service connectivity down in between, which is the outage a stable
// gateway source address exists to avoid. Two independent assertions, because either
// alone can be satisfied by accident:
//
//   - the plan itself must contain an Update and NOT a DeleteBeforeCreate or
//     CreateBeforeDelete (plancheck.ExpectResourceAction);
//   - the API must have seen a PATCH and NO DELETE at all (the scripted server
//     counts them), which no plan-level assertion can fake.
func TestAccEgressGatewayPublicIPIDChangeIsInPlace(t *testing.T) {
	api := startGatewayAPI(t)

	withPin := func(pip string) string {
		return `
resource "frostmoln_gateway" "test" {
  vpc_id                        = "vpc-1"
  mode                          = "public_ip"
  public_ip_id                  = "` + pip + `"
  acknowledge_connectivity_loss = true
}
`
	}

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: acctest.TestAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: withPin("pip-1"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("frostmoln_gateway.test", "public_ip_id", "pip-1"),
					resource.TestCheckResourceAttr("frostmoln_gateway.test", "source_address", "198.51.100.11"),
				),
			},
			{
				Config: withPin("pip-2"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction(
							"frostmoln_gateway.test", plancheck.ResourceActionUpdate),
					},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("frostmoln_gateway.test", "public_ip_id", "pip-2"),
					// The address really moved, so the re-plan that follows is a
					// meaningful stability check and not a no-op.
					resource.TestCheckResourceAttr("frostmoln_gateway.test", "source_address", "198.51.100.22"),
					// Asserted HERE and not after resource.Test returns: the
					// harness destroys the gateway when the case ends, so a
					// count read afterwards always shows one DELETE and would
					// prove nothing.
					api.checkAppliedInPlace("pip-2"),
				),
			},
		},
	})
}

// checkAppliedInPlace asserts, from the API side, that the address change was
// applied to the existing gateway rather than by replacing it.
func (a *gatewayAPI) checkAppliedInPlace(wantPin string) resource.TestCheckFunc {
	return func(*terraform.State) error {
		a.mu.Lock()
		defer a.mu.Unlock()

		if a.deletes != 0 {
			return fmt.Errorf("re-pointing the gateway at another public IP DELETED it %d time(s): the "+
				"VPC lost its outbound path, its DNS resolution and its managed-service connectivity in "+
				"between. The change must be an in-place PATCH", a.deletes)
		}
		if len(a.patchBodies) != 1 {
			return fmt.Errorf("expected exactly one PATCH for the address change, got %d: %v",
				len(a.patchBodies), a.patchBodies)
		}
		body := a.patchBodies[0]
		if body["publicIpId"] != wantPin {
			return fmt.Errorf("the PATCH must carry the newly chosen address %q, got %v", wantPin, body["publicIpId"])
		}
		if body["mode"] != "public_ip" {
			return fmt.Errorf("the PATCH must still carry the mode, got %v", body["mode"])
		}
		if body["acknowledgeConnectivityLoss"] != true {
			return fmt.Errorf("the PATCH must carry the practitioner's acknowledgement, got %v",
				body["acknowledgeConnectivityLoss"])
		}
		return nil
	}
}
