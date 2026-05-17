# s03 upstream excerpt: tool-system.md L36-L88 (Registry + dispatch + static-vs-dynamic)

Source: `guide/tool-system.md` L36-L88 in `nexu-io/harness-engineering-guide`
Permalink: https://github.com/nexu-io/harness-engineering-guide/blob/86fec9bea430cecb29ff10afaae36b96496a8f8e/guide/tool-system.md#L36-L88
License: MIT (© 2026 Nexu)

```python
# --- Tool Registry ---
class ToolRegistry:
    def __init__(self):
        self._tools: dict[str, Tool] = {}

    def register(self, name: str, schema: dict, handler: Callable):
        self._tools[name] = Tool(name=name, schema=schema, handler=handler)

    def get_schemas(self) -> list[dict]:
        """Return schemas for the LLM API call."""
        return [t.schema for t in self._tools.values()]

    def dispatch(self, name: str, arguments: dict) -> str:
        """Execute a tool call and return the result as a string."""
        tool = self._tools.get(name)
        if not tool:
            return f"Error: Unknown tool '{name}'"
        try:
            result = tool.handler(**arguments)
            return str(result)
        except Exception as e:
            return f"Error: {type(e).__name__}: {e}"

# Note: dispatch always returns a string, even for errors. This is intentional —
# the model needs to see error messages so it can adapt its approach, not crash
# silently.

# --- Static vs. Dynamic Tools ---
# Static tools: loaded at startup, always available. Works for 5-15 tools.
# 100 tools means 100 schemas per API call → tokens + confusion.
#
# Dynamic tools (skill loading): present a menu, load on demand.
SKILL_MENU = """
Available skills (use load_skill to activate):
- file_ops: Read, write, search files
- git: Git operations (status, diff, commit, push)
- web: HTTP requests, web search
- database: SQL queries, schema inspection
"""

def load_skill(skill_name: str) -> str:
    tools = skill_registry.load(skill_name)
    active_tools.extend(tools)
    return f"Loaded {len(tools)} tools: {[t.name for t in tools]}"

# Skill menu ≈ 200 tokens. Loading all 100 tools upfront ≈ 5,000+ tokens.
```

## Reading notes

1. **The always-string contract at L62-L63 is the most important sentence in the chapter.** Every later layer — guardrails (s06), classifier permissions (s14), retry (s07) — wraps `Dispatch` and adds its own pre/post logic. If `Dispatch` returned `error`, every wrapper would have to translate it; if it panicked on a missing tool, the loop would die mid-turn. Returning a *visible* string lets the model self-correct, exactly the way it self-corrects from a tool's *content* output. Our Go `Registry.Dispatch` enforces this for all three failure modes (unknown tool / invalid JSON / tool error).

2. **`get_schemas()` order is unspecified in the upstream; Go has to make it explicit.** Python's `dict` preserves insertion order since 3.7, so the Python code is *accidentally* deterministic. Go maps are explicitly randomised. We sort by `Name` in `Schemas()` for two reasons: (a) tests need a stable snapshot to compare against, (b) Anthropic and OpenAI both cache the tool block as part of the request prefix, and a reshuffled order = cache miss = slower + more expensive.

3. **Python kwargs vs. Go `json.RawMessage`.** `tool.handler(**arguments)` unpacks a dict so the handler signature reads `def read_file(path: str) -> str`. Go has no kwargs, so we pass `args json.RawMessage` and each tool calls `json.Unmarshal(args, &input)` into a typed struct. The Go form is more verbose but explicit — and it lets `Registry.Dispatch` do one syntax-only JSON validation pass *before* the tool sees the bytes, which is where we get the "Error: invalid args: ..." canonical message.

4. **Static vs. dynamic is the seam where s08 picks up.** L65-L88 lays out the math: 100 tools × ~50 tokens of schema each = 5K tokens *per turn* just for the menu. The skill system (s08) lazily activates bundles instead. s03 builds the static layer; s08 layers `SkillRegistry` on top — so the `Registry` we ship here must already be cheap to mutate at runtime, hence `Register(t Tool)` (not `Register(name, schema, handler)`; a `Tool` is a self-describing unit).

5. **Pitfalls from L157-L162 that this chapter pre-empts.** *Silent failures*: never return an empty string on error — `Dispatch` always returns a non-empty descriptive string. *Missing tool results*: covered by the s01 loop discipline (every `tool_use` must produce a matching `tool_result`). *Inconsistent return types*: the `Tool.Run` signature forces `(string, error)` — there's no "sometimes dict, sometimes string" footgun.

## Reading map

| Topic | Upstream file | Lines | Mapped chapter |
|-------|---------------|-------|----------------|
| Tool registry + dispatch contract | `guide/tool-system.md` | L36-L63 | s03 (this) |
| `read_file` / `write_file` Python | `guide/your-first-harness.md` | L42-L88 | s03 (the two real tools) |
| Static vs. dynamic tools | `guide/tool-system.md` | L65-L88 | s08 skills |
| Tool description quality | `guide/tool-system.md` | L90-L115 | s03 (we mirror good descriptions) |
| Tool composition patterns | `guide/tool-system.md` | L123-L134 | (emergent, not chapter-ized) |
| MCP | `guide/tool-system.md` | L136-L155 | (out of scope for the curriculum) |
| Common pitfalls | `guide/tool-system.md` | L157-L162 | s03 + s06 (guardrails) |
