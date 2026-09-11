package volume_attachment

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
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
	if req.Device != "/dev/vdb" {
		t.Errorf("expected devicePath /dev/vdb, got %s", req.Device)
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
	if req.Device != "" {
		t.Errorf("expected empty devicePath, got %s", req.Device)
	}
}

func TestVolumeAttachmentModel_fromAttachment(t *testing.T) {
	att := &apiVolumeAttachment{
		ID:         "att-1",
		VolumeID:   "vol-123",
		InstanceID: "inst-456",
		Device:     "/dev/vdb",
	}

	model := &VolumeAttachmentModel{}
	model.fromAttachment("vol-123", att)

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

func TestVolumeAttachmentModel_fromAttachment_noDevice(t *testing.T) {
	att := &apiVolumeAttachment{
		ID:         "att-1",
		VolumeID:   "vol-123",
		InstanceID: "inst-456",
	}

	model := &VolumeAttachmentModel{}
	model.fromAttachment("vol-123", att)

	if !model.DevicePath.IsNull() {
		t.Errorf("expected device_path to be null, got %s", model.DevicePath.ValueString())
	}
}

func TestVolumeAttachmentResource_Attach(t *testing.T) {
	var attachCalled atomic.Int32

	volume := apiVolume{
		ID:     "vol-123",
		Status: "available",
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/me":
			_ = json.NewEncoder(w).Encode(client.UserProfile{
				ID: "user-1", TenantID: "tenant-1",
			})

		case r.Method == http.MethodPost && r.URL.Path == "/v1/tenants/tenant-1/volumes/vol-123/attach":
			attachCalled.Add(1)
			var req apiAttachRequest
			_ = json.NewDecoder(r.Body).Decode(&req)
			volume.Attachments = []apiVolumeAttachment{{VolumeID: volume.ID, InstanceID: req.InstanceID, Device: "/dev/vdb"}}
			volume.Status = "in-use"
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(map[string]string{
				"operationId": "op-attach-1", "status": "accepted", "resourceType": "volume",
			})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/tenant-1/operations/op-attach-1":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"operationId": "op-attach-1", "status": "completed", "resourceType": "volume",
			})

		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/tenant-1/volumes/vol-123":
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(volume)

		default:
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]string{"code": "NOT_FOUND", "message": "not found"})
		}
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key", client.WithHTTPClient(server.Client()))
	if err := c.Configure(context.Background()); err != nil {
		t.Fatalf("configure failed: %v", err)
	}

	ctx := context.Background()

	// Attach
	attachReq := apiAttachRequest{InstanceID: "inst-456", Device: "/dev/vdb"}
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
	if att := vol.findAttachment("inst-456"); att == nil {
		t.Errorf("expected volume attached to inst-456, got attachments %+v", vol.Attachments)
	}
	if vol.Status != "in-use" {
		t.Errorf("expected status in-use, got %s", vol.Status)
	}
}

func TestVolumeAttachmentResource_Detach(t *testing.T) {
	var detachCalled atomic.Int32

	volume := apiVolume{
		ID:          "vol-123",
		Status:      "in-use",
		Attachments: []apiVolumeAttachment{{InstanceID: "inst-456", Device: "/dev/vdb"}},
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/me":
			_ = json.NewEncoder(w).Encode(client.UserProfile{
				ID: "user-1", TenantID: "tenant-1",
			})

		case r.Method == http.MethodPost && r.URL.Path == "/v1/tenants/tenant-1/volumes/vol-123/detach":
			detachCalled.Add(1)
			volume.Attachments = nil
			volume.Status = "available"
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(map[string]string{
				"operationId": "op-detach-1", "status": "accepted", "resourceType": "volume",
			})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/tenant-1/operations/op-detach-1":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"operationId": "op-detach-1", "status": "completed", "resourceType": "volume",
			})

		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/tenant-1/volumes/vol-123":
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(volume)

		default:
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]string{"code": "NOT_FOUND", "message": "not found"})
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
	if len(vol.Attachments) != 0 {
		t.Errorf("expected no attachments, got %+v", vol.Attachments)
	}
	if vol.Status != "available" {
		t.Errorf("expected status available, got %s", vol.Status)
	}
}

