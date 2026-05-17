# s09 — Context Compression

> Sliding window over conversation history. Keep the last 15 messages verbatim; fold everything older into a cumulative summary. Trigger by token budget, not by message count.

## Problem

By s08 the harness has a budget-aware context assembler (s04), a memory layer (s05), guardrails (s06), retry (s07), and on-demand skills (s08). But there is still a property no earlier chapter has: **the conversation history grows monotonically**. Every turn appends user/assistant/tool messages; nothing ever leaves. At ~3 tokens per word and ~500 words per turn-with-tool-result, a 50-turn coding session crosses 75K tokens — and a 128K-token model is paying retail price for that runway every turn.

There is also a softer, more interesting failure mode the guide spends a whole chapter on: as the context window fills, the model starts cutting corners. `guide/long-running-harness.md` L19-L46 calls this **context anxiety** — emergent rushing behavior visible as fewer tool calls, terser outputs, premature "done" declarations. The bigger window doesn't fix it; *managing* the window does.

So s09 does the active-compression job. It does NOT replace s04 (which picks *which sections* to include for one turn); it sits *upstream* of s04 by mutating the conversation history itself across turns.

## Solution

`SlidingWindowContext` directly from `guide/context-engineering.md` L194-L238:

```go
swc := NewSlidingWindowContext(
    /*windowSize=*/ 15,
    /*maxTokens=*/  128_000,
    /*threshold=*/  0.7,
    /*summarizer=*/ &MockSummarizer{}, // or a real LLM-backed one
)
swc.Add(msg1)
swc.Add(msg2)
// ... after many adds ...
msgs := swc.GetMessages() // [system...] + [summary?] + [last 15 non-system]
```

Three discipline rules:

| | Rule |
|---|---|
| Trigger | `EstimateTokens(messages) > threshold * maxTokens` (default 70%). Not message count. |
| System | Role="system" messages are NEVER compressed. They pass through every event. |
| Summary | Cumulative. Each compression takes (prevSummary, oldMessages) → new summary. |

A `Summarizer` interface keeps the LLM call abstract. In production it's a fast cheap model; in tests we inject `MockSummarizer` that returns a deterministic string (`[summarized N msgs; prev=L%d]`) so the test matrix is reproducible without network.

## How It Works

**Add** does two things: append, and maybe compress.

```go
func (s *SlidingWindowContext) Add(msg Message) error {
    s.messages = append(s.messages, msg)
    budget := int(float64(s.maxTokens) * s.threshold)
    if EstimateTokens(s.messages) > budget {
        return s.compress(context.Background())
    }
    return nil
}
```

The threshold check is on *total token count*, not on `len(s.messages)`. That's the entire reason the threshold check exists: a single message with a giant tool result can trip the budget while the message count is tiny. `TestSlidingWindow_ThresholdRespectsTokens` is the canary.

**compress** partitions, splits, summarizes, rebuilds:

```
all messages
   ├── system     → kept verbatim
   └── non-system
        ├── pre-window  → fed (with prevSummary) to summarizer → new summary
        └── recent (last windowSize) → kept verbatim
```

The new state is `s.messages = system + recent`, and `s.summary = newSummary`. The summary is **not** spliced back into `s.messages` — that would mean the next compression has to figure out "is this an original system message or a summary I produced?". Instead, `GetMessages()` synthesizes the summary block at read time:

```
GetMessages output:
    [original system messages]
    [synthetic system message: "[Conversation history summary]\n<summary>"]
    [last windowSize non-system messages]
```

The "cumulative" part is the most important property and the easiest to get wrong. On the *second* compression we don't summarize-the-already-summarized — we ask the summarizer to produce a *fresh* summary of (previous summary, new old messages). The mock summarizer encodes `len(prevSummary)` into its output (`prev=L%d`) so the test `TestSlidingWindow_SummaryAccumulates` can verify each call's `prevSummary` exactly equals the previous call's output. This is the property that lets the strategy work for very long sessions: the summary block stays roughly constant-size instead of growing linearly.

There is one trick edge case worth calling out. If a single message crosses the threshold but `len(nonSystem) <= windowSize`, there's nothing pre-window to summarize. `compress()` still runs (it still increments `CompressAttempts`), but the body short-circuits and the summarizer is not called. The caller's huge message stays in the buffer verbatim. This is intentional: silently splitting a message would be worse than honestly admitting the budget can't help. The doc test `TestSlidingWindow_ThresholdRespectsTokens` pins this behavior.

## What Changed

| | s04 (assembler) | s09 (compression) |
|---|---|---|
| Lifetime | one LLM call | spans many turns |
| Mutates | nothing — picks subset of fixed sections | rewrites conversation history |
| Token budget | yes — drops/truncates sections | yes — triggers `compress()` |
| Layer | reads finished history → packs once | sits *upstream* of history; runs per Add() |
| Composition | reads memory snippet from s05 | composes with s04 *outside* this chapter |

