package appgw_route

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	tfpath "github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
)

// schemaOf returns this resource's schema, which every plan and state in these
// tests is built against.
func schemaOf(t *testing.T) tfsdk.Plan {
	t.Helper()
	r := NewResource()
	var sr resource.SchemaResponse
	r.Schema(context.Background(), resource.SchemaRequest{}, &sr)
	if sr.Diagnostics.HasError() {
		t.Fatalf("schema: %v", sr.Diagnostics.Errors())
	}
	return tfsdk.Plan{Schema: sr.Schema}
}

func planOf(t *testing.T, m RouteModel) tfsdk.Plan {
	t.Helper()
	p := schemaOf(t)
	if d := p.Set(context.Background(), &m); d.HasError() {
		t.Fatalf("plan: %v", d.Errors())
	}
	return p
}

func stateOf(t *testing.T, m RouteModel) tfsdk.State {
	t.Helper()
	p := schemaOf(t)
	s := tfsdk.State{Schema: p.Schema}
	if d := s.Set(context.Background(), &m); d.HasError() {
		t.Fatalf("state: %v", d.Errors())
	}
	return s
}

// emptyState is what Create writes into.
func emptyState(t *testing.T) tfsdk.State {
	t.Helper()
	return tfsdk.State{Schema: schemaOf(t).Schema}
}

// serve builds a client pointed at a handler, with the tenant pre-resolved.
func serve(t *testing.T, h http.HandlerFunc) (*client.Client, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	c := client.NewClient(srv.URL, "test-key", client.WithHTTPClient(srv.Client())) // pragma: allowlist secret
	c.SetTenantIDForTest("t-1")
	return c, srv
}

// TestConfigureRejectsUnexpectedProviderData. A provider that hands the wrong
// type must be a clear error, not a nil-pointer panic on the first API call.
func TestConfigureRejectsUnexpectedProviderData(t *testing.T) {
	r := NewResource().(interface {
		Configure(context.Context, resource.ConfigureRequest, *resource.ConfigureResponse)
	})
	var resp resource.ConfigureResponse
	r.Configure(context.Background(), resource.ConfigureRequest{}, &resp)
	if resp.Diagnostics.HasError() {
		t.Errorf("a nil ProviderData is the not-yet-configured case and must be silent: %v",
			resp.Diagnostics.Errors())
	}
	r.Configure(context.Background(), resource.ConfigureRequest{ProviderData: 42}, &resp)
	if !resp.Diagnostics.HasError() {
		t.Error("the wrong ProviderData type must be an error, not a later panic")
	}
}

var (
	_ = json.Marshal
	_ = types.StringValue
)

const rtBase = "/v1/tenants/t-1/application-gateways/agw-1/listeners/lsn-1/routes"

func rtFixture() apiRoute {
	return apiRoute{
		ID: "rt-1", ListenerID: "lsn-1", Name: "api", Priority: 110,
		PathMatchType: "prefix", Path: "/v1", Action: "forward", BackendPoolID: "pool-1",
		Enabled: true, CreatedAt: "2026-08-01T00:00:00Z",
	}
}

// mapValue / listValue build the framework collection types the header
// validators read. Fatal on error rather than returning one: a fixture that
// silently became null would make every assertion below vacuous.
func mapValue(t *testing.T, m map[string]string) types.Map {
	t.Helper()
	v, d := types.MapValueFrom(context.Background(), types.StringType, m)
	if d.HasError() {
		t.Fatalf("building the header map fixture: %v", d)
	}
	return v
}

func listValue(t *testing.T, l []string) types.List {
	t.Helper()
	v, d := types.ListValueFrom(context.Background(), types.StringType, l)
	if d.HasError() {
		t.Fatalf("building the header list fixture: %v", d)
	}
	return v
}

func rtModel() RouteModel {
	return RouteModel{
		GatewayID:            types.StringValue("agw-1"),
		ListenerID:           types.StringValue("lsn-1"),
		Name:                 types.StringValue("api"),
		BackendPoolID:        types.StringValue("pool-1"),
		Priority:             types.Int64Null(),
		RequestHeadersSet:    types.MapNull(types.StringType),
		ResponseHeadersSet:   types.MapNull(types.StringType),
		RequestHeadersRemove: types.ListNull(types.StringType),
	}
}