func TestVolumeAttachmentResource_ReadNotAttached(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/me":
			_ = json.NewEncoder(w).Encode(client.UserProfile{
				ID: "user-1", TenantID: "tenant-1",
			})

		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/tenant-1/volumes/vol-123":
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(apiVolume{
				ID:     "vol-123",
				Status: "available",
			})

		default:
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]string{"code": "NOT_FOUND", "message": "not found"})
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
	if len(vol.Attachments) != 0 {
		t.Errorf("expected no attachments, got %+v", vol.Attachments)
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

// --- tfsdk-level CRUD tests ---

func getVASchema(t *testing.T) resource.SchemaResponse {
	t.Helper()
	r := NewResource()
	var schemaResp resource.SchemaResponse
	r.Schema(context.Background(), resource.SchemaRequest{}, &schemaResp)
	return schemaResp
}

func configureVAResource(t *testing.T, r resource.Resource, c *client.Client) {
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

func TestVolumeAttachment_TFSDKCreate(t *testing.T) {
	volume := apiVolume{
		ID:     "vol-att-1",
		Status: "available",
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/me":
			_ = json.NewEncoder(w).Encode(map[string]string{"id": "user-123", "tenantId": "tenant-456"})

		case r.Method == http.MethodPost && r.URL.Path == "/v1/tenants/tenant-456/volumes/vol-att-1/attach":
			var req apiAttachRequest
			_ = json.NewDecoder(r.Body).Decode(&req)
			volume.Attachments = []apiVolumeAttachment{{VolumeID: volume.ID, InstanceID: req.InstanceID, Device: "/dev/vdb"}}
			volume.Status = "in-use"
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(map[string]string{
				"operationId": "op-attach-1", "status": "accepted", "resourceType": "volume",
			})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/tenant-456/operations/op-attach-1":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"operationId": "op-attach-1", "status": "completed", "resourceType": "volume",
			})

		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/tenant-456/volumes/vol-att-1":
			_ = json.NewEncoder(w).Encode(volume)

		default:
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]string{"code": "NOT_FOUND", "message": "not found"})
		}
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key") // pragma: allowlist secret
	if err := c.Configure(context.Background()); err != nil {
		t.Fatalf("client configure failed: %v", err)
	}

	r := NewResource()
	configureVAResource(t, r, c)
	schemaResp := getVASchema(t)

	ctx := context.Background()
	tfType := schemaResp.Schema.Type().TerraformType(ctx)

	planVal := tftypes.NewValue(tfType, map[string]tftypes.Value{
		"id":          tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"volume_id":   tftypes.NewValue(tftypes.String, "vol-att-1"),
		"instance_id": tftypes.NewValue(tftypes.String, "inst-att-1"),
		"device_path": tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"timeouts":    tftypes.NewValue(tfType.(tftypes.Object).AttributeTypes["timeouts"], nil),
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

	var model VolumeAttachmentModel
	createResp.State.Get(ctx, &model)

	if model.ID.ValueString() != "vol-att-1/inst-att-1" {
		t.Errorf("expected ID vol-att-1/inst-att-1, got %s", model.ID.ValueString())
	}
	if model.VolumeID.ValueString() != "vol-att-1" {
		t.Errorf("expected VolumeID vol-att-1, got %s", model.VolumeID.ValueString())
	}
	if model.InstanceID.ValueString() != "inst-att-1" {
		t.Errorf("expected InstanceID inst-att-1, got %s", model.InstanceID.ValueString())
	}
	if model.DevicePath.ValueString() != "/dev/vdb" {
		t.Errorf("expected DevicePath /dev/vdb, got %s", model.DevicePath.ValueString())
	}
}

func TestVolumeAttachment_TFSDKRead(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/me":
			_ = json.NewEncoder(w).Encode(map[string]string{"id": "user-123", "tenantId": "tenant-456"})

		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/tenant-456/volumes/vol-r-1":
			_ = json.NewEncoder(w).Encode(apiVolume{
				ID:          "vol-r-1",
				Attachments: []apiVolumeAttachment{{InstanceID: "inst-r-1", Device: "/dev/vdc"}},
				Status:      "in-use",
			})

		default:
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]string{"code": "NOT_FOUND", "message": "not found"})
		}
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key") // pragma: allowlist secret
	if err := c.Configure(context.Background()); err != nil {
		t.Fatalf("client configure failed: %v", err)
	}

	r := NewResource()
	configureVAResource(t, r, c)
	schemaResp := getVASchema(t)

	ctx := context.Background()
	tfType := schemaResp.Schema.Type().TerraformType(ctx)

	stateVal := tftypes.NewValue(tfType, map[string]tftypes.Value{
		"id":          tftypes.NewValue(tftypes.String, "vol-r-1/inst-r-1"),
		"volume_id":   tftypes.NewValue(tftypes.String, "vol-r-1"),
		"instance_id": tftypes.NewValue(tftypes.String, "inst-r-1"),
		"device_path": tftypes.NewValue(tftypes.String, "/dev/vdc"),
		"timeouts":    tftypes.NewValue(tfType.(tftypes.Object).AttributeTypes["timeouts"], nil),
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

	var model VolumeAttachmentModel
	readResp.State.Get(ctx, &model)

	if model.VolumeID.ValueString() != "vol-r-1" {
		t.Errorf("expected VolumeID vol-r-1, got %s", model.VolumeID.ValueString())
	}
	if model.DevicePath.ValueString() != "/dev/vdc" {
		t.Errorf("expected DevicePath /dev/vdc, got %s", model.DevicePath.ValueString())
	}
}

func TestVolumeAttachment_TFSDKReadDetached(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/me":
			_ = json.NewEncoder(w).Encode(map[string]string{"id": "user-123", "tenantId": "tenant-456"})

		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/tenant-456/volumes/vol-det-1":
			// Volume exists but attached to a different instance
			_ = json.NewEncoder(w).Encode(apiVolume{
				ID:          "vol-det-1",
				Attachments: []apiVolumeAttachment{{InstanceID: "inst-other"}},
				Status:      "in-use",
			})

		default:
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]string{"code": "NOT_FOUND", "message": "not found"})
		}
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key") // pragma: allowlist secret
	if err := c.Configure(context.Background()); err != nil {
		t.Fatalf("client configure failed: %v", err)
	}

	r := NewResource()
	configureVAResource(t, r, c)
	schemaResp := getVASchema(t)

	ctx := context.Background()
	tfType := schemaResp.Schema.Type().TerraformType(ctx)

	stateVal := tftypes.NewValue(tfType, map[string]tftypes.Value{
		"id":          tftypes.NewValue(tftypes.String, "vol-det-1/inst-expected"),
		"volume_id":   tftypes.NewValue(tftypes.String, "vol-det-1"),
		"instance_id": tftypes.NewValue(tftypes.String, "inst-expected"),
		"device_path": tftypes.NewValue(tftypes.String, "/dev/vdb"),
		"timeouts":    tftypes.NewValue(tfType.(tftypes.Object).AttributeTypes["timeouts"], nil),
	})

	readReq := resource.ReadRequest{
		State: tfsdk.State{Schema: schemaResp.Schema, Raw: stateVal},
	}
	var readResp resource.ReadResponse
	readResp.State = tfsdk.State{Schema: schemaResp.Schema}

	r.Read(ctx, readReq, &readResp)

	// Should not error - resource should be removed from state
	if readResp.Diagnostics.HasError() {
		t.Fatalf("Read should not error for detached volume, got: %v", readResp.Diagnostics.Errors())
	}
}

