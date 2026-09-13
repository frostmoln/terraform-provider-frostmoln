package appgw_route

import (
	"context"
	"io"
	"net/http"
	"sort"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// The two shapes of the route API these tests hold the provider to.
//
// LEGACY is what appgw answers today: the header maps come back with their
// values. SEALED is what it answers once header values are secrets: only the
// NAMES come back, in requestHeadersSetNames / responseHeadersSetNames, and the
// value maps are absent. Routes are immutable (appgw registers POST, GET,
// DELETE and reorder only), so a sealed server can never report a value that
// differs from the one the route was created with — it simply stops saying.
//
// The secrets carry a quote and a backslash on purpose, so a leak rendered
// with %q would still be caught by a plain Contains check on the raw value's
// distinctive middle.
const (
	hdrSecret   = `Bearer sk-live-"sealed-canary"`
	hdrCanary   = "sealed-canary"
	hdrEnv      = "env-canary-7"
	hdrRespVal  = "no-store-canary-9"
	routeCommon = `"id":"rt-1","listenerId":"lsn-1","name":"api","priority":110,` +
		`"pathMatchType":"prefix","path":"/v1","action":"forward","backendPoolId":"pool-1",` +
		`"enabled":true,"createdAt":"2026-08-01T00:00:00Z"`

	sealedBody = `{` + routeCommon + `,` +
		`"requestHeadersSetNames":["Authorization","X-Env"],` +
		`"responseHeadersSetNames":["Cache-Control"]}`

	legacyBody = `{` + routeCommon + `,` +
		`"requestHeadersSet":{"Authorization":"Bearer sk-live-\"sealed-canary\"","X-Env":"env-canary-7"},` +
		`"responseHeadersSet":{"Cache-Control":"no-store-canary-9"}}`
)

// serveRoute answers every GET and POST on the route path with body.
func serveRoute(t *testing.T, body string) *routeResource {
	t.Helper()
	c, _ := serve(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == rtBase:
			w.WriteHeader(http.StatusAccepted)
			_, _ = io.WriteString(w, body)
		case r.Method == http.MethodGet && r.URL.Path == rtBase+"/rt-1":
			_, _ = io.WriteString(w, body)
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	})
	return &routeResource{client: c}
}

// configured is the route as a practitioner wrote it: both maps set.
func configured(t *testing.T) RouteModel {
	t.Helper()
	m := rtModel()
	m.ID = types.StringValue("rt-1")
	m.PathMatchType = types.StringValue("prefix")
	m.Path = types.StringValue("/v1")
	m.RequestHeadersSet = mapValue(t, map[string]string{"Authorization": hdrSecret, "X-Env": hdrEnv})
	m.ResponseHeadersSet = mapValue(t, map[string]string{"Cache-Control": hdrRespVal})
	return m
}

func readWith(t *testing.T, rr *routeResource, prior tfsdk.State) (RouteModel, diag.Diagnostics) {
	t.Helper()
	resp := resource.ReadResponse{State: prior}
	rr.Read(context.Background(), resource.ReadRequest{State: prior}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("read: %v", resp.Diagnostics.Errors())
	}
	var got RouteModel
	if d := resp.State.Get(context.Background(), &got); d.HasError() {
		t.Fatalf("state: %v", d.Errors())
	}
	return got, resp.Diagnostics
}

func mapOf(t *testing.T, m types.Map) map[string]string {
	t.Helper()
	if m.IsNull() {
		return nil
	}
	out := map[string]string{}
	if d := m.ElementsAs(context.Background(), &out, false); d.HasError() {
		t.Fatalf("elements: %v", d)
	}
	return out
}

