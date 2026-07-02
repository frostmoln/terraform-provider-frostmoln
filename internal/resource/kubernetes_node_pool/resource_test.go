package kubernetes_node_pool

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
)

// --- Model unit tests ---

func TestToCreateRequestMinimal(t *testing.T) {
	m := KubernetesNodePoolModel{
		ClusterID: types.StringValue("c-1"),
		Name:      types.StringUnknown(),
		FlavorID:  types.StringValue("k8s.gp1.small"),
		NodeCount: types.Int64Value(1),
	}

	req := m.toCreateRequest()
	if req.FlavorID != "k8s.gp1.small" || req.NodeCount != 1 {
		t.Errorf("unexpected create request: %+v", req)
	}
	if req.Name != "" {
		t.Errorf("expected empty name for unknown value (backend generates one), got %q", req.Name)
	}
	b, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(b), `"name"`) {
		t.Errorf("expected name omitted from wire body when unset, got %s", b)
	}
}

func TestToCreateRequestNamed(t *testing.T) {
	m := KubernetesNodePoolModel{
		ClusterID: types.StringValue("c-1"),
		Name:      types.StringValue("workers"),
		FlavorID:  types.StringValue("k8s.gp1.medium"),
		NodeCount: types.Int64Value(3),
	}

	req := m.toCreateRequest()
	if req.Name != "workers" || req.FlavorID != "k8s.gp1.medium" || req.NodeCount != 3 {
		t.Errorf("unexpected create request: %+v", req)
	}
}

func TestFromAPI(t *testing.T) {
	var m KubernetesNodePoolModel
	m.fromAPI(&apiNodePool{
		ID:        "np-2",
		ClusterID: "c-1",
		Name:      "pool-ab12cd34",
		Status:    statusActive,
		FlavorID:  "k8s.gp1.small",
		NodeCount: 2,
		CreatedAt: "2026-07-01T00:00:00Z",
		UpdatedAt: "2026-07-01T00:10:00Z",
	})

	if m.ID.ValueString() != "np-2" || m.ClusterID.ValueString() != "c-1" {
		t.Errorf("unexpected identity: %s/%s", m.ClusterID.ValueString(), m.ID.ValueString())
	}
	if m.Name.ValueString() != "pool-ab12cd34" {
		t.Errorf("expected generated name adopted, got %q", m.Name.ValueString())
	}
	if m.NodeCount.ValueInt64() != 2 || m.Status.ValueString() != statusActive {
		t.Errorf("unexpected pool state: %+v", m)
	}
	if m.UpdatedAt.ValueString() != "2026-07-01T00:10:00Z" {
		t.Errorf("unexpected updated_at: %v", m.UpdatedAt)
	}
}

func TestFromAPIPreservesClusterIDWhenEmpty(t *testing.T) {
	m := KubernetesNodePoolModel{ClusterID: types.StringValue("c-1")}
	m.fromAPI(&apiNodePool{ID: "np-2", Name: "workers", Status: statusActive, CreatedAt: "2026-07-01T00:00:00Z"})
	if m.ClusterID.ValueString() != "c-1" {
		t.Error("fromAPI must keep the state cluster_id when the response omits clusterId")
	}
	if m.UpdatedAt.IsNull() == false {
		t.Error("expected null updated_at for empty response value")
	}
}

// --- Resource plumbing tests ---

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
	if resp.TypeName != "frostmoln_kubernetes_node_pool" {
		t.Errorf("expected type name frostmoln_kubernetes_node_pool, got %s", resp.TypeName)
	}
}

func TestSchema(t *testing.T) {
	r := NewResource()
	var resp resource.SchemaResponse
	r.Schema(context.Background(), resource.SchemaRequest{}, &resp)

	for _, attr := range []string{
		"id", "cluster_id", "name", "flavor_id", "node_count", "status", "created_at", "updated_at",
	} {
		if _, ok := resp.Schema.Attributes[attr]; !ok {
			t.Errorf("expected attribute %s in schema", attr)
		}
	}
}

