package nginx_instance

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

// mustCfgMap builds a types.Map from a Go map for use in test models.
func mustCfgMap(m map[string]string) types.Map {
	v, diags := types.MapValueFrom(context.Background(), types.StringType, m)
	if diags.HasError() {
		panic(diags.Errors())
	}
	return v
}

// --- Model unit tests ---

func TestNginxInstanceModelToCreateRequest(t *testing.T) {
	ctx := context.Background()
	diags := diag.Diagnostics{}

	model := NginxInstanceModel{
		Name:       types.StringValue("my-nginx"),
		Version:    types.StringValue("1.27"),
		Flavor:     types.StringValue("web.gp1.small"),
		StorageGB:  types.Int64Value(20),
		VPCID:      types.StringValue("vpc-1"),
		SubnetID:   types.StringValue("sn-1"),
		TLSEnabled: types.BoolNull(),
		Config:     types.MapNull(types.StringType),
	}

	req := model.toCreateRequest(ctx, &diags)
	if diags.HasError() {
		t.Fatalf("unexpected diagnostics: %v", diags.Errors())
	}

	if req.Engine != "nginx" {
		t.Errorf("expected engine nginx, got %s", req.Engine)
	}
	if req.Name != "my-nginx" {
		t.Errorf("expected name my-nginx, got %s", req.Name)
	}
	if req.Flavor != "web.gp1.small" {
		t.Errorf("expected flavorId web.gp1.small, got %s", req.Flavor)
	}
	if req.StorageGB != 20 {
		t.Errorf("expected storageGb 20, got %d", req.StorageGB)
	}
	if req.VPCID != "vpc-1" {
		t.Errorf("expected vpcId vpc-1, got %s", req.VPCID)
	}
	if req.SubnetID != "sn-1" {
		t.Errorf("expected subnetId sn-1, got %s", req.SubnetID)
	}
	if req.TLSEnabled != nil {
		t.Error("expected nil tlsEnabled for null value")
	}
	if req.EngineConfig != nil {
		t.Error("expected nil engineConfig for null config")
	}
}

func TestNginxInstanceModelToCreateRequestWithOptionals(t *testing.T) {
	ctx := context.Background()
	diags := diag.Diagnostics{}

	model := NginxInstanceModel{
		Name:       types.StringValue("my-nginx"),
		Version:    types.StringValue("1.27"),
		Flavor:     types.StringValue("web.gp1.medium"),
		StorageGB:  types.Int64Value(40),
		VPCID:      types.StringValue("vpc-1"),
		SubnetID:   types.StringValue("sn-1"),
		TLSEnabled: types.BoolValue(true),
		Config:     mustCfgMap(map[string]string{"client_max_body_size": "10m"}),
	}

	req := model.toCreateRequest(ctx, &diags)
	if diags.HasError() {
		t.Fatalf("unexpected diagnostics: %v", diags.Errors())
	}

	if req.TLSEnabled == nil || !*req.TLSEnabled {
		t.Error("expected tlsEnabled true")
	}
	if req.EngineConfig["client_max_body_size"] != "10m" {
		t.Errorf("expected engineConfig client_max_body_size=10m, got %v", req.EngineConfig)
	}
}

func TestNginxInstanceModelToUpdateRequest(t *testing.T) {
	ctx := context.Background()
	diags := diag.Diagnostics{}

	plan := NginxInstanceModel{
		Name:       types.StringValue("new-name"),
		Flavor:     types.StringValue("web.gp1.large"),
		StorageGB:  types.Int64Value(80),
		TLSEnabled: types.BoolValue(true),
		Config:     mustCfgMap(map[string]string{"gzip": "on"}),
	}
	state := NginxInstanceModel{
		Name:       types.StringValue("old-name"),
		Flavor:     types.StringValue("web.gp1.small"),
		StorageGB:  types.Int64Value(20),
		TLSEnabled: types.BoolValue(false),
		Config:     types.MapNull(types.StringType),
	}

	req := plan.toUpdateRequest(ctx, &state, &diags)
	if diags.HasError() {
		t.Fatalf("unexpected diagnostics: %v", diags.Errors())
	}
	if req.Name == nil || *req.Name != "new-name" {
		t.Error("expected name update")
	}
	if req.Flavor == nil || *req.Flavor != "web.gp1.large" {
		t.Error("expected flavorId update")
	}
	if req.StorageGB == nil || *req.StorageGB != 80 {
		t.Error("expected storageGb update")
	}
	if req.TLSEnabled == nil || !*req.TLSEnabled {
		t.Error("expected tlsEnabled update")
	}
	if req.EngineConfig["gzip"] != "on" {
		t.Errorf("expected engineConfig gzip=on, got %v", req.EngineConfig)
	}
}

