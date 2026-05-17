# s03-tool-registry

> Tool interface + Registry: schemas (what the model sees) vs. dispatch (what the harness runs).
> Tool 接口 + Registry：schema（模型看到的）与 dispatch（harness 真正执行的）分离。

## Scope / 范围

Make the tool surface a first-class abstraction. The model sees a stable
list of JSON schemas; the harness owns dispatch and always returns a string
(per `guide/tool-system.md` L62). Two real tools — `read_file`, `write_file` —
demonstrate the contract end-to-end without any network call.
把"工具表面"做成一等抽象。模型看到稳定的 JSON schema；harness 负责分发并永远返回字符串
（对应 `guide/tool-system.md` L62）。本章用 `read_file` / `write_file` 两个真实工具展示这一契约，
**全程零网络调用**。

## Files / 文件

```
tool.go             Tool interface + ToolSchema struct
registry.go         Registry: Register / Schemas (sorted) / Dispatch (always-string)
tools_fileops.go    ReadFileTool, WriteFileTool (with auto-mkdir for nested paths)
main.go             CLI demo: registers both tools, dispatches one of each, plus an unknown-tool call
registry_test.go    6 tests: schemas order, unknown tool, invalid JSON, tool error, read/write happy path, missing file
```

## Run / 运行

```bash
cd agents/s03-tool-registry
go run . demo
# === Schemas (what the model sees) ===
# - read_file: Read the contents of a file at the given path.
# - write_file: Write content to a file (creates or overwrites)...
# === Dispatch: write_file ===
# Wrote 15 chars to /tmp/.../nested/hello.txt
# === Dispatch: read_file ===
# hello from s03
# === Dispatch: unknown tool ===
# Error: unknown tool 'delete_universe'
```

## Test / 测试

```bash
go test -count=1 ./...
# PASS - 6 tests
```

## Key teaching points / 教学要点

1. **Schema and implementation are decoupled.** `ToolSchema` is a plain struct, not a reference to the `Tool`. Future chapters (s06 guardrails, s08 skills) can rewrite *which* tool runs without touching the schema the model is trained against. See `guide/tool-system.md` L9-L33.
   Schema 与实现解耦。`ToolSchema` 是普通结构体，不持有 `Tool` 引用。后续章节（s06 护栏、s08 skill）可以替换"实际跑哪个工具"而不动模型看到的 schema。

2. **`Dispatch` returns a string, NEVER an error.** Three failure modes each become a canonical string the model can read: `"Error: unknown tool 'X'"`, `"Error: invalid args: ..."`, `"Error running X: ..."`. Per `guide/tool-system.md` L62. The model needs to *see* errors so it adapts; if `Dispatch` returned `error` the caller would have to stringify it anyway.
   `Dispatch` 永远返回 string，不返回 error。三种失败各自有规范字符串，让模型自己读到再调整。

3. **`Schemas()` is alphabetically sorted.** Stable order avoids non-determinism in tests and — more importantly — keeps the prompt-cache key stable across requests. Map iteration in Go is randomised on purpose; we have to sort.
   `Schemas()` 字母序排序。除了让测试稳定，更重要是让 prompt 缓存命中率不被 map 随机迭代毁掉。

4. **`WriteFileTool` auto-creates parent dirs.** The upstream Python uses `os.makedirs(dirname, exist_ok=True)` (`guide/your-first-harness.md` L81); we mirror with `os.MkdirAll`. Without it the first write to a fresh path always fails — and the model has to learn to call a "make directory" tool first, which is wasteful.
   `WriteFileTool` 自动建父目录。上游 Python 用 `os.makedirs(..., exist_ok=True)`；Go 端用 `os.MkdirAll`。不这么做的话每次写新路径都会先 fail，浪费一个模型回合。

## What the next chapter changes / 下一节的变化

s04 leaves the registry untouched and adds a *context assembler* that decides which sections (system, tools, memory, recent messages, files) make it into the LLM call when token budget is tight. The registry's `Schemas()` becomes one input to that assembler.
s04 不改本章，而是新增一个 *上下文装配器*：在 token 预算紧张时，决定哪些段落（system、tools、memory、recent、files）能进入这次 LLM 调用。本章的 `Schemas()` 会成为装配器的一个输入。
