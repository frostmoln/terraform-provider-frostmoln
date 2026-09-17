package kubernetes_cluster

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
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
	if resp.TypeName != "frostmoln_kubernetes_cluster" {
		t.Errorf("expected frostmoln_kubernetes_cluster, got %s", resp.TypeName)
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
		"id", "name", "status", "version", "control_plane_tier", "ha_enabled",
		"region", "vpc_id", "subnet_id", "pod_cidr", "service_cidr", "endpoint",
		"load_balancer_id", "public_ip", "ca_cert_hash", "created_at",
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
	for _, attr := range []string{"status", "version", "control_plane_tier", "ha_enabled", "endpoint", "ca_cert_hash"} {
		if a, ok := s.Attributes[attr]; !ok || !a.IsComputed() {
			t.Errorf("%s must be computed", attr)
		}
	}
	// One collection, one owner: the addons collection lives on
	// frostmoln_kubernetes_cluster_addons and must never be mirrored here.
	if _, ok := s.Attributes["addons"]; ok {
		t.Error("addons must NOT be on this schema; frostmoln_kubernetes_cluster_addons owns that collection")
	}
	// The plan-time id guard is load-bearing (an unusable id must fail the
	// configuration instead of becoming a request); its presence on the
	// attribute is pinned here so a refactor cannot silently drop it.
	if id, ok := s.Attributes["id"].(schema.StringAttribute); !ok || len(id.Validators) == 0 {
		t.Error("id must carry the plan-time id validator")
	}
}

func TestConfigureNilProviderData(t *testing.T) {
	ds := &kubernetesClusterDataSource{}
	var resp datasource.ConfigureResponse
	ds.Configure(context.Background(), datasource.ConfigureRequest{}, &resp)
	if resp.Diagnostics.HasError() {
		t.Errorf("expected no errors, got %v", resp.Diagnostics.Errors())
	}
}

func TestConfigureWrongType(t *testing.T) {
	ds := &kubernetesClusterDataSource{}
	var resp datasource.ConfigureResponse
	ds.Configure(context.Background(), datasource.ConfigureRequest{ProviderData: "not-a-client"}, &resp)
	if !resp.Diagnostics.HasError() {
		t.Error("expected error for wrong type")
	}
}

// --- the plan-time id guard ---

func TestIDValidatorRefusesUnusableIDs(t *testing.T) {
	for _, id := range []string{"", ".", "..", "a/b", "a/..", `a\b`, "a?b", "a#b", "a%2fb"} {
		if err := validClusterID(id); err == nil {
			t.Errorf("the cluster id %q must be refused", id)
		}
	}
	if err := validClusterID("cluster-abc123"); err != nil {
		t.Errorf("an ordinary cluster id must be accepted: %v", err)
	}
}

// --- Read plumbing helpers ---

