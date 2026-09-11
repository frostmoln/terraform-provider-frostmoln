package orphan

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/diag"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
)

// The orphan contract's copy is load-bearing: each arm names exactly what the
// practitioner may (and may never) do next. These tests pin the arms' verdicts
// and the sentences that carry the contract.

func TestAdoptCreateOnTimeoutFoundAdoptsWithAWarning(t *testing.T) {
	var d diag.Diagnostics
	id := AdoptCreateOnTimeout(context.Background(), &d, CreateParams{
		ResourceName: "VPC",
		FMList:       "`fm network vpc list`",
		Resolve: func(context.Context) (string, error) {
			return "vpc-found-1", nil
		},
		WaitErr: errors.New("timed out waiting for operation op-1 (last state: running)"),
	})

	if id != "vpc-found-1" {
		t.Fatalf("expected the found id back for the caller's honest read, got %q", id)
	}
	if d.HasError() {
		t.Fatalf("the found arm must warn, not error: %+v", d)
	}
	if d.WarningsCount() != 1 {
		t.Fatalf("expected exactly one adoption warning, got %d", d.WarningsCount())
	}
	w := d.Warnings()[0]
	if want := "VPC Was Adopted After The Apply Timed Out"; w.Summary() != want {
		t.Fatalf("adoption warning summary = %q, want %q", w.Summary(), want)
	}
	for _, want := range []string{"vpc-found-1", "fresh read of the platform", "tracked from here on"} {
		if !strings.Contains(w.Detail(), want) {
			t.Errorf("adoption warning detail must carry %q, got: %s", want, w.Detail())
		}
	}
}

func TestAdoptCreateOnTimeoutAbsentIsVerifiedAndReApplySafe(t *testing.T) {
	var d diag.Diagnostics
	AdoptCreateOnTimeout(context.Background(), &d, CreateParams{
		ResourceName: "subnet",
		FMList:       "`fm network subnet list`",
		Resolve: func(context.Context) (string, error) {
			return "", ErrAbsent
		},
		WaitErr: errors.New("timed out waiting for operation op-1"),
	})

	if !d.HasError() {
		t.Fatal("the absent arm must error")
	}
	if d.WarningsCount() != 0 {
		t.Fatalf("the absent arm must not adopt: %d warnings", d.WarningsCount())
	}
	e := d.Errors()[0]
	if want := "subnet Create Timed Out — Verified Absent"; e.Summary() != want {
		t.Fatalf("absent summary = %q, want %q", e.Summary(), want)
	}
	for _, want := range []string{"safe to re-apply", "at lookup time", "import"} {
		if !strings.Contains(e.Detail(), want) {
			t.Errorf("absent detail must carry %q, got: %s", want, e.Detail())
		}
	}
}

func TestAdoptCreateOnTimeoutAmbiguousRefusesToGuess(t *testing.T) {
	var d diag.Diagnostics
	AdoptCreateOnTimeout(context.Background(), &d, CreateParams{
		ResourceName: "instance",
		FMList:       "`fm compute instance list`",
		Resolve: func(context.Context) (string, error) {
			return "", fmt.Errorf("%w: inst-a (2026-09-11T10:00:00Z), inst-b (2026-09-11T10:00:01Z)", ErrAmbiguous)
		},
		WaitErr: errors.New("timed out waiting for operation op-1"),
	})

	if !d.HasError() {
		t.Fatal("the ambiguous arm must error rather than adopt a guess")
	}
	e := d.Errors()[0]
	for _, want := range []string{
		"several candidates",
		"inst-a",
		"Adopting the wrong object would be worse than adopting none",
		"terraform import",
		"terraform state rm",
	} {
		if !strings.Contains(e.Detail(), want) {
			t.Errorf("ambiguous detail must carry %q, got: %s", want, e.Detail())
		}
	}
}

func TestAdoptCreateOnTimeoutUnreadableNamesLastStateAndListHint(t *testing.T) {
	var d diag.Diagnostics
	waitErr := errors.New("timed out waiting for operation op-1 (last state: running, last poll error: 503)")
	AdoptCreateOnTimeout(context.Background(), &d, CreateParams{
		ResourceName: "volume",
		FMList:       "`fm storage volume list`",
		Resolve: func(context.Context) (string, error) {
			return "", fmt.Errorf("listing volumes: connection reset")
		},
		WaitErr: waitErr,
	})

	if !d.HasError() {
		t.Fatal("the unreadable arm must error")
	}
	e := d.Errors()[0]
	if want := "volume Create Timed Out — Its Fate Could Not Be Resolved"; e.Summary() != want {
		t.Fatalf("unreadable summary = %q, want %q", e.Summary(), want)
	}
	for _, want := range []string{
		waitErr.Error(),                   // last state + last poll error carried, never swallowed
		"the lookup failed with:",         // the sweep's own failure named
		"`fm storage volume list`",        // the fm-list hint
		"terraform import",                // import offered
		"re-apply once the platform",      // — re-apply/import only, no third path
		"Do NOT run `terraform state rm`", // the promise, verbatim
		"may still exist and still bill",  // what state rm actually does
	} {
		if !strings.Contains(e.Detail(), want) {
			t.Errorf("unreadable detail must carry %q, got: %s", want, e.Detail())
		}
	}
}

