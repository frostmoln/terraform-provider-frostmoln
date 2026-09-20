package postgres_instance

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/objectplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
)

// plannedCreate is the model the FRAMEWORK really produces for a create whose
// configuration omits every optional attribute: each Optional+Computed one is
// UNKNOWN, not null.
//
// The distinction is the reason a whole review round was needed.
// `withExtensionSetDefaults` normalises zero values to NULL, which is the one
// value real Terraform never plans for an omitted Optional+Computed attribute,
// and a suite built on it cannot see what the framework does with unknowns.
func plannedCreate() PostgresInstanceModel {
	m := fullPlanModel()
	m.HAEnabled = types.BoolUnknown()
	m.BackupEnabled = types.BoolUnknown()
	m.BackupSchedule = types.StringUnknown()
	m.BackupRetentionDays = types.Int64Unknown()
	m.PITREnabled = types.BoolUnknown()
	m.Extensions = types.SetNull(types.StringType)
	return m
}

// --- the wire mapping ---

// A database below the P8 release omits every PITR field. Reading any of them
// back as a zero value would be this provider asserting something no service
// said — "point-in-time recovery: off" on an instance the platform has not
// spoken about — and `pitr_enabled = false` in state is what a practitioner
// would then act on.
func TestFromAPIAbsentPITRFieldsAreNullNotFalse(t *testing.T) {
	var m PostgresInstanceModel
	m.fromAPI(context.Background(), &apiPostgresInstance{ID: "db-1", Name: "n"}, &diag.Diagnostics{})

	for name, v := range map[string]attr.Value{
		"pitr_enabled":               m.PITREnabled,
		"pitr_capable":               m.PITRCapable,
		"pitr_archive_paused_reason": m.PITRArchivePausedReason,
		"earliest_restorable_time":   m.EarliestRestorableTime,
		"latest_restorable_time":     m.LatestRestorableTime,
	} {
		if !v.IsNull() {
			t.Errorf("%s = %v, want null: the platform said nothing about it", name, v)
		}
	}
}

func TestFromAPIReadsThePITRWindow(t *testing.T) {
	no, yes := false, true
	var m PostgresInstanceModel
	m.fromAPI(context.Background(), &apiPostgresInstance{
		ID: "db-1", Name: "n",
		PITREnabled:             &no,
		PITRCapable:             &yes,
		PITRArchivePausedReason: "tenant_cap",
		EarliestRestorableTime:  "2026-09-19T08:00:00Z",
		LatestRestorableTime:    "2026-09-20T11:59:31Z",
	}, &diag.Diagnostics{})

	if m.PITREnabled.ValueBool() || m.PITREnabled.IsNull() {
		t.Errorf("pitr_enabled = %v, want a present false", m.PITREnabled)
	}
	if !m.PITRCapable.ValueBool() {
		t.Errorf("pitr_capable = %v, want true", m.PITRCapable)
	}
	if got := m.PITRArchivePausedReason.ValueString(); got != "tenant_cap" {
		t.Errorf("pitr_archive_paused_reason = %q", got)
	}
	if got := m.LatestRestorableTime.ValueString(); got != "2026-09-20T11:59:31Z" {
		t.Errorf("latest_restorable_time = %q — the wire value must be carried through unreformatted", got)
	}
}

// pitrEnabled reaches the create body only from a value the practitioner WROTE.
// An omitted attribute is UNKNOWN in the plan of a create, and inventing a
// value there decides the instance's permanent point-in-time-recovery stamp for
// them.
func TestToCreateRequestSendsPITREnabledOnlyWhenConfigured(t *testing.T) {
	for _, tc := range []struct {
		name  string
		value types.Bool
		want  *bool
	}{
		{"omitted (unknown in the plan)", types.BoolUnknown(), nil},
		{"null", types.BoolNull(), nil},
		{"explicit true", types.BoolValue(true), boolPtr(true)},
		{"explicit false", types.BoolValue(false), boolPtr(false)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := fullPlanModel()
			m.PITREnabled = tc.value
			got := m.toCreateRequest(context.Background(), &diag.Diagnostics{}).PITREnabled
			switch {
			case tc.want == nil && got != nil:
				t.Fatalf("sent pitrEnabled=%t for a value the practitioner did not write", *got)
			case tc.want != nil && got == nil:
				t.Fatal("did not send an explicitly configured pitrEnabled")
			case tc.want != nil && *got != *tc.want:
				t.Fatalf("sent pitrEnabled=%t, want %t", *got, *tc.want)
			}
		})
	}
}

// ValueBool()/ValueInt64() of an unknown or null are the ZERO values, so a diff
// taken without guarding them PUTs `false` and `0` as if the practitioner had
// asked for them. Both are reachable: a restore's create plans its backup
// fields unknown so the target inherits the source's, and a resize plans the
// schedule unknown so the platform can re-pick it.
func TestToUpdateRequestNeverSendsAZeroValueForAnUnknownOrNull(t *testing.T) {
	state := PostgresInstanceModel{
		Name:                types.StringValue("db"),
		BackupEnabled:       types.BoolValue(true),
		BackupSchedule:      types.StringValue("0 2 * * 0"),
		BackupRetentionDays: types.Int64Value(90),
		PITREnabled:         types.BoolValue(true),
		ParameterGroupID:    types.StringNull(),
	}

	t.Run("unknown plan values send nothing", func(t *testing.T) {
		plan := state
		plan.BackupEnabled = types.BoolUnknown()
		plan.BackupSchedule = types.StringUnknown()
		plan.BackupRetentionDays = types.Int64Unknown()
		plan.PITREnabled = types.BoolUnknown()

		req := plan.toUpdateRequest(&state)
		if req.hasChanges() {
			t.Fatalf("an all-unknown plan produced a PUT body: %+v", req)
		}
	})

	t.Run("a null pitr_enabled never disables a live one", func(t *testing.T) {
		plan := state
		plan.PITREnabled = types.BoolNull()
		if got := plan.toUpdateRequest(&state).PITREnabled; got != nil {
			t.Fatalf("sent pitrEnabled=%t against a null plan value — that turns off a customer's recovery", *got)
		}
	})

	t.Run("an explicit change is still sent", func(t *testing.T) {
		plan := state
		plan.PITREnabled = types.BoolValue(false)
		got := plan.toUpdateRequest(&state).PITREnabled
		if got == nil || *got {
			t.Fatalf("an explicit pitr_enabled = false must be sent, got %v", got)
		}
	})
}

