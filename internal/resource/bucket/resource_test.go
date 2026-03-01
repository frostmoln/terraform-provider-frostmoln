package bucket

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/types"

	"git.nl.cloud/NordicLight/terraform-provider-frostmoln/internal/client"
)

func TestBucketModelToCreateRequest(t *testing.T) {
	ctx := context.Background()
	tags, _ := types.MapValueFrom(ctx, types.StringType, map[string]string{"env": "prod"})

	model := BucketModel{
		Name:         types.StringValue("my-bucket"),
		Region:       types.StringValue("eu-north-1"),
		StorageClass: types.StringValue("standard"),
		Versioning:   types.StringValue("enabled"),
		Tags:         tags,
	}

	req, diags := model.toCreateRequest(ctx)
	if diags.HasError() {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}

	if req.Name != "my-bucket" {
		t.Errorf("expected name my-bucket, got %s", req.Name)
	}
	if req.Region != "eu-north-1" {
		t.Errorf("expected region eu-north-1, got %s", req.Region)
	}
	if req.StorageClass != "standard" {
		t.Errorf("expected storage_class standard, got %s", req.StorageClass)
	}
	if req.Versioning != "enabled" {
		t.Errorf("expected versioning enabled, got %s", req.Versioning)
	}
	if req.Tags["env"] != "prod" {
		t.Errorf("expected tag env=prod, got %v", req.Tags)
	}
}

func TestBucketModelToCreateRequestOptionalFieldsNull(t *testing.T) {
	ctx := context.Background()

	model := BucketModel{
		Name:         types.StringValue("minimal-bucket"),
		Region:       types.StringNull(),
		StorageClass: types.StringNull(),
		Versioning:   types.StringNull(),
		Tags:         types.MapNull(types.StringType),
	}

	req, diags := model.toCreateRequest(ctx)
	if diags.HasError() {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}

	if req.Name != "minimal-bucket" {
		t.Errorf("expected name minimal-bucket, got %s", req.Name)
	}
	if req.Region != "" {
		t.Errorf("expected empty region, got %s", req.Region)
	}
	if req.StorageClass != "" {
		t.Errorf("expected empty storage class, got %s", req.StorageClass)
	}
	if req.Tags != nil {
		t.Errorf("expected nil tags, got %v", req.Tags)
	}
}

func TestBucketModelToUpdateRequest(t *testing.T) {
	ctx := context.Background()
	tags, _ := types.MapValueFrom(ctx, types.StringType, map[string]string{"team": "ops"})

	model := BucketModel{
		Versioning: types.StringValue("suspended"),
		Tags:       tags,
	}

	req, diags := model.toUpdateRequest(ctx)
	if diags.HasError() {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}

	if req.Versioning == nil || *req.Versioning != "suspended" {
		t.Errorf("expected versioning suspended, got %v", req.Versioning)
	}
	if req.Tags["team"] != "ops" {
		t.Errorf("expected tag team=ops, got %v", req.Tags)
	}
}

