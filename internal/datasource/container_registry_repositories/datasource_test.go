package container_registry_repositories

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
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
	if resp.TypeName != "frostmoln_container_registry_repositories" {
		t.Errorf("expected type name frostmoln_container_registry_repositories, got %s", resp.TypeName)
	}
}

func TestSchema(t *testing.T) {
	ds := NewDataSource()
	var resp datasource.SchemaResponse
	ds.Schema(context.Background(), datasource.SchemaRequest{}, &resp)

	if _, ok := resp.Schema.Attributes["repositories"]; !ok {
		t.Fatal("expected repositories attribute in schema")
	}
	// The data source takes no arguments: a filter argument would be the kind of
	// thing that silently narrows an inventory listing.
	if len(resp.Schema.Attributes) != 1 {
		t.Errorf("expected exactly one attribute, got %d", len(resp.Schema.Attributes))
	}
}

// TestSchemaSaysTheValuesAreObservational pins the honesty copy that is the
// reason this data source exists in the shape it does. Every number here moves
// on a push or a pull with no Terraform change involved, and a practitioner who
// does not know that will write a configuration that plans against them.
func TestSchemaSaysTheValuesAreObservational(t *testing.T) {
	ds := NewDataSource()
	var resp datasource.SchemaResponse
	ds.Schema(context.Background(), datasource.SchemaRequest{}, &resp)

	if !strings.Contains(resp.Schema.Description, "OBSERVATIONAL") {
		t.Error("the data source description must say the values are observational")
	}
	// And that a tenant without a registry is an error, not an empty list —
	// which is the decision the reader most needs stated.
	if !strings.Contains(resp.Schema.Description, "not an empty list") {
		t.Error("the data source description must say a disabled registry is an error, not an empty list")
	}
}

func TestConfigureNilProviderData(t *testing.T) {
	ds := &repositoriesDataSource{}
	var resp datasource.ConfigureResponse
	ds.Configure(context.Background(), datasource.ConfigureRequest{}, &resp)
	if resp.Diagnostics.HasError() {
		t.Errorf("expected no errors, got %v", resp.Diagnostics.Errors())
	}
}

func TestConfigureWrongType(t *testing.T) {
	ds := &repositoriesDataSource{}
	var resp datasource.ConfigureResponse
	ds.Configure(context.Background(), datasource.ConfigureRequest{ProviderData: "not-a-client"}, &resp)
	if !resp.Diagnostics.HasError() {
		t.Error("expected error for wrong type")
	}
}

func TestRepositoryItemAttrTypes(t *testing.T) {
	for _, key := range []string{"name", "artifact_count", "pull_count", "created_at", "updated_at"} {
		if _, ok := repositoryItemAttrTypes[key]; !ok {
			t.Errorf("expected key %q in repositoryItemAttrTypes", key)
		}
	}
	if len(repositoryItemAttrTypes) != 5 {
		t.Errorf("expected 5 attr types, got %d", len(repositoryItemAttrTypes))
	}
}

// TestApiRepositoryDecodesCamelCaseKeys locks the wire contract: the API speaks
// camelCase and the schema speaks snake_case, so a tag "corrected" to
// artifact_count would silently zero both counters rather than fail.
func TestApiRepositoryDecodesCamelCaseKeys(t *testing.T) {
	const body = `{"name":"team/api-server","artifactCount":7,"pullCount":42,"createdAt":"2026-01-02T03:04:05Z","updatedAt":"2026-02-03T04:05:06Z"}`
	var r apiRepository
	if err := json.Unmarshal([]byte(body), &r); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if r.ArtifactCount != 7 {
		t.Errorf("expected ArtifactCount 7 from \"artifactCount\", got %d", r.ArtifactCount)
	}
	if r.PullCount != 42 {
		t.Errorf("expected PullCount 42 from \"pullCount\", got %d", r.PullCount)
	}
	if r.CreatedAt != "2026-01-02T03:04:05Z" || r.UpdatedAt != "2026-02-03T04:05:06Z" {
		t.Errorf("timestamps decoded wrong: %+v", r)
	}
}

// --- test server ---

