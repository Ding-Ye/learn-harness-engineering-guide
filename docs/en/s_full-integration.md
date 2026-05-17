# s_full — Full Integration: end-to-end trace through the 14 chapters

This is the bookend of the curriculum. There is no new code in this chapter — every component below was already implemented and tested in `s01..s14`. What we do instead is walk a single, very ordinary user request from input character to final answer, naming every chapter the request crosses and the exact file in `agents/sNN-…/` that holds the logic.

If you have read the previous fourteen chapters as isolated mechanisms, this chapter is the wire diagram that connects them. If you skipped ahead, this is the map that tells you which chapters to read first.

## Architecture Overview

```
                            ┌──────────────────────────┐
                            │       User input         │
                            └────────────┬─────────────┘
                                         │
              ┌──────────────────────────▼──────────────────────────┐
              │                  Agentic Loop (s01)                  │
              │           "think → act → observe", bounded           │
              └───┬─────────┬─────────────┬────────────┬─────────┬──┘
                  │         │             │            │         │
        assemble  │  call   │  dispatch   │  retry on  │ snapshot│
                  │   LLM   │    tool     │  transient │  every N│
                  │         │             │            │   turns │
   ┌──────────────▼──┐ ┌────▼──────┐ ┌────▼─────────┐ ┌▼───────┐ ┌▼─────────┐
   │ Context (s04)   │ │ Provider  │ │ Tool Registry│ │Retry   │ │Checkpoint│
   │ Memory   (s05)  │ │   (s02)   │ │     (s03)    │ │ (s07)  │ │  (s11)   │
   │ Compress (s09)  │ │ Anthropic │ │ + Guardrail  │ │exp+jit │ │.tmp+ren  │
   │ Skill    (s08)  │ │ + Mock    │ │     (s06)    │ │        │ │          │
   └─────────────────┘ └───────────┘ └──┬───────────┘ └────────┘ └──────────┘
                                        │
                              ┌─────────▼─────────┐    ┌─────────────────┐
                              │ Classifier (s14)  │    │ Event Log (s10) │
                              │ tier1/2/3 + LLM   │◄──►│  append-only    │
                              └─────────┬─────────┘    └─────────────────┘
                                        │
                              ┌─────────▼─────────┐    ┌─────────────────┐
                              │ Sub-agent (s12)   │    │  Cron   (s13)   │
                              │ child processes   │    │ NextRun/Should  │
                              └───────────────────┘    └─────────────────┘
```

The spine is the agentic loop from `s01`: pull messages in, ask the model, dispatch any `tool_use` blocks, append `tool_result`, repeat until the model stops or the turn budget is exhausted. Everything else either feeds the loop (context, memory, skill schemas), wraps the loop (retry, checkpoint, event log), or vetoes a step inside the loop (guardrail, classifier). The cron scheduler and sub-agent spawner are out-of-band entry points that ultimately invoke the same loop.

## Execution Trace

Scenario: a user types **"Read `data.json` and write a summary to `summary.txt`"**.

The trace below is the 16-step end-to-end from `.learn/research-notes.md`. Each step names the harness file that owns the behaviour and the upstream guide passage that motivates it.

1. **User submits the request.**
   - Harness: `agents/s01-minimum-loop/main.go` accepts the user string; `agents/s10-session-event-log/main.go` opens the session.
   - Upstream: `.learn/upstream/guide/your-first-harness.md` L75-L120 (driver loop) and `.learn/upstream/guide/managed-agents-architecture.md` L74-L112 (session begins).
   - The very first event written to `sessions/<id>.jsonl` is `{type:"user_message"}`.

2. **Harness assembles context for turn 1.**
   - Harness: `agents/s04-context-assembler/assembler.go` sorts sections by priority; `agents/s05-memory-layer/memory.go` reads `MEMORY.md` + the last few daily logs; `agents/s08-skill-system/registry.go` emits only schemas of *active* skills, not the full catalogue.
   - Upstream: `.learn/upstream/guide/context-engineering.md` L15-L87 (priority order), `.learn/upstream/guide/memory-and-context.md` L80-L144 (two-tier memory).
   - Result: a single packed string plus a `tools[]` slice ready for the model.