func TestBucketModelFromAPI(t *testing.T) {
	ctx := context.Background()
	b := &apiBucket{
		Name:         "test-bucket",
		Region:       "eu-north-1",
		StorageClass: "standard",
		Versioning:   "enabled",
		ObjectCount:  42,
		SizeBytes:    1024000,
		Endpoint:     "https://s3.eu-north-1.nordiclight.cloud",
		AccessKey:    "AKIAEXAMPLE",
		Tags:         map[string]string{"env": "staging"},
		CreatedAt:    "2025-03-01T10:00:00Z",
	}

	var model BucketModel
	diags := model.fromAPI(ctx, b)
	if diags != nil && diags.HasError() {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}

	if model.Name.ValueString() != "test-bucket" {
		t.Errorf("expected name test-bucket, got %s", model.Name.ValueString())
	}
	if model.Region.ValueString() != "eu-north-1" {
		t.Errorf("expected region eu-north-1, got %s", model.Region.ValueString())
	}
	if model.StorageClass.ValueString() != "standard" {
		t.Errorf("expected storage class standard, got %s", model.StorageClass.ValueString())
	}
	if model.Versioning.ValueString() != "enabled" {
		t.Errorf("expected versioning enabled, got %s", model.Versioning.ValueString())
	}
	if model.ObjectCount.ValueInt64() != 42 {
		t.Errorf("expected object_count 42, got %d", model.ObjectCount.ValueInt64())
	}
	if model.SizeBytes.ValueInt64() != 1024000 {
		t.Errorf("expected size_bytes 1024000, got %d", model.SizeBytes.ValueInt64())
	}
	if model.Endpoint.ValueString() != "https://s3.eu-north-1.nordiclight.cloud" {
		t.Errorf("expected endpoint, got %s", model.Endpoint.ValueString())
	}
	if model.AccessKey.ValueString() != "AKIAEXAMPLE" {
		t.Errorf("expected access key AKIAEXAMPLE, got %s", model.AccessKey.ValueString())
	}
	if model.CreatedAt.ValueString() != "2025-03-01T10:00:00Z" {
		t.Errorf("expected created_at, got %s", model.CreatedAt.ValueString())
	}
}

func TestBucketModelFromAPINoAccessKey(t *testing.T) {
	ctx := context.Background()
	b := &apiBucket{
		Name:         "test-bucket",
		Region:       "eu-north-1",
		StorageClass: "standard",
		Versioning:   "enabled",
		CreatedAt:    "2025-03-01T10:00:00Z",
	}

	model := BucketModel{
		AccessKey: types.StringValue("preserved-key"),
	}
	diags := model.fromAPI(ctx, b)
	if diags != nil && diags.HasError() {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}

	// When AccessKey is empty in the API response, fromAPI should NOT overwrite it.
	// The resource.go Read method handles preserving the state value.
	// fromAPI itself does not touch AccessKey when it's empty.
	if model.AccessKey.ValueString() != "preserved-key" {
		t.Errorf("expected access key to be preserved, got %s", model.AccessKey.ValueString())
	}
}

func TestBucketCreate(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/me":
			json.NewEncoder(w).Encode(map[string]string{
				"id":       "user-123",
				"tenantId": "tenant-456",
			})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/tenants/tenant-456/buckets":
			var req apiCreateBucketRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatalf("failed to decode request: %v", err)
			}
			if req.Name != "test-bucket" {
				t.Errorf("expected name test-bucket, got %s", req.Name)
			}
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(apiBucket{
				Name:         req.Name,
				Region:       "eu-north-1",
				StorageClass: "standard",
				Versioning:   "enabled",
				Endpoint:     "https://s3.eu-north-1.nordiclight.cloud",
				AccessKey:    "AKIAEXAMPLE",
				CreatedAt:    "2025-03-01T10:00:00Z",
			})
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key")
	if err := c.Configure(context.Background()); err != nil {
		t.Fatalf("configure failed: %v", err)
	}

	apiReq := apiCreateBucketRequest{Name: "test-bucket", Region: "eu-north-1"}
	resp, err := c.Post(context.Background(), c.TenantPath("/buckets"), apiReq)
	if err != nil {
		t.Fatalf("post failed: %v", err)
	}

	b, err := client.ParseResponse[apiBucket](resp)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}

	if b.Name != "test-bucket" {
		t.Errorf("expected name test-bucket, got %s", b.Name)
	}
	if b.AccessKey != "AKIAEXAMPLE" {
		t.Errorf("expected access key, got %s", b.AccessKey)
	}
}

