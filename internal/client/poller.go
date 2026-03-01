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

	for {
		state, err := cfg.PollFunc(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return "", fmt.Errorf("timed out waiting for %s: %w", cfg.ResourceName, ctx.Err())
			}
			// Transient errors during polling are retried
			select {
			case <-ctx.Done():
				return "", fmt.Errorf("timed out waiting for %s: %w", cfg.ResourceName, ctx.Err())
			case <-ticker.C:
				continue
			}
		}

		if targetSet[state] {
			return state, nil
		}

		if errorSet[state] {
			return state, fmt.Errorf("%s entered error state: %s", cfg.ResourceName, state)
		}

		select {
		case <-ctx.Done():
			return state, fmt.Errorf("timed out waiting for %s (last state: %s): %w", cfg.ResourceName, state, ctx.Err())
		case <-ticker.C:
			// continue polling
		}
	}
}
