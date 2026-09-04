package appgw_waf_policy

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	tfpath "github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// strList builds a list attribute value. A typed NULL is types.ListNull, never
// the zero types.List -- a list with no element type fails the framework's type
// check before any assertion in these tests can run.
func strList(ss ...string) types.List {
	elems := make([]attr.Value, 0, len(ss))
	for _, s := range ss {
		elems = append(elems, types.StringValue(s))
	}
	return types.ListValueMust(types.StringType, elems)
}

func nullList() types.List { return types.ListNull(types.StringType) }

// captureUpdate runs Update against a stub server and returns the RAW PATCH
// body, keyed by field.
//
// json.RawMessage rather than `any` on purpose: this whole file turns on the
// difference between a field that is ABSENT and one whose value is `[]`, and a
// map[string]any decode renders the second as an empty slice that reads much
// like a missing key at a glance. Raw bytes cannot be confused.
func captureUpdate(t *testing.T, plan, state PolicyModel, respond apiPolicy) (map[string]json.RawMessage, resource.UpdateResponse) {
	t.Helper()
	var body map[string]json.RawMessage
	c, _ := serve(t, func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode PATCH body: %v", err)
		}
		_ = json.NewEncoder(w).Encode(respond)
	})
	pr := &policyResource{client: c}
	resp := resource.UpdateResponse{State: stateOf(t, state)}
	pr.Update(context.Background(), resource.UpdateRequest{
		Plan: planOf(t, plan), State: stateOf(t, state),
	}, &resp)
	return body, resp
}

// gatewayPolicy is a settled gateway-scoped policy: the state an update starts
// from.
func gatewayPolicy() PolicyModel {
	var m PolicyModel
	m.fromAPI(&apiPolicy{
		ID: "wp-1", GatewayID: "agw-1", Name: "default", Scope: scopeGateway,
		Mode: modeDetect, EffectiveMode: modeDetect, ParanoiaLevel: 2,
		AnomalyScoreThreshold: 5, CRSVersion: "4.7.0", FailMode: "open",
		RequestBodyLimitBytes: 16384,
		EffectiveAllowedMethods: []string{
			"GET", "HEAD", "POST", "OPTIONS", "PUT", "PATCH", "DELETE",
		},
		EffectiveAllowedRequestContentTypes: []string{"application/json"},
	})
	return m
}

// TestTheAllowListTriStateReachesTheWire.
//
// 🔴 THREE WIRE STATES, AND A CLIENT THAT CANNOT EMIT ALL THREE EVENTUALLY
// STRANDS A CUSTOMER. The server models each list as a pointer to a slice:
// absent leaves the override alone, `[]` CLEARS it and returns the policy to
// the platform default, and a list replaces it. `[]` is the only spelling of a
// reset the API has -- omitting the field does not undo a narrowing.
//
// The case this exists for is the third row: the practitioner deleted the
// attribute from their configuration. Sending nothing there would report a
// successful apply while the narrowing stayed in force.
func TestTheAllowListTriStateReachesTheWire(t *testing.T) {
	for _, tc := range []struct {
		name string
		// planned and stored values of allowed_methods.
		planned, stored types.List
		// wantBody is the raw JSON expected for the field; "" means the field
		// must be ABSENT.
		wantBody string
	}{
		{
			name:    "unchanged sends nothing, so an unrelated edit never restates it",
			planned: strList("GET", "HEAD"), stored: strList("GET", "HEAD"),
			wantBody: "",
		},
		{
			name:    "no override, none planned: still nothing, not an empty list",
			planned: nullList(), stored: nullList(),
			wantBody: "",
		},
		{
			name:    "REMOVED FROM HCL clears the override with []",
			planned: nullList(), stored: strList("GET", "HEAD"),
			wantBody: `[]`,
		},
		{
			name:    "a narrowing is sent exactly",
			planned: strList("GET"), stored: nullList(),
			wantBody: `["GET"]`,
		},
		{
			name:    "a changed narrowing is sent exactly",
			planned: strList("GET", "PROPFIND"), stored: strList("GET"),
			wantBody: `["GET","PROPFIND"]`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state := gatewayPolicy()
			state.AllowedMethods = tc.stored
			plan := state
			plan.AllowedMethods = tc.planned

			body, resp := captureUpdate(t, plan, state, apiPolicy{
				ID: "wp-1", GatewayID: "agw-1", Name: "default", Mode: modeDetect,
			})
			if resp.Diagnostics.HasError() {
				t.Fatalf("update: %v", resp.Diagnostics.Errors())
			}
			raw, present := body["allowedMethods"]
			if tc.wantBody == "" {
				if present {
					t.Fatalf("allowedMethods was sent as %s; it must be ABSENT so the server "+
						"leaves the override alone", raw)
				}
				return
			}
			if !present {
				t.Fatalf("allowedMethods was NOT sent; %q needs it on the wire. Omitting it "+
					"leaves the override in force while the apply reports success", tc.wantBody)
			}
			if got := string(raw); got != tc.wantBody {
				t.Fatalf("allowedMethods = %s, want %s", got, tc.wantBody)
			}
		})
	}
}

