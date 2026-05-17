# s05 — 记忆层

> 用 ~150 行 Go 实现跨 session 持久化：精炼版 `MEMORY.md` + 仅追加的每日 log。不调 LLM、不走网络。

## Problem

s01-s04 之后，harness 已经能跑循环、选模型、分发工具、组装 context。但每次 session 都是空白的。用户 14:00 提到一个偏好，1 小时后 agent 已经忘了。这是结构性 bug：循环只在一次运行内持有状态，跑完就死。

我们想要的是 `guide/memory-and-context.md` L78 描述的属性：

> 凡是需要跨重启保留的东西，都应该写到 memory 文件里，而不是留在 session state 里。

我们需要一个小型的、文件系统支撑的记忆层 —— session 启动时 `Read()`，运行期间 `AppendLog()`，并且**两个 goroutine 同时 append 时永不丢写**。

## Solution

上游 guide（L82-L125）规定了两层架构，我们直接照搬：

| 层 | 文件 | 写入纪律 |
|----|------|----------|
| 长期 | `<baseDir>/MEMORY.md` | 精炼版。人或 agent 周期性更新。 |
| 每日 log | `<baseDir>/memory/YYYY-MM-DD.md` | 仅追加。廉价。一天一个文件。 |

Go API 是 `Memory` 上的四个操作：

```go
mem, _ := New("/var/agent/memory", RealClock{})
view, _ := mem.Read()             // 长期 + 今天 + 昨天
mem.AppendLog("## 14:30 — 重构 auth")
mem.RotateOlderThan(30)           // 删除 30 天前的每日 log
```

就这些。没有 DB、没有 SQL、没有 embedded 索引。

## How It Works

Read 是 `guide/memory-and-context.md` L130-L143 那段 Python 的直译：

```
sections = []
1. 如果 MEMORY.md 存在            → append 内容
2. for daysAgo in [0, 1]:
     如果 memory/<那天>.md 存在   → append 内容
3. return strings.Join(sections, "\n---\n")
```

两天前的 log 故意被排除。上游 L138 选 `[0, 1]`，理由是：今天 session 自己就记得；昨天是最值得回顾的；更早的要么已经被提炼到 `MEMORY.md`，要么不值得这么多 token。

Append 是每次调用一个 `O_APPEND|O_CREATE` 的 open，外面套 mutex：

```go
func (m *Memory) AppendLog(entry string) error {
    m.mu.Lock(); defer m.mu.Unlock()

    date := m.clock.Now().Format("2006-01-02")
    path := filepath.Join(m.baseDir, "memory", date+".md")
    f, _ := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
    defer f.Close()
    _, err := f.WriteString(entry + "\n")
    return err
}
```

三个要点：

1. **为什么 `O_APPEND` + mutex 都要？** POSIX 上一个 `O_APPEND` fd 的小于 `PIPE_BUF` 的 `write()` 对其他 appender 是原子的 —— 不会交错。mutex 是双保险：让契约在 `O_APPEND` 语义更弱的平台依然成立，也防御未来某条改成 truncate-rewrite 的代码路径。
2. **结尾的换行必须有**。没有的话，两次 append 会粘成一行，整个 log 就废了。`TestMemory_AppendIsAtomicAcrossWriters` 会立刻挂。
3. **`Clock` 是唯一的 seam**。文件名依赖 `time.Now()`。测试注入 `FakeClock{T: …}`，`main` 里传 `RealClock{}`。

Rotation 删除比 cutoff 旧的文件。`RotateOlderThan(7)` 的规则是：保留今天和之前 6 天（一共 7 个文件），第 7 天及更旧的删掉。解析 `YYYY-MM-DD.md`，和 `now-N`（截到日界）比较，剩下的 `os.Remove`。`MEMORY.md` 在 `<baseDir>` 根目录、不在 `<baseDir>/memory/` 里，所以结构上就在 rotation 循环之外 —— 永远不会被删。

## What Changed

