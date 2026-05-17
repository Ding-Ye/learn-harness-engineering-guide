# s12 — Sub-Agent

> Spawn isolated child Go processes via `os/exec`. File-based IPC: parent writes `TASK.md`, child writes `RESULT.json`. Worker pool caps concurrency at `MaxWorkers`; per-task timeout via `exec.CommandContext` sends SIGKILL when the deadline fires.

## Problem

By s11 the harness can survive crashes (checkpoint resume) and replay history (event log). But everything still happens in **one process** with **one context window**. A 4-hour task that touches 60 files can't fit in 128K tokens, no matter how aggressively s09's sliding window compresses. And as `guide/long-running-harness.md` L19-L46 spells out, the model starts *rushing* before it hits the hard token limit — context anxiety, premature wrap-up, declining tool-call counts.

The escape valve is multiplication, not compression. Spawn N children, each with its own fresh 128K window, each doing one well-scoped sub-task. The parent stays small and orchestrates; the workers stay focused and finish.

That's the upstream `guide/sub-agent.md` L66-L145 pattern. The Python version uses `ThreadPoolExecutor` + `subprocess.run`. Go has both of those primitives — a `chan struct{}` semaphore for the pool, `exec.CommandContext` for the process — and the result is shorter and more robust than the Python original.

## Solution

`SubAgentSpawner` runs `[]SubTask` and returns `[]SubResult` of the same length. Each task gets:

1. Its own `WorkDir` (caller-supplied; `t.TempDir()` in tests).
2. A `TASK.md` file inside that dir, whose body is the task's `Instruction` string.
3. A fresh `exec.CommandContext(timeout, childBinary, workDir)` invocation.
4. A `RESULT.json` read-back on exit-0, OR a failure SubResult if the child exits non-zero / times out / forgot to write the file.

```go
spawner := &SubAgentSpawner{
    ChildBinary: "/path/to/cmd/child",
    MaxWorkers:  4,
    Timeout:     30 * time.Second,
}
results := spawner.Spawn(ctx, []SubTask{
    {Name: "a", Instruction: "instruction: ...", WorkDir: dirA},
    {Name: "b", Instruction: "instruction: ...", WorkDir: dirB},
})
for _, r := range results {
    if !r.Success { log.Printf("%s failed: %s", r.Name, r.Output) }
}
```

Three discipline rules:

| | Rule |
|---|---|
| IPC | Only files. Parent writes `<WorkDir>/TASK.md`; child reads `os.Args[1]` as work dir; child writes `<WorkDir>/RESULT.json`. No stdin pipes, no env vars beyond what `os.Environ()` inherits. |
| Pool | Buffered `chan struct{}` of capacity `MaxWorkers`. Each task is one goroutine; acquire-on-entry, release-on-defer. Cancellation-aware. |
| Failure | Never returned as an error. `Spawn` always produces one `SubResult` per task. Crash, timeout, missing RESULT.json → `Success: false` with the reason in `Output`. |

## How It Works

**Spawn** is a flat loop over tasks, one goroutine per task:

```go
for i, t := range tasks {
    go func(i int, t SubTask) {
        sem <- struct{}{}        // acquire (cancellation-aware via select)
        defer func() { <-sem }() // release
        results[i] = s.runOne(ctx, t)
    }(i, t)
}
```

**runOne** is the entire per-task drama: prepare WorkDir, write TASK.md, build a context with timeout, exec the child, classify the outcome. The classification has four arms:

1. `ctx.Err() == context.DeadlineExceeded` → timeout. Output: `"timeout: child killed after <dur>; stderr tail: <tail>"`.
2. `ctx.Err() == context.Canceled` → parent canceled. Output: `"canceled: ..."`.
3. `runErr != nil` (and ctx still healthy) → child exited non-zero. Output: `"child exited non-zero: <err>; stderr tail: <tail>"`.
4. `runErr == nil` → read RESULT.json. If missing → Success=false with reason. If present → unmarshal into SubResult, copy fields onto the parent's result struct.

The check order matters. `exec.CommandContext` translates a context-cancel into `runErr` as a generic `*exec.ExitError` that looks identical to a normal child crash; checking `ctx.Err()` first is what lets us produce the right message.

**Stderr is captured into an 8 KiB tail buffer.** Anything more would let a noisy child blow up the parent's memory; anything less and a panic stack trace gets truncated. Stdout is discarded — the contract is "write to RESULT.json", not "print to stdout".