// TestRemovingTheContentTypeOverrideAlsoClears pins the same rule on the second
// list rather than trusting that one implementation covers both. They are two
// fields on the wire, and the failure mode -- a stranded narrowing -- is per
// field.
func TestRemovingTheContentTypeOverrideAlsoClears(t *testing.T) {
	state := gatewayPolicy()
	state.AllowedRequestContentTypes = strList("application/json")
	plan := state
	plan.AllowedRequestContentTypes = nullList()

	body, resp := captureUpdate(t, plan, state, apiPolicy{
		ID: "wp-1", GatewayID: "agw-1", Name: "default", Mode: modeDetect,
	})
	if resp.Diagnostics.HasError() {
		t.Fatalf("update: %v", resp.Diagnostics.Errors())
	}
	raw, present := body["allowedRequestContentTypes"]
	if !present || string(raw) != `[]` {
		t.Fatalf("allowedRequestContentTypes = %s (present=%v), want [] -- removing the "+
			"attribute must CLEAR the override, not leave it in force", raw, present)
	}
	// And the other list, which did not change, stays off the wire.
	if _, sent := body["allowedMethods"]; sent {
		t.Errorf("allowedMethods was restated though it did not change: %v", body)
	}
}

// TestClearingOneOverrideDoesNotDisturbTheOther.
//
// The two lists are independent budgets of trust. Narrowing methods while
// clearing content types must produce exactly one list and one `[]`, not one
// instruction applied to both.
func TestClearingOneOverrideDoesNotDisturbTheOther(t *testing.T) {
	state := gatewayPolicy()
	state.AllowedMethods = strList("GET")
	state.AllowedRequestContentTypes = strList("application/json")
	plan := state
	plan.AllowedMethods = strList("GET", "POST")
	plan.AllowedRequestContentTypes = nullList()

	body, resp := captureUpdate(t, plan, state, apiPolicy{
		ID: "wp-1", GatewayID: "agw-1", Name: "default", Mode: modeDetect,
	})
	if resp.Diagnostics.HasError() {
		t.Fatalf("update: %v", resp.Diagnostics.Errors())
	}
	if got := string(body["allowedMethods"]); got != `["GET","POST"]` {
		t.Errorf("allowedMethods = %s, want [\"GET\",\"POST\"]", got)
	}
	if got := string(body["allowedRequestContentTypes"]); got != `[]` {
		t.Errorf("allowedRequestContentTypes = %s, want []", got)
	}
}

