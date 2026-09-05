package container_registry_cache_upstreams

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
)

func newMeAndCacheServer(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/v1/me" {
			_ = json.NewEncoder(w).Encode(map[string]string{"id": "user-123", "tenantId": "tenant-456"})
			return
		}
		handler(w, r)
	}))
}

func upstreamsSchema(t *testing.T) schema.Schema {
	t.Helper()
	resp := datasource.SchemaResponse{}
	(&cacheUpstreamsDataSource{}).Schema(context.Background(), datasource.SchemaRequest{}, &resp)
	return resp.Schema
}

func readUpstreams(t *testing.T, serverURL string) (*datasource.ReadResponse, cacheUpstreamsModel) {
	t.Helper()
	c := client.NewClient(serverURL, "test-key")
	if err := c.Configure(context.Background()); err != nil {
		t.Fatalf("configure failed: %v", err)
	}
	d := &cacheUpstreamsDataSource{client: c}

	s := upstreamsSchema(t)
	cfgType := s.Type().TerraformType(context.Background())
	config := tfsdk.Config{Schema: s, Raw: tftypes.NewValue(cfgType, map[string]tftypes.Value{
		"upstreams":   tftypes.NewValue(s.Attributes["upstreams"].GetType().TerraformType(context.Background()), nil),
		"cache_limit": tftypes.NewValue(tftypes.Number, nil),
	})}
	resp := &datasource.ReadResponse{State: tfsdk.State{Schema: s}}
	d.Read(context.Background(), datasource.ReadRequest{Config: config}, resp)

	var got cacheUpstreamsModel
	if !resp.State.Raw.IsNull() {
		if diags := resp.State.Get(context.Background(), &got); diags.HasError() {
			t.Fatalf("reading back state: %v", diags)
		}
	}
	return resp, got
}

const catalogBody = `{"data":[],"totalCount":0,"limit":5,"upstreams":[
	{"key":"dockerhub","display":"Docker Hub (docker.io)","requiresCredentials":true},
	{"key":"ghcr","display":"GitHub Container Registry (ghcr.io)","requiresCredentials":false}]}`

func TestReadReturnsTheServersCatalogAndCap(t *testing.T) {
	var gotPath string
	server := newMeAndCacheServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_, _ = io.WriteString(w, catalogBody)
	})
	defer server.Close()

	resp, got := readUpstreams(t, server.URL)
	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected error: %v", resp.Diagnostics)
	}
	// TWO-SEGMENT: a bare `/registry` is captured by the api-gateway's unanchored
	// feature-gate substring match and 403s a customer's own "registry" resources.
	if gotPath != "/v1/tenants/tenant-456/registry/caches" {
		t.Errorf("path = %s", gotPath)
	}

	var items []cacheUpstreamItemModel
	if diags := got.Upstreams.ElementsAs(context.Background(), &items, false); diags.HasError() {
		t.Fatalf("decoding upstreams: %v", diags)
	}
	if len(items) != 2 {
		t.Fatalf("got %d upstreams, want 2", len(items))
	}
	if items[0].Key.ValueString() != "dockerhub" || !items[0].RequiresCredentials.ValueBool() {
		t.Errorf("row 0 = %+v, want dockerhub with requires_credentials true", items[0])
	}
	if items[1].Key.ValueString() != "ghcr" || items[1].RequiresCredentials.ValueBool() {
		t.Errorf("row 1 = %+v, want ghcr with requires_credentials false", items[1])
	}
	if items[1].Display.ValueString() != "GitHub Container Registry (ghcr.io)" {
		t.Errorf("display = %q", items[1].Display.ValueString())
	}
	// The cap is served so a configuration need not hardcode a number.
	if got.CacheLimit.ValueInt64() != 5 {
		t.Errorf("cache_limit = %d, want 5", got.CacheLimit.ValueInt64())
	}
}

