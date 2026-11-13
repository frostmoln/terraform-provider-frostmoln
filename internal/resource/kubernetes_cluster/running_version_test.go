package kubernetes_cluster

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// The running patch versions come off the wire as-is; an absent field is null —
// never "" and never derived from kubernetesVersion (the minor line).
func TestRunningVersionsFromTheWire(t *testing.T) {
	for _, tc := range []struct {
		name, cluster, pool string
		wantCP, wantPool    *string
	}{
		{
			name:    "known",
			cluster: `{"id":"c-1","kubernetesVersion":"1.35","controlPlaneVersion":"1.35.3"}`,
			pool:    `{"id":"np-1","kubernetesVersion":"1.35.2"}`,
			wantCP:  ptr("1.35.3"), wantPool: ptr("1.35.2"),
		},
		{
			name:    "absent",
			cluster: `{"id":"c-1","kubernetesVersion":"1.35"}`,
			pool:    `{"id":"np-1"}`,
		},
		{
			name:    "empty string is unknown too",
			cluster: `{"id":"c-1","kubernetesVersion":"1.35","controlPlaneVersion":""}`,
			pool:    `{"id":"np-1","kubernetesVersion":""}`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var c apiKubernetesCluster
			var p apiNodePool
			if err := json.Unmarshal([]byte(tc.cluster), &c); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal([]byte(tc.pool), &p); err != nil {
				t.Fatal(err)
			}
			var m KubernetesClusterModel
			m.fromAPI(&c)
			m.setInitialNodePool(context.Background(), &p, types.MapNull(types.StringType), &diag.Diagnostics{})
			assertVersion(t, "control_plane_version", m.ControlPlaneVersion, tc.wantCP)
			assertVersion(t, "initial_node_pool.kubernetes_version", m.InitialNodePool.KubernetesVersion, tc.wantPool)
		})
	}
}

// Both attributes are read-only and move when the platform rolls a patch, so they
// must carry no plan modifier: UseStateForUnknown would plan the stale state value
// and the apply would then return the new patch (an inconsistent result).
func TestRunningVersionsAreComputedOnlyAndUnpinned(t *testing.T) {
	var resp resource.SchemaResponse
	NewResource().Schema(context.Background(), resource.SchemaRequest{}, &resp)
	cp, ok := resp.Schema.Attributes["control_plane_version"].(schema.StringAttribute)
	if !ok {
		t.Fatal("control_plane_version missing or not a string")
	}
	pool, ok := resp.Schema.Attributes["initial_node_pool"].(schema.SingleNestedAttribute)
	if !ok {
		t.Fatal("initial_node_pool missing")
	}
	pv, ok := pool.Attributes["kubernetes_version"].(schema.StringAttribute)
	if !ok {
		t.Fatal("initial_node_pool.kubernetes_version missing or not a string")
	}
	for name, a := range map[string]schema.StringAttribute{"control_plane_version": cp, "initial_node_pool.kubernetes_version": pv} {
		if !a.Computed || a.Optional || a.Required {
			t.Errorf("%s must be Computed only", name)
		}
		if len(a.PlanModifiers) != 0 {
			t.Errorf("%s must carry no plan modifier, has %d", name, len(a.PlanModifiers))
		}
	}
}

func assertVersion(t *testing.T, name string, got types.String, want *string) {
	t.Helper()
	if want == nil {
		if !got.IsNull() {
			t.Errorf("%s = %v, want null", name, got)
		}
		return
	}
	if got.IsNull() || got.ValueString() != *want {
		t.Errorf("%s = %v, want %q", name, got, *want)
	}
}

func ptr(s string) *string { return &s }
