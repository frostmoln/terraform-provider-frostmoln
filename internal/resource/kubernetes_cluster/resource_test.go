package kubernetes_cluster

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
)

// --- Model unit tests ---

func TestToCreateRequestMinimal(t *testing.T) {
	m := KubernetesClusterModel{
		Name:     types.StringValue("my-cluster"),
		Version:  types.StringUnknown(),
		Region:   types.StringNull(),
		VPCID:    types.StringValue("vpc-1"),
		SubnetID: types.StringValue("sn-1"),
		InitialNodePool: &InitialNodePoolModel{
			FlavorID:  types.StringValue("k8s.gp1.small"),
			NodeCount: types.Int64Value(1),
			Name:      types.StringUnknown(),
		},
	}

	req := m.toCreateRequest()
	if req.Name != "my-cluster" || req.VPCID != "vpc-1" || req.SubnetID != "sn-1" {
		t.Errorf("unexpected required fields: %+v", req)
	}
	if req.KubernetesVersion != "" || req.ControlPlaneTier != "" || req.Region != "" || req.FloatingIPID != "" {
		t.Errorf("expected empty optional fields, got %+v", req)
	}
	if req.InitialNodePool.FlavorID != "k8s.gp1.small" || req.InitialNodePool.NodeCount != 1 {
		t.Errorf("unexpected initial pool: %+v", req.InitialNodePool)
	}
	if req.InitialNodePool.Name != "" {
		t.Errorf("expected empty pool name for unknown value, got %q", req.InitialNodePool.Name)
	}
}

func TestToCreateRequestFull(t *testing.T) {
	m := KubernetesClusterModel{
		Name:             types.StringValue("my-cluster"),
		Version:          types.StringValue("1.35"),
		ControlPlaneTier: types.StringValue("standard"),
		Region:           types.StringValue("falkenberg"),
		VPCID:            types.StringValue("vpc-1"),
		SubnetID:         types.StringValue("sn-1"),
		FloatingIPID:     types.StringValue("11111111-2222-3333-4444-555555555555"),
		InitialNodePool: &InitialNodePoolModel{
			Name:      types.StringValue("workers"),
			FlavorID:  types.StringValue("k8s.gp1.medium"),
			NodeCount: types.Int64Value(3),
		},
	}

	req := m.toCreateRequest()
	if req.KubernetesVersion != "1.35" || req.ControlPlaneTier != "standard" || req.Region != "falkenberg" {
		t.Errorf("unexpected optional fields: %+v", req)
	}
	if req.FloatingIPID != "11111111-2222-3333-4444-555555555555" {
		t.Errorf("unexpected floatingIpId: %s", req.FloatingIPID)
	}
	if req.InitialNodePool.Name != "workers" || req.InitialNodePool.NodeCount != 3 {
		t.Errorf("unexpected initial pool: %+v", req.InitialNodePool)
	}
}

func TestFromAPIPreservesWriteOnlyFields(t *testing.T) {
	m := KubernetesClusterModel{
		FloatingIPID: types.StringValue("fip-uuid"),
		Kubeconfig:   types.StringValue("prior-kubeconfig"),
	}

	m.fromAPI(&apiKubernetesCluster{
		ID:                "c-1",
		Name:              "my-cluster",
		Status:            "running",
		KubernetesVersion: "1.35",
		ControlPlaneTier:  "development",
		Region:            "falkenberg",
		VPCID:             "vpc-1",
		SubnetID:          "sn-1",
		FloatingIP:        "203.0.113.10",
		CreatedAt:         "2026-07-01T00:00:00Z",
	})

	if m.FloatingIPID.ValueString() != "fip-uuid" {
		t.Error("fromAPI must not touch floating_ip_id (write-only, never echoed)")
	}
	if m.Kubeconfig.ValueString() != "prior-kubeconfig" {
		t.Error("fromAPI must not touch kubeconfig (separate endpoint)")
	}
	if m.FloatingIP.ValueString() != "203.0.113.10" {
		t.Errorf("expected floating_ip address, got %s", m.FloatingIP.ValueString())
	}
}

func TestFromAPINulls(t *testing.T) {
	var m KubernetesClusterModel
	m.fromAPI(&apiKubernetesCluster{
		ID:                "c-1",
		Name:              "my-cluster",
		Status:            "creating",
		KubernetesVersion: "1.35",
		ControlPlaneTier:  "development",
		Region:            "falkenberg",
		VPCID:             "vpc-1",
		SubnetID:          "sn-1",
		CreatedAt:         "2026-07-01T00:00:00Z",
	})

	for name, v := range map[string]types.String{
		"pod_cidr":         m.PodCIDR,
		"service_cidr":     m.ServiceCIDR,
		"endpoint":         m.Endpoint,
		"load_balancer_id": m.LoadBalancerID,
		"floating_ip":      m.FloatingIP,
		"ca_cert_hash":     m.CACertHash,
		"updated_at":       m.UpdatedAt,
	} {
		if !v.IsNull() {
			t.Errorf("expected null %s for empty API value", name)
		}
	}
}

