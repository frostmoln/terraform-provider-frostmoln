package volume

import (
	"context"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
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
		Zone:        types.StringValue("sweden-a"),
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
	if req.Size != 100 {
		t.Errorf("expected sizeGb 100, got %d", req.Size)
	}
	if req.VolumeType != "ssd" {
		t.Errorf("expected volumeType ssd, got %s", req.VolumeType)
	}
	if req.Zone != "sweden-a" {
		t.Errorf("expected zone sweden-a, got %s", req.Zone)
	}
	if req.SourceSnapshotID != "" {
		t.Errorf("expected empty snapshotId, got %s", req.SourceSnapshotID)
	}
	if !req.Encrypted {
		t.Error("expected encrypted true")
	}
	if req.Metadata["env"] != "prod" {
		t.Errorf("expected tag env=prod, got %v", req.Metadata)
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
	if req.Size != 50 {
		t.Errorf("expected sizeGb 50, got %d", req.Size)
	}
	if req.VolumeType != "" {
		t.Errorf("expected empty volumeType, got %s", req.VolumeType)
	}
	if req.Metadata != nil {
		t.Errorf("expected nil tags, got %v", req.Metadata)
	}
}

func TestVolumeModel_fromAPI(t *testing.T) {
	ctx := context.Background()
	diags := diag.Diagnostics{}

	apiVol := &apiVolume{
		ID:          "vol-123",
		Name:        "my-volume",
		Description: "test description",
		Size:        100,
		VolumeType:  "ssd",
		Zone:        "sweden-a",
		Encrypted:   true,
		Status:      "available",
		IOPS:        3000,
		Throughput:  125,
		Attachments: []apiVolumeAttachment{{InstanceID: "inst-456", Device: "/dev/vdb"}},
		Metadata:    map[string]string{"env": "prod"},
		CreatedAt:   "2025-01-01T00:00:00Z",
	}

	// description is Optional-only and preserved from plan/state on read: a
	// user-set description (non-null) adopts the backend value.
	model := &VolumeModel{Description: types.StringValue("test description")}
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
	if model.Zone.ValueString() != "sweden-a" {
		t.Errorf("expected zone sweden-a, got %s", model.Zone.ValueString())
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
		Size:       50,
		VolumeType: "hdd",
		Encrypted:  false,
		Status:     "available",
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

func TestVolumeResource_CreateAndRead(t *testing.T) {
	var createCalls atomic.Int32

	volume := apiVolume{
		ID:         "vol-abc",
		Name:       "test-vol",
		Size:       100,
		VolumeType: "ssd",
		Encrypted:  true,
		Status:     "available",
		IOPS:       3000,
		Throughput: 125,
		CreatedAt:  "2025-01-01T00:00:00Z",
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/me":
			_ = json.NewEncoder(w).Encode(client.UserProfile{
				ID: "user-1", TenantID: "tenant-1",
			})

		case r.Method == http.MethodPost && r.URL.Path == "/v1/tenants/tenant-1/volumes":
			n := createCalls.Add(1)
			if n == 1 {
				// Return 202 with creating status
				creating := volume
				creating.Status = "creating"
				w.WriteHeader(http.StatusAccepted)
				_ = json.NewEncoder(w).Encode(creating)
			}

		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/tenant-1/volumes/vol-abc":
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(volume)

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

	// Test the Create flow at the API level
	ctx := context.Background()
	createReq := apiCreateVolumeRequest{
		Name:      "test-vol",
		Size:      100,
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
		Size:       100,
		VolumeType: "ssd",
		Encrypted:  true,
		Status:     "available",
		CreatedAt:  "2025-01-01T00:00:00Z",
	}

	var patchCalled, resizeCalled atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/me":
			_ = json.NewEncoder(w).Encode(client.UserProfile{
				ID: "user-1", TenantID: "tenant-1",
			})

		case r.Method == http.MethodPatch && r.URL.Path == "/v1/tenants/tenant-1/volumes/vol-abc":
			patchCalled.Add(1)
			var updateReq apiUpdateVolumeRequest
			_ = json.NewDecoder(r.Body).Decode(&updateReq)
			if updateReq.Name != nil {
				currentVolume.Name = *updateReq.Name
			}
			if updateReq.Description != nil {
				currentVolume.Description = *updateReq.Description
			}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(currentVolume)

		case r.Method == http.MethodPost && r.URL.Path == "/v1/tenants/tenant-1/volumes/vol-abc/resize":
			resizeCalled.Add(1)
			var resizeReq apiResizeVolumeRequest
			_ = json.NewDecoder(r.Body).Decode(&resizeReq)
			currentVolume.Size = int64(resizeReq.NewSizeGB)
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(map[string]string{
				"operationId": "op-resize-1", "status": "accepted", "resourceType": "volume",
			})

		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/tenant-1/volumes/vol-abc":
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(currentVolume)

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
	resizeReq := apiResizeVolumeRequest{NewSizeGB: 200}
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
	if vol.Size != 200 {
		t.Errorf("expected sizeGb 200, got %d", vol.Size)
	}
}

func TestVolumeResource_Delete(t *testing.T) {
	var deleteCalled atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/me":
			_ = json.NewEncoder(w).Encode(client.UserProfile{
				ID: "user-1", TenantID: "tenant-1",
			})

		case r.Method == http.MethodDelete && r.URL.Path == "/v1/tenants/tenant-1/volumes/vol-abc":
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

	_, err := c.Get(context.Background(), c.TenantPath("/volumes/nonexistent"), nil)
	if err == nil {
		t.Fatal("expected error for nonexistent volume")
	}
	if !client.IsNotFound(err) {
		t.Errorf("expected not found error, got %v", err)
	}
}

// --- tfsdk-level CRUD tests ---

func getVolumeSchema(t *testing.T) resource.SchemaResponse {
	t.Helper()
	r := NewResource()
	var schemaResp resource.SchemaResponse
	r.Schema(context.Background(), resource.SchemaRequest{}, &schemaResp)
	return schemaResp
}

func configureVolumeResource(t *testing.T, r resource.Resource, c *client.Client) {
	t.Helper()
	rc, ok := r.(resource.ResourceWithConfigure)
	if !ok {
		t.Fatal("resource does not implement ResourceWithConfigure")
	}
	configReq := resource.ConfigureRequest{ProviderData: c}
	var configResp resource.ConfigureResponse
	rc.Configure(context.Background(), configReq, &configResp)
	if configResp.Diagnostics.HasError() {
		t.Fatalf("configure failed: %v", configResp.Diagnostics.Errors())
	}
}

func TestVolumeResource_TFSDKCreate(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/me":
			_ = json.NewEncoder(w).Encode(map[string]string{"id": "user-123", "tenantId": "tenant-456"})

		case r.Method == http.MethodPost && r.URL.Path == "/v1/tenants/tenant-456/volumes":
			// Provisioning returns 202 + an Operation envelope (operationId only).
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"operationId":  "op-vol-1",
				"status":       "pending",
				"resourceType": "volume",
			})

		case r.Method == http.MethodGet && r.URL.Path == "/v1/operations/op-vol-1":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"operationId":  "op-vol-1",
				"status":       "completed",
				"resourceType": "volume",
				"resourceId":   "vol-new-1",
			})

		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/tenant-456/volumes/vol-new-1":
			_ = json.NewEncoder(w).Encode(apiVolume{
				ID:         "vol-new-1",
				Name:       "new-volume",
				Size:       100,
				VolumeType: "ssd",
				Encrypted:  true,
				Status:     "available",
				IOPS:       3000,
				Throughput: 125,
				CreatedAt:  "2025-01-01T00:00:00Z",
			})

		default:
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"error": map[string]string{"code": "NOT_FOUND", "message": "not found"},
			})
		}
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key") // pragma: allowlist secret
	if err := c.Configure(context.Background()); err != nil {
		t.Fatalf("client configure failed: %v", err)
	}

	r := NewResource()
	configureVolumeResource(t, r, c)
	schemaResp := getVolumeSchema(t)

	ctx := context.Background()
	tfType := schemaResp.Schema.Type().TerraformType(ctx)

	planVal := tftypes.NewValue(tfType, map[string]tftypes.Value{
		"id":          tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"name":        tftypes.NewValue(tftypes.String, "new-volume"),
		"description": tftypes.NewValue(tftypes.String, nil),
		"size_gb":     tftypes.NewValue(tftypes.Number, big.NewFloat(100)),
		"volume_type": tftypes.NewValue(tftypes.String, "ssd"),
		"zone":        tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"snapshot_id": tftypes.NewValue(tftypes.String, nil),
		"encrypted":   tftypes.NewValue(tftypes.Bool, true),
		"tags":        tftypes.NewValue(tftypes.Map{ElementType: tftypes.String}, nil),
		"status":      tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"iops":        tftypes.NewValue(tftypes.Number, tftypes.UnknownValue),
		"throughput":  tftypes.NewValue(tftypes.Number, tftypes.UnknownValue),
		"attached_to": tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"device_path": tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"created_at":  tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
	})

	createReq := resource.CreateRequest{
		Plan: tfsdk.Plan{Schema: schemaResp.Schema, Raw: planVal},
	}
	var createResp resource.CreateResponse
	createResp.State = tfsdk.State{Schema: schemaResp.Schema}

	r.Create(ctx, createReq, &createResp)

	if createResp.Diagnostics.HasError() {
		t.Fatalf("Create failed: %v", createResp.Diagnostics.Errors())
	}

	var model VolumeModel
	createResp.State.Get(ctx, &model)

	if model.ID.ValueString() != "vol-new-1" {
		t.Errorf("expected ID vol-new-1, got %s", model.ID.ValueString())
	}
	if model.Name.ValueString() != "new-volume" {
		t.Errorf("expected Name new-volume, got %s", model.Name.ValueString())
	}
	if model.SizeGB.ValueInt64() != 100 {
		t.Errorf("expected SizeGB 100, got %d", model.SizeGB.ValueInt64())
	}
	if model.Status.ValueString() != "available" {
		t.Errorf("expected Status available, got %s", model.Status.ValueString())
	}
	if model.IOPS.ValueInt64() != 3000 {
		t.Errorf("expected IOPS 3000, got %d", model.IOPS.ValueInt64())
	}
}

