package cache_instance

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

func TestCacheInstanceModelToCreateRequest(t *testing.T) {
	ctx := context.Background()
	diags := diag.Diagnostics{}

	model := CacheInstanceModel{
		Name:            types.StringValue("my-cache"),
		Engine:          types.StringValue("redis"),
		EngineVersion:   types.StringValue("7.2"),
		FlavorID:        types.StringValue("cache.gp1.small"),
		VPCID:           types.StringValue("vpc-123"),
		SubnetID:        types.StringValue("subnet-456"),
		PersistenceMode: types.StringNull(),
		EvictionPolicy:  types.StringNull(),
	}

	req := model.toCreateRequest(ctx, &diags)
	if diags.HasError() {
		t.Fatalf("unexpected diagnostics: %v", diags.Errors())
	}

	if req.Engine != "redis" {
		t.Errorf("expected engine redis, got %s", req.Engine)
	}
	if req.Name != "my-cache" {
		t.Errorf("expected name my-cache, got %s", req.Name)
	}
	if req.EngineVersion != "7.2" {
		t.Errorf("expected engineVersion 7.2, got %s", req.EngineVersion)
	}
	if req.FlavorID != "cache.gp1.small" {
		t.Errorf("expected flavorId cache.gp1.small, got %s", req.FlavorID)
	}
	if req.VPCID != "vpc-123" {
		t.Errorf("expected vpcId vpc-123, got %s", req.VPCID)
	}
	if req.SubnetID != "subnet-456" {
		t.Errorf("expected subnetId subnet-456, got %s", req.SubnetID)
	}
	if req.PersistenceMode != "" {
		t.Errorf("expected empty persistenceMode for null, got %s", req.PersistenceMode)
	}
	if req.EvictionPolicy != "" {
		t.Errorf("expected empty evictionPolicy for null, got %s", req.EvictionPolicy)
	}
}

func TestCacheInstanceModelToCreateRequestWithOptionals(t *testing.T) {
	ctx := context.Background()
	diags := diag.Diagnostics{}

	model := CacheInstanceModel{
		Name:            types.StringValue("my-cache"),
		Engine:          types.StringValue("valkey"),
		EngineVersion:   types.StringValue("7.4"),
		FlavorID:        types.StringValue("cache.gp1.medium"),
		VPCID:           types.StringValue("vpc-123"),
		SubnetID:        types.StringValue("subnet-456"),
		PersistenceMode: types.StringValue("aof"),
		EvictionPolicy:  types.StringValue("allkeys-lru"),
	}

	req := model.toCreateRequest(ctx, &diags)
	if diags.HasError() {
		t.Fatalf("unexpected diagnostics: %v", diags.Errors())
	}

	if req.Engine != "valkey" {
		t.Errorf("expected engine valkey, got %s", req.Engine)
	}
	if req.PersistenceMode != "aof" {
		t.Errorf("expected persistenceMode aof, got %s", req.PersistenceMode)
	}
	if req.EvictionPolicy != "allkeys-lru" {
		t.Errorf("expected evictionPolicy allkeys-lru, got %s", req.EvictionPolicy)
	}
}

func TestCacheInstanceModelToUpdateRequest(t *testing.T) {
	plan := CacheInstanceModel{
		Name:            types.StringValue("new-name"),
		FlavorID:        types.StringValue("cache.gp1.large"),
		PersistenceMode: types.StringValue("aof"),
		EvictionPolicy:  types.StringValue("allkeys-lru"),
	}
	state := CacheInstanceModel{
		Name:            types.StringValue("old-name"),
		FlavorID:        types.StringValue("cache.gp1.small"),
		PersistenceMode: types.StringValue("rdb"),
		EvictionPolicy:  types.StringValue("noeviction"),
	}

	req := plan.toUpdateRequest(&state)
	if req.Name == nil || *req.Name != "new-name" {
		t.Error("expected name update to new-name")
	}
	if req.FlavorID == nil || *req.FlavorID != "cache.gp1.large" {
		t.Error("expected flavorId update to cache.gp1.large")
	}
	if req.PersistenceMode == nil || *req.PersistenceMode != "aof" {
		t.Error("expected persistenceMode update to aof")
	}
	if req.EvictionPolicy == nil || *req.EvictionPolicy != "allkeys-lru" {
		t.Error("expected evictionPolicy update to allkeys-lru")
	}
}