// --- Resource unit tests ---

func TestNewResource(t *testing.T) {
	if NewResource() == nil {
		t.Fatal("expected non-nil resource")
	}
}

func TestMetadata(t *testing.T) {
	r := NewResource()
	req := resource.MetadataRequest{ProviderTypeName: "frostmoln"}
	var resp resource.MetadataResponse
	r.Metadata(context.Background(), req, &resp)
	if resp.TypeName != "frostmoln_kubernetes_cluster" {
		t.Errorf("expected type name frostmoln_kubernetes_cluster, got %s", resp.TypeName)
	}
}

func TestSchema(t *testing.T) {
	r := NewResource()
	var resp resource.SchemaResponse
	r.Schema(context.Background(), resource.SchemaRequest{}, &resp)

	for _, attr := range []string{
		"id", "name", "version", "control_plane_tier", "region", "vpc_id", "subnet_id",
		"floating_ip_id", "initial_node_pool", "status", "ha_enabled", "pod_cidr",
		"service_cidr", "endpoint", "load_balancer_id", "floating_ip", "ca_cert_hash",
		"kubeconfig", "created_at", "updated_at", "tenant_id",
	} {
		if _, ok := resp.Schema.Attributes[attr]; !ok {
			t.Errorf("expected attribute %s in schema", attr)
		}
	}
	if !resp.Schema.Attributes["kubeconfig"].IsSensitive() {
		t.Error("kubeconfig must be sensitive")
	}
}

func TestConfigureNilProviderData(t *testing.T) {
	r := &kubernetesClusterResource{}
	var resp resource.ConfigureResponse
	r.Configure(context.Background(), resource.ConfigureRequest{}, &resp)
	if resp.Diagnostics.HasError() {
		t.Errorf("expected no errors for nil provider data, got %v", resp.Diagnostics.Errors())
	}
}

func TestConfigureWrongType(t *testing.T) {
	r := &kubernetesClusterResource{}
	var resp resource.ConfigureResponse
	r.Configure(context.Background(), resource.ConfigureRequest{ProviderData: "not-a-client"}, &resp)
	if !resp.Diagnostics.HasError() {
		t.Error("expected error for wrong provider data type")
	}
}

// --- test harness ---

func buildState(t *testing.T, model KubernetesClusterModel) tfsdk.State {
	t.Helper()
	r := NewResource()
	var schemaResp resource.SchemaResponse
	r.Schema(context.Background(), resource.SchemaRequest{}, &schemaResp)

	state := tfsdk.State{Schema: schemaResp.Schema}
	if diags := state.Set(context.Background(), &model); diags.HasError() {
		t.Fatalf("failed to set state: %v", diags.Errors())
	}
	return state
}

func buildPlan(t *testing.T, model KubernetesClusterModel) tfsdk.Plan {
	t.Helper()
	r := NewResource()
	var schemaResp resource.SchemaResponse
	r.Schema(context.Background(), resource.SchemaRequest{}, &schemaResp)

	plan := tfsdk.Plan{Schema: schemaResp.Schema}
	if diags := plan.Set(context.Background(), &model); diags.HasError() {
		t.Fatalf("failed to set plan: %v", diags.Errors())
	}
	return plan
}

func emptyState(t *testing.T) tfsdk.State {
	t.Helper()
	r := NewResource()
	var schemaResp resource.SchemaResponse
	r.Schema(context.Background(), resource.SchemaRequest{}, &schemaResp)

	stateVal := tftypes.NewValue(schemaResp.Schema.Type().TerraformType(context.Background()), nil)
	return tfsdk.State{Schema: schemaResp.Schema, Raw: stateVal}
}

func testResource(c *client.Client) *kubernetesClusterResource {
	return &kubernetesClusterResource{
		client:       c,
		pollInterval: 5 * time.Millisecond,
		pollTimeout:  5 * time.Second,
	}
}

func writeJSON(t *testing.T, w http.ResponseWriter, v any) {
	t.Helper()
	if err := json.NewEncoder(w).Encode(v); err != nil {
		t.Errorf("failed to encode response: %v", err)
	}
}

