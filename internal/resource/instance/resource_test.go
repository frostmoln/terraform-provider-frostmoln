package instance

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"git.nl.cloud/NordicLight/terraform-provider-frostmoln/internal/client"
)

// --- Model unit tests ---

func TestComputeUserDataHash(t *testing.T) {
	hash := computeUserDataHash("#!/bin/bash\necho hello")
	if hash == "" {
		t.Fatal("expected non-empty hash")
	}
	// Same input should produce same hash.
	hash2 := computeUserDataHash("#!/bin/bash\necho hello")
	if hash != hash2 {
		t.Error("expected same hash for same input")
	}
	// Different input should produce different hash.
	hash3 := computeUserDataHash("#!/bin/bash\necho world")
	if hash == hash3 {
		t.Error("expected different hash for different input")
	}
}

func TestInstanceModelToCreateRequest(t *testing.T) {
	ctx := context.Background()
	diags := diag.Diagnostics{}

	sgs, d := types.SetValueFrom(ctx, types.StringType, []string{"sg-1", "sg-2"})
	diags.Append(d...)
	keys, d := types.SetValueFrom(ctx, types.StringType, []string{"my-key"})
	diags.Append(d...)
	tags, d := types.MapValueFrom(ctx, types.StringType, map[string]string{"env": "test"})
	diags.Append(d...)

	model := InstanceModel{
		Name:           types.StringValue("web-1"),
		FlavorID:       types.StringValue("flavor-small"),
		ImageID:        types.StringValue("img-ubuntu"),
		Region:         types.StringValue("eu-north-1"),
		Zone:           types.StringValue("eu-north-1a"),
		VPCID:          types.StringValue("vpc-123"),
		SubnetID:       types.StringValue("subnet-456"),
		SecurityGroups: sgs,
		SSHKeyNames:    keys,
		UserData:       types.StringValue("#!/bin/bash\necho hello"),
		Tags:           tags,
	}

	req := model.toCreateRequest(ctx, &diags)
	if diags.HasError() {
		t.Fatalf("unexpected diagnostics: %v", diags.Errors())
	}

	if req.Name != "web-1" {
		t.Errorf("expected name web-1, got %s", req.Name)
	}
	if req.FlavorID != "flavor-small" {
		t.Errorf("expected flavor_id flavor-small, got %s", req.FlavorID)
	}
	if req.ImageID != "img-ubuntu" {
		t.Errorf("expected image_id img-ubuntu, got %s", req.ImageID)
	}
	if req.Region != "eu-north-1" {
		t.Errorf("expected region eu-north-1, got %s", req.Region)
	}
	if req.Zone != "eu-north-1a" {
		t.Errorf("expected zone eu-north-1a, got %s", req.Zone)
	}
	if req.VPCID != "vpc-123" {
		t.Errorf("expected vpc_id vpc-123, got %s", req.VPCID)
	}
	if req.SubnetID != "subnet-456" {
		t.Errorf("expected subnet_id subnet-456, got %s", req.SubnetID)
	}
	if len(req.SecurityGroups) != 2 {
		t.Errorf("expected 2 security groups, got %d", len(req.SecurityGroups))
	}
	if len(req.SSHKeyNames) != 1 || req.SSHKeyNames[0] != "my-key" {
		t.Errorf("expected ssh key my-key, got %v", req.SSHKeyNames)
	}
	if req.UserData != "#!/bin/bash\necho hello" {
		t.Errorf("expected user data, got %s", req.UserData)
	}
	if req.Tags["env"] != "test" {
		t.Errorf("expected tag env=test, got %v", req.Tags)
	}
}