type readResult struct {
	diagnostics diag.Diagnostics
	model       kubernetesClusterModel
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
		"id":                 stringOr(id),
		"name":               stringOr(name),
		"status":             tftypes.NewValue(tftypes.String, nil),
		"version":            tftypes.NewValue(tftypes.String, nil),
		"control_plane_tier": tftypes.NewValue(tftypes.String, nil),
		"ha_enabled":         tftypes.NewValue(tftypes.Bool, nil),
		"region":             tftypes.NewValue(tftypes.String, nil),
		"vpc_id":             tftypes.NewValue(tftypes.String, nil),
		"subnet_id":          tftypes.NewValue(tftypes.String, nil),
		"pod_cidr":           tftypes.NewValue(tftypes.String, nil),
		"service_cidr":       tftypes.NewValue(tftypes.String, nil),
		"endpoint":           tftypes.NewValue(tftypes.String, nil),
		"load_balancer_id":   tftypes.NewValue(tftypes.String, nil),
		"public_ip":          tftypes.NewValue(tftypes.String, nil),
		"ca_cert_hash":       tftypes.NewValue(tftypes.String, nil),
		"created_at":         tftypes.NewValue(tftypes.String, nil),
		"updated_at":         tftypes.NewValue(tftypes.String, nil),
		"tenant_id":          tftypes.NewValue(tftypes.String, nil),
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

	id, name := "cluster-1", "billing-prod"
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
		if r.URL.Path != "/v1/tenants/tenant-1/kubernetes-clusters/cluster-1" {
			writeFlatError(w, http.StatusNotFound, "not_found", "not found")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":"cluster-1","name":"billing-prod","tenantId":"tenant-1","status":"running",
			"kubernetesVersion":"1.35","controlPlaneTier":"production","haEnabled":true,
			"region":"arl1","vpcId":"vpc-1","subnetId":"subnet-1",
			"podCidr":"10.200.0.0/16","serviceCidr":"10.210.0.0/18",
			"endpoint":"https://api.billing.k8s.frostmoln.cloud:6443",
			"loadBalancerId":"lb-1","publicIp":"203.0.113.10",
			"caCertHash":"sha256:4f9d2c8e8b9a",
			"createdAt":"2026-01-01T00:00:00Z","updatedAt":"2026-02-01T00:00:00Z"
		}`))
	})
	defer server.Close()

	id := "cluster-1"
	readResp := readWith(t, server.URL, &id, nil)
	if readResp.diagnostics.HasError() {
		t.Fatalf("read: %v", readResp.diagnostics.Errors())
	}
	if p := gotPath.Load(); p == nil || *p != "/v1/tenants/tenant-1/kubernetes-clusters/cluster-1" {
		t.Errorf("the request went to %v", p)
	}
	m := readResp.model
	if m.ID.ValueString() != "cluster-1" || m.Name.ValueString() != "billing-prod" {
		t.Errorf("identity is %s/%s", m.ID.ValueString(), m.Name.ValueString())
	}
	if m.Status.ValueString() != "running" || m.Version.ValueString() != "1.35" {
		t.Errorf("status/version is %s/%s", m.Status.ValueString(), m.Version.ValueString())
	}
	if m.ControlPlaneTier.ValueString() != "production" || !m.HAEnabled.ValueBool() {
		t.Errorf("tier/ha are %s/%t", m.ControlPlaneTier.ValueString(), m.HAEnabled.ValueBool())
	}
	if m.Region.ValueString() != "arl1" || m.VPCID.ValueString() != "vpc-1" || m.SubnetID.ValueString() != "subnet-1" {
		t.Errorf("placement is %s/%s/%s", m.Region.ValueString(), m.VPCID.ValueString(), m.SubnetID.ValueString())
	}
	if m.PodCIDR.ValueString() != "10.200.0.0/16" || m.ServiceCIDR.ValueString() != "10.210.0.0/18" {
		t.Errorf("cidrs are %s/%s", m.PodCIDR.ValueString(), m.ServiceCIDR.ValueString())
	}
	if m.Endpoint.ValueString() != "https://api.billing.k8s.frostmoln.cloud:6443" || m.LoadBalancerID.ValueString() != "lb-1" {
		t.Errorf("endpoint is %s/%s", m.Endpoint.ValueString(), m.LoadBalancerID.ValueString())
	}
	if m.PublicIP.ValueString() != "203.0.113.10" || m.CACertHash.ValueString() != "sha256:4f9d2c8e8b9a" {
		t.Errorf("exposure/ca hash are %s/%s", m.PublicIP.ValueString(), m.CACertHash.ValueString())
	}
	if m.TenantID.ValueString() != "tenant-1" {
		t.Errorf("tenant is %s", m.TenantID.ValueString())
	}
	if m.CreatedAt.ValueString() != "2026-01-01T00:00:00Z" || m.UpdatedAt.ValueString() != "2026-02-01T00:00:00Z" {
		t.Errorf("timestamps are %s/%s", m.CreatedAt.ValueString(), m.UpdatedAt.ValueString())
	}
}

func TestReadByID404ErrorsThatTheClusterDoesNotExist(t *testing.T) {
	var requests atomic.Int32
	server := newTestServer(t, func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		writeFlatError(w, http.StatusNotFound, "not_found", "not found")
	})
	defer server.Close()

	id := "cluster-gone"
	readResp := readWith(t, server.URL, &id, nil)
	if !readResp.diagnostics.HasError() {
		t.Fatal("a flat 404 must fail the read")
	}
	if got := readResp.text(); !strings.Contains(got, "does not exist") {
		t.Errorf("the diagnostic must say the cluster is gone: %s", got)
	}
	if requests.Load() != 1 {
		t.Errorf("a flat 404 is one cause and one request; got %d", requests.Load())
	}
}

// Deletes are SOFT: a deleted cluster answers 200 with status "deleted"
// forever, never 404 — treated as absent, exactly like the
// frostmoln_kubernetes_cluster resource's Read.
func TestReadByIDTreatsASoftDeletedClusterAsAbsent(t *testing.T) {
	server := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/tenants/tenant-1/kubernetes-clusters/cluster-1" {
			writeFlatError(w, http.StatusNotFound, "not_found", "not found")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"cluster-1","name":"gone","status":"deleted","kubernetesVersion":"1.35","controlPlaneTier":"production","haEnabled":false,"region":"arl1","vpcId":"vpc-1","subnetId":"subnet-1","createdAt":"2026-01-01T00:00:00Z"}`))
	})
	defer server.Close()

	id := "cluster-1"
	readResp := readWith(t, server.URL, &id, nil)
	if !readResp.diagnostics.HasError() {
		t.Fatal("a soft-deleted cluster must be treated as absent")
	}
	got := readResp.text()
	if !strings.Contains(got, "does not exist") || !strings.Contains(got, "has been deleted") {
		t.Errorf("the diagnostic must say the cluster is gone because it has been deleted: %s", got)
	}
}

