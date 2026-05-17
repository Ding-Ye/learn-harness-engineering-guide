# s05-memory-layer

> Two-tier memory: curated `MEMORY.md` + append-only `memory/YYYY-MM-DD.md` daily logs.
> 双层记忆：精炼版 `MEMORY.md` + 仅追加的 `memory/YYYY-MM-DD.md` 每日 log。

## Scope / 范围

Implement the two-tier memory architecture from `guide/memory-and-context.md` L80-L144 in ~150 lines of Go. Pure filesystem — no LLM, no network. The session-startup read order is fixed: long-term first, then today's log, then yesterday's. A frozen `Clock` makes filenames deterministic in tests.
用 ~150 行 Go 实现 `guide/memory-and-context.md` L80-L144 的双层记忆架构。纯文件系统 —— 不调 LLM、不走网络。session 启动时的读取次序是定的：先长期记忆，再今天的 log，再昨天的。`Clock` 可注入，让测试里的文件名确定。

## Files / 文件

```
memory.go      Memory struct: New / Read / AppendLog / RotateOlderThan
clock.go       Clock interface + RealClock + FakeClock
main.go        CLI demo: seed MEMORY.md, append 3 lines, print combined view
memory_test.go 5 tests covering combine, append, race, rotate, missing-dir
```

## Run / 运行

```bash
cd agents/s05-memory-layer
go run .
# Prints the combined "long-term + today + yesterday" view to stdout.
```

## Test / 测试

```bash
go test -count=1 ./... -race
# PASS — 5 tests, including a 50-goroutine concurrent-append check
```

## Key teaching points / 教学要点

1. **Two tiers, not one.** `MEMORY.md` is *curated* (judgment required); daily logs are *cheap* (just append). Production harnesses write logs automatically and curate `MEMORY.md` periodically. See upstream L102-L125.
   **两层不是一层**。`MEMORY.md` 要"判断"（要不要写进来）；每日 log 是"廉价的"（直接 append）。生产 harness 自动写 log，定期 curate `MEMORY.md`。见上游 L102-L125。
2. **Read combines, not concatenates blindly.** `Read()` joins sections with `\n---\n` so the LLM can see boundaries between long-term and daily. Missing files are skipped silently. Anything older than yesterday is excluded.
   **Read 是"组合"，不是"无脑拼接"**。section 之间用 `\n---\n` 分隔，让 LLM 看得到长期 / 今日 / 昨日的边界；缺文件直接跳过；比昨天还旧的一律不含。
3. **AppendLog is append-only with a mutex.** Concurrent goroutines all land. On POSIX a sub-PIPE_BUF write to an `O_APPEND` fd is atomic, but we additionally lock so the contract holds on every platform.
   **AppendLog 严格 append-only + mutex**。多 goroutine 全部落盘。POSIX 上 `O_APPEND` 小写本身原子，但我们额外加锁让契约平台无关。
4. **Clock is the only seam.** Daily-log filenames depend on `time.Now()`. Inject `FakeClock` in tests; production wires `RealClock{}`.
   **Clock 是唯一的 seam**。每日 log 的文件名依赖 `time.Now()`，测试注入 `FakeClock`、生产传 `RealClock{}`。

## What the next chapter changes / 下一节的变化

s06 introduces a `GuardrailChecker` that intercepts tool dispatch — orthogonal to memory. Memory and guardrails do not interact directly in this chapter; the integration happens in `s_full`.
s06 引入 `GuardrailChecker` 拦截工具分发 —— 和记忆层正交。本章不让两者直接交互，集成放到 `s_full`。
