# s07 upstream excerpt: error-handling.md L9-L122 (classify + retry)

Source: `guide/error-handling.md` L9-L122 in `nexu-io/harness-engineering-guide`
Permalink: <https://github.com/nexu-io/harness-engineering-guide/blob/86fec9bea430cecb29ff10afaae36b96496a8f8e/guide/error-handling.md#L9-L122>
License: MIT (© 2026 Nexu)

```markdown
## Error Classification

Not all errors are equal. The recovery strategy depends on the error class:

| Class      | Description                                                | Recovery                    |
|------------|------------------------------------------------------------|-----------------------------|
| Transient  | Network timeout, rate limit, temporary outage              | Retry with backoff          |
| Permanent  | File not found, permission denied, invalid input           | Report to model, try again  |
| Model      | Malformed tool call, hallucinated function, invalid JSON   | Re-prompt with correction   |
| Resource   | Out of memory, disk full, token budget exceeded            | Checkpoint and escalate     |

```python
from enum import Enum

class ErrorClass(Enum):
    TRANSIENT = "transient"
    PERMANENT = "permanent"
    MODEL = "model"
    RESOURCE = "resource"

def classify_error(error: Exception, context: dict | None = None) -> ErrorClass:
    error_type = type(error).__name__
    message = str(error).lower()

    transient_signals = ["timeout", "connection", "rate limit", "429", "503",
                         "502", "504", "temporary", "retry"]
    if any(signal in message for signal in transient_signals):
        return ErrorClass.TRANSIENT

    model_signals = ["unknown tool", "invalid json", "missing required",
                     "unexpected argument", "malformed"]
    if any(signal in message for signal in model_signals):
        return ErrorClass.MODEL

    resource_signals = ["out of memory", "disk full", "no space left",
                        "token limit", "context length exceeded"]
    if any(signal in message for signal in resource_signals):
        return ErrorClass.RESOURCE

    return ErrorClass.PERMANENT
```

## Retry with Exponential Backoff

Transient errors should be retried automatically. The key is exponential backoff
with jitter — without it, multiple retries can thundering-herd a recovering service:

```python
class RetryExhausted(Exception):
    def __init__(self, last_error, attempts):
        self.last_error = last_error; self.attempts = attempts
        super().__init__(f"Failed after {attempts} attempts: {last_error}")

def retry(max_attempts=3, base_delay=1.0, max_delay=60.0, retryable=(Exception,)):
    def decorator(func):
        @functools.wraps(func)
        def wrapper(*args, **kwargs):
            last_error = None
            for attempt in range(max_attempts):
                try:
                    return func(*args, **kwargs)
                except retryable as e:
                    last_error = e
                    if classify_error(e) != ErrorClass.TRANSIENT:
                        raise
                    if attempt < max_attempts - 1:
                        delay = min(base_delay * (2 ** attempt) + random.uniform(0, 1),
                                    max_delay)
                        time.sleep(delay)
            raise RetryExhausted(last_error, max_attempts)
        return wrapper
    return decorator
```

The math: with `base_delay=2.0`, retries happen at ~2s, ~5s, ~9s. The jitter
prevents synchronized retries across multiple agents hitting the same API.
```

## Reading notes

1. **The Python classifier is buggy in subtle ways.** The order is `Transient → Model → Resource → Permanent`, so a `"token limit exceeded due to rate limit"` error from a misbehaving upstream gets misclassified as Transient and retried — which OOMs the agent again. The Go port reverses to `Resource → Model → Transient`. We also introduce an explicit `Unknown=0` zero value so a freshly-zeroed `ErrorClass` does not silently mean Transient; in the upstream Python a string enum naturally avoids this, but Go's `iota` makes it the obvious trap.

2. **`type(error).__name__` is dead code in the sketch.** Upstream grabs the exception class name but never branches on it. Real-world Go errors carry richer typed signals — `net.Error.Timeout()`, `errors.Is(err, fs.ErrNotExist)`, custom `*RetryExhaustedError` — and the Go port promotes typed checks to step 1-2 of the classifier. The string match is the fallback, not the primary signal.

3. **The retry loop sleeps after the last failure.** Look closely at `if attempt < max_attempts - 1: time.sleep(delay)`. Upstream has this guard, which is right. But many real-world copies of this pattern drop it and end up sleeping a final `MaxDelay` before reporting `RetryExhausted`. With `MaxAttempts=3, MaxDelay=60s`, that is a wasted minute of latency per failed call. The Go port keeps the guard via an explicit `if attempt == cfg.MaxAttempts-1 { break }`.

4. **`time.sleep` is uncancellable.** A Python `time.sleep(60)` cannot be interrupted by the caller. A Go `time.Sleep(60*time.Second)` is the same — you must use `select { case <-ctx.Done(): case <-time.After(d): }` if you want cancellation. The Go retry layer does this via `sleepWithContext` so a `context.Cancel()` in the parent goroutine takes effect immediately, not at the next retry boundary.

5. **The `Sleeper` interface is mandatory for tests.** Without it, `TestRetry_RaisesAfterMaxAttempts` with `MaxAttempts=3, BaseDelay=1s, MaxDelay=60s` takes 1+2+4 = 7 seconds of wall-clock per test. With it, every test runs in microseconds and asserts on the *recorded* schedule rather than the wall-clock outcome. The same seam appears in s05 (`Clock`) — the pattern is "anything that touches `time.Now` or `time.Sleep` is an interface so tests can drive it deterministically".

## Reading map

| Topic | Upstream file | Lines | Mapped chapter |
|-------|---------------|-------|----------------|
| Error classification | `guide/error-handling.md` | L9-L60 | s07 (this) |
| Retry with backoff | `guide/error-handling.md` | L62-L122 | s07 |
| Graceful degradation | `guide/error-handling.md` | L126-L228 | (not chapter-ized; see s_full integration) |
| Checkpoint and resume | `guide/error-handling.md` | L231-L322 | s11 |
| Context-engineering: failure cascades | `guide/long-running-harness.md` | L19-L46 | Appendix A |
