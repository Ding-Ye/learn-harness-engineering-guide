# s06 upstream excerpt: guardrails.md L22-L116 (three permission models)

Source: `guide/guardrails.md` L22-L116 in `nexu-io/harness-engineering-guide`
Permalink: <https://github.com/nexu-io/harness-engineering-guide/blob/86fec9bea430cecb29ff10afaae36b96496a8f8e/guide/guardrails.md#L22-L116>
License: MIT (© 2026 Nexu)

```markdown
## The Trust Boundary Model

Every harness has a trust boundary between the model and the operating environment. The harness mediates all crossings:

    ┌──────────────────────────────────┐
    │           MODEL SPACE            │
    │  (reasoning, tool call requests) │
    └────────────┬─────────────────────┘
                 │ tool call request
                 ▼
    ┌──────────────────────────────────┐
    │         GUARDRAIL LAYER          │
    │  Permission check → Allow/Deny   │
    └────────────┬─────────────────────┘
                 │ approved call
                 ▼
    ┌──────────────────────────────────┐
    │        EXECUTION SPACE           │
    │  (filesystem, network, shell)    │
    └──────────────────────────────────┘

The guardrail layer intercepts every tool call before execution. It can:
- Allow   — execute as requested
- Deny    — return an error to the model
- Modify  — rewrite the call (e.g., restrict file path to a safe directory)
- Prompt  — ask the human for approval before proceeding

## Permission Models

### Allow-list (Strictest)

ALLOWED_TOOLS = {
    "read_file":   {"paths":    ["/workspace/**"]},
    "write_file":  {"paths":    ["/workspace/**"]},
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

### Deny-list (Permissive)

BLOCKED_PATTERNS = [
    (r"rm\s+-rf\s+/",          "Refusing to delete root filesystem"),
    (r"curl.*\|\s*sh",         "Refusing to pipe remote script to shell"),
    (r"env\s+|printenv|echo\s+\$", "Refusing to expose environment variables"),
]

def check_command(command):
    for pattern, reason in BLOCKED_PATTERNS:
        if re.search(pattern, command):
            return False, reason
    return True, ""

### Tiered Approval

| Risk Level | Examples                                        | Action               |
|-----------|-------------------------------------------------|----------------------|
| Low       | Read files, search                              | Auto-approve         |
| Medium    | Write files, run tests                          | Auto-approve + log   |
| High      | Execute shell commands, network requests        | Human approval       |
| Critical  | Delete files, push to git, send messages        | Explicit approval    |

def get_risk_level(tool_name, args):
    if tool_name == "read_file":  return "low"
    if tool_name == "write_file": return "medium"
    if tool_name == "run_command":
        cmd = args.get("command", "")
        if any(k in cmd for k in ["rm", "git push", "curl"]):
            return "critical"
        return "high"
    return "medium"
```

## Reading notes

1. **The four verbs — Allow, Deny, Modify, Prompt — are not the same shape.** Allow/Deny return a boolean; Modify *rewrites* the call (`{path: "/etc/passwd"}` → `{path: "/workspace/etc/passwd"}`); Prompt blocks the loop on a UI await. Our Go port implements Allow, Deny, and Prompt (the last via `ErrNeedsApproval`). We deliberately skip Modify — it muddies the "guardrail vs. translator" boundary and the tests for it ("did the harness rewrite the args in the way the policy says?") double the chapter's surface area without teaching anything new.

2. **The Python allow-list is a single function; ours is a `Checker` interface.** Why bother with an interface for one mode? Because the wrapper (`Guarded()`) wants to compose. If you stack `Guarded(allow, Guarded(deny, dispatch))`, both checkers need the same signature or the wrapper has to grow knobs. Go's lack of structural typing means the interface is explicit; once it exists, swapping in s14's classifier costs zero lines of plumbing.

3. **Deny-list patterns are signatures, not grammars.** Note the upstream `re.search` (not `re.fullmatch`): `rm -rf /tmp/x` matches `rm\s+-rf\s+/` because of the `/` in `/tmp/`. That's intentional — a deny-list is paranoid by design. The frustrated-but-safe case is "your `rm -rf /tmp/cache` got blocked"; the costly case is "your `rm -rf /` didn't get blocked because the regex was too tight". Tilt toward false positives.

4. **The tiered table conflates *tool identity* and *args content*.** Upstream's `get_risk_level` does both — read_file is always low, but run_command can flip from high to critical depending on the command string. Our Go `TieredChecker` only does tool-identity mapping (one tier per tool name). The args-content slice belongs to the deny-list checker — they can stack. Splitting the responsibility makes each piece simpler to reason about; the integration test (s_full) chains both.

5. **`return True` at L72 is the "open by default" trap.** If a tool has no `paths` and no `commands` policy, the upstream check returns True — allowed. That's the right behavior for tools without sensitive args (e.g. `list_models`), but it means a misconfigured policy where you forgot to add a path constraint silently widens the trust boundary. Our Go port preserves this semantic but documents it as a known footgun in `allowlist.go`'s comment block.

## Reading map

| Topic | Upstream file | Lines | Mapped chapter |
|-------|---------------|-------|----------------|
| Why guardrails exist (threat model) | `guide/guardrails.md` | L9-L20 | s06 (background) |
| Trust boundary diagram + 4 verbs | `guide/guardrails.md` | L22-L49 | s06 (this chapter) |
| Allow-list mode | `guide/guardrails.md` | L51-L73 | s06 |
| Deny-list mode | `guide/guardrails.md` | L75-L91 | s06 |
| Tiered approval | `guide/guardrails.md` | L93-L116 | s06 |
| Sandboxing (Docker/Firecracker/gVisor/WASM) | `guide/guardrails.md` | L118-L131 | (out of scope; OS-level, not a code mechanism) |
| Input sanitisation (`<tool_result>` markers) | `guide/guardrails.md` | L133-L145 | (touched in s09's compression) |
| Common pitfalls | `guide/guardrails.md` | L147-L153 | s06 (referenced in docs) |
| Model-based replacement | `guide/classifier-permissions.md` | L29-L169 | s14 |
