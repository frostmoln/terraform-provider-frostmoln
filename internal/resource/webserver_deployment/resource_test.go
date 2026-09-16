package webserver_deployment

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/timeouts"
)

// --- hash helpers ---

func TestSHA256Hex(t *testing.T) {
	// Known vector: sha256("hello").
	const want = "2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824" // pragma: allowlist secret
	if got := sha256Hex([]byte("hello")); got != want {
		t.Fatalf("sha256Hex(\"hello\") = %s, want %s", got, want)
	}
}

func TestHashArchiveFile(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "site.tar.gz")
	content := []byte("pretend this is a gzipped tarball of a website")
	if err := os.WriteFile(p, content, 0o600); err != nil {
		t.Fatalf("write temp archive: %v", err)
	}

	got, err := hashArchiveFile(p)
	if err != nil {
		t.Fatalf("hashArchiveFile: %v", err)
	}
	if want := sha256Hex(content); got != want {
		t.Fatalf("hashArchiveFile = %s, want %s (must equal sha256Hex of the same bytes)", got, want)
	}

	// A changed archive (different bytes) must yield a different hash — this is
	// what drives change detection / a new deploy.
	if err := os.WriteFile(p, append(content, '!'), 0o600); err != nil {
		t.Fatalf("rewrite temp archive: %v", err)
	}
	changed, err := hashArchiveFile(p)
	if err != nil {
		t.Fatalf("hashArchiveFile (changed): %v", err)
	}
	if changed == got {
		t.Fatal("expected a different hash after the archive contents changed")
	}
}

func TestHashArchiveFileMissing(t *testing.T) {
	if _, err := hashArchiveFile(filepath.Join(t.TempDir(), "does-not-exist.tar.gz")); err == nil {
		t.Fatal("expected an error hashing a missing file")
	}
}

// --- fromAPI write-only preservation ---

// TestFromAPIPreservesWriteOnlyAttrs verifies the memory rule
// (tf-provider-readback-preserve): a read-back must NOT overwrite the write-only
// source_archive / source_hash (nor the create-time id / instance_id) that the
// API omits, or Terraform errors "provider produced inconsistent result".
func TestFromAPIPreservesWriteOnlyAttrs(t *testing.T) {
	m := &WebserverDeploymentModel{
		ID:            types.StringValue("inst-1"),
		InstanceID:    types.StringValue("inst-1"),
		SourceArchive: types.StringValue("/local/site.tar.gz"),
		SourceHash:    types.StringValue("deadbeef"),
		DeployID:      types.StringValue("old-deploy"),
		Status:        types.StringValue("succeeded"),
	}

	m.fromAPI(&apiDeploy{
		ID:         "new-deploy",
		InstanceID: "inst-1",
		Status:     "succeeded",
	})

	if m.ID.ValueString() != "inst-1" {
		t.Errorf("id was overwritten: %s", m.ID.ValueString())
	}
	if m.InstanceID.ValueString() != "inst-1" {
		t.Errorf("instance_id was overwritten: %s", m.InstanceID.ValueString())
	}
	if m.SourceArchive.ValueString() != "/local/site.tar.gz" {
		t.Errorf("source_archive (write-only) was overwritten: %s", m.SourceArchive.ValueString())
	}
	if m.SourceHash.ValueString() != "deadbeef" {
		t.Errorf("source_hash (write-only) was overwritten: %s", m.SourceHash.ValueString())
	}
	if m.DeployID.ValueString() != "new-deploy" {
		t.Errorf("deploy_id not refreshed: %s", m.DeployID.ValueString())
	}
	if m.Status.ValueString() != "succeeded" {
		t.Errorf("status not refreshed: %s", m.Status.ValueString())
	}
}

// TestGetPollDefaults pins the accessor defaults the timeouts block falls
// back to: a 5s internal poll interval and a 15m per-verb wait budget.
func TestGetPollDefaults(t *testing.T) {
	r := &webserverDeploymentResource{}
	if r.getPollInterval() != 5*time.Second {
		t.Errorf("expected default poll interval 5s, got %v", r.getPollInterval())
	}
	if r.getPollTimeout() != 15*time.Minute {
		t.Errorf("expected default poll timeout 15m, got %v", r.getPollTimeout())
	}
}

