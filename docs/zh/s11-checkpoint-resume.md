# s11 — Checkpoint / Resume

> 原子 checkpoint 文件（`.tmp` + fsync + `os.Rename`）—— 让 crash 后的 agent 从最后一次保存的 turn 续跑，而不是从头再来。每 N 个 turn 存一次；优雅退出时清空。

## Problem

到 s10 为止已经有了事件日志 —— session 里所有有意思的事情都按 JSONL 写到了磁盘上。但是 session 不只是"事件列表"；它还包括 agentic loop 的**当前状态**：组装好的消息历史、turn 计数器、在跑的工具调用、当前激活的 skill。如果 harness 进程在任务中途死掉（内核 OOM、机器 reboot、`pkill` 误伤 `kill -9`、硬件故障），loop 变量就**跟着没了** —— 即便事件日志里完整保留了之前所有 turn，从事件**重建** loop 状态也是一个不平凡的"replay + fold"操作。

一个 50-turn 的编码任务在 38 turn 时 crash，**承受不起**把 1-37 重做一遍：每一个 turn 都是真金白银的 LLM 调用，时间和钱都耗不起。我们需要磁盘上有一份**单一权威快照**让下一个进程可以直接 boot 进去；同时还得保证写得**够耐操** —— 在 `write()` 中途断电不会把这份快照写坏。

这份快照就是 **checkpoint**。上游模式（`guide/error-handling.md` L231-L322）看起来很短、但每一步都容易做错：

- 先写到 `.tmp` 文件。
- `fsync` 让字节真的落到盘上、不只是停在内核 buffer。
- `os.Rename` 把临时文件原子地换成最终名字。
- 优雅退出时把 checkpoint 删掉，下一个任务才会从头开始。

s11 就是把这套实现出来，外加一个 toy `Loop` 来演示 resume 的交接。

## Solution

```go
store, _ := NewCheckpointStore("/var/run/agent-checkpoints")

loop := &Loop{
    Store:           store,
    Provider:        myProvider,
    MaxTurns:        50,
    CheckpointEvery: 5,   // checkpoint 节奏
}

history, err := loop.Run(ctx, "task-42", userMessage)
// 如果上一次 crash 了，history 从上一次的 checkpoint 接着跑 —— 不从头开始。
```

三条纪律：

| | 规则 |
|---|---|
| Save | `MarshalIndent → WriteFile(<tmp>) → Sync → Rename(<tmp>, <final>)`。任何一步失败，原文件原封不动。 |
| 节奏 | 每 `CheckpointEvery` turn 存一次（默认 5）。出错时**也**存一次（provider 返回 error），让重试有 resume 起点。 |
| Clear | `provider.Next` 返回 `done=true` 时把文件删了。**幂等**：再删一次"不存在"不算错。 |

`CheckpointStore` 上挂了一个小但关键的注入点：`writeFile func(...)`。默认 `nil` 走 `os.WriteFile`。测试时换成一个"写一半就报错"的函数，让我们能在不让 test runner 真 crash 的前提下验证原子 rename 这件事。

## How It Works

**Save** 是本章的关键：

```go
func (s *CheckpointStore) Save(cp *Checkpoint) error {
    s.mu.Lock(); defer s.mu.Unlock()

    data, _ := json.MarshalIndent(cp, "", "  ")
    tmp, final := s.tmpPath(cp.TaskID), s.path(cp.TaskID)

    if err := s.writeFile(tmp, data, 0o644); err != nil {
        _ = os.Remove(tmp)
        return err
    }
    if err := fsyncFile(tmp); err != nil { ... }
    return os.Rename(tmp, final)
}
```

这三步合起来给了原子契约：并发的 reader 看到的要么是旧文件（Rename 还没跑）、要么是新文件（Rename 跑完）。永远不会看到"写了一半"的中间态。**fsync 是最容易漏掉的一步** —— 没它的话 rename 可以成功，但只要在内核回写完之前机器死掉、字节就丢了。代价是每次 checkpoint 多一次 syscall，比刚跑完的 LLM 调用便宜到可以忽略。

**Load** 是 resume 的交接：

```go
func (s *CheckpointStore) Load(taskID string) (*Checkpoint, error) {
    data, err := os.ReadFile(s.path(taskID))
    if errors.Is(err, fs.ErrNotExist) {
        return nil, nil // "没有 checkpoint" —— **不是**错误
    }
    if err != nil { return nil, err }
    var cp Checkpoint
    return &cp, json.Unmarshal(data, &cp)
}
```