func TestRouteCreateReadDelete(t *testing.T) {
	c, _ := serve(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == rtBase:
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(rtFixture())
		case r.Method == http.MethodGet && r.URL.Path == rtBase+"/rt-1":
			_ = json.NewEncoder(w).Encode(rtFixture())
		case r.Method == http.MethodDelete && r.URL.Path == rtBase+"/rt-1":
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	})
	rr := &routeResource{client: c}

	createResp := resource.CreateResponse{State: emptyState(t)}
	rr.Create(context.Background(), resource.CreateRequest{Plan: planOf(t, rtModel())}, &createResp)
	if createResp.Diagnostics.HasError() {
		t.Fatalf("create: %v", createResp.Diagnostics.Errors())
	}
	var created RouteModel
	createResp.State.Get(context.Background(), &created)
	if created.ID.ValueString() != "rt-1" {
		t.Fatalf("id = %q", created.ID.ValueString())
	}
	// gateway_id is not in the route payload (a route belongs to a listener),
	// so it must be carried over from the plan rather than blanked.
	if created.GatewayID.ValueString() != "agw-1" {
		t.Fatalf("gateway_id = %q; the route payload does not carry it, so it must survive from "+
			"the plan", created.GatewayID.ValueString())
	}
	if created.Priority.ValueInt64() != 110 {
		t.Errorf("priority = %d, want the server-assigned 110", created.Priority.ValueInt64())
	}

	readResp := resource.ReadResponse{State: stateOf(t, created)}
	rr.Read(context.Background(), resource.ReadRequest{State: stateOf(t, created)}, &readResp)
	if readResp.Diagnostics.HasError() {
		t.Fatalf("read: %v", readResp.Diagnostics.Errors())
	}
	var refreshed RouteModel
	readResp.State.Get(context.Background(), &refreshed)
	if refreshed.GatewayID.ValueString() != "agw-1" {
		t.Error("read blanked gateway_id")
	}

	delResp := resource.DeleteResponse{State: stateOf(t, created)}
	rr.Delete(context.Background(), resource.DeleteRequest{State: stateOf(t, created)}, &delResp)
	if delResp.Diagnostics.HasError() {
		t.Fatalf("delete: %v", delResp.Diagnostics.Errors())
	}
}

func TestRouteReadRemovesAVanishedResource(t *testing.T) {
	c, _ := serve(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]string{"code": "NOT_FOUND", "message": "gone"})
	})
	rr := &routeResource{client: c}
	m := rtModel()
	m.ID = types.StringValue("rt-1")
	resp := resource.ReadResponse{State: stateOf(t, m)}
	rr.Read(context.Background(), resource.ReadRequest{State: stateOf(t, m)}, &resp)
	if resp.Diagnostics.HasError() || !resp.State.Raw.IsNull() {
		t.Fatal("a 404 must remove the route from state without erroring")
	}
}

func TestRouteUpdateIsRefused(t *testing.T) {
	var resp resource.UpdateResponse
	(&routeResource{}).Update(context.Background(), resource.UpdateRequest{}, &resp)
	if !resp.Diagnostics.HasError() {
		t.Fatal("Update must refuse: the API has no route update")
	}
}

// TestRouteValidateConfigCompilesTheRegexLocally. A bad pattern found at apply
// time has usually already created the gateway and the listener, leaving a
// half-built stack to unpick over a typo.
func TestRouteValidateConfigCompilesTheRegexLocally(t *testing.T) {
	r := NewResource().(resource.ResourceWithValidateConfig)
	check := func(m RouteModel) bool {
		p := planOf(t, m)
		var resp resource.ValidateConfigResponse
		r.ValidateConfig(context.Background(), resource.ValidateConfigRequest{
			Config: tfsdk.Config(p),
		}, &resp)
		return resp.Diagnostics.HasError()
	}

	bad := rtModel()
	bad.PathMatchType = types.StringValue("regex")
	bad.Path = types.StringValue("([unclosed")
	if !check(bad) {
		t.Error("an uncompilable regex must be refused at plan time")
	}

	missing := rtModel()
	missing.PathMatchType = types.StringValue("regex")
	if !check(missing) {
		t.Error("regex matching with no path must be refused")
	}

	good := rtModel()
	good.PathMatchType = types.StringValue("regex")
	good.Path = types.StringValue("^/v[0-9]+/")
	if check(good) {
		t.Error("a valid RE2 pattern was refused")
	}
}