The **child binary** is a tiny `main` package that lives at `cmd/child/main.go`. It reads `os.Args[1]/TASK.md`, looks for two test-control directives (`sleep:<dur>` and `crash:true`) so the parent-side tests can exercise the timeout and crash paths, and then runs the "real" tool — a unique-word count over the (de-directive-d) instruction text. The result lands in `RESULT.json` as `{success, output, artifacts}`.

```
Parent                                  Child
──────                                  ─────
mkdir -p WorkDir                        (separate process)
write TASK.md                              ↓
exec.CommandContext(timeout,            read os.Args[1]+"/TASK.md"
  childBinary, workDir)                 honor sleep:/crash: directives
   │                                    count unique words
   ↓ (waits)                            write WorkDir+"/RESULT.json"
read RESULT.json                        exit 0
unmarshal → SubResult
```

The parent's `Spawn` finishes when every worker goroutine has finished (`wg.Wait()`), regardless of the order tasks completed. Results slice is in input order (worker writes `results[i] = ...` keyed on its input index), so callers can pair `tasks[i]` with `results[i]` without inspecting `Name`.

## What Changed

| | s10 (event log) | s11 (checkpoint) | s12 (sub-agent) |
|---|---|---|---|
| Concurrency | one writer | one writer | N processes |
| Process boundary | in-process | in-process | OS-level |
| Failure isolation | n/a (single process crash = lose log writer) | atomic .tmp + rename | per-task: one child's crash leaves siblings unaffected |
| Token budget | shared | shared | N independent windows |
| Wall-clock | sequential turn-by-turn | sequential turn-by-turn | parallel up to MaxWorkers |

s11 keeps state durable; s12 keeps **work** parallel. They compose: each sub-agent could checkpoint into its own WorkDir, and the parent could read those checkpoints back. We don't implement that wiring here — it belongs to s_full — but the file-IPC boundary makes it natural.

The other thing that changed: **`Spawn` returns no error.** Every earlier chapter's primary methods (`Provider.Chat`, `Registry.Dispatch`, `Memory.AppendLog`) return `(value, error)`. `Spawn` returns just `[]SubResult` because **per-task failure modes are common** and a single error return would force the caller to choose between "fail the whole batch" (lose siblings' work) and "ignore the error" (lose the failure signal). The Python upstream solves the same problem by catching exceptions inside `as_completed`; we solve it by collapsing the error into the result.

## Try It

```bash
cd agents/s12-sub-agent
go vet ./... && go build ./... && go test -count=1 -timeout=60s ./...
# PASS — 5 tests

# Demo: 3 parallel tasks
go build -o /tmp/s12-child  ./cmd/child
go build -o /tmp/s12-parent ./cmd/parent
mkdir -p /tmp/s12demo/a /tmp/s12demo/b /tmp/s12demo/c
echo "instruction: count these words"   > /tmp/s12demo/a/task-a.md
echo "instruction: another bag of stuff" > /tmp/s12demo/b/task-b.md
echo "instruction: short"                > /tmp/s12demo/c/task-c.md
/tmp/s12-parent -child /tmp/s12-child -root /tmp/s12demo
# [OK] task=a name= duration=12ms
#        output: 4 unique words
# [OK] task=b name= duration=11ms
#        output: 5 unique words
# [OK] task=c name= duration=10ms
#        output: 2 unique words
```

To watch the timeout in action, change one task to include `sleep:5s` and pass `-timeout 1s`:

```bash
printf "instruction: hang\nsleep:5s\n" > /tmp/s12demo/a/task-a.md
/tmp/s12-parent -child /tmp/s12-child -root /tmp/s12demo -timeout 1s
# [FAIL] task=a name= duration=1001ms
#        output: timeout: child killed after 1s; stderr tail:
```

## Upstream Source Reading

Source: `guide/sub-agent.md` L66-L145. Permalink: <https://github.com/nexu-io/harness-engineering-guide/blob/86fec9bea430cecb29ff10afaae36b96496a8f8e/guide/sub-agent.md#L66-L145>

Cross-reference: `guide/multi-agent-orchestration.md` L104-L126 ("Sub-Agent Spawning") explains *why* this is the foundational primitive — each spawned session gets its own context window, system prompt, and tool set, and the parent only sees the final result.

