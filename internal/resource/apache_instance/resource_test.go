package apache_instance

import (
	"context"
	"encoding/json"
	"fmt"
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

func TestApacheInstanceModelToCreateRequest(t *testing.T) {
	ctx := context.Background()
	diags := diag.Diagnostics{}

	model := ApacheInstanceModel{
		Name:          types.StringValue("my-apache"),
		EngineVersion: types.StringValue("2.4"),
		Flavor:        types.StringValue("web.gp1.small"),
		StorageGB:     types.Int64Value(20),
		TLSEnabled:    types.BoolNull(),
		PHPEnabled:    types.BoolNull(),
		PHPVersion:    types.StringNull(),
		EngineConfig:  types.StringNull(),
	}

	req := model.toCreateRequest(ctx, &diags)
	if diags.HasError() {
		t.Fatalf("unexpected diagnostics: %v", diags.Errors())
	}

	if req.Engine != "apache" {
		t.Errorf("expected engine apache, got %s", req.Engine)
	}
	if req.Name != "my-apache" {
		t.Errorf("expected name my-apache, got %s", req.Name)
	}
	if req.EngineVersion != "2.4" {
		t.Errorf("expected engineVersion 2.4, got %s", req.EngineVersion)
	}
	if req.Flavor != "web.gp1.small" {
		t.Errorf("expected flavor web.gp1.small, got %s", req.Flavor)
	}
	if req.StorageGB != 20 {
		t.Errorf("expected storageGb 20, got %d", req.StorageGB)
	}
	if req.TLSEnabled != nil {
		t.Error("expected nil tlsEnabled for null value")
	}
	if req.PHPEnabled != nil {
		t.Error("expected nil phpEnabled for null value")
	}
}

func TestApacheInstanceModelToCreateRequestWithOptionals(t *testing.T) {
	ctx := context.Background()
	diags := diag.Diagnostics{}

	model := ApacheInstanceModel{
		Name:          types.StringValue("my-apache"),
		EngineVersion: types.StringValue("2.4"),
		Flavor:        types.StringValue("web.gp1.medium"),
		StorageGB:     types.Int64Value(40),
		TLSEnabled:    types.BoolValue(true),
		PHPEnabled:    types.BoolValue(true),
		PHPVersion:    types.StringValue("8.3"),
		EngineConfig:  types.StringValue(`{"k":"v"}`),
	}

	req := model.toCreateRequest(ctx, &diags)
	if diags.HasError() {
		t.Fatalf("unexpected diagnostics: %v", diags.Errors())
	}

	if req.TLSEnabled == nil || !*req.TLSEnabled {
		t.Error("expected tlsEnabled true")
	}
	if req.PHPEnabled == nil || !*req.PHPEnabled {
		t.Error("expected phpEnabled true")
	}
	if req.PHPVersion != "8.3" {
		t.Errorf("expected phpVersion 8.3, got %s", req.PHPVersion)
	}
	if req.EngineConfig != `{"k":"v"}` {
		t.Errorf("expected engineConfig set, got %s", req.EngineConfig)
	}
}

func TestApacheInstanceModelToUpdateRequest(t *testing.T) {
	plan := ApacheInstanceModel{
		Name:         types.StringValue("new-name"),
		Flavor:       types.StringValue("web.gp1.large"),
		StorageGB:    types.Int64Value(80),
		TLSEnabled:   types.BoolValue(true),
		PHPEnabled:   types.BoolValue(true),
		PHPVersion:   types.StringValue("8.3"),
		EngineConfig: types.StringValue("new"),
	}
	state := ApacheInstanceModel{
		Name:         types.StringValue("old-name"),
		Flavor:       types.StringValue("web.gp1.small"),
		StorageGB:    types.Int64Value(20),
		TLSEnabled:   types.BoolValue(false),
		PHPEnabled:   types.BoolValue(false),
		PHPVersion:   types.StringNull(),
		EngineConfig: types.StringNull(),
	}

	req := plan.toUpdateRequest(&state)
	if req.Name == nil || *req.Name != "new-name" {
		t.Error("expected name update")
	}
	if req.Flavor == nil || *req.Flavor != "web.gp1.large" {
		t.Error("expected flavor update")
	}
	if req.StorageGB == nil || *req.StorageGB != 80 {
		t.Error("expected storageGb update")
	}
	if req.TLSEnabled == nil || !*req.TLSEnabled {
		t.Error("expected tlsEnabled update")
	}
	if req.PHPEnabled == nil || !*req.PHPEnabled {
		t.Error("expected phpEnabled update")
	}
	if req.PHPVersion == nil || *req.PHPVersion != "8.3" {
		t.Error("expected phpVersion update")
	}
	if req.EngineConfig == nil || *req.EngineConfig != "new" {
		t.Error("expected engineConfig update")
	}
}

func TestApacheInstanceModelToUpdateRequestNoChanges(t *testing.T) {
	same := ApacheInstanceModel{
		Name:         types.StringValue("same"),
		Flavor:       types.StringValue("web.gp1.small"),
		StorageGB:    types.Int64Value(20),
		TLSEnabled:   types.BoolValue(true),
		PHPEnabled:   types.BoolValue(false),
		PHPVersion:   types.StringValue("8.2"),
		EngineConfig: types.StringValue("cfg"),
	}

	req := same.toUpdateRequest(&same)
	if req.Name != nil || req.Flavor != nil || req.StorageGB != nil ||
		req.TLSEnabled != nil || req.PHPEnabled != nil || req.PHPVersion != nil || req.EngineConfig != nil {
		t.Error("expected no changes in update request")
	}
}

func TestApacheInstanceModelFromAPI(t *testing.T) {
	ctx := context.Background()
	diags := diag.Diagnostics{}

	api := &apiWebserverInstance{
		ID:            "apache-123",
		Name:          "my-apache",
		Engine:        "apache",
		EngineVersion: "2.4",
		Flavor:        "web.gp1.small",
		StorageGB:     20,
		TLSEnabled:    true,
		PHPEnabled:    true,
		PHPVersion:    "8.3",
		EngineConfig:  "cfg",
		Status:        "running",
		PrivateIP:     "10.0.1.5",
		Port:          443,
		CreatedAt:     "2025-01-01T00:00:00Z",
		UpdatedAt:     "2025-01-02T00:00:00Z",
		TenantID:      "t-1",
	}

	var model ApacheInstanceModel
	model.fromAPI(ctx, api, &diags)
	if diags.HasError() {
		t.Fatalf("unexpected diagnostics: %v", diags.Errors())
	}

	if model.ID.ValueString() != "apache-123" {
		t.Errorf("expected ID apache-123, got %s", model.ID.ValueString())
	}
	if !model.TLSEnabled.ValueBool() {
		t.Error("expected tls_enabled true")
	}
	if model.PHPVersion.ValueString() != "8.3" {
		t.Errorf("expected php_version 8.3, got %s", model.PHPVersion.ValueString())
	}
	if model.Port.ValueInt64() != 443 {
		t.Errorf("expected port 443, got %d", model.Port.ValueInt64())
	}
	if model.TenantID.ValueString() != "t-1" {
		t.Errorf("expected tenant_id t-1, got %s", model.TenantID.ValueString())
	}
	if model.Status.ValueString() != "running" {
		t.Errorf("expected status running, got %s", model.Status.ValueString())
	}
}

func TestApacheInstanceModelFromAPINulls(t *testing.T) {
	ctx := context.Background()
	diags := diag.Diagnostics{}

	api := &apiWebserverInstance{
		ID:            "apache-123",
		Name:          "my-apache",
		Engine:        "apache",
		EngineVersion: "2.4",
		Flavor:        "web.gp1.small",
		StorageGB:     20,
		TLSEnabled:    false,
		PHPEnabled:    false,
		Status:        "provisioning",
		CreatedAt:     "2025-01-01T00:00:00Z",
	}

	var model ApacheInstanceModel
	model.fromAPI(ctx, api, &diags)
	if diags.HasError() {
		t.Fatalf("unexpected diagnostics: %v", diags.Errors())
	}

	if !model.PHPVersion.IsNull() {
		t.Error("expected null php_version")
	}
	if !model.EngineConfig.IsNull() {
		t.Error("expected null engine_config")
	}
	if !model.PrivateIP.IsNull() {
		t.Error("expected null private_ip")
	}
	if !model.Port.IsNull() {
		t.Error("expected null port")
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
	if resp.TypeName != "frostmoln_apache_instance" {
		t.Errorf("expected type name frostmoln_apache_instance, got %s", resp.TypeName)
	}
}

func TestSchema(t *testing.T) {
	r := NewResource()
	req := resource.SchemaRequest{}
	var resp resource.SchemaResponse
	r.Schema(context.Background(), req, &resp)

	requiredAttrs := []string{"name", "engine_version", "flavor", "storage_gb"}
	for _, attr := range requiredAttrs {
		if _, ok := resp.Schema.Attributes[attr]; !ok {
			t.Errorf("expected attribute %s in schema", attr)
		}
	}

	computedAttrs := []string{"id", "status", "private_ip", "port", "created_at", "updated_at", "tenant_id"}
	for _, attr := range computedAttrs {
		if _, ok := resp.Schema.Attributes[attr]; !ok {
			t.Errorf("expected computed attribute %s in schema", attr)
		}
	}
}

func TestConfigureNilProviderData(t *testing.T) {
	r := &apacheInstanceResource{}
	req := resource.ConfigureRequest{}
	var resp resource.ConfigureResponse
	r.Configure(context.Background(), req, &resp)
	if resp.Diagnostics.HasError() {
		t.Errorf("expected no errors for nil provider data, got %v", resp.Diagnostics.Errors())
	}
}

func TestConfigureWrongType(t *testing.T) {
	r := &apacheInstanceResource{}
	req := resource.ConfigureRequest{ProviderData: "not-a-client"}
	var resp resource.ConfigureResponse
	r.Configure(context.Background(), req, &resp)
	if !resp.Diagnostics.HasError() {
		t.Error("expected error for wrong provider data type")
	}
}

func TestConfigureValidClient(t *testing.T) {
	r := &apacheInstanceResource{}
	c := client.NewClient("http://localhost", "test-key") // pragma: allowlist secret
	req := resource.ConfigureRequest{ProviderData: c}
	var resp resource.ConfigureResponse
	r.Configure(context.Background(), req, &resp)
	if resp.Diagnostics.HasError() {
		t.Errorf("expected no errors for valid client, got %v", resp.Diagnostics.Errors())
	}
	if r.client != c {
		t.Error("expected client to be set")
	}
}

// --- state/plan helpers ---

func buildApacheInstanceState(t *testing.T, model ApacheInstanceModel) tfsdk.State {
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

func buildApacheInstancePlan(t *testing.T, model ApacheInstanceModel) tfsdk.Plan {
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

func emptyApacheInstanceState(t *testing.T) tfsdk.State {
	t.Helper()
	r := NewResource()
	var schemaResp resource.SchemaResponse
	r.Schema(context.Background(), resource.SchemaRequest{}, &schemaResp)

	stateVal := tftypes.NewValue(schemaResp.Schema.Type().TerraformType(context.Background()), nil)
	return tfsdk.State{Schema: schemaResp.Schema, Raw: stateVal}
}

func newTestApacheResource(c *client.Client) *apacheInstanceResource {
	return &apacheInstanceResource{
		client:       c,
		pollInterval: 5 * time.Millisecond,
		pollTimeout:  5 * time.Second,
	}
}

func baseApacheModel() ApacheInstanceModel {
	return ApacheInstanceModel{
		Name:          types.StringValue("my-apache"),
		EngineVersion: types.StringValue("2.4"),
		Flavor:        types.StringValue("web.gp1.small"),
		StorageGB:     types.Int64Value(20),
		TLSEnabled:    types.BoolValue(true),
		PHPEnabled:    types.BoolValue(false),
		PHPVersion:    types.StringNull(),
		EngineConfig:  types.StringNull(),
	}
}

// --- CRUD tests ---

func TestCreate(t *testing.T) {
	var callCount atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/tenants/t-1/webservers":
			var body apiCreateWebserverInstanceRequest
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("failed to decode request: %v", err)
			}
			if body.Engine != "apache" {
				t.Errorf("expected engine apache, got %s", body.Engine)
			}
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(apiWebserverInstance{
				ID:            "apache-new",
				Name:          body.Name,
				Engine:        "apache",
				EngineVersion: body.EngineVersion,
				Flavor:        body.Flavor,
				StorageGB:     body.StorageGB,
				Status:        "provisioning",
				CreatedAt:     "2025-01-01T00:00:00Z",
			})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-1/webservers/apache-new":
			count := callCount.Add(1)
			status := "provisioning"
			if count >= 2 {
				status = "running"
			}
			_ = json.NewEncoder(w).Encode(apiWebserverInstance{
				ID:            "apache-new",
				Name:          "test-apache",
				Engine:        "apache",
				EngineVersion: "2.4",
				Flavor:        "web.gp1.small",
				StorageGB:     20,
				TLSEnabled:    true,
				Status:        status,
				PrivateIP:     "10.0.1.5",
				Port:          443,
				CreatedAt:     "2025-01-01T00:00:00Z",
				TenantID:      "t-1",
			})
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key", client.WithHTTPClient(server.Client())) // pragma: allowlist secret
	c.SetTenantIDForTest("t-1")

	r := newTestApacheResource(c)

	model := baseApacheModel()
	model.Name = types.StringValue("test-apache")
	plan := buildApacheInstancePlan(t, model)

	createResp := resource.CreateResponse{State: emptyApacheInstanceState(t)}
	r.Create(context.Background(), resource.CreateRequest{Plan: plan}, &createResp)

	if createResp.Diagnostics.HasError() {
		t.Fatalf("create failed: %v", createResp.Diagnostics.Errors())
	}

	var result ApacheInstanceModel
	createResp.State.Get(context.Background(), &result)
	if result.ID.ValueString() != "apache-new" {
		t.Errorf("expected ID apache-new, got %s", result.ID.ValueString())
	}
	if result.Status.ValueString() != "running" {
		t.Errorf("expected status running, got %s", result.Status.ValueString())
	}
	if result.Port.ValueInt64() != 443 {
		t.Errorf("expected port 443, got %d", result.Port.ValueInt64())
	}
}

func TestCreateAPIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/v1/tenants/t-1/webservers" {
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(map[string]any{"code": "INTERNAL", "message": "boom"})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key", client.WithHTTPClient(server.Client())) // pragma: allowlist secret
	c.SetTenantIDForTest("t-1")

	r := newTestApacheResource(c)

	createResp := resource.CreateResponse{State: emptyApacheInstanceState(t)}
	r.Create(context.Background(), resource.CreateRequest{Plan: buildApacheInstancePlan(t, baseApacheModel())}, &createResp)
	if !createResp.Diagnostics.HasError() {
		t.Error("expected error for API failure on create")
	}
}

func TestCreatePollErrorState(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/tenants/t-1/webservers":
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(apiWebserverInstance{
				ID: "apache-err", Name: "x", Engine: "apache", EngineVersion: "2.4",
				Flavor: "f", StorageGB: 20, Status: "provisioning", CreatedAt: "2025-01-01T00:00:00Z",
			})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-1/webservers/apache-err":
			_ = json.NewEncoder(w).Encode(apiWebserverInstance{
				ID: "apache-err", Name: "x", Engine: "apache", EngineVersion: "2.4",
				Flavor: "f", StorageGB: 20, Status: "error", CreatedAt: "2025-01-01T00:00:00Z",
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key", client.WithHTTPClient(server.Client())) // pragma: allowlist secret
	c.SetTenantIDForTest("t-1")

	r := newTestApacheResource(c)

	createResp := resource.CreateResponse{State: emptyApacheInstanceState(t)}
	r.Create(context.Background(), resource.CreateRequest{Plan: buildApacheInstancePlan(t, baseApacheModel())}, &createResp)
	if !createResp.Diagnostics.HasError() {
		t.Error("expected error when instance enters error state during polling")
	}
}

func TestRead(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-1/webservers/apache-123" {
			_ = json.NewEncoder(w).Encode(apiWebserverInstance{
				ID:            "apache-123",
				Name:          "my-apache",
				Engine:        "apache",
				EngineVersion: "2.4",
				Flavor:        "web.gp1.small",
				StorageGB:     20,
				TLSEnabled:    true,
				Status:        "running",
				Port:          443,
				CreatedAt:     "2025-01-01T00:00:00Z",
			})
			return
		}
		t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key", client.WithHTTPClient(server.Client())) // pragma: allowlist secret
	c.SetTenantIDForTest("t-1")

	r := &apacheInstanceResource{client: c}

	model := baseApacheModel()
	model.ID = types.StringValue("apache-123")
	model.Status = types.StringValue("running")
	model.CreatedAt = types.StringValue("2025-01-01T00:00:00Z")
	state := buildApacheInstanceState(t, model)

	readResp := resource.ReadResponse{State: state}
	r.Read(context.Background(), resource.ReadRequest{State: state}, &readResp)

	if readResp.Diagnostics.HasError() {
		t.Fatalf("read failed: %v", readResp.Diagnostics.Errors())
	}

	var result ApacheInstanceModel
	readResp.State.Get(context.Background(), &result)
	if result.Status.ValueString() != "running" {
		t.Errorf("expected status running, got %s", result.Status.ValueString())
	}
}

func TestReadNotFound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]any{"code": "NOT_FOUND", "message": "not found"})
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key", client.WithHTTPClient(server.Client())) // pragma: allowlist secret
	c.SetTenantIDForTest("t-1")

	r := &apacheInstanceResource{client: c}

	model := baseApacheModel()
	model.ID = types.StringValue("apache-gone")
	model.Status = types.StringValue("running")
	model.CreatedAt = types.StringValue("2025-01-01T00:00:00Z")
	state := buildApacheInstanceState(t, model)

	readResp := resource.ReadResponse{State: state}
	r.Read(context.Background(), resource.ReadRequest{State: state}, &readResp)

	if readResp.Diagnostics.HasError() {
		t.Fatalf("expected no error for not-found, got %v", readResp.Diagnostics.Errors())
	}

	var result ApacheInstanceModel
	diags := readResp.State.Get(context.Background(), &result)
	if !diags.HasError() {
		if result.ID.ValueString() != "" {
			t.Error("expected state to be removed after 404")
		}
	}
}

