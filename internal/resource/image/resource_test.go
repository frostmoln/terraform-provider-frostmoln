package image

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	rschema "github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
)

// --- helpers -----------------------------------------------------------------

// testTenantID is the tenant every mock in this package is scoped to — the
// tenant the provider is configured for, which is NOT necessarily the caller's
// home tenant.
const testTenantID = "tenant-456"

// newTestImageClient builds a client already scoped to testTenantID, the way a
// configured provider is. Every image call has to name the tenant in the path:
// the api-gateway re-scopes and re-signs the auth-context JWT only for
// /tenants/{id}/ paths, so a bare /v1/images request would be served against the
// caller's home tenant instead. The mocks below route only the tenant-scoped
// paths, so a regression to a bare path shows up as an unexpected request.
func newTestImageClient(server *httptest.Server) *client.Client {
	c := client.NewClient(server.URL, "test-key", client.WithHTTPClient(server.Client())) // pragma: allowlist secret
	c.SetTenantIDForTest(testTenantID)
	return c
}

func newTestImageResource(c *client.Client, upload *http.Client) *imageResource {
	return &imageResource{
		client:              c,
		uploadClient:        upload,
		pollInterval:        time.Millisecond,
		pollTimeout:         5 * time.Second,
		deleteRetryInterval: time.Millisecond,
	}
}

// resourceSchema returns the resource's schema, which every tfsdk-level test
// needs to build a plan or state value.
func resourceSchema(t *testing.T) rschema.Schema {
	t.Helper()
	var schemaResp resource.SchemaResponse
	(&imageResource{}).Schema(context.Background(), resource.SchemaRequest{}, &schemaResp)
	if schemaResp.Diagnostics.HasError() {
		t.Fatalf("schema: %v", schemaResp.Diagnostics.Errors())
	}
	return schemaResp.Schema
}

// writeImageFile writes n bytes of stand-in disk image and returns the path.
func writeImageFile(t *testing.T, n int) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "ubuntu.qcow2")
	if err := os.WriteFile(p, make([]byte, n), 0o600); err != nil {
		t.Fatalf("write temp image: %v", err)
	}
	return p
}

// createPlan builds a full Create plan value, with every computed attribute
// unknown — what the framework hands Create in a real apply.
func createPlan(t *testing.T, s rschema.Schema, sourceFile string) tftypes.Value {
	t.Helper()
	return objectValue(s, createPlanAttrs(sourceFile))
}

// createPlanAttrs is createPlan's attribute map, exposed so a test can override
// individual attributes before building the value (tftypes.Value is immutable
// and offers no way to edit one after the fact).
func createPlanAttrs(sourceFile string) map[string]tftypes.Value {
	return map[string]tftypes.Value{
		"id":               tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"name":             tftypes.NewValue(tftypes.String, "custom-ubuntu"),
		"description":      tftypes.NewValue(tftypes.String, nil),
		"source_file":      tftypes.NewValue(tftypes.String, sourceFile),
		"source_file_hash": tftypes.NewValue(tftypes.String, nil),
		"disk_format":      tftypes.NewValue(tftypes.String, "qcow2"),
		"container_format": tftypes.NewValue(tftypes.String, "bare"),
		"os_distro":        tftypes.NewValue(tftypes.String, nil),
		"os_version":       tftypes.NewValue(tftypes.String, nil),
		"architecture":     tftypes.NewValue(tftypes.String, nil),
		"default_user":     tftypes.NewValue(tftypes.String, nil),
		"min_disk_gb":      tftypes.NewValue(tftypes.Number, tftypes.UnknownValue),
		"min_ram_mb":       tftypes.NewValue(tftypes.Number, tftypes.UnknownValue),
		"status":           tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"size":             tftypes.NewValue(tftypes.Number, tftypes.UnknownValue),
		"virtual_size":     tftypes.NewValue(tftypes.Number, tftypes.UnknownValue),
		"checksum":         tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"visibility":       tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"owner":            tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"created_at":       tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
	}
}

// objectValue assembles an attribute map into a value of the resource's type.
func objectValue(s rschema.Schema, attrs map[string]tftypes.Value) tftypes.Value {
	return tftypes.NewValue(s.Type().TerraformType(context.Background()), attrs)
}

// stateValue builds a prior-state value for an image that already exists, with
// every attribute set to something plausible (Read and Delete only ever look at
// the id, but a state value has to be complete for its schema).
func stateValue(t *testing.T, s rschema.Schema, imageID string) tftypes.Value {
	t.Helper()
	return objectValue(s, stateAttrs(imageID))
}

// stateAttrs is stateValue's attribute map, exposed for the same reason as
// createPlanAttrs.
func stateAttrs(imageID string) map[string]tftypes.Value {
	return map[string]tftypes.Value{
		"id":               tftypes.NewValue(tftypes.String, imageID),
		"name":             tftypes.NewValue(tftypes.String, "custom-ubuntu"),
		"description":      tftypes.NewValue(tftypes.String, nil),
		"source_file":      tftypes.NewValue(tftypes.String, "/local/ubuntu.qcow2"),
		"source_file_hash": tftypes.NewValue(tftypes.String, nil),
		"disk_format":      tftypes.NewValue(tftypes.String, "qcow2"),
		"container_format": tftypes.NewValue(tftypes.String, "bare"),
		"os_distro":        tftypes.NewValue(tftypes.String, nil),
		"os_version":       tftypes.NewValue(tftypes.String, nil),
		"architecture":     tftypes.NewValue(tftypes.String, nil),
		"default_user":     tftypes.NewValue(tftypes.String, nil),
		"min_disk_gb":      tftypes.NewValue(tftypes.Number, 0),
		"min_ram_mb":       tftypes.NewValue(tftypes.Number, 0),
		"status":           tftypes.NewValue(tftypes.String, "active"),
		"size":             tftypes.NewValue(tftypes.Number, 1024),
		"virtual_size":     tftypes.NewValue(tftypes.Number, 4096),
		"checksum":         tftypes.NewValue(tftypes.String, "abc123"),
		"visibility":       tftypes.NewValue(tftypes.String, "private"),
		"owner":            tftypes.NewValue(tftypes.String, "proj-1"),
		"created_at":       tftypes.NewValue(tftypes.String, "2026-08-07T10:00:00Z"),
	}
}

// runCreate drives the real Create entry point against the given resource and
// returns the response, so the tests can assert on what reached STATE — the
// property that matters most here (an id that never lands orphans a real image).
func runCreate(t *testing.T, r *imageResource, sourceFile string) *resource.CreateResponse {
	t.Helper()
	ctx := context.Background()
	s := resourceSchema(t)

	planVal := createPlan(t, s, sourceFile)
	createResp := &resource.CreateResponse{State: tfsdk.State{Schema: s}}
	r.Create(ctx, resource.CreateRequest{Plan: tfsdk.Plan{Schema: s, Raw: planVal}}, createResp)
	return createResp
}

// --- create -> upload -> import ordering -------------------------------------

// TestCreateUploadImportOrder exercises the full create -> upload -> import ->
// poll flow and asserts both the ORDER the platform is called in and that the
// multipart upload carries every signed policy field plus a trailing "file"
// part. Order is the contract: importing before the bytes are staged, or
// uploading before the image record exists, both fail server-side.
func TestCreateUploadImportOrder(t *testing.T) {
	source := writeImageFile(t, 1024)

	var calls []string
	var uploadFields map[string]string
	var sawFilePart bool

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, req *http.Request) {
		p := req.URL.Path
		switch {
		case req.Method == http.MethodPost && p == "/v1/tenants/tenant-456/images":
			calls = append(calls, "create")
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(apiCreateImageResponse{
				apiImage: apiImage{ID: "img-1", Name: "custom-ubuntu", Status: "queued", Visibility: "private", Owner: "proj-1"},
				Upload: &apiImageUpload{
					// Built from the request's own Host: the test goroutine
					// assigning server.URL to a captured variable has no
					// happens-before edge with the handler goroutines reading it.
					URL:             "http://" + req.Host + "/staging",
					Fields:          map[string]string{"key": "staging/proj-1/img-1", "policy": "signed", "x-amz-signature": "sig"},
					MaxBytes:        1 << 30,
					MaxVirtualBytes: 4 << 30,
				},
			})
		case req.Method == http.MethodPost && p == "/staging":
			calls = append(calls, "upload")
			if err := req.ParseMultipartForm(1 << 20); err != nil {
				t.Errorf("upload not multipart: %v", err)
			}
			uploadFields = map[string]string{}
			for k := range req.MultipartForm.Value {
				uploadFields[k] = req.FormValue(k)
			}
			if f, _, err := req.FormFile("file"); err != nil {
				t.Errorf("missing file part: %v", err)
			} else {
				sawFilePart = true
				_ = f.Close()
			}
			w.WriteHeader(http.StatusNoContent)
		case req.Method == http.MethodPost && p == "/v1/tenants/tenant-456/images/img-1/import":
			calls = append(calls, "import")
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(apiImage{ID: "img-1", Status: "importing"})
		case req.Method == http.MethodGet && p == "/v1/tenants/tenant-456/images/img-1":
			calls = append(calls, "poll")
			_ = json.NewEncoder(w).Encode(apiImage{
				ID: "img-1", Name: "custom-ubuntu", Status: "active", Visibility: "private",
				Owner: "proj-1", Size: 1024, VirtualSize: 4096, Checksum: "abc123", CreatedAt: "2026-08-07T10:00:00Z",
			})
		default:
			t.Errorf("unexpected request %s %s", req.Method, p)
			w.WriteHeader(http.StatusNotFound)
		}
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	c := newTestImageClient(server)
	r := newTestImageResource(c, server.Client())

	createResp := runCreate(t, r, source)
	if createResp.Diagnostics.HasError() {
		t.Fatalf("Create failed: %v", createResp.Diagnostics.Errors())
	}

	if len(calls) < 4 || calls[0] != "create" || calls[1] != "upload" || calls[2] != "import" || calls[3] != "poll" {
		t.Fatalf("wrong call order: %v (want create, upload, import, poll...)", calls)
	}
	if !sawFilePart {
		t.Error("the image never rode as the \"file\" part")
	}
	for k, v := range map[string]string{"key": "staging/proj-1/img-1", "policy": "signed", "x-amz-signature": "sig"} {
		if uploadFields[k] != v {
			t.Errorf("upload field %q = %q, want %q (signed policy fields must be sent verbatim)", k, uploadFields[k], v)
		}
	}

	var state ImageModel
	createResp.State.Get(context.Background(), &state)
	if state.ID.ValueString() != "img-1" {
		t.Errorf("id = %q, want img-1", state.ID.ValueString())
	}
	if state.Status.ValueString() != "active" {
		t.Errorf("status = %q, want active", state.Status.ValueString())
	}
	if state.Checksum.ValueString() != "abc123" {
		t.Errorf("checksum = %q, want abc123", state.Checksum.ValueString())
	}
}