func runningCluster() apiKubernetesCluster {
	return apiKubernetesCluster{
		ID:                "c-1",
		Name:              "test-cluster",
		TenantID:          "t-1",
		Status:            statusRunning,
		KubernetesVersion: "1.35",
		ControlPlaneTier:  "development",
		HAEnabled:         false,
		Region:            "falkenberg",
		VPCID:             "vpc-1",
		SubnetID:          "sn-1",
		PodCIDR:           "10.180.0.0/18",
		ServiceCIDR:       "10.180.64.0/18",
		Endpoint:          "https://203.0.113.10:6443",
		LoadBalancerID:    "lb-1",
		FloatingIP:        "203.0.113.10",
		CACertHash:        "sha256:abc",
		CreatedAt:         "2026-07-01T00:00:00Z",
		UpdatedAt:         "2026-07-01T00:10:00Z",
	}
}

func initialPool(status string) apiNodePool {
	return apiNodePool{
		ID:        "np-1",
		ClusterID: "c-1",
		Name:      "default",
		Status:    status,
		FlavorID:  "k8s.gp1.small",
		NodeCount: 2,
		IsInitial: true,
		CreatedAt: "2026-07-01T00:00:00Z",
	}
}

// --- Create ---

func TestCreate(t *testing.T) {
	var clusterGets, kubeconfigGets atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/tenants/t-1/kubernetes-clusters":
			var body apiCreateClusterRequest
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("failed to decode request: %v", err)
			}
			if body.FloatingIPID != "11111111-2222-3333-4444-555555555555" {
				t.Errorf("expected floatingIpId in create request, got %q", body.FloatingIPID)
			}
			if body.InitialNodePool.FlavorID != "k8s.gp1.small" || body.InitialNodePool.NodeCount != 2 {
				t.Errorf("unexpected initialNodePool: %+v", body.InitialNodePool)
			}
			created := runningCluster()
			created.Status = "creating"
			created.Endpoint = ""
			created.LoadBalancerID = ""
			created.FloatingIP = ""
			created.CACertHash = ""
			w.WriteHeader(http.StatusCreated)
			writeJSON(t, w, created)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-1/kubernetes-clusters/c-1":
			c := runningCluster()
			if clusterGets.Add(1) < 2 {
				c.Status = "creating"
			}
			writeJSON(t, w, c)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-1/kubernetes-clusters/c-1/node-pools":
			// Includes a soft-deleted initial pool row (name reuse) that the
			// discovery must skip, plus a non-initial pool.
			writeJSON(t, w, apiNodePoolList{NodePools: []apiNodePool{
				{ID: "np-0", Name: "default", Status: statusDeleted, IsInitial: true, FlavorID: "k8s.gp1.small", NodeCount: 1},
				{ID: "np-9", Name: "extra", Status: statusActive, IsInitial: false, FlavorID: "k8s.gp1.small", NodeCount: 1},
				initialPool(statusActive),
			}})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-1/kubernetes-clusters/c-1/node-pools/np-1":
			writeJSON(t, w, initialPool(statusActive))
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-1/kubernetes-clusters/c-1/kubeconfig":
			// First attempt fails transiently; the bounded retry must recover.
			if kubeconfigGets.Add(1) == 1 {
				w.WriteHeader(http.StatusInternalServerError)
				writeJSON(t, w, map[string]any{"code": "INTERNAL_ERROR", "message": "vault hiccup"})
				return
			}
			writeJSON(t, w, apiKubeconfig{Endpoint: "https://203.0.113.10:6443", Kubeconfig: "kubeconfig-yaml"})
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key", client.WithHTTPClient(server.Client()))
	c.SetTenantIDForTest("t-1")
	r := testResource(c)

	plan := buildPlan(t, KubernetesClusterModel{
		Name:             types.StringValue("test-cluster"),
		Version:          types.StringUnknown(),
		ControlPlaneTier: types.StringUnknown(),
		Region:           types.StringUnknown(),
		VPCID:            types.StringValue("vpc-1"),
		SubnetID:         types.StringValue("sn-1"),
		FloatingIPID:     types.StringValue("11111111-2222-3333-4444-555555555555"),
		InitialNodePool: &InitialNodePoolModel{
			ID:        types.StringUnknown(),
			Name:      types.StringUnknown(),
			FlavorID:  types.StringValue("k8s.gp1.small"),
			NodeCount: types.Int64Value(2),
			Status:    types.StringUnknown(),
		},
	})

	createResp := resource.CreateResponse{State: emptyState(t)}
	r.Create(context.Background(), resource.CreateRequest{Plan: plan}, &createResp)
	if createResp.Diagnostics.HasError() {
		t.Fatalf("create failed: %v", createResp.Diagnostics.Errors())
	}

	var result KubernetesClusterModel
	createResp.State.Get(context.Background(), &result)
	if result.ID.ValueString() != "c-1" {
		t.Errorf("expected id c-1, got %s", result.ID.ValueString())
	}
	if result.Status.ValueString() != statusRunning {
		t.Errorf("expected status running, got %s", result.Status.ValueString())
	}
	if result.Version.ValueString() != "1.35" {
		t.Errorf("expected resolved version 1.35, got %s", result.Version.ValueString())
	}
	if result.FloatingIPID.ValueString() != "11111111-2222-3333-4444-555555555555" {
		t.Error("expected floating_ip_id preserved in state (write-only field)")
	}
	if result.Kubeconfig.ValueString() != "kubeconfig-yaml" {
		t.Errorf("expected kubeconfig from retry, got %q", result.Kubeconfig.ValueString())
	}
	if result.InitialNodePool == nil {
		t.Fatal("expected initial_node_pool in state")
	}
	if result.InitialNodePool.ID.ValueString() != "np-1" {
		t.Errorf("expected live initial pool np-1 (deleted row filtered), got %s", result.InitialNodePool.ID.ValueString())
	}
	if result.InitialNodePool.Name.ValueString() != "default" {
		t.Errorf("expected adopted pool name default, got %s", result.InitialNodePool.Name.ValueString())
	}
	if result.InitialNodePool.Status.ValueString() != statusActive {
		t.Errorf("expected pool status active, got %s", result.InitialNodePool.Status.ValueString())
	}
	if kubeconfigGets.Load() != 2 {
		t.Errorf("expected 2 kubeconfig attempts (one failed + one retry), got %d", kubeconfigGets.Load())
	}
}

func TestCreateClusterErrorStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/tenants/t-1/kubernetes-clusters":
			created := runningCluster()
			created.Status = "creating"
			w.WriteHeader(http.StatusCreated)
			writeJSON(t, w, created)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-1/kubernetes-clusters/c-1":
			c := runningCluster()
			c.Status = statusError
			writeJSON(t, w, c)
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key", client.WithHTTPClient(server.Client()))
	c.SetTenantIDForTest("t-1")
	r := testResource(c)

	plan := buildPlan(t, KubernetesClusterModel{
		Name:     types.StringValue("test-cluster"),
		VPCID:    types.StringValue("vpc-1"),
		SubnetID: types.StringValue("sn-1"),
		InitialNodePool: &InitialNodePoolModel{
			FlavorID:  types.StringValue("k8s.gp1.small"),
			NodeCount: types.Int64Value(1),
		},
	})

	createResp := resource.CreateResponse{State: emptyState(t)}
	r.Create(context.Background(), resource.CreateRequest{Plan: plan}, &createResp)
	if !createResp.Diagnostics.HasError() {
		t.Fatal("expected error for cluster reaching error status")
	}

	// Orphan guard: the ID must be tracked even though creation failed.
	var result KubernetesClusterModel
	createResp.State.Get(context.Background(), &result)
	if result.ID.ValueString() != "c-1" {
		t.Errorf("expected id c-1 tracked in state after failed create, got %s", result.ID.ValueString())
	}
}

func TestCreateKubeconfigExhaustedIsWarning(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/tenants/t-1/kubernetes-clusters":
			created := runningCluster()
			created.Status = "creating"
			w.WriteHeader(http.StatusCreated)
			writeJSON(t, w, created)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-1/kubernetes-clusters/c-1":
			writeJSON(t, w, runningCluster())
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-1/kubernetes-clusters/c-1/node-pools":
			writeJSON(t, w, apiNodePoolList{NodePools: []apiNodePool{initialPool(statusActive)}})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-1/kubernetes-clusters/c-1/node-pools/np-1":
			writeJSON(t, w, initialPool(statusActive))
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-1/kubernetes-clusters/c-1/kubeconfig":
			w.WriteHeader(http.StatusInternalServerError)
			writeJSON(t, w, map[string]any{"code": "INTERNAL_ERROR", "message": "vault down"})
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key", client.WithHTTPClient(server.Client()))
	c.SetTenantIDForTest("t-1")
	r := testResource(c)

	plan := buildPlan(t, KubernetesClusterModel{
		Name:     types.StringValue("test-cluster"),
		VPCID:    types.StringValue("vpc-1"),
		SubnetID: types.StringValue("sn-1"),
		InitialNodePool: &InitialNodePoolModel{
			FlavorID:  types.StringValue("k8s.gp1.small"),
			NodeCount: types.Int64Value(2),
		},
	})

	createResp := resource.CreateResponse{State: emptyState(t)}
	r.Create(context.Background(), resource.CreateRequest{Plan: plan}, &createResp)
	if createResp.Diagnostics.HasError() {
		t.Fatalf("kubeconfig failure must not fail the apply: %v", createResp.Diagnostics.Errors())
	}
	if createResp.Diagnostics.WarningsCount() == 0 {
		t.Error("expected a warning diagnostic for the unfetchable kubeconfig")
	}

	var result KubernetesClusterModel
	createResp.State.Get(context.Background(), &result)
	if !result.Kubeconfig.IsNull() {
		t.Errorf("expected null kubeconfig, got %q", result.Kubeconfig.ValueString())
	}
	if result.Status.ValueString() != statusRunning {
		t.Errorf("expected status running, got %s", result.Status.ValueString())
	}
}