// An empty catalog is the shape that does damage: a `for_each` over it plans the
// destruction of every cache driven from it — which deletes their cached images
// — and reports it as an ordinary diff. The allow-list is closed and never
// empty, so an empty one is an unreadable answer, not a real one.
func TestReadRefusesAnEmptyCatalog(t *testing.T) {
	server := newMeAndCacheServer(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"data":[],"totalCount":0,"limit":5,"upstreams":[]}`)
	})
	defer server.Close()

	resp, _ := readUpstreams(t, server.URL)
	if !resp.Diagnostics.HasError() {
		t.Fatal("an empty upstream catalog must be refused, not returned as a real answer")
	}
}

// Deliberately an error rather than an empty catalog: an empty list would make
// "this tenant has no registry" and "this platform offers no upstreams"
// indistinguishable.
func TestReadRefusesWhenTheRegistryIsNotEnabled(t *testing.T) {
	server := newMeAndCacheServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusConflict)
		_, _ = io.WriteString(w, `{"code":"invalid_state","message":"x",
			"details":{"reason":"REGISTRY_NOT_ENABLED"}}`)
	})
	defer server.Close()

	resp, _ := readUpstreams(t, server.URL)
	if !resp.Diagnostics.HasError() {
		t.Fatal("expected an error for a tenant with no registry")
	}
	if summary := resp.Diagnostics.Errors()[0].Summary(); summary != "This tenant's container registry is not enabled" {
		t.Errorf("summary = %q — the diagnostic must name the remedy, not the transport failure", summary)
	}
}

// The data source name is a public contract: renaming it breaks every
// configuration that reads the catalog.
func TestMetadataNamesTheDataSource(t *testing.T) {
	var resp datasource.MetadataResponse
	NewDataSource().Metadata(context.Background(),
		datasource.MetadataRequest{ProviderTypeName: "frostmoln"}, &resp)
	if resp.TypeName != "frostmoln_container_registry_cache_upstreams" {
		t.Errorf("TypeName = %q", resp.TypeName)
	}
}

func TestConfigureRefusesTheWrongProviderData(t *testing.T) {
	d := &cacheUpstreamsDataSource{}

	// A nil ProviderData is the provider not being configured YET, which the
	// framework does on purpose — it must be tolerated, not refused.
	var ok datasource.ConfigureResponse
	d.Configure(context.Background(), datasource.ConfigureRequest{}, &ok)
	if ok.Diagnostics.HasError() {
		t.Errorf("a nil ProviderData must be tolerated: %v", ok.Diagnostics)
	}

	var bad datasource.ConfigureResponse
	d.Configure(context.Background(), datasource.ConfigureRequest{ProviderData: "not a client"}, &bad)
	if !bad.Diagnostics.HasError() {
		t.Error("a ProviderData of the wrong type must be refused")
	}
}

// A body that does not decode must be an error, never an empty catalog: an
// empty catalog is the shape that plans a destroy of every cache read from it.
func TestReadRefusesAnUndecodableBody(t *testing.T) {
	server := newMeAndCacheServer(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"upstreams": "not an array"}`)
	})
	defer server.Close()

	resp, _ := readUpstreams(t, server.URL)
	if !resp.Diagnostics.HasError() {
		t.Fatal("an undecodable catalog response must be refused")
	}
}

// The transport failure path: not every error carries a details.reason, and the
// predicate must say "no" rather than panic on one that does not.
func TestReadSurfacesAPlainFailure(t *testing.T) {
	server := newMeAndCacheServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, `{"code":"internal_error","message":"boom"}`)
	})
	defer server.Close()

	resp, _ := readUpstreams(t, server.URL)
	if !resp.Diagnostics.HasError() {
		t.Fatal("expected an error")
	}
	if summary := resp.Diagnostics.Errors()[0].Summary(); summary != "Failed to read the container registry cache upstreams" {
		t.Errorf("summary = %q, want the generic failure rather than the not-enabled remedy", summary)
	}
}
