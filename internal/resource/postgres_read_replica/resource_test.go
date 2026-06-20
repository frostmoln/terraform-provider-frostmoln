package postgres_read_replica

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

func TestPostgresReadReplicaModelToCreateRequest(t *testing.T) {
	ctx := context.Background()
	diags := diag.Diagnostics{}
	model := PostgresReadReplicaModel{Name: types.StringValue("replica-1")}
	req := model.toCreateRequest(ctx, &diags)
	if diags.HasError() {
		t.Fatalf("unexpected diagnostics: %v", diags.Errors())
	}
	if req.Name != "replica-1" {
		t.Errorf("expected name replica-1, got %s", req.Name)
	}
}

func TestPostgresReadReplicaModelFromAPI(t *testing.T) {
	ctx := context.Background()
	diags := diag.Diagnostics{}
	api := &apiPostgresReadReplica{
		ID:                  "rr-1",
		InstanceID:          "pg-1",
		Name:                "replica-1",
		Status:              "running",
		PrivateIP:           "10.0.1.6",
		Port:                5432,
		ReplicationLagBytes: 512,
	}
	var model PostgresReadReplicaModel
	model.fromAPI(ctx, api, &diags)
	if diags.HasError() {
		t.Fatalf("unexpected diagnostics: %v", diags.Errors())
	}
	if model.ID.ValueString() != "rr-1" {
		t.Errorf("expected ID rr-1, got %s", model.ID.ValueString())
	}
	if model.InstanceID.ValueString() != "pg-1" {
		t.Errorf("expected instance_id pg-1, got %s", model.InstanceID.ValueString())
	}
	if model.PrivateIP.ValueString() != "10.0.1.6" {
		t.Errorf("expected private_ip, got %s", model.PrivateIP.ValueString())
	}
	if model.Port.ValueInt64() != 5432 {
		t.Errorf("expected port 5432, got %d", model.Port.ValueInt64())
	}
	if model.ReplicationLagBytes.ValueInt64() != 512 {
		t.Errorf("expected replication_lag_bytes 512, got %d", model.ReplicationLagBytes.ValueInt64())
	}
}

func TestPostgresReadReplicaModelFromAPINulls(t *testing.T) {
	ctx := context.Background()
	diags := diag.Diagnostics{}
	api := &apiPostgresReadReplica{
		ID:         "rr-1",
		InstanceID: "pg-1",
		Name:       "replica-1",
		Status:     "provisioning",
	}
	var model PostgresReadReplicaModel
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
	if NewResource() == nil {
		t.Fatal("expected non-nil resource")
	}
}

func TestMetadata(t *testing.T) {
	r := NewResource()
	req := resource.MetadataRequest{ProviderTypeName: "frostmoln"}
	var resp resource.MetadataResponse
	r.Metadata(context.Background(), req, &resp)
	if resp.TypeName != "frostmoln_postgres_read_replica" {
		t.Errorf("expected type name frostmoln_postgres_read_replica, got %s", resp.TypeName)
	}
}

func TestSchema(t *testing.T) {
	r := NewResource()
	var resp resource.SchemaResponse
	r.Schema(context.Background(), resource.SchemaRequest{}, &resp)
	for _, attr := range []string{"id", "instance_id", "name", "status", "private_ip", "port", "replication_lag_bytes"} {
		if _, ok := resp.Schema.Attributes[attr]; !ok {
			t.Errorf("expected attribute %s in schema", attr)
		}
	}
}

func TestConfigureNilProviderData(t *testing.T) {
	r := &postgresReadReplicaResource{}
	var resp resource.ConfigureResponse
	r.Configure(context.Background(), resource.ConfigureRequest{}, &resp)
	if resp.Diagnostics.HasError() {
		t.Errorf("expected no errors for nil provider data, got %v", resp.Diagnostics.Errors())
	}
}

func TestConfigureWrongType(t *testing.T) {
	r := &postgresReadReplicaResource{}
	var resp resource.ConfigureResponse
	r.Configure(context.Background(), resource.ConfigureRequest{ProviderData: "not-a-client"}, &resp)
	if !resp.Diagnostics.HasError() {
		t.Error("expected error for wrong provider data type")
	}
}