// A 200 that does not carry the requested id is not the cluster read this
// provider builds its contract on.
func TestReadByIDRefusesA200ThatDoesNotCarryTheID(t *testing.T) {
	server := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/tenants/tenant-1/kubernetes-clusters/cluster-1" {
			writeFlatError(w, http.StatusNotFound, "not_found", "not found")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"cluster-other","name":"other","status":"running","kubernetesVersion":"1.35","controlPlaneTier":"development","haEnabled":false,"region":"arl1","vpcId":"vpc-1","subnetId":"subnet-1","createdAt":"2026-01-01T00:00:00Z"}`))
	})
	defer server.Close()

	id := "cluster-1"
	readResp := readWith(t, server.URL, &id, nil)
	if !readResp.diagnostics.HasError() {
		t.Fatal("a mismatched 200 must refuse")
	}
	if got := readResp.text(); !strings.Contains(got, "did not identify the requested cluster") {
		t.Errorf("the diagnostic must name the identity guard: %s", got)
	}
}

// --- the name path ---

func TestReadByNameResolvesFromTheList(t *testing.T) {
	var gotQuery atomic.Pointer[url.Values]
	newListBody := func(pages [][]string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			q := r.URL.Query()
			gotQuery.Store(&q)
			limit, _ := strconv.Atoi(q.Get("limit"))
			if limit == 0 {
				limit = 50
			}
			offset, _ := strconv.Atoi(q.Get("offset"))
			pageIdx := offset / limit
			rows := []string{}
			if pageIdx < len(pages) {
				rows = pages[pageIdx]
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprintf(w, `{"clusters":[%s],"totalCount":%d}`,
				strings.Join(rows, ","), len(rows))
		}
	}

	billing := `{"id":"cluster-billing","name":"billing-prod","status":"running","kubernetesVersion":"1.35","controlPlaneTier":"production","haEnabled":true,"region":"arl1","vpcId":"vpc-1","subnetId":"subnet-1","endpoint":"https://api.billing.k8s.frostmoln.cloud:6443","podCidr":"10.200.0.0/16","serviceCidr":"10.210.0.0/18","createdAt":"2026-01-01T00:00:00Z"}`
	other := `{"id":"cluster-other","name":"analytics","status":"running","kubernetesVersion":"1.34","controlPlaneTier":"development","haEnabled":false,"region":"arl1","vpcId":"vpc-1","subnetId":"subnet-1","createdAt":"2026-01-02T00:00:00Z"}`

	server := newTestServer(t, newListBody([][]string{{other, billing}}))
	defer server.Close()

	name := "billing-prod"
	readResp := readWith(t, server.URL, nil, &name)
	if readResp.diagnostics.HasError() {
		t.Fatalf("read: %v", readResp.diagnostics.Errors())
	}
	if q := gotQuery.Load(); q == nil || (q.Get("limit") == "" && q.Get("offset") == "") {
		t.Errorf("the list request must page explicitly, got query %v", q)
	}
	m := readResp.model
	if m.ID.ValueString() != "cluster-billing" || m.Version.ValueString() != "1.35" {
		t.Errorf("resolution is %s/%s", m.ID.ValueString(), m.Version.ValueString())
	}
	if m.Endpoint.ValueString() != "https://api.billing.k8s.frostmoln.cloud:6443" {
		t.Errorf("endpoint is %s", m.Endpoint.ValueString())
	}
	if !m.PublicIP.IsNull() {
		t.Errorf("public_ip absent on the wire must stay null")
	}
	if !m.UpdatedAt.IsNull() {
		t.Errorf("updated_at absent on the wire must stay null")
	}
}

// A soft-deleted row with the right name stays invisible to the resolver:
// absent, not resolved as a phantom.
func TestReadByNameSkipsASoftDeletedRowWithTheSameName(t *testing.T) {
	server := newTestServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"clusters":[{"id":"cluster-1","name":"gone","status":"deleted","kubernetesVersion":"1.35","controlPlaneTier":"production","haEnabled":false,"region":"arl1","vpcId":"vpc-1","subnetId":"subnet-1","createdAt":"2026-01-01T00:00:00Z"}],"totalCount":1}`))
	})
	defer server.Close()

	name := "gone"
	readResp := readWith(t, server.URL, nil, &name)
	if !readResp.diagnostics.HasError() {
		t.Fatal("a soft-deleted row must not resolve")
	}
	if got := readResp.text(); !strings.Contains(got, "No Kubernetes cluster with this name") {
		t.Errorf("the diagnostic must say the lookup is empty: %s", got)
	}
}

