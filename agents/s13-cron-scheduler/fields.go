package main

import (
	"fmt"
	"strconv"
	"strings"
)

// fieldSet is a fixed-size bitmap of which integer values in a cron field's
// range are "selected". Index i is true iff the value i matches the field.
// We allocate the slice to the exact range size (60 for minute, 24 for hour,
// etc.) so a value outside the range is a slice-bounds panic, not a silent
// false. Parser code is responsible for rejecting out-of-range inputs BEFORE
// touching the slice — see parseField.
//
// Why a []bool instead of a uint64 bitmask? Two reasons. First, the
// day-of-month field's max is 31, the minute field's max is 60, so a single
// integer width doesn't fit all five fields uniformly. Second, the []bool form
// reads like the spec ("does this minute fire?") with zero bit-twiddling — a
// teaching repo's first job is to be easy to read.
type fieldSet []bool

// fieldKind labels which of the five cron positions a parser invocation is
// working on. The min/max are the per-field range from the standard 5-field
// cron grammar (see scheduling-and-automation.md L84-L94):
//
//	┌───────── minute (0–59)
//	│ ┌─────── hour (0–23)
//	│ │ ┌───── day of month (1–31)
//	│ │ │ ┌─── month (1–12)
//	│ │ │ │ ┌─ day of week (0–7, 0 and 7 = Sunday)
//
// Day-of-week is special: the spec maps both 0 and 7 to Sunday. We model that
// in the parser by accepting up to 7 and then collapsing index 7 onto index 0
// at the end of the field — see parseField below.
type fieldKind struct {
	name     string // human-readable label for error messages
	min, max int    // inclusive bounds for valid integer literals
	allowDow bool   // if true, treat 7 as 0 (Sunday)
}

var (
	kindMinute = fieldKind{name: "minute", min: 0, max: 59}
	kindHour   = fieldKind{name: "hour", min: 0, max: 23}
	kindDom    = fieldKind{name: "day-of-month", min: 1, max: 31}
	kindMonth  = fieldKind{name: "month", min: 1, max: 12}
	// Day-of-week accepts 0..7 with 7 collapsing to 0 (Sunday). This is the
	// classic Vixie-cron behavior and what scheduling-and-automation.md L91
	// documents.
	kindDow = fieldKind{name: "day-of-week", min: 0, max: 7, allowDow: true}
)

// parseField turns one cron field string (e.g. "*/15", "0", "1-5", "0,3,6")
// into a populated fieldSet. The grammar we accept is a comma-separated list
// of terms, where each term is one of:
//
//	*               -> every value in [min, max]
//	*/step          -> every step-th value starting at min
//	A               -> the single value A
//	A-B             -> every value in [A, B] (inclusive)
//	A-B/step        -> every step-th value from A through B
//	A/step          -> every step-th value from A through max (Vixie extension)
//
// Anything else is a parse error. Out-of-range integers are rejected here so
// the caller can safely index into the returned fieldSet by raw time-component
// without bounds checks downstream.
//
// On the day-of-week field, the value 7 is accepted as a legal literal and
// then aliased to 0 (Sunday) before returning — both `0` and `7` in the same
// field expression are silently de-duplicated by the set semantics.
func parseField(expr string, kind fieldKind) (fieldSet, error) {
	if expr == "" {
		return nil, fmt.Errorf("%s: empty field", kind.name)
	}

	// Size the bitmap to (max + 1) so index `max` is valid. For day-of-week
	// we briefly use index 7 during parsing, then fold it onto 0 at the end.
	size := kind.max + 1
	out := make(fieldSet, size)

	for _, term := range strings.Split(expr, ",") {
		if term == "" {
			return nil, fmt.Errorf("%s: empty term in %q", kind.name, expr)
		}
		if err := applyTerm(out, term, kind); err != nil {
			return nil, err
		}
	}

	// Day-of-week fold: collapse 7 onto 0. After this step, index 7 is never
	// inspected by the matcher; we keep size=8 only to avoid a bounds-check
	// branch during parsing.
	if kind.allowDow && len(out) == 8 && out[7] {
		out[0] = true
		out[7] = false
	}

	return out, nil
}

