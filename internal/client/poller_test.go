package client

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestWaitForStateSuccess(t *testing.T) {
	var calls atomic.Int32
	state, err := WaitForState(context.Background(), PollConfig{
		Interval:     10 * time.Millisecond,
		Timeout:      1 * time.Second,
		TargetStates: []string{"active"},
		ErrorStates:  []string{"error"},
		ResourceName: "test-resource",
		PollFunc: func(_ context.Context) (string, error) {
			n := calls.Add(1)
			if n < 3 {
				return "creating", nil
			}
			return "active", nil
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if state != "active" {
		t.Errorf("expected state active, got %s", state)
	}
}

func TestWaitForStateError(t *testing.T) {
	state, err := WaitForState(context.Background(), PollConfig{
		Interval:     10 * time.Millisecond,
		Timeout:      1 * time.Second,
		TargetStates: []string{"active"},
		ErrorStates:  []string{"error"},
		ResourceName: "test-resource",
		PollFunc: func(_ context.Context) (string, error) {
			return "error", nil
		},
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if state != "error" {
		t.Errorf("expected state error, got %s", state)
	}
}

func TestWaitForStateTimeout(t *testing.T) {
	_, err := WaitForState(context.Background(), PollConfig{
		Interval:     10 * time.Millisecond,
		Timeout:      50 * time.Millisecond,
		TargetStates: []string{"active"},
		ResourceName: "test-resource",
		PollFunc: func(_ context.Context) (string, error) {
			return "creating", nil
		},
	})
	if err == nil {
		t.Fatal("expected timeout error")
	}
}

func TestWaitForStateTransientErrors(t *testing.T) {
	var calls atomic.Int32
	state, err := WaitForState(context.Background(), PollConfig{
		Interval:     10 * time.Millisecond,
		Timeout:      1 * time.Second,
		TargetStates: []string{"active"},
		ResourceName: "test-resource",
		PollFunc: func(_ context.Context) (string, error) {
			n := calls.Add(1)
			if n < 3 {
				return "", fmt.Errorf("transient error")
			}
			return "active", nil
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if state != "active" {
		t.Errorf("expected state active, got %s", state)
	}
}

func TestWaitForStateContextCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := WaitForState(ctx, PollConfig{
		Interval:     10 * time.Millisecond,
		Timeout:      1 * time.Second,
		TargetStates: []string{"active"},
		ResourceName: "test-resource",
		PollFunc: func(_ context.Context) (string, error) {
			return "creating", nil
		},
	})
	if err == nil {
		t.Fatal("expected error for cancelled context")
	}
}

func TestWaitForStateDefaults(t *testing.T) {
	state, err := WaitForState(context.Background(), PollConfig{
		TargetStates: []string{"done"},
		ResourceName: "test",
		PollFunc: func(_ context.Context) (string, error) {
			return "done", nil
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if state != "done" {
		t.Errorf("expected state done, got %s", state)
	}
}

// A timeout must name the CAUSE of the last failed poll. WaitForState retries
// every poll error to the deadline, so without this the whole budget elapses and
// the practitioner is told only "timed out" — the 404 and its code are discarded.
//
// That path became reachable when IsNotFound started requiring a flat envelope: a
// delete poll meeting a routing 404 no longer short-circuits to "deleted", it
// retries for the full timeout and then reports a bare deadline. The difference
// between naming the cause and not is the difference between "retry, the gateway
// is mid-rollout" and a practitioner reaching for `terraform state rm`.
func TestWaitForState_TimeoutNamesTheLastPollError(t *testing.T) {
	_, err := WaitForState(context.Background(), PollConfig{
		Interval:     time.Millisecond,
		Timeout:      20 * time.Millisecond,
		TargetStates: []string{"deleted"},
		ResourceName: "instance",
		PollFunc: func(context.Context) (string, error) {
			return "", &APIError{
				Code: "PATH_NOT_ROUTED", Message: "no route found for path", StatusCode: 404,
			}
		},
	})
	if err == nil {
		t.Fatal("expected a timeout error")
	}
	if !strings.Contains(err.Error(), "PATH_NOT_ROUTED") {
		t.Errorf("the timeout must name the cause of the last poll failure, got: %v", err)
	}
}

// A poll that recovers must not have a stale error attributed to its timeout.
func TestWaitForState_TimeoutDoesNotReportAStalePollError(t *testing.T) {
	calls := 0
	_, err := WaitForState(context.Background(), PollConfig{
		Interval:     time.Millisecond,
		Timeout:      30 * time.Millisecond,
		TargetStates: []string{"deleted"},
		ResourceName: "instance",
		PollFunc: func(context.Context) (string, error) {
			calls++
			if calls == 1 {
				return "", &APIError{Code: "TRANSIENT", Message: "blip", StatusCode: 503}
			}
			return "building", nil
		},
	})
	if err == nil {
		t.Fatal("expected a timeout error")
	}
	if strings.Contains(err.Error(), "TRANSIENT") {
		t.Errorf("a recovered poll error must not be attributed to the timeout, got: %v", err)
	}
	if !strings.Contains(err.Error(), "building") {
		t.Errorf("the timeout should name the last observed STATE, got: %v", err)
	}
}
