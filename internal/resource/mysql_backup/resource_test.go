package mysql_backup

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

	"git.nl.cloud/NordicLight/terraform-provider-frostmoln/internal/client"
)

// --- Model unit tests ---

func TestMysqlBackupModelToCreateRequest(t *testing.T) {
	ctx := context.Background()
	diags := diag.Diagnostics{}

	model := MysqlBackupModel{
		InstanceID: types.StringValue("db-123"),
		Name:       types.StringValue("daily-backup"),
		Type:       types.StringValue("full"),
	}

	req := model.toCreateRequest(ctx, &diags)
	if diags.HasError() {
		t.Fatalf("unexpected diagnostics: %v", diags.Errors())
	}

	if req.Name != "daily-backup" {
		t.Errorf("expected name daily-backup, got %s", req.Name)
	}
	if req.Type != "full" {
		t.Errorf("expected type full, got %s", req.Type)
	}
}

func TestMysqlBackupModelToCreateRequestBinlog(t *testing.T) {
	ctx := context.Background()
	diags := diag.Diagnostics{}

	model := MysqlBackupModel{
		InstanceID: types.StringValue("db-123"),
		Name:       types.StringValue("binlog-backup"),
		Type:       types.StringValue("binlog"),
	}

	req := model.toCreateRequest(ctx, &diags)
	if diags.HasError() {
		t.Fatalf("unexpected diagnostics: %v", diags.Errors())
	}

	if req.Type != "binlog" {
		t.Errorf("expected type binlog, got %s", req.Type)
	}
}

func TestMysqlBackupModelToCreateRequestNoType(t *testing.T) {
	ctx := context.Background()
	diags := diag.Diagnostics{}

	model := MysqlBackupModel{
		InstanceID: types.StringValue("db-123"),
		Name:       types.StringValue("auto-backup"),
		Type:       types.StringNull(),
	}

	req := model.toCreateRequest(ctx, &diags)
	if diags.HasError() {
		t.Fatalf("unexpected diagnostics: %v", diags.Errors())
	}

	if req.Type != "" {
		t.Errorf("expected empty type, got %s", req.Type)
	}
}

func TestMysqlBackupModelFromAPI(t *testing.T) {
	ctx := context.Background()
	diags := diag.Diagnostics{}

	api := &apiMysqlBackup{
		ID:          "bk-123",
		InstanceID:  "db-456",
		Name:        "daily-backup",
		Type:        "full",
		Status:      "completed",
		SizeBytes:   1073741824,
		StartedAt:   "2025-01-01T02:00:00Z",
		CompletedAt: "2025-01-01T02:15:00Z",
	}

	var model MysqlBackupModel
	model.fromAPI(ctx, api, &diags)
	if diags.HasError() {
		t.Fatalf("unexpected diagnostics: %v", diags.Errors())
	}

	if model.ID.ValueString() != "bk-123" {
		t.Errorf("expected ID bk-123, got %s", model.ID.ValueString())
	}
	if model.Type.ValueString() != "full" {
		t.Errorf("expected type full, got %s", model.Type.ValueString())
	}
	if model.SizeBytes.ValueInt64() != 1073741824 {
		t.Errorf("expected size 1073741824, got %d", model.SizeBytes.ValueInt64())
	}
}

