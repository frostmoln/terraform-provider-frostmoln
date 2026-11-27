package kubernetes_cluster

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
)

func TestCompareMinor(t *testing.T) {
	for _, tc := range []struct {
		a, b string
		want int
		ok   bool
	}{
		{"1.35", "1.36", -1, true},
		{"1.36", "1.35", 1, true},
		{"1.36", "1.36", 0, true},
		{"1.10", "1.9", 1, true}, // numeric, not lexical
		{"2.0", "1.99", 1, true},
		{"1.36.2", "1.36", 0, false},
		{"latest", "1.36", 0, false},
		{"", "1.36", 0, false},
	} {
		got, ok := compareMinor(tc.a, tc.b)
		if got != tc.want || ok != tc.ok {
			t.Errorf("compareMinor(%q, %q) = %d, %v; want %d, %v", tc.a, tc.b, got, ok, tc.want, tc.ok)
		}
	}
}

// The plan never refuses with an ERROR (that would block a destroy), never replaces, and says
// what will happen: lower is warned and refused at apply, higher is warned as an in-place upgrade.
func TestModifyPlan_VersionChange(t *testing.T) {
	for _, tc := range []struct {
		name, config, wantWarn string
	}{
		{"lower", "1.34", "Kubernetes Version Cannot Be Lowered"},
		{"higher", "1.36", "This Apply Upgrades The Cluster In Place"},
		{"same", "1.35", ""},
		{"unparsable is left to the server", "latest", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			api := &tagsAPI{t: t}
			r, stop := api.resource(nil)
			defer stop()
			planM := stateModel()
			planM.Version = types.StringValue(tc.config)
			plan := buildPlan(t, planM)
			resp := resource.ModifyPlanResponse{Plan: plan}
			r.ModifyPlan(context.Background(), resource.ModifyPlanRequest{
				State: buildState(t, stateModel()), Plan: plan, Config: tfsdk.Config(plan),
			}, &resp)
			if resp.Diagnostics.HasError() {
				t.Fatalf("plan raised an error: %v", resp.Diagnostics.Errors())
			}
			var got []string
			for _, w := range resp.Diagnostics.Warnings() {
				if strings.Contains(w.Summary(), "Version") || strings.Contains(w.Summary(), "Upgrades") {
					got = append(got, w.Summary())
				}
			}
			if tc.wantWarn == "" && len(got) != 0 || tc.wantWarn != "" && (len(got) != 1 || got[0] != tc.wantWarn) {
				t.Errorf("warnings = %v, want %q", got, tc.wantWarn)
			}
		})
	}
}

func TestRefuseDowngrade(t *testing.T) {
	api := &tagsAPI{t: t}
	r, stop := api.resource(nil)
	defer stop()
	state := stateModel() // 1.35
	for _, v := range []string{"1.34", "1.33"} {
		plan := stateModel()
		plan.Version = types.StringValue(v)
		var d diag.Diagnostics
		if !r.refuseDowngrade(context.Background(), plan, state, &d) || !d.HasError() {
			t.Errorf("%s: not refused (diags %v)", v, d)
		}
	}
	plan := stateModel()
	var d diag.Diagnostics
	if r.refuseDowngrade(context.Background(), plan, state, &d) || d.HasError() {
		t.Errorf("unchanged version refused: %v", d)
	}
}

// A cluster upgraded outside Terraform keeps the recorded version (no diff back down, no
// replacement) and warns; a read at or below the recorded version follows the API.
func TestKeepRecordedVersion(t *testing.T) {
	for _, tc := range []struct {
		running, recorded, want string
		warn                    bool
	}{
		{"1.36", "1.35", "1.35", true},
		{"1.35", "1.35", "1.35", false},
		{"1.35", "1.36", "1.35", false}, // a stopped upgrade: state follows what the API reports
	} {
		m := KubernetesClusterModel{Version: types.StringValue(tc.running)}
		var d diag.Diagnostics
		keepRecordedVersion(&m, types.StringValue(tc.recorded), &d)
		if m.Version.ValueString() != tc.want || (d.WarningsCount() == 1) != tc.warn {
			t.Errorf("running %s recorded %s: version %s, warnings %v", tc.running, tc.recorded, m.Version, d)
		}
		if tc.warn && !strings.Contains(d.Warnings()[0].Detail(), `version = "1.36"`) {
			t.Errorf("warning does not say what to set: %s", d.Warnings()[0].Detail())
		}
	}
	m := KubernetesClusterModel{Version: types.StringValue("1.36")}
	var d diag.Diagnostics
	keepRecordedVersion(&m, types.StringNull(), &d) // import, or a failed upgrade: no record to keep
	if m.Version.ValueString() != "1.36" || d.WarningsCount() != 0 {
		t.Errorf("null record: %s %v", m.Version, d)
	}
}