func TestNginxInstanceModelToUpdateRequestNoChanges(t *testing.T) {
	ctx := context.Background()
	diags := diag.Diagnostics{}

	same := NginxInstanceModel{
		Name:       types.StringValue("same"),
		Flavor:     types.StringValue("web.gp1.small"),
		StorageGB:  types.Int64Value(20),
		TLSEnabled: types.BoolValue(true),
		Config:     mustCfgMap(map[string]string{"gzip": "on"}),
	}

	req := same.toUpdateRequest(ctx, &same, &diags)
	if diags.HasError() {
		t.Fatalf("unexpected diagnostics: %v", diags.Errors())
	}
	if req.Name != nil || req.Flavor != nil || req.StorageGB != nil || req.TLSEnabled != nil ||
		req.EngineConfig != nil {
		t.Error("expected no changes in update request")
	}
}

func TestNginxInstanceModelFromAPI(t *testing.T) {
	ctx := context.Background()
	diags := diag.Diagnostics{}

	api := &apiWebserverInstance{
		ID:            "nginx-123",
		Name:          "my-nginx",
		Engine:        "nginx",
		EngineVersion: "1.27",
		Flavor:        "web.gp1.small",
		StorageGB:     20,
		VPCID:         "vpc-1",
		SubnetID:      "sn-1",
		TLSEnabled:    true,
		EngineConfig:  map[string]string{"client_max_body_size": "10m"},
		Status:        "running",
		PrivateIP:     "10.0.1.5",
		Port:          443,
		CreatedAt:     "2025-01-01T00:00:00Z",
		UpdatedAt:     "2025-01-02T00:00:00Z",
		TenantID:      "t-1",
	}

	var model NginxInstanceModel
	model.fromAPI(ctx, api, &diags)
	if diags.HasError() {
		t.Fatalf("unexpected diagnostics: %v", diags.Errors())
	}

	if model.ID.ValueString() != "nginx-123" {
		t.Errorf("expected ID nginx-123, got %s", model.ID.ValueString())
	}
	if model.Flavor.ValueString() != "web.gp1.small" {
		t.Errorf("expected flavor web.gp1.small, got %s", model.Flavor.ValueString())
	}
	if model.VPCID.ValueString() != "vpc-1" {
		t.Errorf("expected vpc_id vpc-1, got %s", model.VPCID.ValueString())
	}
	if model.SubnetID.ValueString() != "sn-1" {
		t.Errorf("expected subnet_id sn-1, got %s", model.SubnetID.ValueString())
	}
	cfg := map[string]string{}
	model.Config.ElementsAs(ctx, &cfg, false)
	if cfg["client_max_body_size"] != "10m" {
		t.Errorf("expected config client_max_body_size=10m, got %v", cfg)
	}
	if model.Port.ValueInt64() != 443 {
		t.Errorf("expected port 443, got %d", model.Port.ValueInt64())
	}
	if model.TenantID.ValueString() != "t-1" {
		t.Errorf("expected tenant_id t-1, got %s", model.TenantID.ValueString())
	}
}