func TestInstanceModelToCreateRequestMinimal(t *testing.T) {
	ctx := context.Background()
	diags := diag.Diagnostics{}

	model := InstanceModel{
		Name:           types.StringValue("minimal-vm"),
		FlavorID:       types.StringValue("flavor-small"),
		ImageID:        types.StringValue("img-ubuntu"),
		Region:         types.StringNull(),
		Zone:           types.StringNull(),
		VPCID:          types.StringNull(),
		SubnetID:       types.StringNull(),
		SecurityGroups: types.SetNull(types.StringType),
		SSHKeyNames:    types.SetNull(types.StringType),
		UserData:       types.StringNull(),
		Tags:           types.MapNull(types.StringType),
	}

	req := model.toCreateRequest(ctx, &diags)
	if diags.HasError() {
		t.Fatalf("unexpected diagnostics: %v", diags.Errors())
	}

	if req.Name != "minimal-vm" {
		t.Errorf("expected name minimal-vm, got %s", req.Name)
	}
	if req.Region != "" {
		t.Errorf("expected empty region, got %s", req.Region)
	}
	if req.SecurityGroups != nil {
		t.Errorf("expected nil security groups, got %v", req.SecurityGroups)
	}
	if req.UserData != "" {
		t.Errorf("expected empty user data, got %s", req.UserData)
	}
}

func TestInstanceModelToUpdateRequest(t *testing.T) {
	ctx := context.Background()
	diags := diag.Diagnostics{}

	sgs, d := types.SetValueFrom(ctx, types.StringType, []string{"sg-new"})
	diags.Append(d...)
	tags, d := types.MapValueFrom(ctx, types.StringType, map[string]string{"env": "prod"})
	diags.Append(d...)

	model := InstanceModel{
		Name:           types.StringValue("renamed-vm"),
		SecurityGroups: sgs,
		Tags:           tags,
	}

	req := model.toUpdateRequest(ctx, &diags)
	if diags.HasError() {
		t.Fatalf("unexpected diagnostics: %v", diags.Errors())
	}

	if req.Name == nil || *req.Name != "renamed-vm" {
		t.Errorf("expected name renamed-vm, got %v", req.Name)
	}
	if len(req.SecurityGroups) != 1 || req.SecurityGroups[0] != "sg-new" {
		t.Errorf("expected security group sg-new, got %v", req.SecurityGroups)
	}
	if req.Tags["env"] != "prod" {
		t.Errorf("expected tag env=prod, got %v", req.Tags)
	}
}

func TestInstanceModelFromAPI(t *testing.T) {
	ctx := context.Background()
	diags := diag.Diagnostics{}

	inst := &apiInstance{
		ID:             "inst-abc",
		Name:           "web-1",
		Status:         "running",
		FlavorID:       "flavor-small",
		FlavorName:     "Small",
		ImageID:        "img-ubuntu",
		ImageName:      "Ubuntu 24.04",
		Region:         "eu-north-1",
		Zone:           "eu-north-1a",
		VPCID:          "vpc-123",
		SubnetID:       "subnet-456",
		PrivateIP:      "10.0.1.5",
		PublicIP:       "203.0.113.10",
		SecurityGroups: []string{"sg-1"},
		SSHKeyNames:    []string{"my-key"},
		Tags:           map[string]string{"env": "test"},
		CreatedAt:      "2025-06-01T12:00:00Z",
	}

	model := InstanceModel{
		SecurityGroups: types.SetNull(types.StringType),
		SSHKeyNames:    types.SetNull(types.StringType),
		Tags:           types.MapNull(types.StringType),
	}
	model.fromAPI(ctx, inst, &diags)
	if diags.HasError() {
		t.Fatalf("unexpected diagnostics: %v", diags.Errors())
	}

	if model.ID.ValueString() != "inst-abc" {
		t.Errorf("expected ID inst-abc, got %s", model.ID.ValueString())
	}
	if model.Name.ValueString() != "web-1" {
		t.Errorf("expected name web-1, got %s", model.Name.ValueString())
	}
	if model.Status.ValueString() != "running" {
		t.Errorf("expected status running, got %s", model.Status.ValueString())
	}
	if model.FlavorID.ValueString() != "flavor-small" {
		t.Errorf("expected flavor_id flavor-small, got %s", model.FlavorID.ValueString())
	}
	if model.FlavorName.ValueString() != "Small" {
		t.Errorf("expected flavor_name Small, got %s", model.FlavorName.ValueString())
	}
	if model.ImageID.ValueString() != "img-ubuntu" {
		t.Errorf("expected image_id img-ubuntu, got %s", model.ImageID.ValueString())
	}
	if model.ImageName.ValueString() != "Ubuntu 24.04" {
		t.Errorf("expected image_name Ubuntu 24.04, got %s", model.ImageName.ValueString())
	}
	if model.Region.ValueString() != "eu-north-1" {
		t.Errorf("expected region eu-north-1, got %s", model.Region.ValueString())
	}
	if model.Zone.ValueString() != "eu-north-1a" {
		t.Errorf("expected zone eu-north-1a, got %s", model.Zone.ValueString())
	}
	if model.VPCID.ValueString() != "vpc-123" {
		t.Errorf("expected vpc_id vpc-123, got %s", model.VPCID.ValueString())
	}
	if model.SubnetID.ValueString() != "subnet-456" {
		t.Errorf("expected subnet_id subnet-456, got %s", model.SubnetID.ValueString())
	}
	if model.PrivateIP.ValueString() != "10.0.1.5" {
		t.Errorf("expected private_ip 10.0.1.5, got %s", model.PrivateIP.ValueString())
	}
	if model.PublicIP.ValueString() != "203.0.113.10" {
		t.Errorf("expected public_ip 203.0.113.10, got %s", model.PublicIP.ValueString())
	}
	if model.CreatedAt.ValueString() != "2025-06-01T12:00:00Z" {
		t.Errorf("expected created_at, got %s", model.CreatedAt.ValueString())
	}
}