func TestVolumeResource_TFSDKRead(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/me":
			_ = json.NewEncoder(w).Encode(map[string]string{"id": "user-123", "tenantId": "tenant-456"})

		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/tenant-456/volumes/vol-read-1":
			_ = json.NewEncoder(w).Encode(apiVolume{
				ID:         "vol-read-1",
				Name:       "read-vol",
				Size:       200,
				VolumeType: "nvme",
				Encrypted:  false,
				Status:     "available",
				IOPS:       5000,
				Throughput: 250,
				Zone:       "falkenberg",
				Metadata:   map[string]string{"team": "backend"},
				CreatedAt:  "2025-02-01T00:00:00Z",
			})

		default:
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"error": map[string]string{"code": "NOT_FOUND", "message": "not found"},
			})
		}
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key") // pragma: allowlist secret
	if err := c.Configure(context.Background()); err != nil {
		t.Fatalf("client configure failed: %v", err)
	}

	r := NewResource()
	configureVolumeResource(t, r, c)
	schemaResp := getVolumeSchema(t)

	ctx := context.Background()
	tfType := schemaResp.Schema.Type().TerraformType(ctx)

	stateVal := tftypes.NewValue(tfType, map[string]tftypes.Value{
		"id":          tftypes.NewValue(tftypes.String, "vol-read-1"),
		"name":        tftypes.NewValue(tftypes.String, "read-vol"),
		"description": tftypes.NewValue(tftypes.String, nil),
		"size_gb":     tftypes.NewValue(tftypes.Number, big.NewFloat(200)),
		"volume_type": tftypes.NewValue(tftypes.String, "nvme"),
		"zone":        tftypes.NewValue(tftypes.String, nil),
		"snapshot_id": tftypes.NewValue(tftypes.String, nil),
		"encrypted":   tftypes.NewValue(tftypes.Bool, false),
		"tags":        tftypes.NewValue(tftypes.Map{ElementType: tftypes.String}, nil),
		"status":      tftypes.NewValue(tftypes.String, "available"),
		"iops":        tftypes.NewValue(tftypes.Number, big.NewFloat(5000)),
		"throughput":  tftypes.NewValue(tftypes.Number, big.NewFloat(250)),
		"attached_to": tftypes.NewValue(tftypes.String, nil),
		"device_path": tftypes.NewValue(tftypes.String, nil),
		"created_at":  tftypes.NewValue(tftypes.String, "2025-02-01T00:00:00Z"),
	})

	readReq := resource.ReadRequest{
		State: tfsdk.State{Schema: schemaResp.Schema, Raw: stateVal},
	}
	var readResp resource.ReadResponse
	readResp.State = tfsdk.State{Schema: schemaResp.Schema}

	r.Read(ctx, readReq, &readResp)

	if readResp.Diagnostics.HasError() {
		t.Fatalf("Read failed: %v", readResp.Diagnostics.Errors())
	}

	var model VolumeModel
	readResp.State.Get(ctx, &model)

	if model.ID.ValueString() != "vol-read-1" {
		t.Errorf("expected ID vol-read-1, got %s", model.ID.ValueString())
	}
	if model.Zone.ValueString() != "falkenberg" {
		t.Errorf("expected Zone falkenberg, got %s", model.Zone.ValueString())
	}
	if model.IOPS.ValueInt64() != 5000 {
		t.Errorf("expected IOPS 5000, got %d", model.IOPS.ValueInt64())
	}
}

func TestVolumeResource_TFSDKReadNotFound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/me":
			_ = json.NewEncoder(w).Encode(map[string]string{"id": "user-123", "tenantId": "tenant-456"})

		default:
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"error": map[string]string{"code": "NOT_FOUND", "message": "not found"},
			})
		}
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key") // pragma: allowlist secret
	if err := c.Configure(context.Background()); err != nil {
		t.Fatalf("client configure failed: %v", err)
	}

	r := NewResource()
	configureVolumeResource(t, r, c)
	schemaResp := getVolumeSchema(t)

	ctx := context.Background()
	tfType := schemaResp.Schema.Type().TerraformType(ctx)

	stateVal := tftypes.NewValue(tfType, map[string]tftypes.Value{
		"id":          tftypes.NewValue(tftypes.String, "vol-gone"),
		"name":        tftypes.NewValue(tftypes.String, "gone-vol"),
		"description": tftypes.NewValue(tftypes.String, nil),
		"size_gb":     tftypes.NewValue(tftypes.Number, big.NewFloat(50)),
		"volume_type": tftypes.NewValue(tftypes.String, "ssd"),
		"zone":        tftypes.NewValue(tftypes.String, nil),
		"snapshot_id": tftypes.NewValue(tftypes.String, nil),
		"encrypted":   tftypes.NewValue(tftypes.Bool, false),
		"tags":        tftypes.NewValue(tftypes.Map{ElementType: tftypes.String}, nil),
		"status":      tftypes.NewValue(tftypes.String, "available"),
		"iops":        tftypes.NewValue(tftypes.Number, big.NewFloat(1000)),
		"throughput":  tftypes.NewValue(tftypes.Number, big.NewFloat(100)),
		"attached_to": tftypes.NewValue(tftypes.String, nil),
		"device_path": tftypes.NewValue(tftypes.String, nil),
		"created_at":  tftypes.NewValue(tftypes.String, "2025-01-01T00:00:00Z"),
	})

	readReq := resource.ReadRequest{
		State: tfsdk.State{Schema: schemaResp.Schema, Raw: stateVal},
	}
	var readResp resource.ReadResponse
	readResp.State = tfsdk.State{Schema: schemaResp.Schema}

	r.Read(ctx, readReq, &readResp)

	// Should not error - just remove the resource from state
	if readResp.Diagnostics.HasError() {
		t.Fatalf("Read should not error for 404, got: %v", readResp.Diagnostics.Errors())
	}
}

