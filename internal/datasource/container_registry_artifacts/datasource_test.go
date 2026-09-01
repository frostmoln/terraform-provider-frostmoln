package container_registry_artifacts

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
)

func TestNewDataSource(t *testing.T) {
	if NewDataSource() == nil {
		t.Fatal("expected non-nil data source")
	}
}

func TestMetadata(t *testing.T) {
	ds := NewDataSource()
	req := datasource.MetadataRequest{ProviderTypeName: "frostmoln"}
	var resp datasource.MetadataResponse
	ds.Metadata(context.Background(), req, &resp)
	if resp.TypeName != "frostmoln_container_registry_artifacts" {
		t.Errorf("expected type name frostmoln_container_registry_artifacts, got %s", resp.TypeName)
	}
}

func TestSchema(t *testing.T) {
	ds := NewDataSource()
	var resp datasource.SchemaResponse
	ds.Schema(context.Background(), datasource.SchemaRequest{}, &resp)

	repo, ok := resp.Schema.Attributes["repository"]
	if !ok {
		t.Fatal("expected repository attribute in schema")
	}
	if !repo.IsRequired() {
		t.Error("repository must be required: there is no sane default repository to list")
	}
	if _, ok := resp.Schema.Attributes["artifacts"]; !ok {
		t.Error("expected artifacts attribute in schema")
	}
}

// TestSchemaCarriesTheHonestyCopy pins the three statements this data source
// exists to make. They are not decoration: each one is a wrong inference a
// practitioner would otherwise draw from the numbers, and the schema description
// is the only place the Terraform Registry shows them.
func TestSchemaCarriesTheHonestyCopy(t *testing.T) {
	ds := NewDataSource()
	var resp datasource.SchemaResponse
	ds.Schema(context.Background(), datasource.SchemaRequest{}, &resp)

	attrs := artifactNestedAttributes(t)

	sizeDesc := attrs["size_bytes"]
	if !strings.Contains(sizeDesc, "DO NOT SUM") {
		t.Error("size_bytes must tell the reader not to sum it")
	}
	if !strings.Contains(sizeDesc, "storage_used_bytes") {
		t.Error("size_bytes must point at storage_used_bytes as the correct quota figure")
	}

	tagsDesc := attrs["tags"]
	if !strings.Contains(tagsDesc, "EMPTY LIST") || !strings.Contains(tagsDesc, "ORPHANED") {
		t.Error("tags must explain that an empty list is an untagged, orphaned manifest")
	}

	pulledDesc := attrs["pulled_at"]
	if !strings.Contains(pulledDesc, "NULL MEANS NEVER PULLED") {
		t.Error("pulled_at must say null means never pulled")
	}

	if !strings.Contains(resp.Schema.Description, "OBSERVATIONAL") {
		t.Error("the data source description must say the values are observational")
	}
	if !strings.Contains(resp.Schema.Description, "not an empty list") {
		t.Error("the data source description must say a disabled registry is an error, not an empty list")
	}
}

// artifactNestedAttributes returns the description of every nested attribute of
// the artifacts list, keyed by attribute name.
func artifactNestedAttributes(t *testing.T) map[string]string {
	t.Helper()
	ds := NewDataSource()
	var resp datasource.SchemaResponse
	ds.Schema(context.Background(), datasource.SchemaRequest{}, &resp)

	attr, ok := resp.Schema.Attributes["artifacts"].(schema.ListNestedAttribute)
	if !ok {
		t.Fatalf("artifacts is not a ListNestedAttribute, got %T", resp.Schema.Attributes["artifacts"])
	}
	out := map[string]string{}
	for name, a := range attr.NestedObject.Attributes {
		out[name] = a.GetDescription()
	}
	return out
}

func TestConfigureNilProviderData(t *testing.T) {
	ds := &artifactsDataSource{}
	var resp datasource.ConfigureResponse
	ds.Configure(context.Background(), datasource.ConfigureRequest{}, &resp)
	if resp.Diagnostics.HasError() {
		t.Errorf("expected no errors, got %v", resp.Diagnostics.Errors())
	}
}

func TestConfigureWrongType(t *testing.T) {
	ds := &artifactsDataSource{}
	var resp datasource.ConfigureResponse
	ds.Configure(context.Background(), datasource.ConfigureRequest{ProviderData: "not-a-client"}, &resp)
	if !resp.Diagnostics.HasError() {
		t.Error("expected error for wrong type")
	}
}

