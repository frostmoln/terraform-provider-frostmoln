package mysql_instance

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

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
)

// --- Model unit tests ---

func TestMysqlInstanceModelToCreateRequest(t *testing.T) {
	ctx := context.Background()
	diags := diag.Diagnostics{}

	model := MysqlInstanceModel{
		Name:      types.StringValue("my-mysql"),
		Version:   types.StringValue("8.4"),
		FlavorID:  types.StringValue("db.small"),
		StorageGB: types.Int64Value(50),
		VPCID:     types.StringValue("vpc-123"),
		SubnetID:  types.StringValue("subnet-456"),
		HAEnabled: types.BoolNull(),
	}

	req := model.toCreateRequest(ctx, &diags)
	if diags.HasError() {
		t.Fatalf("unexpected diagnostics: %v", diags.Errors())
	}

	if req.Name != "my-mysql" {
		t.Errorf("expected name my-mysql, got %s", req.Name)
	}
	if req.Engine != "mysql" {
		t.Errorf("expected engine mysql, got %s", req.Engine)
	}
	if req.EngineVersion != "8.4" {
		t.Errorf("expected engineVersion 8.4, got %s", req.EngineVersion)
	}
	if req.FlavorID != "db.small" {
		t.Errorf("expected flavor db.small, got %s", req.FlavorID)
	}
	if req.StorageGB != 50 {
		t.Errorf("expected storageGb 50, got %d", req.StorageGB)
	}
	if req.VPCID != "vpc-123" {
		t.Errorf("expected vpcId vpc-123, got %s", req.VPCID)
	}
	if req.SubnetID != "subnet-456" {
		t.Errorf("expected subnetId subnet-456, got %s", req.SubnetID)
	}
	if req.HAEnabled != nil {
		t.Error("expected nil haEnabled for null value")
	}
}

func TestMysqlInstanceModelToCreateRequestWithOptionals(t *testing.T) {
	ctx := context.Background()
	diags := diag.Diagnostics{}

	model := MysqlInstanceModel{
		Name:                types.StringValue("my-mysql"),
		Version:             types.StringValue("9.2"),
		FlavorID:            types.StringValue("db.medium"),
		StorageGB:           types.Int64Value(100),
		VPCID:               types.StringValue("vpc-123"),
		SubnetID:            types.StringValue("subnet-456"),
		HAEnabled:           types.BoolValue(true),
		BackupEnabled:       types.BoolValue(true),
		BackupSchedule:      types.StringValue("0 3 * * *"),
		BackupRetentionDays: types.Int64Value(14),
		ParameterGroupID:    types.StringValue("pg-789"),
	}

	req := model.toCreateRequest(ctx, &diags)
	if diags.HasError() {
		t.Fatalf("unexpected diagnostics: %v", diags.Errors())
	}

	if req.Engine != "mysql" {
		t.Errorf("expected engine mysql, got %s", req.Engine)
	}
	if req.HAEnabled == nil || !*req.HAEnabled {
		t.Error("expected haEnabled true")
	}
	if req.BackupEnabled == nil || !*req.BackupEnabled {
		t.Error("expected backupEnabled true")
	}
	if req.BackupSchedule != "0 3 * * *" {
		t.Errorf("expected backup schedule '0 3 * * *', got %s", req.BackupSchedule)
	}
	if req.BackupRetentionDays == nil || *req.BackupRetentionDays != 14 {
		t.Error("expected backupRetentionDays 14")
	}
	if req.ParameterGroupID != "pg-789" {
		t.Errorf("expected parameterGroupId pg-789, got %s", req.ParameterGroupID)
	}
}

func TestMysqlInstanceModelToUpdateRequest(t *testing.T) {
	plan := MysqlInstanceModel{
		Name:      types.StringValue("new-name"),
		FlavorID:  types.StringValue("db.large"),
		StorageGB: types.Int64Value(200),
	}
	state := MysqlInstanceModel{
		Name:      types.StringValue("old-name"),
		FlavorID:  types.StringValue("db.small"),
		StorageGB: types.Int64Value(100),
	}

	req := plan.toUpdateRequest(&state)
	if req.Name == nil || *req.Name != "new-name" {
		t.Error("expected name update to new-name")
	}
	if !req.hasChanges() {
		t.Error("expected hasChanges to be true")
	}
	// flavor_id and storage_gb are not part of the PUT update request: storage
	// grows via POST /resize and flavor changes are rejected at plan time.
}

