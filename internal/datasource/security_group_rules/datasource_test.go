package security_group_rules

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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

const testGroupPath = "/v1/tenants/t-123/security-groups/sg-123"

// --- schema, metadata, configure ---

func TestMetadata(t *testing.T) {
	ds := NewDataSource()
	req := datasource.MetadataRequest{ProviderTypeName: "frostmoln"}
	var resp datasource.MetadataResponse
	ds.Metadata(context.Background(), req, &resp)
	if resp.TypeName != "frostmoln_security_group_rules" {
		t.Errorf("expected frostmoln_security_group_rules, got %s", resp.TypeName)
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
	for _, attr := range []string{"security_group_id", "id", "rules"} {
		if _, ok := s.Attributes[attr]; !ok {
			t.Errorf("attribute %s missing from the schema", attr)
		}
	}
	if sgID, ok := s.Attributes["security_group_id"].(schema.StringAttribute); !ok || !sgID.Required {
		t.Error("security_group_id must be a required string attribute")
	}
	// The plan-time id guard is load-bearing (a bad id must fail the
	// configuration, not become a request): its presence on the attribute is
	// pinned here so a refactor cannot silently drop it.
	if sgID, ok := s.Attributes["security_group_id"].(schema.StringAttribute); !ok || len(sgID.Validators) == 0 {
		t.Error("security_group_id must carry the plan-time id validator")
	}
	if id, ok := s.Attributes["id"].(schema.StringAttribute); !ok || !id.Computed {
		t.Error("id must be a computed string attribute")
	}
	rules, ok := s.Attributes["rules"].(schema.ListNestedAttribute)
	if !ok {
		t.Fatalf("rules must be a ListNestedAttribute, got %T", s.Attributes["rules"])
	}
	if !rules.Computed {
		t.Error("rules must be computed")
	}
	for _, attr := range []string{"id", "direction", "protocol", "port_range_min", "port_range_max", "remote_cidr", "remote_group_id", "description"} {
		row, ok := rules.NestedObject.Attributes[attr]
		if !ok {
			t.Errorf("row attribute %s missing from the schema", attr)
			continue
		}
		// A row attribute that is not Computed would surface as an unwanted
		// plan-time input; every row of this listing is read-only.
		if !row.IsComputed() {
			t.Errorf("row attribute %s must be computed", attr)
		}
	}
}

func TestConfigureNilProviderData(t *testing.T) {
	ds := &securityGroupRulesDataSource{}
	var resp datasource.ConfigureResponse
	ds.Configure(context.Background(), datasource.ConfigureRequest{}, &resp)
	if resp.Diagnostics.HasError() {
		t.Errorf("expected no errors, got %v", resp.Diagnostics.Errors())
	}
}

func TestConfigureWrongType(t *testing.T) {
	ds := &securityGroupRulesDataSource{}
	var resp datasource.ConfigureResponse
	ds.Configure(context.Background(), datasource.ConfigureRequest{ProviderData: "not-a-client"}, &resp)
	if !resp.Diagnostics.HasError() {
		t.Error("expected error for wrong type")
	}
}

// --- the plan-time id guard ---

// The same vector the vpc_routes listing pins
// (internal/datasource/vpc_routes TestVPCIDValidatorRefusesUnusableIDs) — the
// two guards are deliberate copies building URLs the same way, so extend BOTH
// vectors together. A "." or ".." id does not stay one path segment: the client
// joins with path.Join, which CLEANS, and the cleaned URL addresses a DIFFERENT
// resource. The SG resources run no such guard today; that changes nothing
// about what a configuration-reachable path must refuse here.
func TestSecurityGroupIDValidatorRefusesUnusableIDs(t *testing.T) {
	for _, sgID := range []string{"", ".", "..", "a/b", "a/..", `a\b`, "a?b", "a#b", "a%2fb"} {
		var resp validator.StringResponse
		validSecurityGroupIDValidator{}.ValidateString(context.Background(), stringRequest(sgID), &resp)
		if !resp.Diagnostics.HasError() {
			t.Errorf("the security group id %q must be refused at plan time", sgID)
		}
		// And at the request boundary, where Read re-runs the same guard.
		if err := validSecurityGroupID(sgID); err == nil {
			t.Errorf("the security group id %q must be refused by the request-boundary guard too", sgID)
		}
	}
	if err := validSecurityGroupID("sg-abc123"); err != nil {
		t.Errorf("an ordinary security group id must be accepted: %v", err)
	}
	var unknown validator.StringResponse
	validSecurityGroupIDValidator{}.ValidateString(context.Background(), stringRequestUnknown(), &unknown)
	if unknown.Diagnostics.HasError() {
		t.Errorf("an unknown value (a reference to another resource) must not error: %v", unknown.Diagnostics)
	}
}

// A Read with an unusable id must refuse without issuing any request.
func TestReadRefusesAnUnusableIDWithoutCallingTheAPI(t *testing.T) {
	var requests atomic.Int32
	server := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusNotFound)
	})
	defer server.Close()

	readResp := readListing(t, server.URL, "..")
	if !readResp.diagnostics.HasError() {
		t.Fatal("a dot-segment security group id must be refused")
	}
	if requests.Load() != 0 {
		t.Errorf("no request should have been made, got %d", requests.Load())
	}
}

