// Package orphan is the Terraform side of the convergence wall's orphan
// contract — what the provider says and records when a write may have outlived
// its wait. The provider may never record intent the platform doesn't enact,
// and it may never silently forget intent the platform MAY have enacted.
//
// Two halves:
//
//   - Create: a timed-out saga create whose 202 carried no resourceId resolves
//     the object by a name/list sweep (ADOPT-AS-TRACKED, settled 2026-09-10).
//     Found becomes an adopted, honestly-read state row; verified absent
//     becomes a re-apply-safe error; unreadable names the platform's last word
//     plus the list/import path. It never invites `terraform state rm` —
//     docs/guides/not-found-handling.md's promise is load-bearing here, and so
//     is REAP's rejection (a delete on timeout would itself be an untracked
//     intent the platform may have already enacted).
//   - Delete: a destroy either verifies absence (the poll-to-404 resources,
//     instance-style) or drops state only on a classified outcome — a refused
//     operation changed nothing; an unknown one may still be running. Never
//     silently, and the diagnostic says which arm it was.
package orphan

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/diag"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
)

// Sentinel outcomes a Resolve func answers with. A real id means "exactly one
// object matches this apply — adopt it"; anything else is one of these or a
// genuine listing failure.
var (
	// ErrAbsent — the listing succeeded and nothing matched. The platform never
	// finished creating the object (or it is already gone): the sweep just
	// verified the absence.
	ErrAbsent = errors.New("no object matched this apply")

	// ErrAmbiguous — several objects matched. Names are not unique on every
	// surface, and adopting the wrong one outranks adopting none: the matcher
	// must refuse to guess (wrap ErrAmbiguous with the candidates it saw).
	ErrAmbiguous = errors.New("several objects matched this apply; refusing to guess")
)

// ResolveFunc answers the name/list sweep for one timed-out create: the id of
// the object this apply produced, ErrAbsent, ErrAmbiguous (wrapped, with the
// candidates), or a real error when the listing itself could not be read —
// which is the one outcome that says nothing at all.
type ResolveFunc func(ctx context.Context) (string, error)

// Candidate is one row of a family listing, as the sweep consumes it.
type Candidate struct {
	ID        string
	Name      string
	CreatedAt string // RFC3339 as the platform stamps it; "" if unknown
}

// PickCreated resolves the object an apply created from a family listing.
//
// The matcher is deliberately narrow: name equality (when the family names
// things) and a created-at floor — when the apply started — so an object that
// existed before this apply is never adopted by mistake. A candidate whose
// createdAt cannot be parsed is KEPT rather than discarded: losing the real
// object to a formatting difference would trade adoption for a wrong absence
// verdict, and the ambiguity rule below exists for exactly the case the floor
// cannot settle.
//
// Exactly one survivor is the id. None is ErrAbsent (the sweep just verified
// the absence). More than one is ErrAmbiguous wrapped with what was seen —
// adopting the wrong object outranks adopting none, so the matcher refuses to
// guess.
func PickCreated(candidates []Candidate, name string, floor time.Time) (string, error) {
	var survivors []Candidate
	for _, c := range candidates {
		if c.ID == "" {
			continue
		}
		if name != "" && c.Name != name {
			continue
		}
		if !floor.IsZero() {
			if created, err := time.Parse(time.RFC3339, c.CreatedAt); err == nil && created.Before(floor) {
				continue
			}
		}
		survivors = append(survivors, c)
	}

	switch {
	case len(survivors) == 1:
		return survivors[0].ID, nil
	case len(survivors) == 0:
		return "", ErrAbsent
	default:
		detail := make([]string, 0, len(survivors))
		for _, c := range survivors {
			detail = append(detail, fmt.Sprintf("%s (%s)", c.ID, c.CreatedAt))
		}
		return "", fmt.Errorf("%w: %s", ErrAmbiguous, strings.Join(detail, ", "))
	}
}

// CreateParams carries what the create-timeout arm needs beyond the sweep.
type CreateParams struct {
	// ResourceName names the family in diagnostic summaries ("VPC",
	// "security group rule").
	ResourceName string

	// FMList names the CLI surface a practitioner lists the family with,
	// e.g. "`fm network vpc list`".
	FMList string

	// Resolve is the sweep (see ResolveFunc).
	Resolve ResolveFunc

	// WaitErr is the error the wait returned — the poller's timeout names the
	// operation's last state and its last poll error, which the unreadable arm
	// must carry rather than swallow.
	WaitErr error
}

