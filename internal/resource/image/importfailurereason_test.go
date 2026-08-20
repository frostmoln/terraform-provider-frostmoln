package image

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
)

// waitForImportError runs a failing import against a server that answers with
// the given image, and returns the diagnostic the practitioner reads.
func waitForImportError(t *testing.T, img apiImage) string {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(img)
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key", client.WithHTTPClient(server.Client())) // pragma: allowlist secret
	r := newTestImageResource(c, server.Client())
	r.pollTimeout = time.Minute

	done := make(chan error, 1)
	go func() {
		_, err := r.waitForImport(context.Background(), img.ID)
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected an error for a failed import")
		}
		return err.Error()
	case <-time.After(10 * time.Second):
		t.Fatal("waitForImport did not terminate on importFailed")
		return ""
	}
}

// "check that disk_format matches the file" is WRONG advice for the failure this
// exists to surface: a qcow2 whose guest content is itself a qcow2 has the right
// disk_format, and a re-apply re-uploads the whole file to fail identically.
func TestImportFailureDiagnosticNamesTheReason(t *testing.T) {
	msg := waitForImportError(t, apiImage{
		ID: "img-fail", Status: "queued", ImportFailed: true,
		ImportFailedStores: "rbd", ImportFailureReason: importFailureNestedFormat,
	})

	if !strings.Contains(msg, "disk image whose contents are themselves a disk image") {
		t.Errorf("the diagnostic must name the reason, got: %v", msg)
	}
	if !strings.Contains(msg, "Re-applying will fail the same way") {
		t.Errorf("the diagnostic must say a re-apply is not the remedy, got: %v", msg)
	}
	if strings.Contains(msg, "check that disk_format matches the file") {
		t.Errorf("the reason must REPLACE the guess it contradicts, got: %v", msg)
	}
}

// A code from a compute NEWER than this provider must not be rendered as the
// explanation, and must not fall through to the disk_format guess either — that
// guess is wrong by construction for everything the codes cover, which is the
// whole reason the codes exist.
//
// Naming the code as an unrecognised IDENTIFIER is the point, not a leak: it is
// a compute-defined stable code, it tells the practitioner an upgrade will
// explain it, and it is the string they would quote in a bug report.
func TestImportFailureDiagnosticHandlesAnUnknownCodeHonestly(t *testing.T) {
	msg := waitForImportError(t, apiImage{
		ID: "img-fail", Status: "queued", ImportFailed: true,
		ImportFailedStores: "rbd", ImportFailureReason: "someFutureCode",
	})

	if !strings.Contains(msg, "does not recognise") {
		t.Errorf("the diagnostic must SAY the code is unrecognised rather than render it as "+
			"the reason, got: %v", msg)
	}
	if !strings.Contains(msg, "someFutureCode") {
		t.Errorf("the code should be quoted so the practitioner can act on it, got: %v", msg)
	}
	if strings.Contains(msg, "disk_format matches the file") {
		t.Errorf("an unrecognised code re-issued the disk_format guess, which the reason codes "+
			"exist to replace: %v", msg)
	}
}

func TestImportFailureReasonCopy(t *testing.T) {
	for _, code := range []string{
		importFailureNestedFormat, importFailureUnsupportedFeature,
		importFailureDeclaredFormat, importFailureConversionFailed, importFailureUnknown,
	} {
		if importFailureReason(code) == "" {
			t.Errorf("%s has no copy; compute emits it and the practitioner would see nothing", code)
		}
	}
	for _, code := range []string{"", "nested_format", "someFutureCode"} {
		if got := importFailureReason(code); got != "" {
			t.Errorf("importFailureReason(%q) = %q, want empty", code, got)
		}
	}
}
