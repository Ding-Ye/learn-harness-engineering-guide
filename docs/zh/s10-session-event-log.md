# s10 — Session 事件日志

> Append-only JSONL 日志：每个 session 一个文件、每行一个 JSON 事件。**Session 比 harness 命长** —— harness 崩了文件还在，`Replay()` 能从中重建消息历史。

## Problem

到目前为止每一章都默认 "harness == session"。当 s01 的 loop 退出、s07 的重试放弃、s09 的窗口扔掉 pre-window 消息 —— 内存里 `[]Message` 切片之外的所有东西都消失了。上游架构文档把这点钉为**结构性缺陷**：harness 是一个进程，session 是用户的工作，把它们的生命周期绑在一起就意味着 harness 崩 = 数据丢。`guide/managed-agents-architecture.md` L74-L83：

> Session 是**所有发生过的事的 append-only 日志**：LLM 调用、工具结果、user 消息、系统事件。它住在 harness 和 sandbox **外面**的持久存储里。

同一份文件 L91-L112 还要强调第二个概念：**session ≠ context window**。Session 是**完整记录** —— 可能几百万 token、跨越几小时；context window 是**每次 LLM 调用挑出来的子集**。s09 管子集；s10 管记录。

## Solution

一个 `SessionStore` 接口（三个方法：`EmitEvent` / `GetEvents` / `Close`）+ 一个具体实现 `FileStore`，写到：

```
<dir>/sessions/<sessionID>.jsonl
```

一行一个 `Event`。事件带 Timestamp、SessionID、Type 和不透明的 `Data json.RawMessage`。Type 是开放式的；s10 给了 6 个跟上游对齐的规范类型：

```go
EventUserMessage  = "user_message"
EventLLMCall      = "llm_call"
EventToolCall     = "tool_call"
EventToolResult   = "tool_result"
EventError        = "error"
EventSessionEnd   = "session_end"
```

`GetEvents` 接受 `GetEventsOpts{Offset, Limit, TypeFilter}`，**按这个顺序**应用（跳 → 过滤 → 截断）。**顺序重要**："给我位置 100 之后的 10 个 tool_result 事件" 等于 `Offset=100, TypeFilter=[tool_result], Limit=10`，语义是"先跳 100 条**原始**事件、再过滤"。

`Replay(store, sessionID) → []Message` 从事件日志重建扁平消息历史。**转换表故意保持很小**：

| 事件类型 | 还原成 |
|---|---|
| `user_message` | `Message{Role: "user", Content: data.text}` |
| `llm_call` | `Message{Role: "assistant", Content: data.text}` |
| `tool_result` | `Message{Role: "tool", Content: data.output}` |
| `tool_call`, `error`, `session_end`, 其它 | 跳过 |

## How It Works

`EmitEvent` 是热路径。它 marshal 成 JSON、追加 `\n`、上 mutex、打开（或从缓存拿）文件（`O_APPEND|O_CREATE|O_WRONLY`）、做一次 `Write`。在 open + write 全程持有 mutex 是为了保证 FD 缓存一致 + 处理"Close 之后再 emit"这种情况。**文件写本身在 POSIX 上并不严格需要 mutex**（`O_APPEND` 写一次 < `PIPE_BUF` (~ 4 KiB) 时对其他 `O_APPEND` 写者是内核原子的），但我们仍然持锁 —— 因为还要改 `openFiles` 和 `closed`，一把锁比两把好推理。

`openFiles` 缓存活到 store 销毁为止。教学实现**不**做 idle 驱逐 —— `Close()` 一次性释放所有 FD。生产版会按 TTL 驱逐以防 FD 数失控。

`GetEvents` 是冷路径。**每次重新打开文件**，行扫描（buffer 提到 1 MiB/行，bufio.Scanner 默认 64 KiB 会截断大的工具结果），每行 JSON 解码，再应用 Offset / TypeFilter / Limit。**故意不维护内存索引**：访问模式是"读很少（debug、resume、看板）、写很多"，最简单正确的实现就是每次重读。

`Replay` 读全部事件、按 Timestamp 稳定排序（保留 emit 顺序的 tie）、然后应用上表。**排序是防御性的** —— 文件通常已经是单调时间序（emit 是顺序的），但未来如果有并发 sub-agent 往同一 store 写，会出现乱序，读时统一一次更省心。

Replay 跳过 `tool_call` 是**故意的**。`tool_call` 是 assistant **请求**调用工具；接下来的 `tool_result` 才携带"下一次 LLM 调用要看见的可观测文本"。两个都 replay 会把 assistant 的 turn 权重翻倍、让后续模型迷惑。更丰富的 Replay（把配对还原成 `assistant.tool_use` + `tool.tool_result`）是合理的扩展；教学版保持最小、转换表一屏看完。

## What Changed

| | s09（压缩） | s10（事件日志） |
|---|---|---|
| 生命周期 | 单次 harness 跑 | 跨 harness 崩溃 |
| 存储 | 内存 `[]Message` | 磁盘 JSONL 文件 |
| 修改 | 触发阈值时改写历史 | append-only、永不改 |
| 每次调用 API | `GetMessages()` 返回当前视图 | `GetEvents()` 返回按位置切片的日志 |
| 概念 | "模型此刻看到的内容" | "这个 session 里曾经发生的所有事" |

**s09 和 s10 不是二选一**。上游图（`managed-agents-architecture.md` L94-L112）描述的体系是：harness 无条件往 s10 `EmitEvent`；每次 LLM 调用读回一段 `GetEvents`、再让 s09 的滑动窗口在这段上跑、产出 context window。**Session 是持久真相、Window 是派生视图**。

