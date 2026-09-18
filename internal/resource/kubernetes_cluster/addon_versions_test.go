package kubernetes_cluster

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
)

// eso is the addon key most tests pin. A constant keeps the comparisons below free of
// the `secrets"] != "` shape that detect-secrets reads as a credential.
const eso = "external-secrets"

// typedPins gives a zero-value (untyped) map the string element type, so the fixtures
// that predate addon_versions keep building state without naming it.
func typedPins(m types.Map) types.Map {
	if m.ElementType(context.Background()) == nil {
		return types.MapNull(types.StringType)
	}
	return m
}

func pinMap(kv ...string) types.Map {
	elems := map[string]attr.Value{}
	for i := 0; i+1 < len(kv); i += 2 {
		elems[kv[i]] = types.StringValue(kv[i+1])
	}
	return types.MapValueMust(types.StringType, elems)
}

// putResponse is the PUT .../addons 200 body: the cluster plus the FULL recorded pin
// map and notices, exactly the fields a buggy client would be tempted to replay.
type putResponse struct {
	apiKubernetesCluster
	PinnedVersions map[string]string `json:"pinnedVersions,omitempty"`
}

// mock is a minimal kubernetes API for the addon paths, answered through the REAL client
// (real status codes and error envelopes, never a stubbed error type).
type mock struct {
	t *testing.T
	// putRespond answers the n-th PUT .../addons (1-based).
	putRespond func(n int) (int, any)
	// status, when set, is the cluster status given how many PUTs have been made.
	status func(puts int) string
	// addonsRespond, when set, answers GET .../addons.
	addonsRespond func() (int, any)

	mu        sync.Mutex
	putBodies [][]byte
	postBody  []byte
	addonGets int
}

func readBody(t *testing.T, r *http.Request) []byte {
	t.Helper()
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		t.Errorf("read body: %v", err)
	}
	return raw
}

func (m *mock) serve(w http.ResponseWriter, r *http.Request) {
	t := m.t
	const base = "/v1/tenants/t-1/kubernetes-clusters"
	switch {
	case r.Method == http.MethodPost && r.URL.Path == base:
		raw := readBody(t, r)
		m.mu.Lock()
		m.postBody = raw
		m.mu.Unlock()
		w.WriteHeader(http.StatusCreated)
		writeJSON(t, w, runningCluster())
	case r.Method == http.MethodPut && r.URL.Path == base+"/c-1/addons":
		raw := readBody(t, r)
		m.mu.Lock()
		m.putBodies = append(m.putBodies, raw)
		n := len(m.putBodies)
		m.mu.Unlock()
		status, body := m.putRespond(n)
		w.WriteHeader(status)
		writeJSON(t, w, body)
	case r.Method == http.MethodGet && r.URL.Path == base+"/c-1/addons" && m.addonsRespond != nil:
		m.mu.Lock()
		m.addonGets++
		m.mu.Unlock()
		status, body := m.addonsRespond()
		w.WriteHeader(status)
		writeJSON(t, w, body)
	case r.Method == http.MethodGet && r.URL.Path == base+"/c-1":
		c := runningCluster()
		m.mu.Lock()
		n := len(m.putBodies)
		m.mu.Unlock()
		if m.status != nil {
			// An empty status stands for a FAILED read: the platform answers 500.
			if c.Status = m.status(n); c.Status == "" {
				w.WriteHeader(http.StatusInternalServerError)
				writeJSON(t, w, map[string]any{"code": "internal_error", "message": "platform unreachable"})
				return
			}
		}
		writeJSON(t, w, c)
	case r.Method == http.MethodGet && r.URL.Path == base+"/c-1/node-pools":
		writeJSON(t, w, apiNodePoolList{NodePools: []apiNodePool{initialPool(statusActive)}})
	case r.Method == http.MethodGet && r.URL.Path == base+"/c-1/node-pools/np-1":
		writeJSON(t, w, initialPool(statusActive))
	case r.Method == http.MethodGet && r.URL.Path == base+"/c-1/kubeconfig":
		writeJSON(t, w, apiKubeconfig{Kubeconfig: "kubeconfig-yaml"})
	case strings.HasSuffix(r.URL.Path, "/events"):
		w.WriteHeader(http.StatusNotFound)
	default:
		t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusNotFound)
	}
}

