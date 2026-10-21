package postgres_instance

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// MANAGED-OFFER-TLS-AND-INTERNAL-DNS-PLAN §8, D1, Phase 4b: require_tls is set at create
// (server default), fixed afterwards; tls_enabled and hostname are read back. Mirrors
// frostmoln_messaging_instance's require_tls (Phase 4a), minus the port nulling:
// PostgreSQL serves TLS and plaintext on one port.

const pgHostname = "orders.db.prod.omkcmw.ck4to7f6.fm.internal"

// replayPlanModifiers runs an attribute's plan modifiers in schema order, as the
// framework does, and reports the planned value and whether a replacement was recorded.
func replayRequireTLS(t *testing.T, state, config types.Bool) (types.Bool, bool) {
	t.Helper()
	resp := &resource.SchemaResponse{}
	NewResource().Schema(context.Background(), resource.SchemaRequest{}, resp)
	a, ok := resp.Schema.Attributes["require_tls"].(schema.BoolAttribute)
	if !ok {
		t.Fatal("require_tls must be a BoolAttribute")
	}
	if !a.Optional || !a.Computed || a.Default != nil {
		t.Fatal("require_tls must be Optional+Computed without a schema Default (the server owns the default)")
	}
	// The framework plans an Optional+Computed attribute with a null config as unknown.
	plan := config
	if config.IsNull() {
		plan = types.BoolUnknown()
	}
	// Non-null raw state/plan objects: the RequiresReplace family returns early on a
	// null resource (create / destroy).
	stateObj := buildState(t, PostgresInstanceModel{ID: types.StringValue("pg-1")})
	planObj := buildPlan(t, PostgresInstanceModel{ID: types.StringValue("pg-1")})
	req := planmodifier.BoolRequest{
		Path: path.Root("require_tls"), State: stateObj, Plan: planObj,
		StateValue: state, PlanValue: plan, ConfigValue: config,
	}
	replace := false
	for _, m := range a.PlanModifiers {
		r := &planmodifier.BoolResponse{PlanValue: req.PlanValue}
		m.PlanModifyBool(context.Background(), req, r)
		if r.Diagnostics.HasError() {
			t.Fatalf("plan modifier error: %v", r.Diagnostics)
		}
		req.PlanValue = r.PlanValue
		replace = replace || r.RequiresReplace
	}
	return req.PlanValue, replace
}

func TestRequireTLSPlan(t *testing.T) {
	cases := []struct {
		name          string
		state, config types.Bool
		wantReplace   bool
	}{
		// The legacy cases: state written by a provider without the attribute, planned
		// with -refresh=false. None may destroy the database.
		{"legacy null state, omitted", types.BoolNull(), types.BoolNull(), false},
		{"legacy null state, configured true", types.BoolNull(), types.BoolValue(true), false},
		{"legacy null state, configured false", types.BoolNull(), types.BoolValue(false), false},
		// Omitted against a known value: kept, no replace.
		{"omitted against true", types.BoolValue(true), types.BoolNull(), false},
		{"omitted against false", types.BoolValue(false), types.BoolNull(), false},
		{"configured equal", types.BoolValue(true), types.BoolValue(true), false},
		// A real change replaces.
		{"true to false", types.BoolValue(true), types.BoolValue(false), true},
		{"false to true", types.BoolValue(false), types.BoolValue(true), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			planned, replace := replayRequireTLS(t, tc.state, tc.config)
			if replace != tc.wantReplace {
				t.Errorf("replace = %v, want %v", replace, tc.wantReplace)
			}
			if tc.config.IsNull() && !tc.state.IsNull() && !planned.Equal(tc.state) {
				t.Errorf("an omitted require_tls must plan the prior value %v, got %v", tc.state, planned)
			}
			// A null prior (legacy state, -refresh=false) with the attribute omitted must
			// plan UNKNOWN, never null: the apply reads the real value, and a planned null
			// would fail as "inconsistent result after apply" on any in-place change.
			if tc.config.IsNull() && tc.state.IsNull() && !planned.IsUnknown() {
				t.Errorf("legacy null state, omitted: require_tls must plan unknown, got %v", planned)
			}
			if !tc.config.IsNull() && !planned.Equal(tc.config) {
				t.Errorf("a configured require_tls must plan the configured value %v, got %v", tc.config, planned)
			}
		})
	}
}