func TestVolumeResource_TFSDKUpdate_PatchAndResize(t *testing.T) {
	var patchCalled, resizeCalled bool

	currentVol := apiVolume{
		ID:         "vol-upd-1",
		Name:       "updated-vol",
		Size:       200,
		VolumeType: "ssd",
		Encrypted:  true,
		Status:     "available",
		IOPS:       3000,
		Throughput: 125,
		CreatedAt:  "2025-01-01T00:00:00Z",
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/me":
			_ = json.NewEncoder(w).Encode(map[string]string{"id": "user-123", "tenantId": "tenant-456"})

		case r.Method == http.MethodPatch && r.URL.Path == "/v1/tenants/tenant-456/volumes/vol-upd-1":
			patchCalled = true
			currentVol.Name = "updated-vol"
			currentVol.Description = "new desc"
			_ = json.NewEncoder(w).Encode(currentVol)

		case r.Method == http.MethodPost && r.URL.Path == "/v1/tenants/tenant-456/volumes/vol-upd-1/resize":
			resizeCalled = true
			currentVol.Size = 200
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(map[string]string{
				"operationId": "op-resize-1", "status": "pending", "resourceType": "volume",
			})

		case r.Method == http.MethodGet && r.URL.Path == "/v1/operations/op-resize-1":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"operationId": "op-resize-1", "status": "completed", "resourceType": "volume", "resourceId": "vol-upd-1",
			})

		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/tenant-456/volumes/vol-upd-1":
			_ = json.NewEncoder(w).Encode(currentVol)

		default:
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"error": map[string]string{"code": "NOT_FOUND", "message": "not found"},
			})
		}
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key") // pragma: allowlist secret
	if err := c.Configure(context.Background()); err != nil {
		t.Fatalf("client configure failed: %v", err)
	}

	r := NewResource()
	configureVolumeResource(t, r, c)
	schemaResp := getVolumeSchema(t)

	ctx := context.Background()
	tfType := schemaResp.Schema.Type().TerraformType(ctx)

	stateVal := tftypes.NewValue(tfType, map[string]tftypes.Value{
		"id":          tftypes.NewValue(tftypes.String, "vol-upd-1"),
		"name":        tftypes.NewValue(tftypes.String, "old-vol"),
		"description": tftypes.NewValue(tftypes.String, nil),
		"size_gb":     tftypes.NewValue(tftypes.Number, big.NewFloat(100)),
		"volume_type": tftypes.NewValue(tftypes.String, "ssd"),
		"zone":        tftypes.NewValue(tftypes.String, nil),
		"snapshot_id": tftypes.NewValue(tftypes.String, nil),
		"encrypted":   tftypes.NewValue(tftypes.Bool, true),
		"tags":        tftypes.NewValue(tftypes.Map{ElementType: tftypes.String}, nil),
		"status":      tftypes.NewValue(tftypes.String, "available"),
		"iops":        tftypes.NewValue(tftypes.Number, big.NewFloat(3000)),
		"throughput":  tftypes.NewValue(tftypes.Number, big.NewFloat(125)),
		"attached_to": tftypes.NewValue(tftypes.String, nil),
		"device_path": tftypes.NewValue(tftypes.String, nil),
		"created_at":  tftypes.NewValue(tftypes.String, "2025-01-01T00:00:00Z"),
	})

	planVal := tftypes.NewValue(tfType, map[string]tftypes.Value{
		"id":          tftypes.NewValue(tftypes.String, "vol-upd-1"),
		"name":        tftypes.NewValue(tftypes.String, "updated-vol"),
		"description": tftypes.NewValue(tftypes.String, "new desc"),
		"size_gb":     tftypes.NewValue(tftypes.Number, big.NewFloat(200)),
		"volume_type": tftypes.NewValue(tftypes.String, "ssd"),
		"zone":        tftypes.NewValue(tftypes.String, nil),
		"snapshot_id": tftypes.NewValue(tftypes.String, nil),
		"encrypted":   tftypes.NewValue(tftypes.Bool, true),
		"tags":        tftypes.NewValue(tftypes.Map{ElementType: tftypes.String}, nil),
		"status":      tftypes.NewValue(tftypes.String, "available"),
		"iops":        tftypes.NewValue(tftypes.Number, big.NewFloat(3000)),
		"throughput":  tftypes.NewValue(tftypes.Number, big.NewFloat(125)),
		"attached_to": tftypes.NewValue(tftypes.String, nil),
		"device_path": tftypes.NewValue(tftypes.String, nil),
		"created_at":  tftypes.NewValue(tftypes.String, "2025-01-01T00:00:00Z"),
	})

	updateReq := resource.UpdateRequest{
		Plan:  tfsdk.Plan{Schema: schemaResp.Schema, Raw: planVal},
		State: tfsdk.State{Schema: schemaResp.Schema, Raw: stateVal},
	}
	var updateResp resource.UpdateResponse
	updateResp.State = tfsdk.State{Schema: schemaResp.Schema}

	r.Update(ctx, updateReq, &updateResp)

	if updateResp.Diagnostics.HasError() {
		t.Fatalf("Update failed: %v", updateResp.Diagnostics.Errors())
	}

	if !patchCalled {
		t.Error("expected PATCH to be called for name/description change")
	}
	if !resizeCalled {
		t.Error("expected resize POST to be called for size increase")
	}

	var model VolumeModel
	updateResp.State.Get(ctx, &model)

	if model.Name.ValueString() != "updated-vol" {
		t.Errorf("expected Name updated-vol, got %s", model.Name.ValueString())
	}
	if model.SizeGB.ValueInt64() != 200 {
		t.Errorf("expected SizeGB 200, got %d", model.SizeGB.ValueInt64())
	}
}

