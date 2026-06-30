package snapshot

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
)

func TestSnapshotModel_toCreateRequest(t *testing.T) {
	ctx := context.Background()
	diags := diag.Diagnostics{}

	tags, d := types.MapValueFrom(ctx, types.StringType, map[string]string{"backup": "daily"})
	if d.HasError() {
		t.Fatalf("failed to create tags: %s", d.Errors())
	}

	model := &SnapshotModel{
		Name:        types.StringValue("my-snapshot"),
		Description: types.StringValue("daily backup"),
		VolumeID:    types.StringValue("vol-123"),
		Tags:        tags,
	}

	req := model.toCreateRequest(ctx, &diags)
	if diags.HasError() {
		t.Fatalf("unexpected diagnostics: %s", diags.Errors())
	}

	if req.Name != "my-snapshot" {
		t.Errorf("expected name my-snapshot, got %s", req.Name)
	}
	if req.Description != "daily backup" {
		t.Errorf("expected description 'daily backup', got %s", req.Description)
	}
	if req.VolumeID != "vol-123" {
		t.Errorf("expected volumeId vol-123, got %s", req.VolumeID)
	}
	if req.Metadata["backup"] != "daily" {
		t.Errorf("expected tag backup=daily, got %v", req.Metadata)
	}
}

func TestSnapshotModel_toCreateRequest_minimal(t *testing.T) {
	ctx := context.Background()
	diags := diag.Diagnostics{}

	model := &SnapshotModel{
		Name:        types.StringValue("basic-snap"),
		Description: types.StringNull(),
		VolumeID:    types.StringValue("vol-456"),
		Tags:        types.MapNull(types.StringType),
	}

	req := model.toCreateRequest(ctx, &diags)
	if diags.HasError() {
		t.Fatalf("unexpected diagnostics: %s", diags.Errors())
	}

	if req.Name != "basic-snap" {
		t.Errorf("expected name basic-snap, got %s", req.Name)
	}
	if req.Description != "" {
		t.Errorf("expected empty description, got %s", req.Description)
	}
	if req.VolumeID != "vol-456" {
		t.Errorf("expected volumeId vol-456, got %s", req.VolumeID)
	}
	if req.Metadata != nil {
		t.Errorf("expected nil tags, got %v", req.Metadata)
	}
}

func TestSnapshotModel_fromAPI(t *testing.T) {
	ctx := context.Background()
	diags := diag.Diagnostics{}

	apiSnap := &apiSnapshot{
		ID:          "snap-123",
		Name:        "my-snapshot",
		Description: "test description",
		VolumeID:    "vol-123",
		Status:      "available",
		Size:        100,
		Metadata:    map[string]string{"backup": "daily"},
		CreatedAt:   "2025-01-01T00:00:00Z",
	}

	model := &SnapshotModel{}
	model.fromAPI(ctx, apiSnap, &diags)
	if diags.HasError() {
		t.Fatalf("unexpected diagnostics: %s", diags.Errors())
	}

	if model.ID.ValueString() != "snap-123" {
		t.Errorf("expected ID snap-123, got %s", model.ID.ValueString())
	}
	if model.Name.ValueString() != "my-snapshot" {
		t.Errorf("expected name my-snapshot, got %s", model.Name.ValueString())
	}
	if model.Description.ValueString() != "test description" {
		t.Errorf("expected description 'test description', got %s", model.Description.ValueString())
	}
	if model.VolumeID.ValueString() != "vol-123" {
		t.Errorf("expected volumeId vol-123, got %s", model.VolumeID.ValueString())
	}
	if model.Status.ValueString() != "available" {
		t.Errorf("expected status available, got %s", model.Status.ValueString())
	}
	if model.SizeGB.ValueInt64() != 100 {
		t.Errorf("expected sizeGb 100, got %d", model.SizeGB.ValueInt64())
	}
	if model.CreatedAt.ValueString() != "2025-01-01T00:00:00Z" {
		t.Errorf("expected createdAt 2025-01-01T00:00:00Z, got %s", model.CreatedAt.ValueString())
	}
}

