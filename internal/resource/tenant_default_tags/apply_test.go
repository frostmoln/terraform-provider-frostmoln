package tenant_default_tags

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/tagsettings/tagsettingstest"
)

// apply_to_existing_on_change (RESOURCE-TAGS-PLAN Phase E, D8): the apply runs
// only when `tags` CHANGES to a non-empty set — on create or update, never on a
// flag-only change or a destroy.

func fastApply(t *testing.T) {
	t.Helper()
	interval, timeout := applyPollInterval, applyWaitTimeout
	applyPollInterval, applyWaitTimeout = 5*time.Millisecond, 10*time.Second
	t.Cleanup(func() { applyPollInterval, applyWaitTimeout = interval, timeout })
}

// calls is the recorded requests that decide something — the default-tag
// writes, the apply POSTs and the operation reads — in order, as "METHOD kind".
func calls(f *tagsettingstest.Fake) []string {
	var out []string
	for _, r := range f.Requests() {
		switch {
		case r.Method == "PUT" && strings.HasSuffix(r.Path, "/default-tags"):
			out = append(out, "PUT default-tags")
		case r.Method == "POST" && strings.HasSuffix(r.Path, "/default-tags/apply"):
			out = append(out, "POST apply")
		case r.Method == "GET" && strings.Contains(r.Path, "/operations/"):
			out = append(out, "GET operation")
		}
	}
	return out
}

func posts(f *tagsettingstest.Fake) int {
	n := 0
	for _, c := range calls(f) {
		if c == "POST apply" {
			n++
		}
	}
	return n
}

func update(t *testing.T, r *tenantDefaultTagsResource, prior tfsdk.State, plan tftypes.Value) *resource.UpdateResponse {
	t.Helper()
	resp := &resource.UpdateResponse{State: prior}
	r.Update(context.Background(), resource.UpdateRequest{State: prior, Plan: tfsdk.Plan{Schema: tdSchema(t), Raw: plan}}, resp)
	return resp
}

func flagOf(t *testing.T, st tfsdk.State) bool {
	t.Helper()
	var m TenantDefaultTagsModel
	if d := st.Get(context.Background(), &m); d.HasError() {
		t.Fatalf("state: %v", d.Errors())
	}
	return m.ApplyToExistingOnChange.ValueBool()
}

// A create with the flag writes the set, THEN starts the apply (no body) and
// waits for it; nothing failed, so nothing is said.
func TestCreate_WithFlagAppliesToExistingResources(t *testing.T) {
	fastApply(t)
	f := tagsettingstest.New(t)
	f.ApplyPolls = 2
	r := configured(t, f)

	resp := create(t, r, objFlag(t, unknown, unknown, map[string]string{"env": "prod"}, flagOn))
	mustOK(t, "create", resp.Diagnostics)
	got := calls(f)
	if len(got) < 4 || got[0] != "PUT default-tags" || got[1] != "POST apply" || got[len(got)-1] != "GET operation" || posts(f) != 1 {
		t.Fatalf("want PUT, then one POST apply, then its operation read to the end; got %v", got)
	}
	for _, req := range f.Only("POST") {
		if len(req.Body) != 0 {
			t.Errorf("the apply POST carried a body %q; the route takes none", req.Body)
		}
	}
	if len(resp.Diagnostics) != 0 {
		t.Errorf("a clean apply is not worth a diagnostic, got:\n%s", diagsText(resp.Diagnostics))
	}
	if !flagOf(t, resp.State) {
		t.Error("the flag must be recorded in state")
	}
}

// The flag is off by default, and an empty set has nothing to apply.
func TestCreate_ApplyOnlyWhenFlaggedAndNonEmpty(t *testing.T) {
	fastApply(t)
	for _, tc := range []struct {
		name string
		tags map[string]string
		flag tftypes.Value
	}{
		{"flag off", map[string]string{"env": "prod"}, flagOff},
		{"empty set", map[string]string{}, flagOn},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := tagsettingstest.New(t)
			r := configured(t, f)
			resp := create(t, r, objFlag(t, unknown, unknown, tc.tags, tc.flag))
			mustOK(t, "create", resp.Diagnostics)
			if n := posts(f); n != 0 {
				t.Errorf("an apply was started: %v", calls(f))
			}
		})
	}
}