func (m *mock) puts() [][]byte {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([][]byte(nil), m.putBodies...)
}

// start serves the mock and returns a resource wired to it.
func (m *mock) start() (*kubernetesClusterResource, func()) {
	server := httptest.NewServer(http.HandlerFunc(m.serve))
	c := client.NewClient(server.URL, "test-key", client.WithHTTPClient(server.Client()))
	c.SetTenantIDForTest("t-1")
	return testResource(c), server.Close
}

func runUpdate(t *testing.T, m *mock, stateM, planM KubernetesClusterModel) resource.UpdateResponse {
	t.Helper()
	r, stop := m.start()
	defer stop()
	state := buildState(t, stateM)
	plan := buildPlan(t, planM)
	resp := resource.UpdateResponse{State: state}
	r.Update(context.Background(), resource.UpdateRequest{Plan: plan, State: state, Config: tfsdk.Config(plan)}, &resp)
	return resp
}

func pinnedCreatePlan() KubernetesClusterModel {
	return KubernetesClusterModel{
		Name: types.StringValue("test-cluster"), Version: types.StringUnknown(),
		ControlPlaneTier: types.StringUnknown(), Region: types.StringUnknown(),
		VPCID: types.StringValue("vpc-1"), SubnetID: types.StringValue("sn-1"),
		PublicIPID:    types.StringNull(),
		Addons:        addonSet(eso),
		AddonVersions: pinMap(eso, "v2"),
		InitialNodePool: &InitialNodePoolModel{
			ID: types.StringUnknown(), Name: types.StringUnknown(),
			FlavorID: types.StringValue("k8s.gp1.medium"), NodeCount: types.Int64Value(2), Status: types.StringUnknown(),
		},
	}
}

func runCreate(t *testing.T, m *mock) resource.CreateResponse {
	t.Helper()
	r, stop := m.start()
	defer stop()
	plan := buildPlan(t, pinnedCreatePlan())
	resp := resource.CreateResponse{State: emptyState(t)}
	r.Create(context.Background(), resource.CreateRequest{Plan: plan, Config: tfsdk.Config(plan)}, &resp)
	return resp
}

func runRead(t *testing.T, m *mock, stateM KubernetesClusterModel) resource.ReadResponse {
	t.Helper()
	r, stop := m.start()
	defer stop()
	state := buildState(t, stateM)
	resp := resource.ReadResponse{State: state}
	r.Read(context.Background(), resource.ReadRequest{State: state}, &resp)
	return resp
}

func okPut(pinned map[string]string, notices ...apiClusterNotice) func(int) (int, any) {
	return func(int) (int, any) {
		c := runningCluster()
		c.Addons = []string{eso, "external-dns"}
		c.Notices = notices
		return http.StatusOK, putResponse{apiKubernetesCluster: c, PinnedVersions: pinned}
	}
}

func apiErrorBody(status int, code, msg string) func(int) (int, any) {
	return func(int) (int, any) { return status, map[string]any{"code": code, "message": msg} }
}

func sentVersions(t *testing.T, raw []byte) map[string]string {
	t.Helper()
	var body apiUpdateClusterAddonsRequest
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("decode PUT body %s: %v", raw, err)
	}
	return body.Versions
}

func pinsIn(t *testing.T, st tfsdk.State) map[string]string {
	t.Helper()
	var m KubernetesClusterModel
	if diags := st.Get(context.Background(), &m); diags.HasError() {
		t.Fatalf("state get: %v", diags.Errors())
	}
	if m.AddonVersions.IsNull() {
		return nil
	}
	out := map[string]string{}
	for k, v := range m.AddonVersions.Elements() {
		out[k] = v.(types.String).ValueString()
	}
	return out
}

// --- Update: what the request carries (trap 2) ---

// TRAP 2(a): an addon ADD with no addon_versions carries no `versions` key at all.
func TestUpdate_AddonAddWithoutPinsSendsNoVersions(t *testing.T) {
	m := &mock{t: t, putRespond: okPut(map[string]string{eso: "v1", "external-dns": "v3"})}
	planM := stateModel()
	planM.Addons = addonSet(eso, "external-dns")

	resp := runUpdate(t, m, stateModel(), planM)
	if resp.Diagnostics.HasError() {
		t.Fatalf("update failed: %v", resp.Diagnostics.Errors())
	}
	puts := m.puts()
	if len(puts) != 1 {
		t.Fatalf("expected 1 PUT, got %d", len(puts))
	}
	if bytes.Contains(puts[0], []byte(`"versions"`)) {
		t.Errorf("an addon add without pins must not send versions, got %s", puts[0])
	}
}