// --- the dark-in-production path ---------------------------------------------

// TestCreateWithoutUploadFormPersistsIDOn503 is the production-today path: image
// staging is off, so the create still answers 201 (with no `upload`), the single
// mint attempt 503s, and the apply fails.
//
// Two things must hold. The id has to be in state anyway — the image EXISTS and
// consumes the tenant's allowance, and an id that never lands leaves them owning
// something Terraform cannot destroy. And the diagnostic has to carry the
// server's own message rather than a provider-invented one.
func TestCreateWithoutUploadFormPersistsIDOn503(t *testing.T) {
	source := writeImageFile(t, 512)

	const serverMessage = "image upload is not available in this environment"
	mintAttempts := 0

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, req *http.Request) {
		switch {
		case req.Method == http.MethodPost && req.URL.Path == "/v1/tenants/tenant-456/images":
			// 201 with the image at the top level and NO upload form.
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(apiCreateImageResponse{
				apiImage: apiImage{ID: "img-dark", Name: "custom-ubuntu", Status: "queued", Visibility: "private"},
			})
		case req.Method == http.MethodPost && req.URL.Path == "/v1/tenants/tenant-456/images/img-dark/upload":
			mintAttempts++
			w.WriteHeader(http.StatusServiceUnavailable)
			_ = json.NewEncoder(w).Encode(map[string]string{
				"code": "internal_error", "message": serverMessage,
			})
		default:
			t.Errorf("unexpected request %s %s", req.Method, req.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	c := newTestImageClient(server)
	r := newTestImageResource(c, server.Client())

	createResp := runCreate(t, r, source)
	if !createResp.Diagnostics.HasError() {
		t.Fatal("expected an error when no upload form is available")
	}
	if mintAttempts != 1 {
		t.Errorf("minted %d times, want exactly 1 — a mint is charged against a per-tenant hourly budget "+
			"and must never be retried", mintAttempts)
	}

	detail := createResp.Diagnostics.Errors()[0].Detail()
	if !strings.Contains(detail, serverMessage) {
		t.Errorf("diagnostic must carry the server's own message, got: %s", detail)
	}

	var state ImageModel
	createResp.State.Get(context.Background(), &state)
	if state.ID.ValueString() != "img-dark" {
		t.Fatalf("id = %q, want img-dark — the image exists server-side and MUST be in state, "+
			"or Terraform orphans it", state.ID.ValueString())
	}
}

// --- size pre-check ----------------------------------------------------------

// TestCreateRefusesOversizedFileBeforeUploading asserts the local file is
// measured against upload.maxBytes BEFORE a single byte is sent: the staging
// endpoint must never be reached, and the failure must still leave the created
// image's id in state.
func TestCreateRefusesOversizedFileBeforeUploading(t *testing.T) {
	source := writeImageFile(t, 4096)

	uploadHit := false
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, req *http.Request) {
		switch {
		case req.Method == http.MethodPost && req.URL.Path == "/v1/tenants/tenant-456/images":
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(apiCreateImageResponse{
				apiImage: apiImage{ID: "img-big", Name: "custom-ubuntu", Status: "queued", Visibility: "private"},
				Upload: &apiImageUpload{
					URL:             "http://" + req.Host + "/staging",
					Fields:          map[string]string{"key": "staging/img-big"},
					MaxBytes:        1024,
					MaxVirtualBytes: 8192,
				},
			})
		case req.URL.Path == "/staging":
			uploadHit = true
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected request %s %s", req.Method, req.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	c := newTestImageClient(server)
	r := newTestImageResource(c, server.Client())

	createResp := runCreate(t, r, source)
	if !createResp.Diagnostics.HasError() {
		t.Fatal("expected an error for a file larger than maxBytes")
	}
	if uploadHit {
		t.Error("the upload was attempted despite the file exceeding maxBytes")
	}

	detail := createResp.Diagnostics.Errors()[0].Detail()
	if !strings.Contains(detail, "4096") || !strings.Contains(detail, "1024") {
		t.Errorf("the diagnostic should quote both sizes, got: %s", detail)
	}
	// maxVirtualBytes cannot be checked locally, but it must be mentioned — it is
	// otherwise discoverable only by uploading and failing at import.
	if !strings.Contains(detail, "8192") {
		t.Errorf("the diagnostic should mention the post-conversion limit, got: %s", detail)
	}

	var state ImageModel
	createResp.State.Get(context.Background(), &state)
	if state.ID.ValueString() != "img-big" {
		t.Errorf("id = %q, want img-big", state.ID.ValueString())
	}
}

// TestCreateRefusesImportWhenTheStorageEdgeChecksumDisagrees covers the arrival
// check: the edge reports an MD5 over what it received, and if that is not what
// we sent the staged object is not source_file. Importing it would convert
// corrupt bytes into an image that boots wrong instead of one that visibly
// failed, so the apply must fail with the import never started.
//
// It also pins the direction of the ETag test. An ETag that is NOT a plain MD5
// (multipart, or an opaque value from a server-side-encrypted bucket) means
// UNCHECKED, never corrupt — TestCreateUploadImportOrder covers that path, where
// the edge sends no ETag at all and the import proceeds. Getting this backwards
// would turn a bucket-policy change into a total BYOI outage.
func TestCreateRefusesImportWhenTheStorageEdgeChecksumDisagrees(t *testing.T) {
	source := writeImageFile(t, 2048)

	importHit := false
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, req *http.Request) {
		switch {
		case req.Method == http.MethodPost && req.URL.Path == "/v1/tenants/tenant-456/images":
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(apiCreateImageResponse{
				apiImage: apiImage{ID: "img-bad", Name: "custom-ubuntu", Status: "queued", Visibility: "private"},
				Upload: &apiImageUpload{
					URL:             "http://" + req.Host + "/staging",
					Fields:          map[string]string{"key": "staging/img-bad"},
					MaxBytes:        1 << 30,
					MaxVirtualBytes: 4 << 30,
				},
			})
		case req.URL.Path == "/staging":
			_, _ = io.Copy(io.Discard, req.Body)
			// A well-formed MD5 that is not the one we sent: the shape of an
			// object that arrived damaged, not of an edge that cannot answer.
			w.Header().Set("ETag", `"00000000000000000000000000000000"`)
			w.WriteHeader(http.StatusNoContent)
		case req.URL.Path == "/v1/tenants/tenant-456/images/img-bad/import":
			importHit = true
			w.WriteHeader(http.StatusAccepted)
		default:
			t.Errorf("unexpected request %s %s", req.Method, req.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	c := newTestImageClient(server)
	r := newTestImageResource(c, server.Client())

	createResp := runCreate(t, r, source)
	if !createResp.Diagnostics.HasError() {
		t.Fatal("expected the apply to fail when the storage edge reports a different checksum")
	}
	if importHit {
		t.Error("the import must not start for an object that did not arrive intact")
	}

	// The image record exists and holds staging quota; losing its id would leave
	// the customer unable to destroy it.
	var state ImageModel
	createResp.State.Get(context.Background(), &state)
	if state.ID.ValueString() != "img-bad" {
		t.Errorf("id = %q, want img-bad", state.ID.ValueString())
	}
}

// --- import failure ----------------------------------------------------------

// TestWaitForImportTerminatesOnImportFailed is the trap this resource exists to
// avoid: Glance reverts a FAILED interoperable import to `queued`, not to
// `killed`, so a wait keyed on `active` never terminates. The poll timeout is
// far longer than the test's own patience, so the test fails by hanging if the
// importFailed signal is ever dropped.
func TestWaitForImportTerminatesOnImportFailed(t *testing.T) {
	polls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Method != http.MethodGet || req.URL.Path != "/v1/tenants/tenant-456/images/img-fail" {
			t.Errorf("unexpected request %s %s", req.Method, req.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		polls++
		// Status stays `queued` forever — exactly like an image nobody uploaded.
		_ = json.NewEncoder(w).Encode(apiImage{
			ID: "img-fail", Status: "queued", ImportFailed: true, ImportFailedStores: "rbd",
		})
	}))
	defer server.Close()

	c := newTestImageClient(server)
	r := newTestImageResource(c, server.Client())
	r.pollTimeout = time.Minute // long enough that a hang is a hang, not a timeout

	done := make(chan error, 1)
	go func() {
		_, err := r.waitForImport(context.Background(), "img-fail")
		done <- err
	}()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected an error for a failed import")
		}
		if !strings.Contains(err.Error(), "rbd") {
			t.Errorf("the error should name the failed stores, got: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatalf("waitForImport did not terminate on importFailed (polled %d times) — "+
			"a status-only wait hangs until the timeout on every failed import", polls)
	}
}

// TestWaitForImportSucceedsOnActive is the happy-path counterpart: an image that
// reaches `active` terminates and is returned.
func TestWaitForImportSucceedsOnActive(t *testing.T) {
	polls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		polls++
		status := "importing"
		if polls > 2 {
			status = "active"
		}
		_ = json.NewEncoder(w).Encode(apiImage{ID: "img-ok", Status: status, Size: 99})
	}))
	defer server.Close()

	c := newTestImageClient(server)
	r := newTestImageResource(c, server.Client())

	img, err := r.waitForImport(context.Background(), "img-ok")
	if err != nil {
		t.Fatalf("waitForImport: %v", err)
	}
	if img.Status != "active" || img.Size != 99 {
		t.Errorf("got %+v, want the active image", img)
	}
}