func TestMysqlInstanceModelToUpdateRequestNoChanges(t *testing.T) {
	same := MysqlInstanceModel{
		Name:      types.StringValue("same"),
		FlavorID:  types.StringValue("db.small"),
		StorageGB: types.Int64Value(50),
	}

	req := same.toUpdateRequest(&same)
	if req.Name != nil {
		t.Error("expected no changes in update request")
	}
	if req.hasChanges() {
		t.Error("expected hasChanges to be false when nothing changed")
	}
}

func TestMysqlInstanceModelFromAPI(t *testing.T) {
	ctx := context.Background()
	diags := diag.Diagnostics{}

	api := &apiMysqlInstance{
		ID:            "db-123",
		Name:          "my-mysql",
		Engine:        "mysql",
		EngineVersion: "8.4",
		FlavorID:      "db.small",
		StorageGB:     50,
		VPCID:         "vpc-123",
		SubnetID:      "subnet-456",
		HAEnabled:     false,
		BackupEnabled: true,
		Status:        "running",
		PrivateIP:     "10.0.1.5",
		Port:          3306,
		AdminUsername: "mysqladmin",
		CreatedAt:     "2025-01-01T00:00:00Z",
		UpdatedAt:     "2025-01-02T00:00:00Z",
		TenantID:      "tenant-abc",
	}

	var model MysqlInstanceModel
	model.fromAPI(ctx, api, &diags)
	if diags.HasError() {
		t.Fatalf("unexpected diagnostics: %v", diags.Errors())
	}

	if model.ID.ValueString() != "db-123" {
		t.Errorf("expected ID db-123, got %s", model.ID.ValueString())
	}
	if model.Version.ValueString() != "8.4" {
		t.Errorf("expected version 8.4, got %s", model.Version.ValueString())
	}
	if model.Port.ValueInt64() != 3306 {
		t.Errorf("expected port 3306, got %d", model.Port.ValueInt64())
	}
	if model.AdminUsername.ValueString() != "mysqladmin" {
		t.Errorf("expected admin_username mysqladmin, got %s", model.AdminUsername.ValueString())
	}
	if model.Status.ValueString() != "running" {
		t.Errorf("expected status running, got %s", model.Status.ValueString())
	}
}

func TestMysqlInstanceModelFromAPINulls(t *testing.T) {
	ctx := context.Background()
	diags := diag.Diagnostics{}

	api := &apiMysqlInstance{
		ID:            "db-123",
		Name:          "my-mysql",
		Engine:        "mysql",
		EngineVersion: "8.0",
		FlavorID:      "db.small",
		StorageGB:     50,
		VPCID:         "vpc-123",
		SubnetID:      "subnet-456",
		Status:        "provisioning",
		CreatedAt:     "2025-01-01T00:00:00Z",
	}

	var model MysqlInstanceModel
	model.fromAPI(ctx, api, &diags)
	if diags.HasError() {
		t.Fatalf("unexpected diagnostics: %v", diags.Errors())
	}

	if !model.PrivateIP.IsNull() {
		t.Error("expected null private_ip")
	}
	if !model.Port.IsNull() {
		t.Error("expected null port")
	}
	if !model.PublicIP.IsNull() {
		t.Error("expected null public_ip")
	}
	if !model.AdminUsername.IsNull() {
		t.Error("expected null admin_username")
	}
	if !model.UpdatedAt.IsNull() {
		t.Error("expected null updated_at")
	}
	if !model.TenantID.IsNull() {
		t.Error("expected null tenant_id")
	}
}

// --- Resource unit tests ---

func TestNewResource(t *testing.T) {
	r := NewResource()
	if r == nil {
		t.Fatal("expected non-nil resource")
	}
}

func TestMetadata(t *testing.T) {
	r := NewResource()
	req := resource.MetadataRequest{ProviderTypeName: "frostmoln"}
	var resp resource.MetadataResponse
	r.Metadata(context.Background(), req, &resp)
	if resp.TypeName != "frostmoln_mysql_instance" {
		t.Errorf("expected type name frostmoln_mysql_instance, got %s", resp.TypeName)
	}
}