func TestCreateInitialPoolErrorStatus(t *testing.T) {
	var poolGets atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/tenants/t-1/kubernetes-clusters":
			created := runningCluster()
			created.Status = "creating"
			w.WriteHeader(http.StatusCreated)
			writeJSON(t, w, created)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-1/kubernetes-clusters/c-1":
			writeJSON(t, w, runningCluster())
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-1/kubernetes-clusters/c-1/node-pools":
			writeJSON(t, w, apiNodePoolList{NodePools: []apiNodePool{initialPool("creating")}})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-1/kubernetes-clusters/c-1/node-pools/np-1":
			p := initialPool("creating")
			if poolGets.Add(1) >= 2 {
				p.Status = statusError
			}
			writeJSON(t, w, p)
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key", client.WithHTTPClient(server.Client()))
	c.SetTenantIDForTest("t-1")
	r := testResource(c)

	plan := buildPlan(t, KubernetesClusterModel{
		Name:     types.StringValue("test-cluster"),
		VPCID:    types.StringValue("vpc-1"),
		SubnetID: types.StringValue("sn-1"),
		InitialNodePool: &InitialNodePoolModel{
			FlavorID:  types.StringValue("k8s.gp1.small"),
			NodeCount: types.Int64Value(2),
		},
	})

	createResp := resource.CreateResponse{State: emptyState(t)}
	r.Create(context.Background(), resource.CreateRequest{Plan: plan}, &createResp)
	if !createResp.Diagnostics.HasError() {
		t.Fatal("expected error when the initial pool goes to error status")
	}
}

// --- Read ---

func stateModel() KubernetesClusterModel {
	return KubernetesClusterModel{
		ID:               types.StringValue("c-1"),
		Name:             types.StringValue("test-cluster"),
		Version:          types.StringValue("1.35"),
		ControlPlaneTier: types.StringValue("development"),
		Region:           types.StringValue("falkenberg"),
		VPCID:            types.StringValue("vpc-1"),
		SubnetID:         types.StringValue("sn-1"),
		FloatingIPID:     types.StringValue("fip-uuid"),
		Status:           types.StringValue(statusRunning),
		HAEnabled:        types.BoolValue(false),
		Kubeconfig:       types.StringValue("prior-kubeconfig"),
		CreatedAt:        types.StringValue("2026-07-01T00:00:00Z"),
		InitialNodePool: &InitialNodePoolModel{
			ID:        types.StringValue("np-1"),
			Name:      types.StringValue("default"),
			FlavorID:  types.StringValue("k8s.gp1.small"),
			NodeCount: types.Int64Value(2),
			Status:    types.StringValue(statusActive),
		},
	}
}

func TestRead(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-1/kubernetes-clusters/c-1":
			writeJSON(t, w, runningCluster())
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-1/kubernetes-clusters/c-1/node-pools":
			p := initialPool(statusActive)
			p.NodeCount = 5 // out-of-band scale — Read must pick up the drift
			writeJSON(t, w, apiNodePoolList{NodePools: []apiNodePool{p}})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-1/kubernetes-clusters/c-1/kubeconfig":
			writeJSON(t, w, apiKubeconfig{Endpoint: "https://203.0.113.10:6443", Kubeconfig: "fresh-kubeconfig"})
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key", client.WithHTTPClient(server.Client()))
	c.SetTenantIDForTest("t-1")
	r := testResource(c)

	state := buildState(t, stateModel())
	readResp := resource.ReadResponse{State: state}
	r.Read(context.Background(), resource.ReadRequest{State: state}, &readResp)
	if readResp.Diagnostics.HasError() {
		t.Fatalf("read failed: %v", readResp.Diagnostics.Errors())
	}

	var result KubernetesClusterModel
	readResp.State.Get(context.Background(), &result)
	if result.FloatingIPID.ValueString() != "fip-uuid" {
		t.Error("expected floating_ip_id preserved from prior state")
	}
	if result.InitialNodePool.NodeCount.ValueInt64() != 5 {
		t.Errorf("expected node_count drift 5, got %d", result.InitialNodePool.NodeCount.ValueInt64())
	}
	if result.Kubeconfig.ValueString() != "fresh-kubeconfig" {
		t.Errorf("expected refreshed kubeconfig, got %q", result.Kubeconfig.ValueString())
	}
	if result.Endpoint.ValueString() != "https://203.0.113.10:6443" {
		t.Errorf("expected endpoint, got %s", result.Endpoint.ValueString())
	}
}

