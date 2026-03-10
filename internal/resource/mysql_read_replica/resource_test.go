package mysql_read_replica

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

func TestMysqlReadReplicaModelToCreateRequest(t *testing.T) {
	ctx := context.Background()
	diags := diag.Diagnostics{}

	model := MysqlReadReplicaModel{
		InstanceID: types.StringValue("db-123"),
		Name:       types.StringValue("replica-1"),
	}

	req := model.toCreateRequest(ctx, &diags)
	if diags.HasError() {
		t.Fatalf("unexpected diagnostics: %v", diags.Errors())
	}

	if req.Name != "replica-1" {
		t.Errorf("expected name replica-1, got %s", req.Name)
	}
}

func TestMysqlReadReplicaModelFromAPI(t *testing.T) {
	ctx := context.Background()
	diags := diag.Diagnostics{}

	api := &apiMysqlReadReplica{
		ID:                  "rep-123",
		InstanceID:          "db-456",
		Name:                "replica-1",
		Status:              "running",
		PrivateIP:           "10.0.1.10",
		Port:                3306,
		ReplicationLagBytes: 1024,
	}

	var model MysqlReadReplicaModel
	model.fromAPI(ctx, api, &diags)
	if diags.HasError() {
		t.Fatalf("unexpected diagnostics: %v", diags.Errors())
	}

	if model.ID.ValueString() != "rep-123" {
		t.Errorf("expected ID rep-123, got %s", model.ID.ValueString())
	}
	if model.Port.ValueInt64() != 3306 {
		t.Errorf("expected port 3306, got %d", model.Port.ValueInt64())
	}
	if model.ReplicationLagBytes.ValueInt64() != 1024 {
		t.Errorf("expected replication lag 1024, got %d", model.ReplicationLagBytes.ValueInt64())
	}
}

func TestMysqlReadReplicaModelFromAPINulls(t *testing.T) {
	ctx := context.Background()
	diags := diag.Diagnostics{}

	api := &apiMysqlReadReplica{
		ID:         "rep-123",
		InstanceID: "db-456",
		Name:       "replica-1",
		Status:     "provisioning",
	}

	var model MysqlReadReplicaModel
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
	if !model.ReplicationLagBytes.IsNull() {
		t.Error("expected null replication_lag_bytes")
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
	if resp.TypeName != "frostmoln_mysql_read_replica" {
		t.Errorf("expected type name frostmoln_mysql_read_replica, got %s", resp.TypeName)
	}
}

func TestSchema(t *testing.T) {
	r := NewResource()
	req := resource.SchemaRequest{}
	var resp resource.SchemaResponse
	r.Schema(context.Background(), req, &resp)

	for _, attr := range []string{"id", "instance_id", "name", "status", "private_ip", "port", "replication_lag_bytes"} {
		if _, ok := resp.Schema.Attributes[attr]; !ok {
			t.Errorf("expected attribute %s in schema", attr)
		}
	}
}

func buildMysqlReplicaState(t *testing.T, model MysqlReadReplicaModel) tfsdk.State {
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

func buildMysqlReplicaPlan(t *testing.T, model MysqlReadReplicaModel) tfsdk.Plan {
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

func emptyMysqlReplicaState(t *testing.T) tfsdk.State {
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
		case r.Method == http.MethodPost && r.URL.Path == "/v1/tenants/t-1/databases/db-123/replicas":
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(apiMysqlReadReplica{
				ID:         "rep-new",
				InstanceID: "db-123",
				Name:       "replica-1",
				Status:     "provisioning",
			})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-1/databases/db-123/replicas/rep-new":
			count := callCount.Add(1)
			status := "provisioning"
			if count >= 2 {
				status = "running"
			}
			_ = json.NewEncoder(w).Encode(apiMysqlReadReplica{
				ID:         "rep-new",
				InstanceID: "db-123",
				Name:       "replica-1",
				Status:     status,
				PrivateIP:  "10.0.1.10",
				Port:       3306,
			})
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key", client.WithHTTPClient(server.Client()))
	c.SetTenantIDForTest("t-1")

	r := &mysqlReadReplicaResource{
		client:       c,
		pollInterval: 10 * time.Millisecond,
		pollTimeout:  5 * time.Second,
	}

	plan := buildMysqlReplicaPlan(t, MysqlReadReplicaModel{
		InstanceID: types.StringValue("db-123"),
		Name:       types.StringValue("replica-1"),
	})

	createResp := resource.CreateResponse{State: emptyMysqlReplicaState(t)}
	r.Create(context.Background(), resource.CreateRequest{Plan: plan}, &createResp)

	if createResp.Diagnostics.HasError() {
		t.Fatalf("create failed: %v", createResp.Diagnostics.Errors())
	}

	var result MysqlReadReplicaModel
	createResp.State.Get(context.Background(), &result)
	if result.ID.ValueString() != "rep-new" {
		t.Errorf("expected ID rep-new, got %s", result.ID.ValueString())
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
		if r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-1/databases/db-123/replicas/rep-456" {
			_ = json.NewEncoder(w).Encode(apiMysqlReadReplica{
				ID:         "rep-456",
				InstanceID: "db-123",
				Name:       "replica-1",
				Status:     "running",
				PrivateIP:  "10.0.1.10",
				Port:       3306,
			})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key", client.WithHTTPClient(server.Client()))
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

	if readResp.Diagnostics.HasError() {
		t.Fatalf("read failed: %v", readResp.Diagnostics.Errors())
	}
}

func TestDelete(t *testing.T) {
	deleted := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodDelete && r.URL.Path == "/v1/tenants/t-1/databases/db-123/replicas/rep-456":
			deleted = true
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-1/databases/db-123/replicas/rep-456":
			if deleted {
				w.WriteHeader(http.StatusNotFound)
				_ = json.NewEncoder(w).Encode(map[string]interface{}{
					"code": "NOT_FOUND", "message": "not found",
				})
			} else {
				_ = json.NewEncoder(w).Encode(apiMysqlReadReplica{
					ID: "rep-456", Status: "deleting",
				})
			}
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key", client.WithHTTPClient(server.Client()))
	c.SetTenantIDForTest("t-1")

	r := &mysqlReadReplicaResource{
		client:       c,
		pollInterval: 10 * time.Millisecond,
		pollTimeout:  5 * time.Second,
	}

	state := buildMysqlReplicaState(t, MysqlReadReplicaModel{
		ID:         types.StringValue("rep-456"),
		InstanceID: types.StringValue("db-123"),
		Name:       types.StringValue("replica-1"),
		Status:     types.StringValue("running"),
	})

	deleteResp := resource.DeleteResponse{State: state}
	r.Delete(context.Background(), resource.DeleteRequest{State: state}, &deleteResp)

	if deleteResp.Diagnostics.HasError() {
		t.Fatalf("delete failed: %v", deleteResp.Diagnostics.Errors())
	}
}

func TestUpdateNotSupported(t *testing.T) {
	r := &mysqlReadReplicaResource{}
	var resp resource.UpdateResponse
	r.Update(context.Background(), resource.UpdateRequest{}, &resp)
	if !resp.Diagnostics.HasError() {
		t.Error("expected error for update on immutable resource")
	}
}