func TestSchema(t *testing.T) {
	r := NewResource()
	req := resource.SchemaRequest{}
	var resp resource.SchemaResponse
	r.Schema(context.Background(), req, &resp)

	requiredAttrs := []string{"name", "version", "flavor_id", "storage_gb", "vpc_id", "subnet_id"}
	for _, attr := range requiredAttrs {
		if _, ok := resp.Schema.Attributes[attr]; !ok {
			t.Errorf("expected attribute %s in schema", attr)
		}
	}

	computedAttrs := []string{"id", "status", "private_ip", "port", "public_ip", "admin_username", "created_at", "updated_at", "tenant_id"}
	for _, attr := range computedAttrs {
		if _, ok := resp.Schema.Attributes[attr]; !ok {
			t.Errorf("expected computed attribute %s in schema", attr)
		}
	}
}

// TestSchemaBackupAttributes pins the plan-time shape of the two backup attributes the
// database backend server-defaults when backups are enabled.
//
// Optional alone is an apply failure for `backup_enabled = true` with the attribute omitted
// (config null vs a server-echoed value -> "Provider produced inconsistent result after
// apply"), so both must be Computed. Neither may carry a schema Default: TransformDefaults
// substitutes a default whenever the CONFIG value is null regardless of prior state, so a
// Default of 35 rewrites a deliberately-raised retention down to the floor the moment the
// attribute is dropped from HCL — and the retention reaper then reaps every backup older than
// 35 days. UseStateForUnknown pins the server's value instead, which is only sound because
// fromAPI never leaves these attributes null (see TestMysqlInstanceModelFromAPIBackupDefaults).
func TestSchemaBackupAttributes(t *testing.T) {
	ctx := context.Background()
	var resp resource.SchemaResponse
	NewResource().Schema(ctx, resource.SchemaRequest{}, &resp)

	sched, ok := resp.Schema.Attributes["backup_schedule"].(schema.StringAttribute)
	if !ok {
		t.Fatalf("backup_schedule is %T, want schema.StringAttribute", resp.Schema.Attributes["backup_schedule"])
	}
	if !sched.Optional || !sched.Computed {
		t.Errorf("backup_schedule Optional=%v Computed=%v, want both true", sched.Optional, sched.Computed)
	}
	if sched.Default != nil {
		t.Error("backup_schedule must not carry a schema Default: dropping it from config would then rewrite the server's schedule")
	}
	if len(sched.PlanModifiers) != 1 {
		t.Fatalf("backup_schedule has %d plan modifiers, want exactly 1 (UseStateForUnknown)", len(sched.PlanModifiers))
	}
	if got := sched.PlanModifiers[0].Description(ctx); got != useStateOrDefaultDescription {
		t.Errorf("backup_schedule plan modifier = %q, want planmod.StringUseStateOrDefault (%q)", got, useStateOrDefaultDescription)
	}

	ret, ok := resp.Schema.Attributes["backup_retention_days"].(schema.Int64Attribute)
	if !ok {
		t.Fatalf("backup_retention_days is %T, want schema.Int64Attribute", resp.Schema.Attributes["backup_retention_days"])
	}
	if !ret.Optional || !ret.Computed {
		t.Errorf("backup_retention_days Optional=%v Computed=%v, want both true", ret.Optional, ret.Computed)
	}
	if ret.Default != nil {
		t.Error("backup_retention_days must not carry a schema Default: dropping it from config would then downgrade retention to the floor and the reaper would delete backups")
	}
	if len(ret.PlanModifiers) != 1 {
		t.Fatalf("backup_retention_days has %d plan modifiers, want exactly 1 (UseStateForUnknown)", len(ret.PlanModifiers))
	}
	if got := ret.PlanModifiers[0].Description(ctx); got != useStateOrDefaultDescription {
		t.Errorf("backup_retention_days plan modifier = %q, want planmod.Int64UseStateOrDefault (%q)", got, useStateOrDefaultDescription)
	}
}

// useStateOrDefaultDescription is planmod.{String,Int64}UseStateOrDefault's own description
// string; the plan-modifier interface exposes no other identity. Plain UseStateForUnknown is
// NOT interchangeable here — it copies a null prior state into the plan (see planmod).
const useStateOrDefaultDescription = "keeps the value already in state; a null state plans the server-side default"

