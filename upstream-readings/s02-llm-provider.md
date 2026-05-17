# s02 upstream excerpt: your-first-harness.md L209-L236 (Swapping Models)

Source: `guide/your-first-harness.md` L209-L236 in `nexu-io/harness-engineering-guide`
Permalink: https://github.com/nexu-io/harness-engineering-guide/blob/86fec9bea430cecb29ff10afaae36b96496a8f8e/guide/your-first-harness.md#L209-L236
License: MIT (© 2026 Nexu)

```python
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

## Reading notes

1. **The pivot point is the tool definition translation.** The list comprehension at lines 223-227 is the load-bearing detail of the whole section. OpenAI tools live in `{type: "function", function: {name, description, parameters}}`; Anthropic tools live in `{name, description, input_schema}`. The comprehension renames `parameters` → `input_schema` and flattens the wrapper. Our Go port internalizes this inside `AnthropicProvider.buildRequestBody` so callers never see it.

2. **`response.content` is a *list* of blocks, not a string.** This is the single biggest shape difference from OpenAI's API. A Claude assistant response might contain `[{type:"text", text:"Let me check"}, {type:"tool_use", id:"...", name:"...", input:{...}}]` — reasoning and a tool call in one response. Our s01 simplified shape (string + parallel `ToolCalls`) couldn't represent the ordering; s02's `[]ContentBlock` does, faithfully.

3. **`max_tokens` is required for Anthropic.** OpenAI's `chat.completions.create` defaults if you omit it; Anthropic's `messages.create` rejects requests without it. We default to 4096 when `req.MaxTokens == 0` to match the guide.

4. **"Same loop. Same tools. Different model."** — the closing sentence at L236 is the entire pedagogical point of the chapter. The Go `Provider` interface makes this literally true: change `Loop.Provider` from `*MockProvider` to `*AnthropicProvider` and nothing else moves.

5. **What we deliberately omit at this stage:**
   - Streaming responses (`messages.stream`) — covered conceptually in `guide/agentic-loop.md` L119-L137; not chapter-ized.
   - Prompt caching (`cache_control` markers on content blocks) — not in the upstream guide, would be a Phase-G addendum.
   - Vision blocks (`image` content type) — same; covered by extending `ContentBlock.Type`.
   - Retry/backoff (`anthropic-ratelimit-*` headers) — s07 owns this. Our 429-handler returns an error containing "rate limit" so s07 can key off it without re-classifying.

## Reading map

| Topic | Upstream file | Lines | Mapped chapter |
|-------|---------------|-------|----------------|
| OpenAI tool/messages call | `guide/your-first-harness.md` | L91-L119 | s01 |
| Anthropic provider (this excerpt) | `guide/your-first-harness.md` | L209-L236 | s02 (this) |
| Generic loop abstraction | `guide/agentic-loop.md` | L41-L65 | s02 (provider-agnostic by design) |
| Tool registry / dispatch | `guide/tool-system.md` | L9-L88 | s03 |
| Retry + backoff | `guide/error-handling.md` | L62-L122 | s07 (wraps `Provider.Chat`) |
