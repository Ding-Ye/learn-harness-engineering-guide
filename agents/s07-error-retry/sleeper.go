package main

import "time"

// Sleeper is the seam we put around time.Sleep so tests can avoid
// wall-clock waits. Upstream Python (guide/error-handling.md L105)
// calls time.sleep() directly inside the retry decorator; that is fine
// in a Python REPL but a unit-test killer.
//
// Production uses RealSleeper{} (a thin wrapper over time.Sleep).
// Tests use FakeSleeper{} which records every duration into a slice
// so assertions can verify "exactly two backoffs happened, each
// between BaseDelay and BaseDelay+Jitter".
//
// We deliberately keep the interface minimal — just Sleep(d). A
// fancier interface (with Cancel(), Reset(), etc.) is not needed for
// the retry logic in this chapter; ctx.Done() handles cancellation
// at a higher level.
type Sleeper interface {
	Sleep(d time.Duration)
}

// RealSleeper is the production implementation. It uses time.Sleep
// directly. Zero-sized — pass as a value, not a pointer.
type RealSleeper struct{}

// Sleep blocks the calling goroutine for d.
func (RealSleeper) Sleep(d time.Duration) { time.Sleep(d) }

// FakeSleeper records every Sleep duration without actually sleeping.
// It is safe for single-goroutine use only (the retry loop runs on
// one goroutine, so we do not pay for a mutex here). If a future test
// drives concurrent retries through the same FakeSleeper, wrap with
// sync.Mutex at the call site.
type FakeSleeper struct {
	// Sleeps is the ordered list of durations Sleep() was called with.
	// Tests read it back to assert the backoff schedule.
	Sleeps []time.Duration
}

// Sleep records d and returns immediately. Time passes only inside
// the test's assertions, not on the wall clock.
func (f *FakeSleeper) Sleep(d time.Duration) {
	f.Sleeps = append(f.Sleeps, d)
}
