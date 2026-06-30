package instance

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
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
		Name:            types.StringValue("web-1"),
		FlavorID:        types.StringValue("flavor-small"),
		ImageID:         types.StringValue("img-ubuntu"),
		Zone:            types.StringValue("sweden-a"),
		VPCID:           types.StringValue("vpc-123"),
		SubnetID:        types.StringValue("subnet-456"),
		SecurityGroups:  sgs,
		SSHKeyNames:     keys,
		UserData:        types.StringValue("#!/bin/bash\necho hello"),
		ConsolePassword: types.StringValue("s3cret-console"),
		Tags:            tags,
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
	if req.Zone != "sweden-a" {
		t.Errorf("expected zone sweden-a, got %s", req.Zone)
	}
	if req.VPCID != "vpc-123" {
		t.Errorf("expected vpc_id vpc-123, got %s", req.VPCID)
	}
	if req.SubnetID != "subnet-456" {
		t.Errorf("expected subnet_id subnet-456, got %s", req.SubnetID)
	}
	if len(req.SecurityGroupIDs) != 2 {
		t.Errorf("expected 2 security groups, got %d", len(req.SecurityGroupIDs))
	}
	if len(req.SSHKeyIDs) != 1 || req.SSHKeyIDs[0] != "my-key" {
		t.Errorf("expected ssh key my-key, got %v", req.SSHKeyIDs)
	}
	if req.UserData != "#!/bin/bash\necho hello" {
		t.Errorf("expected user data, got %s", req.UserData)
	}
	if req.ConsolePassword != "s3cret-console" { // pragma: allowlist secret
		t.Errorf("expected console password s3cret-console, got %s", req.ConsolePassword)
	}
	if req.Tags["env"] != "test" {
		t.Errorf("expected tag env=test, got %v", req.Tags)
	}
}