func TestBucketRead(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/me":
			json.NewEncoder(w).Encode(map[string]string{
				"id":       "user-123",
				"tenantId": "tenant-456",
			})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/tenant-456/buckets/test-bucket":
			json.NewEncoder(w).Encode(apiBucket{
				Name:         "test-bucket",
				Region:       "eu-north-1",
				StorageClass: "standard",
				Versioning:   "enabled",
				ObjectCount:  10,
				SizeBytes:    5000,
				Endpoint:     "https://s3.eu-north-1.nordiclight.cloud",
				CreatedAt:    "2025-03-01T10:00:00Z",
			})
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key")
	if err := c.Configure(context.Background()); err != nil {
		t.Fatalf("configure failed: %v", err)
	}

	resp, err := c.Get(context.Background(), c.TenantPath("/buckets/test-bucket"), nil)
	if err != nil {
		t.Fatalf("get failed: %v", err)
	}

	b, err := client.ParseResponse[apiBucket](resp)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}

	if b.Name != "test-bucket" {
		t.Errorf("expected name test-bucket, got %s", b.Name)
	}
	if b.ObjectCount != 10 {
		t.Errorf("expected object count 10, got %d", b.ObjectCount)
	}
}

func TestBucketUpdate(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/me":
			json.NewEncoder(w).Encode(map[string]string{
				"id":       "user-123",
				"tenantId": "tenant-456",
			})
		case r.Method == http.MethodPatch && r.URL.Path == "/v1/tenants/tenant-456/buckets/test-bucket":
			var req apiUpdateBucketRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatalf("failed to decode request: %v", err)
			}
			if req.Versioning == nil || *req.Versioning != "suspended" {
				t.Errorf("expected versioning suspended, got %v", req.Versioning)
			}
			json.NewEncoder(w).Encode(apiBucket{
				Name:         "test-bucket",
				Region:       "eu-north-1",
				StorageClass: "standard",
				Versioning:   "suspended",
				Endpoint:     "https://s3.eu-north-1.nordiclight.cloud",
				CreatedAt:    "2025-03-01T10:00:00Z",
			})
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key")
	if err := c.Configure(context.Background()); err != nil {
		t.Fatalf("configure failed: %v", err)
	}

	v := "suspended"
	apiReq := apiUpdateBucketRequest{Versioning: &v}
	resp, err := c.Patch(context.Background(), c.TenantPath("/buckets/test-bucket"), apiReq)
	if err != nil {
		t.Fatalf("patch failed: %v", err)
	}

	b, err := client.ParseResponse[apiBucket](resp)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}

	if b.Versioning != "suspended" {
		t.Errorf("expected versioning suspended, got %s", b.Versioning)
	}
}

func TestBucketDelete(t *testing.T) {
	deleted := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/me":
			json.NewEncoder(w).Encode(map[string]string{
				"id":       "user-123",
				"tenantId": "tenant-456",
			})
		case r.Method == http.MethodDelete && r.URL.Path == "/v1/tenants/tenant-456/buckets/test-bucket":
			deleted = true
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key")
	if err := c.Configure(context.Background()); err != nil {
		t.Fatalf("configure failed: %v", err)
	}

	_, err := c.Delete(context.Background(), c.TenantPath("/buckets/test-bucket"))
	if err != nil {
		t.Fatalf("delete failed: %v", err)
	}

	if !deleted {
		t.Error("expected delete to be called")
	}
}

func TestBucketReadNotFound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/me":
			json.NewEncoder(w).Encode(map[string]string{
				"id":       "user-123",
				"tenantId": "tenant-456",
			})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/tenant-456/buckets/nonexistent":
			w.WriteHeader(http.StatusNotFound)
			json.NewEncoder(w).Encode(map[string]interface{}{
				"error": map[string]string{
					"code":    "NOT_FOUND",
					"message": "Bucket not found",
				},
			})
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key")
	if err := c.Configure(context.Background()); err != nil {
		t.Fatalf("configure failed: %v", err)
	}

	_, err := c.Get(context.Background(), c.TenantPath("/buckets/nonexistent"), nil)
	if err == nil {
		t.Fatal("expected error for not found")
	}
	if !client.IsNotFound(err) {
		t.Errorf("expected not found error, got %v", err)
	}
}