// A MISSING id (null configuration value) is the same refusal: no request,
// an error — never a read of some default group that does not exist.
func TestReadRefusesAMissingIDWithoutCallingTheAPI(t *testing.T) {
	var requests atomic.Int32
	server := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusNotFound)
	})
	defer server.Close()

	c := client.NewClient(server.URL, "test-key") // pragma: allowlist secret
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
		"security_group_id": tftypes.NewValue(tftypes.String, nil),
		"id":                tftypes.NewValue(tftypes.String, nil),
		"rules":             tftypes.NewValue(objType.AttributeTypes["rules"], nil),
	})

	var readResp datasource.ReadResponse
	readResp.State = tfsdk.State{Schema: s}
	ds.Read(context.Background(), datasource.ReadRequest{
		Config: tfsdk.Config{Schema: s, Raw: config},
	}, &readResp)

	if !readResp.Diagnostics.HasError() {
		t.Fatal("a null security_group_id must be refused")
	}
	if requests.Load() != 0 {
		t.Errorf("no request should have been made, got %d", requests.Load())
	}
}

// --- happy list ---

// The rows pass through VERBATIM, and the null experiments match the
// resource's own mapping: absent ports are null (never 0), an empty remote
// stays null (never ""). One row here is the injected egress pair's exact
// signature — egress, any protocol, both remotes null.
func TestReadListsTheWholeStoredRuleSet(t *testing.T) {
	var gotPath, gotMethod atomic.Pointer[string]
	server := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		p, m := r.URL.Path, r.Method
		gotPath.Store(&p)
		gotMethod.Store(&m)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"sg-123","name":"app","rules":[
			{"id":"r-1","direction":"ingress","protocol":"tcp","portRangeMin":22,"portRangeMax":22,"remoteCidr":"203.0.113.0/24","description":"ssh from the office"},
			{"id":"r-2","direction":"egress","protocol":"any"},
			{"id":"r-3","direction":"egress","protocol":"tcp","portRangeMax":443,"remoteSecurityGroupId":"sg-peer"}
		]}`))
	})
	defer server.Close()

	readResp := readListing(t, server.URL, "sg-123")
	if readResp.diagnostics.HasError() {
		t.Fatalf("read: %v", readResp.diagnostics.Errors())
	}
	if p := gotPath.Load(); p == nil || *p != testGroupPath {
		t.Errorf("the request went to %v, want %s", p, testGroupPath)
	}
	if m := gotMethod.Load(); m == nil || *m != http.MethodGet {
		t.Errorf("the request method was %v, want GET", m)
	}

	if readResp.model.ID.ValueString() != "sg-123" {
		t.Errorf("id is %q, want the security_group_id", readResp.model.ID.ValueString())
	}
	if readResp.model.SecurityGroupID.ValueString() != "sg-123" {
		t.Errorf("security_group_id is %q", readResp.model.SecurityGroupID.ValueString())
	}
	if readResp.model.Rules.IsNull() {
		t.Fatal("rules must never be null on a successful read")
	}
	if len(readResp.model.Rules.Elements()) != 3 {
		t.Fatalf("rules has %d rows, want 3", len(readResp.model.Rules.Elements()))
	}

	var rows []securityGroupRuleRowModel
	if diags := readResp.model.Rules.ElementsAs(context.Background(), &rows, false); diags.HasError() {
		t.Fatalf("ElementsAs: %v", diags)
	}

	// Row 1: fully populated, verbatim.
	if rows[0].ID.ValueString() != "r-1" || rows[0].Direction.ValueString() != "ingress" || rows[0].Protocol.ValueString() != "tcp" {
		t.Errorf("row 0 identity is %s/%s/%s", rows[0].ID, rows[0].Direction, rows[0].Protocol)
	}
	if rows[0].PortRangeMin.ValueInt64() != 22 || rows[0].PortRangeMax.ValueInt64() != 22 {
		t.Errorf("row 0 ports are %d/%d, want 22/22", rows[0].PortRangeMin.ValueInt64(), rows[0].PortRangeMax.ValueInt64())
	}
	if rows[0].RemoteCIDR.ValueString() != "203.0.113.0/24" {
		t.Errorf("row 0 remote_cidr is %q", rows[0].RemoteCIDR.ValueString())
	}
	if !rows[0].RemoteGroupID.IsNull() {
		t.Errorf("row 0 remote_group_id is %q, want null", rows[0].RemoteGroupID.ValueString())
	}
	if rows[0].Description.ValueString() != "ssh from the office" {
		t.Errorf("row 0 description is %q", rows[0].Description.ValueString())
	}

	// Row 2: BOTH REMOTES NULL, rendered as null — the WHAT A NULL REMOTE MEANS
	// case. The injected pair's signature, shown unsoftened.
	if !rows[1].PortRangeMin.IsNull() || !rows[1].PortRangeMax.IsNull() {
		t.Errorf("row 1 ports are %d/%d, want null/null", rows[1].PortRangeMin.ValueInt64(), rows[1].PortRangeMax.ValueInt64())
	}
	if !rows[1].RemoteCIDR.IsNull() || !rows[1].RemoteGroupID.IsNull() {
		t.Errorf("row 1 remotes are %q/%q, want null/null", rows[1].RemoteCIDR.ValueString(), rows[1].RemoteGroupID.ValueString())
	}
	if !rows[1].Description.IsNull() {
		t.Errorf("row 1 description is %q, want null", rows[1].Description.ValueString())
	}

	// Row 3: one-sided port range and only the group remote set.
	if !rows[2].PortRangeMin.IsNull() {
		t.Errorf("row 2 port min is %d, want null", rows[2].PortRangeMin.ValueInt64())
	}
	if rows[2].PortRangeMax.ValueInt64() != 443 {
		t.Errorf("row 2 port max is %d, want 443", rows[2].PortRangeMax.ValueInt64())
	}
	if !rows[2].RemoteCIDR.IsNull() {
		t.Errorf("row 2 remote_cidr is %q, want null", rows[2].RemoteCIDR.ValueString())
	}
	if rows[2].RemoteGroupID.ValueString() != "sg-peer" {
		t.Errorf("row 2 remote_group_id is %q, want sg-peer", rows[2].RemoteGroupID.ValueString())
	}
}

// --- the empty set ---

// TO A CHECK BLOCK, `[]` AND null ARE DIFFERENT ANSWERS: `length(null)` fails a
// plan on its own, so an honest empty set has to render as an empty list.
func TestReadEmptyRuleSetRendersAnEmptyList(t *testing.T) {
	server := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != testGroupPath {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"sg-123","name":"app","rules":[]}`))
	})
	defer server.Close()

	readResp := readListing(t, server.URL, "sg-123")
	if readResp.diagnostics.HasError() {
		t.Fatalf("read: %v", readResp.diagnostics.Errors())
	}
	if readResp.model.Rules.IsNull() {
		t.Fatal("an empty rule set must render as [], never null")
	}
	if len(readResp.model.Rules.Elements()) != 0 {
		t.Errorf("rules has %d rows, want 0", len(readResp.model.Rules.Elements()))
	}
}

