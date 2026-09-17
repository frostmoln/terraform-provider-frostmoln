package bucket

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
	if resp.TypeName != "frostmoln_bucket" {
		t.Errorf("expected frostmoln_bucket, got %s", resp.TypeName)
	}
}

func bucketTestSchema(t *testing.T) schema.Schema {
	t.Helper()
	ds := NewDataSource()
	var resp datasource.SchemaResponse
	ds.Schema(context.Background(), datasource.SchemaRequest{}, &resp)
	return resp.Schema
}

func TestSchemaAttributes(t *testing.T) {
	s := bucketTestSchema(t)
	for _, attr := range []string{
		"name", "id", "region", "acl", "versioning", "default_storage_class",
		"object_count", "total_size", "quota_bytes", "tags", "created_at",
		"updated_at", "tenant_id",
	} {
		if _, ok := s.Attributes[attr]; !ok {
			t.Errorf("attribute %s missing from the schema", attr)
		}
	}
	if name, ok := s.Attributes["name"].(schema.StringAttribute); !ok || !name.Required {
		t.Error("name must be a required string attribute — it IS the id")
	}
	if id, ok := s.Attributes["id"].(schema.StringAttribute); !ok || !id.Computed {
		t.Error("id must be a computed string attribute")
	}
	if oc, ok := s.Attributes["object_count"].(schema.Int64Attribute); !ok || !oc.Computed {
		t.Error("object_count must be a computed int attribute")
	}
	if ts, ok := s.Attributes["total_size"].(schema.Int64Attribute); !ok || !ts.Computed {
		t.Error("total_size must be a computed int attribute")
	}
	if qb, ok := s.Attributes["quota_bytes"].(schema.Int64Attribute); !ok || !qb.Computed {
		t.Error("quota_bytes must be a computed int attribute")
	}
	if tags, ok := s.Attributes["tags"].(schema.MapAttribute); !ok || !tags.Computed {
		t.Error("tags must be a computed map attribute")
	}
	// The plan-time name guard is load-bearing (an unusable name must fail the
	// configuration instead of becoming a request); its presence on the
	// attribute is pinned here so a refactor cannot silently drop it.
	if name, ok := s.Attributes["name"].(schema.StringAttribute); !ok || len(name.Validators) == 0 {
		t.Error("name must carry the plan-time name validator")
	}

	// The CORS rules, lifecycle rules and website configuration are the
	// dedicated resources' collections (one collection, one owner): this data
	// source must never grow a second rendering of them, under any name.
	for _, attr := range []string{"cors_rules", "lifecycle_rules", "website"} {
		if _, ok := s.Attributes[attr]; ok {
			t.Errorf("attribute %s must NOT exist here — it belongs to the dedicated bucket configuration resources", attr)
		}
	}
}

func TestConfigureNilProviderData(t *testing.T) {
	ds := &bucketDataSource{}
	var resp datasource.ConfigureResponse
	ds.Configure(context.Background(), datasource.ConfigureRequest{}, &resp)
	if resp.Diagnostics.HasError() {
		t.Errorf("expected no errors, got %v", resp.Diagnostics.Errors())
	}
}

func TestConfigureWrongType(t *testing.T) {
	ds := &bucketDataSource{}
	var resp datasource.ConfigureResponse
	ds.Configure(context.Background(), datasource.ConfigureRequest{ProviderData: "not-a-client"}, &resp)
	if !resp.Diagnostics.HasError() {
		t.Error("expected error for wrong type")
	}
}

// --- the plan-time name guard ---

func TestNameValidatorRefusesUnusableNames(t *testing.T) {
	for _, name := range []string{"", ".", "..", "a/b", "a/..", `a\b`, "a?b", "a#b", "a%2fb"} {
		if err := validBucketName(name); err == nil {
			t.Errorf("the bucket name %q must be refused", name)
		}
	}
	if err := validBucketName("billing-backups"); err != nil {
		t.Errorf("an ordinary bucket name must be accepted: %v", err)
	}
}

// --- Read plumbing helpers ---