// TestCreateOmitsAnAbsentOverrideAndSendsAPresentOne.
//
// A create has nothing to clear, so it has two states rather than three, and
// omitting is what "run on the platform default" means there. Sending `[]`
// would be the same request with more words -- but sending a NARROWING must
// actually reach the server, or every policy created with an allow-list
// silently runs on the default.
func TestCreateOmitsAnAbsentOverrideAndSendsAPresentOne(t *testing.T) {
	create := func(t *testing.T, plan PolicyModel) map[string]json.RawMessage {
		t.Helper()
		var body map[string]json.RawMessage
		c, _ := serve(t, func(w http.ResponseWriter, r *http.Request) {
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decode POST body: %v", err)
			}
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(wpFixture())
		})
		pr := &policyResource{client: c}
		resp := resource.CreateResponse{State: emptyState(t)}
		pr.Create(context.Background(), resource.CreateRequest{Plan: planOf(t, plan)}, &resp)
		if resp.Diagnostics.HasError() {
			t.Fatalf("create: %v", resp.Diagnostics.Errors())
		}
		return body
	}

	body := create(t, wpModel())
	for _, absent := range []string{"allowedMethods", "allowedRequestContentTypes"} {
		if raw, sent := body[absent]; sent {
			t.Errorf("%s was sent as %s on a create that set no override; omitting it is what "+
				"\"use the platform default\" means", absent, raw)
		}
	}

	narrowed := wpModel()
	narrowed.AllowedMethods = strList("GET", "HEAD")
	narrowed.AllowedRequestContentTypes = strList("application/json")
	body = create(t, narrowed)
	if got := string(body["allowedMethods"]); got != `["GET","HEAD"]` {
		t.Errorf("allowedMethods = %s, want [\"GET\",\"HEAD\"] -- a narrowing must reach the "+
			"create or the policy silently runs on the default", got)
	}
	if got := string(body["allowedRequestContentTypes"]); got != `["application/json"]` {
		t.Errorf("allowedRequestContentTypes = %s, want [\"application/json\"]", got)
	}
}

// TestAnEffectiveListIsNeverWritten.
//
// 🔴 THE DEFAULT MOVES. The platform's content-type default has been widened
// three times, so a client that reads the effective list back and writes it as
// an override pins its tenant out of the next widening -- and the plan that
// does it looks like a no-op. The effective fields must never appear in a
// request body, on either door.
func TestAnEffectiveListIsNeverWritten(t *testing.T) {
	forbidden := []string{
		"effectiveAllowedMethods", "effectiveAllowedRequestContentTypes", "effectiveMode",
	}

	var createBody map[string]json.RawMessage
	c, _ := serve(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&createBody)
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(wpFixture())
	})
	pr := &policyResource{client: c}
	plan := wpModel()
	plan.AllowedMethods = strList("GET")
	createResp := resource.CreateResponse{State: emptyState(t)}
	pr.Create(context.Background(), resource.CreateRequest{Plan: planOf(t, plan)}, &createResp)
	if createResp.Diagnostics.HasError() {
		t.Fatalf("create: %v", createResp.Diagnostics.Errors())
	}
	for _, f := range forbidden {
		if _, sent := createBody[f]; sent {
			t.Errorf("the create body carries %s, which the server computes", f)
		}
	}

	// An update where the effective lists in state are RICHER than the override
	// being written: exactly the shape that tempts a round-trip.
	state := gatewayPolicy()
	next := state
	next.AllowedMethods = strList("GET")
	updateBody, updResp := captureUpdate(t, next, state, apiPolicy{
		ID: "wp-1", GatewayID: "agw-1", Name: "default", Mode: modeDetect,
		AllowedMethods: []string{"GET"}, EffectiveAllowedMethods: []string{"GET"},
	})
	if updResp.Diagnostics.HasError() {
		t.Fatalf("update: %v", updResp.Diagnostics.Errors())
	}
	for _, f := range forbidden {
		if _, sent := updateBody[f]; sent {
			t.Errorf("the update body carries %s, which the server computes", f)
		}
	}
	if got := string(updateBody["allowedMethods"]); got != `["GET"]` {
		t.Errorf("allowedMethods = %s, want the AUTHORED [\"GET\"] and never the effective list", got)
	}
}