func TestUpgradeRefusal(t *testing.T) {
	for _, tc := range []struct {
		err  *client.APIError
		want []string
	}{
		{
			&client.APIError{StatusCode: http.StatusNotImplemented, Code: "not_implemented", Message: "not offered"},
			[]string{"not available to this tenant", `Keep version = "1.35"`},
		},
		{
			&client.APIError{StatusCode: http.StatusBadRequest, Code: "invalid_input", Message: "unsupported kubernetes upgrade target"},
			[]string{"1.36 is not an upgrade target", "unsupported kubernetes upgrade target", "one minor version at a time"},
		},
		{
			&client.APIError{StatusCode: http.StatusConflict, Code: "pool_not_active", Message: "a node pool is not active"},
			[]string{"pool_not_active", "repair or delete it", "started nothing"},
		},
		{
			&client.APIError{StatusCode: http.StatusConflict, Code: "rollout_in_progress", Message: "busy"},
			[]string{"platform update of the cluster is running"},
		},
		{
			&client.APIError{StatusCode: http.StatusServiceUnavailable, Code: "service_unavailable", Message: "busy"},
			[]string{"This is temporary: apply again", "stays on 1.35"},
		},
	} {
		got := upgradeRefusal(tc.err, "1.35", "1.36").Error()
		for _, w := range tc.want {
			if !strings.Contains(got, w) {
				t.Errorf("%d %s: %q lacks %q", tc.err.StatusCode, tc.err.Code, got, w)
			}
		}
	}
	if got := upgradeRefusal(errors.New("dial tcp: refused"), "1.35", "1.36").Error(); !strings.Contains(got, "may or may not have started") {
		t.Errorf("transport error: %q", got)
	}
}

// A 403 for an IAM or scope deny, or for a policy-granted principal, says the change was not
// applied, who may upgrade, and that an access-policy API key cannot; it names the demanded
// permission only in the 3-part shape. A gate's 403 keeps the server's own message.
func TestUpgradeRefusalForbidden(t *testing.T) {
	const perm = "kubernetes:kubernetes-clusters:upgrade"
	common := []string{
		"version change to 1.36 was not applied", "kubernetes:kubernetes-clusters:upgrade and kubernetes:kubernetes-clusters:create",
		"the Owner and Admin roles hold them, and a group document or custom role", "access-policy API key cannot start or resume an upgrade",
		"fm auth login", "Never copy an fm sign-in into CI", "stays on 1.35",
	}
	for _, tc := range []struct {
		err          *client.APIError
		named, never bool // named: the permission is echoed in our sentence; never: the deny copy must not appear
		want         []string
	}{
		{err: &client.APIError{StatusCode: 403, Code: "insufficient_permission", Message: "denied", RequiredPermission: perm}, named: true},
		{err: &client.APIError{StatusCode: 403, Code: "INSUFFICIENT_PERMISSIONS", Message: "denied", RequiredPermission: "kubernetes:write"}},
		{err: &client.APIError{StatusCode: 403, Code: "insufficient_scope", Message: "denied", RequiredPermission: "kubernetes:x:<script>"}},
		{err: &client.APIError{StatusCode: 403, Code: "insufficient_permission", Message: "denied", RequiredPermission: "kubernetes:kubernetes-clusters:unclassified"}},
		{err: &client.APIError{StatusCode: 403, Code: "insufficient_permission", Message: "denied", RequiredPermission: "unclassified:kubernetes-clusters:upgrade"}},
		{err: &client.APIError{StatusCode: 403, Code: "policy_granted_not_supported", Message: "this operation cannot be requested by a policy-granted principal"}},
		{err: &client.APIError{StatusCode: 403, Code: "ACCOUNT_SUSPENDED", Message: "the account is suspended"}, never: true, want: []string{"ACCOUNT_SUSPENDED: the account is suspended", "stays on 1.35"}},
		{err: &client.APIError{StatusCode: 403, Code: "forbidden", Message: "retry from the portal"}, never: true, want: []string{"forbidden: retry from the portal", "stays on 1.35"}},
	} {
		got := upgradeRefusal(tc.err, "1.35", "1.36").Error()
		want := tc.want
		if !tc.never {
			want = append(common, tc.err.Message)
		}
		for _, w := range want {
			if !strings.Contains(got, w) {
				t.Errorf("%s: %q lacks %q", tc.err.Code, got, w)
			}
		}
		if tc.never && strings.Contains(got, "access-policy API key") {
			t.Errorf("%s: a gate 403 got the deny copy: %q", tc.err.Code, got)
		}
		if n := strings.Contains(got, "upgrade the cluster (required permission: "+perm+")"); n != tc.named {
			t.Errorf("%s: names permission = %v, want %v: %q", tc.err.Code, n, tc.named, got)
		}
		if strings.Count(got, "required permission:") > 1 || strings.Contains(got, "kubernetes:write") {
			t.Errorf("%s: the demand is echoed twice or in the 2-part shape: %q", tc.err.Code, got)
		}
		if strings.Contains(got, "unclassified") {
			t.Errorf("%s: echoed the gate's unclassified marker: %q", tc.err.Code, got)
		}
		if strings.Contains(got, "<script>") {
			t.Errorf("%s: echoed a malformed permission: %q", tc.err.Code, got)
		}
	}
}

