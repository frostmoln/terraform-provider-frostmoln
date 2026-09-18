package application_gateway

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
	if resp.TypeName != "frostmoln_application_gateway" {
		t.Errorf("expected frostmoln_application_gateway, got %s", resp.TypeName)
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
		"id", "name", "status", "flavor_id", "version", "vpc_id", "subnet_id",
		"private_ip", "vpc_cidr", "public_ip_mode", "public_ip_id", "public_ip",
		"waf_policy_id", "applied_waf_version", "config_generation", "config_revision",
		"config_status", "config_detail", "config_applied_at", "created_at",
		"updated_at", "tenant_id",
	} {
		if _, ok := s.Attributes[attr]; !ok {
			t.Errorf("attribute %s missing from the schema", attr)
		}
	}
	if id, ok := s.Attributes["id"].(schema.StringAttribute); !ok || !id.Optional {
		t.Error("id must be an optional string attribute")
	}
	if name, ok := s.Attributes["name"].(schema.StringAttribute); !ok || !name.Optional {
		t.Error("name must be an optional string attribute")
	}
	if status, ok := s.Attributes["status"].(schema.StringAttribute); !ok || !status.Computed {
		t.Error("status must be computed")
	}
	// The plan-time id guard is load-bearing (an unusable id must fail the
	// configuration instead of becoming a request); its presence on the
	// attribute is pinned here so a refactor cannot silently drop it.
	if id, ok := s.Attributes["id"].(schema.StringAttribute); !ok || len(id.Validators) == 0 {
		t.Error("id must carry the plan-time id validator")
	}
}

func TestConfigureNilProviderData(t *testing.T) {
	ds := &applicationGatewayDataSource{}
	var resp datasource.ConfigureResponse
	ds.Configure(context.Background(), datasource.ConfigureRequest{}, &resp)
	if resp.Diagnostics.HasError() {
		t.Errorf("expected no errors, got %v", resp.Diagnostics.Errors())
	}
}

func TestConfigureWrongType(t *testing.T) {
	ds := &applicationGatewayDataSource{}
	var resp datasource.ConfigureResponse
	ds.Configure(context.Background(), datasource.ConfigureRequest{ProviderData: "not-a-client"}, &resp)
	if !resp.Diagnostics.HasError() {
		t.Error("expected error for wrong type")
	}
}

// --- the plan-time id guard ---

func TestIDValidatorRefusesUnusableIDs(t *testing.T) {
	for _, id := range []string{"", ".", "..", "a/b", "a/..", `a\b`, "a?b", "a#b", "a%2fb"} {
		if err := validGatewayID(id); err == nil {
			t.Errorf("the gateway id %q must be refused", id)
		}
	}
	if err := validGatewayID("gw-abc123"); err != nil {
		t.Errorf("an ordinary gateway id must be accepted: %v", err)
	}
}

// --- Read plumbing helpers ---

