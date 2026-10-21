package messaging_instance

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
)

// MANAGED-OFFER-TLS-AND-INTERNAL-DNS-PLAN §8, D1: require_tls is set at create (server
// default true), fixed afterwards, and closes the plaintext ports.

// replayRequireTLS runs require_tls's plan modifiers in schema order, as the framework
// does, and reports the planned value and whether a replacement was recorded.
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
	stateObj := buildMessagingInstanceState(t, MessagingInstanceModel{ID: types.StringValue("mq-1")})
	planObj := buildMessagingInstancePlan(t, MessagingInstanceModel{ID: types.StringValue("mq-1")})
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
		// with -refresh=false. Neither may destroy the broker.
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

func TestFromAPI_RequireTLSClosesPlaintextPorts(t *testing.T) {
	var m MessagingInstanceModel
	var diags diag.Diagnostics
	// A service that still reports the plaintext ports beside requireTls: they are not
	// endpoints, so they read back null.
	m.fromAPI(context.Background(), &apiMessagingInstance{
		ID: "mq-1", RequireTLS: true, TLSEnabled: true, Port: 5672, AMQPSPort: 5671, ManagementPort: 15672,
	}, &diags)
	if !m.RequireTLS.ValueBool() {
		t.Errorf("require_tls = %v, want true", m.RequireTLS)
	}
	if !m.Port.IsNull() || !m.ManagementPort.IsNull() {
		t.Errorf("port / management_port must be null when TLS is required, got %v / %v", m.Port, m.ManagementPort)
	}
	if m.AMQPSPort.ValueInt64() != 5671 {
		t.Errorf("amqps_port = %v, want 5671", m.AMQPSPort)
	}

	// The server's own shape: ports omitted.
	m = MessagingInstanceModel{}
	m.fromAPI(context.Background(), &apiMessagingInstance{ID: "mq-1", RequireTLS: true, AMQPSPort: 5671}, &diags)
	if !m.Port.IsNull() || !m.ManagementPort.IsNull() {
		t.Errorf("omitted ports must read null, got %v / %v", m.Port, m.ManagementPort)
	}

	// No requirement (and an older service that never sends the field): false, ports kept.
	m = MessagingInstanceModel{}
	m.fromAPI(context.Background(), &apiMessagingInstance{ID: "mq-1", Port: 5672, ManagementPort: 15672}, &diags)
	if m.RequireTLS.IsNull() || m.RequireTLS.ValueBool() {
		t.Errorf("require_tls = %v, want false", m.RequireTLS)
	}
	if m.Port.ValueInt64() != 5672 || m.ManagementPort.ValueInt64() != 15672 {
		t.Errorf("plaintext ports lost: %v / %v", m.Port, m.ManagementPort)
	}
	if diags.HasError() {
		t.Fatalf("fromAPI: %v", diags)
	}
}

