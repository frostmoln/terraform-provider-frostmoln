package dns_zone

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
)

func TestDNSZoneModelFromAPI(t *testing.T) {
	zone := &apiDNSZone{
		ID:          "zone-1",
		Name:        "example.com.",
		Email:       "admin@example.com",
		Type:        "primary",
		Status:      "active",
		Serial:      2026062701,
		TTL:         3600,
		RecordCount: 2,
		NameServers: []string{"ns1.set-a.dns.frostmoln.cloud.", "ns2.set-a.dns.frostmoln.cloud."},
		CreatedAt:   "2026-06-27T00:00:00Z",
	}

	var model DNSZoneModel
	var diags diag.Diagnostics
	model.fromAPI(context.Background(), zone, &diags)
	if diags.HasError() {
		t.Fatalf("unexpected diags: %v", diags)
	}

	if model.Name.ValueString() != "example.com." {
		t.Errorf("expected name example.com., got %s", model.Name.ValueString())
	}
	if model.TTL.ValueInt64() != 3600 {
		t.Errorf("expected ttl 3600, got %d", model.TTL.ValueInt64())
	}
	var ns []string
	model.NameServers.ElementsAs(context.Background(), &ns, false)
	if len(ns) != 2 || ns[0] != "ns1.set-a.dns.frostmoln.cloud." {
		t.Errorf("expected delegation name servers, got %v", ns)
	}
	// description was empty -> must be null, not "".
	if !model.Description.IsNull() {
		t.Errorf("expected null description, got %q", model.Description.ValueString())
	}
}

func TestDNSZoneToCreateRequestOmitsType(t *testing.T) {
	model := DNSZoneModel{
		Name:        types.StringValue("example.com."),
		Email:       types.StringValue("admin@example.com"),
		Description: types.StringNull(),
		TTL:         types.Int64Null(),
		Tags:        types.MapNull(types.StringType),
	}
	var diags diag.Diagnostics
	req := model.toCreateRequest(context.Background(), &diags)
	if diags.HasError() {
		t.Fatalf("unexpected diags: %v", diags)
	}

	// The backend defaults the type, so the request struct has no type field.
	// An unset tags attribute must also be omitted (never sent as {} on create).
	body, _ := json.Marshal(req)
	var m map[string]any
	_ = json.Unmarshal(body, &m)
	if _, ok := m["type"]; ok {
		t.Errorf("create request must not carry a type, got %v", m["type"])
	}
	if _, ok := m["tags"]; ok {
		t.Errorf("create request must omit tags when unset, got %v", m["tags"])
	}
	if req.Name != "example.com." {
		t.Errorf("expected name example.com., got %s", req.Name)
	}
}

// TestDNSZoneTagsRoundTrip exercises the null-vs-empty-map drift edges: an unset
// tags attribute must round-trip to null and a configured set must round-trip
// verbatim, since the backend omits tags entirely when a zone has none.
func TestDNSZoneTagsRoundTrip(t *testing.T) {
	ctx := context.Background()

	t.Run("configured tags round-trip verbatim", func(t *testing.T) {
		tags, _ := types.MapValueFrom(ctx, types.StringType, map[string]string{"env": "prod", "team": "platform"})
		model := DNSZoneModel{Tags: tags}

		var diags diag.Diagnostics
		req := model.toCreateRequest(ctx, &diags)
		if diags.HasError() {
			t.Fatalf("unexpected diags: %v", diags)
		}
		if req.Tags["env"] != "prod" || req.Tags["team"] != "platform" {
			t.Errorf("expected create tags env=prod,team=platform, got %v", req.Tags)
		}

		// Backend echoes the persisted set back.
		var out DNSZoneModel
		out.Tags = tags
		out.fromAPI(ctx, &apiDNSZone{ID: "z1", Tags: map[string]string{"env": "prod", "team": "platform"}}, &diags)
		if diags.HasError() {
			t.Fatalf("unexpected diags: %v", diags)
		}
		var got map[string]string
		out.Tags.ElementsAs(ctx, &got, false)
		if got["env"] != "prod" || got["team"] != "platform" || len(got) != 2 {
			t.Errorf("expected round-tripped tags, got %v", got)
		}
	})

	t.Run("unset tags stay null (backend omits empty)", func(t *testing.T) {
		model := DNSZoneModel{Tags: types.MapNull(types.StringType)}

		var diags diag.Diagnostics
		// Backend returns a zone with no tags field at all.
		model.fromAPI(ctx, &apiDNSZone{ID: "z1"}, &diags)
		if diags.HasError() {
			t.Fatalf("unexpected diags: %v", diags)
		}
		if !model.Tags.IsNull() {
			t.Errorf("expected tags to stay null, got %v", model.Tags)
		}
	})

	t.Run("explicit empty map stays empty (not null)", func(t *testing.T) {
		empty, _ := types.MapValueFrom(ctx, types.StringType, map[string]string{})
		model := DNSZoneModel{Tags: empty}

		var diags diag.Diagnostics
		model.fromAPI(ctx, &apiDNSZone{ID: "z1"}, &diags)
		if diags.HasError() {
			t.Fatalf("unexpected diags: %v", diags)
		}
		if model.Tags.IsNull() {
			t.Errorf("explicit empty map must not flip to null (spurious diff)")
		}
		if len(model.Tags.Elements()) != 0 {
			t.Errorf("expected empty tag map, got %v", model.Tags)
		}
	})
}

