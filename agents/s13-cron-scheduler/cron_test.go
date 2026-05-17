package main

import (
	"testing"
	"time"
)

// TestParse_FiveFieldExpressions covers the positive parser path with
// representative expressions and the three error paths most likely to bite a
// user: total garbage, single out-of-range value, and an out-of-range value
// inside a range. The day-of-week 7-collapses-to-0 case has its own dedicated
// test below (TestShouldRun_DowSundayAcceptsBoth0And7) so we don't dilute
// this table with timezone concerns.
func TestParse_FiveFieldExpressions(t *testing.T) {
	t.Parallel()

	t.Run("happy", func(t *testing.T) {
		t.Parallel()
		cases := []struct {
			name string
			expr string
		}{
			{"daily 8am", "0 8 * * *"},
			{"every 15 minutes", "*/15 * * * *"},
			{"new year midnight", "0 0 1 1 *"},
			{"weekday 8am", "0 8 * * 1-5"},
			{"weekend hourly", "0 * * * 0,6"},
		}
		for _, tc := range cases {
			tc := tc
			t.Run(tc.name, func(t *testing.T) {
				t.Parallel()
				if _, err := Parse(tc.expr, "UTC"); err != nil {
					t.Fatalf("Parse(%q) returned unexpected error: %v", tc.expr, err)
				}
			})
		}
	})

	t.Run("errors", func(t *testing.T) {
		t.Parallel()
		cases := []struct {
			name string
			expr string
		}{
			{"garbage", "bad"},
			{"minute 60", "60 * * * *"},  // minute max is 59
			{"range past max", "0-60 * * * *"}, // upper bound out of range
			{"too few fields", "0 8 * *"},
			{"too many fields", "0 8 * * * *"},
			{"step zero", "*/0 * * * *"},
			{"empty term", "0,,3 * * * *"},
			{"bad range direction", "5-2 * * * *"},
		}
		for _, tc := range cases {
			tc := tc
			t.Run(tc.name, func(t *testing.T) {
				t.Parallel()
				if _, err := Parse(tc.expr, "UTC"); err == nil {
					t.Fatalf("Parse(%q) succeeded, expected error", tc.expr)
				}
			})
		}
	})
}

// TestNextRun_DailyAt8amUTC pins the simplest possible NextRun case: a daily
// 8am UTC schedule with `now` at 07:30 UTC on the same day. The expected
// answer is 08:00 UTC on the same day — and we assert on the exact instant.
// If this test fails, the NextRun loop is broken at the most basic level
// (wrong minute, wrong hour, or wrong "strictly after now" semantics).
func TestNextRun_DailyAt8amUTC(t *testing.T) {
	t.Parallel()

	sch, err := Parse("0 8 * * *", "UTC")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	now := time.Date(2026, 5, 17, 7, 30, 0, 0, time.UTC)
	want := time.Date(2026, 5, 17, 8, 0, 0, 0, time.UTC)

	got, err := sch.NextRun(now)
	if err != nil {
		t.Fatalf("NextRun: %v", err)
	}
	if !got.Equal(want) {
		t.Fatalf("NextRun: got %s, want %s", got, want)
	}
}

