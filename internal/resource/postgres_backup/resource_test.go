package postgres_backup

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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

func TestPostgresBackupModelToCreateRequest(t *testing.T) {
	ctx := context.Background()
	diags := diag.Diagnostics{}

	model := PostgresBackupModel{
		Name: types.StringValue("nightly"),
		Type: types.StringNull(),
	}
	req := model.toCreateRequest(ctx, &diags)
	if diags.HasError() {
		t.Fatalf("unexpected diagnostics: %v", diags.Errors())
	}
	if req.Name != "nightly" {
		t.Errorf("expected name nightly, got %s", req.Name)
	}
	if req.Type != "" {
		t.Errorf("expected empty type for null value, got %s", req.Type)
	}
}

func TestPostgresBackupModelToCreateRequestWithType(t *testing.T) {
	ctx := context.Background()
	diags := diag.Diagnostics{}

	model := PostgresBackupModel{
		Name: types.StringValue("nightly"),
		Type: types.StringValue("incremental"),
	}
	req := model.toCreateRequest(ctx, &diags)
	if diags.HasError() {
		t.Fatalf("unexpected diagnostics: %v", diags.Errors())
	}
	if req.Type != "incremental" {
		t.Errorf("expected type incremental, got %s", req.Type)
	}
}

func TestPostgresBackupModelFromAPI(t *testing.T) {
	ctx := context.Background()
	diags := diag.Diagnostics{}

	api := &apiPostgresBackup{
		ID:          "bk-1",
		InstanceID:  "pg-1",
		Name:        "nightly",
		Type:        "full",
		Status:      "completed",
		SizeBytes:   1024,
		StartedAt:   "2025-01-01T00:00:00Z",
		CompletedAt: "2025-01-01T01:00:00Z",
	}
	var model PostgresBackupModel
	model.fromAPI(ctx, api, &diags)
	if diags.HasError() {
		t.Fatalf("unexpected diagnostics: %v", diags.Errors())
	}
	if model.ID.ValueString() != "bk-1" {
		t.Errorf("expected ID bk-1, got %s", model.ID.ValueString())
	}
	if model.InstanceID.ValueString() != "pg-1" {
		t.Errorf("expected instance_id pg-1, got %s", model.InstanceID.ValueString())
	}
	if model.Type.ValueString() != "full" {
		t.Errorf("expected type full, got %s", model.Type.ValueString())
	}
	if model.SizeBytes.ValueInt64() != 1024 {
		t.Errorf("expected size_bytes 1024, got %d", model.SizeBytes.ValueInt64())
	}
	if model.CompletedAt.ValueString() != "2025-01-01T01:00:00Z" {
		t.Errorf("expected completed_at, got %s", model.CompletedAt.ValueString())
	}
}

func TestPostgresBackupModelFromAPINulls(t *testing.T) {
	ctx := context.Background()
	diags := diag.Diagnostics{}

	api := &apiPostgresBackup{
		ID:         "bk-1",
		InstanceID: "pg-1",
		Name:       "nightly",
		Status:     "creating",
	}
	var model PostgresBackupModel
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
	if NewResource() == nil {
		t.Fatal("expected non-nil resource")
	}
}

func TestMetadata(t *testing.T) {
	r := NewResource()
	req := resource.MetadataRequest{ProviderTypeName: "frostmoln"}
	var resp resource.MetadataResponse
	r.Metadata(context.Background(), req, &resp)
	if resp.TypeName != "frostmoln_postgres_backup" {
		t.Errorf("expected type name frostmoln_postgres_backup, got %s", resp.TypeName)
	}
}

func TestSchema(t *testing.T) {
	r := NewResource()
	var resp resource.SchemaResponse
	r.Schema(context.Background(), resource.SchemaRequest{}, &resp)
	for _, attr := range []string{"id", "instance_id", "name", "type", "status", "size_bytes", "started_at", "completed_at"} {
		if _, ok := resp.Schema.Attributes[attr]; !ok {
			t.Errorf("expected attribute %s in schema", attr)
		}
	}
}

func TestConfigureNilProviderData(t *testing.T) {
	r := &postgresBackupResource{}
	var resp resource.ConfigureResponse
	r.Configure(context.Background(), resource.ConfigureRequest{}, &resp)
	if resp.Diagnostics.HasError() {
		t.Errorf("expected no errors for nil provider data, got %v", resp.Diagnostics.Errors())
	}
}