// TestImportState pins the composite id format.
//
// An import id is the only interface a practitioner has for adopting an
// existing resource, and getting it wrong silently produces a resource with
// empty addressing that fails on its first refresh with an unrelated error.
// The malformed cases must be refused by name.
func TestImportState(t *testing.T) {
	r := NewResource().(resource.ResourceWithImportState)

	run := func(id string) (resource.ImportStateResponse, bool) {
		resp := resource.ImportStateResponse{State: importState(t)}
		r.ImportState(context.Background(), resource.ImportStateRequest{ID: id}, &resp)
		return resp, resp.Diagnostics.HasError()
	}

	for _, bad := range []string{"", "agw-1", "agw-1/lsn-1", "agw-1//rt-1", "a/b/c/d"} {
		if _, err := run(bad); !err {
			t.Errorf("import id %q was accepted; the format is %s", bad, "{gateway_id}/{listener_id}/{route_id}")
		}
	}

	resp, err := run("agw-1/lsn-1/rt-1")
	if err {
		t.Fatalf("a well-formed import id was refused: %v", resp.Diagnostics.Errors())
	}
	{
		var got types.String
		if d := resp.State.GetAttribute(context.Background(), tfpath.Root("gateway_id"), &got); d.HasError() {
			t.Fatalf("read gateway_id: %v", d.Errors())
		}
		if got.ValueString() != "agw-1" {
			t.Errorf("gateway_id = %q, want agw-1", got.ValueString())
		}
	}
	{
		var got types.String
		if d := resp.State.GetAttribute(context.Background(), tfpath.Root("listener_id"), &got); d.HasError() {
			t.Fatalf("read listener_id: %v", d.Errors())
		}
		if got.ValueString() != "lsn-1" {
			t.Errorf("listener_id = %q, want lsn-1", got.ValueString())
		}
	}
	{
		var got types.String
		if d := resp.State.GetAttribute(context.Background(), tfpath.Root("id"), &got); d.HasError() {
			t.Fatalf("read id: %v", d.Errors())
		}
		if got.ValueString() != "rt-1" {
			t.Errorf("id = %q, want rt-1", got.ValueString())
		}
	}
}

// importState is the state Terraform hands ImportState: an object whose every
// attribute is NULL, not the zero tfsdk.State — writing an attribute into the
// latter fails, because there is no object to write into.
//
// Derived from the schema rather than hand-listed, so adding an attribute does
// not silently leave it out.
func importState(t *testing.T) tfsdk.State {
	t.Helper()
	s := schemaOf(t).Schema
	obj := s.Type().TerraformType(context.Background()).(tftypes.Object)
	attrs := make(map[string]tftypes.Value, len(obj.AttributeTypes))
	for name, at := range obj.AttributeTypes {
		attrs[name] = tftypes.NewValue(at, nil)
	}
	return tfsdk.State{Schema: s, Raw: tftypes.NewValue(obj, attrs)}
}

