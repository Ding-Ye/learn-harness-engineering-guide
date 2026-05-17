package main

import "time"

// Clock is the boundary we put around time.Now() so tests can freeze the
// date used to derive log filenames. Production code wires in RealClock;
// tests wire in FakeClock and pin the day of the week / calendar.
//
// We intentionally keep this interface minimal — only Now(). The upstream
// guide's Python sketch (guide/memory-and-context.md L130-L143) calls
// datetime.now() directly; introducing this seam is the first concession
// to "code we want to test deterministically".
type Clock interface {
	Now() time.Time
}

// RealClock returns the wall-clock time. Use this in main / production.
type RealClock struct{}

// Now returns time.Now().
func (RealClock) Now() time.Time { return time.Now() }

// FakeClock returns a fixed point in time. Use in tests to pin the date
// used by AppendLog / Read so YYYY-MM-DD filenames are deterministic.
type FakeClock struct {
	T time.Time
}

// Now returns the pinned time.
func (f FakeClock) Now() time.Time { return f.T }