func TestConfigureWrongType(t *testing.T) {
	r := &postgresBackupResource{}
	var resp resource.ConfigureResponse
	r.Configure(context.Background(), resource.ConfigureRequest{ProviderData: "not-a-client"}, &resp)
	if !resp.Diagnostics.HasError() {
		t.Error("expected error for wrong provider data type")
	}
}

func TestConfigureValid(t *testing.T) {
	r := &postgresBackupResource{}
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
	r := &postgresBackupResource{}
	var resp resource.UpdateResponse
	r.Update(context.Background(), resource.UpdateRequest{}, &resp)
	if !resp.Diagnostics.HasError() {
		t.Error("expected error: backups are immutable")
	}
}

func TestImportState(t *testing.T) {
	r := NewResource().(resource.ResourceWithImportState)
	var schemaResp resource.SchemaResponse
	r.(resource.Resource).Schema(context.Background(), resource.SchemaRequest{}, &schemaResp)
	stateVal := tftypes.NewValue(schemaResp.Schema.Type().TerraformType(context.Background()), nil)
	resp := resource.ImportStateResponse{State: tfsdk.State{Schema: schemaResp.Schema, Raw: stateVal}}
	r.ImportState(context.Background(), resource.ImportStateRequest{ID: "bk-imported"}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("import failed: %v", resp.Diagnostics.Errors())
	}
	var imported PostgresBackupModel
	resp.State.Get(context.Background(), &imported)
	if imported.ID.ValueString() != "bk-imported" {
		t.Errorf("expected imported ID bk-imported, got %s", imported.ID.ValueString())
	}
}

// --- tfsdk helpers ---

func buildState(t *testing.T, model PostgresBackupModel) tfsdk.State {
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

func buildPlan(t *testing.T, model PostgresBackupModel) tfsdk.Plan {
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

func newResource(c *client.Client) *postgresBackupResource {
	return &postgresBackupResource{
		client:       c,
		pollInterval: 5 * time.Millisecond,
		pollTimeout:  5 * time.Second,
	}
}

func planModel() PostgresBackupModel {
	return PostgresBackupModel{
		InstanceID: types.StringValue("pg-1"),
		Name:       types.StringValue("nightly"),
		Type:       types.StringValue("full"),
	}
}

// --- CRUD tests ---

func TestCreate(t *testing.T) {
	created := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/tenants/t-1/databases/pg-1/backups":
			var body apiCreatePostgresBackupRequest
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decode failed: %v", err)
			}
			if body.Name != "nightly" {
				t.Errorf("expected name nightly, got %s", body.Name)
			}
			created = true
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(apiPostgresBackup{
				ID: "bk-new", InstanceID: "pg-1", Name: "nightly", Type: "full", Status: "creating",
			})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-1/databases/pg-1/backups/bk-new":
			status := "creating"
			if created {
				status = "completed"
			}
			_ = json.NewEncoder(w).Encode(apiPostgresBackup{
				ID: "bk-new", InstanceID: "pg-1", Name: "nightly", Type: "full", Status: status,
				SizeBytes: 2048, StartedAt: "2025-01-01T00:00:00Z", CompletedAt: "2025-01-01T01:00:00Z",
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
	var result PostgresBackupModel
	createResp.State.Get(context.Background(), &result)
	if result.ID.ValueString() != "bk-new" {
		t.Errorf("expected ID bk-new, got %s", result.ID.ValueString())
	}
	if result.Status.ValueString() != "completed" {
		t.Errorf("expected status completed, got %s", result.Status.ValueString())
	}
	if result.SizeBytes.ValueInt64() != 2048 {
		t.Errorf("expected size_bytes 2048, got %d", result.SizeBytes.ValueInt64())
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
			_ = json.NewEncoder(w).Encode(apiPostgresBackup{ID: "bk-err", InstanceID: "pg-1", Status: "creating"})
		case http.MethodGet:
			_ = json.NewEncoder(w).Encode(apiPostgresBackup{ID: "bk-err", InstanceID: "pg-1", Status: "failed"})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	r := newResource(newClient(t, server))
	createResp := resource.CreateResponse{State: emptyState(t)}
	r.Create(context.Background(), resource.CreateRequest{Plan: buildPlan(t, planModel())}, &createResp)
	if !createResp.Diagnostics.HasError() {
		t.Error("expected error when backup enters failed state")
	}
}

func TestRead(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-1/databases/pg-1/backups/bk-1" {
			_ = json.NewEncoder(w).Encode(apiPostgresBackup{
				ID: "bk-1", InstanceID: "pg-1", Name: "nightly", Type: "full", Status: "completed",
				SizeBytes: 1024, CompletedAt: "2025-01-01T01:00:00Z",
			})
			return
		}
		t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	r := newResource(newClient(t, server))
	state := buildState(t, PostgresBackupModel{
		ID: types.StringValue("bk-1"), InstanceID: types.StringValue("pg-1"),
		Name: types.StringValue("nightly"), Type: types.StringValue("full"),
		Status: types.StringValue("completed"),
	})
	readResp := resource.ReadResponse{State: state}
	r.Read(context.Background(), resource.ReadRequest{State: state}, &readResp)

	if readResp.Diagnostics.HasError() {
		t.Fatalf("read failed: %v", readResp.Diagnostics.Errors())
	}
	var result PostgresBackupModel
	readResp.State.Get(context.Background(), &result)
	if result.Status.ValueString() != "completed" {
		t.Errorf("expected status completed, got %s", result.Status.ValueString())
	}
}

func TestReadNotFound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]string{"code": "NOT_FOUND", "message": "not found"})
	}))
	defer server.Close()

	r := newResource(newClient(t, server))
	state := buildState(t, PostgresBackupModel{
		ID: types.StringValue("bk-gone"), InstanceID: types.StringValue("pg-1"),
		Name: types.StringValue("gone"), Type: types.StringValue("full"),
		Status: types.StringValue("completed"),
	})
	readResp := resource.ReadResponse{State: state}
	r.Read(context.Background(), resource.ReadRequest{State: state}, &readResp)
	if readResp.Diagnostics.HasError() {
		t.Fatalf("expected no error for not-found, got %v", readResp.Diagnostics.Errors())
	}
	var result PostgresBackupModel
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
	state := buildState(t, PostgresBackupModel{
		ID: types.StringValue("bk-1"), InstanceID: types.StringValue("pg-1"),
		Name: types.StringValue("nightly"), Type: types.StringValue("full"),
		Status: types.StringValue("completed"),
	})
	readResp := resource.ReadResponse{State: state}
	r.Read(context.Background(), resource.ReadRequest{State: state}, &readResp)
	if !readResp.Diagnostics.HasError() {
		t.Error("expected error for server error on read")
	}
}

