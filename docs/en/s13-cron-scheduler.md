# s13 — Cron Scheduler

> A self-contained 5-field cron parser plus a tiny scheduler. Parse an expression and a timezone; ask `ShouldRun(now)` whether `now` is exactly on a firing boundary; ask `NextRun(now)` for the next firing instant in UTC. **The caller owns the clock** — no goroutines, no `time.Sleep`, no flake.

## Problem

By s12 the harness can spawn isolated workers, log every event durably (s10), checkpoint state across crashes (s11), compress its context window (s09), and gate every tool call through a guardrail (s06). It can do anything *a user asks it to do*. But the upstream guide spends a whole chapter on a complementary kind of value: the agent that acts **without being asked**.

From `guide/scheduling-and-automation.md` L11-L18:

> Most people encounter Agents as conversational interfaces — you type, it replies. That mental model caps the value at "faster search engine." The real unlock happens when Agents execute work *without being asked*.

To make that real you need a clock primitive — something that knows "every weekday at 8 AM Asia/Shanghai, run this job." Conceptually simple; legendary for hiding nasty bugs around timezones, daylight saving, and "did the minute boundary really fire just once?" The upstream chapter calls out exactly the failure modes that bite people in production (L96-L108 on timezones, L300-L319 on missing timeouts and double-delivery). We re-build the smallest possible primitive that gets *all of those* right.

We deliberately **do not** build: a `time.Ticker` goroutine, distributed locking, persistence, retry, jitter, `@daily`/`@hourly` shorthand, six-field "with seconds" cron, or `MON`/`TUE` aliases. Those are layered on around the primitive; the chapter is about getting the primitive right.

## Solution

`CronSchedule` is the parsed form of a 5-field expression plus an IANA timezone:

```go
sch, err := Parse("0 8 * * *", "Asia/Shanghai")
// sch.Expression = "0 8 * * *"
// sch.Timezone   = "Asia/Shanghai"
// sch.loc        = *time.Location for Asia/Shanghai
// sch.minute/hour/dom/month/dow = parsed fieldSets

sch.ShouldRun(now)            // is `now` exactly on a fire-boundary minute?
nextUTC, _ := sch.NextRun(now) // when does it next fire, in UTC?
```

The grammar each field accepts is the upstream's L84-L94 minimum: `*`, `*/N`, `A-B`, `A,B,C`, absolute integers, and the day-of-week quirk where both `0` and `7` mean Sunday. Anything else is a parse error.

`Scheduler` is a thin wrapper over `map[string]*CronSchedule` with a single `Tick(now time.Time) []string` method that returns the names of every schedule whose `ShouldRun(now)` is true. The caller drives the clock. There is no goroutine; there is no `time.Sleep`.

| Discipline | Why |
|---|---|
| Store UTC. Display local. Match in local. | `time.Location` is the source of truth; `now.In(loc)` is the first thing every match does. |
| Truncate-to-minute in `ShouldRun`. | Real clocks have jitter; a one-minute granularity that's robust to sub-second noise is the contract. |
| Dom-vs-dow is OR when both are restricted. | Classic Vixie semantics; the guide's examples assume it. |
| Day-of-week 7 folds onto 0 at parse time. | One place to handle it; the matcher only ever indexes 0..6. |
| Caller drives the clock. | Frozen-time tests, no goroutines, no flakiness. |

## How It Works

**Parse** is a strict five-field check followed by five independent field parsers (`parseField`). Each field parser handles `,`-separated terms; each term may have a `/N` step and a base that is `*`, `A`, or `A-B`. The result is a `fieldSet = []bool` sized exactly to the field's legal range — a value outside the range is a slice-bounds error, so the parser is the only place range-checks live.

```go
// One term, all the cases collapsed into one stride loop:
for v := lo; v <= hi; v += step {
    out[v] = true
}
```

Where `(lo, hi, step)` comes from:

| Term | lo | hi | step |
|---|---|---|---|
| `*` | min | max | 1 |
| `A` | A | A | 1 |
| `A-B` | A | B | 1 |
| `*/N` | min | max | N |
| `A/N` | A | max | N |
| `A-B/N` | A | B | N |

