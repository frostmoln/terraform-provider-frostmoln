package appgw_route

import (
	"context"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

func TestRouteModelFromAPI(t *testing.T) {
	rt := &apiRoute{
		ID: "rt-1", ListenerID: "lsn-1", Name: "api", Priority: 100,
		Host: "api.example.com", PathMatchType: "prefix", Path: "/v1",
		Action: "forward", BackendPoolID: "pool-1",
		RequestHeadersSet: map[string]string{"X-A": "b"},
		Enabled:           true, CreatedAt: "2026-08-01T00:00:00Z",
	}
	var m RouteModel
	var diags diag.Diagnostics
	m.fromAPI(context.Background(), rt, &diags)
	if diags.HasError() {
		t.Fatalf("diagnostics: %v", diags)
	}
	if m.Priority.ValueInt64() != 100 || m.Path.ValueString() != "/v1" {
		t.Fatalf("basic fields wrong: %+v", m)
	}
}

// TestOmittedPriorityIsNotSentAsZero.
//
// 🔴 A SENT 0 PUTS A NEW ROUTE AHEAD OF EVERYTHING. Priority is explicit and
// lower wins, so omitting it must mean "server assigns last", never "priority
// 0" — otherwise adding a route silently takes precedence over a live
// configuration's most specific rules.
func TestOmittedPriorityIsNotSentAsZero(t *testing.T) {
	var diags diag.Diagnostics
	m := &RouteModel{
		Name:          types.StringValue("api"),
		BackendPoolID: types.StringValue("pool-1"),
		Priority:      types.Int64Null(),
	}
	req := m.toCreateRequest(context.Background(), &diags)
	if req.Priority != nil {
		t.Fatalf("priority was sent as %d though the configuration omitted it", *req.Priority)
	}

	m.Priority = types.Int64Value(0)
	req = m.toCreateRequest(context.Background(), &diags)
	if req.Priority == nil || *req.Priority != 0 {
		t.Fatalf("an explicit priority of 0 must still be sent, got %v", req.Priority)
	}
}

// TestAbsentHeaderCollectionsBecomeNull. Same trap as the listener's lists: the
// server omits them when empty.
func TestAbsentHeaderCollectionsBecomeNull(t *testing.T) {
	var m RouteModel
	var diags diag.Diagnostics
	m.fromAPI(context.Background(), &apiRoute{ID: "rt-1", Name: "api"}, &diags)
	if !m.RequestHeadersSet.IsNull() || !m.ResponseHeadersSet.IsNull() ||
		!m.RequestHeadersRemove.IsNull() {
		t.Fatalf("absent header collections must be null, got %+v / %+v / %+v",
			m.RequestHeadersSet, m.ResponseHeadersSet, m.RequestHeadersRemove)
	}
	// An absent host must be null too, so `host` round-trips an unset value.
	if !m.Host.IsNull() {
		t.Errorf("host = %v, want null", m.Host)
	}
}