type readResult struct {
	diagnostics diag.Diagnostics
	model       applicationGatewayModel
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

// readWith runs one Read against the server with the given id/name values
// (nil = leave the attribute null), returning diagnostics and the state model.
func readWith(t *testing.T, serverURL string, id, name *string) readResult {
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
		"status":              tftypes.NewValue(tftypes.String, nil),
		"flavor_id":           tftypes.NewValue(tftypes.String, nil),
		"version":             tftypes.NewValue(tftypes.String, nil),
		"vpc_id":              tftypes.NewValue(tftypes.String, nil),
		"subnet_id":           tftypes.NewValue(tftypes.String, nil),
		"private_ip":          tftypes.NewValue(tftypes.String, nil),
		"vpc_cidr":            tftypes.NewValue(tftypes.String, nil),
		"public_ip_mode":      tftypes.NewValue(tftypes.String, nil),
		"public_ip_id":        tftypes.NewValue(tftypes.String, nil),
		"public_ip":           tftypes.NewValue(tftypes.String, nil),
		"waf_policy_id":       tftypes.NewValue(tftypes.String, nil),
		"applied_waf_version": tftypes.NewValue(tftypes.Number, nil),
		"config_generation":   tftypes.NewValue(tftypes.Number, nil),
		"config_revision":     tftypes.NewValue(tftypes.Number, nil),
		"config_status":       tftypes.NewValue(tftypes.String, nil),
		"config_detail":       tftypes.NewValue(tftypes.String, nil),
		"config_applied_at":   tftypes.NewValue(tftypes.String, nil),
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

	readResp := readWith(t, server.URL, nil, nil)
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

	id, name := "gw-1", "edge"
	readResp := readWith(t, server.URL, &id, &name)
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
	readResp := readWith(t, server.URL, &dot, nil)
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
		if r.URL.Path != "/v1/tenants/tenant-1/application-gateways/gw-1" {
			writeFlatError(w, http.StatusNotFound, "not_found", "not found")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":"gw-1","name":"edge","tenantId":"tenant-1","status":"running",
			"flavorId":"agw.co1.medium","version":"2.4.1",
			"vpcId":"vpc-1","subnetId":"subnet-1","privateIp":"10.0.1.10","vpcCidr":"10.0.0.0/16",
			"publicIpMode":"selected","publicIpId":"pip-1","publicIp":"203.0.113.7",
			"wafPolicyId":"waf-1","appliedWafVersion":7,
			"configGeneration":3,"configStatus":"applied","configRevision":3,
			"configAppliedAt":"2026-02-01T12:30:00Z","updatedAt":"2026-02-01T12:30:00Z",
			"createdAt":"2026-01-01T00:00:00Z"
		}`))
	})
	defer server.Close()

	id := "gw-1"
	readResp := readWith(t, server.URL, &id, nil)
	if readResp.diagnostics.HasError() {
		t.Fatalf("read: %v", readResp.diagnostics.Errors())
	}
	if p := gotPath.Load(); p == nil || *p != "/v1/tenants/tenant-1/application-gateways/gw-1" {
		t.Errorf("the request went to %v", p)
	}
	m := readResp.model
	if m.ID.ValueString() != "gw-1" || m.Name.ValueString() != "edge" {
		t.Errorf("identity is %s/%s", m.ID.ValueString(), m.Name.ValueString())
	}
	if m.Status.ValueString() != "running" {
		t.Errorf("status is %s", m.Status.ValueString())
	}
	if m.FlavorID.ValueString() != "agw.co1.medium" || m.Version.ValueString() != "2.4.1" {
		t.Errorf("flavor/version are %s/%s", m.FlavorID.ValueString(), m.Version.ValueString())
	}
	if m.VPCID.ValueString() != "vpc-1" || m.SubnetID.ValueString() != "subnet-1" {
		t.Errorf("placement is %s/%s", m.VPCID.ValueString(), m.SubnetID.ValueString())
	}
	if m.PrivateIP.ValueString() != "10.0.1.10" || m.VPCCIDR.ValueString() != "10.0.0.0/16" {
		t.Errorf("network is %s/%s", m.PrivateIP.ValueString(), m.VPCCIDR.ValueString())
	}
	if m.PublicIPMode.ValueString() != "selected" || m.PublicIPID.ValueString() != "pip-1" || m.PublicIP.ValueString() != "203.0.113.7" {
		t.Errorf("address is %s/%s/%s", m.PublicIPMode.ValueString(), m.PublicIPID.ValueString(), m.PublicIP.ValueString())
	}
	if m.WafPolicyID.ValueString() != "waf-1" || m.AppliedWafVersion.ValueInt64() != 7 {
		t.Errorf("waf is %s/%d", m.WafPolicyID.ValueString(), m.AppliedWafVersion.ValueInt64())
	}
	if m.ConfigGeneration.ValueInt64() != 3 || m.ConfigRevision.ValueInt64() != 3 || m.ConfigStatus.ValueString() != "applied" {
		t.Errorf("config trail is %d/%d/%s", m.ConfigGeneration.ValueInt64(), m.ConfigRevision.ValueInt64(), m.ConfigStatus.ValueString())
	}
	if m.ConfigAppliedAt.ValueString() != "2026-02-01T12:30:00Z" {
		t.Errorf("config_applied_at is %s", m.ConfigAppliedAt.ValueString())
	}
	if m.CreatedAt.ValueString() != "2026-01-01T00:00:00Z" || m.UpdatedAt.ValueString() != "2026-02-01T12:30:00Z" {
		t.Errorf("timestamps are %s/%s", m.CreatedAt.ValueString(), m.UpdatedAt.ValueString())
	}
	if m.TenantID.ValueString() != "tenant-1" {
		t.Errorf("tenant is %s", m.TenantID.ValueString())
	}
}

func TestReadByID404ErrorsThatTheGatewayDoesNotExist(t *testing.T) {
	var requests atomic.Int32
	server := newTestServer(t, func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		writeFlatError(w, http.StatusNotFound, "not_found", "not found")
	})
	defer server.Close()

	id := "gw-gone"
	readResp := readWith(t, server.URL, &id, nil)
	if !readResp.diagnostics.HasError() {
		t.Fatal("a flat 404 must fail the read")
	}
	if got := readResp.text(); !strings.Contains(got, "does not exist") {
		t.Errorf("the diagnostic must say the gateway is gone: %s", got)
	}
	if requests.Load() != 1 {
		t.Errorf("a flat 404 is one cause and one request; got %d", requests.Load())
	}
}

// A soft-deleted gateway reads back with status "deleted" — resolving it
// would feed a tombstone's id into a child resource that can never apply
// again, so the id path reports it as gone.
func TestReadByIDTreatsADeletedGatewayAsGone(t *testing.T) {
	server := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/tenants/tenant-1/application-gateways/gw-1" {
			writeFlatError(w, http.StatusNotFound, "not_found", "not found")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":"gw-1","name":"edge","tenantId":"tenant-1","status":"deleted",
			"flavorId":"agw.co1.medium","version":"2.4.1","vpcId":"vpc-1","subnetId":"subnet-1",
			"publicIpMode":"allocated","configGeneration":0,"configStatus":"pending",
			"createdAt":"2026-01-01T00:00:00Z","updatedAt":"2026-02-01T00:00:00Z",
			"deletedAt":"2026-02-01T00:00:00Z"
		}`))
	})
	defer server.Close()

	id := "gw-1"
	readResp := readWith(t, server.URL, &id, nil)
	if !readResp.diagnostics.HasError() {
		t.Fatal("a deleted gateway must be reported as gone")
	}
	got := readResp.text()
	if !strings.Contains(got, "does not exist") {
		t.Errorf("the diagnostic must say the gateway is gone: %s", got)
	}
	if strings.Contains(got, "did not identify the requested gateway") {
		t.Errorf("the tombstone verdict is not an identity failure: %s", got)
	}
}

