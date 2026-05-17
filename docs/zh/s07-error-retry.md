# s07 — 错误分类与重试

> 网络抖动、限流、模型胡说八道 —— 都是 "error"，但应对方式不同。本章把 `guide/error-handling.md` L9-L122 译成 ~400 行 Go：带优先级的分类器、带 jitter 的指数退避、`Sleeper` seam 让测试不睡墙钟。

## Problem

s01-s06 之后 harness 已经有循环、provider、registry、assembler、memory、guardrails。**唯独没有**恢复策略。s02 的 `Provider.Chat` 早晚会返回这些：

- `i/o timeout` —— 一次 35 秒的 Anthropic 调用
- `HTTP 429 rate limit exceeded` —— 这分钟请求太多
- `out of memory` —— 一个失控的工具
- `unknown tool 'read_fyle'` —— 模型把工具名拼错了
- `file does not exist` —— Tool.Run 拿到了错误路径

s02 把这些全部当作平铺的 `error` 抛出来。循环只能崩。重试 OOM 是自杀；重试 file-not-found 是浪费；**不**重试 429 意味着一次瞬时抖动就毁掉一个 4 小时的任务。

我们想要的是 `guide/error-handling.md` L9-L18 描述的属性：

> 错误不都一样。恢复策略取决于错误的类别。

一个把错误归类到恢复桶的 classifier，加上一个**只重试值得重试的桶**的重试层。

## Solution

上游 guide 规定了四个桶：

| 类 | 例子 | 恢复 |
|----|------|------|
| **Transient** | timeout、429、503、connection reset | 退避重试 |
| **Permanent** | file not found、permission denied | 上抛给调用方 |
| **ModelError** | unknown tool、invalid JSON、schema 校验 | 重新 prompt（本章只分类、不实现重 prompt） |
| **Resource** | OOM、磁盘满、token 限制、context 超限 | Checkpoint + 升级 |

Go API 是两个操作：

```go
class := Classify(err)  // ErrorClass enum
err = RetryWithBackoff(ctx, cfg, sleeper, func() error { return providerChat(...) })
```

`Classify` 是按优先级的匹配：最强的类型信号先 (`net.Error.Timeout()`、`errors.Is(err, fs.ErrNotExist)`)，再对三组 signal 列表做子串匹配。`RetryWithBackoff` 最多跑 `fn` `MaxAttempts` 次，每次失败之间睡 `BaseDelay * 2^attempt`（封顶 `MaxDelay`，加可选 jitter），且**只**对 `Transient` 重试。

## How It Works

分类器的优先级很重要。考虑这条 `"token limit exceeded due to rate limit on /v1/messages"`。先匹配 transient 信号的 naive 实现会把它当 `Transient`（字符串里有 "rate limit"），重试一次，然后下一次又 OOM。我们反过来先匹配 resource：

```
1. Classify(nil) → Unknown
2. net.Error 且 Timeout()==true → Transient   （类型信号 > 字符串匹配）
3. errors.Is(err, fs.ErrNotExist)    → Permanent
4. resourceSignals 命中              → Resource
5. modelSignals 命中                 → ModelError
6. transientSignals 命中             → Transient
7. 兜底                              → Unknown  （重试层按 Permanent 处理）
```

`Unknown` 在重试层被当作 "不重试"。理由：不认识的错就不该重试，宁可上抛一个不透明 error，也别在黑暗里捶服务。

重试层是带三个出口的 for 循环：

```go
for attempt := 0; attempt < cfg.MaxAttempts; attempt++ {
    if err := ctx.Err(); err != nil { return err }   // 响应 cancel
    err := fn()
    if err == nil { return nil }                     // 成功
    if Classify(err) != Transient { return err }     // 非 Transient → 上抛
    if attempt == cfg.MaxAttempts-1 { break }        // 最后一次失败就别再睡了
    sleepWithContext(ctx, sleeper, backoffDelay(cfg, attempt))
}
return &RetryExhaustedError{LastError: err, Attempts: cfg.MaxAttempts}
```

这段里三行每行都值得：

1. **`if attempt == cfg.MaxAttempts-1 { break }`**。没有这行，循环会在返回 `RetryExhaustedError` 前再睡一次。`MaxAttempts=3, MaxDelay=60s` 的时候，每次失败都白白多 60 秒延迟。正确的 `MaxAttempts=N` 调度是 `N-1` 次 sleep，不是 `N` 次。
2. **`sleepWithContext` 让 sleep 和 `ctx.Done()` 比赛**。直接 `time.Sleep(delay)` 不能取消 —— 调用方中途超时，重试层还会把 goroutine 拽住最多 `MaxDelay`。任何可取消的重试都必须用 `select` 模式。
3. **`Sleeper` 是 FakeSleeper seam**。`RealSleeper` 直接 `time.Sleep`；`FakeSleeper` 把 duration 追加到 slice 里。测试断言 slice，CI 不等待。