// THE 200 MUST IDENTIFY THE GROUP. A body that names no group — or names a
// DIFFERENT one — is not the group read this provider builds its contract on,
// whatever keys it carries; rendering it as `rules = []` would read as "no
// drift" to a check block. The identity discriminator (not key presence) is
// what the platform's `rules,omitempty` forces — see the guard in Read.
func TestReadRefusesA200ThatDoesNotIdentifyTheGroup(t *testing.T) {
	for name, body := range map[string]string{
		"no id":       `{}`,
		"another id":  `{"id":"sg-other","rules":[]}`,
		"empty id":    `{"id":""}`,
		"with id set": `{"id":"sg-other","rules":null}`,
	} {
		t.Run(name, func(t *testing.T) {
			server := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != testGroupPath {
					w.WriteHeader(http.StatusNotFound)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(body))
			})
			defer server.Close()

			readResp := readListing(t, server.URL, "sg-123")
			if !readResp.diagnostics.HasError() {
				t.Fatalf("a 200 that does not identify the requested group must refuse, not render an empty table")
			}
			if !strings.Contains(readResp.text(), "did not identify the requested group") {
				t.Errorf("the diagnostic must say what was wrong: %s", readResp.text())
			}
		})
	}
}

// THE PLATFORM DROPS THE KEY ON AN EMPTY SET (network's
// `json:"rules,omitempty"` — the empty group answers without `rules`, exactly
// the shape a group ends in after delete_default_egress or the
// import-to-destroy recipe). Once the body identifies the group, an absent or
// null set IS the genuinely empty set, so it renders [] — never an error,
// never null.
func TestReadGroupWithoutARulesKeyRendersAnEmptyList(t *testing.T) {
	for name, body := range map[string]string{
		"key dropped": `{"id":"sg-123","name":"app"}`,
		"null set":    `{"id":"sg-123","name":"app","rules":null}`,
	} {
		t.Run(name, func(t *testing.T) {
			server := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != testGroupPath {
					w.WriteHeader(http.StatusNotFound)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(body))
			})
			defer server.Close()

			readResp := readListing(t, server.URL, "sg-123")
			if readResp.diagnostics.HasError() {
				t.Fatalf("read: %v", readResp.diagnostics.Errors())
			}
			if readResp.model.Rules.IsNull() {
				t.Fatal("an empty rule set must render as [], never null")
			}
			if len(readResp.model.Rules.Elements()) != 0 {
				t.Errorf("rules has %d rows, want 0", len(readResp.model.Rules.Elements()))
			}
		})
	}
}