// TestAnAbsentListIsNullNeverEmpty.
//
// An overlay policy carries no allow-list and decides no effective one, so the
// server sends neither -- and null is the only honest rendering. `[]` in an
// attribute called "the methods this accepts" says the policy allows NOTHING,
// which is a different and alarming claim, and it is the value a practitioner
// would branch on.
func TestAnAbsentListIsNullNeverEmpty(t *testing.T) {
	var overlay PolicyModel
	overlay.fromAPI(&apiPolicy{
		ID: "wp-2", GatewayID: "agw-1", Name: "api", Scope: scopeOverlay, Mode: modeInherit,
	})
	for name, got := range map[string]types.List{
		"allowed_methods":                         overlay.AllowedMethods,
		"allowed_request_content_types":           overlay.AllowedRequestContentTypes,
		"effective_allowed_methods":               overlay.EffectiveAllowedMethods,
		"effective_allowed_request_content_types": overlay.EffectiveAllowedRequestContentTypes,
	} {
		if !got.IsNull() {
			t.Errorf("%s = %v on an overlay, want null -- an empty list would read as "+
				"\"allows nothing\"", name, got)
		}
	}

	var gw PolicyModel
	gw.fromAPI(&apiPolicy{
		ID: "wp-1", GatewayID: "agw-1", Name: "default", Mode: modeDetect,
		EffectiveAllowedMethods: []string{"GET", "HEAD"},
	})
	if gw.AllowedMethods.IsNull() != true {
		t.Errorf("allowed_methods = %v, want null: this policy carries no override", gw.AllowedMethods)
	}
	if gw.EffectiveAllowedMethods.IsNull() {
		t.Error("effective_allowed_methods is null though the server sent a list")
	}
}

// TestAnEmptyListIsRefusedWithTheWorkingSpelling.
//
// 🔴 `[]` CANNOT ROUND-TRIP, so it cannot be accepted. Terraform requires the
// applied value of a non-computed attribute to equal the config value; the
// server answers `[]` by deleting the override and then reports the field
// ABSENT, so state comes back null and every later plan proposes the same
// empty list again. Refusing it at plan time, and naming the spelling that
// works, is the only outcome that leaves the practitioner able to act.
func TestAnEmptyListIsRefusedWithTheWorkingSpelling(t *testing.T) {
	var sr resource.SchemaResponse
	NewResource().Schema(context.Background(), resource.SchemaRequest{}, &sr)

	for _, tc := range []struct{ name, sample string }{
		{"allowed_methods", "GET"},
		{"allowed_request_content_types", "application/json"},
	} {
		name := tc.name
		t.Run(name, func(t *testing.T) {
			attr, ok := sr.Schema.Attributes[name].(schema.ListAttribute)
			if !ok {
				t.Fatalf("%s is not a ListAttribute", name)
			}
			run := func(v types.List) []string {
				var resp validator.ListResponse
				for _, vd := range attr.Validators {
					vd.ValidateList(context.Background(), validator.ListRequest{ConfigValue: v}, &resp)
				}
				var msgs []string
				for _, d := range resp.Diagnostics.Errors() {
					msgs = append(msgs, d.Summary()+" "+d.Detail())
				}
				return msgs
			}

			msgs := run(types.ListValueMust(types.StringType, nil))
			if len(msgs) == 0 {
				t.Fatal("an empty list was accepted; it would produce a permanent diff, because " +
					"a policy carrying no override reports none and the value reads back as null")
			}
			joined := strings.Join(msgs, " ")
			if !strings.Contains(joined, "REMOVE THE ATTRIBUTE") {
				t.Errorf("the refusal does not name the spelling that works: %v", msgs)
			}
			// Null is the supported way to say it, so it must NOT be refused.
			if msgs := run(nullList()); len(msgs) != 0 {
				t.Errorf("a null value was refused, but that is exactly how an override is "+
					"cleared: %v", msgs)
			}
			if msgs := run(strList(tc.sample)); len(msgs) != 0 {
				t.Errorf("a one-element list was refused: %v", msgs)
			}
		})
	}
}