// TestMysqlInstanceModelFromAPIBackupDefaults covers the backups-off read: the backend column
// is NULL, so the response omits both fields. They must land in state as the values an enable
// would apply, NOT as null — UseStateForUnknown has nothing to pin against a null prior state,
// so a null here means the plan still carries null across a backups-off -> backups-on flip and
// the server's default breaks the apply. Values are literals matching the database service's
// shared servicekit/managedbackup DefaultBackupSchedule / BackupRetentionMinDays.
func TestMysqlInstanceModelFromAPIBackupDefaults(t *testing.T) {
	ctx := context.Background()
	diags := diag.Diagnostics{}

	var model MysqlInstanceModel
	model.fromAPI(ctx, &apiMysqlInstance{ID: "my-1", BackupEnabled: false}, &diags)
	if diags.HasError() {
		t.Fatalf("unexpected diagnostics: %v", diags.Errors())
	}
	if got := model.BackupSchedule.ValueString(); got != "0 2 * * *" {
		t.Errorf("backup_schedule = %q, want %q", got, "0 2 * * *")
	}
	if got := model.BackupRetentionDays.ValueInt64(); got != 35 {
		t.Errorf("backup_retention_days = %d, want 35 (ADR-0085 object-lock floor)", got)
	}

	// Backups ON with the schedule absent is a genuinely broken row (the sweep's due-list skips
	// it, so it takes no backups). It must NOT be normalized: reporting the default there would
	// present a silently-unbacked-up instance as healthy and suppress the healing diff.
	model.fromAPI(ctx, &apiMysqlInstance{ID: "my-1", BackupEnabled: true}, &diags)
	if !model.BackupSchedule.IsNull() {
		t.Errorf("backup_schedule = %s, want null when backups are enabled and the API omits it", model.BackupSchedule)
	}

	// A legacy sub-floor retention is floored: the server clamps it on the next update, so
	// carrying the pre-clamp number into state would mismatch the echo.
	model.fromAPI(ctx, &apiMysqlInstance{ID: "my-1", BackupEnabled: true, BackupSchedule: "0 2 * * *", BackupRetentionDays: 7}, &diags)
	if got := model.BackupRetentionDays.ValueInt64(); got != 35 {
		t.Errorf("backup_retention_days = %d, want 35 (legacy sub-floor value floored)", got)
	}

	// An explicit server value is never overwritten by the normalization.
	model.fromAPI(ctx, &apiMysqlInstance{
		ID:                  "my-1",
		BackupEnabled:       true,
		BackupSchedule:      "0 4 * * *",
		BackupRetentionDays: 90,
	}, &diags)
	if got := model.BackupSchedule.ValueString(); got != "0 4 * * *" {
		t.Errorf("backup_schedule = %q, want %q", got, "0 4 * * *")
	}
	if got := model.BackupRetentionDays.ValueInt64(); got != 90 {
		t.Errorf("backup_retention_days = %d, want 90", got)
	}
}

func TestConfigureNilProviderData(t *testing.T) {
	r := &mysqlInstanceResource{}
	req := resource.ConfigureRequest{}
	var resp resource.ConfigureResponse
	r.Configure(context.Background(), req, &resp)
	if resp.Diagnostics.HasError() {
		t.Errorf("expected no errors for nil provider data, got %v", resp.Diagnostics.Errors())
	}
}

func TestConfigureWrongType(t *testing.T) {
	r := &mysqlInstanceResource{}
	req := resource.ConfigureRequest{
		ProviderData: "not-a-client",
	}
	var resp resource.ConfigureResponse
	r.Configure(context.Background(), req, &resp)
	if !resp.Diagnostics.HasError() {
		t.Error("expected error for wrong provider data type")
	}
}

// buildMysqlInstanceState creates a tfsdk.State pre-populated with a mysql instance.
func buildMysqlInstanceState(t *testing.T, model MysqlInstanceModel) tfsdk.State {
	t.Helper()
	r := NewResource()
	var schemaResp resource.SchemaResponse
	r.Schema(context.Background(), resource.SchemaRequest{}, &schemaResp)

	state := tfsdk.State{Schema: schemaResp.Schema}
	diags := state.Set(context.Background(), &model)
	if diags.HasError() {
		t.Fatalf("failed to set state: %v", diags.Errors())
	}
	return state
}

