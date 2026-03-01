package snapshot

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"git.nl.cloud/NordicLight/terraform-provider-frostmoln/internal/client"
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
	if req.Tags["backup"] != "daily" {
		t.Errorf("expected tag backup=daily, got %v", req.Tags)
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
	if req.Tags != nil {
		t.Errorf("expected nil tags, got %v", req.Tags)
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
		SizeGB:      100,
		Region:      "eu-north-1",
		Tags:        map[string]string{"backup": "daily"},
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
	if model.Region.ValueString() != "eu-north-1" {
		t.Errorf("expected region eu-north-1, got %s", model.Region.ValueString())
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
		SizeGB:    50,
		Region:    "eu-north-1",
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
		SizeGB:    100,
		Region:    "eu-north-1",
		CreatedAt: "2025-01-01T00:00:00Z",
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/me":
			json.NewEncoder(w).Encode(client.UserProfile{
				ID: "user-1", TenantID: "tenant-1",
			})

		case r.Method == http.MethodPost && r.URL.Path == "/v1/tenants/tenant-1/snapshots":
			createCalls.Add(1)
			creating := snapshot
			creating.Status = "creating"
			w.WriteHeader(http.StatusAccepted)
			json.NewEncoder(w).Encode(creating)

		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/tenant-1/snapshots/snap-abc":
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(snapshot)

		default:
			w.WriteHeader(http.StatusNotFound)
			json.NewEncoder(w).Encode(map[string]interface{}{
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

	apiResp, err := c.Post(ctx, c.TenantPath("/snapshots"), createReq)
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
	getResp, err := c.Get(ctx, c.TenantPath("/snapshots/snap-abc"), nil)
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
			json.NewEncoder(w).Encode(client.UserProfile{
				ID: "user-1", TenantID: "tenant-1",
			})

		case r.Method == http.MethodDelete && r.URL.Path == "/v1/tenants/tenant-1/snapshots/snap-abc":
			deleteCalled.Add(1)
			w.WriteHeader(http.StatusNoContent)

		default:
			w.WriteHeader(http.StatusNotFound)
			json.NewEncoder(w).Encode(map[string]interface{}{
				"error": map[string]string{"code": "NOT_FOUND", "message": "not found"},
			})
		}
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key", client.WithHTTPClient(server.Client()))
	if err := c.Configure(context.Background()); err != nil {
		t.Fatalf("configure failed: %v", err)
	}

	_, err := c.Delete(context.Background(), c.TenantPath("/snapshots/snap-abc"))
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
			json.NewEncoder(w).Encode(client.UserProfile{
				ID: "user-1", TenantID: "tenant-1",
			})

		default:
			w.WriteHeader(http.StatusNotFound)
			json.NewEncoder(w).Encode(map[string]interface{}{
				"error": map[string]string{"code": "NOT_FOUND", "message": "not found"},
			})
		}
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key", client.WithHTTPClient(server.Client()))
	if err := c.Configure(context.Background()); err != nil {
		t.Fatalf("configure failed: %v", err)
	}

	_, err := c.Get(context.Background(), c.TenantPath("/snapshots/nonexistent"), nil)
	if err == nil {
		t.Fatal("expected error for nonexistent snapshot")
	}
	if !client.IsNotFound(err) {
		t.Errorf("expected not found error, got %v", err)
	}
}
