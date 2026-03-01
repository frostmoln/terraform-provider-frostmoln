package volume

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

func TestVolumeModel_toCreateRequest(t *testing.T) {
	ctx := context.Background()
	diags := diag.Diagnostics{}

	tags, d := types.MapValueFrom(ctx, types.StringType, map[string]string{"env": "prod"})
	if d.HasError() {
		t.Fatalf("failed to create tags: %s", d.Errors())
	}

	model := &VolumeModel{
		Name:        types.StringValue("my-volume"),
		Description: types.StringValue("test volume"),
		SizeGB:      types.Int64Value(100),
		VolumeType:  types.StringValue("ssd"),
		Zone:        types.StringValue("eu-north-1a"),
		SnapshotID:  types.StringNull(),
		Encrypted:   types.BoolValue(true),
		Tags:        tags,
	}

	req := model.toCreateRequest(ctx, &diags)
	if diags.HasError() {
		t.Fatalf("unexpected diagnostics: %s", diags.Errors())
	}

	if req.Name != "my-volume" {
		t.Errorf("expected name my-volume, got %s", req.Name)
	}
	if req.Description != "test volume" {
		t.Errorf("expected description 'test volume', got %s", req.Description)
	}
	if req.SizeGB != 100 {
		t.Errorf("expected sizeGb 100, got %d", req.SizeGB)
	}
	if req.VolumeType != "ssd" {
		t.Errorf("expected volumeType ssd, got %s", req.VolumeType)
	}
	if req.Zone != "eu-north-1a" {
		t.Errorf("expected zone eu-north-1a, got %s", req.Zone)
	}
	if req.SnapshotID != "" {
		t.Errorf("expected empty snapshotId, got %s", req.SnapshotID)
	}
	if !req.Encrypted {
		t.Error("expected encrypted true")
	}
	if req.Tags["env"] != "prod" {
		t.Errorf("expected tag env=prod, got %v", req.Tags)
	}
}

func TestVolumeModel_toCreateRequest_minimal(t *testing.T) {
	ctx := context.Background()
	diags := diag.Diagnostics{}

	model := &VolumeModel{
		Name:        types.StringValue("basic"),
		Description: types.StringNull(),
		SizeGB:      types.Int64Value(50),
		VolumeType:  types.StringNull(),
		Zone:        types.StringNull(),
		SnapshotID:  types.StringNull(),
		Encrypted:   types.BoolValue(false),
		Tags:        types.MapNull(types.StringType),
	}

	req := model.toCreateRequest(ctx, &diags)
	if diags.HasError() {
		t.Fatalf("unexpected diagnostics: %s", diags.Errors())
	}

	if req.Name != "basic" {
		t.Errorf("expected name basic, got %s", req.Name)
	}
	if req.SizeGB != 50 {
		t.Errorf("expected sizeGb 50, got %d", req.SizeGB)
	}
	if req.VolumeType != "" {
		t.Errorf("expected empty volumeType, got %s", req.VolumeType)
	}
	if req.Tags != nil {
		t.Errorf("expected nil tags, got %v", req.Tags)
	}
}

func TestVolumeModel_fromAPI(t *testing.T) {
	ctx := context.Background()
	diags := diag.Diagnostics{}

	apiVol := &apiVolume{
		ID:          "vol-123",
		Name:        "my-volume",
		Description: "test description",
		SizeGB:      100,
		VolumeType:  "ssd",
		Zone:        "eu-north-1a",
		Encrypted:   true,
		Status:      "available",
		IOPS:        3000,
		Throughput:  125,
		Region:      "eu-north-1",
		AttachedTo:  "inst-456",
		DevicePath:  "/dev/vdb",
		Tags:        map[string]string{"env": "prod"},
		CreatedAt:   "2025-01-01T00:00:00Z",
	}

	model := &VolumeModel{}
	model.fromAPI(ctx, apiVol, &diags)
	if diags.HasError() {
		t.Fatalf("unexpected diagnostics: %s", diags.Errors())
	}

	if model.ID.ValueString() != "vol-123" {
		t.Errorf("expected ID vol-123, got %s", model.ID.ValueString())
	}
	if model.Name.ValueString() != "my-volume" {
		t.Errorf("expected name my-volume, got %s", model.Name.ValueString())
	}
	if model.Description.ValueString() != "test description" {
		t.Errorf("expected description 'test description', got %s", model.Description.ValueString())
	}
	if model.SizeGB.ValueInt64() != 100 {
		t.Errorf("expected sizeGb 100, got %d", model.SizeGB.ValueInt64())
	}
	if model.VolumeType.ValueString() != "ssd" {
		t.Errorf("expected volumeType ssd, got %s", model.VolumeType.ValueString())
	}
	if model.Zone.ValueString() != "eu-north-1a" {
		t.Errorf("expected zone eu-north-1a, got %s", model.Zone.ValueString())
	}
	if !model.Encrypted.ValueBool() {
		t.Error("expected encrypted true")
	}
	if model.Status.ValueString() != "available" {
		t.Errorf("expected status available, got %s", model.Status.ValueString())
	}
	if model.IOPS.ValueInt64() != 3000 {
		t.Errorf("expected iops 3000, got %d", model.IOPS.ValueInt64())
	}
	if model.Throughput.ValueInt64() != 125 {
		t.Errorf("expected throughput 125, got %d", model.Throughput.ValueInt64())
	}
	if model.Region.ValueString() != "eu-north-1" {
		t.Errorf("expected region eu-north-1, got %s", model.Region.ValueString())
	}
	if model.AttachedTo.ValueString() != "inst-456" {
		t.Errorf("expected attachedTo inst-456, got %s", model.AttachedTo.ValueString())
	}
	if model.DevicePath.ValueString() != "/dev/vdb" {
		t.Errorf("expected devicePath /dev/vdb, got %s", model.DevicePath.ValueString())
	}
	if model.CreatedAt.ValueString() != "2025-01-01T00:00:00Z" {
		t.Errorf("expected createdAt 2025-01-01T00:00:00Z, got %s", model.CreatedAt.ValueString())
	}
}

