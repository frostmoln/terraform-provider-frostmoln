package postgres_instance

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
)

// --- Model unit tests ---

func TestPostgresInstanceModelToCreateRequest(t *testing.T) {
	ctx := context.Background()
	diags := diag.Diagnostics{}

	model := PostgresInstanceModel{
		Name:                types.StringValue("my-pg"),
		Version:             types.StringValue("16"),
		Flavor:              types.StringValue("db.gp1.small"),
		StorageGB:           types.Int64Value(50),
		VPCID:               types.StringValue("vpc-1"),
		SubnetID:            types.StringValue("sn-1"),
		HAEnabled:           types.BoolNull(),
		BackupEnabled:       types.BoolNull(),
		BackupSchedule:      types.StringNull(),
		BackupRetentionDays: types.Int64Null(),
		ParameterGroupID:    types.StringNull(),
	}

	req := model.toCreateRequest(ctx, &diags)
	if diags.HasError() {
		t.Fatalf("unexpected diagnostics: %v", diags.Errors())
	}

	if req.Name != "my-pg" {
		t.Errorf("expected name my-pg, got %s", req.Name)
	}
	if req.PostgresVersion != "16" {
		t.Errorf("expected postgresVersion 16, got %s", req.PostgresVersion)
	}
	if req.Flavor != "db.gp1.small" {
		t.Errorf("expected flavor db.gp1.small, got %s", req.Flavor)
	}
	if req.StorageGB != 50 {
		t.Errorf("expected storageGb 50, got %d", req.StorageGB)
	}
	if req.VPCID != "vpc-1" {
		t.Errorf("expected vpcId vpc-1, got %s", req.VPCID)
	}
	if req.SubnetID != "sn-1" {
		t.Errorf("expected subnetId sn-1, got %s", req.SubnetID)
	}
	if req.HAEnabled != nil {
		t.Error("expected nil haEnabled for null value")
	}
	if req.BackupEnabled != nil {
		t.Error("expected nil backupEnabled for null value")
	}
	if req.BackupSchedule != "" {
		t.Errorf("expected empty backupSchedule, got %s", req.BackupSchedule)
	}
	if req.BackupRetentionDays != nil {
		t.Error("expected nil backupRetentionDays for null value")
	}
	if req.ParameterGroupID != "" {
		t.Errorf("expected empty parameterGroupId, got %s", req.ParameterGroupID)
	}
}

func TestPostgresInstanceModelToCreateRequestWithOptionals(t *testing.T) {
	ctx := context.Background()
	diags := diag.Diagnostics{}

	model := PostgresInstanceModel{
		Name:                types.StringValue("my-pg"),
		Version:             types.StringValue("15"),
		Flavor:              types.StringValue("db.gp1.medium"),
		StorageGB:           types.Int64Value(100),
		VPCID:               types.StringValue("vpc-1"),
		SubnetID:            types.StringValue("sn-1"),
		HAEnabled:           types.BoolValue(true),
		BackupEnabled:       types.BoolValue(true),
		BackupSchedule:      types.StringValue("0 2 * * *"),
		BackupRetentionDays: types.Int64Value(14),
		ParameterGroupID:    types.StringValue("pg-1"),
	}

	req := model.toCreateRequest(ctx, &diags)
	if diags.HasError() {
		t.Fatalf("unexpected diagnostics: %v", diags.Errors())
	}

	if req.HAEnabled == nil || !*req.HAEnabled {
		t.Error("expected haEnabled true")
	}
	if req.BackupEnabled == nil || !*req.BackupEnabled {
		t.Error("expected backupEnabled true")
	}
	if req.BackupSchedule != "0 2 * * *" {
		t.Errorf("expected backupSchedule cron, got %s", req.BackupSchedule)
	}
	if req.BackupRetentionDays == nil || *req.BackupRetentionDays != 14 {
		t.Error("expected backupRetentionDays 14")
	}
	if req.ParameterGroupID != "pg-1" {
		t.Errorf("expected parameterGroupId pg-1, got %s", req.ParameterGroupID)
	}
}