type readResult struct {
	diagnostics diag.Diagnostics
	model       bucketModel
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

// readWith runs one Read against the server with the given name value
// (nil = the attribute set to ""), returning diagnostics and the state model.
func readWith(t *testing.T, serverURL string, name *string) readResult {
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

	s := bucketTestSchema(t)
	objType := s.Type().TerraformType(context.Background()).(tftypes.Object)
	configVals := make(map[string]tftypes.Value, len(objType.AttributeTypes))
	for attr, attrType := range objType.AttributeTypes {
		configVals[attr] = tftypes.NewValue(attrType, nil)
	}
	nameVal := ""
	if name != nil {
		nameVal = *name
	}
	configVals["name"] = tftypes.NewValue(tftypes.String, nameVal)
	config := tftypes.NewValue(objType, configVals)

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

// --- the request-boundary name guard ---

// An unusable name fails at the request boundary without a request. A "/" in
// the name is the exact vector the guard exists for: the client joins with
// path.Join, which CLEANS, and the cleaned URL would address a DIFFERENT
// bucket.
func TestReadRefusesAnUnusableNameWithoutCallingTheAPI(t *testing.T) {
	for _, name := range []string{"", "..", "a/b", `a\b`, "a?b"} {
		var requests atomic.Int32
		server := newTestServer(t, func(w http.ResponseWriter, _ *http.Request) {
			requests.Add(1)
			w.WriteHeader(http.StatusOK)
		})

		readResp := readWith(t, server.URL, &name)
		if !readResp.diagnostics.HasError() {
			t.Fatalf("the bucket name %q must be refused at the request boundary", name)
		}
		if requests.Load() != 0 {
			t.Errorf("no request should have been made for %q, got %d", name, requests.Load())
		}
		server.Close()
	}
}

// --- the read ---

func TestReadByNameIdentifiesTheBucket(t *testing.T) {
	var gotPath atomic.Pointer[string]
	server := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		gotPath.Store(&p)
		if r.URL.Path != "/v1/tenants/tenant-1/buckets/billing-backups" {
			writeFlatError(w, http.StatusNotFound, "not_found", "not found")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"name":"billing-backups","tenantId":"tenant-1","region":"falkenberg",
			"acl":"private","versioning":"enabled","defaultStorageClass":"STANDARD",
			"objectCount":42,"totalSize":1073741824,"quotaBytes":10737418240,
			"tags":{"env":"prod","team":"billing"},
			"createdAt":"2026-01-01T00:00:00Z","updatedAt":"2026-02-01T00:00:00Z"
		}`))
	})
	defer server.Close()

	name := "billing-backups"
	readResp := readWith(t, server.URL, &name)
	if readResp.diagnostics.HasError() {
		t.Fatalf("read: %v", readResp.diagnostics.Errors())
	}
	if p := gotPath.Load(); p == nil || *p != "/v1/tenants/tenant-1/buckets/billing-backups" {
		t.Errorf("the request went to %v", p)
	}
	m := readResp.model
	if m.Name.ValueString() != "billing-backups" || m.ID.ValueString() != "billing-backups" {
		t.Errorf("name/id are %s/%s — a bucket's name IS its id",
			m.Name.ValueString(), m.ID.ValueString())
	}
	if m.Region.ValueString() != "falkenberg" {
		t.Errorf("region is %q", m.Region.ValueString())
	}
	if m.ACL.ValueString() != "private" || m.Versioning.ValueString() != "enabled" {
		t.Errorf("acl/versioning are %s/%s", m.ACL.ValueString(), m.Versioning.ValueString())
	}
	if m.DefaultStorageClass.ValueString() != "STANDARD" {
		t.Errorf("default_storage_class is %q", m.DefaultStorageClass.ValueString())
	}
	if m.ObjectCount.ValueInt64() != 42 || m.TotalSize.ValueInt64() != 1073741824 {
		t.Errorf("counts are %d/%d", m.ObjectCount.ValueInt64(), m.TotalSize.ValueInt64())
	}
	if m.QuotaBytes.ValueInt64() != 10737418240 {
		t.Errorf("quota_bytes is %d", m.QuotaBytes.ValueInt64())
	}
	var wireTags map[string]string
	if diags := m.Tags.ElementsAs(context.Background(), &wireTags, false); diags.HasError() {
		t.Fatalf("tags ElementsAs: %v", diags)
	}
	if wireTags["env"] != "prod" || wireTags["team"] != "billing" {
		t.Errorf("tags are %v", wireTags)
	}
	if m.CreatedAt.ValueString() != "2026-01-01T00:00:00Z" || m.UpdatedAt.ValueString() != "2026-02-01T00:00:00Z" {
		t.Errorf("timestamps are %s/%s", m.CreatedAt.ValueString(), m.UpdatedAt.ValueString())
	}
	if m.TenantID.ValueString() != "tenant-1" {
		t.Errorf("tenant_id is %q", m.TenantID.ValueString())
	}
}

// The measured counts render what the wire carries: an empty bucket answers
// objectCount 0 EXPLICITLY (no omitempty), so 0 renders as 0 — a measured
// answer, never null. The absent optionals (region, quota, tags) render as
// null, never "" or 0.
func TestReadMapsCountedZeroesAndAbsentOptionals(t *testing.T) {
	server := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/tenants/tenant-1/buckets/empty-bucket" {
			writeFlatError(w, http.StatusNotFound, "not_found", "not found")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"name":"empty-bucket","tenantId":"tenant-1","acl":"private","versioning":"disabled","defaultStorageClass":"STANDARD","objectCount":0,"totalSize":0,"createdAt":"2026-01-01T00:00:00Z","updatedAt":"2026-01-01T00:00:00Z"}`))
	})
	defer server.Close()

	name := "empty-bucket"
	readResp := readWith(t, server.URL, &name)
	if readResp.diagnostics.HasError() {
		t.Fatalf("read: %v", readResp.diagnostics.Errors())
	}
	m := readResp.model
	if m.ObjectCount.IsNull() || m.ObjectCount.ValueInt64() != 0 {
		t.Errorf("object_count is %v — a wire 0 must render as 0, never null", m.ObjectCount)
	}
	if m.TotalSize.IsNull() || m.TotalSize.ValueInt64() != 0 {
		t.Errorf("total_size is %v — a wire 0 must render as 0, never null", m.TotalSize)
	}
	if !m.QuotaBytes.IsNull() {
		t.Errorf("quota_bytes is %v — an absent quota (unlimited) must render as null, never 0", m.QuotaBytes)
	}
	if !m.Region.IsNull() {
		t.Errorf("region must render as null when the wire omits it")
	}
	if !m.Tags.IsNull() {
		t.Errorf("tags must render as null when the wire omits them")
	}
}

