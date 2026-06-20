package bucket

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
)

func TestBucketModelToCreateRequest(t *testing.T) {
	ctx := context.Background()
	tags, _ := types.MapValueFrom(ctx, types.StringType, map[string]string{"env": "prod"})

	model := BucketModel{
		Name:         types.StringValue("my-bucket"),
		Region:       types.StringValue("sweden"),
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
	if req.Region != "sweden" {
		t.Errorf("expected region sweden, got %s", req.Region)
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
		Region:       "sweden",
		StorageClass: "standard",
		Versioning:   "enabled",
		ObjectCount:  42,
		SizeBytes:    1024000,
		Endpoint:     "https://s3.sweden.frostmoln.cloud",
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
	if model.Region.ValueString() != "sweden" {
		t.Errorf("expected region sweden, got %s", model.Region.ValueString())
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
	if model.Endpoint.ValueString() != "https://s3.sweden.frostmoln.cloud" {
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
		Region:       "sweden",
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
			_ = json.NewEncoder(w).Encode(map[string]string{
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
			_ = json.NewEncoder(w).Encode(apiBucket{
				Name:         req.Name,
				Region:       "sweden",
				StorageClass: "standard",
				Versioning:   "enabled",
				Endpoint:     "https://s3.sweden.frostmoln.cloud",
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

	apiReq := apiCreateBucketRequest{Name: "test-bucket", Region: "sweden"}
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
			_ = json.NewEncoder(w).Encode(map[string]string{
				"id":       "user-123",
				"tenantId": "tenant-456",
			})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/tenant-456/buckets/test-bucket":
			_ = json.NewEncoder(w).Encode(apiBucket{
				Name:         "test-bucket",
				Region:       "sweden",
				StorageClass: "standard",
				Versioning:   "enabled",
				ObjectCount:  10,
				SizeBytes:    5000,
				Endpoint:     "https://s3.sweden.frostmoln.cloud",
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
			_ = json.NewEncoder(w).Encode(map[string]string{
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
			_ = json.NewEncoder(w).Encode(apiBucket{
				Name:         "test-bucket",
				Region:       "sweden",
				StorageClass: "standard",
				Versioning:   "suspended",
				Endpoint:     "https://s3.sweden.frostmoln.cloud",
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
			_ = json.NewEncoder(w).Encode(map[string]string{
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
			_ = json.NewEncoder(w).Encode(map[string]string{
				"id":       "user-123",
				"tenantId": "tenant-456",
			})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/tenant-456/buckets/nonexistent":
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
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

// --- tfsdk-level resource method tests ---

func bucketSchema(t *testing.T) schema.Schema {
	t.Helper()
	r := NewResource()
	resp := &resource.SchemaResponse{}
	r.Schema(context.Background(), resource.SchemaRequest{}, resp)
	return resp.Schema
}

func bucketObjectType() tftypes.Object {
	return tftypes.Object{
		AttributeTypes: map[string]tftypes.Type{
			"name":          tftypes.String,
			"region":        tftypes.String,
			"storage_class": tftypes.String,
			"versioning":    tftypes.String,
			"tags":          tftypes.Map{ElementType: tftypes.String},
			"object_count":  tftypes.Number,
			"size_bytes":    tftypes.Number,
			"endpoint":      tftypes.String,
			"access_key":    tftypes.String,
			"created_at":    tftypes.String,
		},
	}
}

func TestBucketNewResource(t *testing.T) {
	r := NewResource()
	if r == nil {
		t.Fatal("expected non-nil resource")
	}
}

func TestBucketMetadata(t *testing.T) {
	r := NewResource()
	req := resource.MetadataRequest{ProviderTypeName: "frostmoln"}
	resp := &resource.MetadataResponse{}
	r.Metadata(context.Background(), req, resp)

	if resp.TypeName != "frostmoln_bucket" {
		t.Errorf("expected type name frostmoln_bucket, got %s", resp.TypeName)
	}
}

func TestBucketSchema(t *testing.T) {
	r := NewResource()
	resp := &resource.SchemaResponse{}
	r.Schema(context.Background(), resource.SchemaRequest{}, resp)

	for _, attr := range []string{"name", "region", "storage_class", "versioning", "tags", "object_count", "size_bytes", "endpoint", "access_key", "created_at"} {
		if _, ok := resp.Schema.Attributes[attr]; !ok {
			t.Errorf("expected attribute %s in schema", attr)
		}
	}
}

func TestBucketConfigureNilProviderData(t *testing.T) {
	r := NewResource()
	resp := &resource.ConfigureResponse{}
	r.(resource.ResourceWithConfigure).Configure(context.Background(), resource.ConfigureRequest{ProviderData: nil}, resp)
	if resp.Diagnostics.HasError() {
		t.Errorf("expected no errors, got %v", resp.Diagnostics)
	}
}

func TestBucketConfigureWrongType(t *testing.T) {
	r := NewResource()
	resp := &resource.ConfigureResponse{}
	r.(resource.ResourceWithConfigure).Configure(context.Background(), resource.ConfigureRequest{ProviderData: true}, resp)
	if !resp.Diagnostics.HasError() {
		t.Error("expected error for wrong type")
	}
}

func TestBucketConfigureValidClient(t *testing.T) {
	r := NewResource()
	c := client.NewClient("http://localhost", "test-key") // pragma: allowlist secret
	resp := &resource.ConfigureResponse{}
	r.(resource.ResourceWithConfigure).Configure(context.Background(), resource.ConfigureRequest{ProviderData: c}, resp)
	if resp.Diagnostics.HasError() {
		t.Errorf("expected no errors, got %v", resp.Diagnostics)
	}
}

func TestBucketResourceCreate(t *testing.T) {
	bucketResp := apiBucket{
		Name:         "my-bucket",
		Region:       "sweden",
		StorageClass: "standard",
		Versioning:   "enabled",
		ObjectCount:  0,
		SizeBytes:    0,
		Endpoint:     "https://s3.sweden.frostmoln.cloud",
		AccessKey:    "AKIAEXAMPLE",
		CreatedAt:    "2025-06-01T12:00:00Z",
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/v1/tenants/t-123/buckets" {
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(bucketResp)
			return
		}
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"error": map[string]string{"code": "NOT_FOUND", "message": "not found"},
		})
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key") // pragma: allowlist secret
	c.SetTenantIDForTest("t-123")

	r := NewResource()
	r.(resource.ResourceWithConfigure).Configure(context.Background(), resource.ConfigureRequest{ProviderData: c}, &resource.ConfigureResponse{})

	s := bucketSchema(t)
	planVal := tftypes.NewValue(bucketObjectType(), map[string]tftypes.Value{
		"name":          tftypes.NewValue(tftypes.String, "my-bucket"),
		"region":        tftypes.NewValue(tftypes.String, "sweden"),
		"storage_class": tftypes.NewValue(tftypes.String, "standard"),
		"versioning":    tftypes.NewValue(tftypes.String, "enabled"),
		"tags":          tftypes.NewValue(tftypes.Map{ElementType: tftypes.String}, nil),
		"object_count":  tftypes.NewValue(tftypes.Number, tftypes.UnknownValue),
		"size_bytes":    tftypes.NewValue(tftypes.Number, tftypes.UnknownValue),
		"endpoint":      tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"access_key":    tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"created_at":    tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
	})

	plan := tfsdk.Plan{Schema: s, Raw: planVal}
	resp := &resource.CreateResponse{State: tfsdk.State{Schema: s}}
	r.Create(context.Background(), resource.CreateRequest{Plan: plan}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected errors: %v", resp.Diagnostics)
	}

	var state BucketModel
	resp.State.Get(context.Background(), &state)

	if state.Name.ValueString() != "my-bucket" {
		t.Errorf("expected Name my-bucket, got %s", state.Name.ValueString())
	}
	if state.AccessKey.ValueString() != "AKIAEXAMPLE" {
		t.Errorf("expected AccessKey AKIAEXAMPLE, got %s", state.AccessKey.ValueString())
	}
	if state.Endpoint.ValueString() != "https://s3.sweden.frostmoln.cloud" {
		t.Errorf("expected Endpoint, got %s", state.Endpoint.ValueString())
	}
}

func TestBucketResourceRead(t *testing.T) {
	bucketResp := apiBucket{
		Name:         "read-bucket",
		Region:       "sweden",
		StorageClass: "standard",
		Versioning:   "enabled",
		ObjectCount:  42,
		SizeBytes:    1024000,
		Endpoint:     "https://s3.sweden.frostmoln.cloud",
		CreatedAt:    "2025-06-01T12:00:00Z",
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-123/buckets/read-bucket" {
			_ = json.NewEncoder(w).Encode(bucketResp)
			return
		}
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"error": map[string]string{"code": "NOT_FOUND", "message": "not found"},
		})
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key") // pragma: allowlist secret
	c.SetTenantIDForTest("t-123")

	r := NewResource()
	r.(resource.ResourceWithConfigure).Configure(context.Background(), resource.ConfigureRequest{ProviderData: c}, &resource.ConfigureResponse{})

	s := bucketSchema(t)
	stateVal := tftypes.NewValue(bucketObjectType(), map[string]tftypes.Value{
		"name":          tftypes.NewValue(tftypes.String, "read-bucket"),
		"region":        tftypes.NewValue(tftypes.String, "sweden"),
		"storage_class": tftypes.NewValue(tftypes.String, "standard"),
		"versioning":    tftypes.NewValue(tftypes.String, "enabled"),
		"tags":          tftypes.NewValue(tftypes.Map{ElementType: tftypes.String}, nil),
		"object_count":  tftypes.NewValue(tftypes.Number, 0),
		"size_bytes":    tftypes.NewValue(tftypes.Number, 0),
		"endpoint":      tftypes.NewValue(tftypes.String, "https://s3.sweden.frostmoln.cloud"),
		"access_key":    tftypes.NewValue(tftypes.String, "AKIAOLD"),
		"created_at":    tftypes.NewValue(tftypes.String, "2025-06-01T12:00:00Z"),
	})

	state := tfsdk.State{Schema: s, Raw: stateVal}
	resp := &resource.ReadResponse{State: state}
	r.Read(context.Background(), resource.ReadRequest{State: state}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected errors: %v", resp.Diagnostics)
	}

	var model BucketModel
	resp.State.Get(context.Background(), &model)
	if model.ObjectCount.ValueInt64() != 42 {
		t.Errorf("expected ObjectCount 42, got %d", model.ObjectCount.ValueInt64())
	}
	// AccessKey should be preserved from state when API returns empty
	if model.AccessKey.ValueString() != "AKIAOLD" {
		t.Errorf("expected AccessKey to be preserved as AKIAOLD, got %s", model.AccessKey.ValueString())
	}
}

func TestBucketResourceReadNotFoundRemovesState(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"error": map[string]string{"code": "NOT_FOUND", "message": "not found"},
		})
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key") // pragma: allowlist secret
	c.SetTenantIDForTest("t-123")

	r := NewResource()
	r.(resource.ResourceWithConfigure).Configure(context.Background(), resource.ConfigureRequest{ProviderData: c}, &resource.ConfigureResponse{})

	s := bucketSchema(t)
	stateVal := tftypes.NewValue(bucketObjectType(), map[string]tftypes.Value{
		"name":          tftypes.NewValue(tftypes.String, "gone-bucket"),
		"region":        tftypes.NewValue(tftypes.String, "sweden"),
		"storage_class": tftypes.NewValue(tftypes.String, "standard"),
		"versioning":    tftypes.NewValue(tftypes.String, "enabled"),
		"tags":          tftypes.NewValue(tftypes.Map{ElementType: tftypes.String}, nil),
		"object_count":  tftypes.NewValue(tftypes.Number, 0),
		"size_bytes":    tftypes.NewValue(tftypes.Number, 0),
		"endpoint":      tftypes.NewValue(tftypes.String, "https://s3.frostmoln.cloud"),
		"access_key":    tftypes.NewValue(tftypes.String, nil),
		"created_at":    tftypes.NewValue(tftypes.String, "2025-01-01T00:00:00Z"),
	})

	state := tfsdk.State{Schema: s, Raw: stateVal}
	resp := &resource.ReadResponse{State: state}
	r.Read(context.Background(), resource.ReadRequest{State: state}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected errors: %v", resp.Diagnostics)
	}

	if !resp.State.Raw.IsNull() {
		t.Error("expected state to be null after not found")
	}
}

func TestBucketResourceUpdate(t *testing.T) {
	bucketResp := apiBucket{
		Name:         "upd-bucket",
		Region:       "sweden",
		StorageClass: "standard",
		Versioning:   "suspended",
		ObjectCount:  10,
		SizeBytes:    5000,
		Endpoint:     "https://s3.sweden.frostmoln.cloud",
		CreatedAt:    "2025-06-01T12:00:00Z",
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPatch && r.URL.Path == "/v1/tenants/t-123/buckets/upd-bucket" {
			_ = json.NewEncoder(w).Encode(bucketResp)
			return
		}
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"error": map[string]string{"code": "NOT_FOUND", "message": "not found"},
		})
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key") // pragma: allowlist secret
	c.SetTenantIDForTest("t-123")

	r := NewResource()
	r.(resource.ResourceWithConfigure).Configure(context.Background(), resource.ConfigureRequest{ProviderData: c}, &resource.ConfigureResponse{})

	s := bucketSchema(t)

	stateVal := tftypes.NewValue(bucketObjectType(), map[string]tftypes.Value{
		"name":          tftypes.NewValue(tftypes.String, "upd-bucket"),
		"region":        tftypes.NewValue(tftypes.String, "sweden"),
		"storage_class": tftypes.NewValue(tftypes.String, "standard"),
		"versioning":    tftypes.NewValue(tftypes.String, "enabled"),
		"tags":          tftypes.NewValue(tftypes.Map{ElementType: tftypes.String}, nil),
		"object_count":  tftypes.NewValue(tftypes.Number, 10),
		"size_bytes":    tftypes.NewValue(tftypes.Number, 5000),
		"endpoint":      tftypes.NewValue(tftypes.String, "https://s3.sweden.frostmoln.cloud"),
		"access_key":    tftypes.NewValue(tftypes.String, "AKIAOLD"),
		"created_at":    tftypes.NewValue(tftypes.String, "2025-06-01T12:00:00Z"),
	})

	planVal := tftypes.NewValue(bucketObjectType(), map[string]tftypes.Value{
		"name":          tftypes.NewValue(tftypes.String, "upd-bucket"),
		"region":        tftypes.NewValue(tftypes.String, "sweden"),
		"storage_class": tftypes.NewValue(tftypes.String, "standard"),
		"versioning":    tftypes.NewValue(tftypes.String, "suspended"),
		"tags":          tftypes.NewValue(tftypes.Map{ElementType: tftypes.String}, nil),
		"object_count":  tftypes.NewValue(tftypes.Number, tftypes.UnknownValue),
		"size_bytes":    tftypes.NewValue(tftypes.Number, tftypes.UnknownValue),
		"endpoint":      tftypes.NewValue(tftypes.String, "https://s3.sweden.frostmoln.cloud"),
		"access_key":    tftypes.NewValue(tftypes.String, "AKIAOLD"),
		"created_at":    tftypes.NewValue(tftypes.String, "2025-06-01T12:00:00Z"),
	})

	state := tfsdk.State{Schema: s, Raw: stateVal}
	plan := tfsdk.Plan{Schema: s, Raw: planVal}
	resp := &resource.UpdateResponse{State: tfsdk.State{Schema: s}}
	r.Update(context.Background(), resource.UpdateRequest{Plan: plan, State: state}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected errors: %v", resp.Diagnostics)
	}

	var model BucketModel
	resp.State.Get(context.Background(), &model)
	if model.Versioning.ValueString() != "suspended" {
		t.Errorf("expected Versioning suspended, got %s", model.Versioning.ValueString())
	}
	// AccessKey should be preserved from state when API returns empty
	if model.AccessKey.ValueString() != "AKIAOLD" {
		t.Errorf("expected AccessKey AKIAOLD preserved, got %s", model.AccessKey.ValueString())
	}
}

func TestBucketResourceDelete(t *testing.T) {
	deleted := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete && r.URL.Path == "/v1/tenants/t-123/buckets/del-bucket" {
			deleted = true
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"error": map[string]string{"code": "NOT_FOUND", "message": "not found"},
		})
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key") // pragma: allowlist secret
	c.SetTenantIDForTest("t-123")

	r := NewResource()
	r.(resource.ResourceWithConfigure).Configure(context.Background(), resource.ConfigureRequest{ProviderData: c}, &resource.ConfigureResponse{})

	s := bucketSchema(t)
	stateVal := tftypes.NewValue(bucketObjectType(), map[string]tftypes.Value{
		"name":          tftypes.NewValue(tftypes.String, "del-bucket"),
		"region":        tftypes.NewValue(tftypes.String, "sweden"),
		"storage_class": tftypes.NewValue(tftypes.String, "standard"),
		"versioning":    tftypes.NewValue(tftypes.String, "enabled"),
		"tags":          tftypes.NewValue(tftypes.Map{ElementType: tftypes.String}, nil),
		"object_count":  tftypes.NewValue(tftypes.Number, 0),
		"size_bytes":    tftypes.NewValue(tftypes.Number, 0),
		"endpoint":      tftypes.NewValue(tftypes.String, "https://s3.frostmoln.cloud"),
		"access_key":    tftypes.NewValue(tftypes.String, nil),
		"created_at":    tftypes.NewValue(tftypes.String, "2025-01-01T00:00:00Z"),
	})

	state := tfsdk.State{Schema: s, Raw: stateVal}
	resp := &resource.DeleteResponse{}
	r.Delete(context.Background(), resource.DeleteRequest{State: state}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected errors: %v", resp.Diagnostics)
	}
	if !deleted {
		t.Error("expected delete to be called")
	}
}

func TestBucketResourceDeleteAlreadyGone(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"error": map[string]string{"code": "NOT_FOUND", "message": "not found"},
		})
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key") // pragma: allowlist secret
	c.SetTenantIDForTest("t-123")

	r := NewResource()
	r.(resource.ResourceWithConfigure).Configure(context.Background(), resource.ConfigureRequest{ProviderData: c}, &resource.ConfigureResponse{})

	s := bucketSchema(t)
	stateVal := tftypes.NewValue(bucketObjectType(), map[string]tftypes.Value{
		"name":          tftypes.NewValue(tftypes.String, "gone-bucket"),
		"region":        tftypes.NewValue(tftypes.String, "sweden"),
		"storage_class": tftypes.NewValue(tftypes.String, "standard"),
		"versioning":    tftypes.NewValue(tftypes.String, "enabled"),
		"tags":          tftypes.NewValue(tftypes.Map{ElementType: tftypes.String}, nil),
		"object_count":  tftypes.NewValue(tftypes.Number, 0),
		"size_bytes":    tftypes.NewValue(tftypes.Number, 0),
		"endpoint":      tftypes.NewValue(tftypes.String, "https://s3.frostmoln.cloud"),
		"access_key":    tftypes.NewValue(tftypes.String, nil),
		"created_at":    tftypes.NewValue(tftypes.String, "2025-01-01T00:00:00Z"),
	})

	state := tfsdk.State{Schema: s, Raw: stateVal}
	resp := &resource.DeleteResponse{}
	r.Delete(context.Background(), resource.DeleteRequest{State: state}, resp)

	if resp.Diagnostics.HasError() {
		t.Errorf("expected no errors when deleting already-gone bucket, got %v", resp.Diagnostics)
	}
}

// Ensure fmt is used.
var _ = fmt.Sprintf