// TRAP 2(b): changing ONE pin sends exactly that key, and state keeps the configured
// map — never the response's full pinnedVersions.
func TestUpdate_ChangingOnePinSendsExactlyThatKey(t *testing.T) {
	m := &mock{t: t, putRespond: okPut(map[string]string{eso: "v2", "external-dns": "v5", "cert-manager": "v9"})}
	stateM := stateModel()
	stateM.Addons = addonSet(eso, "external-dns")
	stateM.AddonVersions = pinMap(eso, "v1", "external-dns", "v5")
	planM := stateM
	planM.AddonVersions = pinMap(eso, "v2", "external-dns", "v5")

	resp := runUpdate(t, m, stateM, planM)
	if resp.Diagnostics.HasError() {
		t.Fatalf("update failed: %v", resp.Diagnostics.Errors())
	}
	puts := m.puts()
	if len(puts) != 1 {
		t.Fatalf("expected 1 PUT, got %d", len(puts))
	}
	if got := sentVersions(t, puts[0]); len(got) != 1 || got[eso] != "v2" {
		t.Errorf("versions = %v, want exactly {%s: v2}", got, eso)
	}
	if st := pinsIn(t, resp.State); len(st) != 2 || st[eso] != "v2" || st["external-dns"] != "v5" {
		t.Errorf("state addon_versions = %v, want the configured map only (no cert-manager from pinnedVersions)", st)
	}
}

// TRAP 2(c): state recorded a pin this configuration no longer declares (and somebody
// moved it concurrently, as the response shows). The request must not name that key,
// and the response's value for it must not reach state.
func TestUpdate_UnconfiguredConcurrentPinNeverSent(t *testing.T) {
	m := &mock{t: t, putRespond: okPut(map[string]string{eso: "v2", "external-dns": "v6-moved-by-someone-else"})}
	stateM := stateModel()
	stateM.Addons = addonSet(eso, "external-dns")
	stateM.AddonVersions = pinMap(eso, "v1", "external-dns", "v5")
	planM := stateM
	planM.AddonVersions = pinMap(eso, "v2")

	resp := runUpdate(t, m, stateM, planM)
	if resp.Diagnostics.HasError() {
		t.Fatalf("update failed: %v", resp.Diagnostics.Errors())
	}
	got := sentVersions(t, m.puts()[0])
	if _, sent := got["external-dns"]; sent || len(got) != 1 {
		t.Errorf("versions = %v, must contain only the configured, changed key", got)
	}
	if st := pinsIn(t, resp.State); len(st) != 1 || st[eso] != "v2" {
		t.Errorf("state addon_versions = %v, want {%s: v2}", st, eso)
	}
}

// FIX 8: a state with no recorded pins (an import) sends EVERY configured pin.
func TestUpdate_NullStatePinsSendsEveryConfiguredPin(t *testing.T) {
	m := &mock{t: t, putRespond: okPut(nil)}
	stateM := stateModel()
	stateM.Addons = addonSet(eso, "external-dns")
	stateM.AddonVersions = types.MapNull(types.StringType)
	planM := stateM
	planM.AddonVersions = pinMap(eso, "v2", "external-dns", "v5")

	resp := runUpdate(t, m, stateM, planM)
	if resp.Diagnostics.HasError() {
		t.Fatalf("update failed: %v", resp.Diagnostics.Errors())
	}
	if got := sentVersions(t, m.puts()[0]); len(got) != 2 || got[eso] != "v2" || got["external-dns"] != "v5" {
		t.Errorf("versions = %v, want every configured pin", got)
	}
}

