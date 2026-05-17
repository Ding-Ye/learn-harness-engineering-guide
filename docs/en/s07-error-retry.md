# s07 — Error Classification & Retry

> Network blips, rate limits, malformed model output — all of them are "errors", but the right response differs. This chapter ports `guide/error-handling.md` L9-L122 to ~400 lines of Go: a classifier with precedence rules, exponential backoff with jitter, and a Sleeper seam so tests never wait on the wall clock.

## Problem

After s01-s06 the harness has a loop, a provider, a registry, an assembler, memory, and guardrails. What it does *not* have is a recovery strategy. The s02 `Provider.Chat` will eventually return:

- `i/o timeout` from an Anthropic API call that took 35s
- `HTTP 429 rate limit exceeded` from too many requests this minute
- `out of memory` from a runaway tool
- `unknown tool 'read_fyle'` from a typo'd hallucination
- `file does not exist` from a Tool.Run with the wrong path

s02 surfaces every one of these as a flat `error`. The loop's only option is to crash. Retrying the OOM is suicide; retrying the file-not-found is a waste; *not* retrying the 429 means a single transient blip kills a 4-hour task.

What we need is the property `guide/error-handling.md` L9-L18 describes:

> Not all errors are equal. The recovery strategy depends on the error class.

A classifier that puts errors into recovery buckets, and a retry layer that only retries the bucket that benefits from retrying.

## Solution

The upstream guide prescribes four buckets:

| Class | Examples | Recovery |
|-------|----------|----------|
| **Transient** | timeout, 429, 503, connection reset | Retry with backoff |
| **Permanent** | file not found, permission denied | Surface to caller |
| **ModelError** | unknown tool, invalid JSON, schema validation | Re-prompt (out of scope here, just classified) |
| **Resource** | OOM, disk full, token limit, context exceeded | Checkpoint and escalate |

The Go API is two operations:

```go
class := Classify(err)  // ErrorClass enum
err = RetryWithBackoff(ctx, cfg, sleeper, func() error { return providerChat(...) })
```

`Classify` is a precedence-ordered match: strongest typed signals first (`net.Error.Timeout()`, `errors.Is(err, fs.ErrNotExist)`), then substring matches against three signal lists. `RetryWithBackoff` runs `fn` up to `MaxAttempts` times, sleeps `BaseDelay * 2^attempt` between attempts (capped at `MaxDelay`, plus optional jitter), and refuses to retry anything that does not classify as `Transient`.

## How It Works

The classifier's precedence matters. Consider the error message `"token limit exceeded due to rate limit on /v1/messages"`. A naive substring matcher that checks transient signals first would call this `Transient` (the string contains "rate limit"), retry it, and OOM the agent on the next attempt. We instead check resource signals first:

```
1. Classify(nil) → Unknown
2. net.Error with Timeout()==true → Transient   (typed signal beats string match)
3. errors.Is(err, fs.ErrNotExist)    → Permanent
4. resourceSignals match              → Resource
5. modelSignals match                 → ModelError
6. transientSignals match             → Transient
7. default                            → Unknown  (treated as Permanent for retry)
```

`Unknown` is treated as "do not retry" at the retry layer. The thinking: if we cannot recognise an error, we should not hammer the upstream service hoping it goes away. Better to surface an opaque error than to start a thundering herd in the dark.

The retry layer is a for-loop with three exits:

```go
for attempt := 0; attempt < cfg.MaxAttempts; attempt++ {
    if err := ctx.Err(); err != nil { return err }   // honor cancellation
    err := fn()
    if err == nil { return nil }                     // success
    if Classify(err) != Transient { return err }     // non-transient → surface
    if attempt == cfg.MaxAttempts-1 { break }        // no point sleeping after last try
    sleepWithContext(ctx, sleeper, backoffDelay(cfg, attempt))
}
return &RetryExhaustedError{LastError: err, Attempts: cfg.MaxAttempts}
```

Three things in this snippet earn their lines:

1. **The `if attempt == cfg.MaxAttempts-1 { break }` line.** Without it, the loop would sleep one final time before returning `RetryExhaustedError`. With `MaxAttempts=3, MaxDelay=60s`, that is a wasted 60s of latency for *every* failed call. The right schedule for `MaxAttempts=N` is `N-1` sleeps, not `N`.
2. **`sleepWithContext` races the sleep against `ctx.Done()`.** A naive `time.Sleep(delay)` cannot be cancelled — if the caller times out mid-retry, the retry layer would still hold the goroutine for up to `MaxDelay`. The `select` pattern is mandatory for any cancellable retry.
3. **`Sleeper` is the FakeSleeper seam.** A `RealSleeper` calls `time.Sleep`; a `FakeSleeper` appends the duration to a slice. Tests assert on the slice; CI never waits.

