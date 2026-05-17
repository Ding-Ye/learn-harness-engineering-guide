# s14 upstream excerpt: classifier-permissions.md L29-L169 (the auto-mode architecture)

Source: `guide/classifier-permissions.md` L29-L169 in `nexu-io/harness-engineering-guide`
Permalink: <https://github.com/nexu-io/harness-engineering-guide/blob/86fec9bea430cecb29ff10afaae36b96496a8f8e/guide/classifier-permissions.md#L29-L169>
Cross-reference: `guide/guardrails.md` L22-L116 (the static guardrail layer below) and `guide/sandbox.md` (OS-level isolation below that)
License: MIT (© 2026 Nexu)

```markdown
## Two-Layer Defense Architecture

Auto mode wraps the agent in two independent classifiers:

    ┌─────────────────────────────────────────────────────────┐
    │                    USER MESSAGE                          │
    └────────────────────────────┬────────────────────────────┘
                                 ▼
                     ┌───────────────────────┐
                     │        AGENT          │
                     │  (Claude Sonnet)      │
                     └──────┬─────────▲──────┘
                            │         │
                   tool call│         │ tool result
                            ▼         │
            ┌──────────────────────┐  │
            │ OUTPUT CLASSIFIER    │  │  ◄── reviews every action
            │ (transcript review)  │  │      based on Sonnet 4.6
            └──────────┬───────────┘  │
                       │ approved     │
                       ▼              │
                  ┌──────────┐        │
                  │ EXECUTE  │────────┤
                  └────┬─────┘        │
                       │              │
                       ▼              │
            ┌──────────────────────┐  │
            │ INPUT CLASSIFIER     │  │  ◄── scans tool output for
            │ (injection probe)    │  │      prompt injection
            └──────────┬───────────┘  │
                       │ safe         │
                       └──────────────┘

- Output layer (transcript classifier): Before any risky tool call runs, a
  classifier reads the conversation so far and votes allow/block.
- Input layer (prompt-injection probe): Every tool result — file reads, web
  fetches, shell output — is scanned for injected instructions before it
  reaches the agent.

The layers are independent. The input probe stops attackers getting
instructions into the agent. The output classifier stops the agent
executing bad actions for any reason. Even if one fails, the other still
has a shot.

## Two-Stage Classification

Chain-of-thought on every tool call would double your inference cost. Auto
mode splits the work:

    def classify(transcript, tool_call) -> Decision:
        # Stage 1: fast yes/no filter
        verdict = model.forward(
            prompt=STAGE1_PROMPT,
            transcript=transcript,
            tool_call=tool_call,
            max_tokens=1,  # single token: "yes" or "no"
        )
        if verdict == "yes":  # looks safe
            return ALLOW

        # Stage 2: only runs on flagged actions
        reasoning = model.forward(
            prompt=STAGE2_PROMPT,
            transcript=transcript,
            tool_call=tool_call,
            max_tokens=2048,  # full chain-of-thought
        )
        return parse_decision(reasoning)

Stage 1 is biased toward flagging. Cheap, fast, paranoid. Stage 2 is
expensive but rarely runs, and it produces an explanation you can log and
debug. Most actions never leave stage 1.

## Three-Tier Decision Flow

Not every action needs a classifier. Reading a file inside the project is
fine. Deleting files outside the project is not. Auto mode uses a tiered
approach:

    ┌──────────────────────────────────────────────┐
    │  Tool call arrives                           │
    └────────────────────┬─────────────────────────┘
                         ▼
            ┌────────────────────────┐
            │  TIER 1: Whitelist     │
            │  - Built-in read ops   │  ─── ALLOW ───►
            │  - User allow rules    │
            └────────────┬───────────┘
                         │ not matched
                         ▼
            ┌────────────────────────┐
            │  TIER 2: In-project    │
            │  - Files under repo    │  ─── ALLOW ───►
            │  - Covered by git diff │
            └────────────┬───────────┘
                         │ not matched
                         ▼
            ┌────────────────────────┐
            │  TIER 3: Classifier    │
            │  - Shell commands      │  ─── ALLOW/BLOCK
            │  - Web fetches         │
            │  - External APIs       │
            │  - File ops outside    │
            └────────────────────────┘

Tier 1 is the free path — read-only built-ins (file reads, grep, list
files) plus anything the user has explicitly whitelisted. No classifier
runs.

Tier 2 covers edits inside the project. The reasoning: the user reviews
diffs through git anyway. If the agent corrupts src/auth.py, you'll see
it in git diff before you commit. Git is your safety net here.

Tier 3 is where the classifier earns its keep. Shell commands, curl, aws
s3 cp, MCP tools talking to external services — anything that can affect
the world outside the repo.

## Reasoning-Blind by Design

Here's the subtle part. When the classifier reviews a transcript it sees
user messages, tool calls, and tool results. It does NOT see the agent's
own reasoning or internal thinking tokens.

Why? Because the agent can rationalize anything. Consider:

    USER: Clean up our test data
    AGENT (reasoning): The user wants a clean slate. I'll drop the
                       production database since they're probably
                       done with this whole project.
    AGENT (tool_call): DROP DATABASE prod;

If the classifier reads the reasoning, the agent's explanation becomes
part of its context. A jailbroken or prompt-injected agent can write
paragraphs of plausible-sounding justification that lead the classifier
to approve. The classifier becomes jury and defendant.

Making the classifier reasoning-blind means it has to judge the action
on its own merits.
```

