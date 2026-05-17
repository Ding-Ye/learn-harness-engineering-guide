# s10 upstream excerpt: managed-agents-architecture.md L74-L112 (Session as event log)

Source: `guide/managed-agents-architecture.md` L74-L112 in `nexu-io/harness-engineering-guide`
Permalink: <https://github.com/nexu-io/harness-engineering-guide/blob/86fec9bea430cecb29ff10afaae36b96496a8f8e/guide/managed-agents-architecture.md#L74-L112>
Cross-reference: `guide/memory-and-context.md` L62-L78 (session boundary, persistent sessions need serialization)
License: MIT (© 2026 Nexu)

```markdown
### Session (Event Log)

The session is an **append-only log** of everything that happened: LLM calls,
tool results, user messages, system events. It lives outside both the harness
and the sandbox in durable storage.

Key interfaces:
- `emitEvent(sessionId, event)` — write an event during the agent loop
- `getEvents(sessionId, options)` — read events back (positional slicing, filtering)
- `getSession(sessionId)` — get metadata and status

The session outlives both the harness and the sandbox. If either crashes, the
session remains intact.

### Hands (Sandbox)

Sandboxes are execution environments where the agent runs code, edits files,
and executes commands. They are created on demand via `provision({resources})`
and destroyed when no longer needed.

The harness calls sandboxes the same way it calls any tool:
`execute(name, input) → string`. If a sandbox dies, the harness catches the
error as a failed tool call and passes it to the LLM. The model can decide to
retry on a fresh sandbox.

## Session ≠ Context Window

This distinction is the most subtle and most important part of the
architecture.

The **session** is the complete, durable record of everything — potentially
millions of tokens spanning hours or days of agent work. The **context
window** is the subset of that record that the harness selects for the
current LLM call — typically 128K-200K tokens.

    Session (append-only event log, durable)
    ┌─────────────────────────────────────────────────────────────┐
    │ event_1 │ event_2 │ ... │ event_500 │ ... │ event_2000     │
    └─────────────────────────────────────────────────────────────┘
                                        │
                              getEvents(slice)
                                        │
                                        ▼
                        Context Window (selected subset)
                        ┌───────────────────────────┐
                        │ system_prompt             │
                        │ event_1950 ... event_2000 │
                        │ (50 most recent events)   │
                        └───────────────────────────┘
```

## Reading notes

1. **"Append-only" is the load-bearing word.** It rules out mutation, deletion, and reordering. Once an event lands, it stays — and it stays in the position the writer put it. We honor this by opening files with `O_APPEND|O_CREATE|O_WRONLY` and never seeking. The upstream `getEvents(sessionId, options)` deliberately mentions "positional slicing" rather than "query by timestamp" — events are addressable by position because position is the only invariant the format guarantees.

2. **"Lives outside both the harness and the sandbox" forces the implementation choice.** If the session lived inside the harness, a harness crash would lose it. If it lived inside the sandbox, a sandbox restart (cheap, expected) would lose it. The file lives in *durable storage* — meaning a file system that survives process death. For s10 that's just the local FS; a managed-agents deployment would use object storage (S3, GCS) with the same append semantics emulated by content-addressed blobs.

3. **The "Session ≠ Context Window" diagram is the conceptual key.** L91-L112 spells out that a session can be millions of tokens spanning hours, while the context window is the much smaller subset (typically 50-200 events) that gets selected for the current LLM call. s10 implements the *Session* row of the diagram; s09's `SlidingWindowContext` implements the *getEvents(slice)* arrow plus the *Context Window* row. They are not alternatives — they're the two halves of the same picture.

4. **`emitEvent` is fire-and-forget from the loop's point of view.** The signature is `(sessionId, event) → void` (or in Go, `→ error`). The loop doesn't wait for analytics, doesn't wait for replication, doesn't get back a transaction ID. This keeps the agentic loop fast: the worst case for an emit is a single `write()` syscall to a local file, which is microseconds on a modern SSD. A future implementation could buffer + flush asynchronously; we don't because it would complicate the crash-safety story.

5. **The cross-reference at `memory-and-context.md` L62-L78 closes the loop.** It tells us *when* sessions need to be persistent: "Persistent sessions require serialization — writing session state to disk so it can be restored." The "per-task" and "per-conversation" strategies in that table can use s10 too, just with shorter retention. The point is that the JSONL file is the serialization format the cross-reference is calling for.

## Reading map

| Topic | Upstream file | Lines | Mapped chapter |
|-------|---------------|-------|----------------|
| Session as append-only event log | `guide/managed-agents-architecture.md` | L74-L83 | s10 (this) |
| Session ≠ context window | `guide/managed-agents-architecture.md` | L91-L112 | s10 + s09 cross-ref |
| Session boundary / persistence strategies | `guide/memory-and-context.md` | L62-L78 | s10 + s05 cross-ref |
| Replay-on-resume pattern (alt approach) | `guide/error-handling.md` | L231-L322 | s11 (checkpoint) |
| Sliding-window derived view | `guide/context-engineering.md` | L194-L238 | s09 |