func TestVolumeAttachment_TFSDKDelete(t *testing.T) {
	var detachCalled bool

	volume := apiVolume{
		ID:          "vol-d-1",
		Attachments: []apiVolumeAttachment{{InstanceID: "inst-d-1", Device: "/dev/vdb"}},
		Status:      "in-use",
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/me":
			_ = json.NewEncoder(w).Encode(map[string]string{"id": "user-123", "tenantId": "tenant-456"})

		case r.Method == http.MethodPost && r.URL.Path == "/v1/tenants/tenant-456/volumes/vol-d-1/detach":
			detachCalled = true
			volume.Attachments = nil
			volume.Status = "available"
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(map[string]string{
				"operationId": "op-detach-1", "status": "accepted", "resourceType": "volume",
			})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/tenant-456/operations/op-detach-1":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"operationId": "op-detach-1", "status": "completed", "resourceType": "volume",
			})

		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/tenant-456/volumes/vol-d-1":
			_ = json.NewEncoder(w).Encode(volume)

		default:
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]string{"code": "NOT_FOUND", "message": "not found"})
		}
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key") // pragma: allowlist secret
	if err := c.Configure(context.Background()); err != nil {
		t.Fatalf("client configure failed: %v", err)
	}

	r := NewResource()
	configureVAResource(t, r, c)
	schemaResp := getVASchema(t)

	ctx := context.Background()
	tfType := schemaResp.Schema.Type().TerraformType(ctx)

	stateVal := tftypes.NewValue(tfType, map[string]tftypes.Value{
		"id":          tftypes.NewValue(tftypes.String, "vol-d-1/inst-d-1"),
		"volume_id":   tftypes.NewValue(tftypes.String, "vol-d-1"),
		"instance_id": tftypes.NewValue(tftypes.String, "inst-d-1"),
		"device_path": tftypes.NewValue(tftypes.String, "/dev/vdb"),
		"timeouts":    tftypes.NewValue(tfType.(tftypes.Object).AttributeTypes["timeouts"], nil),
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

	if !detachCalled {
		t.Error("expected detach POST to be called")
	}
}

func TestVolumeAttachment_TFSDKImportState(t *testing.T) {
	r := NewResource()
	schemaResp := getVASchema(t)

	ctx := context.Background()

	tests := []struct {
		name     string
		id       string
		wantErr  bool
		wantVol  string
		wantInst string
	}{
		{
			name:     "valid composite ID",
			id:       "vol-imp-1/inst-imp-1",
			wantVol:  "vol-imp-1",
			wantInst: "inst-imp-1",
		},
		{
			name:    "missing separator",
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
			importReq := resource.ImportStateRequest{ID: tt.id}
			var importResp resource.ImportStateResponse
			tfType := schemaResp.Schema.Type().TerraformType(ctx)
			emptyState := tftypes.NewValue(tfType, map[string]tftypes.Value{
				"id":          tftypes.NewValue(tftypes.String, nil),
				"volume_id":   tftypes.NewValue(tftypes.String, nil),
				"instance_id": tftypes.NewValue(tftypes.String, nil),
				"device_path": tftypes.NewValue(tftypes.String, nil),
				"timeouts":    tftypes.NewValue(tfType.(tftypes.Object).AttributeTypes["timeouts"], nil),
			})
			importResp.State = tfsdk.State{Schema: schemaResp.Schema, Raw: emptyState}

			r.(resource.ResourceWithImportState).ImportState(ctx, importReq, &importResp)

			if tt.wantErr {
				if !importResp.Diagnostics.HasError() {
					t.Error("expected error for invalid import ID")
				}
				return
			}

			if importResp.Diagnostics.HasError() {
				t.Fatalf("ImportState failed: %v", importResp.Diagnostics.Errors())
			}

			var model VolumeAttachmentModel
			importResp.State.Get(ctx, &model)

			if model.VolumeID.ValueString() != tt.wantVol {
				t.Errorf("expected volume_id %s, got %s", tt.wantVol, model.VolumeID.ValueString())
			}
			if model.InstanceID.ValueString() != tt.wantInst {
				t.Errorf("expected instance_id %s, got %s", tt.wantInst, model.InstanceID.ValueString())
			}
		})
	}
}

func TestVolumeAttachment_TFSDKUpdateErrors(t *testing.T) {
	r := NewResource()

	var updateResp resource.UpdateResponse
	r.Update(context.Background(), resource.UpdateRequest{}, &updateResp)

	if !updateResp.Diagnostics.HasError() {
		t.Error("expected error for update - volume attachments cannot be updated")
	}
}

// --- Additional tests for coverage gaps ---

func TestVolumeAttachment_Metadata(t *testing.T) {
	r := NewResource()
	req := resource.MetadataRequest{ProviderTypeName: "frostmoln"}
	resp := &resource.MetadataResponse{}
	r.Metadata(context.Background(), req, resp)

	if resp.TypeName != "frostmoln_volume_attachment" {
		t.Errorf("expected type name frostmoln_volume_attachment, got %s", resp.TypeName)
	}
}

func TestVolumeAttachment_ConfigureNilProviderData(t *testing.T) {
	r := NewResource()
	resp := &resource.ConfigureResponse{}
	r.(resource.ResourceWithConfigure).Configure(context.Background(), resource.ConfigureRequest{ProviderData: nil}, resp)
	if resp.Diagnostics.HasError() {
		t.Errorf("expected no errors, got %v", resp.Diagnostics)
	}
}

