// The managed-PostgreSQL extension surface (initiative 01a0aec3 P2). The
// backend shipped in database v3.5.0 / provisioning v12.65.0 with FROZEN wire
// vocabulary; this file is a pure CLIENT of it — no record-vocabulary widening,
// no request-shape invention, and the server's typed refusal texts are surfaced
// verbatim, not reworded.
//
// The three design points settled with the operator before any code (initiative
// audit trail, 2026-09-18):
//
//   - The declared `extensions` SET is a DECLARATIVE desired state, not a
//     per-extension resource (an extension is a platform catalog value that
//     lives and dies with the instance — rule 10's settled shape). The apply
//     synthesizes the enable/disable operations from the diff between the
//     desired set and the recorded ledger (entries with status enabled), then
//     applies them SERIALIZED: one wire op at a time, because the platform's
//     instance-scoped probe refuses a POST while any non-terminal job holds the
//     instance's one-in-flight slot — including the first op's own.
//
//   - The ledger is the verdict. A job that RAN and reported failure completes
//     its workflow cleanly and the per-entry status lands "failed"; a workflow
//     that could not start marks the operation failed instead. Both are read:
//     the operation is polled to terminal, then the ledger is checked for the
//     per-extension outcome. Entries appear ONLY at terminal record (the
//     platform never stages half-written state), so "entry present" is proof.
//
//   - Never a blind POST retry. Every POST costs a fresh revision (the API is
//     retry-safe but NOT idempotent), and a retry after a lost 202 would
//     re-pay an already-paid restart. Every loop iteration therefore
//     RECONCILES FIRST — the ledger decides whether anything is still to do —
//     and only an unsatisfied target buys a POST. The loop's safety argument,
//     precisely: (1) a refusal the platform answered SYNCHRONOUSLY consumed
//     nothing — the platform's refusals run before the revision bump — so
//     re-POSTing it is free and the converge check kills double-apply;
//     (2) an AMBIGUOUS start (the extension_apply_start_unconfirmed 500, a
//     lost 202) may have consumed a revision and may hold the in-flight slot:
//     a re-POST here takes a NEW revision, is serialized against anything
//     already running by the platform's own probe, and converges through the
//     ledger — the honest cost in the worst case is one extra restart window
//     (S3's "paid twice", documented); (3) a RECORDED FAILURE for a target
//     name is retried exactly once per apply — a retry with a bumped revision
//     is the platform's stated re-apply remedy, and its verdict, success or
//     failure, surfaces through the ledger verbatim.
package postgres_instance

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
)

// The wire vocabulary the P1d surface froze. The wire ops are install|remove;
// the CUSTOMER verbs this provider's copy speaks are enable/disable (the plan's
// naming rule: customer copy says extensions; the retired word is never used).
const (
	wireOpInstall = "install"
	wireOpRemove  = "remove"
)

// The recorded per-extension statuses (the record path's closed vocabulary).
const (
	entryStatusEnabled = "enabled"
	entryStatusRemoved = "removed"
	entryStatusFailed  = "failed"
)

// The catalog lifecycle statuses. A deprecated row is still listed (so the
// name is known) but is refused for new enables by the enqueue.
const catalogStatusDeprecated = "deprecated"

const (
	refusalUnknownExtension  = "unknown extension: "
	refusalUnsupportedMajor  = " is not available for PostgreSQL "
	refusalNotPresent        = " is not present on this instance"
	refusalNotEnableable     = " is not available for new enables (status: "
	refusalHAInstance        = "highly available"
	refusalInflight409       = "another operation is already running"
	refusalFeatureNotEnabled = "feature_not_enabled"
)

type apiExtensionCatalogEntry struct {
	Name            string  `json:"name"`
	PgMajors        []int32 `json:"pgMajors"`
	RequiresPreload bool    `json:"requiresPreload"`
	Status          string  `json:"status"`
}

type apiExtensionCatalogList struct {
	Extensions []apiExtensionCatalogEntry `json:"extensions"`
}

type apiExtensionStateEntry struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Detail string `json:"detail,omitempty"`
}

