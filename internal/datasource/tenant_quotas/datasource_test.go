package tenantquotas

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
)

const (
	report = `{"report":{"scope":"tenant","quotas":[
		{"resourceType":"vcpu","used":4,"limit":20,"unit":"count"},
		{"resourceType":"ram","used":8192,"limit":65536,"unit":"mb"},
		{"resourceType":"registry_storage","used":null,"limit":50,"unit":"gb","requestable":100}]}}`
	limits = `{"instances":10,"images":5,"imageStorageGb":0}`
	usage  = `{"instances":2,"images":3,"imageStorageGb":7}`
)

func read(t *testing.T, bodies map[string]string) *datasource.ReadResponse {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, ok := bodies[r.URL.Path]
		if !ok {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"code":"INTERNAL","message":"boom"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	c := client.NewClient(srv.URL, "test-key", client.WithHTTPClient(srv.Client())) // pragma: allowlist secret
	c.SetTenantIDForTest("t1")

	var sr datasource.SchemaResponse
	NewDataSource().Schema(context.Background(), datasource.SchemaRequest{}, &sr)
	typ := sr.Schema.Type().TerraformType(context.Background())
	resp := &datasource.ReadResponse{State: tfsdk.State{Schema: sr.Schema, Raw: tftypes.NewValue(typ, nil)}}
	(&tenantQuotasDataSource{client: c}).Read(context.Background(),
		datasource.ReadRequest{Config: tfsdk.Config{Schema: sr.Schema, Raw: tftypes.NewValue(typ, nil)}}, resp)
	return resp
}

func all() map[string]string {
	return map[string]string{
		"/v1/tenants/t1/quotas-report": report,
		"/v1/tenants/t1/quotas":        limits,
		"/v1/tenants/t1/quotas/usage":  usage,
	}
}

func quotas(t *testing.T, resp *datasource.ReadResponse) map[string]quotaModel {
	t.Helper()
	if resp.Diagnostics.HasError() {
		t.Fatalf("read: %v", resp.Diagnostics.Errors())
	}
	var m tenantQuotasModel
	if d := resp.State.Get(context.Background(), &m); d.HasError() {
		t.Fatal(d.Errors())
	}
	out := map[string]quotaModel{}
	if d := m.Quotas.ElementsAs(context.Background(), &out, false); d.HasError() {
		t.Fatal(d.Errors())
	}
	return out
}

func TestRead_NullUsedStaysNull(t *testing.T) {
	q := quotas(t, read(t, all()))
	if rs := q["registry_storage"]; !rs.Used.IsNull() || rs.Limit.ValueInt64() != 50 || rs.Unit.ValueString() != "gb" {
		t.Errorf("registry_storage = %+v, want used null, limit 50, unit gb", rs)
	}
	if v := q["vcpu"]; v.Used.ValueInt64() != 4 || v.Limit.ValueInt64() != 20 || v.Unit.ValueString() != "count" {
		t.Errorf("vcpu = %+v", v)
	}
	if r := q["ram"]; r.Unit.ValueString() != "mb" || r.Used.ValueInt64() != 8192 {
		t.Errorf("ram = %+v", r)
	}
}

func TestRead_ImageKeysAlwaysPresent(t *testing.T) {
	q := quotas(t, read(t, all()))
	img, ok := q["image"]
	if !ok || img.Limit.ValueInt64() != 5 || img.Used.ValueInt64() != 3 || img.Unit.ValueString() != "count" {
		t.Errorf("image = %+v (present %v), want limit 5, used 3, count", img, ok)
	}
	// Limit 0 still yields the key: a missing one would fail quotas["image_storage"] at plan time.
	st, ok := q["image_storage"]
	if !ok || st.Limit.ValueInt64() != 0 || st.Used.ValueInt64() != 7 || st.Unit.ValueString() != "gb" {
		t.Errorf("image_storage = %+v (present %v), want limit 0, used 7, gb", st, ok)
	}
	if len(q) != 5 {
		t.Errorf("got %d keys, want 5: %v", len(q), q)
	}
}

func TestRead_ComputeErrorFailsRead(t *testing.T) {
	for _, missing := range []string{"/v1/tenants/t1/quotas", "/v1/tenants/t1/quotas/usage", "/v1/tenants/t1/quotas-report"} {
		b := all()
		delete(b, missing)
		if resp := read(t, b); !resp.Diagnostics.HasError() {
			t.Errorf("%s failing: want an error diagnostic", missing)
		}
	}
}