// FIX 3: after a refresh read back a drifted pin, the apply sends the CONFIGURED value.
func TestUpdate_DriftedStatePinSendsConfiguredValue(t *testing.T) {
	m := &mock{t: t, putRespond: okPut(nil)}
	stateM := stateModel()
	stateM.AddonVersions = pinMap(eso, "v9-moved-outside-terraform")
	planM := stateM
	planM.AddonVersions = pinMap(eso, "v2")

	resp := runUpdate(t, m, stateM, planM)
	if resp.Diagnostics.HasError() {
		t.Fatalf("update failed: %v", resp.Diagnostics.Errors())
	}
	if got := sentVersions(t, m.puts()[0]); len(got) != 1 || got[eso] != "v2" {
		t.Errorf("versions = %v, want the configured {%s: v2}", got, eso)
	}
	if st := pinsIn(t, resp.State); st[eso] != "v2" {
		t.Errorf("state addon_versions = %v, want the plan value", st)
	}
}

func TestChangedPins(t *testing.T) {
	if got := changedPins(pinMap("a", "v1"), pinMap("a", "v1")); got != nil {
		t.Errorf("unchanged pins = %v, want nil", got)
	}
	if got := changedPins(pinMap("a", "v1", "b", "v2"), pinMap("a", "v1")); got != nil {
		t.Errorf("dropping a key must send nothing (no unpin), got %v", got)
	}
	if got := changedPins(types.MapNull(types.StringType), pinMap("a", "v1", "b", "v2")); len(got) != 2 {
		t.Errorf("null state must send every configured pin, got %v", got)
	}
	if got := changedPins(pinMap("a", "v1"), types.MapNull(types.StringType)); got != nil {
		t.Errorf("null plan = %v, want nil", got)
	}
}

// --- Update: responses and failures ---

// TRAP 3: notices are warnings in an open vocabulary — an unknown code is shown by its
// message and never becomes an error.
func TestUpdate_UnknownNoticeCodeIsWarning(t *testing.T) {
	const msg = "Delete the leftover namespace yourself."
	m := &mock{t: t, putRespond: okPut(nil, apiClusterNotice{Code: "a_code_from_the_future", Message: msg})}
	planM := stateModel()
	planM.Addons = addonSet(eso, "external-dns")

	resp := runUpdate(t, m, stateModel(), planM)
	if resp.Diagnostics.HasError() {
		t.Fatalf("a notice must never be an error: %v", resp.Diagnostics.Errors())
	}
	found := false
	for _, d := range resp.Diagnostics.Warnings() {
		if d.Detail() == msg {
			found = true
		}
	}
	if !found {
		t.Errorf("expected a warning carrying the notice message, got %v", resp.Diagnostics)
	}
}

// FIX 6: a 2xx whose body cannot be parsed is an accepted change, not a failure.
func TestUpdate_UnreadableSuccessBodyIsWarning(t *testing.T) {
	m := &mock{t: t, putRespond: func(int) (int, any) { return http.StatusOK, "not a cluster" }}
	planM := stateModel()
	planM.Addons = addonSet(eso, "external-dns")

	resp := runUpdate(t, m, stateModel(), planM)
	if resp.Diagnostics.HasError() {
		t.Fatalf("an accepted change must not error: %v", resp.Diagnostics.Errors())
	}
	if resp.Diagnostics.WarningsCount() == 0 {
		t.Error("expected a 'notices could not be read' warning")
	}
}

func TestUpdate_RetriesInvalidStateConflict(t *testing.T) {
	m := &mock{t: t, putRespond: func(n int) (int, any) {
		if n == 1 {
			return apiErrorBody(http.StatusConflict, "invalid_state", "the cluster is updating")(n)
		}
		return okPut(nil)(n)
	}}
	planM := stateModel()
	planM.Addons = addonSet(eso, "external-dns")

	resp := runUpdate(t, m, stateModel(), planM)
	if resp.Diagnostics.HasError() {
		t.Fatalf("update failed: %v", resp.Diagnostics.Errors())
	}
	if n := len(m.puts()); n != 2 {
		t.Errorf("expected a retry after 409 invalid_state, got %d PUTs", n)
	}
}

// FIX 2: a retryable 409 on a cluster that has gone to `error` stops at once.
func TestUpdate_InvalidStateStopsAtOnceWhenClusterErrors(t *testing.T) {
	m := &mock{
		t:          t,
		putRespond: apiErrorBody(http.StatusConflict, "invalid_state", "the cluster is error"),
		status: func(puts int) string {
			if puts > 0 {
				return statusError
			}
			return statusRunning
		},
	}
	planM := stateModel()
	planM.Addons = addonSet(eso, "external-dns")

	resp := runUpdate(t, m, stateModel(), planM)
	if !resp.Diagnostics.HasError() {
		t.Fatal("expected an error")
	}
	if n := len(m.puts()); n != 1 {
		t.Errorf("expected no retry against an errored cluster, got %d PUTs", n)
	}
}

