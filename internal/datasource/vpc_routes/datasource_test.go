package vpc_routes

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
)

const (
	testRoutesPath = "/v1/tenants/t-123/vpcs/vpc-123/routes"
	testVPCPath    = "/v1/tenants/t-123/vpcs/vpc-123"
)

// --- schema, metadata, configure ---

func TestMetadata(t *testing.T) {
	ds := NewDataSource()
	req := datasource.MetadataRequest{ProviderTypeName: "frostmoln"}
	var resp datasource.MetadataResponse
	ds.Metadata(context.Background(), req, &resp)
	if resp.TypeName != "frostmoln_vpc_routes" {
		t.Errorf("expected frostmoln_vpc_routes, got %s", resp.TypeName)
	}
}

func listingSchema(t *testing.T) schema.Schema {
	t.Helper()
	ds := NewDataSource()
	var resp datasource.SchemaResponse
	ds.Schema(context.Background(), datasource.SchemaRequest{}, &resp)
	return resp.Schema
}

func TestSchemaAttributes(t *testing.T) {
	s := listingSchema(t)
	for _, attr := range []string{"vpc_id", "id", "routes"} {
		if _, ok := s.Attributes[attr]; !ok {
			t.Errorf("attribute %s missing from the schema", attr)
		}
	}
	if vpcID, ok := s.Attributes["vpc_id"].(schema.StringAttribute); !ok || !vpcID.Required {
		t.Error("vpc_id must be a required string attribute")
	}
	if id, ok := s.Attributes["id"].(schema.StringAttribute); !ok || !id.Computed {
		t.Error("id must be a computed string attribute")
	}
	routes, ok := s.Attributes["routes"].(schema.ListNestedAttribute)
	if !ok {
		t.Fatalf("routes must be a ListNestedAttribute, got %T", s.Attributes["routes"])
	}
	if !routes.Computed {
		t.Error("routes must be computed")
	}
	for _, attr := range []string{"destination", "next_hop"} {
		if _, ok := routes.NestedObject.Attributes[attr]; !ok {
			t.Errorf("row attribute %s missing from the schema", attr)
		}
	}
}

func TestConfigureNilProviderData(t *testing.T) {
	ds := &vpcRoutesDataSource{}
	var resp datasource.ConfigureResponse
	ds.Configure(context.Background(), datasource.ConfigureRequest{}, &resp)
	if resp.Diagnostics.HasError() {
		t.Errorf("expected no errors, got %v", resp.Diagnostics.Errors())
	}
}

func TestConfigureWrongType(t *testing.T) {
	ds := &vpcRoutesDataSource{}
	var resp datasource.ConfigureResponse
	ds.Configure(context.Background(), datasource.ConfigureRequest{ProviderData: "not-a-client"}, &resp)
	if !resp.Diagnostics.HasError() {
		t.Error("expected error for wrong type")
	}
}

// --- the plan-time id guard ---

// The same values the resource's routesPath guard refuses, under the same
// vector its test pins (internal/resource/vpc_route TestRoutesPathRefusesDotSegments)
// — the two guards are deliberate copies that build the same URL, so extend
// BOTH vectors together. A "." or ".." id does not stay one path segment: the
// client joins with path.Join, which CLEANS, and the cleaned URL addresses a
// DIFFERENT resource.
func TestVPCIDValidatorRefusesUnusableIDs(t *testing.T) {
	for _, vpcID := range []string{"", ".", "..", "a/b", "a/..", `a\b`, "a?b", "a#b", "a%2fb"} {
		var resp validator.StringResponse
		validVPCIDValidator{}.ValidateString(context.Background(), stringRequest(vpcID), &resp)
		if !resp.Diagnostics.HasError() {
			t.Errorf("the VPC id %q must be refused at plan time", vpcID)
		}
		// And at the request boundary, where Read re-runs the same guard.
		if err := validVPCID(vpcID); err == nil {
			t.Errorf("the VPC id %q must be refused by the request-boundary guard too", vpcID)
		}
	}
	if err := validVPCID("vpc-abc123"); err != nil {
		t.Errorf("an ordinary VPC id must be accepted: %v", err)
	}
	var unknown validator.StringResponse
	validVPCIDValidator{}.ValidateString(context.Background(), stringRequestUnknown(), &unknown)
	if unknown.Diagnostics.HasError() {
		t.Errorf("an unknown value (a reference to another resource) must not error: %v", unknown.Diagnostics)
	}
}