func TestConfigureNilProviderData(t *testing.T) {
	r := &kubernetesNodePoolResource{}
	var resp resource.ConfigureResponse
	r.Configure(context.Background(), resource.ConfigureRequest{}, &resp)
	if resp.Diagnostics.HasError() {
		t.Errorf("expected no errors for nil provider data, got %v", resp.Diagnostics.Errors())
	}
}

func TestConfigureWrongType(t *testing.T) {
	r := &kubernetesNodePoolResource{}
	var resp resource.ConfigureResponse
	r.Configure(context.Background(), resource.ConfigureRequest{ProviderData: "not-a-client"}, &resp)
	if !resp.Diagnostics.HasError() {
		t.Error("expected error for wrong provider data type")
	}
}

// --- test harness ---

func buildState(t *testing.T, model KubernetesNodePoolModel) tfsdk.State {
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

func buildPlan(t *testing.T, model KubernetesNodePoolModel) tfsdk.Plan {
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

func testResource(c *client.Client) *kubernetesNodePoolResource {
	return &kubernetesNodePoolResource{
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

func testPool(status string) apiNodePool {
	return apiNodePool{
		ID:        "np-2",
		ClusterID: "c-1",
		Name:      "workers",
		Status:    status,
		FlavorID:  "k8s.gp1.small",
		NodeCount: 2,
		IsInitial: false,
		CreatedAt: "2026-07-01T00:00:00Z",
	}
}

const (
	poolsPath = "/v1/tenants/t-1/kubernetes-clusters/c-1/node-pools"
	poolPath  = poolsPath + "/np-2"
)

func newTestClient(t *testing.T, server *httptest.Server) *client.Client {
	t.Helper()
	c := client.NewClient(server.URL, "test-key", client.WithHTTPClient(server.Client()))
	c.SetTenantIDForTest("t-1")
	return c
}

// --- Create ---

func TestCreate(t *testing.T) {
	var poolGets atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == poolsPath:
			var body apiCreateNodePoolRequest
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("failed to decode request: %v", err)
			}
			if body.Name != "workers" || body.FlavorID != "k8s.gp1.small" || body.NodeCount != 2 {
				t.Errorf("unexpected create request: %+v", body)
			}
			created := testPool("creating")
			w.WriteHeader(http.StatusCreated)
			writeJSON(t, w, created)
		case r.Method == http.MethodGet && r.URL.Path == poolPath:
			p := testPool(statusActive)
			if poolGets.Add(1) < 2 {
				p.Status = "creating"
			}
			writeJSON(t, w, p)
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	r := testResource(newTestClient(t, server))

	plan := buildPlan(t, KubernetesNodePoolModel{
		ClusterID: types.StringValue("c-1"),
		Name:      types.StringValue("workers"),
		FlavorID:  types.StringValue("k8s.gp1.small"),
		NodeCount: types.Int64Value(2),
		ID:        types.StringUnknown(),
		Status:    types.StringUnknown(),
		CreatedAt: types.StringUnknown(),
		UpdatedAt: types.StringUnknown(),
	})

	req := resource.CreateRequest{Plan: plan}
	resp := resource.CreateResponse{State: emptyState(t)}
	r.Create(context.Background(), req, &resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected error: %v", resp.Diagnostics.Errors())
	}

	var state KubernetesNodePoolModel
	if diags := resp.State.Get(context.Background(), &state); diags.HasError() {
		t.Fatalf("failed to get state: %v", diags.Errors())
	}
	if state.ID.ValueString() != "np-2" || state.Status.ValueString() != statusActive {
		t.Errorf("unexpected final state: %+v", state)
	}
	if state.ClusterID.ValueString() != "c-1" {
		t.Errorf("unexpected cluster_id: %s", state.ClusterID.ValueString())
	}
}

// TestCreateAdoptsGeneratedName: name omitted → backend generates pool-<8 hex>;
// the provider adopts it into state.
func TestCreateAdoptsGeneratedName(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == poolsPath:
			var body apiCreateNodePoolRequest
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("failed to decode request: %v", err)
			}
			if body.Name != "" {
				t.Errorf("expected no name in create request, got %q", body.Name)
			}
			created := testPool("creating")
			created.Name = "pool-ab12cd34"
			w.WriteHeader(http.StatusCreated)
			writeJSON(t, w, created)
		case r.Method == http.MethodGet && r.URL.Path == poolPath:
			p := testPool(statusActive)
			p.Name = "pool-ab12cd34"
			writeJSON(t, w, p)
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	r := testResource(newTestClient(t, server))

	plan := buildPlan(t, KubernetesNodePoolModel{
		ClusterID: types.StringValue("c-1"),
		Name:      types.StringUnknown(),
		FlavorID:  types.StringValue("k8s.gp1.small"),
		NodeCount: types.Int64Value(2),
		ID:        types.StringUnknown(),
		Status:    types.StringUnknown(),
		CreatedAt: types.StringUnknown(),
		UpdatedAt: types.StringUnknown(),
	})

	resp := resource.CreateResponse{State: emptyState(t)}
	r.Create(context.Background(), resource.CreateRequest{Plan: plan}, &resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected error: %v", resp.Diagnostics.Errors())
	}
	var state KubernetesNodePoolModel
	if diags := resp.State.Get(context.Background(), &state); diags.HasError() {
		t.Fatalf("failed to get state: %v", diags.Errors())
	}
	if state.Name.ValueString() != "pool-ab12cd34" {
		t.Errorf("expected generated name adopted into state, got %q", state.Name.ValueString())
	}
}

// TestCreateRetriesInvalidStateConflict: the backend answers 409 invalid_state
// while the cluster is transiently not "running" — the create must retry.
func TestCreateRetriesInvalidStateConflict(t *testing.T) {
	var posts atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == poolsPath:
			if posts.Add(1) < 3 {
				w.WriteHeader(http.StatusConflict)
				writeJSON(t, w, map[string]any{
					"code":    "invalid_state",
					"message": "kubernetes cluster is in an invalid state for this operation",
				})
				return
			}
			w.WriteHeader(http.StatusCreated)
			writeJSON(t, w, testPool("creating"))
		case r.Method == http.MethodGet && r.URL.Path == poolPath:
			writeJSON(t, w, testPool(statusActive))
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	r := testResource(newTestClient(t, server))

	plan := buildPlan(t, KubernetesNodePoolModel{
		ClusterID: types.StringValue("c-1"),
		Name:      types.StringValue("workers"),
		FlavorID:  types.StringValue("k8s.gp1.small"),
		NodeCount: types.Int64Value(2),
		ID:        types.StringUnknown(),
		Status:    types.StringUnknown(),
		CreatedAt: types.StringUnknown(),
		UpdatedAt: types.StringUnknown(),
	})

	resp := resource.CreateResponse{State: emptyState(t)}
	r.Create(context.Background(), resource.CreateRequest{Plan: plan}, &resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("expected create to retry through transient 409, got: %v", resp.Diagnostics.Errors())
	}
	if got := posts.Load(); got != 3 {
		t.Errorf("expected 3 create attempts, got %d", got)
	}
}

// TestCreateDuplicateNameFailsFast: a duplicate-name 409 (code "conflict") is
// permanent — no retry.
func TestCreateDuplicateNameFailsFast(t *testing.T) {
	var posts atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == poolsPath {
			posts.Add(1)
			w.WriteHeader(http.StatusConflict)
			writeJSON(t, w, map[string]any{
				"code":    "conflict",
				"message": "node pool with this name already exists",
			})
			return
		}
		t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	r := testResource(newTestClient(t, server))

	plan := buildPlan(t, KubernetesNodePoolModel{
		ClusterID: types.StringValue("c-1"),
		Name:      types.StringValue("workers"),
		FlavorID:  types.StringValue("k8s.gp1.small"),
		NodeCount: types.Int64Value(2),
		ID:        types.StringUnknown(),
		Status:    types.StringUnknown(),
		CreatedAt: types.StringUnknown(),
		UpdatedAt: types.StringUnknown(),
	})

	resp := resource.CreateResponse{State: emptyState(t)}
	r.Create(context.Background(), resource.CreateRequest{Plan: plan}, &resp)

	if !resp.Diagnostics.HasError() {
		t.Fatal("expected error for duplicate pool name")
	}
	if got := posts.Load(); got != 1 {
		t.Errorf("expected exactly 1 create attempt (no retry on duplicate name), got %d", got)
	}
}

// TestCreateNameRecycle: creating a pool whose name matches a soft-deleted
// error row RECYCLES it (200 with the recycled pool's original ID) — the
// provider must adopt whatever ID the backend answers with.
func TestCreateNameRecycle(t *testing.T) {
	recycled := testPool("creating")
	recycled.ID = "np-recycled"
	recycledPath := poolsPath + "/np-recycled"

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == poolsPath:
			// Recycle answers 200 (not 201) with the prior row's ID.
			writeJSON(t, w, recycled)
		case r.Method == http.MethodGet && r.URL.Path == recycledPath:
			p := recycled
			p.Status = statusActive
			writeJSON(t, w, p)
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	r := testResource(newTestClient(t, server))

	plan := buildPlan(t, KubernetesNodePoolModel{
		ClusterID: types.StringValue("c-1"),
		Name:      types.StringValue("workers"),
		FlavorID:  types.StringValue("k8s.gp1.small"),
		NodeCount: types.Int64Value(2),
		ID:        types.StringUnknown(),
		Status:    types.StringUnknown(),
		CreatedAt: types.StringUnknown(),
		UpdatedAt: types.StringUnknown(),
	})

	resp := resource.CreateResponse{State: emptyState(t)}
	r.Create(context.Background(), resource.CreateRequest{Plan: plan}, &resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected error: %v", resp.Diagnostics.Errors())
	}
	var state KubernetesNodePoolModel
	if diags := resp.State.Get(context.Background(), &state); diags.HasError() {
		t.Fatalf("failed to get state: %v", diags.Errors())
	}
	if state.ID.ValueString() != "np-recycled" {
		t.Errorf("expected recycled pool ID adopted, got %s", state.ID.ValueString())
	}
}

// TestCreatePollError: the pool reaching "error" during the post-create poll
// must fail the apply, with the pool ID already persisted (orphan guard).
func TestCreatePollError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == poolsPath:
			w.WriteHeader(http.StatusCreated)
			writeJSON(t, w, testPool("creating"))
		case r.Method == http.MethodGet && r.URL.Path == poolPath:
			writeJSON(t, w, testPool(statusError))
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	r := testResource(newTestClient(t, server))

	plan := buildPlan(t, KubernetesNodePoolModel{
		ClusterID: types.StringValue("c-1"),
		Name:      types.StringValue("workers"),
		FlavorID:  types.StringValue("k8s.gp1.small"),
		NodeCount: types.Int64Value(2),
		ID:        types.StringUnknown(),
		Status:    types.StringUnknown(),
		CreatedAt: types.StringUnknown(),
		UpdatedAt: types.StringUnknown(),
	})

	resp := resource.CreateResponse{State: emptyState(t)}
	r.Create(context.Background(), resource.CreateRequest{Plan: plan}, &resp)

	if !resp.Diagnostics.HasError() {
		t.Fatal("expected error when pool reaches error state")
	}
	var state KubernetesNodePoolModel
	if diags := resp.State.Get(context.Background(), &state); diags.HasError() {
		t.Fatalf("failed to get state: %v", diags.Errors())
	}
	if state.ID.ValueString() != "np-2" {
		t.Error("expected pool ID persisted in state before the failed poll (orphan guard)")
	}
}

// --- Read ---

func TestRead(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == poolPath {
			p := testPool(statusActive)
			p.NodeCount = 5 // out-of-band scale must surface as drift
			writeJSON(t, w, p)
			return
		}
		t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	r := testResource(newTestClient(t, server))

	state := buildState(t, KubernetesNodePoolModel{
		ID:        types.StringValue("np-2"),
		ClusterID: types.StringValue("c-1"),
		Name:      types.StringValue("workers"),
		FlavorID:  types.StringValue("k8s.gp1.small"),
		NodeCount: types.Int64Value(2),
		Status:    types.StringValue(statusActive),
		CreatedAt: types.StringValue("2026-07-01T00:00:00Z"),
	})

	resp := resource.ReadResponse{State: state}
	r.Read(context.Background(), resource.ReadRequest{State: state}, &resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected error: %v", resp.Diagnostics.Errors())
	}
	var got KubernetesNodePoolModel
	if diags := resp.State.Get(context.Background(), &got); diags.HasError() {
		t.Fatalf("failed to get state: %v", diags.Errors())
	}
	if got.NodeCount.ValueInt64() != 5 {
		t.Errorf("expected refreshed node_count 5, got %d", got.NodeCount.ValueInt64())
	}
}

func TestReadRemovesOn404(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		writeJSON(t, w, map[string]any{"code": "not_found", "message": "node pool not found"})
	}))
	defer server.Close()

	r := testResource(newTestClient(t, server))

	state := buildState(t, KubernetesNodePoolModel{
		ID:        types.StringValue("np-2"),
		ClusterID: types.StringValue("c-1"),
		Name:      types.StringValue("workers"),
		FlavorID:  types.StringValue("k8s.gp1.small"),
		NodeCount: types.Int64Value(2),
	})

	resp := resource.ReadResponse{State: state}
	r.Read(context.Background(), resource.ReadRequest{State: state}, &resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected error: %v", resp.Diagnostics.Errors())
	}
	if !resp.State.Raw.IsNull() {
		t.Error("expected resource removed from state on 404")
	}
}

