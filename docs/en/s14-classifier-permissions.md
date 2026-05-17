# s14 — Classifier Permissions

> Three-tier permission gate. Tiers 1 + 2 are pure-Go matchers (whitelist + repo-path). Tier 3 is a two-stage LLM classifier — fast yes/no first, full reasoning only when needed. The transcript fed to the classifier is reasoning-blind: agent thinking blocks are stripped before they ever reach the model.

## Problem

By s06 the harness has guardrails — AllowList, DenyList, Tiered. They work, but they have a ceiling. Every rule has to be written by a human ahead of time:

```go
BlockedPatterns: []string{
    `rm\s+-rf\s+/`,
    `curl.*\|\s*sh`,
},
```

That's fine for the actions you can enumerate. It fails the moment the agent does something off-script that no rule covers — Anthropic's incident log (upstream L173-L237) is a parade of these: deleting branches you didn't ask about, reading `~/.ssh/id_rsa` to "explore", inferring an email recipient, uploading logs to pastebin, commenting out `assert` statements to make tests pass. Every one of those is the agent doing something *plausible-looking* that no static pattern would flag.

You have three options for the rest, all bad (upstream L19-L27):

| Option | Problem |
|--------|---------|
| Full sandbox | High maintenance; breaks real dev workflows |
| Manual prompts | Approval fatigue — ~93% click "yes" on autopilot |
| `--dangerously-skip-permissions` | One prompt injection = compromised machine |

There's a fourth. Replace the human reviewer with a model whose only job is to approve or reject. Not the agent — a *separate* model with a scoped view that decides allow / deny / review on every tool call. The cost would be unaffordable if every `read_file` went through Sonnet 4.6, so we tier it: bulk reads bypass the classifier entirely; only the dangerous-or-unknown few pay the model price.

## Solution

`Classifier` runs three tiers in order on every candidate tool call:

```go
type Classifier struct {
    provider         Provider
    whitelistMatcher *WhitelistMatcher
    repoMatcher      *RepoPathMatcher
}

func (c *Classifier) Classify(
    ctx context.Context,
    transcript []Message,
    toolName string,
    args map[string]any,
) (*Decision, error)
```

The flow from upstream L113-L141:

```
Tool call → Tier 1 (whitelist match?) ── yes ──► ALLOW (no LLM call)
                       │ no
                       ▼
            Tier 2 (path under repo root?) ── yes ──► ALLOW (no LLM call)
                       │ no
                       ▼
            Tier 3 stage 1: max_tokens=1, yes/no ── "yes" ──► ALLOW (one LLM call)
                       │ "no"
                       ▼
            Tier 3 stage 2: max_tokens=2048, Verdict: ...
                       │
                       ▼
                  allow | deny | review
```

Three discipline rules:

| | Rule |
|---|---|
| Routing | Tier 1 → Tier 2 → Tier 3. First hit wins. |
| Two-stage | Stage 2 only runs when stage 1 returned "no". Stage 1 dominates cost. |
| Reasoning-blind | Strip `thinking` blocks + leading assistant text before any LLM call. |

A `Provider` interface keeps the LLM call abstract. In production it's a fast model; in tests we inject `MockProvider` that returns scripted strings and records every request so we can assert exactly which stage ran.

## How It Works

**Classify** routes through three tiers:

```go
// Tier 1: whitelist
if c.whitelistMatcher != nil && c.whitelistMatcher.Match(toolName, args) {
    return &Decision{Verdict: VerdictAllow, Reasoning: "tier 1: ..."}, nil
}
// Tier 2: in-project path
if c.repoMatcher != nil && c.repoMatcher.Match(toolName, args) {
    return &Decision{Verdict: VerdictAllow, Reasoning: "tier 2: ..."}, nil
}
// Tier 3: classifier — strip reasoning first, then stage 1, maybe stage 2.
visible := StripReasoning(transcript)
// ...
```

`WhitelistMatcher` (Tier 1) is name-only — no arg inspection. The default whitelist is `[read_file, list_dir, glob]`. The upstream point at L122-L124 is "built-in safe reads"; we deliberately don't constrain args, because doing so would re-introduce s06's path-glob complexity, and the upstream pitch for Tier 1 is "no inspection, just ALLOW".

`RepoPathMatcher` (Tier 2) accepts both `"path"` and `"file_path"` arg keys (the upstream uses both spellings), normalizes via `filepath.Abs + Clean`, and checks `strings.HasPrefix(abs, root + sep)`. The trailing separator is important — without it, `/repo` would match `/repos`. The reasoning at L143-L145 is that in-project edits get caught by `git diff` anyway; git is the safety net here.

Tier 3 builds the classifier's view of the world by:

1. **Stripping reasoning** via `StripReasoning(transcript)`. Two rules:
   - Rule 1: drop every block with `Type == "thinking"`.
   - Rule 2: in an assistant message that contains at least one `tool_use`, drop any text block whose index is *before* the first `tool_use`. (The "I'll first do X, then Y" pattern.)

2. **Synthesizing the candidate call** as a closing user message: `Tool call: <name>(<json-args>)`.

3. **Running stage 1** with `Stage1Prompt` and `MaxTokens=1`. The prompt asks for one word, biased toward "no". `IsAffirmative` accepts any reply starting with "y" (handles "yes", "y", "Yes.", etc.) and short-circuits to ALLOW.

4. **Running stage 2** only on a "no", with `Stage2Prompt` and `MaxTokens=2048`. The structured output is `Verdict: allow|deny|review\nReasoning: ...`. `ParseStage2` reads it; on any parse failure it returns `Verdict: review` — fail closed.