func TestUpgradeRefusalTransport(t *testing.T) {
	if got := upgradeRefusal(errors.New("dial tcp: refused"), "1.35", "1.36").Error(); !strings.Contains(got, "may or may not have started") {
		t.Errorf("transport error: %q", got)
	}
}

func TestUpgradeStopped(t *testing.T) {
	for _, tc := range []struct {
		status, failure, want string
	}{
		{"upgrading", "pool_not_converged", "Apply again with the same version to resume"},
		{"running", "unknown", "Apply again to start a new upgrade"},
		{"running", "cp_member_missing", "short of a member"},
		{"upgrading", "commit_not_converged", "Frostmoln support"},
		{"running", "addon_not_verified", "move the pin"},
		{"upgrading", "", "upgradeFailure: unknown"},
	} {
		got := upgradeStopped(&apiKubernetesCluster{Status: tc.status, UpgradeFailure: tc.failure, KubernetesVersion: "1.35"}, "1.36").Error()
		if !strings.Contains(got, tc.want) || !strings.Contains(got, "on 1.35") {
			t.Errorf("%s/%s: %q lacks %q", tc.status, tc.failure, got, tc.want)
		}
	}
}

// An earlier apply timed out while the upgrade ran on: the next one waits on it and never
// requests it again (a POST would answer upgrade_in_progress).
func TestUpgradeCluster_AttachesToARunningUpgrade(t *testing.T) {
	var gets, posts atomic.Int32 // the handler runs on the server's goroutines
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			posts.Add(1)
			w.WriteHeader(http.StatusConflict)
			return
		}
		c := map[string]any{"id": "c-1", "status": "upgrading", "kubernetesVersion": "1.35", "targetKubernetesVersion": "1.36"}
		if gets.Add(1) > 2 {
			c = map[string]any{"id": "c-1", "status": "running", "kubernetesVersion": "1.36"}
		}
		writeJSON(t, w, c)
	}))
	defer srv.Close()
	c := client.NewClient(srv.URL, "test-key", client.WithHTTPClient(srv.Client())) // pragma: allowlist secret
	c.SetTenantIDForTest("t-1")
	if err := testResource(c).upgradeCluster(context.Background(), "c-1", "1.36", time.Now().Add(5*time.Second)); err != nil {
		t.Fatalf("upgradeCluster: %v", err)
	}
	if posts.Load() != 0 || gets.Load() < 3 {
		t.Errorf("posts = %d (want 0), gets = %d (want >= 3)", posts.Load(), gets.Load())
	}
}