// --- the read-back check (O8) ---

func TestCheckPITREnactment(t *testing.T) {
	for _, tc := range []struct {
		name       string
		configured types.Bool
		reported   *bool
		wantFail   bool
	}{
		{"nothing configured, nothing to check", types.BoolNull(), nil, false},
		{"nothing configured, platform says false", types.BoolNull(), boolPtr(false), false},
		{"configured true, applied", types.BoolValue(true), boolPtr(true), false},
		{"configured false, applied", types.BoolValue(false), boolPtr(false), false},
		{"configured true, platform says false", types.BoolValue(true), boolPtr(false), true},
		{"configured true, platform says nothing (pre-P8)", types.BoolValue(true), nil, true},
		{"configured false, platform says true", types.BoolValue(false), boolPtr(true), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var summary, detail string
			calls := 0
			checkPITREnactment(tc.configured, tc.reported, func(s, d string) {
				calls++
				summary, detail = s, d
			})
			if (calls > 0) != tc.wantFail {
				t.Fatalf("reported %d times, want failure=%v", calls, tc.wantFail)
			}
			if !tc.wantFail {
				return
			}
			if summary != "pitr_enabled was not applied" {
				t.Errorf("summary = %q", summary)
			}
			// The diagnostic has to name the cause, or the practitioner has no
			// way to tell a rollout in progress from a bug in their config.
			if !strings.Contains(detail, "older than the release") {
				t.Errorf("the detail must name the old-service cause: %s", detail)
			}
		})
	}
}

// --- restore_from plan behaviour ---

// The two no-op transitions are the whole point: either of them replacing would
// destroy a live database to reconcile bookkeeping.
func TestRestoreSourceChangedFiresOnlyOnARealSwitch(t *testing.T) {
	obj := func(source, pit string) types.Object {
		return types.ObjectValueMust(restoreFromAttrTypes, map[string]attr.Value{
			"source_instance_id": types.StringValue(source),
			"point_in_time":      types.StringValue(pit),
			"backup_id":          types.StringNull(),
		})
	}
	null := types.ObjectNull(restoreFromAttrTypes)

	for _, tc := range []struct {
		name         string
		state, cfg   types.Object
		wantsReplace bool
	}{
		{"block removed from the configuration", obj("db-a", "t1"), null, false},
		{"block added to an existing instance", null, obj("db-a", "t1"), false},
		{"imported: neither side records one", null, null, false},
		{"unchanged", obj("db-a", "t1"), obj("db-a", "t1"), false},
		{"a different source", obj("db-a", "t1"), obj("db-b", "t1"), true},
		{"a different point in time", obj("db-a", "t1"), obj("db-a", "t2"), true},
		{"config not yet known", obj("db-a", "t1"), types.ObjectUnknown(restoreFromAttrTypes), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp := &objectplanmodifier.RequiresReplaceIfFuncResponse{}
			restoreSourceChanged(context.Background(), planmodifier.ObjectRequest{
				StateValue: tc.state, ConfigValue: tc.cfg, PlanValue: tc.cfg,
			}, resp)
			if resp.RequiresReplace != tc.wantsReplace {
				t.Fatalf("RequiresReplace = %v, want %v", resp.RequiresReplace, tc.wantsReplace)
			}
		})
	}
}

// --- the source-shape gate ---

func TestCheckRestoreSourceShapeRefusesWhatCannotConverge(t *testing.T) {
	source := func() *apiPostgresInstance {
		return &apiPostgresInstance{
			ID: "db-src", Type: offerTypePostgreSQL, PostgresVersion: "16",
			FlavorID: "db.gp1.small", StorageGB: 100, VPCID: "vpc-1", SubnetID: "sn-1",
			HAEnabled: false,
		}
	}
	matching := func() *PostgresInstanceModel {
		return &PostgresInstanceModel{
			Version:   types.StringValue("16"),
			FlavorID:  types.StringValue("db.gp1.small"),
			StorageGB: types.Int64Value(100),
			VPCID:     types.StringValue("vpc-1"),
			SubnetID:  types.StringValue("sn-1"),
		}
	}

	t.Run("a matching configuration passes", func(t *testing.T) {
		if d := checkRestoreSourceShape("db-src", source(), PostgresInstanceModel{}, matching()); d.HasError() {
			t.Fatalf("unexpected refusal: %v", d.Errors())
		}
	})

	t.Run("larger storage is allowed, it is grown afterwards", func(t *testing.T) {
		plan := matching()
		plan.StorageGB = types.Int64Value(250)
		if d := checkRestoreSourceShape("db-src", source(), PostgresInstanceModel{}, plan); d.HasError() {
			t.Fatalf("growing storage must be allowed: %v", d.Errors())
		}
	})

	for _, tc := range []struct {
		name    string
		mutate  func(*PostgresInstanceModel)
		cfg     PostgresInstanceModel
		srcHA   bool
		srcType string
		wantIn  string
	}{
		{name: "version", mutate: func(m *PostgresInstanceModel) { m.Version = types.StringValue("17") }, wantIn: "version"},
		{name: "flavor_id", mutate: func(m *PostgresInstanceModel) { m.FlavorID = types.StringValue("db.gp1.large") }, wantIn: "flavor_id"},
		{name: "vpc_id", mutate: func(m *PostgresInstanceModel) { m.VPCID = types.StringValue("vpc-9") }, wantIn: "vpc_id"},
		{name: "subnet_id", mutate: func(m *PostgresInstanceModel) { m.SubnetID = types.StringValue("sn-9") }, wantIn: "subnet_id"},
		{name: "smaller storage", mutate: func(m *PostgresInstanceModel) { m.StorageGB = types.Int64Value(50) }, wantIn: "smaller than the restore source"},
		{name: "a written ha_enabled that disagrees", cfg: PostgresInstanceModel{HAEnabled: types.BoolValue(true)}, wantIn: "ha_enabled"},
		{name: "a mysql source", srcType: "mysql", wantIn: "not a PostgreSQL instance"},
	} {
		t.Run("refuses "+tc.name, func(t *testing.T) {
			plan := matching()
			if tc.mutate != nil {
				tc.mutate(plan)
			}
			src := source()
			if tc.srcType != "" {
				src.Type = tc.srcType
			}
			d := checkRestoreSourceShape("db-src", src, tc.cfg, plan)
			if !d.HasError() {
				t.Fatal("expected a refusal before anything is created")
			}
			var joined strings.Builder
			for _, e := range d.Errors() {
				joined.WriteString(e.Summary() + " " + e.Detail() + "\n")
			}
			if !strings.Contains(joined.String(), tc.wantIn) {
				t.Errorf("the refusal must name %q: %s", tc.wantIn, joined.String())
			}
		})
	}

	// An OMITTED ha_enabled is unknown in the plan and accepts whatever the
	// source's shape produces, so it must not be compared.
	t.Run("an omitted ha_enabled is not compared", func(t *testing.T) {
		src := source()
		src.HAEnabled = true
		if d := checkRestoreSourceShape("db-src", src, PostgresInstanceModel{HAEnabled: types.BoolNull()}, matching()); d.HasError() {
			t.Fatalf("an omitted ha_enabled must accept the source's shape: %v", d.Errors())
		}
	})
}

