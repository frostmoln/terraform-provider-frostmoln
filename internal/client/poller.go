package client

import (
	"context"
	"fmt"
	"time"
)

// PollConfig configures the async polling behavior.
type PollConfig struct {
	// Interval between polling attempts.
	Interval time.Duration
	// Timeout for the entire polling operation.
	Timeout time.Duration
	// PollFunc is called on each interval. It should return the current state
	// and any error. Return ("", nil) to keep polling.
	PollFunc func(ctx context.Context) (state string, err error)
	// TargetStates are the states that indicate completion.
	TargetStates []string
	// ErrorStates are states that indicate failure.
	ErrorStates []string
	// ResourceName is used in error messages.
	ResourceName string
}

// DefaultPollConfig returns a PollConfig with sensible defaults.
func DefaultPollConfig() PollConfig {
	return PollConfig{
		Interval: 2 * time.Second,
		Timeout:  5 * time.Minute,
	}
}

// WaitForState polls until the resource reaches a target state, an error state,
// or the timeout is exceeded.
func WaitForState(ctx context.Context, cfg PollConfig) (string, error) {
	if cfg.Interval == 0 {
		cfg.Interval = 2 * time.Second
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = 5 * time.Minute
	}

	ctx, cancel := context.WithTimeout(ctx, cfg.Timeout)
	defer cancel()

	targetSet := make(map[string]bool, len(cfg.TargetStates))
	for _, s := range cfg.TargetStates {
		targetSet[s] = true
	}

	errorSet := make(map[string]bool, len(cfg.ErrorStates))
	for _, s := range cfg.ErrorStates {
		errorSet[s] = true
	}

	ticker := time.NewTicker(cfg.Interval)
	defer ticker.Stop()

	// The most recent PollFunc error, carried so a timeout can name its CAUSE.
	// Every poll error is retried to the deadline, so without this the whole
	// budget elapses and the practitioner is told only "timed out" — the 404, its
	// code, and the fact that the DELETE already succeeded are all discarded.
	//
	// That path became reachable when IsNotFound started requiring a flat
	// envelope: a delete poll that meets a routing 404 no longer short-circuits to
	// "deleted", it retries for the full timeout (10 minutes on instances) and
	// then reports a bare deadline. Naming the cause is what makes that
	// diagnosable, and it is the difference between "retry, your gateway is
	// mid-rollout" and a practitioner reaching for `terraform state rm` — which
	// is the one action that re-creates the orphaning this all exists to prevent.
	var lastPollErr error
	timedOut := func(state string) error {
		switch {
		case lastPollErr != nil && state != "":
			return fmt.Errorf("timed out waiting for %s (last state: %s, last poll error: %v): %w",
				cfg.ResourceName, state, lastPollErr, ctx.Err())
		case lastPollErr != nil:
			return fmt.Errorf("timed out waiting for %s (last poll error: %v): %w",
				cfg.ResourceName, lastPollErr, ctx.Err())
		case state != "":
			return fmt.Errorf("timed out waiting for %s (last state: %s): %w",
				cfg.ResourceName, state, ctx.Err())
		default:
			return fmt.Errorf("timed out waiting for %s: %w", cfg.ResourceName, ctx.Err())
		}
	}

	for {
		state, err := cfg.PollFunc(ctx)
		if err != nil {
			lastPollErr = err
			if ctx.Err() != nil {
				return "", timedOut("")
			}
			// Transient errors during polling are retried
			select {
			case <-ctx.Done():
				return "", timedOut("")
			case <-ticker.C:
				continue
			}
		}
		lastPollErr = nil

		if targetSet[state] {
			return state, nil
		}

		if errorSet[state] {
			return state, fmt.Errorf("%s entered error state: %s", cfg.ResourceName, state)
		}

		select {
		case <-ctx.Done():
			return state, timedOut(state)
		case <-ticker.C:
			// continue polling
		}
	}
}