// TestTheMethodDenyListIsRefusedAtPlanTime.
//
// A deny-list of four, not a ceiling of seven: TRACE, TRACK, CONNECT and DEBUG
// are what the managed ruleset's method check exists for and no policy may
// re-admit them, while everything else -- the WebDAV and CalDAV verbs included
// -- is listable and is refused UNTIL listed. The server refuses the four by
// name; catching it here turns a failed apply into a plan a practitioner can
// read.
func TestTheMethodDenyListIsRefusedAtPlanTime(t *testing.T) {
	var sr resource.SchemaResponse
	NewResource().Schema(context.Background(), resource.SchemaRequest{}, &sr)
	attr, ok := sr.Schema.Attributes["allowed_methods"].(schema.ListAttribute)
	if !ok {
		t.Fatal("allowed_methods is not a ListAttribute")
	}
	refused := func(v types.List) []string {
		var resp validator.ListResponse
		for _, vd := range attr.Validators {
			vd.ValidateList(context.Background(), validator.ListRequest{
				ConfigValue: v, Path: tfpath.Root("allowed_methods"),
			}, &resp)
		}
		var msgs []string
		for _, d := range resp.Diagnostics.Errors() {
			msgs = append(msgs, d.Summary()+" "+d.Detail())
		}
		return msgs
	}

	for _, m := range []string{"TRACE", "TRACK", "CONNECT", "DEBUG"} {
		msgs := refused(strList("GET", m))
		if len(msgs) == 0 {
			t.Errorf("%s was accepted; the managed ruleset refuses it on every gateway and the "+
				"server rejects the write", m)
			continue
		}
		if !strings.Contains(strings.Join(msgs, " "), "TRACE, TRACK") {
			t.Errorf("the refusal of %s does not name the whole deny-list: %v", m, msgs)
		}
	}
	// The verbs a WebDAV or CalDAV application needs ARE listable -- refusing
	// them would reimplement the ceiling this deny-list replaced.
	for _, ok := range [][]string{
		{"GET", "HEAD"}, {"PROPFIND", "PROPPATCH", "MKCOL"}, {"REPORT", "SEARCH"},
		{"BASELINE-CONTROL"},
	} {
		if msgs := refused(strList(ok...)); len(msgs) != 0 {
			t.Errorf("%v was refused, but every method token except the four is listable: %v",
				ok, msgs)
		}
	}
	// Shape: the entry is rendered into the managed ruleset's own configuration
	// and the comparison is case-sensitive, so a lowercase entry is a control
	// that can never match.
	for _, bad := range [][]string{{"get"}, {"GET POST"}, {"GET;"}, {""}} {
		if msgs := refused(strList(bad...)); len(msgs) == 0 {
			t.Errorf("%v was accepted; the server refuses that shape", bad)
		}
	}
	// A duplicate is refused by the server, so it is refused here.
	if msgs := refused(strList("GET", "GET")); len(msgs) == 0 {
		t.Error("a duplicated method was accepted")
	}
}