// A 200 that does not carry the requested id is not the gateway read this
// provider builds its contract on.
func TestReadByIDRefusesA200ThatDoesNotCarryTheID(t *testing.T) {
	server := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/tenants/tenant-1/application-gateways/gw-1" {
			writeFlatError(w, http.StatusNotFound, "not_found", "not found")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"gw-other","name":"other","status":"running","flavorId":"agw.co1.medium","version":"2.4.1","vpcId":"vpc-1","subnetId":"subnet-1","publicIpMode":"allocated","configGeneration":0,"configStatus":"pending","createdAt":"2026-01-01T00:00:00Z"}`))
	})
	defer server.Close()

	id := "gw-1"
	readResp := readWith(t, server.URL, &id, nil)
	if !readResp.diagnostics.HasError() {
		t.Fatal("a mismatched 200 must refuse")
	}
	if got := readResp.text(); !strings.Contains(got, "did not identify the requested gateway") {
		t.Errorf("the diagnostic must name the identity guard: %s", got)
	}
}

// --- the name path ---

func TestReadByNameResolvesFromTheList(t *testing.T) {
	var gotPath atomic.Pointer[string]
	var requests atomic.Int32
	server := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		gotPath.Store(&p)
		requests.Add(1)
		if r.URL.Path != "/v1/tenants/tenant-1/application-gateways" {
			writeFlatError(w, http.StatusNotFound, "not_found", "not found")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"gateways":[
			{"id":"gw-other","name":"analytics","tenantId":"tenant-1","status":"running",
			 "flavorId":"agw.co1.medium","version":"2.4.1","vpcId":"vpc-1","subnetId":"subnet-1",
			 "privateIp":"10.0.1.9","publicIpMode":"allocated","configGeneration":0,"configStatus":"pending",
			 "createdAt":"2026-01-02T00:00:00Z"},
			{"id":"gw-1","name":"edge","tenantId":"tenant-1","status":"running",
			 "flavorId":"agw.co1.medium","version":"2.4.1","vpcId":"vpc-1","subnetId":"subnet-1",
			 "privateIp":"10.0.1.10","publicIpMode":"allocated","configGeneration":0,"configStatus":"pending",
			 "createdAt":"2026-01-01T00:00:00Z"}
		],"totalCount":2}`))
	})
	defer server.Close()

	name := "edge"
	readResp := readWith(t, server.URL, nil, &name)
	if readResp.diagnostics.HasError() {
		t.Fatalf("read: %v", readResp.diagnostics.Errors())
	}
	if p := gotPath.Load(); p == nil || *p != "/v1/tenants/tenant-1/application-gateways" {
		t.Errorf("the request went to %v", p)
	}
	// The list takes no query parameters, so ONE request is the whole lookup.
	if got := requests.Load(); got != 1 {
		t.Errorf("expected exactly 1 list request, got %d", got)
	}
	m := readResp.model
	if m.ID.ValueString() != "gw-1" || m.Name.ValueString() != "edge" {
		t.Errorf("resolution is %s/%s", m.ID.ValueString(), m.Name.ValueString())
	}
	if m.PrivateIP.ValueString() != "10.0.1.10" {
		t.Errorf("address is %s", m.PrivateIP.ValueString())
	}
	if !m.PublicIP.IsNull() || !m.UpdatedAt.IsNull() {
		t.Errorf("absent optionals must stay null, got public_ip %v updated_at %v",
			m.PublicIP, m.UpdatedAt)
	}
}