func TestAddDeleteOutcomeArmsKeepTheRowAndSayWhich(t *testing.T) {
	waitErr := fmt.Errorf("operation op-9 failed [quota_exceeded]: refused")

	var refused diag.Diagnostics
	AddDeleteOutcome(&refused, client.OperationRefused, "security group", "sg-1", waitErr)
	if !refused.HasError() || refused.ErrorsCount() != 1 {
		t.Fatalf("refused arm must produce exactly one error, got %+v", refused)
	}
	re := refused.Errors()[0]
	if want := "security group Delete Was Refused By The Platform"; re.Summary() != want {
		t.Fatalf("refused summary = %q, want %q", re.Summary(), want)
	}
	for _, want := range []string{"still tracked in Terraform state", "Destroying again is safe", "partially run"} {
		if !strings.Contains(re.Detail(), want) {
			t.Errorf("refused detail must carry %q, got: %s", want, re.Detail())
		}
	}
	if strings.Contains(re.Detail(), "state rm") {
		t.Errorf("refused arm must not point at state rm: %s", re.Detail())
	}

	var unknown diag.Diagnostics
	AddDeleteOutcome(&unknown, client.OperationUnknown, "listener", "lb-1/listener-2", errors.New("timed out waiting for operation op-9"))
	ue := unknown.Errors()[0]
	if want := "listener Delete Outcome Is Unknown"; ue.Summary() != want {
		t.Fatalf("unknown summary = %q, want %q", ue.Summary(), want)
	}
	for _, want := range []string{
		"MAY have been deleted anyway",
		"NOT removed it from state",
		"verified absence",
		"Do NOT run `terraform state rm`",
	} {
		if !strings.Contains(ue.Detail(), want) {
			t.Errorf("unknown detail must carry %q, got: %s", want, ue.Detail())
		}
	}
}

// --- PickCreated: the sweep's matcher. Narrow on purpose. ---

func TestPickCreatedPicksTheSingleNamedCandidateAfterTheFloor(t *testing.T) {
	floor := time.Date(2026, 9, 11, 10, 0, 0, 0, time.UTC)
	id, err := PickCreated([]Candidate{
		{ID: "old-1", Name: "vpc", CreatedAt: "2026-09-10T09:00:00Z"},
		{ID: "new-1", Name: "vpc", CreatedAt: "2026-09-11T10:00:05Z"},
		{ID: "other", Name: "not-vpc", CreatedAt: "2026-09-11T10:00:06Z"},
	}, "vpc", floor)
	if err != nil {
		t.Fatalf("expected the single post-floor candidate: %v", err)
	}
	if id != "new-1" {
		t.Fatalf("expected new-1, got %q", id)
	}
}

func TestPickCreatedAbsentWithoutAnySurvivor(t *testing.T) {
	floor := time.Date(2026, 9, 11, 10, 0, 0, 0, time.UTC)
	_, err := PickCreated([]Candidate{{ID: "old-1", Name: "vpc", CreatedAt: "2026-09-10T09:00:00Z"}}, "vpc", floor)
	if !errors.Is(err, ErrAbsent) {
		t.Fatalf("expected ErrAbsent, got %v", err)
	}
}

// A second apply with the same name inside the floor's skew window MUST be a
// refusal to guess, not a wrong adoption — that is the whole point of the
// ambiguity arm.
func TestPickCreatedRefusesToGuessBetweenSameNamedCandidates(t *testing.T) {
	floor := time.Date(2026, 9, 11, 10, 0, 0, 0, time.UTC)
	_, err := PickCreated([]Candidate{
		{ID: "new-1", Name: "vpc", CreatedAt: "2026-09-11T10:00:05Z"},
		{ID: "new-2", Name: "vpc", CreatedAt: "2026-09-11T10:00:07Z"},
	}, "vpc", floor)
	if !errors.Is(err, ErrAmbiguous) {
		t.Fatalf("expected ErrAmbiguous, got %v", err)
	}
	if !strings.Contains(err.Error(), "new-1") || !strings.Contains(err.Error(), "new-2") {
		t.Errorf("the ambiguity must name what it saw, got: %v", err)
	}
}

// A pre-existing same-named object that the server stamps BEFORE the apply's
// floor is never a candidate; and an unparseable createdAt is KEPT — losing
// the object to a formatting difference would trade adoption for a wrong
// absence verdict.
func TestPickCreatedKeepsUnparseableStampsRatherThanDroppingTheObject(t *testing.T) {
	floor := time.Date(2026, 9, 11, 10, 0, 0, 0, time.UTC)
	id, err := PickCreated([]Candidate{{ID: "mystery-1", Name: "vpc", CreatedAt: "the day before yesterday"}}, "vpc", floor)
	if err != nil || id != "mystery-1" {
		t.Fatalf("expected the unparseable survivor to be adopted, got id=%q err=%v", id, err)
	}
}

// A floorless sweep (families where the listing carries no usable stamps) is
// still name-safe: one name match is the object; two are a refusal.
func TestPickCreatedWithoutAFloor(t *testing.T) {
	id, err := PickCreated([]Candidate{{ID: "only-1", Name: "vpc"}}, "vpc", time.Time{})
	if err != nil || id != "only-1" {
		t.Fatalf("expected only-1, got id=%q err=%v", id, err)
	}
}