// K8S-VERSION-UPGRADES-PLAN §3.1g decision 5: window_ended is a pause, worded by status, with a
// target that is never empty or invented.
func TestWindowEndedLine(t *testing.T) {
	for _, tc := range []struct {
		name string
		c    apiKubernetesCluster
		want string
	}{
		{
			"upgrading",
			apiKubernetesCluster{Status: "upgrading", TargetKubernetesVersion: "1.36"},
			"The upgrade to 1.36 continues in your next maintenance window. You can resume it now instead.",
		},
		{
			"running",
			apiKubernetesCluster{Status: "running", AvailableUpgrades: []string{"1.36", "1.37"}},
			"The upgrade to 1.36 did not fit in this maintenance window. It starts in your next window. You can start it now instead.",
		},
		{
			"upgrading, no target",
			apiKubernetesCluster{Status: "upgrading"},
			"The upgrade to the next Kubernetes version continues in your next maintenance window. You can resume it now instead.",
		},
		// A running cluster's target is the offered next minor only, never a stale target field.
		{
			"running, no target",
			apiKubernetesCluster{Status: "running", TargetKubernetesVersion: "1.36"},
			"The upgrade to the next Kubernetes version did not fit in this maintenance window. It starts in your next window. You can start it now instead.",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := windowEndedLine(&tc.c); got != tc.want {
				t.Errorf("got  %q\nwant %q", got, tc.want)
			}
		})
	}
}

// The refresh warns of a paused platform upgrade and never errors; any other reason adds nothing
// (Terraform's own apply reports those).
func TestWarnWindowEnded(t *testing.T) {
	for _, tc := range []struct {
		c        apiKubernetesCluster
		warnings int
	}{
		{apiKubernetesCluster{Status: "upgrading", TargetKubernetesVersion: "1.36", UpgradeFailure: "window_ended"}, 1},
		{apiKubernetesCluster{Status: "running", AvailableUpgrades: []string{"1.36"}, UpgradeFailure: "window_ended"}, 1},
		{apiKubernetesCluster{Status: "running", UpgradeFailure: "window_ended"}, 1},
		{apiKubernetesCluster{Status: "upgrading", UpgradeFailure: "window_ended"}, 1},
		{apiKubernetesCluster{Status: "upgrading", TargetKubernetesVersion: "1.36", UpgradeFailure: "pool_not_converged"}, 0},
		{apiKubernetesCluster{Status: "running"}, 0},
	} {
		var d diag.Diagnostics
		warnWindowEnded(&tc.c, &d)
		for _, w := range d.Warnings() {
			if hint := strings.Contains(w.Detail(), `set version = "1.36" and apply`); hint != (windowEndedTarget(&tc.c) != "") {
				t.Errorf("%s: apply hint = %v with target %q: %q", tc.c.Status, hint, windowEndedTarget(&tc.c), w.Detail())
			}
			if strings.Contains(w.Detail(), "set version") && windowEndedTarget(&tc.c) == "" {
				t.Errorf("%s: advises an apply with no known target: %q", tc.c.Status, w.Detail())
			}
		}
		if d.HasError() || d.WarningsCount() != tc.warnings {
			t.Errorf("%s/%s: errors=%v warnings=%d, want 0 errors and %d warnings", tc.c.Status, tc.c.UpgradeFailure, d.HasError(), d.WarningsCount(), tc.warnings)
		}
		for _, w := range d.Warnings() {
			if txt := strings.ToLower(w.Summary() + w.Detail()); strings.Contains(txt, "fail") || strings.Contains(txt, "error") {
				t.Errorf("window_ended reads as a failure: %q", txt)
			}
		}
	}
}

// upgradeFailureEnum is kubernetes api/openapi.yaml's upgradeFailure enum, pinned at v6.57.0
// (window_ended since v6.55.0). A new server value renders the generic line until it is added here.
var upgradeFailureEnum = []string{
	"unknown", "cp_member_missing", "pool_not_converged", "commit_not_converged", "upgrade_path_unavailable",
	"platform_images_not_converged", "addon_not_verified", "cluster_record_incomplete", "rollout_in_progress",
	"window_ended", "QuotaExceeded", "InsufficientCapacity", "InvalidState", "InvalidArgument", "ConfigurationError",
	"AmbiguousState", "Unimplemented",
}

