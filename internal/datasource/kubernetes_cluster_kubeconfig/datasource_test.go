package kubernetes_cluster_kubeconfig

import (
	"context"
	"encoding/json"
	"fmt"
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
	if resp.TypeName != "frostmoln_kubernetes_cluster_kubeconfig" {
		t.Errorf("expected frostmoln_kubernetes_cluster_kubeconfig, got %s", resp.TypeName)
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
	for _, attr := range []string{"id", "name", "endpoint", "kubeconfig"} {
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
	if kc, ok := s.Attributes["kubeconfig"]; !ok || !kc.IsComputed() {
		t.Error("kubeconfig must be computed")
	}
	if ep, ok := s.Attributes["endpoint"]; !ok || !ep.IsComputed() {
		t.Error("endpoint must be computed")
	}
	// The plan-time id guard is load-bearing (an unusable id must fail the
	// configuration instead of becoming a request); its presence on the
	// attribute is pinned here so a refactor cannot silently drop it.
	if id, ok := s.Attributes["id"].(schema.StringAttribute); !ok || len(id.Validators) == 0 {
		t.Error("id must carry the plan-time id validator")
	}
}

// The kubeconfig blob is Vault-served credential material — the FIRST
// Sensitive attribute on any data source in this provider. Its Sensitive flag
// is load-bearing (it keeps the client key out of CLI output), so its presence
// on the attribute is pinned here: a refactor that drops the flag silently
// leaks a private key into every plan.
func TestKubeconfigAttributeIsSensitive(t *testing.T) {
	s := listingSchema(t)
	kc, ok := s.Attributes["kubeconfig"].(schema.StringAttribute)
	if !ok {
		t.Fatal("kubeconfig must be a string attribute")
	}
	if !kc.Computed || !kc.Sensitive {
		t.Error("kubeconfig must be Computed AND Sensitive — it is Vault-served credential material")
	}
	// The endpoint is the non-secret address half: the LB VIP is readable
	// anywhere, and pinning it NOT sensitive keeps the secret boundary exactly
	// at the blob.
	if ep, ok := s.Attributes["endpoint"].(schema.StringAttribute); !ok || !ep.Computed || ep.Sensitive {
		t.Error("endpoint must be Computed and must NOT be Sensitive")
	}
}

func TestConfigureNilProviderData(t *testing.T) {
	ds := &kubernetesClusterKubeconfigDataSource{}
	var resp datasource.ConfigureResponse
	ds.Configure(context.Background(), datasource.ConfigureRequest{}, &resp)
	if resp.Diagnostics.HasError() {
		t.Errorf("expected no errors, got %v", resp.Diagnostics.Errors())
	}
}

func TestConfigureWrongType(t *testing.T) {
	ds := &kubernetesClusterKubeconfigDataSource{}
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
	model       kubernetesClusterKubeconfigModel
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
		"id":         stringOr(id),
		"name":       stringOr(name),
		"endpoint":   tftypes.NewValue(tftypes.String, nil),
		"kubeconfig": tftypes.NewValue(tftypes.String, nil),
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

// --- the happy paths ---

// The kubeconfig must land in state VERBATIM — it is fed raw into provider
// kubeconfig inputs, so any re-encoding here would corrupt a credential.
func TestReadByIDFetchesTheKubeconfigVerbatim(t *testing.T) {
	wantKubeconfig := "apiVersion: v1\nkind: Config\nclusters:\n- name: billing-prod\n  cluster:\n    server: https://api.billing.k8s.frostmoln.cloud:6443\nusers:\n- name: billing-prod\n  user:\n    client-key-data: SU5WH2F0AGX1Cg==\n"

	var clusterGets, kubeconfigGets atomic.Int32
	server := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/tenants/tenant-1/kubernetes-clusters/cluster-1":
			clusterGets.Add(1)
			_, _ = w.Write([]byte(`{"id":"cluster-1","name":"billing-prod","status":"running"}`))
		case "/v1/tenants/tenant-1/kubernetes-clusters/cluster-1/kubeconfig":
			kubeconfigGets.Add(1)
			_, _ = fmt.Fprintf(w, `{"endpoint":"https://api.billing.k8s.frostmoln.cloud:6443","kubeconfig":%q}`, wantKubeconfig)
		default:
			writeFlatError(w, http.StatusNotFound, "not_found", "not found")
		}
	})
	defer server.Close()

	id := "cluster-1"
	readResp := readWith(t, server.URL, &id, nil)
	if readResp.diagnostics.HasError() {
		t.Fatalf("read: %v", readResp.diagnostics.Errors())
	}
	if clusterGets.Load() != 1 || kubeconfigGets.Load() != 1 {
		t.Errorf("expected one cluster read and one kubeconfig read, got %d/%d",
			clusterGets.Load(), kubeconfigGets.Load())
	}
	m := readResp.model
	if m.ID.ValueString() != "cluster-1" {
		t.Errorf("state id must be the resolved cluster id, got %q", m.ID.ValueString())
	}
	if m.Name.ValueString() != "billing-prod" {
		t.Errorf("state name is %q", m.Name.ValueString())
	}
	if m.Endpoint.ValueString() != "https://api.billing.k8s.frostmoln.cloud:6443" {
		t.Errorf("endpoint is %q", m.Endpoint.ValueString())
	}
	if m.Kubeconfig.ValueString() != wantKubeconfig {
		t.Errorf("the kubeconfig must land verbatim, got %q", m.Kubeconfig.ValueString())
	}
}