func TestArtifactItemAttrTypes(t *testing.T) {
	for _, key := range []string{"digest", "tags", "size_bytes", "pushed_at", "pulled_at"} {
		if _, ok := artifactItemAttrTypes[key]; !ok {
			t.Errorf("expected key %q in artifactItemAttrTypes", key)
		}
	}
	if len(artifactItemAttrTypes) != 5 {
		t.Errorf("expected 5 attr types, got %d", len(artifactItemAttrTypes))
	}
}

// TestApiArtifactDecodesCamelCaseKeys locks the wire contract, and with it the
// pointer on PulledAt: an omitted field must stay distinguishable from a present
// one, which a plain string cannot do.
func TestApiArtifactDecodesCamelCaseKeys(t *testing.T) {
	const pulled = `{"digest":"sha256:aa","tags":["v1"],"sizeBytes":1234,"pushedAt":"2026-01-01T00:00:00Z","pulledAt":"2026-01-02T00:00:00Z"}`
	var a apiArtifact
	if err := json.Unmarshal([]byte(pulled), &a); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if a.SizeBytes != 1234 {
		t.Errorf("expected SizeBytes 1234 from \"sizeBytes\", got %d", a.SizeBytes)
	}
	if a.PulledAt == nil || *a.PulledAt != "2026-01-02T00:00:00Z" {
		t.Errorf("expected a decoded pulledAt, got %v", a.PulledAt)
	}

	const never = `{"digest":"sha256:bb","tags":[],"sizeBytes":1,"pushedAt":"2026-01-01T00:00:00Z"}`
	var b apiArtifact
	if err := json.Unmarshal([]byte(never), &b); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if b.PulledAt != nil {
		t.Errorf("an omitted pulledAt must decode as nil, got %q", *b.PulledAt)
	}
}

// TestPulledAtRendersNeverPulledAsNull is the unit-level half of the promise the
// schema makes: null means never pulled. An empty string is treated the same,
// because a timestamp-shaped value that is not a timestamp reads as data — and
// so are the two ZERO times, which are worse: "1970-01-01" reads as a real pull
// decades ago rather than as missing data.
func TestPulledAtRendersNeverPulledAsNull(t *testing.T) {
	ts := "2026-03-04T05:06:07Z"
	empty := ""
	blank := "   "
	goZero := "0001-01-01T00:00:00Z"
	unixZero := "1970-01-01T00:00:00Z"
	for name, tc := range map[string]struct {
		in       *string
		wantNull bool
		want     string
	}{
		"omitted":        {in: nil, wantNull: true},
		"empty string":   {in: &empty, wantNull: true},
		"whitespace":     {in: &blank, wantNull: true},
		"go zero time":   {in: &goZero, wantNull: true},
		"unix zero time": {in: &unixZero, wantNull: true},
		"a timestamp":    {in: &ts, want: ts},
	} {
		t.Run(name, func(t *testing.T) {
			got := pulledAt(tc.in)
			if got.IsNull() != tc.wantNull {
				t.Errorf("null = %v, want %v", got.IsNull(), tc.wantNull)
			}
			if !tc.wantNull && got.ValueString() != tc.want {
				t.Errorf("value = %q, want %q", got.ValueString(), tc.want)
			}
		})
	}
}

// --- test server ---

// artifactsHandler serves the paginated artifacts route.
type artifactsHandler struct {
	// all is the full inventory the fake registry holds; it is sliced into
	// pages of servedPageSize.
	all []apiArtifact
	// servedPageSize is what the server CLAMPS to and echoes back, which may be
	// smaller than what the data source asks for. Zero means "use the requested
	// size".
	servedPageSize int
	// echoPage overrides the page number in the response, to simulate a server
	// that ignores the cursor.
	echoPage int
	// omitPageSize drops pageSize from the response entirely.
	omitPageSize bool
	// scripted, when set, replaces the slicing of `all`: page N answers
	// scripted[N-1] verbatim. It is the only way to serve pages that OVERLAP,
	// which is what a repository changing mid-walk produces and what no
	// consistent slice of a fixed inventory can imitate.
	scripted [][]apiArtifact

	// status/code/reason render a refusal instead of a page.
	status int
	code   string
	reason string

	// Recorded for assertions: what the data source actually requested.
	paths      []string
	rawQueries []string
	repoParams []string
	pages      []string
}

