package tagsettings

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
)

// "Apply the tenant's default tags to its existing resources"
// (RESOURCE-TAGS-PLAN Phase E), behind frostmoln_tenant_default_tags'
// apply_to_existing_on_change. Served by PROVISIONING, not identity:
//
//	POST /v1/tenants/{tid}/default-tags/apply   (no body)
//	  202 -> operation {operationId, status: "pending", resourceType: "tenant_default_tags"}
//	  409 APPLY_IN_PROGRESS + operationId       (one run per tenant at a time)
//	  400 / 403 / 422                           the platform will not run it (tenant not
//	                                            provisioned, no permission, invalid defaults)
//	  503                                       it cannot start one right now
//
// The operation is polled like every other (client.WaitForOperation). It
// COMPLETES even when some resources failed — the result lists them; a refusal
// arrives on the POST, not as a failed operation. For each resource the run
// adds only the default keys the resource lacks; an existing key keeps its value.

// CodeApplyInProgress is the 409 for an apply started while another runs in
// the same tenant.
const CodeApplyInProgress = "APPLY_IN_PROGRESS"

// ApplyInProgressError says an apply is already running in the tenant, so this
// request did NOT start one. OperationID names the running one ("" when the
// server did not say).
type ApplyInProgressError struct {
	OperationID string
	err         error
}

func (e *ApplyInProgressError) Error() string {
	if e.OperationID == "" {
		return "an apply of the tenant's default tags is already in progress"
	}
	return fmt.Sprintf("an apply of the tenant's default tags is already in progress (operation %s)", e.OperationID)
}

func (e *ApplyInProgressError) Unwrap() error { return e.err }

// ApplyRefusalIsRetryable reports whether an error from ApplyDefaultTags means
// the apply could not start NOW rather than that the platform will not run it:
// the platform or the gateway in front of it unavailable (502, 503, 504), or
// the request not answered at all. A 4xx, and any other 5xx, is a refusal a
// retry does not change.
func ApplyRefusalIsRetryable(err error) bool {
	if err == nil {
		return false
	}
	var apiErr *client.APIError
	if !errors.As(err, &apiErr) {
		return true
	}
	switch apiErr.StatusCode {
	case http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return true
	}
	return false
}

// ApplyDefaultTagsPath is the apply route of a tenant.
func ApplyDefaultTagsPath(tenantID string) string {
	return DefaultTagsPath(tenantID) + "/apply"
}

// ApplyDefaultTags starts an apply of the tenant's CURRENT default tags to its
// existing resources and returns the operation to wait for. A run already
// going is an *ApplyInProgressError naming it.
func ApplyDefaultTags(ctx context.Context, c *client.Client, tenantID string) (string, error) {
	resp, err := c.Post(ctx, ApplyDefaultTagsPath(tenantID), nil)
	if err != nil {
		var apiErr *client.APIError
		if errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusConflict && apiErr.Code == CodeApplyInProgress {
			return "", &ApplyInProgressError{OperationID: apiErr.OperationID, err: err}
		}
		return "", err
	}
	op, err := client.ParseResponse[client.Operation](resp)
	if err != nil {
		return "", err
	}
	if !resp.IsAccepted() || op.OperationID == "" {
		// Nothing to follow: the outcome cannot be observed, so it must not be
		// reported as applied.
		return "", fmt.Errorf("the platform accepted the apply (HTTP %d) but named no operation to follow, so its outcome is unknown",
			resp.StatusCode)
	}
	return op.OperationID, nil
}

// ApplyCounts are an apply's outcome counts, per resource type and in total.
type ApplyCounts struct {
	Examined  int `json:"examined"`
	Updated   int `json:"updated"`
	Unchanged int `json:"unchanged"`
	Skipped   int `json:"skipped"`
	Failed    int `json:"failed"`
}

// ApplyFailure is one resource the apply could not update. ResourceID is ""
// for a failure of a whole type (its listing could not be read). Reason is one
// of a fixed set the platform may extend; an unknown one is shown as it is.
type ApplyFailure struct {
	ResourceType string `json:"resourceType"`
	ResourceID   string `json:"resourceId"`
	Reason       string `json:"reason"`
	Message      string `json:"message"`
}

// ApplySkip is one resource the apply deliberately left alone.
type ApplySkip struct {
	ResourceType string `json:"resourceType"`
	ResourceID   string `json:"resourceId"`
	Reason       string `json:"reason"`
}

// ApplyResult is a completed apply's result. Failures and Skipped hold at most
// 200 entries each; Summary always has the true counts.
type ApplyResult struct {
	AppliedTags       map[string]string      `json:"appliedTags"`
	Summary           ApplyCounts            `json:"summary"`
	ByType            map[string]ApplyCounts `json:"byType"`
	Failures          []ApplyFailure         `json:"failures"`
	FailuresTruncated bool                   `json:"failuresTruncated"`
	Skipped           []ApplySkip            `json:"skipped"`
	SkippedTruncated  bool                   `json:"skippedTruncated"`
}

// DecodeApplyResult reads a completed apply's result. An absent result is an
// error: an empty summary would read as "nothing needed tagging".
func DecodeApplyResult(raw json.RawMessage) (*ApplyResult, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, errors.New("the apply completed but the platform returned no result to report")
	}
	var r ApplyResult
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil, fmt.Errorf("failed to read the apply result: %w", err)
	}
	return &r, nil
}
