package client

import "context"

// OperationVerdict classifies what a terminal async operation decided, for the
// two arms of the orphan contract's delete half: a destroy either verifies
// absence (the poll-to-404 resources) or drops state ONLY on a classified
// outcome — never silently.
type OperationVerdict int

const (
	// OperationRefused — the operation reached a terminal failure. The platform
	// decided, and it decided no: nothing was changed behind the provider's
	// back, so the write that failed is safe to attempt again once the refusal's
	// reason is dealt with.
	OperationRefused OperationVerdict = iota

	// OperationUnknown — anything the provider cannot establish: the wait gave
	// up while the operation was still running, a cancelled workflow may have
	// got part-way, or the operation cannot be read back at all. The write may
	// yet complete, so everything downstream must treat the platform's state as
	// unresolved, never as "nothing happened".
	OperationUnknown
)

// ClassifyOperationFailure asks the OPERATION what happened rather than reading
// the wait error's text. A terminal status is the platform's own decision and
// the only evidence that separates "refused, nothing happened" from "still
// running, something may have happened" — and everything the practitioner
// should do next hangs off which of those it was.
//
// Anything it cannot establish is OperationUnknown: an operation that cannot be
// read has said nothing at all, and silence must never be read as either arm of
// the contract.
//
// This is public_ip_association's create-side classify machine generalized to
// the whole surface (convergence wall, Gate 3 / delete-half, 2026-09-10); that
// resource now routes through this method.
func (c *Client) ClassifyOperationFailure(ctx context.Context, operationID string) OperationVerdict {
	op, err := c.GetOperation(ctx, operationID)
	if err != nil {
		return OperationUnknown
	}
	if op.Status == OperationStatusFailed {
		return OperationRefused
	}
	return OperationUnknown
}
