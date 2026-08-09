package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
)

// --- helpers ---

func gwSchema(t *testing.T) schema.Schema {
	t.Helper()
	r := NewResource()
	resp := &resource.SchemaResponse{}
	r.Schema(context.Background(), resource.SchemaRequest{}, resp)
	return resp.Schema
}

func gwObjectType() tftypes.Object {
	return tftypes.Object{
		AttributeTypes: map[string]tftypes.Type{
			"id":                            tftypes.String,
			"vpc_id":                        tftypes.String,
			"mode":                          tftypes.String,
			"source_address":                tftypes.String,
			"status":                        tftypes.String,
			"origin":                        tftypes.String,
			"public_ip_id":                  tftypes.String,
			"acknowledge_connectivity_loss": tftypes.Bool,
		},
	}
}

// gwValue builds a whole-resource value. A nil string is a null attribute.
//
// publicIPID is variadic so the many call sites that predate `public_ip_id`
// keep reading as they did; omitted, the attribute is null, which is the state
// of every gateway the platform addressed on the tenant's behalf.
func gwValue(id, vpcID, mode, sourceAddress, status, origin string, ack *bool, publicIPID ...string) tftypes.Value {
	str := func(s string) tftypes.Value {
		if s == "" {
			return tftypes.NewValue(tftypes.String, nil)
		}
		return tftypes.NewValue(tftypes.String, s)
	}
	ackVal := tftypes.NewValue(tftypes.Bool, nil)
	if ack != nil {
		ackVal = tftypes.NewValue(tftypes.Bool, *ack)
	}
	pin := ""
	if len(publicIPID) > 0 {
		pin = publicIPID[0]
	}
	return tftypes.NewValue(gwObjectType(), map[string]tftypes.Value{
		"id":                            str(id),
		"vpc_id":                        str(vpcID),
		"mode":                          str(mode),
		"source_address":                str(sourceAddress),
		"status":                        str(status),
		"origin":                        str(origin),
		"public_ip_id":                  str(pin),
		"acknowledge_connectivity_loss": ackVal,
	})
}

func boolPtr(b bool) *bool { return &b }

// gwPlanUnknownComputed is the shape Terraform really hands Update: every
// Computed attribute whose config is null is marked UNKNOWN
// (fwserver.MarkComputedNilsAsUnknown). A code path that returns without
// resolving them leaves unknowns in state, which the framework rejects.
//
// public_ip_id defaults to unknown for the same reason — it is Computed too —
// and a variadic argument sets it where the practitioner named an address.
func gwPlanUnknownComputed(vpcID, mode string, ack *bool, publicIPID ...string) tftypes.Value {
	unknown := tftypes.NewValue(tftypes.String, tftypes.UnknownValue)
	ackVal := tftypes.NewValue(tftypes.Bool, nil)
	if ack != nil {
		ackVal = tftypes.NewValue(tftypes.Bool, *ack)
	}
	pin := unknown
	if len(publicIPID) > 0 {
		pin = tftypes.NewValue(tftypes.String, publicIPID[0])
	}
	return tftypes.NewValue(gwObjectType(), map[string]tftypes.Value{
		"id":                            unknown,
		"vpc_id":                        tftypes.NewValue(tftypes.String, vpcID),
		"mode":                          tftypes.NewValue(tftypes.String, mode),
		"source_address":                unknown,
		"status":                        unknown,
		"origin":                        unknown,
		"public_ip_id":                  pin,
		"acknowledge_connectivity_loss": ackVal,
	})
}

func configuredResource(t *testing.T, serverURL string) resource.Resource {
	t.Helper()
	c := client.NewClient(serverURL, "test-key") // pragma: allowlist secret
	c.SetTenantIDForTest("t-123")
	r := NewResource()
	r.(resource.ResourceWithConfigure).Configure(
		context.Background(),
		resource.ConfigureRequest{ProviderData: c},
		&resource.ConfigureResponse{},
	)
	return r
}

// apiErrorHandler serves one error envelope for every request, and records how
// many requests it saw.
func apiErrorHandler(calls *int, status int, code, message string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		*calls++
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"error": map[string]string{"code": code, "message": message},
		})
	}
}

// diagText flattens the error diagnostics so an assertion can match on what the
// practitioner actually reads.
func diagText(diags diag.Diagnostics) string {
	var b strings.Builder
	for _, d := range diags.Errors() {
		b.WriteString(d.Summary())
		b.WriteString("\n")
		b.WriteString(d.Detail())
		b.WriteString("\n")
	}
	return b.String()
}

// --- schema ---

func TestEgressGatewayMetadata(t *testing.T) {
	r := NewResource()
	resp := &resource.MetadataResponse{}
	r.Metadata(context.Background(), resource.MetadataRequest{ProviderTypeName: "frostmoln"}, resp)
	if resp.TypeName != "frostmoln_gateway" {
		t.Errorf("unexpected type name %s", resp.TypeName)
	}
}

func TestEgressGatewayConfigureNilProviderData(t *testing.T) {
	r := NewResource()
	resp := &resource.ConfigureResponse{}
	r.(resource.ResourceWithConfigure).Configure(context.Background(), resource.ConfigureRequest{ProviderData: nil}, resp)
	if resp.Diagnostics.HasError() {
		t.Errorf("expected no errors, got %v", resp.Diagnostics)
	}
}

func TestEgressGatewayConfigureWrongType(t *testing.T) {
	r := NewResource()
	resp := &resource.ConfigureResponse{}
	r.(resource.ResourceWithConfigure).Configure(context.Background(), resource.ConfigureRequest{ProviderData: true}, resp)
	if !resp.Diagnostics.HasError() {
		t.Error("expected error for wrong provider data type")
	}
}

// validateMode runs every validator on the `mode` attribute over one value, the
// way the framework does.
func validateMode(t *testing.T, value types.String) diag.Diagnostics {
	t.Helper()
	attr, ok := gwSchema(t).Attributes["mode"].(schema.StringAttribute)
	if !ok {
		t.Fatalf("expected mode to be a StringAttribute, got %T", gwSchema(t).Attributes["mode"])
	}
	if len(attr.Validators) == 0 {
		t.Fatal("mode has no validators")
	}
	resp := &validator.StringResponse{}
	for _, v := range attr.Validators {
		v.ValidateString(context.Background(), validator.StringRequest{
			Path:        path.Root("mode"),
			ConfigValue: value,
		}, resp)
	}
	return resp.Diagnostics
}

// TestEgressGatewayModeOffersOnlyPublicIP pins the enum a CONFIGURATION may
// set. `nat` is withdrawn: it egressed through an address shared with other
// VPCs and could not coexist with public IPs on the VPC's own instances, so the
// provider must stop offering it — a client that still accepts it sends a
// request the platform refuses, and does so after the practitioner has written
// and reviewed a plan.
func TestEgressGatewayModeOffersOnlyPublicIP(t *testing.T) {
	for _, tc := range []struct {
		value string
		valid bool
	}{
		{ModePublicIP, true},
		{ModeNAT, false},  // WITHDRAWN
		{"none", false},   // "none" is the ABSENCE of the resource, not a mode
		{"shared", false}, // never a mode at all
	} {
		diags := validateMode(t, types.StringValue(tc.value))
		if got := !diags.HasError(); got != tc.valid {
			t.Errorf("mode %q: accepted=%v, want %v (%v)", tc.value, got, tc.valid, diags)
		}
	}
}

// TestEgressGatewayModeNATRefusalExplainsTheWithdrawal: `nat` is not a typo. It
// was a documented, applied mode, so a practitioner meeting this refusal has a
// configuration that worked yesterday. A bare "value must be one of" reads as
// "you made that up" and leaves them with no idea what to write instead, or
// whether the gateway they already have is now broken — so the diagnostic has
// to say the mode is withdrawn, name what replaces it, and say that an existing
// NAT gateway still reads.
func TestEgressGatewayModeNATRefusalExplainsTheWithdrawal(t *testing.T) {
	diags := validateMode(t, types.StringValue(ModeNAT))
	if !diags.HasError() {
		t.Fatal("mode = \"nat\" must be refused: the mode has been withdrawn")
	}
	text := diagText(diags)
	for _, want := range []string{"withdrawn", ModePublicIP, "import"} {
		if !strings.Contains(text, want) {
			t.Errorf("the withdrawal refusal must mention %q, got:\n%s", want, text)
		}
	}
	// The old validator was stringvalidator.OneOf, whose message is exactly this
	// and says nothing a practitioner can act on.
	if strings.Contains(text, "value must be one of") {
		t.Errorf("the refusal must not be the generic OneOf message:\n%s", text)
	}
}

// TestEgressGatewayModeValidatorIgnoresNullAndUnknown: an attribute validator
// that errored on either would break every configuration whose `mode` comes
// from a module output or another resource's attribute — a value the provider
// is handed as unknown at validate time and that is perfectly valid once
// resolved.
func TestEgressGatewayModeValidatorIgnoresNullAndUnknown(t *testing.T) {
	for name, value := range map[string]types.String{
		"null":    types.StringNull(),
		"unknown": types.StringUnknown(),
	} {
		if diags := validateMode(t, value); diags.HasError() {
			t.Errorf("a %s mode must not be judged at validate time: %v", name, diags)
		}
	}
}

// TestEgressGatewayModeDoesNotRequireReplace is behavioural, not a %T match on
// the modifier list: a replacement would DESTROY the gateway and create a new
// one, dropping the recorded source address and leaving the VPC with no
// internet, no DNS and no managed-service connectivity in between. The API has
// PATCH /gateways/{id} precisely so that never happens.
func TestEgressGatewayModeDoesNotRequireReplace(t *testing.T) {
	s := gwSchema(t)
	attr, ok := s.Attributes["mode"].(schema.StringAttribute)
	if !ok {
		t.Fatalf("expected mode to be a StringAttribute, got %T", s.Attributes["mode"])
	}

	req := planmodifier.StringRequest{
		Path:        path.Root("mode"),
		StateValue:  types.StringValue(ModePublicIP),
		PlanValue:   types.StringValue(ModeNAT),
		ConfigValue: types.StringValue(ModeNAT),
	}
	resp := &planmodifier.StringResponse{PlanValue: req.PlanValue}
	for _, m := range attr.PlanModifiers {
		m.PlanModifyString(context.Background(), req, resp)
	}
	if resp.RequiresReplace {
		t.Error("a mode change must be an in-place update, not a replacement")
	}
}