func TestVolumeAttachment_ConfigureWrongType(t *testing.T) {
	r := NewResource()
	resp := &resource.ConfigureResponse{}
	r.(resource.ResourceWithConfigure).Configure(context.Background(), resource.ConfigureRequest{ProviderData: "bad"}, resp)
	if !resp.Diagnostics.HasError() {
		t.Error("expected error for wrong type")
	}
}

func TestVolumeAttachment_TFSDKCreateAttachError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/me":
			_ = json.NewEncoder(w).Encode(map[string]string{"id": "user-123", "tenantId": "tenant-456"})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/tenants/tenant-456/volumes/vol-ae-1/attach":
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"error": map[string]string{"code": "INTERNAL_ERROR", "message": "attach failed"},
			})
		default:
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]string{"code": "NOT_FOUND", "message": "not found"})
		}
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key") // pragma: allowlist secret
	if err := c.Configure(context.Background()); err != nil {
		t.Fatalf("client configure failed: %v", err)
	}

	r := NewResource()
	configureVAResource(t, r, c)
	schemaResp := getVASchema(t)

	ctx := context.Background()
	tfType := schemaResp.Schema.Type().TerraformType(ctx)

	planVal := tftypes.NewValue(tfType, map[string]tftypes.Value{
		"id":          tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"volume_id":   tftypes.NewValue(tftypes.String, "vol-ae-1"),
		"instance_id": tftypes.NewValue(tftypes.String, "inst-ae-1"),
		"device_path": tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"timeouts":    tftypes.NewValue(tfType.(tftypes.Object).AttributeTypes["timeouts"], nil),
	})

	createReq := resource.CreateRequest{
		Plan: tfsdk.Plan{Schema: schemaResp.Schema, Raw: planVal},
	}
	var createResp resource.CreateResponse
	createResp.State = tfsdk.State{Schema: schemaResp.Schema}

	r.Create(ctx, createReq, &createResp)

	if !createResp.Diagnostics.HasError() {
		t.Error("expected error for attach API failure")
	}
}

func TestVolumeAttachment_TFSDKCreatePollErrorState(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/me":
			_ = json.NewEncoder(w).Encode(map[string]string{"id": "user-123", "tenantId": "tenant-456"})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/tenants/tenant-456/volumes/vol-pe-1/attach":
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(map[string]string{
				"operationId": "op-attach-pe-1", "status": "accepted", "resourceType": "volume",
			})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/tenant-456/operations/op-attach-pe-1":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"operationId": "op-attach-pe-1", "status": "completed", "resourceType": "volume",
			})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/tenant-456/volumes/vol-pe-1":
			_ = json.NewEncoder(w).Encode(apiVolume{ID: "vol-pe-1", Status: "error"})
		default:
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]string{"code": "NOT_FOUND", "message": "not found"})
		}
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key") // pragma: allowlist secret
	if err := c.Configure(context.Background()); err != nil {
		t.Fatalf("client configure failed: %v", err)
	}

	r := NewResource()
	configureVAResource(t, r, c)
	schemaResp := getVASchema(t)

	ctx := context.Background()
	tfType := schemaResp.Schema.Type().TerraformType(ctx)

	planVal := tftypes.NewValue(tfType, map[string]tftypes.Value{
		"id":          tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"volume_id":   tftypes.NewValue(tftypes.String, "vol-pe-1"),
		"instance_id": tftypes.NewValue(tftypes.String, "inst-pe-1"),
		"device_path": tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"timeouts":    tftypes.NewValue(tfType.(tftypes.Object).AttributeTypes["timeouts"], nil),
	})

	createReq := resource.CreateRequest{
		Plan: tfsdk.Plan{Schema: schemaResp.Schema, Raw: planVal},
	}
	var createResp resource.CreateResponse
	createResp.State = tfsdk.State{Schema: schemaResp.Schema}

	r.Create(ctx, createReq, &createResp)

	if !createResp.Diagnostics.HasError() {
		t.Error("expected error when volume enters error state during attachment polling")
	}
}

func TestVolumeAttachment_TFSDKCreateFinalReadError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/me":
			_ = json.NewEncoder(w).Encode(map[string]string{"id": "user-123", "tenantId": "tenant-456"})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/tenants/tenant-456/volumes/vol-fre-1/attach":
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(map[string]string{
				"operationId": "op-attach-fre-1", "status": "accepted", "resourceType": "volume",
			})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/tenant-456/operations/op-attach-fre-1":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"operationId": "op-attach-fre-1", "status": "completed", "resourceType": "volume",
			})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/tenant-456/volumes/vol-fre-1":
			// The single verify read after the completed attach operation fails.
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"error": map[string]string{"code": "INTERNAL_ERROR", "message": "read failed"},
			})
		default:
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]string{"code": "NOT_FOUND", "message": "not found"})
		}
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key") // pragma: allowlist secret
	if err := c.Configure(context.Background()); err != nil {
		t.Fatalf("client configure failed: %v", err)
	}

	r := NewResource()
	configureVAResource(t, r, c)
	schemaResp := getVASchema(t)

	ctx := context.Background()
	tfType := schemaResp.Schema.Type().TerraformType(ctx)

	planVal := tftypes.NewValue(tfType, map[string]tftypes.Value{
		"id":          tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"volume_id":   tftypes.NewValue(tftypes.String, "vol-fre-1"),
		"instance_id": tftypes.NewValue(tftypes.String, "inst-fre-1"),
		"device_path": tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"timeouts":    tftypes.NewValue(tfType.(tftypes.Object).AttributeTypes["timeouts"], nil),
	})

	createReq := resource.CreateRequest{
		Plan: tfsdk.Plan{Schema: schemaResp.Schema, Raw: planVal},
	}
	var createResp resource.CreateResponse
	createResp.State = tfsdk.State{Schema: schemaResp.Schema}

	r.Create(ctx, createReq, &createResp)

	if !createResp.Diagnostics.HasError() {
		t.Error("expected error when final read fails after attach")
	}
}