// --- the ambiguity classifier ---

func TestIsAmbiguousWriteFailure(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want bool
	}{
		{"a transport failure", fmt.Errorf("connection reset"), true},
		{"a 504 from the gateway", &client.APIError{StatusCode: 504}, true},
		{"a 500", &client.APIError{StatusCode: 500}, true},
		{"a 408", &client.APIError{StatusCode: http.StatusRequestTimeout}, true},
		{"a 429", &client.APIError{StatusCode: http.StatusTooManyRequests}, true},
		{"the service refused it", &client.APIError{StatusCode: 400, Code: "pitr_out_of_window"}, false},
		{"the source is gone", &client.APIError{StatusCode: 404, Code: "not_found"}, false},
		{"a conflict", &client.APIError{StatusCode: 409, Code: "conflict"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := isAmbiguousWriteFailure(tc.err); got != tc.want {
				t.Fatalf("isAmbiguousWriteFailure = %v, want %v", got, tc.want)
			}
		})
	}
}

// --- ModifyPlan ---

func TestModifyPlanRePlansWhatThePlatformMoves(t *testing.T) {
	recorded := PostgresInstanceModel{
		ID: types.StringValue("db-1"), Name: types.StringValue("db"),
		Version: types.StringValue("16"), FlavorID: types.StringValue("db.gp1.small"),
		StorageGB: types.Int64Value(100), VPCID: types.StringValue("vpc-1"), SubnetID: types.StringValue("sn-1"),
		HAEnabled: types.BoolValue(false), HAStatus: types.StringValue("disabled"),
		BackupEnabled: types.BoolValue(true), BackupSchedule: types.StringValue("0 2 * * *"),
		BackupRetentionDays: types.Int64Value(35), Status: types.StringValue("running"),
		CreatedAt:               types.StringValue("2026-01-01T00:00:00Z"),
		PITREnabled:             types.BoolValue(true),
		PITRCapable:             types.BoolValue(true),
		PITRArchivePausedReason: types.StringNull(),
		EarliestRestorableTime:  types.StringValue("2026-09-19T08:00:00Z"),
		LatestRestorableTime:    types.StringValue("2026-09-20T11:00:00Z"),
	}

	run := func(t *testing.T, planModel PostgresInstanceModel, cfgModel PostgresInstanceModel) PostgresInstanceModel {
		t.Helper()
		r := NewResource()
		plan := buildPlan(t, planModel)
		state := buildState(t, recorded)
		cfg := tfsdk.Config{Schema: plan.Schema, Raw: buildPlan(t, cfgModel).Raw}

		resp := resource.ModifyPlanResponse{Plan: plan}
		r.(*postgresInstanceResource).ModifyPlan(context.Background(),
			resource.ModifyPlanRequest{Plan: plan, State: state, Config: cfg}, &resp)
		if resp.Diagnostics.HasError() {
			t.Fatalf("ModifyPlan: %v", resp.Diagnostics.Errors())
		}
		var out PostgresInstanceModel
		if d := resp.Plan.Get(context.Background(), &out); d.HasError() {
			t.Fatalf("plan.Get: %v", d.Errors())
		}
		return out
	}

	// A no-op plan runs no apply, so nothing can come back different. Marking
	// these unknown here would show a diff on every `terraform plan`.
	t.Run("an unchanged plan is left alone", func(t *testing.T) {
		out := run(t, recorded, PostgresInstanceModel{})
		if out.LatestRestorableTime.IsUnknown() || out.PITRCapable.IsUnknown() {
			t.Fatal("an unchanged plan must not be re-planned — it would invent a permanent diff")
		}
	})

	// The one that matters: latest_restorable_time advances with every archived
	// segment, so planning its prior value against an apply that reads a newer
	// one is "inconsistent result after apply" — a failure on the clock alone.
	t.Run("a changing plan re-plans the platform-moved attributes", func(t *testing.T) {
		planModel := recorded
		planModel.Name = types.StringValue("renamed")
		out := run(t, planModel, PostgresInstanceModel{Name: types.StringValue("renamed")})

		for name, v := range map[string]attr.Value{
			"pitr_capable":               out.PITRCapable,
			"pitr_archive_paused_reason": out.PITRArchivePausedReason,
			"earliest_restorable_time":   out.EarliestRestorableTime,
			"latest_restorable_time":     out.LatestRestorableTime,
		} {
			if !v.IsUnknown() {
				t.Errorf("%s = %v, want unknown: the platform moves it under the apply", name, v)
			}
		}
		// The schedule is NOT one of them: only a resize can make the platform
		// re-pick it, and an omitted attribute must otherwise keep the
		// customer's recorded schedule.
		if out.BackupSchedule.IsUnknown() {
			t.Error("backup_schedule must stay pinned when the size is not moving")
		}
	})

	t.Run("a resize re-plans an unwritten backup_schedule", func(t *testing.T) {
		planModel := recorded
		planModel.StorageGB = types.Int64Value(2000)
		out := run(t, planModel, PostgresInstanceModel{StorageGB: types.Int64Value(2000)})
		if !out.BackupSchedule.IsUnknown() {
			t.Fatalf("backup_schedule = %v, want unknown: the platform's minimum interval grows with the volume and it re-picks the schedule it chose for the old size", out.BackupSchedule)
		}
	})

	t.Run("a resize leaves a WRITTEN backup_schedule alone", func(t *testing.T) {
		planModel := recorded
		planModel.StorageGB = types.Int64Value(2000)
		planModel.BackupSchedule = types.StringValue("0 3 * * 0")
		out := run(t, planModel, PostgresInstanceModel{
			StorageGB:      types.Int64Value(2000),
			BackupSchedule: types.StringValue("0 3 * * 0"),
		})
		if out.BackupSchedule.ValueString() != "0 3 * * 0" {
			t.Fatalf("backup_schedule = %v — a schedule the customer wrote is never re-planned", out.BackupSchedule)
		}
	})
}