func TestPostgresInstanceModelToUpdateRequest(t *testing.T) {
	plan := PostgresInstanceModel{
		Name:                types.StringValue("new-name"),
		Flavor:              types.StringValue("db.gp1.large"),
		StorageGB:           types.Int64Value(200),
		BackupEnabled:       types.BoolValue(true),
		BackupSchedule:      types.StringValue("0 3 * * *"),
		BackupRetentionDays: types.Int64Value(30),
		ParameterGroupID:    types.StringValue("pg-2"),
	}
	state := PostgresInstanceModel{
		Name:                types.StringValue("old-name"),
		Flavor:              types.StringValue("db.gp1.small"),
		StorageGB:           types.Int64Value(50),
		BackupEnabled:       types.BoolValue(false),
		BackupSchedule:      types.StringValue("0 2 * * *"),
		BackupRetentionDays: types.Int64Value(7),
		ParameterGroupID:    types.StringValue("pg-1"),
	}

	req := plan.toUpdateRequest(&state)
	if req.Name == nil || *req.Name != "new-name" {
		t.Error("expected name update to new-name")
	}
	if req.Flavor == nil || *req.Flavor != "db.gp1.large" {
		t.Error("expected flavor update")
	}
	if req.StorageGB == nil || *req.StorageGB != 200 {
		t.Error("expected storageGb update")
	}
	if req.BackupEnabled == nil || !*req.BackupEnabled {
		t.Error("expected backupEnabled update")
	}
	if req.BackupSchedule == nil || *req.BackupSchedule != "0 3 * * *" {
		t.Error("expected backupSchedule update")
	}
	if req.BackupRetentionDays == nil || *req.BackupRetentionDays != 30 {
		t.Error("expected backupRetentionDays update")
	}
	if req.ParameterGroupID == nil || *req.ParameterGroupID != "pg-2" {
		t.Error("expected parameterGroupId update")
	}
}

func TestPostgresInstanceModelToUpdateRequestNoChanges(t *testing.T) {
	same := PostgresInstanceModel{
		Name:                types.StringValue("same"),
		Flavor:              types.StringValue("db.gp1.small"),
		StorageGB:           types.Int64Value(50),
		BackupEnabled:       types.BoolValue(true),
		BackupSchedule:      types.StringValue("0 2 * * *"),
		BackupRetentionDays: types.Int64Value(7),
		ParameterGroupID:    types.StringValue("pg-1"),
	}

	req := same.toUpdateRequest(&same)
	if req.Name != nil || req.Flavor != nil || req.StorageGB != nil ||
		req.BackupEnabled != nil || req.BackupSchedule != nil ||
		req.BackupRetentionDays != nil || req.ParameterGroupID != nil {
		t.Error("expected no changes in update request")
	}
}

func TestPostgresInstanceModelFromAPI(t *testing.T) {
	ctx := context.Background()
	diags := diag.Diagnostics{}

	api := &apiPostgresInstance{
		ID:                  "pg-123",
		Name:                "my-pg",
		PostgresVersion:     "16",
		Flavor:              "db.gp1.small",
		StorageGB:           50,
		VPCID:               "vpc-1",
		SubnetID:            "sn-1",
		HAEnabled:           true,
		BackupEnabled:       true,
		BackupSchedule:      "0 2 * * *",
		BackupRetentionDays: 7,
		ParameterGroupID:    "pg-grp-1",
		Status:              "running",
		PrivateIP:           "10.0.1.5",
		Port:                5432,
		FloatingIP:          "203.0.113.5",
		AdminUsername:       "frostadmin",
		CreatedAt:           "2025-01-01T00:00:00Z",
		UpdatedAt:           "2025-01-02T00:00:00Z",
		TenantID:            "t-1",
	}

	var model PostgresInstanceModel
	model.fromAPI(ctx, api, &diags)
	if diags.HasError() {
		t.Fatalf("unexpected diagnostics: %v", diags.Errors())
	}

	if model.ID.ValueString() != "pg-123" {
		t.Errorf("expected ID pg-123, got %s", model.ID.ValueString())
	}
	if model.Version.ValueString() != "16" {
		t.Errorf("expected version 16, got %s", model.Version.ValueString())
	}
	if model.StorageGB.ValueInt64() != 50 {
		t.Errorf("expected storage_gb 50, got %d", model.StorageGB.ValueInt64())
	}
	if !model.HAEnabled.ValueBool() {
		t.Error("expected ha_enabled true")
	}
	if model.Port.ValueInt64() != 5432 {
		t.Errorf("expected port 5432, got %d", model.Port.ValueInt64())
	}
	if model.FloatingIP.ValueString() != "203.0.113.5" {
		t.Errorf("expected floating_ip, got %s", model.FloatingIP.ValueString())
	}
	if model.AdminUsername.ValueString() != "frostadmin" {
		t.Errorf("expected admin_username frostadmin, got %s", model.AdminUsername.ValueString())
	}
	if model.TenantID.ValueString() != "t-1" {
		t.Errorf("expected tenant_id t-1, got %s", model.TenantID.ValueString())
	}
	if model.BackupRetentionDays.ValueInt64() != 7 {
		t.Errorf("expected backup_retention_days 7, got %d", model.BackupRetentionDays.ValueInt64())
	}
}