// buildMysqlInstancePlan creates a tfsdk.Plan pre-populated with a mysql instance.
func buildMysqlInstancePlan(t *testing.T, model MysqlInstanceModel) tfsdk.Plan {
	t.Helper()
	r := NewResource()
	var schemaResp resource.SchemaResponse
	r.Schema(context.Background(), resource.SchemaRequest{}, &schemaResp)

	plan := tfsdk.Plan{Schema: schemaResp.Schema}
	diags := plan.Set(context.Background(), &model)
	if diags.HasError() {
		t.Fatalf("failed to set plan: %v", diags.Errors())
	}
	return plan
}

func emptyMysqlInstanceState(t *testing.T) tfsdk.State {
	t.Helper()
	r := NewResource()
	var schemaResp resource.SchemaResponse
	r.Schema(context.Background(), resource.SchemaRequest{}, &schemaResp)

	stateVal := tftypes.NewValue(schemaResp.Schema.Type().TerraformType(context.Background()), nil)
	return tfsdk.State{Schema: schemaResp.Schema, Raw: stateVal}
}

func TestCreate(t *testing.T) {
	var callCount atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/tenants/t-1/databases":
			var body apiCreateMysqlInstanceRequest
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("failed to decode request: %v", err)
			}
			if body.Engine != "mysql" {
				t.Errorf("expected engine mysql, got %s", body.Engine)
			}
			if body.EngineVersion != "8.4" {
				t.Errorf("expected engineVersion 8.4, got %s", body.EngineVersion)
			}
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(apiMysqlInstance{
				ID:            "db-new",
				Name:          body.Name,
				Engine:        "mysql",
				EngineVersion: body.EngineVersion,
				FlavorID:      body.FlavorID,
				StorageGB:     body.StorageGB,
				VPCID:         body.VPCID,
				SubnetID:      body.SubnetID,
				Status:        "provisioning",
				CreatedAt:     "2025-01-01T00:00:00Z",
			})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-1/databases/db-new":
			count := callCount.Add(1)
			status := "provisioning"
			if count >= 2 {
				status = "running"
			}
			_ = json.NewEncoder(w).Encode(apiMysqlInstance{
				ID:            "db-new",
				Name:          "test-mysql",
				Engine:        "mysql",
				EngineVersion: "8.4",
				FlavorID:      "db.small",
				StorageGB:     50,
				VPCID:         "vpc-1",
				SubnetID:      "sn-1",
				Status:        status,
				PrivateIP:     "10.0.1.5",
				Port:          3306,
				AdminUsername: "mysqladmin",
				CreatedAt:     "2025-01-01T00:00:00Z",
				TenantID:      "t-1",
			})
		case strings.HasSuffix(r.URL.Path, "/events"):
			// The client waits on the tenant SSE stream instead of a timer
			// (internal/client/events.go). A 404 stands in for a gateway that does
			// not serve it -- an explicitly supported degradation back to timer
			// polling -- so this mock stays hermetic and exercises that path.
			w.WriteHeader(http.StatusNotFound)

		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key", client.WithHTTPClient(server.Client()))
	c.SetTenantIDForTest("t-1")

	r := &mysqlInstanceResource{
		client:       c,
		pollInterval: 10 * time.Millisecond,
		pollTimeout:  5 * time.Second,
	}

	plan := buildMysqlInstancePlan(t, MysqlInstanceModel{
		Name:      types.StringValue("test-mysql"),
		Version:   types.StringValue("8.4"),
		FlavorID:  types.StringValue("db.small"),
		StorageGB: types.Int64Value(50),
		VPCID:     types.StringValue("vpc-1"),
		SubnetID:  types.StringValue("sn-1"),
	})

	createResp := resource.CreateResponse{State: emptyMysqlInstanceState(t)}
	r.Create(context.Background(), resource.CreateRequest{Plan: plan}, &createResp)

	if createResp.Diagnostics.HasError() {
		t.Fatalf("create failed: %v", createResp.Diagnostics.Errors())
	}

	var result MysqlInstanceModel
	createResp.State.Get(context.Background(), &result)
	if result.ID.ValueString() != "db-new" {
		t.Errorf("expected ID db-new, got %s", result.ID.ValueString())
	}
	if result.Status.ValueString() != "running" {
		t.Errorf("expected status running, got %s", result.Status.ValueString())
	}
	if result.Port.ValueInt64() != 3306 {
		t.Errorf("expected port 3306, got %d", result.Port.ValueInt64())
	}
}

