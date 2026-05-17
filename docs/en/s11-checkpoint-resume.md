# s11 — Checkpoint / Resume

> Atomic checkpoint files (`.tmp` + fsync + `os.Rename`) so a crashed agent resumes at the last saved turn instead of starting over. Save every N turns; clear on graceful exit.

## Problem

By s10 we have an event log — every interesting thing that happens during a session lands on disk as a JSONL line. But a session is more than a list of events; it's also the *current* state of the agentic loop: the assembled message history, the turn counter, the in-flight tool execution, the active skill. If the harness process dies mid-task (kernel OOM, host reboot, `kill -9` from a runaway `pkill`, hardware fault), the loop variable goes with it — and even though every prior turn is preserved in the event log, *reconstructing* the loop state from events is a non-trivial replay-and-fold operation.

A 50-turn coding task that crashes at turn 38 can't afford to redo turns 1-37: each turn is a real LLM call costing real money and real time. We need a *single canonical snapshot* on disk that the next process can boot directly into, plus the discipline to write that snapshot durably so a power failure mid-`write()` doesn't corrupt it.

That snapshot is the **checkpoint**. The upstream pattern (`guide/error-handling.md` L231-L322) is small but tricky to get right:

- Write to a `.tmp` file first.
- `fsync` so the bytes are on the platter, not just in the kernel buffer.
- `os.Rename` to swap the temp file into the final name atomically.
- On graceful exit, remove the checkpoint file so the next task starts fresh.

s11 implements exactly that, plus a toy `Loop` to demonstrate the resume hand-off.

## Solution

```go
store, _ := NewCheckpointStore("/var/run/agent-checkpoints")

loop := &Loop{
    Store:           store,
    Provider:        myProvider,
    MaxTurns:        50,
    CheckpointEvery: 5,   // checkpoint cadence
}

history, err := loop.Run(ctx, "task-42", userMessage)
// If the process crashed last time, history starts from the previous turn's
// checkpoint — not from scratch.
```

Three discipline rules:

| | Rule |
|---|---|
| Save | `MarshalIndent → WriteFile(<tmp>) → Sync → Rename(<tmp>, <final>)`. Each step's failure leaves the previous good file untouched. |
| Cadence | Save every `CheckpointEvery` turns (default 5). Save again on graceful exit failure (e.g. provider returns error) so the retry has somewhere to resume from. |
| Clear | On `provider.Next` returning `done=true`, delete the file. Idempotent: re-deleting a missing file is not an error. |

The `CheckpointStore` has a small but load-bearing seam: `writeFile func(...)`. Default `nil` resolves to `os.WriteFile`. Tests substitute a function that simulates a partial write so we can verify the atomic-rename invariant without crashing the test runner.

## How It Works

**Save** is the chapter's keystone:

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

