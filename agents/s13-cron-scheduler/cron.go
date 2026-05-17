package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// CronSchedule is the parsed form of a 5-field cron expression bound to a
// timezone. The Expression and Timezone fields are kept for round-trip
// readability (logs, debugging, marshaling); the actual matching is driven by
// the five fieldSet bitmaps which were produced once at Parse time.
//
// Why store both the raw Expression AND the parsed bitmaps? Because the parse
// is a pure function of (Expression, Timezone) and the test for "did we set
// up this Cron correctly" lives in the raw Expression. The bitmaps are an
// implementation detail. A future feature like a `/cron list` admin command
// is easier to write off Expression than off a bitmap.
//
// The Payload field is a free-form JSON blob: an upstream "this is what the
// Cron should DO when it fires" message. We don't interpret it — that's the
// caller's job. We just hold onto it so the Scheduler can hand the right
// payload back when a schedule fires (see Scheduler.Tick).
type CronSchedule struct {
	Expression string          // verbatim, e.g. "0 8 * * *"
	Timezone   string          // IANA name, e.g. "Asia/Shanghai" or "UTC"
	Payload    json.RawMessage // opaque per-schedule data, not interpreted here

	// loc is the *time.Location loaded once from Timezone. We keep it as a
	// pointer because time.Time methods expect *Location and we want to avoid
	// re-resolving the IANA name on every NextRun call.
	loc *time.Location

	// Parsed field bitmaps. After Parse returns, these five slices are the
	// only things ShouldRun and NextRun consult.
	minute fieldSet // [0..59]
	hour   fieldSet // [0..23]
	dom    fieldSet // [1..31]
	month  fieldSet // [1..12]
	dow    fieldSet // [0..7], with 7 folded onto 0 by parseField
}

// Parse turns a 5-field cron expression and an IANA timezone name into a
// ready-to-evaluate CronSchedule. The expression must have exactly five
// whitespace-separated fields; we deliberately do NOT support the "@daily"
// shorthand or the six-field "with seconds" variant — both are out of scope
// for this teaching chapter, which mirrors the upstream
// scheduling-and-automation.md L84-L94 grammar.
//
// Timezone is resolved through time.LoadLocation. On most systems the IANA
// tzdata is bundled into the Go binary via the `time/tzdata` import on the
// host OS; if a name like "Asia/Shanghai" comes back missing, the caller is
// either on a stripped-down platform or has misspelled the zone. We surface
// that error rather than silently defaulting to UTC.
//
// Storage rule (from L96-L100 of the guide): the Expression is interpreted in
// the schedule's local timezone; matching is done by converting any incoming
// UTC time.Time into that local zone first.
func Parse(expr, tz string) (*CronSchedule, error) {
	fields := strings.Fields(expr)
	if len(fields) != 5 {
		return nil, fmt.Errorf("cron: expected 5 fields, got %d in %q", len(fields), expr)
	}
	loc, err := time.LoadLocation(tz)
	if err != nil {
		return nil, fmt.Errorf("cron: load timezone %q: %w", tz, err)
	}

	minute, err := parseField(fields[0], kindMinute)
	if err != nil {
		return nil, err
	}
	hour, err := parseField(fields[1], kindHour)
	if err != nil {
		return nil, err
	}
	dom, err := parseField(fields[2], kindDom)
	if err != nil {
		return nil, err
	}
	month, err := parseField(fields[3], kindMonth)
	if err != nil {
		return nil, err
	}
	dow, err := parseField(fields[4], kindDow)
	if err != nil {
		return nil, err
	}

	return &CronSchedule{
		Expression: expr,
		Timezone:   tz,
		loc:        loc,
		minute:     minute,
		hour:       hour,
		dom:        dom,
		month:      month,
		dow:        dow,
	}, nil
}

