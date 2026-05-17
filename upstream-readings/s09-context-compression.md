# s09 upstream excerpt: context-engineering.md L91-L160 (compression strategies)

Source: `guide/context-engineering.md` L91-L160 in `nexu-io/harness-engineering-guide`
Permalink: <https://github.com/nexu-io/harness-engineering-guide/blob/86fec9bea430cecb29ff10afaae36b96496a8f8e/guide/context-engineering.md#L91-L160>
Cross-reference: `guide/long-running-harness.md` L19-L92 (context anxiety + reset vs compaction)
License: MIT (© 2026 Nexu)

```markdown
## Context Compression: Three Lines of Defense

As a session progresses, raw conversation history grows without bound. Three
techniques prevent it from consuming the entire context window:

### Line 1: Auto-Decay

Older messages naturally lose relevance. A simple decay strategy drops messages
beyond a fixed window, keeping only the most recent N turns:

    def apply_decay(messages: list[dict], max_turns: int = 20) -> list[dict]:
        """Keep the system prompt and the last max_turns exchanges."""
        system = [m for m in messages if m["role"] == "system"]
        conversation = [m for m in messages if m["role"] != "system"]
        if len(conversation) > max_turns * 3:
            conversation = conversation[-(max_turns * 3):]
        return system + conversation

### Line 2: Threshold Compression

When total tokens cross a threshold (e.g., 70% of budget), compress older
conversation turns into a summary while keeping recent turns verbatim:

    def threshold_compress(messages, budget, threshold=0.7, keep_recent=10):
        total = sum(estimate_tokens(m["content"]) for m in messages)
        if total < budget * threshold:
            return messages

        system = [m for m in messages if m["role"] == "system"]
        conversation = [m for m in messages if m["role"] != "system"]
        old = conversation[:-keep_recent]
        recent = conversation[-keep_recent:]

        summary = summarize_with_llm(old)
        compressed = system + [{
            "role": "system",
            "content": f"[Conversation summary]\n{summary}",
        }] + recent
        return compressed

### Line 3: Active Summarization

For extremely long-running tasks, periodically extract key facts and decisions
into a running summary document. This is not automatic — the harness explicitly
asks the model to produce a checkpoint:

    SUMMARIZE_PROMPT = """Summarize the key decisions, findings, and current
    state from this conversation. Include: files modified, tests run, errors
    encountered, and the current plan. Be concise — under 500 words."""

Real token arithmetic for a 128K context window:

    Total capacity:              128,000 tokens
    Response reserve:             -4,096
    System prompt:                  -500
    Tool schemas (12 tools):      -2,400
    MEMORY.md:                    -1,200
    AGENTS.md:                      -800
    ─────────────────────────────────────
    Available for conversation:  119,004 tokens

    At ~3 tokens/word, that's ~39,600 words of conversation.
    A 50-turn coding session with tool results: ~60,000 tokens.
    → You'll hit the budget around turn 35 without compression.

The takeaway: compression is not optional for any non-trivial session.
```

## Reading notes

1. **Three lines of defense are not three competing strategies — they are layers.** Auto-decay (line 1) is brutal but free: drop anything beyond a fixed window, no LLM call. Threshold compression (line 2) is what we ship: tokens drive the trigger, recent turns survive, an LLM rewrites the rest. Active summarization (line 3) is for *very* long-running tasks where you periodically dump state to disk *outside* the context. s09 implements line 2; s11's checkpoint-resume is the closest match to line 3.

2. **The threshold check (line 2) is on tokens, not on messages.** The Python snippet sums `estimate_tokens(m["content"])` across all messages. We do the same in Go via `EstimateTokens([]Message)`. A naive port that watched `len(messages)` instead would miss the case where a single tool result blows the budget — and that case is the entire reason "threshold compression" is a distinct technique from "auto-decay".

3. **Threshold defaults: 0.7 is the upstream pick, not a law.** The 0.7 leaves 30% headroom for the model's response, the system prompt, tool schemas, and the *new* user message that just arrived. Picking 0.9 means you'll exceed the budget on the next message; picking 0.5 means you compress too aggressively and lose context for no token win. 0.7 is the documented default in line 2 and the one we hard-code as the parameter default.

4. **The `[Conversation summary]` (line 2) and `[Conversation history summary]` (sliding window) wording differ in the upstream and we follow.** Line 2's threshold compressor produces a *single* summary message and inserts it inline. The sliding-window class at L194-L238 — what s09 actually implements — uses `[Conversation history summary]` (with the word "history"), which is the marker we hard-code. Tests grep on the marker; don't drift.

5. **Active summarization (line 3) doesn't run on every turn — it runs on checkpoints.** The `SUMMARIZE_PROMPT` is meant to be invoked manually at phase boundaries, not on every message. A common harness pattern is: every 50 turns (or before a phase change), call `active_summarize`, persist the result to disk, then `context_reset` to start a fresh segment with that summary as the seed. This is the *reset* arm of `long-running-harness.md` L49-L92's reset-vs-compaction trade-off; s11 will pick it up.

## Reading map

| Topic | Upstream file | Lines | Mapped chapter |
|-------|---------------|-------|----------------|
| Three lines of defense | `guide/context-engineering.md` | L91-L158 | s09 (this) |
| Sliding window implementation | `guide/context-engineering.md` | L194-L238 | s09 |
| Token budget arithmetic | `guide/context-engineering.md` | L159-L178 | s04 + s09 |
| Context anxiety (mental model) | `guide/long-running-harness.md` | L19-L46 | Appendix A + s09 cross-ref |
| Reset vs compaction | `guide/long-running-harness.md` | L49-L92 | s09 (compaction) + s11 (reset) |
| Generator-evaluator | `guide/long-running-harness.md` | L94-L138 | s11 |
