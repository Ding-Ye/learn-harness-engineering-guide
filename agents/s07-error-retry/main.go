package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"
)

// main is a tiny CLI demo of the error-retry pipeline:
//
//  1. A fake "call_llm" function that fails twice with a rate-limit
//     error and then succeeds on the third try.
//  2. RetryWithBackoff drives it with a FakeSleeper so we can print
//     the backoff schedule without waiting wall-clock seconds.
//  3. We also demonstrate the "do not retry permanent" case by
//     running the same retry loop against os.ErrNotExist.
//
// Run with:
//
//	go run .
func main() {
	demoRetriesUntilSuccess()
	fmt.Println()
	demoDoesNotRetryPermanent()
}

func demoRetriesUntilSuccess() {
	fmt.Println("=== demo: transient errors retried with backoff ===")

	// Fake LLM call that fails twice with "rate limit" then succeeds.
	calls := 0
	fakeLLMCall := func() error {
		calls++
		if calls < 3 {
			return fmt.Errorf("HTTP 429: rate limit exceeded")
		}
		return nil
	}

	sleeper := &FakeSleeper{}
	cfg := RetryConfig{
		MaxAttempts: 5,
		BaseDelay:   2 * time.Second,
		MaxDelay:    30 * time.Second,
		Jitter:      false, // off so demo output is deterministic
	}

	err := RetryWithBackoff(context.Background(), cfg, sleeper, fakeLLMCall)
	if err != nil {
		fmt.Fprintf(os.Stderr, "unexpected: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("call succeeded after %d attempts\n", calls)
	for i, d := range sleeper.Sleeps {
		fmt.Printf("  backoff #%d: slept %v\n", i+1, d)
	}
}

func demoDoesNotRetryPermanent() {
	fmt.Println("=== demo: permanent error returned immediately ===")

	calls := 0
	missing := func() error {
		calls++
		return os.ErrNotExist
	}

	sleeper := &FakeSleeper{}
	cfg := DefaultRetryConfig()
	cfg.Jitter = false

	err := RetryWithBackoff(context.Background(), cfg, sleeper, missing)
	if err == nil {
		fmt.Fprintln(os.Stderr, "expected error but got nil")
		os.Exit(1)
	}
	if !errors.Is(err, os.ErrNotExist) {
		fmt.Fprintf(os.Stderr, "expected os.ErrNotExist, got %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("call attempted %d time(s); classified as %s, returned without retry\n",
		calls, Classify(err))
	fmt.Printf("sleeps recorded: %d (expected 0)\n", len(sleeper.Sleeps))
}
