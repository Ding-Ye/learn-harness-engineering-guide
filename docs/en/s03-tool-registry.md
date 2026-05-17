# s03 — Tool Registry

> The model sees schemas; the harness owns dispatch. One interface, one registry, two real tools — and a contract that `Dispatch` always returns a string, never an error.

## Problem

s01 hard-wired a single `EchoTool` directly into the `Loop`. s02 introduced a `Provider` interface but left the tool surface a `map[string]Tool` with a half-shaped `Tool` interface (just `Name()` and `Run(map[string]any)`). That doesn't scale:

- We can't ship the model a stable list of tool *schemas* without copying the map's keys and inventing JSON Schemas ad-hoc at every call site.
- We can't change *which* tool runs for a given name (s06 guardrails) without touching every place that calls `Run`.
- We have no canonical answer to "what should happen when the model calls a tool that doesn't exist, or with invalid JSON, or when the tool itself errors?" — three failure modes that the upstream guide explicitly enumerates at `guide/tool-system.md` L52-L60.

We need a registry with a real contract.

## Solution

A two-layer abstraction:

- `Tool` interface (`tool.go`): `Name()` + `Description()` + `Schema() json.RawMessage` + `Run(ctx, json.RawMessage) (string, error)`. Schemas are raw JSON bytes so we don't pay a `json.Marshal` cost on every request and don't re-shuffle key order.
- `Registry` (`registry.go`): `Register(t Tool)`, `Schemas() []ToolSchema` (alphabetically sorted), `Dispatch(ctx, name, args) string` — always-string by contract.

Two concrete tools, `ReadFileTool` and `WriteFileTool`, mirror the upstream Python at `guide/your-first-harness.md` L42-L88. `WriteFileTool` auto-creates parent dirs (via `os.MkdirAll`) so a model that emits `path: "logs/2026/05/run.txt"` never has to learn a separate "make directory" tool.

## How It Works

```
                     ┌─────────────────────────────────────────┐
   model sees ──►    │  Registry.Schemas() []ToolSchema        │
   (stable, sorted)  │  [{Name, Description, Schema}, ...]     │
                     └─────────────────────────────────────────┘
                                       │
   model emits tool_use ──►            ▼
                     ┌─────────────────────────────────────────┐
                     │  Registry.Dispatch(ctx, name, argsJSON) │
                     │                                         │
                     │   if !known:    "Error: unknown tool …" │
                     │   if !json:     "Error: invalid args …" │
                     │   tool.Run(args) ──► err?               │
                     │                       └► "Error running …" │
                     │                       (else return out)   │
                     └─────────────────────────────────────────┘
                                       │
   harness appends as ──►       result string
   tool result message
```

Core dispatch (`registry.go`):

```go
func (r *Registry) Dispatch(ctx context.Context, name string, args json.RawMessage) string {
    tool, ok := r.tools[name]
    if !ok {
        return fmt.Sprintf("Error: unknown tool '%s'", name)
    }
    if len(args) > 0 {
        var probe any
        if err := json.Unmarshal(args, &probe); err != nil {
            return fmt.Sprintf("Error: invalid args: %v", err)
        }
    }
    out, err := tool.Run(ctx, args)
    if err != nil {
        return fmt.Sprintf("Error running %s: %v", name, err)
    }
    return out
}
```

Four invariants this enforces:

1. **The model never sees a Go panic or a half-written response.** Every code path through `Dispatch` produces a non-empty, descriptive string.
2. **Errors are *visible* to the model.** A returned `error` would leave the loop guessing how to format it. A string lets the model read `"Error: unknown tool 'foo'"` and self-correct ("oh, I should have called `read_file`, not `foo`").
3. **JSON validation is a first pass, not a per-tool concern.** A malformed JSON request gets one canonical "invalid args" message instead of N tool-specific decoder errors.
4. **Schemas() is sorted alphabetically.** Map iteration in Go is randomised; without sorting, tests flake and prompt caches miss.

## What Changed

s02 left `Tool` as a two-method interface with `map[string]any` args and no schema. s03 promotes it:

```diff
- type Tool interface { Name() string; Run(map[string]any) (string, error) }
+ type Tool interface {
+     Name() string
+     Description() string
+     Schema() json.RawMessage
+     Run(ctx context.Context, args json.RawMessage) (string, error)
+ }
+
+ type ToolSchema struct {
+     Name        string
+     Description string
+     Schema      json.RawMessage
+ }
+
+ type Registry struct { tools map[string]Tool }
+ func (r *Registry) Register(t Tool)
+ func (r *Registry) Schemas() []ToolSchema           // sorted
+ func (r *Registry) Dispatch(ctx, name, args) string // always-string
```

The Loop body itself doesn't change — it still appends assistant turn, then walks tool calls. The difference is that the per-call branch now reads `registry.Dispatch(ctx, name, args)` instead of an inline switch over tool names.

## Try It

```bash
cd agents/s03-tool-registry
go test -count=1 ./...
# PASS - 6 tests

go run . demo
# === Schemas (what the model sees) ===
# - read_file: Read the contents of a file at the given path.
# - write_file: Write content to a file (creates or overwrites). ...
# === Dispatch: write_file ===
# Wrote 15 chars to /tmp/s03-demo-.../nested/hello.txt
# === Dispatch: read_file ===
# hello from s03
# === Dispatch: unknown tool (returns error string, no panic) ===
# Error: unknown tool 'delete_universe'
```

What the demo proves:
- Schemas come back sorted: `read_file` before `write_file` (alphabetical) even though we registered `read_file` first — the sort is stable on insertion order too.
- `write_file` to a nested path that doesn't exist works on first try; the parent dir is auto-created.
- An unknown-tool dispatch returns a useful error string. No panic, no `error`.

## Upstream Source Reading

The reference implementation lives in `guide/tool-system.md` L36-L88. The key paragraph is L62: *"Note that `dispatch` always returns a string, even for errors. This is intentional — the model needs to see error messages so it can adapt its approach, not crash silently."* Our Go `Dispatch` enforces exactly that.

```python
# Source: guide/tool-system.md L40-L61
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
```

Reading notes:

- **Python returns `list(self._tools.values())`; Go sorts.** Python `dict` preserves insertion order since 3.7, so the upstream gets stable ordering by accident. Go maps are explicitly randomised. We have to sort by `Name` — both for test determinism and for prompt-cache stability.
- **`Tool.handler(**arguments)` becomes `tool.Run(ctx, args)`.** The Python signature unpacks a dict as kwargs; Go takes raw JSON bytes and lets the tool unmarshal them into a typed struct. This means each tool owns its own argument validation but pays only one `json.Unmarshal` call.
- **The bare-except in Python becomes a typed Go error wrap.** `Error: {type(e).__name__}: {e}` in Python becomes `Error running <name>: <err>` in Go. Same observable behaviour, but Go's wrapped errors propagate through `errors.Is` if a later chapter (s07) wants to classify them before stringifying.
- **`read_file` / `write_file` come from a different file.** The two real tools are at `guide/your-first-harness.md` L42-L88. We picked them deliberately: they have non-trivial schemas (path + content), require I/O, and `write_file` exercises the `os.MkdirAll` edge case.
- **Where to read further.** `guide/tool-system.md` L65-L88 (static vs dynamic tools — sets up s08 skill loading), L90-L115 (description quality), L157-L162 (common pitfalls: silent failures and missing tool results).

Upstream permalink: [guide/tool-system.md @ 86fec9b](https://github.com/nexu-io/harness-engineering-guide/blob/86fec9bea430cecb29ff10afaae36b96496a8f8e/guide/tool-system.md#L36-L88)
