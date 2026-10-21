package mysql_instance

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/tlsattr/tlsattrtest"
)

// MANAGED-OFFER-TLS-AND-INTERNAL-DNS-PLAN §8, D1, Phase 4c: frostmoln_mysql_instance gains
// frostmoln_postgres_instance's require_tls (set at create, server default, fixed) and the
// read-only tls_enabled and hostname. MySQL has no HA, so tls_enabled is pinned too.

const mysqlHostname = "shop.db.prod.omkcmw.ck4to7f6.fm.internal"

func TestSchemaTLSAttributes(t *testing.T) {
	resp := &resource.SchemaResponse{}
	NewResource().Schema(context.Background(), resource.SchemaRequest{}, resp)
	tlsattrtest.AssertRequireTLS(t, resp.Schema.Attributes)
	tlsattrtest.AssertPinnedComputed(t, resp.Schema.Attributes, "tls_enabled")
	tlsattrtest.AssertPinnedComputed(t, resp.Schema.Attributes, "hostname")
}

func TestFromAPI_TLSFields(t *testing.T) {
	var m MysqlInstanceModel
	var diags diag.Diagnostics
	m.fromAPI(context.Background(), &apiMysqlInstance{ID: "db-1", Port: 3306, RequireTLS: true, TLSEnabled: true, Hostname: mysqlHostname}, &diags)
	if !m.RequireTLS.ValueBool() || !m.TLSEnabled.ValueBool() || m.Hostname.ValueString() != mysqlHostname {
		t.Errorf("TLS fields not read back: require_tls=%v tls_enabled=%v hostname=%v", m.RequireTLS, m.TLSEnabled, m.Hostname)
	}
	if m.Port.ValueInt64() != 3306 {
		t.Errorf("port = %v, want 3306 under require_tls (one port)", m.Port)
	}
	m = MysqlInstanceModel{}
	m.fromAPI(context.Background(), &apiMysqlInstance{ID: "db-1", Port: 3306}, &diags)
	if m.RequireTLS.IsNull() || m.RequireTLS.ValueBool() || m.TLSEnabled.IsNull() || m.TLSEnabled.ValueBool() || !m.Hostname.IsNull() {
		t.Errorf("pre-TLS: require_tls=%v tls_enabled=%v hostname=%v", m.RequireTLS, m.TLSEnabled, m.Hostname)
	}
}

func TestToCreateRequest_RequireTLS(t *testing.T) {
	for _, tc := range []struct {
		v    types.Bool
		want string
	}{
		{types.BoolNull(), ""}, {types.BoolUnknown(), ""},
		{types.BoolValue(false), `"requireTls":false`}, {types.BoolValue(true), `"requireTls":true`},
	} {
		m := mysqlPlan()
		m.RequireTLS = tc.v
		b, _ := json.Marshal(m.toCreateRequest(context.Background(), nil))
		if tc.want == "" && strings.Contains(string(b), "requireTls") {
			t.Errorf("%v: an omitted require_tls must not be sent: %s", tc.v, b)
		}
		if tc.want != "" && !strings.Contains(string(b), tc.want) {
			t.Errorf("%v: body %s lacks %s", tc.v, b, tc.want)
		}
	}
}

func TestToUpdateRequest_NeverSendsRequireTLS(t *testing.T) {
	plan := MysqlInstanceModel{Name: types.StringValue("b"), RequireTLS: types.BoolValue(false)}
	state := MysqlInstanceModel{Name: types.StringValue("a"), RequireTLS: types.BoolValue(true)}
	b, _ := json.Marshal(plan.toUpdateRequest(&state))
	if strings.Contains(string(b), "requireTls") {
		t.Errorf("update must not send requireTls: %s", b)
	}
}

