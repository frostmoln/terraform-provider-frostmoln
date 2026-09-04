package appgw_flavors

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"

	"github.com/hashicorp/terraform-plugin-framework/types"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
)

// configOf builds a Config from a model. tfsdk.Config has no Set, so the raw
// value is produced through a State (which does) and carried across with the
// same schema.
func configOf(t *testing.T, base tfsdk.Config, m FlavorsModel) tfsdk.Config {
	t.Helper()
	st := tfsdk.State{Schema: base.Schema}
	if d := st.Set(context.Background(), &m); d.HasError() {
		t.Fatalf("config: %v", d.Errors())
	}
	return tfsdk.Config{Schema: base.Schema, Raw: st.Raw}
}

func harness(t *testing.T, h http.HandlerFunc) (*flavorsDataSource, tfsdk.Config, tfsdk.State) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	c := client.NewClient(srv.URL, "k", client.WithHTTPClient(srv.Client())) // pragma: allowlist secret
	c.SetTenantIDForTest("t-1")

	d := NewDataSource().(*flavorsDataSource)
	d.client = c
	var sr datasource.SchemaResponse
	d.Schema(context.Background(), datasource.SchemaRequest{}, &sr)
	return d, tfsdk.Config{Schema: sr.Schema}, tfsdk.State{Schema: sr.Schema}
}

// The fixture still carries vcpus/ramMb/diskGb because the SERVER still sends
// them: the substrate fields are being withdrawn from the catalog in a later
// wave, and this data source must ignore them in the meantime rather than fail
// on them. It is also the shape a practitioner on an older provider sees.
func body() string {
	return `{"flavors":[
	  {"id":"agw.gp1.small","name":"Small","vcpus":2,"ramMb":4096,"diskGb":40,
	   "maxListeners":5,"maxRoutes":50,"maxBackends":50,"maxWafRules":100,
	   "maxWafExclusions":200,
	   "maxRequestsPerSecond":5000,"maxConcurrentConnections":20000,
	   "pricingTier":"app_gateway_gp1","active":true},
	  {"id":"agw.gp1.legacy","name":"Legacy","vcpus":1,"active":false}
	],"totalCount":2}`
}

// An Application Gateway is a managed appliance, so its catalog describes what
// the appliance carries — throughput, connections, and the object and rule caps
// — not the virtual machine underneath it. Operator decision, 2026-08-29:
// "Of course behind the scenes it is a VM, so the VM needs vCPU, RAM and disk.
// But that is not what we sell to the customer."
//
// This asserts the schema itself, because that is the practitioner-visible
// contract: an attribute here is one a customer can interpolate and will then
// depend on.
func TestTheSchemaSellsCapacityNotSubstrate(t *testing.T) {
	d, _, _ := harness(t, func(http.ResponseWriter, *http.Request) {})
	var sr datasource.SchemaResponse
	d.Schema(context.Background(), datasource.SchemaRequest{}, &sr)

	nested, ok := sr.Schema.Attributes["flavors"].(schema.ListNestedAttribute)
	if !ok {
		t.Fatalf("flavors is not a ListNestedAttribute")
	}
	attrs := nested.NestedObject.Attributes

	for _, substrate := range []string{"vcpus", "ram_mb", "disk_gb"} {
		if _, present := attrs[substrate]; present {
			t.Errorf("the catalog exposes %q — that describes the VM, not the service", substrate)
		}
	}
	for _, capacity := range []string{
		"max_requests_per_second", "max_concurrent_connections",
		"max_listeners", "max_routes", "max_backends", "max_waf_rules",
		"max_waf_exclusions",
	} {
		if _, present := attrs[capacity]; !present {
			t.Errorf("the catalog is missing %q, which is what a size is chosen on", capacity)
		}
	}
}

// TestTheCatalogIsReturnedAsTheServerSentIt.
//
// 🔴 NO CLIENT-SIDE FILTER, deliberately. The customer catalog handler
// hard-codes empty list options and the repository appends `WHERE active`, so a
// withdrawn size never crosses the wire. A filter here would read as a
// capability the platform does not have — and `include_inactive` promised
// exactly that until it was deprecated.
//
// The fixture deliberately includes an inactive row the real endpoint would
// never send, to prove the data source reports what it is given rather than
// re-deciding.
func TestTheCatalogIsReturnedAsTheServerSentIt(t *testing.T) {
	d, cfg, st := harness(t, func(w http.ResponseWriter, r *http.Request) {
		// TENANT-SCOPED. The sizes are identical for every tenant, but the
		// entitlement that decides whether you may SEE them is per tenant, and
		// only a path naming one lets the gateway resolve it (ADR-0052).
		if r.URL.Path != "/v1/tenants/t-1/application-gateways/catalog/flavors" {
			t.Errorf("unexpected path %s -- the catalog must be read per tenant", r.URL.Path)
		}
		_, _ = w.Write([]byte(body()))
	})
	cfg = configOf(t, cfg, FlavorsModel{})
	resp := datasource.ReadResponse{State: st}
	d.Read(context.Background(), datasource.ReadRequest{Config: cfg}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("read: %v", resp.Diagnostics.Errors())
	}
	var out FlavorsModel
	resp.State.Get(context.Background(), &out)
	if len(out.Flavors) != 2 {
		t.Fatalf("got %d sizes, want both rows the server sent: %+v", len(out.Flavors), out.Flavors)
	}
	// The caps are the point of this data source: they are enforced server-side,
	// so they must actually arrive.
	if out.Flavors[0].MaxWafRules.ValueInt64() != 100 {
		t.Errorf("max_waf_rules = %v, want 100", out.Flavors[0].MaxWafRules)
	}
	if out.Flavors[0].MaxListeners.ValueInt64() != 5 || out.Flavors[0].MaxRoutes.ValueInt64() != 50 {
		t.Errorf("the structural caps did not survive: %+v", out.Flavors[0])
	}
}

