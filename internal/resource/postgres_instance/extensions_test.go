package postgres_instance

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
)

// --- sets ---

func extSet(t *testing.T, names ...string) types.Set {
	t.Helper()
	set, d := types.SetValueFrom(context.Background(), types.StringType, names)
	if d.HasError() {
		t.Fatalf("set build failed: %v", d.Errors())
	}
	return set
}

func joinNames(names []string) string { return strings.Join(names, ",") }

// --- diff synthesis ---

func TestExtensionDiffSynthesizesEnableAndDisable(t *testing.T) {
	plan := extSet(t, "timescaledb", "pgcrypto")
	state := extSet(t, "pgcrypto", "pg_trgm", "pg_stat_statements")

	enable, disable := extensionDiff(plan, state)
	if joinNames(enable) != "timescaledb" {
		t.Errorf("enable = %v, want [timescaledb]", enable)
	}
	if joinNames(disable) != "pg_stat_statements,pg_trgm" {
		t.Errorf("disable = %v, want sorted [pg_stat_statements pg_trgm]", disable)
	}
}

func TestExtensionDiffNullPlane(t *testing.T) {
	// A null plan (attribute omitted) reconciles nothing — an enable made
	// outside Terraform stays unless the configuration asks for its removal.
	if enable, disable := extensionDiff(types.SetNull(types.StringType), extSet(t, "timescaledb")); len(enable) != 0 || len(disable) != 0 {
		t.Errorf("null plan must reconcile nothing, got enable=%v disable=%v", enable, disable)
	}
	// An unknown plan (Create before any state) reconciles nothing.
	if enable, disable := extensionDiff(types.SetUnknown(types.StringType), types.SetNull(types.StringType)); len(enable) != 0 || len(disable) != 0 {
		t.Errorf("unknown plan must reconcile nothing, got enable=%v disable=%v", enable, disable)
	}
	// An explicit empty set ([]) against recorded state disables everything.
	// Built directly: the framework's SetValueFrom maps an empty Go slice to
	// a NULL set, but real HCL `extensions = []` is a known empty set — the
	// shape the k8s addons doctrine makes "select none".
	emptySet := types.SetValueMust(types.StringType, []attr.Value{})
	enable, disable := extensionDiff(emptySet, extSet(t, "pgcrypto"))
	if len(enable) != 0 {
		t.Errorf("enable = %v, want none", enable)
	}
	if joinNames(disable) != "pgcrypto" {
		t.Errorf("disable = %v, want [pgcrypto]", disable)
	}
}

// --- target reached / verdicts ---

func TestExtensionTargetReached(t *testing.T) {
	state := &apiExtensionState{
		Revision: 3,
		Extensions: []apiExtensionStateEntry{
			{Name: "timescaledb", Status: entryStatusEnabled},
			{Name: "pgcrypto", Status: entryStatusRemoved},
		},
	}
	if !extensionTargetReached(state, wireOpInstall, []string{"timescaledb"}) {
		t.Error("enabled entry must satisfy install")
	}
	if extensionTargetReached(state, wireOpInstall, []string{"pgcrypto"}) {
		t.Error("removed entry must not satisfy install")
	}
	// Removing a name that was never enabled satisfies the target — the
	// desired absence already holds.
	if !extensionTargetReached(state, wireOpRemove, []string{"never-there"}) {
		t.Error("absent entry must satisfy remove")
	}
	if !extensionTargetReached(state, wireOpRemove, []string{"pgcrypto"}) {
		t.Error("removed entry must satisfy remove")
	}
	if extensionTargetReached(state, wireOpRemove, []string{"timescaledb"}) {
		t.Error("enabled entry must not satisfy remove")
	}
}

func TestExtensionVerdictErrorSurfacesDetail(t *testing.T) {
	state := &apiExtensionState{
		Revision: 4,
		Extensions: []apiExtensionStateEntry{
			{Name: "timescaledb", Status: entryStatusFailed, Detail: "extension apply failed during restart"},
		},
	}
	err := extensionVerdictError(state, wireOpInstall, []string{"timescaledb", "pgcrypto"})
	if err == nil {
		t.Fatal("expected a verdict failure")
	}
	for _, want := range []string{"timescaledb", "extension apply failed during restart"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("verdict error must carry %q verbatim, got: %s", want, err.Error())
		}
	}
	if err := extensionVerdictError(state, wireOpRemove, []string{"pgcrypto"}); err != nil {
		t.Errorf("removing a never-recorded name after a completed op is satisfied, got %v", err)
	}
}