func TestDelete(t *testing.T) {
	deleted := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete && r.URL.Path == "/v1/tenants/t-1/databases/pg-1/backups/bk-1" {
			deleted = true
			w.WriteHeader(http.StatusNoContent)
			return
		}
		t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	r := newResource(newClient(t, server))
	state := buildState(t, PostgresBackupModel{
		ID: types.StringValue("bk-1"), InstanceID: types.StringValue("pg-1"),
		Name: types.StringValue("nightly"), Type: types.StringValue("full"),
		Status: types.StringValue("completed"),
	})
	deleteResp := resource.DeleteResponse{State: state}
	r.Delete(context.Background(), resource.DeleteRequest{State: state}, &deleteResp)
	if deleteResp.Diagnostics.HasError() {
		t.Fatalf("delete failed: %v", deleteResp.Diagnostics.Errors())
	}
	if !deleted {
		t.Error("expected DELETE to be called")
	}
}

func TestDeleteAlreadyGone(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]string{"code": "NOT_FOUND", "message": "not found"})
	}))
	defer server.Close()

	r := newResource(newClient(t, server))
	state := buildState(t, PostgresBackupModel{
		ID: types.StringValue("bk-gone"), InstanceID: types.StringValue("pg-1"),
		Name: types.StringValue("gone"), Type: types.StringValue("full"),
		Status: types.StringValue("completed"),
	})
	deleteResp := resource.DeleteResponse{State: state}
	r.Delete(context.Background(), resource.DeleteRequest{State: state}, &deleteResp)
	if deleteResp.Diagnostics.HasError() {
		t.Fatalf("delete of already-gone backup should not error, got %v", deleteResp.Diagnostics.Errors())
	}
}

func TestDeleteServerError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]string{"code": "INTERNAL", "message": "boom"})
	}))
	defer server.Close()

	r := newResource(newClient(t, server))
	state := buildState(t, PostgresBackupModel{
		ID: types.StringValue("bk-1"), InstanceID: types.StringValue("pg-1"),
		Name: types.StringValue("nightly"), Type: types.StringValue("full"),
		Status: types.StringValue("completed"),
	})
	deleteResp := resource.DeleteResponse{State: state}
	r.Delete(context.Background(), resource.DeleteRequest{State: state}, &deleteResp)
	if !deleteResp.Diagnostics.HasError() {
		t.Error("expected error for server error on delete")
	}
}