func TestRead404ErrorsThatTheBucketDoesNotExist(t *testing.T) {
	var requests atomic.Int32
	server := newTestServer(t, func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		writeFlatError(w, http.StatusNotFound, "not_found", "not found")
	})
	defer server.Close()

	name := "vanished"
	readResp := readWith(t, server.URL, &name)
	if !readResp.diagnostics.HasError() {
		t.Fatal("a flat 404 must fail the read")
	}
	if got := readResp.text(); !strings.Contains(got, "does not exist") {
		t.Errorf("the diagnostic must say the bucket is gone: %s", got)
	}
	if requests.Load() != 1 {
		t.Errorf("a flat 404 is one cause and one request; got %d", requests.Load())
	}
}

// A 200 that does not carry the requested name is not the bucket read this
// provider builds its contract on.
func TestReadRefusesA200ThatDoesNotCarryTheName(t *testing.T) {
	server := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/tenants/tenant-1/buckets/billing-backups" {
			writeFlatError(w, http.StatusNotFound, "not_found", "not found")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"name":"not-this-bucket","acl":"private","versioning":"disabled"}`))
	})
	defer server.Close()

	name := "billing-backups"
	readResp := readWith(t, server.URL, &name)
	if !readResp.diagnostics.HasError() {
		t.Fatal("a mismatched 200 must refuse")
	}
	if got := readResp.text(); !strings.Contains(got, "did not identify the requested bucket") {
		t.Errorf("the diagnostic must name the identity guard: %s", got)
	}
}

// --- surfaces ---

// A NESTED 404 (the api-gateway's unrouted-path envelope) is not a verdict
// about the bucket; it takes the generic arm.
func TestReadTreatsANested404AsAGenericFailure(t *testing.T) {
	server := newTestServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":{"code":"NOT_FOUND","message":"no route found for path"}}`))
	})
	defer server.Close()

	name := "billing-backups"
	readResp := readWith(t, server.URL, &name)
	if !readResp.diagnostics.HasError() {
		t.Fatal("an ambiguous verdict must error")
	}
	got := readResp.text()
	if strings.Contains(got, "does not exist") {
		t.Errorf("a nested 404 is not a verdict about the bucket; it must not read as gone: %s", got)
	}
	if !strings.Contains(got, "Failed to Read Bucket") {
		t.Errorf("a nested 404 must take the generic arm: %s", got)
	}
}

func TestReadSurfacesABadResponseBody(t *testing.T) {
	server := newTestServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("not json"))
	})
	defer server.Close()

	name := "billing-backups"
	readResp := readWith(t, server.URL, &name)
	if !readResp.diagnostics.HasError() {
		t.Fatal("an unparseable body must surface")
	}
}