func TestSnapshotModel_fromAPI_nullOptionalFields(t *testing.T) {
	ctx := context.Background()
	diags := diag.Diagnostics{}

	apiSnap := &apiSnapshot{
		ID:        "snap-123",
		Name:      "minimal-snap",
		VolumeID:  "vol-123",
		Status:    "available",
		Size:      50,
		CreatedAt: "2025-01-01T00:00:00Z",
	}

	model := &SnapshotModel{}
	model.fromAPI(ctx, apiSnap, &diags)
	if diags.HasError() {
		t.Fatalf("unexpected diagnostics: %s", diags.Errors())
	}

	if !model.Tags.IsNull() {
		t.Error("expected tags to be null")
	}
}

func TestSnapshotResource_CreateAndRead(t *testing.T) {
	var createCalls atomic.Int32

	snapshot := apiSnapshot{
		ID:        "snap-abc",
		Name:      "test-snap",
		VolumeID:  "vol-123",
		Status:    "available",
		Size:      100,
		CreatedAt: "2025-01-01T00:00:00Z",
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/me":
			_ = json.NewEncoder(w).Encode(client.UserProfile{
				ID: "user-1", TenantID: "tenant-1",
			})

		case r.Method == http.MethodPost && r.URL.Path == "/v1/tenants/tenant-1/volumes/vol-123/snapshots":
			createCalls.Add(1)
			creating := snapshot
			creating.Status = "creating"
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(creating)

		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/tenant-1/volumes/vol-123/snapshots/snap-abc":
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(snapshot)

		default:
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"error": map[string]string{"code": "NOT_FOUND", "message": "not found"},
			})
		}
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key", client.WithHTTPClient(server.Client()))
	if err := c.Configure(context.Background()); err != nil {
		t.Fatalf("configure failed: %v", err)
	}

	ctx := context.Background()

	// Test Create
	createReq := apiCreateSnapshotRequest{
		Name:     "test-snap",
		VolumeID: "vol-123",
	}

	apiResp, err := c.Post(ctx, c.TenantPath("/volumes/vol-123/snapshots"), createReq)
	if err != nil {
		t.Fatalf("create failed: %v", err)
	}
	if apiResp.StatusCode != http.StatusAccepted {
		t.Errorf("expected status 202, got %d", apiResp.StatusCode)
	}

	snap, err := client.ParseResponse[apiSnapshot](apiResp)
	if err != nil {
		t.Fatalf("parse create response failed: %v", err)
	}
	if snap.ID != "snap-abc" {
		t.Errorf("expected ID snap-abc, got %s", snap.ID)
	}

	// Test Read
	getResp, err := c.Get(ctx, c.TenantPath("/volumes/vol-123/snapshots/snap-abc"), nil)
	if err != nil {
		t.Fatalf("read failed: %v", err)
	}

	readSnap, err := client.ParseResponse[apiSnapshot](getResp)
	if err != nil {
		t.Fatalf("parse read response failed: %v", err)
	}
	if readSnap.Status != "available" {
		t.Errorf("expected status available, got %s", readSnap.Status)
	}
	if readSnap.Name != "test-snap" {
		t.Errorf("expected name test-snap, got %s", readSnap.Name)
	}
}