// A restore INHERITS the source's backup settings. backup_retention_days plans
// its documented 35-day default on a create, so without this the apply would
// PUT 35 over a source retaining 90 — and the retention reaper would then be
// free to delete everything older than 35 days on its next tick.
func TestModifyPlanLetsARestoreInheritTheSourcesBackupPolicy(t *testing.T) {
	r := NewResource()

	planModel := fullPlanModel()
	planModel.BackupRetentionDays = types.Int64Value(defaultBackupRetentionDays)
	planModel.RestoreFrom = types.ObjectValueMust(restoreFromAttrTypes, map[string]attr.Value{
		"source_instance_id": types.StringValue("db-src"),
		"point_in_time":      types.StringValue("2026-09-20T10:00:00Z"),
		"backup_id":          types.StringNull(),
	})
	plan := buildPlan(t, planModel)

	cfgModel := fullPlanModel()
	cfgModel.RestoreFrom = planModel.RestoreFrom
	cfg := tfsdk.Config{Schema: plan.Schema, Raw: buildPlan(t, cfgModel).Raw}

	resp := resource.ModifyPlanResponse{Plan: plan}
	r.(*postgresInstanceResource).ModifyPlan(context.Background(),
		resource.ModifyPlanRequest{Plan: plan, State: emptyState(t), Config: cfg}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("ModifyPlan: %v", resp.Diagnostics.Errors())
	}

	var out PostgresInstanceModel
	if d := resp.Plan.Get(context.Background(), &out); d.HasError() {
		t.Fatalf("plan.Get: %v", d.Errors())
	}
	if !out.BackupRetentionDays.IsUnknown() {
		t.Errorf("backup_retention_days = %v, want unknown so the restore inherits the source's retention instead of cutting it to the floor", out.BackupRetentionDays)
	}
	if !out.BackupSchedule.IsUnknown() {
		t.Errorf("backup_schedule = %v, want unknown so the restore inherits the source's schedule", out.BackupSchedule)
	}
}

// --- create from restore ---

type restoreServer struct {
	*httptest.Server
	restorePosts atomic.Int32
	puts         atomic.Int32
	resizes      atomic.Int32
}

// newRestoreServer serves the source, the restore POST and the target. `hook`
// may intercept any request; returning true means it answered.
func newRestoreServer(t *testing.T, target map[string]any, hook func(http.ResponseWriter, *http.Request) bool) *restoreServer {
	t.Helper()
	rs := &restoreServer{}
	source := map[string]any{
		"id": "db-src", "name": "src", "type": "postgresql", "typeVersion": "16",
		"flavorId": "db.gp1.small", "storageGb": 50, "vpcId": "vpc-1", "subnetId": "sn-1",
		"status": "running", "createdAt": "2026-01-01T00:00:00Z",
		"backupEnabled": true, "backupSchedule": "0 2 * * *", "backupRetentionDays": 90,
		"pitrEnabled": true, "pitrCapable": true,
	}

	rs.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hook != nil && hook(w, r) {
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/databases/db-src/restore") && r.Method == http.MethodPost:
			rs.restorePosts.Add(1)
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(target)
		case strings.HasSuffix(r.URL.Path, "/resize"):
			rs.resizes.Add(1)
			target["storageGb"] = 120
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "resizing"})
		case strings.HasSuffix(r.URL.Path, "/databases/db-src"):
			_ = json.NewEncoder(w).Encode(source)
		case strings.HasSuffix(r.URL.Path, "/databases/db-tgt"):
			if r.Method == http.MethodPut {
				rs.puts.Add(1)
			}
			_ = json.NewEncoder(w).Encode(target)
		case strings.HasSuffix(r.URL.Path, "/extensions"):
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]string{"code": "not_found", "message": "no extensions route"})
		case strings.HasSuffix(r.URL.Path, "/databases"):
			_ = json.NewEncoder(w).Encode(map[string]any{"instances": []any{target}, "totalCount": 1})
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusTeapot)
		}
	}))
	return rs
}