func TestEgressGatewaySchemaAttributes(t *testing.T) {
	s := gwSchema(t)
	for _, name := range []string{"id", "vpc_id", "mode", "source_address", "status", "origin", "public_ip_id", "acknowledge_connectivity_loss"} {
		if _, ok := s.Attributes[name]; !ok {
			t.Errorf("expected attribute %q in schema", name)
		}
	}
	ack, ok := s.Attributes["acknowledge_connectivity_loss"].(schema.BoolAttribute)
	if !ok {
		t.Fatalf("expected acknowledge_connectivity_loss to be a BoolAttribute, got %T", s.Attributes["acknowledge_connectivity_loss"])
	}
	if !ack.Optional || ack.Required || ack.Computed {
		t.Error("acknowledge_connectivity_loss must be Optional-only: a Computed default would let the provider acknowledge on the practitioner's behalf")
	}
	// The consequence is not only the internet. Every surface that offers
	// removal must say so.
	if !strings.Contains(ack.Description, "DNS") || !strings.Contains(s.Description, "DNS") {
		t.Error("the schema must state that removal also costs DNS resolution")
	}
}

// TestEgressGatewayStatusDescriptionIsNotAVerdict: `status` is OBSERVED, and
// what the platform observes depends on how the mode is realised — a healthy
// gateway can report "detached". The description must not tell a practitioner
// that "detached" proves the platform and the cloud disagree, or that an
// operator is needed, because acting on that costs a support ticket and, worse,
// an unnecessary mode change (which re-addresses egress and drops connections).
func TestEgressGatewayStatusDescriptionIsNotAVerdict(t *testing.T) {
	s := gwSchema(t)
	attr, ok := s.Attributes["status"].(schema.StringAttribute)
	if !ok {
		t.Fatalf("expected status to be a StringAttribute, got %T", s.Attributes["status"])
	}
	lower := strings.ToLower(attr.Description)
	for _, verdict := range []string{
		"needs an operator",
		"the platform's record and the cloud disagree",
		"cannot converge",
	} {
		if strings.Contains(lower, verdict) {
			t.Errorf("status must not assert that \"detached\" is drift needing intervention (%q), got:\n%s",
				verdict, attr.Description)
		}
	}
	if !strings.Contains(lower, "detached") {
		t.Error("status must still document the \"detached\" value")
	}
}

// TestEgressGatewayVPCIDDescriptionNamesTheAcknowledgement: changing vpc_id
// REPLACES the resource, and the replacement destroys the gateway first — which
// this provider refuses without `acknowledge_connectivity_loss`. A description
// that says only "replaces the resource" sends the practitioner into a failed
// apply.
func TestEgressGatewayVPCIDDescriptionNamesTheAcknowledgement(t *testing.T) {
	s := gwSchema(t)
	attr, ok := s.Attributes["vpc_id"].(schema.StringAttribute)
	if !ok {
		t.Fatalf("expected vpc_id to be a StringAttribute, got %T", s.Attributes["vpc_id"])
	}
	if !strings.Contains(attr.Description, "acknowledge_connectivity_loss") {
		t.Errorf("vpc_id must say the replacement's destroy needs the acknowledgement, got:\n%s", attr.Description)
	}
}

// TestEgressGatewaySurfacesCarryNoInternalNames: every string in this package
// that a practitioner reads ends up on the Terraform Registry, permanently.
// Internal component names ("NAT shard", "managed-agent") publish platform
// topology to the whole internet and mean nothing to the reader; the fm CLI and
// the portal say "not offered in this region yet" and "managed-service
// connectivity", and this surface matches them.
func TestEgressGatewaySurfacesCarryNoInternalNames(t *testing.T) {
	forbidden := []string{"shard", "managed-agent", "neutron", "ovn", "openstack"}

	surfaces := map[string]string{"resource description": gwSchema(t).Description}
	for name, attr := range gwSchema(t).Attributes {
		surfaces["attribute "+name] = attr.GetDescription()
	}
	for _, code := range []string{
		errCodeModeUnavailable, errCodeGatewayExists, errCodeGatewayInUse,
		errCodeLossNotAcked, errCodePoolExhausted,
		errCodePublicIPUnavailable, errCodePublicIPNotAllowed,
	} {
		var diags diag.Diagnostics
		addGatewayError(&diags, "fallback", &client.APIError{Code: code, Message: "api message", StatusCode: 400})
		surfaces["diagnostic "+code] = diagText(diags)
	}

	for where, text := range surfaces {
		lower := strings.ToLower(text)
		for _, term := range forbidden {
			if strings.Contains(lower, term) {
				t.Errorf("%s names the internal component %q on a customer surface:\n%s", where, term, text)
			}
		}
	}
}

// --- plan modification ---

// modifyPlan runs the resource's ModifyPlan the way the framework does: the
// response plan starts as a copy of the proposed plan, and the method rewrites
// what it needs to.
// configRaw defaults to a configuration with nothing set. ModifyPlan reads only
// `public_ip_id` from the config — it is the one attribute whose planned value
// depends on whether the practitioner wrote it — so an all-null config is the
// "practitioner named no address" case every pre-existing test is in.
func modifyPlan(t *testing.T, planRaw, stateRaw tftypes.Value, configRaw ...tftypes.Value) GatewayModel {
	t.Helper()
	s := gwSchema(t)
	r, ok := NewResource().(resource.ResourceWithModifyPlan)
	if !ok {
		t.Fatal("the resource must implement ModifyPlan: without it Terraform pins the PRIOR values of " +
			"source_address, status and origin into the plan, and every mode change fails the apply " +
			"with \"Provider produced inconsistent result after apply\"")
	}

	cfg := gwValue("", "", "", "", "", "", nil)
	if len(configRaw) > 0 {
		cfg = configRaw[0]
	}

	resp := &resource.ModifyPlanResponse{Plan: tfsdk.Plan{Schema: s, Raw: planRaw}}
	r.ModifyPlan(context.Background(), resource.ModifyPlanRequest{
		Plan:   tfsdk.Plan{Schema: s, Raw: planRaw},
		State:  tfsdk.State{Schema: s, Raw: stateRaw},
		Config: tfsdk.Config{Schema: s, Raw: cfg},
	}, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected plan diagnostics: %v", resp.Diagnostics)
	}

	var planned GatewayModel
	resp.Plan.Get(context.Background(), &planned)
	return planned
}

// TestEgressGatewayModifyPlanModeChangeRecomputesObservedValues is the
// regression guard for an apply that CANNOT succeed without it.
//
// For a Computed-only attribute Terraform's proposed new state carries the
// PRIOR STATE value (the framework rewrites only null -> unknown), so the plan
// pins the old source address, status and origin as KNOWN. The PATCH then
// legitimately returns different ones — the platform re-records the source
// address on every mode change, sets origin to "explicit" (so an imported
// "vpc_create" or "legacy" gateway changes), and reports the gateway active —
// and Terraform core aborts with "Provider produced inconsistent result after
// apply". No retry clears it; the practitioner is stuck.
func TestEgressGatewayModifyPlanModeChangeRecomputesObservedValues(t *testing.T) {
	// Exactly what the framework proposes: prior values, new mode.
	planRaw := gwValue("gw-1", "vpc-1", ModeNAT, "46.246.117.231", "active", "vpc_create", boolPtr(true))
	stateRaw := gwValue("gw-1", "vpc-1", ModePublicIP, "46.246.117.231", "active", "vpc_create", boolPtr(true))

	planned := modifyPlan(t, planRaw, stateRaw)

	for name, v := range map[string]types.String{
		"source_address": planned.SourceAddress,
		"status":         planned.Status,
		"origin":         planned.Origin,
	} {
		if !v.IsUnknown() {
			t.Errorf("%s must be planned as unknown across a mode change (the platform re-records it); got %v", name, v)
		}
	}
	// The id survives a mode change — that is why this is a PATCH and not a
	// replacement — so it must NOT be thrown away.
	if planned.ID.ValueString() != "gw-1" {
		t.Errorf("the gateway id survives a mode change, got %v", planned.ID)
	}
}

// TestEgressGatewayModifyPlanReplacementRecomputesObservedValues: a vpc_id
// change replaces the resource, so nothing observed survives it either.
func TestEgressGatewayModifyPlanReplacementRecomputesObservedValues(t *testing.T) {
	planRaw := gwValue("gw-1", "vpc-2", ModePublicIP, "46.246.117.231", "active", "explicit", boolPtr(true))
	stateRaw := gwValue("gw-1", "vpc-1", ModePublicIP, "46.246.117.231", "active", "explicit", boolPtr(true))

	planned := modifyPlan(t, planRaw, stateRaw)

	if !planned.SourceAddress.IsUnknown() {
		t.Errorf("a replacement gets a new address, so source_address must be unknown; got %v", planned.SourceAddress)
	}
}

// TestEgressGatewayModifyPlanKeepsNullSourceAddress is the perpetual-diff
// guard. A Computed attribute that is NULL in state is marked unknown in the
// proposed plan, and null-versus-unknown is a diff on every single run: `terraform
// plan -detailed-exitcode` returns 2 forever and any drift-detection gate built
// on it is broken. Reachable whenever the gateway is detached or its external
// port carries no IPv4.
func TestEgressGatewayModifyPlanKeepsNullSourceAddress(t *testing.T) {
	// What the framework proposes when state.source_address is null: unknown.
	planRaw := gwPlanUnknownComputed("vpc-1", ModeNAT, nil)
	stateRaw := gwValue("gw-1", "vpc-1", ModeNAT, "", "detached", "explicit", nil)

	planned := modifyPlan(t, planRaw, stateRaw)

	if !planned.SourceAddress.IsNull() {
		t.Errorf("with mode unchanged, a null source_address must stay null in the plan (an unknown is a "+
			"diff on every run); got %v", planned.SourceAddress)
	}
	if planned.Status.ValueString() != "detached" || planned.Origin.ValueString() != "explicit" {
		t.Errorf("with mode unchanged the observed values must be taken from state, got %v / %v",
			planned.Status, planned.Origin)
	}
}

// --- model ---