// 🔴 THE HEADER MAPS WERE NEVER VALIDATED, ON ANY ROUTE THAT WAS NOT A REGEX
// ROUTE — WHICH IS ALMOST ALL OF THEM.
//
// ValidateConfig returned early at `path_match_type != "regex"`, above the
// header checks, so a prefix or exact route's headers went straight to apply.
// The failure that produces is the one plan-time validation exists to prevent:
// the gateway and the listener are already created, the route 400s, and the
// practitioner unpicks a half-built stack over a typo in a header name.
//
// Every case below is refused by the SERVER (appgw internal/domain/wire.go);
// the assertion is that the practitioner learns it at plan time instead.
func TestRouteValidateConfigChecksHeadersOnEveryMatchType(t *testing.T) {
	r := NewResource().(resource.ResourceWithValidateConfig)
	check := func(m RouteModel) bool {
		p := planOf(t, m)
		var resp resource.ValidateConfigResponse
		r.ValidateConfig(context.Background(), resource.ValidateConfigRequest{
			Config: tfsdk.Config(p),
		}, &resp)
		return resp.Diagnostics.HasError()
	}
	// A PREFIX route — the case the early return skipped entirely.
	withRequestHeaders := func(h map[string]string) RouteModel {
		m := rtModel()
		m.PathMatchType = types.StringValue("prefix")
		m.Path = types.StringValue("/")
		m.RequestHeadersSet = mapValue(t, h)
		return m
	}

	for _, tc := range []struct {
		name    string
		headers map[string]string
	}{
		{"a space in the name", map[string]string{"X Forwarded": "1"}},
		{"a colon in the name", map[string]string{"X-Trace:": "1"}},
		{"an empty name", map[string]string{"": "1"}},
		// Legal HTTP tokens, refused because the renderer cannot express them:
		// '#' opens a comment and an apostrophe opens a quote, either of which
		// makes the proxy refuse its WHOLE configuration.
		{"a '#' in the name", map[string]string{"X-Rate#Limit": "1"}},
		{"an apostrophe in the name", map[string]string{"X-Don't": "1"}},
		{"an empty value", map[string]string{"X-Trace": ""}},
		{"a newline in the value", map[string]string{"X-Trace": "a\nb"}},
		{"an over-long value", map[string]string{"X-Trace": strings.Repeat("a", 1025)}},
	} {
		if !check(withRequestHeaders(tc.headers)) {
			t.Errorf("%s: accepted at plan time on a prefix route; the server refuses it, "+
				"so the practitioner meets it at apply with the stack half built", tc.name)
		}
	}

	// The converse, so the test cannot pass by refusing everything.
	ok := withRequestHeaders(map[string]string{
		"X-Forwarded-Host": "app.example.test",
		// A value that is JSON, which an earlier server rule wrongly refused —
		// Report-To and NEL are both JSON, and they must still be settable.
		"Report-To": `{"group":"csp","max_age":86400}`,
	})
	if check(ok) {
		t.Error("a valid header map was refused; a client stricter than the server refuses " +
			"a configuration the platform would accept")
	}
}

// The response map and the remove list go through the same rules — a check that
// covered only the request map would leave two of the three collections open.
func TestRouteValidateConfigChecksEveryHeaderCollection(t *testing.T) {
	r := NewResource().(resource.ResourceWithValidateConfig)
	check := func(m RouteModel) bool {
		p := planOf(t, m)
		var resp resource.ValidateConfigResponse
		r.ValidateConfig(context.Background(), resource.ValidateConfigRequest{
			Config: tfsdk.Config(p),
		}, &resp)
		return resp.Diagnostics.HasError()
	}

	badResponse := rtModel()
	badResponse.PathMatchType = types.StringValue("prefix")
	badResponse.Path = types.StringValue("/")
	badResponse.ResponseHeadersSet = mapValue(t, map[string]string{"X Bad": "1"})
	if !check(badResponse) {
		t.Error("response_headers_set was not validated")
	}

	badRemove := rtModel()
	badRemove.PathMatchType = types.StringValue("prefix")
	badRemove.Path = types.StringValue("/")
	badRemove.RequestHeadersRemove = listValue(t, []string{"X Bad"})
	if !check(badRemove) {
		t.Error("request_headers_remove was not validated")
	}
}

