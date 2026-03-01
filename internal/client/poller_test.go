package client

import (
	"context"
	"fmt"
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
