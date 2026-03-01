package volume_attachment

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/types"

	"git.nl.cloud/NordicLight/terraform-provider-frostmoln/internal/client"
)

func TestCompositeID(t *testing.T) {
	id := compositeID("vol-123", "inst-456")
	if id != "vol-123/inst-456" {
		t.Errorf("expected vol-123/inst-456, got %s", id)
	}
}

func TestVolumeAttachmentModel_toAttachRequest_withDevicePath(t *testing.T) {
	model := &VolumeAttachmentModel{
		InstanceID: types.StringValue("inst-456"),
		DevicePath: types.StringValue("/dev/vdb"),
	}

	req := model.toAttachRequest()
	if req.InstanceID != "inst-456" {
		t.Errorf("expected instanceId inst-456, got %s", req.InstanceID)
	}
	if req.DevicePath != "/dev/vdb" {
		t.Errorf("expected devicePath /dev/vdb, got %s", req.DevicePath)
	}
}

func TestVolumeAttachmentModel_toAttachRequest_withoutDevicePath(t *testing.T) {
	model := &VolumeAttachmentModel{
		InstanceID: types.StringValue("inst-789"),
		DevicePath: types.StringNull(),
	}

	req := model.toAttachRequest()
	if req.InstanceID != "inst-789" {
		t.Errorf("expected instanceId inst-789, got %s", req.InstanceID)
	}
	if req.DevicePath != "" {
		t.Errorf("expected empty devicePath, got %s", req.DevicePath)
	}
}

func TestVolumeAttachmentModel_fromAPI(t *testing.T) {
	vol := &apiVolume{
		ID:         "vol-123",
		AttachedTo: "inst-456",
		DevicePath: "/dev/vdb",
		Status:     "in-use",
	}

	model := &VolumeAttachmentModel{}
	model.fromAPI(vol)

	if model.ID.ValueString() != "vol-123/inst-456" {
		t.Errorf("expected ID vol-123/inst-456, got %s", model.ID.ValueString())
	}
	if model.VolumeID.ValueString() != "vol-123" {
		t.Errorf("expected volume_id vol-123, got %s", model.VolumeID.ValueString())
	}
	if model.InstanceID.ValueString() != "inst-456" {
		t.Errorf("expected instance_id inst-456, got %s", model.InstanceID.ValueString())
	}
	if model.DevicePath.ValueString() != "/dev/vdb" {
		t.Errorf("expected device_path /dev/vdb, got %s", model.DevicePath.ValueString())
	}
}

func TestVolumeAttachmentModel_fromAPI_noDevicePath(t *testing.T) {
	vol := &apiVolume{
		ID:         "vol-123",
		AttachedTo: "inst-456",
		Status:     "in-use",
	}

	model := &VolumeAttachmentModel{}
	model.fromAPI(vol)

	if !model.DevicePath.IsNull() {
		t.Errorf("expected device_path to be null, got %s", model.DevicePath.ValueString())
	}
}