3. **Loop calls the LLM provider.**
   - Harness: `agents/s01-minimum-loop/loop.go` invokes `Provider.Chat`; the implementation in `agents/s02-llm-provider/anthropic_provider.go` HTTP-POSTs to `api.anthropic.com/v1/messages`; `agents/s07-error-retry/retry.go` wraps the call with exponential backoff on transient classes.
   - Upstream: `.learn/upstream/guide/agentic-loop.md` L41-L65 (loop body), `.learn/upstream/guide/your-first-harness.md` L209-L236 (request shape), `.learn/upstream/guide/error-handling.md` L62-L122 (retry).

4. **Model reasons internally.**
   - Harness: nothing — this is server-side. `agents/s14-classifier-permissions/reasoning_strip.go` will later make sure these reasoning blocks are *not* leaked to the permission classifier.
   - Upstream: `.learn/upstream/guide/agentic-loop.md` L10-L35 (reason→act→observe), `.learn/upstream/guide/classifier-permissions.md` L151-L169 (reasoning-blind).

5. **Model emits the first `tool_use` block: `read_file(path="data.json")`.**
   - Harness: the JSON arrives in `ChatResponse.Content` as a `ContentBlock{Type:"tool_use"}` parsed by `agents/s02-llm-provider/anthropic_provider.go`.
   - Upstream: `.learn/upstream/guide/your-first-harness.md` L109 (model returns a tool call), `.learn/upstream/guide/tool-system.md` L36-L61 (schema vs implementation split).

6. **Harness extracts the `tool_use` blocks from the assistant message.**
   - Harness: `agents/s01-minimum-loop/loop.go` iterates `response.Content`, separating text from tool calls.
   - Upstream: `.learn/upstream/guide/agentic-loop.md` L52-L53 (loop continues only while `tool_use` blocks remain).

7. **Classifier vets the tool call.**
   - Harness: `agents/s14-classifier-permissions/tiers.go` runs Tier 1 (whitelist `read_file` on any path inside `WORKDIR/**`) → ALLOW; the LLM judge in `agents/s14-classifier-permissions/classifier.go` is **not** invoked because Tier 1 short-circuits.
   - Upstream: `.learn/upstream/guide/classifier-permissions.md` L113-L141 (three-tier flow).

8. **Guardrail-wrapped dispatch executes `read_file`.**
   - Harness: `agents/s06-guardrails/dispatch_wrapper.go` runs allow-list and deny-pattern checks first, then calls into `agents/s03-tool-registry/registry.go`, which routes by name to `agents/s03-tool-registry/tools_fileops.go`.
   - Upstream: `.learn/upstream/guide/guardrails.md` L22-L116 (code-level checks), `.learn/upstream/guide/tool-system.md` L62 (always return a string).

9. **Tool result is appended to messages; event log records it.**
   - Harness: `agents/s01-minimum-loop/loop.go` appends a `ContentBlock{Type:"tool_result", ID:<same id>, Content:<file body>}`; `agents/s10-session-event-log/file_store.go` `O_APPEND`s a `{type:"tool_result"}` line to `sessions/<id>.jsonl`.
   - Upstream: `.learn/upstream/guide/your-first-harness.md` L113-L117 (append discipline), `.learn/upstream/guide/managed-agents-architecture.md` L74-L112 (append-only log).

10. **Loop iterates; sliding window may compress if needed.**
    - Harness: before the next `Provider.Chat`, `agents/s09-context-compression/sliding_window.go` measures token usage with `agents/s09-context-compression/tokens.go`; at ≥70% of `maxTokens` it asks `agents/s09-context-compression/summarize.go` to compress everything outside the last N=15 turns.
    - Upstream: `.learn/upstream/guide/context-engineering.md` L91-L238 (three lines of defence), `.learn/upstream/guide/long-running-harness.md` L19-L92 (context anxiety + compaction).

11. **Model reasons over the file content now in context.**
    - Harness: same path as step 4.
    - Upstream: `.learn/upstream/guide/agentic-loop.md` L10-L35.

12. **Model emits the second `tool_use`: `write_file(path="summary.txt", content="...")`.**
    - Harness: extracted again by `agents/s01-minimum-loop/loop.go`.
    - Upstream: `.learn/upstream/guide/your-first-harness.md` L109.

13. **Classifier Tier 2 fires because the path is inside the repo.**
    - Harness: `agents/s14-classifier-permissions/tiers.go` matches `summary.txt` against the in-project path matcher and returns ALLOW without calling the LLM judge.
    - Upstream: `.learn/upstream/guide/classifier-permissions.md` L113-L141.