// A Read with an unusable id must refuse without issuing any request — the
// request a "." id would produce is a DELETE against a VPC whose id is
// "routes", one layer too late for the validator to matter.
func TestReadRefusesAnUnusableIDWithoutCallingTheAPI(t *testing.T) {
	var requests atomic.Int32
	server := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusNotFound)
	})
	defer server.Close()

	readResp := readRoutes(t, server.URL, "..")
	if !readResp.diagnostics.HasError() {
		t.Fatal("a dot-segment VPC id must be refused")
	}
	if requests.Load() != 0 {
		t.Errorf("no request should have been made, got %d", requests.Load())
	}
}

// --- happy list ---

// The rows pass through verbatim, in the platform's canonical rendering: the
// `internet` token as the token, and a platform default route as the single
// 0.0.0.0/0 it was written as — never as the stored halves.
func TestReadListsTheWholeTenantRouteSet(t *testing.T) {
	var gotPath, gotMethod atomic.Pointer[string]
	server := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		p, m := r.URL.Path, r.Method
		gotPath.Store(&p)
		gotMethod.Store(&m)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"routes":[
			{"destination":"0.0.0.0/0","nextHop":"10.0.1.10"},
			{"destination":"203.0.113.0/24","nextHop":"internet"},
			{"destination":"fd00:1::/64","nextHop":"fd00:1::a"}
		]}`))
	})
	defer server.Close()

	readResp := readRoutes(t, server.URL, "vpc-123")
	if readResp.diagnostics.HasError() {
		t.Fatalf("read: %v", readResp.diagnostics.Errors())
	}
	if p := gotPath.Load(); p == nil || *p != testRoutesPath {
		t.Errorf("the request went to %v, want %s", p, testRoutesPath)
	}
	if m := gotMethod.Load(); m == nil || *m != http.MethodGet {
		t.Errorf("the request method was %v, want GET", m)
	}

	if readResp.model.ID.ValueString() != "vpc-123" {
		t.Errorf("id is %q, want the vpc_id", readResp.model.ID.ValueString())
	}
	if readResp.model.VPCID.ValueString() != "vpc-123" {
		t.Errorf("vpc_id is %q", readResp.model.VPCID.ValueString())
	}
	if readResp.model.Routes.IsNull() {
		t.Fatal("routes must never be null on a successful read")
	}
	if len(readResp.model.Routes.Elements()) != 3 {
		t.Fatalf("routes has %d rows, want 3", len(readResp.model.Routes.Elements()))
	}

	var rows []vpcRouteRowModel
	if diags := readResp.model.Routes.ElementsAs(context.Background(), &rows, false); diags.HasError() {
		t.Fatalf("ElementsAs: %v", diags)
	}
	// VERBATIM: token stays a token, the default route stays the /0.
	want := []struct{ destination, nextHop string }{
		{"0.0.0.0/0", "10.0.1.10"},
		{"203.0.113.0/24", "internet"},
		{"fd00:1::/64", "fd00:1::a"},
	}
	for i, w := range want {
		if rows[i].Destination.ValueString() != w.destination {
			t.Errorf("row %d destination is %q, want %q", i, rows[i].Destination.ValueString(), w.destination)
		}
		if rows[i].NextHop.ValueString() != w.nextHop {
			t.Errorf("row %d next hop is %q, want the rendered form %q", i, rows[i].NextHop.ValueString(), w.nextHop)
		}
	}
}

// --- the empty set ---

// TO A CHECK BLOCK, `[]` AND null ARE DIFFERENT ANSWERS: `length(null)` fails a
// plan on its own, so an honest empty table has to render as an empty list.
func TestReadEmptySetRendersAnEmptyList(t *testing.T) {
	server := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != testRoutesPath {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(`{"routes":[]}`))
	})
	defer server.Close()

	readResp := readRoutes(t, server.URL, "vpc-123")
	if readResp.diagnostics.HasError() {
		t.Fatalf("read: %v", readResp.diagnostics.Errors())
	}
	if readResp.model.Routes.IsNull() {
		t.Fatal("an empty route set must render as [], never null")
	}
	if len(readResp.model.Routes.Elements()) != 0 {
		t.Errorf("routes has %d rows, want 0", len(readResp.model.Routes.Elements()))
	}
}

// --- the two-cause 404 ---

// THE RESOURCE'S HANDLING, INHERITED. The list 404 is two indistinguishable
// causes: route management disabled for the deployment, or no router. A data
// source that rendered `routes = []` here would tell every check block "no
// drift" — the one lie this surface must not tell — so both cases ERROR, and
// this one (VPC still standing) names both.
func TestReadList404WithTheVPCStandingErrorsWithBothCauses(t *testing.T) {
	server := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == testVPCPath {
			_, _ = w.Write([]byte(`{"id":"vpc-123","name":"main"}`))
			return
		}
		writeFlatError(w, http.StatusNotFound, "not_found", "not found")
	})
	defer server.Close()

	readResp := readRoutes(t, server.URL, "vpc-123")
	if !readResp.diagnostics.HasError() {
		t.Fatal("a 404 on the route collection must error, never render an empty set")
	}
	got := readResp.text()
	// BOTH causes, and only these two framings, so the practitioner sees the
	// escape hatch either way. No "here are the rows" framing exists on this
	// arm — the surface refuses instead of rendering.
	if !strings.Contains(got, "route management enabled") {
		t.Errorf("the diagnostic must name the disabled-management cause: %s", got)
	}
	if !strings.Contains(got, "no route table, which means it has no router") {
		t.Errorf("the diagnostic must name the no-router cause: %s", got)
	}
	// THE ESCAPE HATCH, as the resource's own copy carries one: a legitimate
	// router-less VPC (or a deployment without the surface) must leave with a
	// remedy, not a plan that fails forever.
	if !strings.Contains(got, "remove this data source") {
		t.Errorf("the diagnostic must hand over the escape hatch: %s", got)
	}
	if strings.Contains(got, "routes = []") {
		t.Errorf("the diagnostic must not offer an empty set: %s", got)
	}
}

// Real parent drift: the VPC is gone, so its routes are too. No state to drop —
// the honest answer is to fail the read rather than render a table.
func TestReadList404WithTheVPCGoneErrors(t *testing.T) {
	server := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		writeFlatError(w, http.StatusNotFound, "not_found", "not found")
	})
	defer server.Close()

	readResp := readRoutes(t, server.URL, "vpc-123")
	if !readResp.diagnostics.HasError() {
		t.Fatal("a listing for a VPC that does not exist must error, not render []")
	}
	if !strings.Contains(readResp.text(), "does not exist") {
		t.Errorf("the diagnostic must say the VPC is gone: %s", readResp.text())
	}
}

// THE ENVELOPE WALL, INHERITED. Only a FLAT 404 is the service giving a
// verdict; the api-gateway answers an unrouted path NESTED under `error`, and a
// gateway-wide misroute 404s both the routes GET and the VPC GET. If the VPC
// check swallowed that nested 404 as "gone", the read would report "VPC does
// not exist" over a routing failure — so the fail-safe reading wins and this
// still errors with the both-causes copy.
func TestReadTreatsANestedVPC404AsStillStanding(t *testing.T) {
	server := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == testVPCPath {
			w.WriteHeader(http.StatusNotFound)
			// Verbatim api-gateway shape (internal/gateway/gateway.go, pre-rename).
			_, _ = w.Write([]byte(`{"error":{"code":"ROUTE_NOT_FOUND","message":"no route found for path"}}`))
			return
		}
		writeFlatError(w, http.StatusNotFound, "not_found", "not found")
	})
	defer server.Close()

	readResp := readRoutes(t, server.URL, "vpc-123")
	if !readResp.diagnostics.HasError() {
		t.Fatal("an ambiguous verdict must error")
	}
	if !strings.Contains(readResp.text(), "no router") {
		t.Errorf("an ambiguous VPC 404 must take the fail-safe both-causes arm, not report the VPC gone: %s", readResp.text())
	}
}

// --- everything else surfaces ---

// A 200 WHOSE BODY IS NOT THE ROUTE SET IS NOT AN EMPTY SET. json.Unmarshal
// into a plain slice would leave it nil for a body without the `routes` key,
// and rendering that as `routes = []` would read as "no drift" to a check
// block. Whatever answers so is not the collection, so both spellings refuse.
func TestReadRefusesA200WithoutTheRouteSet(t *testing.T) {
	for name, body := range map[string]string{
		"missing key": `{}`,
		"null set":    `{"routes":null}`,
	} {
		t.Run(name, func(t *testing.T) {
			server := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != testRoutesPath {
					w.WriteHeader(http.StatusNotFound)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(body))
			})
			defer server.Close()

			readResp := readRoutes(t, server.URL, "vpc-123")
			if !readResp.diagnostics.HasError() {
				t.Fatalf("a 200 without a route set must refuse, not render an empty table")
			}
			if !strings.Contains(readResp.text(), "did not answer with a route set") {
				t.Errorf("the diagnostic must say what was missing: %s", readResp.text())
			}
		})
	}
}

// ROUTES_UNAVAILABLE (a 503, the only code a GET can answer besides 404) is not
// a verdict about the table. Not an empty set under any circumstances.
func TestReadSurfacesANon404Refusal(t *testing.T) {
	server := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != testRoutesPath {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		writeFlatError(w, http.StatusServiceUnavailable, "ROUTES_UNAVAILABLE",
			"route management is not available in this deployment")
	})
	defer server.Close()

	readResp := readRoutes(t, server.URL, "vpc-123")
	if !readResp.diagnostics.HasError() {
		t.Fatal("a read refusal must surface, never render an empty set")
	}
}

func TestReadSurfacesABadResponseBody(t *testing.T) {
	server := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != testRoutesPath {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("not json"))
	})
	defer server.Close()

	readResp := readRoutes(t, server.URL, "vpc-123")
	if !readResp.diagnostics.HasError() {
		t.Fatal("an unparseable body must surface")
	}
}

// NO SURFACE NAMES A /1 — the same pin the resource's
// TestNoSchemaCopyNamesAStoredHalf runs over its schema, over this data
// source's copy. A default route is stored as two equivalent halves and
// reported back as the single 0.0.0.0/0; a /1 in any schema copy would be a
// diff no apply can settle, so the storage shape must never reach a
// practitioner.
func TestSchemaCopyNeverNamesAStoredHalf(t *testing.T) {
	half := regexp.MustCompile(`(?:^|[^0-9.])(?:0\.0\.0\.0|128\.0\.0\.0)/1(?:[^0-9]|$)`)

	s := listingSchema(t)
	if half.MatchString(s.GetDescription()) {
		t.Errorf("the listing description names a stored /1 half: %s", s.GetDescription())
	}
	for name, attr := range s.Attributes {
		if half.MatchString(attr.GetDescription()) {
			t.Errorf("attribute %s names a stored /1 half: %s", name, attr.GetDescription())
		}
	}
	routes := s.Attributes["routes"].(schema.ListNestedAttribute)
	for name, attr := range routes.NestedObject.Attributes {
		if half.MatchString(attr.GetDescription()) {
			t.Errorf("row attribute %s names a stored /1 half: %s", name, attr.GetDescription())
		}
	}
}

// --- helpers ---

// newTestServer mirrors the data-source test idiom: the /v1/me lookup first
// (unused here — the tenant is set directly), then the handler under test.
func newTestServer(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/me", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(client.UserProfile{ID: "user-1", TenantID: "t-123"})
	})
	mux.HandleFunc("/", handler)
	return httptest.NewServer(mux)
}

// writeFlatError writes a FLAT refusal body — the shape a service's own verdict
// takes, as distinct from the api-gateway's nested {"error":{…}} envelope.
func writeFlatError(w http.ResponseWriter, status int, code, message string) {
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"code": code, "message": message})
}

type readResult struct {
	diagnostics diag.Diagnostics
	model       vpcRoutesModel
}

// text renders the diagnostics to one string so a test can say what the copy
// must contain.
func (r readResult) text() string {
	var b strings.Builder
	for _, d := range r.diagnostics {
		b.WriteString(d.Summary())
		b.WriteString(" ")
		b.WriteString(d.Detail())
		b.WriteString("\n")
	}
	return b.String()
}

// readRoutes runs one Read against a server URL with the given vpc_id,
// returning the diagnostics and the resulting state model.
func readRoutes(t *testing.T, serverURL, vpcID string) readResult {
	t.Helper()

	c := client.NewClient(serverURL, "test-key") // pragma: allowlist secret
	c.SetTenantIDForTest("t-123")

	ds := NewDataSource()
	var cfgResp datasource.ConfigureResponse
	ds.(datasource.DataSourceWithConfigure).Configure(
		context.Background(),
		datasource.ConfigureRequest{ProviderData: c},
		&cfgResp,
	)
	if cfgResp.Diagnostics.HasError() {
		t.Fatalf("configure: %v", cfgResp.Diagnostics.Errors())
	}

	s := listingSchema(t)
	objType := s.Type().TerraformType(context.Background()).(tftypes.Object)
	config := tftypes.NewValue(objType, map[string]tftypes.Value{
		"vpc_id": tftypes.NewValue(tftypes.String, vpcID),
		"id":     tftypes.NewValue(tftypes.String, nil),
		"routes": tftypes.NewValue(objType.AttributeTypes["routes"], nil),
	})

	readReq := datasource.ReadRequest{
		Config: tfsdk.Config{Schema: s, Raw: config},
	}
	var readResp datasource.ReadResponse
	readResp.State = tfsdk.State{Schema: s}

	ds.Read(context.Background(), readReq, &readResp)

	var result readResult
	result.diagnostics = readResp.Diagnostics
	if !readResp.Diagnostics.HasError() && !readResp.State.Raw.IsNull() {
		if diags := readResp.State.Get(context.Background(), &result.model); diags.HasError() {
			t.Fatalf("state.Get: %v", diags)
		}
	}
	return result
}

func stringRequest(value string) validator.StringRequest {
	return validator.StringRequest{
		Path:        path.Root("vpc_id"),
		ConfigValue: types.StringValue(value),
	}
}

func stringRequestUnknown() validator.StringRequest {
	return validator.StringRequest{
		Path:        path.Root("vpc_id"),
		ConfigValue: types.StringUnknown(),
	}
}
