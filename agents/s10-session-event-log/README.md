# s10-session-event-log

> Append-only JSONL session log: one file per session, one JSON event per line. Every significant point in the harness (`user_message`, `llm_call`, `tool_call`, `tool_result`, `error`, `session_end`) is one line. The session outlives the harness — if the harness crashes, the file is still on disk.
> Append-only 的 JSONL session 日志：每个 session 一个文件、每行一个 JSON 事件。Harness 里每个重要节点（`user_message`、`llm_call`、`tool_call`、`tool_result`、`error`、`session_end`）都对应一行。Session 比 harness 命长 —— harness 崩了，文件还在。

## Scope / 范围

Implement the "Session" pillar from `guide/managed-agents-architecture.md` L74-L112 in ~550 LOC of Go. A `FileStore` opens `sessions/<id>.jsonl` lazily, caches the FD, and writes append-only with mutex + `O_APPEND`. `GetEvents` supports offset / limit / type-filter slicing. `Replay` converts events back into a flat `[]Message` for harness rehydration. No real LLM call — the demo and tests use scripted events end-to-end.
用 ~550 行 Go 实现 `guide/managed-agents-architecture.md` L74-L112 的 "Session" 这一支柱。`FileStore` 懒打开 `sessions/<id>.jsonl`、缓存 FD、用 mutex + `O_APPEND` 追加。`GetEvents` 支持 offset / limit / 类型过滤。`Replay` 把事件压回扁平 `[]Message` 用于 harness 重启重建。**不调真实 LLM** —— demo 和测试都用脚本化事件端到端跑通。

## Files / 文件

```
event.go            Event struct + MarshalJSON pinning field order + canonical type constants
store.go            SessionStore interface + GetEventsOpts
file_store.go       FileStore — mutex + O_APPEND, FD cache, MkdirAll on construction
replay.go           Replay() — events → []Message via small conversion table
main.go             CLI demo: 6 events → GetEvents → Replay
store_test.go       5 tests (append-read, slicing, type filter, concurrent emit, replay)
```

## Run / 运行

```bash
cd agents/s10-session-event-log
go run .
# writes 6 events to /tmp/s10-XXXX/sessions/demo-001.jsonl
# prints GetEvents output, type-filtered output, and Replay'd message history
```

## Test / 测试

```bash
go vet ./... && go build ./... && go test -count=1 -race ./...
# PASS — 5 tests, ~1.6s under -race
```

## Key teaching points / 教学要点

1. **One file per session, append-only, JSONL.** Not one big database, not per-event files. The format is human-readable (you can `tail -f sessions/foo.jsonl` while the agent runs) AND machine-parseable (one `json.Unmarshal` per line). Crash-safety is the point: an open `O_APPEND` write is atomic on POSIX up to `PIPE_BUF` (~4KiB+) so even without our mutex, two writers couldn't interleave bytes. With the mutex, behavior is deterministic across platforms.
   **每个 session 一个文件、append-only、JSONL**。不是一个大库、也不是每事件一个文件。这种格式既人类可读（agent 跑的时候可以 `tail -f sessions/foo.jsonl`）又机器可解析（每行一次 `json.Unmarshal`）。**重点是 crash 安全**：POSIX 上 `O_APPEND` 的一次写在 `PIPE_BUF` (~4KiB+) 以内是原子的，所以即便没有 mutex、两个写者也不会交错字节。加 mutex 是为了跨平台行为确定。

2. **Session ≠ context window.** This is the most subtle point of the upstream architecture. The session is the *complete, durable record* — potentially millions of tokens spanning days. The context window is the *subset* the harness picks per LLM call — 128K-200K tokens. s10 is the session; s09 (sliding window) is what feeds the context window. They're complementary: a real harness emits to s10 unconditionally and lets s09 derive the per-call view from a subset of the events.
   **Session ≠ context window**。这是上游架构最微妙的一点。Session 是**完整、持久的记录** —— 可能几百万 token、跨越几天。Context window 是 harness 每次 LLM 调用挑出来的**子集** —— 128K-200K token。**s10 是 session、s09（滑动窗口）是喂给 context window 的视图**。两者互补：真实 harness 无条件往 s10 写、让 s09 从事件子集派生出"这一回合给模型看的内容"。

3. **`Replay()` is intentionally lossy.** Only `user_message`, `llm_call`, and `tool_result` become Messages. `tool_call`, `error`, `session_end` are skipped. The reasoning: `tool_call` is the request that produced a `tool_result` — replaying both doubles the assistant's turn weight; `error` events are diagnostics that would confuse the model if fed back; `session_end` is a marker, not a turn. A production replay path might be richer (e.g., include `tool_call` as `assistant.tool_use`); the teaching version stays minimal so the conversion table is one screen.
   **`Replay()` 故意是有损的**。只有 `user_message`、`llm_call`、`tool_result` 变成 Message。`tool_call`、`error`、`session_end` 都跳过。**理由**：`tool_call` 是产生 `tool_result` 的请求 —— 都 replay 会把 assistant 的 turn 权重翻倍；`error` 是诊断信息、喂回去会让模型迷惑；`session_end` 是标记不是 turn。生产版可能更丰富（比如把 `tool_call` 还原成 `assistant.tool_use`）；教学版保持最小、转换表能一屏看完。

4. **GetEvents order: offset → filter → limit.** The upstream phrasing is "positional slicing and filtering" and the order matters: "show me the 10 tool_result events past position 100" means *skip 100 raw events*, then filter by type, then return at most 10. If you reorder (filter first, then offset) the meaning of Offset changes from "raw position" to "filtered position" and pagination URLs break. The test `GetEventsTypeFilter` pins this — though it only exercises the simple case; the careful reader will notice that the order matters for any combined-options query.
   **GetEvents 的顺序：offset → filter → limit**。上游措辞是"positional slicing and filtering"，**顺序很重要**："给我位置 100 之后的 10 个 tool_result 事件"意味着*先跳 100 条原始事件*、再按类型过滤、再最多返回 10 条。如果调换顺序（先过滤再 offset），Offset 的语义就从"原始位置"变成"过滤后位置"，分页 URL 也就坏了。`GetEventsTypeFilter` 把这件事钉住了 —— 虽然它只测简单 case，仔细的读者会注意到任何组合查询里顺序都重要。

## What the next chapter changes / 下一节的变化

s11 introduces a `Checkpoint` — a *single* snapshot of loop state (messages, turn, metadata) written atomically via `.tmp` + `os.Rename`. s10's event log and s11's checkpoint are NOT alternatives — they're complementary patterns for different access shapes. The event log is high-volume / append-only / "everything that ever happened"; the checkpoint is low-volume / overwrite / "what's the latest state". A real harness uses both: emit events to s10 unconditionally, snapshot to s11 every N turns so resume doesn't have to replay the entire log.
s11 引入 `Checkpoint` —— loop 状态（messages、turn、metadata）的**单点快照**，用 `.tmp` + `os.Rename` 原子写入。**s10 的事件日志和 s11 的 checkpoint 不是二选一** —— 它们是访问模式不同的互补 pattern。事件日志大量、append-only、"一切发生过的事"；checkpoint 少量、覆盖写、"当前最新状态"。真实 harness 两个都用：无条件往 s10 写事件、每 N 回合 snapshot 一次到 s11，这样重启时不必 replay 整条日志。