| | s04（assembler）| s05（memory） |
|---|---|---|
| 生命周期 | 一次 LLM 调用 | 跨多次运行 |
| 存储 | 内存里的 `[]ContextSection` | 磁盘文件 |
| 并发 | 单 goroutine | 多写者、一个快照 |
| Token 预算 | 是 | 否（交给调用方） |

s04 给一次 LLM 调用挑选 section；s05 产出**一个 section**（记忆快照），让 assembler-style 的调用方按 `priority=3` 塞进去。两者在 `s_full` 里干净地组合。本章不 import s04 的代码 —— 这是课程的章节隔离规则。

## Try It

```bash
cd agents/s05-memory-layer
go test -count=1 ./... -race
# PASS —— 5 个测试，含 TestMemory_AppendIsAtomicAcrossWriters
# （50 goroutine 并发追加，全部落盘、不交错）

go run .
# === combined memory view (long-term + today + yesterday) ===
# # Long-term Memory
#
# ## User Preferences
# - Prefers explicit error messages
#
# ---
# ## 09:00 — Started session
# - Reviewed inbox, no urgent items
# ## 10:30 — Refactored memory layer
```

`-race` 是必须的：原子追加测试就是为了发现"未来某次 commit 把 mutex 删了"这种回归 —— 最廉价的方式就是 race detector。

## Upstream Source Reading

来源：`guide/memory-and-context.md` L80-L144。永久链接：<https://github.com/nexu-io/harness-engineering-guide/blob/86fec9bea430cecb29ff10afaae36b96496a8f8e/guide/memory-and-context.md#L80-L144>

```python
# guide/memory-and-context.md L129-L143
def session_startup(memory_dir: str) -> str:
    """Read memory at session start."""
    sections = []
    # Always read long-term memory
    memory_path = os.path.join(memory_dir, "MEMORY.md")
    if os.path.exists(memory_path):
        sections.append(open(memory_path).read())
    # Read recent daily logs (today + yesterday)
    for days_ago in [0, 1]:
        date = (datetime.now() - timedelta(days=days_ago)).strftime("%Y-%m-%d")
        daily_path = os.path.join(memory_dir, f"memory/{date}.md")
        if os.path.exists(daily_path):
            sections.append(open(daily_path).read())
    return "\n---\n".join(sections)
```

阅读笔记：

- **`[0, 1]` 这个窗口是设计选择，不是常量**。guide 取了最小非平凡的窗口。我们硬编码同样的值；一个扩展练习是把它做成参数，让用户在 agent 闲置较久时往前读 3 天。
- **`os.path.exists` 是"静默跳过"**。Go 版用 `os.IsNotExist(err)` 复现同样的形状：文件不存在不报错、只是不 append；其他读错误（权限、IO）则上抛 —— Python 那边偷懒，会在更下游崩。
- **Python 里没有锁**。上游草图是单线程的。Go harness 一旦有并发工具调用 + LLM 流式 token，`AppendLog` 上就很容易 race，我们一开始就放了 mutex。
- **`MEMORY.md` 的"精炼/curation"不在本章**。guide L104 写 "Updated periodically (not every session)"。**何时**精炼、**谁**精炼，是策略问题；本章只交付存储原语。
- **我们比上游多做的事**。`RotateOlderThan` guide 里没有。真实 harness 里没人删的每日 log 第一周就会撑爆磁盘；30 行加一个删除，第一周就值回票价。

阅读地图：

| 主题 | 上游文件 | 行号 | 对应章节 |
|------|----------|------|----------|
| 两层架构 | `guide/memory-and-context.md` | L80-L125 | s05（本章） |
| 读取流程 | `guide/memory-and-context.md` | L127-L144 | s05 |
| Session 生命周期 | `guide/memory-and-context.md` | L62-L78 | s10 |
| AGENTS.md（行为，和 memory 不同） | `guide/memory-and-context.md` | L146-L220 |（不在课程范围） |