func TestSnapshotResource_Delete(t *testing.T) {
	var deleteCalled atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/me":
			_ = json.NewEncoder(w).Encode(client.UserProfile{
				ID: "user-1", TenantID: "tenant-1",
			})

		case r.Method == http.MethodDelete && r.URL.Path == "/v1/tenants/tenant-1/volumes/vol-123/snapshots/snap-abc":
			deleteCalled.Add(1)
			w.WriteHeader(http.StatusNoContent)

		default:
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"error": map[string]string{"code": "NOT_FOUND", "message": "not found"},
			})
		}
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key", client.WithHTTPClient(server.Client()))
	if err := c.Configure(context.Background()); err != nil {
		t.Fatalf("configure failed: %v", err)
	}

	_, err := c.Delete(context.Background(), c.TenantPath("/volumes/vol-123/snapshots/snap-abc"))
	if err != nil {
		t.Fatalf("delete failed: %v", err)
	}
	if deleteCalled.Load() != 1 {
		t.Errorf("expected delete called once, got %d", deleteCalled.Load())
	}
}

func TestSnapshotResource_ReadNotFound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/me":
			_ = json.NewEncoder(w).Encode(client.UserProfile{
				ID: "user-1", TenantID: "tenant-1",
			})

		default:
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"error": map[string]string{"code": "NOT_FOUND", "message": "not found"},
			})
		}
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key", client.WithHTTPClient(server.Client()))
	if err := c.Configure(context.Background()); err != nil {
		t.Fatalf("configure failed: %v", err)
	}

	_, err := c.Get(context.Background(), c.TenantPath("/volumes/vol-123/snapshots/nonexistent"), nil)
	if err == nil {
		t.Fatal("expected error for nonexistent snapshot")
	}
	if !client.IsNotFound(err) {
		t.Errorf("expected not found error, got %v", err)
	}
}

// --- Resource method tests (tfsdk-level) ---

func snapMeServer(t *testing.T, handler func(w http.ResponseWriter, r *http.Request)) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/v1/me" {
			_ = json.NewEncoder(w).Encode(client.UserProfile{
				ID: "user-1", TenantID: "tenant-1",
			})
			return
		}
		handler(w, r)
	}))
}

func configuredSnapshotResource(t *testing.T, serverURL string) *snapshotResource {
	t.Helper()
	c := client.NewClient(serverURL, "test-key", client.WithHTTPClient(http.DefaultClient))
	if err := c.Configure(context.Background()); err != nil {
		t.Fatalf("configure failed: %v", err)
	}
	return &snapshotResource{client: c}
}

func snapshotSchema(t *testing.T) schema.Schema {
	t.Helper()
	r := &snapshotResource{}
	resp := resource.SchemaResponse{}
	r.Schema(context.Background(), resource.SchemaRequest{}, &resp)
	return resp.Schema
}

func snapshotTFType(t *testing.T) tftypes.Type {
	t.Helper()
	s := snapshotSchema(t)
	return s.Type().TerraformType(context.Background())
}

func TestSnapshotResource_NewResource(t *testing.T) {
	r := NewResource()
	if r == nil {
		t.Fatal("expected non-nil resource")
	}
	if _, ok := r.(*snapshotResource); !ok {
		t.Fatalf("expected *snapshotResource, got %T", r)
	}
}

func TestSnapshotResource_Metadata(t *testing.T) {
	r := &snapshotResource{}
	req := resource.MetadataRequest{ProviderTypeName: "frostmoln"}
	resp := resource.MetadataResponse{}
	r.Metadata(context.Background(), req, &resp)

	if resp.TypeName != "frostmoln_snapshot" {
		t.Errorf("expected type name frostmoln_snapshot, got %s", resp.TypeName)
	}
}

func TestSnapshotResource_Schema_Attributes(t *testing.T) {
	s := snapshotSchema(t)
	if s.Description == "" {
		t.Error("expected non-empty schema description")
	}
	for _, name := range []string{"id", "name", "description", "volume_id", "tags", "status", "size_gb", "created_at"} {
		if _, ok := s.Attributes[name]; !ok {
			t.Errorf("expected attribute %s in schema", name)
		}
	}
}

func TestSnapshotResource_Configure_NilProviderData(t *testing.T) {
	r := &snapshotResource{}
	req := resource.ConfigureRequest{ProviderData: nil}
	resp := resource.ConfigureResponse{}
	r.Configure(context.Background(), req, &resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("expected no errors, got %v", resp.Diagnostics.Errors())
	}
	if r.client != nil {
		t.Error("expected nil client")
	}
}

