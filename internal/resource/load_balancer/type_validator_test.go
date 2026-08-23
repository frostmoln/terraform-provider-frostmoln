package load_balancer

import (
	"context"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	fwvalidator "github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types/basetypes"
)

// TestTypeValidatorRejectsLegacyValues is a guard against re-introducing a
// leniency that DESTROYS load balancers.
//
// Accepting "amphora"/"ovn" as config values reads as a kindness to
// practitioners mid-migration. It is the opposite. A config value is never
// replaced by the schema default, and nothing canonicalises the PLAN value, so
// `type = "amphora"` stays "amphora" in the plan while the state upgrader wrote
// "l7". On an attribute that forces replacement, unequal means the plan
// proposes a destroy and recreate -- a new VIP and an outage -- for every load
// balancer the customer has.
//
// It is also the single most likely edit: removing `provider_type` forces a
// config change, and the obvious mechanical fix is to rename the attribute and
// keep the value. So the validator has to be the thing that stops it.
func TestTypeValidatorRejectsLegacyValues(t *testing.T) {
	ctx := context.Background()
	r := &loadBalancerResource{}
	var resp resource.SchemaResponse
	r.Schema(ctx, resource.SchemaRequest{}, &resp)

	attr, ok := resp.Schema.Attributes["type"].(schema.StringAttribute)
	if !ok {
		t.Fatal(`the "type" attribute is missing or is not a StringAttribute`)
	}

	check := func(value string) bool {
		for _, v := range attr.Validators {
			req := fwvalidator.StringRequest{
				Path:        path.Root("type"),
				ConfigValue: basetypes.NewStringValue(value),
			}
			var vr fwvalidator.StringResponse
			v.ValidateString(ctx, req, &vr)
			if vr.Diagnostics.HasError() {
				return false
			}
		}
		return true
	}

	for _, legacy := range []string{"amphora", "ovn"} {
		if check(legacy) {
			t.Errorf("config value %q is ACCEPTED; it must be rejected, or the next plan "+
				"destroys and recreates every existing load balancer", legacy)
		}
	}
	for _, ok := range []string{"l7", "l4"} {
		if !check(ok) {
			t.Errorf("config value %q must be accepted", ok)
		}
	}

	// The default must exist and be canonical too: a non-canonical default
	// would land in the plan for every config that omits the attribute, which
	// is the same replacement hazard by another route.
	if attr.Default == nil {
		t.Fatal("type has no default; a config omitting it would plan a null against state")
	}
}