func TestVolumeResource_TFSDKDelete(t *testing.T) {
	var deleteCalled bool

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/me":
			_ = json.NewEncoder(w).Encode(map[string]string{"id": "user-123", "tenantId": "tenant-456"})

		case r.Method == http.MethodDelete && r.URL.Path == "/v1/tenants/tenant-456/volumes/vol-del-1":
			deleteCalled = true
			// Provisioning returns 202 + an Operation envelope; the resource polls
			// it to completion before dropping state.
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"operationId": "op-del-1", "status": "pending", "resourceType": "volume",
			})

		case r.Method == http.MethodGet && r.URL.Path == "/v1/operations/op-del-1":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"operationId": "op-del-1", "status": "completed", "resourceType": "volume", "resourceId": "vol-del-1",
			})

		default:
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"error": map[string]string{"code": "NOT_FOUND", "message": "not found"},
			})
		}
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key") // pragma: allowlist secret
	if err := c.Configure(context.Background()); err != nil {
		t.Fatalf("client configure failed: %v", err)
	}

	r := NewResource()
	configureVolumeResource(t, r, c)
	schemaResp := getVolumeSchema(t)

	ctx := context.Background()
	tfType := schemaResp.Schema.Type().TerraformType(ctx)

	stateVal := tftypes.NewValue(tfType, map[string]tftypes.Value{
		"id":          tftypes.NewValue(tftypes.String, "vol-del-1"),
		"name":        tftypes.NewValue(tftypes.String, "del-vol"),
		"description": tftypes.NewValue(tftypes.String, nil),
		"size_gb":     tftypes.NewValue(tftypes.Number, big.NewFloat(50)),
		"volume_type": tftypes.NewValue(tftypes.String, "ssd"),
		"zone":        tftypes.NewValue(tftypes.String, nil),
		"snapshot_id": tftypes.NewValue(tftypes.String, nil),
		"encrypted":   tftypes.NewValue(tftypes.Bool, false),
		"tags":        tftypes.NewValue(tftypes.Map{ElementType: tftypes.String}, nil),
		"status":      tftypes.NewValue(tftypes.String, "available"),
		"iops":        tftypes.NewValue(tftypes.Number, big.NewFloat(1000)),
		"throughput":  tftypes.NewValue(tftypes.Number, big.NewFloat(100)),
		"attached_to": tftypes.NewValue(tftypes.String, nil),
		"device_path": tftypes.NewValue(tftypes.String, nil),
		"created_at":  tftypes.NewValue(tftypes.String, "2025-01-01T00:00:00Z"),
	})

	deleteReq := resource.DeleteRequest{
		State: tfsdk.State{Schema: schemaResp.Schema, Raw: stateVal},
	}
	var deleteResp resource.DeleteResponse
	deleteResp.State = tfsdk.State{Schema: schemaResp.Schema}

	r.Delete(ctx, deleteReq, &deleteResp)

	if deleteResp.Diagnostics.HasError() {
		t.Fatalf("Delete failed: %v", deleteResp.Diagnostics.Errors())
	}

	if !deleteCalled {
		t.Error("expected DELETE to be called")
	}
}

// --- Additional tests for coverage gaps ---

func TestVolumeResource_Metadata(t *testing.T) {
	r := NewResource()
	req := resource.MetadataRequest{ProviderTypeName: "frostmoln"}
	resp := &resource.MetadataResponse{}
	r.Metadata(context.Background(), req, resp)

	if resp.TypeName != "frostmoln_volume" {
		t.Errorf("expected type name frostmoln_volume, got %s", resp.TypeName)
	}
}

func TestVolumeResource_ConfigureNilProviderData(t *testing.T) {
	r := NewResource()
	resp := &resource.ConfigureResponse{}
	r.(resource.ResourceWithConfigure).Configure(context.Background(), resource.ConfigureRequest{ProviderData: nil}, resp)
	if resp.Diagnostics.HasError() {
		t.Errorf("expected no errors, got %v", resp.Diagnostics)
	}
}

func TestVolumeResource_ConfigureWrongType(t *testing.T) {
	r := NewResource()
	resp := &resource.ConfigureResponse{}
	r.(resource.ResourceWithConfigure).Configure(context.Background(), resource.ConfigureRequest{ProviderData: "bad"}, resp)
	if !resp.Diagnostics.HasError() {
		t.Error("expected error for wrong type")
	}
}

func TestVolumeResource_TFSDKCreateAPIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/me":
			_ = json.NewEncoder(w).Encode(map[string]string{"id": "user-123", "tenantId": "tenant-456"})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/tenants/tenant-456/volumes":
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"error": map[string]string{"code": "INTERNAL_ERROR", "message": "server error"},
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key") // pragma: allowlist secret
	if err := c.Configure(context.Background()); err != nil {
		t.Fatalf("client configure failed: %v", err)
	}

	r := NewResource()
	configureVolumeResource(t, r, c)
	schemaResp := getVolumeSchema(t)

	ctx := context.Background()
	tfType := schemaResp.Schema.Type().TerraformType(ctx)

	planVal := tftypes.NewValue(tfType, map[string]tftypes.Value{
		"id":          tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"name":        tftypes.NewValue(tftypes.String, "fail-vol"),
		"description": tftypes.NewValue(tftypes.String, nil),
		"size_gb":     tftypes.NewValue(tftypes.Number, big.NewFloat(100)),
		"volume_type": tftypes.NewValue(tftypes.String, "ssd"),
		"zone":        tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"snapshot_id": tftypes.NewValue(tftypes.String, nil),
		"encrypted":   tftypes.NewValue(tftypes.Bool, false),
		"tags":        tftypes.NewValue(tftypes.Map{ElementType: tftypes.String}, nil),
		"status":      tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"iops":        tftypes.NewValue(tftypes.Number, tftypes.UnknownValue),
		"throughput":  tftypes.NewValue(tftypes.Number, tftypes.UnknownValue),
		"attached_to": tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"device_path": tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"created_at":  tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
	})

	createReq := resource.CreateRequest{
		Plan: tfsdk.Plan{Schema: schemaResp.Schema, Raw: planVal},
	}
	var createResp resource.CreateResponse
	createResp.State = tfsdk.State{Schema: schemaResp.Schema}

	r.Create(ctx, createReq, &createResp)

	if !createResp.Diagnostics.HasError() {
		t.Error("expected error for API failure on create")
	}
}

func TestVolumeResource_TFSDKCreateBadResponseBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/me":
			_ = json.NewEncoder(w).Encode(map[string]string{"id": "user-123", "tenantId": "tenant-456"})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/tenants/tenant-456/volumes":
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte("not json"))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key") // pragma: allowlist secret
	if err := c.Configure(context.Background()); err != nil {
		t.Fatalf("client configure failed: %v", err)
	}

	r := NewResource()
	configureVolumeResource(t, r, c)
	schemaResp := getVolumeSchema(t)

	ctx := context.Background()
	tfType := schemaResp.Schema.Type().TerraformType(ctx)

	planVal := tftypes.NewValue(tfType, map[string]tftypes.Value{
		"id":          tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"name":        tftypes.NewValue(tftypes.String, "bad-vol"),
		"description": tftypes.NewValue(tftypes.String, nil),
		"size_gb":     tftypes.NewValue(tftypes.Number, big.NewFloat(50)),
		"volume_type": tftypes.NewValue(tftypes.String, nil),
		"zone":        tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"snapshot_id": tftypes.NewValue(tftypes.String, nil),
		"encrypted":   tftypes.NewValue(tftypes.Bool, false),
		"tags":        tftypes.NewValue(tftypes.Map{ElementType: tftypes.String}, nil),
		"status":      tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"iops":        tftypes.NewValue(tftypes.Number, tftypes.UnknownValue),
		"throughput":  tftypes.NewValue(tftypes.Number, tftypes.UnknownValue),
		"attached_to": tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"device_path": tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"created_at":  tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
	})

	createReq := resource.CreateRequest{
		Plan: tfsdk.Plan{Schema: schemaResp.Schema, Raw: planVal},
	}
	var createResp resource.CreateResponse
	createResp.State = tfsdk.State{Schema: schemaResp.Schema}

	r.Create(ctx, createReq, &createResp)

	if !createResp.Diagnostics.HasError() {
		t.Error("expected error for bad response body on create")
	}
}

