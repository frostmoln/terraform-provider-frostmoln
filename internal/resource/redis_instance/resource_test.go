package redis_instance

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

func TestRedisInstanceModelToCreateRequest(t *testing.T) {
	ctx := context.Background()
	diags := diag.Diagnostics{}

	model := RedisInstanceModel{
		Name:            types.StringValue("my-redis"),
		Version:         types.StringValue("7.2"),
		FlavorID:        types.StringValue("cache.small"),
		StorageGB:       types.Int64Value(10),
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
	if req.StorageGB != 10 {
		t.Errorf("expected storageGb 10, got %d", req.StorageGB)
	}
	if req.Name != "my-redis" {
		t.Errorf("expected name my-redis, got %s", req.Name)
	}
	if req.EngineVersion != "7.2" {
		t.Errorf("expected engineVersion 7.2, got %s", req.EngineVersion)
	}
	if req.FlavorID != "cache.small" {
		t.Errorf("expected flavorId cache.small, got %s", req.FlavorID)
	}
	if req.VPCID != "vpc-123" {
		t.Errorf("expected vpcId vpc-123, got %s", req.VPCID)
	}
	if req.SubnetID != "subnet-456" {
		t.Errorf("expected subnetId subnet-456, got %s", req.SubnetID)
	}
	if req.PersistenceMode != "" {
		t.Errorf("expected empty persistenceMode for null value, got %s", req.PersistenceMode)
	}
	if req.EvictionPolicy != "" {
		t.Errorf("expected empty evictionPolicy for null value, got %s", req.EvictionPolicy)
	}
}

func TestRedisInstanceModelToCreateRequestWithOptionals(t *testing.T) {
	ctx := context.Background()
	diags := diag.Diagnostics{}

	model := RedisInstanceModel{
		Name:            types.StringValue("my-redis"),
		Version:         types.StringValue("7.4"),
		FlavorID:        types.StringValue("cache.medium"),
		VPCID:           types.StringValue("vpc-123"),
		SubnetID:        types.StringValue("subnet-456"),
		PersistenceMode: types.StringValue("aof"),
		EvictionPolicy:  types.StringValue("allkeys-lru"),
	}

	req := model.toCreateRequest(ctx, &diags)
	if diags.HasError() {
		t.Fatalf("unexpected diagnostics: %v", diags.Errors())
	}

	if req.Engine != "redis" {
		t.Errorf("expected engine redis, got %s", req.Engine)
	}
	if req.PersistenceMode != "aof" {
		t.Errorf("expected persistenceMode aof, got %s", req.PersistenceMode)
	}
	if req.EvictionPolicy != "allkeys-lru" {
		t.Errorf("expected evictionPolicy allkeys-lru, got %s", req.EvictionPolicy)
	}
}

func TestRedisInstanceModelToUpdateRequest(t *testing.T) {
	plan := RedisInstanceModel{
		Name:            types.StringValue("new-name"),
		FlavorID:        types.StringValue("cache.large"),
		PersistenceMode: types.StringValue("aof"),
		EvictionPolicy:  types.StringValue("allkeys-lru"),
	}
	state := RedisInstanceModel{
		Name:            types.StringValue("old-name"),
		FlavorID:        types.StringValue("cache.small"),
		PersistenceMode: types.StringValue("rdb"),
		EvictionPolicy:  types.StringValue("noeviction"),
	}

	req := plan.toUpdateRequest(&state)
	if req.Name == nil || *req.Name != "new-name" {
		t.Error("expected name update to new-name")
	}
	if req.FlavorID == nil || *req.FlavorID != "cache.large" {
		t.Error("expected flavorId update to cache.large")
	}
	if req.PersistenceMode == nil || *req.PersistenceMode != "aof" {
		t.Error("expected persistenceMode update to aof")
	}
	if req.EvictionPolicy == nil || *req.EvictionPolicy != "allkeys-lru" {
		t.Error("expected evictionPolicy update to allkeys-lru")
	}
}

func TestRedisInstanceModelToUpdateRequestNoChanges(t *testing.T) {
	same := RedisInstanceModel{
		Name:            types.StringValue("same"),
		FlavorID:        types.StringValue("cache.small"),
		PersistenceMode: types.StringValue("rdb"),
		EvictionPolicy:  types.StringValue("noeviction"),
	}

	req := same.toUpdateRequest(&same)
	if req.Name != nil || req.FlavorID != nil || req.PersistenceMode != nil || req.EvictionPolicy != nil {
		t.Error("expected no changes in update request")
	}
}

func TestRedisInstanceModelFromAPI(t *testing.T) {
	ctx := context.Background()
	diags := diag.Diagnostics{}

	api := &apiRedisInstance{
		ID:              "redis-123",
		Name:            "my-redis",
		EngineVersion:   "7.2",
		FlavorID:        "cache.small",
		StorageGB:       25,
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

	var model RedisInstanceModel
	model.fromAPI(ctx, api, &diags)
	if diags.HasError() {
		t.Fatalf("unexpected diagnostics: %v", diags.Errors())
	}

	if model.ID.ValueString() != "redis-123" {
		t.Errorf("expected ID redis-123, got %s", model.ID.ValueString())
	}
	if model.Version.ValueString() != "7.2" {
		t.Errorf("expected version 7.2, got %s", model.Version.ValueString())
	}
	if model.StorageGB.ValueInt64() != 25 {
		t.Errorf("expected storage_gb 25, got %d", model.StorageGB.ValueInt64())
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
	if model.PersistenceMode.ValueString() != "rdb" {
		t.Errorf("expected persistence_mode rdb, got %s", model.PersistenceMode.ValueString())
	}
	if model.EvictionPolicy.ValueString() != "noeviction" {
		t.Errorf("expected eviction_policy noeviction, got %s", model.EvictionPolicy.ValueString())
	}
}

func TestRedisInstanceModelFromAPINulls(t *testing.T) {
	ctx := context.Background()
	diags := diag.Diagnostics{}

	api := &apiRedisInstance{
		ID:              "redis-123",
		Name:            "my-redis",
		EngineVersion:   "7.2",
		FlavorID:        "cache.small",
		VPCID:           "vpc-123",
		SubnetID:        "subnet-456",
		PersistenceMode: "rdb",
		EvictionPolicy:  "noeviction",
		Status:          "provisioning",
		CreatedAt:       "2025-01-01T00:00:00Z",
	}

	var model RedisInstanceModel
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
	if resp.TypeName != "frostmoln_redis_instance" {
		t.Errorf("expected type name frostmoln_redis_instance, got %s", resp.TypeName)
	}
}

func TestSchema(t *testing.T) {
	r := NewResource()
	req := resource.SchemaRequest{}
	var resp resource.SchemaResponse
	r.Schema(context.Background(), req, &resp)

	requiredAttrs := []string{"name", "version", "flavor_id", "vpc_id", "subnet_id"}
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

	// Verify defaults
	optionalAttrs := []string{"storage_gb", "persistence_mode", "eviction_policy"}
	for _, attr := range optionalAttrs {
		if _, ok := resp.Schema.Attributes[attr]; !ok {
			t.Errorf("expected optional attribute %s in schema", attr)
		}
	}
}

func TestConfigureNilProviderData(t *testing.T) {
	r := &redisInstanceResource{}
	req := resource.ConfigureRequest{}
	var resp resource.ConfigureResponse
	r.Configure(context.Background(), req, &resp)
	if resp.Diagnostics.HasError() {
		t.Errorf("expected no errors for nil provider data, got %v", resp.Diagnostics.Errors())
	}
}

func TestConfigureWrongType(t *testing.T) {
	r := &redisInstanceResource{}
	req := resource.ConfigureRequest{
		ProviderData: "not-a-client",
	}
	var resp resource.ConfigureResponse
	r.Configure(context.Background(), req, &resp)
	if !resp.Diagnostics.HasError() {
		t.Error("expected error for wrong provider data type")
	}
}

// buildRedisInstanceState creates a tfsdk.State pre-populated with a redis instance.
func buildRedisInstanceState(t *testing.T, model RedisInstanceModel) tfsdk.State {
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

// buildRedisInstancePlan creates a tfsdk.Plan pre-populated with a redis instance.
func buildRedisInstancePlan(t *testing.T, model RedisInstanceModel) tfsdk.Plan {
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

func emptyRedisInstanceState(t *testing.T) tfsdk.State {
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
		case r.Method == http.MethodPost && r.URL.Path == "/v1/tenants/t-1/caches":
			var body apiCreateRedisInstanceRequest
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("failed to decode request: %v", err)
			}
			if body.Engine != "redis" {
				t.Errorf("expected engine redis, got %s", body.Engine)
			}
			if body.EngineVersion != "7.2" {
				t.Errorf("expected engineVersion 7.2, got %s", body.EngineVersion)
			}
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(apiRedisInstance{
				ID:              "redis-new",
				Name:            body.Name,
				EngineVersion:   body.EngineVersion,
				FlavorID:        body.FlavorID,
				VPCID:           body.VPCID,
				SubnetID:        body.SubnetID,
				PersistenceMode: "rdb",
				EvictionPolicy:  "noeviction",
				Status:          "provisioning",
				CreatedAt:       "2025-01-01T00:00:00Z",
			})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-1/caches/redis-new":
			count := callCount.Add(1)
			status := "provisioning"
			if count >= 2 {
				status = "running"
			}
			_ = json.NewEncoder(w).Encode(apiRedisInstance{
				ID:              "redis-new",
				Name:            "test-redis",
				EngineVersion:   "7.2",
				FlavorID:        "cache.small",
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

	c := client.NewClient(server.URL, "test-key", client.WithHTTPClient(server.Client()))
	c.SetTenantIDForTest("t-1")

	r := &redisInstanceResource{
		client:       c,
		pollInterval: 10 * time.Millisecond,
		pollTimeout:  5 * time.Second,
	}

	plan := buildRedisInstancePlan(t, RedisInstanceModel{
		Name:            types.StringValue("test-redis"),
		Version:         types.StringValue("7.2"),
		FlavorID:        types.StringValue("cache.small"),
		VPCID:           types.StringValue("vpc-1"),
		SubnetID:        types.StringValue("sn-1"),
		PersistenceMode: types.StringValue("rdb"),
		EvictionPolicy:  types.StringValue("noeviction"),
	})

	createResp := resource.CreateResponse{State: emptyRedisInstanceState(t)}
	r.Create(context.Background(), resource.CreateRequest{Plan: plan}, &createResp)

	if createResp.Diagnostics.HasError() {
		t.Fatalf("create failed: %v", createResp.Diagnostics.Errors())
	}

	var result RedisInstanceModel
	createResp.State.Get(context.Background(), &result)
	if result.ID.ValueString() != "redis-new" {
		t.Errorf("expected ID redis-new, got %s", result.ID.ValueString())
	}
	if result.Status.ValueString() != "running" {
		t.Errorf("expected status running, got %s", result.Status.ValueString())
	}
	if result.Port.ValueInt64() != 6379 {
		t.Errorf("expected port 6379, got %d", result.Port.ValueInt64())
	}
}

func TestRead(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-1/caches/redis-123" {
			_ = json.NewEncoder(w).Encode(apiRedisInstance{
				ID:              "redis-123",
				Name:            "my-redis",
				EngineVersion:   "7.2",
				FlavorID:        "cache.small",
				StorageGB:       25,
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

	c := client.NewClient(server.URL, "test-key", client.WithHTTPClient(server.Client()))
	c.SetTenantIDForTest("t-1")

	r := &redisInstanceResource{client: c}

	state := buildRedisInstanceState(t, RedisInstanceModel{
		ID:              types.StringValue("redis-123"),
		Name:            types.StringValue("my-redis"),
		Version:         types.StringValue("7.2"),
		FlavorID:        types.StringValue("cache.small"),
		StorageGB:       types.Int64Value(25),
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

	var result RedisInstanceModel
	readResp.State.Get(context.Background(), &result)
	if result.Status.ValueString() != "running" {
		t.Errorf("expected status running, got %s", result.Status.ValueString())
	}
	if result.StorageGB.ValueInt64() != 25 {
		t.Errorf("expected storage_gb 25, got %d", result.StorageGB.ValueInt64())
	}
}

func TestReadNotFound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"code": "NOT_FOUND", "message": "not found",
		})
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key", client.WithHTTPClient(server.Client()))
	c.SetTenantIDForTest("t-1")

	r := &redisInstanceResource{client: c}

	state := buildRedisInstanceState(t, RedisInstanceModel{
		ID:              types.StringValue("redis-gone"),
		Name:            types.StringValue("gone"),
		Version:         types.StringValue("7.2"),
		FlavorID:        types.StringValue("cache.small"),
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

	// State should be removed (empty raw value).
	var result RedisInstanceModel
	diags := readResp.State.Get(context.Background(), &result)
	if !diags.HasError() {
		if result.ID.ValueString() != "" {
			t.Error("expected state to be removed after 404")
		}
	}
}

func TestDelete(t *testing.T) {
	deleted := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodDelete && r.URL.Path == "/v1/tenants/t-1/caches/redis-123":
			deleted = true
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-1/caches/redis-123":
			if deleted {
				w.WriteHeader(http.StatusNotFound)
				_ = json.NewEncoder(w).Encode(map[string]interface{}{
					"code": "NOT_FOUND", "message": "not found",
				})
			} else {
				_ = json.NewEncoder(w).Encode(apiRedisInstance{
					ID: "redis-123", Status: "deleting",
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

	r := &redisInstanceResource{
		client:       c,
		pollInterval: 10 * time.Millisecond,
		pollTimeout:  5 * time.Second,
	}

	state := buildRedisInstanceState(t, RedisInstanceModel{
		ID:              types.StringValue("redis-123"),
		Name:            types.StringValue("my-redis"),
		Version:         types.StringValue("7.2"),
		FlavorID:        types.StringValue("cache.small"),
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
}

func TestUpdate(t *testing.T) {
	var updatedBody apiUpdateRedisInstanceRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPut && r.URL.Path == "/v1/tenants/t-1/caches/redis-123":
			_ = json.NewDecoder(r.Body).Decode(&updatedBody)
			w.WriteHeader(http.StatusOK)
			_, _ = fmt.Fprint(w, `{}`)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-1/caches/redis-123":
			_ = json.NewEncoder(w).Encode(apiRedisInstance{
				ID:              "redis-123",
				Name:            "updated-redis",
				EngineVersion:   "7.2",
				FlavorID:        "cache.large",
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

	c := client.NewClient(server.URL, "test-key", client.WithHTTPClient(server.Client()))
	c.SetTenantIDForTest("t-1")

	r := &redisInstanceResource{
		client:       c,
		pollInterval: 10 * time.Millisecond,
		pollTimeout:  5 * time.Second,
	}

	state := buildRedisInstanceState(t, RedisInstanceModel{
		ID:              types.StringValue("redis-123"),
		Name:            types.StringValue("old-redis"),
		Version:         types.StringValue("7.2"),
		FlavorID:        types.StringValue("cache.small"),
		VPCID:           types.StringValue("vpc-1"),
		SubnetID:        types.StringValue("sn-1"),
		PersistenceMode: types.StringValue("rdb"),
		EvictionPolicy:  types.StringValue("noeviction"),
		Status:          types.StringValue("running"),
		CreatedAt:       types.StringValue("2025-01-01T00:00:00Z"),
	})

	plan := buildRedisInstancePlan(t, RedisInstanceModel{
		ID:              types.StringValue("redis-123"),
		Name:            types.StringValue("updated-redis"),
		Version:         types.StringValue("7.2"),
		FlavorID:        types.StringValue("cache.large"),
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

	if updatedBody.Name == nil || *updatedBody.Name != "updated-redis" {
		t.Error("expected name in update request")
	}
	if updatedBody.FlavorID == nil || *updatedBody.FlavorID != "cache.large" {
		t.Error("expected flavorId in update request")
	}
	if updatedBody.PersistenceMode == nil || *updatedBody.PersistenceMode != "aof" {
		t.Error("expected persistenceMode in update request")
	}
	if updatedBody.EvictionPolicy == nil || *updatedBody.EvictionPolicy != "allkeys-lru" {
		t.Error("expected evictionPolicy in update request")
	}
}