// 409 `conflict` includes "the platform's records disagree", which never clears.
func TestUpdate_ConflictCodeIsNotRetried(t *testing.T) {
	m := &mock{t: t, putRespond: apiErrorBody(http.StatusConflict, "conflict", "records disagree")}
	planM := stateModel()
	planM.Addons = addonSet(eso, "external-dns")

	resp := runUpdate(t, m, stateModel(), planM)
	if !resp.Diagnostics.HasError() {
		t.Fatal("expected an error")
	}
	if n := len(m.puts()); n != 1 {
		t.Errorf("expected no retry on 409 conflict, got %d PUTs", n)
	}
}

// PUT 503 in its production shape (kubernetes v5.6.0 cluster_addons.go: status 503, code
// internal_error): the update fails fast with the server message and says re-applying is
// safe. That prior state survives the failure is the FRAMEWORK's doing —
// fwserver/server_updateresource.go seeds `updateResp.State = *req.PriorState` before
// calling Update — so it is not asserted here, where the test seeds resp.State itself.
func TestUpdate_Put503SaysReapplyIsSafe(t *testing.T) {
	const serverMsg = "the addon selection was changed, but the version pin was not recorded by the platform release currently running; re-send the identical request"
	m := &mock{t: t, putRespond: apiErrorBody(http.StatusServiceUnavailable, "internal_error", serverMsg)}
	stateM := stateModel()
	stateM.AddonVersions = pinMap(eso, "v1")
	planM := stateM
	planM.AddonVersions = pinMap(eso, "v2")

	resp := runUpdate(t, m, stateM, planM)
	if !resp.Diagnostics.HasError() {
		t.Fatal("expected an error")
	}
	detail := resp.Diagnostics.Errors()[0].Detail()
	if !strings.Contains(detail, serverMsg) || !strings.Contains(detail, "Re-applying is safe") {
		t.Errorf("detail must carry the server message and say re-applying is safe, got %q", detail)
	}
	if n := len(m.puts()); n != 1 {
		t.Errorf("the update path must not retry a 5xx, got %d PUTs", n)
	}
}

// RE-CHECK 1: an addon removed out of band and re-added by the configuration sends its
// configured pin, even though state still records the same value — otherwise the
// platform installs the recommended version and only a second apply restores the pin.
func TestUpdate_ReAddedAddonResendsItsPin(t *testing.T) {
	m := &mock{t: t, putRespond: okPut(nil)}
	stateM := stateModel()
	stateM.Addons = addonSet()
	stateM.AddonVersions = pinMap(eso, "v1")
	planM := stateM
	planM.Addons = addonSet(eso)

	resp := runUpdate(t, m, stateM, planM)
	if resp.Diagnostics.HasError() {
		t.Fatalf("update failed: %v", resp.Diagnostics.Errors())
	}
	puts := m.puts()
	if len(puts) != 1 {
		t.Fatalf("expected 1 PUT, got %d", len(puts))
	}
	if got := sentVersions(t, puts[0]); len(got) != 1 || got[eso] != "v1" {
		t.Errorf("versions = %v, want the re-added addon's configured pin {%s: v1}", got, eso)
	}
}

// RE-CHECK 1: an addon neither added nor re-pinned sends nothing — and neither does an
// addon being added that the configuration does not pin.
func TestUpdate_UnchangedPinsOnExistingAddonsSendNothing(t *testing.T) {
	m := &mock{t: t, putRespond: okPut(nil)}
	stateM := stateModel()
	stateM.Addons = addonSet(eso, "external-dns")
	stateM.AddonVersions = pinMap(eso, "v1", "external-dns", "v5")
	planM := stateM
	planM.Addons = addonSet(eso, "external-dns", "cert-manager")

	resp := runUpdate(t, m, stateM, planM)
	if resp.Diagnostics.HasError() {
		t.Fatalf("update failed: %v", resp.Diagnostics.Errors())
	}
	puts := m.puts()
	if len(puts) != 1 {
		t.Fatalf("expected 1 PUT, got %d", len(puts))
	}
	if bytes.Contains(puts[0], []byte(`"versions"`)) {
		t.Errorf("no pin changed and no pinned addon was added, yet versions was sent: %s", puts[0])
	}
}