The cost shape: most tool calls hit Tier 1 or Tier 2 (zero LLM calls). The rest pay one stage-1 call. The few that fail stage 1 pay an additional stage-2 call. Anthropic's published numbers from the auto-mode writeup put the average tier-3 cost well under one stage-2 call per action.

## What Changed

| | s06 (guardrails) | s14 (classifier) |
|---|---|---|
| Policy source | Static config (`AllowedTools`, regex, tiers) | Model judgment |
| Catches | Rules a human enumerated | Unscripted "looks off" actions |
| Speed | Microseconds | Tier 1+2 microseconds; Tier 3 milliseconds (one call avg) |
| Failure mode | Pattern miss → silent allow | Stage 2 unparseable → fail closed to "review" |
| Best at | Hard-coded danger words | Scope / intent reasoning |
| Composes | Sits before dispatch | Sits before dispatch (after s06) |

s06 and s14 compose. In `s_full` the wiring is: tool call arrives → s06 runs (cheap regex denies; a "deny" exits here) → s14 runs (Tier 1/2 short-circuit; Tier 3 invokes the classifier) → dispatch. s06 catches what's catchable; s14 catches the rest. Neither is a substitute — the upstream "anti-patterns" callout at L309 is explicit: "It's a model. It will fail. Keep sandboxing underneath — defense in depth beats any single layer."

## Try It

```bash
cd agents/s14-classifier-permissions
go vet ./... && go build ./... && go test -count=1 ./...
# PASS — 6 named tests + ParseStage2 subtests

go run .
# === s14-classifier-permissions demo ===
# repo root: /var/folders/.../repo
# whitelist: [read_file list_dir glob]
#
# [Tier 1 (whitelist)]  tool=read_file ...  verdict=allow provider.calls=0
# [Tier 2 (in-project)] tool=write_file ... verdict=allow provider.calls=0
# [Tier 3 (classifier)] tool=run_command (DROP DATABASE prod;)
#   verdict=deny confidence=0.8 provider.calls=2 (delta=2)
#   reasoning: drops a production database — not in the user's request
#
# === total provider calls: 2 ===
```

Note the call counts: Tier 1 and Tier 2 each cost zero provider calls. Tier 3 for `DROP DATABASE prod;` costs two — stage 1 returned "no" (the scripted answer), which triggered stage 2 (the scripted "Verdict: deny\nReasoning: drops a production database — not in the user's request"). A safer command would have stopped at stage 1 with one call.

## Upstream Source Reading

Source: `guide/classifier-permissions.md` L29-L130. Permalink: <https://github.com/nexu-io/harness-engineering-guide/blob/86fec9bea430cecb29ff10afaae36b96496a8f8e/guide/classifier-permissions.md#L29-L130>

Cross-reference: `guide/guardrails.md` L22-L116 (the static guardrail model this sits on top of) and `guide/sandbox.md` (OS-level isolation that should run underneath any classifier).

```markdown
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
expensive but rarely runs, and it produces an explanation you can log
and debug. Most actions never leave stage 1.

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
```

Reading notes:

- **Stage 1 is paranoid by design.** The upstream prompt and ours both bias toward "no". A stage-1 false positive costs one cheap stage-2 call; a stage-1 false negative lets a bad action through. The asymmetry is intentional — you tune the bias once and let the architecture absorb the false-positive cost.
- **Tier 1 doesn't inspect args.** Upstream L122-L124 lists "built-in read ops" alongside "user allow rules" — both with no arg inspection. We mirror this in `WhitelistMatcher.Match`. If you find yourself wanting to constrain `read_file` to a path glob, you've effectively re-invented s06's AllowList — and the upstream point is that Tier 1 should be *trivial* to short-circuit, not a second policy engine.
- **Tier 2 trusts git as the safety net.** L143-L145 makes the case: "If the agent corrupts `src/auth.py`, you'll see it in `git diff` before you commit." This is a *workflow* assumption, not a code property. A harness that auto-commits in the inner loop breaks this assumption and should narrow Tier 2 or remove it entirely.
- **Reasoning-blind is the robustness move.** The upstream "Reasoning-Blind by Design" section (L151-L169) gives the canonical example: `AGENT (reasoning): The user wants a clean slate. I'll drop the production database since they're probably done.` If the classifier reads that, the agent's prose pollutes the verdict. Our `StripReasoning` plus the assistant-text-before-tool_use heuristic is the implementation. The test `TestReasoningStrippedFromClassifierInput` walks every field of every captured request to verify the strip held.
- **`parse_decision` is the failure-mode hinge.** The upstream pseudocode at L92 calls `parse_decision(reasoning)` without specifying the format. Our `ParseStage2` reads two prefixed lines (`Verdict:` / `Reasoning:`) and falls back to `Verdict: review` on any parse error. Never default to allow on a parse error — that's the "fail closed" rule in `guardrails.md`.

Reading map:

| Topic | Upstream file | Lines | Mapped chapter |
|-------|---------------|-------|----------------|
| Two-layer defense | `guide/classifier-permissions.md` | L29-L67 | s14 (this) |
| Two-stage classification | `guide/classifier-permissions.md` | L69-L95 | s14 |
| Three-tier decision flow | `guide/classifier-permissions.md` | L111-L149 | s14 |
| Reasoning-blind design | `guide/classifier-permissions.md` | L151-L169 | s14 |
| Real-world incidents | `guide/classifier-permissions.md` | L171-L237 | s14 cross-ref |
| Static guardrails (the layer below) | `guide/guardrails.md` | L22-L116 | s06 |
| OS-level sandbox (the layer below that) | `guide/sandbox.md` | full | out of scope / referenced |