// TestApplyToModelAbsentSourceAddressIsNull guards the difference between "the
// address is not known yet" and "the address is the empty string": the API omits
// sourceAddress while a gateway is detached, and "" is a value a practitioner
// could interpolate into a firewall rule.
func TestApplyToModelAbsentSourceAddressIsNull(t *testing.T) {
	var m GatewayModel
	applyToModel(&m, &apiGateway{
		ID: "gw-1", VPCID: "vpc-1", Mode: ModeNAT, Status: "detached", Origin: "legacy",
	})
	if !m.SourceAddress.IsNull() {
		t.Errorf("expected null source_address, got %v", m.SourceAddress)
	}
	if m.Status.ValueString() != "detached" || m.Origin.ValueString() != "legacy" {
		t.Errorf("unexpected observed values: %v / %v", m.Status, m.Origin)
	}
}

func TestApplyToModelPresentSourceAddress(t *testing.T) {
	var m GatewayModel
	applyToModel(&m, &apiGateway{
		ID: "gw-1", VPCID: "vpc-1", Mode: ModePublicIP,
		SourceAddress: "46.246.117.231", Status: "active", Origin: "explicit",
	})
	if m.SourceAddress.ValueString() != "46.246.117.231" {
		t.Errorf("unexpected source_address %v", m.SourceAddress)
	}
}

// --- create ---

func TestEgressGatewayCreate(t *testing.T) {
	var got apiCreateGatewayRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/v1/tenants/t-123/gateways" {
			_ = json.NewDecoder(r.Body).Decode(&got)
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id":"gw-1","vpcId":"vpc-1","tenantId":"t-123","mode":"public_ip",
				"sourceAddress":"46.246.117.231","status":"active","origin":"explicit"}`))
			return
		}
		http.Error(w, "unexpected", http.StatusNotFound)
	}))
	defer server.Close()

	r := configuredResource(t, server.URL)
	s := gwSchema(t)

	resp := &resource.CreateResponse{State: tfsdk.State{Schema: s}}
	r.Create(context.Background(), resource.CreateRequest{
		Plan: tfsdk.Plan{Schema: s, Raw: gwValue("", "vpc-1", ModePublicIP, "", "", "", nil)},
	}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected errors: %v", resp.Diagnostics)
	}
	if got.VPCID != "vpc-1" || got.Mode != ModePublicIP {
		t.Errorf("unexpected create body %+v", got)
	}

	var state GatewayModel
	resp.State.Get(context.Background(), &state)
	if state.ID.ValueString() != "gw-1" {
		t.Errorf("expected id gw-1, got %v", state.ID)
	}
	if state.SourceAddress.ValueString() != "46.246.117.231" {
		t.Errorf("unexpected source_address %v", state.SourceAddress)
	}
}

// TestEgressGatewayCreateModeUnavailable: GATEWAY_MODE_UNAVAILABLE is a 400, so
// passed through raw it reads as "fix your syntax". It is not — it is the
// platform declining to provision a mode. `nat` is WITHDRAWN, permanently, so
// the configuration has to change; the diagnostic must say that and must NOT
// leave a "not available" reading as "not available yet", because there is no
// other mode waiting to ship (the API's own message is now "supported modes:
// public_ip").
//
// This provider refuses `nat` at validate time, so the only way to reach the
// API with it is a `mode` that was unknown then — a module output, another
// resource's attribute — which is exactly the case this mapping exists for.
func TestEgressGatewayCreateModeUnavailable(t *testing.T) {
	calls := 0
	server := httptest.NewServer(apiErrorHandler(&calls, http.StatusBadRequest,
		"GATEWAY_MODE_UNAVAILABLE", `gateway mode "nat" is not available; supported modes: public_ip`))
	defer server.Close()

	r := configuredResource(t, server.URL)
	s := gwSchema(t)

	resp := &resource.CreateResponse{State: tfsdk.State{Schema: s}}
	r.Create(context.Background(), resource.CreateRequest{
		Plan: tfsdk.Plan{Schema: s, Raw: gwValue("", "vpc-1", ModeNAT, "", "", "", nil)},
	}, resp)

	if !resp.Diagnostics.HasError() {
		t.Fatal("expected an error")
	}
	text := strings.ToLower(diagText(resp.Diagnostics))
	if !strings.Contains(text, "withdrawn") {
		t.Errorf("diagnostic must say `nat` is withdrawn, got:\n%s", text)
	}
	if !strings.Contains(text, `mode = "public_ip"`) {
		t.Errorf("diagnostic must name the mode that replaces it, got:\n%s", text)
	}
	// The withdrawal is permanent, so the diagnostic must not offer waiting as a
	// way out. An earlier version told the practitioner any other mode was one
	// "this region has not shipped yet", which read straight back onto `nat`.
	for _, forbidden := range []string{"not shipped yet", "not available yet", "yet to ship"} {
		if strings.Contains(text, forbidden) {
			t.Errorf("diagnostic must not suggest the mode is merely not available YET (%q), got:\n%s",
				forbidden, text)
		}
	}
	if !strings.Contains(text, "not a syntax error") {
		t.Errorf("diagnostic must not let a 400 read as invalid syntax, got:\n%s", text)
	}
}

func TestEgressGatewayCreateGatewayExists(t *testing.T) {
	calls := 0
	server := httptest.NewServer(apiErrorHandler(&calls, http.StatusConflict,
		"GATEWAY_EXISTS", "VPC vpc-1 already has a gateway; change its mode instead of creating another"))
	defer server.Close()

	r := configuredResource(t, server.URL)
	s := gwSchema(t)

	resp := &resource.CreateResponse{State: tfsdk.State{Schema: s}}
	r.Create(context.Background(), resource.CreateRequest{
		Plan: tfsdk.Plan{Schema: s, Raw: gwValue("", "vpc-1", ModePublicIP, "", "", "", nil)},
	}, resp)

	text := diagText(resp.Diagnostics)
	if !strings.Contains(text, "terraform import frostmoln_gateway") {
		t.Errorf("diagnostic must point at importing the existing gateway, got:\n%s", text)
	}
	if !strings.Contains(strings.ToLower(text), "mode") {
		t.Errorf("diagnostic must point at changing the mode, got:\n%s", text)
	}
}

// TestEgressGatewayCreatePoolExhausted pins the one presentation that costs a
// practitioner a pointless support ticket: GATEWAY_POOL_EXHAUSTED is PLATFORM
// inventory, temporary and retryable — not the tenant's quota, and no amount of
// quota granted fixes it.
func TestEgressGatewayCreatePoolExhausted(t *testing.T) {
	calls := 0
	server := httptest.NewServer(apiErrorHandler(&calls, http.StatusServiceUnavailable,
		"GATEWAY_POOL_EXHAUSTED", "no public IPv4 addresses are currently available in this region; this is a platform capacity limit, not your quota"))
	defer server.Close()

	r := configuredResource(t, server.URL)
	s := gwSchema(t)

	resp := &resource.CreateResponse{State: tfsdk.State{Schema: s}}
	r.Create(context.Background(), resource.CreateRequest{
		Plan: tfsdk.Plan{Schema: s, Raw: gwValue("", "vpc-1", ModePublicIP, "", "", "", nil)},
	}, resp)

	text := diagText(resp.Diagnostics)
	lower := strings.ToLower(text)
	if !strings.Contains(lower, "temporary") || !strings.Contains(lower, "re-run `terraform apply`") {
		t.Errorf("diagnostic must present the 503 as retryable, got:\n%s", text)
	}
	for _, forbidden := range []string{"request more quota", "increase your quota", "ask for more quota", "raise your quota"} {
		if strings.Contains(lower, forbidden) {
			t.Errorf("diagnostic must not send the practitioner to ask for quota (%q), got:\n%s", forbidden, text)
		}
	}
	if !strings.Contains(lower, "not your tenant's quota") {
		t.Errorf("diagnostic must say this is not a quota limit, got:\n%s", text)
	}
}

// --- read ---

func TestEgressGatewayReadUsesVPCFilteredList(t *testing.T) {
	var gotQuery string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-123/gateways" {
			gotQuery = r.URL.RawQuery
			_, _ = w.Write([]byte(`{"gateways":[{"id":"gw-1","vpcId":"vpc-1","mode":"public_ip",
				"sourceAddress":"46.246.117.231","status":"active","origin":"vpc_create"}],"totalCount":1}`))
			return
		}
		http.Error(w, "unexpected", http.StatusNotFound)
	}))
	defer server.Close()

	r := configuredResource(t, server.URL)
	s := gwSchema(t)
	raw := gwValue("gw-1", "vpc-1", ModePublicIP, "46.246.117.231", "active", "vpc_create", boolPtr(true))

	resp := &resource.ReadResponse{State: tfsdk.State{Schema: s, Raw: raw}}
	r.Read(context.Background(), resource.ReadRequest{State: tfsdk.State{Schema: s, Raw: raw}}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected errors: %v", resp.Diagnostics)
	}
	if gotQuery != "vpcId=vpc-1" {
		t.Errorf("expected the read to be narrowed to the VPC, got %q", gotQuery)
	}

	var state GatewayModel
	resp.State.Get(context.Background(), &state)
	if state.Origin.ValueString() != "vpc_create" {
		t.Errorf("unexpected origin %v", state.Origin)
	}
	// The acknowledgement is configuration, never returned by the API — a read
	// must not clear it, or the next destroy would refuse.
	if !state.AcknowledgeConnectivityLoss.ValueBool() {
		t.Error("read must preserve acknowledge_connectivity_loss")
	}
}

// TestEgressGatewayReadKeepsWithdrawnNATMode is the other half of the
// withdrawal, and the half that is easy to get wrong.
//
// `nat` cannot be SET any more, but VPCs are still on it. A read that refused
// the value, or "helpfully" normalised it to public_ip, would be far worse than
// offering the mode: refusing turns every refresh of a live gateway into an
// error the practitioner cannot clear from their configuration, and rewriting
// records a mode the platform is not running — after which the next apply would
// see no drift and never move the VPC, or Terraform would plan a change nobody
// asked for against the VPC's only outbound path.
//
// So: the API's value goes to state verbatim.
func TestEgressGatewayReadKeepsWithdrawnNATMode(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-123/gateways" {
			_, _ = w.Write([]byte(`{"gateways":[{"id":"gw-1","vpcId":"vpc-1","mode":"nat",
				"sourceAddress":"46.246.117.240","status":"active","origin":"explicit"}],"totalCount":1}`))
			return
		}
		http.Error(w, "unexpected", http.StatusNotFound)
	}))
	defer server.Close()

	r := configuredResource(t, server.URL)
	sch := gwSchema(t)
	raw := gwValue("gw-1", "vpc-1", ModeNAT, "46.246.117.240", "active", "explicit", boolPtr(true))

	resp := &resource.ReadResponse{State: tfsdk.State{Schema: sch, Raw: raw}}
	r.Read(context.Background(), resource.ReadRequest{State: tfsdk.State{Schema: sch, Raw: raw}}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("reading a gateway still on the withdrawn mode must not error: %v", resp.Diagnostics)
	}
	if resp.State.Raw.IsNull() {
		t.Fatal("a gateway still on the withdrawn mode must not be dropped from state")
	}

	var state GatewayModel
	resp.State.Get(context.Background(), &state)
	if got := state.Mode.ValueString(); got != ModeNAT {
		t.Errorf("the read must record the mode the platform reports verbatim; got %q, want %q", got, ModeNAT)
	}
	if got := state.SourceAddress.ValueString(); got != "46.246.117.240" {
		t.Errorf("unexpected source_address %q", got)
	}
}

// TestEgressGatewayModifyPlanWithdrawnNATIsStable: a gateway still on `nat`,
// with nothing changed, must plan clean. An unknown planted on any observed
// attribute here is a diff on every single run — `terraform plan
// -detailed-exitcode` returns 2 forever — for a resource whose only remaining
// operations are "leave it alone" and "move it off, deliberately".
func TestEgressGatewayModifyPlanWithdrawnNATIsStable(t *testing.T) {
	planRaw := gwPlanUnknownComputed("vpc-1", ModeNAT, nil)
	stateRaw := gwValue("gw-1", "vpc-1", ModeNAT, "46.246.117.240", "active", "explicit", nil)

	planned := modifyPlan(t, planRaw, stateRaw)

	for name, v := range map[string]types.String{
		"source_address": planned.SourceAddress,
		"status":         planned.Status,
		"origin":         planned.Origin,
		"public_ip_id":   planned.PublicIPID,
	} {
		if v.IsUnknown() {
			t.Errorf("%s must keep its recorded value on an unchanged NAT gateway; an unknown is a diff "+
				"on every run", name)
		}
	}
	if planned.SourceAddress.ValueString() != "46.246.117.240" {
		t.Errorf("unexpected planned source_address %v", planned.SourceAddress)
	}
}