"缺失 → `(nil, nil)`"这个契约就是让 `LoadOrStartFresh` 写得干净的原因：nil = 全新启动、非 nil = resume、err = 立即报错。**malformed JSON 不被无声当作"全新启动"** —— 它会报错，让人来介入。`LoadMissingReturnsNilNoError` 把这个契约钉死。

**Loop.Run** 把这些串起来：

```
1. LoadOrStartFresh(taskID, userMsg)  → (history, turn, startedFresh)
2. for turn < MaxTurns:
       msg, done, err = Provider.Next(ctx, turn, history)
       if err: Store.Save(state); return err
       history = append(history, msg)
       if done: Store.Clear(taskID); return history, nil
       if (turn+1) % CheckpointEvery == 0: Store.Save(state with Turn=turn+1)
       turn++
```

"存 Turn=turn+1"这个细节**很关键**。磁盘上的 Turn 是 resume 时要执行的**下一个** turn，不是刚跑完的那个。如果存的是刚跑完的 turn，resume 会把它重做一遍 —— 助手消息已经在 history 里了，但 loop 的 `for` 还是会带着这个 turn 号调一次 provider。存 `turn+1` 让 resume 幂等。

**Test 5（resume）** 编排了整个故事：

1. Loop 配 `CheckpointEvery=5`、`PanicAtTurn=6`（loop 上的 seam，**不是** provider 的）。
2. Loop 正常跑 turn 0..5。turn 4 结束时存了 `Turn=5`。turn 5 跑完 append。turn 6 开始、provider 返回消息、loop append，**然后** `PanicAtTurn` 触发。
3. 测试 `recover()`、检查磁盘：checkpoint 是 Turn=5、len(Messages)=6。
4. 新建 provider + loop、同样的 TaskID。Run。
5. LoadOrStartFresh 返回 Turn=5 + 之前那 6 条 history。Loop 跑 script 的 5、6、7。turn 8 script 没东西、provider 报 done=true、loop 清掉 checkpoint 退出。
6. 断言：`provider2.Calls == 4`（只跑了 resume 的 turn）。`final history len == 10`。Checkpoint 文件已经没了。

## What Changed

| | s10（事件日志）| s11（checkpoint） |
|---|---|---|
| 存什么 | 所有事件 | 单一当前快照 |
| 体量 | append-only、随 session 线性增长 | 恒定大小 —— 后续 save 覆盖前面 |
| 写法 | `O_APPEND`、一行一条 JSONL | `MarshalIndent → tmp → fsync → rename` |
| 读法 | 流式 + filter | 单次 `ReadFile + Unmarshal` |
| 成功后 | 留着（审计日志）| 删掉（状态清理）|
| 主要用途 | 审计、回放、observability | crash 后 resume |

s10 和 s11 是**互补**的，不是替代关系。真实 harness 两个都要：事件日志是真相源（只要 CPU 够，**任何东西**都可以从它重建），checkpoint 是热缓存（不用 replay 就能快速 resume）。本章的 `Loop.Run` 只碰 checkpoint —— 加上事件日志的 emit 会稀释教学重点 —— 但 s_full 集成会把两者一起接上。

## Try It

```bash
cd agents/s11-checkpoint-resume
go vet ./... && go build ./... && go test -count=1 ./...
# PASS —— 6 个测试

go run .
# === s11 checkpoint/resume demo ===
# checkpoint dir: /tmp/s11-checkpoint-demo-XXXX
#
# --- Run 1: crashes after turn 6 ---
# recovered from panic: loop: induced panic at turn 6
# (run 1 crashed as expected)
# checkpoint on disk: turn=5  history_len=6  file=/tmp/.../demo-task.json
#
# --- Run 2: resumes from checkpoint ---
# run 2 completed: provider.Calls=4  history_len=10
#   → only 4 provider calls in run 2 means turns 0-4 were skipped.
# checkpoint was cleared after success — happy-path invariant holds.
#
# === final history ===
# [ 0] user      summarize the codebase
# [ 1] assistant step 0: understand the request
# ...
# [ 9] assistant task complete
```

Provider 的调用次数把 resume 故事讲清楚了：run 1 调了 7 次（0..6）、run 2 调了 4 次（5..8）。**总工作量 11 次** vs 没 checkpoint 时需要的 18 次（crash 后从头开始那种）。

## Upstream Source Reading

来源：`guide/error-handling.md` L231-L322。永久链接：<https://github.com/nexu-io/harness-engineering-guide/blob/86fec9bea430cecb29ff10afaae36b96496a8f8e/guide/error-handling.md#L231-L322>

