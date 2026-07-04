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

	"github.com/hashicorp/terraform-plugin-framework/types"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
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

	deployID, status, err := r.runDeploy(context.Background(), "inst-1", archive, sum)
	if err != nil {
		t.Fatalf("runDeploy: %v", err)
	}
	if deployID != "dep-1" {
		t.Errorf("deployID = %s, want dep-1", deployID)
	}
	if status != "succeeded" {
		t.Errorf("status = %s, want succeeded", status)
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

	_, _, err := r.runDeploy(context.Background(), "inst-1", archive, "abc")
	if err == nil {
		t.Fatal("expected an error for a failed deploy")
	}
	if !strings.Contains(err.Error(), "checksum mismatch") {
		t.Errorf("error should carry the agent message, got: %v", err)
	}
}
