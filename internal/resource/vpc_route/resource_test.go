package vpc_route

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
)

const routesPath = "/v1/tenants/t-123/vpcs/vpc-123/routes"

func routeSchema(t *testing.T) schema.Schema {
	t.Helper()
	r := NewResource()
	resp := &resource.SchemaResponse{}
	r.Schema(context.Background(), resource.SchemaRequest{}, resp)
	return resp.Schema
}

func routeObjectType() tftypes.Object {
	return tftypes.Object{
		AttributeTypes: map[string]tftypes.Type{
			"id":          tftypes.String,
			"vpc_id":      tftypes.String,
			"destination": tftypes.String,
			"next_hop":    tftypes.String,
		},
	}
}

func routeValue(id, vpcID, destination, nextHop any) tftypes.Value {
	return tftypes.NewValue(routeObjectType(), map[string]tftypes.Value{
		"id":          tftypes.NewValue(tftypes.String, id),
		"vpc_id":      tftypes.NewValue(tftypes.String, vpcID),
		"destination": tftypes.NewValue(tftypes.String, destination),
		"next_hop":    tftypes.NewValue(tftypes.String, nextHop),
	})
}

func configured(t *testing.T, serverURL string) resource.Resource {
	t.Helper()
	c := client.NewClient(serverURL, "test-key") // pragma: allowlist secret
	c.SetTenantIDForTest("t-123")
	r := NewResource()
	cfgResp := &resource.ConfigureResponse{}
	r.(resource.ResourceWithConfigure).Configure(context.Background(), resource.ConfigureRequest{ProviderData: c}, cfgResp)
	if cfgResp.Diagnostics.HasError() {
		t.Fatalf("configure: %v", cfgResp.Diagnostics)
	}
	return r
}

func routeError(w http.ResponseWriter, status int, code, message string) {
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"code": code, "message": message})
}

// --- wire contract ---

// The VPC-scoped surface is camelCase. `nexthop` is the older router-scoped
// spelling, and a body carrying it is rejected by the handler's binding.
func TestVPCRouteWireContract(t *testing.T) {
	body, err := json.Marshal(apiVPCRoute{Destination: "203.0.113.0/24", NextHop: "internet"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got := string(body)
	if !strings.Contains(got, `"nextHop":"internet"`) {
		t.Errorf("the next hop must be sent as `nextHop`, got %s", got)
	}
	if strings.Contains(got, `"nexthop"`) {
		t.Errorf("the router-scoped `nexthop` spelling must never be sent: %s", got)
	}

	var list apiVPCRouteList
	if err := json.Unmarshal([]byte(`{"routes":[{"destination":"10.0.0.0/8","nextHop":"10.0.1.1"}]}`), &list); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(list.Routes) != 1 || list.Routes[0].NextHop != "10.0.1.1" {
		t.Errorf("route set decoded wrong: %+v", list)
	}
}

// --- CRUD against a real client ---

func TestVPCRouteCreateFindsItselfInTheWholeRouteSet(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != routesPath {
			routeError(w, http.StatusNotFound, "not_found", "not found")
			return
		}
		// Every operation answers with the tenant's WHOLE route set. A sibling
		// route another apply created is in it too.
		_, _ = w.Write([]byte(`{"routes":[
			{"destination":"10.9.0.0/16","nextHop":"10.0.1.99"},
			{"destination":"203.0.113.0/24","nextHop":"internet"}
		]}`))
	}))
	defer server.Close()

	r := configured(t, server.URL)
	s := routeSchema(t)
	plan := tfsdk.Plan{Schema: s, Raw: routeValue(tftypes.UnknownValue, "vpc-123", "203.0.113.0/24", "internet")}
	resp := &resource.CreateResponse{State: tfsdk.State{Schema: s}}
	r.Create(context.Background(), resource.CreateRequest{Plan: plan}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("create: %v", resp.Diagnostics)
	}
	var state VPCRouteModel
	resp.State.Get(context.Background(), &state)
	if state.ID.ValueString() != "vpc-123/203.0.113.0/24" {
		t.Errorf("id is %q", state.ID.ValueString())
	}
	// THE TOKEN, NOT THE ADDRESS. The VPC-scoped surface renders `internet`
	// back symbolically; storing a resolved address would leave state holding a
	// literal that stops routing when the gateway is rebuilt.
	if state.NextHop.ValueString() != NextHopInternet {
		t.Errorf("next hop is %q, want the %q token", state.NextHop.ValueString(), NextHopInternet)
	}
}