// TestContentTypesAreRefusedByShape. Lowercase type/subtype with no parameters:
// the ruleset matches the type alone, so a parameterised entry is a control
// that never matches anything.
func TestContentTypesAreRefusedByShape(t *testing.T) {
	var sr resource.SchemaResponse
	NewResource().Schema(context.Background(), resource.SchemaRequest{}, &sr)
	attr, ok := sr.Schema.Attributes["allowed_request_content_types"].(schema.ListAttribute)
	if !ok {
		t.Fatal("allowed_request_content_types is not a ListAttribute")
	}
	bad := func(v types.List) bool {
		var resp validator.ListResponse
		for _, vd := range attr.Validators {
			vd.ValidateList(context.Background(), validator.ListRequest{
				ConfigValue: v, Path: tfpath.Root("allowed_request_content_types"),
			}, &resp)
		}
		return resp.Diagnostics.HasError()
	}
	for _, good := range [][]string{
		{"application/json"}, {"application/vnd.acme.v2+json"}, {"text/plain", "image/png"},
	} {
		if bad(strList(good...)) {
			t.Errorf("%v was refused, but it is an ordinary media type list", good)
		}
	}
	for _, wrong := range [][]string{
		{"application/json; charset=utf-8"}, {"Application/JSON"}, {"application"},
		{"application/json", "application/json"},
	} {
		if !bad(strList(wrong...)) {
			t.Errorf("%v was accepted; the server refuses it", wrong)
		}
	}
}

// TestTheAllowListsAreRefusedOnAnOverlay.
//
// The server refuses them at BOTH doors -- create checks whether the list is
// non-empty, update checks whether the field is PRESENT AT ALL, so even the
// `[]` that clears an override is refused there. Config validation covers both,
// because it refuses the attribute being set at all rather than any particular
// value.
func TestTheAllowListsAreRefusedOnAnOverlay(t *testing.T) {
	r := NewResource().(resource.ResourceWithValidateConfig)
	check := func(m PolicyModel) []string {
		p := planOf(t, m)
		var resp resource.ValidateConfigResponse
		r.ValidateConfig(context.Background(), resource.ValidateConfigRequest{
			Config: tfsdk.Config(p),
		}, &resp)
		var msgs []string
		for _, d := range resp.Diagnostics.Errors() {
			msgs = append(msgs, d.Summary()+" "+d.Detail())
		}
		return msgs
	}

	for _, attr := range []struct {
		name string
		set  func(*PolicyModel, types.List)
	}{
		{"allowed_methods", func(m *PolicyModel, v types.List) { m.AllowedMethods = v }},
		{"allowed_request_content_types", func(m *PolicyModel, v types.List) {
			m.AllowedRequestContentTypes = v
		}},
	} {
		t.Run(attr.name, func(t *testing.T) {
			overlay := wpModel()
			overlay.Scope = types.StringValue(scopeOverlay)
			attr.set(&overlay, strList("GET"))
			msgs := check(overlay)
			if len(msgs) == 0 {
				t.Fatalf("an overlay accepted %s; the server refuses it and the value would be "+
					"stored and never applied", attr.name)
			}
			if !strings.Contains(strings.Join(msgs, " "), "ONE managed ruleset") {
				t.Errorf("the refusal does not say why: %v", msgs)
			}

			// PRESENCE, not emptiness: `[]` is refused on an overlay update too.
			empty := wpModel()
			empty.Scope = types.StringValue(scopeOverlay)
			attr.set(&empty, types.ListValueMust(types.StringType, nil))
			if msgs := check(empty); len(msgs) == 0 {
				t.Errorf("an overlay accepted an empty %s; the server's update refusal is keyed "+
					"on the field being present at all", attr.name)
			}

			// The same list on a GATEWAY policy is exactly right, so the guard
			// is keyed on scope and not simply refusing the attribute.
			gw := wpModel()
			attr.set(&gw, strList("GET"))
			if msgs := check(gw); len(msgs) != 0 {
				t.Errorf("a gateway policy owns %s: %v", attr.name, msgs)
			}
		})
	}
}

