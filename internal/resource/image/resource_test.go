package image

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
	rschema "github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
)

// --- helpers -----------------------------------------------------------------

func newTestImageResource(c *client.Client, upload *http.Client) *imageResource {
	return &imageResource{
		client:       c,
		uploadClient: upload,
		pollInterval: time.Millisecond,
		pollTimeout:  5 * time.Second,
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
		case req.Method == http.MethodPost && p == "/v1/images":
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
		case req.Method == http.MethodPost && p == "/v1/images/img-1/import":
			calls = append(calls, "import")
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(apiImage{ID: "img-1", Status: "importing"})
		case req.Method == http.MethodGet && p == "/v1/images/img-1":
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

	c := client.NewClient(server.URL, "test-key", client.WithHTTPClient(server.Client())) // pragma: allowlist secret
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
		case req.Method == http.MethodPost && req.URL.Path == "/v1/images":
			// 201 with the image at the top level and NO upload form.
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(apiCreateImageResponse{
				apiImage: apiImage{ID: "img-dark", Name: "custom-ubuntu", Status: "queued", Visibility: "private"},
			})
		case req.Method == http.MethodPost && req.URL.Path == "/v1/images/img-dark/upload":
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

	c := client.NewClient(server.URL, "test-key", client.WithHTTPClient(server.Client())) // pragma: allowlist secret
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
		case req.Method == http.MethodPost && req.URL.Path == "/v1/images":
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

	c := client.NewClient(server.URL, "test-key", client.WithHTTPClient(server.Client())) // pragma: allowlist secret
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

// --- import failure ----------------------------------------------------------

// TestWaitForImportTerminatesOnImportFailed is the trap this resource exists to
// avoid: Glance reverts a FAILED interoperable import to `queued`, not to
// `killed`, so a wait keyed on `active` never terminates. The poll timeout is
// far longer than the test's own patience, so the test fails by hanging if the
// importFailed signal is ever dropped.
func TestWaitForImportTerminatesOnImportFailed(t *testing.T) {
	polls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Method != http.MethodGet || req.URL.Path != "/v1/images/img-fail" {
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

	c := client.NewClient(server.URL, "test-key", client.WithHTTPClient(server.Client())) // pragma: allowlist secret
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

	c := client.NewClient(server.URL, "test-key", client.WithHTTPClient(server.Client())) // pragma: allowlist secret
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

	c := client.NewClient(server.URL, "test-key", client.WithHTTPClient(server.Client())) // pragma: allowlist secret
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

// TestReadRemovesResourceOn404 covers an image deleted outside Terraform: the
// refresh must drop it from state so the next apply recreates it.
func TestReadRemovesResourceOn404(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]string{"code": "not_found", "message": "image not found"})
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key", client.WithHTTPClient(server.Client())) // pragma: allowlist secret
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
// refusals get opposite advice. They share a code and differ only by HTTP
// status; a 403 is the transient staging limit (wait), a 409 is the tenant's
// image allowance (delete something). Rendering the 409 as retryable would send
// a customer into a loop that can never succeed.
func TestImageErrorDetailSeparatesTheTwoQuotas(t *testing.T) {
	staging := &client.APIError{Code: "quota_exceeded", Message: "too many images awaiting upload", StatusCode: http.StatusForbidden}
	allowance := &client.APIError{Code: "quota_exceeded", Message: "custom image quota exceeded", StatusCode: http.StatusConflict}

	stagingDetail := imageErrorDetail(staging)
	if !strings.Contains(stagingDetail, "too many images awaiting upload") {
		t.Errorf("the server's own message must survive, got: %s", stagingDetail)
	}
	if !strings.Contains(stagingDetail, "apply again") {
		t.Errorf("a 403 staging refusal is transient and should say so, got: %s", stagingDetail)
	}

	allowanceDetail := imageErrorDetail(allowance)
	if !strings.Contains(allowanceDetail, "custom image quota exceeded") {
		t.Errorf("the server's own message must survive, got: %s", allowanceDetail)
	}
	if !strings.Contains(allowanceDetail, "does NOT") || !strings.Contains(allowanceDetail, "delete an existing") {
		t.Errorf("a 409 allowance refusal is permanent and must NOT read as retryable, got: %s", allowanceDetail)
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
				case "/v1/images/img-import-refused/import":
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
				case "/v1/images/img-timeout/import":
					w.WriteHeader(http.StatusAccepted)
					_ = json.NewEncoder(w).Encode(apiImage{ID: "img-timeout", Status: "importing"})
				case "/v1/images/img-timeout":
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
				if req.Method == http.MethodPost && req.URL.Path == "/v1/images" {
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

			c := client.NewClient(server.URL, "test-key", client.WithHTTPClient(server.Client())) // pragma: allowlist secret
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

	c := client.NewClient(server.URL, "test-key", client.WithHTTPClient(server.Client())) // pragma: allowlist secret
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

			c := client.NewClient(server.URL, "test-key", client.WithHTTPClient(server.Client())) // pragma: allowlist secret
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
		case req.Method == http.MethodPost && req.URL.Path == "/v1/images":
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
		case req.URL.Path == "/v1/images/img-rb/import":
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(apiImage{ID: "img-rb", Status: "importing"})
		case req.Method == http.MethodGet && req.URL.Path == "/v1/images/img-rb":
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

	c := client.NewClient(server.URL, "test-key", client.WithHTTPClient(server.Client())) // pragma: allowlist secret
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
		if req.Method != http.MethodPut || req.URL.Path != "/v1/images/img-1" {
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

	c := client.NewClient(server.URL, "test-key", client.WithHTTPClient(server.Client())) // pragma: allowlist secret
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

	c := client.NewClient(server.URL, "test-key", client.WithHTTPClient(server.Client())) // pragma: allowlist secret
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