// --- refusal classification ---

func extAPIErr(status int, code, message string) error {
	return &client.APIError{StatusCode: status, Code: code, Message: message}
}

func TestClassifyExtensionRefusal(t *testing.T) {
	perm := func(err error) {
		if _, retryable, _ := classifyExtensionRefusal(wireOpInstall, err); retryable {
			t.Errorf("expected permanent: %v (retryable=%v)", err, retryable)
		}
	}
	retry := func(err error) {
		if converged, retryable, _ := classifyExtensionRefusal(wireOpInstall, err); converged || !retryable {
			t.Errorf("expected retryable: %v (converged=%v retryable=%v)", err, converged, retryable)
		}
	}
	converged := func(err error) {
		if cvg, retryable, _ := classifyExtensionRefusal(wireOpRemove, err); !cvg || retryable {
			t.Errorf("expected converged: %v (converged=%v retryable=%v)", err, cvg, retryable)
		}
	}

	perm(extAPIErr(403, refusalFeatureNotEnabled, "this feature is not enabled for your account"))
	perm(extAPIErr(400, "invalid_input", "unknown extension: nope"))
	perm(extAPIErr(400, "invalid_input", "extension nope is not available for PostgreSQL 17"))
	perm(extAPIErr(400, "invalid_input", "extension old is not available for new enables (status: deprecated)"))
	perm(extAPIErr(409, "invalid_state", "extensions are not yet available for highly available instances; coordinated node sequencing is not built yet"))
	perm(extAPIErr(404, "not_found", "database instance not found"))
	perm(extAPIErr(400, "invalid_input", "extensions must carry between 1 and 8 names"))

	retry(extAPIErr(409, "conflict", "another operation is already running on this instance; extensions can be applied only while no backup, restore or other operation is in flight — please retry once it completes"))
	retry(extAPIErr(409, "invalid_state", "cannot apply extensions to an instance in resizing state"))
	retry(extAPIErr(502, "extension_job_probe_unavailable", "extension apply refused: it cannot be confirmed that no other operation is running on this instance; nothing was started"))
	retry(extAPIErr(500, "extension_apply_start_unconfirmed", "the extension apply could not be confirmed to have started; check the instance's extensions and retry — retrying is always safe"))
	retry(errTransport{})

	// Removing a name the instance does not carry is converged truth.
	converged(extAPIErr(400, "invalid_input", "extension pgcrypto is not present on this instance"))
}

type errTransport struct{}

func (errTransport) Error() string { return "connection reset by peer" }

// --- HTTP helpers ---

func writeExtJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func decodeExtensionReq(t *testing.T, r *http.Request) apiExtensionOpRequest {
	t.Helper()
	var req apiExtensionOpRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		t.Fatalf("bad extension op body: %v", err)
	}
	return req
}

func catalogServer(t *testing.T, status int, body string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/databases/extensions" {
			if r.URL.Path == "/v1/me" {
				writeExtJSON(w, http.StatusOK, map[string]string{"id": "user-1", "tenantId": "t-1"})
				return
			}
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			writeExtJSON(w, http.StatusNotFound, map[string]string{"code": "not_found", "message": "no"})
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
}

// --- catalog plan-time checks (HTTP) ---

func TestCheckPlanExtensionsRefusesUnknownAndUnsupported(t *testing.T) {
	server := catalogServer(t, http.StatusOK, `{"extensions":[
		{"name":"timescaledb","pgMajors":[16,17,18],"requiresPreload":true,"status":"current"},
		{"name":"pgcrypto","pgMajors":[16,18],"requiresPreload":false,"status":"supported"},
		{"name":"legacy_ext","pgMajors":[16,17,18],"requiresPreload":false,"status":"deprecated"}
	]}`)
	defer server.Close()
	c := newClient(t, server)
	r := newResource(c)

	plan := &PostgresInstanceModel{
		Version:    types.StringValue("17"),
		Extensions: extSet(t, "timescaledb", "nope", "pgcrypto", "legacy_ext"),
	}
	diags := r.checkPlanExtensions(context.Background(), plan)
	if !diags.HasError() {
		t.Fatal("expected plan-time refusals")
	}
	text := ""
	for _, e := range diags.Errors() {
		text += e.Summary() + " :: " + e.Detail() + "\n"
	}
	// The platform's typed sentences, verbatim, modelled at plan.
	for _, want := range []string{
		refusalUnknownExtension + "nope",
		"extension pgcrypto" + refusalUnsupportedMajor + "17.",
		"extension legacy_ext" + refusalNotEnableable + "deprecated)",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("plan refusal must contain %q, got:\n%s", want, text)
		}
	}
}

