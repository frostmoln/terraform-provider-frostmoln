package mysql_backup

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

func TestConfigureNilProviderData(t *testing.T) {
	r := &mysqlBackupResource{}
	var resp resource.ConfigureResponse
	r.Configure(context.Background(), resource.ConfigureRequest{}, &resp)
	if resp.Diagnostics.HasError() {
		t.Errorf("expected no error for nil provider data, got %v", resp.Diagnostics.Errors())
	}
	if r.client != nil {
		t.Error("expected client to stay nil")
	}
}

func TestConfigureWrongType(t *testing.T) {
	r := &mysqlBackupResource{}
	var resp resource.ConfigureResponse
	r.Configure(context.Background(), resource.ConfigureRequest{ProviderData: "not-a-client"}, &resp)
	if !resp.Diagnostics.HasError() {
		t.Error("expected error for wrong provider data type")
	}
}

func TestConfigureValidClient(t *testing.T) {
	r := &mysqlBackupResource{}
	c := client.NewClient("http://example.invalid", "test-key") // pragma: allowlist secret
	var resp resource.ConfigureResponse
	r.Configure(context.Background(), resource.ConfigureRequest{ProviderData: c}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected error: %v", resp.Diagnostics.Errors())
	}
	if r.client != c {
		t.Error("expected client to be set")
	}
}

func TestGetPollDefaults(t *testing.T) {
	r := &mysqlBackupResource{}
	if r.getPollInterval() != 5*time.Second {
		t.Errorf("expected default poll interval 5s, got %v", r.getPollInterval())
	}
	if r.getPollTimeout() != 30*time.Minute {
		t.Errorf("expected default poll timeout 30m, got %v", r.getPollTimeout())
	}
}

