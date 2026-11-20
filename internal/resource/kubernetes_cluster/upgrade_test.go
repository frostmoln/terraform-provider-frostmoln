package kubernetes_cluster

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
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
	var gets, posts int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			posts++
			w.WriteHeader(http.StatusConflict)
			return
		}
		gets++
		c := map[string]any{"id": "c-1", "status": "upgrading", "kubernetesVersion": "1.35", "targetKubernetesVersion": "1.36"}
		if gets > 2 {
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
	if posts != 0 || gets < 3 {
		t.Errorf("posts = %d (want 0), gets = %d (want >= 3)", posts, gets)
	}
}