func newTestServer(t *testing.T, h *artifactsHandler) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/me", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(client.UserProfile{ID: "user-1", TenantID: "tenant-1"})
	})
	mux.HandleFunc("/v1/tenants/tenant-1/registry/artifacts", func(w http.ResponseWriter, r *http.Request) {
		h.paths = append(h.paths, r.URL.Path)
		h.rawQueries = append(h.rawQueries, r.URL.RawQuery)
		h.repoParams = append(h.repoParams, r.URL.Query().Get("repository"))
		h.pages = append(h.pages, r.URL.Query().Get("page"))

		w.Header().Set("Content-Type", "application/json")
		if h.status != 0 {
			w.WriteHeader(h.status)
			// The FLAT refusal envelope a service renders — the shape both
			// client.IsNotFound and the reason discriminator read.
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code":    h.code,
				"message": "refused",
				"details": map[string]any{"reason": h.reason},
			})
			return
		}

		requested, _ := strconv.Atoi(r.URL.Query().Get("pageSize"))
		size := h.servedPageSize
		if size == 0 {
			size = requested
		}
		page, _ := strconv.Atoi(r.URL.Query().Get("page"))
		if page < 1 {
			page = 1
		}

		data := []apiArtifact{}
		if h.scripted != nil {
			if page-1 < len(h.scripted) {
				data = h.scripted[page-1]
			}
		} else {
			start := (page - 1) * size
			end := start + size
			if start > len(h.all) {
				start = len(h.all)
			}
			if end > len(h.all) {
				end = len(h.all)
			}
			data = h.all[start:end]
		}

		body := map[string]any{
			"data": data,
			"page": page,
		}
		if h.echoPage != 0 {
			body["page"] = h.echoPage
		}
		if !h.omitPageSize {
			body["pageSize"] = size
		}
		_ = json.NewEncoder(w).Encode(body)
	})
	return httptest.NewServer(mux)
}

func newConfiguredClient(t *testing.T, server *httptest.Server) *client.Client {
	t.Helper()
	c := client.NewClient(server.URL, "test-key") // pragma: allowlist secret
	if err := c.Configure(context.Background()); err != nil {
		t.Fatalf("Configure failed: %v", err)
	}
	return c
}

// readDataSource drives a full tfsdk Read for one repository.
func readDataSource(t *testing.T, c *client.Client, repository string) (datasource.ReadResponse, artifactsModel) {
	t.Helper()
	ctx := context.Background()

	ds := NewDataSource()
	dc, ok := ds.(datasource.DataSourceWithConfigure)
	if !ok {
		t.Fatal("datasource does not implement DataSourceWithConfigure")
	}
	var configResp datasource.ConfigureResponse
	dc.Configure(ctx, datasource.ConfigureRequest{ProviderData: c}, &configResp)
	if configResp.Diagnostics.HasError() {
		t.Fatalf("configure failed: %v", configResp.Diagnostics.Errors())
	}

	var schemaResp datasource.SchemaResponse
	ds.Schema(ctx, datasource.SchemaRequest{}, &schemaResp)

	tfType := schemaResp.Schema.Type().TerraformType(ctx)
	configVal := tftypes.NewValue(tfType, map[string]tftypes.Value{
		"repository": tftypes.NewValue(tftypes.String, repository),
		"artifacts":  tftypes.NewValue(schemaResp.Schema.Attributes["artifacts"].GetType().TerraformType(ctx), nil),
	})

	readReq := datasource.ReadRequest{Config: tfsdk.Config{Schema: schemaResp.Schema, Raw: configVal}}
	var readResp datasource.ReadResponse
	readResp.State = tfsdk.State{Schema: schemaResp.Schema}
	ds.Read(ctx, readReq, &readResp)

	var state artifactsModel
	if !readResp.Diagnostics.HasError() {
		readResp.State.Get(ctx, &state)
	}
	return readResp, state
}