// registryHandler answers the repositories route. A nil `repos` with a non-zero
// status renders the refusal envelope for that status instead.
type registryHandler struct {
	repos []apiRepository

	// status/code/reason render a refusal. Deliberately the FLAT envelope a
	// service renders, since that is what the client's discriminators read.
	status int
	code   string
	reason string

	// totalCount overrides the count sent beside the rows, so a test can serve a
	// listing that disagrees with itself. Nil sends the honest count.
	totalCount *int64

	// path records what the data source actually requested, so a test can pin
	// that the route stayed tenant-scoped and two-segment.
	path string
}

func newTestServer(t *testing.T, h *registryHandler) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/me", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(client.UserProfile{ID: "user-1", TenantID: "tenant-1"})
	})
	mux.HandleFunc("/v1/tenants/tenant-1/registry/repositories", func(w http.ResponseWriter, r *http.Request) {
		h.path = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		if h.status != 0 {
			w.WriteHeader(h.status)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code":    h.code,
				"message": "the registry is not enabled for this tenant",
				"details": map[string]any{"reason": h.reason},
			})
			return
		}
		total := int64(len(h.repos))
		if h.totalCount != nil {
			total = *h.totalCount
		}
		_ = json.NewEncoder(w).Encode(apiRepositoryList{Data: h.repos, TotalCount: total})
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

// readDataSource drives a full tfsdk Read against a configured client and
// returns the response, so each test asserts on state rather than on a
// hand-assembled model.
func readDataSource(t *testing.T, c *client.Client) (datasource.ReadResponse, repositoriesModel) {
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
		"repositories": tftypes.NewValue(schemaResp.Schema.Attributes["repositories"].GetType().TerraformType(ctx), nil),
	})

	readReq := datasource.ReadRequest{Config: tfsdk.Config{Schema: schemaResp.Schema, Raw: configVal}}
	var readResp datasource.ReadResponse
	readResp.State = tfsdk.State{Schema: schemaResp.Schema}
	ds.Read(ctx, readReq, &readResp)

	var state repositoriesModel
	if !readResp.Diagnostics.HasError() {
		readResp.State.Get(ctx, &state)
	}
	return readResp, state
}

func TestTFSDK_ReadListsEveryRepository(t *testing.T) {
	h := &registryHandler{repos: []apiRepository{
		{Name: "api-server", ArtifactCount: 3, PullCount: 10, CreatedAt: "2026-01-01T00:00:00Z", UpdatedAt: "2026-01-05T00:00:00Z"},
		// A name with a slash: it is one repository, not three, and it must
		// survive the round trip intact.
		{Name: "team/tools/linter", ArtifactCount: 1, PullCount: 0, CreatedAt: "2026-02-01T00:00:00Z", UpdatedAt: "2026-02-01T00:00:00Z"},
	}}
	server := newTestServer(t, h)
	defer server.Close()

	readResp, state := readDataSource(t, newConfiguredClient(t, server))
	if readResp.Diagnostics.HasError() {
		t.Fatalf("Read failed: %v", readResp.Diagnostics.Errors())
	}

	ctx := context.Background()
	var items []repositoryItemModel
	state.Repositories.ElementsAs(ctx, &items, false)
	if len(items) != 2 {
		t.Fatalf("expected 2 repositories, got %d", len(items))
	}
	if items[1].Name.ValueString() != "team/tools/linter" {
		t.Errorf("a slashed repository name was mangled: %q", items[1].Name.ValueString())
	}
	if items[0].ArtifactCount.ValueInt64() != 3 || items[0].PullCount.ValueInt64() != 10 {
		t.Errorf("counters decoded wrong: %+v", items[0])
	}

	// The tenant-scoped, two-segment route — never a bare /registry, which the
	// gateway's unanchored feature gate would over-match.
	if h.path != "/v1/tenants/tenant-1/registry/repositories" {
		t.Errorf("unexpected request path %q", h.path)
	}
}

// TestTFSDK_ReadEmptyRegistryYieldsAnEmptyListNotNull pins the difference a
// configuration actually branches on: an enabled registry holding nothing is an
// EMPTY list. A null one would read as "unknown" and make `length(...) == 0`
// unusable.
func TestTFSDK_ReadEmptyRegistryYieldsAnEmptyListNotNull(t *testing.T) {
	server := newTestServer(t, &registryHandler{repos: []apiRepository{}})
	defer server.Close()

	readResp, state := readDataSource(t, newConfiguredClient(t, server))
	if readResp.Diagnostics.HasError() {
		t.Fatalf("Read failed: %v", readResp.Diagnostics.Errors())
	}
	if state.Repositories.IsNull() {
		t.Fatal("an enabled but empty registry must render an empty list, not a null one")
	}
	if n := len(state.Repositories.Elements()); n != 0 {
		t.Errorf("expected 0 repositories, got %d", n)
	}
}

