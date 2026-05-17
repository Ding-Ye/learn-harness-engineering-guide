package main

import (
	"errors"
	"fmt"
)

// DispatchFunc is the contract a tool dispatcher must satisfy to be
// wrapped by Guarded. It mirrors the simple shape s03's tool registry
// uses (name + args → string result). The string return is the model's
// observation; an empty string is *not* an error sentinel — every
// implementation must return a non-empty explanation on failure too.
type DispatchFunc func(name string, args map[string]any) string

// Guarded wraps a dispatcher with a Checker pre-check. The wrapper has
// exactly three behaviors:
//
//  1. checker.Check returns ErrNeedsApproval
//     → wrapper returns "Error: needs human approval (tier=<Tier>)"
//     The inner dispatch is NEVER called.
//
//  2. checker.Check returns (Decision{Allow:false, Reason:r}, nil)
//     → wrapper returns "Error: blocked by guardrail: <r>"
//     The inner dispatch is NEVER called.
//
//  3. checker.Check returns (Decision{Allow:true}, nil)
//     → wrapper calls dispatch(name, args) and passes through its
//     string result verbatim.
//
// "Inner dispatch is never called on block" is the load-bearing
// invariant — it's why the guardrail goes in code, not in the prompt:
// the model could be jailbroken into asking for "rm -rf /" via a hundred
// phrasings; the wrapper only sees the post-decode tool call and stops it
// before a single byte of dispatcher logic runs.
//
// On any other error from checker.Check (a real bug, not a policy "no"),
// we also block — fail-closed is the only safe direction for a guardrail.
func Guarded(checker Checker, dispatch DispatchFunc) DispatchFunc {
	return func(name string, args map[string]any) string {
		decision, err := checker.Check(name, args)
		if err != nil {
			if errors.Is(err, ErrNeedsApproval) {
				return fmt.Sprintf("Error: needs human approval (tier=%s)", decision.Tier)
			}
			// Some other checker-side error — also block. Surfacing the
			// raw error here is useful for debugging and keeps the
			// guardrail's failure mode loud rather than silent.
			return fmt.Sprintf("Error: guardrail check failed: %v", err)
		}
		if !decision.Allow {
			return fmt.Sprintf("Error: blocked by guardrail: %s", decision.Reason)
		}
		return dispatch(name, args)
	}
}