func TestImportState(t *testing.T) {
	r := NewResource().(resource.ResourceWithImportState)
	resp := resource.ImportStateResponse{State: emptyMysqlBackupState(t)}
	r.ImportState(context.Background(), resource.ImportStateRequest{ID: "bkp-import-1"}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("import failed: %v", resp.Diagnostics.Errors())
	}
	var id types.String
	resp.State.GetAttribute(context.Background(), path.Root("id"), &id)
	if id.ValueString() != "bkp-import-1" {
		t.Errorf("expected imported id bkp-import-1, got %s", id.ValueString())
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

	r := &mysqlBackupResource{client: c, pollInterval: 10 * time.Millisecond, pollTimeout: time.Second}
	plan := buildMysqlBackupPlan(t, MysqlBackupModel{
		InstanceID: types.StringValue("db-123"),
		Name:       types.StringValue("backup-1"),
	})
	createResp := resource.CreateResponse{State: emptyMysqlBackupState(t)}
	r.Create(context.Background(), resource.CreateRequest{Plan: plan}, &createResp)
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

	r := &mysqlBackupResource{client: c, pollInterval: 10 * time.Millisecond, pollTimeout: time.Second}
	plan := buildMysqlBackupPlan(t, MysqlBackupModel{
		InstanceID: types.StringValue("db-123"),
		Name:       types.StringValue("backup-1"),
	})
	createResp := resource.CreateResponse{State: emptyMysqlBackupState(t)}
	r.Create(context.Background(), resource.CreateRequest{Plan: plan}, &createResp)
	if !createResp.Diagnostics.HasError() {
		t.Error("expected error when create response body is malformed")
	}
}

func TestCreatePollErrorState(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost:
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id":"bkp-err","instanceId":"db-123","name":"backup-1","status":"creating"}`))
		case http.MethodGet:
			_, _ = w.Write([]byte(`{"id":"bkp-err","instanceId":"db-123","name":"backup-1","status":"failed"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key", client.WithHTTPClient(server.Client())) // pragma: allowlist secret
	c.SetTenantIDForTest("t-1")

	r := &mysqlBackupResource{client: c, pollInterval: 10 * time.Millisecond, pollTimeout: time.Second}
	plan := buildMysqlBackupPlan(t, MysqlBackupModel{
		InstanceID: types.StringValue("db-123"),
		Name:       types.StringValue("backup-1"),
	})
	createResp := resource.CreateResponse{State: emptyMysqlBackupState(t)}
	r.Create(context.Background(), resource.CreateRequest{Plan: plan}, &createResp)
	if !createResp.Diagnostics.HasError() {
		t.Error("expected error when backup enters failed state during polling")
	}
}

func TestCreateRefreshError(t *testing.T) {
	var getCount int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost:
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id":"bkp-ref","instanceId":"db-123","name":"backup-1","status":"creating"}`))
		case http.MethodGet:
			getCount++
			if getCount == 1 {
				_, _ = w.Write([]byte(`{"id":"bkp-ref","instanceId":"db-123","name":"backup-1","status":"completed"}`))
				return
			}
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"code":"INTERNAL","message":"boom"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key", client.WithHTTPClient(server.Client())) // pragma: allowlist secret
	c.SetTenantIDForTest("t-1")

	r := &mysqlBackupResource{client: c, pollInterval: 10 * time.Millisecond, pollTimeout: time.Second}
	plan := buildMysqlBackupPlan(t, MysqlBackupModel{
		InstanceID: types.StringValue("db-123"),
		Name:       types.StringValue("backup-1"),
	})
	createResp := resource.CreateResponse{State: emptyMysqlBackupState(t)}
	r.Create(context.Background(), resource.CreateRequest{Plan: plan}, &createResp)
	if !createResp.Diagnostics.HasError() {
		t.Error("expected error when post-poll refresh read fails")
	}
}

func TestReadNotFoundRemovesState(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"code":"NOT_FOUND","message":"gone"}`))
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key", client.WithHTTPClient(server.Client())) // pragma: allowlist secret
	c.SetTenantIDForTest("t-1")

	r := &mysqlBackupResource{client: c}
	state := buildMysqlBackupState(t, MysqlBackupModel{
		ID:         types.StringValue("bkp-gone"),
		InstanceID: types.StringValue("db-123"),
		Name:       types.StringValue("backup-1"),
		Status:     types.StringValue("completed"),
	})
	readResp := resource.ReadResponse{State: state}
	r.Read(context.Background(), resource.ReadRequest{State: state}, &readResp)
	if readResp.Diagnostics.HasError() {
		t.Fatalf("expected no error for 404, got %v", readResp.Diagnostics.Errors())
	}
	var result MysqlBackupModel
	diags := readResp.State.Get(context.Background(), &result)
	if !diags.HasError() && result.ID.ValueString() != "" {
		t.Error("expected state to be removed after 404")
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

	r := &mysqlBackupResource{client: c}
	state := buildMysqlBackupState(t, MysqlBackupModel{
		ID:         types.StringValue("bkp-456"),
		InstanceID: types.StringValue("db-123"),
		Name:       types.StringValue("backup-1"),
		Status:     types.StringValue("completed"),
	})
	readResp := resource.ReadResponse{State: state}
	r.Read(context.Background(), resource.ReadRequest{State: state}, &readResp)
	if !readResp.Diagnostics.HasError() {
		t.Error("expected error when read GET returns 500")
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

	r := &mysqlBackupResource{client: c}
	state := buildMysqlBackupState(t, MysqlBackupModel{
		ID:         types.StringValue("bkp-gone"),
		InstanceID: types.StringValue("db-123"),
		Name:       types.StringValue("backup-1"),
		Status:     types.StringValue("completed"),
	})
	deleteResp := resource.DeleteResponse{State: state}
	r.Delete(context.Background(), resource.DeleteRequest{State: state}, &deleteResp)
	if deleteResp.Diagnostics.HasError() {
		t.Fatalf("expected no error deleting already-gone backup, got %v", deleteResp.Diagnostics.Errors())
	}
}

func TestDeleteAPIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"code":"INTERNAL","message":"boom"}`))
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key", client.WithHTTPClient(server.Client())) // pragma: allowlist secret
	c.SetTenantIDForTest("t-1")

	r := &mysqlBackupResource{client: c}
	state := buildMysqlBackupState(t, MysqlBackupModel{
		ID:         types.StringValue("bkp-456"),
		InstanceID: types.StringValue("db-123"),
		Name:       types.StringValue("backup-1"),
		Status:     types.StringValue("completed"),
	})
	deleteResp := resource.DeleteResponse{State: state}
	r.Delete(context.Background(), resource.DeleteRequest{State: state}, &deleteResp)
	if !deleteResp.Diagnostics.HasError() {
		t.Error("expected error when delete returns 500")
	}
}