func TestEgressGatewayReadEmptyListRemovesResource(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/tenants/t-123/gateways" {
			_, _ = w.Write([]byte(`{"gateways":[],"totalCount":0}`))
			return
		}
		// The by-id confirmation: this gateway really is gone.
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"error": map[string]string{"code": "NOT_FOUND", "message": "gateway not found"},
		})
	}))
	defer server.Close()

	r := configuredResource(t, server.URL)
	s := gwSchema(t)
	raw := gwValue("gw-1", "vpc-1", ModePublicIP, "", "active", "explicit", nil)

	resp := &resource.ReadResponse{State: tfsdk.State{Schema: s, Raw: raw}}
	r.Read(context.Background(), resource.ReadRequest{State: tfsdk.State{Schema: s, Raw: raw}}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected errors: %v", resp.Diagnostics)
	}
	if !resp.State.Raw.IsNull() {
		t.Error("a VPC with no gateway must be removed from state")
	}
}

// TestEgressGatewayReadEmptyListIsConfirmedBeforeRemoval: an empty FILTERED
// list is not proof the gateway is gone. The API returns the same empty body
// for a vpcId that is unknown or that this tenant does not own, so a stale
// vpc_id — or a key scoped to another tenant — would otherwise drop a live,
// address-spending gateway out of state, and the next apply would create a
// SECOND one for the same VPC (or fail with GATEWAY_EXISTS).
func TestEgressGatewayReadEmptyListIsConfirmedBeforeRemoval(t *testing.T) {
	var byIDCalls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/tenants/t-123/gateways" {
			// The filtered list comes back empty — but the gateway is alive.
			_, _ = w.Write([]byte(`{"gateways":[],"totalCount":0}`))
			return
		}
		if r.URL.Path == "/v1/tenants/t-123/gateways/gw-1" {
			byIDCalls++
			_, _ = w.Write([]byte(`{"id":"gw-1","vpcId":"vpc-1","mode":"public_ip",
				"sourceAddress":"46.246.117.231","status":"active","origin":"explicit"}`))
			return
		}
		http.Error(w, "unexpected", http.StatusNotFound)
	}))
	defer server.Close()

	r := configuredResource(t, server.URL)
	s := gwSchema(t)
	raw := gwValue("gw-1", "vpc-1", ModePublicIP, "46.246.117.231", "active", "explicit", nil)

	resp := &resource.ReadResponse{State: tfsdk.State{Schema: s, Raw: raw}}
	r.Read(context.Background(), resource.ReadRequest{State: tfsdk.State{Schema: s, Raw: raw}}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected errors: %v", resp.Diagnostics)
	}
	if byIDCalls != 1 {
		t.Errorf("an empty filtered list must be confirmed against GET /gateways/{id}; saw %d such calls", byIDCalls)
	}
	if resp.State.Raw.IsNull() {
		t.Fatal("a live gateway was dropped from state on the strength of an empty filtered list")
	}

	var state GatewayModel
	resp.State.Get(context.Background(), &state)
	if state.SourceAddress.ValueString() != "46.246.117.231" {
		t.Errorf("the confirmed gateway must be written to state, got %+v", state)
	}
}

// TestEgressGatewayReadRefusesAnotherVPCsGateway: state binds this resource to
// one gateway of one VPC. A response naming a different VPC means the lookup
// resolved something else (a stale vpc_id, a key scoped elsewhere); rebinding
// state to it would put the NEXT mode change or destroy on another VPC's
// internet, DNS and managed-service path.
func TestEgressGatewayReadRefusesAnotherVPCsGateway(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"gateways":[{"id":"gw-other","vpcId":"vpc-other","mode":"public_ip",
			"sourceAddress":"185.9.9.9","status":"active","origin":"explicit"}],"totalCount":1}`))
	}))
	defer server.Close()

	r := configuredResource(t, server.URL)
	s := gwSchema(t)
	raw := gwValue("gw-1", "vpc-1", ModePublicIP, "46.246.117.231", "active", "explicit", nil)

	resp := &resource.ReadResponse{State: tfsdk.State{Schema: s, Raw: raw}}
	r.Read(context.Background(), resource.ReadRequest{State: tfsdk.State{Schema: s, Raw: raw}}, resp)

	if !resp.Diagnostics.HasError() {
		t.Fatal("expected the read to refuse a response naming a different gateway/VPC")
	}
	text := diagText(resp.Diagnostics)
	for _, id := range []string{"vpc-1", "vpc-other"} {
		if !strings.Contains(text, id) {
			t.Errorf("the diagnostic must name both ids (missing %q), got:\n%s", id, text)
		}
	}

	var state GatewayModel
	resp.State.Get(context.Background(), &state)
	if state.VPCID.ValueString() != "vpc-1" || state.ID.ValueString() != "gw-1" {
		t.Errorf("state must not be rebound to the other gateway, got %+v", state)
	}
}

// TestEgressGatewayReadRefusesResponseWithoutID: an id is what every later
// request is addressed by, and "/gateways/" with an empty id is cleaned
// straight back to the COLLECTION path — so a DELETE built from an id-less
// state row 404s, IsNotFound reads that as "already gone", and the provider
// forgets a gateway that is still up and still spending an address.
func TestEgressGatewayReadRefusesResponseWithoutID(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"gateways":[{"vpcId":"vpc-1","mode":"nat","status":"active",
			"origin":"explicit"}],"totalCount":1}`))
	}))
	defer server.Close()

	r := configuredResource(t, server.URL)
	s := gwSchema(t)
	raw := gwValue("gw-1", "vpc-1", ModeNAT, "", "active", "explicit", nil)

	resp := &resource.ReadResponse{State: tfsdk.State{Schema: s, Raw: raw}}
	r.Read(context.Background(), resource.ReadRequest{State: tfsdk.State{Schema: s, Raw: raw}}, resp)

	if !resp.Diagnostics.HasError() {
		t.Fatal("expected a response with no id to be refused, not written to state")
	}
	if !strings.Contains(diagText(resp.Diagnostics), "`id`") {
		t.Errorf("the diagnostic must name the missing field, got:\n%s", diagText(resp.Diagnostics))
	}
}

// TestEgressGatewayReadNeverSendsEmptyVPCID is the regression guard for the
// worst failure this resource can have. `?vpcId=` present-but-empty is a 400 by
// design; if a state row with no vpc_id (a fresh import by gateway id) made the
// provider send the parameter empty — or, worse, drop it — the response would be
// the TENANT-WIDE list, the resource would bind to element [0] (an unrelated
// VPC's gateway) and the next apply would PATCH or DELETE that VPC's internet,
// DNS and managed-service path.
func TestEgressGatewayReadNeverSendsEmptyVPCID(t *testing.T) {
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path+"?"+r.URL.RawQuery)
		if r.URL.Path == "/v1/tenants/t-123/gateways" {
			// The tenant-wide list: a DIFFERENT VPC's gateway comes first.
			_, _ = w.Write([]byte(`{"gateways":[{"id":"gw-other","vpcId":"vpc-other",
				"mode":"public_ip","status":"active","origin":"explicit"}],"totalCount":1}`))
			return
		}
		if r.URL.Path == "/v1/tenants/t-123/gateways/gw-1" {
			_, _ = w.Write([]byte(`{"id":"gw-1","vpcId":"vpc-1","mode":"nat","status":"active","origin":"explicit"}`))
			return
		}
		http.Error(w, "unexpected", http.StatusNotFound)
	}))
	defer server.Close()

	r := configuredResource(t, server.URL)
	s := gwSchema(t)
	// Exactly the shape ImportState leaves behind: an id, no vpc_id.
	raw := gwValue("gw-1", "", "", "", "", "", nil)

	resp := &resource.ReadResponse{State: tfsdk.State{Schema: s, Raw: raw}}
	r.Read(context.Background(), resource.ReadRequest{State: tfsdk.State{Schema: s, Raw: raw}}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected errors: %v", resp.Diagnostics)
	}
	for _, p := range paths {
		if strings.HasPrefix(p, "/v1/tenants/t-123/gateways?") {
			t.Fatalf("the collection was queried without a vpcId filter (%q): that returns the tenant-wide list", p)
		}
		if strings.Contains(p, "vpcId=&") || strings.HasSuffix(p, "vpcId=") {
			t.Fatalf("an empty vpcId was sent (%q)", p)
		}
	}

	var state GatewayModel
	resp.State.Get(context.Background(), &state)
	if state.VPCID.ValueString() != "vpc-1" {
		t.Errorf("expected the by-id lookup to bind vpc-1, got %v", state.VPCID)
	}
}