// apiExtensionState is the GET /databases/{id}/extensions body. Revision is
// the asked-state stamp (what was last requested); the entries are what is
// RECORDED — the only surface that carries outcomes.
type apiExtensionState struct {
	Revision   int64                    `json:"extensionRevision"`
	Extensions []apiExtensionStateEntry `json:"extensions"`
}

// apiExtensionOpRequest is the POST /databases/{id}/extensions body, 1..8
// lowercase catalog names, exactly as the platform freezes it.
type apiExtensionOpRequest struct {
	Op         string   `json:"op"`
	Extensions []string `json:"extensions"`
}

// fetchExtensionCatalog reads the platform's extension catalog. The catalog is
// SERVER-SIDE TRUTH and is never embedded here: a static allowlist copied into
// the provider drifts from the catalog rows the day they change. The platform
// itself degrades a failed catalog read to an empty list (plan-time reads are
// advisory; the enqueue re-verifies everyone, fail-closed).
func (r *postgresInstanceResource) fetchExtensionCatalog(ctx context.Context) ([]apiExtensionCatalogEntry, error) {
	resp, err := r.client.Get(ctx, "/v1/databases/extensions", nil)
	if err != nil {
		return nil, err
	}
	parsed, err := client.ParseResponse[apiExtensionCatalogList](resp)
	if err != nil {
		return nil, err
	}
	return parsed.Extensions, nil
}

// fetchExtensionState reads the instance's recorded extension state.
func (r *postgresInstanceResource) fetchExtensionState(ctx context.Context, id string) (*apiExtensionState, error) {
	resp, err := r.client.Get(ctx, r.client.TenantPath("/databases/"+id+"/extensions"), nil)
	if err != nil {
		return nil, err
	}
	parsed, err := client.ParseResponse[apiExtensionState](resp)
	if err != nil {
		return nil, err
	}
	return parsed, nil
}

// enabledExtensionNames projects the ledger to the names the instance is
// actually serving: status enabled. removed entries are intent history;
// failed entries never reached a working enable. This is the value `extensions`
// refreshes to, so out-of-band changes surface as ordinary drift.
func enabledExtensionNames(state *apiExtensionState) []string {
	if state == nil {
		return []string{}
	}
	names := []string{}
	for _, e := range state.Extensions {
		if e.Status == entryStatusEnabled {
			names = append(names, e.Name)
		}
	}
	sort.Strings(names)
	return names
}

// setMembers flattens a string set into a sorted slice; null and unknown
// produce an empty slice.
func setMembers(set types.Set) []string {
	if set.IsNull() || set.IsUnknown() {
		return []string{}
	}
	names := []string{}
	for _, v := range set.Elements() {
		s, ok := v.(types.String)
		if !ok || s.IsNull() || s.IsUnknown() {
			continue
		}
		names = append(names, s.ValueString())
	}
	sort.Strings(names)
	return names
}

// extensionDiff synthesizes the wire ops from the declarative set:
//   - enable names the recorded-now-disabled names the configuration wants;
//   - disable names the recorded-now-enabled names the configuration drops.
//
// The diff is computed between the PLAN (what was declared) and the recorded
// STATE last refreshed from the platform. The apply re-reads the ledger before
// every POST (reconcile-first), so a diff staled by an out-of-band change is
// reconciled there: an already-satisfied name buys no second apply, and a
// not-present removal buys no refusal.
func extensionDiff(plan, state types.Set) (enable, disable []string) {
	if plan.IsNull() || plan.IsUnknown() {
		// A plan value that records "nothing declared" (omitted, or the
		// refresh left it unknown) reconciles NOTHING: omitted keeps
		// whatever the instance has (the same doctrine as the addons set),
		// and nothing here may disable on the provider's own uncertainty.
		return nil, nil
	}
	desired := map[string]bool{}
	for _, n := range setMembers(plan) {
		desired[n] = true
	}
	recorded := map[string]bool{}
	for _, n := range setMembers(state) {
		recorded[n] = true
	}
	for _, n := range setMembers(plan) {
		if !recorded[n] {
			enable = append(enable, n)
		}
	}
	for _, n := range setMembers(state) {
		if !desired[n] {
			disable = append(disable, n)
		}
	}
	sort.Strings(enable)
	sort.Strings(disable)
	return enable, disable
}