// TestTFSDK_ReadSendsTheRepositoryInTheQueryString is the pin the API contract
// asks for: a repository name legally contains "/", so splicing it into the path
// would restructure the URL. It must arrive as a query parameter, on the
// tenant-scoped two-segment route.
func TestTFSDK_ReadSendsTheRepositoryInTheQueryString(t *testing.T) {
	h := &artifactsHandler{all: []apiArtifact{{Digest: "sha256:aa", PushedAt: "2026-01-01T00:00:00Z"}}}
	server := newTestServer(t, h)
	defer server.Close()

	const repo = "team/tools/linter"
	readResp, _ := readDataSource(t, newConfiguredClient(t, server), repo)
	if readResp.Diagnostics.HasError() {
		t.Fatalf("Read failed: %v", readResp.Diagnostics.Errors())
	}

	if len(h.paths) != 1 {
		t.Fatalf("expected 1 request, got %d", len(h.paths))
	}
	if h.paths[0] != "/v1/tenants/tenant-1/registry/artifacts" {
		t.Errorf("the repository leaked into the path: %q", h.paths[0])
	}
	if h.repoParams[0] != repo {
		t.Errorf("repository query parameter = %q, want %q", h.repoParams[0], repo)
	}
	// The slashes must be escaped in the query, not left to restructure the URL.
	if !strings.Contains(h.rawQueries[0], "repository=team%2Ftools%2Flinter") {
		t.Errorf("expected an escaped repository in the query, got %q", h.rawQueries[0])
	}
}

// TestTFSDK_ReadAssemblesEveryPage is the reason listAll exists. Terraform has
// no pagination concept, so a data source that returned only the first page
// would have a configuration planning against a registry missing most of its
// images — silently, because a short list looks exactly like a small registry.
func TestTFSDK_ReadAssemblesEveryPage(t *testing.T) {
	// 250 artifacts over a server that clamps to 100: three pages, the last
	// short.
	all := make([]apiArtifact, 250)
	for i := range all {
		all[i] = apiArtifact{
			Digest:    fmt.Sprintf("sha256:%03d", i),
			Tags:      []string{fmt.Sprintf("v%d", i)},
			SizeBytes: int64(i),
			PushedAt:  "2026-01-01T00:00:00Z",
		}
	}
	h := &artifactsHandler{all: all, servedPageSize: 100}
	server := newTestServer(t, h)
	defer server.Close()

	readResp, state := readDataSource(t, newConfiguredClient(t, server), "api-server")
	if readResp.Diagnostics.HasError() {
		t.Fatalf("Read failed: %v", readResp.Diagnostics.Errors())
	}

	ctx := context.Background()
	var items []artifactItemModel
	state.Artifacts.ElementsAs(ctx, &items, false)
	if len(items) != 250 {
		t.Fatalf("expected all 250 artifacts, got %d", len(items))
	}
	if items[0].Digest.ValueString() != "sha256:000" || items[249].Digest.ValueString() != "sha256:249" {
		t.Errorf("pages were assembled out of order: first=%q last=%q",
			items[0].Digest.ValueString(), items[249].Digest.ValueString())
	}
	if want := []string{"1", "2", "3"}; len(h.pages) != 3 ||
		h.pages[0] != want[0] || h.pages[1] != want[1] || h.pages[2] != want[2] {
		t.Errorf("expected pages 1,2,3 to be requested, got %v", h.pages)
	}
}

// TestTFSDK_ReadTerminatesOnTheServersPageSize pins WHICH page size ends the
// loop. The server clamps to its own maximum and echoes it; measuring a full
// page against the larger REQUESTED size would make it look short and stop the
// listing one page in.
func TestTFSDK_ReadTerminatesOnTheServersPageSize(t *testing.T) {
	all := make([]apiArtifact, 25)
	for i := range all {
		all[i] = apiArtifact{Digest: fmt.Sprintf("sha256:%02d", i), PushedAt: "2026-01-01T00:00:00Z"}
	}
	// The server clamps to 10 although 100 is requested. A loop keyed on the
	// requested size would see 10 < 100 on the first page and stop with 10 of 25.
	h := &artifactsHandler{all: all, servedPageSize: 10}
	server := newTestServer(t, h)
	defer server.Close()

	readResp, state := readDataSource(t, newConfiguredClient(t, server), "api-server")
	if readResp.Diagnostics.HasError() {
		t.Fatalf("Read failed: %v", readResp.Diagnostics.Errors())
	}
	if n := len(state.Artifacts.Elements()); n != 25 {
		t.Fatalf("expected 25 artifacts, got %d (the loop keyed on the requested page size)", n)
	}
}