func TestReadSoftDeletedRemovesResource(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Soft delete: 200 with status "deleted" — never 404.
		c := runningCluster()
		c.Status = statusDeleted
		writeJSON(t, w, c)
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key", client.WithHTTPClient(server.Client()))
	c.SetTenantIDForTest("t-1")
	r := testResource(c)

	state := buildState(t, stateModel())
	readResp := resource.ReadResponse{State: state}
	r.Read(context.Background(), resource.ReadRequest{State: state}, &readResp)
	if readResp.Diagnostics.HasError() {
		t.Fatalf("expected no error for soft-deleted cluster, got %v", readResp.Diagnostics.Errors())
	}
	if !readResp.State.Raw.IsNull() {
		t.Error("expected state removed for status=deleted cluster")
	}
}

func TestReadNotFoundRemovesResource(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]any{"code": "NOT_FOUND", "message": "not found"})
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key", client.WithHTTPClient(server.Client()))
	c.SetTenantIDForTest("t-1")
	r := testResource(c)

	state := buildState(t, stateModel())
	readResp := resource.ReadResponse{State: state}
	r.Read(context.Background(), resource.ReadRequest{State: state}, &readResp)
	if readResp.Diagnostics.HasError() {
		t.Fatalf("expected no error for 404, got %v", readResp.Diagnostics.Errors())
	}
	if !readResp.State.Raw.IsNull() {
		t.Error("expected state removed for 404")
	}
}

func TestReadInitialPoolDeletedOutOfBand(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-1/kubernetes-clusters/c-1":
			writeJSON(t, w, runningCluster())
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-1/kubernetes-clusters/c-1/node-pools":
			// Only the soft-deleted initial row remains.
			writeJSON(t, w, apiNodePoolList{NodePools: []apiNodePool{initialPool(statusDeleted)}})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-1/kubernetes-clusters/c-1/kubeconfig":
			writeJSON(t, w, apiKubeconfig{Kubeconfig: "fresh-kubeconfig"})
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key", client.WithHTTPClient(server.Client()))
	c.SetTenantIDForTest("t-1")
	r := testResource(c)

	state := buildState(t, stateModel())
	readResp := resource.ReadResponse{State: state}
	r.Read(context.Background(), resource.ReadRequest{State: state}, &readResp)
	if readResp.Diagnostics.HasError() {
		t.Fatalf("expected no error, got %v", readResp.Diagnostics.Errors())
	}
	if readResp.Diagnostics.WarningsCount() == 0 {
		t.Error("expected an actionable warning for the out-of-band-deleted initial pool")
	}

	var result KubernetesClusterModel
	readResp.State.Get(context.Background(), &result)
	if result.InitialNodePool == nil || result.InitialNodePool.ID.ValueString() != "np-1" {
		t.Error("expected prior initial pool state kept when the live pool is gone")
	}
}

func TestReadKubeconfigConflictKeepsPrior(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-1/kubernetes-clusters/c-1":
			writeJSON(t, w, runningCluster())
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-1/kubernetes-clusters/c-1/node-pools":
			writeJSON(t, w, apiNodePoolList{NodePools: []apiNodePool{initialPool(statusActive)}})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-1/kubernetes-clusters/c-1/kubeconfig":
			w.WriteHeader(http.StatusConflict)
			writeJSON(t, w, map[string]any{"code": "CONFLICT", "message": "cluster not ready"})
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key", client.WithHTTPClient(server.Client()))
	c.SetTenantIDForTest("t-1")
	r := testResource(c)

	state := buildState(t, stateModel())
	readResp := resource.ReadResponse{State: state}
	r.Read(context.Background(), resource.ReadRequest{State: state}, &readResp)
	if readResp.Diagnostics.HasError() {
		t.Fatalf("read failed: %v", readResp.Diagnostics.Errors())
	}

	var result KubernetesClusterModel
	readResp.State.Get(context.Background(), &result)
	if result.Kubeconfig.ValueString() != "prior-kubeconfig" {
		t.Errorf("expected prior kubeconfig kept on 409, got %q", result.Kubeconfig.ValueString())
	}
}

// --- Update ---