func keysOf(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// 🔴 THE TRAP THIS WHOLE CHANGE EXISTS FOR: A NAMES-ONLY SERVER MUST NOT
// PRODUCE A DIFF.
//
// Before this change Read overwrote both maps from the response. Against a
// server that returns only the names, that wrote null over the configured
// values; the next plan saw config != state on a RequiresReplace attribute and
// REPLACED every header route on every apply, forever. The refreshed state has
// to equal the prior state exactly, because the configuration equals the prior
// state — that equality is what "no diff" means for this resource.
func TestRouteReadKeepsHeaderValuesWhenTheServerReturnsOnlyNames(t *testing.T) {
	rr := serveRoute(t, sealedBody)
	prior := configured(t)

	got, diags := readWith(t, rr, stateOf(t, prior))

	if !got.RequestHeadersSet.Equal(prior.RequestHeadersSet) {
		t.Errorf("request_headers_set = %v after a names-only refresh, want the prior state's value "+
			"kept; anything else is a replacement of this route on every apply", keysOf(mapOf(t, got.RequestHeadersSet)))
	}
	if !got.ResponseHeadersSet.Equal(prior.ResponseHeadersSet) {
		t.Errorf("response_headers_set = %v after a names-only refresh, want the prior state's value "+
			"kept", keysOf(mapOf(t, got.ResponseHeadersSet)))
	}
	if len(diags.Warnings()) != 0 {
		t.Errorf("an unchanged route must refresh silently, got %v", diags.Warnings())
	}
}

// Against today's server nothing changes: the values come back, and they are
// what state records — so a value that differs on the server (which only a
// recreated route could produce) still shows as drift, exactly as before.
func TestRouteReadStillUsesValuesWhenTheServerReturnsThem(t *testing.T) {
	rr := serveRoute(t, legacyBody)
	prior := configured(t)
	prior.RequestHeadersSet = mapValue(t, map[string]string{"Authorization": "stale", "X-Env": hdrEnv})

	got, diags := readWith(t, rr, stateOf(t, prior))

	want := map[string]string{"Authorization": hdrSecret, "X-Env": hdrEnv}
	if g := mapOf(t, got.RequestHeadersSet); len(g) != len(want) || g["Authorization"] != want["Authorization"] ||
		g["X-Env"] != want["X-Env"] {
		t.Errorf("request_headers_set did not take the server's values; drift detection on values "+
			"against a server that still returns them is lost (keys %v)", keysOf(g))
	}
	if g := mapOf(t, got.ResponseHeadersSet); g["Cache-Control"] != hdrRespVal {
		t.Errorf("response_headers_set did not take the server's value")
	}
	if len(diags.Warnings()) != 0 {
		t.Errorf("a server that returns values needs no warning, got %v", diags.Warnings())
	}
}

// When the server's NAME set differs from state — impossible through the API,
// which has no route update, but reachable through a platform-side scrub or a
// partial migration — the refreshed state must differ from the configuration so
// that the plan REPLACES the route and re-sends the configured values. Keeping
// the prior values would hide that the server no longer holds them.
//
// A name the server reports that state never had gets the empty string: the
// one value no configuration can hold (ValidateConfig and the server both
// refuse an empty header value), so the diff is guaranteed.
func TestRouteReadPlansAReplacementWhenTheServerNameSetDiffers(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
		want map[string]string
	}{
		{
			name: "server reports a name state lacks",
			body: `{` + routeCommon + `,"requestHeadersSetNames":["Authorization","X-Env","X-Extra"],` +
				`"responseHeadersSetNames":["Cache-Control"]}`,
			want: map[string]string{"Authorization": hdrSecret, "X-Env": hdrEnv, "X-Extra": ""},
		},
		{
			name: "server lost a name state has",
			body: `{` + routeCommon + `,"requestHeadersSetNames":["Authorization"],` +
				`"responseHeadersSetNames":["Cache-Control"]}`,
			want: map[string]string{"Authorization": hdrSecret},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rr := serveRoute(t, tc.body)
			prior := configured(t)

			got, diags := readWith(t, rr, stateOf(t, prior))

			g := mapOf(t, got.RequestHeadersSet)
			if strings.Join(keysOf(g), ",") != strings.Join(keysOf(tc.want), ",") {
				t.Fatalf("request_headers_set names = %v, want the server's %v", keysOf(g), keysOf(tc.want))
			}
			for k, v := range tc.want {
				if g[k] != v {
					t.Errorf("request_headers_set[%q] is not the expected kept-or-placeholder value", k)
				}
			}
			if got.RequestHeadersSet.Equal(prior.RequestHeadersSet) {
				t.Error("state equals the configuration, so the plan shows no replacement for a route " +
					"whose header names changed on the server")
			}
			if !got.ResponseHeadersSet.Equal(prior.ResponseHeadersSet) {
				t.Error("the unchanged response map must still be kept")
			}
			if len(diags.Warnings()) == 0 {
				t.Error("a name-set mismatch must say why the route is about to be replaced")
			}
			assertNoValueIn(t, diags)
		})
	}
}

// A names-only server that reports no names at all means the route has no
// headers — the same meaning an absent map has today — so state goes null and a
// configuration that sets headers plans a replacement.
func TestRouteReadTreatsNoNamesAndNoValuesAsNoHeaders(t *testing.T) {
	rr := serveRoute(t, `{`+routeCommon+`}`)
	got, _ := readWith(t, rr, stateOf(t, configured(t)))
	if !got.RequestHeadersSet.IsNull() || !got.ResponseHeadersSet.IsNull() {
		t.Error("a route the server reports with no headers must refresh to null maps")
	}
}