// D8: a plan that only flips the flag writes nothing and starts nothing.
func TestUpdate_FlagOnlyChangeWritesAndStartsNothing(t *testing.T) {
	fastApply(t)
	f := tagsettingstest.New(t)
	r := configured(t, f)
	tags := map[string]string{"env": "prod"}
	created := create(t, r, objFlag(t, unknown, unknown, tags, flagOff))
	mustOK(t, "create", created.Diagnostics)

	for _, flag := range []tftypes.Value{flagOn, flagOff} {
		f.Reset()
		upd := update(t, r, created.State, objFlag(t, str(f.TenantID), str(f.TenantID), tags, flag))
		mustOK(t, "update", upd.Diagnostics)
		if got := f.Requests(); len(got) != 0 {
			t.Errorf("a flag-only change must not call the platform, got %+v", got)
		}
		var want bool
		_ = flag.As(&want)
		if flagOf(t, upd.State) != want {
			t.Errorf("the new flag must be recorded in state (want %v)", want)
		}
		created = &resource.CreateResponse{State: upd.State}
	}
}

// An update applies only when `tags` changed to a non-empty set, and the flag is on.
func TestUpdate_AppliesOnlyWhenTagsChange(t *testing.T) {
	fastApply(t)
	cases := []struct {
		name    string
		tags    map[string]string
		flag    tftypes.Value
		applies bool
	}{
		{"tags changed, flag on", map[string]string{"env": "staging"}, flagOn, true},
		{"tags changed, flag off", map[string]string{"env": "staging"}, flagOff, false},
		{"tags cleared, flag on", map[string]string{}, flagOn, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := tagsettingstest.New(t)
			r := configured(t, f)
			created := create(t, r, objFlag(t, unknown, unknown, map[string]string{"env": "prod"}, tc.flag))
			mustOK(t, "create", created.Diagnostics)
			f.Reset()

			upd := update(t, r, created.State, objFlag(t, str(f.TenantID), str(f.TenantID), tc.tags, tc.flag))
			mustOK(t, "update", upd.Diagnostics)
			if got := posts(f); (got == 1) != tc.applies || got > 1 {
				t.Errorf("apply POSTs = %d, want applies=%v; calls %v", got, tc.applies, calls(f))
			}
			if len(f.Only("PUT")) != 1 {
				t.Errorf("a tags change must write the set once, got %v", calls(f))
			}
		})
	}
}

// Unchanged tags with the flag on (a refresh-only apply, say) starts nothing.
func TestUpdate_UnchangedTagsWithFlagOnStartsNothing(t *testing.T) {
	fastApply(t)
	f := tagsettingstest.New(t)
	r := configured(t, f)
	tags := map[string]string{"env": "prod"}
	created := create(t, r, objFlag(t, unknown, unknown, tags, flagOn))
	mustOK(t, "create", created.Diagnostics)
	f.Reset()
	upd := update(t, r, created.State, objFlag(t, str(f.TenantID), str(f.TenantID), tags, flagOn))
	mustOK(t, "update", upd.Diagnostics)
	if len(f.Requests()) != 0 {
		t.Errorf("unchanged tags must not call the platform, got %v", calls(f))
	}
}

// Destroying never applies anything, whatever the flag says.
func TestDelete_NeverApplies(t *testing.T) {
	fastApply(t)
	f := tagsettingstest.New(t)
	r := configured(t, f)
	created := create(t, r, objFlag(t, unknown, unknown, map[string]string{"env": "prod"}, flagOn))
	mustOK(t, "create", created.Diagnostics)
	f.Reset()
	del := &resource.DeleteResponse{State: created.State}
	r.Delete(context.Background(), resource.DeleteRequest{State: created.State}, del)
	mustOK(t, "delete", del.Diagnostics)
	if posts(f) != 0 {
		t.Errorf("a destroy started an apply: %v", calls(f))
	}
}