The backoff math (`backoffDelay`) is `BaseDelay << attempt`, capped at `MaxDelay`, plus optional `rand.Int63n(BaseDelay)` jitter. The cap matters: without it, attempt 30 tries to sleep for years. The jitter matters too: without it, ten agents that all rate-limited at t=0 all retry at t=2s, t=4s, t=8s in lockstep — a thundering herd that prevents the service from recovering.

## What Changed

| | s06 (guardrails) | s07 (error-retry) |
|---|---|---|
| Wraps | tool dispatch | `Provider.Chat` (and any retryable call) |
| Decision input | tool name + args | error class |
| Outputs | allow / deny / approval | retry / surface / exhausted |
| Seam | policy file | `Sleeper` interface |

s06 sits *before* tool execution (was this call allowed?). s07 sits *after* a call has run and failed (is this error worth retrying?). They are independent layers; in `s_full` the dispatch path is `guardrail → registry → tool`, and any LLM call inside the loop is wrapped in `RetryWithBackoff`. No s06 code is imported here — chapters are isolated per the curriculum's pedagogy rule.

## Try It

```bash
cd agents/s07-error-retry
go test -count=1 ./...
# PASS — 8 tests including TestRetry_HonorsContextCancellation
# (no wall-clock sleeps anywhere)

go run .
# === demo: transient errors retried with backoff ===
# call succeeded after 3 attempts
#   backoff #1: slept 2s
#   backoff #2: slept 4s
#
# === demo: permanent error returned immediately ===
# call attempted 1 time(s); classified as Permanent, returned without retry
# sleeps recorded: 0 (expected 0)
```

Two demos run back to back: the transient case shows the exponential backoff schedule (2s, 4s) for a function that fails twice then succeeds; the permanent case shows `os.ErrNotExist` returning immediately with zero sleeps recorded. The whole demo finishes in under a millisecond because we wired `FakeSleeper` instead of `RealSleeper`.

## Upstream Source Reading

Source: `guide/error-handling.md` L9-L122. Permalink: <https://github.com/nexu-io/harness-engineering-guide/blob/86fec9bea430cecb29ff10afaae36b96496a8f8e/guide/error-handling.md#L9-L122>

```python
# guide/error-handling.md L29-L60 — the classifier
def classify_error(error: Exception, context: dict | None = None) -> ErrorClass:
    error_type = type(error).__name__
    message = str(error).lower()

    transient_signals = ["timeout", "connection", "rate limit", "429", "503",
                         "502", "504", "temporary", "retry"]
    if any(s in message for s in transient_signals):
        return ErrorClass.TRANSIENT

    model_signals = ["unknown tool", "invalid json", "missing required",
                     "unexpected argument", "malformed"]
    if any(s in message for s in model_signals):
        return ErrorClass.MODEL

    resource_signals = ["out of memory", "disk full", "no space left",
                        "token limit", "context length exceeded"]
    if any(s in message for s in resource_signals):
        return ErrorClass.RESOURCE

    return ErrorClass.PERMANENT
```

Reading notes:

- **The upstream order is buggy.** Python checks Transient → Model → Resource → Permanent. "token limit exceeded due to rate limit" hits "rate limit" first and is misclassified Transient. The Go port reorders to Resource → Model → Transient and adds a `Unknown` zero value so the default is "do not retry", not "retry everything".
- **Typed signals beat string matches.** `net.Error.Timeout()` is a real method on real Go errors. `errors.Is(err, fs.ErrNotExist)` walks the `%w` chain. Python's `type(error).__name__` shows up in the upstream sketch but is never used. We promote both typed checks to step 1-2 of the classifier.
- **The `[0, 1, ...max-1]` retry math is a counted loop, not a `while True`.** Upstream's `for attempt in range(max_attempts)` is correct; we mirror it but add the `attempt == max-1 → break` early exit so a failure on the last attempt does not waste one more `MaxDelay` sleep before reporting.
- **Jitter is `random.uniform(0, 1)` in the Python sketch — we use `rand.Int63n(BaseDelay)`.** Same intent: a sub-`BaseDelay` random nudge breaks lockstep retries. The exact distribution does not matter; the *presence* of jitter does.
- **What we add beyond upstream.** `Sleeper` interface, `FakeSleeper`, `ctx.Done()`-aware backoff, `Unknown` zero value, and the precedence reorder. These are the Go shape required to make the code testable in CI without ANTHROPIC_API_KEY and without 60-second wall-clock waits.

Reading map:

| Topic | Upstream file | Lines | Mapped chapter |
|-------|---------------|-------|----------------|
| Error classification | `guide/error-handling.md` | L9-L60 | s07 (this) |
| Retry + exponential backoff | `guide/error-handling.md` | L62-L122 | s07 |
| Graceful degradation | `guide/error-handling.md` | L126-L228 | (not chapter-ized) |
| Checkpoint and resume | `guide/error-handling.md` | L231-L322 | s11 |