func TestVolumeAttachment_TFSDKCreateInstanceMismatch(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/me":
			_ = json.NewEncoder(w).Encode(map[string]string{"id": "user-123", "tenantId": "tenant-456"})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/tenants/tenant-456/volumes/vol-mm-1/attach":
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(map[string]string{
				"operationId": "op-attach-mm-1", "status": "accepted", "resourceType": "volume",
			})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/tenant-456/operations/op-attach-mm-1":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"operationId": "op-attach-mm-1", "status": "completed", "resourceType": "volume",
			})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/tenant-456/volumes/vol-mm-1":
			// Return in-use but attached to a DIFFERENT instance
			_ = json.NewEncoder(w).Encode(apiVolume{
				ID:          "vol-mm-1",
				Status:      "in-use",
				Attachments: []apiVolumeAttachment{{InstanceID: "inst-other", Device: "/dev/vdb"}},
			})
		default:
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]string{"code": "NOT_FOUND", "message": "not found"})
		}
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key") // pragma: allowlist secret
	if err := c.Configure(context.Background()); err != nil {
		t.Fatalf("client configure failed: %v", err)
	}

	r := NewResource()
	configureVAResource(t, r, c)
	schemaResp := getVASchema(t)

	ctx := context.Background()
	tfType := schemaResp.Schema.Type().TerraformType(ctx)

	planVal := tftypes.NewValue(tfType, map[string]tftypes.Value{
		"id":          tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"volume_id":   tftypes.NewValue(tftypes.String, "vol-mm-1"),
		"instance_id": tftypes.NewValue(tftypes.String, "inst-expected"),
		"device_path": tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"timeouts":    tftypes.NewValue(tfType.(tftypes.Object).AttributeTypes["timeouts"], nil),
	})

	createReq := resource.CreateRequest{
		Plan: tfsdk.Plan{Schema: schemaResp.Schema, Raw: planVal},
	}
	var createResp resource.CreateResponse
	createResp.State = tfsdk.State{Schema: schemaResp.Schema}

	r.Create(ctx, createReq, &createResp)

	if !createResp.Diagnostics.HasError() {
		t.Error("expected error for instance mismatch after attach")
	}
}

func TestVolumeAttachment_TFSDKReadNotFound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/me":
			_ = json.NewEncoder(w).Encode(map[string]string{"id": "user-123", "tenantId": "tenant-456"})
		default:
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]string{"code": "NOT_FOUND", "message": "not found"})
		}
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key") // pragma: allowlist secret
	if err := c.Configure(context.Background()); err != nil {
		t.Fatalf("client configure failed: %v", err)
	}

	r := NewResource()
	configureVAResource(t, r, c)
	schemaResp := getVASchema(t)

	ctx := context.Background()
	tfType := schemaResp.Schema.Type().TerraformType(ctx)

	stateVal := tftypes.NewValue(tfType, map[string]tftypes.Value{
		"id":          tftypes.NewValue(tftypes.String, "vol-nf-1/inst-nf-1"),
		"volume_id":   tftypes.NewValue(tftypes.String, "vol-nf-1"),
		"instance_id": tftypes.NewValue(tftypes.String, "inst-nf-1"),
		"device_path": tftypes.NewValue(tftypes.String, "/dev/vdb"),
		"timeouts":    tftypes.NewValue(tfType.(tftypes.Object).AttributeTypes["timeouts"], nil),
	})

	readReq := resource.ReadRequest{
		State: tfsdk.State{Schema: schemaResp.Schema, Raw: stateVal},
	}
	var readResp resource.ReadResponse
	readResp.State = tfsdk.State{Schema: schemaResp.Schema}

	r.Read(ctx, readReq, &readResp)

	// Should not error - resource should be removed from state (volume not found)
	if readResp.Diagnostics.HasError() {
		t.Fatalf("Read should not error for 404 volume, got: %v", readResp.Diagnostics.Errors())
	}
}

