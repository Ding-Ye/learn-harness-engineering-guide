package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"testing"
	"time"
)

// fakeNetTimeoutErr satisfies net.Error with Timeout()=true so we can
// exercise the typed-net.Error branch of Classify without making a real
// network call.
type fakeNetTimeoutErr struct{}

func (fakeNetTimeoutErr) Error() string   { return "i/o timeout" }
func (fakeNetTimeoutErr) Timeout() bool   { return true }
func (fakeNetTimeoutErr) Temporary() bool { return true }

// Make sure the type assertion in Classify actually finds it.
var _ net.Error = fakeNetTimeoutErr{}

// TestClassify_TransientSignals exercises the substring matcher for
// every transient signal we promise to recognise. A miss here would
// silently disable retries for that signal — a regression worth catching.
func TestClassify_TransientSignals(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want ErrorClass
	}{
		{"net.Error timeout typed", fakeNetTimeoutErr{}, Transient},
		{"http 429 string", errors.New("HTTP 429 Too Many Requests"), Transient},
		{"rate limit string", errors.New("openai: rate limit exceeded"), Transient},
		{"503 string", errors.New("upstream 503 Service Unavailable"), Transient},
		{"502 string", errors.New("502 bad gateway"), Transient},
		{"504 string", errors.New("504 gateway timeout"), Transient},
		{"connection reset", errors.New("read tcp: connection reset by peer"), Transient},
		{"connection refused", errors.New("dial tcp: connection refused"), Transient},
		{"EOF string", errors.New("unexpected EOF reading response"), Transient},
		{"timeout substring", errors.New("operation timeout after 30s"), Transient},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Classify(tc.err); got != tc.want {
				t.Errorf("Classify(%v) = %s, want %s", tc.err, got, tc.want)
			}
		})
	}
}

// TestClassify_ResourceSignals checks the system-exhaustion bucket.
// These MUST NOT be reclassified as Transient — retrying an OOM error
// just causes another OOM.
func TestClassify_ResourceSignals(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want ErrorClass
	}{
		{"out of memory", errors.New("out of memory: process killed"), Resource},
		{"token limit", errors.New("token limit exceeded for model"), Resource},
		{"context length exceeded", errors.New("context length exceeded: 200000 > 128000"), Resource},
		{"disk full", errors.New("write failed: disk full"), Resource},
		{"no space left", errors.New("write failed: no space left on device"), Resource},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Classify(tc.err); got != tc.want {
				t.Errorf("Classify(%v) = %s, want %s", tc.err, got, tc.want)
			}
		})
	}
}

// TestClassify_ModelSignals verifies that things the *model* gets wrong
// (unknown tool, invalid JSON, schema mismatch) classify as ModelError
// — recovery is a re-prompt, not a backoff.
func TestClassify_ModelSignals(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want ErrorClass
	}{
		{"unknown tool", errors.New("unknown tool 'read_fyle'"), ModelError},
		{"invalid json", errors.New("tool args: invalid json"), ModelError},
		{"schema validation", errors.New("schema validation failed: 'path' required"), ModelError},
		{"malformed", errors.New("malformed tool_use block"), ModelError},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Classify(tc.err); got != tc.want {
				t.Errorf("Classify(%v) = %s, want %s", tc.err, got, tc.want)
			}
		})
	}
}

// TestClassify_PermanentSignal verifies that os.IsNotExist (including
// wrapped variants) classify as Permanent — these never retry.
func TestClassify_PermanentSignal(t *testing.T) {
	cases := []struct {
		name string
		err  error
	}{
		{"raw os.ErrNotExist", os.ErrNotExist},
		{"wrapped not-exist", fmt.Errorf("open /no/such: %w", os.ErrNotExist)},
		{"PathError from missing file", func() error {
			_, err := os.Open("/definitely-does-not-exist-s07-test")
			return err
		}()},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Classify(tc.err); got != Permanent {
				t.Errorf("Classify(%v) = %s, want Permanent", tc.err, got)
			}
		})
	}
}

// TestRetry_RetriesTransientUntilSuccess exercises the happy retry
// path: fn fails twice with a transient error, succeeds on the third
// try, and the FakeSleeper recorded exactly two backoffs (one between
// each failure and the next attempt).
func TestRetry_RetriesTransientUntilSuccess(t *testing.T) {
	calls := 0
	fn := func() error {
		calls++
		if calls < 3 {
			return errors.New("HTTP 429: rate limit")
		}
		return nil
	}

	sleeper := &FakeSleeper{}
	cfg := RetryConfig{
		MaxAttempts: 5,
		BaseDelay:   100 * time.Millisecond,
		MaxDelay:    1 * time.Second,
		Jitter:      false,
	}

	if err := RetryWithBackoff(context.Background(), cfg, sleeper, fn); err != nil {
		t.Fatalf("RetryWithBackoff: %v", err)
	}

	if calls != 3 {
		t.Errorf("fn called %d times, want 3", calls)
	}
	if len(sleeper.Sleeps) != 2 {
		t.Fatalf("FakeSleeper recorded %d sleeps, want 2 (one between each pair of attempts)\n  sleeps: %v",
			len(sleeper.Sleeps), sleeper.Sleeps)
	}
	// With Jitter=false, sleeps should be exactly BaseDelay, BaseDelay*2.
	if want := 100 * time.Millisecond; sleeper.Sleeps[0] != want {
		t.Errorf("first backoff = %v, want %v", sleeper.Sleeps[0], want)
	}
	if want := 200 * time.Millisecond; sleeper.Sleeps[1] != want {
		t.Errorf("second backoff = %v, want %v", sleeper.Sleeps[1], want)
	}
}