// extensionTargetReached reports whether the ledger already satisfies the op
// for every name. This is the loop's converge check — it is what makes a lost
// 202 (or any other ambiguous delivery) safe: whichever client's ask landed,
// the terminal record satisfies the target and nothing else is attempted.
func extensionTargetReached(state *apiExtensionState, op string, names []string) bool {
	byName := map[string]string{}
	if state != nil {
		for _, e := range state.Extensions {
			byName[e.Name] = e.Status
		}
	}
	for _, n := range names {
		status := byName[n]
		switch op {
		case wireOpInstall:
			if status != entryStatusEnabled {
				return false
			}
		case wireOpRemove:
			// removed, or never present in the ledger: both mean the
			// extension is not on the instance.
			if status != entryStatusRemoved && status != "" {
				return false
			}
		}
	}
	return true
}

// extensionVerdictError turns a completed operation's ledger into the apply's
// verdict. It runs AFTER the operation reached terminal state: the record path
// writes before the workflow returns, so any name not at its expected terminal
// status means the workflow failed the apply — and the per-entry detail is the
// customer-facing reason (never reworded here). For a remove, a name with no
// ledger entry at all is satisfied: the extension is not on the instance.
func extensionVerdictError(state *apiExtensionState, op string, names []string) error {
	byName := map[string]apiExtensionStateEntry{}
	if state != nil {
		for _, e := range state.Extensions {
			byName[e.Name] = e
		}
	}
	verb := "enable"
	if op == wireOpRemove {
		verb = "disable"
	}
	for _, n := range names {
		e, ok := byName[n]
		if op == wireOpInstall && !ok {
			return verdictFailureError{fmt.Errorf("extension %s has no recorded verdict after the enable operation completed "+
				"(the platform records outcomes only once, at the end — try again; if it repeats, contact support)", n)}
		}
		if !ok {
			continue
		}
		switch {
		case e.Status == entryStatusFailed:
			detail := e.Detail
			if detail == "" {
				detail = "no detail was recorded"
			}
			return verdictFailureError{fmt.Errorf("extension %s failed to %s: %s", n, verb, detail)}
		case op == wireOpInstall && e.Status != entryStatusEnabled:
			return verdictFailureError{fmt.Errorf("extension %s is still recorded as %q after the enable operation completed", n, e.Status)}
		case op == wireOpRemove && e.Status == entryStatusEnabled:
			return verdictFailureError{fmt.Errorf("extension %s is still recorded as enabled after the disable operation completed", n)}
		}
	}
	return nil
}

// classifyExtensionRefusal maps the platform's typed refusals onto the apply
// loop's three dispositions: stop permanently (surfaced verbatim), converged
// (the target already holds), keep retrying. Anything not recognised stops
// permanently — an unrecognized verdict must never be retried blind.
func classifyExtensionRefusal(op string, err error) (converged bool, retryable bool, fsErr error) {
	// Cancellation/timeout of the caller's context is a stop, never a
	// retryable "transport" failure — the ctx check at the loop top catches
	// most of it; these individual calls must not re-queue either.
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false, false, err
	}
	var apiErr *client.APIError
	if !errors.As(err, &apiErr) {
		// Transport-level: the request may or may not have arrived. The
		// reconcile-first loop handles both — never a blind retry, so
		// retrying after a lost 202 converges instead of double-applying.
		return false, true, nil
	}
	msg := apiErr.Message
	switch {
	case apiErr.StatusCode == 403 && apiErr.Code == refusalFeatureNotEnabled:
		return false, false, err
	case apiErr.StatusCode == 404:
		return false, false, err
	case apiErr.StatusCode == 400 && strings.HasPrefix(msg, refusalUnknownExtension):
		return false, false, err
	case apiErr.StatusCode == 400 && strings.Contains(msg, refusalUnsupportedMajor):
		return false, false, err
	case apiErr.StatusCode == 400 && strings.Contains(msg, refusalNotEnableable):
		return false, false, err
	case apiErr.StatusCode == 400 && strings.Contains(msg, refusalNotPresent):
		// Removing a name the instance does not carry: the desired absence
		// is already the recorded truth (a concurrent apply won the race).
		// Converged, not an error. NOTE: the matcher is CONTRACTUAL — the
		// frozen v3.5.0 refusal text, not a heuristic; the database service's
		// typed refusals name the extension in exactly this sentence.
		return true, false, nil
	case apiErr.StatusCode == 409 && strings.Contains(msg, refusalHAInstance):
		// The HA restart-sequencing refusal is honest and standable: the
		// platform cannot coordinate primary/standby restarts yet.
		return false, false, err
	case apiErr.StatusCode == 409:
		// In-flight (the common serialized-op refusal) or a non-running
		// instance: both can clear while we wait.
		return false, true, nil
	case apiErr.StatusCode == 429 || apiErr.StatusCode >= 500:
		return false, true, nil
	default:
		return false, false, err
	}
}