func TestReadByNameResolvesThenFetchesTheKubeconfig(t *testing.T) {
	var gotQuery atomic.Pointer[string]
	server := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/tenants/tenant-1/kubernetes-clusters":
			q := r.URL.Query().Get("offset")
			gotQuery.Store(&q)
			_, _ = w.Write([]byte(`{"clusters":[{"id":"cluster-1","name":"billing-prod","status":"running"}],"totalCount":1}`))
		case "/v1/tenants/tenant-1/kubernetes-clusters/cluster-1/kubeconfig":
			_, _ = w.Write([]byte(`{"endpoint":"https://api.billing.k8s.frostmoln.cloud:6443","kubeconfig":"apiVersion: v1\nkind: Config\n"}`))
		default:
			writeFlatError(w, http.StatusNotFound, "not_found", "not found")
		}
	})
	defer server.Close()

	name := "billing-prod"
	readResp := readWith(t, server.URL, nil, &name)
	if readResp.diagnostics.HasError() {
		t.Fatalf("read: %v", readResp.diagnostics.Errors())
	}
	if q := gotQuery.Load(); q == nil || *q != "0" {
		t.Errorf("the list request must page explicitly, got offset %v", q)
	}
	m := readResp.model
	if m.ID.ValueString() != "cluster-1" || m.Name.ValueString() != "billing-prod" {
		t.Errorf("resolution is %s/%s", m.ID.ValueString(), m.Name.ValueString())
	}
	if m.Kubeconfig.ValueString() != "apiVersion: v1\nkind: Config\n" {
		t.Errorf("the kubeconfig must land verbatim, got %q", m.Kubeconfig.ValueString())
	}
}

// --- refusals before any credential material moves ---

func TestReadByIDErrorsWhenTheClusterDoesNotExist(t *testing.T) {
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
// forever — treated as absent, and the kubeconfig of a phantom cluster is
// never fetched.
func TestReadByIDTreatsASoftDeletedClusterAsAbsent(t *testing.T) {
	var kubeconfigGets atomic.Int32
	server := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/tenants/tenant-1/kubernetes-clusters/cluster-1":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"cluster-1","name":"gone","status":"deleted"}`))
		default:
			kubeconfigGets.Add(1)
			writeFlatError(w, http.StatusNotFound, "not_found", "not found")
		}
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
	if kubeconfigGets.Load() != 0 {
		t.Errorf("no kubeconfig must be fetched for a deleted cluster, got %d requests", kubeconfigGets.Load())
	}
}

// A 404 on the kubeconfig sub-resource is NOT a verdict that the cluster is
// gone — the cluster was read live moments before — so it takes the generic
// arm and never the "does not exist" arm.
func TestReadByIDSurfacesAKubeconfig404AsAGenericFailure(t *testing.T) {
	server := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/tenants/tenant-1/kubernetes-clusters/cluster-1":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"cluster-1","name":"billing-prod","status":"running"}`))
		default:
			writeFlatError(w, http.StatusNotFound, "not_found", "not found")
		}
	})
	defer server.Close()

	id := "cluster-1"
	readResp := readWith(t, server.URL, &id, nil)
	if !readResp.diagnostics.HasError() {
		t.Fatal("a kubeconfig 404 must fail the read")
	}
	got := readResp.text()
	if strings.Contains(got, "does not exist") {
		t.Errorf("the cluster is live, so the diagnostic must NOT read as gone: %s", got)
	}
	if !strings.Contains(got, "Failed to Read the Cluster Kubeconfig") {
		t.Errorf("the diagnostic must take the generic arm: %s", got)
	}
}

