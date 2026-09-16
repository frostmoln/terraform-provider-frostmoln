package postgres_backup

import (
	"testing"
	"time"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/timeouts"
)

func TestGetPollDefaults(t *testing.T) {
	r := &postgresBackupResource{}
	if r.getPollInterval() != 5*time.Second {
		t.Errorf("expected default poll interval 5s, got %v", r.getPollInterval())
	}
	if r.getPollTimeout() != 2*time.Hour {
		t.Errorf("expected default poll timeout 2h (the platform's backupPollDeadline), got %v", r.getPollTimeout())
	}
}

// TestResolveBudgetsDefaultsPinTodaysConstants pins the timeouts block's
// fallback: with no block configured, every verb budgets at the value this
// resource has always hardcoded (only create has a wait to bound), and a
// test's pollTimeout injection still shrinks the default.
func TestResolveBudgetsDefaultsPinTodaysConstants(t *testing.T) {
	bare := (&postgresBackupResource{}).resolveBudgets(nil)
	if want := timeouts.Uniform(2 * time.Hour); bare != want {
		t.Errorf("resolveBudgets(nil) = %+v, want %+v", bare, want)
	}

	r := &postgresBackupResource{pollTimeout: time.Second}
	if got := r.resolveBudgets(nil); got != timeouts.Uniform(time.Second) {
		t.Errorf("an injected pollTimeout must stay the default budget, got %+v", got)
	}
}