func TestCheckPlanExtensionsAcceptsValidNames(t *testing.T) {
	server := catalogServer(t, http.StatusOK, `{"extensions":[
		{"name":"timescaledb","pgMajors":[16,17,18],"requiresPreload":true,"status":"current"}
	]}`)
	defer server.Close()
	c := newClient(t, server)
	r := newResource(c)

	for _, version := range []string{"16", "18"} {
		plan := &PostgresInstanceModel{
			Version:    types.StringValue(version),
			Extensions: extSet(t, "timescaledb"),
		}
		diags := r.checkPlanExtensions(context.Background(), plan)
		if diags.HasError() {
			t.Errorf("valid name for major %s must plan clean, got %v", version, diags.Errors())
		}
	}
}

func TestCheckPlanExtensionsDegradesOnCatalogReadFailure(t *testing.T) {
	server := catalogServer(t, http.StatusInternalServerError, `{"code":"internal_error","message":"administrativa"}`)
	defer server.Close()
	c := newClient(t, server)
	r := newResource(c)

	plan := &PostgresInstanceModel{
		Version:    types.StringValue("17"),
		Extensions: extSet(t, "nope"),
	}
	diags := r.checkPlanExtensions(context.Background(), plan)
	if diags.HasError() {
		t.Fatalf("a catalog read failure must degrade to a warning, not refuse the plan (the apply enforces fail-closed): %v", diags.Errors())
	}
	if len(diags.Warnings()) == 0 {
		t.Error("expected a warning diagnostic when the catalog cannot be read")
	}
}

// --- apply flow (HTTP) ---

func extensionHandlers(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v1/me":
			writeExtJSON(w, http.StatusOK, map[string]string{"id": "user-1", "tenantId": "t-1"})
		case strings.HasSuffix(r.URL.Path, "/events"):
			// The SSE stream's hermetic degradation: a 404 stands in for a
			// gateway that does not serve it, and the poller waits on its
			// timer.
			w.WriteHeader(http.StatusNotFound)
		default:
			handler(w, r)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

func TestApplyExtensionChangeHappyPath(t *testing.T) {
	var mu sync.Mutex
	posts := 0
	server := extensionHandlers(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/extensions"):
			posts++
			body := decodeExtensionReq(t, r)
			if body.Op != wireOpInstall || len(body.Extensions) != 1 || body.Extensions[0] != "timescaledb" {
				t.Errorf("unexpected op body: %+v", body)
			}
			writeExtJSON(w, http.StatusAccepted, map[string]any{
				"operationId": "op-1", "status": "applying_extensions",
				"resourceType": "database", "resourceId": "db-1", "extensionRevision": 1,
			})
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/operations/"):
			writeExtJSON(w, http.StatusOK, map[string]any{"operationId": "op-1", "status": "completed"})
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/extensions"):
			// timescaledb reads back disabled until its own enable POST has
			// happened — the reconcile-first pass must see an unsatisfied
			// target before the POST and a satisfied one after it.
			timescale := "removed"
			if posts >= 1 {
				timescale = "enabled"
			}
			writeExtJSON(w, http.StatusOK, map[string]any{
				"extensionRevision": posts,
				"extensions": []map[string]any{
					{"name": "timescaledb", "status": timescale},
				},
			})
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	})
	c := newClient(t, server)
	r := newResource(c)

	if err := r.applyExtensionChange(context.Background(), "db-1", wireOpInstall, []string{"timescaledb"}, 5*time.Second); err != nil {
		t.Fatalf("apply failed: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if posts != 1 {
		t.Errorf("expected exactly one POST (reconcile-first must not double-apply), got %d", posts)
	}
}

func TestApplyExtensionRestoreConvergesOnNotPresentRefusal(t *testing.T) {
	server := extensionHandlers(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/extensions"):
			writeExtJSON(w, http.StatusBadRequest, map[string]string{
				"code":    "invalid_input",
				"message": "extension pgcrypto is not present on this instance",
			})
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/extensions"):
			writeExtJSON(w, http.StatusOK, map[string]any{"extensionRevision": 0, "extensions": []map[string]any{}})
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	})
	c := newClient(t, server)
	r := newResource(c)

	if err := r.applyExtensionChange(context.Background(), "db-1", wireOpRemove, []string{"pgcrypto"}, 5*time.Second); err != nil {
		t.Fatalf("a not-present removal must converge, got %v", err)
	}
}