func TestUpdateName(t *testing.T) {
	var putBody apiUpdateClusterRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPut && r.URL.Path == "/v1/tenants/t-1/kubernetes-clusters/c-1":
			if err := json.NewDecoder(r.Body).Decode(&putBody); err != nil {
				t.Errorf("failed to decode request: %v", err)
			}
			c := runningCluster()
			c.Name = "renamed"
			writeJSON(t, w, c)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-1/kubernetes-clusters/c-1":
			c := runningCluster()
			c.Name = "renamed"
			writeJSON(t, w, c)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-1/kubernetes-clusters/c-1/node-pools":
			writeJSON(t, w, apiNodePoolList{NodePools: []apiNodePool{initialPool(statusActive)}})
		case r.Method == http.MethodPost:
			t.Errorf("unexpected POST during name-only update: %s", r.URL.Path)
			w.WriteHeader(http.StatusBadRequest)
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key", client.WithHTTPClient(server.Client()))
	c.SetTenantIDForTest("t-1")
	r := testResource(c)

	state := buildState(t, stateModel())
	planModel := stateModel()
	planModel.Name = types.StringValue("renamed")
	plan := buildPlan(t, planModel)

	updateResp := resource.UpdateResponse{State: state}
	r.Update(context.Background(), resource.UpdateRequest{Plan: plan, State: state}, &updateResp)
	if updateResp.Diagnostics.HasError() {
		t.Fatalf("update failed: %v", updateResp.Diagnostics.Errors())
	}
	if putBody.Name == nil || *putBody.Name != "renamed" {
		t.Error("expected PUT with the new name")
	}

	var result KubernetesClusterModel
	updateResp.State.Get(context.Background(), &result)
	if result.Name.ValueString() != "renamed" {
		t.Errorf("expected renamed, got %s", result.Name.ValueString())
	}
}

func TestUpdateScaleInitialPool(t *testing.T) {
	var scaleBody apiScaleNodePoolRequest
	var poolGets atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/tenants/t-1/kubernetes-clusters/c-1/node-pools/np-1/scale":
			if err := json.NewDecoder(r.Body).Decode(&scaleBody); err != nil {
				t.Errorf("failed to decode request: %v", err)
			}
			p := initialPool("scaling")
			writeJSON(t, w, p)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-1/kubernetes-clusters/c-1/node-pools/np-1":
			p := initialPool("scaling")
			if poolGets.Add(1) >= 2 {
				p.Status = statusActive
				p.NodeCount = 4
			}
			writeJSON(t, w, p)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-1/kubernetes-clusters/c-1":
			writeJSON(t, w, runningCluster())
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-1/kubernetes-clusters/c-1/node-pools":
			p := initialPool(statusActive)
			p.NodeCount = 4
			writeJSON(t, w, apiNodePoolList{NodePools: []apiNodePool{p}})
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key", client.WithHTTPClient(server.Client()))
	c.SetTenantIDForTest("t-1")
	r := testResource(c)

	state := buildState(t, stateModel())
	planModel := stateModel()
	planModel.InitialNodePool.NodeCount = types.Int64Value(4)
	plan := buildPlan(t, planModel)

	updateResp := resource.UpdateResponse{State: state}
	r.Update(context.Background(), resource.UpdateRequest{Plan: plan, State: state}, &updateResp)
	if updateResp.Diagnostics.HasError() {
		t.Fatalf("update failed: %v", updateResp.Diagnostics.Errors())
	}
	if scaleBody.NodeCount != 4 {
		t.Errorf("expected scale to 4, got %d", scaleBody.NodeCount)
	}

	var result KubernetesClusterModel
	updateResp.State.Get(context.Background(), &result)
	if result.InitialNodePool.NodeCount.ValueInt64() != 4 {
		t.Errorf("expected node_count 4, got %d", result.InitialNodePool.NodeCount.ValueInt64())
	}
	if result.InitialNodePool.Status.ValueString() != statusActive {
		t.Errorf("expected pool status active, got %s", result.InitialNodePool.Status.ValueString())
	}
}

func TestUpdateScaleStalePoolErrors(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-1/kubernetes-clusters/c-1/node-pools":
			// The initial pool was soft-deleted out-of-band; only the dead row remains.
			writeJSON(t, w, apiNodePoolList{NodePools: []apiNodePool{initialPool(statusDeleted)}})
		case r.Method == http.MethodPost:
			t.Errorf("scale must not be POSTed for a stale pool: %s", r.URL.Path)
			w.WriteHeader(http.StatusBadRequest)
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key", client.WithHTTPClient(server.Client()))
	c.SetTenantIDForTest("t-1")
	r := testResource(c)

	state := buildState(t, stateModel())
	planModel := stateModel()
	planModel.InitialNodePool.NodeCount = types.Int64Value(4)
	plan := buildPlan(t, planModel)

	updateResp := resource.UpdateResponse{State: state}
	r.Update(context.Background(), resource.UpdateRequest{Plan: plan, State: state}, &updateResp)
	if !updateResp.Diagnostics.HasError() {
		t.Fatal("expected error when scaling a pool that was deleted out-of-band")
	}
}