func TestVolumeModel_fromAPI_nullOptionalFields(t *testing.T) {
	ctx := context.Background()
	diags := diag.Diagnostics{}

	apiVol := &apiVolume{
		ID:         "vol-123",
		Name:       "minimal-vol",
		SizeGB:     50,
		VolumeType: "hdd",
		Encrypted:  false,
		Status:     "available",
		Region:     "eu-north-1",
		CreatedAt:  "2025-01-01T00:00:00Z",
	}

	model := &VolumeModel{}
	model.fromAPI(ctx, apiVol, &diags)
	if diags.HasError() {
		t.Fatalf("unexpected diagnostics: %s", diags.Errors())
	}

	if !model.Zone.IsNull() {
		t.Errorf("expected zone to be null, got %s", model.Zone.ValueString())
	}
	if !model.SnapshotID.IsNull() {
		t.Errorf("expected snapshotId to be null, got %s", model.SnapshotID.ValueString())
	}
	if !model.AttachedTo.IsNull() {
		t.Errorf("expected attachedTo to be null, got %s", model.AttachedTo.ValueString())
	}
	if !model.DevicePath.IsNull() {
		t.Errorf("expected devicePath to be null, got %s", model.DevicePath.ValueString())
	}
	if !model.Tags.IsNull() {
		t.Error("expected tags to be null")
	}
}

func newTestClient(t *testing.T, server *httptest.Server) *client.Client {
	t.Helper()
	c := client.NewClient(server.URL, "test-key", client.WithHTTPClient(server.Client()))
	// Set tenant ID directly via Configure with a mock /v1/me endpoint,
	// but since we control the server, we just configure through it.
	return c
}