// hostname is read-only and frozen: a NULL prior (state from an older provider) plans
// unknown, a known prior is kept. tls_enabled is read-only and NOT pinned: for an HA
// pair a standby rebuild can flip it, so every update plans it unknown.
func TestTLSComputedAttributesPlan(t *testing.T) {
	resp := &resource.SchemaResponse{}
	NewResource().Schema(context.Background(), resource.SchemaRequest{}, resp)
	stateObj := buildState(t, PostgresInstanceModel{ID: types.StringValue("pg-1")})
	planObj := buildPlan(t, PostgresInstanceModel{ID: types.StringValue("pg-1")})

	te, ok := resp.Schema.Attributes["tls_enabled"].(schema.BoolAttribute)
	if !ok || !te.Computed || te.Optional || te.Required {
		t.Fatal("tls_enabled must be a computed-only BoolAttribute")
	}
	if len(te.PlanModifiers) != 0 {
		t.Errorf("tls_enabled must carry no plan modifier (an HA standby rebuild changes it), got %d", len(te.PlanModifiers))
	}

	hn, ok := resp.Schema.Attributes["hostname"].(schema.StringAttribute)
	if !ok || !hn.Computed || hn.Optional || hn.Required {
		t.Fatal("hostname must be a computed-only StringAttribute")
	}
	for _, prior := range []types.String{types.StringNull(), types.StringValue(pgHostname)} {
		planned := types.StringUnknown()
		for _, m := range hn.PlanModifiers {
			r := &planmodifier.StringResponse{PlanValue: planned}
			m.PlanModifyString(context.Background(), planmodifier.StringRequest{
				Path: path.Root("hostname"), State: stateObj, Plan: planObj,
				StateValue: prior, PlanValue: planned, ConfigValue: types.StringNull(),
			}, r)
			planned = r.PlanValue
		}
		if prior.IsNull() && !planned.IsUnknown() {
			t.Errorf("hostname: a null prior must plan unknown, got %v", planned)
		}
		if !prior.IsNull() && !planned.Equal(prior) {
			t.Errorf("hostname: a known prior must be kept, got %v", planned)
		}
	}
}

func TestFromAPI_TLSFields(t *testing.T) {
	var m PostgresInstanceModel
	var diags diag.Diagnostics
	m.fromAPI(context.Background(), &apiPostgresInstance{
		ID: "pg-1", Port: 5432, RequireTLS: true, TLSEnabled: true, Hostname: pgHostname,
	}, &diags)
	if diags.HasError() {
		t.Fatalf("fromAPI: %v", diags)
	}
	if !m.RequireTLS.ValueBool() || !m.TLSEnabled.ValueBool() || m.Hostname.ValueString() != pgHostname {
		t.Errorf("TLS fields not read back: require_tls=%v tls_enabled=%v hostname=%v", m.RequireTLS, m.TLSEnabled, m.Hostname)
	}
	// PostgreSQL negotiates TLS on its one port: require_tls never nulls it.
	if m.Port.ValueInt64() != 5432 {
		t.Errorf("port = %v, want 5432 under require_tls", m.Port)
	}

	// A pre-TLS instance (or a service that predates the fields): false, false, null.
	m = PostgresInstanceModel{}
	m.fromAPI(context.Background(), &apiPostgresInstance{ID: "pg-1", Port: 5432}, &diags)
	if m.RequireTLS.IsNull() || m.RequireTLS.ValueBool() || m.TLSEnabled.IsNull() || m.TLSEnabled.ValueBool() {
		t.Errorf("require_tls / tls_enabled = %v / %v, want false / false", m.RequireTLS, m.TLSEnabled)
	}
	if !m.Hostname.IsNull() {
		t.Errorf("hostname = %v, want null", m.Hostname)
	}
}

func TestToCreateRequest_RequireTLS(t *testing.T) {
	m := PostgresInstanceModel{RequireTLS: types.BoolNull()}
	b, _ := json.Marshal(m.toCreateRequest(context.Background(), nil))
	if strings.Contains(string(b), "requireTls") {
		t.Errorf("an omitted require_tls must not be sent (the server defaults it): %s", b)
	}
	m.RequireTLS = types.BoolUnknown()
	if req := m.toCreateRequest(context.Background(), nil); req.RequireTLS != nil {
		t.Error("an unknown require_tls must not be sent")
	}
	m.RequireTLS = types.BoolValue(false)
	b, _ = json.Marshal(m.toCreateRequest(context.Background(), nil))
	if !strings.Contains(string(b), `"requireTls":false`) {
		t.Errorf("a configured false must be sent explicitly: %s", b)
	}
	m.RequireTLS = types.BoolValue(true)
	b, _ = json.Marshal(m.toCreateRequest(context.Background(), nil))
	if !strings.Contains(string(b), `"requireTls":true`) {
		t.Errorf("a configured true must be sent: %s", b)
	}
}