// urlPathEscapeSegments escapes a resource id before it is interpolated into
// a path. TenantPath escapes the tenant; the subpath then goes through
// path.Join, which CLEANS dot segments — the same trap client.GetOperation
// documents for operation ids. The ids here come from platform state, not
// config, so the hole is defense-in-depth, not a reachable route.
func urlPathEscapeSegments(id string) string { return url.PathEscape(id) }

// applyExtensionDiff runs the synthesized ops SERIALIZED: the whole enable
// batch, then the whole disable batch. The serialization is not a stylistic
// choice — the platform admits one non-terminal job per instance, so a second
// POST while the first still runs is REFUSED by the instance-scoped probe.
func (r *postgresInstanceResource) applyExtensionDiff(ctx context.Context, id string, enable, disable []string, budget time.Duration) error {
	if len(enable) > 0 {
		if err := r.applyExtensionChange(ctx, id, wireOpInstall, enable, budget); err != nil {
			return err
		}
	}
	if len(disable) > 0 {
		if err := r.applyExtensionChange(ctx, id, wireOpRemove, disable, budget); err != nil {
			return err
		}
	}
	return nil
}

// applyExtensionChange drives ONE wire op to its recorded verdict within the
// budget. Retry-dispositions only; permanent refusals surface immediately.
func (r *postgresInstanceResource) applyExtensionChange(ctx context.Context, id, op string, names []string, budget time.Duration) error {
	deadline := time.Now().Add(budget)
	var lastErr error
	for {
		// Context cancelled (Terraform interrupt, parent timeout): stop
		// NOW. Without this the deadline only bounds the loop, so an
		// interrupted apply would busy-spin failures until the wall clock
		// gives up.
		if ctxErr := ctx.Err(); ctxErr != nil {
			if lastErr != nil {
				return lastErr
			}
			return ctxErr
		}

		// Reconcile first — the ledger decides whether anything is left to
		// do. A satisfied target buys nothing: no revision, no restart. An
		// UNREADABLE ledger also buys nothing: no POST goes out on a blind
		// read, because the loop's whole safety argument is ledger-primacy
		// — the exception is a 404 (this deployment has no extension read
		// at all), where a POST would only 404 too, and failing fast
		// surfaces the real constraint instead of spinning out the budget.
		state, ferr := r.fetchExtensionState(ctx, urlPathEscapeSegments(id))
		if ferr != nil {
			var apiErr *client.APIError
			if errors.As(ferr, &apiErr) && apiErr.StatusCode == http.StatusNotFound {
				return ferr
			}
			lastErr = ferr
			r.sleepPollInterval(ctx, deadline)
			continue
		}
		if extensionTargetReached(state, op, names) {
			return nil
		}

		if time.Now().After(deadline) {
			if lastErr != nil {
				return lastErr
			}
			verb := "enable"
			if op == wireOpRemove {
				verb = "disable"
			}
			return fmt.Errorf("timed out waiting to %s extensions %s on the instance (the platform runs one operation at a time per instance)", verb, strings.Join(names, ", "))
		}

		apiResp, perr := r.client.Post(ctx, r.client.TenantPath("/databases/"+urlPathEscapeSegments(id)+"/extensions"),
			apiExtensionOpRequest{Op: op, Extensions: names})
		if perr != nil {
			converged, retryable, errOut := classifyExtensionRefusal(op, perr)
			if converged {
				return nil
			}
			if !retryable {
				return errOut
			}
			lastErr = perr
			r.sleepPollInterval(ctx, deadline)
			continue
		}

		if !apiResp.IsAccepted() {
			// The platform's extension route always answers 202 (the
			// synchronous fallback branch is deliberately absent
			// server-side). Anything else is an unexpected shape; stop
			// rather than guess.
			return fmt.Errorf("the platform answered the extension request with an unexpected synchronous response (expected an accepted-operation answer); nothing was confirmed")
		}

		waitErr := r.awaitExtensionOperation(ctx, id, apiResp, op, names, deadline)
		if waitErr == nil {
			continue // the next reconcile-first pass returns nil
		}
		// An operation that ran and recorded failure is a PERMANENT verdict
		// for this apply (its detail is the customer-facing reason) — except
		// the in-flight refusal, which the enqueue records failed but which
		// clears once the other operation finishes: that one waits and
		// retries, exactly like its synchronous 409 twin. The one prose
		// matcher here reads the ledger Detail, which is prose BY CONTRACT
		// (the enqueue's recorded refusal text; frozen at v3.5.0). An
		// operation that could not be watched at all is also retryable.
		if isExtensionVerdictFailure(waitErr) && !strings.Contains(waitErr.Error(), refusalInflight409) {
			return waitErr
		}
		lastErr = waitErr
		r.sleepPollInterval(ctx, deadline)
	}
}