func TestVolumeAttachment_TFSDKReadAPIError(t *testing.T) {
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
	configureVAResource(t, r, c)
	schemaResp := getVASchema(t)

	ctx := context.Background()
	tfType := schemaResp.Schema.Type().TerraformType(ctx)

	stateVal := tftypes.NewValue(tfType, map[string]tftypes.Value{
		"id":          tftypes.NewValue(tftypes.String, "vol-err-1/inst-err-1"),
		"volume_id":   tftypes.NewValue(tftypes.String, "vol-err-1"),
		"instance_id": tftypes.NewValue(tftypes.String, "inst-err-1"),
		"device_path": tftypes.NewValue(tftypes.String, "/dev/vdb"),
		"timeouts":    tftypes.NewValue(tfType.(tftypes.Object).AttributeTypes["timeouts"], nil),
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

func TestVolumeAttachment_TFSDKReadBadJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/me":
			_ = json.NewEncoder(w).Encode(map[string]string{"id": "user-123", "tenantId": "tenant-456"})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/tenant-456/volumes/vol-bj-1":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("not json"))
		default:
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]string{"code": "NOT_FOUND", "message": "not found"})
		}
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key") // pragma: allowlist secret
	if err := c.Configure(context.Background()); err != nil {
		t.Fatalf("client configure failed: %v", err)
	}

	r := NewResource()
	configureVAResource(t, r, c)
	schemaResp := getVASchema(t)

	ctx := context.Background()
	tfType := schemaResp.Schema.Type().TerraformType(ctx)

	stateVal := tftypes.NewValue(tfType, map[string]tftypes.Value{
		"id":          tftypes.NewValue(tftypes.String, "vol-bj-1/inst-bj-1"),
		"volume_id":   tftypes.NewValue(tftypes.String, "vol-bj-1"),
		"instance_id": tftypes.NewValue(tftypes.String, "inst-bj-1"),
		"device_path": tftypes.NewValue(tftypes.String, "/dev/vdb"),
		"timeouts":    tftypes.NewValue(tfType.(tftypes.Object).AttributeTypes["timeouts"], nil),
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

func TestVolumeAttachment_TFSDKDeleteDetachError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/me":
			_ = json.NewEncoder(w).Encode(map[string]string{"id": "user-123", "tenantId": "tenant-456"})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/tenants/tenant-456/volumes/vol-de-1/detach":
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"error": map[string]string{"code": "INTERNAL_ERROR", "message": "detach failed"},
			})
		default:
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]string{"code": "NOT_FOUND", "message": "not found"})
		}
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key") // pragma: allowlist secret
	if err := c.Configure(context.Background()); err != nil {
		t.Fatalf("client configure failed: %v", err)
	}

	r := NewResource()
	configureVAResource(t, r, c)
	schemaResp := getVASchema(t)

	ctx := context.Background()
	tfType := schemaResp.Schema.Type().TerraformType(ctx)

	stateVal := tftypes.NewValue(tfType, map[string]tftypes.Value{
		"id":          tftypes.NewValue(tftypes.String, "vol-de-1/inst-de-1"),
		"volume_id":   tftypes.NewValue(tftypes.String, "vol-de-1"),
		"instance_id": tftypes.NewValue(tftypes.String, "inst-de-1"),
		"device_path": tftypes.NewValue(tftypes.String, "/dev/vdb"),
		"timeouts":    tftypes.NewValue(tfType.(tftypes.Object).AttributeTypes["timeouts"], nil),
	})

	deleteReq := resource.DeleteRequest{
		State: tfsdk.State{Schema: schemaResp.Schema, Raw: stateVal},
	}
	var deleteResp resource.DeleteResponse
	deleteResp.State = tfsdk.State{Schema: schemaResp.Schema}

	r.Delete(ctx, deleteReq, &deleteResp)

	if !deleteResp.Diagnostics.HasError() {
		t.Error("expected error for detach API failure")
	}
}

func TestVolumeAttachment_TFSDKDeleteNotFound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/me":
			_ = json.NewEncoder(w).Encode(map[string]string{"id": "user-123", "tenantId": "tenant-456"})
		default:
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]string{"code": "NOT_FOUND", "message": "not found"})
		}
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key") // pragma: allowlist secret
	if err := c.Configure(context.Background()); err != nil {
		t.Fatalf("client configure failed: %v", err)
	}

	r := NewResource()
	configureVAResource(t, r, c)
	schemaResp := getVASchema(t)

	ctx := context.Background()
	tfType := schemaResp.Schema.Type().TerraformType(ctx)

	stateVal := tftypes.NewValue(tfType, map[string]tftypes.Value{
		"id":          tftypes.NewValue(tftypes.String, "vol-gone/inst-gone"),
		"volume_id":   tftypes.NewValue(tftypes.String, "vol-gone"),
		"instance_id": tftypes.NewValue(tftypes.String, "inst-gone"),
		"device_path": tftypes.NewValue(tftypes.String, "/dev/vdb"),
		"timeouts":    tftypes.NewValue(tfType.(tftypes.Object).AttributeTypes["timeouts"], nil),
	})

	deleteReq := resource.DeleteRequest{
		State: tfsdk.State{Schema: schemaResp.Schema, Raw: stateVal},
	}
	var deleteResp resource.DeleteResponse
	deleteResp.State = tfsdk.State{Schema: schemaResp.Schema}

	r.Delete(ctx, deleteReq, &deleteResp)

	// Delete of already-gone should not error
	if deleteResp.Diagnostics.HasError() {
		t.Fatalf("Delete should not error for already-gone volume, got: %v", deleteResp.Diagnostics.Errors())
	}
}

func TestVolumeAttachment_TFSDKDeletePollErrorState(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/me":
			_ = json.NewEncoder(w).Encode(map[string]string{"id": "user-123", "tenantId": "tenant-456"})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/tenants/tenant-456/volumes/vol-dpe-1/detach":
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(map[string]string{
				"operationId": "op-detach-dpe-1", "status": "accepted", "resourceType": "volume",
			})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/tenant-456/operations/op-detach-dpe-1":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"operationId": "op-detach-dpe-1", "status": "completed", "resourceType": "volume",
			})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/tenant-456/volumes/vol-dpe-1":
			_ = json.NewEncoder(w).Encode(apiVolume{ID: "vol-dpe-1", Status: "error"})
		default:
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]string{"code": "NOT_FOUND", "message": "not found"})
		}
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key") // pragma: allowlist secret
	if err := c.Configure(context.Background()); err != nil {
		t.Fatalf("client configure failed: %v", err)
	}

	r := NewResource()
	configureVAResource(t, r, c)
	schemaResp := getVASchema(t)

	ctx := context.Background()
	tfType := schemaResp.Schema.Type().TerraformType(ctx)

	stateVal := tftypes.NewValue(tfType, map[string]tftypes.Value{
		"id":          tftypes.NewValue(tftypes.String, "vol-dpe-1/inst-dpe-1"),
		"volume_id":   tftypes.NewValue(tftypes.String, "vol-dpe-1"),
		"instance_id": tftypes.NewValue(tftypes.String, "inst-dpe-1"),
		"device_path": tftypes.NewValue(tftypes.String, "/dev/vdb"),
		"timeouts":    tftypes.NewValue(tfType.(tftypes.Object).AttributeTypes["timeouts"], nil),
	})

	deleteReq := resource.DeleteRequest{
		State: tfsdk.State{Schema: schemaResp.Schema, Raw: stateVal},
	}
	var deleteResp resource.DeleteResponse
	deleteResp.State = tfsdk.State{Schema: schemaResp.Schema}

	r.Delete(ctx, deleteReq, &deleteResp)

	// The detach decision is now the OPERATION's verdict: with the workflow
	// reporting completed, the volume's own (unrelated) error status does NOT
	// veto the destroy — the old flow conflated volume status with detach
	// convergence and dropped the verdict envelope entirely.
	if deleteResp.Diagnostics.HasError() {
		t.Fatalf("a completed detach operation is the verdict: %v", deleteResp.Diagnostics.Errors())
	}
}

