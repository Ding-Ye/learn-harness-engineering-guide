# s07-error-retry

> Classify errors into recovery buckets, then retry only the ones that deserve it. Exponential backoff with jitter, mutex-free FakeSleeper for deterministic tests.
> 把错误归类到不同的恢复策略，然后只对值得重试的那些做重试。指数退避 + jitter，测试用 FakeSleeper 不睡墙钟。

## Scope / 范围

Implement the classifier + retry layer from `guide/error-handling.md` L9-L122 in ~400 lines of Go. Pure logic — no LLM, no network. The classifier is a substring matcher with a precedence rule (Resource > Model > Transient) plus typed checks for `net.Error.Timeout()` and `errors.Is(err, fs.ErrNotExist)`. The retry layer drives any `func() error` with exponential backoff, honors `ctx.Done()` mid-sleep, and refuses to retry anything that is not classified `Transient`.
用 ~400 行 Go 实现 `guide/error-handling.md` L9-L122 的分类 + 重试层。纯逻辑 —— 不调 LLM、不走网络。分类器是子串匹配 + 优先级（Resource > Model > Transient），并加上对 `net.Error.Timeout()` 和 `errors.Is(err, fs.ErrNotExist)` 的类型检查。重试层驱动任意 `func() error`，按指数退避重试，在 sleep 中也响应 `ctx.Done()`，且只重试 `Transient`。

## Files / 文件

```
classify.go     ErrorClass enum + Classify(err) function
retry.go        RetryConfig, RetryWithBackoff, RetryExhaustedError, backoff math
sleeper.go      Sleeper interface + RealSleeper + FakeSleeper
main.go         CLI demo: rate-limited fake call + permanent-error fast-fail
retry_test.go   8 tests covering classification, retry, exhaustion, cancellation
```

## Run / 运行

```bash
cd agents/s07-error-retry
go run .
# Prints the backoff schedule for a 3-attempt transient retry, then
# demonstrates that a Permanent error is returned without any sleep.
```

## Test / 测试

```bash
go test -count=1 ./...
# PASS — 8 tests, no wall-clock sleeps anywhere (FakeSleeper records,
# never blocks).
```

## Key teaching points / 教学要点

1. **Four error classes, four recovery strategies.** Transient (retry), Permanent (surface to caller), Model (re-prompt — out of scope for this chapter; we still classify), Resource (escalate, checkpoint). Upstream L13-L18.
   **四种错误类、四种恢复策略**。Transient（重试）、Permanent（直接上抛）、Model（重新 prompt —— 本章不实现，只分类）、Resource（升级、checkpoint）。见上游 L13-L18。
2. **Precedence beats simple substring match.** `Resource > Model > Transient`. Without it, "token limit exceeded due to rate limit" would be retried as a 429 — and OOM the agent again. The classifier checks the strongest bucket first.
   **优先级比简单子串匹配重要**。`Resource > Model > Transient`。否则 "token limit exceeded due to rate limit" 会当作 429 重试 —— 然后 agent 再 OOM 一次。先匹配最强的桶。
3. **`Unknown` is treated as Permanent.** A class we cannot recognise is not a class we should retry. Better to surface an opaque error than hammer a service we do not understand.
   **`Unknown` 当 Permanent 处理**。不认识的错就不该重试，宁可上抛一个不透明 error，也别把不了解的服务捶死。
4. **Jitter is on by default.** Without it, N agents that all rate-limited at t=0 all retry at t=2s, t=4s, t=8s in lockstep — a thundering herd that prevents the service from recovering. Upstream L124.
   **Jitter 默认开**。否则 N 个同时被限流的 agent 在 t=2s、t=4s、t=8s 同步重试 —— 把刚要恢复的服务再砸一次。见上游 L124。
5. **`Sleeper` is the only seam, mirroring s05's `Clock`.** Tests inject `FakeSleeper` which records every duration. Wall-clock sleeps in tests are slow, flaky, and stop being CI-friendly above ~100 of them — the seam is non-negotiable.
   **`Sleeper` 是唯一的 seam，和 s05 的 `Clock` 同构**。测试注入 `FakeSleeper`，记录每次的 duration。测试里睡墙钟既慢又不稳，超过 100 次就 CI 不友好 —— seam 必须有。
6. **`MaxAttempts=N` produces at most `N-1` sleeps.** After the last failure there is nothing to wait for. This is the difference between a working retry loop and one that always sleeps a final 60s before reporting failure.
   **`MaxAttempts=N` 最多产生 `N-1` 次 sleep**。最后一次失败之后没什么可等的了。这就是"能工作的重试循环"和"每次失败前再睡 60 秒"的区别。

## What the next chapter changes / 下一节的变化

s08 introduces the skill system — on-demand tool bundles that save context tokens. s07's retry layer is orthogonal: a skill that calls out to the LLM provider would naturally wrap `Provider.Chat` in `RetryWithBackoff`, but s08 itself does not depend on this chapter. Integration happens in `s_full`.
s08 引入 skill 系统 —— 按需加载的工具 bundle，节省 context token。s07 的重试层和它正交：skill 内部要调 LLM 时自然会用 `RetryWithBackoff` 包 `Provider.Chat`，但 s08 自身不依赖本章。集成放到 `s_full`。
