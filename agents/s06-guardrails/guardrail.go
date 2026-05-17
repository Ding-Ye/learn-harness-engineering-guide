package main

import "errors"

// Decision is the verdict a Checker returns for a single tool call.
//
// Allow == true   → dispatch may proceed.
// Allow == false  → dispatch is blocked; Reason explains why and is shown
//                   back to the model (so it can pick a different approach).
// Tier            → optional risk label for tiered checkers ("low",
//                   "medium", "high", "critical"). Upstream
//                   guardrails.md L96-L116 enumerates the standard tiers.
//
// A Decision is purely metadata — the wrapper (dispatch_wrapper.go) decides
// what to do with it. This keeps checkers stateless and composable.
type Decision struct {
	Allow  bool
	Reason string
	Tier   string
}

// Checker is the trust-boundary interface from guardrails.md L22-L49.
// Every tool call crosses the boundary; every crossing goes through Check.
//
// The error return is reserved for *out-of-band* signals — most importantly
// ErrNeedsApproval, which the tiered checker emits for "critical" tools
// so the wrapper can route them to a human review queue instead of
// returning a plain block string. Regular "policy says no" is a Decision
// with Allow=false, not an error.
type Checker interface {
	Check(toolName string, args map[string]any) (Decision, error)
}

// ErrNeedsApproval is the sentinel a Checker returns when a tool call
// passes static policy but requires human sign-off before execution. The
// dispatch wrapper translates this to a distinct error string so the
// caller (or a UI on top of the harness) can wire in an approval queue.
//
// This matches the upstream "Tiered Approval" table in guardrails.md
// L96-L116, specifically the "Critical" row: "Always require explicit
// approval".
var ErrNeedsApproval = errors.New("needs human approval")