func TestPostgresInstanceModelFromAPINulls(t *testing.T) {
	ctx := context.Background()
	diags := diag.Diagnostics{}

	api := &apiPostgresInstance{
		ID:              "pg-123",
		Name:            "my-pg",
		PostgresVersion: "16",
		Flavor:          "db.gp1.small",
		StorageGB:       50,
		VPCID:           "vpc-1",
		SubnetID:        "sn-1",
		Status:          "provisioning",
		CreatedAt:       "2025-01-01T00:00:00Z",
	}

	var model PostgresInstanceModel
	model.fromAPI(ctx, api, &diags)
	if diags.HasError() {
		t.Fatalf("unexpected diagnostics: %v", diags.Errors())
	}

	if !model.BackupSchedule.IsNull() {
		t.Error("expected null backup_schedule")
	}
	if !model.BackupRetentionDays.IsNull() {
		t.Error("expected null backup_retention_days")
	}
	if !model.ParameterGroupID.IsNull() {
		t.Error("expected null parameter_group_id")
	}
	if !model.PrivateIP.IsNull() {
		t.Error("expected null private_ip")
	}
	if !model.Port.IsNull() {
		t.Error("expected null port")
	}
	if !model.FloatingIP.IsNull() {
		t.Error("expected null floating_ip")
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
	if NewResource() == nil {
		t.Fatal("expected non-nil resource")
	}
}

func TestMetadata(t *testing.T) {
	r := NewResource()
	req := resource.MetadataRequest{ProviderTypeName: "frostmoln"}
	var resp resource.MetadataResponse
	r.Metadata(context.Background(), req, &resp)
	if resp.TypeName != "frostmoln_postgres_instance" {
		t.Errorf("expected type name frostmoln_postgres_instance, got %s", resp.TypeName)
	}
}

func TestSchema(t *testing.T) {
	r := NewResource()
	var resp resource.SchemaResponse
	r.Schema(context.Background(), resource.SchemaRequest{}, &resp)

	required := []string{"name", "version", "flavor", "storage_gb", "vpc_id", "subnet_id"}
	for _, attr := range required {
		if _, ok := resp.Schema.Attributes[attr]; !ok {
			t.Errorf("expected attribute %s in schema", attr)
		}
	}
	computed := []string{"id", "status", "private_ip", "port", "floating_ip", "admin_username", "created_at", "updated_at", "tenant_id"}
	for _, attr := range computed {
		if _, ok := resp.Schema.Attributes[attr]; !ok {
			t.Errorf("expected computed attribute %s in schema", attr)
		}
	}
}

func TestConfigureNilProviderData(t *testing.T) {
	r := &postgresInstanceResource{}
	var resp resource.ConfigureResponse
	r.Configure(context.Background(), resource.ConfigureRequest{}, &resp)
	if resp.Diagnostics.HasError() {
		t.Errorf("expected no errors for nil provider data, got %v", resp.Diagnostics.Errors())
	}
}

func TestConfigureWrongType(t *testing.T) {
	r := &postgresInstanceResource{}
	var resp resource.ConfigureResponse
	r.Configure(context.Background(), resource.ConfigureRequest{ProviderData: "not-a-client"}, &resp)
	if !resp.Diagnostics.HasError() {
		t.Error("expected error for wrong provider data type")
	}
}

func TestConfigureValid(t *testing.T) {
	r := &postgresInstanceResource{}
	c := client.NewClient("http://example.invalid", "test-key") // pragma: allowlist secret
	var resp resource.ConfigureResponse
	r.Configure(context.Background(), resource.ConfigureRequest{ProviderData: c}, &resp)
	if resp.Diagnostics.HasError() {
		t.Errorf("expected no errors, got %v", resp.Diagnostics.Errors())
	}
	if r.client != c {
		t.Error("expected client to be set")
	}
}

func TestImportState(t *testing.T) {
	r := NewResource().(resource.ResourceWithImportState)
	var schemaResp resource.SchemaResponse
	r.(resource.Resource).Schema(context.Background(), resource.SchemaRequest{}, &schemaResp)

	stateVal := tftypes.NewValue(schemaResp.Schema.Type().TerraformType(context.Background()), nil)
	resp := resource.ImportStateResponse{State: tfsdk.State{Schema: schemaResp.Schema, Raw: stateVal}}
	r.ImportState(context.Background(), resource.ImportStateRequest{ID: "pg-imported"}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("import failed: %v", resp.Diagnostics.Errors())
	}

	var imported PostgresInstanceModel
	resp.State.Get(context.Background(), &imported)
	if imported.ID.ValueString() != "pg-imported" {
		t.Errorf("expected imported ID pg-imported, got %s", imported.ID.ValueString())
	}
}

// --- tfsdk helpers ---

func buildState(t *testing.T, model PostgresInstanceModel) tfsdk.State {
	t.Helper()
	r := NewResource()
	var schemaResp resource.SchemaResponse
	r.Schema(context.Background(), resource.SchemaRequest{}, &schemaResp)
	state := tfsdk.State{Schema: schemaResp.Schema}
	if diags := state.Set(context.Background(), &model); diags.HasError() {
		t.Fatalf("failed to set state: %v", diags.Errors())
	}
	return state
}

func buildPlan(t *testing.T, model PostgresInstanceModel) tfsdk.Plan {
	t.Helper()
	r := NewResource()
	var schemaResp resource.SchemaResponse
	r.Schema(context.Background(), resource.SchemaRequest{}, &schemaResp)
	plan := tfsdk.Plan{Schema: schemaResp.Schema}
	if diags := plan.Set(context.Background(), &model); diags.HasError() {
		t.Fatalf("failed to set plan: %v", diags.Errors())
	}
	return plan
}

func emptyState(t *testing.T) tfsdk.State {
	t.Helper()
	r := NewResource()
	var schemaResp resource.SchemaResponse
	r.Schema(context.Background(), resource.SchemaRequest{}, &schemaResp)
	stateVal := tftypes.NewValue(schemaResp.Schema.Type().TerraformType(context.Background()), nil)
	return tfsdk.State{Schema: schemaResp.Schema, Raw: stateVal}
}

func fullPlanModel() PostgresInstanceModel {
	return PostgresInstanceModel{
		Name:      types.StringValue("test-pg"),
		Version:   types.StringValue("16"),
		Flavor:    types.StringValue("db.gp1.small"),
		StorageGB: types.Int64Value(50),
		VPCID:     types.StringValue("vpc-1"),
		SubnetID:  types.StringValue("sn-1"),
	}
}

func newClient(t *testing.T, server *httptest.Server) *client.Client {
	t.Helper()
	c := client.NewClient(server.URL, "test-key", client.WithHTTPClient(server.Client())) // pragma: allowlist secret
	c.SetTenantIDForTest("t-1")
	return c
}

func newResource(c *client.Client) *postgresInstanceResource {
	return &postgresInstanceResource{
		client:       c,
		pollInterval: 5 * time.Millisecond,
		pollTimeout:  5 * time.Second,
	}
}

// --- CRUD tests ---

func TestCreate(t *testing.T) {
	var getCount atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/tenants/t-1/databases":
			var body apiCreatePostgresInstanceRequest
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decode failed: %v", err)
			}
			if body.PostgresVersion != "16" {
				t.Errorf("expected postgresVersion 16, got %s", body.PostgresVersion)
			}
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(apiPostgresInstance{
				ID: "pg-new", Name: body.Name, PostgresVersion: body.PostgresVersion,
				Flavor: body.Flavor, StorageGB: body.StorageGB, VPCID: body.VPCID,
				SubnetID: body.SubnetID, Status: "provisioning", CreatedAt: "2025-01-01T00:00:00Z",
			})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-1/databases/pg-new":
			status := "provisioning"
			if getCount.Add(1) >= 2 {
				status = "running"
			}
			_ = json.NewEncoder(w).Encode(apiPostgresInstance{
				ID: "pg-new", Name: "test-pg", PostgresVersion: "16", Flavor: "db.gp1.small",
				StorageGB: 50, VPCID: "vpc-1", SubnetID: "sn-1", Status: status,
				PrivateIP: "10.0.1.5", Port: 5432, AdminUsername: "frostadmin",
				CreatedAt: "2025-01-01T00:00:00Z",
			})
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	r := newResource(newClient(t, server))
	createResp := resource.CreateResponse{State: emptyState(t)}
	r.Create(context.Background(), resource.CreateRequest{Plan: buildPlan(t, fullPlanModel())}, &createResp)

	if createResp.Diagnostics.HasError() {
		t.Fatalf("create failed: %v", createResp.Diagnostics.Errors())
	}
	var result PostgresInstanceModel
	createResp.State.Get(context.Background(), &result)
	if result.ID.ValueString() != "pg-new" {
		t.Errorf("expected ID pg-new, got %s", result.ID.ValueString())
	}
	if result.Status.ValueString() != "running" {
		t.Errorf("expected status running, got %s", result.Status.ValueString())
	}
	if result.Port.ValueInt64() != 5432 {
		t.Errorf("expected port 5432, got %d", result.Port.ValueInt64())
	}
}

func TestCreateAPIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]string{"code": "INTERNAL", "message": "boom"})
	}))
	defer server.Close()

	r := newResource(newClient(t, server))
	createResp := resource.CreateResponse{State: emptyState(t)}
	r.Create(context.Background(), resource.CreateRequest{Plan: buildPlan(t, fullPlanModel())}, &createResp)
	if !createResp.Diagnostics.HasError() {
		t.Error("expected error for API failure on create")
	}
}

func TestCreateBadResponseBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte("not json"))
	}))
	defer server.Close()

	r := newResource(newClient(t, server))
	createResp := resource.CreateResponse{State: emptyState(t)}
	r.Create(context.Background(), resource.CreateRequest{Plan: buildPlan(t, fullPlanModel())}, &createResp)
	if !createResp.Diagnostics.HasError() {
		t.Error("expected error for bad response body")
	}
}

func TestCreatePollingErrorState(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost:
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(apiPostgresInstance{ID: "pg-err", Status: "provisioning", CreatedAt: "2025-01-01T00:00:00Z"})
		case http.MethodGet:
			_ = json.NewEncoder(w).Encode(apiPostgresInstance{ID: "pg-err", Status: "failed", CreatedAt: "2025-01-01T00:00:00Z"})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	r := newResource(newClient(t, server))
	createResp := resource.CreateResponse{State: emptyState(t)}
	r.Create(context.Background(), resource.CreateRequest{Plan: buildPlan(t, fullPlanModel())}, &createResp)
	if !createResp.Diagnostics.HasError() {
		t.Error("expected error when instance enters failed state")
	}
}

