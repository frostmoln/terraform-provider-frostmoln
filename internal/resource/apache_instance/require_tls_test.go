package apache_instance

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	resSchema "github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/tlsattr/tlsattrtest"
)

// MANAGED-OFFER-TLS-AND-INTERNAL-DNS-PLAN §8, Phase 4e: require_tls (default false, set at
// create, fixed: true = port 80 redirects), the read-only hostname, and tls_enabled, which
// the platform flips false -> true once the instance serves a certificate: no pinning plan
// modifier, and a configured value it would contradict is refused at plan time.

const wsHostname = "site.web.prod.omkcmw.ck4to7f6.fm.internal"

func TestSchemaTLSAttributes_4e(t *testing.T) {
	var resp resource.SchemaResponse
	NewResource().Schema(context.Background(), resource.SchemaRequest{}, &resp)
	tlsattrtest.AssertRequireTLS(t, resp.Schema.Attributes)
	tlsattrtest.AssertPinnedComputed(t, resp.Schema.Attributes, "hostname")
	te, ok := resp.Schema.Attributes["tls_enabled"].(resSchema.BoolAttribute)
	if !ok || !te.Optional || !te.Computed {
		t.Fatal("tls_enabled must stay an Optional+Computed BoolAttribute")
	}
	if len(te.PlanModifiers) != 0 {
		t.Errorf("tls_enabled must carry no plan modifier (it flips once a certificate is served), got %d", len(te.PlanModifiers))
	}
	// Planned on an update with the attribute omitted: unknown, so the apply may read true.
	planned, _ := tlsattrtest.ReplayBool(t, te.PlanModifiers, "tls_enabled", types.BoolValue(false), types.BoolNull())
	if !planned.IsUnknown() {
		t.Errorf("an omitted tls_enabled must plan unknown on an update, got %v", planned)
	}
}

func TestFromAPI_TLSFields_4e(t *testing.T) {
	var m ApacheInstanceModel
	var diags diag.Diagnostics
	m.fromAPI(context.Background(), &apiWebserverInstance{ID: "w-1", Port: 80, TLSEnabled: true, RequireTLS: true, Hostname: wsHostname}, &diags)
	if !m.TLSEnabled.ValueBool() || !m.RequireTLS.ValueBool() || m.Hostname.ValueString() != wsHostname || m.Port.ValueInt64() != 80 {
		t.Errorf("read: tls_enabled=%v require_tls=%v hostname=%v port=%v", m.TLSEnabled, m.RequireTLS, m.Hostname, m.Port)
	}
	m = ApacheInstanceModel{}
	m.fromAPI(context.Background(), &apiWebserverInstance{ID: "w-1", Port: 80}, &diags)
	if m.RequireTLS.IsNull() || m.RequireTLS.ValueBool() || !m.Hostname.IsNull() || m.TLSEnabled.ValueBool() {
		t.Errorf("pre-TLS: require_tls=%v hostname=%v tls_enabled=%v", m.RequireTLS, m.Hostname, m.TLSEnabled)
	}
}

func TestToCreateRequest_RequireTLS_4e(t *testing.T) {
	for _, tc := range []struct {
		v    types.Bool
		want string
	}{
		{types.BoolNull(), ""}, {types.BoolUnknown(), ""},
		{types.BoolValue(false), `"requireTls":false`}, {types.BoolValue(true), `"requireTls":true`},
	} {
		m := baseApacheModel()
		m.RequireTLS = tc.v
		var diags diag.Diagnostics
		req := m.toCreateRequest(context.Background(), &diags)
		b, _ := json.Marshal(req)
		if tc.want == "" && strings.Contains(string(b), "requireTls") {
			t.Errorf("%v: an omitted require_tls must not be sent: %s", tc.v, b)
		}
		if tc.want != "" && !strings.Contains(string(b), tc.want) {
			t.Errorf("%v: body %s lacks %s", tc.v, b, tc.want)
		}
		requireNoTLSEnabledOnWire(t, req)
	}
}