func restorePlanModel(t *testing.T, storageGB int64) PostgresInstanceModel {
	t.Helper()
	m := plannedCreate()
	m.Name = types.StringValue("tgt")
	m.StorageGB = types.Int64Value(storageGB)
	// WRITTEN, and different from what the target inherits from the source
	// (90), so the converge step has a real in-place update to make.
	m.BackupRetentionDays = types.Int64Value(60)
	m.RestoreFrom = types.ObjectValueMust(restoreFromAttrTypes, map[string]attr.Value{
		"source_instance_id": types.StringValue("db-src"),
		"point_in_time":      types.StringValue("2026-09-20T10:00:00Z"),
		"backup_id":          types.StringNull(),
	})
	return m
}

func runRestoreCreate(t *testing.T, rs *restoreServer, m PostgresInstanceModel) resource.CreateResponse {
	t.Helper()
	r := newResource(newClient(t, rs.Server))
	createResp := resource.CreateResponse{State: emptyState(t)}
	r.Create(context.Background(), createRequest(buildPlan(t, m)), &createResp)
	return createResp
}

func TestCreateFromRestoreWritesStateAndConvergesInPlace(t *testing.T) {
	target := map[string]any{
		"id": "db-tgt", "name": "tgt", "type": "postgresql", "typeVersion": "16",
		"flavorId": "db.gp1.small", "storageGb": 50, "vpcId": "vpc-1", "subnetId": "sn-1",
		"status": "running", "createdAt": justNow(),
		"backupEnabled": true, "backupSchedule": "0 2 * * *", "backupRetentionDays": 90,
		"pitrEnabled": true, "pitrCapable": true,
	}
	rs := newRestoreServer(t, target, nil)
	defer rs.Close()

	// 120 GB against the source's 50: the restore starts at the source's size
	// and the difference is grown in place afterwards.
	createResp := runRestoreCreate(t, rs, restorePlanModel(t, 120))
	if createResp.Diagnostics.HasError() {
		t.Fatalf("restore create: %v", createResp.Diagnostics.Errors())
	}
	if got := rs.restorePosts.Load(); got != 1 {
		t.Errorf("restore POSTs = %d, want exactly 1", got)
	}
	if got := rs.resizes.Load(); got != 1 {
		t.Errorf("resizes = %d, want 1 (120 GB against the source's 50)", got)
	}

	var state PostgresInstanceModel
	if d := createResp.State.Get(context.Background(), &state); d.HasError() {
		t.Fatalf("state.Get: %v", d.Errors())
	}
	if state.ID.ValueString() != "db-tgt" {
		t.Errorf("state id = %q, want the restored target", state.ID.ValueString())
	}
	// The platform does not report a restore, so state carries what Terraform
	// asked for — which is what makes removing the block later a no-op.
	if state.RestoreFrom.IsNull() {
		t.Error("restore_from must be recorded in state, or removing the block would plan a change")
	}
	if !state.PITREnabled.ValueBool() {
		t.Error("the restored target's pitr_enabled must be read back from the GET")
	}
}

// 🔴 THE TAINT GUARD. Once the restore POST has been accepted, a database
// holding the customer's recovered data exists. An ERROR returned from Create
// leaves Terraform holding a tainted resource, and the next apply destroys and
// re-creates it — losing exactly the data the restore recovered. Everything
// after the POST must therefore be a warning.
func TestCreateFromRestoreNeverErrorsOnceTheDatabaseExists(t *testing.T) {
	for _, tc := range []struct {
		name string
		hook func(http.ResponseWriter, *http.Request) bool
	}{
		{
			name: "the in-place update is refused",
			hook: func(w http.ResponseWriter, r *http.Request) bool {
				if r.Method != http.MethodPut {
					return false
				}
				w.WriteHeader(http.StatusConflict)
				_ = json.NewEncoder(w).Encode(map[string]string{"code": "conflict", "message": "a backup is in progress"})
				return true
			},
		},
		{
			name: "the resize is refused",
			hook: func(w http.ResponseWriter, r *http.Request) bool {
				if !strings.HasSuffix(r.URL.Path, "/resize") {
					return false
				}
				w.WriteHeader(http.StatusBadRequest)
				_ = json.NewEncoder(w).Encode(map[string]string{"code": "invalid_input", "message": "no"})
				return true
			},
		},
		{
			name: "the target never reaches running",
			hook: func(w http.ResponseWriter, r *http.Request) bool {
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
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			target := map[string]any{
				"id": "db-tgt", "name": "tgt", "type": "postgresql", "typeVersion": "16",
				"flavorId": "db.gp1.small", "storageGb": 50, "vpcId": "vpc-1", "subnetId": "sn-1",
				"status": "running", "createdAt": justNow(),
				"backupEnabled": true, "backupSchedule": "0 2 * * *", "backupRetentionDays": 90,
				"pitrEnabled": true, "pitrCapable": true,
			}
			rs := newRestoreServer(t, target, tc.hook)
			defer rs.Close()

			createResp := runRestoreCreate(t, rs, restorePlanModel(t, 120))

			if createResp.Diagnostics.HasError() {
				t.Fatalf("Create ERRORED after the restore landed — Terraform taints the resource and the "+
					"next apply destroys the restored database: %v", createResp.Diagnostics.Errors())
			}
			if createResp.Diagnostics.WarningsCount() == 0 {
				t.Fatal("the failure must still be reported, as a warning")
			}
			var state PostgresInstanceModel
			if d := createResp.State.Get(context.Background(), &state); d.HasError() {
				t.Fatalf("state.Get: %v", d.Errors())
			}
			if state.ID.ValueString() != "db-tgt" {
				t.Fatalf("the restored database must stay tracked, state id = %q", state.ID.ValueString())
			}
		})
	}
}