// 🔴 A DIAGNOSTIC MUST NAME THE HEADER AND NEVER THE VALUE.
//
// The server refuses this way for a stated reason — a header value is where a
// tenant's upstream credential lives — and a Terraform diagnostic is a WORSE
// place to leak one than an API error: it lands in plan output, in CI logs, and
// in whatever captures them.
func TestRouteValidateConfigNeverEchoesAHeaderValue(t *testing.T) {
	// 🔴 THE SECRET CARRIES A QUOTE ON PURPOSE. A leak rendered with %q would
	// escape it, so a Contains check against a plain marker would MISS the very
	// leak it is looking for.
	const secret = `Bearer sk-live-"do-not-log-me"`
	r := NewResource().(resource.ResourceWithValidateConfig)

	// EVERY refusal branch, not just the one the first version covered: a bad
	// name, and each of the three value rules. The code is airtight today; this
	// is what notices when a later edit adds the value to one message.
	for _, tc := range []struct {
		name    string
		headers map[string]string
	}{
		{"bad name", map[string]string{"X Bad": secret}},
		{"control character", map[string]string{"X-Trace": secret + "\n"}},
		{"over length", map[string]string{"X-Trace": secret + strings.Repeat("x", 1024)}},
		// The empty-value branch cannot carry a secret by construction, but it
		// is included so the table matches the switch it guards.
		{"empty value", map[string]string{"X-Trace": ""}},
	} {
		m := rtModel()
		m.PathMatchType = types.StringValue("prefix")
		m.Path = types.StringValue("/")
		m.RequestHeadersSet = mapValue(t, tc.headers)

		p := planOf(t, m)
		var resp resource.ValidateConfigResponse
		r.ValidateConfig(context.Background(), resource.ValidateConfigRequest{
			Config: tfsdk.Config(p),
		}, &resp)
		if !resp.Diagnostics.HasError() {
			t.Fatalf("%s: the fixture must be refused, or this assertion is vacuous", tc.name)
		}
		for _, d := range resp.Diagnostics.Errors() {
			if strings.Contains(d.Summary(), secret) || strings.Contains(d.Detail(), secret) {
				t.Errorf("%s: a diagnostic echoed the header VALUE into plan output:\n  %s\n  %s",
					tc.name, d.Summary(), d.Detail())
			}
		}
	}
}

// 🔴 AN UNKNOWN VALUE MUST NOT ABANDON THE WHOLE MAP.
//
// The first version converted the map with ElementsAs, which errors on ANY
// unknown element — so one value interpolated from another resource made the
// validator return having checked nothing, and the bad NAME beside it sailed
// through to apply. That is not a rare configuration; it is the ordinary one.
//
// A map KEY can never be unknown, so every name is checkable even when a value
// is not. Only the individual unknown value is deferred to the server.
func TestRouteValidateConfigChecksNamesEvenWhenAValueIsUnknown(t *testing.T) {
	r := NewResource().(resource.ResourceWithValidateConfig)

	m := rtModel()
	m.PathMatchType = types.StringValue("prefix")
	m.Path = types.StringValue("/")
	// A bad name beside a value Terraform cannot resolve at plan time.
	m.RequestHeadersSet = types.MapValueMust(types.StringType, map[string]attr.Value{
		"X Forwarded":   types.StringValue("1"),
		"Authorization": types.StringUnknown(),
	})

	p := planOf(t, m)
	var resp resource.ValidateConfigResponse
	r.ValidateConfig(context.Background(), resource.ValidateConfigRequest{
		Config: tfsdk.Config(p),
	}, &resp)

	if !resp.Diagnostics.HasError() {
		t.Error("an unknown value beside a bad header NAME abandoned the whole map; the " +
			"name reaches apply, which is the half-built-stack failure this validator exists " +
			"to prevent, in the most ordinary config there is")
	}
}

// And the converse, which is the regression the fix could introduce: an unknown
// value must not be REFUSED. Treating unknown as an empty string would fail
// every configuration that interpolates a header value, with no workaround.
func TestRouteValidateConfigAcceptsAnUnknownHeaderValue(t *testing.T) {
	r := NewResource().(resource.ResourceWithValidateConfig)

	m := rtModel()
	m.PathMatchType = types.StringValue("prefix")
	m.Path = types.StringValue("/")
	m.RequestHeadersSet = types.MapValueMust(types.StringType, map[string]attr.Value{
		"Authorization": types.StringUnknown(),
	})
	m.RequestHeadersRemove = types.ListValueMust(types.StringType, []attr.Value{
		types.StringUnknown(),
	})

	p := planOf(t, m)
	var resp resource.ValidateConfigResponse
	r.ValidateConfig(context.Background(), resource.ValidateConfigRequest{
		Config: tfsdk.Config(p),
	}, &resp)

	if resp.Diagnostics.HasError() {
		t.Errorf("an unknown header value was refused at plan time; every configuration "+
			"that interpolates one would break with no workaround: %v", resp.Diagnostics.Errors())
	}
}

