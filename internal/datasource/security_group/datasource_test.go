package security_group

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
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
	if resp.TypeName != "frostmoln_security_group" {
		t.Errorf("expected frostmoln_security_group, got %s", resp.TypeName)
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
		"id", "name", "vpc_id", "description", "is_default", "tags",
		"created_at", "updated_at", "tenant_id",
	} {
		if _, ok := s.Attributes[attr]; !ok {
			t.Errorf("attribute %s missing from the schema", attr)
		}
	}
	// One collection, one owner: the rule listing is OWNED by the sibling
	// frostmoln_security_group_rules data source. This resolver's job is the
	// id, and it must never grow a rules collection back.
	if _, ok := s.Attributes["rules"]; ok {
		t.Error("attribute rules must NOT be on this data source: frostmoln_security_group_rules owns that listing")
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
	if def, ok := s.Attributes["is_default"].(schema.BoolAttribute); !ok || !def.Computed {
		t.Error("is_default must be computed")
	}
	if _, ok := s.Attributes["tags"]; !ok {
		t.Error("tags must exist")
	}
	// The plan-time id guard is load-bearing (an unusable id must fail the
	// configuration instead of becoming a request); its presence on the
	// attribute is pinned here so a refactor cannot silently drop it.
	if id, ok := s.Attributes["id"].(schema.StringAttribute); !ok || len(id.Validators) == 0 {
		t.Error("id must carry the plan-time id validator")
	}
}

func TestConfigureNilProviderData(t *testing.T) {
	ds := &securityGroupDataSource{}
	var resp datasource.ConfigureResponse
	ds.Configure(context.Background(), datasource.ConfigureRequest{}, &resp)
	if resp.Diagnostics.HasError() {
		t.Errorf("expected no errors, got %v", resp.Diagnostics.Errors())
	}
}

func TestConfigureWrongType(t *testing.T) {
	ds := &securityGroupDataSource{}
	var resp datasource.ConfigureResponse
	ds.Configure(context.Background(), datasource.ConfigureRequest{ProviderData: "not-a-client"}, &resp)
	if !resp.Diagnostics.HasError() {
		t.Error("expected error for wrong type")
	}
}

// --- the plan-time id guard ---

func TestIDValidatorRefusesUnusableIDs(t *testing.T) {
	for _, id := range []string{"", ".", "..", "a/b", "a/..", `a\b`, "a?b", "a#b", "a%2fb"} {
		if err := validSecurityGroupID(id); err == nil {
			t.Errorf("the security group id %q must be refused", id)
		}
	}
	if err := validSecurityGroupID("sg-abc123"); err != nil {
		t.Errorf("an ordinary security group id must be accepted: %v", err)
	}
}

// --- Read plumbing helpers ---

