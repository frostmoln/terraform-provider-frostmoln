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
	}
	req := model.toCreateRequest()

	// The backend defaults the type, so the request struct has no type field.
	body, _ := json.Marshal(req)
	var m map[string]any
	_ = json.Unmarshal(body, &m)
	if _, ok := m["type"]; ok {
		t.Errorf("create request must not carry a type, got %v", m["type"])
	}
	if req.Name != "example.com." {
		t.Errorf("expected name example.com., got %s", req.Name)
	}
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