// A run already going (409): wait for THAT operation to end — however it
// ends — then start ours, once.
func TestApply_InProgressWaitsThenStartsOnce(t *testing.T) {
	fastApply(t)
	for _, outcome := range []string{"completed", "failed"} {
		t.Run("the running apply "+outcome, func(t *testing.T) {
			f := tagsettingstest.New(t)
			r := configured(t, f)
			f.StartForeignApply(f.TenantID, 2, outcome)

			resp := create(t, r, objFlag(t, unknown, unknown, map[string]string{"env": "prod"}, flagOn))
			mustOK(t, "create", resp.Diagnostics)
			got := calls(f)
			if posts(f) != 2 {
				t.Fatalf("want exactly two POSTs (refused, then started), got %v", got)
			}
			// PUT, POST (409), the running operation read to its end, POST, ours.
			second := -1
			for i := 2; i < len(got); i++ {
				if got[i] == "POST apply" {
					second = i
					break
				}
			}
			if got[0] != "PUT default-tags" || got[1] != "POST apply" || second < 5 || got[len(got)-1] != "GET operation" {
				t.Errorf("want the running operation waited out (3 reads) before the second POST, got %v", got)
			}
			if len(resp.Diagnostics) != 0 {
				t.Errorf("our own run completed cleanly, got:\n%s", diagsText(resp.Diagnostics))
			}
		})
	}
}

// The re-POST happens ONCE: if another run started while we waited, it is not
// chased. It started after this resource's write, so it applies these
// defaults: a warning naming it, not an error (which would taint a create).
func TestApply_InProgressAgainWarnsNamingTheOtherRun(t *testing.T) {
	fastApply(t)
	f := tagsettingstest.New(t)
	r := configured(t, f)
	f.StartForeignApply(f.TenantID, 1, "completed")
	f.ForeignRestarts = 1

	resp := create(t, r, objFlag(t, unknown, unknown, map[string]string{"env": "prod"}, flagOn))
	if resp.Diagnostics.HasError() {
		t.Fatalf("another run applying these defaults is not an error:\n%s", diagsText(resp.Diagnostics))
	}
	if ws := resp.Diagnostics.Warnings(); len(ws) != 1 || !strings.Contains(ws[0].Detail(), "started while Terraform") ||
		!strings.Contains(ws[0].Detail(), tagsettingstest.ApplyOperationID(f.TenantID)) {
		t.Fatalf("want one warning naming the run started meanwhile, got:\n%s", diagsText(resp.Diagnostics))
	}
	if posts(f) != 2 {
		t.Errorf("want exactly two POSTs, got %v", calls(f))
	}
	if resp.State.Raw.IsNull() {
		t.Error("the defaults were written: state must be saved even though the apply failed")
	}
}

// D7: a completed run with failed resources is a WARNING naming the summary,
// the failures and the skipped count — never an error.
func TestApply_CompletedWithFailuresWarns(t *testing.T) {
	fastApply(t)
	f := tagsettingstest.New(t)
	f.ApplyFailures = []tagsettingstest.ApplyFailure{
		{ResourceType: "instance", ResourceID: "inst-7", Reason: "invalid_state",
			Message: "the resource is in a state that does not accept a tag update right now; run the apply again once it is ready"},
		{ResourceType: "bucket", Reason: "backend_unavailable",
			Message: "the service that owns this resource could not be reached; run the apply again later"},
		{ResourceType: "volume", ResourceID: "vol-1", Reason: "some_future_reason", Message: "m"},
	}
	f.ApplySkipped = 2
	r := configured(t, f)

	resp := create(t, r, objFlag(t, unknown, unknown, map[string]string{"env": "prod"}, flagOn))
	if resp.Diagnostics.HasError() {
		t.Fatalf("failed resources must not fail the apply:\n%s", diagsText(resp.Diagnostics))
	}
	ws := resp.Diagnostics.Warnings()
	if len(ws) != 1 {
		t.Fatalf("want one warning, got:\n%s", diagsText(resp.Diagnostics))
	}
	w := ws[0].Detail()
	for _, want := range []string{
		"3 could not be updated", "3 updated", "2 skipped", "3 failed",
		"instance inst-7", "does not accept a tag update", "invalid_state",
		"bucket (all of this type)", "could not be reached",
		"some_future_reason",
		"2 resource(s) were skipped: managed by the platform",
	} {
		if !strings.Contains(w, want) {
			t.Errorf("the warning must say %q:\n%s", want, w)
		}
	}
}

