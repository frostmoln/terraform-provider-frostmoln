package appgw_flavors

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
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

func body() string {
	return `{"flavors":[
	  {"id":"agw.gp1.small","name":"Small","vcpus":2,"ramMb":4096,"diskGb":40,
	   "maxListeners":5,"maxRoutes":50,"maxBackends":50,"maxWafRules":100,
	   "maxRequestsPerSecond":5000,"pricingTier":"app_gateway_gp1","active":true},
	  {"id":"agw.gp1.legacy","name":"Legacy","vcpus":1,"active":false}
	],"totalCount":2}`
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