The `A/N` Vixie-extension is the one that's easy to miss: it means "from A through max, every N", not "just A". `parseField` patches `hi = kind.max` when it sees a bare-integer base with a step. The mistake to avoid is hard-coding it into `resolveBase`, which would then double-widen for `A-B/N` — keeping the widen in the caller localizes the special case.

**Match** is a five-field bitmap probe against `now.In(loc).Truncate(time.Minute)`:

```go
if !c.minute[local.Minute()] { return false }
if !c.hour[local.Hour()]     { return false }
if !c.month[int(local.Month())] { return false }
// dom-vs-dow goes OR when both restricted, AND with * otherwise:
domIsAll := isFullField(c.dom, kindDom)
dowIsAll := isFullField(c.dow, kindDow)
switch {
case domIsAll && dowIsAll: return true
case domIsAll:             return c.dow[dow]
case dowIsAll:             return c.dom[dom]
default:                   return c.dom[dom] || c.dow[dow]
}
```

The `isFullField` check looks at the parsed bitmap rather than re-parsing the raw expression. That means `0-23` on the hour field correctly behaves as "any hour" for the dom-vs-dow OR rule — and so does `*`. There is one shape of expression for "match everything" in this code, regardless of how the user wrote it.

**NextRun** is the brute force: starting at `now.In(loc).Truncate(Minute).Add(Minute)` (strictly after now), walk forward one minute at a time, checking each candidate against `matches()`, until you hit a match or four years pass. Four years is the smallest horizon that covers a Feb-29-only schedule; the brute-force loop tops out around 2.1M iterations in the worst case and finishes in microseconds for everything in the test suite. The clever "skip non-matching fields" optimization saves cycles but adds 100+ lines of off-by-one risk — wrong trade for a teaching chapter.

**Scheduler.Tick** does what its name says: it asks each registered schedule whether `ShouldRun(now)` and returns the names of the ones that say yes, sorted alphabetically. The sort matters because a real harness pipes the output into an event log (s10) or a worker pool (s12), and stable ordering makes those downstream artifacts diff-reviewable.

The most-likely bug shape this design forecloses: the `Scheduler` does not own a goroutine, a `time.Ticker`, or a `time.Sleep`. A real harness will want all of those — but layered *outside* this primitive. Tests pass `time.Date(...)` directly; production passes `time.Now().UTC()`; nothing in the type changes between the two.

## What Changed

| | s12 (sub-agent) | s13 (cron scheduler) |
|---|---|---|
| Concern | spawning isolated workers | knowing *when* to do something |
| Concurrency | yes — process pool, timeouts | none — pure functions of `(now, schedules)` |
| Time | doesn't care | the whole subject |
| LLM | child runs a mini-loop | no LLM involvement at all |
| External state | files in a work dir | none |

s13 is the first chapter since s04 with no LLM call in its happy path. That's by design: the cron primitive is pure time arithmetic. In `s_full` the wiring is: `Scheduler.Tick(time.Now().UTC())` runs every minute → returns due schedule names → caller dispatches each name via s12's sub-agent spawner → each child runs a fresh agentic loop through s06 + s07 + s14. The cron stays out of the agent's way; the agent stays out of the cron's way.

## Try It

```bash
cd agents/s13-cron-scheduler
go vet ./... && go build ./... && go test -count=1 ./...
# PASS — 7 tests

go run .
# registered schedule "0 8 * * *" in Asia/Shanghai (next run after now in UTC = 2026-...)
#
# === ticking from 2026-05-17T22:00:00Z for 25 hours ===
# [hour  0 UTC=22:00 UTC] (nothing due)
# [hour  1 UTC=23:00 UTC] (nothing due)
# [hour  2 UTC=00:00 UTC local=2026-05-18 08:00 CST] FIRES: [daily-digest]
# [hour  3 UTC=01:00 UTC] (nothing due)
# ...
```

