# s04 — Context Assembler

> Priority-based context packing in ~250 lines of Go. Seven tiers, a token budget, truncate the critical and drop the rest. No network, no API key.

## Problem

The s01 loop happily appends every assistant message and tool result to history. That works for a 2-turn demo. It does not work for a 50-turn coding session: a single file can swallow 10K tokens, twenty tool schemas eat 3K, and conversation history grows linearly with every turn. Within a dozen turns of a real task, you're already making hard choices about what to keep.

The upstream `guide/context-engineering.md` (L9-L13) puts it this way:

> A 128K-token context window sounds enormous until you start filling it. […] Context engineering is the art of those choices. It has three pillars: assembly (what goes in), compression (what gets shrunk), and budgeting (how you allocate capacity).

This chapter implements **assembly** — the act of choosing what goes in the window each turn. Compression (s09) and budgeting are layered on top later.

## Solution

A small priority-based packer. The data model is two structs:

```go
type ContextSection struct {
    Priority int    // 0 = highest, 6 = lowest (matches upstream table L19-L28)
    Name     string // for debugging / logging
    Content  string // raw text to include in the prompt
}

type ContextAssembler struct {
    maxTokens     int  // total budget, e.g. 128_000
    reserveTokens int  // headroom for the model's response, e.g. 4_096
    sections      []orderedSection
}
```

The packing algorithm in `Build()`:

1. Sort sections ascending by `Priority`. Within the same priority, preserve add-order (stable sort).
2. Walk in order. If the section fits in the remaining budget (`budget = maxTokens - reserveTokens`), include it.
3. Otherwise:
   - **Priority ≤ 2** (critical: system / tools / task): truncate the content to fit the remaining budget and append `" (truncated)"` as a marker.
   - **Priority ≥ 3** (droppable: memory / files / recent / older conversation): silently drop.

Token counting uses a dependency-free heuristic — `words * 13 / 10`. The factor 1.3 accounts for sub-word splits. It's intentionally pessimistic, which is what you want from a budget guard.

## How It Works

```
Add(0, "system",   "...")
Add(1, "tools",    "...")
Add(5, "recent",   "...")  ─┐
Add(2, "task",     "...")   │  Build():
Add(4, "files",    "...")   │   1. sort by Priority asc
Add(3, "memory",   "...")  ─┘   2. for each in order:
                                     fits?    → include
                                     pri ≤ 2 → truncate
                                     pri ≥ 3 → drop
                                3. return (packed, used)
```

Tracing the test fixture in `main.go` with `maxTokens=200, reserve=20` (so `budget=180`):

| Pri | Name | Tokens | Outcome |
|---:|------|------:|--------|
| 0 | system-prompt | 10 | fits → included (used=10) |
| 1 | tool-schemas | 7 | fits → included (used=17) |
| 2 | task | 260 | over budget → **truncated** to 163 tokens + `(truncated)` (used=180) |
| 3 | memory | 10 | over budget, pri ≥ 3 → **dropped** |
| 4 | file-snippet | 65 | over budget, pri ≥ 3 → **dropped** |
| 5 | recent-chat | 104 | over budget, pri ≥ 3 → **dropped** |

The critical `task` row stays — possibly mangled, but the model still sees that there was a task instruction. The droppable rows vanish silently.

Two design choices worth flagging:

1. **Stable sort + explicit add-order.** Go's `sort.SliceStable` already preserves order for ties, but we also track an explicit `addOrder` integer. Tests run 50 trials to defeat any future change that swaps in an unstable algorithm.
2. **Truncation suffix is part of the content.** A separate `Truncated bool` field on `ContextSection` would be cleaner Go, but the suffix lives in `Content` because the *model* needs to see the marker — it's a contract with the prompt, not a downstream type assertion.

## What Changed (vs s03)

s03 built a `Tool` interface and a `Registry`. The loop now had real tools, but the prompt assembly was still "concatenate every message verbatim". s04 inserts a new module *between* `messages := []Message{...}` and `Provider.Chat(ctx, messages)`:

```diff
  loop.go (conceptually, integrated in s_full):
    messages := []Message{}
+   ca := NewContextAssembler(maxTokens, reserveTokens)
+   ca.Add(0, "system", systemPrompt)
+   ca.Add(1, "tools", toolSchemasAsText)
+   ca.Add(5, "recent", recentConversation)
+   packed, _ := ca.Build()
+   messages = packedToMessages(packed)
    resp, _ := provider.Chat(ctx, messages)
```