func TestRead(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-1/databases/pg-123" {
			_ = json.NewEncoder(w).Encode(apiPostgresInstance{
				ID: "pg-123", Name: "my-pg", PostgresVersion: "16", Flavor: "db.gp1.small",
				StorageGB: 50, VPCID: "vpc-1", SubnetID: "sn-1", Status: "running",
				Port: 5432, CreatedAt: "2025-01-01T00:00:00Z",
			})
			return
		}
		t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	r := newResource(newClient(t, server))
	state := buildState(t, PostgresInstanceModel{
		ID: types.StringValue("pg-123"), Name: types.StringValue("my-pg"),
		Version: types.StringValue("16"), Flavor: types.StringValue("db.gp1.small"),
		StorageGB: types.Int64Value(50), VPCID: types.StringValue("vpc-1"),
		SubnetID: types.StringValue("sn-1"), Status: types.StringValue("running"),
		CreatedAt: types.StringValue("2025-01-01T00:00:00Z"),
	})
	readResp := resource.ReadResponse{State: state}
	r.Read(context.Background(), resource.ReadRequest{State: state}, &readResp)

	if readResp.Diagnostics.HasError() {
		t.Fatalf("read failed: %v", readResp.Diagnostics.Errors())
	}
	var result PostgresInstanceModel
	readResp.State.Get(context.Background(), &result)
	if result.Status.ValueString() != "running" {
		t.Errorf("expected status running, got %s", result.Status.ValueString())
	}
}

