package redis_instance

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

// MANAGED-OFFER-TLS-AND-INTERNAL-DNS-PLAN §8, D1, Phase 4d: require_tls (set at create,
// server default, fixed), and the read-only tls_enabled, hostname and tls_port. With
// require_tls the plaintext port is absent from the API and null here, never 0.

const cacheHostname = "sessions.cache.prod.omkcmw.ck4to7f6.fm.internal"

func TestSchemaTLSAttributes(t *testing.T) {
	resp := &resource.SchemaResponse{}
	NewResource().Schema(context.Background(), resource.SchemaRequest{}, resp)
	tlsattrtest.AssertRequireTLS(t, resp.Schema.Attributes)
	for _, name := range []string{"tls_enabled", "hostname", "tls_port"} {
		tlsattrtest.AssertPinnedComputed(t, resp.Schema.Attributes, name)
	}
}

func TestFromAPI_TLSFields(t *testing.T) {
	var diags diag.Diagnostics
	for _, tc := range []struct {
		name             string
		inst             apiRedisInstance
		port, tlsPort    int64
		require, enabled bool
		hostname         string
	}{
		{"tls required: no plaintext port", apiRedisInstance{ID: "c-1", RequireTLS: true, TLSEnabled: true, Hostname: cacheHostname, TLSPort: 6380},
			0, 6380, true, true, cacheHostname},
		{"tls beside plaintext", apiRedisInstance{ID: "c-1", Port: 6379, RequireTLS: false, TLSEnabled: true, Hostname: cacheHostname, TLSPort: 6380},
			6379, 6380, false, true, cacheHostname},
		{"pre-TLS", apiRedisInstance{ID: "c-1", Port: 6379}, 6379, 0, false, false, ""},
		{"required, not issued", apiRedisInstance{ID: "c-1", RequireTLS: true}, 0, 0, true, false, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var m RedisInstanceModel
			m.fromAPI(context.Background(), &tc.inst, &diags)
			if (tc.port == 0) != m.Port.IsNull() || (tc.port != 0 && m.Port.ValueInt64() != tc.port) {
				t.Errorf("port = %v, want %d (null when 0)", m.Port, tc.port)
			}
			if (tc.tlsPort == 0) != m.TLSPort.IsNull() || (tc.tlsPort != 0 && m.TLSPort.ValueInt64() != tc.tlsPort) {
				t.Errorf("tls_port = %v, want %d (null when 0)", m.TLSPort, tc.tlsPort)
			}
			if m.RequireTLS.IsNull() || m.RequireTLS.ValueBool() != tc.require || m.TLSEnabled.IsNull() || m.TLSEnabled.ValueBool() != tc.enabled {
				t.Errorf("require_tls=%v tls_enabled=%v", m.RequireTLS, m.TLSEnabled)
			}
			if (tc.hostname == "") != m.Hostname.IsNull() || (tc.hostname != "" && m.Hostname.ValueString() != tc.hostname) {
				t.Errorf("hostname = %v, want %q", m.Hostname, tc.hostname)
			}
		})
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
		m := RedisInstanceModel{Name: types.StringValue("x"), RequireTLS: tc.v}
		b, _ := json.Marshal(m.toCreateRequest(context.Background(), nil))
		if tc.want == "" && strings.Contains(string(b), "requireTls") {
			t.Errorf("%v: an omitted require_tls must not be sent: %s", tc.v, b)
		}
		if tc.want != "" && !strings.Contains(string(b), tc.want) {
			t.Errorf("%v: body %s lacks %s", tc.v, b, tc.want)
		}
	}
	plan := RedisInstanceModel{Name: types.StringValue("b"), RequireTLS: types.BoolValue(false)}
	state := RedisInstanceModel{Name: types.StringValue("a"), RequireTLS: types.BoolValue(true)}
	if b, _ := json.Marshal(plan.toUpdateRequest(&state)); strings.Contains(string(b), "requireTls") {
		t.Errorf("update must not send requireTls: %s", b)
	}
}

func cacheTLSServer(t *testing.T, inst apiRedisInstance, write func(w http.ResponseWriter, r *http.Request)) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/events"):
			w.WriteHeader(http.StatusNotFound)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-1/caches/"+inst.ID:
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

func cacheTLSResource(server *httptest.Server) *redisInstanceResource {
	c := client.NewClient(server.URL, "test-key", client.WithHTTPClient(server.Client())) // pragma: allowlist secret
	c.SetTenantIDForTest("t-1")
	return &redisInstanceResource{client: c, pollInterval: 5 * time.Millisecond, pollTimeout: 5 * time.Second}
}

