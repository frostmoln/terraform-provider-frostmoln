package kubernetes_node_pool

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
)

// kubernetes_version comes off the wire as-is; absent (or "") is null, never "".
func TestKubernetesVersionFromTheWire(t *testing.T) {
	for body, want := range map[string]string{
		`{"id":"np-1","kubernetesVersion":"1.35.3"}`: "1.35.3",
		`{"id":"np-1"}`:                        "",
		`{"id":"np-1","kubernetesVersion":""}`: "",
	} {
		var p apiNodePool
		if err := json.Unmarshal([]byte(body), &p); err != nil {
			t.Fatal(err)
		}
		var m KubernetesNodePoolModel
		m.fromAPI(&p)
		if want == "" {
			if !m.KubernetesVersion.IsNull() {
				t.Errorf("%s: kubernetes_version = %v, want null", body, m.KubernetesVersion)
			}
			continue
		}
		if m.KubernetesVersion.ValueString() != want {
			t.Errorf("%s: kubernetes_version = %v, want %q", body, m.KubernetesVersion, want)
		}
	}
}

// Read-only and platform-moving: no plan modifier, so a rolled patch never
// produces an inconsistent result or a pinned stale plan value.
func TestKubernetesVersionIsComputedOnlyAndUnpinned(t *testing.T) {
	var resp resource.SchemaResponse
	NewResource().Schema(context.Background(), resource.SchemaRequest{}, &resp)
	a, ok := resp.Schema.Attributes["kubernetes_version"].(schema.StringAttribute)
	if !ok {
		t.Fatal("kubernetes_version missing or not a string")
	}
	if !a.Computed || a.Optional || a.Required {
		t.Error("kubernetes_version must be Computed only")
	}
	if len(a.PlanModifiers) != 0 {
		t.Errorf("kubernetes_version must carry no plan modifier, has %d", len(a.PlanModifiers))
	}
}