// Create reads back the same response. Against a names-only server the state
// written after apply must be the planned values — otherwise Terraform aborts
// the apply with "Provider produced inconsistent result after apply" and the
// route exists on the platform but not in state.
func TestRouteCreateKeepsPlannedValuesWhenTheServerReturnsOnlyNames(t *testing.T) {
	rr := serveRoute(t, sealedBody)
	plan := configured(t)
	plan.ID = types.StringUnknown()

	resp := resource.CreateResponse{State: emptyState(t)}
	rr.Create(context.Background(), resource.CreateRequest{Plan: planOf(t, plan)}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("create: %v", resp.Diagnostics.Errors())
	}
	var got RouteModel
	resp.State.Get(context.Background(), &got)
	if !got.RequestHeadersSet.Equal(plan.RequestHeadersSet) || !got.ResponseHeadersSet.Equal(plan.ResponseHeadersSet) {
		t.Errorf("created state's header maps (%v / %v) are not the planned values",
			keysOf(mapOf(t, got.RequestHeadersSet)), keysOf(mapOf(t, got.ResponseHeadersSet)))
	}
}

// Import against a names-only server cannot recover a value: the API does not
// have one to give. The honest result is a state carrying the names with
// placeholder values — so the next plan REPLACES the route and writes the
// configured values — plus a warning that says so. Against today's server the
// values come back and the import is clean.
func TestRouteImportCannotRecoverValuesFromANamesOnlyServer(t *testing.T) {
	importThenRead := func(t *testing.T, body string) (RouteModel, diag.Diagnostics) {
		t.Helper()
		r := NewResource().(resource.ResourceWithImportState)
		imp := resource.ImportStateResponse{State: importState(t)}
		r.ImportState(context.Background(), resource.ImportStateRequest{ID: "agw-1/lsn-1/rt-1"}, &imp)
		if imp.Diagnostics.HasError() {
			t.Fatalf("import: %v", imp.Diagnostics.Errors())
		}
		return readWith(t, serveRoute(t, body), imp.State)
	}

	t.Run("sealed", func(t *testing.T) {
		got, diags := importThenRead(t, sealedBody)
		req, resp := mapOf(t, got.RequestHeadersSet), mapOf(t, got.ResponseHeadersSet)
		if strings.Join(keysOf(req), ",") != "Authorization,X-Env" || strings.Join(keysOf(resp), ",") != "Cache-Control" {
			t.Fatalf("imported names = %v / %v, want the server's", keysOf(req), keysOf(resp))
		}
		for k, v := range req {
			if v != "" {
				t.Errorf("imported request_headers_set[%q] is not the placeholder", k)
			}
		}
		if configured(t).RequestHeadersSet.Equal(got.RequestHeadersSet) {
			t.Error("the imported state equals a real configuration; the plan would adopt a route " +
				"whose values nobody has seen")
		}
		if len(diags.Warnings()) == 0 {
			t.Error("an import that cannot recover the header values must warn")
		}
		assertNoValueIn(t, diags)
	})

	t.Run("legacy", func(t *testing.T) {
		got, diags := importThenRead(t, legacyBody)
		if mapOf(t, got.RequestHeadersSet)["Authorization"] != hdrSecret {
			t.Error("a server that returns values must import them, as it always has")
		}
		if len(diags.Warnings()) != 0 {
			t.Errorf("a clean import must not warn, got %v", diags.Warnings())
		}
	})
}

// Both maps hold what is usually an upstream credential, so neither may print
// in plan output.
func TestRouteHeaderMapsAreSensitive(t *testing.T) {
	s := schemaOf(t).Schema
	for _, name := range []string{"request_headers_set", "response_headers_set"} {
		if !s.GetAttributes()[name].IsSensitive() {
			t.Errorf("%s is not Sensitive; its values land in plan output and CI logs", name)
		}
	}
}

func assertNoValueIn(t *testing.T, diags diag.Diagnostics) {
	t.Helper()
	for _, d := range diags {
		for _, v := range []string{hdrCanary, hdrEnv, hdrRespVal} {
			if strings.Contains(d.Summary(), v) || strings.Contains(d.Detail(), v) {
				t.Errorf("a diagnostic carries a header VALUE: %s / %s", d.Summary(), d.Detail())
			}
		}
	}
}