func TestEgressGatewayReadByIDNotFoundRemovesResource(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"error": map[string]string{"code": "NOT_FOUND", "message": "gateway not found"},
		})
	}))
	defer server.Close()

	r := configuredResource(t, server.URL)
	s := gwSchema(t)
	raw := gwValue("gw-1", "", "", "", "", "", nil)

	resp := &resource.ReadResponse{State: tfsdk.State{Schema: s, Raw: raw}}
	r.Read(context.Background(), resource.ReadRequest{State: tfsdk.State{Schema: s, Raw: raw}}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected errors: %v", resp.Diagnostics)
	}
	if !resp.State.Raw.IsNull() {
		t.Error("a 404 on the by-id lookup must remove the resource from state")
	}
}

// --- update ---

func TestEgressGatewayUpdateModeRequiresAcknowledgement(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		http.Error(w, "the provider must not reach the API", http.StatusInternalServerError)
	}))
	defer server.Close()

	r := configuredResource(t, server.URL)
	s := gwSchema(t)
	state := gwValue("gw-1", "vpc-1", ModePublicIP, "46.246.117.231", "active", "explicit", nil)
	plan := gwValue("gw-1", "vpc-1", ModeNAT, "46.246.117.231", "active", "explicit", nil)

	resp := &resource.UpdateResponse{State: tfsdk.State{Schema: s, Raw: state}}
	r.Update(context.Background(), resource.UpdateRequest{
		Plan:  tfsdk.Plan{Schema: s, Raw: plan},
		State: tfsdk.State{Schema: s, Raw: state},
	}, resp)

	if !resp.Diagnostics.HasError() {
		t.Fatal("expected the mode change to be refused without an acknowledgement")
	}
	if calls != 0 {
		t.Errorf("the provider must refuse locally, before any request; saw %d", calls)
	}
	text := diagText(resp.Diagnostics)
	if !strings.Contains(text, "acknowledge_connectivity_loss") || !strings.Contains(text, "DNS") {
		t.Errorf("diagnostic must name the attribute and the DNS consequence, got:\n%s", text)
	}
}

// TestEgressGatewayUpdateModeInPlace feeds Update the plan a mode change really
// produces: ModifyPlan has already marked source_address, status and origin
// UNKNOWN, because the PATCH re-records all three. (Feeding it the old address
// as a known planned value would encode the bug this resource used to have —
// Terraform core aborts such an apply with "Provider produced inconsistent
// result after apply".)
//
// The direction is the only mode change left: OFF the withdrawn `nat` and onto
// `public_ip`. It must stay an in-place PATCH, because the alternative — a
// destroy and a create — is an outage on the VPC's only outbound path, and
// every VPC still on `nat` has to make this exact move.
func TestEgressGatewayUpdateModeInPlace(t *testing.T) {
	var gotMethod, gotPath string
	var gotBody apiUpdateGatewayRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		_, _ = w.Write([]byte(`{"id":"gw-1","vpcId":"vpc-1","mode":"public_ip","sourceAddress":"185.1.2.3",
			"status":"active","origin":"explicit"}`))
	}))
	defer server.Close()

	r := configuredResource(t, server.URL)
	s := gwSchema(t)
	state := gwValue("gw-1", "vpc-1", ModeNAT, "46.246.117.231", "active", "explicit", boolPtr(true))
	plan := gwPlanUnknownComputed("vpc-1", ModePublicIP, boolPtr(true))

	resp := &resource.UpdateResponse{State: tfsdk.State{Schema: s, Raw: state}}
	r.Update(context.Background(), resource.UpdateRequest{
		Plan:  tfsdk.Plan{Schema: s, Raw: plan},
		State: tfsdk.State{Schema: s, Raw: state},
	}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected errors: %v", resp.Diagnostics)
	}
	if gotMethod != http.MethodPatch {
		t.Errorf("a mode change must be a PATCH, got %s", gotMethod)
	}
	if gotPath != "/v1/tenants/t-123/gateways/gw-1" {
		t.Errorf("the PATCH must target the gateway id, got %s", gotPath)
	}
	if gotBody.Mode != ModePublicIP || !gotBody.AcknowledgeConnectivityLoss {
		t.Errorf("unexpected PATCH body %+v", gotBody)
	}

	var got GatewayModel
	resp.State.Get(context.Background(), &got)
	if got.SourceAddress.ValueString() != "185.1.2.3" || got.Mode.ValueString() != ModePublicIP {
		t.Errorf("state was not refreshed from the response: %+v", got)
	}
}

// TestEgressGatewayUpdateAckOnlyMakesNoRequest: the acknowledgement is intent
// the API never stores, so setting it must not mutate the gateway — and must
// not leave unknown values in state either.
func TestEgressGatewayUpdateAckOnlyMakesNoRequest(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		http.Error(w, "unexpected", http.StatusInternalServerError)
	}))
	defer server.Close()

	r := configuredResource(t, server.URL)
	s := gwSchema(t)
	state := gwValue("gw-1", "vpc-1", ModePublicIP, "46.246.117.231", "active", "explicit", nil)
	plan := gwPlanUnknownComputed("vpc-1", ModePublicIP, boolPtr(true))

	resp := &resource.UpdateResponse{State: tfsdk.State{Schema: s, Raw: state}}
	r.Update(context.Background(), resource.UpdateRequest{
		Plan:  tfsdk.Plan{Schema: s, Raw: plan},
		State: tfsdk.State{Schema: s, Raw: state},
	}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected errors: %v", resp.Diagnostics)
	}
	if calls != 0 {
		t.Errorf("expected no API call for an acknowledgement-only change, saw %d", calls)
	}

	var got GatewayModel
	resp.State.Get(context.Background(), &got)
	if !got.AcknowledgeConnectivityLoss.ValueBool() {
		t.Error("the acknowledgement must be stored")
	}
	if got.SourceAddress.ValueString() != "46.246.117.231" || got.ID.ValueString() != "gw-1" {
		t.Errorf("observed values must be carried over from state, got %+v", got)
	}
	// Nothing may stay unknown: the framework rejects an unknown value in the
	// state a provider returns from Update.
	for name, v := range map[string]interface{ IsUnknown() bool }{
		"id": got.ID, "source_address": got.SourceAddress, "status": got.Status, "origin": got.Origin,
	} {
		if v.IsUnknown() {
			t.Errorf("%s was left unknown after an acknowledgement-only update", name)
		}
	}
}