// modifyPlan runs ModifyPlan for a configuration (and prior state; nil = create).
func modifyPlan(t *testing.T, config ApacheInstanceModel, prior *ApacheInstanceModel) diag.Diagnostics {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()
	c := client.NewClient(server.URL, "test-key", client.WithHTTPClient(server.Client())) // pragma: allowlist secret
	c.SetTenantIDForTest("t-1")
	r := newTestApacheResource(c)
	plan := buildApacheInstancePlan(t, config)
	state := emptyApacheInstanceState(t)
	if prior != nil {
		state = buildApacheInstanceState(t, *prior)
	}
	resp := resource.ModifyPlanResponse{Plan: plan}
	r.ModifyPlan(context.Background(), resource.ModifyPlanRequest{
		Plan: plan, State: state, Config: configOf(plan),
	}, &resp)
	return resp.Diagnostics
}

func TestModifyPlan_TLSEnabledConfigRefusal(t *testing.T) {
	onPath := func(d diag.Diagnostics) bool {
		if !d.HasError() {
			return false
		}
		wp, ok := d.Errors()[0].(diag.DiagnosticWithPath)
		return ok && wp.Path().Equal(path.Root("tls_enabled"))
	}
	create := baseApacheModel()
	create.TLSEnabled = types.BoolValue(false)
	if d := modifyPlan(t, create, nil); !onPath(d) {
		t.Errorf("a configured tls_enabled = false on a create must be refused on tls_enabled, got %v", d)
	}
	create.TLSEnabled = types.BoolNull()
	if d := modifyPlan(t, create, nil); d.HasError() {
		t.Errorf("a create without tls_enabled must plan: %v", d)
	}

	prior := baseApacheModel()
	prior.ID = types.StringValue("w-1")
	prior.TLSEnabled = types.BoolValue(true)
	cfg := prior
	cfg.TLSEnabled = types.BoolValue(false)
	// On an update: a WARNING at plan (an error would also block a destroy); Update refuses.
	d := modifyPlan(t, cfg, &prior)
	if d.HasError() || d.WarningsCount() != 1 || !strings.Contains(d.Warnings()[0].Detail(), "reports true") {
		t.Errorf("tls_enabled = false against an instance serving TLS must be warned about, got %v", d)
	}
	prior.TLSEnabled = types.BoolValue(false)
	if d := modifyPlan(t, cfg, &prior); d.HasError() || d.WarningsCount() != 0 {
		t.Errorf("tls_enabled = false on an instance serving no certificate must plan quietly: %v", d)
	}
	cfg.TLSEnabled = types.BoolNull()
	prior.TLSEnabled = types.BoolValue(true)
	if d := modifyPlan(t, cfg, &prior); d.HasError() {
		t.Errorf("an omitted tls_enabled must plan: %v", d)
	}
	// A state written before tls_enabled was observed (null prior) is no contradiction.
	cfg.TLSEnabled = types.BoolValue(false)
	prior.TLSEnabled = types.BoolNull()
	if d := modifyPlan(t, cfg, &prior); d.HasError() || d.WarningsCount() != 0 {
		t.Errorf("tls_enabled = false against a null prior must plan quietly: %v", d)
	}
}