type readResult struct {
	diagnostics diag.Diagnostics
	model       securityGroupModel
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
		"id":          stringOr(id),
		"name":        stringOr(name),
		"vpc_id":      stringOr(vpcID),
		"description": tftypes.NewValue(tftypes.String, nil),
		"is_default":  tftypes.NewValue(tftypes.Bool, nil),
		"tags":        tftypes.NewValue(tftypes.Map{ElementType: tftypes.String}, nil),
		"created_at":  tftypes.NewValue(tftypes.String, nil),
		"updated_at":  tftypes.NewValue(tftypes.String, nil),
		"tenant_id":   tftypes.NewValue(tftypes.String, nil),
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

	id, name := "sg-1", "web"
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
		if r.URL.Path != "/v1/tenants/tenant-1/security-groups/sg-1" {
			writeFlatError(w, http.StatusNotFound, "not_found", "not found")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":"sg-1","name":"web","description":"web tier",
			"vpcId":"vpc-1","isDefault":true,
			"tags":{"team":"platform","frostmoln_cluster_id":"hidden"},
			"createdAt":"2026-01-01T00:00:00Z","updatedAt":"2026-02-01T00:00:00Z",
			"tenantId":"tenant-1"
		}`))
	})
	defer server.Close()

	id := "sg-1"
	readResp := readWith(t, server.URL, &id, nil, nil)
	if readResp.diagnostics.HasError() {
		t.Fatalf("read: %v", readResp.diagnostics.Errors())
	}
	if p := gotPath.Load(); p == nil || *p != "/v1/tenants/tenant-1/security-groups/sg-1" {
		t.Errorf("the request went to %v", p)
	}
	m := readResp.model
	if m.ID.ValueString() != "sg-1" || m.Name.ValueString() != "web" {
		t.Errorf("identity is %s/%s", m.ID.ValueString(), m.Name.ValueString())
	}
	if m.VPCID.ValueString() != "vpc-1" {
		t.Errorf("vpc is %s", m.VPCID.ValueString())
	}
	if m.IsDefault.ValueBool() != true {
		t.Errorf("is_default is %t", m.IsDefault.ValueBool())
	}
	if m.CreatedAt.ValueString() != "2026-01-01T00:00:00Z" || m.UpdatedAt.ValueString() != "2026-02-01T00:00:00Z" {
		t.Errorf("timestamps are %s/%s", m.CreatedAt.ValueString(), m.UpdatedAt.ValueString())
	}
	if m.TenantID.ValueString() != "tenant-1" {
		t.Errorf("tenant is %s", m.TenantID.ValueString())
	}
	// Platform-reserved frostmoln_* keys are filtered: `tags` means customer
	// tags on every surface.
	if m.Tags.IsNull() {
		t.Fatal("tags must carry the customer set")
	}
	var tags map[string]string
	if diags := m.Tags.ElementsAs(context.Background(), &tags, false); diags.HasError() {
		t.Fatalf("tags decode: %v", diags.Errors())
	}
	if len(tags) != 1 || tags["team"] != "platform" {
		t.Errorf("tags must carry only the customer keys, got %v", tags)
	}
}

// The platform-managed groups a managed offer provisions for the tenant are
// VISIBLE to this resolver — offer-internal hiding keys on the reserved tag,
// not on Managed — so a group must read fine by id, whatever owns it.
func TestReadByIDResolvesAPlatformManagedGroup(t *testing.T) {
	server := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/tenants/tenant-1/security-groups/sg-managed" {
			writeFlatError(w, http.StatusNotFound, "not_found", "not found")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"sg-managed","name":"managed-db-xyz","vpcId":"vpc-1",
			"isDefault":false,"createdAt":"2026-01-01T00:00:00Z","tenantId":"tenant-1"}`))
	})
	defer server.Close()

	id := "sg-managed"
	readResp := readWith(t, server.URL, &id, nil, nil)
	if readResp.diagnostics.HasError() {
		t.Fatalf("read: %v", readResp.diagnostics.Errors())
	}
	m := readResp.model
	if m.ID.ValueString() != "sg-managed" || m.Name.ValueString() != "managed-db-xyz" {
		t.Errorf("identity is %s/%s", m.ID.ValueString(), m.Name.ValueString())
	}
}

func TestReadByID404ErrorsThatTheSecurityGroupDoesNotExist(t *testing.T) {
	var requests atomic.Int32
	server := newTestServer(t, func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		writeFlatError(w, http.StatusNotFound, "not_found", "not found")
	})
	defer server.Close()

	id := "sg-gone"
	readResp := readWith(t, server.URL, &id, nil, nil)
	if !readResp.diagnostics.HasError() {
		t.Fatal("a flat 404 must fail the read")
	}
	if got := readResp.text(); !strings.Contains(got, "does not exist") {
		t.Errorf("the diagnostic must say the security group is gone: %s", got)
	}
	if requests.Load() != 1 {
		t.Errorf("a flat 404 is one cause and one request; got %d", requests.Load())
	}
}

