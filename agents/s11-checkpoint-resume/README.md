# s11-checkpoint-resume

> Atomic checkpoint files (`.tmp` + `os.Rename` + `fsync`) so a crashed agent can pick up at the last saved turn instead of starting over. Save every N turns; clear on success.
> 原子 checkpoint 文件（`.tmp` + `os.Rename` + `fsync`）—— 让 crash 后的 agent 从最后一次保存的 turn 续跑，而不是从头再来。每 N 个 turn 存一次；成功后清空。

## Scope / 范围

Implement the checkpoint pattern from `guide/error-handling.md` L231-L322 in ~350 lines of Go. The atomic-write contract (write to `.tmp`, fsync, rename) is the chapter's keystone — five tests pin it from different angles. Resume is wired through a toy `Loop` driven by a `MockProvider`; the loop has a `panicAtTurn` seam so tests can force a crash AFTER the last cadence save and verify resume picks up at the right turn.

用 ~350 行 Go 实现 `guide/error-handling.md` L231-L322 的 checkpoint 模式。原子写契约（写 `.tmp`、fsync、rename）是本章的核心 —— 五个测试从不同角度把它钉死。Resume 通过一个 toy `Loop`（由 `MockProvider` 驱动）来验证；loop 上挂了 `panicAtTurn` 钩子，让测试能在最近一次 cadence save 之后强行 crash，然后验证 resume 是从正确的 turn 续跑。

## Files / 文件

```
types.go             Local Message + Provider interface + MockProvider (with PanicAtTurn / FailAtTurn seams)
checkpoint.go        Checkpoint struct + CheckpointStore.{Save,Load,Clear} with atomic rename
loop.go              Loop.{LoadOrStartFresh,Run} — checkpoints every 5 turns, clears on success
main.go              CLI demo: panic at turn 6 → resume → finish
checkpoint_test.go   6 tests (round-trip / atomic-write / missing-returns-nil / resume / clear / on-disk JSON valid)
```

## Run / 运行

```bash
cd agents/s11-checkpoint-resume
go run .
# Run 1 panics at turn 6 (after the turn-5 cadence save).
# Run 2 (same TaskID) resumes from turn 5 and finishes.
# Output shows provider.Calls=4 in run 2 (turns 5,6,7 plus the "done" call),
# proving turns 0-4 were skipped.
```

## Test / 测试

```bash
go vet ./... && go build ./... && go test -count=1 ./...
# PASS — 6 tests
```

## Key teaching points / 教学要点

1. **Atomic write = temp + fsync + rename.** `os.WriteFile` alone is NOT atomic — a process killed mid-write leaves a half-written file. The three-step dance (write to `<path>.tmp`, `f.Sync()` to flush kernel buffers, then `os.Rename` to swap the final name) is what makes the next reader see EITHER the old contents OR the new contents, never half. `TestCheckpoint_AtomicWriteSurvivesPartialWrite` injects a faulty writer mid-step-2 and verifies the final file is unchanged.
   **原子写 = 临时文件 + fsync + rename**。`os.WriteFile` 本身**不是**原子的 —— 写到一半被 kill 会留下半成品。三步法（写到 `<path>.tmp`、`f.Sync()` 让内核 buffer 落盘、再 `os.Rename` 换名）保证下一个 reader 看到的要么是旧内容、要么是新内容，绝不会半新半旧。`TestCheckpoint_AtomicWriteSurvivesPartialWrite` 在第二步注入失败、验证最终文件**没**变。

2. **Missing-checkpoint is `(nil, nil)`, not `error`.** Distinguishing "task never started" from "checkpoint exists but is broken" matters for the resume flow. We use the former case as the LoadOrStartFresh hand-off; a malformed JSON file should NOT silently start over — it should error so a human can investigate. The test `LoadMissingReturnsNilNoError` pins the contract.
   **缺失 checkpoint 是 `(nil, nil)`、不是 `error`**。区分"任务从没开始过"和"checkpoint 存在但坏了"对 resume 流程是关键的。前者是 LoadOrStartFresh 的正常分支；malformed JSON **不应该**被无声地当作"从头开始"—— 必须报错让人介入。`LoadMissingReturnsNilNoError` 把契约钉死。

3. **Save AFTER the turn completes, store turn+1.** The cadence check at end of turn N writes `Turn=N+1` so resume starts past the completed work. If we stored `Turn=N` we'd re-execute N — confusing the model (state already mentions it as "done") and burning tokens. `TestLoop_ResumesFromCheckpoint` asserts the second provider's `Calls=4` for an 8-turn script with crash at turn 6 and checkpoint at turn 5.
   **在 turn 完成**之后**保存、存 turn+1**。第 N turn 结束的 cadence 检查写 `Turn=N+1`，让 resume 从已完成的工作之**后**起跑。如果存 `Turn=N`，会把 N 重做一遍 —— 模型懵了（state 里已经记着这是"done"）、token 也白烧。`TestLoop_ResumesFromCheckpoint` 用"8-turn 脚本、turn 6 crash、turn 5 checkpoint"的场景断言第二个 provider 的 `Calls=4`。

4. **Clear on success makes resume idempotent.** After a graceful exit the file must be GONE — otherwise the next task with the same ID would inherit stale state. `Clear` is also idempotent ("file already missing = no error") so cleanup code can call it unconditionally. `TestCheckpoint_ClearAfterSuccess` pre-populates a checkpoint and verifies the loop deletes it on graceful completion.
   **成功后 Clear 让 resume 幂等**。优雅退出后文件必须**消失** —— 否则下一个同 ID 的任务会拿到陈旧状态。`Clear` 本身也幂等（"文件已经没了 = 不算错误"）让清理代码可以无脑调用。`TestCheckpoint_ClearAfterSuccess` 预先放一个 checkpoint，跑完后验证 loop 把它删了。

5. **`writeFile` is an injectable seam, not a global.** To test atomic semantics we need to simulate a partial-write failure. Patching the package-level `os.WriteFile` is messy (goroutine racing in CI). A field on the store, defaulted to `nil → os.WriteFile`, lets the test substitute exactly one operation without touching globals. The test sets it back to nil before subsequent reads.
   **`writeFile` 是注入点、不是全局替换**。要测原子语义就需要模拟"写一半就失败"。Patch 包级别的 `os.WriteFile` 在 CI 的并发环境里非常脏。把字段挂在 store 上、默认 `nil → os.WriteFile`，让测试**只**替换一次操作，不动全局变量。测试用完恢复 nil。

## What the next chapter changes / 下一节的变化

s12 spawns **sub-agents** as separate Go processes via `os/exec`. Each child has its own context window and its own checkpoint file (if it uses s11). Parent and child communicate via file IPC (`TASK.md` / `RESULT.json`), not via shared memory — the same atomic-rename trick from s11 applies to those files. s11 is the prerequisite mental model: "if it lives on disk and must survive a crash, write tmp → fsync → rename."

s12 用 `os/exec` 把**子 agent** 拉成独立进程。每个子进程有自己的 context window、也可以有自己的 checkpoint 文件（如果它用了 s11）。父子通过文件 IPC（`TASK.md` / `RESULT.json`）通信、不是共享内存 —— s11 这一套原子 rename 同样适用。s11 是 s12 的前置心智模型："凡是落盘、并且要扛 crash 的东西，都走 tmp → fsync → rename。"
