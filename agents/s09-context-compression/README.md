# s09-context-compression

> Sliding window with cumulative summary: keep the last N=15 messages verbatim, fold everything older into one `[Conversation history summary]` block. Trigger by token budget (70%), not by message count.
> 滑动窗口 + 累积式摘要：最近 N=15 条消息原样保留，更早的全部折叠成单一的 `[Conversation history summary]`。按 token 预算（70%）触发，不按消息条数。

## Scope / 范围

Implement the third "line of defense" against runaway context growth from `guide/context-engineering.md` L91-L238 in ~250 lines of Go. Token-based threshold, system messages never compressed, summary is *cumulative* (re-summarized each event with previous summary fed back). No real LLM call — a deterministic `MockSummarizer` makes the test matrix reproducible.
用 ~250 行 Go 实现 `guide/context-engineering.md` L91-L238 的"第三道防线"。Token 触发、system 消息永不压缩、摘要**累积**（每次压缩把上一次的摘要作为上下文喂回去）。不调真实 LLM —— 用确定性的 `MockSummarizer` 让测试矩阵可复现。

## Files / 文件

```
types.go               Local Message/ContentBlock (no import from s02)
tokens.go              EstimateTextTokens + EstimateTokens (copied from s04 heuristic)
summarize.go           Summarizer interface + MockSummarizer + SummarizePrompt
sliding_window.go      SlidingWindowContext: Add, compress, GetMessages, Summary, Len
main.go                CLI demo: feeds 60 dummy turns, prints compression events
sliding_window_test.go 5 tests
```

## Run / 运行

```bash
cd agents/s09-context-compression
go run .
# Feeds 60 turns through a small-budget context; prints 3 compression events
# then the final GetMessages view (1 system + 1 summary + 15 user messages).
```

## Test / 测试

```bash
go test -count=1 ./...
# PASS — 5 tests
```

## Key teaching points / 教学要点

1. **Trigger is token-based, not message-count-based.** Compression runs when `EstimateTokens(messages) > threshold * maxTokens`. One huge message can trip the threshold even if total message count is well below the window. The test `ThresholdRespectsTokens` is the canary.
   **触发条件是 token 数、不是消息条数**。当 `EstimateTokens(messages) > threshold * maxTokens` 时压缩才跑。一条超大消息就可能触发，哪怕消息总数远小于 window。`ThresholdRespectsTokens` 测试就是金丝雀。
2. **System messages are never compressed.** They pass through every compression event unchanged, and they always appear in `GetMessages()` *before* the synthetic summary block. This is the upstream contract at L217 (`system = [m for m in self.messages if m["role"] == "system"]`).
   **System 消息永不压缩**。每次压缩都原样穿过，在 `GetMessages()` 输出里总是排在 summary 块之前。这是上游 L217 的契约。
3. **Summary is cumulative, not concatenated.** Each compression takes `(previousSummary, oldMessages)` and produces a *new* summary that supersedes the old one. The summary block stays roughly constant-size across the session lifetime instead of growing linearly. The mock summarizer's `prev=L%d` format makes this observable in tests.
   **摘要是累积的、不是拼接的**。每次压缩接受 `(上一次的摘要, 待压缩的旧消息)`、产出**新**摘要并替换掉旧的。整个 session 生命周期内摘要块的体积大致恒定，不会线性膨胀。Mock summarizer 的 `prev=L%d` 把这个性质暴露给测试断言。
4. **Threshold trips compress() even when there's nothing to compress.** With one huge message and windowSize=15, `compress()` is called but finds `nonSystem (1) <= windowSize (15)` and bails. `CompressAttempts` still ticks up so the test can verify the trigger fired. The caller's only recourse is to raise `maxTokens` or break the message.
   **触发 compress() 不等于真的压缩了东西**。一条超大消息 + windowSize=15 的情况下，`compress()` 会被调用，但因为 `nonSystem (1) <= windowSize (15)` 直接返回。`CompressAttempts` 仍然 +1，让测试能验证触发本身发生了。调用方要么加大 `maxTokens`、要么自己切分消息。

## What the next chapter changes / 下一节的变化

s10 introduces a `SessionStore` — append-only JSONL event log. It manages a *durable record* of everything that happened; s09 manages the *transient view* the model sees. Distinct concepts. A real harness uses both: the event log is the source of truth, the sliding window is a derived snapshot recomputed per LLM call.
s10 引入 `SessionStore` —— append-only JSONL 事件日志。它管"所有发生过的事的持久记录"；s09 管"模型这一回合能看到的临时视图"。两个完全不同的概念。真实 harness 两个都要：事件日志是真相源、滑动窗口是每次 LLM 调用之前重算的派生快照。