func TestReadRemovesOnSoftDeleted(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Soft delete: 200 with status "deleted" forever — never 404.
		writeJSON(t, w, testPool(statusDeleted))
	}))
	defer server.Close()

	r := testResource(newTestClient(t, server))

	state := buildState(t, KubernetesNodePoolModel{
		ID:        types.StringValue("np-2"),
		ClusterID: types.StringValue("c-1"),
		Name:      types.StringValue("workers"),
		FlavorID:  types.StringValue("k8s.gp1.small"),
		NodeCount: types.Int64Value(2),
	})

	resp := resource.ReadResponse{State: state}
	r.Read(context.Background(), resource.ReadRequest{State: state}, &resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected error: %v", resp.Diagnostics.Errors())
	}
	if !resp.State.Raw.IsNull() {
		t.Error("expected resource removed from state on soft-deleted status")
	}
}

// TestReadRefusesInitialPool: the initial pool belongs to the cluster
// resource; reading it here (only reachable via import) must error.
func TestReadRefusesInitialPool(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := testPool(statusActive)
		p.IsInitial = true
		p.Name = "default"
		writeJSON(t, w, p)
	}))
	defer server.Close()

	r := testResource(newTestClient(t, server))

	state := buildState(t, KubernetesNodePoolModel{
		ID:        types.StringValue("np-2"),
		ClusterID: types.StringValue("c-1"),
	})

	resp := resource.ReadResponse{State: state}
	r.Read(context.Background(), resource.ReadRequest{State: state}, &resp)

	if !resp.Diagnostics.HasError() {
		t.Fatal("expected error when reading the cluster's initial pool")
	}
	found := false
	for _, d := range resp.Diagnostics.Errors() {
		if strings.Contains(d.Detail(), "initial pool") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected an initial-pool refusal diagnostic, got %v", resp.Diagnostics.Errors())
	}
}