func TestSnapshotResource_Configure_ValidClient(t *testing.T) {
	r := &snapshotResource{}
	c := client.NewClient("http://localhost", "test-key")
	req := resource.ConfigureRequest{ProviderData: c}
	resp := resource.ConfigureResponse{}
	r.Configure(context.Background(), req, &resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("expected no errors, got %v", resp.Diagnostics.Errors())
	}
	if r.client != c {
		t.Error("expected client to be set")
	}
}

func TestSnapshotResource_Configure_WrongType(t *testing.T) {
	r := &snapshotResource{}
	req := resource.ConfigureRequest{ProviderData: "wrong"}
	resp := resource.ConfigureResponse{}
	r.Configure(context.Background(), req, &resp)

	if !resp.Diagnostics.HasError() {
		t.Fatal("expected error for wrong type")
	}
}

func TestSnapshotResource_Create_TFSDK(t *testing.T) {
	server := snapMeServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/tenants/tenant-1/volumes/vol-123/snapshots":
			// Provisioning returns 202 + an Operation envelope (operationId only).
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"operationId":  "op-snap-1",
				"status":       "pending",
				"resourceType": "snapshot",
			})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/operations/op-snap-1":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"operationId":  "op-snap-1",
				"status":       "completed",
				"resourceType": "snapshot",
				"resourceId":   "snap-abc",
			})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/tenant-1/volumes/vol-123/snapshots/snap-abc":
			_ = json.NewEncoder(w).Encode(apiSnapshot{
				ID:        "snap-abc",
				Name:      "test-snap",
				VolumeID:  "vol-123",
				Status:    "available",
				Size:      100,
				CreatedAt: "2025-06-01T12:00:00Z",
			})
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	})
	defer server.Close()

	ctx := context.Background()
	res := configuredSnapshotResource(t, server.URL)
	s := snapshotSchema(t)
	tfType := snapshotTFType(t)

	planVal := tftypes.NewValue(tfType, map[string]tftypes.Value{
		"id":          tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"name":        tftypes.NewValue(tftypes.String, "test-snap"),
		"description": tftypes.NewValue(tftypes.String, nil),
		"volume_id":   tftypes.NewValue(tftypes.String, "vol-123"),
		"tags":        tftypes.NewValue(tftypes.Map{ElementType: tftypes.String}, nil),
		"status":      tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"size_gb":     tftypes.NewValue(tftypes.Number, tftypes.UnknownValue),
		"created_at":  tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
	})

	createReq := resource.CreateRequest{
		Plan: tfsdk.Plan{Schema: s, Raw: planVal},
	}
	createResp := &resource.CreateResponse{
		State: tfsdk.State{Schema: s},
	}

	res.Create(ctx, createReq, createResp)

	if createResp.Diagnostics.HasError() {
		t.Fatalf("unexpected errors: %v", createResp.Diagnostics.Errors())
	}

	var model SnapshotModel
	createResp.State.Get(ctx, &model)
	if model.ID.ValueString() != "snap-abc" {
		t.Errorf("expected ID snap-abc, got %s", model.ID.ValueString())
	}
	if model.Status.ValueString() != "available" {
		t.Errorf("expected status available, got %s", model.Status.ValueString())
	}
	if model.VolumeID.ValueString() != "vol-123" {
		t.Errorf("expected volume_id vol-123, got %s", model.VolumeID.ValueString())
	}
	if model.SizeGB.ValueInt64() != 100 {
		t.Errorf("expected size_gb 100, got %d", model.SizeGB.ValueInt64())
	}
}