14. **`write_file` runs through the same guardrail → registry pipeline.**
    - Harness: `agents/s06-guardrails/dispatch_wrapper.go` → `agents/s03-tool-registry/registry.go` → `agents/s03-tool-registry/tools_fileops.go` writes the file and returns the success string.
    - Upstream: `.learn/upstream/guide/your-first-harness.md` L80-L84.

15. **Event log records the result; checkpoint snapshots state if the turn counter is a multiple of N.**
    - Harness: `agents/s10-session-event-log/file_store.go` writes another `tool_result` event; `agents/s11-checkpoint-resume/checkpoint.go` atomically (`.tmp` + `os.Rename`) snapshots `{messages, turn}` to disk if `turn % 5 == 0`.
    - Upstream: `.learn/upstream/guide/error-handling.md` L231-L322 (atomic checkpoint), `.learn/upstream/guide/long-running-harness.md` L94-L138 (resume).

16. **Model emits a text-only response; loop exits cleanly.**
    - Harness: `agents/s01-minimum-loop/loop.go` sees no `tool_use` blocks in the assistant message and returns; `agents/s10-session-event-log/main.go` writes a final `session_end` event; `agents/s11-checkpoint-resume/checkpoint.go` clears the checkpoint file because the task succeeded.
    - Upstream: `.learn/upstream/guide/agentic-loop.md` L52-L53 (termination), `.learn/upstream/guide/error-handling.md` L296-L322 (clear-on-success).

For a long-running variant of the same task (say, a 4-hour build), step 1 may originate from `agents/s13-cron-scheduler/scheduler.go` instead of a human, and steps 5–14 may be fanned out by `agents/s12-sub-agent/spawner.go` into N children that each run their own copy of the s01 loop in a fresh process with a clean context.

## Cross-chapter Interaction

```
User                Loop              Provider          Tool                  Classifier        Event Log     Checkpoint
 │ "read & summarize"│                  │                 │                      │                  │              │
 ├──────────────────►│                  │                 │                      │                  │              │
 │                   │ assemble (s04+s05+s08)                                    │                  │              │
 │                   ├─────────────────►│ (s02, type contract)                   │                  │              │
 │                   │                  │ Chat(messages,tools)                   │                  │              │
 │                   │                  │  retry (s07) on transient              │                  │              │
 │                   │◄─────────────────┤ tool_use{read_file}                    │                  │              │
 │                   ├────────────────────────────────────►│ vet (Tier1 whitelist)                 │              │
 │                   │                  │                 │◄─────────────────────┤ ALLOW           │              │
 │                   │                  │                 │  guardrail (s06)     │                  │              │
 │                   │                  │                 │  dispatch (s03, dynamic dispatch)        │              │
 │                   │                  │                 │◄────── result ───────│                  │              │
 │                   │                  │                 │                      │                  │              │
 │                   ├──────────────────────────────────────────────────────────────────────────────►│ append tool_result
 │                   │ (loop iterates, s09 compresses if >70%)                   │                  │              │
 │                   ├─────────────────►│                                        │                  │              │
 │                   │◄─────────────────┤ tool_use{write_file}                   │                  │              │
 │                   ├────────────────────────────────────►│ vet (Tier2 in-repo) │                  │              │
 │                   │                  │                 │◄─── ALLOW            │                  │              │
 │                   │                  │                 │ dispatch, write      │                  │              │
 │                   │                  │                 │◄────── ok ───────────│                  │              │
 │                   │                                                                                ├─►snapshot(s11)
 │                   ├─────────────────►│                                                                          │
 │                   │◄─────────────────┤ text-only (end_turn)                                                      │
 │                   │                                                            │                  │ session_end  │
 │◄──────────────────┤                                                                                ├─► clear     │
```

Two kinds of edges show up: **type contracts** (the `Provider` interface, the `Tool` interface, the `SessionStore` interface — stable boundaries set up once in `s02`, `s03`, `s10`) and **dynamic dispatch** (the registry lookup by tool name in `s03`, the skill activation in `s08`, the classifier verdict in `s14` — runtime decisions). The architecture stays approachable because the type contracts are few and the dynamic dispatches are localised.

## Deliberate Omissions

