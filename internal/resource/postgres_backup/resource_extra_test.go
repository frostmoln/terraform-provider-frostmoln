package postgres_backup

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"

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

// A `base` row reaches Read by import, or when an id already in state turns
// out to name one. It must be refused with a sentence about the PLATFORM —
// base backups are taken by the platform for point-in-time recovery and the
// customer neither creates nor deletes them — rather than by the schema's
// OneOf("full") validator, whose message is about this provider's own
// allow-list and tells a practitioner nothing about what to do next.
func TestReadRefusesABaseBackupWithAnExplanation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": "bk-1", "instanceId": "db-1", "name": "auto-base",
			"type": "base", "status": "completed",
		})
	}))
	defer server.Close()

	r := newResource(newClient(t, server))
	state := buildState(t, PostgresBackupModel{
		ID:         types.StringValue("bk-1"),
		InstanceID: types.StringValue("db-1"),
		Name:       types.StringValue("auto-base"),
		Type:       types.StringValue("full"),
		Status:     types.StringValue("completed"),
	})

	readResp := resource.ReadResponse{State: state}
	r.Read(context.Background(), resource.ReadRequest{State: state}, &readResp)

	if !readResp.Diagnostics.HasError() {
		t.Fatal("reading a base row must be refused")
	}
	d := readResp.Diagnostics.Errors()[0]
	if d.Summary() != "Base backups are managed by the platform" {
		t.Errorf("summary = %q", d.Summary())
	}
	for _, want := range []string{"point-in-time", "terraform state rm", "restore_from"} {
		if !strings.Contains(strings.ToLower(d.Detail()), strings.ToLower(want)) {
			t.Errorf("the refusal must mention %q: %s", want, d.Detail())
		}
	}
}