// AdoptCreateOnTimeout runs the create-timeout arm of the orphan contract and
// returns the id of the object it adopted — empty when the caller must simply
// return (the helper has already added the diagnostic to describe why).
//
// The three arms, and nothing else:
//
//   - found → the shared adoption warning is added and the id returned; the
//     CALLER then does the honest read (the platform's response, not the
//     configuration's intent) and writes the state row.
//   - ErrAbsent → a verified-absence error: safe to re-apply.
//   - anything else → the unreadable arm: last state, last error, the fm list
//     hint, import — never `terraform state rm`.
func AdoptCreateOnTimeout(ctx context.Context, diags *diag.Diagnostics, p CreateParams) string {
	id, err := p.Resolve(ctx)
	if err == nil && id != "" {
		diags.AddWarning(
			fmt.Sprintf("%s Was Adopted After The Apply Timed Out", p.ResourceName),
			fmt.Sprintf("The create was accepted by the platform, but the provider could not resolve "+
				"its resource ID within its wait, and the follow-up lookup found exactly one %s matching "+
				"this apply (id %s), so it has been adopted into Terraform state: the state row is written "+
				"from a fresh read of the platform — what the platform HAS, not what the configuration "+
				"asked for. The %s is tracked from here on; destroying it removes the platform object.\n\n"+
				"The wait gave up with: %s",
				p.ResourceName, id, p.ResourceName, p.WaitErr),
		)
		return id
	}

	switch {
	case errors.Is(err, ErrAbsent):
		diags.AddError(
			fmt.Sprintf("%s Create Timed Out — Verified Absent", p.ResourceName),
			fmt.Sprintf("The create was accepted by the platform, but the provider could not resolve its "+
				"outcome within its wait, and the follow-up lookup found NO %s matching this apply at lookup "+
				"time: the saga had not committed an object the listing could see. Nothing was recorded in "+
				"Terraform state, and it is safe to re-apply in the ordinary case — a saga that lands late "+
				"or a name this apply shares with an older object are the exceptions; re-running the plan "+
				"surfaces either, and `terraform import` settles the landed object rather than a second.\n\n "+
				"The wait gave up with: %s", p.ResourceName, p.WaitErr),
		)
	case errors.Is(err, ErrAmbiguous):
		diags.AddError(
			fmt.Sprintf("%s Create Timed Out — Its Fate Could Not Be Resolved", p.ResourceName),
			fmt.Sprintf("The create was accepted by the platform, but it had not finished when the "+
				"provider stopped waiting, and the follow-up lookup found several candidates instead of "+
				"one: %s. Adopting the wrong object would be worse than adopting none, so nothing was "+
				"recorded in Terraform state.\n\n"+
				"Identify the right object in the portal or with %s and `terraform import` it, or "+
				"re-apply once the platform has settled. Do NOT run `terraform state rm` — it is the one "+
				"action that re-creates the orphaning this contract exists to prevent.\n\n"+
				"The wait gave up with: %s\nThe lookup said: %s",
				err, p.FMList, p.WaitErr, err),
		)
	default:
		reason := ""
		if err != nil {
			reason = fmt.Sprintf(" (the lookup failed with: %s)", err)
		}
		diags.AddError(
			fmt.Sprintf("%s Create Timed Out — Its Fate Could Not Be Resolved", p.ResourceName),
			fmt.Sprintf("The create was accepted by the platform, but it had not finished when the "+
				"provider stopped waiting, and the follow-up lookup could not establish what happened"+
				"%s. Nothing was recorded in Terraform state.\n\n"+
				"Check the platform directly — %s — and either `terraform import` the object if it "+
				"exists or re-apply once the platform responds. Do NOT run `terraform state rm`: it "+
				"orphans a live object that may still exist and still bill, with nothing that will "+
				"ever destroy it.\n\n"+
				"The wait gave up with: %s", reason, p.FMList, p.WaitErr),
		)
	}
	return ""
}

// AddCreateRefused words the refused arm of a failed create wait: the
// operation reached a terminal failure, so the platform decided NO and
// created nothing. Nothing was recorded and applying again is safe once the
// refusal's reason is dealt with — which is worth SAYING, because the
// unknown-timeout arm reads very differently.
func AddCreateRefused(diags *diag.Diagnostics, resourceName, subject string, waitErr error) {
	diags.AddError(
		fmt.Sprintf("%s Creation Was Refused By The Platform", resourceName),
		fmt.Sprintf("The platform refused to create %s. Nothing was created and nothing has been "+
			"recorded in Terraform state, so applying again is safe once the reason below is dealt with.\n\n"+
			"The platform said: %s", subject, waitErr),
	)
}

// AddDeleteOutcome words the classified outcome of a delete whose wait failed.
// BOTH arms keep the state row — the row is what a still-live object needs to
// stay destroyable — and the copy tells the practitioner which arm they are in.
func AddDeleteOutcome(diags *diag.Diagnostics, verdict client.OperationVerdict, resourceName, subject string, waitErr error) {
	switch verdict {
	case client.OperationRefused:
		diags.AddError(
			fmt.Sprintf("%s Delete Was Refused By The Platform", resourceName),
			fmt.Sprintf("The platform's delete operation for %s failed: the platform reports the %s "+
				"still there, and it is still tracked in Terraform state. If the destroy had already "+
				"partially run on the platform, a refresh shows what remains. Destroying again is safe "+
				"once the reason below is dealt with.\n\nThe platform said: %s",
				subject, resourceName, waitErr),
		)
	default:
		diags.AddError(
			fmt.Sprintf("%s Delete Outcome Is Unknown", resourceName),
			fmt.Sprintf("The delete of %s was accepted, but the provider could not establish its "+
				"outcome before the wait gave up — the %s MAY have been deleted anyway. Terraform has "+
				"NOT removed it from state. Refresh (the next apply's read does it) to let the "+
				"platform's answer settle it: a refresh that reports it gone is the verified absence "+
				"the destroy wanted, and one that still sees it can be destroyed again. Do NOT run "+
				"`terraform state rm` — it is the one action that re-orphans a %s that may be live.\n\n"+
				"The wait gave up with: %s",
				subject, resourceName, resourceName, waitErr),
		)
	}
}
