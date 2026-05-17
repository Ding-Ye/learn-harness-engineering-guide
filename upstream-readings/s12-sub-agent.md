# s12 upstream excerpt: sub-agent.md L66-L145 (SubAgentSpawner)

Source: `guide/sub-agent.md` L66-L145 in `nexu-io/harness-engineering-guide`
Permalink: <https://github.com/nexu-io/harness-engineering-guide/blob/86fec9bea430cecb29ff10afaae36b96496a8f8e/guide/sub-agent.md#L66-L145>
Cross-reference: `guide/multi-agent-orchestration.md` L104-L126 (sub-agent spawning as foundational primitive)
License: MIT (© 2026 Nexu)

```python
# guide/sub-agent.md L66-L145
class SubAgentSpawner:
    """Spawn and manage sub-agents as isolated processes."""

    def __init__(
        self,
        agent_command: str = "python -m agent",
        max_workers: int = 4,
        timeout: int = 300,
    ):
        self.agent_command = agent_command
        self.max_workers = max_workers
        self.timeout = timeout

    def spawn(self, tasks: list[SubTask]) -> list[SubResult]:
        """Spawn sub-agents for each task and collect results."""
        results = []
        with ThreadPoolExecutor(max_workers=self.max_workers) as pool:
            futures = {
                pool.submit(self._run_agent, task): task
                for task in tasks
            }
            for future in as_completed(futures):
                task = futures[future]
                try:
                    result = future.result(timeout=self.timeout)
                    results.append(result)
                except Exception as e:
                    results.append(SubResult(
                        name=task.name,
                        success=False,
                        output=f"Agent failed: {type(e).__name__}: {e}",
                        artifacts=[],
                    ))
        return results

    def _run_agent(self, task: SubTask) -> SubResult:
        """Run a single sub-agent in an isolated process."""
        # Each sub-agent gets its own working directory
        work_dir = task.working_dir or tempfile.mkdtemp(prefix=f"agent-{task.name}-")

        # Write the task instruction to a file the sub-agent reads
        task_file = os.path.join(work_dir, "TASK.md")
        with open(task_file, "w") as f:
            f.write(task.instruction)

        # Write a result file path for the sub-agent to populate
        result_file = os.path.join(work_dir, "RESULT.json")

        env = os.environ.copy()
        env["AGENT_TASK_FILE"] = task_file
        env["AGENT_RESULT_FILE"] = result_file
        env["AGENT_WORK_DIR"] = work_dir

        proc = subprocess.run(
            self.agent_command.split(),
            cwd=work_dir,
            env=env,
            capture_output=True,
            text=True,
            timeout=self.timeout,
        )

        # Read the result file if it exists
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
            name=task.name,
            success=proc.returncode == 0,
            output=proc.stdout[-5000:] or proc.stderr[-5000:],
            artifacts=[],
        )
```

## Reading notes

1. **The file IPC contract is the actual point of the chapter.** The Python class wraps a `ThreadPoolExecutor`, but everything load-bearing is in `_run_agent`: write `TASK.md` into a work dir, run a child process pointed at that dir, read `RESULT.json` back. The pool/timeout machinery is *plumbing*; the file protocol is the *interface*. A re-implementation that picked gRPC or stdin pipes instead would lose the property that makes this pattern work — that a crashed parent can leave the child's `RESULT.json` on disk for *anyone* to pick up, including a different parent on a different host. Files are the lowest common denominator and that's the design.

2. **Env vars are how the upstream tells the child where its files are; we use a single CLI arg.** The upstream sets `AGENT_TASK_FILE`, `AGENT_RESULT_FILE`, `AGENT_WORK_DIR` — three env vars that the child reads. We pass exactly one positional arg (the work dir) and let the child compute `filepath.Join(workDir, "TASK.md")` and `filepath.Join(workDir, "RESULT.json")` from there. The Python style has the advantage of letting the parent put `TASK.md` outside the work dir; the Go style has the advantage of fewer moving parts in the contract. Either works for this teaching version; we pick the one with less drift surface.

3. **`subprocess.run(..., timeout=self.timeout)` is what we replace with `exec.CommandContext`**. They are not the same primitive. The Python `subprocess.run` timeout raises `TimeoutExpired` and the caller has to wrap it in try/except — which is what the `as_completed` loop does. The Go `exec.CommandContext` plumbs the context's cancellation into a SIGKILL on the child process and surfaces the error as a regular `cmd.Run()` failure — no special exception, no separate machinery. The trade-off: in Go you have to check `ctx.Err() == context.DeadlineExceeded` to *distinguish* a timeout from a normal crash, because the `runErr` looks the same. We do that in `runOne`.

4. **`as_completed` returns futures in completion order; we preserve input order.** This is a subtle but important divergence. Python's `as_completed` iterates over futures *as they finish*, so the `results` list ends up in whatever order tasks happened to complete. A caller who wants the i-th result has to look at `result.name` and reconstruct the mapping. Our `Spawn` writes `results[i] = ...` from inside the per-task goroutine, so `results[i]` always pairs with `tasks[i]`. The cost is that the `Spawn` API doesn't expose any "first-complete" signal — if you want streaming results, you'd add a `Spawn(ctx, tasks, out chan<- SubResult)` variant. We don't need it for the teaching version.

5. **The Python fallback `proc.stdout[-5000:] or proc.stderr[-5000:]` is *strange*.** It says: "if the child wrote stdout, use the tail of that; otherwise the tail of stderr". In practice the upstream agent writes its work to RESULT.json, so this fallback only fires when something went wrong — and in that case, stdout is usually empty and stderr has the panic. We collapse this to "always capture stderr tail" because we *never* read stdout in the happy path (the RESULT.json is the happy path), and the stderr-tail-on-failure pattern is exactly what we want for diagnostics. Less code, identical behavior in the cases the upstream actually exercises.

## Reading map

| Topic | Upstream file | Lines | Mapped chapter |
|-------|---------------|-------|----------------|
| SubAgentSpawner class | `guide/sub-agent.md` | L66-L145 | s12 (this) |
| Sub-agent foundational primitive | `guide/multi-agent-orchestration.md` | L104-L126 | s12 cross-ref |
| File inbox + atomic claim | `guide/sub-agent.md` | L147-L195 | s12 design rationale (not implemented in s12; left as exercise) |
| Session isolation rules | `guide/sub-agent.md` | L197-L224 | s12 motivation |
| Git worktrees for parallel code edits | `guide/sub-agent.md` | L226-L273 | future extension; uses s12 spawner |
| Common pitfalls | `guide/sub-agent.md` | L275-L281 | s12 design constraints |
| Pipeline / fan-out / supervisor patterns | `guide/multi-agent-orchestration.md` | L33-L99 | Appendix B (s12 as the primitive that makes these patterns possible) |
