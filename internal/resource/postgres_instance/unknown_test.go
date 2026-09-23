package postgres_instance

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

// frameworkPlannedCreate is a create plan as terraform-plugin-framework REALLY
// builds it for a configuration that writes only the required attributes:
// every Computed attribute without a schema Default is UNKNOWN, `extensions`
// included.
//
// It deliberately does not start from plannedCreate, which plans `extensions`
// as NULL — the value the framework never produces for an omitted
// Optional+Computed attribute, and the reason no test caught v0.73.3 leaving it
// unknown in state on every failed create (Ambix 01a0cb47-9d37).
func frameworkPlannedCreate() PostgresInstanceModel {
	m := plannedCreate()
	m.Extensions = types.SetUnknown(types.StringType)
	m.ID = types.StringUnknown()
	m.HAStatus = types.StringUnknown()
	m.PITRCapable = types.BoolUnknown()
	m.PITRArchivePausedReason = types.StringUnknown()
	m.EarliestRestorableTime = types.StringUnknown()
	m.LatestRestorableTime = types.StringUnknown()
	m.Status = types.StringUnknown()
	m.PrivateIP = types.StringUnknown()
	m.Port = types.Int64Unknown()
	m.PublicIP = types.StringUnknown()
	m.AdminUsername = types.StringUnknown()
	m.CreatedAt = types.StringUnknown()
	m.UpdatedAt = types.StringUnknown()
	m.TenantID = types.StringUnknown()
	return m
}

// assertNoUnknownInState is core's own check on an apply result. An unknown
// left in the state Create returns is "Provider returned invalid result object
// after apply", an ERROR, and an errored create is TAINTED: the next apply
// destroys the instance. It is also asserted that `extensions` is a known SET,
// never null, because a refresh reads it back as one.
func assertNoUnknownInState(t *testing.T, createResp resource.CreateResponse) {
	t.Helper()
	if !createResp.State.Raw.IsFullyKnown() {
		t.Fatalf("Create returned state with an UNKNOWN in it — Terraform raises \"invalid result object "+
			"after apply\" and taints the instance: %s", createResp.State.Raw.String())
	}
	if createResp.State.Raw.IsNull() {
		return
	}
	var ext types.Set
	if d := createResp.State.GetAttribute(context.Background(), path.Root("extensions"), &ext); d.HasError() {
		t.Fatalf("read extensions: %v", d.Errors())
	}
	if ext.IsNull() {
		t.Error("extensions recorded as null; a refresh reads the instance's ledger back as a set, so an " +
			"instance with none must record the EMPTY set")
	}
}

// The restore paths once the target exists: nothing may be an error, and the
// state must be fully known — together, what "not tainted" means to core.
func assertRestoreNotTainted(t *testing.T, createResp resource.CreateResponse) {
	t.Helper()
	if createResp.Diagnostics.HasError() {
		t.Fatalf("Create ERRORED after the restore target existed — Terraform taints it: %v",
			createResp.Diagnostics.Errors())
	}
	assertNoUnknownInState(t, createResp)
	var state PostgresInstanceModel
	if d := createResp.State.Get(context.Background(), &state); d.HasError() {
		t.Fatalf("state.Get: %v", d.Errors())
	}
	if state.ID.ValueString() != "db-tgt" {
		t.Fatalf("the restore target must stay tracked, state id = %q", state.ID.ValueString())
	}
}

func restoreTarget() map[string]any {
	return map[string]any{
		"id": "db-tgt", "name": "tgt", "type": "postgresql", "typeVersion": "16",
		"flavorId": "db.gp1.small", "storageGb": 50, "vpcId": "vpc-1", "subnetId": "sn-1",
		"status": "running", "createdAt": justNow(),
		"backupEnabled": true, "backupSchedule": "0 2 * * *", "backupRetentionDays": 90,
		"pitrEnabled": true, "pitrCapable": true,
	}
}