// TestVolumeAttachment_TFSDKDeleteSyncFallbackPollErrorState: on a NON-202
// backend (synchronous detach) the volume status poll remains the wait, and an
// error state fails the destroy.
func TestVolumeAttachment_TFSDKDeleteSyncFallbackPollErrorState(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/me":
			_ = json.NewEncoder(w).Encode(map[string]string{"id": "user-123", "tenantId": "tenant-456"})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/tenants/tenant-456/volumes/vol-dsf-1/detach":
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(apiVolume{ID: "vol-dsf-1", Status: "detaching"})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/tenant-456/volumes/vol-dsf-1":
			_ = json.NewEncoder(w).Encode(apiVolume{ID: "vol-dsf-1", Status: "error"})
		default:
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]string{"code": "NOT_FOUND", "message": "not found"})
		}
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key") // pragma: allowlist secret
	if err := c.Configure(context.Background()); err != nil {
		t.Fatalf("client configure failed: %v", err)
	}

	r := NewResource()
	configureVAResource(t, r, c)
	schemaResp := getVASchema(t)

	ctx := context.Background()
	tfType := schemaResp.Schema.Type().TerraformType(ctx)

	stateVal := tftypes.NewValue(tfType, map[string]tftypes.Value{
		"id":          tftypes.NewValue(tftypes.String, "vol-dsf-1/inst-dsf-1"),
		"volume_id":   tftypes.NewValue(tftypes.String, "vol-dsf-1"),
		"instance_id": tftypes.NewValue(tftypes.String, "inst-dsf-1"),
		"device_path": tftypes.NewValue(tftypes.String, "/dev/vdb"),
		"timeouts":    tftypes.NewValue(tfType.(tftypes.Object).AttributeTypes["timeouts"], nil),
	})

	deleteReq := resource.DeleteRequest{
		State: tfsdk.State{Schema: schemaResp.Schema, Raw: stateVal},
	}
	var deleteResp resource.DeleteResponse
	deleteResp.State = tfsdk.State{Schema: schemaResp.Schema}

	r.Delete(ctx, deleteReq, &deleteResp)

	if !deleteResp.Diagnostics.HasError() {
		t.Error("expected error when volume enters error state during sync fallback detach polling")
	}
}

// --- op-verdict tests: the provisioning answer outranks the volume status
// poll, and a failure must be classified (refused vs unknown), never silent. ---

func volumeAttachmentCreateTest(t *testing.T, serverURL string) (context.Context, resource.CreateRequest, resource.CreateResponse, resource.Resource) {
	t.Helper()
	c := client.NewClient(serverURL, "test-key") // pragma: allowlist secret
	if err := c.Configure(context.Background()); err != nil {
		t.Fatalf("client configure failed: %v", err)
	}
	r := NewResource()
	configureVAResource(t, r, c)
	schemaResp := getVASchema(t)
	ctx := context.Background()
	tfType := schemaResp.Schema.Type().TerraformType(ctx)

	planVal := tftypes.NewValue(tfType, map[string]tftypes.Value{
		"id":          tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"volume_id":   tftypes.NewValue(tftypes.String, "vol-cv-1"),
		"instance_id": tftypes.NewValue(tftypes.String, "inst-cv-1"),
		"device_path": tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"timeouts":    tftypes.NewValue(tfType.(tftypes.Object).AttributeTypes["timeouts"], nil),
	})
	req := resource.CreateRequest{Plan: tfsdk.Plan{Schema: schemaResp.Schema, Raw: planVal}}
	var resp resource.CreateResponse
	resp.State = tfsdk.State{Schema: schemaResp.Schema}
	return ctx, req, resp, r
}

func volumeAttachmentDeleteTest(t *testing.T, serverURL string, volumeID string) (context.Context, resource.DeleteRequest, resource.DeleteResponse, resource.Resource) {
	t.Helper()
	c := client.NewClient(serverURL, "test-key") // pragma: allowlist secret
	if err := c.Configure(context.Background()); err != nil {
		t.Fatalf("client configure failed: %v", err)
	}
	r := NewResource()
	configureVAResource(t, r, c)
	schemaResp := getVASchema(t)
	ctx := context.Background()
	tfType := schemaResp.Schema.Type().TerraformType(ctx)

	stateVal := tftypes.NewValue(tfType, map[string]tftypes.Value{
		"id":          tftypes.NewValue(tftypes.String, volumeID+"/inst-cd-1"),
		"volume_id":   tftypes.NewValue(tftypes.String, volumeID),
		"instance_id": tftypes.NewValue(tftypes.String, "inst-cd-1"),
		"device_path": tftypes.NewValue(tftypes.String, "/dev/vdb"),
		"timeouts":    tftypes.NewValue(tfType.(tftypes.Object).AttributeTypes["timeouts"], nil),
	})
	req := resource.DeleteRequest{State: tfsdk.State{Schema: schemaResp.Schema, Raw: stateVal}}
	var resp resource.DeleteResponse
	resp.State = tfsdk.State{Schema: schemaResp.Schema}
	return ctx, req, resp, r
}

