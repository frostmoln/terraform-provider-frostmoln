package mysql_instance

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
)

// --- Model edge cases ---

func TestModelToUpdateRequestAllFields(t *testing.T) {
	plan := MysqlInstanceModel{
		Name:                types.StringValue("new-name"),
		Flavor:              types.StringValue("db.large"),
		StorageGB:           types.Int64Value(200),
		BackupEnabled:       types.BoolValue(true),
		BackupSchedule:      types.StringValue("0 3 * * *"),
		BackupRetentionDays: types.Int64Value(14),
		ParameterGroupID:    types.StringValue("pg-new"),
	}
	state := MysqlInstanceModel{
		Name:                types.StringValue("old-name"),
		Flavor:              types.StringValue("db.small"),
		StorageGB:           types.Int64Value(100),
		BackupEnabled:       types.BoolValue(false),
		BackupSchedule:      types.StringValue("0 2 * * *"),
		BackupRetentionDays: types.Int64Value(7),
		ParameterGroupID:    types.StringValue("pg-old"),
	}

	req := plan.toUpdateRequest(&state)
	if req.Name == nil || *req.Name != "new-name" {
		t.Error("expected name change")
	}
	if req.Flavor == nil || *req.Flavor != "db.large" {
		t.Error("expected flavor change")
	}
	if req.StorageGB == nil || *req.StorageGB != 200 {
		t.Error("expected storage change")
	}
	if req.BackupEnabled == nil || *req.BackupEnabled != true {
		t.Error("expected backup_enabled change")
	}
	if req.BackupSchedule == nil || *req.BackupSchedule != "0 3 * * *" {
		t.Error("expected backup_schedule change")
	}
	if req.BackupRetentionDays == nil || *req.BackupRetentionDays != 14 {
		t.Error("expected backup_retention_days change")
	}
	if req.ParameterGroupID == nil || *req.ParameterGroupID != "pg-new" {
		t.Error("expected parameter_group_id change")
	}
}

func TestModelFromAPIFullyPopulated(t *testing.T) {
	api := &apiMysqlInstance{
		ID:                  "db-1",
		Name:                "db",
		EngineVersion:       "8.0",
		Flavor:              "db.small",
		StorageGB:           100,
		VPCID:               "vpc-1",
		SubnetID:            "sn-1",
		Status:              "running",
		BackupSchedule:      "0 2 * * *",
		BackupRetentionDays: 7,
		ParameterGroupID:    "pg-1",
		PrivateIP:           "10.0.0.5",
		Port:                3306,
		FloatingIP:          "203.0.113.5",
		AdminUsername:       "admin",
		CreatedAt:           "2025-01-01T00:00:00Z",
		UpdatedAt:           "2025-01-02T00:00:00Z",
		TenantID:            "t-1",
	}
	var m MysqlInstanceModel
	m.fromAPI(context.Background(), api, nil)
	if m.FloatingIP.ValueString() != "203.0.113.5" {
		t.Errorf("expected floating ip set, got %s", m.FloatingIP.ValueString())
	}
	if m.BackupSchedule.ValueString() != "0 2 * * *" {
		t.Error("expected backup schedule set")
	}
	if m.BackupRetentionDays.ValueInt64() != 7 {
		t.Error("expected retention days set")
	}
	if m.ParameterGroupID.ValueString() != "pg-1" {
		t.Error("expected parameter group set")
	}
	if m.AdminUsername.ValueString() != "admin" {
		t.Error("expected admin username set")
	}
	if m.TenantID.ValueString() != "t-1" {
		t.Error("expected tenant id set")
	}
}

// --- Resource edge cases ---

func TestGetPollDefaults(t *testing.T) {
	r := &mysqlInstanceResource{}
	if r.getPollInterval() != 5*time.Second {
		t.Errorf("expected default poll interval 5s, got %v", r.getPollInterval())
	}
	if r.getPollTimeout() != 15*time.Minute {
		t.Errorf("expected default poll timeout 15m, got %v", r.getPollTimeout())
	}
}