退避公式 (`backoffDelay`) 是 `BaseDelay << attempt`，封顶 `MaxDelay`，可选加 `rand.Int63n(BaseDelay)` 的 jitter。封顶很重要：不封的话，attempt 30 想要睡好几年。jitter 也重要：没有 jitter 时，10 个在 t=0 同时被限流的 agent 会在 t=2s、t=4s、t=8s 同步重试 —— 把刚要恢复的服务再砸一次。

## What Changed

| | s06（guardrails）| s07（error-retry） |
|---|---|---|
| 包装 | 工具分发 | `Provider.Chat`（以及任何可重试的调用） |
| 判定输入 | 工具名 + 参数 | error 类别 |
| 输出 | allow / deny / 审批 | retry / 上抛 / exhausted |
| Seam | 策略文件 | `Sleeper` 接口 |

s06 在工具执行**之前**（这次调用允许吗？）。s07 在调用**之后**已经失败（这个 error 值得重试吗？）。两者是独立的层；在 `s_full` 里分发路径是 `guardrail → registry → tool`，循环里的 LLM 调用都用 `RetryWithBackoff` 包起来。本章不 import s06 的代码 —— 课程的章节隔离规则。

## Try It

```bash
cd agents/s07-error-retry
go test -count=1 ./...
# PASS —— 8 个测试，含 TestRetry_HonorsContextCancellation
# （全程不睡墙钟）

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

两个 demo 连跑：transient 那个展示 2 次失败 + 1 次成功的退避节拍（2s, 4s）；permanent 那个展示 `os.ErrNotExist` 立刻返回，0 次 sleep。整个 demo 不到 1 毫秒就跑完 —— 因为我们注入的是 `FakeSleeper` 而不是 `RealSleeper`。

## Upstream Source Reading

来源：`guide/error-handling.md` L9-L122。永久链接：<https://github.com/nexu-io/harness-engineering-guide/blob/86fec9bea430cecb29ff10afaae36b96496a8f8e/guide/error-handling.md#L9-L122>

```python
# guide/error-handling.md L29-L60 —— classifier
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

阅读笔记：

- **上游的判断顺序是有 bug 的**。Python 是 Transient → Model → Resource → Permanent。"token limit exceeded due to rate limit" 命中 "rate limit"，被误判成 Transient。Go 版调成 Resource → Model → Transient，并加 `Unknown` 零值，让默认是"不重试"，而不是"全部重试"。
- **类型信号 > 字符串匹配**。`net.Error.Timeout()` 是真实 Go error 上的真实方法。`errors.Is(err, fs.ErrNotExist)` 会沿着 `%w` 链走。Python 那段 `type(error).__name__` 取了但没用 —— 我们把这两类型检查提升到 step 1-2。
- **`[0, 1, ...max-1]` 的重试数是有限循环，不是 `while True`**。上游 `for attempt in range(max_attempts)` 是对的；我们照搬，但加了 "最后一次失败直接 break" 的早退出 —— 不让最后一次失败白白浪费一个 `MaxDelay`。
- **Jitter 在 Python 里是 `random.uniform(0, 1)`，我们用 `rand.Int63n(BaseDelay)`**。意图一样：一个 sub-`BaseDelay` 的随机扰动打散同步重试。具体分布不重要，**有 jitter** 这件事才重要。
- **比上游多做的事**。`Sleeper` 接口、`FakeSleeper`、能响应 `ctx.Done()` 的退避、`Unknown` 零值、优先级重排。这些都是让代码在 CI 里**无需 ANTHROPIC_API_KEY 也无需 60 秒墙钟**就能测出来的必备 Go 形状。

阅读地图：

| 主题 | 上游文件 | 行号 | 对应章节 |
|------|----------|------|----------|
| 错误分类 | `guide/error-handling.md` | L9-L60 | s07（本章） |
| 重试 + 指数退避 | `guide/error-handling.md` | L62-L122 | s07 |
| 优雅降级 | `guide/error-handling.md` | L126-L228 |（不在课程范围） |
| Checkpoint 与 resume | `guide/error-handling.md` | L231-L322 | s11 |
