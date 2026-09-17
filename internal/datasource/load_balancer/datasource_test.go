package load_balancer

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
)

// --- schema, metadata, configure ---

func TestMetadata(t *testing.T) {
	ds := NewDataSource()
	req := datasource.MetadataRequest{ProviderTypeName: "frostmoln"}
	var resp datasource.MetadataResponse
	ds.Metadata(context.Background(), req, &resp)
	if resp.TypeName != "frostmoln_load_balancer" {
		t.Errorf("expected frostmoln_load_balancer, got %s", resp.TypeName)
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
	for _, attr := range []string{
		"id", "name", "vpc_id", "description", "subnet_id", "vip_address",
		"vip_port_id", "scheme", "public_ip_id", "public_ip_address", "type", "flavor_id",
		"status", "provisioning_status", "operating_status", "created_at", "updated_at",
		"tenant_id",
	} {
		if _, ok := s.Attributes[attr]; !ok {
			t.Errorf("attribute %s missing from the schema", attr)
		}
	}
	// One collection, one owner: the listeners and pools are the child
	// resources' collections (frostmoln_lb_listener / lb_pool / lb_member /
	// lb_health_monitor); this resolver's job is the id. A refactor that
	// inlines them back must fail here.
	for _, child := range []string{"listeners", "pools"} {
		if _, ok := s.Attributes[child]; ok {
			t.Errorf("attribute %s must NOT be on this data source: the child resources own that collection", child)
		}
	}
	if id, ok := s.Attributes["id"].(schema.StringAttribute); !ok || !id.Optional {
		t.Error("id must be an optional string attribute")
	}
	if name, ok := s.Attributes["name"].(schema.StringAttribute); !ok || !name.Optional {
		t.Error("name must be an optional string attribute")
	}
	// The subnet data-source pattern: the same attribute is the filter input
	// on a name lookup and the observed value in the result.
	if vpc, ok := s.Attributes["vpc_id"].(schema.StringAttribute); !ok || !vpc.Optional || !vpc.Computed {
		t.Error("vpc_id must be an optional+computed string attribute")
	}
	if scheme, ok := s.Attributes["scheme"].(schema.StringAttribute); !ok || !scheme.Computed {
		t.Error("scheme must be computed")
	}
	if typ, ok := s.Attributes["type"].(schema.StringAttribute); !ok || !typ.Computed {
		t.Error("type must be computed")
	}
	// The plan-time id guard is load-bearing (an unusable id must fail the
	// configuration instead of becoming a request); its presence on the
	// attribute is pinned here so a refactor cannot silently drop it.
	if id, ok := s.Attributes["id"].(schema.StringAttribute); !ok || len(id.Validators) == 0 {
		t.Error("id must carry the plan-time id validator")
	}
}

func TestConfigureNilProviderData(t *testing.T) {
	ds := &loadBalancerDataSource{}
	var resp datasource.ConfigureResponse
	ds.Configure(context.Background(), datasource.ConfigureRequest{}, &resp)
	if resp.Diagnostics.HasError() {
		t.Errorf("expected no errors, got %v", resp.Diagnostics.Errors())
	}
}

func TestConfigureWrongType(t *testing.T) {
	ds := &loadBalancerDataSource{}
	var resp datasource.ConfigureResponse
	ds.Configure(context.Background(), datasource.ConfigureRequest{ProviderData: "not-a-client"}, &resp)
	if !resp.Diagnostics.HasError() {
		t.Error("expected error for wrong type")
	}
}

// --- the plan-time id guard ---

func TestIDValidatorRefusesUnusableIDs(t *testing.T) {
	for _, id := range []string{"", ".", "..", "a/b", "a/..", `a\b`, "a?b", "a#b", "a%2fb"} {
		if err := validLoadBalancerID(id); err == nil {
			t.Errorf("the load balancer id %q must be refused", id)
		}
	}
	if err := validLoadBalancerID("lb-abc123"); err != nil {
		t.Errorf("an ordinary load balancer id must be accepted: %v", err)
	}
}

// --- Read plumbing helpers ---

type readResult struct {
	diagnostics diag.Diagnostics
	model       loadBalancerModel
}

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

// readWith runs one Read against the server with the given id/name/vpc_id
// values (nil = leave the attribute null), returning diagnostics and the
// state model.
func readWith(t *testing.T, serverURL string, id, name, vpcID *string) readResult {
	t.Helper()

	c := client.NewClient(serverURL, "test-key") // pragma: allowlist secret
	c.SetTenantIDForTest("tenant-1")

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
	stringOr := func(v *string) tftypes.Value {
		if v == nil {
			return tftypes.NewValue(tftypes.String, nil)
		}
		return tftypes.NewValue(tftypes.String, *v)
	}
	config := tftypes.NewValue(objType, map[string]tftypes.Value{
		"id":                  stringOr(id),
		"name":                stringOr(name),
		"vpc_id":              stringOr(vpcID),
		"description":         tftypes.NewValue(tftypes.String, nil),
		"subnet_id":           tftypes.NewValue(tftypes.String, nil),
		"vip_address":         tftypes.NewValue(tftypes.String, nil),
		"vip_port_id":         tftypes.NewValue(tftypes.String, nil),
		"scheme":              tftypes.NewValue(tftypes.String, nil),
		"public_ip_id":        tftypes.NewValue(tftypes.String, nil),
		"public_ip_address":   tftypes.NewValue(tftypes.String, nil),
		"type":                tftypes.NewValue(tftypes.String, nil),
		"flavor_id":           tftypes.NewValue(tftypes.String, nil),
		"status":              tftypes.NewValue(tftypes.String, nil),
		"provisioning_status": tftypes.NewValue(tftypes.String, nil),
		"operating_status":    tftypes.NewValue(tftypes.String, nil),
		"created_at":          tftypes.NewValue(tftypes.String, nil),
		"updated_at":          tftypes.NewValue(tftypes.String, nil),
		"tenant_id":           tftypes.NewValue(tftypes.String, nil),
	})

	var readResp datasource.ReadResponse
	readResp.State = tfsdk.State{Schema: s}
	ds.Read(context.Background(), datasource.ReadRequest{
		Config: tfsdk.Config{Schema: s, Raw: config},
	}, &readResp)

	var result readResult
	result.diagnostics = readResp.Diagnostics
	if !readResp.Diagnostics.HasError() && !readResp.State.Raw.IsNull() {
		if diags := readResp.State.Get(context.Background(), &result.model); diags.HasError() {
			t.Fatalf("state.Get: %v", diags)
		}
	}
	return result
}

// newTestServer mirrors the data-source test idiom: the /v1/me lookup first
// (unused here — the tenant is set directly), then the handler under test.
func newTestServer(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/me", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(client.UserProfile{ID: "user-1", TenantID: "tenant-1"})
	})
	mux.HandleFunc("/", handler)
	return httptest.NewServer(mux)
}