// A run that FAILED is an ERROR, raised after the state is saved: the defaults
// were written, and a create is then tainted.
func TestApply_FailedRunIsAnErrorWithStateSaved(t *testing.T) {
	fastApply(t)
	for _, outcome := range []string{"failed"} {
		t.Run(outcome, func(t *testing.T) {
			f := tagsettingstest.New(t)
			f.ApplyOutcome = outcome
			r := configured(t, f)
			resp := create(t, r, objFlag(t, unknown, unknown, map[string]string{"env": "prod"}, flagOn))
			if !resp.Diagnostics.HasError() {
				t.Fatalf("a %s run must be an error, got:\n%s", outcome, diagsText(resp.Diagnostics))
			}
			for _, want := range []string{"The default tags were saved", "run `fm tenant default-tags apply`"} {
				if !strings.Contains(diagsText(resp.Diagnostics), want) {
					t.Errorf("the error must say %q:\n%s", want, diagsText(resp.Diagnostics))
				}
			}
			if resp.State.Raw.IsNull() {
				t.Fatal("state must be saved before the error, so the create is tainted rather than lost")
			}
			if _, tags := stateOf(t, resp.State); tags["env"] != "prod" {
				t.Errorf("state = %v", tags)
			}
		})
	}
}

// The platform unable to START the apply right now (503, or a gateway 502/504)
// is a WARNING with state saved: an error would taint the create, and its
// replacement would briefly clear the tenant's defaults for a cause that
// clears on its own.
func TestApply_RetryableRefusalWarnsWithoutTaint(t *testing.T) {
	fastApply(t)
	for _, status := range []int{503, 502, 504} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			f := tagsettingstest.New(t)
			f.RefuseApply = status
			r := configured(t, f)
			resp := create(t, r, objFlag(t, unknown, unknown, map[string]string{"env": "prod"}, flagOn))
			if resp.Diagnostics.HasError() {
				t.Fatalf("a %d on the apply POST must not fail (and so taint) the create:\n%s", status, diagsText(resp.Diagnostics))
			}
			ws := resp.Diagnostics.Warnings()
			if len(ws) != 1 || !strings.Contains(ws[0].Detail(), "apply to existing resources could not start") ||
				!strings.Contains(ws[0].Detail(), "fm tenant default-tags apply") {
				t.Fatalf("want one warning that it could not start, got:\n%s", diagsText(resp.Diagnostics))
			}
			if _, tags := stateOf(t, resp.State); tags["env"] != "prod" || !flagOf(t, resp.State) {
				t.Errorf("state must be saved normally, got %v", tags)
			}
			if posts(f) != 1 {
				t.Errorf("want one POST, got %v", calls(f))
			}
		})
	}
}

// A refusal a retry does not change stays an ERROR, raised after the state is
// saved (so a create is tainted).
func TestApply_PermanentRefusalIsAnError(t *testing.T) {
	fastApply(t)
	for _, status := range []int{403, 400, 422, 500} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			f := tagsettingstest.New(t)
			f.RefuseApply = status
			r := configured(t, f)
			resp := create(t, r, objFlag(t, unknown, unknown, map[string]string{"env": "prod"}, flagOn))
			if !resp.Diagnostics.HasError() || !strings.Contains(diagsText(resp.Diagnostics), "The default tags were saved") {
				t.Fatalf("a %d must be an error saying the defaults were saved, got:\n%s", status, diagsText(resp.Diagnostics))
			}
			if resp.State.Raw.IsNull() {
				t.Fatal("state must be saved before the error")
			}
		})
	}
}