The three operations together give the atomic contract: a concurrent reader sees *either* the old file (if Rename hasn't fired) *or* the new file (after Rename returns). It never sees a half-written intermediate. The fsync is the easy step to forget — without it, the rename succeeds but the kernel can lose the bytes if the host dies before the writeback flushes. Cost: one syscall per checkpoint, negligible compared to the LLM call we just made.

**Load** is the resume hand-off:

```go
func (s *CheckpointStore) Load(taskID string) (*Checkpoint, error) {
    data, err := os.ReadFile(s.path(taskID))
    if errors.Is(err, fs.ErrNotExist) {
        return nil, nil // "no checkpoint" — NOT an error
    }
    if err != nil { return nil, err }
    var cp Checkpoint
    return &cp, json.Unmarshal(data, &cp)
}
```

The `(nil, nil)` for missing is the contract that makes `LoadOrStartFresh` clean: nil = fresh start, non-nil = resume, error = bail out. A malformed JSON file is NOT silently treated as "fresh start" — it errors so a human can investigate. The test `LoadMissingReturnsNilNoError` pins this.

**Loop.Run** ties it together:

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

The "save Turn=turn+1" detail is load-bearing. The on-disk Turn is the *next* turn to execute on resume, not the most recent one. If we stored the just-completed turn the resumed loop would re-run it — the assistant message would already be in history but the loop's `for` would call the provider with that same turn anyway. Storing `turn+1` makes resume idempotent.

**Test 5 (resume)** orchestrates the whole story:

1. Loop with `CheckpointEvery=5` and `PanicAtTurn=6` (a Loop-level seam, NOT the provider's).
2. Loop runs turns 0..5 normally. At end of turn 4 it saves `Turn=5`. Turn 5 runs and appends. Turn 6 starts, the provider returns its message, the loop appends, and *then* the `PanicAtTurn` seam fires.
3. We `recover()`, inspect the disk: checkpoint has Turn=5, len(Messages)=6.
4. Build a fresh provider and loop with the same TaskID. Run.
5. LoadOrStartFresh returns Turn=5 and the saved 6-message history. The loop runs turns 5,6,7 from the script. Turn 8 has no script entry, the provider reports done=true, the loop clears the checkpoint and returns.
6. Assert: `provider2.Calls == 4` (only the resume turns ran). `final history len == 10`. Checkpoint file no longer exists.

## What Changed

| | s10 (event log) | s11 (checkpoint) |
|---|---|---|
| What's stored | Every event, ever | Single current snapshot |
| Volume | Append-only, grows linearly with session | Constant size — last save overwrites prior |
| Write pattern | `O_APPEND`, one JSONL line per call | `MarshalIndent → tmp → fsync → rename` |
| Read pattern | Stream and filter | Single `ReadFile + Unmarshal` |
| On success | Keep (audit log) | Delete (state cleanup) |
| Primary use | Audit, replay, observability | Resume after crash |

s10 and s11 are complementary, not alternatives. A real harness uses both: the event log is the source of truth (you can rebuild ANYTHING from it given enough CPU), and the checkpoint is the warm cache (fast resume without replay). The chapter's `Loop.Run` only touches the checkpoint because adding event-log emission would dilute the lesson — but the s_full integration narrates both being wired together.

## Try It

```bash
cd agents/s11-checkpoint-resume
go vet ./... && go build ./... && go test -count=1 ./...
# PASS — 6 tests

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

The provider call count tells the resume story: run 1 used 7 calls (0..6), run 2 used 4 (5..8). Total work: 11 provider calls vs the 18 a no-checkpoint world would need (if you naively re-ran from the start after crash).

## Upstream Source Reading

Source: `guide/error-handling.md` L231-L322. Permalink: <https://github.com/nexu-io/harness-engineering-guide/blob/86fec9bea430cecb29ff10afaae36b96496a8f8e/guide/error-handling.md#L231-L322>

Cross-reference: `guide/long-running-harness.md` L94-L138 (generator-evaluator architecture; a different pattern for long-running tasks that DOESN'T checkpoint a single loop — the evaluator runs in a fresh context each iteration. Read alongside to understand WHY checkpoints exist: they're for tasks whose state must survive crash, not for tasks where reset-and-retry is acceptable).

```python
# guide/error-handling.md L240-L274 (atomic Checkpoint class)
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

Reading notes:

- **The upstream Python `state: dict` is a freeform bucket; we unpack it into typed fields.** Go prefers concrete shapes. We surface `Turn`, `Messages`, and `Metadata` as explicit struct fields — the on-disk JSON ends up with the same overall shape (`{task_id, turn, messages, metadata, timestamp}`) but the Go reader gets compile-time field access instead of `state["turn"].(float64)`.
- **The upstream skips fsync; we add it.** Python's `os.rename` semantics on POSIX guarantee atomicity of the directory entry swap, but NOT that the temp file's content has been flushed to durable storage. A crash between `f.write` and the kernel's writeback can publish a zero-byte file. Adding `f.Sync()` after the write closes that hole. Tiny extra cost, big robustness win.
- **`load()` returns `None` when the file is missing — same as our `(nil, nil)`.** The Python code uses `if not os.path.exists(...)` as a control-flow check; Go uses `errors.Is(err, fs.ErrNotExist)` after `ReadFile`. Same semantics, different idiom. The contract that "missing checkpoint is not an error" is the upstream's, and we keep it.
- **The upstream checkpoint cadence is `if turn % 5 == 0` (L307-L308); we use `(turn+1) % N`.** Subtle: the Python code saves at the START of turn-0, turn-5, turn-10, etc. — but only AFTER turn 0 actually completed. Our `(turn+1) % N` after the turn body completes is mathematically the same trigger for N=5 (saves at end of turn 4, 9, 14...) but conceptually clearer: we save when we've JUST finished a turn whose number is one less than a multiple of N. Either reading is fine; the on-disk `Turn` field is what callers actually depend on.
- **`agentic_loop_with_checkpoint` on L278-L322 is the resume pattern, not a separate component.** The interesting code isn't the `Checkpoint` class — it's the `if saved_state: ... else: ...` hand-off at L281-L288. Our `LoadOrStartFresh` is the same idea exposed as a method so tests can exercise it without spinning up a whole Loop.

Reading map:

| Topic | Upstream file | Lines | Mapped chapter |
|-------|---------------|-------|----------------|
| Checkpoint class | `guide/error-handling.md` | L240-L274 | s11 (this) |
| Resume pattern in loop | `guide/error-handling.md` | L278-L322 | s11 |
| Atomic write notes | `guide/error-handling.md` | L324 | s11 |
| Generator-evaluator (alt pattern) | `guide/long-running-harness.md` | L94-L138 | Appendix A + s11 cross-ref |
| Long-running mental model | `guide/long-running-harness.md` | L19-L92 | Appendix A |
| Event log (complementary) | `guide/managed-agents-architecture.md` | L74-L112 | s10 |