// TestCreateAttachOperationRefusedIsClassified: the platform DECIDED no.
// Nothing was attached, nothing is recorded, and the refusal says so.
func TestCreateAttachOperationRefusedIsClassified(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/me":
			_ = json.NewEncoder(w).Encode(map[string]string{"id": "user-123", "tenantId": "tenant-456"})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/tenants/tenant-456/volumes/vol-cv-1/attach":
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(map[string]string{
				"operationId": "op-cv-1", "status": "accepted", "resourceType": "volume",
			})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/tenant-456/operations/op-cv-1":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"operationId": "op-cv-1", "status": "failed", "resourceType": "volume",
				"errorCode": "conflict", "error": "instance inst-cv-1 has no free device slots",
			})
		default:
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]string{"code": "NOT_FOUND", "message": "not found"})
		}
	}))
	defer server.Close()

	ctx, req, resp, r := volumeAttachmentCreateTest(t, server.URL)
	r.Create(ctx, req, &resp)

	if !resp.Diagnostics.HasError() {
		t.Fatal("a refused attach must fail the apply")
	}
	err := resp.Diagnostics.Errors()[0]
	if !strings.Contains(err.Summary(), "Refused By The Platform") {
		t.Errorf("refused attach must be classified as refused, got summary %q", err.Summary())
	}
	if !strings.Contains(err.Detail(), "instance inst-cv-1 has no free device slots") {
		t.Errorf("refusal must carry the platform's prose (and prefer the typed code), got: %s", err.Detail())
	}
	if !strings.Contains(err.Detail(), "conflict") {
		t.Errorf("the machine-readable errorCode must be cited when present, got: %s", err.Detail())
	}
}

// TestAttachUnparseable202IsClassifiedUnknown: an accepted attach whose
// envelope cannot be read is neither a success nor a refusal.
func TestAttachUnparseable202IsClassifiedUnknown(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/me":
			_ = json.NewEncoder(w).Encode(map[string]string{"id": "user-123", "tenantId": "tenant-456"})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/tenants/tenant-456/volumes/vol-cv-1/attach":
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte("not-json"))
		default:
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]string{"code": "NOT_FOUND", "message": "not found"})
		}
	}))
	defer server.Close()

	ctx, req, resp, r := volumeAttachmentCreateTest(t, server.URL)
	r.Create(ctx, req, &resp)

	if !resp.Diagnostics.HasError() {
		t.Fatal("an accepted-but-untrackable attach must fail the apply, not report success")
	}
	if !strings.Contains(resp.Diagnostics.Errors()[0].Summary(), "Could Not Be Tracked") {
		t.Errorf("untrackable attach must be classified, got summary %q", resp.Diagnostics.Errors()[0].Summary())
	}
}

// TestDeleteOperationFailureRefusesAndKeepsRow: a refused DETACH leaves the
// attachment exactly where it was, and the row must stay to describe it.
func TestDeleteOperationFailureRefusesAndKeepsRow(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/me":
			_ = json.NewEncoder(w).Encode(map[string]string{"id": "user-123", "tenantId": "tenant-456"})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/tenants/tenant-456/volumes/vol-cd-1/detach":
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(map[string]string{
				"operationId": "op-cd-1", "status": "accepted", "resourceType": "volume",
			})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/tenant-456/operations/op-cd-1":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"operationId": "op-cd-1", "status": "failed", "resourceType": "volume",
				"error": "volume is busy: a snapshot batch is running",
			})
		default:
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]string{"code": "NOT_FOUND", "message": "not found"})
		}
	}))
	defer server.Close()

	ctx, req, resp, r := volumeAttachmentDeleteTest(t, server.URL, "vol-cd-1")
	r.Delete(ctx, req, &resp)

	if !resp.Diagnostics.HasError() {
		t.Fatal("a refused detach must fail the apply, not drop the row")
	}
	if !strings.Contains(resp.Diagnostics.Errors()[0].Summary(), "Refused By The Platform") {
		t.Errorf("refused detach must be classified as refused, got summary %q", resp.Diagnostics.Errors()[0].Summary())
	}
}

// TestDeleteUnwatchable202IsClassifiedUnknown: a 202 envelope that cannot be
// read is an unknown, never a silent success.
func TestDeleteUnwatchable202IsClassifiedUnknown(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/me":
			_ = json.NewEncoder(w).Encode(map[string]string{"id": "user-123", "tenantId": "tenant-456"})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/tenants/tenant-456/volumes/vol-cd-1/detach":
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte("not-json"))
		default:
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]string{"code": "NOT_FOUND", "message": "not found"})
		}
	}))
	defer server.Close()

	ctx, req, resp, r := volumeAttachmentDeleteTest(t, server.URL, "vol-cd-1")
	r.Delete(ctx, req, &resp)

	if !resp.Diagnostics.HasError() {
		t.Fatal("an accepted-but-unwatchable detach is classified unknown, never a silent success")
	}
	if !strings.Contains(resp.Diagnostics.Errors()[0].Summary(), "Outcome Is Unknown") {
		t.Errorf("unwatchable detach must be classified unknown, got summary %q", resp.Diagnostics.Errors()[0].Summary())
	}
}