func TestReadMissingIdentity(t *testing.T) {
	r := testResource(nil)

	state := buildState(t, KubernetesNodePoolModel{
		ID:        types.StringValue(""),
		ClusterID: types.StringValue("c-1"),
	})

	resp := resource.ReadResponse{State: state}
	r.Read(context.Background(), resource.ReadRequest{State: state}, &resp)

	if !resp.Diagnostics.HasError() {
		t.Fatal("expected error for missing pool ID")
	}
}

// --- Update (scale) ---

func TestUpdateScales(t *testing.T) {
	var scaled, poolGets atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == poolPath+"/scale":
			var body apiScaleNodePoolRequest
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("failed to decode request: %v", err)
			}
			if body.NodeCount != 4 {
				t.Errorf("expected nodeCount 4, got %d", body.NodeCount)
			}
			scaled.Add(1)
			p := testPool("scaling")
			p.NodeCount = 4
			writeJSON(t, w, p)
		case r.Method == http.MethodGet && r.URL.Path == poolPath:
			p := testPool(statusActive)
			p.NodeCount = 4
			if poolGets.Add(1) < 2 {
				p.Status = "scaling"
			}
			writeJSON(t, w, p)
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	r := testResource(newTestClient(t, server))

	prior := KubernetesNodePoolModel{
		ID:        types.StringValue("np-2"),
		ClusterID: types.StringValue("c-1"),
		Name:      types.StringValue("workers"),
		FlavorID:  types.StringValue("k8s.gp1.small"),
		NodeCount: types.Int64Value(2),
		Status:    types.StringValue(statusActive),
		CreatedAt: types.StringValue("2026-07-01T00:00:00Z"),
	}
	planned := prior
	planned.NodeCount = types.Int64Value(4)

	resp := resource.UpdateResponse{State: buildState(t, prior)}
	r.Update(context.Background(), resource.UpdateRequest{
		Plan:  buildPlan(t, planned),
		State: buildState(t, prior),
	}, &resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected error: %v", resp.Diagnostics.Errors())
	}
	if scaled.Load() != 1 {
		t.Errorf("expected exactly 1 scale call, got %d", scaled.Load())
	}
	var got KubernetesNodePoolModel
	if diags := resp.State.Get(context.Background(), &got); diags.HasError() {
		t.Fatalf("failed to get state: %v", diags.Errors())
	}
	if got.NodeCount.ValueInt64() != 4 || got.Status.ValueString() != statusActive {
		t.Errorf("unexpected final state: count=%d status=%s", got.NodeCount.ValueInt64(), got.Status.ValueString())
	}
}

