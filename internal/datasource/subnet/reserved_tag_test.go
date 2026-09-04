package subnet

import (
	"context"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
)

// Wires the reserved-key filter to the DATA SOURCE call site, which has its own
// read path and does not share the resource model's fromAPI. Without this,
// deleting the FilterNetwork call here leaves CI green.
//
// A data source has no plan to converge, so the reason is composition: passing
// `tags = data.frostmoln_subnet.x.tags` into any network write would 400 on the
// reserved key. `tags` has to mean customer tags on every surface.
func TestSubnetDataSource_ReservedTagsNeverReachState(t *testing.T) {
	ctx := context.Background()
	var d subnetDataSource
	// The setter ends with resp.State.Set, which needs a real schema: a
	// zero-value ReadResponse panics on a nil schema rather than failing.
	var schemaResp datasource.SchemaResponse
	d.Schema(ctx, datasource.SchemaRequest{}, &schemaResp)
	resp := &datasource.ReadResponse{State: tfsdk.State{Schema: schemaResp.Schema}}
	var state subnetModel

	d.setSubnetState(ctx, &state, &apiSubnet{
		ID: "subnet-1",
		Tags: map[string]string{
			"env":            "prod",
			"frostmoln_type": "kubernetes-control-plane",
			"customer-id":    "cust-1",
			"frostmoln-type": "mine",
		},
	}, resp)

	got := map[string]string{}
	state.Tags.ElementsAs(ctx, &got, false)
	if _, ok := got["frostmoln_type"]; ok {
		t.Errorf("reserved key reached data source output: %v", got)
	}
	for _, k := range []string{"env", "customer-id", "frostmoln-type"} {
		if _, ok := got[k]; !ok {
			t.Errorf("customer tag %q was dropped: %v", k, got)
		}
	}
}
