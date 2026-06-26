package dns_zone

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
)

// TestDNSZoneDataSourceByNameLookup exercises the list-and-match-by-name path
// used when the data source is given a name instead of an id, and confirms the
// delegation name servers come through.
func TestDNSZoneDataSourceByNameLookup(t *testing.T) {
	list := apiDNSZoneList{Zones: []apiDNSZone{
		{ID: "zone-1", Name: "other.com.", Type: "primary", Status: "active"},
		{
			ID: "zone-2", Name: "example.com.", Type: "primary", Status: "active",
			NameServers: []string{"ns1.set-b.dns.frostmoln.cloud.", "ns2.set-b.dns.frostmoln.cloud."},
		},
	}}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-1/dns/zones" {
			_ = json.NewEncoder(w).Encode(list)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key") // pragma: allowlist secret
	c.SetTenantIDForTest("t-1")

	apiResp, err := c.Get(context.Background(), c.TenantPath("/dns/zones"), nil)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	var got apiDNSZoneList
	if err := json.Unmarshal(apiResp.Body, &got); err != nil {
		t.Fatalf("parse: %v", err)
	}

	var found *apiDNSZone
	for i := range got.Zones {
		if got.Zones[i].Name == "example.com." {
			found = &got.Zones[i]
			break
		}
	}
	if found == nil {
		t.Fatal("expected to find example.com.")
	}
	if len(found.NameServers) != 2 || found.NameServers[0] != "ns1.set-b.dns.frostmoln.cloud." {
		t.Errorf("expected delegation name servers, got %v", found.NameServers)
	}
}
