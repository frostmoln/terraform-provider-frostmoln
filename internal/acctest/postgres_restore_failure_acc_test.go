package acctest

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
)

// A REAL `terraform apply` of frostmoln_postgres_instance's restore_from
// against an in-process fake, for the two failure paths that tainted the
// resource on v0.73.3 (Ambix 01a0cb47-9d37). Unit tests assert the state
// Create returns; only core decides "invalid result object after apply" and
// "tainted, so must be replaced", so only a real run proves the guarantee.
// TF_ACC-gated and self-contained, like instance_plan_error_acc_test.go.

const restoreFailureConfig = `
resource "frostmoln_postgres_instance" "t" {
  name                  = "db-restored"
  version               = "16"
  flavor_id             = "db.gp1.small"
  storage_gb            = 50
  vpc_id                = "vpc-1"
  subnet_id             = "sn-1"
  backup_enabled        = true
  backup_retention_days = 60

  restore_from = {
    source_instance_id = "pg-src"
    point_in_time      = "2026-09-22T10:00:00Z"
  }
}
`

// restoreFake serves one restore source and the target its restore creates.
// The target inherits the source's 90-day retention, so the configuration's 60
// is a real in-place difference for the provider to converge after the restore.
type restoreFake struct {
	mu sync.Mutex
	// targetStatus is what the target reports once created.
	targetStatus string
	// refusePuts is how many PUTs to refuse before accepting one.
	refusePuts int
	target     map[string]any
}

func (f *restoreFake) serve(t *testing.T) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		p := r.URL.Path
		switch {
		case p == "/v1/me":
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "u-1", "tenantId": "t-1"})
		case strings.HasSuffix(p, "/events"):
			w.WriteHeader(http.StatusNotFound)
		case strings.HasSuffix(p, "/extensions"):
			_ = json.NewEncoder(w).Encode(map[string]any{"extensionRevision": 0, "extensions": []any{}})
		case r.Method == http.MethodGet && p == "/v1/tenants/t-1/databases/pg-src":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id": "pg-src", "name": "src", "type": "postgresql", "typeVersion": "16",
				"flavorId": "db.gp1.small", "storageGb": 50, "vpcId": "vpc-1", "subnetId": "sn-1",
				"status": "running", "createdAt": "2026-01-01T00:00:00Z",
				"backupEnabled": true, "backupSchedule": "0 2 * * *", "backupRetentionDays": 90,
				"pitrEnabled": true, "pitrCapable": true,
			})
		case r.Method == http.MethodPost && p == "/v1/tenants/t-1/databases/pg-src/restore":
			f.target = map[string]any{
				"id": "pg-1", "name": "db-restored", "type": "postgresql", "typeVersion": "16",
				"flavorId": "db.gp1.small", "storageGb": 50, "vpcId": "vpc-1", "subnetId": "sn-1",
				"status": f.targetStatus, "createdAt": "2026-09-22T22:27:00Z",
				"backupEnabled": true, "backupSchedule": "0 2 * * *", "backupRetentionDays": 90,
				"pitrEnabled": true, "pitrCapable": true,
			}
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(f.target)
		case p == "/v1/tenants/t-1/databases/pg-1":
			switch {
			case f.target == nil:
				w.WriteHeader(http.StatusNotFound)
				_ = json.NewEncoder(w).Encode(map[string]string{"code": "not_found", "message": "gone"})
			case r.Method == http.MethodGet:
				_ = json.NewEncoder(w).Encode(f.target)
			case r.Method == http.MethodPut && f.refusePuts > 0:
				f.refusePuts--
				w.WriteHeader(http.StatusConflict)
				_ = json.NewEncoder(w).Encode(map[string]string{"code": "conflict", "message": "a backup is in progress"})
			case r.Method == http.MethodPut:
				var body map[string]any
				_ = json.NewDecoder(r.Body).Decode(&body)
				for k, v := range body {
					f.target[k] = v
				}
				_ = json.NewEncoder(w).Encode(f.target)
			case r.Method == http.MethodDelete:
				f.target = nil
				w.WriteHeader(http.StatusNoContent)
			}
		default:
			t.Errorf("unexpected request %s %s", r.Method, p)
			w.WriteHeader(http.StatusTeapot)
		}
	}))
	t.Cleanup(srv.Close)
	t.Setenv("FROSTMOLN_API_ENDPOINT", srv.URL)
	t.Setenv("FROSTMOLN_API_KEY", "acc-test-key") // pragma: allowlist secret
}

// 🔴 The data-loss path: the restore SUCCEEDED and only the follow-up in-place
// update was refused. The apply must not error (an errored create is tainted,
// and the next apply would destroy the recovered database), and the next plan
// must be the remaining in-place update — never a replacement — which then
// converges.
func TestAccPostgresRestore_updateRefusedAfterRestoreIsNotTainted(t *testing.T) {
	f := &restoreFake{targetStatus: "running", refusePuts: 1}
	f.serve(t)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: TestAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				// No ExpectError: any error, core's "invalid result object"
				// included, fails the step. The refused update leaves 60 vs 90,
				// and the plan right after must be that update, not a replace.
				Config:             restoreFailureConfig,
				ExpectNonEmptyPlan: true,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PostApplyPostRefresh: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("frostmoln_postgres_instance.t", plancheck.ResourceActionUpdate),
					},
				},
			},
			{
				Config: restoreFailureConfig,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("frostmoln_postgres_instance.t", plancheck.ResourceActionUpdate),
					},
				},
			},
		},
	})
}

// 🔴 The target was created and then refused before provisioning (live
// 2026-09-22 ~22:27Z: a tenant storage quota; status error, no VM). The apply
// warns and does not error, and the resource is NOT tainted: the next plan is
// an in-place update, not "tainted, so must be replaced".
func TestAccPostgresRestore_targetRefusedBeforeProvisioningIsNotTainted(t *testing.T) {
	f := &restoreFake{targetStatus: "error"}
	f.serve(t)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: TestAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config:             restoreFailureConfig,
				ExpectNonEmptyPlan: true,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PostApplyPostRefresh: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("frostmoln_postgres_instance.t", plancheck.ResourceActionUpdate),
					},
				},
			},
		},
	})
}