// TestUpdateNoChangeSkipsScale: an update with no node_count change (e.g.
// refresh-driven) must not call /scale.
func TestUpdateNoChangeSkipsScale(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == poolPath {
			writeJSON(t, w, testPool(statusActive))
			return
		}
		t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	r := testResource(newTestClient(t, server))

	prior := KubernetesNodePoolModel{
		ID:        types.StringValue("np-2"),
		ClusterID: types.StringValue("c-1"),
		Name:      types.StringValue("workers"),
		FlavorID:  types.StringValue("k8s.gp1.small"),
		NodeCount: types.Int64Value(2),
		Status:    types.StringValue(statusActive),
		CreatedAt: types.StringValue("2026-07-01T00:00:00Z"),
	}

	resp := resource.UpdateResponse{State: buildState(t, prior)}
	r.Update(context.Background(), resource.UpdateRequest{
		Plan:  buildPlan(t, prior),
		State: buildState(t, prior),
	}, &resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected error: %v", resp.Diagnostics.Errors())
	}
}

// TestUpdateScale404: the backend answers 404 for a soft-deleted pool
// (stale-state resurrection guard) — surface an actionable error.
func TestUpdateScale404(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == poolPath+"/scale" {
			w.WriteHeader(http.StatusNotFound)
			writeJSON(t, w, map[string]any{"code": "not_found", "message": "node pool not found"})
			return
		}
		t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	r := testResource(newTestClient(t, server))

	prior := KubernetesNodePoolModel{
		ID:        types.StringValue("np-2"),
		ClusterID: types.StringValue("c-1"),
		Name:      types.StringValue("workers"),
		FlavorID:  types.StringValue("k8s.gp1.small"),
		NodeCount: types.Int64Value(2),
		Status:    types.StringValue(statusActive),
		CreatedAt: types.StringValue("2026-07-01T00:00:00Z"),
	}
	planned := prior
	planned.NodeCount = types.Int64Value(4)

	resp := resource.UpdateResponse{State: buildState(t, prior)}
	r.Update(context.Background(), resource.UpdateRequest{
		Plan:  buildPlan(t, planned),
		State: buildState(t, prior),
	}, &resp)

	if !resp.Diagnostics.HasError() {
		t.Fatal("expected error when scaling a deleted pool")
	}
	found := false
	for _, d := range resp.Diagnostics.Errors() {
		if strings.Contains(d.Summary(), "no longer exists") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected a pool-no-longer-exists diagnostic, got %v", resp.Diagnostics.Errors())
	}
}