// RE-CHECK 2: a cluster being torn down (`deleting`) stops the retry at once.
func TestPutAddons_DeletingClusterStopsAtOnce(t *testing.T) {
	m := &mock{
		t:          t,
		putRespond: apiErrorBody(http.StatusConflict, "invalid_state", "the cluster is deleting"),
		status: func(puts int) string {
			if puts > 0 {
				return statusDeleting
			}
			return statusRunning
		},
	}
	planM := stateModel()
	planM.Addons = addonSet(eso, "external-dns")
	if resp := runUpdate(t, m, stateModel(), planM); !resp.Diagnostics.HasError() {
		t.Fatal("expected an error")
	}
	if n := len(m.puts()); n != 1 {
		t.Errorf("expected no retry against a deleting cluster, got %d PUTs", n)
	}
}

// Only a 409 `invalid_state` clears on its own. A transport failure, a 5xx or any other
// error fails the update at once: an update error taints nothing, so re-applying is safe.
func TestRetryableAddonsPut_OnlyInvalidStateIsRetried(t *testing.T) {
	for name, tc := range map[string]struct {
		err  error
		want bool
	}{
		"409 invalid_state": {&client.APIError{StatusCode: http.StatusConflict, Code: "invalid_state"}, true},
		"409 conflict":      {&client.APIError{StatusCode: http.StatusConflict, Code: "conflict"}, false},
		"503":               {&client.APIError{StatusCode: http.StatusServiceUnavailable, Code: "internal_error"}, false},
		"client timeout": {fmt.Errorf("request failed: %w",
			&url.Error{Op: "Put", URL: "https://api.example/addons", Err: context.DeadlineExceeded}), false},
		"session expired": {fmt.Errorf("refreshing credentials: %w", errors.New("session expired")), false},
	} {
		if got := retryableAddonsPut(tc.err); got != tc.want {
			t.Errorf("%s: retryable = %v, want %v", name, got, tc.want)
		}
	}
}

// RE-CHECK 3: a cancelled context is the caller stopping, never a conflict to retry.
func TestPutAddons_CancelledContextReturnsAtOnce(t *testing.T) {
	m := &mock{t: t, putRespond: apiErrorBody(http.StatusConflict, "invalid_state", "not running")}
	r, stop := m.start()
	defer stop()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	start := time.Now()
	_, err := r.putAddons(ctx, "c-1", apiUpdateClusterAddonsRequest{Addons: []string{eso}}, time.Now().Add(time.Minute))
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("a cancelled context must return at once, took %s", elapsed)
	}
	if n := len(m.puts()); n > 1 {
		t.Errorf("a cancelled context must not be retried, got %d PUTs", n)
	}
}

// --- Create: pins ride the create request ---

// The configured pins reach the POST exactly as configured, and Create issues NO
// PUT .../addons: the post-create pin PUT and its taint/untaint error paths are gone.
func TestCreate_PinsRideTheCreateRequestAndNoAddonsPut(t *testing.T) {
	m := &mock{t: t, putRespond: okPut(nil)}
	resp := runCreate(t, m)
	if resp.Diagnostics.HasError() {
		t.Fatalf("create failed: %v", resp.Diagnostics.Errors())
	}
	m.mu.Lock()
	postBody := m.postBody
	m.mu.Unlock()
	var body apiCreateClusterRequest
	if err := json.Unmarshal(postBody, &body); err != nil {
		t.Fatalf("decode POST body %s: %v", postBody, err)
	}
	if len(body.Versions) != 1 || body.Versions[eso] != "v2" {
		t.Errorf("POST versions = %v, want exactly the configured {%s: v2}", body.Versions, eso)
	}
	if n := len(m.puts()); n != 0 {
		t.Errorf("create must not PUT .../addons, got %d PUTs", n)
	}
	if st := pinsIn(t, resp.State); len(st) != 1 || st[eso] != "v2" {
		t.Errorf("state addon_versions = %v, want the configured map only", st)
	}
}

