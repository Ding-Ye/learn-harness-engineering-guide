# s02 — LLM Provider

> Hide the LLM behind a stable interface so the loop never knows whether it's talking to a script, Anthropic, or OpenAI. Upgrade the message shape to the canonical Anthropic form that every later chapter inherits.

## Problem

s01 hard-coded a `MockProvider` and used a simplified message shape (`Message{Role, Content string}` plus separate `ToolCalls` / `ToolCallID` fields). That worked for proving the loop, but two limitations are immediate:

1. **Cannot swap models.** The upstream guide spends a section (`guide/your-first-harness.md` L209-L236) demonstrating that "the harness is model-agnostic — switch to Anthropic's Claude by changing the client." Our s01 cannot do this; the `MockProvider` type is referenced directly in the loop.
2. **The message shape doesn't match reality.** A real Anthropic assistant message has `content: []` — a list of blocks, where some are `text` and some are `tool_use`, often *in the same message*. The simplified s01 shape (string + parallel ToolCalls slice) loses the ordering and means we can't model the wire format faithfully.

We want to fix both, today, in one chapter. The fix establishes a stable boundary that every later chapter (s07's retry layer, s09's compression, s14's classifier) reuses without modification.

## Solution

Introduce three things:

**a) A `Provider` interface.** One method: `Chat(ctx, ChatRequest) (*ChatResponse, error)`. The loop holds a `Provider` field and never refers to any concrete type.

**b) Two implementations.**
- `MockProvider` — replays a list of `ChatResponse`. Either constructed in-memory or loaded from a JSON file in `testdata/`. Deterministic. No network. No API key. Used by every test and the default CLI mode.
- `AnthropicProvider` — issues a real `POST https://api.anthropic.com/v1/messages` with `x-api-key` and `anthropic-version: 2023-06-01` headers. Marshals our `ChatRequest` into Anthropic's exact wire shape (tools become `{name, description, input_schema}`, *not* `parameters`). Parses the response into `[]ContentBlock`. 429 responses return an error whose message contains "rate limit" so s07's retry layer can identify retryable failures.

**c) Canonical types.** `Message{Role, []ContentBlock}` and a `ContentBlock` union type with `Type ∈ {text, tool_use, tool_result}`. These are what Anthropic actually puts on the wire and they lose nothing under translation to OpenAI's flatter shape.

## How It Works

The Loop is structurally identical to s01 — same for-loop, same append-then-branch discipline, same `MaxTurns` guard. What changed is the data going through it.

```
                     ┌──────────────────────────┐
                     │     ChatRequest          │
                     │   {Model, System,        │
                     │    Messages, Tools,      │
                     │    MaxTokens}            │
                     └────────────┬─────────────┘
                                  │
                ┌─────────────────┴─────────────────┐
                ▼                                   ▼
        ┌───────────────┐                  ┌─────────────────┐
        │ MockProvider  │                  │ Anthropic       │
        │ (in-memory or │                  │ Provider        │
        │  testdata)    │                  │ (HTTPS)         │
        └───────┬───────┘                  └────────┬────────┘
                │                                   │
                └─────────────────┬─────────────────┘
                                  ▼
                       ┌─────────────────────┐
                       │   ChatResponse      │
                       │  {[]ContentBlock,   │
                       │   StopReason}       │
                       └─────────────────────┘
```

Loop iteration looks like this:

```go
for turn := 0; turn < l.MaxTurns; turn++ {
    req := ChatRequest{
        Model: l.Model, System: l.System,
        Messages: messages, Tools: tools,
    }
    resp, err := l.Provider.Chat(ctx, req)
    if err != nil { return "", err }

    // 1. Append assistant message — content is []ContentBlock now.
    messages = append(messages, Message{Role: "assistant", Content: resp.Content})

    // 2. end_turn → final text from text blocks.
    if resp.StopReason == "end_turn" {
        return extractText(resp.Content), nil
    }

    // 3. tool_use → execute every tool_use block in this turn.
    toolResults := make([]ContentBlock, 0)
    for _, block := range resp.Content {
        if block.Type != "tool_use" { continue }
        result, isErr := l.executeTool(ctx, block)
        toolResults = append(toolResults, ContentBlock{
            Type: "tool_result", ID: block.ID,
            Content: result, IsError: isErr,
        })
    }
    if len(toolResults) > 0 {
        messages = append(messages, Message{Role: "tool", Content: toolResults})
    }
}
```

Four callouts:

1. **One assistant turn can contain mixed blocks.** A real model often emits a sentence of reasoning ("Let me check that file…") *and* a tool_use in the same response. We loop over `resp.Content` and treat each block by its `Type`. s01 couldn't represent this.
2. **Tool results live in a `tool_result` block, not a separate field.** The tool message is `Message{Role:"tool", Content:[]ContentBlock{...tool_result blocks...}}`. One tool message can carry multiple results when parallel tool calls happened.
3. **The Anthropic wire field is `input_schema`.** OpenAI calls the same thing `parameters`. Crossing this up is a 5-minute debugging session. `TestAnthropicProvider_RequestShape` makes the cost zero by asserting the key.
4. **429 → "rate limit" in the error.** No retry yet (s07 owns that), but the signal is there for s07 to key off.

## What Changed

The big diff is the `Message` shape:

```diff
- // s01: simplified shape
- type Message struct {
-     Role       string
-     Content    string     // plain text
-     ToolCalls  []ToolCall // assistant turns only
-     ToolCallID string     // tool turns only
- }
- type ToolCall struct {
-     ID, Name string
-     Args     map[string]any
- }
- type ChatResponse struct {
-     Content    string
-     ToolCalls  []ToolCall
-     StopReason string
- }
- type Provider interface {
-     Chat(ctx context.Context, []Message) (*ChatResponse, error)
- }

+ // s02: canonical Anthropic shape
+ type Message struct {
+     Role    string
+     Content []ContentBlock
+ }
+ type ContentBlock struct {
+     Type    string          // "text" | "tool_use" | "tool_result"
+     Text    string          // when Type=="text"
+     ID      string          // tool_use id / tool_result correlation
+     Name    string          // when Type=="tool_use"
+     Input   json.RawMessage // when Type=="tool_use"
+     Content string          // when Type=="tool_result"
+     IsError bool            // when Type=="tool_result"
+ }
+ type ChatResponse struct {
+     Content    []ContentBlock
+     StopReason string
+ }
+ type Provider interface {
+     Chat(ctx context.Context, req ChatRequest) (*ChatResponse, error)
+ }
```

Three other changes:

1. **`ChatRequest` is now a struct** carrying `Model`, `System`, `Tools`, `MaxTokens` — not just `[]Message`. The loop can plumb config into the provider without bypass.
2. **`Tool` interface gained `Schema()` and `Run(ctx, json.RawMessage)`** (was `Run(map[string]any)`). Tools see raw JSON so the provider can pass it through unchanged.
3. **`AnthropicProvider` is the first real-world I/O code in the curriculum.** `httptest.Server` makes it CI-safe.

## Try It

```bash
cd agents/s02-llm-provider

go test -count=1 ./...
# ok  ...  6 tests
# (TestAnthropicProvider_SkipsWhenNoKey shows as PASS-skipped without a key)

go run . -provider=mock "echo hello s02"
# Done. The echo tool returned "echo: hello s02".

# Optional: with a real API key
export ANTHROPIC_API_KEY=sk-ant-...
go test -count=1 -run TestAnthropicProvider_SkipsWhenNoKey ./...
# (now actually runs the live request)
```

What you observe with the mock provider:
- Turn 0: provider emits `{text + tool_use}` in one `Content` slice. Loop dispatches `EchoTool.Run` and appends a `tool` message containing a `tool_result` block.
- Turn 1: provider emits one `text` block with `StopReason="end_turn"`. Loop extracts the text and returns.

What you would observe with the Anthropic provider given the same prompt: identical loop behavior, real text from Claude, tool_use blocks with real `toolu_*` IDs. Same loop, same tools, different model — exactly what the upstream guide promises at L236.

## Upstream Source Reading

The Anthropic-side excerpt from `guide/your-first-harness.md` L209-L236 is the conceptual seed for this chapter. The guide demonstrates that swapping providers is a mechanical translation — same loop, same tools, just a different client.

```python
# Source: guide/your-first-harness.md L209-L236
## Swapping Models

The harness is model-agnostic. Switch to Anthropic's Claude by changing the client:

from anthropic import Anthropic

client = Anthropic()

response = client.messages.create(
    model="claude-sonnet-4-20250514",
    max_tokens=4096,
    system=SYSTEM,
    messages=messages,
    tools=[{
        "name": t["function"]["name"],
        "description": t["function"]["description"],
        "input_schema": t["function"]["parameters"]
    } for t in TOOLS]
)

# Parse tool calls from response.content blocks
for block in response.content:
    if block.type == "tool_use":
        result = execute_tool(block.name, block.input)

Same loop. Same tools. Different model.
```

Reading notes:

- **The Python upstream's translation is one comprehension at the call site.** The OpenAI client returns `tool_calls` (flat), the Anthropic client returns `content[]` with `tool_use` blocks (rich). The comprehension at line 223-227 maps OpenAI tool definitions into Anthropic's shape (note: `t["function"]["parameters"]` → `input_schema`). Our Go port internalizes this — `ChatRequest.Tools` is provider-agnostic, and each provider's body builder does the translation in one place.
- **`response.content` is a list, not a string.** This is the single most important shape difference from OpenAI's API. The guide's `for block in response.content` is exactly what our `for _, block := range resp.Content` does in Go.
- **`max_tokens` is required for Anthropic.** OpenAI defaults; Anthropic does not. We default to 4096 in `buildRequestBody` when `req.MaxTokens == 0`.
- **What we deliberately omit at this stage.** Streaming (`messages.stream`), prompt caching (`cache_control` on blocks), vision blocks (`image` content type), and extended thinking (`thinking` parameter). All would fit in the existing interface; none are needed to teach the boundary.
- **Where to read further.** `guide/your-first-harness.md` L91-L120 (the OpenAI loop we ported as s01); `guide/agentic-loop.md` L41-L65 (generic loop abstraction — provider-agnostic by intent); `guide/error-handling.md` L62-L122 (retry/backoff, picked up by s07).

Upstream permalink: [guide/your-first-harness.md @ 86fec9b](https://github.com/nexu-io/harness-engineering-guide/blob/86fec9bea430cecb29ff10afaae36b96496a8f8e/guide/your-first-harness.md#L209-L236)