The one tick that fires is at UTC `00:00`, which corresponds to Shanghai `08:00`. That's "Store UTC, match in local" working as advertised: the expression is `0 8 * * *` in Asia/Shanghai, but the matcher answers `true` on a UTC moment because `now.In(loc)` rolls it into local time first.

## Upstream Source Reading

Source: `guide/scheduling-and-automation.md` L78-L160. Permalink: <https://github.com/nexu-io/harness-engineering-guide/blob/86fec9bea430cecb29ff10afaae36b96496a8f8e/guide/scheduling-and-automation.md#L78-L160>

```text
Cron Implementation Patterns (excerpt, L78-L160)

Schedule Definition

Cron expressions use the standard five-field format:

    ┌───────── minute (0–59)
    │ ┌─────── hour (0–23)
    │ │ ┌───── day of month (1–31)
    │ │ │ ┌─── month (1–12)
    │ │ │ │ ┌─ day of week (0–7, 0 and 7 = Sunday)
    │ │ │ │ │
    * * * * *

Timezone handling is the single most common source of Cron bugs. The rule:

    > Store UTC. Display local.

Internally, the Harness evaluates all Cron expressions against UTC. When
showing the user their schedule, convert to their local timezone.

    User: "Run my digest every morning at 8am"
    Agent: "Got it — daily digest at 8:00 AM Asia/Shanghai (0:00 UTC). ✓"

Session Targeting / Payload Types / Delivery

Isolated session vs main session injection; agentTurn vs systemEvent payloads;
announce / webhook / silent delivery. Delivery is pinned to the originating
session at creation time.
```

Reading notes:

- **"Store UTC. Display local." is a type-system claim disguised as advice.** The schedule's authoritative form is `(UTC time.Time, *time.Location)`; everything else is rendering. In our Go port, `CronSchedule.Timezone` is the rendering hint and `loc *time.Location` is the parsed authority. Skip the `now.In(loc)` conversion and `TestNextRun_TimezoneConversion` fails immediately.
- **The grammar at L84-L94 is intentionally minimal — no `@daily`, no seconds, no `L`/`#`, no `MON`/`TUE`.** Implementing only `*`, `*/N`, `A-B`, `A,B,C`, and absolute integers gets 90% of real coverage for ~200 LOC. Each extension would add ~50 lines plus edge-case tests; out of scope for a teaching chapter.
- **L91's "0 and 7 = Sunday" is the field's single design-by-committee scar.** Old `cron` accepted 1-7 with 1=Sunday; BSD `cron` flipped to 0-6 with 0=Sunday; Vixie unified them by accepting *both* 0 and 7 for Sunday. We fold index 7 onto 0 at the end of `parseField` so the matcher only ever indexes 0..6.
- **Session targeting / payload / delivery (L111-L153) are *not* a cron concern at our layer.** They are downstream consumer concerns. The primitive holds `Expression`, `Timezone`, and an opaque `json.RawMessage` Payload; *what to do with the payload when the schedule fires* is the caller's problem. Same separation as s10's event log.
- **The "delivery pinned to originating session" invariant (L152-L153) is structural.** Our `Payload` is `json.RawMessage`, set at construction time, with no setter — if a user wants different delivery they create a new schedule. Forecloses the "oh, redirect this to #ops" bug shape.

Reading map:

| Topic | Upstream file | Lines | Mapped chapter |
|-------|---------------|-------|----------------|
| Why scheduling matters | `guide/scheduling-and-automation.md` | L9-L75 | s13 mental model |
| Five-field grammar | `guide/scheduling-and-automation.md` | L84-L94 | s13 (this) |
| Store UTC, display local | `guide/scheduling-and-automation.md` | L96-L107 | s13 (this) |
| Session targeting / payload / delivery | `guide/scheduling-and-automation.md` | L111-L153 | s13 consumer (out of scope here) |
| Heartbeat vs Cron | `guide/scheduling-and-automation.md` | L250-L272 | s13 cross-ref |
| Anti-patterns | `guide/scheduling-and-automation.md` | L278-L319 | s13 + s12 cross-ref |