// TestEgressGatewayUpdateRefusesEmptyID: see the Delete counterpart. An id-less
// state row cannot address anything, so the PATCH would be sent to the
// collection instead of to this gateway.
func TestEgressGatewayUpdateRefusesEmptyID(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"gw-1","vpcId":"vpc-1","mode":"nat","status":"active","origin":"explicit"}`))
	}))
	defer server.Close()

	r := configuredResource(t, server.URL)
	s := gwSchema(t)
	state := gwValue("", "vpc-1", ModeNAT, "46.246.117.231", "active", "explicit", boolPtr(true))
	plan := gwValue("", "vpc-1", ModePublicIP, "46.246.117.231", "active", "explicit", boolPtr(true))

	resp := &resource.UpdateResponse{State: tfsdk.State{Schema: s, Raw: state}}
	r.Update(context.Background(), resource.UpdateRequest{
		Plan:  tfsdk.Plan{Schema: s, Raw: plan},
		State: tfsdk.State{Schema: s, Raw: state},
	}, resp)

	if !resp.Diagnostics.HasError() {
		t.Fatal("expected an update on an id-less state row to be refused")
	}
	if calls != 0 {
		t.Errorf("no request may be built from an empty id; saw %d", calls)
	}
}

func TestEgressGatewayUpdateSurfacesPoolExhausted(t *testing.T) {
	calls := 0
	server := httptest.NewServer(apiErrorHandler(&calls, http.StatusServiceUnavailable,
		"GATEWAY_POOL_EXHAUSTED", "no public IPv4 addresses are currently available in this region"))
	defer server.Close()

	r := configuredResource(t, server.URL)
	s := gwSchema(t)
	state := gwValue("gw-1", "vpc-1", ModeNAT, "", "active", "explicit", boolPtr(true))
	plan := gwValue("gw-1", "vpc-1", ModePublicIP, "", "active", "explicit", boolPtr(true))

	resp := &resource.UpdateResponse{State: tfsdk.State{Schema: s, Raw: state}}
	r.Update(context.Background(), resource.UpdateRequest{
		Plan:  tfsdk.Plan{Schema: s, Raw: plan},
		State: tfsdk.State{Schema: s, Raw: state},
	}, resp)

	text := strings.ToLower(diagText(resp.Diagnostics))
	if !strings.Contains(text, "temporary") {
		t.Errorf("a 503 during a mode change must read as retryable, got:\n%s", text)
	}
}

// --- delete ---

// TestEgressGatewayDeleteWithoutAcknowledgementRefuses: `terraform destroy` is
// a plan approval, not an acknowledgement — it says nothing about DNS or
// managed-service connectivity, and a gateway is routinely destroyed as a side
// effect of tearing down something else.
func TestEgressGatewayDeleteWithoutAcknowledgementRefuses(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	r := configuredResource(t, server.URL)
	s := gwSchema(t)
	raw := gwValue("gw-1", "vpc-1", ModePublicIP, "46.246.117.231", "active", "explicit", nil)

	resp := &resource.DeleteResponse{State: tfsdk.State{Schema: s, Raw: raw}}
	r.Delete(context.Background(), resource.DeleteRequest{State: tfsdk.State{Schema: s, Raw: raw}}, resp)

	if !resp.Diagnostics.HasError() {
		t.Fatal("expected the destroy to be refused without an acknowledgement")
	}
	if calls != 0 {
		t.Errorf("no request may be sent when the removal is unacknowledged; saw %d", calls)
	}
	text := diagText(resp.Diagnostics)
	if !strings.Contains(text, "DNS") || !strings.Contains(text, "managed-service") {
		t.Errorf("the refusal must state the full cost, got:\n%s", text)
	}
}

// TestEgressGatewayDeleteSendsAcknowledgementAsQuery also pins the wire form:
// building "?acknowledgeConnectivityLoss=true" into the path string
// percent-encodes the "?" into the path segment, and the API then answers as if
// the acknowledgement had never been sent.
func TestEgressGatewayDeleteSendsAcknowledgementAsQuery(t *testing.T) {
	var gotPath, gotAck string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAck = r.URL.Query().Get("acknowledgeConnectivityLoss")
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	r := configuredResource(t, server.URL)
	s := gwSchema(t)
	raw := gwValue("gw-1", "vpc-1", ModePublicIP, "46.246.117.231", "active", "explicit", boolPtr(true))

	resp := &resource.DeleteResponse{State: tfsdk.State{Schema: s, Raw: raw}}
	r.Delete(context.Background(), resource.DeleteRequest{State: tfsdk.State{Schema: s, Raw: raw}}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected errors: %v", resp.Diagnostics)
	}
	if gotPath != "/v1/tenants/t-123/gateways/gw-1" {
		t.Errorf("unexpected path %q — the query must not be built into it", gotPath)
	}
	if gotAck != "true" {
		t.Errorf("expected acknowledgeConnectivityLoss=true as a query parameter, got %q", gotAck)
	}
}

// TestEgressGatewayDeleteRefusesEmptyID pins the failure mode that looks like
// success. `path.Join` cleans "/v1/tenants/t/gateways/" back to the
// COLLECTION path, so a DELETE built from an empty id is answered by the
// collection — and whatever it answers, this resource's IsNotFound branch would
// read as "the gateway was already gone" and report a clean destroy for a
// gateway that is still up, still spending an address, and now unmanaged.
func TestEgressGatewayDeleteRefusesEmptyID(t *testing.T) {
	var gotPaths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPaths = append(gotPaths, r.URL.Path)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	r := configuredResource(t, server.URL)
	s := gwSchema(t)
	raw := gwValue("", "vpc-1", ModePublicIP, "46.246.117.231", "active", "explicit", boolPtr(true))

	resp := &resource.DeleteResponse{State: tfsdk.State{Schema: s, Raw: raw}}
	r.Delete(context.Background(), resource.DeleteRequest{State: tfsdk.State{Schema: s, Raw: raw}}, resp)

	if len(gotPaths) != 0 {
		t.Errorf("no request may be built from an empty id (it addresses the collection); sent %v", gotPaths)
	}
	if !resp.Diagnostics.HasError() {
		t.Fatal("expected a destroy on an id-less state row to be refused, not reported as done")
	}
}

// TestEgressGatewayInUseDiagnosticPutsDependsOnOnThePublicIP: the ordering
// advice has to be the right way round. `depends_on` makes Terraform destroy
// the DEPENDENT first, so the dependency belongs on the public IPs pointing at
// the gateway. Written the other way (on the gateway, listing the public IPs)
// the gateway is destroyed FIRST — this same 409 again — and on create the
// public IP is associated first, which makes the platform attach an implicit
// gateway and the explicit gateway create then fails with
// GATEWAY_EXISTS.
func TestEgressGatewayInUseDiagnosticPutsDependsOnOnThePublicIP(t *testing.T) {
	var diags diag.Diagnostics
	addGatewayError(&diags, "Failed to delete gateway", &client.APIError{
		Code: errCodeGatewayInUse, Message: "2 public IPs in this VPC depend on the gateway", StatusCode: 409,
	})

	text := diagText(diags)
	if !strings.Contains(text, "depends_on = [frostmoln_gateway") {
		t.Errorf("the diagnostic must put depends_on ON the public IPs, pointing at the gateway, got:\n%s", text)
	}
	if strings.Contains(text, "depends_on = [frostmoln_public_ip") {
		t.Errorf("a depends_on from the gateway to the public IPs reverses the destroy order and does not "+
			"fix this failure, got:\n%s", text)
	}
}

func TestEgressGatewayDeleteInUse(t *testing.T) {
	calls := 0
	server := httptest.NewServer(apiErrorHandler(&calls, http.StatusConflict,
		"GATEWAY_IN_USE", "2 public IPs in this VPC depend on the gateway; release or detach them before removing it"))
	defer server.Close()

	r := configuredResource(t, server.URL)
	s := gwSchema(t)
	raw := gwValue("gw-1", "vpc-1", ModePublicIP, "46.246.117.231", "active", "explicit", boolPtr(true))

	resp := &resource.DeleteResponse{State: tfsdk.State{Schema: s, Raw: raw}}
	r.Delete(context.Background(), resource.DeleteRequest{State: tfsdk.State{Schema: s, Raw: raw}}, resp)

	text := strings.ToLower(diagText(resp.Diagnostics))
	if !strings.Contains(text, "public ip") {
		t.Errorf("diagnostic must name what depends on the gateway, got:\n%s", text)
	}

	// DETACH BEFORE RELEASE, in that order, and not merely both present.
	//
	// Detaching is free, reversible and keeps the address; releasing is
	// permanent — the address goes back to a shared pool, and every partner
	// allow-list entry and DNS record naming it silently stops matching. Someone
	// reading this has something already blocked and acts on the first remedy
	// offered, so leading with the irreversible one is unsafe guidance. The
	// network service orders it the same way (NewEgressGatewayInUseError), as do
	// the CLI and the customer docs; this client renders that source and was the
	// one place still leading with "release".
	detach := strings.Index(text, "detach")
	release := strings.Index(text, "releas")
	if detach < 0 {
		t.Fatalf("diagnostic must offer detaching at all, got:\n%s", text)
	}
	if release >= 0 && release < detach {
		t.Errorf("the diagnostic leads with the IRREVERSIBLE remedy: %q appears before %q, got:\n%s",
			text[release:min(release+20, len(text))], text[detach:min(detach+20, len(text))], text)
	}

	// The same code is returned for a holder the tenant CANNOT see or release
	// (NewEgressGatewayInUseByOtherTenantError, network/internal/domain), so the
	// text must not send them hunting for an address of their own to release.
	if !strings.Contains(text, "managed-service") {
		t.Errorf("diagnostic must cover the holder that is not in the tenant's own listing, got:\n%s", text)
	}
}

func TestEgressGatewayDeleteNotFoundIsSuccess(t *testing.T) {
	calls := 0
	server := httptest.NewServer(apiErrorHandler(&calls, http.StatusNotFound, "NOT_FOUND", "gateway not found"))
	defer server.Close()

	r := configuredResource(t, server.URL)
	s := gwSchema(t)
	raw := gwValue("gw-1", "vpc-1", ModePublicIP, "", "active", "explicit", boolPtr(true))

	resp := &resource.DeleteResponse{State: tfsdk.State{Schema: s, Raw: raw}}
	r.Delete(context.Background(), resource.DeleteRequest{State: tfsdk.State{Schema: s, Raw: raw}}, resp)

	if resp.Diagnostics.HasError() {
		t.Errorf("a gateway already gone is a successful destroy, got %v", resp.Diagnostics)
	}
}

// --- import ---

// TestEgressGatewayImportSetsOnlyID: the import id may be the gateway id OR the
// VPC id (GET /gateways/{id} accepts both). Copying it into vpc_id as
// well would make the following Read filter the list by a vpcId matching
// nothing, and silently drop the freshly imported resource.
func TestEgressGatewayImportSetsOnlyID(t *testing.T) {
	r := NewResource().(resource.ResourceWithImportState)
	s := gwSchema(t)

	// The framework hands ImportState a state that is a NULL object of the
	// schema type, not a zero tfsdk.State — mirror that.
	resp := &resource.ImportStateResponse{State: tfsdk.State{Schema: s, Raw: tftypes.NewValue(gwObjectType(), nil)}}
	r.ImportState(context.Background(), resource.ImportStateRequest{ID: "gw-1"}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected errors: %v", resp.Diagnostics)
	}
	var state GatewayModel
	resp.State.Get(context.Background(), &state)
	if state.ID.ValueString() != "gw-1" {
		t.Errorf("expected id gw-1, got %v", state.ID)
	}
	if !state.VPCID.IsNull() {
		t.Errorf("vpc_id must be left for Read to resolve, got %v", state.VPCID)
	}
	if !state.AcknowledgeConnectivityLoss.IsNull() {
		t.Error("an imported gateway must not arrive pre-acknowledged for destruction")
	}
}

// --- error mapping fallbacks ---

func TestAddEgressErrorFallbacks(t *testing.T) {
	var diags diag.Diagnostics
	addGatewayError(&diags, "Failed to do the thing", context.Canceled)
	if len(diags.Errors()) != 1 || diags.Errors()[0].Summary() != "Failed to do the thing" {
		t.Errorf("a non-API error must fall back to the caller's summary, got %v", diags)
	}

	var apiDiags diag.Diagnostics
	addGatewayError(&apiDiags, "Failed to do the thing", &client.APIError{
		Code: "SOMETHING_NEW", Message: "unrecognised", StatusCode: 500,
	})
	if len(apiDiags.Errors()) != 1 || !strings.Contains(apiDiags.Errors()[0].Detail(), "SOMETHING_NEW") {
		t.Errorf("an unmapped code must still surface its code, got %v", apiDiags)
	}
}

// --- public_ip_id ---

// TestEgressGatewayPublicIPIDDoesNotRequireReplace is THE regression guard for
// this attribute.
//
// Re-pointing a gateway at another public IP is an in-place update: the
// platform re-addresses the existing outbound path. A RequiresReplace here
// would instead destroy the VPC's only path off-net and build a new one — the
// VPC loses internet, platform DNS resolution and managed-service connectivity
// in between — which is the exact outage a stable, tenant-chosen egress
// address exists to prevent. The plan-modifier replay is deliberate: asserting
// the modifier list "looks right" cannot see an ordering that records a
// replacement anyway.
func TestEgressGatewayPublicIPIDDoesNotRequireReplace(t *testing.T) {
	s := gwSchema(t)
	attr, ok := s.Attributes["public_ip_id"].(schema.StringAttribute)
	if !ok {
		t.Fatalf("expected public_ip_id to be a StringAttribute, got %T", s.Attributes["public_ip_id"])
	}
	if !attr.Optional || !attr.Computed {
		t.Fatal("public_ip_id must be Optional+Computed: Optional so the practitioner can choose the " +
			"address, Computed so a value the platform reports for one they did not choose is " +
			"recorded instead of planning as a permanent diff")
	}

	// A real value change, with the practitioner's chosen value in config.
	req := planmodifier.StringRequest{
		Path:        path.Root("public_ip_id"),
		State:       tfsdk.State{Schema: s, Raw: gwValue("gw-1", "vpc-1", ModePublicIP, "", "", "", nil, "pip-1")},
		Plan:        tfsdk.Plan{Schema: s, Raw: gwValue("gw-1", "vpc-1", ModePublicIP, "", "", "", nil, "pip-2")},
		StateValue:  types.StringValue("pip-1"),
		PlanValue:   types.StringValue("pip-2"),
		ConfigValue: types.StringValue("pip-2"),
	}
	replace := false
	for _, m := range attr.PlanModifiers {
		resp := &planmodifier.StringResponse{PlanValue: req.PlanValue}
		m.PlanModifyString(context.Background(), req, resp)
		req.PlanValue = resp.PlanValue
		replace = replace || resp.RequiresReplace
	}
	if replace {
		t.Error("changing public_ip_id must be an in-place update, never a destroy/create: a replacement " +
			"takes the VPC's internet, DNS and managed-service connectivity down in between")
	}
}

// TestEgressGatewayPublicIPIDOmittedIsPlanStable is the perpetual-diff guard at
// the attribute level, for both shapes state can be in when the practitioner
// omitted the attribute:
//
//   - a recorded id (the platform allocated an address and reported it), and
//   - a null (a NAT gateway, or one whose address predates public-IP-backed
//     egress).
//
// The framework marks a null-config Computed attribute unknown, and an unknown
// facing either of those is a diff on every run: `terraform plan
// -detailed-exitcode` returns 2 forever and any drift gate built on it breaks.
func TestEgressGatewayPublicIPIDOmittedIsPlanStable(t *testing.T) {
	s := gwSchema(t)
	attr, ok := s.Attributes["public_ip_id"].(schema.StringAttribute)
	if !ok {
		t.Fatalf("expected public_ip_id to be a StringAttribute, got %T", s.Attributes["public_ip_id"])
	}

	for _, tc := range []struct {
		name  string
		state types.String
	}{
		{"platform-allocated address recorded in state", types.StringValue("pip-allocated")},
		{"no address recorded at all", types.StringNull()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := planmodifier.StringRequest{
				Path:  path.Root("public_ip_id"),
				State: tfsdk.State{Schema: s, Raw: gwValue("gw-1", "vpc-1", ModePublicIP, "", "", "", nil)},
				Plan:  tfsdk.Plan{Schema: s, Raw: gwValue("gw-1", "vpc-1", ModePublicIP, "", "", "", nil)},
				// What the framework hands a Computed attribute whose config is
				// null: unknown.
				StateValue:  tc.state,
				PlanValue:   types.StringUnknown(),
				ConfigValue: types.StringNull(),
			}
			for _, m := range attr.PlanModifiers {
				resp := &planmodifier.StringResponse{PlanValue: req.PlanValue}
				m.PlanModifyString(context.Background(), req, resp)
				req.PlanValue = resp.PlanValue
			}
			if !req.PlanValue.Equal(tc.state) {
				t.Errorf("an omitted public_ip_id must plan as the recorded value (%v), got %v — anything "+
					"else is a diff on every single run", tc.state, req.PlanValue)
			}
		})
	}
}