// TestUpdateScaleConflict: a 409 (scale already in progress / non-scalable
// state) surfaces as-is.
func TestUpdateScaleConflict(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == poolPath+"/scale" {
			w.WriteHeader(http.StatusConflict)
			writeJSON(t, w, map[string]any{
				"code":    "conflict",
				"message": "a scale operation for this node pool is already in progress; retry when it completes",
			})
			return
		}
		t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	r := testResource(newTestClient(t, server))

	prior := KubernetesNodePoolModel{
		ID:        types.StringValue("np-2"),
		ClusterID: types.StringValue("c-1"),
		Name:      types.StringValue("workers"),
		FlavorID:  types.StringValue("k8s.gp1.small"),
		NodeCount: types.Int64Value(2),
		Status:    types.StringValue(statusActive),
		CreatedAt: types.StringValue("2026-07-01T00:00:00Z"),
	}
	planned := prior
	planned.NodeCount = types.Int64Value(4)

	resp := resource.UpdateResponse{State: buildState(t, prior)}
	r.Update(context.Background(), resource.UpdateRequest{
		Plan:  buildPlan(t, planned),
		State: buildState(t, prior),
	}, &resp)

	if !resp.Diagnostics.HasError() {
		t.Fatal("expected error for scale conflict")
	}
	found := false
	for _, d := range resp.Diagnostics.Errors() {
		if strings.Contains(d.Detail(), "already in progress") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected the API conflict message surfaced as-is, got %v", resp.Diagnostics.Errors())
	}
}