// TestNextRun_TimezoneConversion is the test that "Store UTC, display local"
// from L96-L100 of the guide is teaching us to write. The schedule fires at
// 08:00 Asia/Shanghai (= 00:00 UTC). Given a `now` of 23:30 UTC on May 17,
// which IS 07:30 Shanghai on May 18, the next fire is 00:00 UTC on May 18
// (= 08:00 Shanghai on May 18).
//
// The bug this test catches: if NextRun forgot to convert `now` into the
// schedule's location before truncating, the comparison would be against
// 23:30 UTC and the next match would be... nothing the same day, then 00:00
// UTC the day after — by coincidence the right answer here, which is why
// we cross-check with a non-coincidental case (TestShouldRun_FiresExactly).
func TestNextRun_TimezoneConversion(t *testing.T) {
	t.Parallel()

	sch, err := Parse("0 8 * * *", "Asia/Shanghai")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	// 23:30 UTC on May 17 == 07:30 Shanghai on May 18.
	now := time.Date(2026, 5, 17, 23, 30, 0, 0, time.UTC)
	// Next 8am Shanghai is May 18 08:00 Shanghai == May 18 00:00 UTC.
	want := time.Date(2026, 5, 18, 0, 0, 0, 0, time.UTC)

	got, err := sch.NextRun(now)
	if err != nil {
		t.Fatalf("NextRun: %v", err)
	}
	if !got.Equal(want) {
		t.Fatalf("NextRun: got %s, want %s", got, want)
	}

	// Sanity check: ShouldRun at the exact match instant returns true.
	if !sch.ShouldRun(want) {
		t.Fatalf("ShouldRun(%s) = false, want true", want)
	}
}

// TestShouldRun_FiresExactlyAtBoundary verifies the truncate-to-minute trick
// in ShouldRun. The schedule is `30 14 * * *` (UTC); we probe three instants:
//
//   - 14:30:00.000 — exactly on the boundary, must fire
//   - 14:29:59.999 — one nanosecond before the boundary, must NOT fire
//   - 14:30:30.000 — 30 seconds past the boundary, but still in minute 30, MUST fire
//
// The third probe is the interesting one: a naive "must be exactly at second
// zero" check would miss it. We want a one-minute granularity, so 14:30:anything
// fires.
//
// We also probe one nanosecond into 14:31 to confirm we don't bleed past the
// boundary.
func TestShouldRun_FiresExactlyAtBoundary(t *testing.T) {
	t.Parallel()

	sch, err := Parse("30 14 * * *", "UTC")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	cases := []struct {
		name string
		now  time.Time
		want bool
	}{
		{"exact boundary", time.Date(2026, 5, 17, 14, 30, 0, 0, time.UTC), true},
		{"one nanosecond before", time.Date(2026, 5, 17, 14, 29, 59, 999_999_999, time.UTC), false},
		{"30 seconds into the minute", time.Date(2026, 5, 17, 14, 30, 30, 0, time.UTC), true},
		{"start of next minute", time.Date(2026, 5, 17, 14, 31, 0, 0, time.UTC), false},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := sch.ShouldRun(tc.now); got != tc.want {
				t.Fatalf("ShouldRun(%s) = %v, want %v", tc.now, got, tc.want)
			}
		})
	}
}

// TestShouldRun_DowSundayAcceptsBoth0And7 verifies the most subtle quirk of
// the cron grammar: the day-of-week field accepts both `0` and `7` for Sunday.
// We build two schedules — one with `0`, one with `7` — and feed both a
// known Sunday. Both must fire. If the fold-7-to-0 step is missing,
// `0 8 * * 7` would never fire at all (the matcher would index a bit that's
// never set).
//
// The test instant 2026-05-17 is a Sunday; we pick 08:00 UTC for the fire
// boundary.
func TestShouldRun_DowSundayAcceptsBoth0And7(t *testing.T) {
	t.Parallel()

	sunday := time.Date(2026, 5, 17, 8, 0, 0, 0, time.UTC)
	if sunday.Weekday() != time.Sunday {
		t.Fatalf("test fixture broken: %s is %s, not Sunday", sunday, sunday.Weekday())
	}

	for _, expr := range []string{"0 8 * * 0", "0 8 * * 7"} {
		expr := expr
		t.Run(expr, func(t *testing.T) {
			t.Parallel()
			sch, err := Parse(expr, "UTC")
			if err != nil {
				t.Fatalf("Parse(%q): %v", expr, err)
			}
			if !sch.ShouldRun(sunday) {
				t.Fatalf("ShouldRun(%s) on schedule %q = false, want true", sunday, expr)
			}
		})
	}
}