// --- delete ------------------------------------------------------------------

// TestDeleteTolerates404 covers the image that is already gone (deleted out of
// band, or a create that failed before the import). The end state is the one
// that was asked for, so the destroy must succeed.
func TestDeleteTolerates404(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Method != http.MethodDelete {
			t.Errorf("unexpected request %s %s", req.Method, req.URL.Path)
		}
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]string{"code": "not_found", "message": "image not found"})
	}))
	defer server.Close()

	c := newTestImageClient(server)
	r := newTestImageResource(c, server.Client())

	ctx := context.Background()
	s := resourceSchema(t)

	stateVal := stateValue(t, s, "img-gone")
	deleteResp := &resource.DeleteResponse{State: tfsdk.State{Schema: s, Raw: stateVal}}
	r.Delete(ctx, resource.DeleteRequest{State: tfsdk.State{Schema: s, Raw: stateVal}}, deleteResp)

	if deleteResp.Diagnostics.HasError() {
		t.Fatalf("Delete must tolerate a 404, got: %v", deleteResp.Diagnostics.Errors())
	}
}

// deleteRefusal is compute's 409 for a delete it will not do. reason picks which
// of the three refusals it is; retryAfterSeconds is omitted entirely when <= 0,
// because "absent" is exactly what the platform sends when no wait clears it.
func deleteRefusal(reason string, retryAfterSeconds int) map[string]any {
	return deleteRefusalSeconds(reason, float64(retryAfterSeconds))
}

// deleteRefusalSeconds is deleteRefusal for a wait that is not a whole number of
// seconds — the shape only a buggy or hostile server sends.
func deleteRefusalSeconds(reason string, retryAfterSeconds float64) map[string]any {
	details := map[string]any{"reason": reason}
	if retryAfterSeconds > 0 {
		details["retryAfterSeconds"] = retryAfterSeconds
	}
	return map[string]any{"code": "invalid_state", "message": "image is being imported", "details": details}
}

// deleteServer answers DELETE with the given responses in order (the last one
// repeats), and reports how many requests it took.
func deleteServer(t *testing.T, responses ...func(w http.ResponseWriter)) (*httptest.Server, *int) {
	t.Helper()
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Method != http.MethodDelete {
			t.Errorf("unexpected request %s %s", req.Method, req.URL.Path)
		}
		i := calls
		calls++
		if i >= len(responses) {
			i = len(responses) - 1
		}
		responses[i](w)
	}))
	return server, &calls
}

func refuse(body map[string]any) func(http.ResponseWriter) {
	return func(w http.ResponseWriter) {
		w.WriteHeader(http.StatusConflict)
		_ = json.NewEncoder(w).Encode(body)
	}
}

func accept() func(http.ResponseWriter) {
	return func(w http.ResponseWriter) { w.WriteHeader(http.StatusNoContent) }
}

// deleteResource builds an image resource pointed at server. The poll interval is
// a parameter because the delete loop now waits on it rather than on the server's
// number, so a test that measures requests-per-budget has to set it.
func deleteResource(t *testing.T, server *httptest.Server, pollTimeout, retryInterval time.Duration) *imageResource {
	t.Helper()
	c := newTestImageClient(server)
	r := newTestImageResource(c, server.Client())
	r.pollTimeout, r.deleteRetryInterval = pollTimeout, retryInterval
	return r
}

// runDeleteWith drives r's Delete under ctx and returns the response.
func runDeleteWith(t *testing.T, ctx context.Context, r *imageResource) *resource.DeleteResponse {
	t.Helper()
	s := resourceSchema(t)
	stateVal := stateValue(t, s, "img-wedged")
	deleteResp := &resource.DeleteResponse{State: tfsdk.State{Schema: s, Raw: stateVal}}
	r.Delete(ctx, resource.DeleteRequest{State: tfsdk.State{Schema: s, Raw: stateVal}}, deleteResp)
	return deleteResp
}

// runDelete is runDeleteWith for the common case: no cancellation, and a poll
// interval short enough that a test never waits on it.
func runDelete(t *testing.T, server *httptest.Server, pollTimeout time.Duration) *resource.DeleteResponse {
	t.Helper()
	return runDeleteWith(t, context.Background(), deleteResource(t, server, pollTimeout, time.Millisecond))
}

// An image wedged in `importing` is deletable once Glance's import lock expires,
// and compute says exactly when. A destroy must sit out a wait that fits its
// budget instead of failing an apply that only needed to be patient — that is
// the whole remedy compute v2.37.0 shipped, and before this the provider threw
// away every 409 it was given.
func TestDeleteWaitsOutTheImportLock(t *testing.T) {
	server, calls := deleteServer(t, refuse(deleteRefusal("import_running", 1)), accept())
	defer server.Close()

	start := time.Now()
	deleteResp := runDelete(t, server, 30*time.Second)

	if deleteResp.Diagnostics.HasError() {
		t.Fatalf("Delete must wait the lock out and succeed, got: %v", deleteResp.Diagnostics.Errors())
	}

	if *calls != 2 {
		t.Errorf("made %d delete requests, want 2 (the refusal and the retry)", *calls)
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Errorf("took %s — the destroy did not retry promptly after the refusal", elapsed)
	}
}

// A quote LARGER than the destroy's whole budget must not stop it from trying.
// The quote is the import's stall fallback, not an appointment: a healthy import
// quotes ~65 minutes for its whole life — more than the 60-minute budget — and
// then goes deletable as soon as it finishes. Weighing the quote against the
// budget would refuse the one case retrying exists for, on the first request.
// What the budget bounds is how long the retrying lasts, and the diagnostic then
// quotes the platform's LAST answer.
func TestDeleteRetriesEvenWhenTheQuoteOutlastsTheBudget(t *testing.T) {
	server, calls := deleteServer(t, refuse(deleteRefusal("import_running", 3900)))
	defer server.Close()

	start := time.Now()
	deleteResp := runDeleteWith(t, context.Background(),
		deleteResource(t, server, 600*time.Millisecond, 100*time.Millisecond))

	if !deleteResp.Diagnostics.HasError() {
		t.Fatal("Delete must surface an import that never lets go")
	}
	if *calls < 3 {
		t.Errorf("made %d delete requests — a 65-minute quote stopped it from retrying at all", *calls)
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Errorf("took %s — the destroy outlived its budget", elapsed)
	}
	detail := deleteResp.Diagnostics.Errors()[0].Detail()
	if !strings.Contains(detail, "1h5m0s") {
		t.Errorf("diagnostic does not tell the practitioner how long the stall has left:\n%s", detail)
	}
	if !strings.Contains(detail, "Re-run the destroy") {
		t.Errorf("diagnostic does not say that re-running later works:\n%s", detail)
	}
	// The quoted time is how long the import has left to STALL in, not when the
	// delete starts working: compute derives it from the import task's idle
	// timer, which a healthy import keeps pushing forward. Every delete aimed at
	// a running import lands here, so copy promising success at that moment is
	// wrong on the common path.
	if !strings.Contains(detail, "FINISHES") {
		t.Errorf("diagnostic does not say the import finishing is what clears it:\n%s", detail)
	}
	// The copy must not quote the FULL budget as the thing the wait had to fit
	// inside: after a first wait the bound is what is left of it, and a
	// diagnostic saying 35m did not fit inside 1h reads as a provider bug.
	if strings.Contains(detail, defaultPollTimeout.String()) {
		t.Errorf("diagnostic quotes a budget it does not enforce:\n%s", detail)
	}
}

// The loop polls on the RESOURCE's interval, never on a server-supplied number,
// so a pathological wait cannot become a request storm. Left to the server, a
// fractional second would mean thousands of DELETEs against the service that
// just refused — and then the gateway's rate limiter on top.
func TestATinyServerWaitStillPollsOnTheResourceInterval(t *testing.T) { //nolint:revive // named for the behaviour, not the field
	server, calls := deleteServer(t, refuse(deleteRefusalSeconds("import_running", 0.05)), accept())
	defer server.Close()

	start := time.Now()
	deleteResp := runDeleteWith(t, context.Background(),
		deleteResource(t, server, 30*time.Second, 300*time.Millisecond))

	if deleteResp.Diagnostics.HasError() {
		t.Fatalf("Delete must still succeed, got: %v", deleteResp.Diagnostics.Errors())
	}
	if *calls != 2 {
		t.Errorf("made %d delete requests, want 2", *calls)
	}
	if elapsed := time.Since(start); elapsed < 300*time.Millisecond {
		t.Errorf("retried after %s — a 50ms server wait was slept instead of the poll interval", elapsed)
	}
}

// A wait that clears EARLY must be noticed. compute's wait is the import's
// remaining STALL budget — a healthy import refreshes it about once a minute, so
// a four-minute import quotes ~65 minutes throughout and then becomes deletable
// the moment it finishes. Sleeping the whole quoted wait would miss that by the
// best part of an hour, so a single sleep is capped and the delete re-probed.
func TestALongWaitIsReProbedRatherThanSleptWhole(t *testing.T) {
	// Refused with an hour to go, then the import finishes and the delete works.
	server, calls := deleteServer(t, refuse(deleteRefusal("import_running", 3600)), accept())
	defer server.Close()

	start := time.Now()
	// A budget SMALLER than the quoted wait, as production's always is: 60
	// minutes of patience against the ~65 a healthy import quotes throughout.
	deleteResp := runDeleteWith(t, context.Background(),
		deleteResource(t, server, 5*time.Second, time.Millisecond))

	if deleteResp.Diagnostics.HasError() {
		t.Fatalf("Delete must retry and succeed once the import finishes, got: %v", deleteResp.Diagnostics.Errors())
	}
	if *calls != 2 {
		t.Errorf("made %d delete requests, want 2", *calls)
	}
	// The retry interval bounds the sleep; the quoted hour does not.
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Errorf("slept %s — the whole quoted wait was slept instead of retrying", elapsed)
	}
}

