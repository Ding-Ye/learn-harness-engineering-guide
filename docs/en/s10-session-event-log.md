# s10 — Session Event Log

> Append-only JSONL log: one file per session, one JSON event per line. The session outlives the harness — if the harness crashes, the file is still on disk and `Replay()` can rebuild the message history.

## Problem

Every chapter so far has assumed the harness *is* the session. When s01's loop exits, when s07's retry gives up, when s09's window throws away pre-window messages — everything except the in-memory `[]Message` slice vanishes. The upstream architecture document calls this out as a structural defect: the harness is one process, the session is the user's work, and binding their lifetimes together makes a harness crash equivalent to data loss. From `guide/managed-agents-architecture.md` L74-L83:

> The session is an **append-only log** of everything that happened: LLM calls, tool results, user messages, system events. It lives outside both the harness and the sandbox in durable storage.

There's a second concept the same file insists on (L91-L112): **session ≠ context window**. The session is the *complete record* — potentially millions of tokens across hours; the context window is the per-LLM-call *subset*. s09 manages the subset; s10 manages the record.

## Solution

A `SessionStore` interface with three methods (`EmitEvent`, `GetEvents`, `Close`) and one concrete implementation, `FileStore`, that writes:

```
<dir>/sessions/<sessionID>.jsonl
```

with one `Event` per line. The event carries Timestamp, SessionID, Type, and an opaque `Data json.RawMessage`. The Type is open-ended; s10 ships six canonical types matching the upstream:

```go
EventUserMessage  = "user_message"
EventLLMCall      = "llm_call"
EventToolCall     = "tool_call"
EventToolResult   = "tool_result"
EventError        = "error"
EventSessionEnd   = "session_end"
```

`GetEvents` takes `GetEventsOpts{Offset, Limit, TypeFilter}` and applies them in that order (skip → filter → cap). The order matters: "show me the next 10 tool_result events past position 100" is `Offset=100, TypeFilter=[tool_result], Limit=10` and the semantics is "skip 100 *raw* events, then filter".

`Replay(store, sessionID) → []Message` reconstructs a flat message history from the event log. The conversion table is intentionally small:

| Event type | Replays as |
|---|---|
| `user_message` | `Message{Role: "user", Content: data.text}` |
| `llm_call` | `Message{Role: "assistant", Content: data.text}` |
| `tool_result` | `Message{Role: "tool", Content: data.output}` |
| `tool_call`, `error`, `session_end`, anything else | skipped |

## How It Works

`EmitEvent` is the hot path. It marshals to JSON, appends `\n`, takes the mutex, opens (or pulls from cache) the file with `O_APPEND|O_CREATE|O_WRONLY`, and does a single `Write`. Holding the mutex across the open + write keeps the FD cache consistent and protects the closed-after-Close case. The file write itself doesn't strictly need the mutex on POSIX (an `O_APPEND` write of less than `PIPE_BUF` ~ 4KiB is kernel-atomic against other `O_APPEND` writers), but we hold it anyway because we ALSO mutate `openFiles` and `closed`; one lock is easier to reason about than two.

The `openFiles` cache lives for the life of the store. The teaching implementation doesn't evict on idle — `Close()` releases everything in one batch. A production version would TTL-evict to keep FD usage bounded across many sessions.

`GetEvents` is the cold path. It re-opens the file every call, scans line-by-line with a 1 MiB-per-line buffer (the bufio.Scanner default of 64KiB would truncate large tool results), decodes each line as JSON, and applies Offset / TypeFilter / Limit. We deliberately don't maintain an in-memory index: the access pattern is rare reads (debug, replay-on-start, observability dashboard) vs. very frequent writes, and the simplest correct implementation re-reads each time.

`Replay` reads the full event list, sorts by Timestamp (stable, so emit-order ties are preserved), and applies the conversion table above. Sorting is defensive — the file is usually already timestamp-monotonic because emits are sequential, but a future world with concurrent sub-agents flushing through the same store would produce out-of-order writes that need to be reconciled at read time.

The skipping of `tool_call` in Replay is intentional. A `tool_call` is the assistant's *request* to run a tool; the `tool_result` that follows carries the observable text the next LLM call should see. Replaying both would double-count assistant turns and confuse the model on subsequent calls. A richer Replay (matched pairs as `assistant.tool_use` + `tool.tool_result`) is a fine extension; the teaching version stays minimal so the conversion table fits in one mental snapshot.

## What Changed

| | s09 (compression) | s10 (event log) |
|---|---|---|
| Lifetime | one harness run | survives harness crash |
| Storage | in-memory `[]Message` buffer | on-disk JSONL file |
| Mutation | rewrites history on threshold | append-only, never mutated |
| Per-call API | `GetMessages()` returns current view | `GetEvents()` returns position-sliced log |
| Concept | "what the model sees right now" | "what ever happened in this session" |

s09 and s10 are not alternatives. The intended architecture in the upstream diagram (`managed-agents-architecture.md` L94-L112) is: harness `EmitEvent`s unconditionally into s10; per LLM call it reads back a slice via `GetEvents`, then runs s09's sliding-window over that slice to produce the context window. The session is the durable truth; the window is a derived view.