func TestApplyExtensionChangeFailsHardOnUnknownName(t *testing.T) {
	var posts int
	var mu sync.Mutex
	server := extensionHandlers(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/extensions"):
			posts++
			writeExtJSON(w, http.StatusBadRequest, map[string]string{
				"code":    "invalid_input",
				"message": "unknown extension: nope",
			})
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/extensions"):
			writeExtJSON(w, http.StatusOK, map[string]any{"extensionRevision": 0, "extensions": []map[string]any{}})
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	})
	c := newClient(t, server)
	r := newResource(c)

	err := r.applyExtensionChange(context.Background(), "db-1", wireOpInstall, []string{"nope"}, 5*time.Second)
	if err == nil {
		t.Fatal("unknown name must stop the apply")
	}
	if !strings.Contains(err.Error(), "unknown extension: nope") {
		t.Errorf("the platform's typed refusal must surface verbatim, got: %s", err.Error())
	}
	mu.Lock()
	defer mu.Unlock()
	if posts != 1 {
		t.Errorf("a permanent refusal must not be retried, posts=%d", posts)
	}
}

func TestApplyExtensionChangeSurvivesInFlight409(t *testing.T) {
	var mu sync.Mutex
	posts := 0
	server := extensionHandlers(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/extensions"):
			posts++
			if posts == 1 {
				// First POST: refused by the probe (another op in flight).
				writeExtJSON(w, http.StatusConflict, map[string]string{
					"code":    "conflict",
					"message": "another operation is already running on this instance; extensions can be applied only while no backup, restore or other operation is in flight — please retry once it completes",
				})
				return
			}
			writeExtJSON(w, http.StatusAccepted, map[string]any{
				"operationId": "op-2", "status": "applying_extensions",
				"resourceType": "database", "resourceId": "db-1", "extensionRevision": 2,
			})
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/operations/"):
			writeExtJSON(w, http.StatusOK, map[string]any{"operationId": "op-2", "status": "completed"})
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/extensions"):
			if posts < 2 {
				writeExtJSON(w, http.StatusOK, map[string]any{"extensionRevision": 1, "extensions": []map[string]any{}})
				return
			}
			writeExtJSON(w, http.StatusOK, map[string]any{
				"extensionRevision": 2,
				"extensions":        []map[string]any{{"name": "timescaledb", "status": "enabled"}},
			})
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	})
	c := newClient(t, server)
	r := newResource(c)

	if err := r.applyExtensionChange(context.Background(), "db-1", wireOpInstall, []string{"timescaledb"}, 5*time.Second); err != nil {
		t.Fatalf("in-flight 409 must be waited out and retried, got %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if posts != 2 {
		t.Errorf("expected the retry POST after the 409, posts=%d", posts)
	}
}

func TestApplyExtensionChangeVerdictFailureStopsApply(t *testing.T) {
	server := extensionHandlers(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/extensions"):
			writeExtJSON(w, http.StatusAccepted, map[string]any{
				"operationId": "op-3", "status": "applying_extensions",
				"resourceType": "database", "resourceId": "db-1", "extensionRevision": 1,
			})
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/operations/"):
			writeExtJSON(w, http.StatusOK, map[string]any{"operationId": "op-3", "status": "completed"})
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/extensions"):
			writeExtJSON(w, http.StatusOK, map[string]any{
				"extensionRevision": 1,
				"extensions":        []map[string]any{{"name": "timescaledb", "status": "failed", "detail": "extension apply failed during restart"}},
			})
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	})
	c := newClient(t, server)
	r := newResource(c)

	err := r.applyExtensionChange(context.Background(), "db-1", wireOpInstall, []string{"timescaledb"}, 5*time.Second)
	if err == nil {
		t.Fatal("a recorded failed verdict must stop the apply")
	}
	if !strings.Contains(err.Error(), "extension apply failed during restart") {
		t.Errorf("the recorded detail must surface verbatim, got: %s", err.Error())
	}
}