// TestTFSDK_ReadRefusesANonAdvancingListing guards the loop against a server
// that ignores `page`. Without the check it would append page 1 a thousand times
// and hand Terraform a list of duplicates.
func TestTFSDK_ReadRefusesANonAdvancingListing(t *testing.T) {
	all := make([]apiArtifact, 10)
	for i := range all {
		all[i] = apiArtifact{Digest: fmt.Sprintf("sha256:%02d", i), PushedAt: "2026-01-01T00:00:00Z"}
	}
	// Always answers "page 1" and always a full page.
	h := &artifactsHandler{all: all, servedPageSize: 5, echoPage: 1}
	server := newTestServer(t, h)
	defer server.Close()

	readResp, _ := readDataSource(t, newConfiguredClient(t, server), "api-server")
	if !readResp.Diagnostics.HasError() {
		t.Fatal("expected an error when the listing does not advance")
	}
	if len(h.pages) > 3 {
		t.Errorf("the loop kept going after the page stopped advancing: %d requests", len(h.pages))
	}
	found := false
	for _, d := range readResp.Diagnostics.Errors() {
		if strings.Contains(d.Detail(), "did not advance") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected a diagnostic explaining the non-advancing listing, got %v", readResp.Diagnostics.Errors())
	}
}

// TestTFSDK_ReadRefusesAnUnreportedPageSize: without the applied page size there
// is no way to tell a final short page from a full one, and guessing would
// silently truncate.
func TestTFSDK_ReadRefusesAnUnreportedPageSize(t *testing.T) {
	all := make([]apiArtifact, 10)
	for i := range all {
		all[i] = apiArtifact{Digest: fmt.Sprintf("sha256:%02d", i), PushedAt: "2026-01-01T00:00:00Z"}
	}
	h := &artifactsHandler{all: all, servedPageSize: 5, omitPageSize: true}
	server := newTestServer(t, h)
	defer server.Close()

	readResp, _ := readDataSource(t, newConfiguredClient(t, server), "api-server")
	if !readResp.Diagnostics.HasError() {
		t.Fatal("expected an error when the API does not report the page size it applied")
	}
}

// TestTFSDK_ReadEmptyRepositoryStopsAtOnce: an empty page ends the listing
// whatever the server says about sizes, and renders as an EMPTY list rather than
// a null one.
func TestTFSDK_ReadEmptyRepositoryStopsAtOnce(t *testing.T) {
	h := &artifactsHandler{all: []apiArtifact{}, omitPageSize: true}
	server := newTestServer(t, h)
	defer server.Close()

	readResp, state := readDataSource(t, newConfiguredClient(t, server), "api-server")
	if readResp.Diagnostics.HasError() {
		t.Fatalf("Read failed: %v", readResp.Diagnostics.Errors())
	}
	if state.Artifacts.IsNull() {
		t.Fatal("an empty repository must render an empty list, not a null one")
	}
	if n := len(state.Artifacts.Elements()); n != 0 {
		t.Errorf("expected 0 artifacts, got %d", n)
	}
	if len(h.pages) != 1 {
		t.Errorf("expected a single request for an empty repository, got %d", len(h.pages))
	}
}

// TestTFSDK_ReadRendersNeverPulledAsNullAndUntaggedAsEmpty is the end-to-end
// version of the two promises the schema makes: pulled_at is NULL (never a zero
// timestamp) for an artifact nothing has fetched, and an orphaned manifest has
// an EMPTY tag list (never a null one) so `length(a.tags) == 0` finds it.
func TestTFSDK_ReadRendersNeverPulledAsNullAndUntaggedAsEmpty(t *testing.T) {
	pulled := "2026-02-02T00:00:00Z"
	h := &artifactsHandler{all: []apiArtifact{
		{Digest: "sha256:tagged", Tags: []string{"v1", "latest"}, SizeBytes: 100, PushedAt: "2026-01-01T00:00:00Z", PulledAt: &pulled},
		// The tag it used to carry was moved by a later push: untagged, still
		// occupying storage, and never pulled since.
		{Digest: "sha256:orphan", Tags: nil, SizeBytes: 90, PushedAt: "2026-01-02T00:00:00Z"},
	}}
	server := newTestServer(t, h)
	defer server.Close()

	readResp, state := readDataSource(t, newConfiguredClient(t, server), "api-server")
	if readResp.Diagnostics.HasError() {
		t.Fatalf("Read failed: %v", readResp.Diagnostics.Errors())
	}

	ctx := context.Background()
	var items []artifactItemModel
	state.Artifacts.ElementsAs(ctx, &items, false)
	if len(items) != 2 {
		t.Fatalf("expected 2 artifacts, got %d", len(items))
	}

	if items[0].PulledAt.IsNull() || items[0].PulledAt.ValueString() != pulled {
		t.Errorf("a pulled artifact must carry its timestamp, got %v", items[0].PulledAt)
	}
	var tags []types.String
	items[0].Tags.ElementsAs(ctx, &tags, false)
	if len(tags) != 2 {
		t.Errorf("expected 2 tags, got %d", len(tags))
	}

	orphan := items[1]
	if !orphan.PulledAt.IsNull() {
		t.Errorf("a never-pulled artifact must render pulled_at as NULL, got %q", orphan.PulledAt.ValueString())
	}
	if orphan.Tags.IsNull() {
		t.Fatal("an untagged artifact must render an EMPTY tag list, not a null one")
	}
	if n := len(orphan.Tags.Elements()); n != 0 {
		t.Errorf("expected 0 tags on the orphan, got %d", n)
	}
	if orphan.SizeBytes.ValueInt64() != 90 {
		t.Errorf("an orphaned manifest still reports its size, got %d", orphan.SizeBytes.ValueInt64())
	}
}