// applyTerm parses one comma-separated piece and ORs its matches into `out`.
// Splitting term-parsing from field-parsing keeps the comma loop above trivial
// and lets parseField surface a single error per malformed term.
//
// The Vixie-cron quirk worth knowing: "A/N" (a bare integer base with a step)
// means "every N starting at A through the field's max". So `2/15` minute is
// `{2, 17, 32, 47}`, not just `{2}`. We implement that by widening the upper
// bound to kind.max when we see a bare-integer base with a step.
func applyTerm(out fieldSet, term string, kind fieldKind) error {
	// Detect a step suffix "/N" first. Everything before the slash is the
	// "base" range; the slash-N is the stride.
	step := 1
	base := term
	hasStep := false
	if slash := strings.Index(term, "/"); slash >= 0 {
		baseStr := term[:slash]
		stepStr := term[slash+1:]
		if baseStr == "" {
			return fmt.Errorf("%s: empty base before '/' in %q", kind.name, term)
		}
		if stepStr == "" {
			return fmt.Errorf("%s: empty step after '/' in %q", kind.name, term)
		}
		n, err := strconv.Atoi(stepStr)
		if err != nil {
			return fmt.Errorf("%s: bad step %q in %q", kind.name, stepStr, term)
		}
		if n < 1 {
			return fmt.Errorf("%s: step must be >= 1, got %d in %q", kind.name, n, term)
		}
		step = n
		base = baseStr
		hasStep = true
	}

	// Resolve the base into a [lo, hi] interval.
	lo, hi, err := resolveBase(base, kind)
	if err != nil {
		return err
	}

	// Vixie extension: a bare integer base WITH a step means "from A through
	// the field max", not "just A". resolveBase returns [A, A] in that case;
	// we patch hi up to kind.max here. We detect the bare-integer case by
	// `lo == hi` — and we only apply the widen when the user wrote a step,
	// because `8 * * * *` (no step) should fire only at minute 8.
	if hasStep && lo == hi {
		hi = kind.max
	}

	// Stride through [lo, hi], setting each landed value. This is the same
	// loop for `*`, `A`, `A-B`, `A/N`, `*/N`, and `A-B/N` — the difference is
	// entirely in how (lo, hi, step) get computed.
	for v := lo; v <= hi; v += step {
		out[v] = true
	}
	return nil
}

// resolveBase turns the pre-slash part of a term into a [lo, hi] integer
// interval. Cases:
//
//	"*"   -> [min, max]
//	"A"   -> [A, A]
//	"A-B" -> [A, B], with A <= B and both in range
//
// The Vixie-style "A/N means [A, max]" widening is NOT done here; applyTerm
// patches `hi` to `kind.max` when it sees a bare-integer base with a step,
// so this function stays simple and step-agnostic.
func resolveBase(base string, kind fieldKind) (int, int, error) {
	if base == "*" {
		return kind.min, kind.max, nil
	}
	if dash := strings.Index(base, "-"); dash >= 0 {
		loStr := base[:dash]
		hiStr := base[dash+1:]
		if loStr == "" || hiStr == "" {
			return 0, 0, fmt.Errorf("%s: bad range %q", kind.name, base)
		}
		lo, err := parseInt(loStr, kind)
		if err != nil {
			return 0, 0, err
		}
		hi, err := parseInt(hiStr, kind)
		if err != nil {
			return 0, 0, err
		}
		if lo > hi {
			return 0, 0, fmt.Errorf("%s: range %d-%d has lo > hi", kind.name, lo, hi)
		}
		return lo, hi, nil
	}
	// A bare integer. If applyTerm sees this AND a step, applyTerm will widen
	// hi up to kind.max afterward.
	v, err := parseInt(base, kind)
	if err != nil {
		return 0, 0, err
	}
	return v, v, nil
}

// parseInt parses one integer literal and bounds-checks it against the field
// kind. Out-of-range values are the single most common typo (`60 * * * *`
// meaning "every 60 minutes" — actually invalid because minute max is 59),
// so the error message is explicit about the legal range.
func parseInt(s string, kind fieldKind) (int, error) {
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, fmt.Errorf("%s: not an integer: %q", kind.name, s)
	}
	if n < kind.min || n > kind.max {
		return 0, fmt.Errorf("%s: value %d out of range [%d, %d]", kind.name, n, kind.min, kind.max)
	}
	return n, nil
}