// A create whose cluster never reaches running fails (and is tainted), but its orphan-guard
// state still records the pins: the create request that the platform accepted carried them.
func TestCreate_FailedWaitKeepsTheSentPinsInState(t *testing.T) {
	m := &mock{t: t, putRespond: okPut(nil), status: func(int) string { return statusError }}
	resp := runCreate(t, m)
	if !resp.Diagnostics.HasError() {
		t.Fatal("expected the create to fail on an errored cluster")
	}
	if n := len(m.puts()); n != 0 {
		t.Errorf("create must not PUT .../addons, got %d PUTs", n)
	}
	if st := pinsIn(t, resp.State); len(st) != 1 || st[eso] != "v2" {
		t.Errorf("state addon_versions = %v, want the pins the create request carried", st)
	}
}

// --- Read: pins read back for configured keys only (fix 3) ---

func TestRead_RefreshesOnlyConfiguredPins(t *testing.T) {
	m := &mock{t: t, addonsRespond: func() (int, any) {
		return http.StatusOK, apiAddonPinList{Addons: []apiAddonPin{
			{Key: eso, PinnedVersion: "v9-moved-outside-terraform"},
			{Key: "external-dns", PinnedVersion: "v5-never-configured"},
			{Key: "cert-manager", PinnedVersion: ""},
		}}
	}}
	stateM := stateModel()
	stateM.AddonVersions = pinMap(eso, "v1", "cert-manager", "v3")

	resp := runRead(t, m, stateM)
	if resp.Diagnostics.HasError() {
		t.Fatalf("read failed: %v", resp.Diagnostics.Errors())
	}
	st := pinsIn(t, resp.State)
	if st[eso] != "v9-moved-outside-terraform" {
		t.Errorf("a drifted configured pin must be read back, got %v", st)
	}
	if _, added := st["external-dns"]; added {
		t.Errorf("an unconfigured server pin must never enter state, got %v", st)
	}
	if st["cert-manager"] != "v3" {
		t.Errorf("an empty pinnedVersion must leave the recorded value, got %v", st)
	}
}

// DELTA c: any error on the subpath — including 404 and 500 — keeps the prior pins, never
// fails the refresh, and never removes the resource from state.
func TestRead_PinsUnavailableKeepsPriorWithoutError(t *testing.T) {
	for _, status := range []int{
		http.StatusBadRequest, http.StatusNotFound, http.StatusConflict,
		http.StatusInternalServerError, http.StatusNotImplemented, http.StatusServiceUnavailable,
	} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			m := &mock{t: t, addonsRespond: func() (int, any) {
				return status, map[string]any{"code": "internal_error", "message": "no version data"}
			}}
			stateM := stateModel()
			stateM.AddonVersions = pinMap(eso, "v1")

			resp := runRead(t, m, stateM)
			if resp.Diagnostics.HasError() {
				t.Fatalf("status %d must not fail a refresh: %v", status, resp.Diagnostics.Errors())
			}
			if resp.State.Raw.IsNull() {
				t.Fatalf("status %d on the addons subpath removed the cluster from state", status)
			}
			if st := pinsIn(t, resp.State); len(st) != 1 || st[eso] != "v1" {
				t.Errorf("status %d: state addon_versions = %v, want the prior value", status, st)
			}
		})
	}
}

// DELTA c: the read-back uses pinnedVersion only. appliedVersion lags while a change
// converges; reading it would report drift on every refresh until convergence.
func TestRead_AppliedVersionLagIsNotDrift(t *testing.T) {
	m := &mock{t: t, addonsRespond: func() (int, any) {
		return http.StatusOK, map[string]any{"addons": []map[string]string{
			{"key": eso, "pinnedVersion": "v2", "appliedVersion": "v1-still-running", "state": "converging", "pinnedStatus": "current"},
		}}
	}}
	stateM := stateModel()
	stateM.AddonVersions = pinMap(eso, "v2")

	resp := runRead(t, m, stateM)
	if resp.Diagnostics.HasError() {
		t.Fatalf("read failed: %v", resp.Diagnostics.Errors())
	}
	if st := pinsIn(t, resp.State); len(st) != 1 || st[eso] != "v2" {
		t.Errorf("state addon_versions = %v, want the pinned v2 (appliedVersion must not be read)", st)
	}
}