// TestEffectiveListsAreComputedOnly. A practitioner cannot set what the server
// resolves; making one Optional would let a configuration assert a list the
// platform never agreed to, and then show no diff when reality differed.
func TestEffectiveListsAreComputedOnly(t *testing.T) {
	var sr resource.SchemaResponse
	NewResource().Schema(context.Background(), resource.SchemaRequest{}, &sr)
	for _, name := range []string{
		"effective_allowed_methods", "effective_allowed_request_content_types",
	} {
		a, ok := sr.Schema.Attributes[name]
		if !ok {
			t.Errorf("the schema has no %s, so nothing reports the list actually in force", name)
			continue
		}
		if !a.IsComputed() || a.IsOptional() || a.IsRequired() {
			t.Errorf("%s must be Computed-only, got computed=%v optional=%v required=%v",
				name, a.IsComputed(), a.IsOptional(), a.IsRequired())
		}
	}
	// And the settable pair is Optional and NOT Computed. Computed would make a
	// null config plan as unknown, which is how "removing the attribute does
	// nothing, silently and forever" gets built.
	for _, name := range []string{"allowed_methods", "allowed_request_content_types"} {
		a, ok := sr.Schema.Attributes[name]
		if !ok {
			t.Errorf("the schema has no %s", name)
			continue
		}
		if !a.IsOptional() {
			t.Errorf("%s must be Optional", name)
		}
		if a.IsComputed() {
			t.Errorf("%s is Computed; a null config would then plan as unknown, and removing the "+
				"attribute from a configuration would silently keep the override", name)
		}
	}
}

