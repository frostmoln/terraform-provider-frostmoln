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
			w.WriteHeader(http.StatusAccepted)
			json.NewEncoder(w).Encode(map[string]string{
				"operationId": "op-attach-1", "status": "accepted", "resourceType": "volume",
			})

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
			w.WriteHeader(http.StatusAccepted)
			json.NewEncoder(w).Encode(map[string]string{
				"operationId": "op-detach-1", "status": "accepted", "resourceType": "volume",
			})

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
			json.NewEncoder(w).Encode(map[string]string{"id": "user-123", "tenantId": "tenant-456"})

		case r.Method == http.MethodPost && r.URL.Path == "/v1/tenants/tenant-456/volumes/vol-att-1/attach":
			var req apiAttachRequest
			json.NewDecoder(r.Body).Decode(&req)
			volume.AttachedTo = req.InstanceID
			volume.DevicePath = "/dev/vdb"
			volume.Status = "in-use"
			w.WriteHeader(http.StatusAccepted)
			json.NewEncoder(w).Encode(map[string]string{
				"operationId": "op-attach-1", "status": "accepted", "resourceType": "volume",
			})

		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/tenant-456/volumes/vol-att-1":
			json.NewEncoder(w).Encode(volume)

		default:
			w.WriteHeader(http.StatusNotFound)
			json.NewEncoder(w).Encode(map[string]interface{}{
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
	configureVAResource(t, r, c)
	schemaResp := getVASchema(t)

	ctx := context.Background()
	tfType := schemaResp.Schema.Type().TerraformType(ctx)

	planVal := tftypes.NewValue(tfType, map[string]tftypes.Value{
		"id":          tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"volume_id":   tftypes.NewValue(tftypes.String, "vol-att-1"),
		"instance_id": tftypes.NewValue(tftypes.String, "inst-att-1"),
		"device_path": tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
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
			json.NewEncoder(w).Encode(map[string]string{"id": "user-123", "tenantId": "tenant-456"})

		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/tenant-456/volumes/vol-r-1":
			json.NewEncoder(w).Encode(apiVolume{
				ID:         "vol-r-1",
				AttachedTo: "inst-r-1",
				DevicePath: "/dev/vdc",
				Status:     "in-use",
			})

		default:
			w.WriteHeader(http.StatusNotFound)
			json.NewEncoder(w).Encode(map[string]interface{}{
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
	configureVAResource(t, r, c)
	schemaResp := getVASchema(t)

	ctx := context.Background()
	tfType := schemaResp.Schema.Type().TerraformType(ctx)

	stateVal := tftypes.NewValue(tfType, map[string]tftypes.Value{
		"id":          tftypes.NewValue(tftypes.String, "vol-r-1/inst-r-1"),
		"volume_id":   tftypes.NewValue(tftypes.String, "vol-r-1"),
		"instance_id": tftypes.NewValue(tftypes.String, "inst-r-1"),
		"device_path": tftypes.NewValue(tftypes.String, "/dev/vdc"),
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
			json.NewEncoder(w).Encode(map[string]string{"id": "user-123", "tenantId": "tenant-456"})

		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/tenant-456/volumes/vol-det-1":
			// Volume exists but attached to a different instance
			json.NewEncoder(w).Encode(apiVolume{
				ID:         "vol-det-1",
				AttachedTo: "inst-other",
				Status:     "in-use",
			})

		default:
			w.WriteHeader(http.StatusNotFound)
			json.NewEncoder(w).Encode(map[string]interface{}{
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
	configureVAResource(t, r, c)
	schemaResp := getVASchema(t)

	ctx := context.Background()
	tfType := schemaResp.Schema.Type().TerraformType(ctx)

	stateVal := tftypes.NewValue(tfType, map[string]tftypes.Value{
		"id":          tftypes.NewValue(tftypes.String, "vol-det-1/inst-expected"),
		"volume_id":   tftypes.NewValue(tftypes.String, "vol-det-1"),
		"instance_id": tftypes.NewValue(tftypes.String, "inst-expected"),
		"device_path": tftypes.NewValue(tftypes.String, "/dev/vdb"),
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
		ID:         "vol-d-1",
		AttachedTo: "inst-d-1",
		DevicePath: "/dev/vdb",
		Status:     "in-use",
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/me":
			json.NewEncoder(w).Encode(map[string]string{"id": "user-123", "tenantId": "tenant-456"})

		case r.Method == http.MethodPost && r.URL.Path == "/v1/tenants/tenant-456/volumes/vol-d-1/detach":
			detachCalled = true
			volume.AttachedTo = ""
			volume.DevicePath = ""
			volume.Status = "available"
			w.WriteHeader(http.StatusAccepted)
			json.NewEncoder(w).Encode(map[string]string{
				"operationId": "op-detach-1", "status": "accepted", "resourceType": "volume",
			})

		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/tenant-456/volumes/vol-d-1":
			json.NewEncoder(w).Encode(volume)

		default:
			w.WriteHeader(http.StatusNotFound)
			json.NewEncoder(w).Encode(map[string]interface{}{
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
	configureVAResource(t, r, c)
	schemaResp := getVASchema(t)

	ctx := context.Background()
	tfType := schemaResp.Schema.Type().TerraformType(ctx)

	stateVal := tftypes.NewValue(tfType, map[string]tftypes.Value{
		"id":          tftypes.NewValue(tftypes.String, "vol-d-1/inst-d-1"),
		"volume_id":   tftypes.NewValue(tftypes.String, "vol-d-1"),
		"instance_id": tftypes.NewValue(tftypes.String, "inst-d-1"),
		"device_path": tftypes.NewValue(tftypes.String, "/dev/vdb"),
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
			json.NewEncoder(w).Encode(map[string]string{"id": "user-123", "tenantId": "tenant-456"})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/tenants/tenant-456/volumes/vol-ae-1/attach":
			w.WriteHeader(http.StatusInternalServerError)
			json.NewEncoder(w).Encode(map[string]interface{}{
				"error": map[string]string{"code": "INTERNAL_ERROR", "message": "attach failed"},
			})
		default:
			w.WriteHeader(http.StatusNotFound)
			json.NewEncoder(w).Encode(map[string]interface{}{
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
	configureVAResource(t, r, c)
	schemaResp := getVASchema(t)

	ctx := context.Background()
	tfType := schemaResp.Schema.Type().TerraformType(ctx)

	planVal := tftypes.NewValue(tfType, map[string]tftypes.Value{
		"id":          tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"volume_id":   tftypes.NewValue(tftypes.String, "vol-ae-1"),
		"instance_id": tftypes.NewValue(tftypes.String, "inst-ae-1"),
		"device_path": tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
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
			json.NewEncoder(w).Encode(map[string]string{"id": "user-123", "tenantId": "tenant-456"})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/tenants/tenant-456/volumes/vol-pe-1/attach":
			w.WriteHeader(http.StatusAccepted)
			json.NewEncoder(w).Encode(map[string]string{
				"operationId": "op-attach-pe-1", "status": "accepted", "resourceType": "volume",
			})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/tenant-456/volumes/vol-pe-1":
			json.NewEncoder(w).Encode(apiVolume{ID: "vol-pe-1", Status: "error"})
		default:
			w.WriteHeader(http.StatusNotFound)
			json.NewEncoder(w).Encode(map[string]interface{}{
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
	configureVAResource(t, r, c)
	schemaResp := getVASchema(t)

	ctx := context.Background()
	tfType := schemaResp.Schema.Type().TerraformType(ctx)

	planVal := tftypes.NewValue(tfType, map[string]tftypes.Value{
		"id":          tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"volume_id":   tftypes.NewValue(tftypes.String, "vol-pe-1"),
		"instance_id": tftypes.NewValue(tftypes.String, "inst-pe-1"),
		"device_path": tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
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
	var getCount atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/me":
			json.NewEncoder(w).Encode(map[string]string{"id": "user-123", "tenantId": "tenant-456"})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/tenants/tenant-456/volumes/vol-fre-1/attach":
			w.WriteHeader(http.StatusAccepted)
			json.NewEncoder(w).Encode(map[string]string{
				"operationId": "op-attach-fre-1", "status": "accepted", "resourceType": "volume",
			})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/tenant-456/volumes/vol-fre-1":
			n := getCount.Add(1)
			if n == 1 {
				// Poll: return in-use to pass WaitForState
				json.NewEncoder(w).Encode(apiVolume{
					ID:         "vol-fre-1",
					Status:     "in-use",
					AttachedTo: "inst-fre-1",
					DevicePath: "/dev/vdb",
				})
			} else {
				// Final read: return error
				w.WriteHeader(http.StatusInternalServerError)
				json.NewEncoder(w).Encode(map[string]interface{}{
					"error": map[string]string{"code": "INTERNAL_ERROR", "message": "read failed"},
				})
			}
		default:
			w.WriteHeader(http.StatusNotFound)
			json.NewEncoder(w).Encode(map[string]interface{}{
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
	configureVAResource(t, r, c)
	schemaResp := getVASchema(t)

	ctx := context.Background()
	tfType := schemaResp.Schema.Type().TerraformType(ctx)

	planVal := tftypes.NewValue(tfType, map[string]tftypes.Value{
		"id":          tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"volume_id":   tftypes.NewValue(tftypes.String, "vol-fre-1"),
		"instance_id": tftypes.NewValue(tftypes.String, "inst-fre-1"),
		"device_path": tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
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
			json.NewEncoder(w).Encode(map[string]string{"id": "user-123", "tenantId": "tenant-456"})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/tenants/tenant-456/volumes/vol-mm-1/attach":
			w.WriteHeader(http.StatusAccepted)
			json.NewEncoder(w).Encode(map[string]string{
				"operationId": "op-attach-mm-1", "status": "accepted", "resourceType": "volume",
			})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/tenant-456/volumes/vol-mm-1":
			// Return in-use but attached to a DIFFERENT instance
			json.NewEncoder(w).Encode(apiVolume{
				ID:         "vol-mm-1",
				Status:     "in-use",
				AttachedTo: "inst-other",
				DevicePath: "/dev/vdb",
			})
		default:
			w.WriteHeader(http.StatusNotFound)
			json.NewEncoder(w).Encode(map[string]interface{}{
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
	configureVAResource(t, r, c)
	schemaResp := getVASchema(t)

	ctx := context.Background()
	tfType := schemaResp.Schema.Type().TerraformType(ctx)

	planVal := tftypes.NewValue(tfType, map[string]tftypes.Value{
		"id":          tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"volume_id":   tftypes.NewValue(tftypes.String, "vol-mm-1"),
		"instance_id": tftypes.NewValue(tftypes.String, "inst-expected"),
		"device_path": tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
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
			json.NewEncoder(w).Encode(map[string]string{"id": "user-123", "tenantId": "tenant-456"})
		default:
			w.WriteHeader(http.StatusNotFound)
			json.NewEncoder(w).Encode(map[string]interface{}{
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
	configureVAResource(t, r, c)
	schemaResp := getVASchema(t)

	ctx := context.Background()
	tfType := schemaResp.Schema.Type().TerraformType(ctx)

	stateVal := tftypes.NewValue(tfType, map[string]tftypes.Value{
		"id":          tftypes.NewValue(tftypes.String, "vol-nf-1/inst-nf-1"),
		"volume_id":   tftypes.NewValue(tftypes.String, "vol-nf-1"),
		"instance_id": tftypes.NewValue(tftypes.String, "inst-nf-1"),
		"device_path": tftypes.NewValue(tftypes.String, "/dev/vdb"),
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
			json.NewEncoder(w).Encode(map[string]string{"id": "user-123", "tenantId": "tenant-456"})
		default:
			w.WriteHeader(http.StatusInternalServerError)
			json.NewEncoder(w).Encode(map[string]interface{}{
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
			json.NewEncoder(w).Encode(map[string]string{"id": "user-123", "tenantId": "tenant-456"})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/tenant-456/volumes/vol-bj-1":
			w.WriteHeader(http.StatusOK)
			w.Write([]byte("not json"))
		default:
			w.WriteHeader(http.StatusNotFound)
			json.NewEncoder(w).Encode(map[string]interface{}{
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
	configureVAResource(t, r, c)
	schemaResp := getVASchema(t)

	ctx := context.Background()
	tfType := schemaResp.Schema.Type().TerraformType(ctx)

	stateVal := tftypes.NewValue(tfType, map[string]tftypes.Value{
		"id":          tftypes.NewValue(tftypes.String, "vol-bj-1/inst-bj-1"),
		"volume_id":   tftypes.NewValue(tftypes.String, "vol-bj-1"),
		"instance_id": tftypes.NewValue(tftypes.String, "inst-bj-1"),
		"device_path": tftypes.NewValue(tftypes.String, "/dev/vdb"),
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
			json.NewEncoder(w).Encode(map[string]string{"id": "user-123", "tenantId": "tenant-456"})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/tenants/tenant-456/volumes/vol-de-1/detach":
			w.WriteHeader(http.StatusInternalServerError)
			json.NewEncoder(w).Encode(map[string]interface{}{
				"error": map[string]string{"code": "INTERNAL_ERROR", "message": "detach failed"},
			})
		default:
			w.WriteHeader(http.StatusNotFound)
			json.NewEncoder(w).Encode(map[string]interface{}{
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
	configureVAResource(t, r, c)
	schemaResp := getVASchema(t)

	ctx := context.Background()
	tfType := schemaResp.Schema.Type().TerraformType(ctx)

	stateVal := tftypes.NewValue(tfType, map[string]tftypes.Value{
		"id":          tftypes.NewValue(tftypes.String, "vol-de-1/inst-de-1"),
		"volume_id":   tftypes.NewValue(tftypes.String, "vol-de-1"),
		"instance_id": tftypes.NewValue(tftypes.String, "inst-de-1"),
		"device_path": tftypes.NewValue(tftypes.String, "/dev/vdb"),
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
			json.NewEncoder(w).Encode(map[string]string{"id": "user-123", "tenantId": "tenant-456"})
		default:
			w.WriteHeader(http.StatusNotFound)
			json.NewEncoder(w).Encode(map[string]interface{}{
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
	configureVAResource(t, r, c)
	schemaResp := getVASchema(t)

	ctx := context.Background()
	tfType := schemaResp.Schema.Type().TerraformType(ctx)

	stateVal := tftypes.NewValue(tfType, map[string]tftypes.Value{
		"id":          tftypes.NewValue(tftypes.String, "vol-gone/inst-gone"),
		"volume_id":   tftypes.NewValue(tftypes.String, "vol-gone"),
		"instance_id": tftypes.NewValue(tftypes.String, "inst-gone"),
		"device_path": tftypes.NewValue(tftypes.String, "/dev/vdb"),
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
			json.NewEncoder(w).Encode(map[string]string{"id": "user-123", "tenantId": "tenant-456"})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/tenants/tenant-456/volumes/vol-dpe-1/detach":
			w.WriteHeader(http.StatusAccepted)
			json.NewEncoder(w).Encode(map[string]string{
				"operationId": "op-detach-dpe-1", "status": "accepted", "resourceType": "volume",
			})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/tenant-456/volumes/vol-dpe-1":
			json.NewEncoder(w).Encode(apiVolume{ID: "vol-dpe-1", Status: "error"})
		default:
			w.WriteHeader(http.StatusNotFound)
			json.NewEncoder(w).Encode(map[string]interface{}{
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
	configureVAResource(t, r, c)
	schemaResp := getVASchema(t)

	ctx := context.Background()
	tfType := schemaResp.Schema.Type().TerraformType(ctx)

	stateVal := tftypes.NewValue(tfType, map[string]tftypes.Value{
		"id":          tftypes.NewValue(tftypes.String, "vol-dpe-1/inst-dpe-1"),
		"volume_id":   tftypes.NewValue(tftypes.String, "vol-dpe-1"),
		"instance_id": tftypes.NewValue(tftypes.String, "inst-dpe-1"),
		"device_path": tftypes.NewValue(tftypes.String, "/dev/vdb"),
	})

	deleteReq := resource.DeleteRequest{
		State: tfsdk.State{Schema: schemaResp.Schema, Raw: stateVal},
	}
	var deleteResp resource.DeleteResponse
	deleteResp.State = tfsdk.State{Schema: schemaResp.Schema}

	r.Delete(ctx, deleteReq, &deleteResp)

	if !deleteResp.Diagnostics.HasError() {
		t.Error("expected error when volume enters error state during detach polling")
	}
}