// TestResolveBudgetsDefaultsPinTodaysConstants pins the timeouts block's
// fallback: with no block configured, every verb budgets at the value this
// resource has always hardcoded, and a test's pollTimeout injection still
// shrinks the default (the resolveBudgets seam keeps the harness working).
func TestResolveBudgetsDefaultsPinTodaysConstants(t *testing.T) {
	bare := (&webserverDeploymentResource{}).resolveBudgets(nil)
	if want := timeouts.Uniform(15 * time.Minute); bare != want {
		t.Errorf("resolveBudgets(nil) = %+v, want %+v", bare, want)
	}

	r := &webserverDeploymentResource{pollTimeout: time.Second}
	if got := r.resolveBudgets(nil); got != timeouts.Uniform(time.Second) {
		t.Errorf("an injected pollTimeout must stay the default budget, got %+v", got)
	}
}

// TestTimeoutsBlockInSchema checks the schema carries the customer-tunable
// timeouts block with its three optional verbs.
func TestTimeoutsBlockInSchema(t *testing.T) {
	r := NewResource()
	var schemaResp resource.SchemaResponse
	r.Schema(context.Background(), resource.SchemaRequest{}, &schemaResp)

	b, ok := schemaResp.Schema.Blocks["timeouts"]
	if !ok {
		t.Fatal("expected the timeouts block in the schema")
	}
	nested, ok := b.(schema.SingleNestedBlock)
	if !ok {
		t.Fatalf("timeouts must be a single nested block, got %T", b)
	}
	for _, verb := range []string{"create", "update", "delete"} {
		attr, ok := nested.Attributes[verb]
		if !ok {
			t.Errorf("expected the %s attribute in the timeouts block", verb)
			continue
		}
		if !attr.IsOptional() {
			t.Errorf("timeouts.%s must be optional", verb)
		}
	}
}

// --- end-to-end deploy flow ---

func newTestDeploymentResource(c *client.Client, upload *http.Client) *webserverDeploymentResource {
	return &webserverDeploymentResource{
		client:       c,
		uploadClient: upload,
		pollInterval: time.Millisecond,
		pollTimeout:  5 * time.Second,
	}
}