// --- Delete ---

func TestDelete(t *testing.T) {
	var poolGets atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodDelete && r.URL.Path == poolPath:
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodGet && r.URL.Path == poolPath:
			// Soft delete: the row keeps answering 200, transitioning
			// deleting → deleted.
			p := testPool(statusDeleted)
			if poolGets.Add(1) < 2 {
				p.Status = "deleting"
			}
			writeJSON(t, w, p)
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	r := testResource(newTestClient(t, server))

	state := buildState(t, KubernetesNodePoolModel{
		ID:        types.StringValue("np-2"),
		ClusterID: types.StringValue("c-1"),
		Name:      types.StringValue("workers"),
		FlavorID:  types.StringValue("k8s.gp1.small"),
		NodeCount: types.Int64Value(2),
	})

	resp := resource.DeleteResponse{State: state}
	r.Delete(context.Background(), resource.DeleteRequest{State: state}, &resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected error: %v", resp.Diagnostics.Errors())
	}
}

// TestDeleteLastPoolConflict: deleting a cluster's last pool is refused by the
// backend (409) — the API error surfaces as-is.
func TestDeleteLastPoolConflict(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == poolPath:
			writeJSON(t, w, testPool(statusActive))
		case r.Method == http.MethodDelete && r.URL.Path == poolPath:
			w.WriteHeader(http.StatusConflict)
			writeJSON(t, w, map[string]any{
				"code":    "conflict",
				"message": "cannot delete the last node pool of a cluster",
			})
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	r := testResource(newTestClient(t, server))

	state := buildState(t, KubernetesNodePoolModel{
		ID:        types.StringValue("np-2"),
		ClusterID: types.StringValue("c-1"),
		Name:      types.StringValue("workers"),
		FlavorID:  types.StringValue("k8s.gp1.small"),
		NodeCount: types.Int64Value(2),
	})

	resp := resource.DeleteResponse{State: state}
	r.Delete(context.Background(), resource.DeleteRequest{State: state}, &resp)

	if !resp.Diagnostics.HasError() {
		t.Fatal("expected error for last-pool delete")
	}
	found := false
	for _, d := range resp.Diagnostics.Errors() {
		if strings.Contains(d.Detail(), "last node pool") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected the last-pool API error surfaced as-is, got %v", resp.Diagnostics.Errors())
	}
}