func TestSnapshotResource_Create_APIError(t *testing.T) {
	server := snapMeServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"error": map[string]string{"code": "INTERNAL", "message": "server error"},
		})
	})
	defer server.Close()

	ctx := context.Background()
	res := configuredSnapshotResource(t, server.URL)
	s := snapshotSchema(t)
	tfType := snapshotTFType(t)

	planVal := tftypes.NewValue(tfType, map[string]tftypes.Value{
		"id":          tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"name":        tftypes.NewValue(tftypes.String, "test-snap"),
		"description": tftypes.NewValue(tftypes.String, nil),
		"volume_id":   tftypes.NewValue(tftypes.String, "vol-123"),
		"tags":        tftypes.NewValue(tftypes.Map{ElementType: tftypes.String}, nil),
		"status":      tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"size_gb":     tftypes.NewValue(tftypes.Number, tftypes.UnknownValue),
		"created_at":  tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
	})

	createReq := resource.CreateRequest{
		Plan: tfsdk.Plan{Schema: s, Raw: planVal},
	}
	createResp := &resource.CreateResponse{
		State: tfsdk.State{Schema: s},
	}

	res.Create(ctx, createReq, createResp)

	if !createResp.Diagnostics.HasError() {
		t.Fatal("expected error from API failure")
	}
}

func TestSnapshotResource_Read_TFSDK(t *testing.T) {
	server := snapMeServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/tenant-1/volumes/vol-123/snapshots/snap-abc":
			_ = json.NewEncoder(w).Encode(apiSnapshot{
				ID:        "snap-abc",
				Name:      "test-snap",
				VolumeID:  "vol-123",
				Status:    "available",
				Size:      100,
				CreatedAt: "2025-06-01T12:00:00Z",
			})
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	})
	defer server.Close()

	ctx := context.Background()
	res := configuredSnapshotResource(t, server.URL)
	s := snapshotSchema(t)
	tfType := snapshotTFType(t)

	stateVal := tftypes.NewValue(tfType, map[string]tftypes.Value{
		"id":          tftypes.NewValue(tftypes.String, "snap-abc"),
		"name":        tftypes.NewValue(tftypes.String, "test-snap"),
		"description": tftypes.NewValue(tftypes.String, nil),
		"volume_id":   tftypes.NewValue(tftypes.String, "vol-123"),
		"tags":        tftypes.NewValue(tftypes.Map{ElementType: tftypes.String}, nil),
		"status":      tftypes.NewValue(tftypes.String, "available"),
		"size_gb":     tftypes.NewValue(tftypes.Number, int64(100)),
		"created_at":  tftypes.NewValue(tftypes.String, "2025-06-01T12:00:00Z"),
	})

	readReq := resource.ReadRequest{
		State: tfsdk.State{Schema: s, Raw: stateVal},
	}
	readResp := &resource.ReadResponse{
		State: tfsdk.State{Schema: s},
	}

	res.Read(ctx, readReq, readResp)

	if readResp.Diagnostics.HasError() {
		t.Fatalf("unexpected errors: %v", readResp.Diagnostics.Errors())
	}

	var model SnapshotModel
	readResp.State.Get(ctx, &model)
	if model.ID.ValueString() != "snap-abc" {
		t.Errorf("expected ID snap-abc, got %s", model.ID.ValueString())
	}
	if model.Status.ValueString() != "available" {
		t.Errorf("expected status available, got %s", model.Status.ValueString())
	}
}