// The create's read-back of a served certificate (tls_enabled true after a plan that left
// it unknown) is recorded without error, as are hostname and the requirement.
func TestCreate_TLSServedReadBack(t *testing.T) {
	var sent map[string]any
	inst := `{"id":"w-1","name":"my-apache","type":"apache","typeVersion":"1","flavorId":"web.co1.small","storageGb":20,` +
		`"vpcId":"vpc-1","subnetId":"sn-1","status":"running","port":80,"privateIp":"10.0.0.5","createdAt":"2025-01-01T00:00:00Z",` +
		`"tlsEnabled":true,"requireTls":true,"hostname":"` + wsHostname + `","tags":{}}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/tenants/t-1/webservers":
			_ = json.NewDecoder(r.Body).Decode(&sent)
			w.WriteHeader(http.StatusCreated)
			_, _ = fmt.Fprint(w, inst)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-1/webservers/w-1":
			_, _ = fmt.Fprint(w, inst)
		case strings.HasSuffix(r.URL.Path, "/events"):
			w.WriteHeader(http.StatusNotFound)
		case r.Method == http.MethodPut && strings.HasSuffix(r.URL.Path, "/config"):
			_, _ = fmt.Fprint(w, `{"status":"applied","revision":1,"config":{"client_max_body_size":"10m"}}`)
		default:
			body, _ := io.ReadAll(r.Body)
			t.Logf("unhandled request: %s %s %s", r.Method, r.URL.Path, body)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	c := client.NewClient(server.URL, "test-key", client.WithHTTPClient(server.Client())) // pragma: allowlist secret
	c.SetTenantIDForTest("t-1")
	m := baseApacheModel()
	m.Config = types.MapNull(types.StringType)
	m.TLSEnabled = types.BoolUnknown()
	m.RequireTLS = types.BoolValue(true)
	m.Hostname = types.StringUnknown()
	resp := resource.CreateResponse{State: emptyApacheInstanceState(t)}
	newTestApacheResource(c).Create(context.Background(), resource.CreateRequest{Plan: buildApacheInstancePlan(t, m)}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("create: %v", resp.Diagnostics)
	}
	if sent["requireTls"] != true {
		t.Errorf("requireTls not sent: %v", sent)
	}
	if _, ok := sent["tlsEnabled"]; ok {
		t.Errorf("tlsEnabled is read-only and never sent: %v", sent)
	}
	var got ApacheInstanceModel
	resp.State.Get(context.Background(), &got)
	if !got.TLSEnabled.ValueBool() || !got.RequireTLS.ValueBool() || got.Hostname.ValueString() != wsHostname {
		t.Errorf("state: tls_enabled=%v require_tls=%v hostname=%v", got.TLSEnabled, got.RequireTLS, got.Hostname)
	}
}

// Legacy state (null require_tls) planned without refresh, with a configured value the
// instance does not hold: Update refuses before it writes anything.
func TestUpdate_LegacyRequireTLSMismatchRefusedBeforeAnyWrite(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-1/webservers/w-1" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, `{"id":"w-1","name":"my-apache","status":"running","port":80,"requireTls":false,"tlsEnabled":false,"tags":{}}`)
			return
		}
		t.Errorf("no write may happen: %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer server.Close()
	c := client.NewClient(server.URL, "test-key", client.WithHTTPClient(server.Client())) // pragma: allowlist secret
	c.SetTenantIDForTest("t-1")
	stateModel := baseApacheModel()
	stateModel.ID = types.StringValue("w-1")
	stateModel.Status = types.StringValue("running")
	planModel := stateModel
	planModel.Name = types.StringValue("renamed")
	planModel.RequireTLS = types.BoolValue(true)
	state := buildApacheInstanceState(t, stateModel)
	plan := buildApacheInstancePlan(t, planModel)
	resp := resource.UpdateResponse{State: state}
	newTestApacheResource(c).Update(context.Background(), resource.UpdateRequest{Plan: plan, Config: configOf(plan), State: state}, &resp)
	errs := resp.Diagnostics.Errors()
	if len(errs) != 1 {
		t.Fatalf("want one error, got %v", resp.Diagnostics)
	}
	if wp, ok := errs[0].(diag.DiagnosticWithPath); !ok || !wp.Path().Equal(path.Root("require_tls")) {
		t.Fatalf("the refusal must be on require_tls, got %#v", errs[0])
	}
}

// The apply-time half of the update warning: a configured false against an instance
// serving TLS is refused before any request.
func TestUpdate_TLSEnabledFalseAgainstServedRefusedBeforeAnyWrite(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("no request may happen: %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer server.Close()
	c := client.NewClient(server.URL, "test-key", client.WithHTTPClient(server.Client())) // pragma: allowlist secret
	c.SetTenantIDForTest("t-1")
	stateModel := baseApacheModel()
	stateModel.ID = types.StringValue("w-1")
	stateModel.Status = types.StringValue("running")
	stateModel.TLSEnabled = types.BoolValue(true)
	stateModel.RequireTLS = types.BoolValue(false)
	planModel := stateModel
	planModel.Name = types.StringValue("renamed")
	planModel.TLSEnabled = types.BoolValue(false)
	state := buildApacheInstanceState(t, stateModel)
	plan := buildApacheInstancePlan(t, planModel)
	resp := resource.UpdateResponse{State: state}
	newTestApacheResource(c).Update(context.Background(), resource.UpdateRequest{Plan: plan, Config: configOf(plan), State: state}, &resp)
	if !resp.Diagnostics.HasError() {
		t.Fatal("want the refusal")
	}
	if wp, ok := resp.Diagnostics.Errors()[0].(diag.DiagnosticWithPath); !ok || !wp.Path().Equal(path.Root("tls_enabled")) {
		t.Errorf("the refusal must be on tls_enabled, got %#v", resp.Diagnostics.Errors()[0])
	}
}