s04 and s09 are complementary. In `s_full` the wiring is: turn N starts → s09 (compress if needed) → s04 (pack remaining history + memory + tool schemas into budget) → call LLM. s04 sees a *shorter* conversation than it would without s09. Neither chapter imports the other — the `EstimateTokens` heuristic from s04 is *copied* into `tokens.go` because the curriculum's rule is that every chapter is a self-contained module.

## Try It

```bash
cd agents/s09-context-compression
go vet ./... && go build ./... && go test -count=1 ./...
# PASS — 5 tests

go run .
# === feeding 60 turns into SlidingWindowContext ===
# [turn 29] compression #1: len(messages)=16 summary="[summarized 15 msgs; prev=L0]"
# [turn 44] compression #2: len(messages)=16 summary="[summarized 15 msgs; prev=L29]"
# [turn 59] compression #3: len(messages)=16 summary="[summarized 15 msgs; prev=L30]"
#
# === what the LLM sees (GetMessages) ===
# [ 0] role=system    You are a careful coding assistant.
# [ 1] role=system    [Conversation history summary]
#                     [summarized 15 msgs; prev=L30...
# [ 2] role=user      turn 45: token token token token
# ...
# [16] role=user      turn 59: token token token token
```

Note the summary's `prev=L30` on the third compression — it received the second compression's 30-character output as `prevSummary`. That's the cumulative property in action.

## Upstream Source Reading

Source: `guide/context-engineering.md` L91-L160. Permalink: <https://github.com/nexu-io/harness-engineering-guide/blob/86fec9bea430cecb29ff10afaae36b96496a8f8e/guide/context-engineering.md#L91-L160>

Cross-reference: `guide/long-running-harness.md` L19-L92 ("Context Anxiety" + "Reset vs Compaction") explains *why* this matters and *how to think about* the trade-off.

```python
# guide/context-engineering.md L199-L237 (the canonical SlidingWindowContext)
class SlidingWindowContext:
    def __init__(self, window_size: int = 15, max_tokens: int = 128_000):
        self.window_size = window_size
        self.max_tokens = max_tokens
        self.summary = ""
        self.messages: list[dict] = []

    def add(self, message: dict):
        self.messages.append(message)
        conversation = [m for m in self.messages if m["role"] != "system"]
        if len(conversation) > self.window_size * 3:
            self._compress()

    def _compress(self):
        conversation = [m for m in self.messages if m["role"] != "system"]
        system = [m for m in self.messages if m["role"] == "system"]
        old = conversation[:-(self.window_size * 3)]
        recent = conversation[-(self.window_size * 3):]
        new_summary = summarize_with_llm(
            [{"role": "system", "content": self.summary}] + old
        )
        self.summary = new_summary
        self.messages = system + recent

    def get_messages(self) -> list[dict]:
        result = [m for m in self.messages if m["role"] == "system"]
        if self.summary:
            result.append({
                "role": "system",
                "content": f"[Conversation history summary]\n{self.summary}",
            })
        result.extend(m for m in self.messages if m["role"] != "system")
        return result
```

Reading notes:

- **The upstream Python's `* 3` is a turn-vs-message conversion we drop.** Python's "window_size" counts turns, and a turn is roughly user + assistant + tool ≈ 3 messages. We made `windowSize` count messages directly, because the threshold check is what actually matters and the windowSize ratio is now just "how many to keep verbatim when we do compress". Less arithmetic, same effect.
- **The upstream trigger is message-count, not token-count.** Look at `if len(conversation) > self.window_size * 3` — Python checks count. We trade up to a token-budget check because `context-engineering.md` L110-L138 ("threshold compression") spells out that the budget is the real concern. Message count is a coarse proxy that fails on tool results.
- **`summarize_with_llm` takes the previous summary as the system prompt.** That's the cumulative property in the upstream. Our `Summarizer` interface takes `prevSummary` as an explicit parameter rather than as a synthetic message — same semantics, clearer type.
- **The `[Conversation history summary]` marker is verbatim.** Don't change it. Downstream tooling (eval harnesses, observability) greps for it.
- **Long-running-harness.md is the *why*; context-engineering.md is the *how*.** Read them in that order. The "context anxiety" mental model from `long-running-harness.md` L19-L46 is the reason you build this at all; the sliding-window class is just one mechanism for *handling* it. `s_full` weaves both together.

Reading map:

| Topic | Upstream file | Lines | Mapped chapter |
|-------|---------------|-------|----------------|
| Three lines of defense | `guide/context-engineering.md` | L91-L158 | s09 (this) |
| Sliding window class | `guide/context-engineering.md` | L194-L238 | s09 |
| Context anxiety mental model | `guide/long-running-harness.md` | L19-L46 | Appendix A + s09 cross-ref |
| Reset vs compaction trade-off | `guide/long-running-harness.md` | L49-L92 | s09 cross-ref |
| Generator-evaluator (alt approach) | `guide/long-running-harness.md` | L94-L138 | s11 |
| Token-budgeted assembler (complementary) | `guide/context-engineering.md` | L15-L87 | s04 |
