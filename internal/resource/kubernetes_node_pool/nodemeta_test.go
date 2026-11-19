package kubernetes_node_pool

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/nodemeta"
)

func plannedMeta(t *testing.T) (types.Map, types.Set) {
	t.Helper()
	labels := types.MapValueMust(types.StringType, map[string]attr.Value{"workload": types.StringValue("sensors")})
	objType := types.ObjectType{AttrTypes: nodemeta.TaintAttrTypes}
	taints := types.SetValueMust(objType, []attr.Value{types.ObjectValueMust(nodemeta.TaintAttrTypes, map[string]attr.Value{
		"key": types.StringValue("dedicated"), "value": types.StringValue("sensors"), "effect": types.StringValue("NoSchedule"),
	})})
	return labels, taints
}

// The create carries labels and taints, and the read-back keeps them in state.
func TestCreate_SendsLabelsAndTaints(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := testPool(statusActive)
		p.Labels = map[string]string{"workload": "sensors"}
		p.Taints = []nodemeta.APITaint{{Key: "dedicated", Value: "sensors", Effect: "NoSchedule"}}
		switch {
		case r.Method == http.MethodPost:
			var body apiCreateNodePoolRequest
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decode: %v", err)
			}
			if body.Labels["workload"] != "sensors" || len(body.Taints) != 1 || body.Taints[0] != p.Taints[0] {
				t.Errorf("create body lacks labels/taints: %+v", body)
			}
			w.WriteHeader(http.StatusCreated)
			writeJSON(t, w, p)
		case r.Method == http.MethodGet && !strings.HasSuffix(r.URL.Path, "/events"):
			writeJSON(t, w, p)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	labels, taints := plannedMeta(t)
	plan := buildPlan(t, KubernetesNodePoolModel{
		ClusterID: types.StringValue("c-1"), Name: types.StringValue("workers"),
		FlavorID: types.StringValue("k8s.gp1.medium"), NodeCount: types.Int64Value(2),
		ID: types.StringUnknown(), Status: types.StringUnknown(), CreatedAt: types.StringUnknown(), UpdatedAt: types.StringUnknown(),
		Labels: labels, Taints: taints,
	})
	resp := resource.CreateResponse{State: emptyState(t)}
	testResource(newTestClient(t, server)).Create(context.Background(), resource.CreateRequest{Plan: plan}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatal(resp.Diagnostics.Errors())
	}
	var state KubernetesNodePoolModel
	resp.State.Get(context.Background(), &state)
	if !state.Labels.Equal(labels) || !state.Taints.Equal(taints) {
		t.Errorf("state lost labels/taints: %v %v", state.Labels, state.Taints)
	}
}

// only_tainted_nodes is permanent: one POST, no retry, and a diagnostic that says
// to add an untainted pool first.
func TestCreate_OnlyTaintedNodesFailsFastWithGuidance(t *testing.T) {
	var posts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			posts.Add(1)
			w.WriteHeader(http.StatusConflict)
			writeJSON(t, w, map[string]any{"code": "conflict", "message": "m", "details": map[string]any{"reason": "only_tainted_nodes"}})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	labels, taints := plannedMeta(t)
	plan := buildPlan(t, KubernetesNodePoolModel{
		ClusterID: types.StringValue("c-1"), Name: types.StringValue("workers"),
		FlavorID: types.StringValue("k8s.gp1.medium"), NodeCount: types.Int64Value(2),
		ID: types.StringUnknown(), Status: types.StringUnknown(), CreatedAt: types.StringUnknown(), UpdatedAt: types.StringUnknown(),
		Labels: labels, Taints: taints,
	})
	resp := resource.CreateResponse{State: emptyState(t)}
	testResource(newTestClient(t, server)).Create(context.Background(), resource.CreateRequest{Plan: plan}, &resp)
	if !resp.Diagnostics.HasError() {
		t.Fatal("want an error")
	}
	if n := posts.Load(); n != 1 {
		t.Errorf("POSTs = %d, want 1 (fail-fast)", n)
	}
	d := resp.Diagnostics.Errors()[0]
	if !strings.Contains(d.Summary(), "Only Tainted") || !strings.Contains(d.Detail(), "Add a node pool without") {
		t.Errorf("want the tailored diagnostic, got %q / %q", d.Summary(), d.Detail())
	}
}

// Delete of the last untainted pool is refused with only_tainted_nodes: tailored diagnostic.
func TestDelete_OnlyTaintedNodesIsExplained(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			writeJSON(t, w, testPool(statusActive))
		case http.MethodDelete:
			w.WriteHeader(http.StatusConflict)
			writeJSON(t, w, map[string]any{"code": "conflict", "message": "m", "details": map[string]any{"reason": "only_tainted_nodes"}})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	state := buildState(t, KubernetesNodePoolModel{
		ID: types.StringValue("np-2"), ClusterID: types.StringValue("c-1"), Name: types.StringValue("workers"),
		FlavorID: types.StringValue("k8s.gp1.medium"), NodeCount: types.Int64Value(2),
	})
	resp := resource.DeleteResponse{State: state}
	testResource(newTestClient(t, server)).Delete(context.Background(), resource.DeleteRequest{State: state}, &resp)
	if !resp.Diagnostics.HasError() || !strings.Contains(resp.Diagnostics.Errors()[0].Summary(), "Only Tainted") {
		t.Fatalf("want the tailored diagnostic, got %v", resp.Diagnostics)
	}
}
