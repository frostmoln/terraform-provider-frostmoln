package mysql_read_replica

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
	r := &mysqlReadReplicaResource{}
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
	r := &mysqlReadReplicaResource{}
	var resp resource.ConfigureResponse
	r.Configure(context.Background(), resource.ConfigureRequest{ProviderData: "not-a-client"}, &resp)
	if !resp.Diagnostics.HasError() {
		t.Error("expected error for wrong provider data type")
	}
}

func TestConfigureValidClient(t *testing.T) {
	r := &mysqlReadReplicaResource{}
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
	r := &mysqlReadReplicaResource{}
	if r.getPollInterval() != 5*time.Second {
		t.Errorf("expected default poll interval 5s, got %v", r.getPollInterval())
	}
	if r.getPollTimeout() != 15*time.Minute {
		t.Errorf("expected default poll timeout 15m, got %v", r.getPollTimeout())
	}
}

func TestImportState(t *testing.T) {
	r := NewResource().(resource.ResourceWithImportState)
	var schemaResp resource.SchemaResponse
	r.(resource.Resource).Schema(context.Background(), resource.SchemaRequest{}, &schemaResp)

	resp := resource.ImportStateResponse{State: emptyMysqlReplicaState(t)}
	r.ImportState(context.Background(), resource.ImportStateRequest{ID: "rep-import-1"}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("import failed: %v", resp.Diagnostics.Errors())
	}

	var id types.String
	resp.State.GetAttribute(context.Background(), path.Root("id"), &id)
	if id.ValueString() != "rep-import-1" {
		t.Errorf("expected imported id rep-import-1, got %s", id.ValueString())
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

	r := &mysqlReadReplicaResource{client: c, pollInterval: 10 * time.Millisecond, pollTimeout: time.Second}
	plan := buildMysqlReplicaPlan(t, MysqlReadReplicaModel{
		InstanceID: types.StringValue("db-123"),
		Name:       types.StringValue("replica-1"),
	})
	createResp := resource.CreateResponse{State: emptyMysqlReplicaState(t)}
	r.Create(context.Background(), resource.CreateRequest{Plan: plan}, &createResp)
	if !createResp.Diagnostics.HasError() {
		t.Error("expected error when create POST fails")
	}
}

func TestCreatePollErrorState(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost:
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id":"rep-err","instanceId":"db-123","name":"replica-1","status":"provisioning"}`))
		case http.MethodGet:
			_, _ = w.Write([]byte(`{"id":"rep-err","instanceId":"db-123","name":"replica-1","status":"error"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key", client.WithHTTPClient(server.Client())) // pragma: allowlist secret
	c.SetTenantIDForTest("t-1")

	r := &mysqlReadReplicaResource{client: c, pollInterval: 10 * time.Millisecond, pollTimeout: time.Second}
	plan := buildMysqlReplicaPlan(t, MysqlReadReplicaModel{
		InstanceID: types.StringValue("db-123"),
		Name:       types.StringValue("replica-1"),
	})
	createResp := resource.CreateResponse{State: emptyMysqlReplicaState(t)}
	r.Create(context.Background(), resource.CreateRequest{Plan: plan}, &createResp)
	if !createResp.Diagnostics.HasError() {
		t.Error("expected error when replica enters error state during polling")
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

	r := &mysqlReadReplicaResource{client: c, pollInterval: 10 * time.Millisecond, pollTimeout: time.Second}
	plan := buildMysqlReplicaPlan(t, MysqlReadReplicaModel{
		InstanceID: types.StringValue("db-123"),
		Name:       types.StringValue("replica-1"),
	})
	createResp := resource.CreateResponse{State: emptyMysqlReplicaState(t)}
	r.Create(context.Background(), resource.CreateRequest{Plan: plan}, &createResp)
	if !createResp.Diagnostics.HasError() {
		t.Error("expected error when create response body is malformed")
	}
}

func TestCreateRefreshError(t *testing.T) {
	var getCount int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost:
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id":"rep-ref","instanceId":"db-123","name":"replica-1","status":"provisioning"}`))
		case http.MethodGet:
			getCount++
			if getCount == 1 {
				// First poll: running, so polling succeeds.
				_, _ = w.Write([]byte(`{"id":"rep-ref","instanceId":"db-123","name":"replica-1","status":"running"}`))
				return
			}
			// Subsequent refresh read fails.
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"code":"INTERNAL","message":"boom"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key", client.WithHTTPClient(server.Client())) // pragma: allowlist secret
	c.SetTenantIDForTest("t-1")

	r := &mysqlReadReplicaResource{client: c, pollInterval: 10 * time.Millisecond, pollTimeout: time.Second}
	plan := buildMysqlReplicaPlan(t, MysqlReadReplicaModel{
		InstanceID: types.StringValue("db-123"),
		Name:       types.StringValue("replica-1"),
	})
	createResp := resource.CreateResponse{State: emptyMysqlReplicaState(t)}
	r.Create(context.Background(), resource.CreateRequest{Plan: plan}, &createResp)
	if !createResp.Diagnostics.HasError() {
		t.Error("expected error when post-poll refresh read fails")
	}
}

func TestDeletePollError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodDelete:
			w.WriteHeader(http.StatusNoContent)
		case http.MethodGet:
			// Non-404 error during delete-poll -> propagates as error.
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"code":"INTERNAL","message":"boom"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key", client.WithHTTPClient(server.Client())) // pragma: allowlist secret
	c.SetTenantIDForTest("t-1")

	r := &mysqlReadReplicaResource{client: c, pollInterval: 10 * time.Millisecond, pollTimeout: 200 * time.Millisecond}
	state := buildMysqlReplicaState(t, MysqlReadReplicaModel{
		ID:         types.StringValue("rep-456"),
		InstanceID: types.StringValue("db-123"),
		Name:       types.StringValue("replica-1"),
		Status:     types.StringValue("running"),
	})
	deleteResp := resource.DeleteResponse{State: state}
	r.Delete(context.Background(), resource.DeleteRequest{State: state}, &deleteResp)
	if !deleteResp.Diagnostics.HasError() {
		t.Error("expected error when delete poll keeps returning 500")
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

	r := &mysqlReadReplicaResource{client: c}
	state := buildMysqlReplicaState(t, MysqlReadReplicaModel{
		ID:         types.StringValue("rep-gone"),
		InstanceID: types.StringValue("db-123"),
		Name:       types.StringValue("replica-1"),
		Status:     types.StringValue("running"),
	})
	readResp := resource.ReadResponse{State: state}
	r.Read(context.Background(), resource.ReadRequest{State: state}, &readResp)
	if readResp.Diagnostics.HasError() {
		t.Fatalf("expected no error for 404, got %v", readResp.Diagnostics.Errors())
	}
	var result MysqlReadReplicaModel
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

	r := &mysqlReadReplicaResource{client: c}
	state := buildMysqlReplicaState(t, MysqlReadReplicaModel{
		ID:         types.StringValue("rep-456"),
		InstanceID: types.StringValue("db-123"),
		Name:       types.StringValue("replica-1"),
		Status:     types.StringValue("running"),
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

	r := &mysqlReadReplicaResource{client: c, pollInterval: 10 * time.Millisecond, pollTimeout: time.Second}
	state := buildMysqlReplicaState(t, MysqlReadReplicaModel{
		ID:         types.StringValue("rep-gone"),
		InstanceID: types.StringValue("db-123"),
		Name:       types.StringValue("replica-1"),
		Status:     types.StringValue("running"),
	})
	deleteResp := resource.DeleteResponse{State: state}
	r.Delete(context.Background(), resource.DeleteRequest{State: state}, &deleteResp)
	if deleteResp.Diagnostics.HasError() {
		t.Fatalf("expected no error deleting already-gone replica, got %v", deleteResp.Diagnostics.Errors())
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

	r := &mysqlReadReplicaResource{client: c, pollInterval: 10 * time.Millisecond, pollTimeout: time.Second}
	state := buildMysqlReplicaState(t, MysqlReadReplicaModel{
		ID:         types.StringValue("rep-456"),
		InstanceID: types.StringValue("db-123"),
		Name:       types.StringValue("replica-1"),
		Status:     types.StringValue("running"),
	})
	deleteResp := resource.DeleteResponse{State: state}
	r.Delete(context.Background(), resource.DeleteRequest{State: state}, &deleteResp)
	if !deleteResp.Diagnostics.HasError() {
		t.Error("expected error when delete returns 500")
	}
}