func TestSnapshotResource_Read_NotFound_TFSDK(t *testing.T) {
	server := snapMeServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"error": map[string]string{"code": "NOT_FOUND", "message": "not found"},
		})
	})
	defer server.Close()

	ctx := context.Background()
	res := configuredSnapshotResource(t, server.URL)
	s := snapshotSchema(t)
	tfType := snapshotTFType(t)

	stateVal := tftypes.NewValue(tfType, map[string]tftypes.Value{
		"id":          tftypes.NewValue(tftypes.String, "nonexistent"),
		"name":        tftypes.NewValue(tftypes.String, "snap"),
		"description": tftypes.NewValue(tftypes.String, nil),
		"volume_id":   tftypes.NewValue(tftypes.String, "vol-123"),
		"tags":        tftypes.NewValue(tftypes.Map{ElementType: tftypes.String}, nil),
		"status":      tftypes.NewValue(tftypes.String, "available"),
		"size_gb":     tftypes.NewValue(tftypes.Number, int64(50)),
		"created_at":  tftypes.NewValue(tftypes.String, "2025-06-01T12:00:00Z"),
	})

	readReq := resource.ReadRequest{
		State: tfsdk.State{Schema: s, Raw: stateVal},
	}
	readResp := &resource.ReadResponse{
		State: tfsdk.State{Schema: s},
	}

	res.Read(ctx, readReq, readResp)

	if readResp.Diagnostics.HasError() {
		t.Fatalf("expected no errors on not-found, got %v", readResp.Diagnostics.Errors())
	}
	if !readResp.State.Raw.IsNull() {
		t.Error("expected null state after not-found read")
	}
}

func TestSnapshotResource_Read_APIError(t *testing.T) {
	server := snapMeServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"error": map[string]string{"code": "INTERNAL", "message": "server error"},
		})
	})
	defer server.Close()

	ctx := context.Background()
	res := configuredSnapshotResource(t, server.URL)
	s := snapshotSchema(t)
	tfType := snapshotTFType(t)

	stateVal := tftypes.NewValue(tfType, map[string]tftypes.Value{
		"id":          tftypes.NewValue(tftypes.String, "snap-abc"),
		"name":        tftypes.NewValue(tftypes.String, "snap"),
		"description": tftypes.NewValue(tftypes.String, nil),
		"volume_id":   tftypes.NewValue(tftypes.String, "vol-123"),
		"tags":        tftypes.NewValue(tftypes.Map{ElementType: tftypes.String}, nil),
		"status":      tftypes.NewValue(tftypes.String, "available"),
		"size_gb":     tftypes.NewValue(tftypes.Number, int64(50)),
		"created_at":  tftypes.NewValue(tftypes.String, "2025-06-01T12:00:00Z"),
	})

	readReq := resource.ReadRequest{
		State: tfsdk.State{Schema: s, Raw: stateVal},
	}
	readResp := &resource.ReadResponse{
		State: tfsdk.State{Schema: s},
	}

	res.Read(ctx, readReq, readResp)

	if !readResp.Diagnostics.HasError() {
		t.Fatal("expected error from API failure")
	}
}

func TestSnapshotResource_Update_TFSDK(t *testing.T) {
	r := &snapshotResource{}
	resp := &resource.UpdateResponse{}
	r.Update(context.Background(), resource.UpdateRequest{}, resp)

	if !resp.Diagnostics.HasError() {
		t.Fatal("expected error from unsupported update")
	}

	found := false
	for _, d := range resp.Diagnostics.Errors() {
		if d.Summary() == "Update Not Supported" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected 'Update Not Supported' error")
	}
}

func TestSnapshotResource_Delete_TFSDK(t *testing.T) {
	deleted := false
	server := snapMeServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodDelete && r.URL.Path == "/v1/tenants/tenant-1/volumes/vol-123/snapshots/snap-abc":
			deleted = true
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	})
	defer server.Close()

	ctx := context.Background()
	res := configuredSnapshotResource(t, server.URL)
	s := snapshotSchema(t)
	tfType := snapshotTFType(t)

	stateVal := tftypes.NewValue(tfType, map[string]tftypes.Value{
		"id":          tftypes.NewValue(tftypes.String, "snap-abc"),
		"name":        tftypes.NewValue(tftypes.String, "test-snap"),
		"description": tftypes.NewValue(tftypes.String, nil),
		"volume_id":   tftypes.NewValue(tftypes.String, "vol-123"),
		"tags":        tftypes.NewValue(tftypes.Map{ElementType: tftypes.String}, nil),
		"status":      tftypes.NewValue(tftypes.String, "available"),
		"size_gb":     tftypes.NewValue(tftypes.Number, int64(100)),
		"created_at":  tftypes.NewValue(tftypes.String, "2025-06-01T12:00:00Z"),
	})

	deleteReq := resource.DeleteRequest{
		State: tfsdk.State{Schema: s, Raw: stateVal},
	}
	deleteResp := &resource.DeleteResponse{}

	res.Delete(ctx, deleteReq, deleteResp)

	if deleteResp.Diagnostics.HasError() {
		t.Fatalf("unexpected errors: %v", deleteResp.Diagnostics.Errors())
	}
	if !deleted {
		t.Error("expected delete API call")
	}
}