func cacheLegacyModel() RedisInstanceModel {
	return RedisInstanceModel{
		ID: types.StringValue("c-123"), Name: types.StringValue("old"), Version: types.StringValue("7.2"),
		FlavorID: types.StringValue("cache.mo1.small"), StorageGB: types.Int64Value(10),
		VPCID: types.StringValue("vpc-1"), SubnetID: types.StringValue("sn-1"),
		PersistenceMode: types.StringValue("rdb"), EvictionPolicy: types.StringValue("noeviction"),
		Status: types.StringValue("running"), Port: types.Int64Value(6379), CreatedAt: types.StringValue("2025-01-01T00:00:00Z"),
	}
}

// Legacy state (null require_tls) planned without refresh, with a configured value the
// instance does not hold: Update refuses before it writes anything.
func TestUpdate_LegacyStateConfiguredMismatchRefusedBeforeAnyWrite(t *testing.T) {
	server := cacheTLSServer(t, apiRedisInstance{ID: "c-123", Name: "old", Status: "running", Port: 6379}, nil)
	state := buildRedisInstanceState(t, cacheLegacyModel())
	planModel := cacheLegacyModel()
	planModel.Name = types.StringValue("new")
	planModel.RequireTLS = types.BoolValue(true)
	resp := resource.UpdateResponse{State: state}
	cacheTLSResource(server).Update(context.Background(), resource.UpdateRequest{Plan: buildRedisInstancePlan(t, planModel), State: state}, &resp)
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

// Create sends a configured require_tls, omits an unconfigured one, and reads back a TLS
// instance with no plaintext port without an inconsistent result.
func TestCreate_RequireTLSOnTheWire(t *testing.T) {
	for _, tc := range []struct {
		name    string
		config  types.Bool
		present bool
	}{
		{"omitted", types.BoolUnknown(), false},
		{"configured true", types.BoolValue(true), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var sent map[string]any
			inst := apiRedisInstance{
				ID: "c-new", Name: "sessions", TypeVersion: "7.2", FlavorID: "cache.mo1.small", StorageGB: 10,
				VPCID: "vpc-1", SubnetID: "sn-1", PersistenceMode: "rdb", EvictionPolicy: "noeviction",
				Status: "running", PrivateIP: "10.0.1.5", AdminUsername: "default", CreatedAt: "2025-01-01T00:00:00Z",
				RequireTLS: true, TLSEnabled: true, Hostname: cacheHostname, TLSPort: 6380,
			}
			server := cacheTLSServer(t, inst, func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost || r.URL.Path != "/v1/tenants/t-1/caches" {
					t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
					w.WriteHeader(http.StatusNotFound)
					return
				}
				_ = json.NewDecoder(r.Body).Decode(&sent)
				w.WriteHeader(http.StatusCreated)
				_ = json.NewEncoder(w).Encode(inst)
			})
			m := RedisInstanceModel{
				Name: types.StringValue("sessions"), Version: types.StringValue("7.2"), FlavorID: types.StringValue("cache.mo1.small"),
				VPCID: types.StringValue("vpc-1"), SubnetID: types.StringValue("sn-1"),
				PersistenceMode: types.StringValue("rdb"), EvictionPolicy: types.StringValue("noeviction"),
				RequireTLS: tc.config, TLSEnabled: types.BoolUnknown(), Hostname: types.StringUnknown(),
				TLSPort: types.Int64Unknown(), Port: types.Int64Unknown(),
			}
			createResp := resource.CreateResponse{State: emptyRedisInstanceState(t)}
			cacheTLSResource(server).Create(context.Background(), resource.CreateRequest{Plan: buildRedisInstancePlan(t, m)}, &createResp)
			if createResp.Diagnostics.HasError() {
				t.Fatalf("create: %v", createResp.Diagnostics)
			}
			got, ok := sent["requireTls"]
			if ok != tc.present || (ok && got != tc.config.ValueBool()) {
				t.Errorf("requireTls on the wire = %v (present %v), want present %v", got, ok, tc.present)
			}
			var st RedisInstanceModel
			createResp.State.Get(context.Background(), &st)
			if !st.Port.IsNull() || st.TLSPort.ValueInt64() != 6380 || !st.RequireTLS.ValueBool() || st.Hostname.ValueString() != cacheHostname {
				t.Errorf("state: port=%v tls_port=%v require_tls=%v hostname=%v", st.Port, st.TLSPort, st.RequireTLS, st.Hostname)
			}
		})
	}
}