// TestDNSZoneToUpdateRequestTags confirms update always sends the full desired
// set: a configured map replaces, and null/empty serialize to `"tags":{}` to
// clear (never omitted, which the backend would read as "unchanged").
func TestDNSZoneToUpdateRequestTags(t *testing.T) {
	ctx := context.Background()

	assertTagsWireIsEmptyObject := func(t *testing.T, req apiUpdateDNSZoneRequest) {
		t.Helper()
		body, _ := json.Marshal(req)
		var m map[string]any
		_ = json.Unmarshal(body, &m)
		raw, ok := m["tags"]
		if !ok {
			t.Fatalf("update must always carry a tags field (to clear), got %s", body)
		}
		obj, ok := raw.(map[string]any)
		if !ok || len(obj) != 0 {
			t.Errorf("expected tags to be an empty object {} to clear, got %v", raw)
		}
	}

	t.Run("configured tags replace", func(t *testing.T) {
		tags, _ := types.MapValueFrom(ctx, types.StringType, map[string]string{"env": "staging"})
		model := DNSZoneModel{Email: types.StringValue("a@a.com"), Tags: tags}

		var diags diag.Diagnostics
		req := model.toUpdateRequest(ctx, &diags)
		if diags.HasError() {
			t.Fatalf("unexpected diags: %v", diags)
		}
		if req.Tags["env"] != "staging" || len(req.Tags) != 1 {
			t.Errorf("expected replace tags env=staging, got %v", req.Tags)
		}
	})

	t.Run("null tags clear via explicit empty object", func(t *testing.T) {
		model := DNSZoneModel{Email: types.StringValue("a@a.com"), Tags: types.MapNull(types.StringType)}

		var diags diag.Diagnostics
		req := model.toUpdateRequest(ctx, &diags)
		if diags.HasError() {
			t.Fatalf("unexpected diags: %v", diags)
		}
		if req.Tags == nil {
			t.Errorf("expected non-nil empty tag map to clear, got nil")
		}
		assertTagsWireIsEmptyObject(t, req)
	})

	t.Run("empty tags clear via explicit empty object", func(t *testing.T) {
		empty, _ := types.MapValueFrom(ctx, types.StringType, map[string]string{})
		model := DNSZoneModel{Email: types.StringValue("a@a.com"), Tags: empty}

		var diags diag.Diagnostics
		req := model.toUpdateRequest(ctx, &diags)
		if diags.HasError() {
			t.Fatalf("unexpected diags: %v", diags)
		}
		assertTagsWireIsEmptyObject(t, req)
	})
}

func TestDNSZoneResourceCRUD(t *testing.T) {
	zone := apiDNSZone{
		ID: "zone-1", Name: "example.com.", Email: "admin@example.com",
		Type: "primary", Status: "active", TTL: 3600,
		NameServers: []string{"ns1.set-a.dns.frostmoln.cloud.", "ns2.set-a.dns.frostmoln.cloud."},
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/tenants/t-1/dns/zones":
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(zone)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-1/dns/zones/zone-1":
			_ = json.NewEncoder(w).Encode(zone)
		case r.Method == http.MethodDelete && r.URL.Path == "/v1/tenants/t-1/dns/zones/zone-1":
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"code": "NOT_FOUND", "message": "not found"}})
		}
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key") // pragma: allowlist secret
	c.SetTenantIDForTest("t-1")
	ctx := context.Background()

	apiResp, err := c.Post(ctx, c.TenantPath("/dns/zones"), apiCreateDNSZoneRequest{Name: "example.com.", Email: "admin@example.com"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if apiResp.StatusCode != http.StatusCreated {
		t.Errorf("expected 201, got %d", apiResp.StatusCode)
	}
	var created apiDNSZone
	_ = json.Unmarshal(apiResp.Body, &created)
	if len(created.NameServers) != 2 {
		t.Errorf("expected name servers on create, got %v", created.NameServers)
	}

	if _, err := c.Get(ctx, c.TenantPath("/dns/zones/zone-1"), nil); err != nil {
		t.Fatalf("read: %v", err)
	}
	if _, err := c.Delete(ctx, c.TenantPath("/dns/zones/zone-1")); err != nil {
		t.Fatalf("delete: %v", err)
	}
}
