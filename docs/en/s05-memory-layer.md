# s05 — Memory Layer

> Cross-session persistence in ~150 lines of Go: a curated `MEMORY.md` plus append-only daily logs. No LLM, no network.

## Problem

After s01-s04 the harness can run a loop, pick a model, dispatch tools, and pack a context window. But every session starts blank. A user mentions a preference at 14:00; an hour later the agent has forgotten it. The bug is structural: the loop holds state for one run, then dies.

What we want is the property `guide/memory-and-context.md` L78 describes:

> Anything worth keeping across restarts should be written to a memory file rather than kept in session state.

We need a small filesystem-backed memory that the loop can `Read()` at startup and `AppendLog()` to during the run — and that *never* loses writes when two goroutines call `AppendLog` at once.

## Solution

The upstream guide (L82-L125) prescribes a two-tier architecture and we mirror it directly:

| Tier | File | Discipline |
|------|------|------------|
| Long-term | `<baseDir>/MEMORY.md` | Curated. Human/agent updates periodically. |
| Daily logs | `<baseDir>/memory/YYYY-MM-DD.md` | Append-only. Cheap. One file per day. |

The Go API is four operations on a `Memory` value:

```go
mem, _ := New("/var/agent/memory", RealClock{})
view, _ := mem.Read()             // long-term + today + yesterday
mem.AppendLog("## 14:30 — Refactored auth")
mem.RotateOlderThan(30)           // delete daily logs older than 30 days
```

That's it. No DB, no SQL, no embedded index.

## How It Works

Read is a direct port of the Python in `guide/memory-and-context.md` L130-L143:

```
sections = []
1. if MEMORY.md exists           → append its content
2. for daysAgo in [0, 1]:
     if memory/<that-date>.md    → append its content
3. return strings.Join(sections, "\n---\n")
```

A two-days-ago log is deliberately excluded. The guide picks `[0, 1]` (L138) because: today is what the agent already remembers within session; yesterday is the most-recent context worth surfacing; anything older has either been promoted to `MEMORY.md` or is no longer worth the tokens.

Append is one open-with-`O_APPEND|O_CREATE` per call, mutex-guarded:

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

Three callouts:

1. **Why both `O_APPEND` and a mutex?** On POSIX a single `write()` under `PIPE_BUF` to an `O_APPEND` fd is atomic with respect to other appenders — no interleaving. The mutex is the belt-and-braces version that holds the same contract on platforms where `O_APPEND` semantics are weaker, and it also serializes us against a hypothetical future code path that does truncate-and-rewrite.
2. **The trailing newline matters.** Without it, two appended entries fuse into one line and the daily log becomes unreadable. The test `TestMemory_AppendIsAtomicAcrossWriters` fails if you forget it.
3. **`Clock` is the only seam.** Filenames depend on `time.Now()`. Inject `FakeClock{T: …}` in tests; pass `RealClock{}` in `main`.

Rotation deletes files older than the cutoff. The cutoff rule from `RotateOlderThan(7)` is: keep today and the six prior days (seven files), delete day 7 and beyond. We parse `YYYY-MM-DD.md`, compare against `now-N` truncated to a day boundary, and `os.Remove` the rest. `MEMORY.md` is at the root of `<baseDir>`, not in `<baseDir>/memory/`, so it is structurally outside the rotation loop and can never be deleted.

## What Changed

| | s04 (assembler) | s05 (memory) |
|---|---|---|
| Lifetime | one LLM call | across runs |
| Storage | in-memory `[]ContextSection` | files on disk |
| Concurrency | single goroutine | many writers, one snapshot |
| Token budget | yes | no (caller's problem) |

s04 picked sections for a single LLM call; s05 produces *one such section* (the memory snippet) that an assembler-style caller would slot in at `priority=3`. The two compose cleanly in `s_full`. No s04 code is imported here — chapters are isolated per the curriculum's pedagogy rule.

## Try It

```bash
cd agents/s05-memory-layer
go test -count=1 ./... -race
# PASS — 5 tests including TestMemory_AppendIsAtomicAcrossWriters
# (50 goroutines, all writes land, no interleaving)

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

The `-race` flag is mandatory: the atomic-append test exercises 50 concurrent writers and is the cheapest way to catch a future regression that drops the mutex.

## Upstream Source Reading

Source: `guide/memory-and-context.md` L80-L144. Permalink: <https://github.com/nexu-io/harness-engineering-guide/blob/86fec9bea430cecb29ff10afaae36b96496a8f8e/guide/memory-and-context.md#L80-L144>

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

Reading notes:

- **The `[0, 1]` window is a design choice, not a constant.** The guide picks the smallest non-trivial window. We hard-code the same; a future extension exercise is to make it a parameter so users can read three days back when they know the agent has been idle.
- **`os.path.exists` is silent-skip.** The Go port uses `os.IsNotExist(err)` to get the same shape: missing file isn't an error, it just isn't appended. Any *other* read error (permissions, IO) IS surfaced — the Python is sloppier here and would crash later.
- **No locks in the Python.** The upstream sketch is single-threaded. A Go harness with concurrent tool calls and an LLM streaming token deltas can easily race on `AppendLog`; we add a mutex up-front.
- **`MEMORY.md` curation is out of scope for this chapter.** The guide says "Updated periodically (not every session)" (L104). What "periodically" means and *who* curates is a policy question; this chapter ships only the storage primitive.
- **What we add beyond upstream.** `RotateOlderThan` does not appear in the guide. In a real harness, unbounded daily logs eat disk; deleting old ones is a 30-line addition that pays for itself in week one.

Reading map:

| Topic | Upstream file | Lines | Mapped chapter |
|-------|---------------|-------|----------------|
| Two-tier architecture | `guide/memory-and-context.md` | L80-L125 | s05 (this) |
| Read cycle | `guide/memory-and-context.md` | L127-L144 | s05 |
| Session lifecycle | `guide/memory-and-context.md` | L62-L78 | s10 |
| AGENTS.md (behavior, distinct from memory) | `guide/memory-and-context.md` | L146-L220 | (out of scope) |