func TestReadServerError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]any{"code": "INTERNAL", "message": "boom"})
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key", client.WithHTTPClient(server.Client())) // pragma: allowlist secret
	c.SetTenantIDForTest("t-1")

	r := &apacheInstanceResource{client: c}

	model := baseApacheModel()
	model.ID = types.StringValue("apache-123")
	model.Status = types.StringValue("running")
	model.CreatedAt = types.StringValue("2025-01-01T00:00:00Z")
	state := buildApacheInstanceState(t, model)

	readResp := resource.ReadResponse{State: state}
	r.Read(context.Background(), resource.ReadRequest{State: state}, &readResp)
	if !readResp.Diagnostics.HasError() {
		t.Error("expected error for server error on read")
	}
}

func TestUpdate(t *testing.T) {
	var updatedBody apiUpdateWebserverInstanceRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPut && r.URL.Path == "/v1/tenants/t-1/webservers/apache-123":
			_ = json.NewDecoder(r.Body).Decode(&updatedBody)
			w.WriteHeader(http.StatusOK)
			_, _ = fmt.Fprint(w, `{}`)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-1/webservers/apache-123":
			_ = json.NewEncoder(w).Encode(apiWebserverInstance{
				ID:            "apache-123",
				Name:          "updated-apache",
				Engine:        "apache",
				EngineVersion: "2.4",
				Flavor:        "web.gp1.large",
				StorageGB:     80,
				TLSEnabled:    true,
				Status:        "running",
				Port:          443,
				CreatedAt:     "2025-01-01T00:00:00Z",
			})
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key", client.WithHTTPClient(server.Client())) // pragma: allowlist secret
	c.SetTenantIDForTest("t-1")

	r := newTestApacheResource(c)

	stateModel := baseApacheModel()
	stateModel.ID = types.StringValue("apache-123")
	stateModel.Name = types.StringValue("old-apache")
	stateModel.Status = types.StringValue("running")
	stateModel.CreatedAt = types.StringValue("2025-01-01T00:00:00Z")
	state := buildApacheInstanceState(t, stateModel)

	planModel := stateModel
	planModel.Name = types.StringValue("updated-apache")
	planModel.Flavor = types.StringValue("web.gp1.large")
	planModel.StorageGB = types.Int64Value(80)
	plan := buildApacheInstancePlan(t, planModel)

	updateResp := resource.UpdateResponse{State: state}
	r.Update(context.Background(), resource.UpdateRequest{Plan: plan, State: state}, &updateResp)

	if updateResp.Diagnostics.HasError() {
		t.Fatalf("update failed: %v", updateResp.Diagnostics.Errors())
	}

	if updatedBody.Name == nil || *updatedBody.Name != "updated-apache" {
		t.Error("expected name in update request")
	}
	if updatedBody.Flavor == nil || *updatedBody.Flavor != "web.gp1.large" {
		t.Error("expected flavor in update request")
	}
	if updatedBody.StorageGB == nil || *updatedBody.StorageGB != 80 {
		t.Error("expected storageGb in update request")
	}
}

func TestUpdateAPIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut {
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(map[string]any{"code": "INTERNAL", "message": "boom"})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key", client.WithHTTPClient(server.Client())) // pragma: allowlist secret
	c.SetTenantIDForTest("t-1")

	r := newTestApacheResource(c)

	stateModel := baseApacheModel()
	stateModel.ID = types.StringValue("apache-123")
	stateModel.Status = types.StringValue("running")
	stateModel.CreatedAt = types.StringValue("2025-01-01T00:00:00Z")
	state := buildApacheInstanceState(t, stateModel)

	planModel := stateModel
	planModel.Name = types.StringValue("new")
	plan := buildApacheInstancePlan(t, planModel)

	updateResp := resource.UpdateResponse{State: state}
	r.Update(context.Background(), resource.UpdateRequest{Plan: plan, State: state}, &updateResp)
	if !updateResp.Diagnostics.HasError() {
		t.Error("expected error for API failure on update")
	}
}

func TestDelete(t *testing.T) {
	deleted := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodDelete && r.URL.Path == "/v1/tenants/t-1/webservers/apache-123":
			deleted = true
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-1/webservers/apache-123":
			if deleted {
				w.WriteHeader(http.StatusNotFound)
				_ = json.NewEncoder(w).Encode(map[string]any{"code": "NOT_FOUND", "message": "not found"})
			} else {
				_ = json.NewEncoder(w).Encode(apiWebserverInstance{ID: "apache-123", Status: "deleting"})
			}
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key", client.WithHTTPClient(server.Client())) // pragma: allowlist secret
	c.SetTenantIDForTest("t-1")

	r := newTestApacheResource(c)

	model := baseApacheModel()
	model.ID = types.StringValue("apache-123")
	model.Status = types.StringValue("running")
	model.CreatedAt = types.StringValue("2025-01-01T00:00:00Z")
	state := buildApacheInstanceState(t, model)

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
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]any{"code": "NOT_FOUND", "message": "not found"})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key", client.WithHTTPClient(server.Client())) // pragma: allowlist secret
	c.SetTenantIDForTest("t-1")

	r := newTestApacheResource(c)

	model := baseApacheModel()
	model.ID = types.StringValue("apache-gone")
	model.Status = types.StringValue("running")
	model.CreatedAt = types.StringValue("2025-01-01T00:00:00Z")
	state := buildApacheInstanceState(t, model)

	deleteResp := resource.DeleteResponse{State: state}
	r.Delete(context.Background(), resource.DeleteRequest{State: state}, &deleteResp)
	if deleteResp.Diagnostics.HasError() {
		t.Fatalf("delete of already-gone resource should not error, got %v", deleteResp.Diagnostics.Errors())
	}
}