交叉引用：`guide/long-running-harness.md` L94-L138（generator-evaluator 架构；另一种长任务模式，它**不** checkpoint 单一 loop —— evaluator 每次都在全新 context 里跑。配合本章读，可以理解 checkpoint 的**存在前提**：是给"状态必须扛过 crash"的任务用的、不是给"reset-and-retry 也能接受"的任务用的）。

```python
# guide/error-handling.md L240-L274（原子 Checkpoint 类）
class Checkpoint:
    """Save and restore agent progress for long-running tasks."""

    def __init__(self, checkpoint_dir: str = "/tmp/agent-checkpoints"):
        self.checkpoint_dir = checkpoint_dir
        os.makedirs(checkpoint_dir, exist_ok=True)

    def save(self, task_id: str, state: dict):
        """Save current progress."""
        checkpoint = {
            "task_id": task_id,
            "timestamp": datetime.now().isoformat(),
            "state": state,
        }
        path = os.path.join(self.checkpoint_dir, f"{task_id}.json")
        # Write atomically (write to temp, then rename)
        tmp_path = path + ".tmp"
        with open(tmp_path, "w") as f:
            json.dump(checkpoint, f, indent=2)
        os.rename(tmp_path, path)

    def load(self, task_id: str) -> dict | None:
        """Load the last checkpoint for a task."""
        path = os.path.join(self.checkpoint_dir, f"{task_id}.json")
        if not os.path.exists(path):
            return None
        with open(path) as f:
            return json.load(f)["state"]

    def clear(self, task_id: str):
        """Remove checkpoint after task completes."""
        path = os.path.join(self.checkpoint_dir, f"{task_id}.json")
        if os.path.exists(path):
            os.unlink(path)
```

阅读笔记：

- **上游 Python 的 `state: dict` 是 freeform 口袋；我们把它解包成类型化字段**。Go 喜欢具体形状。我们把 `Turn`、`Messages`、`Metadata` 暴露成显式 struct 字段 —— 磁盘上的 JSON 整体形状一样（`{task_id, turn, messages, metadata, timestamp}`），但 Go reader 拿到的是编译期字段访问、不是 `state["turn"].(float64)`。
- **上游跳过了 fsync；我们加上**。Python 的 `os.rename` 在 POSIX 上保证目录条目交换的原子性，但**不**保证临时文件内容已经刷到持久存储。crash 在 `f.write` 和内核回写之间发生就会发布一个 0 字节文件。在 write 之后加 `f.Sync()` 把这个洞堵上。一点点额外成本、换来巨大的鲁棒性。
- **`load()` 文件缺失时返回 `None` —— 跟我们的 `(nil, nil)` 一回事**。Python 用 `if not os.path.exists(...)` 做流控；Go 用 `errors.Is(err, fs.ErrNotExist)` 在 `ReadFile` 之后。语义一样、惯用法不同。"缺失 checkpoint 不算错误"这个契约是上游的，我们照搬。
- **上游 checkpoint 节奏是 `if turn % 5 == 0`（L307-L308）；我们用 `(turn+1) % N`**。微妙：Python 在 turn-0、turn-5、turn-10... **开始**时存，但只有在 turn 0 已经跑完之后。我们的 `(turn+1) % N` 在 turn body 结束后判断，对 N=5 来说触发点完全一样（在 turn 4、9、14... 结束时存），但概念上更清楚："我们刚跑完一个 turn、其编号 +1 是 N 的倍数"。两种读法都对；调用方真正依赖的是磁盘上的 `Turn` 字段。
- **L278-L322 的 `agentic_loop_with_checkpoint` 是 resume 模式、不是一个独立组件**。有意思的代码不是 `Checkpoint` 类本身 —— 而是 L281-L288 的 `if saved_state: ... else: ...` 交接。我们的 `LoadOrStartFresh` 是同一个想法、做成方法让测试不用起一整个 Loop 就能验证它。

阅读地图：

| 主题 | 上游文件 | 行号 | 对应章节 |
|------|----------|------|----------|
| Checkpoint 类 | `guide/error-handling.md` | L240-L274 | s11（本章）|
| Loop 中的 resume 模式 | `guide/error-handling.md` | L278-L322 | s11 |
| 原子写注释 | `guide/error-handling.md` | L324 | s11 |
| Generator-evaluator（替代模式）| `guide/long-running-harness.md` | L94-L138 | 附录 A + s11 交叉引用 |
| 长任务心智模型 | `guide/long-running-harness.md` | L19-L92 | 附录 A |
| 事件日志（互补）| `guide/managed-agents-architecture.md` | L74-L112 | s10 |
