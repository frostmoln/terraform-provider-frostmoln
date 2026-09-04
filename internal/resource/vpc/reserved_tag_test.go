package vpc

import (
	"context"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// Wires the reserved-key filter to THIS call site. reservedmeta's own unit tests
// cover the predicate; without this, deleting the FilterNetwork call here leaves
// CI green and the resource ships unfiltered.
func TestVPCModelFromAPI_ReservedTagsNeverReachState(t *testing.T) {
	ctx := context.Background()

	var m VPCModel
	var diags diag.Diagnostics
	m.fromAPI(ctx, &apiVPC{
		ID: "vpc-1",
		Tags: map[string]string{
			"env":            "prod",
			"frostmoln_type": "kubernetes-control-plane",
			// Reserved for VOLUMES, not for network — must survive.
			"customer-id":    "cust-1",
			"frostmoln-type": "mine",
		},
	}, &diags)
	if diags.HasError() {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}

	got := map[string]string{}
	diags.Append(m.Tags.ElementsAs(ctx, &got, false)...)
	if _, ok := got["frostmoln_type"]; ok {
		t.Errorf("reserved key reached state: %v", got)
	}
	for _, k := range []string{"env", "customer-id", "frostmoln-type"} {
		if _, ok := got[k]; !ok {
			t.Errorf("customer tag %q was dropped: %v", k, got)
		}
	}

	// Config wrote an empty map and every remaining key is platform-owned:
	// answering null there is a plan that never converges.
	empty, d := types.MapValueFrom(ctx, types.StringType, map[string]string{})
	if d.HasError() {
		t.Fatalf("fixture: %v", d)
	}
	m2 := VPCModel{Tags: empty}
	var diags2 diag.Diagnostics
	m2.fromAPI(ctx, &apiVPC{ID: "vpc-1", Tags: map[string]string{"frostmoln_type": "x"}}, &diags2)
	if m2.Tags.IsNull() {
		t.Error("answered null for a config that wrote an empty map")
	}
}
