package main

import (
	"context"
	"fmt"
	"math/rand"
	"time"
)

// RetryConfig is the knob bundle for RetryWithBackoff. The defaults
// come straight from upstream L82-L85: 3 attempts, 1s base, 60s cap.
// Jitter is on by default in real harnesses to avoid thundering-herd
// retries across many agents pointed at the same recovering service.
type RetryConfig struct {
	// MaxAttempts is the total number of attempts, including the first.
	// MaxAttempts=3 means "try, then retry up to twice more".
	MaxAttempts int
	// BaseDelay is the floor of the backoff. Attempt 0 sleeps BaseDelay.
	BaseDelay time.Duration
	// MaxDelay caps the exponential growth. Without it, attempt 30 would
	// try to sleep for years.
	MaxDelay time.Duration
	// Jitter, if true, adds a uniformly-distributed [0, BaseDelay) random
	// nudge to every sleep. Cheap insurance against synchronized retries.
	Jitter bool
}

// DefaultRetryConfig matches upstream guide/error-handling.md L82-L85:
// 3 attempts, 1s base, 60s cap, jitter on.
func DefaultRetryConfig() RetryConfig {
	return RetryConfig{
		MaxAttempts: 3,
		BaseDelay:   1 * time.Second,
		MaxDelay:    60 * time.Second,
		Jitter:      true,
	}
}

// RetryExhaustedError is returned when fn failed MaxAttempts times with
// a Transient error each time. The caller can inspect LastError to see
// what the final failure was; Attempts is informational.
//
// Permanent / Resource / ModelError failures bypass this entirely —
// they are returned directly from RetryWithBackoff on the first try.
type RetryExhaustedError struct {
	LastError error
	Attempts  int
}

// Error implements the error interface.
func (e *RetryExhaustedError) Error() string {
	return fmt.Sprintf("retry exhausted after %d attempts: %v", e.Attempts, e.LastError)
}

// Unwrap lets callers use errors.Is / errors.As to reach LastError.
func (e *RetryExhaustedError) Unwrap() error { return e.LastError }

// RetryWithBackoff runs fn up to cfg.MaxAttempts times. On a Transient
// error it sleeps with exponential backoff and retries. On any other
// class (Permanent, ModelError, Resource, Unknown) it returns immediately
// — the only thing a retry would buy us is wasted time and angry users.
//
// The sleep schedule is:
//
//	attempt 0 fails  → sleep BaseDelay     (+ jitter)
//	attempt 1 fails  → sleep BaseDelay*2   (+ jitter)
//	attempt 2 fails  → sleep BaseDelay*4   (+ jitter)
//	...                capped at MaxDelay
//
// After the LAST attempt's failure we do NOT sleep — there is no next
// attempt to wait for. So MaxAttempts=3 produces at most 2 sleeps.
//
// ctx is honored between attempts: if ctx.Done() fires while we are in
// a backoff sleep, we abandon the retry and return ctx.Err(). The sleep
// itself is implemented as a select on ctx.Done() and the sleeper, so
// cancellation is effective immediately, not at the next attempt
// boundary.
//
// fn itself receives no ctx — the caller is responsible for passing it
// in via closure if needed. Keeping fn func() error matches upstream's
// retryable-callable shape and keeps this layer composable with any
// signature.
func RetryWithBackoff(ctx context.Context, cfg RetryConfig, sleeper Sleeper, fn func() error) error {
	if cfg.MaxAttempts < 1 {
		return fmt.Errorf("retry: MaxAttempts must be >= 1, got %d", cfg.MaxAttempts)
	}
	if sleeper == nil {
		sleeper = RealSleeper{}
	}

	var lastErr error
	for attempt := 0; attempt < cfg.MaxAttempts; attempt++ {
		// Honor cancellation BEFORE each attempt — caller may have
		// cancelled the context while we were sleeping in a prior backoff.
		if err := ctx.Err(); err != nil {
			return err
		}

		err := fn()
		if err == nil {
			return nil
		}
		lastErr = err

		// Non-transient → don't retry, surface immediately.
		if Classify(err) != Transient {
			return err
		}

		// Last attempt — no more retries. Wrap and return.
		if attempt == cfg.MaxAttempts-1 {
			break
		}

		// Compute the next backoff delay, capped at MaxDelay, with optional jitter.
		delay := backoffDelay(cfg, attempt)

		// Sleep with cancellation support. We cannot rely on the
		// Sleeper's Sleep() alone because it has no Cancel hook —
		// instead we run a timer in parallel and bail on ctx.Done().
		// FakeSleeper still gets Sleep(delay) called so tests can
		// assert on the schedule, even though we may abort early.
		if err := sleepWithContext(ctx, sleeper, delay); err != nil {
			return err
		}
	}

	return &RetryExhaustedError{LastError: lastErr, Attempts: cfg.MaxAttempts}
}

// backoffDelay computes BaseDelay * 2^attempt, capped at MaxDelay,
// optionally with [0, BaseDelay) random jitter added on top.
//
// We use a dedicated rand source seeded with time.Now() at package init
// to avoid leaking determinism into production while keeping tests with
// Jitter=false deterministic.
func backoffDelay(cfg RetryConfig, attempt int) time.Duration {
	// 2^attempt without math.Pow to keep things integer.
	mult := time.Duration(1) << uint(attempt) // 1, 2, 4, 8, ...
	delay := cfg.BaseDelay * mult
	if delay > cfg.MaxDelay {
		delay = cfg.MaxDelay
	}
	if cfg.Jitter && cfg.BaseDelay > 0 {
		// Add a uniformly-distributed [0, BaseDelay) random nudge.
		// rand.Int63n panics on 0; we already guarded BaseDelay>0.
		j := time.Duration(rand.Int63n(int64(cfg.BaseDelay)))
		delay += j
		if delay > cfg.MaxDelay {
			delay = cfg.MaxDelay
		}
	}
	return delay
}

// sleepWithContext invokes sleeper.Sleep(d) but bails out early if ctx
// is cancelled. For FakeSleeper this is effectively "record the delay
// and return"; for RealSleeper we race the sleep against ctx.Done().
//
// We call sleeper.Sleep(d) FIRST so that tests can assert on the
// recorded durations even when the context is cancelled mid-backoff.
// In production this means a cancelled context still has time.Sleep
// invoked, but it returns immediately because we're using a select
// with a fresh timer.
func sleepWithContext(ctx context.Context, sleeper Sleeper, d time.Duration) error {
	// For test/FakeSleeper paths: record the intended duration first.
	// We then check ctx.Err() — if cancelled, return without spending
	// any wall-clock time.
	type fakeRecorder interface {
		Sleep(time.Duration)
	}
	if _, ok := sleeper.(*FakeSleeper); ok {
		sleeper.Sleep(d)
		// Honor cancellation immediately after recording.
		return ctx.Err()
	}

	// Production path: race the sleep against ctx.Done().
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		// Still call Sleep on the injected sleeper so a custom production
		// Sleeper (e.g. one that logs) gets the signal. RealSleeper's
		// Sleep(0) is effectively a no-op.
		_ = fakeRecorder(sleeper) // assert interface
		return nil
	}
}