// TestThePlanIsHonestAboutTheEffectiveLists.
//
// Two failures, opposite in direction, and the plan has to avoid both:
//
//   - a KNOWN planned value that the apply contradicts is "Provider produced
//     inconsistent result after apply", an ERROR. Narrowing allowed_methods
//     changes effective_allowed_methods by definition, so leaving it pinned
//     would fail every such apply.
//   - a value left unknown when nothing decides it is a diff on a resource that
//     is not changing -- and for an overlay, whose effective lists are
//     permanently null, that diff never goes away.
func TestThePlanIsHonestAboutTheEffectiveLists(t *testing.T) {
	r := NewResource().(resource.ResourceWithModifyPlan)
	// What the framework hands ModifyPlan: every Computed attribute whose value
	// is null in the proposed new state has already been marked unknown.
	asFramework := func(m PolicyModel) PolicyModel {
		if m.EffectiveMode.IsNull() {
			m.EffectiveMode = types.StringUnknown()
		}
		if m.EffectiveAllowedMethods.IsNull() {
			m.EffectiveAllowedMethods = types.ListUnknown(types.StringType)
		}
		if m.EffectiveAllowedRequestContentTypes.IsNull() {
			m.EffectiveAllowedRequestContentTypes = types.ListUnknown(types.StringType)
		}
		return m
	}
	run := func(t *testing.T, plan, state PolicyModel) PolicyModel {
		t.Helper()
		resp := resource.ModifyPlanResponse{Plan: planOf(t, plan)}
		r.ModifyPlan(context.Background(), resource.ModifyPlanRequest{
			Plan: planOf(t, plan), State: stateOf(t, state),
		}, &resp)
		if resp.Diagnostics.HasError() {
			t.Fatalf("modify plan: %v", resp.Diagnostics.Errors())
		}
		var out PolicyModel
		resp.Plan.Get(context.Background(), &out)
		return out
	}

	t.Run("changing the override makes its effective list unknown", func(t *testing.T) {
		state := gatewayPolicy()
		plan := asFramework(state)
		plan.AllowedMethods = strList("GET")

		out := run(t, plan, state)
		if !out.EffectiveAllowedMethods.IsUnknown() {
			t.Fatalf("effective_allowed_methods = %v, want unknown: this apply changes it, and a "+
				"known planned value the apply contradicts fails with \"Provider produced "+
				"inconsistent result after apply\"", out.EffectiveAllowedMethods)
		}
		// The list that did NOT change keeps the value the refresh found.
		if out.EffectiveAllowedRequestContentTypes.IsUnknown() {
			t.Error("effective_allowed_request_content_types went unknown though nothing that " +
				"decides it changed")
		}
	})

	t.Run("changing the mode makes effective_mode unknown", func(t *testing.T) {
		state := gatewayPolicy()
		plan := asFramework(state)
		plan.Mode = types.StringValue(modeBlock)

		out := run(t, plan, state)
		if !out.EffectiveMode.IsUnknown() {
			t.Fatalf("effective_mode = %v, want unknown when the mode is changing", out.EffectiveMode)
		}
	})

	t.Run("an unrelated edit leaves all three alone", func(t *testing.T) {
		state := gatewayPolicy()
		plan := asFramework(state)
		plan.ParanoiaLevel = types.Int64Value(3)

		out := run(t, plan, state)
		if out.EffectiveMode.IsUnknown() || out.EffectiveAllowedMethods.IsUnknown() ||
			out.EffectiveAllowedRequestContentTypes.IsUnknown() {
			t.Fatalf("an unrelated edit left an effective value unknown, which reports a change "+
				"nobody made: mode=%v methods=%v types=%v",
				out.EffectiveMode, out.EffectiveAllowedMethods,
				out.EffectiveAllowedRequestContentTypes)
		}
		if !out.EffectiveAllowedMethods.Equal(state.EffectiveAllowedMethods) {
			t.Errorf("effective_allowed_methods = %v, want the refreshed %v",
				out.EffectiveAllowedMethods, state.EffectiveAllowedMethods)
		}
	})

	// 🔴 THE PERPETUAL-DIFF CASE. An overlay decides none of the three, so the
	// server sends none and they are permanently null -- and a null Computed
	// attribute is marked "(known after apply)" on every plan. Left there, the
	// resource shows an update forever and every apply sends an empty PATCH.
	t.Run("an overlay with nothing set plans no change at all", func(t *testing.T) {
		var state PolicyModel
		state.fromAPI(&apiPolicy{
			ID: "wp-2", GatewayID: "agw-1", Name: "api", Scope: scopeOverlay,
			Mode: modeInherit, FailMode: "open", RequestBodyLimitBytes: 8192,
		})
		plan := asFramework(state)

		out := run(t, plan, state)
		for name, got := range map[string]attr.Value{
			"effective_mode":                          out.EffectiveMode,
			"effective_allowed_methods":               out.EffectiveAllowedMethods,
			"effective_allowed_request_content_types": out.EffectiveAllowedRequestContentTypes,
		} {
			if got.IsUnknown() {
				t.Errorf("%s is still unknown on a policy with nothing to change, so every plan "+
					"reports an update and every apply sends an empty request", name)
			}
			if !got.IsNull() {
				t.Errorf("%s = %v, want the null the refresh recorded", name, got)
			}
		}
	})

	// A replacement is a different object; pinning a computed value to the old
	// one's state would describe what is being destroyed.
	// A replacement is a DIFFERENT object. Pinning a computed value to the old
	// one's state would describe what is being destroyed, so the framework's
	// unknown has to survive -- which is why the null-pinning above is gated on
	// every attribute that forces a replacement.
	t.Run("a replacement pins nothing", func(t *testing.T) {
		var state PolicyModel
		state.fromAPI(&apiPolicy{
			ID: "wp-2", GatewayID: "agw-1", Name: "api", Scope: scopeOverlay,
			Mode: modeInherit, FailMode: "open", RequestBodyLimitBytes: 8192,
		})
		for _, tc := range []struct {
			name string
			set  func(*PolicyModel)
		}{
			{"name", func(m *PolicyModel) { m.Name = types.StringValue("renamed") }},
			{"gateway_id", func(m *PolicyModel) { m.GatewayID = types.StringValue("agw-2") }},
			{"scope", func(m *PolicyModel) { m.Scope = types.StringValue(scopeGateway) }},
		} {
			t.Run(tc.name, func(t *testing.T) {
				plan := asFramework(state)
				tc.set(&plan)
				out := run(t, plan, state)
				if !out.EffectiveAllowedMethods.IsUnknown() {
					t.Errorf("effective_allowed_methods = %v on a replacement, want the unknown "+
						"the framework set: the new policy's list is not the old one's",
						out.EffectiveAllowedMethods)
				}
			})
		}
	})
}