func TestVolumeResource_TFSDKCreatePollingErrorState(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/me":
			_ = json.NewEncoder(w).Encode(map[string]string{"id": "user-123", "tenantId": "tenant-456"})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/tenants/tenant-456/volumes":
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"operationId": "op-err-1", "status": "pending", "resourceType": "volume",
			})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/operations/op-err-1":
			// The create workflow failed → operation terminal-failed → the resource
			// must surface an error (was: volume polled to "error" state).
			_ = json.NewEncoder(w).Encode(map[string]any{
				"operationId": "op-err-1", "status": "failed", "resourceType": "volume",
				"error": "volume entered error state",
			})
		default:
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"error": map[string]string{"code": "NOT_FOUND", "message": "not found"},
			})
		}
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key") // pragma: allowlist secret
	if err := c.Configure(context.Background()); err != nil {
		t.Fatalf("client configure failed: %v", err)
	}

	r := NewResource()
	configureVolumeResource(t, r, c)
	schemaResp := getVolumeSchema(t)

	ctx := context.Background()
	tfType := schemaResp.Schema.Type().TerraformType(ctx)

	planVal := tftypes.NewValue(tfType, map[string]tftypes.Value{
		"id":          tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"name":        tftypes.NewValue(tftypes.String, "error-vol"),
		"description": tftypes.NewValue(tftypes.String, nil),
		"size_gb":     tftypes.NewValue(tftypes.Number, big.NewFloat(100)),
		"volume_type": tftypes.NewValue(tftypes.String, nil),
		"zone":        tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"snapshot_id": tftypes.NewValue(tftypes.String, nil),
		"encrypted":   tftypes.NewValue(tftypes.Bool, false),
		"tags":        tftypes.NewValue(tftypes.Map{ElementType: tftypes.String}, nil),
		"status":      tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"iops":        tftypes.NewValue(tftypes.Number, tftypes.UnknownValue),
		"throughput":  tftypes.NewValue(tftypes.Number, tftypes.UnknownValue),
		"attached_to": tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"device_path": tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"created_at":  tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
	})

	createReq := resource.CreateRequest{
		Plan: tfsdk.Plan{Schema: schemaResp.Schema, Raw: planVal},
	}
	var createResp resource.CreateResponse
	createResp.State = tfsdk.State{Schema: schemaResp.Schema}

	r.Create(ctx, createReq, &createResp)

	if !createResp.Diagnostics.HasError() {
		t.Error("expected error when volume enters error state during polling")
	}
}

func TestVolumeResource_TFSDKCreateFinalReadError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/me":
			_ = json.NewEncoder(w).Encode(map[string]string{"id": "user-123", "tenantId": "tenant-456"})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/tenants/tenant-456/volumes":
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"operationId": "op-fre-1", "status": "pending", "resourceType": "volume",
			})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/operations/op-fre-1":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"operationId": "op-fre-1", "status": "completed", "resourceType": "volume", "resourceId": "vol-fre-1",
			})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/tenant-456/volumes/vol-fre-1":
			// Final read after the operation completes returns an error.
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"error": map[string]string{"code": "INTERNAL_ERROR", "message": "read failed"},
			})
		default:
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"error": map[string]string{"code": "NOT_FOUND", "message": "not found"},
			})
		}
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key") // pragma: allowlist secret
	if err := c.Configure(context.Background()); err != nil {
		t.Fatalf("client configure failed: %v", err)
	}

	r := NewResource()
	configureVolumeResource(t, r, c)
	schemaResp := getVolumeSchema(t)

	ctx := context.Background()
	tfType := schemaResp.Schema.Type().TerraformType(ctx)

	planVal := tftypes.NewValue(tfType, map[string]tftypes.Value{
		"id":          tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"name":        tftypes.NewValue(tftypes.String, "fre-vol"),
		"description": tftypes.NewValue(tftypes.String, nil),
		"size_gb":     tftypes.NewValue(tftypes.Number, big.NewFloat(100)),
		"volume_type": tftypes.NewValue(tftypes.String, nil),
		"zone":        tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"snapshot_id": tftypes.NewValue(tftypes.String, nil),
		"encrypted":   tftypes.NewValue(tftypes.Bool, false),
		"tags":        tftypes.NewValue(tftypes.Map{ElementType: tftypes.String}, nil),
		"status":      tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"iops":        tftypes.NewValue(tftypes.Number, tftypes.UnknownValue),
		"throughput":  tftypes.NewValue(tftypes.Number, tftypes.UnknownValue),
		"attached_to": tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"device_path": tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"created_at":  tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
	})

	createReq := resource.CreateRequest{
		Plan: tfsdk.Plan{Schema: schemaResp.Schema, Raw: planVal},
	}
	var createResp resource.CreateResponse
	createResp.State = tfsdk.State{Schema: schemaResp.Schema}

	r.Create(ctx, createReq, &createResp)

	if !createResp.Diagnostics.HasError() {
		t.Error("expected error when final read fails after creation")
	}
}

func TestVolumeResource_TFSDKReadAPIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/me":
			_ = json.NewEncoder(w).Encode(map[string]string{"id": "user-123", "tenantId": "tenant-456"})
		default:
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"error": map[string]string{"code": "INTERNAL_ERROR", "message": "server error"},
			})
		}
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key") // pragma: allowlist secret
	if err := c.Configure(context.Background()); err != nil {
		t.Fatalf("client configure failed: %v", err)
	}

	r := NewResource()
	configureVolumeResource(t, r, c)
	schemaResp := getVolumeSchema(t)

	ctx := context.Background()
	tfType := schemaResp.Schema.Type().TerraformType(ctx)

	stateVal := tftypes.NewValue(tfType, map[string]tftypes.Value{
		"id":          tftypes.NewValue(tftypes.String, "vol-err-r"),
		"name":        tftypes.NewValue(tftypes.String, "err-vol"),
		"description": tftypes.NewValue(tftypes.String, nil),
		"size_gb":     tftypes.NewValue(tftypes.Number, big.NewFloat(50)),
		"volume_type": tftypes.NewValue(tftypes.String, "ssd"),
		"zone":        tftypes.NewValue(tftypes.String, nil),
		"snapshot_id": tftypes.NewValue(tftypes.String, nil),
		"encrypted":   tftypes.NewValue(tftypes.Bool, false),
		"tags":        tftypes.NewValue(tftypes.Map{ElementType: tftypes.String}, nil),
		"status":      tftypes.NewValue(tftypes.String, "available"),
		"iops":        tftypes.NewValue(tftypes.Number, big.NewFloat(1000)),
		"throughput":  tftypes.NewValue(tftypes.Number, big.NewFloat(100)),
		"attached_to": tftypes.NewValue(tftypes.String, nil),
		"device_path": tftypes.NewValue(tftypes.String, nil),
		"created_at":  tftypes.NewValue(tftypes.String, "2025-01-01T00:00:00Z"),
	})

	readReq := resource.ReadRequest{
		State: tfsdk.State{Schema: schemaResp.Schema, Raw: stateVal},
	}
	var readResp resource.ReadResponse
	readResp.State = tfsdk.State{Schema: schemaResp.Schema}

	r.Read(ctx, readReq, &readResp)

	if !readResp.Diagnostics.HasError() {
		t.Error("expected error for API failure on read")
	}
}