func TestInstanceModelFromAPIMinimalFields(t *testing.T) {
	ctx := context.Background()
	diags := diag.Diagnostics{}

	inst := &apiInstance{
		ID:        "inst-min",
		Name:      "minimal",
		Status:    "running",
		FlavorID:  "flavor-small",
		ImageID:   "img-ubuntu",
		Region:    "eu-north-1",
		CreatedAt: "2025-06-01T12:00:00Z",
	}

	model := InstanceModel{
		Zone:           types.StringNull(),
		VPCID:          types.StringNull(),
		SubnetID:       types.StringNull(),
		SecurityGroups: types.SetNull(types.StringType),
		SSHKeyNames:    types.SetNull(types.StringType),
		Tags:           types.MapNull(types.StringType),
	}
	model.fromAPI(ctx, inst, &diags)
	if diags.HasError() {
		t.Fatalf("unexpected diagnostics: %v", diags.Errors())
	}

	if !model.FlavorName.IsNull() {
		t.Error("expected flavor_name to be null for empty response")
	}
	if !model.ImageName.IsNull() {
		t.Error("expected image_name to be null for empty response")
	}
	if !model.Zone.IsNull() {
		t.Error("expected zone to be null")
	}
	if !model.PrivateIP.IsNull() {
		t.Error("expected private_ip to be null")
	}
	if !model.PublicIP.IsNull() {
		t.Error("expected public_ip to be null")
	}
	if !model.SecurityGroups.IsNull() {
		t.Error("expected security_groups to be null")
	}
	if !model.SSHKeyNames.IsNull() {
		t.Error("expected ssh_key_names to be null")
	}
	if !model.Tags.IsNull() {
		t.Error("expected tags to be null")
	}
}

// --- HTTP integration tests ---

func newTestClient(t *testing.T, server *httptest.Server) *client.Client {
	t.Helper()
	c := client.NewClient(server.URL, "test-key")
	if err := c.Configure(context.Background()); err != nil {
		t.Fatalf("configure failed: %v", err)
	}
	return c
}

func meHandler(w http.ResponseWriter, r *http.Request) {
	json.NewEncoder(w).Encode(map[string]string{
		"id":       "user-123",
		"tenantId": "tenant-456",
		"email":    "test@example.com",
	})
}