// Tombstoned rows with the right name stay invisible: absent, not resolved
// wrong. Both tombstone shapes are covered — status "deleted" and a deletedAt.
func TestReadByNameSkipsADeletedGateway(t *testing.T) {
	server := newTestServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"gateways":[
			{"id":"gw-dead","name":"edge","tenantId":"tenant-1","status":"deleted",
			 "flavorId":"agw.co1.medium","version":"2.4.1","vpcId":"vpc-1","subnetId":"subnet-1",
			 "publicIpMode":"allocated","configGeneration":0,"configStatus":"pending",
			 "createdAt":"2026-01-01T00:00:00Z"},
			{"id":"gw-tomb","name":"edge","tenantId":"tenant-1","status":"running",
			 "flavorId":"agw.co1.medium","version":"2.4.1","vpcId":"vpc-1","subnetId":"subnet-1",
			 "publicIpMode":"allocated","configGeneration":0,"configStatus":"pending",
			 "createdAt":"2026-01-01T00:00:00Z","deletedAt":"2026-02-01T00:00:00Z"}
		],"totalCount":2}`))
	})
	defer server.Close()

	name := "edge"
	readResp := readWith(t, server.URL, nil, &name)
	if !readResp.diagnostics.HasError() {
		t.Fatal("a deleted gateway must not resolve")
	}
	if got := readResp.text(); !strings.Contains(got, "No Application Gateway with this name") {
		t.Errorf("the diagnostic must say the lookup is empty: %s", got)
	}
}

func TestReadByName404WhenNoGatewayMatches(t *testing.T) {
	server := newTestServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"gateways":[{"id":"gw-1","name":"analytics","tenantId":"tenant-1","status":"running","flavorId":"agw.co1.medium","version":"2.4.1","vpcId":"vpc-1","subnetId":"subnet-1","publicIpMode":"allocated","configGeneration":0,"configStatus":"pending","createdAt":"2026-01-01T00:00:00Z"}],"totalCount":1}`))
	})
	defer server.Close()

	name := "does-not-exist"
	readResp := readWith(t, server.URL, nil, &name)
	if !readResp.diagnostics.HasError() {
		t.Fatal("a name that matches nothing must fail the read")
	}
	if got := readResp.text(); !strings.Contains(got, "No Application Gateway with this name") {
		t.Errorf("the diagnostic must say the lookup is empty: %s", got)
	}
}

// Two gateways with the same name: refuse, naming the colliding ids.
func TestReadByNameRefusesAnAmbiguousName(t *testing.T) {
	server := newTestServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"gateways":[
			{"id":"gw-a","name":"edge","tenantId":"tenant-1","status":"running","flavorId":"agw.co1.medium","version":"2.4.1","vpcId":"vpc-1","subnetId":"subnet-1","publicIpMode":"allocated","configGeneration":0,"configStatus":"pending","createdAt":"2026-01-01T00:00:00Z"},
			{"id":"gw-b","name":"edge","tenantId":"tenant-1","status":"running","flavorId":"agw.co1.medium","version":"2.4.1","vpcId":"vpc-1","subnetId":"subnet-1","publicIpMode":"allocated","configGeneration":0,"configStatus":"pending","createdAt":"2026-01-02T00:00:00Z"}
		],"totalCount":2}`))
	})
	defer server.Close()

	name := "edge"
	readResp := readWith(t, server.URL, nil, &name)
	if !readResp.diagnostics.HasError() {
		t.Fatal("an ambiguous name must refuse")
	}
	got := readResp.text()
	if !strings.Contains(got, "gw-a") || !strings.Contains(got, "gw-b") {
		t.Errorf("the diagnostic must name the colliding ids: %s", got)
	}
}

// --- surfaces ---

// A NESTED 404 (the api-gateway's unrouted-path envelope) is not a verdict
// about the gateway; it takes the generic arm.
func TestReadTreatsANested404AsAGenericFailure(t *testing.T) {
	server := newTestServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":{"code":"NOT_FOUND","message":"no route found for path"}}`))
	})
	defer server.Close()

	id := "gw-1"
	readResp := readWith(t, server.URL, &id, nil)
	if !readResp.diagnostics.HasError() {
		t.Fatal("an ambiguous verdict must error")
	}
	got := readResp.text()
	if strings.Contains(got, "does not exist") {
		t.Errorf("a nested 404 is not a verdict about the gateway; it must not read as gone: %s", got)
	}
	if !strings.Contains(got, "Failed to Read Application Gateway") {
		t.Errorf("a nested 404 must take the generic arm: %s", got)
	}
}