// A route missing from the set is the one case that legitimately drops the
// resource: someone deleted it outside Terraform.
func TestVPCRouteReadRemovesAMissingRouteFromState(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"routes":[{"destination":"10.9.0.0/16","nextHop":"10.0.1.99"}]}`))
	}))
	defer server.Close()

	r := configured(t, server.URL)
	s := routeSchema(t)
	resp := &resource.ReadResponse{State: tfsdk.State{Schema: s, Raw: routeValue("vpc-123/203.0.113.0/24", "vpc-123", "203.0.113.0/24", "internet")}}
	r.Read(context.Background(), resource.ReadRequest{State: resp.State}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("read: %v", resp.Diagnostics)
	}
	if !resp.State.Raw.IsNull() {
		t.Error("a route that is gone must be removed from state")
	}
}

// THE SPLIT THE TAXONOMY EXISTS FOR, read from the other side: a 404 on the
// route collection while the VPC is still there is NOT parent drift. It is a
// deployment that does not serve route management, and dropping the resource
// would destroy state over a configuration flag.
func TestVPCRouteReadDoesNotDropStateWhenOnlyTheSurfaceIs404(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/tenants/t-123/vpcs/vpc-123" {
			_, _ = w.Write([]byte(`{"id":"vpc-123","name":"main"}`))
			return
		}
		routeError(w, http.StatusNotFound, "not_found", "not found")
	}))
	defer server.Close()

	r := configured(t, server.URL)
	s := routeSchema(t)
	resp := &resource.ReadResponse{State: tfsdk.State{Schema: s, Raw: routeValue("vpc-123/203.0.113.0/24", "vpc-123", "203.0.113.0/24", "internet")}}
	r.Read(context.Background(), resource.ReadRequest{State: resp.State}, resp)

	if !resp.Diagnostics.HasError() {
		t.Fatal("a 404 on the route surface with the VPC still present must be an error, not a silent state drop")
	}
	if resp.State.Raw.IsNull() {
		t.Error("state must be kept when the VPC still exists")
	}
}

// Real parent drift: the VPC itself is gone, so its routes are too.
func TestVPCRouteReadRemovesStateWhenTheVPCIsGone(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		routeError(w, http.StatusNotFound, "not_found", "not found")
	}))
	defer server.Close()

	r := configured(t, server.URL)
	s := routeSchema(t)
	resp := &resource.ReadResponse{State: tfsdk.State{Schema: s, Raw: routeValue("vpc-123/203.0.113.0/24", "vpc-123", "203.0.113.0/24", "internet")}}
	r.Read(context.Background(), resource.ReadRequest{State: resp.State}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("parent drift must not be an error: %v", resp.Diagnostics)
	}
	if !resp.State.Raw.IsNull() {
		t.Error("a route whose VPC is gone must be removed from state")
	}
}

// The destination is a CIDR: it contains a slash and cannot travel in the path.
func TestVPCRouteDeleteSendsDestinationAsAQueryParameter(t *testing.T) {
	var gotPath, gotDestination string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotDestination = r.URL.Query().Get("destination")
		_, _ = w.Write([]byte(`{"routes":[]}`))
	}))
	defer server.Close()

	r := configured(t, server.URL)
	s := routeSchema(t)
	state := tfsdk.State{Schema: s, Raw: routeValue("vpc-123/203.0.113.0/24", "vpc-123", "203.0.113.0/24", "internet")}
	resp := &resource.DeleteResponse{State: state}
	r.Delete(context.Background(), resource.DeleteRequest{State: state}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("delete: %v", resp.Diagnostics)
	}
	if gotPath != routesPath {
		t.Errorf("destination leaked into the path: %q", gotPath)
	}
	if gotDestination != "203.0.113.0/24" {
		t.Errorf("destination query parameter was %q", gotDestination)
	}
}

// ROUTE_NOT_FOUND on delete is the state a delete is trying to reach.
func TestVPCRouteDeleteTreatsRouteNotFoundAsDone(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		routeError(w, http.StatusNotFound, errCodeRouteNotFound, "no route with that destination")
	}))
	defer server.Close()

	r := configured(t, server.URL)
	s := routeSchema(t)
	state := tfsdk.State{Schema: s, Raw: routeValue("vpc-123/203.0.113.0/24", "vpc-123", "203.0.113.0/24", "internet")}
	resp := &resource.DeleteResponse{State: state}
	r.Delete(context.Background(), resource.DeleteRequest{State: state}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("an already-deleted route must not fail the delete: %v", resp.Diagnostics)
	}
}

// --- retry: exactly one code is transient ---

func TestVPCRouteCreateRetriesOnlyWriteConflict(t *testing.T) {
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if attempts.Add(1) == 1 {
			routeError(w, http.StatusConflict, errCodeRouteWriteConflict, "routes kept changing")
			return
		}
		_, _ = w.Write([]byte(`{"routes":[{"destination":"203.0.113.0/24","nextHop":"internet"}]}`))
	}))
	defer server.Close()

	r := configured(t, server.URL)
	s := routeSchema(t)
	plan := tfsdk.Plan{Schema: s, Raw: routeValue(tftypes.UnknownValue, "vpc-123", "203.0.113.0/24", "internet")}
	resp := &resource.CreateResponse{State: tfsdk.State{Schema: s}}
	r.Create(context.Background(), resource.CreateRequest{Plan: plan}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("a write conflict is transient and must be retried: %v", resp.Diagnostics)
	}
	if attempts.Load() < 2 {
		t.Errorf("want at least 2 attempts, got %d", attempts.Load())
	}
}

// A blanket conflict retry would spin here for the whole budget and then report
// a timeout instead of the refusal the customer has to act on.
func TestVPCRouteCreateDoesNotRetryPermanentConflicts(t *testing.T) {
	permanent := []string{
		errCodeRouteExists,
		errCodeRouteDefaultProvidedByGateway,
		errCodeRouteDestinationReserved,
		errCodeRouteShadowsAttachedSubnet,
		errCodeRouteNextHopReserved,
	}

	for _, code := range permanent {
		t.Run(code, func(t *testing.T) {
			var attempts atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				attempts.Add(1)
				routeError(w, http.StatusConflict, code, "refused")
			}))
			defer server.Close()

			r := configured(t, server.URL)
			s := routeSchema(t)
			plan := tfsdk.Plan{Schema: s, Raw: routeValue(tftypes.UnknownValue, "vpc-123", "203.0.113.0/24", "internet")}
			resp := &resource.CreateResponse{State: tfsdk.State{Schema: s}}
			r.Create(context.Background(), resource.CreateRequest{Plan: plan}, resp)

			if !resp.Diagnostics.HasError() {
				t.Fatal("a permanent refusal must surface")
			}
			if attempts.Load() != 1 {
				t.Errorf("%s is permanent and must be attempted once, got %d attempts", code, attempts.Load())
			}
		})
	}
}

// ROUTE_DEFAULT_PROVIDED_BY_GATEWAY must never be reported with the ROUTE_EXISTS
// remedy: the colliding route is invisible and undeletable, so "remove it
// first" is advice nobody can follow.
func TestDefaultProvidedByGatewayNeverOffersTheRemoveRemedy(t *testing.T) {
	var diags diagCollector
	addRouteError(&diags.d, opCreate, &client.APIError{
		Code:       errCodeRouteDefaultProvidedByGateway,
		Message:    "this VPC's gateway already provides a default route",
		StatusCode: http.StatusConflict,
	})

	got := strings.ToLower(diags.text())
	for _, forbidden := range []string{"remove it", "delete that route", "terraform import"} {
		if strings.Contains(got, forbidden) {
			t.Errorf("copy suggests %q, which cannot be done for this refusal: %s", forbidden, got)
		}
	}
	if !strings.Contains(got, "point the default route at an instance") {
		t.Errorf("copy does not name the real remedy: %s", got)
	}
}

// ROUTE_NO_INTERNET_GATEWAY has to name the OTHER resource; that is why it is
// not folded into generic invalid input.
func TestNoInternetGatewayNamesTheGatewayResource(t *testing.T) {
	var diags diagCollector
	addRouteError(&diags.d, opCreate, &client.APIError{
		Code:       errCodeRouteNoInternetGateway,
		Message:    "this VPC has no internet gateway",
		StatusCode: http.StatusBadRequest,
	})
	got := diags.text()
	if !strings.Contains(got, "frostmoln_gateway") {
		t.Errorf("copy must name the gateway resource: %s", got)
	}
	// BOTH cases must be named: the service emits this code for a VPC with no
	// gateway AND for one whose gateway is on the platform service network, and
	// for the second group creating a gateway is refused with GATEWAY_EXISTS.
	if !strings.Contains(got, "already has one") {
		t.Errorf("copy must cover the VPC that already HAS a gateway: %s", got)
	}
	if !strings.Contains(got, "next_hop") {
		t.Errorf("copy must offer the second remedy — an address inside the VPC: %s", got)
	}
}

// A code this build has never seen is a fault to surface, not a refusal to
// reinterpret — and above all not one to retry.
func TestUnknownRouteCodeFallsThrough(t *testing.T) {
	var diags diagCollector
	addRouteError(&diags.d, opCreate, &client.APIError{
		Code:       "ROUTE_SOME_FUTURE_CODE",
		Message:    "something the provider has never heard of",
		StatusCode: http.StatusBadRequest,
	})
	got := diags.text()
	if !strings.Contains(got, "ROUTE_SOME_FUTURE_CODE") || !strings.Contains(got, "never heard of") {
		t.Errorf("an unknown code must be surfaced verbatim: %s", got)
	}
}

// --- schema and validators ---

func TestVPCRouteMetadataAndSchema(t *testing.T) {
	r := NewResource()
	mResp := &resource.MetadataResponse{}
	r.Metadata(context.Background(), resource.MetadataRequest{ProviderTypeName: "frostmoln"}, mResp)
	if mResp.TypeName != "frostmoln_vpc_route" {
		t.Errorf("type name is %s", mResp.TypeName)
	}

	s := routeSchema(t)
	for _, attr := range []string{"id", "vpc_id", "destination", "next_hop"} {
		if _, ok := s.Attributes[attr]; !ok {
			t.Errorf("attribute %s missing from the schema", attr)
		}
	}
}

// THE TRAP THIS RESOURCE WAS WARNED ABOUT: `internet` is a legal next hop, so
// nothing may validate the attribute as an IP address.
func TestNextHopAcceptsTheInternetToken(t *testing.T) {
	for _, value := range []string{NextHopInternet, "10.0.1.10", "fd00:1::a"} {
		var resp validatorResponse
		canonicalAddressOrTokenValidator{}.ValidateString(context.Background(), stringRequest(value), &resp.r)
		if resp.r.Diagnostics.HasError() {
			t.Errorf("%q must be accepted as a next hop: %v", value, resp.r.Diagnostics)
		}
	}
}

// A spelling that reads back differently would leave the resource showing a
// diff no apply can settle, so it is refused at plan time with the form to use.
func TestNonCanonicalValuesAreRefusedAtPlanTime(t *testing.T) {
	var destResp validatorResponse
	canonicalPrefixValidator{}.ValidateString(context.Background(), stringRequest("203.0.113.5/24"), &destResp.r)
	if !destResp.r.Diagnostics.HasError() {
		t.Error("a destination with host bits set must be refused")
	}
	if !strings.Contains(destResp.text(), "203.0.113.0/24") {
		t.Errorf("the refusal must name the canonical form: %s", destResp.text())
	}

	var v6Resp validatorResponse
	canonicalPrefixValidator{}.ValidateString(context.Background(), stringRequest("FD00:1:0:0::/64"), &v6Resp.r)
	if !v6Resp.r.Diagnostics.HasError() {
		t.Error("a non-canonical IPv6 spelling must be refused — it reads back canonicalized")
	}

	var hopResp validatorResponse
	canonicalAddressOrTokenValidator{}.ValidateString(context.Background(), stringRequest("FD00:1::A"), &hopResp.r)
	if !hopResp.r.Diagnostics.HasError() {
		t.Error("a non-canonical IPv6 next hop must be refused")
	}

	// A default route is canonical and must pass.
	for _, ok := range []string{"0.0.0.0/0", "::/0", "203.0.113.0/24"} {
		var resp validatorResponse
		canonicalPrefixValidator{}.ValidateString(context.Background(), stringRequest(ok), &resp.r)
		if resp.r.Diagnostics.HasError() {
			t.Errorf("%q is canonical and must be accepted: %v", ok, resp.r.Diagnostics)
		}
	}
}

// --- import ---

// The destination contains a slash of its own, so only the FIRST one separates.
func TestImportSplitsOnTheFirstSlashOnly(t *testing.T) {
	s := routeSchema(t)
	resp := &resource.ImportStateResponse{State: tfsdk.State{Schema: s, Raw: routeValue(nil, nil, nil, nil)}}
	NewResource().(resource.ResourceWithImportState).ImportState(
		context.Background(),
		resource.ImportStateRequest{ID: "vpc-123/203.0.113.0/24"},
		resp,
	)
	if resp.Diagnostics.HasError() {
		t.Fatalf("import: %v", resp.Diagnostics)
	}

	var vpcID, destination types.String
	resp.State.GetAttribute(context.Background(), path.Root("vpc_id"), &vpcID)
	resp.State.GetAttribute(context.Background(), path.Root("destination"), &destination)
	if vpcID.ValueString() != "vpc-123" {
		t.Errorf("vpc_id is %q", vpcID.ValueString())
	}
	if destination.ValueString() != "203.0.113.0/24" {
		t.Errorf("destination is %q — the CIDR's own slash was treated as a separator", destination.ValueString())
	}
}

func TestImportRejectsMalformedIDs(t *testing.T) {
	for _, id := range []string{"vpc-123", "", "/203.0.113.0/24", "vpc-123/not-a-cidr"} {
		s := routeSchema(t)
		resp := &resource.ImportStateResponse{State: tfsdk.State{Schema: s, Raw: routeValue(nil, nil, nil, nil)}}
		NewResource().(resource.ResourceWithImportState).ImportState(
			context.Background(), resource.ImportStateRequest{ID: id}, resp,
		)
		if !resp.Diagnostics.HasError() {
			t.Errorf("import id %q must be refused", id)
		}
	}
}

// --- helpers ---

func TestFindRouteMatchesOnTheParsedPrefix(t *testing.T) {
	body := []byte(`{"routes":[{"destination":"fd00:1::/64","nextHop":"fd00:1::a"}]}`)
	// The stored form is canonical; a state value spelled differently must still
	// match, or the resource would drop itself and recreate a live route.
	route, err := findRoute(body, "FD00:1:0:0::/64")
	if err != nil {
		t.Fatalf("findRoute: %v", err)
	}
	if route == nil {
		t.Fatal("a differently-spelled prefix must still match the stored route")
	}

	missing, err := findRoute(body, "10.0.0.0/8")
	if err != nil {
		t.Fatalf("findRoute: %v", err)
	}
	if missing != nil {
		t.Error("an absent destination must not match")
	}
}

// diagCollector and validatorResponse keep the assertions above readable: both
// diag.Diagnostics carriers render to one string so a test can say what the
// copy must and must not contain.
type diagCollector struct {
	d diag.Diagnostics
}

func (c *diagCollector) text() string {
	var b strings.Builder
	for _, d := range c.d {
		b.WriteString(d.Summary())
		b.WriteString(" ")
		b.WriteString(d.Detail())
		b.WriteString("\n")
	}
	return b.String()
}

type validatorResponse struct {
	r validator.StringResponse
}

func (v *validatorResponse) text() string {
	c := diagCollector{d: v.r.Diagnostics}
	return c.text()
}

func stringRequest(value string) validator.StringRequest {
	return validator.StringRequest{
		Path:        path.Root("test"),
		ConfigValue: types.StringValue(value),
	}
}

// TestRoutesPathRefusesDotSegments — a "." or ".." id does NOT stay one path
// segment, and url.PathEscape cannot make it: the client joins with path.Join,
// which CLEANS, and both are unreserved so the escape leaves them alone.
// Measured before this guard existed:
//
//	vpcID "."  -> /v1/tenants/{t}/vpcs/routes    (the DELETE VPC endpoint)
//	vpcID ".." -> /v1/tenants/{t}/routes
//
// The first would turn this resource's Delete into a DELETE against a VPC whose
// id is "routes".
func TestRoutesPathRefusesDotSegments(t *testing.T) {
	for _, vpcID := range []string{"", ".", "..", "a/b", "a/..", `a\b`, "a?b", "a#b", "a%2fb"} {
		if err := validVPCID(vpcID); err == nil {
			t.Errorf("the VPC id %q must be refused before it reaches a URL", vpcID)
		}
	}
	if err := validVPCID("vpc-abc123"); err != nil {
		t.Errorf("an ordinary VPC id must be accepted: %v", err)
	}
}

// A Delete against an unusable id must refuse rather than issue a request —
// the request it would issue is a DELETE on another resource.
func TestVPCRouteDeleteRefusesADotSegmentIDWithoutCallingTheAPI(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("no request should have been made: %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	r := configured(t, server.URL)
	s := routeSchema(t)
	state := tfsdk.State{Schema: s, Raw: routeValue("./203.0.113.0/24", ".", "203.0.113.0/24", "internet")}
	resp := &resource.DeleteResponse{State: state}
	r.Delete(context.Background(), resource.DeleteRequest{State: state}, resp)

	if !resp.Diagnostics.HasError() {
		t.Fatal("a dot-segment VPC id must be refused")
	}
}

// vpcExists must FAIL SAFE. An id it cannot use tells it nothing about the VPC,
// and "I could not check" must never become "the parent is gone, drop state".
func TestVPCExistsFailsSafeOnAnUnusableID(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key") // pragma: allowlist secret
	c.SetTenantIDForTest("t-123")
	res := &vpcRouteResource{client: c}

	if !res.vpcExists(context.Background(), "..") {
		t.Error("an unusable id must be treated as `still there`, never as parent drift")
	}
}

// THE DESTROY LEAK. A plain 404 while the VPC is STILL THERE means route
// management is off, or the request never reached the route handler — not that
// the route is gone. Swallowing it makes `terraform destroy` report success,
// drop the resource from state, and leave the route installed on the VPC.
func TestVPCRouteDeleteDoesNotReportSuccessWhenOnlyTheSurfaceIs404(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/tenants/t-123/vpcs/vpc-123" {
			_, _ = w.Write([]byte(`{"id":"vpc-123","name":"main"}`))
			return
		}
		routeError(w, http.StatusNotFound, "not_found", "not found")
	}))
	defer server.Close()

	r := configured(t, server.URL)
	s := routeSchema(t)
	state := tfsdk.State{Schema: s, Raw: routeValue("vpc-123/203.0.113.0/24", "vpc-123", "203.0.113.0/24", "internet")}
	resp := &resource.DeleteResponse{State: state}
	r.Delete(context.Background(), resource.DeleteRequest{State: state}, resp)

	if !resp.Diagnostics.HasError() {
		t.Fatal("a delete that did not reach the route surface must not be reported as done — " +
			"the route is still installed on the VPC")
	}
}

// Real parent drift on delete: the VPC is gone, so its routes went with it.
func TestVPCRouteDeleteTreatsAMissingVPCAsDone(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		routeError(w, http.StatusNotFound, "not_found", "not found")
	}))
	defer server.Close()

	r := configured(t, server.URL)
	s := routeSchema(t)
	state := tfsdk.State{Schema: s, Raw: routeValue("vpc-123/203.0.113.0/24", "vpc-123", "203.0.113.0/24", "internet")}
	resp := &resource.DeleteResponse{State: state}
	r.Delete(context.Background(), resource.DeleteRequest{State: state}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("a route whose VPC is gone is already deleted: %v", resp.Diagnostics)
	}
}

// The import id is validated where the mistake was made, not one Read later.
func TestImportRejectsAnUnusableVPCID(t *testing.T) {
	for _, id := range []string{"../203.0.113.0/24", "./203.0.113.0/24"} {
		s := routeSchema(t)
		resp := &resource.ImportStateResponse{State: tfsdk.State{Schema: s, Raw: routeValue(nil, nil, nil, nil)}}
		NewResource().(resource.ResourceWithImportState).ImportState(
			context.Background(), resource.ImportStateRequest{ID: id}, resp,
		)
		if !resp.Diagnostics.HasError() {
			t.Errorf("import id %q must be refused at import time", id)
		}
	}
}

// TestNextHopUnreachableOnDeleteDropsTheCreateTimeRemedy — since network
// v2.57.1 a DELETE can be refused with ROUTE_NEXT_HOP_UNREACHABLE, which was
// unreachable on this path before. A removal is applied by writing the whole
// REMAINING set and Neutron revalidates all of it, so the refused next hop
// belongs to a route that was already stored, not to the one being destroyed.
//
// The create-time diagnostic is wrong twice over on that path: its title
// accuses the resource being destroyed, and "make this route depend on it" is
// meaningless for a resource Terraform is removing from the graph.
//
// The server message is the SAME for both operations by design — the repository
// classifier deliberately does not know which service operation called it — so
// the operation-specific half has to come from the provider, from the `fallback`
// summary it already passes.
func TestNextHopUnreachableOnDeleteDropsTheCreateTimeRemedy(t *testing.T) {
	// The v2.57.1 wire shape: the repository's own message, verbatim.
	const serverMsg = "a next hop in this route set is not an address on any attached subnet; a next hop " +
		"must be reachable directly, such as an instance acting as a gateway. The whole route set is " +
		"validated on every write, so a route already stored may be the one refused — list the routes " +
		"and correct or remove that one, or send the whole set with PUT"

	refusal := func() *client.APIError {
		return &client.APIError{
			Code:       errCodeRouteNextHopUnreachable,
			Message:    serverMsg,
			StatusCode: http.StatusBadRequest,
		}
	}

	var del diagCollector
	addRouteError(&del.d, opDelete, refusal())
	var create diagCollector
	addRouteError(&create.d, opCreate, refusal())

	// The server's own sentence survives on both — it is appended, not replaced.
	for name, got := range map[string]string{"delete": del.text(), "create": create.text()} {
		if !strings.Contains(got, "a route already stored may be the one refused") {
			t.Errorf("%s: the server's sentence must reach the practitioner: %s", name, got)
		}
	}

	// THE MUTATION TARGET. Destroy must not repeat the create-time remedy...
	gotDelete := del.text()
	if strings.Contains(gotDelete, "make this route depend on it") {
		t.Errorf("destroy must not advise a depends_on for a resource being removed: %s", gotDelete)
	}
	if strings.Contains(gotDelete, "The next hop is not reachable inside this VPC") {
		t.Errorf("destroy must not title the refusal as being about this route's next hop: %s", gotDelete)
	}
	// ...and must say the refused route is a DIFFERENT one.
	if !strings.Contains(gotDelete, "not the one being destroyed") {
		t.Errorf("destroy copy must say the refused route is a different one: %s", gotDelete)
	}
	// IT MUST NAME A COMMAND THAT CAN ACTUALLY SHOW THE OFFENDING ROUTE.
	// `terraform state list` prints resource ADDRESSES, not destinations or next
	// hops, and it reflects state rather than the platform — while the premise of
	// this whole branch is that the offending route may be one Terraform does not
	// manage, and so is absent from state by construction.
	if strings.Contains(gotDelete, "terraform state list") {
		t.Errorf("destroy copy must not point at state for a route that may not be in it: %s", gotDelete)
	}
	if !strings.Contains(gotDelete, "fm network vpc route list") {
		t.Errorf("destroy copy must name a command that lists the stored routes: %s", gotDelete)
	}
	// Delete runs on a plain `terraform apply` whenever the resource leaves the
	// config, and on a RequiresReplace — not only under `terraform destroy`. The
	// write-conflict arm above already says "run `terraform apply` again", so
	// naming destroy here would make the same file contradict itself.
	if strings.Contains(gotDelete, "terraform destroy") {
		t.Errorf("destroy copy must not assume the destroy command: %s", gotDelete)
	}

	// CREATE IS THE CONTROL. Without it, deleting the branch under test still
	// fails only the assertions above, and a reviewer cannot tell whether the
	// create-time remedy was moved or lost.
	if !strings.Contains(create.text(), "make this route depend on it") {
		t.Errorf("create must keep its own remedy: %s", create.text())
	}
}

// TestQuotaExceededOnDeleteDoesNotSendThePractitionerRoundTheLoop —
// ROUTE_QUOTA_EXCEEDED is the SECOND code network v2.57.1 made reachable on a
// removal, and the one where create-shaped copy does not merely fail to help but
// never terminates.
//
// A removal writes the whole REMAINING set. A set already over the network's
// ceiling is still over it once one route goes, so the write is refused and the
// route stays — and "remove a route you no longer need" is the operation that
// just failed. network's own OpenAPI says so on removeVPCRoute's 403: "It
// repeats until the set is back under the limit, which a single DELETE cannot
// achieve". This provider removes one route per resource, so it cannot converge.
func TestQuotaExceededOnDeleteDoesNotSendThePractitionerRoundTheLoop(t *testing.T) {
	refusal := func() *client.APIError {
		return &client.APIError{
			Code:       errCodeRouteQuotaExceeded,
			Message:    "the route set exceeds the maximum number of routes",
			StatusCode: http.StatusForbidden,
		}
	}

	var del diagCollector
	addRouteError(&del.d, opDelete, refusal())
	var create diagCollector
	addRouteError(&create.d, opCreate, refusal())

	gotDelete := del.text()
	// THE MUTATION TARGET.
	if strings.Contains(gotDelete, "remove a route you no longer need") {
		t.Errorf("destroy must not advise the operation that just failed: %s", gotDelete)
	}
	if !strings.Contains(gotDelete, "cannot converge") {
		t.Errorf("destroy must say why retrying cannot work: %s", gotDelete)
	}
	if !strings.Contains(gotDelete, "Contact support") {
		t.Errorf("destroy must name the only path out: %s", gotDelete)
	}

	// CREATE IS THE CONTROL: on a create, removing a spare route IS a real and
	// terminating remedy and must survive.
	if !strings.Contains(create.text(), "remove a route you no longer need") {
		t.Errorf("create must keep its own remedy: %s", create.text())
	}
}

// TestReadPathNeverReachesTheWriteShapedRouteCopy pins that the create-shaped
// arms are DEAD on a read rather than wrong on one.
//
// ROUTE_NEXT_HOP_UNREACHABLE and ROUTE_QUOTA_EXCEEDED are produced only by the
// write paths in the network service — no GET can answer either (its OpenAPI
// gives listVPCRoutes only 503 ROUTES_UNAVAILABLE and 404). So opRead has no
// branch of its own by design, and this test records that as a checked claim
// rather than an assumption a later reader has to re-derive.
func TestReadPathNeverReachesTheWriteShapedRouteCopy(t *testing.T) {
	var diags diagCollector
	addRouteError(&diags.d, opRead, &client.APIError{
		Code:       errCodeRoutesUnavailable,
		Message:    "route management is not available in this deployment",
		StatusCode: http.StatusServiceUnavailable,
	})
	got := diags.text()
	if !strings.Contains(got, "property of the deployment") {
		t.Errorf("a read refusal must get the deployment copy: %s", got)
	}
	// The op still titles the fallback correctly.
	if opRead.summary() != "Failed to Read VPC Routes" {
		t.Errorf("read summary drifted: %s", opRead.summary())
	}
}
