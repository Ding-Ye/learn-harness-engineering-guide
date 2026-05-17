# s11 upstream excerpt: error-handling.md L231-L322 (Checkpoint/Resume)

Source: `guide/error-handling.md` L231-L322 in `nexu-io/harness-engineering-guide`
Permalink: <https://github.com/nexu-io/harness-engineering-guide/blob/86fec9bea430cecb29ff10afaae36b96496a8f8e/guide/error-handling.md#L231-L322>
Cross-reference: `guide/long-running-harness.md` L94-L138 (generator-evaluator as an alternative; reset-vs-compaction trade-off lives at L49-L92)
License: MIT (© 2026 Nexu)

```markdown
## Checkpoint/Resume for Long Tasks

Long-running tasks (20+ turns) are vulnerable to mid-task failures. Checkpointing lets the agent resume without losing progress:

    import json
    import os
    from datetime import datetime

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

    # Usage in the agentic loop
    checkpoint = Checkpoint()

    def agentic_loop_with_checkpoint(task_id, messages, tools):
        """Agentic loop that can resume from a checkpoint."""
        # Try to resume from checkpoint
        saved_state = checkpoint.load(task_id)
        if saved_state:
            messages = saved_state["messages"]
            turn = saved_state["turn"]
            print(f"Resumed from checkpoint at turn {turn}")
        else:
            turn = 0

        for turn in range(turn, 50):
            try:
                response = call_llm(messages, tools)
                assistant_msg = response["choices"][0]["message"]
                messages.append(assistant_msg)

                if not assistant_msg.get("tool_calls"):
                    checkpoint.clear(task_id)
                    return assistant_msg["content"]

                for tc in assistant_msg["tool_calls"]:
                    result = execute_tool(tc)
                    messages.append({
                        "role": "tool",
                        "tool_call_id": tc["id"],
                        "content": result,
                    })

                # Checkpoint every 5 turns
                if turn % 5 == 0:
                    checkpoint.save(task_id, {
                        "messages": messages,
                        "turn": turn,
                    })

            except RetryExhausted as e:
                # Save progress and escalate
                checkpoint.save(task_id, {"messages": messages, "turn": turn})
                escalate(
                    EscalationLevel.BLOCK,
                    f"Task {task_id} failed at turn {turn}: {e}",
                )
                break

The atomic write pattern (`write to .tmp` → `rename`) prevents corrupted checkpoints if the process crashes mid-write.
```

## Reading notes

1. **The atomic write is the whole point — and the Python sample stops short of the full pattern.** The text says "write to .tmp → rename" (L255-L259, L324), but the code itself omits an `f.flush()` + `os.fsync(fd)` step before the rename. On Linux, `os.rename` atomically swaps the directory entry, but the temp file's bytes can still be in the kernel's page cache when the rename returns. A panic / power loss between rename and writeback can publish a zero-byte (or partial) file. Our Go port adds `f.Sync()` after the write specifically because the test `TestCheckpoint_AtomicWriteSurvivesPartialWrite` would otherwise be incomplete: the contract isn't just "atomic name swap", it's "atomic name swap of a flushed file." The cost is one syscall; the benefit is the guarantee actually holding under real crash conditions.

2. **`load()` returning `None` is THE contract, not a convenience.** Look at `agentic_loop_with_checkpoint` L281-L288: `if saved_state: ... else: turn = 0`. The whole resume hand-off depends on `None` being a first-class "no checkpoint" signal, distinct from any kind of failure to read. If `load()` raised on missing files, every loop start would need a try/except wrapper, and a bug that produced a *malformed* file would be indistinguishable from a *missing* one — both would just "start over" silently. Our Go port returns `(nil, nil)` for missing and `(nil, err)` for corrupted; the loop's `LoadOrStartFresh` branches on the nil-ness of the pointer.

3. **The cadence (every 5 turns) is a knob, not a law.** Python encodes it as `if turn % 5 == 0` at L307-L308. We expose it as `Loop.CheckpointEvery int`. Tuning: shorter cadence = less work lost on crash, more disk I/O per turn; longer cadence = more wasted progress on crash, less disk pressure. For LLM-bound loops where each turn costs hundreds of ms, even N=1 is acceptable on local SSD; for batch jobs over network FS, N=10+ is sane. The 5-turn default is a middle-of-the-road choice that the upstream guide and our Go default both inherit.

4. **The "save on error" branch at L314-L320 is not the same as cadence saves.** Notice that the `except RetryExhausted` branch calls `checkpoint.save(...)` *unconditionally* — it doesn't check `turn % 5`. That's because an exhausted-retry is exactly the case where you want the most-up-to-date snapshot on disk: the operator will fix something and restart, and they want resume to pick up from the latest possible point, not from the last cadence boundary. Our Go port replicates this: `Loop.Run`'s `if err != nil` branch saves before propagating, no cadence check.

5. **`clear()` runs on the happy path (no tool calls in the assistant reply, L295-L297).** This is the cleanup that makes the next task with the same task_id start clean — without it, your "task done" status would silently turn into "resume from a stale state" on the next invocation. Idempotency matters here too: the upstream guards with `if os.path.exists`, we use `errors.Is(err, fs.ErrNotExist)` on the Remove call. Either way, calling Clear when there's nothing to clear is a no-op, which lets harness wrappers call it defensively without checking-first.

## Reading map

| Topic | Upstream file | Lines | Mapped chapter |
|-------|---------------|-------|----------------|
| Checkpoint class (save/load/clear) | `guide/error-handling.md` | L240-L274 | s11 (this) |
| Agentic loop with checkpoint hand-off | `guide/error-handling.md` | L278-L322 | s11 |
| Atomic-write note | `guide/error-handling.md` | L324 | s11 |
| Error retry that triggers escalation save | `guide/error-handling.md` | L62-L122 | s07 + s11 cross-ref |
| Reset vs compaction trade-off (when NOT to checkpoint) | `guide/long-running-harness.md` | L49-L92 | Appendix A |
| Generator-evaluator (alternative to checkpoint for some long tasks) | `guide/long-running-harness.md` | L94-L138 | Appendix A + s11 cross-ref |
| Event log (durable record, complementary to single-snapshot checkpoint) | `guide/managed-agents-architecture.md` | L74-L112 | s10 |
