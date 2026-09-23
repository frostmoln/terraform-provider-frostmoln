package appgw_backend_authorization

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// 🔴 `adopted` is Computed with no Default, so the framework plans it UNKNOWN
// on every create. authzModel leaves it zero, which becomes NULL in the plan —
// a value the framework never produces — and that is why no test saw v0.73.3
// write the planned unknown straight into state on every create that did NOT
// adopt. Core refused that ("invalid result object after apply") and tainted
// the resource: every ordinary create of this resource failed (Ambix
// 01a0cd73-3de9, reproduced with a real `terraform apply`).
func TestNonAdoptingCreateRecordsAdoptedFalse(t *testing.T) {
	c, _ := serve(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(authzFixture(false))
	})
	ar := &authorizationResource{client: c}
	m := authzModel()
	// Bypasses authzModel's zero value on purpose: this is what a real plan holds.
	m.ID = types.StringUnknown()
	m.Protocol = types.StringUnknown()
	m.PortMin = types.Int64Unknown()
	m.PortMax = types.Int64Unknown()
	m.Adopted = types.BoolUnknown()
	m.AuthorizedBy = types.StringUnknown()
	m.CreatedAt = types.StringUnknown()

	resp := resource.CreateResponse{State: emptyState(t)}
	ar.Create(context.Background(), resource.CreateRequest{Plan: planOf(t, m)}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("create: %v", resp.Diagnostics.Errors())
	}
	if !resp.State.Raw.IsFullyKnown() {
		t.Fatalf("state carries an UNKNOWN after apply — Terraform refuses it and taints the resource: %s",
			resp.State.Raw.String())
	}
	var out AuthorizationModel
	if d := resp.State.Get(context.Background(), &out); d.HasError() {
		t.Fatalf("state.Get: %v", d.Errors())
	}
	if out.Adopted.IsNull() || out.Adopted.ValueBool() {
		t.Errorf("a create the platform did not answer as adopted must record adopted = false, got %v", out.Adopted)
	}
}