// A 200 that does not carry the requested id is not the group read this
// provider builds its contract on.
func TestReadByIDRefusesA200ThatDoesNotCarryTheID(t *testing.T) {
	server := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/tenants/tenant-1/security-groups/sg-1" {
			writeFlatError(w, http.StatusNotFound, "not_found", "not found")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"sg-other","name":"other","isDefault":false}`))
	})
	defer server.Close()

	id := "sg-1"
	readResp := readWith(t, server.URL, &id, nil, nil)
	if !readResp.diagnostics.HasError() {
		t.Fatal("a mismatched 200 must refuse")
	}
	if got := readResp.text(); !strings.Contains(got, "did not identify the requested security group") {
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
		_, _ = w.Write([]byte(`{"securityGroups":[
			{"id":"sg-web","name":"web","vpcId":"vpc-1","isDefault":false,
			 "tags":{"team":"platform"},
			 "createdAt":"2026-01-01T00:00:00Z","tenantId":"tenant-1"}
		],"totalCount":1}`))
	})
	defer server.Close()

	name := "web"
	readResp := readWith(t, server.URL, nil, &name, nil)
	if readResp.diagnostics.HasError() {
		t.Fatalf("read: %v", readResp.diagnostics.Errors())
	}
	if q := gotQuery.Load(); q == nil || q.Get("name") != "web" {
		t.Errorf("the exact-name filter must ride the request, got query %v", q)
	}
	m := readResp.model
	if m.ID.ValueString() != "sg-web" || m.IsDefault.ValueBool() != false {
		t.Errorf("resolution is %s/%t", m.ID.ValueString(), m.IsDefault.ValueBool())
	}
	if !m.UpdatedAt.IsNull() || !m.Description.IsNull() {
		t.Errorf("absent optionals must stay null, got %v/%v", m.UpdatedAt.IsNull(), m.Description.IsNull())
	}
}

// The exact-name filter is the fast path, never the contract: Read still
// re-verifies, so a row the filter should have excluded cannot resolve.
func TestReadByNameReverifiesTheMatchClientSide(t *testing.T) {
	server := newTestServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"securityGroups":[
			{"id":"sg-1","name":"something-else-entirely","isDefault":false,
			 "createdAt":"2026-01-01T00:00:00Z","tenantId":"tenant-1"}
		],"totalCount":1}`))
	})
	defer server.Close()

	name := "web"
	readResp := readWith(t, server.URL, nil, &name, nil)
	if !readResp.diagnostics.HasError() {
		t.Fatal("a name that matches nothing must fail the read")
	}
	if got := readResp.text(); !strings.Contains(got, "No security group with this name") {
		t.Errorf("the diagnostic must say the lookup is empty: %s", got)
	}
}

// The list pages on a marker: a match beyond the first page must still
// resolve, across as many requests as it takes.
func TestReadByNameWalksListPages(t *testing.T) {
	var requests atomic.Int32

	row := func(id, name string) string {
		return fmt.Sprintf(`{"id":%q,"name":%q,"isDefault":false,
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
				rows = append(rows, row(fmt.Sprintf("sg-p1-%02d", i), "filler"))
			}
			next = "marker-page-2"
		case "marker-page-2":
			rows = append(rows, row("sg-web", "web"))
		default:
			rows = nil
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"securityGroups":[%s],"totalCount":%d,
			"marker":%q,"nextMarker":%q}`,
			strings.Join(rows, ","), len(rows), marker, next)
	})
	defer server.Close()

	name := "web"
	readResp := readWith(t, server.URL, nil, &name, nil)
	if readResp.diagnostics.HasError() {
		t.Fatalf("read: %v", readResp.diagnostics.Errors())
	}
	if readResp.model.ID.ValueString() != "sg-web" {
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
		_, _ = w.Write([]byte(`{"securityGroups":[
			{"id":"sg-web","name":"web","vpcId":"vpc-9","isDefault":false,
			 "createdAt":"2026-01-01T00:00:00Z","tenantId":"tenant-1"}
		],"totalCount":1}`))
	})
	defer server.Close()

	name, vpc := "web", "vpc-9"
	readResp := readWith(t, server.URL, nil, &name, &vpc)
	if readResp.diagnostics.HasError() {
		t.Fatalf("read: %v", readResp.diagnostics.Errors())
	}
	if q := gotQuery.Load(); q == nil || q.Get("vpcId") != "vpc-9" || q.Get("name") != "web" {
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
		_, _ = w.Write([]byte(`{"securityGroups":[
			{"id":"sg-web","name":"web","vpcId":"vpc-2","isDefault":false,
			 "createdAt":"2026-01-01T00:00:00Z","tenantId":"tenant-1"}
		],"totalCount":1}`))
	})
	defer server.Close()

	name, vpc := "web", "vpc-1"
	readResp := readWith(t, server.URL, nil, &name, &vpc)
	if !readResp.diagnostics.HasError() {
		t.Fatal("a name outside the named VPC must not resolve")
	}
	got := readResp.text()
	if !strings.Contains(got, "No security group with this name") || !strings.Contains(got, "vpc-1") {
		t.Errorf("the diagnostic must say the lookup is empty and name the VPC: %s", got)
	}
}

