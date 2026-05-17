package main

import (
	"fmt"
	"sort"
	"time"
)

// Scheduler is a registry of named CronSchedules plus a single Tick entry
// point that, given a "current time", returns the names of every schedule
// whose ShouldRun predicate fires at that exact minute.
//
// Crucially this type does NOT own a goroutine, a time.Ticker, or any clock
// of its own. The caller drives the clock — pass `time.Now().UTC()` in
// production, or a frozen time.Time in tests. This is the same shape as
// s05's injected Clock and s09's deterministic message feeder: a teaching
// chapter that can't be tested in milliseconds is a teaching chapter that
// drifts.
//
// "Real" scheduler infrastructure adds: persistence, distributed locking
// (so a job doesn't double-fire across replicas), retry, jitter, and a
// goroutine pool. All of those are orthogonal to the cron primitive itself,
// which is what this chapter is about. See the upstream
// scheduling-and-automation.md L78-L160 for the architectural framing.
type Scheduler struct {
	schedules map[string]*CronSchedule
}

// NewScheduler returns an empty scheduler. Schedules are registered with Add
// and never garbage-collected — a long-running harness might want a Remove
// counterpart but that's a one-liner to add when needed.
func NewScheduler() *Scheduler {
	return &Scheduler{schedules: make(map[string]*CronSchedule)}
}

// Add registers a schedule under the given name. Re-adding the same name
// overwrites — calling code that wants "create unless exists" should check
// itself; we pick overwrite because it makes "reload config from disk"
// trivially idempotent.
func (s *Scheduler) Add(name string, sch *CronSchedule) error {
	if name == "" {
		return fmt.Errorf("scheduler: empty schedule name")
	}
	if sch == nil {
		return fmt.Errorf("scheduler: nil schedule for %q", name)
	}
	s.schedules[name] = sch
	return nil
}

// Tick returns the names of every schedule whose ShouldRun fires at `now`.
// Returned names are sorted alphabetically so callers can rely on stable
// ordering when persisting Tick output to an event log (s10) or threading it
// through to a worker pool.
//
// The caller is responsible for actually invoking the work. We just say
// "these are due." That separation matters: a real harness will want to
// rate-limit, deduplicate against a "last-fired" marker, or coalesce
// multiple Tick results before dispatching. Returning names instead of
// firing callbacks keeps those policy decisions in the caller's hands.
func (s *Scheduler) Tick(now time.Time) []string {
	due := make([]string, 0)
	for name, sch := range s.schedules {
		if sch.ShouldRun(now) {
			due = append(due, name)
		}
	}
	sort.Strings(due)
	return due
}

// Len reports how many schedules are currently registered. Useful in tests
// and for a "/cron list" debug command.
func (s *Scheduler) Len() int {
	return len(s.schedules)
}