// An ambiguous failure is not a verdict: the restore may have been accepted
// while the answer was lost. A blind retry would be a SECOND billable database
// and a second full backup into locked storage, one of them untracked forever.
func TestCreateFromRestoreNeverRePostsAfterAnAmbiguousFailure(t *testing.T) {
	t.Run("the target exists: adopt it, do not restore again", func(t *testing.T) {
		target := map[string]any{
			"id": "db-tgt", "name": "tgt", "type": "postgresql", "typeVersion": "16",
			"flavorId": "db.gp1.small", "storageGb": 50, "vpcId": "vpc-1", "subnetId": "sn-1",
			"status": "running", "createdAt": justNow(),
			"backupEnabled": true, "backupSchedule": "0 2 * * *", "backupRetentionDays": 90,
			"pitrEnabled": true, "pitrCapable": true,
		}
		var posts atomic.Int32
		rs := newRestoreServer(t, target, func(w http.ResponseWriter, r *http.Request) bool {
			if r.Method != http.MethodPost || !strings.HasSuffix(r.URL.Path, "/restore") {
				return false
			}
			posts.Add(1)
			// The write landed; the ANSWER was lost.
			w.WriteHeader(http.StatusGatewayTimeout)
			_ = json.NewEncoder(w).Encode(map[string]string{"code": "gateway_timeout", "message": "upstream timed out"})
			return true
		})
		defer rs.Close()

		createResp := runRestoreCreate(t, rs, restorePlanModel(t, 50))
		if createResp.Diagnostics.HasError() {
			t.Fatalf("the target was found by name and must be adopted: %v", createResp.Diagnostics.Errors())
		}
		if got := posts.Load(); got != 1 {
			t.Fatalf("restore POSTs = %d, want exactly 1 — a retry is a second billable database", got)
		}
		var state PostgresInstanceModel
		if d := createResp.State.Get(context.Background(), &state); d.HasError() {
			t.Fatalf("state.Get: %v", d.Errors())
		}
		if state.ID.ValueString() != "db-tgt" {
			t.Fatalf("the adopted target must be in state, got %q", state.ID.ValueString())
		}
	})

	t.Run("nothing was created: say so, and say applying again is safe", func(t *testing.T) {
		rs := newRestoreServer(t, map[string]any{}, func(w http.ResponseWriter, r *http.Request) bool {
			switch {
			case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/restore"):
				w.WriteHeader(http.StatusBadGateway)
				_ = json.NewEncoder(w).Encode(map[string]string{"code": "bad_gateway", "message": "no upstream"})
				return true
			case strings.HasSuffix(r.URL.Path, "/databases") && r.Method == http.MethodGet:
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(map[string]any{"instances": []any{}, "totalCount": 0})
				return true
			}
			return false
		})
		defer rs.Close()

		createResp := runRestoreCreate(t, rs, restorePlanModel(t, 50))
		if !createResp.Diagnostics.HasError() {
			t.Fatal("an unaccepted restore must fail the create")
		}
		joined := diagText(createResp.Diagnostics.Errors())
		if !strings.Contains(joined, "Applying again is safe") {
			t.Errorf("the diagnostic must say the retry is safe: %s", joined)
		}
	})
}

// The source is read FIRST and a 404 is fatal: silently creating an empty
// database where a restore was asked for is the one wrong answer.
func TestCreateFromRestoreRefusesAMissingSourceWithoutCreatingAnything(t *testing.T) {
	var created atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			created.Add(1)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]string{"code": "not_found", "message": "no such instance"})
	}))
	defer server.Close()

	r := newResource(newClient(t, server))
	createResp := resource.CreateResponse{State: emptyState(t)}
	r.Create(context.Background(), createRequest(buildPlan(t, restorePlanModel(t, 50))), &createResp)

	if !createResp.Diagnostics.HasError() {
		t.Fatal("a missing restore source must fail the create")
	}
	if got := created.Load(); got != 0 {
		t.Fatalf("%d writes were made after the source could not be read — a restore must never fall back to a plain create", got)
	}
	if joined := diagText(createResp.Diagnostics.Errors()); !strings.Contains(joined, "does not exist") {
		t.Errorf("the diagnostic must say the source is missing: %s", joined)
	}
}

// --- schema shape ---

func TestPITRSchemaShape(t *testing.T) {
	ctx := context.Background()
	var resp resource.SchemaResponse
	NewResource().Schema(ctx, resource.SchemaRequest{}, &resp)

	enabled, ok := resp.Schema.Attributes["pitr_enabled"].(interface {
		IsOptional() bool
		IsComputed() bool
	})
	if !ok || !enabled.IsOptional() || !enabled.IsComputed() {
		t.Error("pitr_enabled must be Optional+Computed: writeable, and read back from the platform")
	}

	// NONE of these may pin state: the platform moves them under the customer.
	for _, name := range platformRecomputedAttributes {
		a, ok := resp.Schema.Attributes[name]
		if !ok {
			t.Fatalf("%s is missing from the schema", name)
		}
		if a.IsOptional() || a.IsRequired() {
			t.Errorf("%s must be Computed-only — nothing a configuration writes", name)
		}
	}
	if len(resp.Schema.Attributes) == 0 {
		t.Fatal("empty schema")
	}
}

// --- helpers ---

func boolPtr(b bool) *bool { return &b }

// justNow is a createdAt the restore-target lookup will accept as new: the
// adoption bound refuses any candidate that existed before the POST was sent.
func justNow() string { return time.Now().UTC().Format(time.RFC3339) }

func diagText(ds []diag.Diagnostic) string {
	var b strings.Builder
	for _, d := range ds {
		b.WriteString(d.Summary())
		b.WriteString(" ")
		b.WriteString(d.Detail())
		b.WriteString("\n")
	}
	return b.String()
}