// Every pinned reason renders on both statuses; only window_ended gets the pause copy, and it never
// reads as a failure.
func TestUpgradeStoppedCoversThePinnedEnum(t *testing.T) {
	for _, reason := range upgradeFailureEnum {
		for _, status := range []string{"upgrading", "running"} {
			c := &apiKubernetesCluster{
				Status: status, UpgradeFailure: reason, KubernetesVersion: "1.35",
				TargetKubernetesVersion: "1.36", AvailableUpgrades: []string{"1.36"},
			}
			got := upgradeStopped(c, "1.36").Error()
			if !strings.Contains(got, "upgradeFailure: "+reason) {
				t.Errorf("%s/%s: %q does not name the reason", status, reason, got)
			}
			if pause := strings.Contains(got, windowEndedLine(c)) && strings.Contains(got, " paused ("); pause != (reason == "window_ended") {
				t.Errorf("%s/%s: pause copy = %v: %q", status, reason, pause, got)
			}
			if reason == "window_ended" && (strings.Contains(got, "stopped") || strings.Contains(got, "fail")) {
				t.Errorf("%s/window_ended reads as a failure: %q", status, got)
			}
		}
	}
}

// Version raised to the target while a forced run to it is running: the apply waits on that run
// without a request, and when its window closes the apply did not reach its version, so it fails,
// worded as a pause with the resume advice (applying again resumes it with the ordinary request).
func TestUpgradeCluster_AttachedForcedRunPauses(t *testing.T) {
	var gets, posts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			posts.Add(1)
			w.WriteHeader(http.StatusConflict)
			return
		}
		c := map[string]any{"id": "c-1", "status": "upgrading", "kubernetesVersion": "1.35", "targetKubernetesVersion": "1.36"}
		if gets.Add(1) > 2 {
			c["upgradeFailure"] = "window_ended"
		}
		writeJSON(t, w, c)
	}))
	defer srv.Close()
	c := client.NewClient(srv.URL, "test-key", client.WithHTTPClient(srv.Client())) // pragma: allowlist secret
	c.SetTenantIDForTest("t-1")
	err := testResource(c).upgradeCluster(context.Background(), "c-1", "1.36", time.Now().Add(5*time.Second))
	want := "the upgrade to 1.36 paused (upgradeFailure: window_ended); the cluster is upgrading on 1.35. " +
		"The upgrade to 1.36 continues in your next maintenance window. You can resume it now instead. " +
		"Apply again with the same version to do that."
	if err == nil || err.Error() != want {
		t.Fatalf("err = %v\nwant %s", err, want)
	}
	if posts.Load() != 0 {
		t.Errorf("posts = %d, want 0", posts.Load())
	}
}

// A paused upgrade resumes only with its own version: another configured version starts nothing,
// and the refusal says paused, never stopped.
func TestUpgradeCluster_PausedRunNeedsItsOwnVersion(t *testing.T) {
	for _, tc := range []struct{ failure, want string }{
		{"window_ended", "an upgrade of this cluster to 1.36 paused until the next maintenance window (upgradeFailure: window_ended), and only that version can resume it. " +
			`Set version = "1.36" and apply again. Nothing was started`},
		{"pool_not_converged", "an upgrade of this cluster to 1.36 stopped (upgradeFailure: pool_not_converged), and only that version can resume it."},
	} {
		var posts atomic.Int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodPost {
				posts.Add(1)
				w.WriteHeader(http.StatusConflict)
				return
			}
			writeJSON(t, w, map[string]any{
				"id": "c-1", "status": "upgrading", "kubernetesVersion": "1.35",
				"targetKubernetesVersion": "1.36", "upgradeFailure": tc.failure,
			})
		}))
		c := client.NewClient(srv.URL, "test-key", client.WithHTTPClient(srv.Client())) // pragma: allowlist secret
		c.SetTenantIDForTest("t-1")
		err := testResource(c).upgradeCluster(context.Background(), "c-1", "1.37", time.Now().Add(5*time.Second))
		srv.Close()
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want %q", tc.failure, err, tc.want)
		}
		if posts.Load() != 0 {
			t.Errorf("%s: posts = %d, want 0", tc.failure, posts.Load())
		}
	}
}