// TestRunDeployFlow exercises the full create -> upload -> start -> poll flow and
// asserts the multipart upload carries every signed policy field plus the trailing
// "file" part.
func TestRunDeployFlow(t *testing.T) {
	dir := t.TempDir()
	archive := filepath.Join(dir, "site.tar.gz")
	archiveBytes := []byte("website archive bytes")
	if err := os.WriteFile(archive, archiveBytes, 0o600); err != nil {
		t.Fatalf("write archive: %v", err)
	}
	sum := sha256Hex(archiveBytes)

	var uploadHit, startHit bool
	var startedSHA string

	var serverURL string
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, req *http.Request) {
		p := req.URL.Path
		switch {
		case req.Method == http.MethodPost && strings.HasSuffix(p, "/deploys"):
			_ = json.NewEncoder(w).Encode(apiCreateDeployResponse{
				DeployID:     "dep-1",
				UploadURL:    serverURL + "/upload",
				UploadFields: map[string]string{"key": "staging/dep-1", "policy": "signed", "x-amz-signature": "sig"},
				ExpiresAt:    time.Now().Add(time.Hour).Format(time.RFC3339),
				Status:       "pending_upload",
			})
		case req.Method == http.MethodPost && p == "/upload":
			uploadHit = true
			if err := req.ParseMultipartForm(1 << 20); err != nil {
				t.Errorf("upload not multipart: %v", err)
			}
			// Every signed policy field must be present verbatim.
			for k, v := range map[string]string{"key": "staging/dep-1", "policy": "signed", "x-amz-signature": "sig"} {
				if got := req.FormValue(k); got != v {
					t.Errorf("upload field %q = %q, want %q", k, got, v)
				}
			}
			// The archive must ride as the "file" part.
			f, _, err := req.FormFile("file")
			if err != nil {
				t.Errorf("missing file part: %v", err)
			} else {
				_ = f.Close()
			}
			w.WriteHeader(http.StatusNoContent)
		case req.Method == http.MethodPost && strings.HasSuffix(p, "/start"):
			startHit = true
			var body apiStartDeployRequest
			_ = json.NewDecoder(req.Body).Decode(&body)
			startedSHA = body.SHA256
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(apiDeploy{ID: "dep-1", InstanceID: "inst-1", Status: "deploying"})
		case req.Method == http.MethodGet && strings.HasSuffix(p, "/deploys/dep-1"):
			_ = json.NewEncoder(w).Encode(apiDeploy{ID: "dep-1", InstanceID: "inst-1", Status: "succeeded", SHA256: sum})
		case strings.HasSuffix(req.URL.Path, "/events"):
			// The client waits on the tenant SSE stream instead of a timer
			// (internal/client/events.go). A 404 stands in for a gateway that does
			// not serve it -- an explicitly supported degradation back to timer
			// polling -- so this mock stays hermetic and exercises that path.
			w.WriteHeader(http.StatusNotFound)

		default:
			t.Errorf("unexpected request %s %s", req.Method, p)
			w.WriteHeader(http.StatusNotFound)
		}
	})
	server := httptest.NewServer(mux)
	defer server.Close()
	serverURL = server.URL

	c := client.NewClient(server.URL, "test-key", client.WithHTTPClient(server.Client())) // pragma: allowlist secret
	c.SetTenantIDForTest("t-1")
	r := newTestDeploymentResource(c, server.Client())

	outcome, err := r.runDeploy(context.Background(), "inst-1", archive, sum, r.getPollTimeout())
	if err != nil {
		t.Fatalf("runDeploy: %v", err)
	}
	if outcome.DeployID != "dep-1" {
		t.Errorf("deployID = %s, want dep-1", outcome.DeployID)
	}
	if outcome.Status != "succeeded" {
		t.Errorf("status = %s, want succeeded", outcome.Status)
	}
	if outcome.Unresolved {
		t.Error("a succeeded deploy must not be unresolved")
	}
	if !uploadHit {
		t.Error("upload endpoint was never called")
	}
	if !startHit {
		t.Error("start endpoint was never called")
	}
	if startedSHA != sum {
		t.Errorf("start SHA256 = %s, want %s", startedSHA, sum)
	}
}

// TestRunDeployFailed verifies a failed deploy surfaces the agent's error message.
func TestRunDeployFailed(t *testing.T) {
	dir := t.TempDir()
	archive := filepath.Join(dir, "site.tar.gz")
	if err := os.WriteFile(archive, []byte("x"), 0o600); err != nil {
		t.Fatalf("write archive: %v", err)
	}

	var serverURL string
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, req *http.Request) {
		p := req.URL.Path
		switch {
		case req.Method == http.MethodPost && strings.HasSuffix(p, "/deploys"):
			_ = json.NewEncoder(w).Encode(apiCreateDeployResponse{
				DeployID:     "dep-2",
				UploadURL:    serverURL + "/upload",
				UploadFields: map[string]string{"key": "staging/dep-2"},
				Status:       "pending_upload",
			})
		case p == "/upload":
			w.WriteHeader(http.StatusNoContent)
		case strings.HasSuffix(p, "/start"):
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(apiDeploy{ID: "dep-2", Status: "deploying"})
		case req.Method == http.MethodGet && strings.HasSuffix(p, "/deploys/dep-2"):
			_ = json.NewEncoder(w).Encode(apiDeploy{ID: "dep-2", Status: "failed", ErrorMessage: "checksum mismatch"})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})
	server := httptest.NewServer(mux)
	defer server.Close()
	serverURL = server.URL

	c := client.NewClient(server.URL, "test-key", client.WithHTTPClient(server.Client())) // pragma: allowlist secret
	c.SetTenantIDForTest("t-1")
	r := newTestDeploymentResource(c, server.Client())

	outcome, err := r.runDeploy(context.Background(), "inst-1", archive, "abc", r.getPollTimeout())
	if err == nil {
		t.Fatal("expected an error for a failed deploy")
	}
	if outcome.Unresolved {
		t.Error("an agent-reported failed deploy is a definite outcome, not unresolved")
	}
	if outcome.DeployID == "" {
		t.Error("a failed deploy must still carry its deploy id for observability")
	}
	if !strings.Contains(err.Error(), "checksum mismatch") {
		t.Errorf("error should carry the agent message, got: %v", err)
	}
}