func TestVolumeResource_CreateAndRead(t *testing.T) {
	var createCalls atomic.Int32

	volume := apiVolume{
		ID:         "vol-abc",
		Name:       "test-vol",
		SizeGB:     100,
		VolumeType: "ssd",
		Encrypted:  true,
		Status:     "available",
		IOPS:       3000,
		Throughput: 125,
		Region:     "eu-north-1",
		CreatedAt:  "2025-01-01T00:00:00Z",
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/me":
			json.NewEncoder(w).Encode(client.UserProfile{
				ID: "user-1", TenantID: "tenant-1",
			})

		case r.Method == http.MethodPost && r.URL.Path == "/v1/tenants/tenant-1/volumes":
			n := createCalls.Add(1)
			if n == 1 {
				// Return 202 with creating status
				creating := volume
				creating.Status = "creating"
				w.WriteHeader(http.StatusAccepted)
				json.NewEncoder(w).Encode(creating)
			}

		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/tenant-1/volumes/vol-abc":
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(volume)

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

	// Test the Create flow at the API level
	ctx := context.Background()
	createReq := apiCreateVolumeRequest{
		Name:      "test-vol",
		SizeGB:    100,
		Encrypted: true,
	}

	apiResp, err := c.Post(ctx, c.TenantPath("/volumes"), createReq)
	if err != nil {
		t.Fatalf("create failed: %v", err)
	}
	if apiResp.StatusCode != http.StatusAccepted {
		t.Errorf("expected status 202, got %d", apiResp.StatusCode)
	}

	vol, err := client.ParseResponse[apiVolume](apiResp)
	if err != nil {
		t.Fatalf("parse create response failed: %v", err)
	}
	if vol.ID != "vol-abc" {
		t.Errorf("expected ID vol-abc, got %s", vol.ID)
	}

	// Test Read
	getResp, err := c.Get(ctx, c.TenantPath("/volumes/vol-abc"), nil)
	if err != nil {
		t.Fatalf("read failed: %v", err)
	}

	readVol, err := client.ParseResponse[apiVolume](getResp)
	if err != nil {
		t.Fatalf("parse read response failed: %v", err)
	}
	if readVol.Status != "available" {
		t.Errorf("expected status available, got %s", readVol.Status)
	}
	if readVol.Name != "test-vol" {
		t.Errorf("expected name test-vol, got %s", readVol.Name)
	}
}

func TestVolumeResource_Update(t *testing.T) {
	currentVolume := apiVolume{
		ID:         "vol-abc",
		Name:       "original-name",
		SizeGB:     100,
		VolumeType: "ssd",
		Encrypted:  true,
		Status:     "available",
		Region:     "eu-north-1",
		CreatedAt:  "2025-01-01T00:00:00Z",
	}

	var patchCalled, resizeCalled atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/me":
			json.NewEncoder(w).Encode(client.UserProfile{
				ID: "user-1", TenantID: "tenant-1",
			})

		case r.Method == http.MethodPatch && r.URL.Path == "/v1/tenants/tenant-1/volumes/vol-abc":
			patchCalled.Add(1)
			var updateReq apiUpdateVolumeRequest
			json.NewDecoder(r.Body).Decode(&updateReq)
			if updateReq.Name != nil {
				currentVolume.Name = *updateReq.Name
			}
			if updateReq.Description != nil {
				currentVolume.Description = *updateReq.Description
			}
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(currentVolume)

		case r.Method == http.MethodPost && r.URL.Path == "/v1/tenants/tenant-1/volumes/vol-abc/resize":
			resizeCalled.Add(1)
			var resizeReq apiResizeVolumeRequest
			json.NewDecoder(r.Body).Decode(&resizeReq)
			currentVolume.SizeGB = resizeReq.SizeGB
			w.WriteHeader(http.StatusAccepted)
			json.NewEncoder(w).Encode(currentVolume)

		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/tenant-1/volumes/vol-abc":
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(currentVolume)

		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key", client.WithHTTPClient(server.Client()))
	if err := c.Configure(context.Background()); err != nil {
		t.Fatalf("configure failed: %v", err)
	}

	ctx := context.Background()

	// Test PATCH update
	name := "updated-name"
	patchReq := apiUpdateVolumeRequest{Name: &name}
	_, err := c.Patch(ctx, c.TenantPath("/volumes/vol-abc"), patchReq)
	if err != nil {
		t.Fatalf("patch failed: %v", err)
	}
	if patchCalled.Load() != 1 {
		t.Errorf("expected patch to be called once, got %d", patchCalled.Load())
	}

	// Test resize
	resizeReq := apiResizeVolumeRequest{SizeGB: 200}
	_, err = c.Post(ctx, c.TenantPath("/volumes/vol-abc/resize"), resizeReq)
	if err != nil {
		t.Fatalf("resize failed: %v", err)
	}
	if resizeCalled.Load() != 1 {
		t.Errorf("expected resize to be called once, got %d", resizeCalled.Load())
	}

	// Verify final state
	getResp, err := c.Get(ctx, c.TenantPath("/volumes/vol-abc"), nil)
	if err != nil {
		t.Fatalf("read failed: %v", err)
	}
	vol, err := client.ParseResponse[apiVolume](getResp)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if vol.Name != "updated-name" {
		t.Errorf("expected name updated-name, got %s", vol.Name)
	}
	if vol.SizeGB != 200 {
		t.Errorf("expected sizeGb 200, got %d", vol.SizeGB)
	}
}

func TestVolumeResource_Delete(t *testing.T) {
	var deleteCalled atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/me":
			json.NewEncoder(w).Encode(client.UserProfile{
				ID: "user-1", TenantID: "tenant-1",
			})

		case r.Method == http.MethodDelete && r.URL.Path == "/v1/tenants/tenant-1/volumes/vol-abc":
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

	_, err := c.Delete(context.Background(), c.TenantPath("/volumes/vol-abc"))
	if err != nil {
		t.Fatalf("delete failed: %v", err)
	}
	if deleteCalled.Load() != 1 {
		t.Errorf("expected delete to be called once, got %d", deleteCalled.Load())
	}
}

func TestVolumeResource_ReadNotFound(t *testing.T) {
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

	_, err := c.Get(context.Background(), c.TenantPath("/volumes/nonexistent"), nil)
	if err == nil {
		t.Fatal("expected error for nonexistent volume")
	}
	if !client.IsNotFound(err) {
		t.Errorf("expected not found error, got %v", err)
	}
}