func writeFlatError(w http.ResponseWriter, status int, code, message string) {
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"code": code, "message": message})
}

//--- the two-cause refusal matrix ---

// NEITHER id nor name: refused without a request.
func TestReadRefusesAnEmptyLookupWithoutCallingTheAPI(t *testing.T) {
	var requests atomic.Int32
	server := newTestServer(t, func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusOK)
	})
	defer server.Close()

	readResp := readWith(t, server.URL, nil, nil, nil)
	if !readResp.diagnostics.HasError() {
		t.Fatal("neither id nor name must be refused")
	}
	if requests.Load() != 0 {
		t.Errorf("no request should have been made, got %d", requests.Load())
	}
}

// BOTH id and name: refused without a request.
func TestReadRefusesBothIDAndNameWithoutCallingTheAPI(t *testing.T) {
	var requests atomic.Int32
	server := newTestServer(t, func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusOK)
	})
	defer server.Close()

	id, name := "lb-1", "edge-https"
	readResp := readWith(t, server.URL, &id, &name, nil)
	if !readResp.diagnostics.HasError() {
		t.Fatal("both id and name must be refused")
	}
	if requests.Load() != 0 {
		t.Errorf("no request should have been made, got %d", requests.Load())
	}
}

// An unusable id fails at the request boundary without a request.
func TestReadRefusesAnUnusableIDWithoutCallingTheAPI(t *testing.T) {
	var requests atomic.Int32
	server := newTestServer(t, func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusOK)
	})
	defer server.Close()

	dot := ".."
	readResp := readWith(t, server.URL, &dot, nil, nil)
	if !readResp.diagnostics.HasError() {
		t.Fatal("a dot-segment id must be refused")
	}
	if requests.Load() != 0 {
		t.Errorf("no request should have been made, got %d", requests.Load())
	}
}

