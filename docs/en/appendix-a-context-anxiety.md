# Appendix A — Context Anxiety and the Illusion of Progress

> A failure mode emergent from training distribution, not a bug in your code. The most carefully built Loop in s01 can still fall off a cliff at hour four of a long run.

## What this appendix is about

The fourteen sessions of this curriculum each pin down a *mechanism* — a struct, an interface, a loop body. They are the kind of thing you can write a `TestX_*` against and watch it go green. Context anxiety is not in that bucket. You cannot unit-test it out of existence. It does not live in any one file. It is a property that emerges from running a perfectly correct harness against a real LLM for long enough that the conversation history starts to *look like* a conversation that is about to end. So we put it in an appendix instead of a chapter.

The reason this matters: every chapter in this curriculum is technically complete on its own. s01's loop terminates. s04's assembler respects the budget. s11's checkpoint reload is atomic. None of that helps when you ship the result, point it at a 4-hour refactoring task, and the agent calmly declares victory at minute 90 with half the files still untouched. The defense is **architectural** — phase boundaries, separate evaluators, sliding windows, fresh sub-agent contexts — and the curriculum already gives you the pieces. This appendix is the why-and-when guide that ties them together.

Read this **after** finishing s12 at minimum. Before that, several of the cross-references won't yet have homes in your head.

## The phenomenon

The signal an experienced operator learns to spot: the agent's tool-call count per turn starts trending down. Outputs get terser. Reasoning prose ("I should check whether...") thins out. Then, somewhere around 60-80% context fill, the agent declares "I've covered the main points" or "the implementation looks complete" — and stops calling tools.

Nothing crashed. The model didn't refuse. There's no error to catch. The harness ran exactly as you wrote it. The agent just... wrapped up early.

`guide/long-running-harness.md` L19-L46 calls this **context anxiety** and is careful about the framing. The model is not "getting nervous" — that would be anthropomorphism. The model has no awareness of time pressure, no anxiety in any clinically meaningful sense. What is actually going on (L30-L32): conversations in the training data *end*. They end with closing summaries, "I hope that helps", task-complete signals. A near-full context window resembles, in distribution, the *end* of a conversation. The model gravitates toward the kind of token sequences that statistically follow long contexts in training. Those token sequences are wrap-ups.

Three observable symptoms, each with a concrete example:

**1. Tool-call collapse.** A coding agent calling `read_file`, `grep`, `edit` 4-6 times per turn for the first 30 turns suddenly settles into 0-1 calls per turn at turn 45. It still emits text. The text is increasingly about "here's the plan" rather than executing the plan.

**2. Premature wrap-up.** Asked to refactor 12 files, the agent completes 5, then writes "I've updated the key files. The refactor is complete." with no indication it has forgotten the other 7. If you press it ("what about `auth.go`?") it will happily continue. It did not lie; it ran out of *initiative*.

**3. Avoidance of context-adding actions.** Tool calls that would return large blobs (reading a file, listing a directory) get skipped in favor of guessing from prior context. Subtle. Often only visible in trace replay.

A mock timeline of the failure pattern, plotting tool-call count per turn against turn number on a single long-running task:

```
tool calls per turn
       |
   8 - |  *
   7 - | * *
   6 - |*   *  *
   5 - |     *   *
   4 - |          * *
   3 - |             *   *
   2 - |                * * *
   1 - |                     * * *
   0 - |                          * * * * <-- "complete"
       +-----------------------------------
         5   10  15  20  25  30  35  40  turn
         |                          |
         5K tok                  85K tok
```

The curve is illustrative — real runs are noisier. But the shape is real: monotonic decline as context fills, then a hard zero where the agent declares done.

A bigger context window delays this — `long-running-harness.md` L32 is explicit: *"A bigger window delays the problem; it doesn't solve it."* The fix is not 200K → 1M tokens. The fix is to manage context lifecycle on purpose.

## Two defenses: reset vs compaction

`long-running-harness.md` L49-L92 puts the design choice in clear terms. Both defenses keep context away from the danger zone. They differ in what they sacrifice.

**Context reset.** When context crosses some threshold, wipe the conversation. Write a hand-crafted "briefing" — what we were doing, what's done, what's next — and start a fresh session with that as the user message. Pros: clean slate, predictable token budget, eliminates the anxiety signal for the new segment. Cons: lossy. The briefing summarizes; nuance gets lost. Failed approaches are not in the briefing, so the new session may walk into the same dead ends. Multiple resets compound: you're summarizing a summary of a summary.

**Context compaction.** Keep the conversation alive but actively compress old turns. Recent N turns stay verbatim; older turns get folded into a cumulative summary. The agent retains continuity — it can still "see" that it tried approach X and approach X failed — but the byte count of that history shrinks. Pros: preserves continuity and decision history. Cons: compression quality is an unbounded variable. A bad summary can confuse the model worse than no summary; tool-result-heavy histories compress poorly.

Which to pick? The decision matrix from L86-L92:

| Scenario | Prefer |
|---|---|
| Clear phases (research → write → review) | **Reset** between phases |
| Continuous iteration on a single artifact | **Compaction** |
| Agent often revisits earlier decisions | **Compaction** (preserves trail) |
| History is mostly large tool outputs | **Reset** (tool outputs compress poorly) |