func TestReadSurfacesABadResponseBody(t *testing.T) {
	server := newTestServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("not json"))
	})
	defer server.Close()

	id := "gw-1"
	readResp := readWith(t, server.URL, &id, nil)
	if !readResp.diagnostics.HasError() {
		t.Fatal("an unparseable body must surface")
	}
}

// A null optional renders as null, never "" or 0 — but the ALWAYS-ON config
// trail does not: a gateway with nothing authored sits at generation 0 and
// reports 0, because that zero is a real fact. The always-on ints are the
// vector here in both directions.
func TestReadMapsAbsentOptionalsToNull(t *testing.T) {
	server := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/tenants/tenant-1/application-gateways/gw-1" {
			writeFlatError(w, http.StatusNotFound, "not_found", "not found")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"gw-1","name":"edge","tenantId":"tenant-1","status":"provisioning","flavorId":"agw.co1.medium","version":"2.4.1","vpcId":"vpc-1","subnetId":"subnet-1","publicIpMode":"allocated","appliedWafVersion":0,"configGeneration":0,"configStatus":"pending","createdAt":"2026-01-01T00:00:00Z"}`))
	})
	defer server.Close()

	id := "gw-1"
	readResp := readWith(t, server.URL, &id, nil)
	if readResp.diagnostics.HasError() {
		t.Fatalf("read: %v", readResp.diagnostics.Errors())
	}
	m := readResp.model
	for name, check := range map[string]func() bool{
		"private_ip":        m.PrivateIP.IsNull,
		"vpc_cidr":          m.VPCCIDR.IsNull,
		"public_ip_id":      m.PublicIPID.IsNull,
		"public_ip":         m.PublicIP.IsNull,
		"waf_policy_id":     m.WafPolicyID.IsNull,
		"config_revision":   m.ConfigRevision.IsNull,
		"config_detail":     m.ConfigDetail.IsNull,
		"config_applied_at": m.ConfigAppliedAt.IsNull,
		"updated_at":        m.UpdatedAt.IsNull,
	} {
		if !check() {
			t.Errorf("%s must render as null when the wire omits it", name)
		}
	}
	if m.AppliedWafVersion.ValueInt64() != 0 || m.ConfigGeneration.ValueInt64() != 0 {
		t.Errorf("the always-on config ints must render as their wire zero, got %d/%d",
			m.AppliedWafVersion.ValueInt64(), m.ConfigGeneration.ValueInt64())
	}
	if m.ConfigStatus.ValueString() != "pending" {
		t.Errorf("config_status is always on the wire, got %s", m.ConfigStatus.ValueString())
	}
}

// The returned window: the appgw list takes NO page parameters, so the
// envelope's totalCount is the only window check there is. When it names more
// gateways than the one answered page carried, zero matches must read as "not
// in the returned window", never as plain absence.
func TestReadByNameReportsTheReturnedWindowInsteadOfAbsence(t *testing.T) {
	server := newTestServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"gateways":[{"id":"gw-1","name":"edge","status":"active"}],"totalCount":7}`))
	})
	defer server.Close()

	name := "missing"
	readResp := readWith(t, server.URL, nil, &name)
	if !readResp.diagnostics.HasError() {
		t.Fatal("a full envelope that truncates its page must fail the read")
	}
	got := readResp.text()
	if !strings.Contains(got, "returned window") {
		t.Errorf("a truncated page must report the window, not absence: %s", got)
	}
	if strings.Contains(got, "Check the spelling") {
		t.Errorf("the not-found copy must not fire for a truncated page: %s", got)
	}
}