// Terraform stopping its wait while the operation is still pending or running
// is not the run failing: a warning naming the operation, state saved. The
// wait is the timeouts block's, per verb, else the default.
func TestApply_StoppedWaitingWarns(t *testing.T) {
	fastApply(t)
	f := tagsettingstest.New(t)
	f.ApplyPolls = 1 << 20
	r := configured(t, f)
	op := tagsettingstest.ApplyOperationID(f.TenantID)
	check := func(what string, d diag.Diagnostics, st tfsdk.State) {
		t.Helper()
		if d.HasError() {
			t.Fatalf("%s: a run still going is not an error:\n%s", what, diagsText(d))
		}
		ws := d.Warnings()
		if len(ws) != 1 || !strings.Contains(ws[0].Detail(), "apply to existing resources is still running (operation "+op+
			"); it continues in the background") {
			t.Errorf("%s: want one warning that it is still running, got:\n%s", what, diagsText(d))
		}
		if st.Raw.IsNull() {
			t.Errorf("%s: state must be saved", what)
		}
	}

	// timeouts.create bounds the create's wait (the default is far longer here).
	start := time.Now()
	created := create(t, r, objTimeouts(t, objFlag(t, unknown, unknown, map[string]string{"env": "prod"}, flagOn), "50ms", ""))
	check("create", created.Diagnostics, created.State)
	if time.Since(start) > 5*time.Second {
		t.Errorf("the create waited %s: timeouts.create was not used", time.Since(start))
	}

	// timeouts.update bounds an update's.
	f.StartForeignApply(f.TenantID, 0, "completed") // the create's run is over
	start = time.Now()
	upd := update(t, r, created.State, objTimeouts(t,
		objFlag(t, str(f.TenantID), str(f.TenantID), map[string]string{"env": "staging"}, flagOn), "", "60ms"))
	check("update", upd.Diagnostics, upd.State)
	if time.Since(start) > 5*time.Second {
		t.Errorf("the update waited %s: timeouts.update was not used", time.Since(start))
	}

	// With no block, the default applies.
	applyWaitTimeout = 50 * time.Millisecond
	f2 := tagsettingstest.New(t)
	f2.ApplyPolls = 1 << 20
	r2 := configured(t, f2)
	op = tagsettingstest.ApplyOperationID(f2.TenantID)
	def := create(t, r2, objFlag(t, unknown, unknown, map[string]string{"env": "prod"}, flagOn))
	check("create, default budget", def.Diagnostics, def.State)
}

// The flag is the configuration's: a refresh keeps it, and a null one (after an
// import, or from an older provider's state) reads as its default.
func TestRead_KeepsTheFlagAndDefaultsANullOne(t *testing.T) {
	f := tagsettingstest.New(t)
	r := configured(t, f)
	s := tdSchema(t)
	for _, tc := range []struct {
		flag tftypes.Value
		want bool
	}{{flagOn, true}, {flagOff, false}, {tftypes.NewValue(tftypes.Bool, nil), false}} {
		st := tfsdk.State{Schema: s, Raw: objFlag(t, str(f.TenantID), str(f.TenantID), map[string]string{}, tc.flag)}
		resp := &resource.ReadResponse{State: st}
		r.Read(context.Background(), resource.ReadRequest{State: st}, resp)
		mustOK(t, "read", resp.Diagnostics)
		var m TenantDefaultTagsModel
		_ = resp.State.Get(context.Background(), &m)
		if m.ApplyToExistingOnChange.IsNull() || m.ApplyToExistingOnChange.ValueBool() != tc.want {
			t.Errorf("flag %v read back as %v, want %v", tc.flag, m.ApplyToExistingOnChange, tc.want)
		}
	}
}

// The page a practitioner reads before turning the flag on says what it does
// and does not do.
func TestSchema_DescribesApplyToExisting(t *testing.T) {
	s := tdSchema(t)
	for _, want := range []string{
		"apply_to_existing_on_change",
		"adds only the default keys a resource is missing",
		"changing a default's value does not reach resources that already carry the key",
		"`depends_on`",
		"Resources the platform manages",
		"boot volume",
		"instance snapshots and images are not included",
		"appears in `tags_all`, never in `tags`, and plans no diff",
		"`timeouts.create` and `timeouts.update`",
		"still running and continues in the background",
		"cannot start it right now (it is unavailable), the apply also succeeds, with a warning",
		"refuses it (no permission, the tenant is not provisioned yet, or the defaults are not valid), the apply fails",
		"before v0.62.0 remove it",
	} {
		if !strings.Contains(s.Description, want) {
			t.Errorf("the resource description must say %q", want)
		}
	}
	attr := s.Attributes["apply_to_existing_on_change"]
	if attr == nil || !attr.IsOptional() || !attr.IsComputed() || attr.IsRequired() {
		t.Fatalf("apply_to_existing_on_change must be Optional+Computed, got %+v", attr)
	}
	for _, want := range []string{"changes `tags` to a non-empty set", "alone changes nothing", "Default `false`"} {
		if !strings.Contains(attr.GetDescription(), want) {
			t.Errorf("the flag's description must say %q", want)
		}
	}
}