// The update request never carries requireTls: it is fixed at create.
func TestToUpdateRequest_NeverSendsRequireTLS(t *testing.T) {
	plan := PostgresInstanceModel{Name: types.StringValue("b"), RequireTLS: types.BoolValue(false)}
	state := PostgresInstanceModel{Name: types.StringValue("a"), RequireTLS: types.BoolValue(true)}
	b, _ := json.Marshal(plan.toUpdateRequest(&state))
	if strings.Contains(string(b), "requireTls") {
		t.Errorf("update must not send requireTls: %s", b)
	}
}

// tlsServer answers the instance GET (and the reads that accompany it) with inst, and
// hands every other request to write (nil: any write fails the test).
func tlsServer(t *testing.T, inst apiPostgresInstance, write func(w http.ResponseWriter, r *http.Request)) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/extensions"):
			_ = json.NewEncoder(w).Encode(map[string]any{"extensionRevision": 0, "extensions": []map[string]any{}})
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

func legacyBase() PostgresInstanceModel {
	return PostgresInstanceModel{
		ID: types.StringValue("pg-123"), Name: types.StringValue("old"), Version: types.StringValue("16"),
		FlavorID: types.StringValue("db.gp1.small"), StorageGB: types.Int64Value(50),
		VPCID: types.StringValue("vpc-1"), SubnetID: types.StringValue("sn-1"),
		Status: types.StringValue("running"), CreatedAt: types.StringValue("2025-01-01T00:00:00Z"),
	}
}

// Legacy state (null require_tls) planned without refresh, with a configured value the
// instance does not hold: Update refuses before it writes anything.
func TestUpdate_LegacyStateConfiguredMismatchRefusedBeforeAnyWrite(t *testing.T) {
	server := tlsServer(t, apiPostgresInstance{
		ID: "pg-123", Name: "old", PostgresVersion: "16", Status: "running", RequireTLS: false, Port: 5432,
	}, nil)
	r := newResource(newClient(t, server))

	state := buildState(t, legacyBase())
	planModel := legacyBase()
	planModel.Name = types.StringValue("new")
	planModel.RequireTLS = types.BoolValue(true)
	plan := buildPlan(t, planModel)

	resp := resource.UpdateResponse{State: state}
	r.Update(context.Background(), updateRequest(plan, state), &resp)
	errs := resp.Diagnostics.Errors()
	if len(errs) != 1 {
		t.Fatalf("want one error, got %v", resp.Diagnostics)
	}
	withPath, ok := errs[0].(diag.DiagnosticWithPath)
	if !ok || !withPath.Path().Equal(path.Root("require_tls")) {
		t.Fatalf("the refusal must be an attribute error on require_tls, got %#v", errs[0])
	}
	if !strings.Contains(errs[0].Detail(), "require_tls = false") {
		t.Errorf("the detail must name the value the instance holds: %s", errs[0].Detail())
	}
}