// --- the id path ---

func TestReadByID(t *testing.T) {
	var gotPath atomic.Pointer[string]
	server := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		gotPath.Store(&p)
		if r.URL.Path != "/v1/tenants/tenant-1/load-balancers/lb-1" {
			writeFlatError(w, http.StatusNotFound, "not_found", "not found")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":"lb-1","name":"edge-https","description":"public edge",
			"vpcId":"vpc-1","subnetId":"subnet-1",
			"vipAddress":"10.0.1.7","vipPortId":"port-1",
			"scheme":"public","publicIpId":"pip-1","publicIpAddress":"203.0.113.10",
			"type":"l7","flavorId":"lb.gp1.small",
			"status":"active","provisioningStatus":"ACTIVE","operatingStatus":"ONLINE",
			"createdAt":"2026-01-01T00:00:00Z","updatedAt":"2026-02-01T00:00:00Z",
			"tenantId":"tenant-1"
		}`))
	})
	defer server.Close()

	id := "lb-1"
	readResp := readWith(t, server.URL, &id, nil, nil)
	if readResp.diagnostics.HasError() {
		t.Fatalf("read: %v", readResp.diagnostics.Errors())
	}
	if p := gotPath.Load(); p == nil || *p != "/v1/tenants/tenant-1/load-balancers/lb-1" {
		t.Errorf("the request went to %v", p)
	}
	m := readResp.model
	if m.ID.ValueString() != "lb-1" || m.Name.ValueString() != "edge-https" {
		t.Errorf("identity is %s/%s", m.ID.ValueString(), m.Name.ValueString())
	}
	if m.VPCID.ValueString() != "vpc-1" || m.SubnetID.ValueString() != "subnet-1" {
		t.Errorf("placement is %s/%s", m.VPCID.ValueString(), m.SubnetID.ValueString())
	}
	if m.VIPAddress.ValueString() != "10.0.1.7" || m.VIPPortID.ValueString() != "port-1" {
		t.Errorf("vip is %s/%s", m.VIPAddress.ValueString(), m.VIPPortID.ValueString())
	}
	if m.Scheme.ValueString() != "public" || m.PublicIPID.ValueString() != "pip-1" {
		t.Errorf("scheme/pip are %s/%s", m.Scheme.ValueString(), m.PublicIPID.ValueString())
	}
	if m.PublicIPAddress.ValueString() != "203.0.113.10" {
		t.Errorf("public address is %s", m.PublicIPAddress.ValueString())
	}
	if m.Type.ValueString() != "l7" || m.FlavorID.ValueString() != "lb.gp1.small" {
		t.Errorf("type/flavor are %s/%s", m.Type.ValueString(), m.FlavorID.ValueString())
	}
	if m.Status.ValueString() != "active" ||
		m.ProvisioningStatus.ValueString() != "ACTIVE" ||
		m.OperatingStatus.ValueString() != "ONLINE" {
		t.Errorf("status axes are %s/%s/%s", m.Status.ValueString(),
			m.ProvisioningStatus.ValueString(), m.OperatingStatus.ValueString())
	}
	if m.CreatedAt.ValueString() != "2026-01-01T00:00:00Z" || m.UpdatedAt.ValueString() != "2026-02-01T00:00:00Z" {
		t.Errorf("timestamps are %s/%s", m.CreatedAt.ValueString(), m.UpdatedAt.ValueString())
	}
	if m.TenantID.ValueString() != "tenant-1" {
		t.Errorf("tenant is %s", m.TenantID.ValueString())
	}
}

func TestReadByID404ErrorsThatTheLoadBalancerDoesNotExist(t *testing.T) {
	var requests atomic.Int32
	server := newTestServer(t, func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		writeFlatError(w, http.StatusNotFound, "not_found", "not found")
	})
	defer server.Close()

	id := "lb-gone"
	readResp := readWith(t, server.URL, &id, nil, nil)
	if !readResp.diagnostics.HasError() {
		t.Fatal("a flat 404 must fail the read")
	}
	if got := readResp.text(); !strings.Contains(got, "does not exist") {
		t.Errorf("the diagnostic must say the load balancer is gone: %s", got)
	}
	// guardManagedLB hides offer-internal load balancers behind the SAME 404
	// the service answers for a genuinely absent one, so the copy says the
	// 404 may be a managed offer's rather than empty space.
	if got := readResp.text(); !strings.Contains(got, "managed offer") {
		t.Errorf("the diagnostic must name the managed-offer hiding: %s", got)
	}
	if requests.Load() != 1 {
		t.Errorf("a flat 404 is one cause and one request; got %d", requests.Load())
	}
}

// A 200 that does not carry the requested id is not the load-balancer read
// this provider builds its contract on.
func TestReadByIDRefusesA200ThatDoesNotCarryTheID(t *testing.T) {
	server := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/tenants/tenant-1/load-balancers/lb-1" {
			writeFlatError(w, http.StatusNotFound, "not_found", "not found")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"lb-other","name":"other","vpcId":"vpc-1","subnetId":"subnet-1","status":"active"}`))
	})
	defer server.Close()

	id := "lb-1"
	readResp := readWith(t, server.URL, &id, nil, nil)
	if !readResp.diagnostics.HasError() {
		t.Fatal("a mismatched 200 must refuse")
	}
	if got := readResp.text(); !strings.Contains(got, "did not identify the requested load balancer") {
		t.Errorf("the diagnostic must name the identity guard: %s", got)
	}
}

