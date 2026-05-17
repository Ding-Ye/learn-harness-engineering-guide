# s03 — 工具注册

> 模型看到的是 schema；harness 拥有 dispatch。一个接口、一个注册表、两个真实工具 —— 加上一条契约：`Dispatch` 永远返回字符串，永不返回 error。

## Problem

s01 把 `EchoTool` 硬塞进 `Loop`。s02 引入了 `Provider` 接口，但工具表面还是 `map[string]Tool` + 半成品的 `Tool` 接口（只有 `Name()` 和 `Run(map[string]any)`）。这显然不够：

- 没法把"模型该看到的工具 schema 列表"稳定输出 —— 要么调用方每次现编 JSON Schema，要么从 map key 拼凑，毫无标准。
- 没法在不改所有 `Run` 调用点的前提下替换"某个名字到底跑哪个工具"（s06 护栏会用到）。
- 对"模型调用了不存在的工具 / 传了非法 JSON / 工具自己 error 了"这三种错误，没有统一答案 —— 而 `guide/tool-system.md` L52-L60 明确列出了它们。

我们需要一个有契约的 Registry。

## Solution

两层抽象：

- `Tool` 接口（`tool.go`）：`Name()` + `Description()` + `Schema() json.RawMessage` + `Run(ctx, json.RawMessage) (string, error)`。Schema 用原始 JSON 字节而非 Go map，避免每次请求重新 `json.Marshal` 且不会因 map 迭代顺序而 key 乱序。
- `Registry`（`registry.go`）：`Register(t Tool)`、`Schemas() []ToolSchema`（按字母排序）、`Dispatch(ctx, name, args) string` —— 契约上永远返回 string。

两个具体工具 `ReadFileTool`、`WriteFileTool` 对应上游 Python `guide/your-first-harness.md` L42-L88。`WriteFileTool` 用 `os.MkdirAll` 自动创建父目录，模型直接写 `path: "logs/2026/05/run.txt"` 就能成功，不必先学一个"建目录"工具。

## How It Works

```
                     ┌─────────────────────────────────────────┐
   模型看到 ──►       │  Registry.Schemas() []ToolSchema        │
   （稳定有序）       │  [{Name, Description, Schema}, ...]     │
                     └─────────────────────────────────────────┘
                                       │
   模型 emit tool_use ──►              ▼
                     ┌─────────────────────────────────────────┐
                     │  Registry.Dispatch(ctx, name, argsJSON) │
                     │                                         │
                     │   未注册:        "Error: unknown tool …" │
                     │   args 非法 JSON: "Error: invalid args …" │
                     │   tool.Run(args) ──► err?               │
                     │                       └► "Error running …" │
                     │                       （否则返回输出）       │
                     └─────────────────────────────────────────┘
                                       │
   harness 作为 ──►            result string
   tool result 消息追加
```

核心分发逻辑（`registry.go`）：

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

这套设计强制四条不变量：

1. **模型不会看到 Go panic 或半截响应**。`Dispatch` 的每条路径都返回非空、可读的字符串。
2. **错误对模型"可见"**。返回 `error` 的话调用方还得自己 stringify；返回字符串，模型直接读到 `"Error: unknown tool 'foo'"` 就能纠错（"哦我应该叫 `read_file` 不是 `foo`"）。
3. **JSON 校验一次性集中**。非法 JSON 拿到统一的 "invalid args" 消息，而不是 N 个工具各写一套 unmarshal 错误。
4. **Schemas() 按字母序排序**。Go map 迭代随机；不排序的话测试会 flake、prompt 缓存也会 miss。

## What Changed

s02 留下了一个两方法的 `Tool` 接口 + `map[string]any` args + 没有 schema。s03 把它升级：

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
+ func (r *Registry) Schemas() []ToolSchema           // 已排序
+ func (r *Registry) Dispatch(ctx, name, args) string // 永远 string
```

Loop 本体不变 —— 还是"先追加 assistant turn，再遍历 tool calls"。区别只在单次调用的分支：现在读 `registry.Dispatch(ctx, name, args)`，而不是 inline switch 工具名。

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

demo 验证的几件事：
- Schemas 是按字母序返回的：`read_file` 在 `write_file` 之前 —— 哪怕我们先 `Register` 的也是 `read_file`，排序对插入顺序也稳定。
- `write_file` 写到一个不存在的嵌套目录，第一次就能成功 —— 父目录自动建好。
- 调用不存在的工具，返回的是一条有用的错误字符串。没有 panic，没有 error。

## Upstream Source Reading

参考实现在 `guide/tool-system.md` L36-L88。关键段落是 L62：**"`dispatch` 永远返回字符串，即便出错也是。这是故意的 —— 模型需要看到错误信息才能调整策略，而不是静默崩溃。"** 我们的 Go `Dispatch` 严格遵循这一点。

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

阅读笔记：

- **Python 返回 `list(self._tools.values())`；Go 必须排序**。Python `dict` 从 3.7 起保持插入顺序，相当于"顺便"获得稳定顺序。Go map 是显式随机化的，必须按 `Name` 排序 —— 既为了测试可重现，也为了 prompt 缓存命中。
- **`Tool.handler(**arguments)` 变成 `tool.Run(ctx, args)`**。Python 把 dict 解包成 kwargs；Go 直接传原始 JSON 字节，让每个工具自己 unmarshal 到强类型 struct。结果是参数校验由每个工具自己负责，但只付一次 `json.Unmarshal` 开销。
- **Python 的裸 except 在 Go 里变成有类型的 error wrap**。Python 的 `Error: {type(e).__name__}: {e}` 变成 Go 的 `Error running <name>: <err>`。可观测行为一致，但 Go 的 wrap 错误可以被后续章节（s07）通过 `errors.Is` 识别后再 stringify。
- **`read_file` / `write_file` 来自另一篇文档**。两个真实工具的实现在 `guide/your-first-harness.md` L42-L88。挑这两个是故意的：它们 schema 非平凡（path + content）、必须做 I/O，且 `write_file` 能触发 `os.MkdirAll` 这个边界情况。
- **延伸阅读**。`guide/tool-system.md` L65-L88（静态 vs 动态工具 —— 为 s08 skill 加载铺垫）、L90-L115（工具描述质量）、L157-L162（常见坑：静默失败、漏掉 tool result）。

上游永久链接：[guide/tool-system.md @ 86fec9b](https://github.com/nexu-io/harness-engineering-guide/blob/86fec9bea430cecb29ff10afaae36b96496a8f8e/guide/tool-system.md#L36-L88)