func TestCacheInstanceModelToUpdateRequestNoChanges(t *testing.T) {
	same := CacheInstanceModel{
		Name:            types.StringValue("same"),
		FlavorID:        types.StringValue("cache.gp1.small"),
		PersistenceMode: types.StringValue("rdb"),
		EvictionPolicy:  types.StringValue("noeviction"),
	}

	req := same.toUpdateRequest(&same)
	if req.Name != nil || req.FlavorID != nil || req.PersistenceMode != nil || req.EvictionPolicy != nil {
		t.Error("expected no changes in update request")
	}
}

func TestCacheInstanceModelFromAPI(t *testing.T) {
	ctx := context.Background()
	diags := diag.Diagnostics{}

	api := &apiCacheInstance{
		ID:              "cache-123",
		Name:            "my-cache",
		Engine:          "redis",
		EngineVersion:   "7.2",
		FlavorID:        "cache.gp1.small",
		VPCID:           "vpc-123",
		SubnetID:        "subnet-456",
		PersistenceMode: "rdb",
		EvictionPolicy:  "noeviction",
		Status:          "running",
		PrivateIP:       "10.0.1.5",
		Port:            6379,
		AdminUsername:   "default",
		CreatedAt:       "2025-01-01T00:00:00Z",
		UpdatedAt:       "2025-01-02T00:00:00Z",
	}

	var model CacheInstanceModel
	model.fromAPI(ctx, api, &diags)
	if diags.HasError() {
		t.Fatalf("unexpected diagnostics: %v", diags.Errors())
	}

	if model.ID.ValueString() != "cache-123" {
		t.Errorf("expected ID cache-123, got %s", model.ID.ValueString())
	}
	if model.Engine.ValueString() != "redis" {
		t.Errorf("expected engine redis, got %s", model.Engine.ValueString())
	}
	if model.Port.ValueInt64() != 6379 {
		t.Errorf("expected port 6379, got %d", model.Port.ValueInt64())
	}
	if model.AdminUsername.ValueString() != "default" {
		t.Errorf("expected admin_username default, got %s", model.AdminUsername.ValueString())
	}
	if model.Status.ValueString() != "running" {
		t.Errorf("expected status running, got %s", model.Status.ValueString())
	}
	if model.UpdatedAt.ValueString() != "2025-01-02T00:00:00Z" {
		t.Errorf("expected updated_at set, got %s", model.UpdatedAt.ValueString())
	}
}