func TestVolumeResource_TFSDKReadBadJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/me":
			_ = json.NewEncoder(w).Encode(map[string]string{"id": "user-123", "tenantId": "tenant-456"})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/tenant-456/volumes/vol-bj-r":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("not json"))
		default:
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"error": map[string]string{"code": "NOT_FOUND", "message": "not found"},
			})
		}
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key") // pragma: allowlist secret
	if err := c.Configure(context.Background()); err != nil {
		t.Fatalf("client configure failed: %v", err)
	}

	r := NewResource()
	configureVolumeResource(t, r, c)
	schemaResp := getVolumeSchema(t)

	ctx := context.Background()
	tfType := schemaResp.Schema.Type().TerraformType(ctx)

	stateVal := tftypes.NewValue(tfType, map[string]tftypes.Value{
		"id":          tftypes.NewValue(tftypes.String, "vol-bj-r"),
		"name":        tftypes.NewValue(tftypes.String, "bj-vol"),
		"description": tftypes.NewValue(tftypes.String, nil),
		"size_gb":     tftypes.NewValue(tftypes.Number, big.NewFloat(50)),
		"volume_type": tftypes.NewValue(tftypes.String, "ssd"),
		"zone":        tftypes.NewValue(tftypes.String, nil),
		"snapshot_id": tftypes.NewValue(tftypes.String, nil),
		"encrypted":   tftypes.NewValue(tftypes.Bool, false),
		"tags":        tftypes.NewValue(tftypes.Map{ElementType: tftypes.String}, nil),
		"status":      tftypes.NewValue(tftypes.String, "available"),
		"iops":        tftypes.NewValue(tftypes.Number, big.NewFloat(1000)),
		"throughput":  tftypes.NewValue(tftypes.Number, big.NewFloat(100)),
		"attached_to": tftypes.NewValue(tftypes.String, nil),
		"device_path": tftypes.NewValue(tftypes.String, nil),
		"created_at":  tftypes.NewValue(tftypes.String, "2025-01-01T00:00:00Z"),
	})

	readReq := resource.ReadRequest{
		State: tfsdk.State{Schema: schemaResp.Schema, Raw: stateVal},
	}
	var readResp resource.ReadResponse
	readResp.State = tfsdk.State{Schema: schemaResp.Schema}

	r.Read(ctx, readReq, &readResp)

	if !readResp.Diagnostics.HasError() {
		t.Error("expected error for bad JSON in read response")
	}
}

func TestVolumeResource_TFSDKUpdatePatchError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/me":
			_ = json.NewEncoder(w).Encode(map[string]string{"id": "user-123", "tenantId": "tenant-456"})
		case r.Method == http.MethodPatch && r.URL.Path == "/v1/tenants/tenant-456/volumes/vol-pe-1":
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"error": map[string]string{"code": "INTERNAL_ERROR", "message": "patch failed"},
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key") // pragma: allowlist secret
	if err := c.Configure(context.Background()); err != nil {
		t.Fatalf("client configure failed: %v", err)
	}

	r := NewResource()
	configureVolumeResource(t, r, c)
	schemaResp := getVolumeSchema(t)

	ctx := context.Background()
	tfType := schemaResp.Schema.Type().TerraformType(ctx)

	stateVal := tftypes.NewValue(tfType, map[string]tftypes.Value{
		"id":          tftypes.NewValue(tftypes.String, "vol-pe-1"),
		"name":        tftypes.NewValue(tftypes.String, "old-name"),
		"description": tftypes.NewValue(tftypes.String, nil),
		"size_gb":     tftypes.NewValue(tftypes.Number, big.NewFloat(100)),
		"volume_type": tftypes.NewValue(tftypes.String, "ssd"),
		"zone":        tftypes.NewValue(tftypes.String, nil),
		"snapshot_id": tftypes.NewValue(tftypes.String, nil),
		"encrypted":   tftypes.NewValue(tftypes.Bool, true),
		"tags":        tftypes.NewValue(tftypes.Map{ElementType: tftypes.String}, nil),
		"status":      tftypes.NewValue(tftypes.String, "available"),
		"iops":        tftypes.NewValue(tftypes.Number, big.NewFloat(3000)),
		"throughput":  tftypes.NewValue(tftypes.Number, big.NewFloat(125)),
		"attached_to": tftypes.NewValue(tftypes.String, nil),
		"device_path": tftypes.NewValue(tftypes.String, nil),
		"created_at":  tftypes.NewValue(tftypes.String, "2025-01-01T00:00:00Z"),
	})

	planVal := tftypes.NewValue(tfType, map[string]tftypes.Value{
		"id":          tftypes.NewValue(tftypes.String, "vol-pe-1"),
		"name":        tftypes.NewValue(tftypes.String, "new-name"),
		"description": tftypes.NewValue(tftypes.String, nil),
		"size_gb":     tftypes.NewValue(tftypes.Number, big.NewFloat(100)),
		"volume_type": tftypes.NewValue(tftypes.String, "ssd"),
		"zone":        tftypes.NewValue(tftypes.String, nil),
		"snapshot_id": tftypes.NewValue(tftypes.String, nil),
		"encrypted":   tftypes.NewValue(tftypes.Bool, true),
		"tags":        tftypes.NewValue(tftypes.Map{ElementType: tftypes.String}, nil),
		"status":      tftypes.NewValue(tftypes.String, "available"),
		"iops":        tftypes.NewValue(tftypes.Number, big.NewFloat(3000)),
		"throughput":  tftypes.NewValue(tftypes.Number, big.NewFloat(125)),
		"attached_to": tftypes.NewValue(tftypes.String, nil),
		"device_path": tftypes.NewValue(tftypes.String, nil),
		"created_at":  tftypes.NewValue(tftypes.String, "2025-01-01T00:00:00Z"),
	})

	updateReq := resource.UpdateRequest{
		Plan:  tfsdk.Plan{Schema: schemaResp.Schema, Raw: planVal},
		State: tfsdk.State{Schema: schemaResp.Schema, Raw: stateVal},
	}
	var updateResp resource.UpdateResponse
	updateResp.State = tfsdk.State{Schema: schemaResp.Schema}

	r.Update(ctx, updateReq, &updateResp)

	if !updateResp.Diagnostics.HasError() {
		t.Error("expected error for PATCH failure")
	}
}

func TestVolumeResource_TFSDKUpdateResizeError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/me":
			_ = json.NewEncoder(w).Encode(map[string]string{"id": "user-123", "tenantId": "tenant-456"})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/tenants/tenant-456/volumes/vol-re-1/resize":
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"error": map[string]string{"code": "INTERNAL_ERROR", "message": "resize failed"},
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key") // pragma: allowlist secret
	if err := c.Configure(context.Background()); err != nil {
		t.Fatalf("client configure failed: %v", err)
	}

	r := NewResource()
	configureVolumeResource(t, r, c)
	schemaResp := getVolumeSchema(t)

	ctx := context.Background()
	tfType := schemaResp.Schema.Type().TerraformType(ctx)

	stateVal := tftypes.NewValue(tfType, map[string]tftypes.Value{
		"id":          tftypes.NewValue(tftypes.String, "vol-re-1"),
		"name":        tftypes.NewValue(tftypes.String, "same-name"),
		"description": tftypes.NewValue(tftypes.String, nil),
		"size_gb":     tftypes.NewValue(tftypes.Number, big.NewFloat(100)),
		"volume_type": tftypes.NewValue(tftypes.String, "ssd"),
		"zone":        tftypes.NewValue(tftypes.String, nil),
		"snapshot_id": tftypes.NewValue(tftypes.String, nil),
		"encrypted":   tftypes.NewValue(tftypes.Bool, true),
		"tags":        tftypes.NewValue(tftypes.Map{ElementType: tftypes.String}, nil),
		"status":      tftypes.NewValue(tftypes.String, "available"),
		"iops":        tftypes.NewValue(tftypes.Number, big.NewFloat(3000)),
		"throughput":  tftypes.NewValue(tftypes.Number, big.NewFloat(125)),
		"attached_to": tftypes.NewValue(tftypes.String, nil),
		"device_path": tftypes.NewValue(tftypes.String, nil),
		"created_at":  tftypes.NewValue(tftypes.String, "2025-01-01T00:00:00Z"),
	})

	planVal := tftypes.NewValue(tfType, map[string]tftypes.Value{
		"id":          tftypes.NewValue(tftypes.String, "vol-re-1"),
		"name":        tftypes.NewValue(tftypes.String, "same-name"),
		"description": tftypes.NewValue(tftypes.String, nil),
		"size_gb":     tftypes.NewValue(tftypes.Number, big.NewFloat(200)),
		"volume_type": tftypes.NewValue(tftypes.String, "ssd"),
		"zone":        tftypes.NewValue(tftypes.String, nil),
		"snapshot_id": tftypes.NewValue(tftypes.String, nil),
		"encrypted":   tftypes.NewValue(tftypes.Bool, true),
		"tags":        tftypes.NewValue(tftypes.Map{ElementType: tftypes.String}, nil),
		"status":      tftypes.NewValue(tftypes.String, "available"),
		"iops":        tftypes.NewValue(tftypes.Number, big.NewFloat(3000)),
		"throughput":  tftypes.NewValue(tftypes.Number, big.NewFloat(125)),
		"attached_to": tftypes.NewValue(tftypes.String, nil),
		"device_path": tftypes.NewValue(tftypes.String, nil),
		"created_at":  tftypes.NewValue(tftypes.String, "2025-01-01T00:00:00Z"),
	})

	updateReq := resource.UpdateRequest{
		Plan:  tfsdk.Plan{Schema: schemaResp.Schema, Raw: planVal},
		State: tfsdk.State{Schema: schemaResp.Schema, Raw: stateVal},
	}
	var updateResp resource.UpdateResponse
	updateResp.State = tfsdk.State{Schema: schemaResp.Schema}

	r.Update(ctx, updateReq, &updateResp)

	if !updateResp.Diagnostics.HasError() {
		t.Error("expected error for resize failure")
	}
}