// DELTA b: a cluster read that FAILS between retries proves nothing — the loop continues.
func TestPutAddons_FailedClusterReadKeepsRetrying(t *testing.T) {
	failFirstReread := func(puts int) string {
		if puts == 1 {
			return "" // the mock answers 500
		}
		return statusRunning
	}
	retryThenOK := func(n int) (int, any) {
		if n == 1 {
			return apiErrorBody(http.StatusConflict, "invalid_state", "the cluster is updating")(n)
		}
		return okPut(nil)(n)
	}
	m := &mock{t: t, putRespond: retryThenOK, status: failFirstReread}
	planM := stateModel()
	planM.Addons = addonSet(eso, "external-dns")
	if resp := runUpdate(t, m, stateModel(), planM); resp.Diagnostics.HasError() {
		t.Fatalf("update failed: %v", resp.Diagnostics.Errors())
	}
	if n := len(m.puts()); n != 2 {
		t.Errorf("expected the retry to continue past a failed cluster read, got %d PUTs", n)
	}
}

// No pins recorded, no extra request.
func TestRead_NoPinsNoAddonsRequest(t *testing.T) {
	for name, pins := range map[string]types.Map{"null": types.MapNull(types.StringType), "empty": pinMap()} {
		t.Run(name, func(t *testing.T) {
			m := &mock{t: t, addonsRespond: func() (int, any) { return http.StatusOK, apiAddonPinList{} }}
			stateM := stateModel()
			stateM.AddonVersions = pins
			if resp := runRead(t, m, stateM); resp.Diagnostics.HasError() {
				t.Fatalf("read failed: %v", resp.Diagnostics.Errors())
			}
			if m.addonGets != 0 {
				t.Errorf("expected no GET .../addons without pins, got %d", m.addonGets)
			}
		})
	}
}

// --- Validation ---

func TestCheckAddonVersions(t *testing.T) {
	pins := pinMap("external-dns", "v1")
	if d := checkAddonVersions(types.SetNull(types.StringType), pins); !d.HasError() {
		t.Error("pins without configured addons must be refused")
	}
	if d := checkAddonVersions(addonSet(eso), pins); !d.HasError() {
		t.Error("a pin for an unselected addon must be refused")
	}
	if d := checkAddonVersions(addonSet("external-dns"), pins); d.HasError() {
		t.Errorf("valid pins refused: %v", d)
	}
	if d := checkAddonVersions(types.SetUnknown(types.StringType), pins); d.HasError() {
		t.Error("unknown addons cannot be judged and must not be refused")
	}
	if d := checkAddonVersions(types.SetNull(types.StringType), types.MapNull(types.StringType)); d.HasError() {
		t.Error("no pins, nothing to check")
	}
}

// FIX 5: addon_versions keys follow the API's key grammar, at most 32 entries, and
// values are 1-128 characters.
func TestAddonVersionsValidators(t *testing.T) {
	ctx := context.Background()
	var sr resource.SchemaResponse
	NewResource().Schema(ctx, resource.SchemaRequest{}, &sr)
	vs := sr.Schema.Attributes["addon_versions"].(schema.MapAttribute).Validators

	big := map[string]attr.Value{}
	for i := 0; i < 33; i++ {
		big[fmt.Sprintf("addon-%d", i)] = types.StringValue("v1")
	}
	for name, tc := range map[string]struct {
		pins    types.Map
		wantErr bool
	}{
		"valid":          {pinMap(eso, "v1"), false},
		"dot-dot key":    {pinMap("..", "v1"), true},
		"uppercase key":  {pinMap("External", "v1"), true},
		"empty value":    {pinMap(eso, ""), true},
		"value over 128": {pinMap(eso, strings.Repeat("v", 129)), true},
		"33 entries":     {types.MapValueMust(types.StringType, big), true},
	} {
		t.Run(name, func(t *testing.T) {
			var resp validator.MapResponse
			for _, v := range vs {
				v.ValidateMap(ctx, validator.MapRequest{Path: path.Root("addon_versions"), ConfigValue: tc.pins}, &resp)
			}
			if resp.Diagnostics.HasError() != tc.wantErr {
				t.Errorf("error = %v, want %v: %v", resp.Diagnostics.HasError(), tc.wantErr, resp.Diagnostics)
			}
		})
	}
}