// --- the name path ---

func TestReadByNameResolvesFromTheList(t *testing.T) {
	var gotQuery atomic.Pointer[url.Values]
	server := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		gotQuery.Store(&q)
		w.Header().Set("Content-Type", "application/json")
		// The count key is `total` — the load-balancer list is the one
		// service whose envelope deviates from totalCount.
		_, _ = w.Write([]byte(`{"loadBalancers":[
			{"id":"lb-edge","name":"edge-https","vpcId":"vpc-1","subnetId":"subnet-1",
			 "vipAddress":"10.0.1.7","scheme":"public","publicIpId":"pip-1",
			 "publicIpAddress":"203.0.113.10","type":"l7",
			 "status":"active","provisioningStatus":"ACTIVE","operatingStatus":"ONLINE",
			 "createdAt":"2026-01-01T00:00:00Z","tenantId":"tenant-1"}
		],"total":1,"limit":100}`))
	})
	defer server.Close()

	name := "edge-https"
	readResp := readWith(t, server.URL, nil, &name, nil)
	if readResp.diagnostics.HasError() {
		t.Fatalf("read: %v", readResp.diagnostics.Errors())
	}
	if q := gotQuery.Load(); q == nil || q.Get("name") != "edge-https" {
		t.Errorf("the exact-name filter must ride the request, got query %v", q)
	}
	m := readResp.model
	if m.ID.ValueString() != "lb-edge" || m.Scheme.ValueString() != "public" {
		t.Errorf("resolution is %s/%s", m.ID.ValueString(), m.Scheme.ValueString())
	}
	if m.PublicIPAddress.ValueString() != "203.0.113.10" || !m.UpdatedAt.IsNull() {
		t.Errorf("address/updated are %s/%v", m.PublicIPAddress.ValueString(), m.UpdatedAt.IsNull())
	}
}