```python
# guide/sub-agent.md L66-L145 (the canonical SubAgentSpawner)
class SubAgentSpawner:
    def __init__(self, agent_command="python -m agent",
                 max_workers=4, timeout=300):
        self.agent_command = agent_command
        self.max_workers = max_workers
        self.timeout = timeout

    def spawn(self, tasks):
        results = []
        with ThreadPoolExecutor(max_workers=self.max_workers) as pool:
            futures = {pool.submit(self._run_agent, task): task
                       for task in tasks}
            for future in as_completed(futures):
                task = futures[future]
                try:
                    results.append(future.result(timeout=self.timeout))
                except Exception as e:
                    results.append(SubResult(
                        name=task.name, success=False,
                        output=f"Agent failed: {type(e).__name__}: {e}",
                        artifacts=[],
                    ))
        return results

    def _run_agent(self, task):
        work_dir = task.working_dir or tempfile.mkdtemp(...)
        task_file = os.path.join(work_dir, "TASK.md")
        with open(task_file, "w") as f:
            f.write(task.instruction)
        result_file = os.path.join(work_dir, "RESULT.json")
        env = os.environ.copy()
        env["AGENT_TASK_FILE"] = task_file
        env["AGENT_RESULT_FILE"] = result_file
        env["AGENT_WORK_DIR"] = work_dir
        proc = subprocess.run(
            self.agent_command.split(), cwd=work_dir, env=env,
            capture_output=True, text=True, timeout=self.timeout,
        )
        if os.path.exists(result_file):
            with open(result_file) as f:
                result_data = json.load(f)
            return SubResult(
                name=task.name,
                success=result_data.get("success", True),
                output=result_data.get("output", ""),
                artifacts=result_data.get("artifacts", []),
            )
        return SubResult(
            name=task.name, success=proc.returncode == 0,
            output=proc.stdout[-5000:] or proc.stderr[-5000:],
            artifacts=[],
        )
```

Reading notes:

- **The upstream uses env vars (`AGENT_TASK_FILE`, `AGENT_RESULT_FILE`, `AGENT_WORK_DIR`); we use a single CLI arg.** Env vars work but mean the child has three independent things to read; a single work-dir arg means the child reads ONE thing and derives the rest by `filepath.Join`. Less coupling, less drift.
- **The upstream's `as_completed` collects results in completion order; we preserve input order.** Python's pattern is convenient because Python's typical caller would `print(result)` immediately. Go's tighter type system rewards "results[i] pairs with tasks[i]" — callers can `for i, r := range results { fmt.Println(tasks[i].Name, r) }` without inspecting `r.Name`.
- **`exec.CommandContext` replaces the `subprocess.run(timeout=...)` argument.** They serve the same role, but the Go primitive is what plumbs SIGKILL onto the context. A separate `time.AfterFunc(timeout, cmd.Process.Kill)` would work too, but invites a "kill arrived after process exited" race that the context API closes off.
- **The stderr/stdout precedence flips.** Python falls back to stderr only when stdout is empty; we capture stderr eagerly and never look at stdout. Reason: in production the child is going to be running `python -m agent` (the upstream default), which writes its own logs to stderr — and stdout is meant for tool output that doesn't fit the file-IPC pattern. Throwing stdout away is cheaper than figuring out which one had the diagnostic.
- **The `[Conversation history summary]` vs `[Conversation summary]` marker drift is a real maintenance hazard the upstream lives with.** The s12 equivalent is the choice of filename: `TASK.md` and `RESULT.json` are hard-coded constants in BOTH parent and child. Don't change them, or the parent and child silently stop communicating.

Reading map:

| Topic | Upstream file | Lines | Mapped chapter |
|-------|---------------|-------|----------------|
| SubAgentSpawner class | `guide/sub-agent.md` | L66-L145 | s12 (this) |
| Sub-agent foundational primitive | `guide/multi-agent-orchestration.md` | L104-L126 | s12 cross-ref |
| File-based message passing (inbox/claim) | `guide/sub-agent.md` | L147-L195 | s12 design rationale |
| Session isolation (independent context windows) | `guide/sub-agent.md` | L197-L224 | s12 motivation |
| Git worktrees for parallel code edits | `guide/sub-agent.md` | L226-L273 | future extension |
| Common pitfalls (over-decomposition, no timeout) | `guide/sub-agent.md` | L275-L281 | s12 design constraints |