func TestConfigureValid(t *testing.T) {
	r := &postgresReadReplicaResource{}
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

func TestUpdateNotSupported(t *testing.T) {
	r := &postgresReadReplicaResource{}
	var resp resource.UpdateResponse
	r.Update(context.Background(), resource.UpdateRequest{}, &resp)
	if !resp.Diagnostics.HasError() {
		t.Error("expected error: read replicas are immutable")
	}
}

func TestImportState(t *testing.T) {
	r := NewResource().(resource.ResourceWithImportState)
	var schemaResp resource.SchemaResponse
	r.(resource.Resource).Schema(context.Background(), resource.SchemaRequest{}, &schemaResp)
	stateVal := tftypes.NewValue(schemaResp.Schema.Type().TerraformType(context.Background()), nil)
	resp := resource.ImportStateResponse{State: tfsdk.State{Schema: schemaResp.Schema, Raw: stateVal}}
	r.ImportState(context.Background(), resource.ImportStateRequest{ID: "rr-imported"}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("import failed: %v", resp.Diagnostics.Errors())
	}
	var imported PostgresReadReplicaModel
	resp.State.Get(context.Background(), &imported)
	if imported.ID.ValueString() != "rr-imported" {
		t.Errorf("expected imported ID rr-imported, got %s", imported.ID.ValueString())
	}
}

// --- tfsdk helpers ---

func buildState(t *testing.T, model PostgresReadReplicaModel) tfsdk.State {
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

func buildPlan(t *testing.T, model PostgresReadReplicaModel) tfsdk.Plan {
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

func newClient(t *testing.T, server *httptest.Server) *client.Client {
	t.Helper()
	c := client.NewClient(server.URL, "test-key", client.WithHTTPClient(server.Client())) // pragma: allowlist secret
	c.SetTenantIDForTest("t-1")
	return c
}

func newResource(c *client.Client) *postgresReadReplicaResource {
	return &postgresReadReplicaResource{
		client:       c,
		pollInterval: 5 * time.Millisecond,
		pollTimeout:  5 * time.Second,
	}
}

func planModel() PostgresReadReplicaModel {
	return PostgresReadReplicaModel{
		InstanceID: types.StringValue("pg-1"),
		Name:       types.StringValue("replica-1"),
	}
}

// --- CRUD tests ---

func TestCreate(t *testing.T) {
	var getCount atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/tenants/t-1/databases/pg-1/replicas":
			var body apiCreatePostgresReadReplicaRequest
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decode failed: %v", err)
			}
			if body.Name != "replica-1" {
				t.Errorf("expected name replica-1, got %s", body.Name)
			}
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(apiPostgresReadReplica{
				ID: "rr-new", InstanceID: "pg-1", Name: "replica-1", Status: "provisioning",
			})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-1/databases/pg-1/replicas/rr-new":
			status := "provisioning"
			if getCount.Add(1) >= 2 {
				status = "running"
			}
			_ = json.NewEncoder(w).Encode(apiPostgresReadReplica{
				ID: "rr-new", InstanceID: "pg-1", Name: "replica-1", Status: status,
				PrivateIP: "10.0.1.6", Port: 5432, ReplicationLagBytes: 128,
			})
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	r := newResource(newClient(t, server))
	createResp := resource.CreateResponse{State: emptyState(t)}
	r.Create(context.Background(), resource.CreateRequest{Plan: buildPlan(t, planModel())}, &createResp)

	if createResp.Diagnostics.HasError() {
		t.Fatalf("create failed: %v", createResp.Diagnostics.Errors())
	}
	var result PostgresReadReplicaModel
	createResp.State.Get(context.Background(), &result)
	if result.ID.ValueString() != "rr-new" {
		t.Errorf("expected ID rr-new, got %s", result.ID.ValueString())
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
	r.Create(context.Background(), resource.CreateRequest{Plan: buildPlan(t, planModel())}, &createResp)
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
	r.Create(context.Background(), resource.CreateRequest{Plan: buildPlan(t, planModel())}, &createResp)
	if !createResp.Diagnostics.HasError() {
		t.Error("expected error for bad response body")
	}
}

func TestCreatePollingErrorState(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost:
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(apiPostgresReadReplica{ID: "rr-err", InstanceID: "pg-1", Status: "provisioning"})
		case http.MethodGet:
			_ = json.NewEncoder(w).Encode(apiPostgresReadReplica{ID: "rr-err", InstanceID: "pg-1", Status: "failed"})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	r := newResource(newClient(t, server))
	createResp := resource.CreateResponse{State: emptyState(t)}
	r.Create(context.Background(), resource.CreateRequest{Plan: buildPlan(t, planModel())}, &createResp)
	if !createResp.Diagnostics.HasError() {
		t.Error("expected error when replica enters failed state")
	}
}

func TestRead(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-1/databases/pg-1/replicas/rr-1" {
			_ = json.NewEncoder(w).Encode(apiPostgresReadReplica{
				ID: "rr-1", InstanceID: "pg-1", Name: "replica-1", Status: "running",
				PrivateIP: "10.0.1.6", Port: 5432,
			})
			return
		}
		t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	r := newResource(newClient(t, server))
	state := buildState(t, PostgresReadReplicaModel{
		ID: types.StringValue("rr-1"), InstanceID: types.StringValue("pg-1"),
		Name: types.StringValue("replica-1"), Status: types.StringValue("running"),
	})
	readResp := resource.ReadResponse{State: state}
	r.Read(context.Background(), resource.ReadRequest{State: state}, &readResp)

	if readResp.Diagnostics.HasError() {
		t.Fatalf("read failed: %v", readResp.Diagnostics.Errors())
	}
	var result PostgresReadReplicaModel
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
	state := buildState(t, PostgresReadReplicaModel{
		ID: types.StringValue("rr-gone"), InstanceID: types.StringValue("pg-1"),
		Name: types.StringValue("gone"), Status: types.StringValue("running"),
	})
	readResp := resource.ReadResponse{State: state}
	r.Read(context.Background(), resource.ReadRequest{State: state}, &readResp)
	if readResp.Diagnostics.HasError() {
		t.Fatalf("expected no error for not-found, got %v", readResp.Diagnostics.Errors())
	}
	var result PostgresReadReplicaModel
	if diags := readResp.State.Get(context.Background(), &result); !diags.HasError() {
		if result.ID.ValueString() != "" {
			t.Error("expected state removed after 404")
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
	state := buildState(t, PostgresReadReplicaModel{
		ID: types.StringValue("rr-1"), InstanceID: types.StringValue("pg-1"),
		Name: types.StringValue("replica-1"), Status: types.StringValue("running"),
	})
	readResp := resource.ReadResponse{State: state}
	r.Read(context.Background(), resource.ReadRequest{State: state}, &readResp)
	if !readResp.Diagnostics.HasError() {
		t.Error("expected error for server error on read")
	}
}

func TestDelete(t *testing.T) {
	var deleted atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodDelete && r.URL.Path == "/v1/tenants/t-1/databases/pg-1/replicas/rr-1":
			deleted.Store(true)
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-1/databases/pg-1/replicas/rr-1":
			if deleted.Load() {
				w.WriteHeader(http.StatusNotFound)
				_ = json.NewEncoder(w).Encode(map[string]string{"code": "NOT_FOUND", "message": "not found"})
			} else {
				_ = json.NewEncoder(w).Encode(apiPostgresReadReplica{ID: "rr-1", Status: "deleting"})
			}
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	r := newResource(newClient(t, server))
	state := buildState(t, PostgresReadReplicaModel{
		ID: types.StringValue("rr-1"), InstanceID: types.StringValue("pg-1"),
		Name: types.StringValue("replica-1"), Status: types.StringValue("running"),
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
	state := buildState(t, PostgresReadReplicaModel{
		ID: types.StringValue("rr-gone"), InstanceID: types.StringValue("pg-1"),
		Name: types.StringValue("gone"), Status: types.StringValue("running"),
	})
	deleteResp := resource.DeleteResponse{State: state}
	r.Delete(context.Background(), resource.DeleteRequest{State: state}, &deleteResp)
	if deleteResp.Diagnostics.HasError() {
		t.Fatalf("delete of already-gone replica should not error, got %v", deleteResp.Diagnostics.Errors())
	}
}

func TestDeleteServerError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]string{"code": "INTERNAL", "message": "boom"})
	}))
	defer server.Close()

	r := newResource(newClient(t, server))
	state := buildState(t, PostgresReadReplicaModel{
		ID: types.StringValue("rr-1"), InstanceID: types.StringValue("pg-1"),
		Name: types.StringValue("replica-1"), Status: types.StringValue("running"),
	})
	deleteResp := resource.DeleteResponse{State: state}
	r.Delete(context.Background(), resource.DeleteRequest{State: state}, &deleteResp)
	if !deleteResp.Diagnostics.HasError() {
		t.Error("expected error for server error on delete")
	}
}