// matches reports whether the given local-time instant satisfies all five
// fields. Caller passes a time.Time ALREADY in the schedule's location; we
// pull the components straight off without re-converting. The split between
// "convert to local" (done in ShouldRun/NextRun) and "test the components"
// (done here) keeps the timezone arithmetic in one place.
//
// Day-of-month and day-of-week semantics: in classic Unix cron, when BOTH
// fields are restricted (not `*`), the predicate is OR — the job fires if
// EITHER matches. When one is `*` and the other restricted, the restricted
// one wins. We follow that convention here because the guide's example
// expressions all assume it, and breaking from it would be a surprise.
func (c *CronSchedule) matches(local time.Time) bool {
	if !c.minute[local.Minute()] {
		return false
	}
	if !c.hour[local.Hour()] {
		return false
	}
	if !c.month[int(local.Month())] {
		return false
	}

	// Classic dom-vs-dow OR/AND rule. time.Weekday(): Sunday=0, Saturday=6,
	// which already lines up with the cron field's 0-indexed Sunday. The
	// parseField fold has already removed any index-7 entries.
	dom := local.Day()
	dow := int(local.Weekday())
	domIsAll := isFullField(c.dom, kindDom)
	dowIsAll := isFullField(c.dow, kindDow)
	switch {
	case domIsAll && dowIsAll:
		return true
	case domIsAll:
		return c.dow[dow]
	case dowIsAll:
		return c.dom[dom]
	default:
		// Both restricted — OR semantics.
		return c.dom[dom] || c.dow[dow]
	}
}

// isFullField reports whether `fs` matches every legal value for the given
// kind. We use this to detect "*" semantically (rather than parsing the raw
// expression a second time) so an expression like "0-23" on the hour field
// is correctly treated as equivalent to "*" for the dom/dow OR-rule above.
func isFullField(fs fieldSet, kind fieldKind) bool {
	// For day-of-week with allowDow we ignore the (folded-away) index 7.
	upper := kind.max
	if kind.allowDow {
		upper = 6
	}
	for i := kind.min; i <= upper; i++ {
		if !fs[i] {
			return false
		}
	}
	return true
}

// ShouldRun reports whether `now` is exactly on a firing boundary for this
// schedule. The granularity is one minute: ShouldRun is true iff the
// truncate-to-minute of `now` (in the schedule's local zone) matches all
// five fields.
//
// Callers driving the scheduler from a real clock typically tick once per
// minute and pass `time.Now().UTC()`. The truncate step makes the boundary
// check robust to sub-second jitter: 14:30:00.123 and 14:30:59.987 both
// answer true if `30 14 * * *` fires at 14:30.
func (c *CronSchedule) ShouldRun(now time.Time) bool {
	local := now.In(c.loc).Truncate(time.Minute)
	return c.matches(local)
}

// NextRun computes the next time strictly after `now` at which this schedule
// fires. The algorithm is the "increment-and-check" brute force:
//
//  1. Convert now to the schedule's local zone and truncate to minute.
//  2. Add one minute (we want STRICTLY after; the current minute, if it
//     happens to match, is not the next run).
//  3. Walk forward one minute at a time, checking each candidate against the
//     five fieldSets. Return the first match (back in UTC for callers).
//
// Brute force is fine here: we cap the search at four years to bound the
// worst-case (Feb 29 schedules) and most real schedules match in fewer than
// 60 * 24 * 31 candidates ≈ 45k iterations. The fancy "compute next minute
// by skipping over non-matching fields" optimization saves cycles but adds
// 100+ lines of off-by-one risk for a chapter that's about teaching the
// algorithm, not shipping a high-frequency scheduler.
func (c *CronSchedule) NextRun(now time.Time) (time.Time, error) {
	local := now.In(c.loc).Truncate(time.Minute).Add(time.Minute)
	deadline := local.AddDate(4, 0, 0)
	for t := local; t.Before(deadline); t = t.Add(time.Minute) {
		if c.matches(t) {
			return t.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("cron: no match in 4-year horizon for %q in %s", c.Expression, c.Timezone)
}