func TestMysqlBackupModelFromAPINulls(t *testing.T) {
	ctx := context.Background()
	diags := diag.Diagnostics{}

	api := &apiMysqlBackup{
		ID:         "bk-123",
		InstanceID: "db-456",
		Name:       "backup",
		Status:     "in_progress",
	}

	var model MysqlBackupModel
	model.fromAPI(ctx, api, &diags)
	if diags.HasError() {
		t.Fatalf("unexpected diagnostics: %v", diags.Errors())
	}

	if !model.Type.IsNull() {
		t.Error("expected null type")
	}
	if !model.SizeBytes.IsNull() {
		t.Error("expected null size_bytes")
	}
	if !model.StartedAt.IsNull() {
		t.Error("expected null started_at")
	}
	if !model.CompletedAt.IsNull() {
		t.Error("expected null completed_at")
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
	if resp.TypeName != "frostmoln_mysql_backup" {
		t.Errorf("expected type name frostmoln_mysql_backup, got %s", resp.TypeName)
	}
}

func TestSchema(t *testing.T) {
	r := NewResource()
	req := resource.SchemaRequest{}
	var resp resource.SchemaResponse
	r.Schema(context.Background(), req, &resp)

	for _, attr := range []string{"id", "instance_id", "name", "type", "status", "size_bytes", "started_at", "completed_at"} {
		if _, ok := resp.Schema.Attributes[attr]; !ok {
			t.Errorf("expected attribute %s in schema", attr)
		}
	}
}

func buildMysqlBackupState(t *testing.T, model MysqlBackupModel) tfsdk.State {
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

func buildMysqlBackupPlan(t *testing.T, model MysqlBackupModel) tfsdk.Plan {
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

func emptyMysqlBackupState(t *testing.T) tfsdk.State {
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
		case r.Method == http.MethodPost && r.URL.Path == "/v1/tenants/t-1/databases/db-123/backups":
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(apiMysqlBackup{
				ID:         "bk-new",
				InstanceID: "db-123",
				Name:       "test-backup",
				Type:       "full",
				Status:     "in_progress",
				StartedAt:  "2025-01-01T02:00:00Z",
			})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-1/databases/db-123/backups/bk-new":
			count := callCount.Add(1)
			status := "in_progress"
			if count >= 2 {
				status = "completed"
			}
			_ = json.NewEncoder(w).Encode(apiMysqlBackup{
				ID:          "bk-new",
				InstanceID:  "db-123",
				Name:        "test-backup",
				Type:        "full",
				Status:      status,
				SizeBytes:   524288000,
				StartedAt:   "2025-01-01T02:00:00Z",
				CompletedAt: "2025-01-01T02:10:00Z",
			})
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key", client.WithHTTPClient(server.Client()))
	c.SetTenantIDForTest("t-1")

	r := &mysqlBackupResource{
		client:       c,
		pollInterval: 10 * time.Millisecond,
		pollTimeout:  5 * time.Second,
	}

	plan := buildMysqlBackupPlan(t, MysqlBackupModel{
		InstanceID: types.StringValue("db-123"),
		Name:       types.StringValue("test-backup"),
		Type:       types.StringValue("full"),
	})

	createResp := resource.CreateResponse{State: emptyMysqlBackupState(t)}
	r.Create(context.Background(), resource.CreateRequest{Plan: plan}, &createResp)

	if createResp.Diagnostics.HasError() {
		t.Fatalf("create failed: %v", createResp.Diagnostics.Errors())
	}

	var result MysqlBackupModel
	createResp.State.Get(context.Background(), &result)
	if result.ID.ValueString() != "bk-new" {
		t.Errorf("expected ID bk-new, got %s", result.ID.ValueString())
	}
	if result.Status.ValueString() != "completed" {
		t.Errorf("expected status completed, got %s", result.Status.ValueString())
	}
}

func TestRead(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-1/databases/db-123/backups/bk-456" {
			_ = json.NewEncoder(w).Encode(apiMysqlBackup{
				ID:          "bk-456",
				InstanceID:  "db-123",
				Name:        "daily",
				Type:        "full",
				Status:      "completed",
				SizeBytes:   1000000,
				StartedAt:   "2025-01-01T02:00:00Z",
				CompletedAt: "2025-01-01T02:10:00Z",
			})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key", client.WithHTTPClient(server.Client()))
	c.SetTenantIDForTest("t-1")

	r := &mysqlBackupResource{client: c}

	state := buildMysqlBackupState(t, MysqlBackupModel{
		ID:         types.StringValue("bk-456"),
		InstanceID: types.StringValue("db-123"),
		Name:       types.StringValue("daily"),
		Status:     types.StringValue("completed"),
	})

	readResp := resource.ReadResponse{State: state}
	r.Read(context.Background(), resource.ReadRequest{State: state}, &readResp)

	if readResp.Diagnostics.HasError() {
		t.Fatalf("read failed: %v", readResp.Diagnostics.Errors())
	}
}

func TestDelete(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete && r.URL.Path == "/v1/tenants/t-1/databases/db-123/backups/bk-456" {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key", client.WithHTTPClient(server.Client()))
	c.SetTenantIDForTest("t-1")

	r := &mysqlBackupResource{client: c}

	state := buildMysqlBackupState(t, MysqlBackupModel{
		ID:         types.StringValue("bk-456"),
		InstanceID: types.StringValue("db-123"),
		Name:       types.StringValue("daily"),
		Status:     types.StringValue("completed"),
	})

	deleteResp := resource.DeleteResponse{State: state}
	r.Delete(context.Background(), resource.DeleteRequest{State: state}, &deleteResp)

	if deleteResp.Diagnostics.HasError() {
		t.Fatalf("delete failed: %v", deleteResp.Diagnostics.Errors())
	}
}

func TestUpdateNotSupported(t *testing.T) {
	r := &mysqlBackupResource{}
	var resp resource.UpdateResponse
	r.Update(context.Background(), resource.UpdateRequest{}, &resp)
	if !resp.Diagnostics.HasError() {
		t.Error("expected error for update on immutable resource")
	}
}