// TestTFSDK_ReadDeduplicatesArtifactsRepeatedAcrossPages is the reason the walk
// keeps a digest set.
//
// The API does not sort and promises no order, so the page boundary moves when
// the repository changes mid-walk and an artifact already returned can be served
// again on the next page. In a CLI that prints one twice it is untidy; here the
// duplicate lands in STATE, and every subsequent plan — every `count`, every
// `for_each`, every prune loop — is computed against a repository that appears
// to hold an image twice.
func TestTFSDK_ReadDeduplicatesArtifactsRepeatedAcrossPages(t *testing.T) {
	// "sha256:bb" was on page 1 and, after a push landed between the two calls,
	// is served again on page 2. Both pages are FULL (2 of 2), so the walk goes
	// on to the short page 3.
	h := &artifactsHandler{
		servedPageSize: 2,
		scripted: [][]apiArtifact{
			{{Digest: "sha256:aa", PushedAt: "2026-01-01T00:00:00Z"}, {Digest: "sha256:bb", PushedAt: "2026-01-01T00:00:00Z"}},
			{{Digest: "sha256:bb", PushedAt: "2026-01-01T00:00:00Z"}, {Digest: "sha256:cc", PushedAt: "2026-01-01T00:00:00Z"}},
			{{Digest: "sha256:dd", PushedAt: "2026-01-01T00:00:00Z"}},
		},
	}
	server := newTestServer(t, h)
	defer server.Close()

	readResp, state := readDataSource(t, newConfiguredClient(t, server), "api-server")
	if readResp.Diagnostics.HasError() {
		t.Fatalf("Read failed: %v", readResp.Diagnostics.Errors())
	}

	ctx := context.Background()
	var items []artifactItemModel
	state.Artifacts.ElementsAs(ctx, &items, false)

	var digests []string
	for _, a := range items {
		digests = append(digests, a.Digest.ValueString())
	}
	if got, want := strings.Join(digests, ","), "sha256:aa,sha256:bb,sha256:cc,sha256:dd"; got != want {
		t.Errorf("digests = %q, want %q — a repeated artifact reached Terraform state", got, want)
	}
	// And the page that lost a row to the dedupe was still measured as FULL: page
	// 2 sent 2 rows of a 2-row page, so the walk had to continue. Measuring the
	// count KEPT against pageSize would have ended it a page early, truncating
	// the listing — the failure this data source exists to avoid.
	if want := []string{"1", "2", "3"}; len(h.pages) != len(want) {
		t.Errorf("pages requested = %v, want %v", h.pages, want)
	}
}

