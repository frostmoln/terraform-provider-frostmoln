package dns_zone

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
)

// The update-consistency helpers mirror the appgw_* crud_test.go shape: build
// the real schema, set models into plans/states, drive the real CRUD methods
// against an httptest server.

func schemaOf(t *testing.T) tfsdk.Plan {
	t.Helper()
	r := NewResource()
	var sr resource.SchemaResponse
	r.Schema(context.Background(), resource.SchemaRequest{}, &sr)
	if sr.Diagnostics.HasError() {
		t.Fatalf("schema: %v", sr.Diagnostics.Errors())
	}
	return tfsdk.Plan{Schema: sr.Schema}
}

func planOf(t *testing.T, m DNSZoneModel) tfsdk.Plan {
	t.Helper()
	p := schemaOf(t)
	if d := p.Set(context.Background(), &m); d.HasError() {
		t.Fatalf("plan: %v", d.Errors())
	}
	return p
}

func stateOf(t *testing.T, m DNSZoneModel) tfsdk.State {
	t.Helper()
	p := schemaOf(t)
	s := tfsdk.State{Schema: p.Schema}
	if d := s.Set(context.Background(), &m); d.HasError() {
		t.Fatalf("state: %v", d.Errors())
	}
	return s
}

func serve(t *testing.T, h http.HandlerFunc) *client.Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	c := client.NewClient(srv.URL, "test-key", client.WithHTTPClient(srv.Client())) // pragma: allowlist secret
	c.SetTenantIDForTest("t-1")
	return c
}

// priorZoneModel is a zone in state as a create/read left it: derived metadata
// known, status active — the values the plan pins on every update.
func priorZoneModel() DNSZoneModel {
	pinnedNS, _ := types.ListValueFrom(context.Background(), types.StringType, []string{
		"ns1.set-a.dns.frostmoln.cloud.", "ns2.set-a.dns.frostmoln.cloud.",
	})
	return DNSZoneModel{
		ID:          types.StringValue("zone-1"),
		Name:        types.StringValue("example.com."),
		Email:       types.StringValue("admin@example.com"),
		Type:        types.StringValue("primary"),
		Status:      types.StringValue("active"),
		Serial:      types.Int64Value(2026062701),
		TTL:         types.Int64Value(3600),
		RecordCount: types.Int64Value(3),
		NameServers: pinnedNS,
		Tags:        types.MapNull(types.StringType),
		TagsAll:     types.MapNull(types.StringType),
		CreatedAt:   types.StringValue("2026-06-27T00:00:00Z"),
	}
}

// runUpdate drives the real Update against a PUT response the test stages.
func runUpdate(t *testing.T, putResp map[string]any) DNSZoneModel {
	t.Helper()
	c := serve(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPut && r.URL.Path == "/v1/tenants/t-1/dns/zones/zone-1":
			_ = json.NewEncoder(w).Encode(putResp)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-1/dns/zones/zone-1":
			_ = json.NewEncoder(w).Encode(putResp) // currentTags re-reads the zone
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	})
	er := &dnsZoneResource{client: c}

	plan := priorZoneModel()
	plan.TTL = types.Int64Value(600) // the change under apply

	updResp := resource.UpdateResponse{State: stateOf(t, priorZoneModel())}
	er.Update(context.Background(), resource.UpdateRequest{Plan: planOf(t, plan), State: stateOf(t, priorZoneModel())}, &updResp)
	if updResp.Diagnostics.HasError() {
		t.Fatalf("update: %v", updResp.Diagnostics.Errors())
	}

	var out DNSZoneModel
	if d := updResp.State.Get(context.Background(), &out); d.HasError() {
		t.Fatalf("state get: %v", d.Errors())
	}
	return out
}

// assertPinnedComputedFields: the four server-owned volatile fields must carry
// the plan-pinned values, whatever the PUT response answered.
func assertPinnedComputedFields(t *testing.T, out DNSZoneModel) {
	t.Helper()
	prior := priorZoneModel()
	if !out.RecordCount.Equal(prior.RecordCount) {
		t.Errorf("record_count = %v, want the pinned %v", out.RecordCount, prior.RecordCount)
	}
	if !out.NameServers.Equal(prior.NameServers) {
		t.Errorf("name_servers = %v, want the pinned delegation set %v", out.NameServers, prior.NameServers)
	}
	if !out.Status.Equal(prior.Status) {
		t.Errorf("status = %v, want the pinned %v", out.Status, prior.Status)
	}
	if !out.Serial.Equal(prior.Serial) {
		t.Errorf("serial = %v, want the pinned %v", out.Serial, prior.Serial)
	}
}

// TestUpdateCarriesPlanPinnedComputedFields pins the B1 contract. The network
// service answers the PUT with a re-fetched zone: its derived metadata is
// best-effort and its status/serial are snapshots of a zone the platform moves
// independently. Update must not write any of that over the plan-pinned
// values — doing so made core reject every apply with "inconsistent result
// after apply" after the zone had already changed server-side.
func TestUpdateCarriesPlanPinnedComputedFields(t *testing.T) {
	t.Run("blank update response is not asserted", func(t *testing.T) {
		out := runUpdate(t, map[string]any{
			"id": "zone-1", "name": "example.com.", "email": "ops@example.com",
			"type": "primary", "status": "active", "serial": 2026062701,
			"ttl": 600, "recordCount": 0,
			"createdAt": "2026-06-27T00:00:00Z", "updatedAt": "2026-09-16T00:00:00Z",
		})
		if out.TTL.ValueInt64() != 600 || out.Email.ValueString() != "ops@example.com" {
			t.Errorf("update did not apply: ttl=%v email=%v", out.TTL, out.Email)
		}
		// recordCount 0 / omitted nameServers is the degraded-backend answer;
		// asserting it here is exactly the B1 state clobber.
		assertPinnedComputedFields(t, out)
	})

	t.Run("honest but different update response is still pinned", func(t *testing.T) {
		// The bare NS and credited record changes arrive the same instant the
		// apply runs: the PUT carries truthful values the plan never saw —
		// five records now, a concurrent record write landed, the zone is mid
		// propagation (PENDING), the SOA serial moved. The pin wins and the
		// next refresh (GET, authoritative) reconciles; asserting this
		// response would reject the apply wholesale.
		out := runUpdate(t, map[string]any{
			"id": "zone-1", "name": "example.com.", "email": "ops@example.com",
			"type": "primary", "status": "pending", "serial": 2026062801,
			"ttl": 600, "recordCount": 5,
			"nameServers": []string{"ns1.set-b.dns.frostmoln.cloud.", "ns2.set-b.dns.frostmoln.cloud."},
			"createdAt":   "2026-06-27T00:00:00Z", "updatedAt": "2026-09-16T00:00:00Z",
		})
		if out.TTL.ValueInt64() != 600 {
			t.Errorf("update did not apply: ttl=%v", out.TTL)
		}
		assertPinnedComputedFields(t, out)
	})
}