func TestCreateKubeconfig4xxNotRetried(t *testing.T) {
	var kubeconfigGets atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/tenants/t-1/kubernetes-clusters":
			created := runningCluster()
			created.Status = "creating"
			w.WriteHeader(http.StatusCreated)
			writeJSON(t, w, created)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-1/kubernetes-clusters/c-1":
			writeJSON(t, w, runningCluster())
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-1/kubernetes-clusters/c-1/node-pools":
			writeJSON(t, w, apiNodePoolList{NodePools: []apiNodePool{initialPool(statusActive)}})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-1/kubernetes-clusters/c-1/node-pools/np-1":
			writeJSON(t, w, initialPool(statusActive))
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-1/kubernetes-clusters/c-1/kubeconfig":
			kubeconfigGets.Add(1)
			w.WriteHeader(http.StatusForbidden)
			writeJSON(t, w, map[string]any{"code": "feature_not_enabled", "message": "kubernetes not enabled"})
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key", client.WithHTTPClient(server.Client()))
	c.SetTenantIDForTest("t-1")
	r := testResource(c)

	plan := buildPlan(t, KubernetesClusterModel{
		Name:     types.StringValue("test-cluster"),
		VPCID:    types.StringValue("vpc-1"),
		SubnetID: types.StringValue("sn-1"),
		InitialNodePool: &InitialNodePoolModel{
			FlavorID:  types.StringValue("k8s.gp1.small"),
			NodeCount: types.Int64Value(2),
		},
	})

	createResp := resource.CreateResponse{State: emptyState(t)}
	r.Create(context.Background(), resource.CreateRequest{Plan: plan}, &createResp)
	if createResp.Diagnostics.HasError() {
		t.Fatalf("kubeconfig 4xx must not fail the apply: %v", createResp.Diagnostics.Errors())
	}
	if got := kubeconfigGets.Load(); got != 1 {
		t.Errorf("expected a single kubeconfig attempt for a 4xx, got %d", got)
	}
	if createResp.Diagnostics.WarningsCount() == 0 {
		t.Error("expected a warning diagnostic for the unfetchable kubeconfig")
	}
}

// --- Delete ---

func TestDeleteSoftDeletePoll(t *testing.T) {
	var deleted atomic.Bool
	var gets atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodDelete && r.URL.Path == "/v1/tenants/t-1/kubernetes-clusters/c-1":
			deleted.Store(true)
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-1/kubernetes-clusters/c-1":
			// Soft delete: the row stays and answers 200 forever — first
			// "deleting", then "deleted". Never a 404.
			c := runningCluster()
			if !deleted.Load() {
				c.Status = statusRunning
			} else if gets.Add(1) < 2 {
				c.Status = "deleting"
			} else {
				c.Status = statusDeleted
			}
			writeJSON(t, w, c)
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key", client.WithHTTPClient(server.Client()))
	c.SetTenantIDForTest("t-1")
	r := testResource(c)

	state := buildState(t, stateModel())
	deleteResp := resource.DeleteResponse{State: state}
	r.Delete(context.Background(), resource.DeleteRequest{State: state}, &deleteResp)
	if deleteResp.Diagnostics.HasError() {
		t.Fatalf("delete failed: %v", deleteResp.Diagnostics.Errors())
	}
	if gets.Load() < 2 {
		t.Errorf("expected the delete poll to observe the deleting->deleted transition, got %d polls", gets.Load())
	}
}

// --- Import ---

func TestImportStatePassthrough(t *testing.T) {
	r := NewResource().(*kubernetesClusterResource)

	var schemaResp resource.SchemaResponse
	r.Schema(context.Background(), resource.SchemaRequest{}, &schemaResp)

	stateVal := tftypes.NewValue(schemaResp.Schema.Type().TerraformType(context.Background()), nil)
	importResp := resource.ImportStateResponse{State: tfsdk.State{Schema: schemaResp.Schema, Raw: stateVal}}
	r.ImportState(context.Background(), resource.ImportStateRequest{ID: "c-42"}, &importResp)
	if importResp.Diagnostics.HasError() {
		t.Fatalf("import failed: %v", importResp.Diagnostics.Errors())
	}

	var id types.String
	importResp.State.GetAttribute(context.Background(), path.Root("id"), &id)
	if id.ValueString() != "c-42" {
		t.Errorf("expected imported id c-42, got %s", id.ValueString())
	}
}

func TestImportRejectsPathSeparator(t *testing.T) {
	r := NewResource().(*kubernetesClusterResource)

	var schemaResp resource.SchemaResponse
	r.Schema(context.Background(), resource.SchemaRequest{}, &schemaResp)

	stateVal := tftypes.NewValue(schemaResp.Schema.Type().TerraformType(context.Background()), nil)
	importResp := resource.ImportStateResponse{State: tfsdk.State{Schema: schemaResp.Schema, Raw: stateVal}}
	r.ImportState(context.Background(), resource.ImportStateRequest{ID: "../v1/api-keys/k-1"}, &importResp)
	if !importResp.Diagnostics.HasError() {
		t.Error("expected error for an import ID containing path separators")
	}
}