// THE wire quirk this package pins: the load-balancer list counts on `total`,
// not `totalCount`. Every other service in the platform answers totalCount;
// this is the one that deviates. The struct field is the only reader of the
// count, so its json tag IS the contract — a rename to `totalCount` here
// silently decodes 0 forever.
func TestListEnvelopeCountsOnTotalNotTotalCount(t *testing.T) {
	field, ok := reflect.TypeOf(apiLoadBalancerList{}).FieldByName("Total")
	if !ok {
		t.Fatal("the list envelope must carry a Total field")
	}
	if tag := field.Tag.Get("json"); tag != "total" {
		t.Errorf("the list envelope must count on `total`, not %q — the load-balancer list "+
			"is the one service that deviates from totalCount", tag)
	}
	if _, ok := reflect.TypeOf(apiLoadBalancerList{}).FieldByName("TotalCount"); ok {
		t.Error("the list envelope must carry no totalCount field: the wire has no such key")
	}
}

// The exact-name filter is the fast path, never the contract: Read still
// re-verifies, so a row the filter should have excluded cannot resolve.
func TestReadByNameReverifiesTheMatchClientSide(t *testing.T) {
	server := newTestServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"loadBalancers":[
			{"id":"lb-1","name":"something-else-entirely","vpcId":"vpc-1","subnetId":"subnet-1",
			 "status":"active","provisioningStatus":"ACTIVE","operatingStatus":"ONLINE",
			 "createdAt":"2026-01-01T00:00:00Z","tenantId":"tenant-1"}
		],"total":1,"limit":100}`))
	})
	defer server.Close()

	name := "edge-https"
	readResp := readWith(t, server.URL, nil, &name, nil)
	if !readResp.diagnostics.HasError() {
		t.Fatal("a name that matches nothing must fail the read")
	}
	if got := readResp.text(); !strings.Contains(got, "No load balancer with this name") {
		t.Errorf("the diagnostic must say the lookup is empty: %s", got)
	}
}

// The list pages on a marker: a match beyond the first page must still
// resolve, across as many requests as it takes.
func TestReadByNameWalksListPages(t *testing.T) {
	var requests atomic.Int32

	row := func(id, name string) string {
		return fmt.Sprintf(`{"id":%q,"name":%q,"vpcId":"vpc-1","subnetId":"subnet-1",
			"status":"active","provisioningStatus":"ACTIVE","operatingStatus":"ONLINE",
			"createdAt":"2026-01-01T00:00:00Z","tenantId":"tenant-1"}`, id, name)
	}

	server := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		marker := r.URL.Query().Get("marker")
		var rows []string
		next := ""
		switch marker {
		case "":
			for i := 0; i < 100; i++ {
				rows = append(rows, row(fmt.Sprintf("lb-p1-%02d", i), "filler"))
			}
			next = "marker-page-2"
		case "marker-page-2":
			rows = append(rows, row("lb-edge", "edge-https"))
		default:
			rows = nil
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"loadBalancers":[%s],"total":%d,"limit":100,
			"marker":%q,"nextMarker":%q}`,
			strings.Join(rows, ","), len(rows), marker, next)
	})
	defer server.Close()

	name := "edge-https"
	readResp := readWith(t, server.URL, nil, &name, nil)
	if readResp.diagnostics.HasError() {
		t.Fatalf("read: %v", readResp.diagnostics.Errors())
	}
	if readResp.model.ID.ValueString() != "lb-edge" {
		t.Errorf("resolution is %q", readResp.model.ID.ValueString())
	}
	if got := requests.Load(); got != 2 {
		t.Errorf("expected exactly 2 page requests, got %d", got)
	}
}