// --- create-timeout orphan contract (adopt-and-track) ---

// buildDeploymentPlan creates a tfsdk.Plan pre-populated with a deployment.
func buildDeploymentPlan(t *testing.T, model WebserverDeploymentModel) tfsdk.Plan {
	t.Helper()
	r := NewResource()
	var schemaResp resource.SchemaResponse
	r.Schema(context.Background(), resource.SchemaRequest{}, &schemaResp)

	plan := tfsdk.Plan{Schema: schemaResp.Schema}
	if diags := plan.Set(context.Background(), &model); diags.HasError() {
		t.Fatalf("failed to set plan: %v", diags.Errors())
	}
	return plan
}

func emptyDeploymentState(t *testing.T) tfsdk.State {
	t.Helper()
	r := NewResource()
	var schemaResp resource.SchemaResponse
	r.Schema(context.Background(), resource.SchemaRequest{}, &schemaResp)

	stateVal := tftypes.NewValue(schemaResp.Schema.Type().TerraformType(context.Background()), nil)
	return tfsdk.State{Schema: schemaResp.Schema, Raw: stateVal}
}

func writeTempArchive(t *testing.T, bytes []byte) (path, sum string) {
	t.Helper()
	dir := t.TempDir()
	path = filepath.Join(dir, "site.tar.gz")
	if err := os.WriteFile(path, bytes, 0o600); err != nil {
		t.Fatalf("write archive: %v", err)
	}
	return path, sha256Hex(bytes)
}

// TestRunDeployWaitTimeoutIsUnresolved: start accepted, the agent never reaches
// a terminal status inside the budget → the UNKNOWN arm, carrying the deploy id.
func TestRunDeployWaitTimeoutIsUnresolved(t *testing.T) {
	archive, sum := writeTempArchive(t, []byte("unresolved flow"))

	var serverURL string
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, req *http.Request) {
		p := req.URL.Path
		switch {
		case req.Method == http.MethodPost && strings.HasSuffix(p, "/deploys") && !strings.HasSuffix(p, "/start"):
			_ = json.NewEncoder(w).Encode(apiCreateDeployResponse{
				DeployID: "dep-u", UploadURL: serverURL + "/upload",
				UploadFields: map[string]string{"key": "staging/dep-u"}, Status: "pending_upload",
			})
		case p == "/upload":
			w.WriteHeader(http.StatusNoContent)
		case strings.HasSuffix(p, "/start"):
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(apiDeploy{ID: "dep-u", Status: "deploying"})
		case strings.HasSuffix(p, "/deploys/dep-u"):
			_ = json.NewEncoder(w).Encode(apiDeploy{ID: "dep-u", Status: "deploying"})
		case strings.HasSuffix(p, "/events"):
			w.WriteHeader(http.StatusNotFound)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})
	server := httptest.NewServer(mux)
	defer server.Close()
	serverURL = server.URL

	c := client.NewClient(server.URL, "test-key", client.WithHTTPClient(server.Client())) // pragma: allowlist secret
	c.SetTenantIDForTest("t-1")
	r := newTestDeploymentResource(c, server.Client())
	r.pollTimeout = 150 * time.Millisecond

	outcome, err := r.runDeploy(context.Background(), "inst-1", archive, sum, r.getPollTimeout())
	if err == nil {
		t.Fatal("a wait that never resolves must error")
	}
	if !outcome.Unresolved {
		t.Error("a mid-deploy wait timeout is the unresolved arm — the deploy may still land")
	}
	if outcome.DeployID != "dep-u" {
		t.Errorf("unresolved outcome must carry the deploy id, got %q", outcome.DeployID)
	}
}

