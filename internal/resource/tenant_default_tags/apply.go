package tenant_default_tags

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/diag"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/tagsettings"
)

// apply_to_existing_on_change (RESOURCE-TAGS-PLAN Phase E, D8): after an apply
// that CHANGED `tags` to a non-empty set, the provider asks the platform to
// stamp the new defaults onto the tenant's existing resources and waits for it.
//
// Outcomes, and why each is what it is:
//
//   - completed, nothing failed: silent.
//   - completed with failed resources: WARNINGS (D7) — a partly tagged tenant is
//     progress, and a re-run is idempotent.
//   - the platform REFUSED the apply for a reason a retry does not change (400
//     tenant not provisioned, 403 no permission, 422 invalid defaults, another
//     4xx or a plain 500), or an operation reported failed: an ERROR, raised
//     after the state is saved (the default tags themselves were written), so
//     a create is tainted and replaced on the next apply, which runs it again.
//   - the platform could not START it right now (503, a gateway 502/504, no
//     answer at all — tagsettings.ApplyRefusalIsRetryable): a WARNING, state
//     saved normally. An error would taint a create whose setting took effect,
//     and its replacement would briefly clear the tenant's defaults: the harm
//     the timeout case avoids, for a cause that clears on its own.
//   - Terraform stopped waiting (the timeouts block, applyWaitTimeout by
//     default) while the operation is still pending or running: a WARNING. The
//     platform sets no deadline of its own and the run goes on; failing here
//     would taint a create whose setting took effect, and its replacement would
//     briefly clear the tenant's defaults.
//   - another run started while Terraform waited out the previous one: a
//     WARNING. It started after this resource's write, so it applies these
//     defaults.

// applyWaitTimeout is the default wait for the apply, per verb, when the
// timeouts block does not set one. A var so tests can shorten it.
var applyWaitTimeout = 30 * time.Minute

// applyPollInterval is the poll cadence while the event stream is not
// connected. A var so tests can shorten it.
var applyPollInterval = 5 * time.Second

// applyFailuresShown is how many failed resources a warning names.
const applyFailuresShown = 10

// applyToExisting runs the apply for tenantID, waiting at most budget, and
// reports its outcome as diagnostics. It never returns an error: the caller has
// already saved state.
func (r *tenantDefaultTagsResource) applyToExisting(ctx context.Context, tenantID string, budget time.Duration, diags *diag.Diagnostics) {
	deadline := time.Now().Add(budget)
	saved := "The default tags were saved; only applying them to existing resources "
	// retry is how to run it again: a create that fails here is tainted and
	// replaced, but an update error taints nothing, so the next plan shows no
	// change and would not retry it on its own.
	retry := " To apply them to existing resources now, run `fm tenant default-tags apply`."
	stillRunning := func(opID string, err error) {
		diags.AddWarning("Apply to Existing Resources Still Running",
			fmt.Sprintf("The default tags were saved. The apply to existing resources is still running (operation %s); "+
				"it continues in the background. Terraform stopped waiting after %s (`timeouts`). (%s)", opID, budget, err))
	}

	opID, err := tagsettings.ApplyDefaultTags(ctx, r.client, tenantID)
	var busy *tagsettings.ApplyInProgressError
	if errors.As(err, &busy) {
		if busy.OperationID == "" {
			diags.AddError("Default Tags Not Applied to Existing Resources",
				saved+"failed: "+busy.Error()+", and the platform did not name it, so there is nothing to wait for."+retry)
			return
		}
		// Wait for the running apply to END, however it ends, then start ours
		// ONCE: that run may have read older defaults, so it cannot stand in for
		// this one.
		if _, werr := r.client.WaitForOperation(ctx, busy.OperationID, applyPollInterval, remaining(deadline)); stoppedWaiting(werr) {
			diags.AddWarning("Default Tags Not Yet Applied to Existing Resources",
				fmt.Sprintf("%swas not started: another apply of the tenant's default tags (operation %s) was still "+
					"running when Terraform stopped waiting after %s (`timeouts`). It may have read the defaults from "+
					"before this change.%s (%s)", saved, busy.OperationID, budget, retry, werr))
			return
		}
		opID, err = tagsettings.ApplyDefaultTags(ctx, r.client, tenantID)
		if errors.As(err, &busy) {
			diags.AddWarning("Default Tags Being Applied to Existing Resources by Another Run",
				fmt.Sprintf("The default tags were saved. Another apply of them (operation %s) started while Terraform "+
					"waited for the previous one, after this change was written, so it applies these defaults; "+
					"Terraform did not start its own.", busy.OperationID))
			return
		}
	}
	if tagsettings.ApplyRefusalIsRetryable(err) {
		diags.AddWarning("Apply to Existing Resources Could Not Start",
			fmt.Sprintf("The default tags were saved, but the apply to existing resources could not start (%s). Run "+
				"`fm tenant default-tags apply` once the platform is available: a later `terraform apply` starts it "+
				"again only when `tags` changes.", err))
		return
	}
	if err != nil {
		diags.AddError("Default Tags Not Applied to Existing Resources", saved+"failed: "+err.Error()+"."+retry)
		return
	}

	op, err := r.client.WaitForOperation(ctx, opID, applyPollInterval, remaining(deadline))
	if stoppedWaiting(err) {
		stillRunning(opID, err)
		return
	}
	if err != nil {
		diags.AddError("Default Tags Not Applied to Existing Resources", saved+"failed: "+err.Error()+"."+retry)
		return
	}
	result, err := tagsettings.DecodeApplyResult(op.Result)
	if err != nil {
		diags.AddWarning("Default Tags Applied to Existing Resources, Result Unknown",
			fmt.Sprintf("The apply to existing resources (operation %s) completed, but %s.", opID, err))
		return
	}
	if msg, ok := applyFailuresWarning(result); ok {
		diags.AddWarning("Some Existing Resources Did Not Get the Default Tags", msg)
	}
}