// A refusal with NO wait attached is not slept on: compute sends no wait when it
// cannot compute one, and there is nothing to sleep until. The copy, though, must
// NOT declare the refusal permanent — one of the causes compute omits the wait
// for is simply failing to read the import task from Glance at that moment, which
// clears by itself. So: no sleep, but "try again shortly" rather than "waiting
// will not help, open a ticket".
func TestDeleteDoesNotWaitARefusalWithNoWait(t *testing.T) {
	server, calls := deleteServer(t, refuse(deleteRefusal("import_running", 0)))
	defer server.Close()

	start := time.Now()
	deleteResp := runDelete(t, server, 30*time.Second)

	if !deleteResp.Diagnostics.HasError() {
		t.Fatal("Delete must surface a refusal no wait clears")
	}
	if *calls != 1 {
		t.Errorf("made %d delete requests, want 1", *calls)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("took %s — it slept on a refusal that carries no wait", elapsed)
	}
	detail := deleteResp.Diagnostics.Errors()[0].Detail()
	if !strings.Contains(detail, "Re-run the destroy shortly") {
		t.Errorf("diagnostic does not offer the retry that clears the transient cause:\n%s", detail)
	}
	// A refusal with no wait is NOT proof of permanence: compute also omits the
	// wait when it merely could not read the import task, and a rolling
	// glance-api is a routine event. Copy that forecasts failure sends someone to
	// support for a thirty-second blip.
	for _, forecast := range []string{"Waiting is not the remedy", "will keep failing"} {
		if strings.Contains(detail, forecast) {
			t.Errorf("diagnostic forecasts permanence for a cause that can be transient (%q):\n%s", forecast, detail)
		}
	}
}

// The sibling 409s share the code and differ only by `details.reason`. Neither
// is cleared by waiting, so both must fail the destroy on the first request and
// must NOT pick up the import-lock copy.
func TestDeleteDoesNotWaitTheSiblingRefusals(t *testing.T) {
	for _, reason := range []string{"protected", "undeletable_status"} {
		t.Run(reason, func(t *testing.T) {
			server, calls := deleteServer(t, refuse(deleteRefusal(reason, 3900)))
			defer server.Close()

			deleteResp := runDelete(t, server, 30*time.Second)

			if !deleteResp.Diagnostics.HasError() {
				t.Fatalf("Delete must surface the %s refusal", reason)
			}
			if *calls != 1 {
				t.Errorf("made %d delete requests, want 1 — %s is not cleared by waiting", *calls, reason)
			}
			// "still being imported" is the provider's own copy; the fixture's
			// server message says "image is being imported", so this cannot pass
			// on the server sentence alone.
			if detail := deleteResp.Diagnostics.Errors()[0].Detail(); strings.Contains(detail, "still being imported") {
				t.Errorf("%s got the import copy:\n%s", reason, detail)
			}
		})
	}
}

// A wait the provider cannot make sense of must never be treated as known — and
// above all must never come back NEGATIVE, which is less than the remaining
// budget by construction and so walks straight past the deadline guard. Values
// at the conversion boundary are implementation-defined (arm64 saturates to
// MaxInt64, amd64 lands on MinInt64), so this pins the OUTCOME rather than the
// arithmetic: whatever the platform does with it, the wait is not believed.
func TestNoWaitOutsideAPlausibleLockIsEverBelieved(t *testing.T) {
	for _, seconds := range []float64{9.3e9, 1e10, 1.8e12, 1e300, maxImportLockWait.Seconds() + 1} {
		refusal := &client.APIError{
			StatusCode: http.StatusConflict, Code: "invalid_state",
			Details: map[string]any{"reason": "import_running", "retryAfterSeconds": seconds},
		}
		if wait, known := importLockWait(refusal); known {
			t.Errorf("retryAfterSeconds %g was believed as a wait of %s", seconds, wait)
		}
	}
	// The ceiling is a ceiling, not a smaller budget: a long but credible lock is
	// still a wait.
	credible := &client.APIError{
		StatusCode: http.StatusConflict, Code: "invalid_state",
		Details: map[string]any{"reason": "import_running", "retryAfterSeconds": maxImportLockWait.Seconds() - 1},
	}
	if _, known := importLockWait(credible); !known {
		t.Error("a wait just inside the ceiling must still be believed")
	}
}

// The bound is the whole budget, not the budget per refusal. Repeated refusals
// each carrying a wait that individually fits must still stop when the destroy
// has spent its patience — the case the loop actually exists for, and the one a
// single-shot oversized wait never exercises.
func TestRepeatedRefusalsStillStopAtTheBudget(t *testing.T) {
	server, calls := deleteServer(t, refuse(deleteRefusal("import_running", 3900)))
	defer server.Close()

	start := time.Now()
	deleteResp := runDeleteWith(t, context.Background(),
		deleteResource(t, server, 2*time.Second, 300*time.Millisecond))

	if !deleteResp.Diagnostics.HasError() {
		t.Fatal("an import that never lets go must eventually fail the destroy")
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Errorf("took %s — the destroy outlived its 2s budget", elapsed)
	}
	// One request per 300ms retry over a 2s budget — nowhere near the thousands an
	// unbounded loop would make, and not the single one a fit check would.
	if *calls < 2 || *calls > 12 {
		t.Errorf("made %d delete requests over a 2s budget at a 300ms interval, want ~7", *calls)
	}
}

// Ctrl-C during the wait must not surface a bare "context canceled": the one
// fact that tells a practitioner to simply re-run is that the destroy was
// part-way through a wait the platform itself had quoted.
func TestCancellingTheWaitSaysWhatItWasWaitingFor(t *testing.T) {
	server, _ := deleteServer(t, refuse(deleteRefusal("import_running", 10)))
	defer server.Close()

	// A poll interval long enough that the cancellation lands in the WAIT rather
	// than in the HTTP request — the branch this test exists for.
	r := deleteResource(t, server, 30*time.Second, 2*time.Second)

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	err := r.deleteImage(ctx, "img-wedged")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation must survive the wrap, got: %v", err)
	}
	if !strings.Contains(err.Error(), "image import") {
		t.Errorf("a cancelled destroy does not say what it was waiting for: %v", err)
	}
}

// The predicate that decides whether a destroy waits. It keys on
// `details.reason`, because status+code alone cover all three delete refusals,
// and it treats a MISSING retryAfterSeconds as "no wait clears this" rather than
// as a zero wait — the distinction compute encodes by omitting the field.
func TestOnlyTheImportLockRefusalIsWaited(t *testing.T) {
	cases := map[string]struct {
		err       error
		refusal   bool
		wantWait  time.Duration
		waitKnown bool
	}{
		// A fractional wait must survive as the fraction it is. Judging the value
		// before converting it truncates 0.5 to a ZERO duration reported as
		// KNOWN, and deleteImage would then re-issue DELETE with no pause at all
		// until the whole budget was gone.
		"a sub-second wait": {&client.APIError{
			StatusCode: http.StatusConflict, Code: "invalid_state",
			Details: map[string]any{"reason": "import_running", "retryAfterSeconds": float64(0.5)},
		}, true, 500 * time.Millisecond, true},
		// A remaining-seconds delta sent as an absolute timestamp is the classic
		// regression this ceiling exists for. Epoch seconds converts cleanly to
		// ~57 years, which the practitioner would be told to wait out; epoch
		// millis overflows int64 nanoseconds, and a NEGATIVE wait is less than
		// the remaining budget by construction, so the deadline guard would never
		// fire and the destroy would never return.
		"a wait sent as epoch seconds": {&client.APIError{
			StatusCode: http.StatusConflict, Code: "invalid_state",
			Details: map[string]any{"reason": "import_running", "retryAfterSeconds": float64(1.8e9)},
		}, true, 0, false},
		"a wait sent as epoch millis": {&client.APIError{
			StatusCode: http.StatusConflict, Code: "invalid_state",
			Details: map[string]any{"reason": "import_running", "retryAfterSeconds": float64(1.8e12)},
		}, true, 0, false},
		"a wait beyond any plausible lock": {&client.APIError{
			StatusCode: http.StatusConflict, Code: "invalid_state",
			Details: map[string]any{"reason": "import_running", "retryAfterSeconds": float64(1e300)},
		}, true, 0, false},
		"a non-numeric wait": {&client.APIError{
			StatusCode: http.StatusConflict, Code: "invalid_state",
			Details: map[string]any{"reason": "import_running", "retryAfterSeconds": "3900"},
		}, true, 0, false},
		"the import lock with a known wait": {&client.APIError{
			StatusCode: http.StatusConflict, Code: "invalid_state",
			Details: map[string]any{"reason": "import_running", "retryAfterSeconds": float64(3900)},
		}, true, 65 * time.Minute, true},
		"the import lock with no wait": {&client.APIError{
			StatusCode: http.StatusConflict, Code: "invalid_state",
			Details: map[string]any{"reason": "import_running"},
		}, true, 0, false},
		"the import lock with a zero wait": {&client.APIError{
			StatusCode: http.StatusConflict, Code: "invalid_state",
			Details: map[string]any{"reason": "import_running", "retryAfterSeconds": float64(0)},
		}, true, 0, false},
		"a protected image": {&client.APIError{
			StatusCode: http.StatusConflict, Code: "invalid_state",
			Details: map[string]any{"reason": "protected", "retryAfterSeconds": float64(3900)},
		}, false, 0, false},
		"an undeletable status": {&client.APIError{
			StatusCode: http.StatusConflict, Code: "invalid_state",
			Details: map[string]any{"reason": "undeletable_status"},
		}, false, 0, false},
		"a plain conflict": {&client.APIError{
			StatusCode: http.StatusConflict, Code: "conflict",
			Details: map[string]any{"reason": "import_running", "retryAfterSeconds": float64(3900)},
		}, false, 0, false},
		"the same reason on a 429": {&client.APIError{
			StatusCode: http.StatusTooManyRequests, Code: "invalid_state",
			Details: map[string]any{"reason": "import_running", "retryAfterSeconds": float64(3900)},
		}, false, 0, false},
		"a refusal with no details": {&client.APIError{
			StatusCode: http.StatusConflict, Code: "invalid_state",
		}, false, 0, false},
		"a transport error": {errors.New("connection reset"), false, 0, false},
		"no error":          {nil, false, 0, false},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := importLockRefusal(tc.err) != nil; got != tc.refusal {
				t.Errorf("importLockRefusal(%v) != nil = %v, want %v", tc.err, got, tc.refusal)
			}
			wait, known := importLockWait(tc.err)
			if known != tc.waitKnown {
				t.Errorf("importLockWait(%v) known = %v, want %v", tc.err, known, tc.waitKnown)
			}
			if wait != tc.wantWait {
				t.Errorf("importLockWait(%v) = %s, want %s", tc.err, wait, tc.wantWait)
			}
		})
	}
}