// --- the one-cause 404 ---

// A FLAT 404 FROM THE GROUP READ IS THE SERVICE'S VERDICT, in one cause and
// one arm: the group is absent or not visible to this caller, both mean stop,
// and the listing fails — it NEVER renders `rules = []`, which every check
// block would read as "no drift". Unlike the routes listing there is NO
// second-cause probe to disambiguate: this is exactly one request.
func TestReadGroupGET404FlatErrorsThatTheGroupDoesNotExist(t *testing.T) {
	var requests atomic.Int32
	server := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		writeFlatError(w, http.StatusNotFound, "not_found", "not found")
	})
	defer server.Close()

	readResp := readListing(t, server.URL, "sg-123")
	if !readResp.diagnostics.HasError() {
		t.Fatal("a listing for a group that does not exist must error, not render []")
	}
	if got := readResp.text(); !strings.Contains(got, "does not exist") {
		t.Errorf("the diagnostic must say the group is gone: %s", got)
	}
	if requests.Load() != 1 {
		t.Errorf("a flat 404 is one cause and one request; got %d requests", requests.Load())
	}
}

// THE ENVELOPE WALL, INHERITED. Only a FLAT 404 is the service giving a
// verdict; the api-gateway answers an unrouted path NESTED under `error`. That
// is not a verdict about the group at all, so it takes the generic arm — an
// error, never "does not exist", never a rendered table.
func TestReadTreatsANested404AsAGenericFailure(t *testing.T) {
	server := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != testGroupPath {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusNotFound)
		// Verbatim api-gateway shape (internal/gateway/gateway.go, pre-rename).
		_, _ = w.Write([]byte(`{"error":{"code":"SG_NOT_FOUND","message":"no route found for path"}}`))
	})
	defer server.Close()

	readResp := readListing(t, server.URL, "sg-123")
	if !readResp.diagnostics.HasError() {
		t.Fatal("an ambiguous verdict must error")
	}
	got := readResp.text()
	if !strings.Contains(got, "Failed to Read Security Group") {
		t.Errorf("a nested 404 must take the generic arm: %s", got)
	}
	if strings.Contains(got, "does not exist") {
		t.Errorf("a nested 404 is not a verdict about the group; it must not read as gone: %s", got)
	}
}

// --- everything else surfaces ---

// A 503 (or any other refusal) is not a verdict about the rule set. Not an
// empty set under any circumstances.
func TestReadSurfacesANon404Refusal(t *testing.T) {
	server := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != testGroupPath {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		writeFlatError(w, http.StatusServiceUnavailable, "SG_UNAVAILABLE",
			"the network service is not available in this deployment")
	})
	defer server.Close()

	readResp := readListing(t, server.URL, "sg-123")
	if !readResp.diagnostics.HasError() {
		t.Fatal("a read refusal must surface, never render an empty set")
	}
}

func TestReadSurfacesABadResponseBody(t *testing.T) {
	server := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != testGroupPath {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("not json"))
	})
	defer server.Close()

	readResp := readListing(t, server.URL, "sg-123")
	if !readResp.diagnostics.HasError() {
		t.Fatal("an unparseable body must surface")
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
	model       securityGroupRulesModel
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

// readListing runs one Read against a server URL with the given
// security_group_id, returning the diagnostics and the resulting state model.
func readListing(t *testing.T, serverURL, sgID string) readResult {
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
		"security_group_id": tftypes.NewValue(tftypes.String, sgID),
		"id":                tftypes.NewValue(tftypes.String, nil),
		"rules":             tftypes.NewValue(objType.AttributeTypes["rules"], nil),
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
		Path:        path.Root("security_group_id"),
		ConfigValue: types.StringValue(value),
	}
}

func stringRequestUnknown() validator.StringRequest {
	return validator.StringRequest{
		Path:        path.Root("security_group_id"),
		ConfigValue: types.StringUnknown(),
	}
}