// TestRunDeployUploadFailureIsDefinite: the deploy record exists (observability
// handle) but the flow ended definitively — nothing was published.
func TestRunDeployUploadFailureIsDefinite(t *testing.T) {
	archive, sum := writeTempArchive(t, []byte("upload-fail flow"))

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, req *http.Request) {
		p := req.URL.Path
		switch {
		case req.Method == http.MethodPost && strings.HasSuffix(p, "/deploys") && !strings.HasSuffix(p, "/start"):
			_ = json.NewEncoder(w).Encode(apiCreateDeployResponse{
				DeployID: "dep-f", UploadURL: "http://127.0.0.1:1/upload",
				UploadFields: map[string]string{"key": "staging/dep-f"}, Status: "pending_upload",
			})
		case strings.HasSuffix(p, "/events"):
			w.WriteHeader(http.StatusNotFound)
		default:
			t.Errorf("unexpected request: %s %s", req.Method, p)
			w.WriteHeader(http.StatusNotFound)
		}
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	c := client.NewClient(server.URL, "test-key", client.WithHTTPClient(server.Client())) // pragma: allowlist secret
	c.SetTenantIDForTest("t-1")
	r := newTestDeploymentResource(c, &http.Client{Timeout: 2 * time.Second})

	outcome, err := r.runDeploy(context.Background(), "inst-1", archive, sum, r.getPollTimeout())
	if err == nil {
		t.Fatal("a rejected upload must error")
	}
	if outcome.Unresolved {
		t.Error("a rejected upload is definite — nothing was started, a fresh deploy is safe")
	}
	if outcome.DeployID != "dep-f" {
		t.Errorf("the deploy record exists after create, so the id must ride along, got %q", outcome.DeployID)
	}
}

// TestCreateWaitTimeoutAdoptsAndTracks: the full Create path on the unresolved
// arm — the deploy is adopted into state (deploy id + honest status read)
// BEFORE the error, so the tracked row survives the failed apply.
func TestCreateWaitTimeoutAdoptsAndTracks(t *testing.T) {
	archive, sum := writeTempArchive(t, []byte("adopt deploy"))
	var serverURL string
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, req *http.Request) {
		p := req.URL.Path
		switch {
		case req.Method == http.MethodPost && strings.HasSuffix(p, "/deploys") && !strings.HasSuffix(p, "/start"):
			_ = json.NewEncoder(w).Encode(apiCreateDeployResponse{
				DeployID: "dep-9", UploadURL: serverURL + "/upload",
				UploadFields: map[string]string{"key": "staging/dep-9"}, Status: "pending_upload",
			})
		case p == "/upload":
			w.WriteHeader(http.StatusNoContent)
		case strings.HasSuffix(p, "/start"):
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(apiDeploy{ID: "dep-9", Status: "deploying"})
		case strings.HasSuffix(p, "/deploys/dep-9"):
			// Deploying through the wait AND through the adoption's honest
			// read — the deploy simply has not finished when the provider
			// stopped looking, which is exactly the unresolved arm.
			_ = json.NewEncoder(w).Encode(apiDeploy{ID: "dep-9", Status: "deploying"})
		case strings.HasSuffix(p, "/events"):
			w.WriteHeader(http.StatusNotFound)
		default:
			t.Errorf("unexpected request: %s %s", req.Method, p)
			w.WriteHeader(http.StatusNotFound)
		}
	})
	server := httptest.NewServer(mux)
	defer server.Close()
	serverURL = server.URL

	c := client.NewClient(server.URL, "test-key", client.WithHTTPClient(server.Client())) // pragma: allowlist secret
	c.SetTenantIDForTest("t-1")
	r := newTestDeploymentResource(c, server.Client())
	r.pollTimeout = 100 * time.Millisecond

	plan := buildDeploymentPlan(t, WebserverDeploymentModel{
		InstanceID:    types.StringValue("inst-1"),
		SourceArchive: types.StringValue(archive),
	})
	createResp := resource.CreateResponse{State: emptyDeploymentState(t)}
	r.Create(context.Background(), resource.CreateRequest{Plan: plan}, &createResp)

	if !createResp.Diagnostics.HasError() {
		t.Fatal("an unobserved deploy outcome must error")
	}
	named := false
	for _, e := range createResp.Diagnostics.Errors() {
		if strings.Contains(e.Summary(), "Tracked In State") && strings.Contains(e.Detail(), "dep-9") {
			named = true
		}
	}
	if !named {
		t.Errorf("the adoption error must name the tracked deploy, got: %v", createResp.Diagnostics.Errors())
	}
	var result WebserverDeploymentModel
	if diags := createResp.State.Get(context.Background(), &result); diags.HasError() {
		t.Fatalf("the adopted state row must be readable: %v", diags.Errors())
	}
	if result.DeployID.ValueString() != "dep-9" {
		t.Errorf("tracked deploy_id = %q, want dep-9", result.DeployID.ValueString())
	}
	if result.SourceHash.ValueString() != sum {
		t.Errorf("tracked source_hash = %q, want %q", result.SourceHash.ValueString(), sum)
	}
	if result.Status.ValueString() != "deploying" {
		t.Errorf("the honest read settles status = %q, want deploying (the platform's last word)", result.Status.ValueString())
	}
}