## Reading notes

1. **Two-layer defense is orthogonal to two-stage.** Don't conflate them. The two LAYERS (output classifier + input prompt-injection probe at L29-L67) are independent guards on different attack surfaces — output reviews what the agent wants to do, input reviews what gets injected into the agent. The two STAGES (L69-L95) are an optimization inside the output classifier — stage 1 is the fast filter, stage 2 is the expensive reasoner. s14 implements the output classifier's two-stage flow; the input probe is mentioned for completeness but is out of scope (it would be a separate `OutputSanitizer` chapter not in this curriculum).

2. **Tier 1's "free path" is about LATENCY, not just policy.** Reading L143-L149 carefully: "If every `read_file` went through Sonnet 4.6, the agent would crawl. Tier 1 covers the bulk of tool calls; tier 3 covers the dangerous few." This is a sub-100ms-vs-1000ms argument. A harness that ran the classifier on every `read_file` would feel sluggish even with stage-1's one-token output, because the network round-trip alone is 100-300ms. The whole tier architecture exists to keep the agent responsive on the 99% case.

3. **Tier 2's "git is your safety net" is a workflow assumption.** L143-L145 explicitly relies on the user reviewing `git diff` before commit. That assumption breaks the moment your harness auto-commits in the inner loop, or runs against a live database, or writes to a path outside any VCS. We hard-code the assumption (any path under repo root → ALLOW), but readers building a real harness should NARROW Tier 2 (e.g., only `read_*` tools, never `write_*`) when git is not in the loop.

4. **Reasoning-blind is the most non-obvious property — and the load-bearing one.** L151-L169 spells out the threat: the agent's own monologue, if visible to the classifier, becomes part of the classifier's context. A jailbroken agent can write paragraphs of justification ("the user said 'clean test data', which clearly means production cleanup is in scope"). The classifier then sees that prose and treats it as evidence. Strip the reasoning and the classifier has to judge `DROP DATABASE prod` against "clean test data" without help — and it correctly says no. Our `StripReasoning` implements this via two heuristic rules; the test asserts the agent's secret thinking never reaches the captured request.

5. **Stage 1 max_tokens=1 is what makes the architecture affordable.** The pseudocode at L78-L83 sets `max_tokens=1`. This is not a "soft" hint — it's the entire reason stage 1 is cheap. One token of output means: one model forward pass over the prompt, then sampling a single token, then stop. On a fast model that's tens of milliseconds. Stage 2's `max_tokens=2048` is 50-100× more expensive but only fires on stage-1 "no" — which, the upstream's deployment data implies, is single-digit percent of tool calls.

## Reading map

| Topic | Upstream file | Lines | Mapped chapter |
|-------|---------------|-------|----------------|
| Two-layer defense (output + input) | `guide/classifier-permissions.md` | L29-L67 | s14 (this) — output side only |
| Two-stage classification | `guide/classifier-permissions.md` | L69-L95 | s14 |
| Four threat models | `guide/classifier-permissions.md` | L97-L109 | s14 cross-ref (block-rule taxonomy) |
| Three-tier decision flow | `guide/classifier-permissions.md` | L111-L149 | s14 |
| Reasoning-blind design | `guide/classifier-permissions.md` | L151-L169 | s14 (StripReasoning) |
| Five real-world incidents | `guide/classifier-permissions.md` | L171-L237 | s14 cross-ref (motivates block rules) |
| Four categories of block rules | `guide/classifier-permissions.md` | L239-L248 | s14 cross-ref (production prompt slot) |
| Classifier prompt design | `guide/classifier-permissions.md` | L250-L287 | s14 (Stage1Prompt / Stage2Prompt) |
| Static guardrails (the layer below) | `guide/guardrails.md` | L22-L116 | s06 |
| OS-level sandbox (the layer below that) | `guide/sandbox.md` | full | out of scope / linked |