// tlsServer answers the instance GET with inst and hands every other request to write
// (nil: any write fails the test).
func tlsServer(t *testing.T, inst apiMysqlInstance, write func(w http.ResponseWriter, r *http.Request)) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/events"):
			w.WriteHeader(http.StatusNotFound)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-1/databases/"+inst.ID:
			_ = json.NewEncoder(w).Encode(inst)
		default:
			if write == nil {
				body, _ := io.ReadAll(r.Body)
				t.Errorf("no write may happen: %s %s %s", r.Method, r.URL.Path, body)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			write(w, r)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

// Legacy state (null require_tls) planned without refresh, with a configured value the
// instance does not hold: Update refuses before it writes anything.
func TestUpdate_LegacyStateConfiguredMismatchRefusedBeforeAnyWrite(t *testing.T) {
	server := tlsServer(t, apiMysqlInstance{ID: "db-123", Name: "my-mysql", Status: "running", RequireTLS: false, Port: 3306}, nil)
	state := mysqlUpdateGuardModel()
	plan := mysqlUpdateGuardModel()
	plan.Name = types.StringValue("renamed")
	plan.RequireTLS = types.BoolValue(true)
	resp := mysqlUpdate(t, server, state, plan)
	errs := resp.Diagnostics.Errors()
	if len(errs) != 1 {
		t.Fatalf("want one error, got %v", resp.Diagnostics)
	}
	if wp, ok := errs[0].(diag.DiagnosticWithPath); !ok || !wp.Path().Equal(path.Root("require_tls")) {
		t.Fatalf("the refusal must be on require_tls, got %#v", errs[0])
	}
	if !strings.Contains(errs[0].Detail(), "require_tls = false") {
		t.Errorf("the detail must name the value the instance holds: %s", errs[0].Detail())
	}
}

// Same legacy state with a matching configured value: the update proceeds and the
// read-back fills the TLS attributes.
func TestUpdate_LegacyStateConfiguredMatchProceeds(t *testing.T) {
	puts := 0
	server := tlsServer(t, apiMysqlInstance{
		ID: "db-123", Name: "renamed", TypeVersion: "8.4", FlavorID: "db.gp1.small", StorageGB: 100,
		VPCID: "vpc-1", SubnetID: "sn-1", Status: "running", Port: 3306, CreatedAt: "2025-01-01T00:00:00Z",
		RequireTLS: true, TLSEnabled: true, Hostname: mysqlHostname,
	}, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		puts++
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(string(body), "requireTls") {
			t.Errorf("update must not send requireTls: %s", body)
		}
		_, _ = w.Write([]byte(`{}`))
	})
	state := mysqlUpdateGuardModel()
	plan := mysqlUpdateGuardModel()
	plan.Name = types.StringValue("renamed")
	plan.RequireTLS = types.BoolValue(true)
	resp := mysqlUpdate(t, server, state, plan)
	if resp.Diagnostics.HasError() {
		t.Fatalf("update: %v", resp.Diagnostics)
	}
	if puts != 1 {
		t.Errorf("PUTs = %d, want 1 (the rename)", puts)
	}
	var got MysqlInstanceModel
	resp.State.Get(context.Background(), &got)
	if !got.RequireTLS.ValueBool() || !got.TLSEnabled.ValueBool() || got.Hostname.ValueString() != mysqlHostname {
		t.Errorf("read-back: require_tls=%v tls_enabled=%v hostname=%v", got.RequireTLS, got.TLSEnabled, got.Hostname)
	}
}

// Create sends a configured require_tls and omits an unconfigured one; both read the
// platform's answer back.
func TestCreate_RequireTLSOnTheWire(t *testing.T) {
	for _, tc := range []struct {
		name    string
		config  types.Bool
		present bool
	}{
		{"omitted", types.BoolUnknown(), false},
		{"configured false", types.BoolValue(false), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var sent map[string]any
			inst := apiMysqlInstance{
				ID: "db-new", Name: "test-mysql", TypeVersion: "8.4", FlavorID: "db.gp1.small", StorageGB: 50,
				VPCID: "vpc-1", SubnetID: "sn-1", Status: "running", Port: 3306, CreatedAt: "2025-01-01T00:00:00Z",
				RequireTLS: !tc.present, TLSEnabled: true, Hostname: mysqlHostname,
			}
			server := tlsServer(t, inst, func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost || r.URL.Path != "/v1/tenants/t-1/databases" {
					t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
					w.WriteHeader(http.StatusNotFound)
					return
				}
				_ = json.NewDecoder(r.Body).Decode(&sent)
				w.WriteHeader(http.StatusCreated)
				_ = json.NewEncoder(w).Encode(inst)
			})
			c := client.NewClient(server.URL, "test-key", client.WithHTTPClient(server.Client())) // pragma: allowlist secret
			c.SetTenantIDForTest("t-1")
			r := &mysqlInstanceResource{client: c, pollInterval: 10 * time.Millisecond, pollTimeout: 5 * time.Second}
			m := mysqlPlan()
			m.RequireTLS = tc.config
			m.TLSEnabled = types.BoolUnknown()
			m.Hostname = types.StringUnknown()
			createResp := resource.CreateResponse{State: emptyMysqlInstanceState(t)}
			r.Create(context.Background(), resource.CreateRequest{Plan: buildMysqlInstancePlan(t, m)}, &createResp)
			if createResp.Diagnostics.HasError() {
				t.Fatalf("create: %v", createResp.Diagnostics)
			}
			got, ok := sent["requireTls"]
			if ok != tc.present || (ok && got != tc.config.ValueBool()) {
				t.Errorf("requireTls on the wire = %v (present %v), want present %v", got, ok, tc.present)
			}
			var state MysqlInstanceModel
			createResp.State.Get(context.Background(), &state)
			if state.RequireTLS.ValueBool() != inst.RequireTLS || state.Hostname.ValueString() != mysqlHostname || !state.TLSEnabled.ValueBool() {
				t.Errorf("state: require_tls=%v tls_enabled=%v hostname=%v", state.RequireTLS, state.TLSEnabled, state.Hostname)
			}
		})
	}
}
