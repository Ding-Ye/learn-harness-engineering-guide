# s01-minimum-loop

> The smallest think → act → observe loop in Go.
> 最小的 think → act → observe agentic loop（Go 版）。

## Scope / 范围

Build the agentic loop in ~250 lines of Go. No network, no real LLM — a `MockProvider` returns scripted responses so tests are deterministic and CI is offline-safe.
用 ~250 行 Go 写出 agentic loop。无网络、无真实 LLM —— `MockProvider` 返回脚本化响应，使测试可重现、CI 可离线运行。

## Files / 文件

```
types.go          Message / ToolCall / ChatResponse / Provider / Tool — minimal shapes
loop.go           Loop.Run — the for-turn loop with message-append discipline
mock_provider.go  MockProvider — scripted responses for offline tests
echo_tool.go      EchoTool — "echo: <text>" tool
main.go           CLI wrapper: takes one arg, runs a 2-turn scripted convo
loop_test.go      5 table-driven tests covering happy + edge cases
```

## Run / 运行

```bash
cd agents/s01-minimum-loop
go run . "hello world"
# I ran the echo tool on "hello world". Task complete.
```

## Test / 测试

```bash
go test ./...
# PASS - 5 tests
```

## Key teaching points / 教学要点

1. **The loop is a `for turn < maxTurns`**, not recursion — easier to add timeouts and metrics in later chapters.
   循环是 `for turn < maxTurns`，不是递归 —— 后面加超时和指标更容易。
2. **Every assistant message goes into history BEFORE its tool results.** Otherwise the model loses track of what it asked for. See `guide/your-first-harness.md` L113-L117.
   每个 assistant 消息必须先于其 tool 结果入历史，否则模型会丢失"我刚才请求过什么"的信息。
3. **Tools never return Go errors to the caller.** Errors become `string` content so the model can reason about them. See `guide/tool-system.md` L62.
   工具不把 Go error 返回给调用方 —— 错误变成字符串内容，让模型可以"读到"并自行调整。
4. **`MaxTurns` is non-negotiable.** A confused model loops forever; this is the cheapest safety belt.
   `MaxTurns` 是必须的 —— 困惑的模型会一直 loop，这是最廉价的保险。

## What the next chapter changes / 下一节的变化

s02 replaces `MockProvider` with a `Provider` interface having a real `AnthropicProvider` implementation, and upgrades `Message.Content string` to `[]ContentBlock` to match Anthropic's wire format.
s02 把 `MockProvider` 替换为 `Provider` 接口 + 真实的 `AnthropicProvider`，并把 `Message.Content string` 升级为 `[]ContentBlock` 以匹配 Anthropic 协议。