Tool registry (s03) is untouched. Loop (s01) is untouched. The assembler is purely additive — it sits before the LLM call and decides what subset of context to send.

## Try It

```bash
cd agents/s04-context-assembler
go test -count=1 ./...
# PASS - 6 tests in ~0.4s

go run .
# Budget: 180 (max=200, reserve=20)
# Used:   180 tokens across 3 sections
# (table of packed + dropped sections)
```

What just happened (annotated):
- We added 6 sections at priorities 0, 1, 2, 3, 4, 5 in *scrambled* order.
- Build sorted them by priority (0 first), then packed until the 180-token budget was full.
- The priority-2 `task` section was too large to fit whole, so it was truncated and marked.
- The priority-3 / 4 / 5 sections were over budget and dropped silently.

The full output is deterministic — re-running gives the same packed table and the same `used` count. This is the property tests rely on.

## Upstream Source Reading

Two upstream files feed this chapter: `guide/context-engineering.md` (the priority table and Python `ContextAssembler`) and `guide/memory-and-context.md` (the conceptual setup of "context vs session vs memory" with a simpler Python sketch).

```python
# Source: guide/context-engineering.md L40-L79
class ContextAssembler:
    """Assemble context with priority-based token budgeting."""

    def __init__(self, max_tokens: int = 128_000, reserve: int = 4_096):
        self.max_tokens = max_tokens
        self.reserve = reserve
        self.budget = max_tokens - reserve
        self.sections: list[tuple[int, str, str]] = []

    def add(self, priority: int, name: str, content: str):
        self.sections.append((priority, name, content))

    def build(self) -> list[dict]:
        self.sections.sort(key=lambda s: s[0])
        messages = []
        used = 0
        for priority, name, content in self.sections:
            tokens = estimate_tokens(content)
            if used + tokens <= self.budget:
                messages.append({"role": "system", "content": f"[{name}]\n{content}"})
                used += tokens
            elif priority <= 2:
                remaining = self.budget - used
                truncated = self._truncate_to_tokens(content, remaining)
                if truncated:
                    messages.append({"role": "system",
                                     "content": f"[{name} (truncated)]\n{truncated}"})
                    used += estimate_tokens(truncated)
            # Priority > 2: silently dropped
        return messages
```

Reading notes:

- **`priority <= 2` is the central rule.** The upstream's three lines `elif priority <= 2:` encode the whole "critical vs droppable" split. Our Go version names that threshold (`const criticalCutoff = 2`) so future readers can grep for it.
- **The Python `estimate_tokens` uses `tiktoken`.** That's an OpenAI library tied to a specific model encoding. Our Go version uses a 1.3x word-count heuristic to keep the chapter dependency-free; s09 will swap in a real tokenizer when summarization needs accuracy.
- **`reserve = 4_096` is not obvious.** L43-L45 spell it out: "you need to leave headroom for the model's response. If you pack the context to 100%, the model has no room to reply." Skipping this gives you puzzling truncated answers in production.
- **The upstream returns `list[dict]`** (Anthropic / OpenAI message shape). Our Go version returns `[]ContextSection` because that's the natural domain object; s_full will write a `packedToMessages(packed []ContextSection) []Message` adapter at the integration point.
- **What the upstream skips that we surface.** The Python silently relies on `sort()` being stable (CPython's Timsort is). We name the requirement explicitly in `Build()` because Go's `sort.Slice` is *not* stable; you have to opt in via `sort.SliceStable`. A subtle portability gotcha.

Upstream permalink: [guide/context-engineering.md @ 86fec9b L15-L87](https://github.com/nexu-io/harness-engineering-guide/blob/86fec9bea430cecb29ff10afaae36b96496a8f8e/guide/context-engineering.md#L15-L87)

Companion reading: [guide/memory-and-context.md L21-L60](https://github.com/nexu-io/harness-engineering-guide/blob/86fec9bea430cecb29ff10afaae36b96496a8f8e/guide/memory-and-context.md#L21-L60) introduces the three concepts (context / session / memory) and shows the priority-stack diagram our assembler implements.