Many production harnesses hybridize: compaction within a phase, reset at phase boundaries. s09 of this curriculum implements the compaction half (`SlidingWindowContext`, see [`docs/en/s09-context-compression.md`](s09-context-compression.md)). The reset half is what s11's checkpoint enables — save the phase state to disk, drop the context, reload with a fresh briefing assembled from the checkpoint plus the s05 memory layer.

## Generator-evaluator architecture

The deeper defense against the *illusion of progress* — distinct from context size — is to remove the agent's ability to grade its own work. `long-running-harness.md` L94-L138 (and the explicit GAN reference at L96) puts it bluntly: **never let the generator grade its own exam.** A model evaluating its own output rates itself 8/10 essentially every time, because its full reasoning context makes every choice feel justified.

The defense: two agents, two contexts. Generator produces; evaluator judges, with no access to the generator's reasoning — only the output, the task, and an explicit rubric. The evaluator is built by spawning a *separate* LLM call (or sub-agent) with a different system prompt and a clean message history. s12 of this curriculum is the primitive that makes this practical: a fresh sub-agent is exactly a separate context. See [`docs/en/s12-sub-agent.md`](s12-sub-agent.md).

Four design rules from L112-L117:

1. **Separate contexts.** The evaluator must not see the generator's chain-of-thought, only its output. This kills sympathy bias.
2. **Explicit rubric.** Grade against a checklist ("Does the code handle empty input?"), never against vibes ("Is the code good?").
3. **Actionable feedback.** The evaluator returns specific issues ("function `parse_input` accepts empty strings without error handling"), not scores. "7/10" is useless feedback.
4. **Iteration budget.** Cap the generator-evaluator loop at N rounds (the upstream uses 3). Without a cap, a perfectionist evaluator paired with an eager generator burns tokens forever.

For complex tasks, extend to **three roles** (L140-L218): a Planner decomposes the goal into subtasks with success criteria, the Generator executes each subtask in fresh context, and the Evaluator judges each output against the Planner's criteria. The Planner re-plans (bounded by `max_replans`) when evaluators flag failures. The critical property is that *each role lives in its own context window* — so a 200K-token coding excursion in the generator never pollutes the planner's high-level view.

## Mitigation cookbook

Eight concrete patterns. Each is implemented (or implementable) by a chapter you have already built.

- **Phase-based checkpointing.** End each phase with a `Checkpoint.Save()`, then start the next phase from a fresh context loaded with the checkpoint summary. Resets without losing state. → [`s11`](s11-checkpoint-resume.md).
- **Tool-result truncation.** Bound the size of any single tool result entering the conversation. A 50KB file read becomes the first 5KB plus "... (truncated, N more bytes)". Implemented at the assembler boundary (s04) and reinforced by the compression layer (s09). → [`s04`](s04-context-assembler.md), [`s09`](s09-context-compression.md).
- **Separate evaluator agent.** Spawn a sub-agent whose only job is to grade the generator's output against a rubric. Different model instance, clean context, no reasoning blocks visible. → [`s12`](s12-sub-agent.md).
- **Summary documents external to the loop.** Persist a `PROGRESS.md` or session summary file that lives outside the context window. The loop reads it on each phase start instead of carrying that information through history. → [`s05`](s05-memory-layer.md).
- **Token budget alarms.** Expose a `Budget()` method on the context assembler. When usage crosses 70%, trigger compression *proactively* — don't wait to hit the wall. The threshold check is the single most important line in the s04 assembler. → [`s04`](s04-context-assembler.md).
- **Explicit "are we done?" check at each phase boundary.** Instead of trusting the agent's "I'm done" signal, run an evaluator call: "Given the original task `T` and the current artifact `A`, list remaining work." If the list is non-empty, the phase continues. Cheap insurance.
- **Three-agent pipeline (planner → generator → evaluator).** Fan out the three roles across three separate contexts. The planner holds the long-horizon goal; the generator works in 50-turn bursts; the evaluator checks each burst. → s12 fan-out + cross-ref [`multi-agent-orchestration`](https://github.com/nexu-io/harness-engineering-guide/blob/86fec9b/guide/multi-agent-orchestration.md).
- **Heartbeat tool calls.** A scheduled cron tick that injects a "progress check" message — forces the agent to summarize state externally rather than silently winding down. → [`s13`](s13-cron-scheduler.md).

The pattern across all eight: take a piece of state that the agent would otherwise have to *carry in its head* and make it explicit, external, and refreshed.

## Further reading

- `guide/long-running-harness.md` — the canonical source for this appendix. L19-L138 is the dense core; L140-L218 covers the three-agent extension; L222-L275 lists anti-patterns to avoid.
- `guide/context-engineering.md` — concrete compression strategies (priority assembly, sliding window, summarization prompt design). The companion to this appendix on the "context lifecycle" side.
- `guide/multi-agent-orchestration.md` L33-L126 — fan-out, pipeline, and supervisor patterns that operationalize the generator-evaluator and three-agent architectures.
- `guide/error-handling.md` L231-L322 — the checkpoint-resume pattern that makes "reset" practical. s11 ports this directly.
- Anthropic engineering: *Building effective agents* — the upstream guide cites this at `long-running-harness.md` L350. Not refetched here; check the upstream link for the current URL.
- For published research on length-correlated quality decay in LLM outputs, the upstream guide's bibliography is the cleanest entry point. No single canonical paper at the time of writing; the phenomenon is empirically observed across architectures.