// A NESTED 404 (the api-gateway's unrouted-path envelope) is not a verdict
// about the cluster; it takes the generic arm.
func TestReadTreatsANested404AsAGenericFailure(t *testing.T) {
	server := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/tenants/tenant-1/kubernetes-clusters/cluster-1":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"cluster-1","name":"billing-prod","status":"running"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":{"code":"NOT_FOUND","message":"no route found for path"}}`))
		}
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
	if !strings.Contains(got, "Failed to Read the Cluster Kubeconfig") {
		t.Errorf("a nested 404 must take the generic arm: %s", got)
	}
}

// An EMPTY kubeconfig on a 200 is an error, never rendered silently: empty
// credential material would reach a provider block as if it were a working
// credential, and the empty answer usually means the cluster is not
// serviceable yet.
func TestReadByIDRefusesAnEmptyKubeconfig(t *testing.T) {
	server := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/tenants/tenant-1/kubernetes-clusters/cluster-1":
			_, _ = w.Write([]byte(`{"id":"cluster-1","name":"billing-prod","status":"created"}`))
		default:
			_, _ = w.Write([]byte(`{"endpoint":"https://api.billing.k8s.frostmoln.cloud:6443","kubeconfig":""}`))
		}
	})
	defer server.Close()

	id := "cluster-1"
	readResp := readWith(t, server.URL, &id, nil)
	if !readResp.diagnostics.HasError() {
		t.Fatal("an empty kubeconfig on a 200 must fail the read")
	}
	got := readResp.text()
	if !strings.Contains(got, "came back empty") {
		t.Errorf("the diagnostic must say the kubeconfig is empty: %s", got)
	}
	if !strings.Contains(got, "may not be serviceable") {
		t.Errorf("the diagnostic must point at the serviceability cause: %s", got)
	}
}

// --- the pair contract ---

// The cluster read and the kubeconfig read each carry an endpoint claim, and
// state exports BOTH halves (host + kubeconfig). When the two claims disagree,
// the pair is refused: a config fed a credential for one cluster and the API
// address of another is the relay no resolver may be.
func TestReadRefusesAPairWhoseEndpointClaimsDisagree(t *testing.T) {
	server := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/tenants/tenant-1/kubernetes-clusters/cluster-1":
			_, _ = w.Write([]byte(`{"id":"cluster-1","name":"billing-prod","status":"running","endpoint":"https://api.billing.k8s.frostmoln.cloud:6443"}`))
		case "/v1/tenants/tenant-1/kubernetes-clusters/cluster-1/kubeconfig":
			_, _ = w.Write([]byte(`{"endpoint":"https://api.OTHER.k8s.frostmoln.cloud:6443","kubeconfig":"apiVersion: v1\nkind: Config\n"}`))
		default:
			writeFlatError(w, http.StatusNotFound, "not_found", "not found")
		}
	})
	defer server.Close()

	id := "cluster-1"
	readResp := readWith(t, server.URL, &id, nil)
	if !readResp.diagnostics.HasError() {
		t.Fatal("a pair whose endpoint claims disagree must be refused")
	}
	got := readResp.text()
	if !strings.Contains(got, "does not belong to this cluster") {
		t.Errorf("the diagnostic must name the pair check: %s", got)
	}
	if strings.Contains(got, "apiVersion") {
		t.Errorf("the kubeconfig itself must never ride into a diagnostic: %s", got)
	}
}

// A 403 on the kubeconfig read is the scope gate in the NEGATIVE — permanent,
// not transient — so it keeps the generic arm but adds its own sentence; it
// must never read as "the cluster does not exist".
func TestReadTreatsAKubeconfig403AsAScopeRefusal(t *testing.T) {
	server := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/tenants/tenant-1/kubernetes-clusters/cluster-1":
			_, _ = w.Write([]byte(`{"id":"cluster-1","name":"billing-prod","status":"running"}`))
		default:
			w.WriteHeader(http.StatusForbidden)
			_ = json.NewEncoder(w).Encode(map[string]string{"code": "forbidden", "message": "scope check failed"})
		}
	})
	defer server.Close()

	id := "cluster-1"
	readResp := readWith(t, server.URL, &id, nil)
	if !readResp.diagnostics.HasError() {
		t.Fatal("a 403 must fail the read")
	}
	got := readResp.text()
	if strings.Contains(got, "does not exist") {
		t.Errorf("a 403 is not a verdict about the cluster: %s", got)
	}
	if !strings.Contains(got, "AUTHORIZE") || !strings.Contains(got, "kubernetes:read") {
		t.Errorf("the diagnostic must name the scope gate: %s", got)
	}
}

// The search window: when the tenant's list passes the page cap with a FULL
// final page, zero matches must read as "not in the window we searched" —
// never as plain absence, and never as a false credential verdict.
func TestReadByNameReportsTheSearchWindowInsteadOfAbsence(t *testing.T) {
	requests := 0
	server := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		requests++
		// Always a full page of non-matching live clusters: the walk ends on
		// the cap, with the server plainly holding more.
		rows := []string{}
		for i := 0; i < 100; i++ {
			rows = append(rows, `{"id":"cluster-p","name":"other-i","status":"running"}`)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"clusters":[%s],"totalCount":%d}`, strings.Join(rows, ","), requests*100)
	})
	defer server.Close()

	name := "billing-prod"
	readResp := readWith(t, server.URL, nil, &name)
	if !readResp.diagnostics.HasError() {
		t.Fatal("the walk must end at the cap")
	}
	got := readResp.text()
	if !strings.Contains(got, "searched window") {
		t.Errorf("a capped walk must report the window, not absence: %s", got)
	}
	if strings.Contains(got, "Check the spelling") {
		t.Errorf("the not-found copy must not fire for a capped walk: %s", got)
	}
}