// TestReadRemovesResourceOn404 covers an image deleted outside Terraform: the
// refresh must drop it from state so the next apply recreates it.
func TestReadRemovesResourceOn404(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]string{"code": "not_found", "message": "image not found"})
	}))
	defer server.Close()

	c := newTestImageClient(server)
	r := newTestImageResource(c, server.Client())

	ctx := context.Background()
	s := resourceSchema(t)

	stateVal := stateValue(t, s, "img-gone")
	readResp := &resource.ReadResponse{State: tfsdk.State{Schema: s, Raw: stateVal}}
	r.Read(ctx, resource.ReadRequest{State: tfsdk.State{Schema: s, Raw: stateVal}}, readResp)

	if readResp.Diagnostics.HasError() {
		t.Fatalf("Read failed: %v", readResp.Diagnostics.Errors())
	}
	if !readResp.State.Raw.IsNull() {
		t.Error("expected the resource to be removed from state on a 404")
	}
}

// --- diagnostics -------------------------------------------------------------

// TestImageErrorDetailSeparatesTheTwoQuotas asserts the two quota_exceeded
// refusals name different things to remove. They share a code and differ only
// by HTTP status; a 403 is the staging limit (uploads in flight), a 409 the
// tenant's image allowance (images owned). Naming the wrong one sends a
// customer to clear something that was never the constraint.
//
// NEITHER may read as "wait and it clears". The 403's first check counts the
// tenant's `queued` images and a failed import leaves the image queued, so
// abandoned attempts hold their slot until deleted — the reason this test
// pinned "apply again" on the 403 arm until 2026-08-11, and why it no longer
// does.
func TestImageErrorDetailSeparatesTheTwoQuotas(t *testing.T) {
	staging := &client.APIError{Code: "quota_exceeded", Message: "too many images awaiting upload", StatusCode: http.StatusForbidden}
	allowance := &client.APIError{Code: "quota_exceeded", Message: "custom image quota exceeded", StatusCode: http.StatusConflict}

	stagingDetail := imageErrorDetail(staging)
	if !strings.Contains(stagingDetail, "too many images awaiting upload") {
		t.Errorf("the server's own message must survive, got: %s", stagingDetail)
	}
	if !strings.Contains(stagingDetail, "does NOT") || !strings.Contains(stagingDetail, "delete") {
		t.Errorf("a 403 staging refusal is not self-clearing and must say what to remove, got: %s", stagingDetail)
	}

	allowanceDetail := imageErrorDetail(allowance)
	if !strings.Contains(allowanceDetail, "custom image quota exceeded") {
		t.Errorf("the server's own message must survive, got: %s", allowanceDetail)
	}
	if !strings.Contains(allowanceDetail, "does NOT") || !strings.Contains(allowanceDetail, "delete an existing") {
		t.Errorf("a 409 allowance refusal is permanent and must NOT read as retryable, got: %s", allowanceDetail)
	}
	// The advice "delete an existing custom image" is itself refusable: that
	// delete answers 409 resource_in_use while instances built from the image
	// exist (compute v2.32.5). Advice a customer can follow and still get
	// nowhere is the same defect in a different place.
	if !strings.Contains(allowanceDetail, "resource_in_use") {
		t.Errorf("the allowance advice must warn that the delete can itself be refused, got: %s", allowanceDetail)
	}

	// Anything else is passed through untouched.
	plain := &client.APIError{Code: "not_found", Message: "image not found", StatusCode: http.StatusNotFound}
	if got := imageErrorDetail(plain); got != plain.Error() {
		t.Errorf("a non-quota error must pass through unchanged, got: %s", got)
	}
}

// --- model -------------------------------------------------------------------

// TestFromAPIPreservesCreateOnlyAttrs verifies fromAPI never overwrites the
// write-only source_file / source_file_hash or the create-only format and OS
// attributes the API does not round-trip. Overwriting them is how a create earns
// Terraform's "provider produced inconsistent result after apply".
func TestFromAPIPreservesCreateOnlyAttrs(t *testing.T) {
	m := &ImageModel{
		SourceFile:      types.StringValue("/local/ubuntu.qcow2"),
		SourceFileHash:  types.StringValue("deadbeef"),
		DiskFormat:      types.StringValue("qcow2"),
		ContainerFormat: types.StringValue("bare"),
		OSDistro:        types.StringValue("ubuntu"),
		OSVersion:       types.StringValue("24.04"),
		Architecture:    types.StringValue("x86_64"),
	}

	m.fromAPI(&apiImage{
		ID: "img-1", Name: "custom-ubuntu", Status: "active", Visibility: "private",
		// The API echoes formats and OS properties back; they must be ignored.
		DiskFormat: "raw", ContainerFormat: "ovf", OSDistro: "debian", OSVersion: "13", Architecture: "aarch64",
		Size: 10, VirtualSize: 20, MinDisk: 8, MinRAM: 2048, Owner: "proj-1", CreatedAt: "2026-08-07T10:00:00Z",
	})

	for _, tc := range []struct{ name, got, want string }{
		{"source_file", m.SourceFile.ValueString(), "/local/ubuntu.qcow2"},
		{"source_file_hash", m.SourceFileHash.ValueString(), "deadbeef"},
		{"disk_format", m.DiskFormat.ValueString(), "qcow2"},
		{"container_format", m.ContainerFormat.ValueString(), "bare"},
		{"os_distro", m.OSDistro.ValueString(), "ubuntu"},
		{"os_version", m.OSVersion.ValueString(), "24.04"},
		{"architecture", m.Architecture.ValueString(), "x86_64"},
	} {
		if tc.got != tc.want {
			t.Errorf("%s was overwritten: %q, want %q", tc.name, tc.got, tc.want)
		}
	}

	if m.ID.ValueString() != "img-1" || m.Status.ValueString() != "active" {
		t.Errorf("server-owned attributes were not refreshed: %+v", m)
	}
	if m.MinDiskGB.ValueInt64() != 8 || m.MinRAMMB.ValueInt64() != 2048 {
		t.Errorf("min_disk_gb/min_ram_mb not refreshed: %+v", m)
	}
	// An absent description must be null: it is Optional and NOT Computed, so a
	// "" would stop matching a configuration that omitted it.
	if !m.Description.IsNull() {
		t.Errorf("an empty description must be null, got %v", m.Description)
	}
	// checksum is the opposite case, and deliberately so. It is Computed and can
	// legitimately be empty before the image goes active; leaving it NULL would
	// make the framework re-mark it unknown on the next plan
	// (MarkComputedNilsAsUnknown) while UseStateForUnknown bails on a null state
	// value — rendering `checksum = (known after apply)` and calling Update on
	// every plan, forever, for a value nothing changed.
	if m.Checksum.IsNull() {
		t.Error("an empty checksum must be \"\", not null, or every subsequent plan shows a phantom diff")
	}
	if m.Checksum.ValueString() != "" {
		t.Errorf("checksum = %q, want the empty string", m.Checksum.ValueString())
	}
}

// --- the remaining orphan paths ----------------------------------------------

// orphanCase describes one failure that happens AFTER the image record exists.
// Each must leave the id in state: the image is real, it holds part of the
// tenant's custom-image allowance, and an id that never lands means the
// practitioner owns something `terraform destroy` cannot reach.
type orphanCase struct {
	name    string
	imageID string
	// handle serves everything after the create, which the harness below
	// answers uniformly.
	handle func(t *testing.T, w http.ResponseWriter, req *http.Request)
}