func TestRead(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-1/databases/db-123" {
			_ = json.NewEncoder(w).Encode(apiMysqlInstance{
				ID:            "db-123",
				Name:          "my-mysql",
				Engine:        "mysql",
				EngineVersion: "8.0",
				FlavorID:      "db.small",
				StorageGB:     50,
				VPCID:         "vpc-1",
				SubnetID:      "sn-1",
				Status:        "running",
				Port:          3306,
				CreatedAt:     "2025-01-01T00:00:00Z",
			})
			return
		}
		t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key", client.WithHTTPClient(server.Client()))
	c.SetTenantIDForTest("t-1")

	r := &mysqlInstanceResource{client: c}

	state := buildMysqlInstanceState(t, MysqlInstanceModel{
		ID:        types.StringValue("db-123"),
		Name:      types.StringValue("my-mysql"),
		Version:   types.StringValue("8.0"),
		FlavorID:  types.StringValue("db.small"),
		StorageGB: types.Int64Value(50),
		VPCID:     types.StringValue("vpc-1"),
		SubnetID:  types.StringValue("sn-1"),
		Status:    types.StringValue("running"),
		CreatedAt: types.StringValue("2025-01-01T00:00:00Z"),
	})

	readResp := resource.ReadResponse{State: state}
	r.Read(context.Background(), resource.ReadRequest{State: state}, &readResp)

	if readResp.Diagnostics.HasError() {
		t.Fatalf("read failed: %v", readResp.Diagnostics.Errors())
	}

	var result MysqlInstanceModel
	readResp.State.Get(context.Background(), &result)
	if result.Status.ValueString() != "running" {
		t.Errorf("expected status running, got %s", result.Status.ValueString())
	}
}

func TestReadNotFound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"code": "NOT_FOUND", "message": "not found",
		})
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key", client.WithHTTPClient(server.Client()))
	c.SetTenantIDForTest("t-1")

	r := &mysqlInstanceResource{client: c}

	state := buildMysqlInstanceState(t, MysqlInstanceModel{
		ID:        types.StringValue("db-gone"),
		Name:      types.StringValue("gone"),
		Version:   types.StringValue("8.0"),
		FlavorID:  types.StringValue("db.small"),
		StorageGB: types.Int64Value(50),
		VPCID:     types.StringValue("vpc-1"),
		SubnetID:  types.StringValue("sn-1"),
		Status:    types.StringValue("running"),
		CreatedAt: types.StringValue("2025-01-01T00:00:00Z"),
	})

	readResp := resource.ReadResponse{State: state}
	r.Read(context.Background(), resource.ReadRequest{State: state}, &readResp)

	if readResp.Diagnostics.HasError() {
		t.Fatalf("expected no error for not-found, got %v", readResp.Diagnostics.Errors())
	}

	// State should be removed (empty raw value).
	var result MysqlInstanceModel
	diags := readResp.State.Get(context.Background(), &result)
	if !diags.HasError() {
		// If Get succeeds and we can read state, the resource was not removed.
		if result.ID.ValueString() != "" {
			t.Error("expected state to be removed after 404")
		}
	}
}

func TestDelete(t *testing.T) {
	deleted := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodDelete && r.URL.Path == "/v1/tenants/t-1/databases/db-123":
			deleted = true
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-1/databases/db-123":
			if deleted {
				w.WriteHeader(http.StatusNotFound)
				_ = json.NewEncoder(w).Encode(map[string]interface{}{
					"code": "NOT_FOUND", "message": "not found",
				})
			} else {
				_ = json.NewEncoder(w).Encode(apiMysqlInstance{
					ID: "db-123", Status: "deleting",
				})
			}
		case strings.HasSuffix(r.URL.Path, "/events"):
			// The client waits on the tenant SSE stream instead of a timer
			// (internal/client/events.go). A 404 stands in for a gateway that does
			// not serve it -- an explicitly supported degradation back to timer
			// polling -- so this mock stays hermetic and exercises that path.
			w.WriteHeader(http.StatusNotFound)

		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key", client.WithHTTPClient(server.Client()))
	c.SetTenantIDForTest("t-1")

	r := &mysqlInstanceResource{
		client:       c,
		pollInterval: 10 * time.Millisecond,
		pollTimeout:  5 * time.Second,
	}

	state := buildMysqlInstanceState(t, MysqlInstanceModel{
		ID:        types.StringValue("db-123"),
		Name:      types.StringValue("my-mysql"),
		Version:   types.StringValue("8.0"),
		FlavorID:  types.StringValue("db.small"),
		StorageGB: types.Int64Value(50),
		VPCID:     types.StringValue("vpc-1"),
		SubnetID:  types.StringValue("sn-1"),
		Status:    types.StringValue("running"),
		CreatedAt: types.StringValue("2025-01-01T00:00:00Z"),
	})

	deleteResp := resource.DeleteResponse{State: state}
	r.Delete(context.Background(), resource.DeleteRequest{State: state}, &deleteResp)

	if deleteResp.Diagnostics.HasError() {
		t.Fatalf("delete failed: %v", deleteResp.Diagnostics.Errors())
	}
}