func frameworkRestorePlan(t *testing.T) PostgresInstanceModel {
	t.Helper()
	m := frameworkPlannedCreate()
	r := restorePlanModel(t, 50)
	m.Name, m.BackupRetentionDays, m.RestoreFrom = r.Name, r.BackupRetentionDays, r.RestoreFrom
	return m
}

// 🔴 A plain create whose saga FAILS after the 202 has recorded the instance.
// Live 2026-09-22 on v0.73.3 (a Cinder quota refusal): the error was right, but
// the early record still carried the planned-unknown `extensions`, and core
// added "invalid result object after apply" on top of it.
func TestFailedPlainCreateLeavesNoUnknownInState(t *testing.T) {
	for _, tc := range []struct {
		name string
		// opStatus is what the create operation reports; instStatus what the
		// instance GET answers afterwards.
		opStatus, instStatus string
	}{
		{name: "the saga fails", opStatus: "failed", instStatus: "error"},
		{name: "the operation completes but the instance never reaches running", opStatus: "completed", instStatus: "error"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch {
				case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/databases"):
					w.WriteHeader(http.StatusAccepted)
					_ = json.NewEncoder(w).Encode(map[string]any{"operationId": "op-1", "status": "pending", "resourceId": "db-1"})
				case strings.HasSuffix(r.URL.Path, "/events"):
					// No event stream on this fake: the poller falls back to polling.
					w.WriteHeader(http.StatusNotFound)
				case strings.Contains(r.URL.Path, "/operations/op-1"):
					_ = json.NewEncoder(w).Encode(map[string]any{
						"operationId": "op-1", "status": tc.opStatus, "resourceId": "db-1", "error": "quota exceeded",
					})
				case strings.HasSuffix(r.URL.Path, "/databases/db-1"):
					_ = json.NewEncoder(w).Encode(map[string]any{
						"id": "db-1", "name": "test-pg", "type": "postgresql", "typeVersion": "16",
						"flavorId": "db.gp1.small", "storageGb": 50, "vpcId": "vpc-1", "subnetId": "sn-1",
						"status": tc.instStatus, "createdAt": justNow(),
					})
				default:
					t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
					w.WriteHeader(http.StatusTeapot)
				}
			}))
			defer server.Close()

			r := newResource(newClient(t, server))
			createResp := resource.CreateResponse{State: emptyState(t)}
			r.Create(context.Background(), createRequest(buildPlan(t, frameworkPlannedCreate())), &createResp)

			if !createResp.Diagnostics.HasError() {
				t.Fatal("a failed plain create must still fail — only the restore path keeps a failure as a warning")
			}
			if createResp.State.Raw.IsNull() {
				t.Fatal("the instance was recorded before the wait; it must stay recorded so it can be destroyed")
			}
			assertNoUnknownInState(t, createResp)
		})
	}
}

// 🔴 The restore target was created and then REFUSED before provisioning
// (live 2026-09-22 ~22:27Z, a tenant storage quota): status error, no VM, no
// data. v0.73.3 warned — correctly not an error — but left `extensions` unknown,
// core turned that into an error, and the next plan said "tainted, so must be
// replaced".
func TestRestoreTargetRefusedBeforeProvisioningIsNotTainted(t *testing.T) {
	target := restoreTarget()
	rs := newRestoreServer(t, target, func(w http.ResponseWriter, r *http.Request) bool {
		if !strings.HasSuffix(r.URL.Path, "/databases/db-tgt") || r.Method != http.MethodGet {
			return false
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": "db-tgt", "name": "tgt", "type": "postgresql", "typeVersion": "16",
			"flavorId": "db.gp1.small", "storageGb": 50, "vpcId": "vpc-1", "subnetId": "sn-1",
			"status": "error", "createdAt": justNow(),
		})
		return true
	})
	defer rs.Close()

	createResp := runRestoreCreate(t, rs, frameworkRestorePlan(t))
	assertRestoreNotTainted(t, createResp)

	// The copy must say what is KNOWN. A target that never reached running
	// may hold nothing at all, and telling the practitioner "the restored
	// database EXISTS" sends them looking for data that was never restored.
	text := diagText(createResp.Diagnostics.Warnings())
	if strings.Contains(text, "restored database EXISTS") {
		t.Errorf("a target that never reached running must not be called a restored database that exists:\n%s", text)
	}
	if !strings.Contains(text, `"error"`) {
		t.Errorf("the warning must name the status the target reported:\n%s", text)
	}
}