// TestCreateFailuresAfterCreatePersistID covers the three post-create failures
// the earlier tests did not: the upload being rejected by the storage edge, the
// import refusing to start, and the wait giving up. The size-limit and
// missing-form paths are covered by their own tests above.
func TestCreateFailuresAfterCreatePersistID(t *testing.T) {
	cases := []orphanCase{
		{
			name:    "upload rejected by the storage edge",
			imageID: "img-upload-rejected",
			handle: func(_ *testing.T, w http.ResponseWriter, req *http.Request) {
				if req.URL.Path == "/staging" {
					w.WriteHeader(http.StatusForbidden)
					_, _ = w.Write([]byte("<Error><Code>AccessDenied</Code></Error>"))
					return
				}
				w.WriteHeader(http.StatusNotFound)
			},
		},
		{
			name:    "import refuses to start",
			imageID: "img-import-refused",
			handle: func(_ *testing.T, w http.ResponseWriter, req *http.Request) {
				switch req.URL.Path {
				case "/staging":
					w.WriteHeader(http.StatusNoContent)
				case "/v1/tenants/tenant-456/images/img-import-refused/import":
					w.WriteHeader(http.StatusServiceUnavailable)
					_ = json.NewEncoder(w).Encode(map[string]string{
						"code": "internal_error", "message": "image upload is not available in this environment",
					})
				default:
					w.WriteHeader(http.StatusNotFound)
				}
			},
		},
		{
			name:    "wait times out with the image still importing",
			imageID: "img-timeout",
			handle: func(_ *testing.T, w http.ResponseWriter, req *http.Request) {
				switch req.URL.Path {
				case "/staging":
					w.WriteHeader(http.StatusNoContent)
				case "/v1/tenants/tenant-456/images/img-timeout/import":
					w.WriteHeader(http.StatusAccepted)
					_ = json.NewEncoder(w).Encode(apiImage{ID: "img-timeout", Status: "importing"})
				case "/v1/tenants/tenant-456/images/img-timeout":
					// Never reaches a terminal state — the poll timeout wins.
					_ = json.NewEncoder(w).Encode(apiImage{ID: "img-timeout", Status: "importing"})
				default:
					w.WriteHeader(http.StatusNotFound)
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			source := writeImageFile(t, 128)

			mux := http.NewServeMux()
			mux.HandleFunc("/", func(w http.ResponseWriter, req *http.Request) {
				if req.Method == http.MethodPost && req.URL.Path == "/v1/tenants/tenant-456/images" {
					w.WriteHeader(http.StatusCreated)
					_ = json.NewEncoder(w).Encode(apiCreateImageResponse{
						apiImage: apiImage{ID: tc.imageID, Name: "custom-ubuntu", Status: "queued", Visibility: "private"},
						Upload: &apiImageUpload{
							URL:      "http://" + req.Host + "/staging",
							Fields:   map[string]string{"key": "staging/" + tc.imageID},
							MaxBytes: 1 << 30,
						},
					})
					return
				}
				tc.handle(t, w, req)
			})
			server := httptest.NewServer(mux)
			defer server.Close()

			c := newTestImageClient(server)
			r := newTestImageResource(c, server.Client())
			r.pollTimeout = 150 * time.Millisecond // only the timeout case reaches this

			createResp := runCreate(t, r, source)
			if !createResp.Diagnostics.HasError() {
				t.Fatal("expected the apply to fail")
			}

			var state ImageModel
			createResp.State.Get(context.Background(), &state)
			if state.ID.ValueString() != tc.imageID {
				t.Fatalf("id = %q, want %q — the image exists server-side and MUST be in state, "+
					"or Terraform orphans it", state.ID.ValueString(), tc.imageID)
			}
			// Every one of these leaves a real image behind, so the diagnostic has
			// to say so and name it.
			detail := createResp.Diagnostics.Errors()[0].Detail()
			if !strings.Contains(detail, tc.imageID) || !strings.Contains(detail, "terraform destroy") {
				t.Errorf("the diagnostic must name the surviving image and how to remove it, got: %s", detail)
			}
		})
	}
}

// TestWaitForImportTerminatesOn404 covers an image deleted out of band while the
// import is running. client.WaitForState retries every PollFunc error until the
// deadline, so a 404 returned as an error would hold the apply open for the full
// poll timeout and then report a generic deadline instead of what happened. It
// must be mapped onto a terminal state inside PollFunc.
func TestWaitForImportTerminatesOn404(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]string{"code": "not_found", "message": "image not found"})
	}))
	defer server.Close()

	c := newTestImageClient(server)
	r := newTestImageResource(c, server.Client())
	r.pollTimeout = time.Minute // long enough that a hang is a hang, not a timeout

	done := make(chan error, 1)
	go func() {
		_, err := r.waitForImport(context.Background(), "img-vanished")
		done <- err
	}()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected an error when the image 404s mid-import")
		}
		if !strings.Contains(err.Error(), "no longer exists") {
			t.Errorf("the error should say the image is gone, got: %v", err)
		}
		// The image does NOT exist, so this one must NOT carry the orphan warning.
		if strings.Contains(err.Error(), "terraform destroy") {
			t.Errorf("a vanished image must not be reported as an orphan to destroy, got: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("waitForImport did not terminate on a 404 — a mid-import delete hangs the apply " +
			"for the whole poll timeout")
	}
}

// TestWaitForImportTerminatesOnOutOfBandStatuses covers the three terminal
// statuses beyond active/killed that compute's own isTerminalImport recognises.
// Each is reachable by an operator or another client acting on the image
// mid-import; a wait that did not list them would poll until timeout.
func TestWaitForImportTerminatesOnOutOfBandStatuses(t *testing.T) {
	for _, status := range []string{"deleted", "pending_delete", "deactivated"} {
		t.Run(status, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_ = json.NewEncoder(w).Encode(apiImage{ID: "img-oob", Status: status})
			}))
			defer server.Close()

			c := newTestImageClient(server)
			r := newTestImageResource(c, server.Client())
			r.pollTimeout = time.Minute

			done := make(chan error, 1)
			go func() {
				_, err := r.waitForImport(context.Background(), "img-oob")
				done <- err
			}()

			select {
			case err := <-done:
				if err == nil {
					t.Fatalf("expected an error for terminal status %q", status)
				}
			case <-time.After(10 * time.Second):
				t.Fatalf("waitForImport did not terminate on status %q", status)
			}
		})
	}
}

// --- read-back guards --------------------------------------------------------