func TestInstanceCreate(t *testing.T) {
	var pollCount atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/me":
			meHandler(w, r)

		case r.Method == http.MethodPost && r.URL.Path == "/v1/tenants/tenant-456/instances":
			var req apiCreateInstanceRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatalf("failed to decode request: %v", err)
			}
			if req.Name != "web-1" {
				t.Errorf("expected name web-1, got %s", req.Name)
			}
			if req.FlavorID != "flavor-small" {
				t.Errorf("expected flavor_id flavor-small, got %s", req.FlavorID)
			}
			if req.ImageID != "img-ubuntu" {
				t.Errorf("expected image_id img-ubuntu, got %s", req.ImageID)
			}
			if req.UserData != "#!/bin/bash" {
				t.Errorf("expected user data, got %s", req.UserData)
			}
			w.WriteHeader(http.StatusAccepted)
			json.NewEncoder(w).Encode(apiInstance{
				ID:        "inst-new",
				Name:      req.Name,
				Status:    "provisioning",
				FlavorID:  req.FlavorID,
				ImageID:   req.ImageID,
				Region:    "eu-north-1",
				CreatedAt: "2025-06-01T12:00:00Z",
			})

		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/tenant-456/instances/inst-new":
			n := pollCount.Add(1)
			status := "provisioning"
			if n >= 2 {
				status = "running"
			}
			json.NewEncoder(w).Encode(apiInstance{
				ID:         "inst-new",
				Name:       "web-1",
				Status:     status,
				FlavorID:   "flavor-small",
				FlavorName: "Small",
				ImageID:    "img-ubuntu",
				ImageName:  "Ubuntu 24.04",
				Region:     "eu-north-1",
				PrivateIP:  "10.0.1.5",
				CreatedAt:  "2025-06-01T12:00:00Z",
			})

		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	c := newTestClient(t, server)

	// Simulate the create flow.
	apiReq := apiCreateInstanceRequest{
		Name:     "web-1",
		FlavorID: "flavor-small",
		ImageID:  "img-ubuntu",
		UserData: "#!/bin/bash",
	}
	resp, err := c.Post(context.Background(), c.TenantPath("/instances"), apiReq)
	if err != nil {
		t.Fatalf("post failed: %v", err)
	}

	inst, err := client.ParseResponse[apiInstance](resp)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}

	if inst.ID != "inst-new" {
		t.Errorf("expected ID inst-new, got %s", inst.ID)
	}
	if inst.Status != "provisioning" {
		t.Errorf("expected status provisioning, got %s", inst.Status)
	}

	// Simulate polling until running.
	finalState, err := client.WaitForState(context.Background(), client.PollConfig{
		Interval:     10 * time.Millisecond,
		Timeout:      1 * time.Second,
		TargetStates: []string{"running"},
		ErrorStates:  []string{"error"},
		ResourceName: "instance",
		PollFunc: func(ctx context.Context) (string, error) {
			pollResp, pollErr := c.Get(ctx, c.TenantPath("/instances/"+inst.ID), nil)
			if pollErr != nil {
				return "", pollErr
			}
			current, parseErr := client.ParseResponse[apiInstance](pollResp)
			if parseErr != nil {
				return "", parseErr
			}
			return current.Status, nil
		},
	})
	if err != nil {
		t.Fatalf("polling failed: %v", err)
	}
	if finalState != "running" {
		t.Errorf("expected final state running, got %s", finalState)
	}
}

func TestInstanceRead(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/me":
			meHandler(w, r)

		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/tenant-456/instances/inst-abc":
			json.NewEncoder(w).Encode(apiInstance{
				ID:             "inst-abc",
				Name:           "web-1",
				Status:         "running",
				FlavorID:       "flavor-small",
				FlavorName:     "Small",
				ImageID:        "img-ubuntu",
				ImageName:      "Ubuntu 24.04",
				Region:         "eu-north-1",
				Zone:           "eu-north-1a",
				VPCID:          "vpc-123",
				SubnetID:       "subnet-456",
				PrivateIP:      "10.0.1.5",
				PublicIP:       "203.0.113.10",
				SecurityGroups: []string{"sg-1"},
				SSHKeyNames:    []string{"my-key"},
				Tags:           map[string]string{"env": "test"},
				CreatedAt:      "2025-06-01T12:00:00Z",
			})

		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	c := newTestClient(t, server)

	resp, err := c.Get(context.Background(), c.TenantPath("/instances/inst-abc"), nil)
	if err != nil {
		t.Fatalf("get failed: %v", err)
	}

	inst, err := client.ParseResponse[apiInstance](resp)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}

	if inst.ID != "inst-abc" {
		t.Errorf("expected ID inst-abc, got %s", inst.ID)
	}
	if inst.Name != "web-1" {
		t.Errorf("expected name web-1, got %s", inst.Name)
	}
	if inst.Status != "running" {
		t.Errorf("expected status running, got %s", inst.Status)
	}
	if inst.PrivateIP != "10.0.1.5" {
		t.Errorf("expected private_ip 10.0.1.5, got %s", inst.PrivateIP)
	}
	if len(inst.SecurityGroups) != 1 || inst.SecurityGroups[0] != "sg-1" {
		t.Errorf("expected security groups [sg-1], got %v", inst.SecurityGroups)
	}
}

