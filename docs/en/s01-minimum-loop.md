# s01 — Minimum Loop

> The smallest think → act → observe loop that deserves the name "agentic". One scripted provider, one tool, one for-loop. No network, no API key.

## Problem

We want to build a harness in Go that turns a bare LLM into an agent. Before we worry about retries, guardrails, memory tiers, or sub-agents, we need to *see* the central abstraction: the agentic loop. The upstream guide spends 13K lines describing what a harness can be; almost none of those features make sense without first internalizing the core cycle:

> The model emits a turn → the harness inspects it → if the turn contains tool calls, the harness executes them and feeds results back → repeat until the model produces a turn with no tool calls.

This chapter teaches that cycle, end-to-end, in ~250 lines of Go that build and pass tests without an API key.

## Solution

A `Loop` struct that owns:
- A `Provider` (in this chapter, a hand-scripted `MockProvider` — s02 will introduce a real one)
- A `map[string]Tool` registry (in this chapter, a single `EchoTool`)
- A `MaxTurns` integer (the cheapest safety belt against runaway loops)

The loop's job, per turn:
1. Call `Provider.Chat(ctx, messages)` to get the next response.
2. **Always** append the assistant message to history (even when it only contains tool calls).
3. Branch on `StopReason`: `"end_turn"` → return text; `"tool_use"` → execute every tool call in the assistant turn and append each result as a `"tool"` message.

Then repeat.

## How It Works

```
┌──────────┐    Chat()    ┌──────────┐
│ messages │ ───────────► │ Provider │
└──────────┘ ◄─────────── └──────────┘
     │       ChatResponse
     │
     │  StopReason == "end_turn"?  ──── yes ──► return Content
     │
     │  StopReason == "tool_use"?
     ▼
  ┌──────────────────┐
  │ for each call:   │
  │   tool.Run(args) │ ───► append {Role:"tool", Content:result}
  └──────────────────┘
     │
     └─── back to top, turn++
```

Core code excerpt (`loop.go`):

```go
for turn := 0; turn < l.MaxTurns; turn++ {
    resp, err := l.Provider.Chat(ctx, messages)
    if err != nil {
        return "", fmt.Errorf("turn %d: provider error: %w", turn, err)
    }

    // 1. Append the assistant turn — even when only tool calls are present.
    messages = append(messages, Message{
        Role:      "assistant",
        Content:   resp.Content,
        ToolCalls: resp.ToolCalls,
    })

    // 2. Decide based on stop reason.
    if resp.StopReason == "end_turn" {
        return resp.Content, nil
    }
    if resp.StopReason != "tool_use" {
        return "", fmt.Errorf("turn %d: unexpected stop_reason %q", turn, resp.StopReason)
    }

    // 3. Execute every tool call in this assistant turn.
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

Four callouts:

1. **The assistant turn is appended BEFORE its tool results.** If you flip the order, the next provider call sees `tool` messages with no preceding `assistant` to anchor them — most APIs reject this with a 400. The Python upstream makes the same call at `guide/your-first-harness.md` L98-L99.
2. **Tools never return Go errors to the caller.** Inside `executeTool`, both `unknown tool` and `tool.Run(...) error` become `string` content. The model reads them and can react. See `guide/tool-system.md` L62.
3. **Parallel tool calls happen in one assistant turn.** When the model emits two `tool_use` blocks at once, we run both and append two `tool` messages before the next provider call. Mirrors `guide/agentic-loop.md` L70-L85.
4. **`MaxTurns` returns an error, not a silent fallback.** A loop without a turn cap is the textbook way to burn an API budget in minutes.

## What Changed

First chapter — there's no `s00` to diff against. The loop is the spine; every subsequent chapter adds one component:

```diff
+ types.go        Message / ToolCall / ChatResponse / Provider / Tool
+ loop.go         Loop.Run with append-then-branch discipline
+ mock_provider.go ScriptedProvider for deterministic tests
+ echo_tool.go    EchoTool ("echo: <text>")
+ main.go         CLI demo
+ loop_test.go    5 unit tests
```

## Try It

```bash
cd agents/s01-minimum-loop
go test -count=1 ./...
# PASS: 5 tests in ~0.5s

go run . "hello world"
# I ran the echo tool on "hello world". Task complete.
```

What just happened (annotated):
- Turn 0: `MockProvider` returns `StopReason="tool_use"` + `ToolCalls=[{echo "hello world"}]`. Loop dispatches `EchoTool.Run`, appends `{Role:"tool", Content:"echo: hello world"}`.
- Turn 1: `MockProvider` returns `StopReason="end_turn"` + `Content="I ran the echo tool..."`. Loop returns final text.

## Upstream Source Reading

The reference implementation in `guide/your-first-harness.md` L90-L120 is ~30 lines of Python that does the same thing. It uses OpenAI's `chat.completions.create` so the wire format is OpenAI-shape; our Go port stays format-agnostic in s01 (we'll plug in real Anthropic/OpenAI in s02).

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
        messages.append(msg)                          # ← append assistant FIRST

        if not msg.tool_calls:                        # ← end-turn signal: no tool calls
            return msg.content

        for tc in msg.tool_calls:                     # ← execute each tool call
            args = json.loads(tc.function.arguments)
            result = execute_tool(tc.function.name, args)
            messages.append({
                "role": "tool",
                "tool_call_id": tc.id,
                "content": result
            })
    return "Max turns reached."
```

Reading notes:

- **Stop-reason vs `tool_calls` empty check.** OpenAI's API signals "done" by emitting an assistant message with no `tool_calls`. Anthropic uses an explicit `stop_reason="end_turn"` field. Our Go `Provider` interface uses Anthropic-shape `StopReason` because s02 will introduce a real `AnthropicProvider` and it's easier to translate down to OpenAI later than up from OpenAI.
- **`MAX_TURNS = 15`.** The upstream picks 15; we use 5 in tests for fast feedback and 5-20 in main flag (s_full integration). Either way, *some* number is non-negotiable.
- **Error handling is silent in the upstream.** `execute_tool` catches `Exception` and returns the string. We make this explicit in `Loop.executeTool` so future chapters (s07) can wrap individual tool errors in retry/classification logic.
- **What we deliberately omit.** The upstream's `read_file`/`write_file` tools live in s03 (`tool-registry`), not here. s01 keeps the tool surface as small as possible so the loop is in focus.
- **Where to read further.** `guide/agentic-loop.md` L41-L65 (generic loop abstraction), L70-L85 (parallel tool calls), L119-L137 (streaming — out of scope for this chapter).

Upstream permalink: [guide/your-first-harness.md @ 86fec9b](https://github.com/nexu-io/harness-engineering-guide/blob/86fec9bea430cecb29ff10afaae36b96496a8f8e/guide/your-first-harness.md#L91-L119)