func TestReadByName404WhenNoClusterMatches(t *testing.T) {
	server := newTestServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"clusters":[{"id":"cluster-1","name":"analytics","status":"running","kubernetesVersion":"1.35","controlPlaneTier":"development","haEnabled":false,"region":"arl1","vpcId":"vpc-1","subnetId":"subnet-1","createdAt":"2026-01-01T00:00:00Z"}],"totalCount":1}`))
	})
	defer server.Close()

	name := "does-not-exist"
	readResp := readWith(t, server.URL, nil, &name)
	if !readResp.diagnostics.HasError() {
		t.Fatal("a name that matches nothing must fail the read")
	}
	if got := readResp.text(); !strings.Contains(got, "No Kubernetes cluster with this name") {
		t.Errorf("the diagnostic must say the lookup is empty: %s", got)
	}
}

// Two live clusters with the same name: refuse, naming the colliding ids.
func TestReadByNameRefusesAnAmbiguousName(t *testing.T) {
	server := newTestServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"clusters":[
			{"id":"cluster-a","name":"billing-prod","status":"running","kubernetesVersion":"1.35","controlPlaneTier":"development","haEnabled":false,"region":"arl1","vpcId":"vpc-1","subnetId":"subnet-1","createdAt":"2026-01-01T00:00:00Z"},
			{"id":"cluster-b","name":"billing-prod","status":"running","kubernetesVersion":"1.35","controlPlaneTier":"development","haEnabled":false,"region":"arl1","vpcId":"vpc-1","subnetId":"subnet-1","createdAt":"2026-01-02T00:00:00Z"}
		],"totalCount":2}`))
	})
	defer server.Close()

	name := "billing-prod"
	readResp := readWith(t, server.URL, nil, &name)
	if !readResp.diagnostics.HasError() {
		t.Fatal("an ambiguous name must refuse")
	}
	got := readResp.text()
	if !strings.Contains(got, "cluster-a") || !strings.Contains(got, "cluster-b") {
		t.Errorf("the diagnostic must name the colliding ids: %s", got)
	}
}

