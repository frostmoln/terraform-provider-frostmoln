package acctest

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

// A REAL `terraform apply` of an ordinary (non-adopting)
// frostmoln_appgw_backend_authorization create. On v0.73.3 it failed with
// "Provider returned invalid result object after apply ... unknown value for
// ...adopted" and tainted: every ordinary create of the resource (Ambix
// 01a0cd73-3de9). TF_ACC-gated, self-contained. The post-apply plan must be
// empty, and the destroy at the end must work.
func TestAccAppgwBackendAuthorization_nonAdoptingCreateApplies(t *testing.T) {
	base := "/v1/tenants/t-1/application-gateways/agw-1"
	row := map[string]any{
		"id": "a-1", "gatewayId": "agw-1", "targetSecurityGroupId": "sg-1",
		"protocol": "tcp", "portMin": 8080, "portMax": 8080, "authorizedBy": "u", "createdAt": "2026-09-23T00:00:00Z",
	}
	gone := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/v1/me":
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "u-1", "tenantId": "t-1"})
		case r.Method == http.MethodPost && r.URL.Path == base+"/backend-pools/pool-1/backends/b-1/authorize":
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(row)
		case r.Method == http.MethodGet && r.URL.Path == base+"/authorizations":
			items := []any{row}
			if gone {
				items = []any{}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"items": items})
		case r.Method == http.MethodDelete:
			gone = true
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	t.Setenv("FROSTMOLN_API_ENDPOINT", srv.URL)
	t.Setenv("FROSTMOLN_API_KEY", "k") // pragma: allowlist secret
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: TestAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: `
resource "frostmoln_appgw_backend_authorization" "a" {
  gateway_id        = "agw-1"
  pool_id           = "pool-1"
  backend_id        = "b-1"
  security_group_id = "sg-1"
}
`,
			Check: resource.TestCheckResourceAttr("frostmoln_appgw_backend_authorization.a", "adopted", "false"),
		}},
	})
}