// 🔴 The data-loss path. The restore SUCCEEDED — the target is running and
// holds the recovered data — and only the follow-up in-place update failed.
// On v0.73.3 the unknown `extensions` made core error and taint, and the next
// apply would have destroyed the recovered database.
func TestRestoreSucceededThenUpdateFailedIsNotTainted(t *testing.T) {
	var refusedPuts atomic.Int32
	rs := newRestoreServer(t, restoreTarget(), func(w http.ResponseWriter, r *http.Request) bool {
		if r.Method != http.MethodPut {
			return false
		}
		refusedPuts.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		_ = json.NewEncoder(w).Encode(map[string]string{"code": "conflict", "message": "a backup is in progress"})
		return true
	})
	defer rs.Close()

	createResp := runRestoreCreate(t, rs, frameworkRestorePlan(t))
	assertRestoreNotTainted(t, createResp)
	if refusedPuts.Load() != 1 {
		t.Fatalf("PUTs = %d; the test's premise is that the one follow-up update was attempted and refused", refusedPuts.Load())
	}
	if text := diagText(createResp.Diagnostics.Warnings()); !strings.Contains(text, "restored database EXISTS") {
		t.Errorf("a target that reached running holds the recovered data, and the warning must say so:\n%s", text)
	}
}

// The restore POST itself refused: nothing exists, the create fails, and no
// state at all is written — so nothing can be left unknown either.
func TestRestoreRefusedBeforeAnyTargetWritesNoState(t *testing.T) {
	rs := newRestoreServer(t, restoreTarget(), func(w http.ResponseWriter, r *http.Request) bool {
		if !strings.HasSuffix(r.URL.Path, "/restore") {
			return false
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"code": "pitr_out_of_window", "message": "out of window"})
		return true
	})
	defer rs.Close()

	createResp := runRestoreCreate(t, rs, frameworkRestorePlan(t))
	if !createResp.Diagnostics.HasError() {
		t.Fatal("a refused restore POST created nothing and must fail the create")
	}
	if !createResp.State.Raw.IsNull() {
		t.Fatalf("nothing exists, so nothing may be recorded: %s", createResp.State.Raw.String())
	}
}

// The CLASS, not the one attribute: settleUnknowns must leave nothing unknown
// whatever attribute a future change forgets to fill. Every attribute of the
// schema is made unknown here — derived from the schema itself, so an attribute
// added later is covered without editing this test.
func TestSettleUnknownsCoversEverySchemaAttribute(t *testing.T) {
	st := emptyState(t)
	objType := st.Schema.Type().TerraformType(context.Background()).(tftypes.Object)
	vals := map[string]tftypes.Value{}
	for name, typ := range objType.AttributeTypes {
		vals[name] = tftypes.NewValue(typ, tftypes.UnknownValue)
	}
	vals["id"] = tftypes.NewValue(tftypes.String, "db-1")
	st.Raw = tftypes.NewValue(objType, vals)

	createResp := resource.CreateResponse{State: st}
	settleUnknowns(&createResp.State, &createResp.Diagnostics)
	if createResp.Diagnostics.HasError() {
		t.Fatalf("settleUnknowns: %v", createResp.Diagnostics.Errors())
	}
	assertNoUnknownInState(t, createResp)
	var id types.String
	_ = createResp.State.GetAttribute(context.Background(), path.Root("id"), &id)
	if id.ValueString() != "db-1" {
		t.Errorf("a KNOWN value must pass through untouched, id = %q", id.ValueString())
	}
}