func TestApplyExtensionDiffSerialized(t *testing.T) {
	var mu sync.Mutex
	var order []string
	posts := 0
	// pg_trgm reads back enabled (removable) until its own disable POST has
	// happened, so the serialized enable→disable order is exercised for real.
	server := extensionHandlers(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/extensions"):
			posts++
			body := decodeExtensionReq(t, r)
			order = append(order, body.Op+":"+strings.Join(body.Extensions, "|"))
			writeExtJSON(w, http.StatusAccepted, map[string]any{
				"operationId": "op-" + body.Op, "status": "applying_extensions",
				"resourceType": "database", "resourceId": "db-1",
			})
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/operations/"):
			writeExtJSON(w, http.StatusOK, map[string]any{"status": "completed"})
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/extensions"):
			pgTrgm := "enabled"
			if posts >= 2 {
				pgTrgm = "removed"
			}
			// timescaledb is not enabled until its own enable POST has
			// happened.
			timescale := "removed"
			if posts >= 1 {
				timescale = "enabled"
			}
			writeExtJSON(w, http.StatusOK, map[string]any{
				"extensionRevision": 9,
				"extensions": []map[string]any{
					{"name": "timescaledb", "status": timescale},
					{"name": "pg_trgm", "status": pgTrgm},
				},
			})
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	})
	c := newClient(t, server)
	r := newResource(c)

	if err := r.applyExtensionDiff(context.Background(), "db-1", []string{"timescaledb"}, []string{"pg_trgm"}, 5*time.Second); err != nil {
		t.Fatalf("serialized diff failed: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if strings.Join(order, ";") != "install:timescaledb;remove:pg_trgm" {
		t.Errorf("ops must run enables-first then disables, got %v", order)
	}
}

func TestApplyExtensionRecordedInFlightVerdictRetries(t *testing.T) {
	// The κ3-window shape: the POST was accepted, the workflow's enqueue
	// refused against a concurrent op, the refusal landed as a RECORDED
	// failed entry naming the in-flight detail — the one verdict failure
	// whose detail means "not started, wait and retry". The second POST
	// (a fresh revision, straight from the platform's re-apply remedy)
	// runs and converges. A permanent verdict failure (any other detail)
	// must still stop the apply — see TestApplyExtensionChangeVerdictFailureStopsApply.
	var mu sync.Mutex
	posts := 0
	server := extensionHandlers(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/extensions"):
			posts++
			writeExtJSON(w, http.StatusAccepted, map[string]any{
				"operationId": "op-wf-" + strconv.Itoa(posts), "status": "applying_extensions",
				"resourceType": "database", "resourceId": "db-1", "extensionRevision": posts,
			})
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/operations/"):
			// The first workflow fails (enqueue refusal); the second completes.
			if strings.Contains(r.URL.Path, "op-wf-1") {
				writeExtJSON(w, http.StatusOK, map[string]any{"operationId": "op-wf-1", "status": "failed"})
				return
			}
			writeExtJSON(w, http.StatusOK, map[string]any{"operationId": "op-wf-2", "status": "completed"})
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/extensions"):
			if posts < 2 {
				writeExtJSON(w, http.StatusOK, map[string]any{
					"extensionRevision": 1,
					"extensions": []map[string]any{
						{"name": "timescaledb", "status": "failed", "detail": "extension apply refused: another operation is already running on this instance"},
					},
				})
				return
			}
			writeExtJSON(w, http.StatusOK, map[string]any{
				"extensionRevision": 2,
				"extensions":        []map[string]any{{"name": "timescaledb", "status": "enabled"}},
			})
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	})
	c := newClient(t, server)
	r := newResource(c)

	if err := r.applyExtensionChange(context.Background(), "db-1", wireOpInstall, []string{"timescaledb"}, 5*time.Second); err != nil {
		t.Fatalf("a recorded in-flight verdict must be waited out and retried, got %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if posts != 2 {
		t.Errorf("expected a second POST with a fresh revision, posts=%d", posts)
	}
}

func TestApplyExtensionStopsOnContextCancelled(t *testing.T) {
	// The interrupt contract: a cancelled context stops the loop at once —
	// the deadline bounds the budget, not an interrupt.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	server := extensionHandlers(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	c := newClient(t, server)
	r := newResource(c)

	start := time.Now()
	err := r.applyExtensionChange(ctx, "db-1", wireOpInstall, []string{"timescaledb"}, 30*time.Minute)
	if err == nil {
		t.Fatal("a cancelled context must stop the apply with an error")
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("cancelled context must not spin until the budget expires, took %s", elapsed)
	}
}