func TestReadByName404WhenNoNameMatches(t *testing.T) {
	server := newTestServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"securityGroups":[
			{"id":"sg-1","name":"database","isDefault":false,
			 "createdAt":"2026-01-01T00:00:00Z","tenantId":"tenant-1"}
		],"totalCount":1}`))
	})
	defer server.Close()

	name := "does-not-exist"
	readResp := readWith(t, server.URL, nil, &name, nil)
	if !readResp.diagnostics.HasError() {
		t.Fatal("a name that matches nothing must fail the read")
	}
	if got := readResp.text(); !strings.Contains(got, "No security group with this name") {
		t.Errorf("the diagnostic must say the lookup is empty: %s", got)
	}
}

// Two groups with the same name: refuse, naming the colliding ids.
func TestReadByNameRefusesAnAmbiguousName(t *testing.T) {
	server := newTestServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"securityGroups":[
			{"id":"sg-a","name":"web","isDefault":false,
			 "createdAt":"2026-01-01T00:00:00Z","tenantId":"tenant-1"},
			{"id":"sg-b","name":"web","isDefault":false,
			 "createdAt":"2026-01-02T00:00:00Z","tenantId":"tenant-1"}
		],"totalCount":2}`))
	})
	defer server.Close()

	name := "web"
	readResp := readWith(t, server.URL, nil, &name, nil)
	if !readResp.diagnostics.HasError() {
		t.Fatal("an ambiguous name must refuse")
	}
	got := readResp.text()
	if !strings.Contains(got, "sg-a") || !strings.Contains(got, "sg-b") {
		t.Errorf("the diagnostic must name the colliding ids: %s", got)
	}
}

// --- surfaces ---

// A NESTED 404 (the api-gateway's unrouted-path envelope) is not a verdict
// about the security group; it takes the generic arm.
func TestReadTreatsANested404AsAGenericFailure(t *testing.T) {
	server := newTestServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":{"code":"NOT_FOUND","message":"no route found for path"}}`))
	})
	defer server.Close()

	id := "sg-1"
	readResp := readWith(t, server.URL, &id, nil, nil)
	if !readResp.diagnostics.HasError() {
		t.Fatal("an ambiguous verdict must error")
	}
	got := readResp.text()
	if strings.Contains(got, "does not exist") {
		t.Errorf("a nested 404 is not a verdict about the security group; it must not read as gone: %s", got)
	}
	if !strings.Contains(got, "Failed to Read Security Group") {
		t.Errorf("a nested 404 must take the generic arm: %s", got)
	}
}

func TestReadSurfacesABadResponseBody(t *testing.T) {
	server := newTestServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("not json"))
	})
	defer server.Close()

	id := "sg-1"
	readResp := readWith(t, server.URL, &id, nil, nil)
	if !readResp.diagnostics.HasError() {
		t.Fatal("an unparseable body must surface")
	}
}

// A null optional field renders as null, never "" — the absent description is
// the vector, and an empty vpcId must not masquerade as a VPC.
func TestReadMapsAbsentOptionalsToNull(t *testing.T) {
	server := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/tenants/tenant-1/security-groups/sg-1" {
			writeFlatError(w, http.StatusNotFound, "not_found", "not found")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"sg-1","name":"default","isDefault":true,
			"createdAt":"2026-01-01T00:00:00Z","tenantId":"tenant-1"}`))
	})
	defer server.Close()

	id := "sg-1"
	readResp := readWith(t, server.URL, &id, nil, nil)
	if readResp.diagnostics.HasError() {
		t.Fatalf("read: %v", readResp.diagnostics.Errors())
	}
	m := readResp.model
	for name, check := range map[string]func() bool{
		"description": m.Description.IsNull,
		"vpc_id":      m.VPCID.IsNull,
		"updated_at":  m.UpdatedAt.IsNull,
	} {
		if !check() {
			t.Errorf("%s must render as null when the wire omits it", name)
		}
	}
	if !m.Tags.IsNull() {
		t.Error("tags with no entries must render as a null map")
	}
}
