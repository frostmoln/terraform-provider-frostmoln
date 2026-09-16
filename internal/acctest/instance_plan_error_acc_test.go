package acctest

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

// TestAccInstance_pinnedSubnetWithoutSecurityGroupsIsAPlanError pins the
// plan-time refusal (Ambix 01a041f8-98ef): `subnet_id` set without
// `security_groups` — null or empty — is refused by ValidateConfig BEFORE any
// API call, so the failure is a plan diagnostic naming the real constraint
// (`security_group_ids is required`), not a 202-turned-failed-operation after
// the saga's pin-port step. TF_ACC-gated, self-contained (httptest in-process):
// the config's import references never resolve because the plan never gets
// there — ValidateConfig fires first — but /v1/me must answer, because the
// provider configures first.
func TestAccInstance_pinnedSubnetWithoutSecurityGroupsIsAPlanError(t *testing.T) {
	var mu sync.Mutex
	var apiCallsPastMe int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path != "/v1/me" {
			// Anything past configuration is a failure of THIS test's premise:
			// ValidateConfig must refuse before the provider talks to the API
			// about instances, VPCs or subnets.
			mu.Lock()
			apiCallsPastMe++
			mu.Unlock()
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]string{"code": "NOT_FOUND", "message": r.Method + " " + r.URL.Path})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "u-1", "tenantId": "t-1"})
	}))
	t.Cleanup(srv.Close)
	t.Setenv("FROSTMOLN_API_ENDPOINT", srv.URL)
	t.Setenv("FROSTMOLN_API_KEY", "acc-test-key") // pragma: allowlist secret

	for _, tc := range []struct {
		name string
		sg   string
	}{
		{"attribute omitted", ""},
		{"attribute empty", "security_groups = []"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sgLine := ""
			if tc.sg != "" {
				sgLine = "  " + tc.sg + "\n"
			}

			resource.Test(t, resource.TestCase{
				ProtoV6ProviderFactories: TestAccProtoV6ProviderFactories,
				Steps: []resource.TestStep{
					{
						Config: `
resource "frostmoln_vpc" "test" {
  name = "vpc-plan-err"
  cidr = "10.231.0.0/16"
}

resource "frostmoln_subnet" "test" {
  name   = "subnet-plan-err"
  vpc_id = frostmoln_vpc.test.id
  cidr   = "10.231.1.0/24"
}

resource "frostmoln_instance" "test" {
  name      = "inst-plan-err"
  flavor_id = "f-1"
  image_id  = "img-1"
  subnet_id = frostmoln_subnet.test.id
` + sgLine + `}
`,
						PlanOnly:    true,
						ExpectError: regexp.MustCompile(`security_group_ids is required`),
					},
				},
			})

			mu.Lock()
			defer mu.Unlock()
			if apiCallsPastMe != 0 {
				t.Fatalf("the refusal must fire before any API call; the fake saw %d API call(s) past /v1/me", apiCallsPastMe)
			}
		})
	}
}