// TestDeleteAlreadySoftDeleted: a Delete against an already-soft-deleted pool
// must no-op WITHOUT issuing the DELETE — the backend's pre-delete lookup
// still finds the deleted row, so the request would either 409 "last node
// pool" or resurrect the row to "deleting".
func TestDeleteAlreadySoftDeleted(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == poolPath {
			writeJSON(t, w, testPool(statusDeleted))
			return
		}
		t.Errorf("unexpected request (DELETE must not be issued): %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	r := testResource(newTestClient(t, server))

	state := buildState(t, KubernetesNodePoolModel{
		ID:        types.StringValue("np-2"),
		ClusterID: types.StringValue("c-1"),
		Name:      types.StringValue("workers"),
		FlavorID:  types.StringValue("k8s.gp1.small"),
		NodeCount: types.Int64Value(2),
	})

	resp := resource.DeleteResponse{State: state}
	r.Delete(context.Background(), resource.DeleteRequest{State: state}, &resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("expected soft-deleted pool delete to no-op, got: %v", resp.Diagnostics.Errors())
	}
}

func TestDeleteAlreadyGone(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		writeJSON(t, w, map[string]any{"code": "not_found", "message": "node pool not found"})
	}))
	defer server.Close()

	r := testResource(newTestClient(t, server))

	state := buildState(t, KubernetesNodePoolModel{
		ID:        types.StringValue("np-2"),
		ClusterID: types.StringValue("c-1"),
		Name:      types.StringValue("workers"),
		FlavorID:  types.StringValue("k8s.gp1.small"),
		NodeCount: types.Int64Value(2),
	})

	resp := resource.DeleteResponse{State: state}
	r.Delete(context.Background(), resource.DeleteRequest{State: state}, &resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("expected 404 on delete to be tolerated, got: %v", resp.Diagnostics.Errors())
	}
}

// --- Import ---

func TestImportState(t *testing.T) {
	r := NewResource().(*kubernetesNodePoolResource)

	resp := resource.ImportStateResponse{State: emptyState(t)}
	r.ImportState(context.Background(), resource.ImportStateRequest{ID: "c-1/np-2"}, &resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected error: %v", resp.Diagnostics.Errors())
	}
	var got KubernetesNodePoolModel
	if diags := resp.State.Get(context.Background(), &got); diags.HasError() {
		t.Fatalf("failed to get state: %v", diags.Errors())
	}
	if got.ClusterID.ValueString() != "c-1" || got.ID.ValueString() != "np-2" {
		t.Errorf("unexpected imported identity: %s/%s", got.ClusterID.ValueString(), got.ID.ValueString())
	}
}

func TestImportStateInvalidID(t *testing.T) {
	r := NewResource().(*kubernetesNodePoolResource)

	// Dot-segments survive url.PathEscape and would be collapsed by the
	// client's path joining — they must be rejected at the import boundary,
	// as must a "/" inside the pool-ID part.
	for _, id := range []string{"np-2", "/np-2", "c-1/", "", "../..", "c-1/..", "./np-2", "c-1/np-2/extra"} {
		resp := resource.ImportStateResponse{State: emptyState(t)}
		r.ImportState(context.Background(), resource.ImportStateRequest{ID: id}, &resp)
		if !resp.Diagnostics.HasError() {
			t.Errorf("expected error for import ID %q", id)
		}
	}
}