func TestVolumeAttachmentResource_Attach(t *testing.T) {
	var attachCalled atomic.Int32

	volume := apiVolume{
		ID:         "vol-123",
		Status:     "available",
		AttachedTo: "",
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/me":
			json.NewEncoder(w).Encode(client.UserProfile{
				ID: "user-1", TenantID: "tenant-1",
			})

		case r.Method == http.MethodPost && r.URL.Path == "/v1/tenants/tenant-1/volumes/vol-123/attach":
			attachCalled.Add(1)
			var req apiAttachRequest
			json.NewDecoder(r.Body).Decode(&req)
			volume.AttachedTo = req.InstanceID
			volume.DevicePath = "/dev/vdb"
			volume.Status = "in-use"
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(volume)

		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/tenant-1/volumes/vol-123":
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

	ctx := context.Background()

	// Attach
	attachReq := apiAttachRequest{InstanceID: "inst-456", DevicePath: "/dev/vdb"}
	_, err := c.Post(ctx, c.TenantPath("/volumes/vol-123/attach"), attachReq)
	if err != nil {
		t.Fatalf("attach failed: %v", err)
	}
	if attachCalled.Load() != 1 {
		t.Errorf("expected attach called once, got %d", attachCalled.Load())
	}

	// Verify state
	getResp, err := c.Get(ctx, c.TenantPath("/volumes/vol-123"), nil)
	if err != nil {
		t.Fatalf("read failed: %v", err)
	}
	vol, err := client.ParseResponse[apiVolume](getResp)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if vol.AttachedTo != "inst-456" {
		t.Errorf("expected attachedTo inst-456, got %s", vol.AttachedTo)
	}
	if vol.Status != "in-use" {
		t.Errorf("expected status in-use, got %s", vol.Status)
	}
}

func TestVolumeAttachmentResource_Detach(t *testing.T) {
	var detachCalled atomic.Int32

	volume := apiVolume{
		ID:         "vol-123",
		Status:     "in-use",
		AttachedTo: "inst-456",
		DevicePath: "/dev/vdb",
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/me":
			json.NewEncoder(w).Encode(client.UserProfile{
				ID: "user-1", TenantID: "tenant-1",
			})

		case r.Method == http.MethodPost && r.URL.Path == "/v1/tenants/tenant-1/volumes/vol-123/detach":
			detachCalled.Add(1)
			volume.AttachedTo = ""
			volume.DevicePath = ""
			volume.Status = "available"
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(volume)

		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/tenant-1/volumes/vol-123":
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

	ctx := context.Background()

	// Detach
	detachReq := apiDetachRequest{Force: false}
	_, err := c.Post(ctx, c.TenantPath("/volumes/vol-123/detach"), detachReq)
	if err != nil {
		t.Fatalf("detach failed: %v", err)
	}
	if detachCalled.Load() != 1 {
		t.Errorf("expected detach called once, got %d", detachCalled.Load())
	}

	// Verify state
	getResp, err := c.Get(ctx, c.TenantPath("/volumes/vol-123"), nil)
	if err != nil {
		t.Fatalf("read failed: %v", err)
	}
	vol, err := client.ParseResponse[apiVolume](getResp)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if vol.AttachedTo != "" {
		t.Errorf("expected empty attachedTo, got %s", vol.AttachedTo)
	}
	if vol.Status != "available" {
		t.Errorf("expected status available, got %s", vol.Status)
	}
}

func TestVolumeAttachmentResource_ReadNotAttached(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/me":
			json.NewEncoder(w).Encode(client.UserProfile{
				ID: "user-1", TenantID: "tenant-1",
			})

		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/tenant-1/volumes/vol-123":
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(apiVolume{
				ID:     "vol-123",
				Status: "available",
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

	getResp, err := c.Get(context.Background(), c.TenantPath("/volumes/vol-123"), nil)
	if err != nil {
		t.Fatalf("read failed: %v", err)
	}
	vol, err := client.ParseResponse[apiVolume](getResp)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	// Volume exists but is not attached - the resource would be removed.
	if vol.AttachedTo != "" {
		t.Errorf("expected empty attachedTo, got %s", vol.AttachedTo)
	}
}

func TestImportIDParsing(t *testing.T) {
	tests := []struct {
		name     string
		id       string
		wantVol  string
		wantInst string
		wantErr  bool
	}{
		{
			name:     "valid composite ID",
			id:       "vol-123/inst-456",
			wantVol:  "vol-123",
			wantInst: "inst-456",
		},
		{
			name:    "missing slash",
			id:      "invalid-no-slash",
			wantErr: true,
		},
		{
			name:    "empty volume ID",
			id:      "/inst-456",
			wantErr: true,
		},
		{
			name:    "empty instance ID",
			id:      "vol-123/",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			parts := strings.SplitN(tt.id, "/", 2)
			if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
				if !tt.wantErr {
					t.Errorf("unexpected parse failure for ID %s", tt.id)
				}
				return
			}
			if tt.wantErr {
				t.Errorf("expected parse failure for ID %s", tt.id)
				return
			}
			if parts[0] != tt.wantVol {
				t.Errorf("expected volume_id %s, got %s", tt.wantVol, parts[0])
			}
			if parts[1] != tt.wantInst {
				t.Errorf("expected instance_id %s, got %s", tt.wantInst, parts[1])
			}
		})
	}
}