func TestUpdate(t *testing.T) {
	var updatedBody apiUpdateMysqlInstanceRequest
	var resizeBody apiResizeMysqlInstanceRequest
	var resizeCalled atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/tenants/t-1/databases/db-123/resize":
			_ = json.NewDecoder(r.Body).Decode(&resizeBody)
			resizeCalled.Store(true)
			w.WriteHeader(http.StatusOK)
			_, _ = fmt.Fprint(w, `{}`)
		case r.Method == http.MethodPut && r.URL.Path == "/v1/tenants/t-1/databases/db-123":
			_ = json.NewDecoder(r.Body).Decode(&updatedBody)
			w.WriteHeader(http.StatusOK)
			_, _ = fmt.Fprint(w, `{}`)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-1/databases/db-123":
			_ = json.NewEncoder(w).Encode(apiMysqlInstance{
				ID:            "db-123",
				Name:          "updated-mysql",
				Engine:        "mysql",
				EngineVersion: "8.4",
				FlavorID:      "db.small",
				StorageGB:     200,
				VPCID:         "vpc-1",
				SubnetID:      "sn-1",
				Status:        "running",
				Port:          3306,
				CreatedAt:     "2025-01-01T00:00:00Z",
			})
		case strings.HasSuffix(r.URL.Path, "/events"):
			// The client waits on the tenant SSE stream instead of a timer
			// (internal/client/events.go). A 404 stands in for a gateway that does
			// not serve it -- an explicitly supported degradation back to timer
			// polling -- so this mock stays hermetic and exercises that path.
			w.WriteHeader(http.StatusNotFound)

		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key", client.WithHTTPClient(server.Client()))
	c.SetTenantIDForTest("t-1")

	r := &mysqlInstanceResource{
		client:       c,
		pollInterval: 10 * time.Millisecond,
		pollTimeout:  5 * time.Second,
	}

	state := buildMysqlInstanceState(t, MysqlInstanceModel{
		ID:        types.StringValue("db-123"),
		Name:      types.StringValue("old-mysql"),
		Version:   types.StringValue("8.4"),
		FlavorID:  types.StringValue("db.small"),
		StorageGB: types.Int64Value(100),
		VPCID:     types.StringValue("vpc-1"),
		SubnetID:  types.StringValue("sn-1"),
		Status:    types.StringValue("running"),
		CreatedAt: types.StringValue("2025-01-01T00:00:00Z"),
	})

	// Rename + storage grow. flavor_id is unchanged (a flavor change is rejected
	// at plan time, so Update never receives one).
	plan := buildMysqlInstancePlan(t, MysqlInstanceModel{
		ID:        types.StringValue("db-123"),
		Name:      types.StringValue("updated-mysql"),
		Version:   types.StringValue("8.4"),
		FlavorID:  types.StringValue("db.small"),
		StorageGB: types.Int64Value(200),
		VPCID:     types.StringValue("vpc-1"),
		SubnetID:  types.StringValue("sn-1"),
		Status:    types.StringValue("running"),
		CreatedAt: types.StringValue("2025-01-01T00:00:00Z"),
	})

	updateResp := resource.UpdateResponse{State: state}
	r.Update(context.Background(), resource.UpdateRequest{Plan: plan, State: state}, &updateResp)

	if updateResp.Diagnostics.HasError() {
		t.Fatalf("update failed: %v", updateResp.Diagnostics.Errors())
	}

	if !resizeCalled.Load() {
		t.Error("expected POST /resize to be called for storage grow")
	}
	if resizeBody.StorageGB != 200 {
		t.Errorf("expected resize to 200 GB, got %d", resizeBody.StorageGB)
	}
	if updatedBody.Name == nil || *updatedBody.Name != "updated-mysql" {
		t.Error("expected name in update request")
	}
}