func TestNginxInstanceModelFromAPINulls(t *testing.T) {
	ctx := context.Background()
	diags := diag.Diagnostics{}

	api := &apiWebserverInstance{
		ID:            "nginx-123",
		Name:          "my-nginx",
		Engine:        "nginx",
		EngineVersion: "1.27",
		Flavor:        "web.gp1.small",
		StorageGB:     20,
		VPCID:         "vpc-1",
		SubnetID:      "sn-1",
		Status:        "provisioning",
		CreatedAt:     "2025-01-01T00:00:00Z",
	}

	var model NginxInstanceModel
	model.fromAPI(ctx, api, &diags)
	if diags.HasError() {
		t.Fatalf("unexpected diagnostics: %v", diags.Errors())
	}

	if !model.Config.IsNull() {
		t.Error("expected null config")
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
	if resp.TypeName != "frostmoln_nginx_instance" {
		t.Errorf("expected type name frostmoln_nginx_instance, got %s", resp.TypeName)
	}
}

func TestSchema(t *testing.T) {
	r := NewResource()
	req := resource.SchemaRequest{}
	var resp resource.SchemaResponse
	r.Schema(context.Background(), req, &resp)

	requiredAttrs := []string{"name", "version", "flavor", "storage_gb", "vpc_id", "subnet_id"}
	for _, attr := range requiredAttrs {
		if _, ok := resp.Schema.Attributes[attr]; !ok {
			t.Errorf("expected attribute %s in schema", attr)
		}
	}

	if _, ok := resp.Schema.Attributes["config"]; !ok {
		t.Error("expected config attribute in schema")
	}

	computedAttrs := []string{"id", "status", "private_ip", "port", "created_at", "updated_at", "tenant_id"}
	for _, attr := range computedAttrs {
		if _, ok := resp.Schema.Attributes[attr]; !ok {
			t.Errorf("expected computed attribute %s in schema", attr)
		}
	}
}

func TestConfigureNilProviderData(t *testing.T) {
	r := &nginxInstanceResource{}
	req := resource.ConfigureRequest{}
	var resp resource.ConfigureResponse
	r.Configure(context.Background(), req, &resp)
	if resp.Diagnostics.HasError() {
		t.Errorf("expected no errors for nil provider data, got %v", resp.Diagnostics.Errors())
	}
}

func TestConfigureWrongType(t *testing.T) {
	r := &nginxInstanceResource{}
	req := resource.ConfigureRequest{ProviderData: "not-a-client"}
	var resp resource.ConfigureResponse
	r.Configure(context.Background(), req, &resp)
	if !resp.Diagnostics.HasError() {
		t.Error("expected error for wrong provider data type")
	}
}

func TestConfigureValidClient(t *testing.T) {
	r := &nginxInstanceResource{}
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

func buildNginxInstanceState(t *testing.T, model NginxInstanceModel) tfsdk.State {
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

func buildNginxInstancePlan(t *testing.T, model NginxInstanceModel) tfsdk.Plan {
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

func emptyNginxInstanceState(t *testing.T) tfsdk.State {
	t.Helper()
	r := NewResource()
	var schemaResp resource.SchemaResponse
	r.Schema(context.Background(), resource.SchemaRequest{}, &schemaResp)

	stateVal := tftypes.NewValue(schemaResp.Schema.Type().TerraformType(context.Background()), nil)
	return tfsdk.State{Schema: schemaResp.Schema, Raw: stateVal}
}

func newTestNginxResource(c *client.Client) *nginxInstanceResource {
	return &nginxInstanceResource{
		client:       c,
		pollInterval: 5 * time.Millisecond,
		pollTimeout:  5 * time.Second,
	}
}

func baseNginxModel() NginxInstanceModel {
	return NginxInstanceModel{
		Name:       types.StringValue("my-nginx"),
		Version:    types.StringValue("1.27"),
		Flavor:     types.StringValue("web.gp1.small"),
		StorageGB:  types.Int64Value(20),
		VPCID:      types.StringValue("vpc-1"),
		SubnetID:   types.StringValue("sn-1"),
		TLSEnabled: types.BoolValue(true),
		Config:     mustCfgMap(map[string]string{"client_max_body_size": "10m"}),
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
			if body.Engine != "nginx" {
				t.Errorf("expected engine nginx, got %s", body.Engine)
			}
			if body.Flavor != "web.gp1.small" {
				t.Errorf("expected flavorId web.gp1.small, got %s", body.Flavor)
			}
			if body.VPCID != "vpc-1" {
				t.Errorf("expected vpcId vpc-1, got %s", body.VPCID)
			}
			if body.SubnetID != "sn-1" {
				t.Errorf("expected subnetId sn-1, got %s", body.SubnetID)
			}
			if body.EngineConfig["client_max_body_size"] != "10m" {
				t.Errorf("expected engineConfig object client_max_body_size=10m, got %v", body.EngineConfig)
			}
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(apiWebserverInstance{
				ID:            "nginx-new",
				Name:          body.Name,
				Engine:        "nginx",
				EngineVersion: body.EngineVersion,
				Flavor:        body.Flavor,
				StorageGB:     body.StorageGB,
				VPCID:         body.VPCID,
				SubnetID:      body.SubnetID,
				EngineConfig:  body.EngineConfig,
				Status:        "provisioning",
				CreatedAt:     "2025-01-01T00:00:00Z",
			})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-1/webservers/nginx-new":
			count := callCount.Add(1)
			status := "provisioning"
			if count >= 2 {
				status = "running"
			}
			_ = json.NewEncoder(w).Encode(apiWebserverInstance{
				ID:            "nginx-new",
				Name:          "test-nginx",
				Engine:        "nginx",
				EngineVersion: "1.27",
				Flavor:        "web.gp1.small",
				StorageGB:     20,
				VPCID:         "vpc-1",
				SubnetID:      "sn-1",
				TLSEnabled:    true,
				EngineConfig:  map[string]string{"client_max_body_size": "10m"},
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

	r := newTestNginxResource(c)

	model := baseNginxModel()
	model.Name = types.StringValue("test-nginx")
	plan := buildNginxInstancePlan(t, model)

	createResp := resource.CreateResponse{State: emptyNginxInstanceState(t)}
	r.Create(context.Background(), resource.CreateRequest{Plan: plan}, &createResp)

	if createResp.Diagnostics.HasError() {
		t.Fatalf("create failed: %v", createResp.Diagnostics.Errors())
	}

	var result NginxInstanceModel
	createResp.State.Get(context.Background(), &result)
	if result.ID.ValueString() != "nginx-new" {
		t.Errorf("expected ID nginx-new, got %s", result.ID.ValueString())
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

	r := newTestNginxResource(c)

	createResp := resource.CreateResponse{State: emptyNginxInstanceState(t)}
	r.Create(context.Background(), resource.CreateRequest{Plan: buildNginxInstancePlan(t, baseNginxModel())}, &createResp)
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
				ID: "nginx-err", Name: "x", Engine: "nginx", EngineVersion: "1.27",
				Flavor: "f", StorageGB: 20, VPCID: "vpc-1", SubnetID: "sn-1",
				Status: "provisioning", CreatedAt: "2025-01-01T00:00:00Z",
			})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-1/webservers/nginx-err":
			_ = json.NewEncoder(w).Encode(apiWebserverInstance{
				ID: "nginx-err", Name: "x", Engine: "nginx", EngineVersion: "1.27",
				Flavor: "f", StorageGB: 20, VPCID: "vpc-1", SubnetID: "sn-1",
				Status: "failed", CreatedAt: "2025-01-01T00:00:00Z",
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key", client.WithHTTPClient(server.Client())) // pragma: allowlist secret
	c.SetTenantIDForTest("t-1")

	r := newTestNginxResource(c)

	createResp := resource.CreateResponse{State: emptyNginxInstanceState(t)}
	r.Create(context.Background(), resource.CreateRequest{Plan: buildNginxInstancePlan(t, baseNginxModel())}, &createResp)
	if !createResp.Diagnostics.HasError() {
		t.Error("expected error when instance enters failed state during polling")
	}
}

func TestRead(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-1/webservers/nginx-123" {
			_ = json.NewEncoder(w).Encode(apiWebserverInstance{
				ID:            "nginx-123",
				Name:          "my-nginx",
				Engine:        "nginx",
				EngineVersion: "1.27",
				Flavor:        "web.gp1.small",
				StorageGB:     20,
				VPCID:         "vpc-1",
				SubnetID:      "sn-1",
				TLSEnabled:    true,
				EngineConfig:  map[string]string{"client_max_body_size": "10m"},
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

	r := &nginxInstanceResource{client: c}

	model := baseNginxModel()
	model.ID = types.StringValue("nginx-123")
	model.Status = types.StringValue("running")
	model.CreatedAt = types.StringValue("2025-01-01T00:00:00Z")
	state := buildNginxInstanceState(t, model)

	readResp := resource.ReadResponse{State: state}
	r.Read(context.Background(), resource.ReadRequest{State: state}, &readResp)

	if readResp.Diagnostics.HasError() {
		t.Fatalf("read failed: %v", readResp.Diagnostics.Errors())
	}

	var result NginxInstanceModel
	readResp.State.Get(context.Background(), &result)
	if result.Status.ValueString() != "running" {
		t.Errorf("expected status running, got %s", result.Status.ValueString())
	}
	cfg := map[string]string{}
	result.Config.ElementsAs(context.Background(), &cfg, false)
	if cfg["client_max_body_size"] != "10m" {
		t.Errorf("expected config client_max_body_size=10m, got %v", cfg)
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

	r := &nginxInstanceResource{client: c}

	model := baseNginxModel()
	model.ID = types.StringValue("nginx-gone")
	model.Status = types.StringValue("running")
	model.CreatedAt = types.StringValue("2025-01-01T00:00:00Z")
	state := buildNginxInstanceState(t, model)

	readResp := resource.ReadResponse{State: state}
	r.Read(context.Background(), resource.ReadRequest{State: state}, &readResp)

	if readResp.Diagnostics.HasError() {
		t.Fatalf("expected no error for not-found, got %v", readResp.Diagnostics.Errors())
	}

	var result NginxInstanceModel
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

	r := &nginxInstanceResource{client: c}

	model := baseNginxModel()
	model.ID = types.StringValue("nginx-123")
	model.Status = types.StringValue("running")
	model.CreatedAt = types.StringValue("2025-01-01T00:00:00Z")
	state := buildNginxInstanceState(t, model)

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
		case r.Method == http.MethodPut && r.URL.Path == "/v1/tenants/t-1/webservers/nginx-123":
			_ = json.NewDecoder(r.Body).Decode(&updatedBody)
			w.WriteHeader(http.StatusOK)
			_, _ = fmt.Fprint(w, `{}`)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-1/webservers/nginx-123":
			_ = json.NewEncoder(w).Encode(apiWebserverInstance{
				ID:            "nginx-123",
				Name:          "updated-nginx",
				Engine:        "nginx",
				EngineVersion: "1.27",
				Flavor:        "web.gp1.large",
				StorageGB:     80,
				VPCID:         "vpc-1",
				SubnetID:      "sn-1",
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

	r := newTestNginxResource(c)

	stateModel := baseNginxModel()
	stateModel.ID = types.StringValue("nginx-123")
	stateModel.Name = types.StringValue("old-nginx")
	stateModel.Status = types.StringValue("running")
	stateModel.CreatedAt = types.StringValue("2025-01-01T00:00:00Z")
	state := buildNginxInstanceState(t, stateModel)

	planModel := stateModel
	planModel.Name = types.StringValue("updated-nginx")
	planModel.Flavor = types.StringValue("web.gp1.large")
	planModel.StorageGB = types.Int64Value(80)
	plan := buildNginxInstancePlan(t, planModel)

	updateResp := resource.UpdateResponse{State: state}
	r.Update(context.Background(), resource.UpdateRequest{Plan: plan, State: state}, &updateResp)

	if updateResp.Diagnostics.HasError() {
		t.Fatalf("update failed: %v", updateResp.Diagnostics.Errors())
	}

	if updatedBody.Name == nil || *updatedBody.Name != "updated-nginx" {
		t.Error("expected name in update request")
	}
	if updatedBody.Flavor == nil || *updatedBody.Flavor != "web.gp1.large" {
		t.Error("expected flavorId in update request")
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

	r := newTestNginxResource(c)

	stateModel := baseNginxModel()
	stateModel.ID = types.StringValue("nginx-123")
	stateModel.Status = types.StringValue("running")
	stateModel.CreatedAt = types.StringValue("2025-01-01T00:00:00Z")
	state := buildNginxInstanceState(t, stateModel)

	planModel := stateModel
	planModel.Name = types.StringValue("new")
	plan := buildNginxInstancePlan(t, planModel)

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
		case r.Method == http.MethodDelete && r.URL.Path == "/v1/tenants/t-1/webservers/nginx-123":
			deleted = true
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-1/webservers/nginx-123":
			if deleted {
				w.WriteHeader(http.StatusNotFound)
				_ = json.NewEncoder(w).Encode(map[string]any{"code": "NOT_FOUND", "message": "not found"})
			} else {
				_ = json.NewEncoder(w).Encode(apiWebserverInstance{ID: "nginx-123", Status: "deleting"})
			}
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key", client.WithHTTPClient(server.Client())) // pragma: allowlist secret
	c.SetTenantIDForTest("t-1")

	r := newTestNginxResource(c)

	model := baseNginxModel()
	model.ID = types.StringValue("nginx-123")
	model.Status = types.StringValue("running")
	model.CreatedAt = types.StringValue("2025-01-01T00:00:00Z")
	state := buildNginxInstanceState(t, model)

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

	r := newTestNginxResource(c)

	model := baseNginxModel()
	model.ID = types.StringValue("nginx-gone")
	model.Status = types.StringValue("running")
	model.CreatedAt = types.StringValue("2025-01-01T00:00:00Z")
	state := buildNginxInstanceState(t, model)

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
		case r.Method == http.MethodDelete && r.URL.Path == "/v1/tenants/t-1/webservers/nginx-123":
			deleted = true
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-1/webservers/nginx-123":
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(map[string]any{"code": "INTERNAL", "message": "boom"})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key", client.WithHTTPClient(server.Client())) // pragma: allowlist secret
	c.SetTenantIDForTest("t-1")

	r := &nginxInstanceResource{client: c, pollInterval: 5 * time.Millisecond, pollTimeout: 60 * time.Millisecond}

	model := baseNginxModel()
	model.ID = types.StringValue("nginx-123")
	model.Status = types.StringValue("running")
	model.CreatedAt = types.StringValue("2025-01-01T00:00:00Z")
	state := buildNginxInstanceState(t, model)

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
	r := &nginxInstanceResource{}
	if got := r.getPollInterval(); got != 5*time.Second {
		t.Errorf("expected default poll interval 5s, got %v", got)
	}
	if got := r.getPollTimeout(); got != 15*time.Minute {
		t.Errorf("expected default poll timeout 15m, got %v", got)
	}
}

func TestImportState(t *testing.T) {
	r := NewResource().(resource.ResourceWithImportState)
	importResp := resource.ImportStateResponse{State: emptyNginxInstanceState(t)}
	r.ImportState(context.Background(), resource.ImportStateRequest{ID: "nginx-123"}, &importResp)
	if importResp.Diagnostics.HasError() {
		t.Fatalf("import failed: %v", importResp.Diagnostics.Errors())
	}

	var result NginxInstanceModel
	importResp.State.Get(context.Background(), &result)
	if result.ID.ValueString() != "nginx-123" {
		t.Errorf("expected imported ID nginx-123, got %s", result.ID.ValueString())
	}
}
