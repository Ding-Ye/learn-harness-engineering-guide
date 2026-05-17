# s01 — 最小循环

> 配得上"agentic"这个名字的最小 think → act → observe 循环。一个脚本化 provider、一个工具、一个 for 循环。无网络、无 API key。

## Problem

我们要用 Go 写一个把裸 LLM 变成 agent 的 harness。在引入重试、护栏、记忆分层、子 agent 之前，必须先**看清楚**核心抽象：agentic loop。上游 guide 用 13K 行讲 harness 能做什么；如果不先把核心循环装进脑子，后面任何特性都没意义：

> 模型发出一个 turn → harness 检查 → 如果含 tool calls，harness 执行并把结果回灌 → 重复，直到模型发出一个没有 tool calls 的 turn。

这一章用 ~250 行 Go 端到端实现这个循环，**无需 API key 也能编译并通过测试**。

## Solution

一个 `Loop` 结构体持有：
- 一个 `Provider`（本章是手写脚本化的 `MockProvider`，s02 引入真实实现）
- 一个 `map[string]Tool` 注册表（本章只有 `EchoTool`）
- 一个 `MaxTurns` 整数（防止失控循环的最廉价保险）

每一 turn 的流程：
1. 调 `Provider.Chat(ctx, messages)` 拿到下一条响应。
2. **永远先把 assistant 消息追加到历史**（即便它只含 tool calls）。
3. 按 `StopReason` 分支：`"end_turn"` → 返回文本；`"tool_use"` → 执行该 turn 里所有 tool calls，把每个结果作为 `"tool"` 消息追加。

然后继续循环。

## How It Works

```
┌──────────┐    Chat()    ┌──────────┐
│ messages │ ───────────► │ Provider │
└──────────┘ ◄─────────── └──────────┘
     │       ChatResponse
     │
     │  StopReason == "end_turn"?  ──── yes ──► 返回 Content
     │
     │  StopReason == "tool_use"?
     ▼
  ┌──────────────────┐
  │ for each call:   │
  │   tool.Run(args) │ ───► append {Role:"tool", Content:result}
  └──────────────────┘
     │
     └─── 回到顶部, turn++
```

核心代码片段（`loop.go`）：

```go
for turn := 0; turn < l.MaxTurns; turn++ {
    resp, err := l.Provider.Chat(ctx, messages)
    if err != nil {
        return "", fmt.Errorf("turn %d: provider error: %w", turn, err)
    }

    // 1. 追加 assistant turn —— 即便只含 tool calls 也要追加。
    messages = append(messages, Message{
        Role:      "assistant",
        Content:   resp.Content,
        ToolCalls: resp.ToolCalls,
    })

    // 2. 按 stop reason 分支。
    if resp.StopReason == "end_turn" {
        return resp.Content, nil
    }
    if resp.StopReason != "tool_use" {
        return "", fmt.Errorf("turn %d: unexpected stop_reason %q", turn, resp.StopReason)
    }

    // 3. 执行该 turn 里所有 tool calls。
    for _, call := range resp.ToolCalls {
        result := l.executeTool(call)
        messages = append(messages, Message{
            Role:       "tool",
            ToolCallID: call.ID,
            Content:    result,
        })
    }
}
return "", fmt.Errorf("max turns reached (%d) without end_turn", l.MaxTurns)
```

四个关键点：

1. **assistant turn 必须先于 tool 结果入历史**。次序反了的话，下一次 provider 调用会看到没有 assistant 锚定的 tool 消息 —— 大多数 API 会直接 400。Python 上游也是这么做的，见 `guide/your-first-harness.md` L98-L99。
2. **工具永远不向调用方返回 Go error**。`executeTool` 内部，`unknown tool` 和 `tool.Run(...) error` 都变成字符串内容。模型读到后能自己判断。见 `guide/tool-system.md` L62。
3. **并发 tool calls 在同一个 assistant turn 内**。当模型同时发两个 `tool_use` block，我们都执行，并追加两个 `tool` 消息后才进入下一轮。对应 `guide/agentic-loop.md` L70-L85。
4. **`MaxTurns` 返回 error，不是静默回退**。没有 turn 上限的 loop 是教科书级烧 API 配额方式。