| Feature | Upstream impl | Why we skip |
|---|---|---|
| Streaming responses | `.learn/upstream/guide/agentic-loop.md` L119-L137 | adds two-to-three chapters of channel plumbing without revealing a new mental model. Once the loop is clear, streaming is a refactor, not a concept. |
| OS-level sandbox (Docker / Firecracker / chroot) | `.learn/upstream/guide/sandbox.md` (whole file, esp. L40-L160) | OS infrastructure rather than Go code; teaching value diminishes per line. We ship policy-level guardrails in `s06` instead and link out. |
| MCP protocol (Model Context Protocol servers) | `.learn/upstream/guide/tool-system.md` (mentions), wider Anthropic docs | separate spec; orthogonal to teaching tool dispatch. `s03` makes the boundary; an MCP transport is a small additional implementation of `Tool`. |
| Real classifier model | `.learn/upstream/guide/classifier-permissions.md` L29-L169 | `s14` uses a `MockProvider` so tests are deterministic; production use needs the Phase G multi-model layer plus a fast model like Haiku for stage 1. |
| Eval harness (LLM-judge benchmarks) | `.learn/upstream/guide/eval-awareness.md`, `eval-infrastructure.md` | separate discipline; we test mechanisms with `go test`, not capabilities with judge prompts. |
| Two-phase initializer-coding pattern | `.learn/upstream/guide/initializer-coding-pattern.md` | a *pattern* over `s01 + s11`; once you have the loop and checkpoints, the pattern reads as "do two loops". Listed as an extension exercise in Appendix B. |
| Generator-evaluator pair | `.learn/upstream/guide/long-running-harness.md` L94-L138 | another *pattern*, composes `s01` × 2 + `s10`. Covered in Appendix A. |
| Real Docker process isolation for sub-agents | `.learn/upstream/guide/sub-agent.md` L150-L210 | `s12` ships file-IPC + `os/exec` isolation, which is enough to teach the *shape*. Going further is environment work, not Go work. |
| Prompt-injection corpus / heuristics | `.learn/upstream/guide/guardrails.md` L120-L156 | `s06` carries the deny-list shape; a real injection corpus needs hundreds of examples that age fast. |
| Distributed cron with leader election | `.learn/upstream/guide/scheduling-and-automation.md` L205-L300 | `s13` is single-process and deterministic; clustering is operational concern. |

## Multi-model bridge

The `Provider` interface introduced in `agents/s02-llm-provider/provider.go` is shaped around the Anthropic Messages API: a system string, a list of messages composed of role + content blocks (`text`, `tool_use`, `tool_result`), and a `stop_reason` of `end_turn | tool_use | max_tokens`. That shape is convenient because it lines up almost 1:1 with how the model thinks, but it does mean `anthropic_provider.go` is a thin marshaller while an OpenAI provider has more translation to do — function-call arguments come back as a JSON string in `tool_calls[].function.arguments` rather than a structured block, and the role layout is flatter.

If you want to run the same loop against OpenAI, DeepSeek, Qwen, Moonshot or a local vLLM endpoint that speaks the OpenAI Chat Completions wire format, see `docs/en/multi-model.md` (Phase G addendum). That file specifies an additional `OpenAIProvider` that implements the same `Provider` interface and a translation layer from canonical `[]ContentBlock` to OpenAI's `messages[].content` + `tool_calls[]` shapes. The rest of `s01..s14` does not change.

## Read Further

- For every chapter you visited above, the matching `docs/{zh,en}/sNN-…md` has an **Upstream Source Reading** section that walks the relevant guide passage line by line in a Go-developer voice — start there if you want to deepen any single mechanism.
- For OS-level sandboxing (Docker, Firecracker, chroot, capability drop, read-only fs) read upstream `.learn/upstream/guide/sandbox.md` directly; we deliberately did not port that into a Go chapter.
- For the discipline of testing agents — capability evals, judge prompts, eval-awareness leakage — read `.learn/upstream/guide/eval-awareness.md` and `.learn/upstream/guide/eval-infrastructure.md`.
- For the mental model behind why long sessions silently degrade, read `docs/en/appendix-a-context-anxiety.md`. It explains the failure mode that motivates `s09`, `s11`, and `s12` together.
- For a complete reading order through all 25 upstream guides mapped to our 14 chapters, see `docs/en/appendix-b-upstream-map.md`.
- For pinned upstream permalinks, every citation above is reproducible at `https://github.com/nexu-io/harness-engineering-guide/blob/86fec9bea430cecb29ff10afaae36b96496a8f8e/<path>`.