// awaitExtensionOperation waits the accepted operation to terminal and reads
// the ledger verdict. The two outcomes a failed apply can take, both ending in
// the ledger and both surfaced verbatim:
//   - the workflow could not even start (enqueue refusal) — the operation
//     reports failed with the refusal's reason, and the record path lands
//     failed entries naming the extension;
//   - the workflow ran, the job reported failure, and the workflow completed
//     cleanly — the operation reads completed and only the ledger carries the
//     failure, which is why the ledger, not the operation, is the verdict.
func (r *postgresInstanceResource) awaitExtensionOperation(ctx context.Context, id string, apiResp *client.Response, op string, names []string, deadline time.Time) error {
	parsed, perr := client.ParseResponse[client.Operation](apiResp)
	if perr != nil {
		return fmt.Errorf("could not read the extension operation response: %w", perr)
	}
	if parsed.OperationID == "" {
		return fmt.Errorf("the extension operation was accepted but returned no operation id; the outcome is unwatchable from here")
	}
	remaining := time.Until(deadline)
	if remaining <= 0 {
		return fmt.Errorf("timed out waiting for the extension operation to complete")
	}
	if _, err := r.client.WaitForOperation(ctx, parsed.OperationID, r.getPollInterval(), remaining); err != nil {
		// The operation reached a failed/cancelled verdict (or the wait
		// expired). The ledger holds the per-extension truth either way:
		// read it before deciding whether this failure is permanent.
		if state, ferr := r.fetchExtensionState(ctx, urlPathEscapeSegments(id)); ferr == nil {
			if verr := extensionVerdictError(state, op, names); verr != nil {
				return verr
			}
		}
		return err
	}
	state, err := r.fetchExtensionState(ctx, urlPathEscapeSegments(id))
	if err != nil {
		return fmt.Errorf("the extension operation completed but the recorded result could not be read back: %w", err)
	}
	return extensionVerdictError(state, op, names)
}

// verdictFailureError types a RECORDED verdict: the operation reached its
// end and the ledger carries an outcome that is not the target state. The
// wrapper is load-bearing: the retry/permanent split keys on the STRUCTURAL
// class (a ledger verdict vs an unwatchable operation), never on matching
// prose, so the sentences extensionVerdictError writes can evolve without
// silently reclassifying every verdict failure as retryable.
type verdictFailureError struct{ err error }