// Same legacy state with a matching configured value: the update proceeds and the
// read-back fills the TLS attributes.
func TestUpdate_LegacyStateConfiguredMatchProceeds(t *testing.T) {
	puts := 0
	server := tlsServer(t, apiPostgresInstance{
		ID: "pg-123", Name: "new", PostgresVersion: "16", FlavorID: "db.gp1.small", StorageGB: 50,
		VPCID: "vpc-1", SubnetID: "sn-1", Status: "running", Port: 5432, CreatedAt: "2025-01-01T00:00:00Z",
		RequireTLS: true, TLSEnabled: true, Hostname: pgHostname,
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
	r := newResource(newClient(t, server))

	state := buildState(t, legacyBase())
	planModel := legacyBase()
	planModel.Name = types.StringValue("new")
	planModel.RequireTLS = types.BoolValue(true)
	plan := buildPlan(t, planModel)

	resp := resource.UpdateResponse{State: state}
	r.Update(context.Background(), updateRequest(plan, state), &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("update: %v", resp.Diagnostics)
	}
	if puts != 1 {
		t.Errorf("PUTs = %d, want 1 (the rename)", puts)
	}
	var got PostgresInstanceModel
	resp.State.Get(context.Background(), &got)
	if !got.RequireTLS.ValueBool() || !got.TLSEnabled.ValueBool() || got.Hostname.ValueString() != pgHostname {
		t.Errorf("read-back: require_tls=%v tls_enabled=%v hostname=%v", got.RequireTLS, got.TLSEnabled, got.Hostname)
	}
}

// A known require_tls is never re-checked against the platform: the plan already
// decided (replace or keep).
func TestUpdate_KnownStateSkipsTheRequireTLSRead(t *testing.T) {
	gets := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-1/databases/pg-123" {
			gets++
		}
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()
	r := newResource(newClient(t, server))
	m := legacyBase()
	m.RequireTLS = types.BoolValue(true)
	var diags diag.Diagnostics
	r.checkRequireTLSUnchanged(context.Background(), "pg-123", &m, &m, &diags)
	if diags.HasError() || gets != 0 {
		t.Errorf("a known prior require_tls must not be re-read: gets=%d diags=%v", gets, diags)
	}
}

func validateConfig(t *testing.T, requireTLS types.Bool, restore types.Object) diag.Diagnostics {
	t.Helper()
	m := fullPlanModel()
	m.RequireTLS = requireTLS
	m.RestoreFrom = restore
	plan := buildPlan(t, m)
	resp := &resource.ValidateConfigResponse{}
	NewResource().(resource.ResourceWithValidateConfig).ValidateConfig(context.Background(),
		resource.ValidateConfigRequest{Config: tfsdk.Config{Schema: plan.Schema, Raw: plan.Raw}}, resp)
	return resp.Diagnostics
}

// A restore target takes the platform default; a configured require_tls beside
// restore_from could never be enacted, so it is refused before anything is planned.
func TestValidateConfig_RequireTLSWithRestoreFrom(t *testing.T) {
	restore := types.ObjectValueMust(restoreFromAttrTypes, map[string]attr.Value{
		"source_instance_id": types.StringValue("pg-src"),
		"point_in_time":      types.StringNull(),
		"backup_id":          types.StringValue("bk-1"),
	})
	for _, v := range []bool{true, false} {
		diags := validateConfig(t, types.BoolValue(v), restore)
		if !diags.HasError() {
			t.Fatalf("require_tls = %v with restore_from must be refused", v)
		}
		withPath, ok := diags.Errors()[0].(diag.DiagnosticWithPath)
		if !ok || !withPath.Path().Equal(path.Root("require_tls")) {
			t.Errorf("the refusal must be on require_tls, got %#v", diags.Errors()[0])
		}
	}
	if diags := validateConfig(t, types.BoolNull(), restore); diags.HasError() {
		t.Errorf("restore_from without require_tls must validate: %v", diags)
	}
	if diags := validateConfig(t, types.BoolValue(true), types.ObjectNull(restoreFromAttrTypes)); diags.HasError() {
		t.Errorf("require_tls without restore_from must validate: %v", diags)
	}
	if diags := validateConfig(t, types.BoolUnknown(), restore); diags.HasError() {
		t.Errorf("an unknown require_tls cannot be judged yet: %v", diags)
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
			inst := apiPostgresInstance{
				ID: "pg-new", Name: "test-pg", PostgresVersion: "16", FlavorID: "db.gp1.small", StorageGB: 50,
				VPCID: "vpc-1", SubnetID: "sn-1", Status: "running", Port: 5432, CreatedAt: "2025-01-01T00:00:00Z",
				RequireTLS: !tc.present, TLSEnabled: true, Hostname: pgHostname,
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
			r := newResource(newClient(t, server))
			m := fullPlanModel()
			m.RequireTLS = tc.config
			m.TLSEnabled = types.BoolUnknown()
			m.Hostname = types.StringUnknown()
			createResp := resource.CreateResponse{State: emptyState(t)}
			r.Create(context.Background(), createRequest(buildPlan(t, m)), &createResp)
			if createResp.Diagnostics.HasError() {
				t.Fatalf("create: %v", createResp.Diagnostics)
			}
			got, ok := sent["requireTls"]
			if ok != tc.present || (ok && got != tc.config.ValueBool()) {
				t.Errorf("requireTls on the wire = %v (present %v), want present %v", got, ok, tc.present)
			}
			var state PostgresInstanceModel
			createResp.State.Get(context.Background(), &state)
			if state.RequireTLS.ValueBool() != inst.RequireTLS || state.Hostname.ValueString() != pgHostname || !state.TLSEnabled.ValueBool() {
				t.Errorf("state: require_tls=%v tls_enabled=%v hostname=%v", state.RequireTLS, state.TLSEnabled, state.Hostname)
			}
		})
	}
}