// TestUpgradeState_V0ToV1 guards the v0→v1 migration: a v0 state row carries
// the old `flavor` attribute; the upgrader must copy it into `flavor_id` and
// carry the other attributes through, so the first post-upgrade plan is clean
// (no spurious update, no destroy). Other attributes are null-filled here —
// only the rename behaviour is under test.
func TestUpgradeState_V0ToV1(t *testing.T) {
	ctx := context.Background()
	r := &mysqlInstanceResource{}

	up, ok := r.UpgradeState(ctx)[0]
	if !ok {
		t.Fatal("expected a v0 state upgrader")
	}
	if up.PriorSchema == nil {
		t.Fatal("expected PriorSchema for v0")
	}
	if _, ok := up.PriorSchema.Attributes["flavor"]; !ok {
		t.Error("prior schema must carry the old `flavor` attribute")
	}
	if _, ok := up.PriorSchema.Attributes["flavor_id"]; ok {
		t.Error("prior schema must not carry the new `flavor_id` attribute")
	}

	priorType := up.PriorSchema.Type().TerraformType(ctx)
	raw := map[string]tftypes.Value{}
	for name, at := range priorType.(tftypes.Object).AttributeTypes {
		raw[name] = tftypes.NewValue(at, nil)
	}
	raw["id"] = tftypes.NewValue(tftypes.String, "db-123")
	raw["name"] = tftypes.NewValue(tftypes.String, "my-db")
	raw["flavor"] = tftypes.NewValue(tftypes.String, "db.gp1.small")
	priorVal := tftypes.NewValue(priorType, raw)

	var schemaResp resource.SchemaResponse
	r.Schema(ctx, resource.SchemaRequest{}, &schemaResp)

	req := resource.UpgradeStateRequest{State: &tfsdk.State{Schema: *up.PriorSchema, Raw: priorVal}}
	resp := &resource.UpgradeStateResponse{State: tfsdk.State{Schema: schemaResp.Schema}}

	up.StateUpgrader(ctx, req, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected errors: %v", resp.Diagnostics.Errors())
	}
	var model MysqlInstanceModel
	resp.State.Get(ctx, &model)
	if model.FlavorID.ValueString() != "db.gp1.small" {
		t.Errorf("expected flavor_id db.gp1.small, got %s", model.FlavorID.ValueString())
	}
	if model.ID.ValueString() != "db-123" {
		t.Errorf("expected id carried through, got %s", model.ID.ValueString())
	}
	if model.Name.ValueString() != "my-db" {
		t.Errorf("expected name carried through, got %s", model.Name.ValueString())
	}
}

// TestMysqlInstanceToUpdateRequestEnableCarriesPolicy pins that turning backups ON sends the
// planned schedule and retention even though the plan modifier pinned both to state and neither
// changed. Without it the server fills its NULL columns from its own constants: a divergence from
// ours becomes an inconsistent-result error on every enable, and the pre-ADR-0085 backend wrote no
// schedule at all, leaving a row that silently takes no backups.
func TestMysqlInstanceToUpdateRequestEnableCarriesPolicy(t *testing.T) {
	state := MysqlInstanceModel{
		BackupEnabled:       types.BoolValue(false),
		BackupSchedule:      types.StringValue("0 2 * * *"),
		BackupRetentionDays: types.Int64Value(35),
	}
	plan := state
	plan.BackupEnabled = types.BoolValue(true)

	req := plan.toUpdateRequest(&state)
	if req.BackupSchedule == nil || *req.BackupSchedule != "0 2 * * *" {
		t.Error("expected the enable to carry backupSchedule")
	}
	if req.BackupRetentionDays == nil || *req.BackupRetentionDays != 35 {
		t.Error("expected the enable to carry backupRetentionDays")
	}

	// Disabling carries only the flag: the columns already hold the values, and resending them
	// would be noise on a path where nothing about the policy changed.
	off := plan
	off.BackupEnabled = types.BoolValue(false)
	if req := off.toUpdateRequest(&plan); req.BackupSchedule != nil || req.BackupRetentionDays != nil {
		t.Error("expected a disable to carry only backupEnabled")
	}
}