func (e verdictFailureError) Error() string { return e.err.Error() }
func (e verdictFailureError) Unwrap() error { return e.err }

// isExtensionVerdictFailure reports whether the error came from reading a
// recorded failure (permanent for this apply) rather than from watching an
// operation that could not be observed (retryable).
func isExtensionVerdictFailure(err error) bool {
	var vErr verdictFailureError
	return errors.As(err, &vErr)
}

func (r *postgresInstanceResource) sleepPollInterval(ctx context.Context, deadline time.Time) {
	remaining := time.Until(deadline)
	d := r.getPollInterval()
	if d > remaining {
		d = remaining
	}
	if d <= 0 {
		return
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
	case <-timer.C:
	}
}

// checkPlanExtensions is the plan-time refusal the P2 row promises: every
// declared name is checked against the LIVE platform catalog (drift-safe —
// the catalog rows change, and this read never embeds a copy of them). The
// refusal wording reuses the platform's own typed sentences so plan and apply
// tell one story. A catalog read that fails is a WARNING: the apply enforces
// the same check fail-closed, and blocking every plan on a transient catalog
// outage would refuse plans the platform itself would accept.
func (r *postgresInstanceResource) checkPlanExtensions(ctx context.Context, plan *PostgresInstanceModel) diag.Diagnostics {
	var diags diag.Diagnostics
	if plan.Extensions.IsNull() || plan.Extensions.IsUnknown() {
		return diags
	}
	names := setMembers(plan.Extensions)
	if len(names) == 0 {
		return diags
	}
	catalog, err := r.fetchExtensionCatalog(ctx)
	if err != nil {
		diags.AddWarning(
			"Extension catalog could not be read at plan time",
			fmt.Sprintf("The config's extensions could not be checked against the platform's extension catalog (%s). "+
				"Planning continues; the platform enforces the same check when the request is made and refuses unknown or unsupported names with its typed refusal.", err.Error()),
		)
		return diags
	}
	byName := map[string]apiExtensionCatalogEntry{}
	for _, e := range catalog {
		byName[e.Name] = e
	}
	// The catalog read is deliberately NOT major-filtered: with ?major= an
	// absent row would be ambiguous between "unknown name" and "unknown for
	// this major", and the two carry different remedies for the customer.
	major := parsePostgresMajor(plan.Version)
	for _, name := range names {
		row, known := byName[name]
		switch {
		case !known:
			diags.AddAttributeError(
				path.Root("extensions"),
				"Unknown extension",
				fmt.Sprintf("%s%s. Only names the platform's extension catalog advertises can be enabled; the catalog read (GET /v1/databases/extensions) or the portal lists what exists.", refusalUnknownExtension, name),
			)
		case row.Status == catalogStatusDeprecated:
			diags.AddAttributeError(
				path.Root("extensions"),
				"Extension is not available for new enables",
				fmt.Sprintf("extension %s%s%s). Enabled instances keep the extension until it is removed; choose a non-deprecated extension instead.", name, refusalNotEnableable, row.Status),
			)
		case major > 0 && !supportsMajor(row, major):
			diags.AddAttributeError(
				path.Root("extensions"),
				"Extension is not available for this PostgreSQL version",
				fmt.Sprintf("extension %s%s%d.", name, refusalUnsupportedMajor, major),
			)
		}
	}
	return diags
}

func supportsMajor(e apiExtensionCatalogEntry, major int32) bool {
	for _, m := range e.PgMajors {
		if m == major {
			return true
		}
	}
	return false
}

// parsePostgresMajor reads the `version` attribute's integer major; an
// unknown, null or non-numeric value yields 0 (checks that need the major
// degrade silently — the platform re-verifies at execution time).
func parsePostgresMajor(v types.String) int32 {
	if v.IsNull() || v.IsUnknown() {
		return 0
	}
	m, err := strconv.ParseInt(strings.TrimSpace(v.ValueString()), 10, 32)
	if err != nil || m < 0 {
		return 0
	}
	return int32(m)
}