## What Changed

第一章 —— 没有 `s00` 可以 diff。这个循环就是后续所有章节的脊柱：

```diff
+ types.go        Message / ToolCall / ChatResponse / Provider / Tool
+ loop.go         Loop.Run：先追加再分支
+ mock_provider.go ScriptedProvider 用于确定性测试
+ echo_tool.go    EchoTool（"echo: <text>"）
+ main.go         CLI demo
+ loop_test.go    5 个单测
```

## Try It

```bash
cd agents/s01-minimum-loop
go test -count=1 ./...
# PASS: 5 tests in ~0.5s

go run . "hello world"
# I ran the echo tool on "hello world". Task complete.
```

刚才发生了什么（标注版）：
- Turn 0：`MockProvider` 返回 `StopReason="tool_use"` + `ToolCalls=[{echo "hello world"}]`。Loop 调用 `EchoTool.Run`，追加 `{Role:"tool", Content:"echo: hello world"}`。
- Turn 1：`MockProvider` 返回 `StopReason="end_turn"` + `Content="I ran the echo tool..."`。Loop 返回最终文本。

## Upstream Source Reading

`guide/your-first-harness.md` L90-L120 的参考实现是 ~30 行 Python，做同样的事。它用 OpenAI 的 `chat.completions.create`，所以线材格式是 OpenAI-shape；我们的 Go 版在 s01 保持格式无关（s02 才接入真实的 Anthropic/OpenAI）。

```python
# Source: guide/your-first-harness.md L91-L119
def run(user_message: str) -> str:
    messages = [
        {"role": "system", "content": SYSTEM},
        {"role": "user", "content": user_message}
    ]
    for turn in range(MAX_TURNS):
        response = client.chat.completions.create(
            model=MODEL, messages=messages, tools=TOOLS
        )
        msg = response.choices[0].message
        messages.append(msg)                          # ← 先追加 assistant

        if not msg.tool_calls:                        # ← 结束信号：没有 tool calls
            return msg.content

        for tc in msg.tool_calls:                     # ← 执行每个 tool call
            args = json.loads(tc.function.arguments)
            result = execute_tool(tc.function.name, args)
            messages.append({
                "role": "tool",
                "tool_call_id": tc.id,
                "content": result
            })
    return "Max turns reached."
```

阅读笔记：

- **stop_reason 与"tool_calls 为空"判断**。OpenAI API 用"没有 tool_calls"表示结束；Anthropic 用显式的 `stop_reason="end_turn"` 字段。我们的 Go `Provider` 接口走 Anthropic shape 的 `StopReason`，因为 s02 要引入真实的 `AnthropicProvider`；从 Anthropic 翻译到 OpenAI 比反向更容易。
- **`MAX_TURNS = 15`**。上游选 15；测试里用 5（更快反馈），main flag 在 5-20 之间（s_full 集成时）。无论选多少，**必须有这个数**。
- **上游的错误处理是静默的**。`execute_tool` 捕获 `Exception` 返回字符串。我们在 `Loop.executeTool` 里显式处理，后续 s07 章节可以把单个工具错误包进重试/分类逻辑。
- **我们刻意省略的**。上游的 `read_file`/`write_file` 工具放在 s03（`tool-registry`），不在这里。s01 把工具面积压到最小，让 loop 是聚光灯下唯一的角色。
- **延伸阅读**。`guide/agentic-loop.md` L41-L65（通用 loop 抽象）、L70-L85（并发 tool calls）、L119-L137（流式 —— 本章范围外）。

上游永久链接：[guide/your-first-harness.md @ 86fec9b](https://github.com/nexu-io/harness-engineering-guide/blob/86fec9bea430cecb29ff10afaae36b96496a8f8e/guide/your-first-harness.md#L91-L119)