// stoppedWaiting reports whether a wait ended because TERRAFORM stopped
// waiting (its timeout, or the apply being interrupted) rather than because the
// operation ended. client.WaitForOperation retries every poll error until its
// deadline, so any other error it returns is the operation's own terminal
// failure.
func stoppedWaiting(err error) bool {
	return errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled)
}

// applyFailuresWarning words a completed apply that left resources behind:
// the summary, the first failures, and how many were skipped. ok is false when
// nothing failed.
func applyFailuresWarning(r *tagsettings.ApplyResult) (string, bool) {
	if r.Summary.Failed == 0 && len(r.Failures) == 0 {
		return "", false
	}
	var b strings.Builder
	fmt.Fprintf(&b, "The default tags were saved and applied to the tenant's existing resources, but %d could not be "+
		"updated. Of %d examined: %d updated, %d already had every default key, %d skipped, %d failed.",
		r.Summary.Failed, r.Summary.Examined, r.Summary.Updated, r.Summary.Unchanged, r.Summary.Skipped, r.Summary.Failed)

	shown := r.Failures
	if len(shown) > applyFailuresShown {
		shown = shown[:applyFailuresShown]
	}
	b.WriteString("\n\nFailed:")
	for _, f := range shown {
		id := f.ResourceID
		if id == "" {
			id = "(all of this type)"
		}
		fmt.Fprintf(&b, "\n  - %s %s: %s (%s)", f.ResourceType, id, f.Message, f.Reason)
	}
	if more := r.Summary.Failed - len(shown); more > 0 {
		fmt.Fprintf(&b, "\n  - and %d more", more)
	}
	if r.Summary.Skipped > 0 {
		fmt.Fprintf(&b, "\n\n%d resource(s) were skipped: %s.", r.Summary.Skipped, skipReasons(r.Skipped))
	}
	b.WriteString("\n\nAn apply adds only missing keys, so running it again (`fm tenant default-tags apply`) retries " +
		"what failed and leaves the rest alone.")
	return b.String(), true
}

// skipReasons names the skip reasons among the listed skipped resources, in
// words where the reason is known and as sent where it is not.
func skipReasons(skipped []tagsettings.ApplySkip) string {
	words := map[string]string{
		"platform_managed": "managed by the platform",
		"tag_budget":       "the added tags would exceed the resource's tag limit",
	}
	seen := map[string]bool{}
	var out []string
	for _, s := range skipped {
		if seen[s.Reason] {
			continue
		}
		seen[s.Reason] = true
		w, ok := words[s.Reason]
		if !ok {
			w = s.Reason
		}
		out = append(out, w)
	}
	if len(out) == 0 {
		return "the platform manages them, or the added tags would exceed their tag limit"
	}
	sort.Strings(out)
	return strings.Join(out, "; ")
}

// remaining is the time left until deadline, never zero or less: a zero
// timeout means "the default" to client.WaitForOperation, and an exhausted
// budget must end the wait, not restart it.
func remaining(deadline time.Time) time.Duration {
	return max(time.Until(deadline), time.Nanosecond)
}