// An id that does not stay ONE path segment must be refused before it reaches a
// request. url.PathEscape leaves "." and ".." intact and the client joins with
// path.Join, which cleans — so ".." addresses a different resource and "" the
// LIST endpoint.
func TestRestoreSourceIDMustBeOnePathSegment(t *testing.T) {
	for _, id := range []string{"", ".", "..", "a/b", "a/..", `a\b`, "a?b", "a#b", "a%2fb"} {
		if err := validSourceInstanceID(id); err == nil {
			t.Errorf("the source instance id %q must be refused", id)
		}
	}
	if err := validSourceInstanceID("db-abc123"); err != nil {
		t.Errorf("an ordinary instance id must be accepted: %v", err)
	}

	// The plan-time validator carries the same rule.
	resp := &validator.StringResponse{}
	sourceInstanceIDValidator{}.ValidateString(context.Background(), validator.StringRequest{
		Path:        path.Root("restore_from").AtName("source_instance_id"),
		ConfigValue: types.StringValue(".."),
	}, resp)
	if !resp.Diagnostics.HasError() {
		t.Error("the plan-time validator must refuse \"..\"")
	}
}

// And Create must not trust that the validator ran: a value that resolves only
// at apply reaches here unvalidated, and this id goes into two request paths.
func TestCreateFromRestoreRefusesAnUnusableSourceIDBeforeAnyRequest(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	m := restorePlanModel(t, 50)
	m.RestoreFrom = types.ObjectValueMust(restoreFromAttrTypes, map[string]attr.Value{
		"source_instance_id": types.StringValue(".."),
		"point_in_time":      types.StringValue("2026-09-20T10:00:00Z"),
		"backup_id":          types.StringNull(),
	})

	r := newResource(newClient(t, server))
	createResp := resource.CreateResponse{State: emptyState(t)}
	r.Create(context.Background(), createRequest(buildPlan(t, m)), &createResp)

	if !createResp.Diagnostics.HasError() {
		t.Fatal("an unusable source id must fail the create")
	}
	if got := requests.Load(); got != 0 {
		t.Fatalf("%d requests were made with an unusable id in the path", got)
	}
}

// 🔴 EVERY value must be known after an apply, and restore_from is the attribute
// that nearly broke that for the whole resource.
//
// It was Optional+COMPUTED in the first draft. MarkComputedNilsAsUnknown keys
// only on the CONFIG value, so an omitted block planned UNKNOWN on every create
// and every changing update; UseStateForUnknown bails on a null prior value, so
// it rescued nothing; and nothing downstream could settle it, because the
// platform never reports how an instance came to exist and `fromAPI` therefore
// has nothing to write. Terraform refused the result — "All values must be
// known after apply" — AFTER the database existed, which tainted it, so the
// next apply destroyed it. Every ordinary create.
//
// The fix was to drop Computed, so this guards BOTH halves: the schema shape
// that makes the unknown impossible, and the invariant itself.
func TestApplyLeavesNoUnknownInState(t *testing.T) {
	t.Run("restore_from must never be Computed again", func(t *testing.T) {
		var resp resource.SchemaResponse
		NewResource().Schema(context.Background(), resource.SchemaRequest{}, &resp)
		a, ok := resp.Schema.Attributes["restore_from"]
		if !ok {
			t.Fatal("restore_from is missing from the schema")
		}
		if !a.IsOptional() {
			t.Error("restore_from must be Optional")
		}
		if a.IsComputed() {
			t.Fatal("restore_from must NOT be Computed: a Computed attribute with a null config is " +
				"planned unknown, and nothing can settle this one — the platform does not report " +
				"how an instance came to exist. Every ordinary create then fails after the " +
				"database already exists.")
		}
	})

	instance := map[string]any{
		"id": "db-1", "name": "test-pg", "type": "postgresql", "typeVersion": "16",
		"flavorId": "db.gp1.small", "storageGb": 50, "vpcId": "vpc-1", "subnetId": "sn-1",
		"status": "running", "createdAt": "2026-01-01T00:00:00Z",
		"backupEnabled": true, "backupSchedule": "0 2 * * *", "backupRetentionDays": 35,
		// A P8-or-later service always sends both booleans.
		"pitrEnabled": false, "pitrCapable": false,
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/extensions") {
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]string{"code": "not_found", "message": "no extensions route"})
			return
		}
		_ = json.NewEncoder(w).Encode(instance)
	}))
	defer server.Close()

	t.Run("create", func(t *testing.T) {
		r := newResource(newClient(t, server))
		createResp := resource.CreateResponse{State: emptyState(t)}
		r.Create(context.Background(), createRequest(buildPlan(t, plannedCreate())), &createResp)
		if createResp.Diagnostics.HasError() {
			t.Fatalf("create: %v", createResp.Diagnostics.Errors())
		}
		if !createResp.State.Raw.IsFullyKnown() {
			t.Fatalf("state carries an unknown after apply — Terraform refuses the result and taints the instance: %s",
				createResp.State.Raw.String())
		}
	})

	t.Run("update of an instance that was never restored", func(t *testing.T) {
		recorded := plannedCreate()
		recorded.ID = types.StringValue("db-1")
		recorded.Status = types.StringValue("running")
		recorded.CreatedAt = types.StringValue("2026-01-01T00:00:00Z")
		recorded.HAStatus = types.StringValue("disabled")
		recorded.HAEnabled = types.BoolValue(false)
		recorded.BackupEnabled = types.BoolValue(true)
		recorded.BackupSchedule = types.StringValue("0 2 * * *")
		recorded.BackupRetentionDays = types.Int64Value(35)
		recorded.PITREnabled = types.BoolValue(false)
		recorded.RestoreFrom = types.ObjectNull(restoreFromAttrTypes)

		plan := recorded
		plan.Name = types.StringValue("renamed")

		r := newResource(newClient(t, server))
		updateResp := resource.UpdateResponse{State: buildState(t, recorded)}
		r.Update(context.Background(), updateRequest(buildPlan(t, plan), buildState(t, recorded)), &updateResp)
		if updateResp.Diagnostics.HasError() {
			t.Fatalf("update: %v", updateResp.Diagnostics.Errors())
		}
		if !updateResp.State.Raw.IsFullyKnown() {
			t.Fatalf("state carries an unknown after apply: %s", updateResp.State.Raw.String())
		}
	})
}