func TestCacheInstanceModelFromAPINulls(t *testing.T) {
	ctx := context.Background()
	diags := diag.Diagnostics{}

	api := &apiCacheInstance{
		ID:              "cache-123",
		Name:            "my-cache",
		Engine:          "redis",
		EngineVersion:   "7.2",
		FlavorID:        "cache.gp1.small",
		VPCID:           "vpc-123",
		SubnetID:        "subnet-456",
		PersistenceMode: "rdb",
		EvictionPolicy:  "noeviction",
		Status:          "provisioning",
		CreatedAt:       "2025-01-01T00:00:00Z",
	}

	var model CacheInstanceModel
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
	if !model.AdminUsername.IsNull() {
		t.Error("expected null admin_username")
	}
	if !model.UpdatedAt.IsNull() {
		t.Error("expected null updated_at")
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
	if resp.TypeName != "frostmoln_cache_instance" {
		t.Errorf("expected type name frostmoln_cache_instance, got %s", resp.TypeName)
	}
}

func TestSchema(t *testing.T) {
	r := NewResource()
	req := resource.SchemaRequest{}
	var resp resource.SchemaResponse
	r.Schema(context.Background(), req, &resp)

	requiredAttrs := []string{"name", "engine", "engine_version", "flavor_id", "vpc_id", "subnet_id"}
	for _, attr := range requiredAttrs {
		if _, ok := resp.Schema.Attributes[attr]; !ok {
			t.Errorf("expected attribute %s in schema", attr)
		}
	}

	computedAttrs := []string{"id", "status", "private_ip", "port", "admin_username", "created_at", "updated_at"}
	for _, attr := range computedAttrs {
		if _, ok := resp.Schema.Attributes[attr]; !ok {
			t.Errorf("expected computed attribute %s in schema", attr)
		}
	}
}

func TestConfigureNilProviderData(t *testing.T) {
	r := &cacheInstanceResource{}
	req := resource.ConfigureRequest{}
	var resp resource.ConfigureResponse
	r.Configure(context.Background(), req, &resp)
	if resp.Diagnostics.HasError() {
		t.Errorf("expected no errors for nil provider data, got %v", resp.Diagnostics.Errors())
	}
}

func TestConfigureWrongType(t *testing.T) {
	r := &cacheInstanceResource{}
	req := resource.ConfigureRequest{ProviderData: "not-a-client"}
	var resp resource.ConfigureResponse
	r.Configure(context.Background(), req, &resp)
	if !resp.Diagnostics.HasError() {
		t.Error("expected error for wrong provider data type")
	}
}

func TestConfigureValidClient(t *testing.T) {
	r := &cacheInstanceResource{}
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

func buildCacheInstanceState(t *testing.T, model CacheInstanceModel) tfsdk.State {
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

func buildCacheInstancePlan(t *testing.T, model CacheInstanceModel) tfsdk.Plan {
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

func emptyCacheInstanceState(t *testing.T) tfsdk.State {
	t.Helper()
	r := NewResource()
	var schemaResp resource.SchemaResponse
	r.Schema(context.Background(), resource.SchemaRequest{}, &schemaResp)

	stateVal := tftypes.NewValue(schemaResp.Schema.Type().TerraformType(context.Background()), nil)
	return tfsdk.State{Schema: schemaResp.Schema, Raw: stateVal}
}

func newTestCacheResource(c *client.Client) *cacheInstanceResource {
	return &cacheInstanceResource{
		client:       c,
		pollInterval: 5 * time.Millisecond,
		pollTimeout:  5 * time.Second,
	}
}

// --- CRUD tests ---

func TestCreate(t *testing.T) {
	var callCount atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/tenants/t-1/caches":
			var body apiCreateCacheInstanceRequest
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("failed to decode request: %v", err)
			}
			if body.Engine != "redis" {
				t.Errorf("expected engine redis, got %s", body.Engine)
			}
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(apiCacheInstance{
				ID:              "cache-new",
				Name:            body.Name,
				Engine:          body.Engine,
				EngineVersion:   body.EngineVersion,
				FlavorID:        body.FlavorID,
				VPCID:           body.VPCID,
				SubnetID:        body.SubnetID,
				PersistenceMode: "rdb",
				EvictionPolicy:  "noeviction",
				Status:          "provisioning",
				CreatedAt:       "2025-01-01T00:00:00Z",
			})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-1/caches/cache-new":
			count := callCount.Add(1)
			status := "provisioning"
			if count >= 2 {
				status = "running"
			}
			_ = json.NewEncoder(w).Encode(apiCacheInstance{
				ID:              "cache-new",
				Name:            "test-cache",
				Engine:          "redis",
				EngineVersion:   "7.2",
				FlavorID:        "cache.gp1.small",
				VPCID:           "vpc-1",
				SubnetID:        "sn-1",
				PersistenceMode: "rdb",
				EvictionPolicy:  "noeviction",
				Status:          status,
				PrivateIP:       "10.0.1.5",
				Port:            6379,
				AdminUsername:   "default",
				CreatedAt:       "2025-01-01T00:00:00Z",
			})
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key", client.WithHTTPClient(server.Client())) // pragma: allowlist secret
	c.SetTenantIDForTest("t-1")

	r := newTestCacheResource(c)

	plan := buildCacheInstancePlan(t, CacheInstanceModel{
		Name:            types.StringValue("test-cache"),
		Engine:          types.StringValue("redis"),
		EngineVersion:   types.StringValue("7.2"),
		FlavorID:        types.StringValue("cache.gp1.small"),
		VPCID:           types.StringValue("vpc-1"),
		SubnetID:        types.StringValue("sn-1"),
		PersistenceMode: types.StringValue("rdb"),
		EvictionPolicy:  types.StringValue("noeviction"),
	})

	createResp := resource.CreateResponse{State: emptyCacheInstanceState(t)}
	r.Create(context.Background(), resource.CreateRequest{Plan: plan}, &createResp)

	if createResp.Diagnostics.HasError() {
		t.Fatalf("create failed: %v", createResp.Diagnostics.Errors())
	}

	var result CacheInstanceModel
	createResp.State.Get(context.Background(), &result)
	if result.ID.ValueString() != "cache-new" {
		t.Errorf("expected ID cache-new, got %s", result.ID.ValueString())
	}
	if result.Status.ValueString() != "running" {
		t.Errorf("expected status running, got %s", result.Status.ValueString())
	}
	if result.Port.ValueInt64() != 6379 {
		t.Errorf("expected port 6379, got %d", result.Port.ValueInt64())
	}
}

func TestCreateAPIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/v1/tenants/t-1/caches" {
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(map[string]any{"code": "INTERNAL", "message": "boom"})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key", client.WithHTTPClient(server.Client())) // pragma: allowlist secret
	c.SetTenantIDForTest("t-1")

	r := newTestCacheResource(c)

	plan := buildCacheInstancePlan(t, CacheInstanceModel{
		Name:            types.StringValue("test-cache"),
		Engine:          types.StringValue("redis"),
		EngineVersion:   types.StringValue("7.2"),
		FlavorID:        types.StringValue("cache.gp1.small"),
		VPCID:           types.StringValue("vpc-1"),
		SubnetID:        types.StringValue("sn-1"),
		PersistenceMode: types.StringValue("rdb"),
		EvictionPolicy:  types.StringValue("noeviction"),
	})

	createResp := resource.CreateResponse{State: emptyCacheInstanceState(t)}
	r.Create(context.Background(), resource.CreateRequest{Plan: plan}, &createResp)
	if !createResp.Diagnostics.HasError() {
		t.Error("expected error for API failure on create")
	}
}

func TestCreatePollErrorState(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/tenants/t-1/caches":
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(apiCacheInstance{
				ID: "cache-err", Name: "x", Engine: "redis", EngineVersion: "7.2",
				FlavorID: "f", VPCID: "v", SubnetID: "s", Status: "provisioning",
				CreatedAt: "2025-01-01T00:00:00Z",
			})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-1/caches/cache-err":
			_ = json.NewEncoder(w).Encode(apiCacheInstance{
				ID: "cache-err", Name: "x", Engine: "redis", EngineVersion: "7.2",
				FlavorID: "f", VPCID: "v", SubnetID: "s", Status: "error",
				CreatedAt: "2025-01-01T00:00:00Z",
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key", client.WithHTTPClient(server.Client())) // pragma: allowlist secret
	c.SetTenantIDForTest("t-1")

	r := newTestCacheResource(c)

	plan := buildCacheInstancePlan(t, CacheInstanceModel{
		Name:            types.StringValue("x"),
		Engine:          types.StringValue("redis"),
		EngineVersion:   types.StringValue("7.2"),
		FlavorID:        types.StringValue("f"),
		VPCID:           types.StringValue("v"),
		SubnetID:        types.StringValue("s"),
		PersistenceMode: types.StringValue("rdb"),
		EvictionPolicy:  types.StringValue("noeviction"),
	})

	createResp := resource.CreateResponse{State: emptyCacheInstanceState(t)}
	r.Create(context.Background(), resource.CreateRequest{Plan: plan}, &createResp)
	if !createResp.Diagnostics.HasError() {
		t.Error("expected error when instance enters error state during polling")
	}
}

func TestRead(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-1/caches/cache-123" {
			_ = json.NewEncoder(w).Encode(apiCacheInstance{
				ID:              "cache-123",
				Name:            "my-cache",
				Engine:          "redis",
				EngineVersion:   "7.2",
				FlavorID:        "cache.gp1.small",
				VPCID:           "vpc-1",
				SubnetID:        "sn-1",
				PersistenceMode: "rdb",
				EvictionPolicy:  "noeviction",
				Status:          "running",
				Port:            6379,
				CreatedAt:       "2025-01-01T00:00:00Z",
			})
			return
		}
		t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key", client.WithHTTPClient(server.Client())) // pragma: allowlist secret
	c.SetTenantIDForTest("t-1")

	r := &cacheInstanceResource{client: c}

	state := buildCacheInstanceState(t, CacheInstanceModel{
		ID:              types.StringValue("cache-123"),
		Name:            types.StringValue("my-cache"),
		Engine:          types.StringValue("redis"),
		EngineVersion:   types.StringValue("7.2"),
		FlavorID:        types.StringValue("cache.gp1.small"),
		VPCID:           types.StringValue("vpc-1"),
		SubnetID:        types.StringValue("sn-1"),
		PersistenceMode: types.StringValue("rdb"),
		EvictionPolicy:  types.StringValue("noeviction"),
		Status:          types.StringValue("running"),
		CreatedAt:       types.StringValue("2025-01-01T00:00:00Z"),
	})

	readResp := resource.ReadResponse{State: state}
	r.Read(context.Background(), resource.ReadRequest{State: state}, &readResp)

	if readResp.Diagnostics.HasError() {
		t.Fatalf("read failed: %v", readResp.Diagnostics.Errors())
	}

	var result CacheInstanceModel
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

	r := &cacheInstanceResource{client: c}

	state := buildCacheInstanceState(t, CacheInstanceModel{
		ID:              types.StringValue("cache-gone"),
		Name:            types.StringValue("gone"),
		Engine:          types.StringValue("redis"),
		EngineVersion:   types.StringValue("7.2"),
		FlavorID:        types.StringValue("cache.gp1.small"),
		VPCID:           types.StringValue("vpc-1"),
		SubnetID:        types.StringValue("sn-1"),
		PersistenceMode: types.StringValue("rdb"),
		EvictionPolicy:  types.StringValue("noeviction"),
		Status:          types.StringValue("running"),
		CreatedAt:       types.StringValue("2025-01-01T00:00:00Z"),
	})

	readResp := resource.ReadResponse{State: state}
	r.Read(context.Background(), resource.ReadRequest{State: state}, &readResp)

	if readResp.Diagnostics.HasError() {
		t.Fatalf("expected no error for not-found, got %v", readResp.Diagnostics.Errors())
	}

	var result CacheInstanceModel
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

	r := &cacheInstanceResource{client: c}

	state := buildCacheInstanceState(t, CacheInstanceModel{
		ID:              types.StringValue("cache-123"),
		Name:            types.StringValue("x"),
		Engine:          types.StringValue("redis"),
		EngineVersion:   types.StringValue("7.2"),
		FlavorID:        types.StringValue("f"),
		VPCID:           types.StringValue("v"),
		SubnetID:        types.StringValue("s"),
		PersistenceMode: types.StringValue("rdb"),
		EvictionPolicy:  types.StringValue("noeviction"),
		Status:          types.StringValue("running"),
		CreatedAt:       types.StringValue("2025-01-01T00:00:00Z"),
	})

	readResp := resource.ReadResponse{State: state}
	r.Read(context.Background(), resource.ReadRequest{State: state}, &readResp)
	if !readResp.Diagnostics.HasError() {
		t.Error("expected error for server error on read")
	}
}

func TestUpdate(t *testing.T) {
	var updatedBody apiUpdateCacheInstanceRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPut && r.URL.Path == "/v1/tenants/t-1/caches/cache-123":
			_ = json.NewDecoder(r.Body).Decode(&updatedBody)
			w.WriteHeader(http.StatusOK)
			_, _ = fmt.Fprint(w, `{}`)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-1/caches/cache-123":
			_ = json.NewEncoder(w).Encode(apiCacheInstance{
				ID:              "cache-123",
				Name:            "updated-cache",
				Engine:          "redis",
				EngineVersion:   "7.2",
				FlavorID:        "cache.gp1.large",
				VPCID:           "vpc-1",
				SubnetID:        "sn-1",
				PersistenceMode: "aof",
				EvictionPolicy:  "allkeys-lru",
				Status:          "running",
				Port:            6379,
				CreatedAt:       "2025-01-01T00:00:00Z",
			})
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key", client.WithHTTPClient(server.Client())) // pragma: allowlist secret
	c.SetTenantIDForTest("t-1")

	r := newTestCacheResource(c)

	state := buildCacheInstanceState(t, CacheInstanceModel{
		ID:              types.StringValue("cache-123"),
		Name:            types.StringValue("old-cache"),
		Engine:          types.StringValue("redis"),
		EngineVersion:   types.StringValue("7.2"),
		FlavorID:        types.StringValue("cache.gp1.small"),
		VPCID:           types.StringValue("vpc-1"),
		SubnetID:        types.StringValue("sn-1"),
		PersistenceMode: types.StringValue("rdb"),
		EvictionPolicy:  types.StringValue("noeviction"),
		Status:          types.StringValue("running"),
		CreatedAt:       types.StringValue("2025-01-01T00:00:00Z"),
	})

	plan := buildCacheInstancePlan(t, CacheInstanceModel{
		ID:              types.StringValue("cache-123"),
		Name:            types.StringValue("updated-cache"),
		Engine:          types.StringValue("redis"),
		EngineVersion:   types.StringValue("7.2"),
		FlavorID:        types.StringValue("cache.gp1.large"),
		VPCID:           types.StringValue("vpc-1"),
		SubnetID:        types.StringValue("sn-1"),
		PersistenceMode: types.StringValue("aof"),
		EvictionPolicy:  types.StringValue("allkeys-lru"),
		Status:          types.StringValue("running"),
		CreatedAt:       types.StringValue("2025-01-01T00:00:00Z"),
	})

	updateResp := resource.UpdateResponse{State: state}
	r.Update(context.Background(), resource.UpdateRequest{Plan: plan, State: state}, &updateResp)

	if updateResp.Diagnostics.HasError() {
		t.Fatalf("update failed: %v", updateResp.Diagnostics.Errors())
	}

	if updatedBody.Name == nil || *updatedBody.Name != "updated-cache" {
		t.Error("expected name in update request")
	}
	if updatedBody.FlavorID == nil || *updatedBody.FlavorID != "cache.gp1.large" {
		t.Error("expected flavorId in update request")
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

	r := newTestCacheResource(c)

	base := CacheInstanceModel{
		ID:              types.StringValue("cache-123"),
		Name:            types.StringValue("old"),
		Engine:          types.StringValue("redis"),
		EngineVersion:   types.StringValue("7.2"),
		FlavorID:        types.StringValue("cache.gp1.small"),
		VPCID:           types.StringValue("vpc-1"),
		SubnetID:        types.StringValue("sn-1"),
		PersistenceMode: types.StringValue("rdb"),
		EvictionPolicy:  types.StringValue("noeviction"),
		Status:          types.StringValue("running"),
		CreatedAt:       types.StringValue("2025-01-01T00:00:00Z"),
	}
	plan := base
	plan.Name = types.StringValue("new")

	state := buildCacheInstanceState(t, base)
	updateResp := resource.UpdateResponse{State: state}
	r.Update(context.Background(), resource.UpdateRequest{Plan: buildCacheInstancePlan(t, plan), State: state}, &updateResp)
	if !updateResp.Diagnostics.HasError() {
		t.Error("expected error for API failure on update")
	}
}

func TestDelete(t *testing.T) {
	deleted := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodDelete && r.URL.Path == "/v1/tenants/t-1/caches/cache-123":
			deleted = true
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-1/caches/cache-123":
			if deleted {
				w.WriteHeader(http.StatusNotFound)
				_ = json.NewEncoder(w).Encode(map[string]any{"code": "NOT_FOUND", "message": "not found"})
			} else {
				_ = json.NewEncoder(w).Encode(apiCacheInstance{ID: "cache-123", Status: "deleting"})
			}
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key", client.WithHTTPClient(server.Client())) // pragma: allowlist secret
	c.SetTenantIDForTest("t-1")

	r := newTestCacheResource(c)

	state := buildCacheInstanceState(t, CacheInstanceModel{
		ID:              types.StringValue("cache-123"),
		Name:            types.StringValue("my-cache"),
		Engine:          types.StringValue("redis"),
		EngineVersion:   types.StringValue("7.2"),
		FlavorID:        types.StringValue("cache.gp1.small"),
		VPCID:           types.StringValue("vpc-1"),
		SubnetID:        types.StringValue("sn-1"),
		PersistenceMode: types.StringValue("rdb"),
		EvictionPolicy:  types.StringValue("noeviction"),
		Status:          types.StringValue("running"),
		CreatedAt:       types.StringValue("2025-01-01T00:00:00Z"),
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

	r := newTestCacheResource(c)

	state := buildCacheInstanceState(t, CacheInstanceModel{
		ID:              types.StringValue("cache-gone"),
		Name:            types.StringValue("gone"),
		Engine:          types.StringValue("redis"),
		EngineVersion:   types.StringValue("7.2"),
		FlavorID:        types.StringValue("f"),
		VPCID:           types.StringValue("v"),
		SubnetID:        types.StringValue("s"),
		PersistenceMode: types.StringValue("rdb"),
		EvictionPolicy:  types.StringValue("noeviction"),
		Status:          types.StringValue("running"),
		CreatedAt:       types.StringValue("2025-01-01T00:00:00Z"),
	})

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
		case r.Method == http.MethodDelete && r.URL.Path == "/v1/tenants/t-1/caches/cache-123":
			deleted = true
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-1/caches/cache-123":
			// Return a server error during the delete-poll loop.
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(map[string]any{"code": "INTERNAL", "message": "boom"})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key", client.WithHTTPClient(server.Client())) // pragma: allowlist secret
	c.SetTenantIDForTest("t-1")

	r := &cacheInstanceResource{client: c, pollInterval: 5 * time.Millisecond, pollTimeout: 60 * time.Millisecond}

	state := buildCacheInstanceState(t, CacheInstanceModel{
		ID:              types.StringValue("cache-123"),
		Name:            types.StringValue("x"),
		Engine:          types.StringValue("redis"),
		EngineVersion:   types.StringValue("7.2"),
		FlavorID:        types.StringValue("f"),
		VPCID:           types.StringValue("v"),
		SubnetID:        types.StringValue("s"),
		PersistenceMode: types.StringValue("rdb"),
		EvictionPolicy:  types.StringValue("noeviction"),
		Status:          types.StringValue("running"),
		CreatedAt:       types.StringValue("2025-01-01T00:00:00Z"),
	})

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
	// Exercise the default getPollInterval/getPollTimeout branches via a
	// zero-value resource on the Read path (Read does not poll, so the
	// defaults are only constructed, not waited on).
	r := &cacheInstanceResource{}
	if got := r.getPollInterval(); got != 5*time.Second {
		t.Errorf("expected default poll interval 5s, got %v", got)
	}
	if got := r.getPollTimeout(); got != 15*time.Minute {
		t.Errorf("expected default poll timeout 15m, got %v", got)
	}
}

func TestImportState(t *testing.T) {
	r := NewResource().(resource.ResourceWithImportState)
	var schemaResp resource.SchemaResponse
	NewResource().Schema(context.Background(), resource.SchemaRequest{}, &schemaResp)

	importResp := resource.ImportStateResponse{State: emptyCacheInstanceState(t)}
	r.ImportState(context.Background(), resource.ImportStateRequest{ID: "cache-123"}, &importResp)
	if importResp.Diagnostics.HasError() {
		t.Fatalf("import failed: %v", importResp.Diagnostics.Errors())
	}

	var result CacheInstanceModel
	importResp.State.Get(context.Background(), &result)
	if result.ID.ValueString() != "cache-123" {
		t.Errorf("expected imported ID cache-123, got %s", result.ID.ValueString())
	}
}