func TestReadNotFound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]string{"code": "NOT_FOUND", "message": "not found"})
	}))
	defer server.Close()

	r := newResource(newClient(t, server))
	state := buildState(t, PostgresInstanceModel{
		ID: types.StringValue("pg-gone"), Name: types.StringValue("gone"),
		Version: types.StringValue("16"), Flavor: types.StringValue("db.gp1.small"),
		StorageGB: types.Int64Value(50), VPCID: types.StringValue("vpc-1"),
		SubnetID: types.StringValue("sn-1"), Status: types.StringValue("running"),
		CreatedAt: types.StringValue("2025-01-01T00:00:00Z"),
	})
	readResp := resource.ReadResponse{State: state}
	r.Read(context.Background(), resource.ReadRequest{State: state}, &readResp)

	if readResp.Diagnostics.HasError() {
		t.Fatalf("expected no error for not-found, got %v", readResp.Diagnostics.Errors())
	}
	var result PostgresInstanceModel
	if diags := readResp.State.Get(context.Background(), &result); !diags.HasError() {
		if result.ID.ValueString() != "" {
			t.Error("expected state to be removed after 404")
		}
	}
}

func TestReadServerError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]string{"code": "INTERNAL", "message": "boom"})
	}))
	defer server.Close()

	r := newResource(newClient(t, server))
	state := buildState(t, PostgresInstanceModel{
		ID: types.StringValue("pg-123"), Name: types.StringValue("my-pg"),
		Version: types.StringValue("16"), Flavor: types.StringValue("db.gp1.small"),
		StorageGB: types.Int64Value(50), VPCID: types.StringValue("vpc-1"),
		SubnetID: types.StringValue("sn-1"), Status: types.StringValue("running"),
		CreatedAt: types.StringValue("2025-01-01T00:00:00Z"),
	})
	readResp := resource.ReadResponse{State: state}
	r.Read(context.Background(), resource.ReadRequest{State: state}, &readResp)
	if !readResp.Diagnostics.HasError() {
		t.Error("expected error for server error on read")
	}
}