func TestInstanceReadNotFound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/me":
			meHandler(w, r)

		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/tenant-456/instances/nonexistent":
			w.WriteHeader(http.StatusNotFound)
			json.NewEncoder(w).Encode(map[string]interface{}{
				"error": map[string]string{
					"code":    "NOT_FOUND",
					"message": "Instance not found",
				},
			})

		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	defer server.Close()

	c := newTestClient(t, server)

	_, err := c.Get(context.Background(), c.TenantPath("/instances/nonexistent"), nil)
	if err == nil {
		t.Fatal("expected error for not found")
	}
	if !client.IsNotFound(err) {
		t.Errorf("expected not found error, got %v", err)
	}
}

func TestInstanceUpdate(t *testing.T) {
	patched := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/me":
			meHandler(w, r)

		case r.Method == http.MethodPatch && r.URL.Path == "/v1/tenants/tenant-456/instances/inst-abc":
			patched = true
			var req apiUpdateInstanceRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatalf("failed to decode request: %v", err)
			}
			if req.Name == nil || *req.Name != "renamed-vm" {
				t.Errorf("expected name renamed-vm, got %v", req.Name)
			}
			json.NewEncoder(w).Encode(apiInstance{
				ID:        "inst-abc",
				Name:      "renamed-vm",
				Status:    "running",
				FlavorID:  "flavor-small",
				ImageID:   "img-ubuntu",
				Region:    "eu-north-1",
				CreatedAt: "2025-06-01T12:00:00Z",
			})

		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/tenant-456/instances/inst-abc":
			json.NewEncoder(w).Encode(apiInstance{
				ID:        "inst-abc",
				Name:      "renamed-vm",
				Status:    "running",
				FlavorID:  "flavor-small",
				ImageID:   "img-ubuntu",
				Region:    "eu-north-1",
				CreatedAt: "2025-06-01T12:00:00Z",
			})

		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	c := newTestClient(t, server)

	name := "renamed-vm"
	updateReq := apiUpdateInstanceRequest{
		Name: &name,
	}
	_, err := c.Patch(context.Background(), c.TenantPath("/instances/inst-abc"), updateReq)
	if err != nil {
		t.Fatalf("patch failed: %v", err)
	}

	if !patched {
		t.Error("expected patch to be called")
	}
}