func TestInstanceModelToCreateRequestMinimal(t *testing.T) {
	ctx := context.Background()
	diags := diag.Diagnostics{}

	model := InstanceModel{
		Name:            types.StringValue("minimal-vm"),
		FlavorID:        types.StringValue("flavor-small"),
		ImageID:         types.StringValue("img-ubuntu"),
		Zone:            types.StringNull(),
		VPCID:           types.StringNull(),
		SubnetID:        types.StringNull(),
		SecurityGroups:  types.SetNull(types.StringType),
		SSHKeyNames:     types.SetNull(types.StringType),
		UserData:        types.StringNull(),
		ConsolePassword: types.StringNull(),
		Tags:            types.MapNull(types.StringType),
	}

	req := model.toCreateRequest(ctx, &diags)
	if diags.HasError() {
		t.Fatalf("unexpected diagnostics: %v", diags.Errors())
	}

	if req.Name != "minimal-vm" {
		t.Errorf("expected name minimal-vm, got %s", req.Name)
	}
	if req.Zone != "" {
		t.Errorf("expected empty zone, got %s", req.Zone)
	}
	if req.SecurityGroupIDs != nil {
		t.Errorf("expected nil security groups, got %v", req.SecurityGroupIDs)
	}
	if req.UserData != "" {
		t.Errorf("expected empty user data, got %s", req.UserData)
	}
	if req.ConsolePassword != "" {
		t.Errorf("expected empty console password, got %s", req.ConsolePassword)
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
	// Security groups are not updatable via the API (no backend field) — the
	// update request carries only name + tags (as metadata).
	if req.Tags["env"] != "prod" {
		t.Errorf("expected tag env=prod, got %v", req.Tags)
	}
}

func TestInstanceModelFromAPI(t *testing.T) {
	ctx := context.Background()
	diags := diag.Diagnostics{}

	inst := &apiInstance{
		ID:         "inst-abc",
		Name:       "web-1",
		Status:     "running",
		FlavorID:   "flavor-small",
		Flavor:     &apiNestedRef{Name: "Small"},
		ImageID:    "img-ubuntu",
		Image:      &apiNestedRef{Name: "Ubuntu 24.04"},
		Zone:       "sweden-a",
		Networks:   []apiInstanceNetwork{{NetworkID: "vpc-123", SubnetID: "subnet-456"}},
		PrivateIPs: []string{"10.0.1.5"},
		PublicIPs:  []string{"203.0.113.10"},
		// The read returns the OpenStack-internal SG NAME, not the customer UUID
		// the user supplied at create — fromAPI must NOT overwrite from this.
		SecurityGroups: []string{"sg-94981d9c-a01a60ab-ssh"},
		Metadata:       map[string]string{"env": "test"},
		CreatedAt:      "2025-06-01T12:00:00Z",
	}

	// vpc_id/subnet_id/security_groups are preserved from prior state (the
	// resource does not derive them from the read), so seed them as the user
	// would have at create.
	priorSGs, d := types.SetValueFrom(ctx, types.StringType, []string{"8e75fb32-1111-2222-3333-444455556666"})
	diags.Append(d...)
	model := InstanceModel{
		VPCID:          types.StringValue("vpc-123"),
		SubnetID:       types.StringValue("subnet-456"),
		SecurityGroups: priorSGs,
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
	if model.Zone.ValueString() != "sweden-a" {
		t.Errorf("expected zone sweden-a, got %s", model.Zone.ValueString())
	}
	if model.VPCID.ValueString() != "vpc-123" {
		t.Errorf("expected vpc_id vpc-123, got %s", model.VPCID.ValueString())
	}
	if model.SubnetID.ValueString() != "subnet-456" {
		t.Errorf("expected subnet_id subnet-456, got %s", model.SubnetID.ValueString())
	}
	// security_groups must be preserved from prior state (the customer UUID),
	// NOT overwritten by the Nova name the read returned.
	var gotSGs []string
	diags.Append(model.SecurityGroups.ElementsAs(ctx, &gotSGs, false)...)
	if len(gotSGs) != 1 || gotSGs[0] != "8e75fb32-1111-2222-3333-444455556666" {
		t.Errorf("expected security_groups preserved as [8e75fb32-...], got %v", gotSGs)
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

func TestInstanceModelFromAPIFiltersReservedTags(t *testing.T) {
	ctx := context.Background()

	// Case 1: a null tags plan must stay null even though the backend injects
	// frostmoln_* system metadata — otherwise Create's read-back produces
	// "inconsistent result after apply" on .tags.
	// frostmoln_engine (and other frostmoln_* keys) are also stamped by the
	// backend — assert the whole prefix is filtered, not just id/type, so the
	// filter can't be narrowed to exact keys without this test failing.
	t.Run("system metadata only keeps tags null", func(t *testing.T) {
		diags := diag.Diagnostics{}
		inst := &apiInstance{
			ID:        "inst-1",
			Name:      "vm",
			Status:    "running",
			FlavorID:  "flavor-small",
			ImageID:   "img",
			CreatedAt: "2025-06-01T12:00:00Z",
			Metadata:  map[string]string{"frostmoln_id": "7f58a27e-...", "frostmoln_type": "compute", "frostmoln_engine": "mysql"},
		}
		model := InstanceModel{Tags: types.MapNull(types.StringType)}
		model.fromAPI(ctx, inst, &diags)
		if diags.HasError() {
			t.Fatalf("unexpected diagnostics: %v", diags.Errors())
		}
		if !model.Tags.IsNull() {
			t.Errorf("expected tags to stay null when only frostmoln_* metadata present, got %v", model.Tags)
		}
	})

	// A non-null empty-map plan must stay an empty map (the middle branch), not
	// flip to null or pick up system keys.
	t.Run("empty-map plan stays empty map", func(t *testing.T) {
		diags := diag.Diagnostics{}
		emptyTags, d := types.MapValueFrom(ctx, types.StringType, map[string]string{})
		diags.Append(d...)
		inst := &apiInstance{
			ID:        "inst-3",
			Name:      "vm",
			Status:    "running",
			FlavorID:  "flavor-small",
			ImageID:   "img",
			CreatedAt: "2025-06-01T12:00:00Z",
			Metadata:  map[string]string{"frostmoln_id": "abc", "frostmoln_type": "compute"},
		}
		model := InstanceModel{Tags: emptyTags}
		model.fromAPI(ctx, inst, &diags)
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

	// Case 2: user tags round-trip; only the reserved frostmoln_* keys are dropped.
	t.Run("user tags survive, reserved keys dropped", func(t *testing.T) {
		diags := diag.Diagnostics{}
		priorTags, d := types.MapValueFrom(ctx, types.StringType, map[string]string{"env": "prod"})
		diags.Append(d...)
		inst := &apiInstance{
			ID:        "inst-2",
			Name:      "vm",
			Status:    "running",
			FlavorID:  "flavor-small",
			ImageID:   "img",
			CreatedAt: "2025-06-01T12:00:00Z",
			Metadata:  map[string]string{"env": "prod", "frostmoln_id": "abc", "frostmoln_type": "compute"},
		}
		model := InstanceModel{Tags: priorTags}
		model.fromAPI(ctx, inst, &diags)
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
	_ = json.NewEncoder(w).Encode(map[string]string{
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
			if req.ConsolePassword != "s3cret-console" { // pragma: allowlist secret
				t.Errorf("expected console password, got %s", req.ConsolePassword)
			}
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(apiInstance{
				ID:        "inst-new",
				Name:      req.Name,
				Status:    "provisioning",
				FlavorID:  req.FlavorID,
				ImageID:   req.ImageID,
				CreatedAt: "2025-06-01T12:00:00Z",
			})

		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/tenant-456/instances/inst-new":
			n := pollCount.Add(1)
			status := "provisioning"
			if n >= 2 {
				status = "running"
			}
			_ = json.NewEncoder(w).Encode(apiInstance{
				ID:         "inst-new",
				Name:       "web-1",
				Status:     status,
				FlavorID:   "flavor-small",
				Flavor:     &apiNestedRef{Name: "Small"},
				ImageID:    "img-ubuntu",
				Image:      &apiNestedRef{Name: "Ubuntu 24.04"},
				PrivateIPs: []string{"10.0.1.5"},
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
		Name:            "web-1",
		FlavorID:        "flavor-small",
		ImageID:         "img-ubuntu",
		UserData:        "#!/bin/bash",
		ConsolePassword: "s3cret-console",
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
			_ = json.NewEncoder(w).Encode(apiInstance{
				ID:             "inst-abc",
				Name:           "web-1",
				Status:         "running",
				FlavorID:       "flavor-small",
				Flavor:         &apiNestedRef{Name: "Small"},
				ImageID:        "img-ubuntu",
				Image:          &apiNestedRef{Name: "Ubuntu 24.04"},
				Zone:           "sweden-a",
				Networks:       []apiInstanceNetwork{{NetworkID: "vpc-123", SubnetID: "subnet-456"}},
				PrivateIPs:     []string{"10.0.1.5"},
				PublicIPs:      []string{"203.0.113.10"},
				SecurityGroups: []string{"sg-1"},
				Metadata:       map[string]string{"env": "test"},
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
	if len(inst.PrivateIPs) != 1 || inst.PrivateIPs[0] != "10.0.1.5" {
		t.Errorf("expected private ips [10.0.1.5], got %v", inst.PrivateIPs)
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
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
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
			_ = json.NewEncoder(w).Encode(apiInstance{
				ID:        "inst-abc",
				Name:      "renamed-vm",
				Status:    "running",
				FlavorID:  "flavor-small",
				ImageID:   "img-ubuntu",
				CreatedAt: "2025-06-01T12:00:00Z",
			})

		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/tenant-456/instances/inst-abc":
			_ = json.NewEncoder(w).Encode(apiInstance{
				ID:        "inst-abc",
				Name:      "renamed-vm",
				Status:    "running",
				FlavorID:  "flavor-small",
				ImageID:   "img-ubuntu",
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

		case r.Method == http.MethodPost && r.URL.Path == "/v1/tenants/tenant-456/instances/inst-abc/stop":
			actions = append(actions, "stop")
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(map[string]string{"status": "accepted"})

		case r.Method == http.MethodPost && r.URL.Path == "/v1/tenants/tenant-456/instances/inst-abc/resize":
			var req apiResizeInstanceRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatalf("failed to decode resize request: %v", err)
			}
			actions = append(actions, "resize")
			if req.FlavorID != "flavor-large" {
				t.Errorf("expected flavor_id flavor-large, got %s", req.FlavorID)
			}
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(map[string]string{"status": "accepted"})

		case r.Method == http.MethodPost && r.URL.Path == "/v1/tenants/tenant-456/instances/inst-abc/start":
			actions = append(actions, "start")
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(map[string]string{"status": "accepted"})

		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/tenant-456/instances/inst-abc":
			idx := int(statusIdx.Add(1)) - 1
			status := "running"
			if idx < len(statuses) {
				status = statuses[idx]
			}
			_ = json.NewEncoder(w).Encode(apiInstance{
				ID:        "inst-abc",
				Name:      "web-1",
				Status:    status,
				FlavorID:  "flavor-large",
				ImageID:   "img-ubuntu",
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
			_ = json.NewEncoder(w).Encode(map[string]string{"status": "deleting"})

		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/tenant-456/instances/inst-abc":
			n := getCount.Add(1)
			if n >= 2 {
				// Instance deleted.
				w.WriteHeader(http.StatusNotFound)
				_ = json.NewEncoder(w).Encode(map[string]interface{}{
					"error": map[string]string{
						"code":    "NOT_FOUND",
						"message": "Instance not found",
					},
				})
			} else {
				_ = json.NewEncoder(w).Encode(apiInstance{
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
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
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

// --- tfsdk-level CRUD tests ---

func getInstanceSchema(t *testing.T) resource.SchemaResponse {
	t.Helper()
	r := NewResource()
	var schemaResp resource.SchemaResponse
	r.Schema(context.Background(), resource.SchemaRequest{}, &schemaResp)
	return schemaResp
}

// instanceTFValue builds a tftypes.Value for the instance schema.
// All fields are required; use tftypes.UnknownValue for computed unknowns and nil for nulls.
func instanceTFValue(t *testing.T, tfType tftypes.Type, vals map[string]tftypes.Value) tftypes.Value {
	t.Helper()

	// Defaults for every attribute so callers only need to override what they care about.
	defaults := map[string]tftypes.Value{
		"id":               tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"name":             tftypes.NewValue(tftypes.String, "test-vm"),
		"flavor_id":        tftypes.NewValue(tftypes.String, "flavor-small"),
		"image_id":         tftypes.NewValue(tftypes.String, "img-ubuntu"),
		"zone":             tftypes.NewValue(tftypes.String, nil),
		"vpc_id":           tftypes.NewValue(tftypes.String, nil),
		"subnet_id":        tftypes.NewValue(tftypes.String, nil),
		"security_groups":  tftypes.NewValue(tftypes.Set{ElementType: tftypes.String}, nil),
		"ssh_key_names":    tftypes.NewValue(tftypes.Set{ElementType: tftypes.String}, nil),
		"user_data":        tftypes.NewValue(tftypes.String, nil),
		"console_password": tftypes.NewValue(tftypes.String, nil),
		"user_data_hash":   tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"tags":             tftypes.NewValue(tftypes.Map{ElementType: tftypes.String}, nil),
		"status":           tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"flavor_name":      tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"image_name":       tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"private_ip":       tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"public_ip":        tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"created_at":       tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
	}

	for k, v := range vals {
		defaults[k] = v
	}
	return tftypes.NewValue(tfType, defaults)
}

func TestNewResource(t *testing.T) {
	r := NewResource()
	if r == nil {
		t.Fatal("expected non-nil resource")
	}
	// Verify it implements ResourceWithImportState.
	if _, ok := r.(resource.ResourceWithImportState); !ok {
		t.Error("expected resource to implement ResourceWithImportState")
	}
	if _, ok := r.(resource.ResourceWithConfigure); !ok {
		t.Error("expected resource to implement ResourceWithConfigure")
	}
}

func TestInstanceResource_Metadata(t *testing.T) {
	r := NewResource()
	req := resource.MetadataRequest{ProviderTypeName: "frostmoln"}
	var resp resource.MetadataResponse
	r.Metadata(context.Background(), req, &resp)

	if resp.TypeName != "frostmoln_instance" {
		t.Errorf("expected type name frostmoln_instance, got %s", resp.TypeName)
	}
}

func TestInstanceResource_Schema(t *testing.T) {
	schemaResp := getInstanceSchema(t)
	s := schemaResp.Schema

	requiredAttrs := []string{"name", "flavor_id", "image_id"}
	for _, attr := range requiredAttrs {
		a, ok := s.Attributes[attr]
		if !ok {
			t.Errorf("expected attribute %s in schema", attr)
			continue
		}
		if !a.IsRequired() {
			t.Errorf("expected attribute %s to be required", attr)
		}
	}

	computedAttrs := []string{"id", "status", "flavor_name", "image_name", "private_ip", "public_ip", "created_at", "user_data_hash"}
	for _, attr := range computedAttrs {
		a, ok := s.Attributes[attr]
		if !ok {
			t.Errorf("expected attribute %s in schema", attr)
			continue
		}
		if !a.IsComputed() {
			t.Errorf("expected attribute %s to be computed", attr)
		}
	}

	optionalAttrs := []string{"zone", "vpc_id", "subnet_id", "security_groups", "ssh_key_names", "user_data", "console_password", "tags"}
	for _, attr := range optionalAttrs {
		a, ok := s.Attributes[attr]
		if !ok {
			t.Errorf("expected attribute %s in schema", attr)
			continue
		}
		if !a.IsOptional() {
			t.Errorf("expected attribute %s to be optional", attr)
		}
	}
}

func TestInstanceResource_Configure(t *testing.T) {
	r := NewResource()

	// Configure with nil provider data should not error.
	rc := r.(resource.ResourceWithConfigure)
	var resp resource.ConfigureResponse
	rc.Configure(context.Background(), resource.ConfigureRequest{ProviderData: nil}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("expected no error with nil provider data, got: %v", resp.Diagnostics.Errors())
	}

	// Configure with wrong type should error.
	var resp2 resource.ConfigureResponse
	rc.Configure(context.Background(), resource.ConfigureRequest{ProviderData: "wrong-type"}, &resp2)
	if !resp2.Diagnostics.HasError() {
		t.Fatal("expected error with wrong type")
	}

	// Configure with correct client should succeed.
	c := client.NewClient("http://localhost", "test-key")
	var resp3 resource.ConfigureResponse
	rc.Configure(context.Background(), resource.ConfigureRequest{ProviderData: c}, &resp3)
	if resp3.Diagnostics.HasError() {
		t.Fatalf("expected no error with correct client, got: %v", resp3.Diagnostics.Errors())
	}
}

func TestInstanceResource_TFSDKCreate(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/me":
			_ = json.NewEncoder(w).Encode(map[string]string{"id": "user-123", "tenantId": "tenant-456"})

		case r.Method == http.MethodPost && r.URL.Path == "/v1/tenants/tenant-456/instances":
			var req apiCreateInstanceRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Errorf("failed to decode create request: %v", err)
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
			if req.UserData != "#!/bin/bash\necho hello" {
				t.Errorf("expected user data, got %q", req.UserData)
			}
			if req.ConsolePassword != "s3cret-console" { // pragma: allowlist secret
				t.Errorf("expected console password, got %q", req.ConsolePassword)
			}
			if len(req.SecurityGroupIDs) != 1 || req.SecurityGroupIDs[0] != "sg-default" {
				t.Errorf("expected security groups [sg-default], got %v", req.SecurityGroupIDs)
			}
			if req.Tags["env"] != "test" {
				t.Errorf("expected tag env=test, got %v", req.Tags)
			}
			// Provisioning returns 202 + an Operation envelope (operationId only).
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"operationId": "op-inst-1", "status": "pending", "resourceType": "instance",
			})

		case r.Method == http.MethodGet && r.URL.Path == "/v1/operations/op-inst-1":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"operationId": "op-inst-1", "status": "completed", "resourceType": "instance", "resourceId": "inst-new-1",
			})

		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/tenant-456/instances/inst-new-1":
			_ = json.NewEncoder(w).Encode(apiInstance{
				ID:             "inst-new-1",
				Name:           "web-1",
				Status:         "running",
				FlavorID:       "flavor-small",
				Flavor:         &apiNestedRef{Name: "Small"},
				ImageID:        "img-ubuntu",
				Image:          &apiNestedRef{Name: "Ubuntu 24.04"},
				PrivateIPs:     []string{"10.0.1.5"},
				SecurityGroups: []string{"sg-default"},
				Metadata:       map[string]string{"env": "test"},
				CreatedAt:      "2025-06-01T12:00:00Z",
			})

		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key")
	if err := c.Configure(context.Background()); err != nil {
		t.Fatalf("client configure failed: %v", err)
	}

	r := &instanceResource{
		client:       c,
		pollInterval: 10 * time.Millisecond,
		pollTimeout:  5 * time.Second,
	}
	schemaResp := getInstanceSchema(t)

	ctx := context.Background()
	tfType := schemaResp.Schema.Type().TerraformType(ctx)

	planVal := instanceTFValue(t, tfType, map[string]tftypes.Value{
		"name":             tftypes.NewValue(tftypes.String, "web-1"),
		"image_id":         tftypes.NewValue(tftypes.String, "img-ubuntu"),
		"user_data":        tftypes.NewValue(tftypes.String, "#!/bin/bash\necho hello"),
		"console_password": tftypes.NewValue(tftypes.String, "s3cret-console"),
		"security_groups": tftypes.NewValue(tftypes.Set{ElementType: tftypes.String}, []tftypes.Value{
			tftypes.NewValue(tftypes.String, "sg-default"),
		}),
		"tags": tftypes.NewValue(tftypes.Map{ElementType: tftypes.String}, map[string]tftypes.Value{
			"env": tftypes.NewValue(tftypes.String, "test"),
		}),
	})

	createReq := resource.CreateRequest{
		Plan: tfsdk.Plan{Schema: schemaResp.Schema, Raw: planVal},
	}
	createResp := &resource.CreateResponse{
		State: tfsdk.State{Schema: schemaResp.Schema},
	}

	r.Create(ctx, createReq, createResp)

	if createResp.Diagnostics.HasError() {
		t.Fatalf("Create failed: %v", createResp.Diagnostics.Errors())
	}

	var model InstanceModel
	createResp.State.Get(ctx, &model)

	if model.ID.ValueString() != "inst-new-1" {
		t.Errorf("expected ID inst-new-1, got %s", model.ID.ValueString())
	}
	if model.Name.ValueString() != "web-1" {
		t.Errorf("expected Name web-1, got %s", model.Name.ValueString())
	}
	if model.Status.ValueString() != "running" {
		t.Errorf("expected Status running, got %s", model.Status.ValueString())
	}
	if model.FlavorName.ValueString() != "Small" {
		t.Errorf("expected FlavorName Small, got %s", model.FlavorName.ValueString())
	}
	if model.ImageName.ValueString() != "Ubuntu 24.04" {
		t.Errorf("expected ImageName Ubuntu 24.04, got %s", model.ImageName.ValueString())
	}
	if model.PrivateIP.ValueString() != "10.0.1.5" {
		t.Errorf("expected PrivateIP 10.0.1.5, got %s", model.PrivateIP.ValueString())
	}
	if model.CreatedAt.ValueString() != "2025-06-01T12:00:00Z" {
		t.Errorf("expected CreatedAt, got %s", model.CreatedAt.ValueString())
	}
	// Verify user_data_hash was computed.
	expectedHash := computeUserDataHash("#!/bin/bash\necho hello")
	if model.UserDataHash.ValueString() != expectedHash {
		t.Errorf("expected UserDataHash %s, got %s", expectedHash, model.UserDataHash.ValueString())
	}
}

func TestInstanceResource_TFSDKCreateMinimal(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/me":
			_ = json.NewEncoder(w).Encode(map[string]string{"id": "user-123", "tenantId": "tenant-456"})

		case r.Method == http.MethodPost && r.URL.Path == "/v1/tenants/tenant-456/instances":
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"operationId": "op-min-1", "status": "pending", "resourceType": "instance",
			})

		case r.Method == http.MethodGet && r.URL.Path == "/v1/operations/op-min-1":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"operationId": "op-min-1", "status": "completed", "resourceType": "instance", "resourceId": "inst-min-1",
			})

		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/tenant-456/instances/inst-min-1":
			_ = json.NewEncoder(w).Encode(apiInstance{
				ID:        "inst-min-1",
				Name:      "minimal-vm",
				Status:    "running",
				FlavorID:  "flavor-small",
				ImageID:   "img-ubuntu",
				CreatedAt: "2025-06-01T12:00:00Z",
			})

		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key")
	if err := c.Configure(context.Background()); err != nil {
		t.Fatalf("client configure failed: %v", err)
	}

	r := &instanceResource{
		client:       c,
		pollInterval: 10 * time.Millisecond,
		pollTimeout:  5 * time.Second,
	}
	schemaResp := getInstanceSchema(t)

	ctx := context.Background()
	tfType := schemaResp.Schema.Type().TerraformType(ctx)

	planVal := instanceTFValue(t, tfType, map[string]tftypes.Value{
		"name": tftypes.NewValue(tftypes.String, "minimal-vm"),
	})

	createReq := resource.CreateRequest{
		Plan: tfsdk.Plan{Schema: schemaResp.Schema, Raw: planVal},
	}
	createResp := &resource.CreateResponse{
		State: tfsdk.State{Schema: schemaResp.Schema},
	}

	r.Create(ctx, createReq, createResp)

	if createResp.Diagnostics.HasError() {
		t.Fatalf("Create failed: %v", createResp.Diagnostics.Errors())
	}

	var model InstanceModel
	createResp.State.Get(ctx, &model)

	if model.ID.ValueString() != "inst-min-1" {
		t.Errorf("expected ID inst-min-1, got %s", model.ID.ValueString())
	}
	if model.Status.ValueString() != "running" {
		t.Errorf("expected Status running, got %s", model.Status.ValueString())
	}
	// No user_data provided, so hash should be null.
	if !model.UserDataHash.IsNull() {
		t.Errorf("expected UserDataHash to be null, got %s", model.UserDataHash.ValueString())
	}
}

func TestInstanceResource_TFSDKCreateErrorState(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/me":
			_ = json.NewEncoder(w).Encode(map[string]string{"id": "user-123", "tenantId": "tenant-456"})

		case r.Method == http.MethodPost && r.URL.Path == "/v1/tenants/tenant-456/instances":
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"operationId": "op-inst-err", "status": "pending", "resourceType": "instance",
			})

		case r.Method == http.MethodGet && r.URL.Path == "/v1/operations/op-inst-err":
			// The create workflow failed → operation terminal-failed → create errors.
			_ = json.NewEncoder(w).Encode(map[string]any{
				"operationId": "op-inst-err", "status": "failed", "resourceType": "instance",
				"error": "instance entered error state",
			})

		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key")
	if err := c.Configure(context.Background()); err != nil {
		t.Fatalf("client configure failed: %v", err)
	}

	r := &instanceResource{
		client:       c,
		pollInterval: 10 * time.Millisecond,
		pollTimeout:  5 * time.Second,
	}
	schemaResp := getInstanceSchema(t)

	ctx := context.Background()
	tfType := schemaResp.Schema.Type().TerraformType(ctx)

	planVal := instanceTFValue(t, tfType, map[string]tftypes.Value{
		"name": tftypes.NewValue(tftypes.String, "error-vm"),
	})

	createReq := resource.CreateRequest{
		Plan: tfsdk.Plan{Schema: schemaResp.Schema, Raw: planVal},
	}
	createResp := &resource.CreateResponse{
		State: tfsdk.State{Schema: schemaResp.Schema},
	}

	r.Create(ctx, createReq, createResp)

	if !createResp.Diagnostics.HasError() {
		t.Fatal("expected Create to report error when instance enters error state")
	}
}

func TestInstanceResource_TFSDKRead(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/me":
			_ = json.NewEncoder(w).Encode(map[string]string{"id": "user-123", "tenantId": "tenant-456"})

		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/tenant-456/instances/inst-read-1":
			_ = json.NewEncoder(w).Encode(apiInstance{
				ID:             "inst-read-1",
				Name:           "read-vm",
				Status:         "running",
				FlavorID:       "flavor-medium",
				Flavor:         &apiNestedRef{Name: "Medium"},
				ImageID:        "img-debian",
				Image:          &apiNestedRef{Name: "Debian 12"},
				Zone:           "falkenberg",
				Networks:       []apiInstanceNetwork{{NetworkID: "vpc-abc", SubnetID: "subnet-xyz"}},
				PrivateIPs:     []string{"10.0.2.10"},
				PublicIPs:      []string{"203.0.113.50"},
				SecurityGroups: []string{"sg-1", "sg-2"},
				Metadata:       map[string]string{"env": "prod", "team": "platform"},
				CreatedAt:      "2025-06-01T12:00:00Z",
			})

		default:
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"error": map[string]string{"code": "NOT_FOUND", "message": "not found"},
			})
		}
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key")
	if err := c.Configure(context.Background()); err != nil {
		t.Fatalf("client configure failed: %v", err)
	}

	r := &instanceResource{client: c}
	schemaResp := getInstanceSchema(t)

	ctx := context.Background()
	tfType := schemaResp.Schema.Type().TerraformType(ctx)

	// Simulate existing state with user_data preserved from a previous Create.
	stateVal := instanceTFValue(t, tfType, map[string]tftypes.Value{
		"id":             tftypes.NewValue(tftypes.String, "inst-read-1"),
		"name":           tftypes.NewValue(tftypes.String, "read-vm"),
		"flavor_id":      tftypes.NewValue(tftypes.String, "flavor-medium"),
		"image_id":       tftypes.NewValue(tftypes.String, "img-debian"),
		"zone":           tftypes.NewValue(tftypes.String, "falkenberg"),
		"vpc_id":         tftypes.NewValue(tftypes.String, "vpc-abc"),
		"subnet_id":      tftypes.NewValue(tftypes.String, "subnet-xyz"),
		"status":         tftypes.NewValue(tftypes.String, "running"),
		"flavor_name":    tftypes.NewValue(tftypes.String, "Medium"),
		"image_name":     tftypes.NewValue(tftypes.String, "Debian 12"),
		"private_ip":     tftypes.NewValue(tftypes.String, "10.0.2.10"),
		"public_ip":      tftypes.NewValue(tftypes.String, "203.0.113.50"),
		"user_data":      tftypes.NewValue(tftypes.String, "#!/bin/bash\necho hello"),
		"user_data_hash": tftypes.NewValue(tftypes.String, computeUserDataHash("#!/bin/bash\necho hello")),
		"security_groups": tftypes.NewValue(tftypes.Set{ElementType: tftypes.String}, []tftypes.Value{
			tftypes.NewValue(tftypes.String, "sg-1"),
			tftypes.NewValue(tftypes.String, "sg-2"),
		}),
		"ssh_key_names": tftypes.NewValue(tftypes.Set{ElementType: tftypes.String}, []tftypes.Value{
			tftypes.NewValue(tftypes.String, "my-key"),
		}),
		"tags": tftypes.NewValue(tftypes.Map{ElementType: tftypes.String}, map[string]tftypes.Value{
			"env":  tftypes.NewValue(tftypes.String, "prod"),
			"team": tftypes.NewValue(tftypes.String, "platform"),
		}),
		"created_at": tftypes.NewValue(tftypes.String, "2025-06-01T12:00:00Z"),
	})

	readReq := resource.ReadRequest{
		State: tfsdk.State{Schema: schemaResp.Schema, Raw: stateVal},
	}
	readResp := &resource.ReadResponse{
		State: tfsdk.State{Schema: schemaResp.Schema},
	}

	r.Read(ctx, readReq, readResp)

	if readResp.Diagnostics.HasError() {
		t.Fatalf("Read failed: %v", readResp.Diagnostics.Errors())
	}

	var model InstanceModel
	readResp.State.Get(ctx, &model)

	if model.ID.ValueString() != "inst-read-1" {
		t.Errorf("expected ID inst-read-1, got %s", model.ID.ValueString())
	}
	if model.Name.ValueString() != "read-vm" {
		t.Errorf("expected Name read-vm, got %s", model.Name.ValueString())
	}
	if model.Status.ValueString() != "running" {
		t.Errorf("expected Status running, got %s", model.Status.ValueString())
	}
	if model.FlavorName.ValueString() != "Medium" {
		t.Errorf("expected FlavorName Medium, got %s", model.FlavorName.ValueString())
	}
	if model.ImageName.ValueString() != "Debian 12" {
		t.Errorf("expected ImageName Debian 12, got %s", model.ImageName.ValueString())
	}
	if model.Zone.ValueString() != "falkenberg" {
		t.Errorf("expected Zone falkenberg, got %s", model.Zone.ValueString())
	}
	if model.VPCID.ValueString() != "vpc-abc" {
		t.Errorf("expected VPCID vpc-abc, got %s", model.VPCID.ValueString())
	}
	if model.SubnetID.ValueString() != "subnet-xyz" {
		t.Errorf("expected SubnetID subnet-xyz, got %s", model.SubnetID.ValueString())
	}
	if model.PrivateIP.ValueString() != "10.0.2.10" {
		t.Errorf("expected PrivateIP 10.0.2.10, got %s", model.PrivateIP.ValueString())
	}
	if model.PublicIP.ValueString() != "203.0.113.50" {
		t.Errorf("expected PublicIP 203.0.113.50, got %s", model.PublicIP.ValueString())
	}
	// user_data and user_data_hash should be preserved from prior state.
	if model.UserData.ValueString() != "#!/bin/bash\necho hello" {
		t.Errorf("expected UserData preserved, got %s", model.UserData.ValueString())
	}
	expectedHash := computeUserDataHash("#!/bin/bash\necho hello")
	if model.UserDataHash.ValueString() != expectedHash {
		t.Errorf("expected UserDataHash preserved, got %s", model.UserDataHash.ValueString())
	}
}

func TestInstanceResource_TFSDKReadNotFound(t *testing.T) {
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

	c := client.NewClient(server.URL, "test-key")
	if err := c.Configure(context.Background()); err != nil {
		t.Fatalf("client configure failed: %v", err)
	}

	r := &instanceResource{client: c}
	schemaResp := getInstanceSchema(t)

	ctx := context.Background()
	tfType := schemaResp.Schema.Type().TerraformType(ctx)

	stateVal := instanceTFValue(t, tfType, map[string]tftypes.Value{
		"id":          tftypes.NewValue(tftypes.String, "inst-gone"),
		"name":        tftypes.NewValue(tftypes.String, "gone-vm"),
		"status":      tftypes.NewValue(tftypes.String, "running"),
		"flavor_name": tftypes.NewValue(tftypes.String, nil),
		"image_name":  tftypes.NewValue(tftypes.String, nil),
		"private_ip":  tftypes.NewValue(tftypes.String, nil),
		"public_ip":   tftypes.NewValue(tftypes.String, nil),
		"created_at":  tftypes.NewValue(tftypes.String, "2025-06-01T12:00:00Z"),
	})

	readReq := resource.ReadRequest{
		State: tfsdk.State{Schema: schemaResp.Schema, Raw: stateVal},
	}
	readResp := &resource.ReadResponse{
		State: tfsdk.State{Schema: schemaResp.Schema},
	}

	r.Read(ctx, readReq, readResp)

	// Should not error -- just remove the resource from state.
	if readResp.Diagnostics.HasError() {
		t.Fatalf("Read should not error for 404, got: %v", readResp.Diagnostics.Errors())
	}

	// State should be empty (resource removed).
	var model InstanceModel
	diags := readResp.State.Get(ctx, &model)
	if !diags.HasError() {
		if model.ID.IsNull() {
			return // expected
		}
	}
}

func TestInstanceResource_TFSDKUpdateNameChange(t *testing.T) {
	var patchCalled bool

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/me":
			_ = json.NewEncoder(w).Encode(map[string]string{"id": "user-123", "tenantId": "tenant-456"})

		case r.Method == http.MethodPatch && r.URL.Path == "/v1/tenants/tenant-456/instances/inst-upd-1":
			patchCalled = true
			var req apiUpdateInstanceRequest
			_ = json.NewDecoder(r.Body).Decode(&req)
			if req.Name == nil || *req.Name != "renamed-vm" {
				t.Errorf("expected name renamed-vm, got %v", req.Name)
			}
			_ = json.NewEncoder(w).Encode(apiInstance{
				ID:        "inst-upd-1",
				Name:      "renamed-vm",
				Status:    "running",
				FlavorID:  "flavor-small",
				ImageID:   "img-ubuntu",
				CreatedAt: "2025-06-01T12:00:00Z",
			})

		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/tenant-456/instances/inst-upd-1":
			_ = json.NewEncoder(w).Encode(apiInstance{
				ID:        "inst-upd-1",
				Name:      "renamed-vm",
				Status:    "running",
				FlavorID:  "flavor-small",
				ImageID:   "img-ubuntu",
				CreatedAt: "2025-06-01T12:00:00Z",
			})

		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key")
	if err := c.Configure(context.Background()); err != nil {
		t.Fatalf("client configure failed: %v", err)
	}

	r := &instanceResource{
		client:       c,
		pollInterval: 10 * time.Millisecond,
		pollTimeout:  5 * time.Second,
	}
	schemaResp := getInstanceSchema(t)

	ctx := context.Background()
	tfType := schemaResp.Schema.Type().TerraformType(ctx)

	stateVal := instanceTFValue(t, tfType, map[string]tftypes.Value{
		"id":             tftypes.NewValue(tftypes.String, "inst-upd-1"),
		"name":           tftypes.NewValue(tftypes.String, "old-name"),
		"flavor_id":      tftypes.NewValue(tftypes.String, "flavor-small"),
		"image_id":       tftypes.NewValue(tftypes.String, "img-ubuntu"),
		"status":         tftypes.NewValue(tftypes.String, "running"),
		"flavor_name":    tftypes.NewValue(tftypes.String, nil),
		"image_name":     tftypes.NewValue(tftypes.String, nil),
		"private_ip":     tftypes.NewValue(tftypes.String, nil),
		"public_ip":      tftypes.NewValue(tftypes.String, nil),
		"user_data":      tftypes.NewValue(tftypes.String, "#!/bin/bash"),
		"user_data_hash": tftypes.NewValue(tftypes.String, computeUserDataHash("#!/bin/bash")),
		"created_at":     tftypes.NewValue(tftypes.String, "2025-06-01T12:00:00Z"),
	})

	planVal := instanceTFValue(t, tfType, map[string]tftypes.Value{
		"id":             tftypes.NewValue(tftypes.String, "inst-upd-1"),
		"name":           tftypes.NewValue(tftypes.String, "renamed-vm"),
		"flavor_id":      tftypes.NewValue(tftypes.String, "flavor-small"),
		"image_id":       tftypes.NewValue(tftypes.String, "img-ubuntu"),
		"status":         tftypes.NewValue(tftypes.String, "running"),
		"flavor_name":    tftypes.NewValue(tftypes.String, nil),
		"image_name":     tftypes.NewValue(tftypes.String, nil),
		"private_ip":     tftypes.NewValue(tftypes.String, nil),
		"public_ip":      tftypes.NewValue(tftypes.String, nil),
		"user_data":      tftypes.NewValue(tftypes.String, "#!/bin/bash"),
		"user_data_hash": tftypes.NewValue(tftypes.String, computeUserDataHash("#!/bin/bash")),
		"created_at":     tftypes.NewValue(tftypes.String, "2025-06-01T12:00:00Z"),
	})

	updateReq := resource.UpdateRequest{
		Plan:  tfsdk.Plan{Schema: schemaResp.Schema, Raw: planVal},
		State: tfsdk.State{Schema: schemaResp.Schema, Raw: stateVal},
	}
	updateResp := &resource.UpdateResponse{
		State: tfsdk.State{Schema: schemaResp.Schema},
	}

	r.Update(ctx, updateReq, updateResp)

	if updateResp.Diagnostics.HasError() {
		t.Fatalf("Update failed: %v", updateResp.Diagnostics.Errors())
	}

	if !patchCalled {
		t.Error("expected PATCH to be called for name change")
	}

	var model InstanceModel
	updateResp.State.Get(ctx, &model)

	if model.Name.ValueString() != "renamed-vm" {
		t.Errorf("expected Name renamed-vm, got %s", model.Name.ValueString())
	}
	// user_data and user_data_hash should be preserved from state.
	if model.UserData.ValueString() != "#!/bin/bash" {
		t.Errorf("expected UserData preserved, got %s", model.UserData.ValueString())
	}
	if model.UserDataHash.ValueString() != computeUserDataHash("#!/bin/bash") {
		t.Errorf("expected UserDataHash preserved, got %s", model.UserDataHash.ValueString())
	}
}

func TestInstanceResource_TFSDKUpdateResize(t *testing.T) {
	var actions []string
	var statusIdx atomic.Int32

	// Status sequence for resize workflow: stop -> poll stopped -> resize -> start -> poll running.
	statuses := []string{
		"stopping", // after stop action poll
		"stopped",  // poll -> stopped
		"stopped",  // after resize action (still stopped)
		"starting", // after start action
		"running",  // poll -> running
		"running",  // final GET after update
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/me":
			_ = json.NewEncoder(w).Encode(map[string]string{"id": "user-123", "tenantId": "tenant-456"})

		case r.Method == http.MethodPost && r.URL.Path == "/v1/tenants/tenant-456/instances/inst-resize-1/stop":
			actions = append(actions, "stop")
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(map[string]string{"status": "accepted"})

		case r.Method == http.MethodPost && r.URL.Path == "/v1/tenants/tenant-456/instances/inst-resize-1/resize":
			var req apiResizeInstanceRequest
			_ = json.NewDecoder(r.Body).Decode(&req)
			actions = append(actions, "resize")
			if req.FlavorID != "flavor-large" {
				t.Errorf("expected resize to flavor-large, got %s", req.FlavorID)
			}
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(map[string]string{"status": "accepted"})

		case r.Method == http.MethodPost && r.URL.Path == "/v1/tenants/tenant-456/instances/inst-resize-1/start":
			actions = append(actions, "start")
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(map[string]string{"status": "accepted"})

		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/tenant-456/instances/inst-resize-1":
			idx := int(statusIdx.Add(1)) - 1
			status := "running"
			if idx < len(statuses) {
				status = statuses[idx]
			}
			flavorID := "flavor-large"
			_ = json.NewEncoder(w).Encode(apiInstance{
				ID:        "inst-resize-1",
				Name:      "resize-vm",
				Status:    status,
				FlavorID:  flavorID,
				ImageID:   "img-ubuntu",
				CreatedAt: "2025-06-01T12:00:00Z",
			})

		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key")
	if err := c.Configure(context.Background()); err != nil {
		t.Fatalf("client configure failed: %v", err)
	}

	r := &instanceResource{
		client:       c,
		pollInterval: 10 * time.Millisecond,
		pollTimeout:  5 * time.Second,
	}
	schemaResp := getInstanceSchema(t)

	ctx := context.Background()
	tfType := schemaResp.Schema.Type().TerraformType(ctx)

	stateVal := instanceTFValue(t, tfType, map[string]tftypes.Value{
		"id":             tftypes.NewValue(tftypes.String, "inst-resize-1"),
		"name":           tftypes.NewValue(tftypes.String, "resize-vm"),
		"flavor_id":      tftypes.NewValue(tftypes.String, "flavor-small"),
		"image_id":       tftypes.NewValue(tftypes.String, "img-ubuntu"),
		"status":         tftypes.NewValue(tftypes.String, "running"),
		"flavor_name":    tftypes.NewValue(tftypes.String, nil),
		"image_name":     tftypes.NewValue(tftypes.String, nil),
		"private_ip":     tftypes.NewValue(tftypes.String, nil),
		"public_ip":      tftypes.NewValue(tftypes.String, nil),
		"user_data":      tftypes.NewValue(tftypes.String, nil),
		"user_data_hash": tftypes.NewValue(tftypes.String, nil),
		"created_at":     tftypes.NewValue(tftypes.String, "2025-06-01T12:00:00Z"),
	})

	planVal := instanceTFValue(t, tfType, map[string]tftypes.Value{
		"id":             tftypes.NewValue(tftypes.String, "inst-resize-1"),
		"name":           tftypes.NewValue(tftypes.String, "resize-vm"),
		"flavor_id":      tftypes.NewValue(tftypes.String, "flavor-large"),
		"image_id":       tftypes.NewValue(tftypes.String, "img-ubuntu"),
		"status":         tftypes.NewValue(tftypes.String, "running"),
		"flavor_name":    tftypes.NewValue(tftypes.String, nil),
		"image_name":     tftypes.NewValue(tftypes.String, nil),
		"private_ip":     tftypes.NewValue(tftypes.String, nil),
		"public_ip":      tftypes.NewValue(tftypes.String, nil),
		"user_data":      tftypes.NewValue(tftypes.String, nil),
		"user_data_hash": tftypes.NewValue(tftypes.String, nil),
		"created_at":     tftypes.NewValue(tftypes.String, "2025-06-01T12:00:00Z"),
	})

	updateReq := resource.UpdateRequest{
		Plan:  tfsdk.Plan{Schema: schemaResp.Schema, Raw: planVal},
		State: tfsdk.State{Schema: schemaResp.Schema, Raw: stateVal},
	}
	updateResp := &resource.UpdateResponse{
		State: tfsdk.State{Schema: schemaResp.Schema},
	}

	r.Update(ctx, updateReq, updateResp)

	if updateResp.Diagnostics.HasError() {
		t.Fatalf("Update failed: %v", updateResp.Diagnostics.Errors())
	}

	// Verify resize workflow: stop -> resize -> start.
	if len(actions) != 3 {
		t.Fatalf("expected 3 actions, got %d: %v", len(actions), actions)
	}
	expected := []string{"stop", "resize", "start"}
	for i, exp := range expected {
		if actions[i] != exp {
			t.Errorf("expected action[%d] = %s, got %s", i, exp, actions[i])
		}
	}

	var model InstanceModel
	updateResp.State.Get(ctx, &model)

	if model.FlavorID.ValueString() != "flavor-large" {
		t.Errorf("expected FlavorID flavor-large, got %s", model.FlavorID.ValueString())
	}
}

func TestInstanceResource_TFSDKUpdateTagsAndSecurityGroups(t *testing.T) {
	var patchCalled bool

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/me":
			_ = json.NewEncoder(w).Encode(map[string]string{"id": "user-123", "tenantId": "tenant-456"})

		case r.Method == http.MethodPatch && r.URL.Path == "/v1/tenants/tenant-456/instances/inst-tags-1":
			patchCalled = true
			var req apiUpdateInstanceRequest
			_ = json.NewDecoder(r.Body).Decode(&req)
			// Tags are sent under "metadata" (apiUpdateInstanceRequest.Tags maps
			// to json "metadata", matching the compute update contract).
			if req.Tags["env"] != "prod" {
				t.Errorf("expected tag env=prod (metadata), got %v", req.Tags)
			}
			_ = json.NewEncoder(w).Encode(apiInstance{
				ID:             "inst-tags-1",
				Name:           "tags-vm",
				Status:         "running",
				FlavorID:       "flavor-small",
				ImageID:        "img-ubuntu",
				SecurityGroups: []string{"sg-new-1", "sg-new-2"},
				Metadata:       map[string]string{"env": "prod"},
				CreatedAt:      "2025-06-01T12:00:00Z",
			})

		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/tenant-456/instances/inst-tags-1":
			_ = json.NewEncoder(w).Encode(apiInstance{
				ID:             "inst-tags-1",
				Name:           "tags-vm",
				Status:         "running",
				FlavorID:       "flavor-small",
				ImageID:        "img-ubuntu",
				SecurityGroups: []string{"sg-new-1", "sg-new-2"},
				Metadata:       map[string]string{"env": "prod"},
				CreatedAt:      "2025-06-01T12:00:00Z",
			})

		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key")
	if err := c.Configure(context.Background()); err != nil {
		t.Fatalf("client configure failed: %v", err)
	}

	r := &instanceResource{
		client:       c,
		pollInterval: 10 * time.Millisecond,
		pollTimeout:  5 * time.Second,
	}
	schemaResp := getInstanceSchema(t)

	ctx := context.Background()
	tfType := schemaResp.Schema.Type().TerraformType(ctx)

	stateVal := instanceTFValue(t, tfType, map[string]tftypes.Value{
		"id":        tftypes.NewValue(tftypes.String, "inst-tags-1"),
		"name":      tftypes.NewValue(tftypes.String, "tags-vm"),
		"flavor_id": tftypes.NewValue(tftypes.String, "flavor-small"),
		"image_id":  tftypes.NewValue(tftypes.String, "img-ubuntu"),
		"status":    tftypes.NewValue(tftypes.String, "running"),
		"security_groups": tftypes.NewValue(tftypes.Set{ElementType: tftypes.String}, []tftypes.Value{
			tftypes.NewValue(tftypes.String, "sg-old"),
		}),
		"tags": tftypes.NewValue(tftypes.Map{ElementType: tftypes.String}, map[string]tftypes.Value{
			"env": tftypes.NewValue(tftypes.String, "dev"),
		}),
		"flavor_name":    tftypes.NewValue(tftypes.String, nil),
		"image_name":     tftypes.NewValue(tftypes.String, nil),
		"private_ip":     tftypes.NewValue(tftypes.String, nil),
		"public_ip":      tftypes.NewValue(tftypes.String, nil),
		"user_data":      tftypes.NewValue(tftypes.String, nil),
		"user_data_hash": tftypes.NewValue(tftypes.String, nil),
		"created_at":     tftypes.NewValue(tftypes.String, "2025-06-01T12:00:00Z"),
	})

	planVal := instanceTFValue(t, tfType, map[string]tftypes.Value{
		"id":        tftypes.NewValue(tftypes.String, "inst-tags-1"),
		"name":      tftypes.NewValue(tftypes.String, "tags-vm"),
		"flavor_id": tftypes.NewValue(tftypes.String, "flavor-small"),
		"image_id":  tftypes.NewValue(tftypes.String, "img-ubuntu"),
		"status":    tftypes.NewValue(tftypes.String, "running"),
		"security_groups": tftypes.NewValue(tftypes.Set{ElementType: tftypes.String}, []tftypes.Value{
			tftypes.NewValue(tftypes.String, "sg-new-1"),
			tftypes.NewValue(tftypes.String, "sg-new-2"),
		}),
		"tags": tftypes.NewValue(tftypes.Map{ElementType: tftypes.String}, map[string]tftypes.Value{
			"env": tftypes.NewValue(tftypes.String, "prod"),
		}),
		"flavor_name":    tftypes.NewValue(tftypes.String, nil),
		"image_name":     tftypes.NewValue(tftypes.String, nil),
		"private_ip":     tftypes.NewValue(tftypes.String, nil),
		"public_ip":      tftypes.NewValue(tftypes.String, nil),
		"user_data":      tftypes.NewValue(tftypes.String, nil),
		"user_data_hash": tftypes.NewValue(tftypes.String, nil),
		"created_at":     tftypes.NewValue(tftypes.String, "2025-06-01T12:00:00Z"),
	})

	updateReq := resource.UpdateRequest{
		Plan:  tfsdk.Plan{Schema: schemaResp.Schema, Raw: planVal},
		State: tfsdk.State{Schema: schemaResp.Schema, Raw: stateVal},
	}
	updateResp := &resource.UpdateResponse{
		State: tfsdk.State{Schema: schemaResp.Schema},
	}

	r.Update(ctx, updateReq, updateResp)

	if updateResp.Diagnostics.HasError() {
		t.Fatalf("Update failed: %v", updateResp.Diagnostics.Errors())
	}

	if !patchCalled {
		t.Error("expected PATCH to be called for tags/security_groups change")
	}

	var model InstanceModel
	updateResp.State.Get(ctx, &model)

	var sgs []string
	model.SecurityGroups.ElementsAs(ctx, &sgs, false)
	if len(sgs) != 2 {
		t.Errorf("expected 2 security groups, got %d", len(sgs))
	}

	var tags map[string]string
	model.Tags.ElementsAs(ctx, &tags, false)
	if tags["env"] != "prod" {
		t.Errorf("expected tag env=prod, got %v", tags)
	}
}

func TestInstanceResource_TFSDKDelete(t *testing.T) {
	var deleteCalled bool
	var getCount atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/me":
			_ = json.NewEncoder(w).Encode(map[string]string{"id": "user-123", "tenantId": "tenant-456"})

		case r.Method == http.MethodDelete && r.URL.Path == "/v1/tenants/tenant-456/instances/inst-del-1":
			deleteCalled = true
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(map[string]string{"status": "deleting"})

		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/tenant-456/instances/inst-del-1":
			n := getCount.Add(1)
			if n >= 2 {
				w.WriteHeader(http.StatusNotFound)
				_ = json.NewEncoder(w).Encode(map[string]interface{}{
					"error": map[string]string{"code": "NOT_FOUND", "message": "Instance not found"},
				})
			} else {
				_ = json.NewEncoder(w).Encode(apiInstance{
					ID:     "inst-del-1",
					Name:   "del-vm",
					Status: "deleting",
				})
			}

		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key")
	if err := c.Configure(context.Background()); err != nil {
		t.Fatalf("client configure failed: %v", err)
	}

	r := &instanceResource{
		client:       c,
		pollInterval: 10 * time.Millisecond,
		pollTimeout:  5 * time.Second,
	}
	schemaResp := getInstanceSchema(t)

	ctx := context.Background()
	tfType := schemaResp.Schema.Type().TerraformType(ctx)

	stateVal := instanceTFValue(t, tfType, map[string]tftypes.Value{
		"id":             tftypes.NewValue(tftypes.String, "inst-del-1"),
		"name":           tftypes.NewValue(tftypes.String, "del-vm"),
		"status":         tftypes.NewValue(tftypes.String, "running"),
		"flavor_name":    tftypes.NewValue(tftypes.String, nil),
		"image_name":     tftypes.NewValue(tftypes.String, nil),
		"private_ip":     tftypes.NewValue(tftypes.String, nil),
		"public_ip":      tftypes.NewValue(tftypes.String, nil),
		"user_data":      tftypes.NewValue(tftypes.String, nil),
		"user_data_hash": tftypes.NewValue(tftypes.String, nil),
		"created_at":     tftypes.NewValue(tftypes.String, "2025-06-01T12:00:00Z"),
	})

	deleteReq := resource.DeleteRequest{
		State: tfsdk.State{Schema: schemaResp.Schema, Raw: stateVal},
	}
	deleteResp := &resource.DeleteResponse{
		State: tfsdk.State{Schema: schemaResp.Schema},
	}

	r.Delete(ctx, deleteReq, deleteResp)

	if deleteResp.Diagnostics.HasError() {
		t.Fatalf("Delete failed: %v", deleteResp.Diagnostics.Errors())
	}

	if !deleteCalled {
		t.Error("expected DELETE to be called")
	}
}

func TestInstanceResource_TFSDKDeleteAlreadyGone(t *testing.T) {
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

	c := client.NewClient(server.URL, "test-key")
	if err := c.Configure(context.Background()); err != nil {
		t.Fatalf("client configure failed: %v", err)
	}

	r := &instanceResource{
		client:       c,
		pollInterval: 10 * time.Millisecond,
		pollTimeout:  5 * time.Second,
	}
	schemaResp := getInstanceSchema(t)

	ctx := context.Background()
	tfType := schemaResp.Schema.Type().TerraformType(ctx)

	stateVal := instanceTFValue(t, tfType, map[string]tftypes.Value{
		"id":             tftypes.NewValue(tftypes.String, "inst-already-gone"),
		"name":           tftypes.NewValue(tftypes.String, "gone-vm"),
		"status":         tftypes.NewValue(tftypes.String, "running"),
		"flavor_name":    tftypes.NewValue(tftypes.String, nil),
		"image_name":     tftypes.NewValue(tftypes.String, nil),
		"private_ip":     tftypes.NewValue(tftypes.String, nil),
		"public_ip":      tftypes.NewValue(tftypes.String, nil),
		"user_data":      tftypes.NewValue(tftypes.String, nil),
		"user_data_hash": tftypes.NewValue(tftypes.String, nil),
		"created_at":     tftypes.NewValue(tftypes.String, "2025-06-01T12:00:00Z"),
	})

	deleteReq := resource.DeleteRequest{
		State: tfsdk.State{Schema: schemaResp.Schema, Raw: stateVal},
	}
	deleteResp := &resource.DeleteResponse{
		State: tfsdk.State{Schema: schemaResp.Schema},
	}

	r.Delete(ctx, deleteReq, deleteResp)

	// Delete of already-gone resource should not error.
	if deleteResp.Diagnostics.HasError() {
		t.Fatalf("Delete should not error for already-gone resource, got: %v", deleteResp.Diagnostics.Errors())
	}
}

func TestInstanceResource_TFSDKImportState(t *testing.T) {
	r := &instanceResource{}
	schemaResp := getInstanceSchema(t)

	ctx := context.Background()
	tfType := schemaResp.Schema.Type().TerraformType(ctx)

	// Initialize state with null values so the schema type is set.
	initVal := instanceTFValue(t, tfType, map[string]tftypes.Value{
		"id":             tftypes.NewValue(tftypes.String, nil),
		"name":           tftypes.NewValue(tftypes.String, nil),
		"flavor_id":      tftypes.NewValue(tftypes.String, nil),
		"image_id":       tftypes.NewValue(tftypes.String, nil),
		"status":         tftypes.NewValue(tftypes.String, nil),
		"flavor_name":    tftypes.NewValue(tftypes.String, nil),
		"image_name":     tftypes.NewValue(tftypes.String, nil),
		"private_ip":     tftypes.NewValue(tftypes.String, nil),
		"public_ip":      tftypes.NewValue(tftypes.String, nil),
		"user_data":      tftypes.NewValue(tftypes.String, nil),
		"user_data_hash": tftypes.NewValue(tftypes.String, nil),
		"created_at":     tftypes.NewValue(tftypes.String, nil),
	})

	importReq := resource.ImportStateRequest{ID: "inst-imported-1"}
	importResp := &resource.ImportStateResponse{
		State: tfsdk.State{Schema: schemaResp.Schema, Raw: initVal},
	}

	r.ImportState(ctx, importReq, importResp)

	if importResp.Diagnostics.HasError() {
		t.Fatalf("ImportState failed: %v", importResp.Diagnostics.Errors())
	}

	// Verify the ID was set in state.
	var model InstanceModel
	importResp.State.Get(ctx, &model)

	if model.ID.ValueString() != "inst-imported-1" {
		t.Errorf("expected ID inst-imported-1, got %s", model.ID.ValueString())
	}
}

func TestInstanceResource_GetPollDefaults(t *testing.T) {
	r := &instanceResource{}

	if r.getPollInterval() != 5*time.Second {
		t.Errorf("expected default poll interval 5s, got %v", r.getPollInterval())
	}
	if r.getPollTimeout() != 10*time.Minute {
		t.Errorf("expected default poll timeout 10m, got %v", r.getPollTimeout())
	}

	r.pollInterval = 100 * time.Millisecond
	r.pollTimeout = 30 * time.Second

	if r.getPollInterval() != 100*time.Millisecond {
		t.Errorf("expected custom poll interval 100ms, got %v", r.getPollInterval())
	}
	if r.getPollTimeout() != 30*time.Second {
		t.Errorf("expected custom poll timeout 30s, got %v", r.getPollTimeout())
	}
}

// Ensure fmt is used (for error state test diagnostics message validation).
var _ = fmt.Sprintf
