# s04 upstream excerpt: context-engineering.md L15-L87 (priority table + ContextAssembler)

Source: `guide/context-engineering.md` L15-L87 in `nexu-io/harness-engineering-guide`
Permalink: https://github.com/nexu-io/harness-engineering-guide/blob/86fec9bea430cecb29ff10afaae36b96496a8f8e/guide/context-engineering.md#L15-L87
License: MIT (© 2026 Nexu)

```markdown
## Context Assembly Priority System

Not all context is created equal. A priority system ensures the most critical information survives when space is tight:

| Priority | Category | Typical Tokens | Notes |
|----------|----------|---------------|-------|
| 0 (highest) | System prompt | 300–800 | Identity, behavior rules, safety constraints |
| 1 | Active tool schemas | 1,000–3,000 | Only loaded skills, not all tools |
| 2 | Task instruction | 200–1,000 | Current user request + any pinned goals |
| 3 | Memory summary | 500–2,000 | Compressed MEMORY.md + today's daily log |
| 4 | Injected files | 2,000–20,000 | AGENTS.md, SKILL.md, relevant source files |
| 5 | Recent conversation | 5,000–50,000 | Last N turns of messages + tool results |
| 6 (lowest) | Older conversation | remainder | Earlier turns, first to be compressed or dropped |

The assembler walks this list top-to-bottom, packing content until the budget is exhausted. Lower-priority content gets truncated or excluded entirely.
```

```python
import tiktoken

encoder = tiktoken.encoding_for_model("gpt-4o")

def estimate_tokens(text: str) -> int:
    """Fast token estimation using tiktoken."""
    return len(encoder.encode(text))

class ContextAssembler:
    """Assemble context with priority-based token budgeting."""

    def __init__(self, max_tokens: int = 128_000, reserve: int = 4_096):
        self.max_tokens = max_tokens
        self.reserve = reserve  # Leave room for the model's response
        self.budget = max_tokens - reserve
        self.sections: list[tuple[int, str, str]] = []

    def add(self, priority: int, name: str, content: str):
        """Add a section. Lower priority number = higher importance."""
        self.sections.append((priority, name, content))

    def build(self) -> list[dict]:
        """Pack sections into messages within the token budget."""
        self.sections.sort(key=lambda s: s[0])
        messages = []
        used = 0

        for priority, name, content in self.sections:
            tokens = estimate_tokens(content)
            if used + tokens <= self.budget:
                messages.append({
                    "role": "system",
                    "content": f"[{name}]\n{content}",
                })
                used += tokens
            elif priority <= 2:
                # Critical sections get truncated rather than dropped
                remaining = self.budget - used
                truncated = self._truncate_to_tokens(content, remaining)
                if truncated:
                    messages.append({
                        "role": "system",
                        "content": f"[{name} (truncated)]\n{truncated}",
                    })
                    used += estimate_tokens(truncated)
            # Priority > 2: silently dropped when over budget

        return messages

    def _truncate_to_tokens(self, text: str, max_tokens: int) -> str:
        """Truncate text to fit within a token limit."""
        tokens = encoder.encode(text)
        if len(tokens) <= max_tokens:
            return text
        return encoder.decode(tokens[:max_tokens]) + "\n[...truncated]"
```

## Reading notes

1. **The priority table at L19-L28 is the load-bearing artifact.** The Python code is one possible implementation; the *numbers* in the table are the contract every harness has to honor. System prompt > tool schemas > task > memory > files > recent > older — that ordering is what survives across model swaps. Our Go port internalises it as `criticalCutoff = 2` (the split between truncate-on-overflow and drop-on-overflow).

2. **`reserve = 4_096` is the unmissable detail at L43-L45.** Packing context to 100% of `max_tokens` leaves the model no room to reply, and you get mysterious truncated answers in prod. Our Go `ContextAssembler` makes this a constructor parameter (`NewContextAssembler(maxTokens, reserveTokens)`) so callers cannot forget it.

3. **`elif priority <= 2:` at L67 is the only branch that prevents catastrophic context loss.** If the system prompt overflows, you can't just drop it — the model loses its identity instructions and starts behaving like the base model. Truncation is a degraded-but-functional mode; dropping is a different agent entirely.

4. **`self.sections.sort(key=lambda s: s[0])` at L55 silently depends on CPython's stable Timsort.** Two same-priority sections always come out in add-order. Go's `sort.Slice` is *not* stable. Our `Build()` uses `sort.SliceStable` and additionally carries an `addOrder` integer so the contract is testable (and we run the test 50 times to defeat any future swap).

5. **`tiktoken` is OpenAI-specific.** Pinning to `encoding_for_model("gpt-4o")` is fine for an OpenAI-only harness, but our Go port aims at provider-agnostic. We use `words * 13 / 10` — pessimistic enough to be safe as a budget guard, cheap enough to call per-section per-turn, and dependency-free. s09 swaps in a real tokenizer when summarization needs accuracy.

## Reading map

| Topic | Upstream file | Lines | Mapped chapter |
|-------|---------------|-------|----------------|
| Priority table + assembler | `guide/context-engineering.md` | L15-L87 | s04 (this) |
| Token budgeting example | `guide/context-engineering.md` | L159-L178 | s04 |
| Context vs session vs memory | `guide/memory-and-context.md` | L11-L19 | s04 setup |
| Priority stack diagram | `guide/memory-and-context.md` | L21-L60 | s04 motivation |
| Auto-decay / threshold compress | `guide/context-engineering.md` | L91-L138 | s09 |
| Sliding window | `guide/context-engineering.md` | L194-L238 | s09 |
| Memory tiers (MEMORY.md + daily logs) | `guide/memory-and-context.md` | L80-L144 | s05 |
