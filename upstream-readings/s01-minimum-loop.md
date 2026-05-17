# s01 upstream excerpt: your-first-harness.md L91-L119 (the tool loop)

Source: `guide/your-first-harness.md` L91-L119 in `nexu-io/harness-engineering-guide`
Permalink: https://github.com/nexu-io/harness-engineering-guide/blob/86fec9bea430cecb29ff10afaae36b96496a8f8e/guide/your-first-harness.md#L91-L119
License: MIT (© 2026 Nexu)

```python
# --- The tool loop ---
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
        messages.append(msg)                          # invariant: append assistant FIRST

        # No tool calls → model is done
        if not msg.tool_calls:                        # OpenAI's "end turn" signal
            return msg.content

        # Execute each tool call
        for tc in msg.tool_calls:
            args = json.loads(tc.function.arguments)
            print(f"  🔧 {tc.function.name}({args})")
            result = execute_tool(tc.function.name, args)
            messages.append({                         # append a tool message per call
                "role": "tool",
                "tool_call_id": tc.id,
                "content": result
            })

    return "Max turns reached."
```

## Reading notes

1. **The append-then-branch discipline is the only invariant of the chapter.** Every later layer (retries, guardrails, compression, sub-agents) wraps this loop without changing it. The Go port at `agents/s01-minimum-loop/loop.go` mirrors this exactly.

2. **OpenAI signals "done" via `not msg.tool_calls`; Anthropic uses `stop_reason == "end_turn"`.** Our `ChatResponse.StopReason` follows Anthropic shape because s02's real provider is Anthropic — translation down to OpenAI is mechanical.

3. **`execute_tool` is *silently* fault-tolerant in the upstream.** The Python uses `try: ... except Exception as e: return f"Error: {e}"`. The Go version does the same in `Loop.executeTool` — every tool result is a string, including errors. The model reads "Error: ..." and either retries or apologizes.

4. **What's hidden in the upstream that we surface in Go.** Python returns `"Max turns reached."` as a normal string; we return an explicit Go `error`. This matters in later chapters where retry/checkpoint logic needs to distinguish "model finished" from "harness gave up".

5. **What we deliberately omit at this stage:**
   - Streaming (upstream covers in `guide/agentic-loop.md` L119-L137)
   - Real LLM provider (s02)
   - Tools beyond `echo` (s03 introduces `read_file`/`write_file`)
   - System prompt customisation (s04's context assembler)

## Reading map

| Topic | Upstream file | Lines | Mapped chapter |
|-------|---------------|-------|----------------|
| Minimal loop | `guide/your-first-harness.md` | L91-L119 | s01 (this) |
| Generic loop abstraction | `guide/agentic-loop.md` | L41-L65 | s01 |
| Parallel tool calls | `guide/agentic-loop.md` | L70-L85 | s01 |
| Streaming responses | `guide/agentic-loop.md` | L119-L137 | (not chapter-ized) |
| Anthropic provider | `guide/your-first-harness.md` | L209-L236 | s02 |
| `read_file` / `write_file` tools | `guide/your-first-harness.md` | L42-L88 | s03 |