func TestSnapshotResource_Delete_NotFound_TFSDK(t *testing.T) {
	server := snapMeServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"error": map[string]string{"code": "NOT_FOUND", "message": "not found"},
		})
	})
	defer server.Close()

	ctx := context.Background()
	res := configuredSnapshotResource(t, server.URL)
	s := snapshotSchema(t)
	tfType := snapshotTFType(t)

	stateVal := tftypes.NewValue(tfType, map[string]tftypes.Value{
		"id":          tftypes.NewValue(tftypes.String, "gone"),
		"name":        tftypes.NewValue(tftypes.String, "snap"),
		"description": tftypes.NewValue(tftypes.String, nil),
		"volume_id":   tftypes.NewValue(tftypes.String, "vol-123"),
		"tags":        tftypes.NewValue(tftypes.Map{ElementType: tftypes.String}, nil),
		"status":      tftypes.NewValue(tftypes.String, "available"),
		"size_gb":     tftypes.NewValue(tftypes.Number, int64(50)),
		"created_at":  tftypes.NewValue(tftypes.String, "2025-06-01T12:00:00Z"),
	})

	deleteReq := resource.DeleteRequest{
		State: tfsdk.State{Schema: s, Raw: stateVal},
	}
	deleteResp := &resource.DeleteResponse{}

	res.Delete(ctx, deleteReq, deleteResp)

	if deleteResp.Diagnostics.HasError() {
		t.Fatalf("expected no errors on delete of nonexistent resource, got %v", deleteResp.Diagnostics.Errors())
	}
}

func TestSnapshotResource_Delete_APIError(t *testing.T) {
	server := snapMeServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"error": map[string]string{"code": "INTERNAL", "message": "server error"},
		})
	})
	defer server.Close()

	ctx := context.Background()
	res := configuredSnapshotResource(t, server.URL)
	s := snapshotSchema(t)
	tfType := snapshotTFType(t)

	stateVal := tftypes.NewValue(tfType, map[string]tftypes.Value{
		"id":          tftypes.NewValue(tftypes.String, "snap-abc"),
		"name":        tftypes.NewValue(tftypes.String, "snap"),
		"description": tftypes.NewValue(tftypes.String, nil),
		"volume_id":   tftypes.NewValue(tftypes.String, "vol-123"),
		"tags":        tftypes.NewValue(tftypes.Map{ElementType: tftypes.String}, nil),
		"status":      tftypes.NewValue(tftypes.String, "available"),
		"size_gb":     tftypes.NewValue(tftypes.Number, int64(50)),
		"created_at":  tftypes.NewValue(tftypes.String, "2025-06-01T12:00:00Z"),
	})

	deleteReq := resource.DeleteRequest{
		State: tfsdk.State{Schema: s, Raw: stateVal},
	}
	deleteResp := &resource.DeleteResponse{}

	res.Delete(ctx, deleteReq, deleteResp)

	if !deleteResp.Diagnostics.HasError() {
		t.Fatal("expected error from API failure")
	}
}

