package load_balancer

import (
	"context"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// TestFromAPIPreservesConfiguredFlavorWhenTheAPIOmitsIt guards against a
// spurious DESTROY of a load balancer that has not changed.
//
// flavor_id is Optional (not Computed) and forces replacement. If a response
// omits flavorId and the model nulls a value the configuration sets, the next
// plan compares null against the configured value and proposes to destroy and
// recreate -- a new VIP and downtime, caused by a missing response field.
//
// Contrast public_ip_id, which SHOULD null on absence: an address can be
// detached out-of-band, so reflecting that is real drift. A flavor is fixed at
// creation and immutable, so an empty flavorId is an API gap, never a change.
func TestFromAPIPreservesConfiguredFlavorWhenTheAPIOmitsIt(t *testing.T) {
	t.Run("configured flavor survives an omitted flavorId", func(t *testing.T) {
		m := &LoadBalancerModel{FlavorID: types.StringValue("lb-large")}
		m.fromAPI(context.Background(), &apiLoadBalancer{ID: "lb-1", Name: "web", Type: "l7"}, &diag.Diagnostics{})
		if m.FlavorID.IsNull() {
			t.Fatal("configured flavor was nulled; the next plan would propose a replacement")
		}
		if got := m.FlavorID.ValueString(); got != "lb-large" {
			t.Errorf("flavor = %q, want lb-large", got)
		}
	})

	t.Run("a returned flavor is adopted", func(t *testing.T) {
		m := &LoadBalancerModel{}
		m.fromAPI(context.Background(), &apiLoadBalancer{ID: "lb-1", Name: "web", Type: "l7", FlavorID: "lb-small"}, &diag.Diagnostics{})
		if got := m.FlavorID.ValueString(); got != "lb-small" {
			t.Errorf("flavor = %q, want lb-small", got)
		}
	})

	t.Run("an unset flavor stays null", func(t *testing.T) {
		m := &LoadBalancerModel{FlavorID: types.StringNull()}
		m.fromAPI(context.Background(), &apiLoadBalancer{ID: "lb-1", Name: "web", Type: "l4"}, &diag.Diagnostics{})
		if !m.FlavorID.IsNull() {
			t.Errorf("flavor = %q, want null", m.FlavorID.ValueString())
		}
	})

	// public_ip_id keeps the opposite behaviour on purpose.
	t.Run("public_ip_id still nulls on absence, deliberately", func(t *testing.T) {
		m := &LoadBalancerModel{PublicIPID: types.StringValue("pip-1")}
		m.fromAPI(context.Background(), &apiLoadBalancer{ID: "lb-1", Name: "web", Type: "l7"}, &diag.Diagnostics{})
		if !m.PublicIPID.IsNull() {
			t.Error("public_ip_id must reflect detachment; an address CAN go away out-of-band")
		}
	})
}
