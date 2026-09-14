package tagsettings

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
)

const applyTenant = "1111abcd-1111-4111-8111-11111111abcd"

func applyServer(t *testing.T, status int, body string) (*client.Client, *[]string) {
	t.Helper()
	var bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		if r.Method != http.MethodPost || r.URL.Path != "/v1/tenants/"+applyTenant+"/default-tags/apply" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		bodies = append(bodies, string(raw))
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	return client.NewClient(srv.URL, "k"), &bodies // pragma: allowlist secret
}

func TestApplyDefaultTags_StartsARun(t *testing.T) {
	c, bodies := applyServer(t, http.StatusAccepted,
		`{"operationId":"apply-default-tags-`+applyTenant+`","status":"pending","resourceType":"tenant_default_tags"}`)
	id, err := ApplyDefaultTags(context.Background(), c, applyTenant)
	if err != nil || id != "apply-default-tags-"+applyTenant {
		t.Fatalf("got (%q, %v)", id, err)
	}
	if len(*bodies) != 1 || (*bodies)[0] != "" {
		t.Errorf("want one POST with no body, got %q", *bodies)
	}
}

// A run already going is distinct, and names the running operation — which
// provisioning sends beside the code, not in details.
func TestApplyDefaultTags_InProgressNamesTheRunningOperation(t *testing.T) {
	c, _ := applyServer(t, http.StatusConflict,
		`{"error":{"code":"APPLY_IN_PROGRESS","message":"an apply of this tenant's default tags is already in progress","operationId":"op-running"}}`)
	_, err := ApplyDefaultTags(context.Background(), c, applyTenant)
	var busy *ApplyInProgressError
	if !errors.As(err, &busy) || busy.OperationID != "op-running" {
		t.Fatalf("want an *ApplyInProgressError naming op-running, got %T %v", err, err)
	}
}

// Another conflict is not "in progress", and an accept naming no operation
// cannot be followed, so it is never reported as started.
func TestApplyDefaultTags_OtherAnswersAreErrors(t *testing.T) {
	for _, tc := range []struct {
		status int
		body   string
	}{
		{http.StatusConflict, `{"error":{"code":"CONFLICT","message":"m","operationId":"x"}}`},
		{http.StatusAccepted, `{"status":"pending"}`},
		{http.StatusOK, `{"operationId":"x"}`},
	} {
		c, _ := applyServer(t, tc.status, tc.body)
		_, err := ApplyDefaultTags(context.Background(), c, applyTenant)
		var busy *ApplyInProgressError
		if err == nil || errors.As(err, &busy) {
			t.Errorf("%d %s: got %v, want a plain error", tc.status, tc.body, err)
		}
	}
}

// Only "could not start now" is retryable: the platform or its gateway
// unavailable, or no answer. A refusal the platform means is not.
func TestApplyRefusalIsRetryable(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want bool
	}{
		{nil, false},
		{&client.APIError{StatusCode: http.StatusServiceUnavailable, Code: "SERVICE_UNAVAILABLE"}, true},
		{&client.APIError{StatusCode: http.StatusBadGateway, Code: "BAD_GATEWAY"}, true},
		{&client.APIError{StatusCode: http.StatusGatewayTimeout, Code: "GATEWAY_TIMEOUT"}, true},
		{errors.New("dial tcp: connection refused"), true},
		{&client.APIError{StatusCode: http.StatusForbidden, Code: "FORBIDDEN"}, false},
		{&client.APIError{StatusCode: http.StatusBadRequest, Code: "TENANT_NOT_PROVISIONED"}, false},
		{&client.APIError{StatusCode: http.StatusUnprocessableEntity, Code: "INVALID_DEFAULT_TAGS"}, false},
		{&client.APIError{StatusCode: http.StatusInternalServerError, Code: "WORKFLOW_ERROR"}, false},
	} {
		if got := ApplyRefusalIsRetryable(tc.err); got != tc.want {
			t.Errorf("ApplyRefusalIsRetryable(%v) = %v, want %v", tc.err, got, tc.want)
		}
	}
}

func TestDecodeApplyResult(t *testing.T) {
	r, err := DecodeApplyResult([]byte(`{"appliedTags":{"env":"prod"},"summary":{"examined":2,"updated":1,"failed":1},` +
		`"failures":[{"resourceType":"volume","resourceId":"v1","reason":"some_future_reason","message":"m"}],"futureField":1}`))
	if err != nil || r.Summary.Failed != 1 || r.Failures[0].Reason != "some_future_reason" || r.AppliedTags["env"] != "prod" {
		t.Fatalf("decoded %+v, %v", r, err)
	}
	for _, raw := range []string{"", "null"} {
		if _, err := DecodeApplyResult([]byte(raw)); err == nil || !strings.Contains(err.Error(), "no result") {
			t.Errorf("%q decoded as a result: %v", raw, err)
		}
	}
}