// An HA pair's standby rebuild cleared the TLS marker after the plan was made: the update
// reads tls_enabled false back over a prior true and records it without error (the plan
// left it unknown, so core accepts the new value).
func TestUpdate_TLSEnabledFlipOnReadBackIsRecorded(t *testing.T) {
	server := tlsServer(t, apiPostgresInstance{
		ID: "pg-123", Name: "new", PostgresVersion: "16", FlavorID: "db.gp1.small", StorageGB: 50,
		VPCID: "vpc-1", SubnetID: "sn-1", Status: "running", Port: 5432, CreatedAt: "2025-01-01T00:00:00Z",
		HAEnabled: true, RequireTLS: true, TLSEnabled: false, Hostname: pgHostname,
	}, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(`{}`))
	})
	r := newResource(newClient(t, server))

	prior := legacyBase()
	prior.HAEnabled = types.BoolValue(true)
	prior.RequireTLS, prior.TLSEnabled, prior.Hostname = types.BoolValue(true), types.BoolValue(true), types.StringValue(pgHostname)
	state := buildState(t, prior)
	planModel := prior
	planModel.Name = types.StringValue("new")
	planModel.TLSEnabled = types.BoolUnknown() // what the framework plans for a modifier-less Computed attribute
	plan := buildPlan(t, planModel)

	resp := resource.UpdateResponse{State: state}
	r.Update(context.Background(), updateRequest(plan, state), &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("update: %v", resp.Diagnostics)
	}
	var got PostgresInstanceModel
	resp.State.Get(context.Background(), &got)
	if got.TLSEnabled.IsNull() || got.TLSEnabled.ValueBool() {
		t.Errorf("tls_enabled = %v, want the platform's false", got.TLSEnabled)
	}
	if got.Hostname.ValueString() != pgHostname {
		t.Errorf("hostname = %v, want it kept", got.Hostname)
	}
}

// ModifyPlan repeats ValidateConfig's refusal on a create: a require_tls that was
// unknown at validation is known by plan time.
func TestModifyPlan_RequireTLSWithRestoreFromRefusedOnCreate(t *testing.T) {
	restore := types.ObjectValueMust(restoreFromAttrTypes, map[string]attr.Value{
		"source_instance_id": types.StringValue("pg-src"),
		"point_in_time":      types.StringNull(),
		"backup_id":          types.StringValue("bk-1"),
	})
	run := func(requireTLS types.Bool, restore types.Object) diag.Diagnostics {
		m := fullPlanModel()
		m.RequireTLS = requireTLS
		m.RestoreFrom = restore
		plan := buildPlan(t, m)
		cfg := tfsdk.Config{Schema: plan.Schema, Raw: plan.Raw}
		resp := resource.ModifyPlanResponse{Plan: plan}
		NewResource().(*postgresInstanceResource).ModifyPlan(context.Background(),
			resource.ModifyPlanRequest{Plan: plan, State: emptyState(t), Config: cfg}, &resp)
		return resp.Diagnostics
	}
	diags := run(types.BoolValue(true), restore)
	if !diags.HasError() {
		t.Fatal("require_tls with restore_from must be refused at plan time")
	}
	if withPath, ok := diags.Errors()[0].(diag.DiagnosticWithPath); !ok || !withPath.Path().Equal(path.Root("require_tls")) {
		t.Errorf("the refusal must be on require_tls, got %#v", diags.Errors()[0])
	}
	if d := run(types.BoolNull(), restore); d.HasError() {
		t.Errorf("restore_from alone must plan: %v", d)
	}
	if d := run(types.BoolValue(true), types.ObjectNull(restoreFromAttrTypes)); d.HasError() {
		t.Errorf("require_tls alone must plan: %v", d)
	}
}