// The list pages at the repository's default limit — a name on page 2 must
// still resolve, across as many requests as it takes.
func TestReadByNameWalksListPages(t *testing.T) {
	var requests atomic.Int32

	row := func(id string) string {
		return fmt.Sprintf(`{"id":%q,"name":"cluster-%s","status":"running","kubernetesVersion":"1.35","controlPlaneTier":"development","haEnabled":false,"region":"arl1","vpcId":"vpc-1","subnetId":"subnet-1","createdAt":"2026-01-01T00:00:00Z"}`, id, strings.TrimPrefix(id, "cluster-"))
	}

	server := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		offset := r.URL.Query().Get("offset")
		var rows []string
		switch offset {
		case "", "0":
			for i := 0; i < 100; i++ {
				rows = append(rows, row(fmt.Sprintf("cluster-p1-%02d", i)))
			}
		case "100":
			// A short page: the terminal page carries the billing cluster
			// plus one filler, and the walk stops here (short page ends it).
			rows = append(rows, row("cluster-billing"), row("cluster-p2-0"))
		default:
			rows = nil
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"clusters":[%s],"totalCount":%d}`, strings.Join(rows, ","), len(rows))
	})
	defer server.Close()

	name := "cluster-billing"
	readResp := readWith(t, server.URL, nil, &name)
	if readResp.diagnostics.HasError() {
		t.Fatalf("read: %v", readResp.diagnostics.Errors())
	}
	if readResp.model.ID.ValueString() != "cluster-billing" {
		t.Errorf("resolution is %q", readResp.model.ID.ValueString())
	}
	if got := requests.Load(); got != 2 {
		t.Errorf("expected exactly 2 page requests, got %d", got)
	}
}

// --- surfaces ---

// A NESTED 404 (the api-gateway's unrouted-path envelope) is not a verdict
// about the cluster; it takes the generic arm.
func TestReadTreatsANested404AsAGenericFailure(t *testing.T) {
	server := newTestServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":{"code":"NOT_FOUND","message":"no route found for path"}}`))
	})
	defer server.Close()

	id := "cluster-1"
	readResp := readWith(t, server.URL, &id, nil)
	if !readResp.diagnostics.HasError() {
		t.Fatal("an ambiguous verdict must error")
	}
	got := readResp.text()
	if strings.Contains(got, "does not exist") {
		t.Errorf("a nested 404 is not a verdict about the cluster; it must not read as gone: %s", got)
	}
	if !strings.Contains(got, "Failed to Read Kubernetes Cluster") {
		t.Errorf("a nested 404 must take the generic arm: %s", got)
	}
}

func TestReadSurfacesABadResponseBody(t *testing.T) {
	server := newTestServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("not json"))
	})
	defer server.Close()

	id := "cluster-1"
	readResp := readWith(t, server.URL, &id, nil)
	if !readResp.diagnostics.HasError() {
		t.Fatal("an unparseable body must surface")
	}
}

// A null optional field renders as null, never "" — the absent
// caCertHash/endpoint/updatedAt set is the vector.
func TestReadMapsAbsentOptionalsToNull(t *testing.T) {
	server := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/tenants/tenant-1/kubernetes-clusters/cluster-1" {
			writeFlatError(w, http.StatusNotFound, "not_found", "not found")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"cluster-1","name":"billing-prod","status":"created","kubernetesVersion":"1.35","controlPlaneTier":"development","haEnabled":false,"region":"arl1","vpcId":"vpc-1","subnetId":"subnet-1","createdAt":"2026-01-01T00:00:00Z"}`))
	})
	defer server.Close()

	id := "cluster-1"
	readResp := readWith(t, server.URL, &id, nil)
	if readResp.diagnostics.HasError() {
		t.Fatalf("read: %v", readResp.diagnostics.Errors())
	}
	m := readResp.model
	for name, check := range map[string]func() bool{
		"pod_cidr":         m.PodCIDR.IsNull,
		"service_cidr":     m.ServiceCIDR.IsNull,
		"endpoint":         m.Endpoint.IsNull,
		"load_balancer_id": m.LoadBalancerID.IsNull,
		"public_ip":        m.PublicIP.IsNull,
		"ca_cert_hash":     m.CACertHash.IsNull,
		"updated_at":       m.UpdatedAt.IsNull,
		"tenant_id":        m.TenantID.IsNull,
	} {
		if !check() {
			t.Errorf("%s must render as null when the wire omits it", name)
		}
	}
}