// TestIncludeInactiveIsInert. It is retained and deprecated so a configuration
// that sets it is told so, rather than having the argument silently vanish.
func TestIncludeInactiveIsInert(t *testing.T) {
	d, cfg, st := harness(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(body()))
	})
	m := FlavorsModel{}
	m.IncludeInactive = boolTrue()
	cfg = configOf(t, cfg, m)
	resp := datasource.ReadResponse{State: st}
	d.Read(context.Background(), datasource.ReadRequest{Config: cfg}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("read: %v", resp.Diagnostics.Errors())
	}
	var out FlavorsModel
	resp.State.Get(context.Background(), &out)
	if len(out.Flavors) != 2 {
		t.Fatalf("setting include_inactive changed the result; it must be inert: %+v", out.Flavors)
	}
}

func TestReadSurfacesAnAPIError(t *testing.T) {
	d, cfg, st := harness(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]string{"code": "BOOM", "message": "no"})
	})
	cfg = configOf(t, cfg, FlavorsModel{})
	resp := datasource.ReadResponse{State: st}
	d.Read(context.Background(), datasource.ReadRequest{Config: cfg}, &resp)
	if !resp.Diagnostics.HasError() {
		t.Fatal("a 500 must surface as an error, not an empty catalog")
	}
}

func boolTrue() types.Bool { return types.BoolValue(true) }

// TestTheExclusionBudgetIsInTheCatalog.
//
// 🔴 TWO WAF BUDGETS, NOT ONE. max_waf_rules and max_waf_exclusions are counted
// separately across ALL of a gateway's policies, and exceeding either is a
// refusal (409 FLAVOR_LIMIT_EXCEEDED), not a throttle. A catalog that publishes
// only the rule cap makes the exclusion cap invisible until an apply fails on
// it -- and tuning away a false positive is exactly the routine act that hits
// it, on a gateway whose rule count looks fine.
//
// Asserted through the SCHEMA and through a READ: the schema is what a
// practitioner can interpolate, and the read is what proves the value actually
// arrives rather than sitting in the schema unwired.
func TestTheExclusionBudgetIsInTheCatalog(t *testing.T) {
	d, cfg, st := harness(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(body()))
	})

	var sr datasource.SchemaResponse
	d.Schema(context.Background(), datasource.SchemaRequest{}, &sr)
	nested, ok := sr.Schema.Attributes["flavors"].(schema.ListNestedAttribute)
	if !ok {
		t.Fatalf("flavors is not a ListNestedAttribute")
	}
	if _, present := nested.NestedObject.Attributes["max_waf_exclusions"]; !present {
		t.Fatal("the catalog has no max_waf_exclusions, so the exclusion budget is invisible " +
			"until an apply is refused by it")
	}

	cfg = configOf(t, cfg, FlavorsModel{})
	resp := datasource.ReadResponse{State: st}
	d.Read(context.Background(), datasource.ReadRequest{Config: cfg}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("read: %v", resp.Diagnostics.Errors())
	}
	var out FlavorsModel
	resp.State.Get(context.Background(), &out)
	if len(out.Flavors) == 0 {
		t.Fatal("no sizes were read")
	}
	if got := out.Flavors[0].MaxWafExclusions.ValueInt64(); got != 200 {
		t.Errorf("max_waf_exclusions = %d, want 200 -- the server sent it and it must survive "+
			"the read", got)
	}
	// A SEPARATE budget: reading the rule cap into it (or the reverse) would be
	// the plausible wiring mistake, and it would tell a customer their exclusion
	// room is the rule room.
	if out.Flavors[0].MaxWafExclusions.Equal(out.Flavors[0].MaxWafRules) {
		t.Errorf("max_waf_exclusions and max_waf_rules read the same value (%v); they are "+
			"separate budgets and the fixture gives them different numbers",
			out.Flavors[0].MaxWafRules)
	}
}