// The re-enable cooldown is the ONE refusal on this surface whose message does
// not carry what the practitioner needs: it says "within 24 hours of turning it
// off" and never says of when. The instant lives in details.retryAfter alone.
func TestWithRetryAfterSurfacesTheCooldownInstant(t *testing.T) {
	cooldown := &client.APIError{
		Code:       "conflict",
		Message:    "point-in-time recovery (or backups) cannot be turned back on within 24 hours of turning it off",
		StatusCode: 409,
		Details:    map[string]any{"reason": "pitr_cooldown", "retryAfter": "2026-09-21T09:15:00Z"},
	}
	got := withRetryAfter(cooldown)
	if !strings.Contains(got, "2026-09-21T09:15:00Z") {
		t.Errorf("the retry instant must reach the practitioner: %s", got)
	}
	if !strings.Contains(got, "within 24 hours") {
		t.Errorf("the platform's own sentence must survive: %s", got)
	}

	// Every other refusal is left exactly as the platform worded it — this
	// provider renders no details, because for those the message already says
	// it.
	plain := &client.APIError{
		Code:       "pitr_out_of_window",
		Message:    "pitrTimestamp must be between 2026-09-19T08:00:00.000000Z and 2026-09-20T11:59:31.000000Z",
		StatusCode: 400,
		Details:    map[string]any{"earliest": "2026-09-19T08:00:00.000000Z", "latest": "2026-09-20T11:59:31.000000Z"},
	}
	if got := withRetryAfter(plain); got != plain.Error() {
		t.Errorf("a refusal with no retryAfter must pass through untouched, got %s", got)
	}
	if got := withRetryAfter(fmt.Errorf("connection reset")); got != "connection reset" {
		t.Errorf("a non-API error must pass through untouched, got %s", got)
	}
}

// 🔴 THE SECOND HALF OF THE TAINT GUARD, and the one a review round caught
// after the first fix: reporting a warning is not enough on its own.
//
// Terraform core compares the planned object with the applied one and raises
// "Provider produced inconsistent result after apply" as an ERROR for any known
// planned value that came back different. An errored create is TAINTED, so the
// next apply destroys the restored database — the warning changes nothing. Every
// give-up path on the restore must therefore record what the PLAN promised and
// take the platform's answer only for what the plan left unknown.
//
// The bug this pins was a baseline mistake, not a missing call: the model had
// already been refreshed from the target before the give-up path read it, so it
// dutifully preserved the platform's values instead of the practitioner's.
func TestRestoreGiveUpPathsRecordWhatThePlanPromised(t *testing.T) {
	for _, tc := range []struct {
		name string
		hook func(http.ResponseWriter, *http.Request) bool
	}{
		{
			name: "the target never reaches running",
			hook: func(w http.ResponseWriter, r *http.Request) bool {
				if !strings.HasSuffix(r.URL.Path, "/databases/db-tgt") || r.Method != http.MethodGet {
					return false
				}
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(map[string]any{
					"id": "db-tgt", "name": "tgt", "type": "postgresql", "typeVersion": "16",
					"flavorId": "db.gp1.small", "storageGb": 50, "vpcId": "vpc-1", "subnetId": "sn-1",
					"status": "error", "createdAt": justNow(),
					"backupEnabled": true, "backupRetentionDays": 90,
					"pitrEnabled": true, "pitrCapable": true,
				})
				return true
			},
		},
		{
			name: "the converging resize fails",
			hook: func(w http.ResponseWriter, r *http.Request) bool {
				if !strings.HasSuffix(r.URL.Path, "/resize") {
					return false
				}
				w.WriteHeader(http.StatusInternalServerError)
				_ = json.NewEncoder(w).Encode(map[string]string{"code": "internal_error", "message": "nope"})
				return true
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			target := map[string]any{
				"id": "db-tgt", "name": "tgt", "type": "postgresql", "typeVersion": "16",
				"flavorId": "db.gp1.small", "storageGb": 50, "vpcId": "vpc-1", "subnetId": "sn-1",
				"status": "running", "createdAt": justNow(),
				"backupEnabled": true, "backupSchedule": "0 2 * * *", "backupRetentionDays": 90,
				"pitrEnabled": true, "pitrCapable": true,
			}
			rs := newRestoreServer(t, target, tc.hook)
			defer rs.Close()

			// The source has 50 GB and 90 days; the configuration WRITES 200 GB
			// and 60 days. Both are known planned values, so both must survive.
			m := restorePlanModel(t, 200)
			m.PITREnabled = types.BoolValue(true)

			createResp := runRestoreCreate(t, rs, m)
			if createResp.Diagnostics.HasError() {
				t.Fatalf("a give-up path must not ERROR — that taints the restored database: %v",
					createResp.Diagnostics.Errors())
			}
			if createResp.Diagnostics.WarningsCount() == 0 {
				t.Fatal("the failure must still be reported, as a warning")
			}

			var state PostgresInstanceModel
			if d := createResp.State.Get(context.Background(), &state); d.HasError() {
				t.Fatalf("state.Get: %v", d.Errors())
			}
			if got := state.StorageGB.ValueInt64(); got != 200 {
				t.Errorf("storage_gb recorded as %d, planned 200 — core reads that as an inconsistent result and taints the instance", got)
			}
			if got := state.BackupRetentionDays.ValueInt64(); got != 60 {
				t.Errorf("backup_retention_days recorded as %d, planned 60", got)
			}
			if !state.PITREnabled.ValueBool() {
				t.Errorf("pitr_enabled recorded as %v, planned true", state.PITREnabled)
			}
			// What the plan left UNKNOWN is still the platform's to fill.
			if state.ID.ValueString() != "db-tgt" {
				t.Errorf("id = %q, want the platform's", state.ID.ValueString())
			}
		})
	}
}