func TestSnapshotResource_ImportState_TFSDK(t *testing.T) {
	r := &snapshotResource{}
	s := snapshotSchema(t)
	tfType := snapshotTFType(t)

	// Initialize state with null values so the schema type is set.
	initVal := tftypes.NewValue(tfType, map[string]tftypes.Value{
		"id":          tftypes.NewValue(tftypes.String, nil),
		"name":        tftypes.NewValue(tftypes.String, nil),
		"description": tftypes.NewValue(tftypes.String, nil),
		"volume_id":   tftypes.NewValue(tftypes.String, nil),
		"tags":        tftypes.NewValue(tftypes.Map{ElementType: tftypes.String}, nil),
		"status":      tftypes.NewValue(tftypes.String, nil),
		"size_gb":     tftypes.NewValue(tftypes.Number, nil),
		"created_at":  tftypes.NewValue(tftypes.String, nil),
	})

	importReq := resource.ImportStateRequest{ID: "snap-abc"}
	importResp := &resource.ImportStateResponse{
		State: tfsdk.State{Schema: s, Raw: initVal},
	}

	r.ImportState(context.Background(), importReq, importResp)

	if importResp.Diagnostics.HasError() {
		t.Fatalf("unexpected errors: %v", importResp.Diagnostics.Errors())
	}

	var model SnapshotModel
	importResp.State.Get(context.Background(), &model)
	if model.ID.ValueString() != "snap-abc" {
		t.Errorf("expected imported ID snap-abc, got %s", model.ID.ValueString())
	}
}

// TestSnapshotModel_fromAPI_FiltersReservedTags asserts a snapshot that inherits
// its source volume's backend-stamped reserved metadata (bare *-id + frostmoln_*)
// never surfaces those as customer tags, mirroring the volume filter. Without it a
// null/unset tags plan reads back the system keys → "inconsistent result after apply".
func TestSnapshotModel_fromAPI_FiltersReservedTags(t *testing.T) {
	ctx := context.Background()

	// null tags plan stays null despite inherited reserved metadata.
	t.Run("system metadata only keeps tags null", func(t *testing.T) {
		diags := diag.Diagnostics{}
		snap := &apiSnapshot{
			ID:       "snap-1",
			Name:     "snap",
			VolumeID: "vol-1",
			Status:   "available",
			Metadata: map[string]string{
				"customer-id":    "c1",
				"request-id":     "r1",
				"project-id":     "p1",
				"frostmoln_type": "managed",
			},
		}
		model := &SnapshotModel{Tags: types.MapNull(types.StringType)}
		model.fromAPI(ctx, snap, &diags)
		if diags.HasError() {
			t.Fatalf("unexpected diagnostics: %v", diags.Errors())
		}
		if !model.Tags.IsNull() {
			t.Errorf("expected tags to stay null when only reserved metadata present, got %v", model.Tags)
		}
	})

	// user tags round-trip; only reserved keys are dropped.
	t.Run("user tags survive, reserved keys dropped", func(t *testing.T) {
		diags := diag.Diagnostics{}
		priorTags, d := types.MapValueFrom(ctx, types.StringType, map[string]string{"backup": "daily"})
		diags.Append(d...)
		snap := &apiSnapshot{
			ID:       "snap-2",
			Name:     "snap",
			VolumeID: "vol-1",
			Status:   "available",
			Metadata: map[string]string{"backup": "daily", "customer-id": "c1", "project-id": "p1"},
		}
		model := &SnapshotModel{Tags: priorTags}
		model.fromAPI(ctx, snap, &diags)
		if diags.HasError() {
			t.Fatalf("unexpected diagnostics: %v", diags.Errors())
		}
		var gotTags map[string]string
		diags.Append(model.Tags.ElementsAs(ctx, &gotTags, false)...)
		if len(gotTags) != 1 || gotTags["backup"] != "daily" {
			t.Errorf("expected tags {backup: daily} with reserved keys filtered, got %v", gotTags)
		}
	})
}
