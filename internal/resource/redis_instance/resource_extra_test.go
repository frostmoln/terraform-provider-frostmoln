package redis_instance

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

func TestGetPollDefaults(t *testing.T) {
	r := &redisInstanceResource{}
	if r.getPollInterval() != 5*time.Second {
		t.Errorf("expected default poll interval 5s, got %v", r.getPollInterval())
	}
	if r.getPollTimeout() != 15*time.Minute {
		t.Errorf("expected default poll timeout 15m, got %v", r.getPollTimeout())
	}
}

func TestImportState(t *testing.T) {
	r := NewResource().(resource.ResourceWithImportState)
	resp := resource.ImportStateResponse{State: emptyRedisInstanceState(t)}
	r.ImportState(context.Background(), resource.ImportStateRequest{ID: "redis-import-1"}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("import failed: %v", resp.Diagnostics.Errors())
	}
	var id types.String
	resp.State.GetAttribute(context.Background(), path.Root("id"), &id)
	if id.ValueString() != "redis-import-1" {
		t.Errorf("expected imported id redis-import-1, got %s", id.ValueString())
	}
}

func redisPlanModel() RedisInstanceModel {
	return RedisInstanceModel{
		Name:            types.StringValue("redis-1"),
		Version:         types.StringValue("7.2"),
		FlavorID:        types.StringValue("cache.small"),
		VPCID:           types.StringValue("vpc-1"),
		SubnetID:        types.StringValue("sn-1"),
		PersistenceMode: types.StringValue("rdb"),
		EvictionPolicy:  types.StringValue("noeviction"),
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

	r := &redisInstanceResource{client: c, pollInterval: 10 * time.Millisecond, pollTimeout: time.Second}
	createResp := resource.CreateResponse{State: emptyRedisInstanceState(t)}
	r.Create(context.Background(), resource.CreateRequest{Plan: buildRedisInstancePlan(t, redisPlanModel())}, &createResp)
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

	r := &redisInstanceResource{client: c, pollInterval: 10 * time.Millisecond, pollTimeout: time.Second}
	createResp := resource.CreateResponse{State: emptyRedisInstanceState(t)}
	r.Create(context.Background(), resource.CreateRequest{Plan: buildRedisInstancePlan(t, redisPlanModel())}, &createResp)
	if !createResp.Diagnostics.HasError() {
		t.Error("expected error when create response body is malformed")
	}
}

func TestCreatePollErrorState(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost:
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id":"redis-err","name":"redis-1","engineVersion":"7.2","flavorId":"cache.small","vpcId":"vpc-1","subnetId":"sn-1","persistenceMode":"rdb","evictionPolicy":"noeviction","status":"provisioning","createdAt":"2025-01-01T00:00:00Z"}`))
		case http.MethodGet:
			_, _ = w.Write([]byte(`{"id":"redis-err","name":"redis-1","engineVersion":"7.2","flavorId":"cache.small","vpcId":"vpc-1","subnetId":"sn-1","persistenceMode":"rdb","evictionPolicy":"noeviction","status":"error","createdAt":"2025-01-01T00:00:00Z"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key", client.WithHTTPClient(server.Client())) // pragma: allowlist secret
	c.SetTenantIDForTest("t-1")

	r := &redisInstanceResource{client: c, pollInterval: 10 * time.Millisecond, pollTimeout: time.Second}
	createResp := resource.CreateResponse{State: emptyRedisInstanceState(t)}
	r.Create(context.Background(), resource.CreateRequest{Plan: buildRedisInstancePlan(t, redisPlanModel())}, &createResp)
	if !createResp.Diagnostics.HasError() {
		t.Error("expected error when instance enters error state during polling")
	}
}

func TestCreateRefreshError(t *testing.T) {
	var getCount int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost:
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id":"redis-ref","name":"redis-1","engineVersion":"7.2","flavorId":"cache.small","vpcId":"vpc-1","subnetId":"sn-1","persistenceMode":"rdb","evictionPolicy":"noeviction","status":"provisioning","createdAt":"2025-01-01T00:00:00Z"}`))
		case http.MethodGet:
			getCount++
			if getCount == 1 {
				_, _ = w.Write([]byte(`{"id":"redis-ref","name":"redis-1","engineVersion":"7.2","flavorId":"cache.small","vpcId":"vpc-1","subnetId":"sn-1","persistenceMode":"rdb","evictionPolicy":"noeviction","status":"running","createdAt":"2025-01-01T00:00:00Z"}`))
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

	r := &redisInstanceResource{client: c, pollInterval: 10 * time.Millisecond, pollTimeout: time.Second}
	createResp := resource.CreateResponse{State: emptyRedisInstanceState(t)}
	r.Create(context.Background(), resource.CreateRequest{Plan: buildRedisInstancePlan(t, redisPlanModel())}, &createResp)
	if !createResp.Diagnostics.HasError() {
		t.Error("expected error when post-poll refresh read fails")
	}
}

func TestUpdatePollErrorState(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPut:
			_, _ = w.Write([]byte(`{}`))
		case http.MethodGet:
			_, _ = w.Write([]byte(`{"id":"redis-1","name":"redis-1","engineVersion":"7.2","flavorId":"cache.small","vpcId":"vpc-1","subnetId":"sn-1","persistenceMode":"rdb","evictionPolicy":"noeviction","status":"error","createdAt":"2025-01-01T00:00:00Z"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key", client.WithHTTPClient(server.Client())) // pragma: allowlist secret
	c.SetTenantIDForTest("t-1")

	r := &redisInstanceResource{client: c, pollInterval: 10 * time.Millisecond, pollTimeout: time.Second}
	base := RedisInstanceModel{
		ID:              types.StringValue("redis-1"),
		Name:            types.StringValue("old"),
		Version:         types.StringValue("7.2"),
		FlavorID:        types.StringValue("cache.small"),
		VPCID:           types.StringValue("vpc-1"),
		SubnetID:        types.StringValue("sn-1"),
		PersistenceMode: types.StringValue("rdb"),
		EvictionPolicy:  types.StringValue("noeviction"),
		Status:          types.StringValue("running"),
		CreatedAt:       types.StringValue("2025-01-01T00:00:00Z"),
	}
	state := buildRedisInstanceState(t, base)
	planM := base
	planM.Name = types.StringValue("new")
	plan := buildRedisInstancePlan(t, planM)
	updateResp := resource.UpdateResponse{State: state}
	r.Update(context.Background(), resource.UpdateRequest{Plan: plan, State: state}, &updateResp)
	if !updateResp.Diagnostics.HasError() {
		t.Error("expected error when instance enters error state during update polling")
	}
}

func TestDeletePollError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodDelete:
			w.WriteHeader(http.StatusNoContent)
		case http.MethodGet:
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"code":"INTERNAL","message":"boom"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key", client.WithHTTPClient(server.Client())) // pragma: allowlist secret
	c.SetTenantIDForTest("t-1")

	r := &redisInstanceResource{client: c, pollInterval: 10 * time.Millisecond, pollTimeout: 200 * time.Millisecond}
	state := buildRedisInstanceState(t, RedisInstanceModel{
		ID:              types.StringValue("redis-1"),
		Name:            types.StringValue("redis-1"),
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
	if !deleteResp.Diagnostics.HasError() {
		t.Error("expected error when delete poll keeps returning 500")
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

	r := &redisInstanceResource{client: c}
	state := buildRedisInstanceState(t, RedisInstanceModel{
		ID:              types.StringValue("redis-1"),
		Name:            types.StringValue("redis-1"),
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

	r := &redisInstanceResource{client: c, pollInterval: 10 * time.Millisecond, pollTimeout: time.Second}
	state := buildRedisInstanceState(t, RedisInstanceModel{
		ID:              types.StringValue("redis-1"),
		Name:            types.StringValue("old"),
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
		ID:              types.StringValue("redis-1"),
		Name:            types.StringValue("new"),
		Version:         types.StringValue("7.2"),
		FlavorID:        types.StringValue("cache.large"),
		VPCID:           types.StringValue("vpc-1"),
		SubnetID:        types.StringValue("sn-1"),
		PersistenceMode: types.StringValue("rdb"),
		EvictionPolicy:  types.StringValue("noeviction"),
		Status:          types.StringValue("running"),
		CreatedAt:       types.StringValue("2025-01-01T00:00:00Z"),
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

	r := &redisInstanceResource{client: c, pollInterval: 10 * time.Millisecond, pollTimeout: time.Second}
	state := buildRedisInstanceState(t, RedisInstanceModel{
		ID:              types.StringValue("redis-gone"),
		Name:            types.StringValue("redis-1"),
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

	r := &redisInstanceResource{client: c, pollInterval: 10 * time.Millisecond, pollTimeout: time.Second}
	state := buildRedisInstanceState(t, RedisInstanceModel{
		ID:              types.StringValue("redis-1"),
		Name:            types.StringValue("redis-1"),
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
	if !deleteResp.Diagnostics.HasError() {
		t.Error("expected error when delete returns 500")
	}
}