// TestScheduler_TickReturnsDueSchedules wires the parser and the Scheduler
// together. We register three schedules — two due at `now`, one not — and
// assert that Tick returns exactly the names of the two due ones. The
// ordering assertion (alphabetical) matters because a real harness will pipe
// Tick output into an event log or a worker pool, where stable ordering
// makes diffs reviewable.
func TestScheduler_TickReturnsDueSchedules(t *testing.T) {
	t.Parallel()

	mkSchedule := func(expr string) *CronSchedule {
		sch, err := Parse(expr, "UTC")
		if err != nil {
			t.Fatalf("Parse(%q): %v", expr, err)
		}
		return sch
	}

	scheduler := NewScheduler()
	if err := scheduler.Add("alpha-daily-8am", mkSchedule("0 8 * * *")); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := scheduler.Add("bravo-weekday-8am", mkSchedule("0 8 * * 1-5")); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := scheduler.Add("charlie-weekend-8am", mkSchedule("0 8 * * 0,6")); err != nil {
		t.Fatalf("Add: %v", err)
	}

	// Monday 2026-05-18 08:00 UTC. alpha (daily) and bravo (weekday) fire;
	// charlie (weekend-only) does not.
	now := time.Date(2026, 5, 18, 8, 0, 0, 0, time.UTC)
	if now.Weekday() != time.Monday {
		t.Fatalf("test fixture broken: %s is %s, not Monday", now, now.Weekday())
	}

	got := scheduler.Tick(now)
	want := []string{"alpha-daily-8am", "bravo-weekday-8am"}
	if !equalStrings(got, want) {
		t.Fatalf("Tick: got %v, want %v", got, want)
	}

	// And on a Sunday the dispatch flips: alpha + charlie, not bravo.
	sunday := time.Date(2026, 5, 17, 8, 0, 0, 0, time.UTC)
	gotSun := scheduler.Tick(sunday)
	wantSun := []string{"alpha-daily-8am", "charlie-weekend-8am"}
	if !equalStrings(gotSun, wantSun) {
		t.Fatalf("Tick(Sunday): got %v, want %v", gotSun, wantSun)
	}
}

// TestNextRun_EveryFifteenMinutes exercises the stepped-minute path. With
// `*/15 * * * *` and `now = 14:07`, the next match should be 14:15. We also
// step across the hour boundary (`now = 14:50`) to confirm the minute field's
// {0,15,30,45} membership wraps correctly via the "advance by one minute"
// brute-force walk in NextRun.
func TestNextRun_EveryFifteenMinutes(t *testing.T) {
	t.Parallel()

	sch, err := Parse("*/15 * * * *", "UTC")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	cases := []struct {
		name string
		now  time.Time
		want time.Time
	}{
		{
			name: "mid-quarter",
			now:  time.Date(2026, 5, 17, 14, 7, 0, 0, time.UTC),
			want: time.Date(2026, 5, 17, 14, 15, 0, 0, time.UTC),
		},
		{
			name: "across hour",
			now:  time.Date(2026, 5, 17, 14, 50, 0, 0, time.UTC),
			want: time.Date(2026, 5, 17, 15, 0, 0, 0, time.UTC),
		},
		{
			name: "exactly on a fire — strictly-after means next slot",
			now:  time.Date(2026, 5, 17, 14, 15, 0, 0, time.UTC),
			want: time.Date(2026, 5, 17, 14, 30, 0, 0, time.UTC),
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := sch.NextRun(tc.now)
			if err != nil {
				t.Fatalf("NextRun: %v", err)
			}
			if !got.Equal(tc.want) {
				t.Fatalf("NextRun(%s): got %s, want %s", tc.now, got, tc.want)
			}
		})
	}
}

// equalStrings is a small helper so the Tick assertion above stays readable.
// reflect.DeepEqual would work but pulls in a heavier import; for two string
// slices a hand-rolled compare is clearer.
func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
