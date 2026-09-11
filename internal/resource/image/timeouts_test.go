package image

import (
	"testing"
	"time"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/timeouts"
)

// TestResolveBudgetsDefaultsPinTodaysConstants pins the timeouts block's
// fallback: with no block configured, every verb budgets at the value this
// resource has always hardcoded, and a test's pollTimeout injection still
// shrinks the default (the resolveBudgets seam keeps the harness working).
func TestResolveBudgetsDefaultsPinTodaysConstants(t *testing.T) {
	for _, tc := range []struct {
		name                string
		pollTimeout         time.Duration
		wantPollTimeout     time.Duration
		deleteRetryInterval time.Duration
		wantDeleteRetry     time.Duration
	}{
		{
			"defaults (nothing injected)",
			0, defaultPollTimeout,
			0, 30 * time.Second,
		},
		{
			"harness injection shrinks the default",
			5 * time.Second, 5 * time.Second,
			time.Millisecond, time.Millisecond,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := &imageResource{pollTimeout: tc.pollTimeout, deleteRetryInterval: tc.deleteRetryInterval}

			if got := r.resolveBudgets(nil); got != timeouts.Uniform(tc.wantPollTimeout) {
				t.Errorf("resolveBudgets(nil) = %+v, want %+v", got, timeouts.Uniform(tc.wantPollTimeout))
			}
			if got := r.getPollTimeout(); got != tc.wantPollTimeout {
				t.Errorf("getPollTimeout() = %s, want %s", got, tc.wantPollTimeout)
			}
			if got := r.getDeleteRetryInterval(); got != tc.wantDeleteRetry {
				t.Errorf("getDeleteRetryInterval() = %s, want %s", got, tc.wantDeleteRetry)
			}
		})
	}

	// deleteRetryInterval is an INTERVAL (provider-internal destroy pacing),
	// not a budget: it must never change what the timeouts block's delete
	// budget resolves to.
	r := &imageResource{deleteRetryInterval: time.Millisecond}
	budgets := r.resolveBudgets(nil)
	if budgets.Delete != defaultPollTimeout {
		t.Errorf("delete budget = %s, want the default %s — the retry interval must not leak into the budget", budgets.Delete, defaultPollTimeout)
	}
}
