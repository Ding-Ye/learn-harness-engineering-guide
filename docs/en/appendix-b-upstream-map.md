# Appendix B — Upstream map

> Where each chapter in this curriculum comes from in [`nexu-io/harness-engineering-guide`](https://github.com/nexu-io/harness-engineering-guide), pinned at sha `86fec9bea430cecb29ff10afaae36b96496a8f8e`. Use this to read the upstream alongside the Go code, or as a syllabus if you want to walk the original guide on its own terms.

## Reading order

If you have the time, read the upstream guide in dependency order rather than alphabetical or `ls` order. Suggested path through the 25 files in `.learn/upstream/guide/`:

1. Read `guide/what-is-harness.md` first — the **4 pillars** introduction (agentic loop, tool system, memory & context, guardrails). This is background; not chapter-mapped.
2. Read `guide/glossary.md` — 24 term definitions. Keep it open in a side tab. Every subsequent chapter assumes these.
3. Read `guide/your-first-harness.md` — directly mirrors s01, s02, s03. The 50-line Python agent is the spine the Go ports follow.
4. Read `guide/agentic-loop.md` — depth on s01's central loop. think/act/observe, turn budgets, parallel tool calls, streaming.
5. Read `guide/tool-system.md` — the schema-vs-implementation split that s03 ports.
6. Read `guide/memory-and-context.md` and `guide/context-engineering.md` together — these underpin s04, s05, s09. Read memory first, context engineering second.
7. Read `guide/guardrails.md` then `guide/classifier-permissions.md` — the static-rule defense (s06) and the model-based defense (s14). They compose; you want the static one in your head first.
8. Read `guide/error-handling.md` — feeds both s07 (classify + retry) and s11 (checkpoint pattern).
9. Read `guide/skill-system.md` — s08. The "thin harness, thick skills" inversion lives here.
10. Read `guide/long-running-harness.md` — the source of Appendix A and the conceptual frame for s09 and s11.
11. Read `guide/managed-agents-architecture.md`, `guide/sub-agent.md`, `guide/multi-agent-orchestration.md` together — these feed s10 (event log) and s12 (sub-agent), and inform Phase G expansions.
12. Read `guide/scheduling-and-automation.md` last among the chapter-mapped files — s13.

The remaining guides (`harness-vs-framework.md`, `comparison.md`, `sandbox.md`, `eval-*.md`, `agent-teams.md`, `ghost-account-hunting.md`, `nexu-windows-packaging.md`, `initializer-coding-pattern.md`) are **breadth reading** — important for context, not directly chapter-mapped. Read them after finishing the curriculum if you want the full picture.

## File-to-session map

All 25 guides in `.learn/upstream/guide/`, each with its key line range and chapter mapping. Line ranges are pinned to the SHA above and may drift in newer commits.

| Upstream guide | Key lines | What it teaches | Our session |
|---|---|---|---|
| `guide/what-is-harness.md` | full | 4 pillars of a harness; loop / tools / memory / guardrails framing | (background, all chapters) |
| `guide/your-first-harness.md` | L24-L236 | 50-line Python harness; OpenAI vs Anthropic side-by-side | s01, s02, s03 |
| `guide/agentic-loop.md` | L9-L137 | think/act/observe; parallel tool calls; streaming; turn budgets | s01 |
| `guide/tool-system.md` | L9-L100 | registry, schemas (model-facing) vs implementations (runtime) | s03 |
| `guide/context-engineering.md` | L15-L238 | priority assembly + sliding window + 3 lines of defense | s04, s09 |
| `guide/memory-and-context.md` | L21-L144 | session vs memory; three-tier assembly; MEMORY.md pattern | s04, s05, s10 |
| `guide/guardrails.md` | L22-L116 | permission model; allow / deny / tiered approval | s06 |
| `guide/error-handling.md` | L9-L322 | classify; backoff with jitter; checkpoint via `.tmp + rename` | s07, s11 |
| `guide/skill-system.md` | L9-L220 | skill bundles (SKILL.md); on-demand load; saves context | s08 |
| `guide/long-running-harness.md` | L19-L138 | context anxiety; reset vs compaction; generator-evaluator | s09, s11, **Appendix A** |
| `guide/managed-agents-architecture.md` | L74-L112 | brain / hands / session decoupling; event-log architecture | s10 |
| `guide/sub-agent.md` | L66-L145 | leader-worker; file IPC (TASK.md / RESULT.json); isolation | s12 |
| `guide/multi-agent-orchestration.md` | L33-L126 | fan-out / pipeline / supervisor / peer-to-peer | s12 (cross-ref) |
| `guide/scheduling-and-automation.md` | L78-L204 | cron grammar; heartbeats; long-running triggers | s13 |
| `guide/classifier-permissions.md` | L29-L169 | two-stage permission model; reasoning-blind classifier | s14 |
| `guide/sandbox.md` | full | Docker / Firecracker; capability drop; read-only fs | out of scope, linked from s06 |
| `guide/eval-awareness.md` | full | when agents recognize being tested | out of scope (breadth) |
| `guide/eval-infrastructure.md` | full | resource-config impact on benchmark noise | out of scope (breadth) |
| `guide/initializer-coding-pattern.md` | full | two-phase harness (init + coding) for long agents | backup mechanism, not chapterized |
| `guide/agent-teams.md` | full | 16-parallel agents; Ralph-loop; git coordination | cross-ref from s12 |
| `guide/harness-vs-framework.md` | full | positioning vs LangChain/CrewAI/AutoGen | background |
| `guide/comparison.md` | full | Harness vs Framework feature matrix | background |
| `guide/glossary.md` | full | 24 term definitions | linked from every chapter footer |
| `guide/ghost-account-hunting.md` | full | detecting/preventing agent credential misuse — case study | tied to s14 motivation |
| `guide/nexu-windows-packaging.md` | full | Electron packaging case study | off-scope |

## Symbol cross-reference

A Go-developer-side mapping: our canonical type or function, and the upstream Python (or prose) it corresponds to.

| Our type / func | Upstream equivalent | Source |
|---|---|---|
| `Provider` interface (s02) | `client.chat.completions.create` + `client.messages.create` | `guide/your-first-harness.md` L98-L102, L218-L228 |
| `ChatRequest` / `ChatResponse` (s02) | the OpenAI/Anthropic request/response shape side-by-side | `guide/your-first-harness.md` L209-L236 |
| `Tool` interface (s03) | the `TOOLS` list + `execute_tool()` function | `guide/your-first-harness.md` L42-L88 |
| `Registry.Dispatch` (s03) | `execute_tool(name, args)` switch | `guide/tool-system.md` L36-L88 |
| `ContextAssembler.Build()` (s04) | `ContextAssembler.build()` prose + listing | `guide/context-engineering.md` L36-L87 |
| `EstimateTokens` heuristic (s04) | `estimate_tokens` via `tiktoken` | `guide/context-engineering.md` L36-L38 |
| `Memory{baseDir, clock}` (s05) | MEMORY.md + `memory/YYYY-MM-DD.md` filesystem layout | `guide/memory-and-context.md` L80-L130 |
| `Checker` interface (s06) | (prose only — concept lives in `guardrails.md`) | `guide/guardrails.md` L22-L116 |
| `Classify(err) ErrorClass` (s07) | error-classification table (Transient / Permanent / Model / Resource) | `guide/error-handling.md` L20-L60 |
| `RetryWithBackoff` (s07) | `@retry` decorator with exponential backoff | `guide/error-handling.md` L62-L122 |
| `SkillRegistry` (s08) | skill menu + on-demand load pattern | `guide/skill-system.md` L78-L101 |
| `list_skills` / `load_skill` meta-tools (s08) | the same two meta-tools, by name | `guide/skill-system.md` L100-L150 |
| `SlidingWindowContext` (s09) | sliding-window compaction defense | `guide/context-engineering.md` L194-L238 |
| `Summarizer` interface (s09) | `SUMMARIZE_PROMPT` constant + summarization call | `guide/context-engineering.md` L146-L148 |
| `SessionStore` interface (s10) | "events log" concept | `guide/managed-agents-architecture.md` L74-L112 |
| `Event{Timestamp, Type, Data}` (s10) | JSONL event record | `guide/memory-and-context.md` L62-L78 |
| `Checkpoint.Save` (s11) | `.tmp` + `os.rename` atomic write recipe | `guide/error-handling.md` L255-L259 |
| `SubAgentSpawner` (s12) | leader-worker spawn pattern | `guide/sub-agent.md` L66-L145 |
| `TASK.md` / `RESULT.json` IPC (s12) | the exact filenames upstream uses | `guide/sub-agent.md` L104-L130 |
| `CronSchedule.NextRun` (s13) | cron expression grammar (5-field) | `guide/scheduling-and-automation.md` L84-L94 |
| `Classifier` (s14) | three-tier permission flow | `guide/classifier-permissions.md` L113-L141 |
| `StripReasoning` (s14) | reasoning-blind classifier rationale | `guide/classifier-permissions.md` L151-L169 |

## Suggested extension exercises

Five exercises that combine chapters or push past the curriculum's stopping point. None are graded; pick whichever stretches your understanding.

1. **Combine s10 + s11 — event-log-driven checkpoint.** Drop s11's separate snapshot format. Instead, replay the s10 event log to rebuild loop state on resume. You'll need a deterministic projection function `events → state`. Decide what happens when a tool result is mid-write (hint: `.tmp` is your friend, even here).

2. **Combine s12 + s13 — scheduled sub-agent.** Use s13's cron to fire a `daily_at_8am` schedule. On each fire, spawn an s12 sub-agent that runs a "yesterday's digest" task in its own context. Use s10 to record the spawn-and-result pair as events. Cap concurrency to 1 (s12's `maxWorkers`).

3. **Extend s09 — real tokenizer.** Replace the word-count × 1.3 heuristic in `tokens.go` with [`github.com/pkoukk/tiktoken-go`](https://github.com/pkoukk/tiktoken-go). Add a benchmark comparing heuristic-vs-real estimation error on a corpus of real LLM messages. Note: the threshold logic must keep working with both implementations swapped in.

4. **Extend s14 — blocked-actions report.** Feed s14 classifier verdicts into the s10 event log (`event.Type == "permission_verdict"`). Then write a daily report script that reads `sessions/*.jsonl`, filters to `permission_verdict` events from the previous 24h, and prints a histogram of blocked tools. Pure offline replay over event files.

5. **Phase G — OpenAI provider.** Translate `ChatRequest`/`ChatResponse` against `chat.completions.create` instead of `messages.create`. Differences to plan for: tools nest under `function`, results come back via `tool_calls` not `content[].tool_use`, system message is a regular message with `role="system"` instead of a top-level field. Plug into s02's `provider_test.go`. Once that works, the same pattern extends to DeepSeek, Qwen, or a local vLLM server with the OpenAI-compatible API.

## Caveats

- **SHA is pinned.** The upstream is pinned to sha `86fec9bea430cecb29ff10afaae36b96496a8f8e`; line numbers may drift in newer commits. When quoting in an issue or PR, use the permalink format `https://github.com/nexu-io/harness-engineering-guide/blob/86fec9b/guide/<file>#Lxx-Lyy` so the link doesn't rot.
- **Upstream is markdown only.** There is no working Python source. The Python excerpts you see (in `your-first-harness.md`, `error-handling.md`, etc.) are **illustrative** — running them would require hand-completing imports, fixtures, and `client` setup. Treat them as pseudocode with valid syntax.
- **Some guides are deliberately scoped out.** `sandbox.md`, `eval-awareness.md`, `eval-infrastructure.md`, `nexu-windows-packaging.md`, `agent-teams.md` describe infrastructure or scenarios we did not chapterize. Read them for breadth — they're worth the time — but you won't find matching Go code in `agents/`.
- **Glossary is authoritative.** Terms in `guide/glossary.md` are the source of truth. If Appendix A, any chapter doc, or even this file seems to use a term differently, the glossary wins. Examples to watch: "session" (always means one agent run, never a TCP session), "skill" (always means a SKILL.md bundle, never a generic capability), "harness" (the wrapper code, not the runtime).