// vpc_id is the filter input: when set it rides the request as the vpcId
// filter AND re-verifies client-side.
func TestReadByNameSendsTheVPCIDFilterWhenSet(t *testing.T) {
	var gotQuery atomic.Pointer[url.Values]
	server := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		gotQuery.Store(&q)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"loadBalancers":[
			{"id":"lb-edge","name":"edge-https","vpcId":"vpc-9","subnetId":"subnet-1",
			 "status":"active","provisioningStatus":"ACTIVE","operatingStatus":"ONLINE",
			 "createdAt":"2026-01-01T00:00:00Z","tenantId":"tenant-1"}
		],"total":1,"limit":100}`))
	})
	defer server.Close()

	name, vpc := "edge-https", "vpc-9"
	readResp := readWith(t, server.URL, nil, &name, &vpc)
	if readResp.diagnostics.HasError() {
		t.Fatalf("read: %v", readResp.diagnostics.Errors())
	}
	if q := gotQuery.Load(); q == nil || q.Get("vpcId") != "vpc-9" || q.Get("name") != "edge-https" {
		t.Errorf("the filters must ride the request, got query %v", q)
	}
	if readResp.model.VPCID.ValueString() != "vpc-9" {
		t.Errorf("vpc_id must carry the observed VPC, got %s", readResp.model.VPCID.ValueString())
	}
}

// The re-verify half of the vpc_id filter: a row whose VPC differs does not
// resolve, even though the exact name matched (here the server ignored the
// filter, as a mid-rollout surface could).
func TestReadByNameRefusesARowOutsideTheVPCFilter(t *testing.T) {
	server := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("vpcId") == "" {
			t.Error("the vpcId filter must ride the request")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"loadBalancers":[
			{"id":"lb-edge","name":"edge-https","vpcId":"vpc-2","subnetId":"subnet-1",
			 "status":"active","provisioningStatus":"ACTIVE","operatingStatus":"ONLINE",
			 "createdAt":"2026-01-01T00:00:00Z","tenantId":"tenant-1"}
		],"total":1,"limit":100}`))
	})
	defer server.Close()

	name, vpc := "edge-https", "vpc-1"
	readResp := readWith(t, server.URL, nil, &name, &vpc)
	if !readResp.diagnostics.HasError() {
		t.Fatal("a name outside the named VPC must not resolve")
	}
	got := readResp.text()
	if !strings.Contains(got, "No load balancer with this name") || !strings.Contains(got, "vpc-1") {
		t.Errorf("the diagnostic must say the lookup is empty and name the VPC: %s", got)
	}
}