func TestVolumeResource_TFSDKUpdateReadError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/me":
			_ = json.NewEncoder(w).Encode(map[string]string{"id": "user-123", "tenantId": "tenant-456"})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/tenant-456/volumes/vol-ure-1":
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"error": map[string]string{"code": "INTERNAL_ERROR", "message": "read failed"},
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key") // pragma: allowlist secret
	if err := c.Configure(context.Background()); err != nil {
		t.Fatalf("client configure failed: %v", err)
	}

	r := NewResource()
	configureVolumeResource(t, r, c)
	schemaResp := getVolumeSchema(t)

	ctx := context.Background()
	tfType := schemaResp.Schema.Type().TerraformType(ctx)

	// No changes - just the final read will fail
	stateVal := tftypes.NewValue(tfType, map[string]tftypes.Value{
		"id":          tftypes.NewValue(tftypes.String, "vol-ure-1"),
		"name":        tftypes.NewValue(tftypes.String, "same"),
		"description": tftypes.NewValue(tftypes.String, nil),
		"size_gb":     tftypes.NewValue(tftypes.Number, big.NewFloat(100)),
		"volume_type": tftypes.NewValue(tftypes.String, "ssd"),
		"zone":        tftypes.NewValue(tftypes.String, nil),
		"snapshot_id": tftypes.NewValue(tftypes.String, nil),
		"encrypted":   tftypes.NewValue(tftypes.Bool, true),
		"tags":        tftypes.NewValue(tftypes.Map{ElementType: tftypes.String}, nil),
		"status":      tftypes.NewValue(tftypes.String, "available"),
		"iops":        tftypes.NewValue(tftypes.Number, big.NewFloat(3000)),
		"throughput":  tftypes.NewValue(tftypes.Number, big.NewFloat(125)),
		"attached_to": tftypes.NewValue(tftypes.String, nil),
		"device_path": tftypes.NewValue(tftypes.String, nil),
		"created_at":  tftypes.NewValue(tftypes.String, "2025-01-01T00:00:00Z"),
	})

	updateReq := resource.UpdateRequest{
		Plan:  tfsdk.Plan{Schema: schemaResp.Schema, Raw: stateVal},
		State: tfsdk.State{Schema: schemaResp.Schema, Raw: stateVal},
	}
	var updateResp resource.UpdateResponse
	updateResp.State = tfsdk.State{Schema: schemaResp.Schema}

	r.Update(ctx, updateReq, &updateResp)

	if !updateResp.Diagnostics.HasError() {
		t.Error("expected error for read failure during update")
	}
}

func TestVolumeResource_TFSDKDeleteNotFound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/me":
			_ = json.NewEncoder(w).Encode(map[string]string{"id": "user-123", "tenantId": "tenant-456"})
		default:
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"error": map[string]string{"code": "NOT_FOUND", "message": "not found"},
			})
		}
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key") // pragma: allowlist secret
	if err := c.Configure(context.Background()); err != nil {
		t.Fatalf("client configure failed: %v", err)
	}

	r := NewResource()
	configureVolumeResource(t, r, c)
	schemaResp := getVolumeSchema(t)

	ctx := context.Background()
	tfType := schemaResp.Schema.Type().TerraformType(ctx)

	stateVal := tftypes.NewValue(tfType, map[string]tftypes.Value{
		"id":          tftypes.NewValue(tftypes.String, "vol-gone"),
		"name":        tftypes.NewValue(tftypes.String, "gone-vol"),
		"description": tftypes.NewValue(tftypes.String, nil),
		"size_gb":     tftypes.NewValue(tftypes.Number, big.NewFloat(50)),
		"volume_type": tftypes.NewValue(tftypes.String, "ssd"),
		"zone":        tftypes.NewValue(tftypes.String, nil),
		"snapshot_id": tftypes.NewValue(tftypes.String, nil),
		"encrypted":   tftypes.NewValue(tftypes.Bool, false),
		"tags":        tftypes.NewValue(tftypes.Map{ElementType: tftypes.String}, nil),
		"status":      tftypes.NewValue(tftypes.String, "available"),
		"iops":        tftypes.NewValue(tftypes.Number, big.NewFloat(1000)),
		"throughput":  tftypes.NewValue(tftypes.Number, big.NewFloat(100)),
		"attached_to": tftypes.NewValue(tftypes.String, nil),
		"device_path": tftypes.NewValue(tftypes.String, nil),
		"created_at":  tftypes.NewValue(tftypes.String, "2025-01-01T00:00:00Z"),
	})

	deleteReq := resource.DeleteRequest{
		State: tfsdk.State{Schema: schemaResp.Schema, Raw: stateVal},
	}
	var deleteResp resource.DeleteResponse

	r.Delete(ctx, deleteReq, &deleteResp)

	if deleteResp.Diagnostics.HasError() {
		t.Fatalf("Delete should not error for already-gone volume, got: %v", deleteResp.Diagnostics.Errors())
	}
}

func TestVolumeResource_TFSDKDeleteAPIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/me":
			_ = json.NewEncoder(w).Encode(map[string]string{"id": "user-123", "tenantId": "tenant-456"})
		default:
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"error": map[string]string{"code": "INTERNAL_ERROR", "message": "server error"},
			})
		}
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key") // pragma: allowlist secret
	if err := c.Configure(context.Background()); err != nil {
		t.Fatalf("client configure failed: %v", err)
	}

	r := NewResource()
	configureVolumeResource(t, r, c)
	schemaResp := getVolumeSchema(t)

	ctx := context.Background()
	tfType := schemaResp.Schema.Type().TerraformType(ctx)

	stateVal := tftypes.NewValue(tfType, map[string]tftypes.Value{
		"id":          tftypes.NewValue(tftypes.String, "vol-del-err"),
		"name":        tftypes.NewValue(tftypes.String, "err-vol"),
		"description": tftypes.NewValue(tftypes.String, nil),
		"size_gb":     tftypes.NewValue(tftypes.Number, big.NewFloat(50)),
		"volume_type": tftypes.NewValue(tftypes.String, "ssd"),
		"zone":        tftypes.NewValue(tftypes.String, nil),
		"snapshot_id": tftypes.NewValue(tftypes.String, nil),
		"encrypted":   tftypes.NewValue(tftypes.Bool, false),
		"tags":        tftypes.NewValue(tftypes.Map{ElementType: tftypes.String}, nil),
		"status":      tftypes.NewValue(tftypes.String, "available"),
		"iops":        tftypes.NewValue(tftypes.Number, big.NewFloat(1000)),
		"throughput":  tftypes.NewValue(tftypes.Number, big.NewFloat(100)),
		"attached_to": tftypes.NewValue(tftypes.String, nil),
		"device_path": tftypes.NewValue(tftypes.String, nil),
		"created_at":  tftypes.NewValue(tftypes.String, "2025-01-01T00:00:00Z"),
	})

	deleteReq := resource.DeleteRequest{
		State: tfsdk.State{Schema: schemaResp.Schema, Raw: stateVal},
	}
	var deleteResp resource.DeleteResponse

	r.Delete(ctx, deleteReq, &deleteResp)

	if !deleteResp.Diagnostics.HasError() {
		t.Error("expected error for API failure on delete")
	}
}