func TestDeletePollError(t *testing.T) {
	deleted := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodDelete && r.URL.Path == "/v1/tenants/t-1/webservers/apache-123":
			deleted = true
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-1/webservers/apache-123":
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(map[string]any{"code": "INTERNAL", "message": "boom"})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key", client.WithHTTPClient(server.Client())) // pragma: allowlist secret
	c.SetTenantIDForTest("t-1")

	r := &apacheInstanceResource{client: c, pollInterval: 5 * time.Millisecond, pollTimeout: 60 * time.Millisecond}

	model := baseApacheModel()
	model.ID = types.StringValue("apache-123")
	model.Status = types.StringValue("running")
	model.CreatedAt = types.StringValue("2025-01-01T00:00:00Z")
	state := buildApacheInstanceState(t, model)

	deleteResp := resource.DeleteResponse{State: state}
	r.Delete(context.Background(), resource.DeleteRequest{State: state}, &deleteResp)
	if !deleted {
		t.Error("expected DELETE to be called")
	}
	if !deleteResp.Diagnostics.HasError() {
		t.Error("expected error when delete poll keeps failing")
	}
}

func TestPollDefaults(t *testing.T) {
	r := &apacheInstanceResource{}
	if got := r.getPollInterval(); got != 5*time.Second {
		t.Errorf("expected default poll interval 5s, got %v", got)
	}
	if got := r.getPollTimeout(); got != 15*time.Minute {
		t.Errorf("expected default poll timeout 15m, got %v", got)
	}
}

func TestImportState(t *testing.T) {
	r := NewResource().(resource.ResourceWithImportState)
	importResp := resource.ImportStateResponse{State: emptyApacheInstanceState(t)}
	r.ImportState(context.Background(), resource.ImportStateRequest{ID: "apache-123"}, &importResp)
	if importResp.Diagnostics.HasError() {
		t.Fatalf("import failed: %v", importResp.Diagnostics.Errors())
	}

	var result ApacheInstanceModel
	importResp.State.Get(context.Background(), &result)
	if result.ID.ValueString() != "apache-123" {
		t.Errorf("expected imported ID apache-123, got %s", result.ID.ValueString())
	}
}