func TestInstanceResize(t *testing.T) {
	var actions []string
	var statusIdx atomic.Int32

	statuses := []string{
		"running",  // initial GET
		"stopping", // after stop action
		"stopped",  // poll -> stopped
		"stopped",  // after resize action (still stopped)
		"starting", // after start action
		"running",  // poll -> running
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/me":
			meHandler(w, r)

		case r.Method == http.MethodPost && r.URL.Path == "/v1/tenants/tenant-456/instances/inst-abc/action":
			var req apiInstanceActionRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatalf("failed to decode action request: %v", err)
			}
			actions = append(actions, req.Action)
			if req.Action == "resize" && req.FlavorID != "flavor-large" {
				t.Errorf("expected flavor_id flavor-large, got %s", req.FlavorID)
			}
			w.WriteHeader(http.StatusAccepted)
			json.NewEncoder(w).Encode(map[string]string{"status": "accepted"})

		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/tenant-456/instances/inst-abc":
			idx := int(statusIdx.Add(1)) - 1
			status := "running"
			if idx < len(statuses) {
				status = statuses[idx]
			}
			json.NewEncoder(w).Encode(apiInstance{
				ID:        "inst-abc",
				Name:      "web-1",
				Status:    status,
				FlavorID:  "flavor-large",
				ImageID:   "img-ubuntu",
				Region:    "eu-north-1",
				CreatedAt: "2025-06-01T12:00:00Z",
			})

		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	c := newTestClient(t, server)
	r := &instanceResource{
		client:       c,
		pollInterval: 10 * time.Millisecond,
		pollTimeout:  5 * time.Second,
	}

	err := r.resizeInstance(context.Background(), "inst-abc", "flavor-large")
	if err != nil {
		t.Fatalf("resize failed: %v", err)
	}

	// Verify the action sequence: stop -> resize -> start.
	if len(actions) != 3 {
		t.Fatalf("expected 3 actions, got %d: %v", len(actions), actions)
	}
	if actions[0] != "stop" {
		t.Errorf("expected first action stop, got %s", actions[0])
	}
	if actions[1] != "resize" {
		t.Errorf("expected second action resize, got %s", actions[1])
	}
	if actions[2] != "start" {
		t.Errorf("expected third action start, got %s", actions[2])
	}
}

func TestInstanceDelete(t *testing.T) {
	deleted := false
	var getCount atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/me":
			meHandler(w, r)

		case r.Method == http.MethodDelete && r.URL.Path == "/v1/tenants/tenant-456/instances/inst-abc":
			deleted = true
			w.WriteHeader(http.StatusAccepted)
			json.NewEncoder(w).Encode(map[string]string{"status": "deleting"})

		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/tenant-456/instances/inst-abc":
			n := getCount.Add(1)
			if n >= 2 {
				// Instance deleted.
				w.WriteHeader(http.StatusNotFound)
				json.NewEncoder(w).Encode(map[string]interface{}{
					"error": map[string]string{
						"code":    "NOT_FOUND",
						"message": "Instance not found",
					},
				})
			} else {
				json.NewEncoder(w).Encode(apiInstance{
					ID:     "inst-abc",
					Name:   "web-1",
					Status: "deleting",
				})
			}

		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	c := newTestClient(t, server)

	// Simulate delete call.
	_, err := c.Delete(context.Background(), c.TenantPath("/instances/inst-abc"))
	if err != nil {
		t.Fatalf("delete failed: %v", err)
	}
	if !deleted {
		t.Error("expected delete to be called")
	}

	// Simulate polling for deletion.
	finalState, err := client.WaitForState(context.Background(), client.PollConfig{
		Interval:     10 * time.Millisecond,
		Timeout:      1 * time.Second,
		TargetStates: []string{"deleted"},
		ErrorStates:  []string{"error"},
		ResourceName: "instance",
		PollFunc: func(ctx context.Context) (string, error) {
			pollResp, pollErr := c.Get(ctx, c.TenantPath("/instances/inst-abc"), nil)
			if pollErr != nil {
				if client.IsNotFound(pollErr) {
					return "deleted", nil
				}
				return "", pollErr
			}
			inst, parseErr := client.ParseResponse[apiInstance](pollResp)
			if parseErr != nil {
				return "", parseErr
			}
			return inst.Status, nil
		},
	})
	if err != nil {
		t.Fatalf("polling failed: %v", err)
	}
	if finalState != "deleted" {
		t.Errorf("expected final state deleted, got %s", finalState)
	}
}

func TestInstanceDeleteAlreadyGone(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/me":
			meHandler(w, r)

		case r.Method == http.MethodDelete && r.URL.Path == "/v1/tenants/tenant-456/instances/gone":
			w.WriteHeader(http.StatusNotFound)
			json.NewEncoder(w).Encode(map[string]interface{}{
				"error": map[string]string{
					"code":    "NOT_FOUND",
					"message": "Instance not found",
				},
			})

		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	defer server.Close()

	c := newTestClient(t, server)

	_, err := c.Delete(context.Background(), c.TenantPath("/instances/gone"))
	if err == nil {
		t.Fatal("expected error")
	}
	if !client.IsNotFound(err) {
		t.Errorf("expected not found error, got %v", err)
	}
}