func TestVolumeResource_TFSDKImportState(t *testing.T) {
	r := NewResource()
	schemaResp := getVolumeSchema(t)

	ctx := context.Background()
	tfType := schemaResp.Schema.Type().TerraformType(ctx)

	emptyState := tftypes.NewValue(tfType, map[string]tftypes.Value{
		"id":          tftypes.NewValue(tftypes.String, nil),
		"name":        tftypes.NewValue(tftypes.String, nil),
		"description": tftypes.NewValue(tftypes.String, nil),
		"size_gb":     tftypes.NewValue(tftypes.Number, nil),
		"volume_type": tftypes.NewValue(tftypes.String, nil),
		"zone":        tftypes.NewValue(tftypes.String, nil),
		"snapshot_id": tftypes.NewValue(tftypes.String, nil),
		"encrypted":   tftypes.NewValue(tftypes.Bool, nil),
		"tags":        tftypes.NewValue(tftypes.Map{ElementType: tftypes.String}, nil),
		"status":      tftypes.NewValue(tftypes.String, nil),
		"iops":        tftypes.NewValue(tftypes.Number, nil),
		"throughput":  tftypes.NewValue(tftypes.Number, nil),
		"attached_to": tftypes.NewValue(tftypes.String, nil),
		"device_path": tftypes.NewValue(tftypes.String, nil),
		"created_at":  tftypes.NewValue(tftypes.String, nil),
	})

	importReq := resource.ImportStateRequest{ID: "vol-import-1"}
	importResp := &resource.ImportStateResponse{
		State: tfsdk.State{Schema: schemaResp.Schema, Raw: emptyState},
	}

	r.(resource.ResourceWithImportState).ImportState(ctx, importReq, importResp)

	if importResp.Diagnostics.HasError() {
		t.Fatalf("ImportState failed: %v", importResp.Diagnostics.Errors())
	}

	var model VolumeModel
	importResp.State.Get(ctx, &model)

	if model.ID.ValueString() != "vol-import-1" {
		t.Errorf("expected ID vol-import-1, got %s", model.ID.ValueString())
	}
}

// TestVolumeModelFromAPIFiltersReservedTags asserts the backend-stamped reserved
// metadata (bare *-id keys + the frostmoln_* namespace) never surfaces as a
// customer tag, mirroring the instance filter. Without it a null/unset tags plan
// reads back the system keys → "inconsistent result after apply".
func TestVolumeModelFromAPIFiltersReservedTags(t *testing.T) {
	ctx := context.Background()

	// Case 1: a null tags plan stays null even though the backend stamps the bare
	// *-id keys and frostmoln_type onto every volume at create.
	t.Run("system metadata only keeps tags null", func(t *testing.T) {
		diags := diag.Diagnostics{}
		vol := &apiVolume{
			ID:     "vol-1",
			Name:   "data",
			Status: "available",
			Metadata: map[string]string{
				"customer-id":    "c1",
				"request-id":     "r1",
				"project-id":     "p1",
				"frostmoln_type": "managed",
			},
		}
		model := VolumeModel{Tags: types.MapNull(types.StringType)}
		model.fromAPI(ctx, vol, &diags)
		if diags.HasError() {
			t.Fatalf("unexpected diagnostics: %v", diags.Errors())
		}
		if !model.Tags.IsNull() {
			t.Errorf("expected tags to stay null when only reserved metadata present, got %v", model.Tags)
		}
	})

	// A non-null empty-map plan stays an empty map (the middle branch).
	t.Run("empty-map plan stays empty map", func(t *testing.T) {
		diags := diag.Diagnostics{}
		emptyTags, d := types.MapValueFrom(ctx, types.StringType, map[string]string{})
		diags.Append(d...)
		vol := &apiVolume{
			ID:       "vol-3",
			Name:     "data",
			Status:   "available",
			Metadata: map[string]string{"customer-id": "c1", "project-id": "p1"},
		}
		model := VolumeModel{Tags: emptyTags}
		model.fromAPI(ctx, vol, &diags)
		if diags.HasError() {
			t.Fatalf("unexpected diagnostics: %v", diags.Errors())
		}
		if model.Tags.IsNull() {
			t.Error("expected tags to stay an empty map, got null")
		}
		var gotTags map[string]string
		diags.Append(model.Tags.ElementsAs(ctx, &gotTags, false)...)
		if len(gotTags) != 0 {
			t.Errorf("expected empty tags map, got %v", gotTags)
		}
	})

	// Case 2: user tags round-trip; only the reserved keys are dropped.
	t.Run("user tags survive, reserved keys dropped", func(t *testing.T) {
		diags := diag.Diagnostics{}
		priorTags, d := types.MapValueFrom(ctx, types.StringType, map[string]string{"env": "prod"})
		diags.Append(d...)
		vol := &apiVolume{
			ID:     "vol-2",
			Name:   "data",
			Status: "available",
			Metadata: map[string]string{
				"env":         "prod",
				"customer-id": "c1",
				"request-id":  "r1",
				"project-id":  "p1",
			},
		}
		model := VolumeModel{Tags: priorTags}
		model.fromAPI(ctx, vol, &diags)
		if diags.HasError() {
			t.Fatalf("unexpected diagnostics: %v", diags.Errors())
		}
		var gotTags map[string]string
		diags.Append(model.Tags.ElementsAs(ctx, &gotTags, false)...)
		if len(gotTags) != 1 || gotTags["env"] != "prod" {
			t.Errorf("expected tags {env: prod} with reserved keys filtered, got %v", gotTags)
		}
	})
}

// TestVolumeModelFromAPIPreservesNullDescription guards bug (A): the backend
// stamps a provisioning default description on every provisioned volume. A null
// description plan MUST read back null (Optional-only attr), else apply fails with
// "inconsistent result after apply: .description was null, but now <default>".
func TestVolumeModelFromAPIPreservesNullDescription(t *testing.T) {
	ctx := context.Background()
	diags := diag.Diagnostics{}

	apiVol := &apiVolume{
		ID:          "vol-1",
		Name:        "data",
		Status:      "available",
		Description: "Created by provisioning for customer 94981d9c-8d35-4fa2-9704-2bc34cca0836",
	}
	model := &VolumeModel{Description: types.StringNull()}
	model.fromAPI(ctx, apiVol, &diags)
	if diags.HasError() {
		t.Fatalf("unexpected diagnostics: %v", diags.Errors())
	}
	if !model.Description.IsNull() {
		t.Errorf("expected description to stay null despite backend provisioning default, got %q", model.Description.ValueString())
	}
}