// 🔴 THE ACCEPTED CHARACTER SET IS PINNED, because narrowing it is invisible.
//
// The client's regexp is the server's verbatim, and the server's own test
// enumerates this set because it was MEASURED against HAProxy 2.8/3.0/3.2.
// Drop `^`, the backtick or `|~` from the class and every other test here still
// passes while the provider starts refusing names the platform accepts — a
// client stricter than the server, which is the failure the copied-constant
// comment is about.
func TestRouteValidateConfigAcceptsEveryTokenCharacterTheServerDoes(t *testing.T) {
	r := NewResource().(resource.ResourceWithValidateConfig)
	check := func(m RouteModel) bool {
		p := planOf(t, m)
		var resp resource.ValidateConfigResponse
		r.ValidateConfig(context.Background(), resource.ValidateConfigRequest{
			Config: tfsdk.Config(p),
		}, &resp)
		return resp.Diagnostics.HasError()
	}
	// Every RFC 7230 token character EXCEPT '#' and the apostrophe, which the
	// gateway cannot render and which are refused on purpose.
	for _, name := range []string{
		"X-Bang!", "X-Dollar$", "X-Percent%", "X-Amp&", "X-Star*", "X-Plus+",
		"X-Dash-", "X-Dot.", "X-Caret^", "X-Under_", "X-Tick`", "X-Pipe|", "X-Tilde~",
		"X-Digits0123456789", "lowercase-name",
	} {
		m := rtModel()
		m.PathMatchType = types.StringValue("prefix")
		m.Path = types.StringValue("/")
		m.RequestHeadersSet = mapValue(t, map[string]string{name: "v"})
		if check(m) {
			t.Errorf("%q was refused; the server accepts it, so this provider is stricter "+
				"than the platform and refuses a valid configuration", name)
		}
	}
}

// The length bound is the SERVER's 1024, on BYTES, and the boundary is exact.
// Literals rather than the package constant: reading our own constant would
// move the test with a drifting value and never observe the divergence.
func TestRouteValidateConfigLengthBoundMatchesTheServerExactly(t *testing.T) {
	r := NewResource().(resource.ResourceWithValidateConfig)
	check := func(value string) bool {
		m := rtModel()
		m.PathMatchType = types.StringValue("prefix")
		m.Path = types.StringValue("/")
		m.RequestHeadersSet = mapValue(t, map[string]string{"X-Trace": value})
		p := planOf(t, m)
		var resp resource.ValidateConfigResponse
		r.ValidateConfig(context.Background(), resource.ValidateConfigRequest{
			Config: tfsdk.Config(p),
		}, &resp)
		return resp.Diagnostics.HasError()
	}
	if check(strings.Repeat("a", 1024)) {
		t.Error("1024 bytes was refused; the server accepts it (its comparison is `>`)")
	}
	if !check(strings.Repeat("a", 1025)) {
		t.Error("1025 bytes was accepted; the server refuses it")
	}
}

// Positive cases for the other two collections, so a validator that refused
// EVERYTHING in them would not pass.
func TestRouteValidateConfigAcceptsValidValuesInEveryCollection(t *testing.T) {
	r := NewResource().(resource.ResourceWithValidateConfig)
	m := rtModel()
	m.PathMatchType = types.StringValue("prefix")
	m.Path = types.StringValue("/")
	m.RequestHeadersSet = mapValue(t, map[string]string{"X-Request-Id": "abc"})
	m.ResponseHeadersSet = mapValue(t, map[string]string{"X-Frame-Options": "DENY"})
	m.RequestHeadersRemove = listValue(t, []string{"X-Internal-Token", "Server"})

	p := planOf(t, m)
	var resp resource.ValidateConfigResponse
	r.ValidateConfig(context.Background(), resource.ValidateConfigRequest{
		Config: tfsdk.Config(p),
	}, &resp)
	if resp.Diagnostics.HasError() {
		t.Errorf("a valid configuration was refused: %v", resp.Diagnostics.Errors())
	}
}
