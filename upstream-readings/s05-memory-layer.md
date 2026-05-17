# s05 upstream excerpt: memory-and-context.md L80-L144 (two-tier memory)

Source: `guide/memory-and-context.md` L80-L144 in `nexu-io/harness-engineering-guide`
Permalink: <https://github.com/nexu-io/harness-engineering-guide/blob/86fec9bea430cecb29ff10afaae36b96496a8f8e/guide/memory-and-context.md#L80-L144>
License: MIT (© 2026 Nexu)

```markdown
## Memory Architecture

The proven memory architecture uses two tiers:

### Tier 1: Daily Logs

Raw, chronological records of what happened. Written during the session, not curated:

    <!-- memory/2026-04-15.md -->
    # 2026-04-15

    ## 14:30 — Refactored auth module
    - Moved JWT validation from middleware to dedicated service
    - Tests passing (23/23)
    - User prefers explicit error messages over error codes

    ## 16:00 — Deploy to staging
    - Used blue-green deployment
    - Rollback plan: revert commit abc123

### Tier 2: Long-term Memory

Curated, distilled knowledge. Updated periodically (not every session):

    <!-- MEMORY.md -->
    # Long-term Memory

    ## User Preferences
    - Prefers explicit error messages over error codes
    - Uses pytest, not unittest

    ## Project Knowledge
    - Auth module: JWT validation in /src/services/auth.py
    - Database: PostgreSQL 15

    ## Lessons Learned
    - Always run tests before committing (broke build on 4/10)

The key insight: daily logs are cheap to write (just append). Long-term memory
requires judgment (what's worth keeping?). Production harnesses write daily
logs automatically and curate MEMORY.md periodically — either on a schedule or
when the agent detects significant learnings.

## Memory Read/Write Cycle

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

## Reading notes

1. **Two tiers are not two stages of the same thing.** Daily logs are *evidence* (what happened, in order); `MEMORY.md` is *summary* (what to remember). The L102-L125 paragraph is explicit that long-term curation "requires judgment". Our Go API stays out of that judgment business — it ships storage primitives only.

2. **The `[0, 1]` window is the whole point of "recent" being two days, not seven.** A naive harness would read every dated file under `memory/`; the guide caps the read to "today and yesterday" because the LLM's context window is finite. Anything older has either been promoted to `MEMORY.md` or isn't worth the tokens. Our Go `Read()` keeps the same cap rather than parameterizing it; future chapters that bolt on a context budget can override.

3. **`os.path.exists` is silent skip — but only for `does not exist`.** A subtle Python anti-pattern: the upstream sketch swallows *every* error a `path.exists`/`open` pair could raise. The Go port differentiates: `os.IsNotExist(err)` → skip; everything else → bubble up. Permission errors should not become silent data loss.

4. **No locks in the Python.** The guide is single-threaded prose. Real Go harnesses run tool calls concurrently and stream LLM deltas — `AppendLog` will be hit from multiple goroutines, and `O_APPEND` alone is not enough on every platform. The `sync.Mutex` in our `Memory.AppendLog` is the bit you only notice when it's missing (the `-race` test catches its absence).

5. **What we add: `RotateOlderThan`.** Upstream doesn't mention disk cleanup, but unbounded daily logs at one file per day will fill any disk that survives a year. The rotation rule (`RotateOlderThan(N)` keeps the last N files) is a 30-line Go addition with one rule-of-thumb test (30-day fixture, rotate to 7). Treat it as a recommended companion to the upstream design, not a port.

## Reading map

| Topic | Upstream file | Lines | Mapped chapter |
|-------|---------------|-------|----------------|
| Two-tier architecture | `guide/memory-and-context.md` | L80-L125 | s05 (this) |
| Read cycle (session_startup) | `guide/memory-and-context.md` | L127-L144 | s05 |
| Session lifetime / clearance | `guide/memory-and-context.md` | L62-L78 | s10 |
| Three-tier context assembly | `guide/memory-and-context.md` | L21-L60 | s04 |
| AGENTS.md (behavior, distinct from memory) | `guide/memory-and-context.md` | L146-L220 | (not chapter-ized) |