## Try It

```bash
cd agents/s10-session-event-log
go vet ./... && go build ./... && go test -count=1 -race ./...
# PASS —— 5 个测试，-race 下 ~1.6s

go run .
# === writing 6 events to /tmp/s10-XXXX/sessions/demo-001.jsonl ===
#   emit user_message
#   emit llm_call
#   emit tool_call
#   emit tool_result
#   emit llm_call
#   emit session_end
#
# === GetEvents(opts={}) — full log ===
#   [0] 2026-05-17T12:00:00Z  type=user_message  data={"text":"Read data.json and summarize it"}
#   [1] 2026-05-17T12:00:01Z  type=llm_call      data={"text":"I'll read the file first."}
#   [2] 2026-05-17T12:00:02Z  type=tool_call     data={"args":{"path":"data.json"},"name":"read_file"}
#   [3] 2026-05-17T12:00:03Z  type=tool_result   data={"output":"{\"name\":\"Ada\",\"score\":42}"}
#   [4] 2026-05-17T12:00:04Z  type=llm_call      data={"text":"The file contains Ada (score 42). Task complete."}
#   [5] 2026-05-17T12:00:05Z  type=session_end   data={"reason":"end_turn"}
#
# === Replay — reconstructed message history (4 messages) ===
#   [0] role=user      content="Read data.json and summarize it"
#   [1] role=assistant content="I'll read the file first."
#   [2] role=tool      content="{\"name\":\"Ada\",\"score\":42}"
#   [3] role=assistant content="The file contains Ada (score 42). Task complete."
```

在另一个终端 `tail -f` 这个 JSONL 文件、再重新跑 demo —— **真实 harness 的整套可观测性故事**就是这 100 行 Go。

## Upstream Source Reading

来源：`guide/managed-agents-architecture.md` L74-L112。永久链接：<https://github.com/nexu-io/harness-engineering-guide/blob/86fec9bea430cecb29ff10afaae36b96496a8f8e/guide/managed-agents-architecture.md#L74-L112>

交叉引用：`guide/memory-and-context.md` L62-L78 解释**什么时候**需要持久 session（per-task / per-conversation / persistent）以及**持久化是什么意思**（"把 session state 写到磁盘以便恢复"）。s10 就是这份契约的"写磁盘"那一半。

```markdown
### Session (Event Log)

Session 是所有发生过的事的 append-only 日志：LLM 调用、工具结果、user 消息、
系统事件。它住在 harness 和 sandbox 外面的持久存储里。

关键接口：
- emitEvent(sessionId, event) —— 在 agent loop 中写一个事件
- getEvents(sessionId, options) —— 读事件回来（位置切片 + 过滤）
- getSession(sessionId) —— 拿元数据和状态

Session 比 harness 和 sandbox 都命长。任一崩了，session 还在。

## Session ≠ Context Window

这是整个架构最微妙、也最重要的区分。

Session 是所有事情的完整、持久记录 —— 可能几百万 token、跨越几小时甚至几天的
agent 工作。Context window 是 harness 为当前这一次 LLM 调用从那份记录里选出来的
子集 —— 通常 128K-200K token。

Session (append-only event log, durable)
┌─────────────────────────────────────────────────────────────┐
│ event_1 │ event_2 │ ... │ event_500 │ ... │ event_2000     │
└─────────────────────────────────────────────────────────────┘
                                    │
                          getEvents(slice)
                                    │
                                    ▼
                    Context Window (selected subset)
```

阅读笔记：

- **"Append-only" 是 load-bearing 的关键词**。它排除了修改、删除、重排序。Go 实现用 `O_APPEND|O_CREATE|O_WRONLY` 打开、永不 seek —— **位置单调由 OS 保证**。
- **`emitEvent` 从 loop 的角度看是 fire-and-forget**。签名是 `(sessionId, event) → void`（Go：`→ error`）；loop **不**等分析、不等复制。未来引入异步 flush 会把 crash 安全的故事复杂化，我们不做。
- **L94-L112 的 "Session ≠ Context Window" 图是核心心智模型**。s10 实现的是图里 *Session* 那一行；s09 的滑动窗口实现的是 *Context Window* 那一行外加 `getEvents(slice)` 那个箭头。**两章一起读才看到完整图**。
- **`getEvents` 是"位置切片"、不是"按 timestamp 查询"**。Append-only 格式唯一能保证的不变量就是**位置**；timestamp 在并发 emit 下可能冲突或微小乱序。我们的 `GetEventsOpts{Offset, Limit, TypeFilter}` 反映这点 —— **offset 按位置、filter 按类型，没有 timestamp range**。
- **`memory-and-context.md` L62-L78 是"什么时候"**。"per-task" session 每次用户请求后清掉；"per-conversation" 跨轮持久；"persistent" 跨进程重启。**三种策略都需要 s10 这个存储机制** —— 只是删除策略不同。

阅读地图：

| 主题 | 上游文件 | 行号 | 对应章节 |
|------|----------|------|----------|
| Session 作为事件日志 | `guide/managed-agents-architecture.md` | L74-L83 | s10（本章） |
| Session ≠ context window | `guide/managed-agents-architecture.md` | L91-L112 | s10 + s09 交叉引用 |
| 持久 session / 序列化 | `guide/memory-and-context.md` | L62-L78 | s10 + s05 交叉引用 |
| Replay 恢复（单快照替代） | `guide/error-handling.md` | L231-L322 | s11 |
| 日志上的滑动窗口 | `guide/context-engineering.md` | L194-L238 | s09 |