// TestEgressGatewayModifyPlanKeepsOmittedPublicIPID: the resource-level half of
// the same invariant. With nothing else changing, an omitted public_ip_id must
// resolve to the state value — including a null.
func TestEgressGatewayModifyPlanKeepsOmittedPublicIPID(t *testing.T) {
	planned := modifyPlan(t,
		gwPlanUnknownComputed("vpc-1", ModePublicIP, nil),
		gwValue("gw-1", "vpc-1", ModePublicIP, "46.246.117.231", "active", "explicit_public_ip", nil, "pip-1"))

	if planned.PublicIPID.ValueString() != "pip-1" {
		t.Errorf("an omitted public_ip_id must keep the recorded value, got %v", planned.PublicIPID)
	}
	if planned.SourceAddress.ValueString() != "46.246.117.231" {
		t.Errorf("nothing changed, so source_address must not be re-planned; got %v", planned.SourceAddress)
	}
}

// TestEgressGatewayModifyPlanUnknownPinOnModeSwitch is the "inconsistent result
// after apply" guard for the switch INTO public_ip mode without naming an
// address. Both the config and the state hold a null, so UseStateForUnknown
// alone would pin the plan to null ACROSS AN APPLY THAT RE-ADDRESSES THE
// GATEWAY, and Terraform core rejects outright any planned value the apply
// contradicts. The plan must not assert what the platform will report before it
// has reported it.
func TestEgressGatewayModifyPlanUnknownPinOnModeSwitch(t *testing.T) {
	planned := modifyPlan(t,
		gwPlanUnknownComputed("vpc-1", ModePublicIP, boolPtr(true)),
		gwValue("gw-1", "vpc-1", ModeNAT, "185.1.2.3", "active", "explicit", boolPtr(true)))

	if !planned.PublicIPID.IsUnknown() {
		t.Errorf("switching into public_ip mode without naming an address must plan public_ip_id as "+
			"unknown — the apply re-addresses the gateway and the plan must not assert the result in "+
			"advance; got %v", planned.PublicIPID)
	}
}

// TestEgressGatewayModifyPlanKeepsConfiguredPin: a value the practitioner wrote
// is their choice and must stay KNOWN in the plan. Overwriting it with unknown
// would hide the very change being planned — `terraform plan` would render
// "(known after apply)" for the address the configuration names.
func TestEgressGatewayModifyPlanKeepsConfiguredPin(t *testing.T) {
	config := gwValue("", "vpc-1", ModePublicIP, "", "", "", boolPtr(true), "pip-2")
	planned := modifyPlan(t,
		gwValue("gw-1", "vpc-1", ModePublicIP, "46.246.117.231", "active", "explicit_public_ip", boolPtr(true), "pip-2"),
		gwValue("gw-1", "vpc-1", ModePublicIP, "46.246.117.231", "active", "explicit_public_ip", boolPtr(true), "pip-1"),
		config)

	if planned.PublicIPID.ValueString() != "pip-2" {
		t.Errorf("a configured public_ip_id must stay known in the plan, got %v", planned.PublicIPID)
	}
	// The address moves with the pin, so the observed values are re-recorded.
	for name, v := range map[string]types.String{
		"source_address": planned.SourceAddress,
		"status":         planned.Status,
		"origin":         planned.Origin,
	} {
		if !v.IsUnknown() {
			t.Errorf("%s must be planned as unknown across an address change (the platform re-records "+
				"it); got %v", name, v)
		}
	}
}

// --- validation ---

// TestEgressGatewayHasNoValidateConfig documents a deliberate removal.
//
// The resource used to implement ValidateConfig for ONE case: `public_ip_id`
// named together with `mode = "nat"`. That pair is unreachable now that `nat`
// itself is refused, and keeping the check would emit a SECOND diagnostic
// saying "public_ip_id is only meaningful with public_ip" — which implies the
// withdrawn mode was otherwise fine, and is the opposite of what the
// practitioner needs to read.
func TestEgressGatewayHasNoValidateConfig(t *testing.T) {
	if _, ok := NewResource().(resource.ResourceWithValidateConfig); ok {
		t.Error("the pin/mode pair check is subsumed by the `mode` validator; a ValidateConfig that " +
			"fires on mode = \"nat\" would add a second, misleading diagnostic to the withdrawal refusal")
	}
}

// --- create with a chosen address ---

func TestEgressGatewayCreateSendsChosenPublicIPID(t *testing.T) {
	var got apiCreateGatewayRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":"gw-1","vpcId":"vpc-1","tenantId":"t-123","mode":"public_ip",
			"sourceAddress":"46.246.117.231","status":"active","origin":"explicit_public_ip",
			"publicIpId":"pip-1"}`))
	}))
	defer server.Close()

	r := configuredResource(t, server.URL)
	s := gwSchema(t)

	resp := &resource.CreateResponse{State: tfsdk.State{Schema: s}}
	r.Create(context.Background(), resource.CreateRequest{
		Plan: tfsdk.Plan{Schema: s, Raw: gwValue("", "vpc-1", ModePublicIP, "", "", "", nil, "pip-1")},
	}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected errors: %v", resp.Diagnostics)
	}
	if got.PublicIPID != "pip-1" {
		t.Errorf("the create must carry the chosen address, got %+v", got)
	}

	var state GatewayModel
	resp.State.Get(context.Background(), &state)
	if state.PublicIPID.ValueString() != "pip-1" {
		t.Errorf("expected public_ip_id pip-1 in state, got %v", state.PublicIPID)
	}
	if state.Origin.ValueString() != OriginExplicitPublicIP {
		t.Errorf("expected origin %q, got %v", OriginExplicitPublicIP, state.Origin)
	}
}

// TestEgressGatewayCreateOmitsPublicIPIDWhenNotChosen: an omitted field means
// "the platform draws the gateway an address itself". Sending an empty string
// instead would be a different request, and it is what would break every
// configuration written before this attribute existed.
//
// The scripted response is the real shape for that request (ADR-0114): the
// address has no id and is not a public IP resource, so the API omits
// `publicIpId` and the attribute must land NULL — it does NOT come back as an
// allocation made on the tenant's behalf.
func TestEgressGatewayCreateOmitsPublicIPIDWhenNotChosen(t *testing.T) {
	var raw map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&raw)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":"gw-1","vpcId":"vpc-1","tenantId":"t-123","mode":"public_ip",
			"sourceAddress":"46.246.117.231","status":"active","origin":"explicit"}`))
	}))
	defer server.Close()

	r := configuredResource(t, server.URL)
	s := gwSchema(t)

	resp := &resource.CreateResponse{State: tfsdk.State{Schema: s}}
	r.Create(context.Background(), resource.CreateRequest{
		Plan: tfsdk.Plan{Schema: s, Raw: gwValue("", "vpc-1", ModePublicIP, "", "", "", nil)},
	}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected errors: %v", resp.Diagnostics)
	}
	if _, present := raw["publicIpId"]; present {
		t.Errorf("publicIpId must be OMITTED when the practitioner chose no address, got body %v", raw)
	}

	var state GatewayModel
	resp.State.Get(context.Background(), &state)
	if !state.PublicIPID.IsNull() {
		t.Errorf("a gateway on a platform-drawn address has no public IP resource, so public_ip_id "+
			"must be NULL (never \"\", and never an id the platform never reported), got %v",
			state.PublicIPID)
	}
	if state.SourceAddress.ValueString() != "46.246.117.231" {
		t.Errorf("the gateway still has a source address, got %v", state.SourceAddress)
	}
}