// TestTFSDK_ReadRendersAZeroTimestampAsNeverPulled: the schema promises
// `pulled_at == null` is a sound test for an image nothing has ever fetched, and
// that a zero or epoch timestamp is never reported. The registry cannot emit one
// today, so this is defence in depth — and parity with the portal and the `fm`
// CLI, both of which reject the same two forms. "1970-01-01" reads as a real
// pull decades ago: a configuration pruning by age would act on it.
func TestTFSDK_ReadRendersAZeroTimestampAsNeverPulled(t *testing.T) {
	goZero, unixZero := "0001-01-01T00:00:00Z", "1970-01-01T00:00:00Z"
	h := &artifactsHandler{all: []apiArtifact{
		{Digest: "sha256:go", PushedAt: "2026-01-01T00:00:00Z", PulledAt: &goZero},
		{Digest: "sha256:unix", PushedAt: "2026-01-01T00:00:00Z", PulledAt: &unixZero},
	}}
	server := newTestServer(t, h)
	defer server.Close()

	readResp, state := readDataSource(t, newConfiguredClient(t, server), "api-server")
	if readResp.Diagnostics.HasError() {
		t.Fatalf("Read failed: %v", readResp.Diagnostics.Errors())
	}

	ctx := context.Background()
	var items []artifactItemModel
	state.Artifacts.ElementsAs(ctx, &items, false)
	if len(items) != 2 {
		t.Fatalf("expected 2 artifacts, got %d", len(items))
	}
	for _, a := range items {
		if !a.PulledAt.IsNull() {
			t.Errorf("%s: a zero timestamp reached state as a pull date: %q",
				a.Digest.ValueString(), a.PulledAt.ValueString())
		}
	}
}

// TestTFSDK_ReadNotEnabledIsAnErrorNamingTheRemedy: a tenant that has not opted
// in gets an ERROR, not an empty list — otherwise "no registry" and "no images"
// are indistinguishable.
func TestTFSDK_ReadNotEnabledIsAnErrorNamingTheRemedy(t *testing.T) {
	server := newTestServer(t, &artifactsHandler{
		status: http.StatusConflict,
		code:   "conflict",
		reason: reasonNotEnabled,
	})
	defer server.Close()

	readResp, _ := readDataSource(t, newConfiguredClient(t, server), "api-server")
	if !readResp.Diagnostics.HasError() {
		t.Fatal("expected an error for a tenant whose registry is not enabled")
	}
	var summary, detail string
	for _, d := range readResp.Diagnostics.Errors() {
		summary, detail = d.Summary(), d.Detail()
	}
	if !strings.Contains(summary, "not enabled") {
		t.Errorf("the summary must say the registry is not enabled, got %q", summary)
	}
	if !strings.Contains(detail, "frostmoln_container_registry") {
		t.Errorf("the detail must name the remedy resource, got %q", detail)
	}
}

// TestTFSDK_ReadUnknownRepositoryNamesTheLikelyMistake: the most common way to
// get a 404 here is passing a full image reference instead of the namespace-
// relative name, so the diagnostic says so.
func TestTFSDK_ReadUnknownRepositoryNamesTheLikelyMistake(t *testing.T) {
	server := newTestServer(t, &artifactsHandler{
		status: http.StatusNotFound,
		code:   "NOT_FOUND",
	})
	defer server.Close()

	readResp, _ := readDataSource(t, newConfiguredClient(t, server), "registry.example/ns/api-server")
	if !readResp.Diagnostics.HasError() {
		t.Fatal("expected an error for a repository that does not exist")
	}
	var summary, detail string
	for _, d := range readResp.Diagnostics.Errors() {
		summary, detail = d.Summary(), d.Detail()
	}
	if !strings.Contains(summary, "No such container registry repository") {
		t.Errorf("unexpected summary %q", summary)
	}
	if !strings.Contains(detail, "RELATIVE") {
		t.Errorf("the detail must explain that repository is namespace-relative, got %q", detail)
	}
}

func TestIsNotEnabled(t *testing.T) {
	for name, tc := range map[string]struct {
		err  error
		want bool
	}{
		"the opt-in refusal": {
			err:  &client.APIError{StatusCode: http.StatusConflict, Code: "conflict", Details: map[string]any{"reason": reasonNotEnabled}},
			want: true,
		},
		"another reason on the same status": {
			err:  &client.APIError{StatusCode: http.StatusConflict, Code: "conflict", Details: map[string]any{"reason": "REGISTRY_SUSPENDED"}},
			want: false,
		},
		"no details at all": {
			err:  &client.APIError{StatusCode: http.StatusConflict, Code: "conflict"},
			want: false,
		},
		"not an APIError": {err: errNotAPI{}, want: false},
	} {
		t.Run(name, func(t *testing.T) {
			if got := isNotEnabled(tc.err); got != tc.want {
				t.Errorf("isNotEnabled = %v, want %v", got, tc.want)
			}
		})
	}
}

type errNotAPI struct{}

func (errNotAPI) Error() string { return "transport failure" }