// TestRetry_DoesNotRetryPermanent ensures os.ErrNotExist (and other
// Permanent errors) propagate on the first failure with zero sleeps.
// The original error is preserved — errors.Is still finds it.
func TestRetry_DoesNotRetryPermanent(t *testing.T) {
	calls := 0
	fn := func() error {
		calls++
		return os.ErrNotExist
	}

	sleeper := &FakeSleeper{}
	cfg := DefaultRetryConfig()

	err := RetryWithBackoff(context.Background(), cfg, sleeper, fn)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, os.ErrNotExist) {
		t.Errorf("error chain lost os.ErrNotExist: got %v", err)
	}
	if calls != 1 {
		t.Errorf("fn called %d times, want 1 (Permanent should not retry)", calls)
	}
	if len(sleeper.Sleeps) != 0 {
		t.Errorf("FakeSleeper recorded %d sleeps, want 0 (no retries on Permanent)\n  sleeps: %v",
			len(sleeper.Sleeps), sleeper.Sleeps)
	}
	// Also verify the error did NOT get wrapped in RetryExhaustedError.
	var rex *RetryExhaustedError
	if errors.As(err, &rex) {
		t.Errorf("Permanent error wrongly wrapped in RetryExhaustedError: %v", err)
	}
}

// TestRetry_RaisesAfterMaxAttempts: fn always returns a transient error.
// After MaxAttempts=3 we expect a RetryExhaustedError with Attempts=3
// and the original error reachable via errors.Unwrap.
func TestRetry_RaisesAfterMaxAttempts(t *testing.T) {
	calls := 0
	transient := errors.New("HTTP 503: service unavailable")
	fn := func() error {
		calls++
		return transient
	}

	sleeper := &FakeSleeper{}
	cfg := RetryConfig{
		MaxAttempts: 3,
		BaseDelay:   10 * time.Millisecond,
		MaxDelay:    100 * time.Millisecond,
		Jitter:      false,
	}

	err := RetryWithBackoff(context.Background(), cfg, sleeper, fn)
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	var rex *RetryExhaustedError
	if !errors.As(err, &rex) {
		t.Fatalf("expected *RetryExhaustedError, got %T: %v", err, err)
	}
	if rex.Attempts != 3 {
		t.Errorf("RetryExhaustedError.Attempts = %d, want 3", rex.Attempts)
	}
	if !errors.Is(rex.LastError, transient) {
		t.Errorf("LastError lost the original error: got %v", rex.LastError)
	}
	if calls != 3 {
		t.Errorf("fn called %d times, want 3", calls)
	}
	// 3 attempts → 2 backoff sleeps (no sleep after the last failure).
	if len(sleeper.Sleeps) != 2 {
		t.Errorf("FakeSleeper recorded %d sleeps, want 2 (no sleep after final attempt)\n  sleeps: %v",
			len(sleeper.Sleeps), sleeper.Sleeps)
	}
}

// TestRetry_HonorsContextCancellation: cancel the context mid-retry
// and the call should return ctx.Err() promptly rather than running
// through the remaining attempts.
//
// We exploit the FakeSleeper path's "record then check ctx.Err()"
// shortcut: cancelling the context BEFORE calling RetryWithBackoff
// would not exercise mid-backoff cancellation, so instead we cancel
// from inside fn() on the first call. The second attempt should
// short-circuit on ctx.Err() check before invoking fn() again.
func TestRetry_HonorsContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	calls := 0
	fn := func() error {
		calls++
		// On the first call, cancel the context, then return a transient
		// error so the retry layer tries to back off and re-check ctx.
		if calls == 1 {
			cancel()
			return errors.New("rate limit (will trigger backoff)")
		}
		// We should never reach this.
		return nil
	}

	sleeper := &FakeSleeper{}
	cfg := RetryConfig{
		MaxAttempts: 5,
		BaseDelay:   10 * time.Millisecond,
		MaxDelay:    100 * time.Millisecond,
		Jitter:      false,
	}

	err := RetryWithBackoff(ctx, cfg, sleeper, fn)
	if err == nil {
		t.Fatal("expected context.Canceled, got nil")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected context.Canceled, got %v", err)
	}
	if calls != 1 {
		t.Errorf("fn called %d times, want 1 (cancellation should short-circuit further attempts)", calls)
	}
	// Sleeps may be 1 — we recorded one backoff before noticing cancellation —
	// but MUST NOT be 4 (the remaining schedule).
	if got := len(sleeper.Sleeps); got > 1 {
		t.Errorf("FakeSleeper recorded %d sleeps, want at most 1 (cancellation should stop the loop)\n  sleeps: %v",
			got, sleeper.Sleeps)
	}
}