// TestCreateKeepsPlannedDescriptionAndMinDisk is the regression guard for the
// two attributes the server may legitimately hand back differently.
//
// description round-trips as empty on a compute predating PR #582, and
// min_disk_gb is raised by compute's reclaim watcher (reassertMinDisk) in a
// goroutine racing this poll. Adopting either would make the applied value
// differ from the configuration — Terraform's "provider produced inconsistent
// result after apply", which is a HARD failure, not a diff. The plan value must
// win.
func TestCreateKeepsPlannedDescriptionAndMinDisk(t *testing.T) {
	source := writeImageFile(t, 128)

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, req *http.Request) {
		switch {
		case req.Method == http.MethodPost && req.URL.Path == "/v1/tenants/tenant-456/images":
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(apiCreateImageResponse{
				apiImage: apiImage{ID: "img-rb", Name: "custom-ubuntu", Status: "queued", Visibility: "private"},
				Upload: &apiImageUpload{
					URL:      "http://" + req.Host + "/staging",
					Fields:   map[string]string{"key": "staging/img-rb"},
					MaxBytes: 1 << 30,
				},
			})
		case req.URL.Path == "/staging":
			w.WriteHeader(http.StatusNoContent)
		case req.URL.Path == "/v1/tenants/tenant-456/images/img-rb/import":
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(apiImage{ID: "img-rb", Status: "importing"})
		case req.Method == http.MethodGet && req.URL.Path == "/v1/tenants/tenant-456/images/img-rb":
			// The hostile read-back: description dropped entirely (pre-#582
			// compute) and min_disk raised by the reclaim watcher.
			_ = json.NewEncoder(w).Encode(apiImage{
				ID: "img-rb", Name: "custom-ubuntu", Status: "active", Visibility: "private",
				Description: "", MinDisk: 40,
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	c := newTestImageClient(server)
	r := newTestImageResource(c, server.Client())

	ctx := context.Background()
	s := resourceSchema(t)
	// A plan that SETS both attributes — the case that hard-fails without the guard.
	attrs := createPlanAttrs(source)
	attrs["description"] = tftypes.NewValue(tftypes.String, "hardened base image")
	attrs["min_disk_gb"] = tftypes.NewValue(tftypes.Number, 20)
	planVal := objectValue(s, attrs)

	createResp := &resource.CreateResponse{State: tfsdk.State{Schema: s}}
	r.Create(ctx, resource.CreateRequest{Plan: tfsdk.Plan{Schema: s, Raw: planVal}}, createResp)
	if createResp.Diagnostics.HasError() {
		t.Fatalf("Create failed: %v", createResp.Diagnostics.Errors())
	}

	var state ImageModel
	createResp.State.Get(ctx, &state)
	if state.Description.ValueString() != "hardened base image" {
		t.Errorf("description = %q, want the configured value — adopting the server's empty echo is a "+
			"hard inconsistent-result error, not a diff", state.Description.ValueString())
	}
	if state.MinDiskGB.ValueInt64() != 20 {
		t.Errorf("min_disk_gb = %d, want the configured 20 — the reclaim watcher's 40 must not be adopted "+
			"during create, or the apply flakes on a goroutine race", state.MinDiskGB.ValueInt64())
	}
	// The genuinely server-owned attributes still come from the read-back.
	if state.Status.ValueString() != "active" {
		t.Errorf("status = %q, want active", state.Status.ValueString())
	}
}

// --- update ------------------------------------------------------------------

// TestUpdateSendsOnlyChangedFields asserts the PUT body carries exactly the keys
// that changed and nothing else. The absence of an Update test is how the
// description round-trip bug reached review, so this one checks both the request
// and the resulting state.
func TestUpdateSendsOnlyChangedFields(t *testing.T) {
	var body map[string]any
	var puts int

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Method != http.MethodPut || req.URL.Path != "/v1/tenants/tenant-456/images/img-1" {
			t.Errorf("unexpected request %s %s", req.Method, req.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		puts++
		_ = json.NewDecoder(req.Body).Decode(&body)
		_ = json.NewEncoder(w).Encode(apiImage{
			ID: "img-1", Name: "renamed", Description: "now described", Status: "active",
			Visibility: "private", Owner: "proj-1", MinDisk: 0, MinRAM: 0,
		})
	}))
	defer server.Close()

	c := newTestImageClient(server)
	r := newTestImageResource(c, server.Client())

	ctx := context.Background()
	s := resourceSchema(t)

	stateVal := stateValue(t, s, "img-1") // name "custom-ubuntu", description null

	// Change exactly two things: the name and the description.
	attrs := stateAttrs("img-1")
	attrs["name"] = tftypes.NewValue(tftypes.String, "renamed")
	attrs["description"] = tftypes.NewValue(tftypes.String, "now described")
	planVal := objectValue(s, attrs)

	updateResp := &resource.UpdateResponse{State: tfsdk.State{Schema: s, Raw: stateVal}}
	r.Update(ctx, resource.UpdateRequest{
		Plan:  tfsdk.Plan{Schema: s, Raw: planVal},
		State: tfsdk.State{Schema: s, Raw: stateVal},
	}, updateResp)

	if updateResp.Diagnostics.HasError() {
		t.Fatalf("Update failed: %v", updateResp.Diagnostics.Errors())
	}
	if puts != 1 {
		t.Fatalf("expected exactly 1 PUT, got %d", puts)
	}
	if body["name"] != "renamed" {
		t.Errorf("name not sent: %v", body["name"])
	}
	if body["description"] != "now described" {
		t.Errorf("description not sent: %v", body["description"])
	}
	// Unchanged fields must be OMITTED, not sent as zero values that would clear
	// them server-side.
	for _, k := range []string{"minDisk", "minRam"} {
		if _, present := body[k]; present {
			t.Errorf("%s was sent despite being unchanged: %v", k, body[k])
		}
	}

	var newState ImageModel
	updateResp.State.Get(ctx, &newState)
	if newState.Name.ValueString() != "renamed" || newState.Description.ValueString() != "now described" {
		t.Errorf("state not refreshed: name=%q description=%q",
			newState.Name.ValueString(), newState.Description.ValueString())
	}
}

// TestUpdateWithNoChangesSkipsTheRequest asserts a plan that changes nothing the
// API owns spends no request at all.
func TestUpdateWithNoChangesSkipsTheRequest(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		t.Errorf("no request should be sent, got %s %s", req.Method, req.URL.Path)
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	c := newTestImageClient(server)
	r := newTestImageResource(c, server.Client())

	ctx := context.Background()
	s := resourceSchema(t)
	stateVal := stateValue(t, s, "img-1")

	updateResp := &resource.UpdateResponse{State: tfsdk.State{Schema: s, Raw: stateVal}}
	r.Update(ctx, resource.UpdateRequest{
		Plan:  tfsdk.Plan{Schema: s, Raw: stateVal},
		State: tfsdk.State{Schema: s, Raw: stateVal},
	}, updateResp)

	if updateResp.Diagnostics.HasError() {
		t.Fatalf("Update failed: %v", updateResp.Diagnostics.Errors())
	}
}

// TestCheckFormNotExpired covers the three shapes of an upload form's expiry. An
// already-expired form is refused before the transfer is spent; an absent or
// unparseable one is deliberately allowed through, because refusing an upload
// the edge would have accepted just because the provider could not read a
// timestamp is the worse failure.
func TestCheckFormNotExpired(t *testing.T) {
	cases := []struct {
		name      string
		expiresAt string
		wantErr   bool
	}{
		{"expired an hour ago", time.Now().Add(-time.Hour).Format(time.RFC3339), true},
		{"valid for another hour", time.Now().Add(time.Hour).Format(time.RFC3339), false},
		{"absent", "", false},
		{"unparseable", "not-a-timestamp", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := checkFormNotExpired(&apiImageUpload{ExpiresAt: tc.expiresAt})
			if tc.wantErr && err == nil {
				t.Error("expected an expired form to be refused before the upload is spent")
			}
			if !tc.wantErr && err != nil {
				t.Errorf("expected the form to be accepted, got: %v", err)
			}
		})
	}
}

// The concurrent-import cap is waited out; every other 429 is not. Both halves
// matter: the gateway's per-key rate limit arrives with the SAME status and has
// already been retried by client.Do by the time it reaches here, so waiting it
// out again would hold an apply open for a limit that is not about imports.
//
// The 409 cases below no longer mean "a 409 is terminal" — deleteImage does wait
// one out (see TestOnlyTheImportLockRefusalIsWaited). They mean this predicate
// must not claim it: the import-lock refusal has its own remedy and its own
// wait, taken from the server rather than from getPollInterval, and routing it
// through startImport's cap loop would poll for an hour on the wrong schedule.
func TestOnlyTheConcurrentImportCapIsWaitedOut(t *testing.T) {
	cases := map[string]struct {
		err  error
		want bool
	}{
		"the import cap": {&client.APIError{
			StatusCode: http.StatusTooManyRequests, Code: "rate_limited",
			Details: map[string]any{"cap": "concurrent_imports", "limit": float64(1), "used": float64(1)},
		}, true},
		"a gateway rate limit": {&client.APIError{
			StatusCode: http.StatusTooManyRequests, Code: "rate_limited",
		}, false},
		"the mint rate cap": {&client.APIError{
			StatusCode: http.StatusTooManyRequests, Code: "rate_limited",
			Details: map[string]any{"cap": "mint_rate"},
		}, false},
		"a conflict": {&client.APIError{
			StatusCode: http.StatusConflict, Code: "invalid_state",
			Details: map[string]any{"cap": "concurrent_imports"},
		}, false},
		"the import-lock refusal": {&client.APIError{
			StatusCode: http.StatusConflict, Code: "invalid_state",
			Details: map[string]any{"reason": "import_running", "retryAfterSeconds": float64(3900)},
		}, false},
		"a transport error": {errors.New("connection reset"), false},
		"no error":          {nil, false},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := isConcurrentImportRefusal(tc.err); got != tc.want {
				t.Errorf("isConcurrentImportRefusal(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

// Waiters that started together must not poll in lockstep: one slot frees, they
// all wake in the same window, one wins and the rest are refused again — tighter
// synchronised each round. Different images must therefore wait different times,
// and the same image must be reproducible across runs.
func TestJitterSeparatesWaitersDeterministically(t *testing.T) {
	const interval = 10 * time.Second
	a := jitter(interval, "6f1e2d3c-4b5a-4c9d-8e7f-0a1b2c3d4e5f")
	b := jitter(interval, "7a2b3c4d-5e6f-4a8b-9c0d-1e2f3a4b5c6d")

	if a == b {
		t.Errorf("two images wait the same %s — they will keep colliding", a)
	}
	if got := jitter(interval, "6f1e2d3c-4b5a-4c9d-8e7f-0a1b2c3d4e5f"); got != a {
		t.Errorf("jitter is not reproducible for one image: %s then %s", a, got)
	}
	for _, w := range []time.Duration{a, b} {
		if w < interval || w >= interval*3/2 {
			t.Errorf("wait %s is outside [%s, %s) — the interval is no longer the floor", w, interval, interval*3/2)
		}
	}
}

// --- tenant scoping ----------------------------------------------------------

// TestEveryImageCallIsTenantScoped is the regression guard for the whole
// resource: every request the provider makes must name the tenant it is
// CONFIGURED for.
//
// The api-gateway's EffectiveTenant middleware re-scopes and re-signs the
// auth-context JWT only for paths matching /tenants/{id}/. On a bare /v1/images
// path the signed context keeps naming the caller's HOME tenant, so compute
// talks to Glance in the wrong OpenStack project — silently, with a 200. It
// bites the OIDC / fm CLI session path, where the configured tenant and the home
// tenant genuinely differ; an API key is bound to a single tenant, so the home
// context happens to be right for it and hides the bug.
//
// The client here is Configured against a /v1/me that reports a DIFFERENT home
// tenant, then pointed at testTenantID the way the provider's tenant_id
// selector does. The mux serves only testTenantID's routes, so any call that
// slipped back to the bare path (or to the home tenant) is recorded and fails
// the test rather than quietly succeeding.
//
// The create flow is covered end to end on purpose: create, mint, import and
// poll must move together, or an apply creates the image in one tenant and polls
// for it in another.
func TestEveryImageCallIsTenantScoped(t *testing.T) {
	const homeTenant = "tenant-home"
	source := writeImageFile(t, 256)

	var mu sync.Mutex
	var gatewayPaths []string

	tenantPrefix := "/v1/tenants/" + testTenantID + "/images"

	// The mock answers the image routes at ANY path shape — it matches on the
	// "/images..." SUFFIX, so a bare /v1/images call is served exactly as
	// happily as a tenant-scoped one. That is the point: in production the
	// gateway answers the bare path with a 200 too, just against the wrong
	// tenant, so a mock that 404'd the bare path would prove the fix only by
	// accident. The recorded paths, asserted at the end, are the whole test.
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, req *http.Request) {
		p := req.URL.Path
		if p != "/staging" {
			mu.Lock()
			gatewayPaths = append(gatewayPaths, req.Method+" "+p)
			mu.Unlock()
		}
		route := p
		if i := strings.Index(p, "/images"); i >= 0 {
			route = p[i:]
		}
		switch {
		case req.Method == http.MethodGet && p == "/v1/me":
			_ = json.NewEncoder(w).Encode(client.UserProfile{ID: "user-1", TenantID: homeTenant})

		// Answered WITHOUT an upload form, so the create flow also has to mint
		// one — that POST is part of the flow and must be tenant-scoped too.
		case req.Method == http.MethodPost && route == "/images":
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(apiCreateImageResponse{
				apiImage: apiImage{ID: "img-1", Name: "custom-ubuntu", Status: "queued", Visibility: "private", Owner: "proj-1"},
			})

		case req.Method == http.MethodPost && route == "/images/img-1/upload":
			_ = json.NewEncoder(w).Encode(apiImageUpload{
				URL:      "http://" + req.Host + "/staging",
				Fields:   map[string]string{"key": "staging/img-1"},
				MaxBytes: 1 << 30,
			})

		case req.Method == http.MethodPost && p == "/staging":
			w.WriteHeader(http.StatusNoContent)

		case req.Method == http.MethodPost && route == "/images/img-1/import":
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(apiImage{ID: "img-1", Status: "importing"})

		case req.Method == http.MethodGet && route == "/images/img-1":
			_ = json.NewEncoder(w).Encode(apiImage{
				ID: "img-1", Name: "custom-ubuntu", Status: "active", Visibility: "private",
				Owner: "proj-1", Size: 256, VirtualSize: 4096, Checksum: "abc123", CreatedAt: "2026-08-07T10:00:00Z",
			})

		case req.Method == http.MethodPut && route == "/images/img-1":
			_ = json.NewEncoder(w).Encode(apiImage{
				ID: "img-1", Name: "renamed", Status: "active", Visibility: "private",
				Owner: "proj-1", CreatedAt: "2026-08-07T10:00:00Z",
			})

		case req.Method == http.MethodDelete && route == "/images/img-1":
			w.WriteHeader(http.StatusNoContent)

		default:
			t.Errorf("unexpected request %s %s", req.Method, p)
			w.WriteHeader(http.StatusNotFound)
		}
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	c := client.NewClient(server.URL, "test-key", client.WithHTTPClient(server.Client())) // pragma: allowlist secret
	if err := c.Configure(context.Background()); err != nil {
		t.Fatalf("configure failed: %v", err)
	}
	if c.TenantID() != homeTenant {
		t.Fatalf("home tenant = %q, want %q", c.TenantID(), homeTenant)
	}
	// What the provider's tenant_id selector does: operate on a tenant that is
	// NOT the caller's home tenant.
	c.SetTenantIDForTest(testTenantID)

	r := newTestImageResource(c, server.Client())
	ctx := context.Background()
	s := resourceSchema(t)

	createResp := runCreate(t, r, source)
	if createResp.Diagnostics.HasError() {
		t.Fatalf("Create failed: %v", createResp.Diagnostics.Errors())
	}

	stateVal := stateValue(t, s, "img-1")

	readResp := &resource.ReadResponse{State: tfsdk.State{Schema: s, Raw: stateVal}}
	r.Read(ctx, resource.ReadRequest{State: tfsdk.State{Schema: s, Raw: stateVal}}, readResp)
	if readResp.Diagnostics.HasError() {
		t.Fatalf("Read failed: %v", readResp.Diagnostics.Errors())
	}

	renamed := stateAttrs("img-1")
	renamed["name"] = tftypes.NewValue(tftypes.String, "renamed")
	updateResp := &resource.UpdateResponse{State: tfsdk.State{Schema: s, Raw: stateVal}}
	r.Update(ctx, resource.UpdateRequest{
		Plan:  tfsdk.Plan{Schema: s, Raw: objectValue(s, renamed)},
		State: tfsdk.State{Schema: s, Raw: stateVal},
	}, updateResp)
	if updateResp.Diagnostics.HasError() {
		t.Fatalf("Update failed: %v", updateResp.Diagnostics.Errors())
	}

	deleteResp := &resource.DeleteResponse{State: tfsdk.State{Schema: s, Raw: stateVal}}
	r.Delete(ctx, resource.DeleteRequest{State: tfsdk.State{Schema: s, Raw: stateVal}}, deleteResp)
	if deleteResp.Diagnostics.HasError() {
		t.Fatalf("Delete failed: %v", deleteResp.Diagnostics.Errors())
	}

	mu.Lock()
	defer mu.Unlock()

	for _, call := range gatewayPaths {
		p := strings.SplitN(call, " ", 2)[1]
		if p == "/v1/me" {
			continue
		}
		if !strings.HasPrefix(p, tenantPrefix) {
			t.Errorf("image call %q does not name the configured tenant. The gateway re-scopes the "+
				"signed auth context only on a /tenants/{id}/ path, so this one acted on the caller's "+
				"home tenant %q instead of %q", call, homeTenant, testTenantID)
		}
	}

	// Every step of the flow has to be present — a missing one would mean the
	// loop above passed only because the call was never made.
	for _, want := range []string{
		http.MethodPost + " " + tenantPrefix,                   // create
		http.MethodPost + " " + tenantPrefix + "/img-1/upload", // mint upload form
		http.MethodPost + " " + tenantPrefix + "/img-1/import", // import
		http.MethodGet + " " + tenantPrefix + "/img-1",         // poll + read
		http.MethodPut + " " + tenantPrefix + "/img-1",         // update
		http.MethodDelete + " " + tenantPrefix + "/img-1",      // delete
	} {
		if !slices.Contains(gatewayPaths, want) {
			t.Errorf("no %q request was made; calls were %v", want, gatewayPaths)
		}
	}
}

// --- dot-segment ids ---------------------------------------------------------

// TestDotSegmentImageIDsAreRefusedNotEscaped is a REGRESSION test for a
// destructive shape, not a hypothetical one.
//
// 🔴 url.PathEscape does NOT protect a path segment from "." or "..": they are
// unreserved characters, so there is nothing for it to escape — and client.do
// finishes the URL with path.Join, which CLEANS dot segments. While the image
// routes were the untenanted /v1/images/{id} that was harmless, because
// "/v1/images/../instances/x" collapsed to "/v1/instances/x" and the api-gateway
// deliberately routes nothing there. Under /v1/tenants/{tenant}/images/{id} the
// same collapse lands inside the practitioner's OWN tenant namespace, where
// every other resource lives. Measured against the unguarded code:
//
//	Delete with id "../instances/i-1" -> DELETE /v1/tenants/T/instances/i-1
//
// which is a registered DELETE route, so it does not 404: destroying a
// frostmoln_image destroys an instance instead. The gateway's
// PathCanonicalization middleware would answer 400 on a literal dot segment —
// it is this client's own path.Join that defeats it, so the refusal has to
// happen provider-side.
//
// The assertion is "NO REQUEST WAS ISSUED", not merely "a diagnostic came back":
// a server-side rejection would satisfy an error-only check while still putting
// the destructive request on the wire.
func TestDotSegmentImageIDsAreRefusedNotEscaped(t *testing.T) {
	var (
		mu               sync.Mutex
		gotMethod, gotAt string
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		mu.Lock()
		gotMethod, gotAt = req.Method, req.URL.Path
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "img-1", "status": "active"})
	}))
	defer server.Close()

	c := newTestImageClient(server)
	r := newTestImageResource(c, server.Client())
	ctx := context.Background()
	s := resourceSchema(t)

	seen := func() (string, string) {
		mu.Lock()
		defer mu.Unlock()
		return gotMethod, gotAt
	}
	reset := func() {
		mu.Lock()
		defer mu.Unlock()
		gotMethod, gotAt = "", ""
	}

	cases := []struct {
		name string
		// call returns an error, or nil when the entry point reports through
		// diagnostics instead.
		call func(bad string) error
	}{
		{"read", func(bad string) error {
			stateVal := stateValue(t, s, bad)
			readResp := &resource.ReadResponse{State: tfsdk.State{Schema: s, Raw: stateVal}}
			r.Read(ctx, resource.ReadRequest{State: tfsdk.State{Schema: s, Raw: stateVal}}, readResp)
			return diagError(readResp.Diagnostics.HasError())
		}},
		{"update", func(bad string) error {
			stateVal := stateValue(t, s, bad)
			// A plan that actually differs, or Update returns before it would
			// build any path and the test would prove nothing.
			planAttrs := stateAttrs(bad)
			planAttrs["name"] = tftypes.NewValue(tftypes.String, "renamed")
			updateResp := &resource.UpdateResponse{State: tfsdk.State{Schema: s, Raw: stateVal}}
			r.Update(ctx, resource.UpdateRequest{
				Plan:  tfsdk.Plan{Schema: s, Raw: objectValue(s, planAttrs)},
				State: tfsdk.State{Schema: s, Raw: stateVal},
			}, updateResp)
			return diagError(updateResp.Diagnostics.HasError())
		}},
		{"delete", func(bad string) error {
			stateVal := stateValue(t, s, bad)
			deleteResp := &resource.DeleteResponse{State: tfsdk.State{Schema: s, Raw: stateVal}}
			r.Delete(ctx, resource.DeleteRequest{State: tfsdk.State{Schema: s, Raw: stateVal}}, deleteResp)
			return diagError(deleteResp.Diagnostics.HasError())
		}},
		{"deleteImage", func(bad string) error { return r.deleteImage(ctx, bad) }},
		{"mint upload form", func(bad string) error {
			created := &apiCreateImageResponse{}
			created.ID = bad
			_, err := r.resolveUploadForm(ctx, created)
			return err
		}},
		{"startImport", func(bad string) error { return r.startImport(ctx, bad) }},
		{"waitForImport", func(bad string) error { _, err := r.waitForImport(ctx, bad); return err }},
	}

	// "." and ".." collapse onto a sibling or the parent; a slash or an escape
	// character builds a different path outright. One guard refuses them all.
	for _, bad := range []string{"..", ".", "../instances/i-1", "../..", "a/b", "a%2Fb", ""} {
		for _, tc := range cases {
			reset()
			err := tc.call(bad)
			method, at := seen()
			if err == nil {
				t.Errorf("image %s with id %q was ACCEPTED and issued %s %s", tc.name, bad, method, at)
				continue
			}
			// The refusal must happen before any request leaves the provider.
			if at != "" {
				t.Errorf("image %s with id %q was refused but had already issued %s %s",
					tc.name, bad, method, at)
			}
		}
	}
}

// diagError turns a diagnostics-reporting entry point's outcome into the error
// the table above compares against, so a framework entry point and a plain
// helper can share one assertion.
func diagError(hasError bool) error {
	if hasError {
		return errors.New("the operation reported an error diagnostic")
	}
	return nil
}