// TestEgressGatewayCreateRefusesADifferentPublicIP: if the platform bound an
// address other than the one named, the VPC is egressing from something the
// configuration does not mention — so the allow-list entry or DNS record the
// practitioner published no longer matches their traffic. Core would catch the
// divergence as a provider bug; this says what is actually true in the cloud.
func TestEgressGatewayCreateRefusesADifferentPublicIP(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":"gw-1","vpcId":"vpc-1","tenantId":"t-123","mode":"public_ip",
			"sourceAddress":"46.246.117.240","status":"active","origin":"explicit_public_ip",
			"publicIpId":"pip-somebody-else"}`))
	}))
	defer server.Close()

	r := configuredResource(t, server.URL)
	s := gwSchema(t)

	resp := &resource.CreateResponse{State: tfsdk.State{Schema: s}}
	r.Create(context.Background(), resource.CreateRequest{
		Plan: tfsdk.Plan{Schema: s, Raw: gwValue("", "vpc-1", ModePublicIP, "", "", "", nil, "pip-1")},
	}, resp)

	if !resp.Diagnostics.HasError() {
		t.Fatal("expected the mismatch to be refused")
	}
	text := diagText(resp.Diagnostics)
	if !strings.Contains(text, "pip-1") || !strings.Contains(text, "pip-somebody-else") {
		t.Errorf("the diagnostic must name both the requested and the bound address:\n%s", text)
	}
}

// --- update: re-pointing at another address ---

// TestEgressGatewayUpdatePinChangeRequiresAcknowledgement: moving the gateway
// onto a different address changes what the VPC's traffic arrives as, so it is
// held to the same rule as a mode change — refused locally, before any request.
func TestEgressGatewayUpdatePinChangeRequiresAcknowledgement(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	r := configuredResource(t, server.URL)
	s := gwSchema(t)
	state := gwValue("gw-1", "vpc-1", ModePublicIP, "46.246.117.231", "active", "explicit_public_ip", nil, "pip-1")
	plan := gwPlanUnknownComputed("vpc-1", ModePublicIP, nil, "pip-2")

	resp := &resource.UpdateResponse{State: tfsdk.State{Schema: s, Raw: state}}
	r.Update(context.Background(), resource.UpdateRequest{
		Plan:  tfsdk.Plan{Schema: s, Raw: plan},
		State: tfsdk.State{Schema: s, Raw: state},
	}, resp)

	if !resp.Diagnostics.HasError() {
		t.Fatal("expected the address change to be refused without the acknowledgement")
	}
	if calls != 0 {
		t.Errorf("nothing may be sent before the acknowledgement is given, saw %d call(s)", calls)
	}
	text := diagText(resp.Diagnostics)
	if !strings.Contains(text, "pip-1") || !strings.Contains(text, "pip-2") {
		t.Errorf("the refusal must name both addresses:\n%s", text)
	}
	if !strings.Contains(text, "acknowledge_connectivity_loss") {
		t.Errorf("the refusal must name the attribute that allows it:\n%s", text)
	}
}

// TestEgressGatewayUpdatePinChangeIsAPatch: the wire-level statement that
// re-pointing is in place. A PATCH by the gateway's own id is what keeps the
// VPC's outbound path up across the change.
func TestEgressGatewayUpdatePinChangeIsAPatch(t *testing.T) {
	var gotMethod, gotPath string
	var gotBody apiUpdateGatewayRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		_, _ = w.Write([]byte(`{"id":"gw-1","vpcId":"vpc-1","mode":"public_ip","sourceAddress":"46.246.117.232",
			"status":"active","origin":"explicit_public_ip","publicIpId":"pip-2"}`))
	}))
	defer server.Close()

	r := configuredResource(t, server.URL)
	s := gwSchema(t)
	state := gwValue("gw-1", "vpc-1", ModePublicIP, "46.246.117.231", "active", "explicit_public_ip", boolPtr(true), "pip-1")
	plan := gwPlanUnknownComputed("vpc-1", ModePublicIP, boolPtr(true), "pip-2")

	resp := &resource.UpdateResponse{State: tfsdk.State{Schema: s, Raw: state}}
	r.Update(context.Background(), resource.UpdateRequest{
		Plan:  tfsdk.Plan{Schema: s, Raw: plan},
		State: tfsdk.State{Schema: s, Raw: state},
	}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected errors: %v", resp.Diagnostics)
	}
	if gotMethod != http.MethodPatch {
		t.Errorf("an address change must be a PATCH, got %s", gotMethod)
	}
	if gotPath != "/v1/tenants/t-123/gateways/gw-1" {
		t.Errorf("the PATCH must target the gateway id, got %s", gotPath)
	}
	if gotBody.PublicIPID != "pip-2" || gotBody.Mode != ModePublicIP || !gotBody.AcknowledgeConnectivityLoss {
		t.Errorf("unexpected PATCH body %+v", gotBody)
	}

	var got GatewayModel
	resp.State.Get(context.Background(), &got)
	if got.PublicIPID.ValueString() != "pip-2" || got.SourceAddress.ValueString() != "46.246.117.232" {
		t.Errorf("state was not refreshed from the response: %+v", got)
	}
}

// TestEgressGatewayUpdateModeSwitchOmitsUnknownPin: switching into public_ip
// mode without naming an address plans public_ip_id as unknown, and an unknown
// must never reach the wire — an empty publicIpId is a different request from
// an absent one.
func TestEgressGatewayUpdateModeSwitchOmitsUnknownPin(t *testing.T) {
	var raw map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&raw)
		_, _ = w.Write([]byte(`{"id":"gw-1","vpcId":"vpc-1","mode":"public_ip","sourceAddress":"46.246.117.231",
			"status":"active","origin":"explicit_public_ip","publicIpId":"pip-allocated"}`))
	}))
	defer server.Close()

	r := configuredResource(t, server.URL)
	s := gwSchema(t)
	state := gwValue("gw-1", "vpc-1", ModeNAT, "185.1.2.3", "active", "explicit", boolPtr(true))
	plan := gwPlanUnknownComputed("vpc-1", ModePublicIP, boolPtr(true))

	resp := &resource.UpdateResponse{State: tfsdk.State{Schema: s, Raw: state}}
	r.Update(context.Background(), resource.UpdateRequest{
		Plan:  tfsdk.Plan{Schema: s, Raw: plan},
		State: tfsdk.State{Schema: s, Raw: state},
	}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected errors: %v", resp.Diagnostics)
	}
	if _, present := raw["publicIpId"]; present {
		t.Errorf("an unresolved public_ip_id must be omitted from the PATCH, got body %v", raw)
	}

	var got GatewayModel
	resp.State.Get(context.Background(), &got)
	if got.PublicIPID.ValueString() != "pip-allocated" {
		t.Errorf("the allocated address must be recorded, got %v", got.PublicIPID)
	}
}

// --- model ---

// TestApplyToModelAbsentPublicIPIDIsNull: the API omits publicIpId under NAT
// and for a gateway whose address predates public-IP-backed egress. "" is a
// value a practitioner could interpolate into another resource's id, so absent
// must map to null.
func TestApplyToModelAbsentPublicIPIDIsNull(t *testing.T) {
	var m GatewayModel
	applyToModel(&m, &apiGateway{
		ID: "gw-1", VPCID: "vpc-1", Mode: ModeNAT, Status: "active", Origin: "explicit",
	})
	if !m.PublicIPID.IsNull() {
		t.Errorf("an absent publicIpId must map to null, got %v", m.PublicIPID)
	}
}

// --- diagnostics ---

// TestAddEgressErrorPublicIPCodes: both new refusals are things the
// practitioner can act on, and neither reads that way as a raw envelope.
func TestAddEgressErrorPublicIPCodes(t *testing.T) {
	for _, tc := range []struct {
		code string
		want []string
	}{
		{errCodePublicIPUnavailable, []string{"not free", "Nothing was changed"}},
		// The ONE cause the server has for this code: publicIpId with
		// mode=nat. A foreign or unknown id is a 404, so a diagnostic that
		// blamed tenancy would send the practitioner to check their
		// credentials while the fault is two lines of their own config.
		{errCodePublicIPNotAllowed, []string{"nat", "public_ip_id", "Nothing was changed"}},
	} {
		t.Run(tc.code, func(t *testing.T) {
			var diags diag.Diagnostics
			addGatewayError(&diags, "fallback", &client.APIError{Code: tc.code, Message: "api message", StatusCode: 409})
			text := diagText(diags)
			if strings.Contains(text, "fallback") {
				t.Errorf("%s must have its own diagnostic, got the fallback:\n%s", tc.code, text)
			}
			for _, want := range tc.want {
				if !strings.Contains(text, want) {
					t.Errorf("%s diagnostic must contain %q, got:\n%s", tc.code, want, text)
				}
			}
			if strings.Contains(strings.ToLower(text), "different tenant") ||
				strings.Contains(strings.ToLower(text), "belongs to a different") {
				t.Errorf("%s must not blame tenancy — a foreign or unknown id is a 404, not this code:\n%s", tc.code, text)
			}
			if !strings.Contains(text, "api message") {
				t.Errorf("%s diagnostic must still quote what the API said:\n%s", tc.code, text)
			}
		})
	}
}