func TestToCreateRequest_RequireTLS(t *testing.T) {
	m := MessagingInstanceModel{RequireTLS: types.BoolNull()}
	if req := m.toCreateRequest(context.Background(), nil); req.RequireTLS != nil {
		t.Errorf("an omitted require_tls must not be sent (the server defaults it), got %v", *req.RequireTLS)
	}
	b, _ := json.Marshal(m.toCreateRequest(context.Background(), nil))
	if strings.Contains(string(b), "requireTls") {
		t.Errorf("omitted require_tls on the wire: %s", b)
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
}

// The update request never carries requireTls: it is fixed at create.
func TestToUpdateRequest_NeverSendsRequireTLS(t *testing.T) {
	plan := MessagingInstanceModel{Name: types.StringValue("b"), RequireTLS: types.BoolValue(false)}
	state := MessagingInstanceModel{Name: types.StringValue("a"), RequireTLS: types.BoolValue(true)}
	b, _ := json.Marshal(plan.toUpdateRequest(&state))
	if strings.Contains(string(b), "requireTls") {
		t.Errorf("update must not send requireTls: %s", b)
	}
}

// Legacy state (null require_tls) planned without refresh, with a configured value the
// instance does not hold: Update refuses before it writes anything.
func TestUpdate_LegacyStateConfiguredMismatchRefusedBeforeAnyWrite(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			body, _ := io.ReadAll(r.Body)
			t.Errorf("no write may happen: %s %s %s", r.Method, r.URL.Path, body)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(apiMessagingInstance{
			ID: "mq-123", Name: "old", Type: "lavinmq", Status: "running", RequireTLS: false, Port: 5672,
		})
	}))
	defer server.Close()
	c := client.NewClient(server.URL, "test-key", client.WithHTTPClient(server.Client())) // pragma: allowlist secret
	c.SetTenantIDForTest("t-1")
	r := newTestMessagingResource(c)

	base := MessagingInstanceModel{
		ID: types.StringValue("mq-123"), Name: types.StringValue("old"), Type: types.StringValue("lavinmq"),
		Version: types.StringValue("2.3"), FlavorID: types.StringValue("mq.gp1.small"),
		VPCID: types.StringValue("vpc-1"), SubnetID: types.StringValue("sn-1"),
		PersistenceMode: types.StringValue("persistent"), Status: types.StringValue("running"),
	}
	state := buildMessagingInstanceState(t, base)
	planModel := base
	planModel.Name = types.StringValue("new")
	planModel.RequireTLS = types.BoolValue(true)
	plan := buildMessagingInstancePlan(t, planModel)

	resp := resource.UpdateResponse{State: state}
	r.Update(context.Background(), resource.UpdateRequest{Plan: plan, State: state}, &resp)
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
// read-back fills require_tls.
func TestUpdate_LegacyStateConfiguredMatchProceeds(t *testing.T) {
	puts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut {
			puts++
			body, _ := io.ReadAll(r.Body)
			if strings.Contains(string(body), "requireTls") {
				t.Errorf("update must not send requireTls: %s", body)
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(apiMessagingInstance{
			ID: "mq-123", Name: "new", Type: "lavinmq", TypeVersion: "2.3", FlavorID: "mq.gp1.small",
			VPCID: "vpc-1", SubnetID: "sn-1", PersistenceMode: "persistent", Status: "running",
			RequireTLS: true, TLSEnabled: true, AMQPSPort: 5671, CreatedAt: "2025-01-01T00:00:00Z",
		})
	}))
	defer server.Close()
	c := client.NewClient(server.URL, "test-key", client.WithHTTPClient(server.Client())) // pragma: allowlist secret
	c.SetTenantIDForTest("t-1")
	r := newTestMessagingResource(c)

	base := MessagingInstanceModel{
		ID: types.StringValue("mq-123"), Name: types.StringValue("old"), Type: types.StringValue("lavinmq"),
		Version: types.StringValue("2.3"), FlavorID: types.StringValue("mq.gp1.small"),
		VPCID: types.StringValue("vpc-1"), SubnetID: types.StringValue("sn-1"),
		PersistenceMode: types.StringValue("persistent"), Status: types.StringValue("running"),
	}
	state := buildMessagingInstanceState(t, base)
	planModel := base
	planModel.Name = types.StringValue("new")
	planModel.RequireTLS = types.BoolValue(true)
	plan := buildMessagingInstancePlan(t, planModel)

	resp := resource.UpdateResponse{State: state}
	r.Update(context.Background(), resource.UpdateRequest{Plan: plan, State: state}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("update: %v", resp.Diagnostics)
	}
	if puts != 1 {
		t.Errorf("PUTs = %d, want 1 (the rename)", puts)
	}
	var got MessagingInstanceModel
	resp.State.Get(context.Background(), &got)
	if !got.RequireTLS.ValueBool() || !got.Port.IsNull() || !got.ManagementPort.IsNull() {
		t.Errorf("read-back: require_tls=%v port=%v management_port=%v", got.RequireTLS, got.Port, got.ManagementPort)
	}
}