// TestTFSDK_ReadRefusesAListingShorterThanItsOwnCount is why totalCount is read
// rather than decoded and ignored.
//
// This route is unpaginated and the server cannot truncate today, but a SHORT
// LIST is the dangerous failure for every reader of this inventory and it is
// invisible: a truncated listing looks exactly like a smaller registry, so a
// configuration written to find what a namespace holds would act on a view
// missing rows and report success. totalCount is the only cross-check on this
// response. fm-cli refuses on the same signal.
func TestTFSDK_ReadRefusesAListingShorterThanItsOwnCount(t *testing.T) {
	seven := int64(7)
	server := newTestServer(t, &registryHandler{
		repos:      []apiRepository{{Name: "api-server"}},
		totalCount: &seven,
	})
	defer server.Close()

	readResp, _ := readDataSource(t, newConfiguredClient(t, server))
	if !readResp.Diagnostics.HasError() {
		t.Fatal("a listing shorter than its own count was accepted")
	}
	var detail string
	for _, d := range readResp.Diagnostics.Errors() {
		detail = d.Detail()
	}
	// Both numbers, or the practitioner cannot tell how much is missing.
	if !strings.Contains(detail, "7") || !strings.Contains(detail, "returned 1") {
		t.Errorf("the diagnostic does not name the mismatch: %q", detail)
	}
}

// The honest case must not be caught by the check above: a listing whose count
// matches its rows — including an empty one — reads normally.
func TestTFSDK_ReadAcceptsAListingThatMatchesItsCount(t *testing.T) {
	for name, repos := range map[string][]apiRepository{
		"empty":     {},
		"populated": {{Name: "api-server"}, {Name: "team/tools/linter"}},
	} {
		t.Run(name, func(t *testing.T) {
			server := newTestServer(t, &registryHandler{repos: repos})
			defer server.Close()

			readResp, state := readDataSource(t, newConfiguredClient(t, server))
			if readResp.Diagnostics.HasError() {
				t.Fatalf("Read failed: %v", readResp.Diagnostics.Errors())
			}
			if n := len(state.Repositories.Elements()); n != len(repos) {
				t.Errorf("got %d repositories, want %d", n, len(repos))
			}
		})
	}
}

// TestTFSDK_ReadNotEnabledIsAnErrorNamingTheRemedy pins the decision documented
// in the schema: a tenant that has not opted in gets an ERROR. Returning an
// empty list would make "no registry" and "no images" indistinguishable, and the
// message has to name the remedy because the server's own copy cannot.
func TestTFSDK_ReadNotEnabledIsAnErrorNamingTheRemedy(t *testing.T) {
	server := newTestServer(t, &registryHandler{
		status: http.StatusConflict,
		code:   "conflict",
		reason: reasonNotEnabled,
	})
	defer server.Close()

	readResp, _ := readDataSource(t, newConfiguredClient(t, server))
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

// TestTFSDK_ReadOtherRefusalIsNotReportedAsNotEnabled guards the discriminator:
// a 409 on this surface is not automatically the opt-in refusal, so branching on
// the status alone would mis-explain an unrelated conflict.
func TestTFSDK_ReadOtherRefusalIsNotReportedAsNotEnabled(t *testing.T) {
	server := newTestServer(t, &registryHandler{
		status: http.StatusConflict,
		code:   "conflict",
		reason: "REGISTRY_SUSPENDED",
	})
	defer server.Close()

	readResp, _ := readDataSource(t, newConfiguredClient(t, server))
	if !readResp.Diagnostics.HasError() {
		t.Fatal("expected an error")
	}
	for _, d := range readResp.Diagnostics.Errors() {
		if strings.Contains(d.Summary(), "not enabled") {
			t.Errorf("a non-opt-in 409 was reported as the opt-in refusal: %q", d.Summary())
		}
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