func TestUpdate(t *testing.T) {
	var updatedBody apiUpdatePostgresInstanceRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPut && r.URL.Path == "/v1/tenants/t-1/databases/pg-123":
			_ = json.NewDecoder(r.Body).Decode(&updatedBody)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{}`))
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-1/databases/pg-123":
			_ = json.NewEncoder(w).Encode(apiPostgresInstance{
				ID: "pg-123", Name: "updated-pg", PostgresVersion: "16", Flavor: "db.gp1.large",
				StorageGB: 100, VPCID: "vpc-1", SubnetID: "sn-1", Status: "running",
				Port: 5432, CreatedAt: "2025-01-01T00:00:00Z",
			})
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	r := newResource(newClient(t, server))
	state := buildState(t, PostgresInstanceModel{
		ID: types.StringValue("pg-123"), Name: types.StringValue("old-pg"),
		Version: types.StringValue("16"), Flavor: types.StringValue("db.gp1.small"),
		StorageGB: types.Int64Value(50), VPCID: types.StringValue("vpc-1"),
		SubnetID: types.StringValue("sn-1"), Status: types.StringValue("running"),
		CreatedAt: types.StringValue("2025-01-01T00:00:00Z"),
	})
	plan := buildPlan(t, PostgresInstanceModel{
		ID: types.StringValue("pg-123"), Name: types.StringValue("updated-pg"),
		Version: types.StringValue("16"), Flavor: types.StringValue("db.gp1.large"),
		StorageGB: types.Int64Value(100), VPCID: types.StringValue("vpc-1"),
		SubnetID: types.StringValue("sn-1"), Status: types.StringValue("running"),
		CreatedAt: types.StringValue("2025-01-01T00:00:00Z"),
	})
	updateResp := resource.UpdateResponse{State: state}
	r.Update(context.Background(), resource.UpdateRequest{Plan: plan, State: state}, &updateResp)

	if updateResp.Diagnostics.HasError() {
		t.Fatalf("update failed: %v", updateResp.Diagnostics.Errors())
	}
	if updatedBody.Name == nil || *updatedBody.Name != "updated-pg" {
		t.Error("expected name in update request")
	}
	if updatedBody.Flavor == nil || *updatedBody.Flavor != "db.gp1.large" {
		t.Error("expected flavor in update request")
	}
	if updatedBody.StorageGB == nil || *updatedBody.StorageGB != 100 {
		t.Error("expected storageGb in update request")
	}
}

func TestUpdateAPIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut {
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(map[string]string{"code": "INTERNAL", "message": "boom"})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	r := newResource(newClient(t, server))
	state := buildState(t, PostgresInstanceModel{
		ID: types.StringValue("pg-123"), Name: types.StringValue("old-pg"),
		Version: types.StringValue("16"), Flavor: types.StringValue("db.gp1.small"),
		StorageGB: types.Int64Value(50), VPCID: types.StringValue("vpc-1"),
		SubnetID: types.StringValue("sn-1"), Status: types.StringValue("running"),
		CreatedAt: types.StringValue("2025-01-01T00:00:00Z"),
	})
	plan := buildPlan(t, PostgresInstanceModel{
		ID: types.StringValue("pg-123"), Name: types.StringValue("new-pg"),
		Version: types.StringValue("16"), Flavor: types.StringValue("db.gp1.small"),
		StorageGB: types.Int64Value(50), VPCID: types.StringValue("vpc-1"),
		SubnetID: types.StringValue("sn-1"), Status: types.StringValue("running"),
		CreatedAt: types.StringValue("2025-01-01T00:00:00Z"),
	})
	updateResp := resource.UpdateResponse{State: state}
	r.Update(context.Background(), resource.UpdateRequest{Plan: plan, State: state}, &updateResp)
	if !updateResp.Diagnostics.HasError() {
		t.Error("expected error for API failure on update")
	}
}

func TestDelete(t *testing.T) {
	var deleted atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodDelete && r.URL.Path == "/v1/tenants/t-1/databases/pg-123":
			deleted.Store(true)
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-1/databases/pg-123":
			if deleted.Load() {
				w.WriteHeader(http.StatusNotFound)
				_ = json.NewEncoder(w).Encode(map[string]string{"code": "NOT_FOUND", "message": "not found"})
			} else {
				_ = json.NewEncoder(w).Encode(apiPostgresInstance{ID: "pg-123", Status: "deleting"})
			}
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	r := newResource(newClient(t, server))
	state := buildState(t, PostgresInstanceModel{
		ID: types.StringValue("pg-123"), Name: types.StringValue("my-pg"),
		Version: types.StringValue("16"), Flavor: types.StringValue("db.gp1.small"),
		StorageGB: types.Int64Value(50), VPCID: types.StringValue("vpc-1"),
		SubnetID: types.StringValue("sn-1"), Status: types.StringValue("running"),
		CreatedAt: types.StringValue("2025-01-01T00:00:00Z"),
	})
	deleteResp := resource.DeleteResponse{State: state}
	r.Delete(context.Background(), resource.DeleteRequest{State: state}, &deleteResp)
	if deleteResp.Diagnostics.HasError() {
		t.Fatalf("delete failed: %v", deleteResp.Diagnostics.Errors())
	}
}

func TestDeleteAlreadyGone(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]string{"code": "NOT_FOUND", "message": "not found"})
	}))
	defer server.Close()

	r := newResource(newClient(t, server))
	state := buildState(t, PostgresInstanceModel{
		ID: types.StringValue("pg-gone"), Name: types.StringValue("gone"),
		Version: types.StringValue("16"), Flavor: types.StringValue("db.gp1.small"),
		StorageGB: types.Int64Value(50), VPCID: types.StringValue("vpc-1"),
		SubnetID: types.StringValue("sn-1"), Status: types.StringValue("running"),
		CreatedAt: types.StringValue("2025-01-01T00:00:00Z"),
	})
	deleteResp := resource.DeleteResponse{State: state}
	r.Delete(context.Background(), resource.DeleteRequest{State: state}, &deleteResp)
	if deleteResp.Diagnostics.HasError() {
		t.Fatalf("delete of already-gone instance should not error, got %v", deleteResp.Diagnostics.Errors())
	}
}