func TestImportState(t *testing.T) {
	r := NewResource().(resource.ResourceWithImportState)
	resp := resource.ImportStateResponse{State: emptyMysqlInstanceState(t)}
	r.ImportState(context.Background(), resource.ImportStateRequest{ID: "db-import-1"}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("import failed: %v", resp.Diagnostics.Errors())
	}
	var id types.String
	resp.State.GetAttribute(context.Background(), path.Root("id"), &id)
	if id.ValueString() != "db-import-1" {
		t.Errorf("expected imported id db-import-1, got %s", id.ValueString())
	}
}

func mysqlPlan() MysqlInstanceModel {
	return MysqlInstanceModel{
		Name:      types.StringValue("db-1"),
		Version:   types.StringValue("8.0"),
		Flavor:    types.StringValue("db.small"),
		StorageGB: types.Int64Value(100),
		VPCID:     types.StringValue("vpc-1"),
		SubnetID:  types.StringValue("sn-1"),
	}
}

func TestCreateAPIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"code":"INTERNAL","message":"boom"}`))
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key", client.WithHTTPClient(server.Client())) // pragma: allowlist secret
	c.SetTenantIDForTest("t-1")

	r := &mysqlInstanceResource{client: c, pollInterval: 10 * time.Millisecond, pollTimeout: time.Second}
	createResp := resource.CreateResponse{State: emptyMysqlInstanceState(t)}
	r.Create(context.Background(), resource.CreateRequest{Plan: buildMysqlInstancePlan(t, mysqlPlan())}, &createResp)
	if !createResp.Diagnostics.HasError() {
		t.Error("expected error when create POST fails")
	}
}

func TestCreateBadResponseBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte("not-json"))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key", client.WithHTTPClient(server.Client())) // pragma: allowlist secret
	c.SetTenantIDForTest("t-1")

	r := &mysqlInstanceResource{client: c, pollInterval: 10 * time.Millisecond, pollTimeout: time.Second}
	createResp := resource.CreateResponse{State: emptyMysqlInstanceState(t)}
	r.Create(context.Background(), resource.CreateRequest{Plan: buildMysqlInstancePlan(t, mysqlPlan())}, &createResp)
	if !createResp.Diagnostics.HasError() {
		t.Error("expected error when create response body is malformed")
	}
}

func TestCreatePollErrorState(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost:
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id":"db-err","name":"db-1","engineVersion":"8.0","flavor":"db.small","storageGb":100,"vpcId":"vpc-1","subnetId":"sn-1","status":"provisioning","createdAt":"2025-01-01T00:00:00Z"}`))
		case http.MethodGet:
			_, _ = w.Write([]byte(`{"id":"db-err","name":"db-1","engineVersion":"8.0","flavor":"db.small","storageGb":100,"vpcId":"vpc-1","subnetId":"sn-1","status":"error","createdAt":"2025-01-01T00:00:00Z"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key", client.WithHTTPClient(server.Client())) // pragma: allowlist secret
	c.SetTenantIDForTest("t-1")

	r := &mysqlInstanceResource{client: c, pollInterval: 10 * time.Millisecond, pollTimeout: time.Second}
	createResp := resource.CreateResponse{State: emptyMysqlInstanceState(t)}
	r.Create(context.Background(), resource.CreateRequest{Plan: buildMysqlInstancePlan(t, mysqlPlan())}, &createResp)
	if !createResp.Diagnostics.HasError() {
		t.Error("expected error when instance enters error state during polling")
	}
}

func TestReadAPIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"code":"INTERNAL","message":"boom"}`))
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key", client.WithHTTPClient(server.Client())) // pragma: allowlist secret
	c.SetTenantIDForTest("t-1")

	r := &mysqlInstanceResource{client: c}
	state := buildMysqlInstanceState(t, MysqlInstanceModel{
		ID:        types.StringValue("db-1"),
		Name:      types.StringValue("db-1"),
		Version:   types.StringValue("8.0"),
		Flavor:    types.StringValue("db.small"),
		StorageGB: types.Int64Value(100),
		VPCID:     types.StringValue("vpc-1"),
		SubnetID:  types.StringValue("sn-1"),
		Status:    types.StringValue("running"),
		CreatedAt: types.StringValue("2025-01-01T00:00:00Z"),
	})
	readResp := resource.ReadResponse{State: state}
	r.Read(context.Background(), resource.ReadRequest{State: state}, &readResp)
	if !readResp.Diagnostics.HasError() {
		t.Error("expected error when read GET returns 500")
	}
}

func TestUpdateAPIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"code":"INTERNAL","message":"boom"}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key", client.WithHTTPClient(server.Client())) // pragma: allowlist secret
	c.SetTenantIDForTest("t-1")

	r := &mysqlInstanceResource{client: c, pollInterval: 10 * time.Millisecond, pollTimeout: time.Second}
	state := buildMysqlInstanceState(t, MysqlInstanceModel{
		ID:        types.StringValue("db-1"),
		Name:      types.StringValue("old"),
		Version:   types.StringValue("8.0"),
		Flavor:    types.StringValue("db.small"),
		StorageGB: types.Int64Value(100),
		VPCID:     types.StringValue("vpc-1"),
		SubnetID:  types.StringValue("sn-1"),
		Status:    types.StringValue("running"),
		CreatedAt: types.StringValue("2025-01-01T00:00:00Z"),
	})
	plan := buildMysqlInstancePlan(t, MysqlInstanceModel{
		ID:        types.StringValue("db-1"),
		Name:      types.StringValue("new"),
		Version:   types.StringValue("8.0"),
		Flavor:    types.StringValue("db.small"),
		StorageGB: types.Int64Value(100),
		VPCID:     types.StringValue("vpc-1"),
		SubnetID:  types.StringValue("sn-1"),
		Status:    types.StringValue("running"),
		CreatedAt: types.StringValue("2025-01-01T00:00:00Z"),
	})
	updateResp := resource.UpdateResponse{State: state}
	r.Update(context.Background(), resource.UpdateRequest{Plan: plan, State: state}, &updateResp)
	if !updateResp.Diagnostics.HasError() {
		t.Error("expected error when update PUT returns 500")
	}
}

func TestDeleteAlreadyGone(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"code":"NOT_FOUND","message":"gone"}`))
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key", client.WithHTTPClient(server.Client())) // pragma: allowlist secret
	c.SetTenantIDForTest("t-1")

	r := &mysqlInstanceResource{client: c, pollInterval: 10 * time.Millisecond, pollTimeout: time.Second}
	state := buildMysqlInstanceState(t, MysqlInstanceModel{
		ID:        types.StringValue("db-gone"),
		Name:      types.StringValue("db-1"),
		Version:   types.StringValue("8.0"),
		Flavor:    types.StringValue("db.small"),
		StorageGB: types.Int64Value(100),
		VPCID:     types.StringValue("vpc-1"),
		SubnetID:  types.StringValue("sn-1"),
		Status:    types.StringValue("running"),
		CreatedAt: types.StringValue("2025-01-01T00:00:00Z"),
	})
	deleteResp := resource.DeleteResponse{State: state}
	r.Delete(context.Background(), resource.DeleteRequest{State: state}, &deleteResp)
	if deleteResp.Diagnostics.HasError() {
		t.Fatalf("expected no error deleting already-gone instance, got %v", deleteResp.Diagnostics.Errors())
	}
}

func TestDeleteAPIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"code":"INTERNAL","message":"boom"}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key", client.WithHTTPClient(server.Client())) // pragma: allowlist secret
	c.SetTenantIDForTest("t-1")

	r := &mysqlInstanceResource{client: c, pollInterval: 10 * time.Millisecond, pollTimeout: time.Second}
	state := buildMysqlInstanceState(t, MysqlInstanceModel{
		ID:        types.StringValue("db-1"),
		Name:      types.StringValue("db-1"),
		Version:   types.StringValue("8.0"),
		Flavor:    types.StringValue("db.small"),
		StorageGB: types.Int64Value(100),
		VPCID:     types.StringValue("vpc-1"),
		SubnetID:  types.StringValue("sn-1"),
		Status:    types.StringValue("running"),
		CreatedAt: types.StringValue("2025-01-01T00:00:00Z"),
	})
	deleteResp := resource.DeleteResponse{State: state}
	r.Delete(context.Background(), resource.DeleteRequest{State: state}, &deleteResp)
	if !deleteResp.Diagnostics.HasError() {
		t.Error("expected error when delete returns 500")
	}
}