// TestCreateAgentReportedFailureKeepsRetry: an agent-reported failed deploy is
// a definite platform decision — error, no state, and the next apply redeploys.
func TestCreateAgentReportedFailureKeepsRetry(t *testing.T) {
	archive, _ := writeTempArchive(t, []byte("definite fail"))
	var serverURL string
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, req *http.Request) {
		p := req.URL.Path
		switch {
		case req.Method == http.MethodPost && strings.HasSuffix(p, "/deploys") && !strings.HasSuffix(p, "/start"):
			_ = json.NewEncoder(w).Encode(apiCreateDeployResponse{
				DeployID: "dep-8", UploadURL: serverURL + "/upload",
				UploadFields: map[string]string{"key": "staging/dep-8"}, Status: "pending_upload",
			})
		case p == "/upload":
			w.WriteHeader(http.StatusNoContent)
		case strings.HasSuffix(p, "/start"):
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(apiDeploy{ID: "dep-8", Status: "deploying"})
		case strings.HasSuffix(p, "/deploys/dep-8"):
			_ = json.NewEncoder(w).Encode(apiDeploy{ID: "dep-8", Status: "failed", ErrorMessage: "checksum mismatch"})
		case strings.HasSuffix(p, "/events"):
			w.WriteHeader(http.StatusNotFound)
		default:
			t.Errorf("unexpected request: %s %s", req.Method, p)
			w.WriteHeader(http.StatusNotFound)
		}
	})
	server := httptest.NewServer(mux)
	defer server.Close()
	serverURL = server.URL

	c := client.NewClient(server.URL, "test-key", client.WithHTTPClient(server.Client())) // pragma: allowlist secret
	c.SetTenantIDForTest("t-1")
	r := newTestDeploymentResource(c, server.Client())

	plan := buildDeploymentPlan(t, WebserverDeploymentModel{
		InstanceID:    types.StringValue("inst-1"),
		SourceArchive: types.StringValue(archive),
	})
	createResp := resource.CreateResponse{State: emptyDeploymentState(t)}
	r.Create(context.Background(), resource.CreateRequest{Plan: plan}, &createResp)

	if !createResp.Diagnostics.HasError() {
		t.Fatal("an agent-reported failed deploy must error")
	}
	named := false
	for _, e := range createResp.Diagnostics.Errors() {
		if strings.Contains(e.Detail(), "dep-8") {
			named = true
		}
	}
	if !named {
		t.Errorf("the failure must name the deploy record, got: %v", createResp.Diagnostics.Errors())
	}
	if !createResp.State.Raw.IsNull() {
		t.Error("a definitely failed deploy stays untracked so the next apply redeploys")
	}
}