## Try It

```bash
cd agents/s10-session-event-log
go vet ./... && go build ./... && go test -count=1 -race ./...
# PASS — 5 tests under -race

go run .
# === writing 6 events to /tmp/s10-XXXX/sessions/demo-001.jsonl ===
#   emit user_message
#   emit llm_call
#   emit tool_call
#   emit tool_result
#   emit llm_call
#   emit session_end
#
# === GetEvents(opts={}) — full log ===
#   [0] 2026-05-17T12:00:00Z  type=user_message  data={"text":"Read data.json and summarize it"}
#   [1] 2026-05-17T12:00:01Z  type=llm_call      data={"text":"I'll read the file first."}
#   [2] 2026-05-17T12:00:02Z  type=tool_call     data={"args":{"path":"data.json"},"name":"read_file"}
#   [3] 2026-05-17T12:00:03Z  type=tool_result   data={"output":"{\"name\":\"Ada\",\"score\":42}"}
#   [4] 2026-05-17T12:00:04Z  type=llm_call      data={"text":"The file contains Ada (score 42). Task complete."}
#   [5] 2026-05-17T12:00:05Z  type=session_end   data={"reason":"end_turn"}
#
# === Replay — reconstructed message history (4 messages) ===
#   [0] role=user      content="Read data.json and summarize it"
#   [1] role=assistant content="I'll read the file first."
#   [2] role=tool      content="{\"name\":\"Ada\",\"score\":42}"
#   [3] role=assistant content="The file contains Ada (score 42). Task complete."
```

`tail -f` the JSONL file in another terminal while you re-run the demo — that's the entire observability story for a real harness in 100 lines of Go.

## Upstream Source Reading

Source: `guide/managed-agents-architecture.md` L74-L112. Permalink: <https://github.com/nexu-io/harness-engineering-guide/blob/86fec9bea430cecb29ff10afaae36b96496a8f8e/guide/managed-agents-architecture.md#L74-L112>

Cross-reference: `guide/memory-and-context.md` L62-L78 explains *when* persistent sessions are needed (per-task / per-conversation / persistent) and *what* persistence means ("writing session state to disk so it can be restored"). s10 is the disk-write half of that contract.

```markdown
### Session (Event Log)

The session is an **append-only log** of everything that happened: LLM calls,
tool results, user messages, system events. It lives outside both the harness
and the sandbox in durable storage.

Key interfaces:
- emitEvent(sessionId, event) — write an event during the agent loop
- getEvents(sessionId, options) — read events back (positional slicing, filtering)
- getSession(sessionId) — get metadata and status

The session outlives both the harness and the sandbox. If either crashes, the
session remains intact.

## Session ≠ Context Window

This distinction is the most subtle and most important part of the architecture.

The session is the complete, durable record of everything — potentially
millions of tokens spanning hours or days of agent work. The context window
is the subset of that record that the harness selects for the current LLM
call — typically 128K-200K tokens.

Session (append-only event log, durable)
┌─────────────────────────────────────────────────────────────┐
│ event_1 │ event_2 │ ... │ event_500 │ ... │ event_2000     │
└─────────────────────────────────────────────────────────────┘
                                    │
                          getEvents(slice)
                                    │
                                    ▼
                    Context Window (selected subset)
```

Reading notes:

- **"Append-only" is load-bearing.** It rules out mutation, deletion, and reordering. The Go implementation honors this by opening `O_APPEND|O_CREATE|O_WRONLY` and never seeking — the OS guarantees position-monotonic writes.
- **`emitEvent` is fire-and-forget from the loop's point of view.** Signature is `(sessionId, event) → void` (Go: `→ error`); the loop doesn't block on analytics or replication. A future async-flush implementation would complicate the crash-safety story; we don't.
- **The "Session ≠ Context Window" diagram (L94-L112) is the conceptual key.** s10 implements the *Session* row; s09's sliding window implements the *Context Window* row plus the `getEvents(slice)` arrow. Read both chapters to see the full picture.
- **`getEvents` is "positional slicing", not "query by timestamp".** Position is the only invariant the append-only format guarantees; timestamps can collide or be slightly out of order under concurrent emits. Our `GetEventsOpts{Offset, Limit, TypeFilter}` reflects that — offset is by position, filter is by type, no timestamp range.
- **`memory-and-context.md` L62-L78 is the *when*.** "Per-task" sessions clear after every user request; "per-conversation" persist across turns; "persistent" survive process restart. s10 is the storage mechanism that all three strategies need — only the deletion policy differs.

Reading map:

| Topic | Upstream file | Lines | Mapped chapter |
|-------|---------------|-------|----------------|
| Session as event log | `guide/managed-agents-architecture.md` | L74-L83 | s10 (this) |
| Session ≠ context window | `guide/managed-agents-architecture.md` | L91-L112 | s10 + s09 cross-ref |
| Persistent sessions / serialization | `guide/memory-and-context.md` | L62-L78 | s10 + s05 cross-ref |
| Replay-on-resume (single-snapshot alt) | `guide/error-handling.md` | L231-L322 | s11 |
| Sliding window over log | `guide/context-engineering.md` | L194-L238 | s09 |