func TestReadByName404WhenNoNameMatches(t *testing.T) {
	server := newTestServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"loadBalancers":[
			{"id":"lb-1","name":"analytics-lb","vpcId":"vpc-1","subnetId":"subnet-1",
			 "status":"active","provisioningStatus":"ACTIVE","operatingStatus":"ONLINE",
			 "createdAt":"2026-01-01T00:00:00Z","tenantId":"tenant-1"}
		],"total":1,"limit":100}`))
	})
	defer server.Close()

	name := "does-not-exist"
	readResp := readWith(t, server.URL, nil, &name, nil)
	if !readResp.diagnostics.HasError() {
		t.Fatal("a name that matches nothing must fail the read")
	}
	if got := readResp.text(); !strings.Contains(got, "No load balancer with this name") {
		t.Errorf("the diagnostic must say the lookup is empty: %s", got)
	}
}

// Two load balancers with the same name: refuse, naming the colliding ids.
func TestReadByNameRefusesAnAmbiguousName(t *testing.T) {
	server := newTestServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"loadBalancers":[
			{"id":"lb-a","name":"edge-https","vpcId":"vpc-1","subnetId":"subnet-1",
			 "status":"active","provisioningStatus":"ACTIVE","operatingStatus":"ONLINE",
			 "createdAt":"2026-01-01T00:00:00Z","tenantId":"tenant-1"},
			{"id":"lb-b","name":"edge-https","vpcId":"vpc-2","subnetId":"subnet-2",
			 "status":"active","provisioningStatus":"ACTIVE","operatingStatus":"ONLINE",
			 "createdAt":"2026-01-02T00:00:00Z","tenantId":"tenant-1"}
		],"total":2,"limit":100}`))
	})
	defer server.Close()

	name := "edge-https"
	readResp := readWith(t, server.URL, nil, &name, nil)
	if !readResp.diagnostics.HasError() {
		t.Fatal("an ambiguous name must refuse")
	}
	got := readResp.text()
	if !strings.Contains(got, "lb-a") || !strings.Contains(got, "lb-b") {
		t.Errorf("the diagnostic must name the colliding ids: %s", got)
	}
}

// --- surfaces ---

// A NESTED 404 (the api-gateway's unrouted-path envelope) is not a verdict
// about the load balancer; it takes the generic arm.
func TestReadTreatsANested404AsAGenericFailure(t *testing.T) {
	server := newTestServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":{"code":"NOT_FOUND","message":"no route found for path"}}`))
	})
	defer server.Close()

	id := "lb-1"
	readResp := readWith(t, server.URL, &id, nil, nil)
	if !readResp.diagnostics.HasError() {
		t.Fatal("an ambiguous verdict must error")
	}
	got := readResp.text()
	if strings.Contains(got, "does not exist") {
		t.Errorf("a nested 404 is not a verdict about the load balancer; it must not read as gone: %s", got)
	}
	if !strings.Contains(got, "Failed to Read Load Balancer") {
		t.Errorf("a nested 404 must take the generic arm: %s", got)
	}
}

func TestReadSurfacesABadResponseBody(t *testing.T) {
	server := newTestServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("not json"))
	})
	defer server.Close()

	id := "lb-1"
	readResp := readWith(t, server.URL, &id, nil, nil)
	if !readResp.diagnostics.HasError() {
		t.Fatal("an unparseable body must surface")
	}
}

// A null optional field renders as null, never "" — every omitempty field the
// wire can leave out is the vector.
func TestReadMapsAbsentOptionalsToNull(t *testing.T) {
	server := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/tenants/tenant-1/load-balancers/lb-1" {
			writeFlatError(w, http.StatusNotFound, "not_found", "not found")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"lb-1","name":"internal-lb","vpcId":"vpc-1",
			"subnetId":"subnet-1","scheme":"internal",
			"status":"active","provisioningStatus":"ACTIVE","operatingStatus":"ONLINE",
			"createdAt":"2026-01-01T00:00:00Z","updatedAt":"2026-02-01T00:00:00Z",
			"tenantId":"tenant-1"}`))
	})
	defer server.Close()

	id := "lb-1"
	readResp := readWith(t, server.URL, &id, nil, nil)
	if readResp.diagnostics.HasError() {
		t.Fatalf("read: %v", readResp.diagnostics.Errors())
	}
	m := readResp.model
	for name, check := range map[string]func() bool{
		"description":       m.Description.IsNull,
		"vip_address":       m.VIPAddress.IsNull,
		"vip_port_id":       m.VIPPortID.IsNull,
		"public_ip_id":      m.PublicIPID.IsNull,
		"public_ip_address": m.PublicIPAddress.IsNull,
		"type":              m.Type.IsNull,
		"flavor_id":         m.FlavorID.IsNull,
	} {
		if !check() {
			t.Errorf("%s must render as null when the wire omits it", name)
		}
	}
}
