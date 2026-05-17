package main

import "fmt"

// Risk tiers from guardrails.md L96-L116. Treated as a string enum rather
// than a Go type so the JSON policy stays human-readable. Unknown tiers
// fall through to "medium" (auto-approve with logging) — the safe default
// for an under-specified policy is *not* "critical" because that would
// effectively halt the agent on any uncatalogued tool.
const (
	TierLow      = "low"
	TierMedium   = "medium"
	TierHigh     = "high"
	TierCritical = "critical"
)

// TieredChecker maps every tool call to a risk tier and decides what to
// do with it:
//
//	low      → Allow=true,  Reason="low risk, auto-approved"
//	medium   → Allow=true,  Reason="medium risk, auto-approved with logging"
//	high     → Allow=true,  Reason="high risk, approved (require external review)"
//	critical → returns ErrNeedsApproval so the wrapper can hand the call
//	           off to a human approval queue instead of executing.
//
// Why does "high" still allow? Upstream describes high as "Require human
// approval" in the table (L101) — but the *mechanism* for that approval
// is application-specific (a UI, a slackbot, an out-of-band CLI). This
// checker draws the line conservatively at "critical": stop the agent in
// code. A real production deployment would chain a UI prompt for "high"
// on top of this checker; we keep the chapter small by treating "high" as
// "approved with a loud log line".
type TieredChecker struct {
	policy *Policy
}

// NewTieredChecker wraps a Policy. policy.ToolTiers is the only field
// consulted.
func NewTieredChecker(p *Policy) *TieredChecker {
	return &TieredChecker{policy: p}
}

// Check looks up the tool's tier and returns the decision documented on
// the type. For TierCritical, the err is ErrNeedsApproval; the Decision
// still carries the tier so the wrapper can format a useful message.
func (c *TieredChecker) Check(toolName string, args map[string]any) (Decision, error) {
	tier := TierMedium
	if c.policy != nil {
		if t, ok := c.policy.ToolTiers[toolName]; ok {
			tier = t
		}
	}

	switch tier {
	case TierLow:
		return Decision{Allow: true, Tier: tier, Reason: "low risk, auto-approved"}, nil
	case TierMedium:
		return Decision{Allow: true, Tier: tier, Reason: "medium risk, auto-approved with logging"}, nil
	case TierHigh:
		return Decision{Allow: true, Tier: tier, Reason: "high risk, approved (require external review)"}, nil
	case TierCritical:
		// The Decision.Allow value is moot once err != nil — the wrapper
		// handles ErrNeedsApproval before consulting Allow. Setting it
		// false here is defense-in-depth in case a future caller forgets
		// to check err first.
		return Decision{
			Allow:  false,
			Tier:   tier,
			Reason: fmt.Sprintf("tool %q is critical-tier; awaiting human approval", toolName),
		}, ErrNeedsApproval
	default:
		// Unknown tier label — treat as medium so a typo doesn't halt the
		// agent. A real harness would also emit a metric here.
		return Decision{
			Allow:  true,
			Tier:   TierMedium,
			Reason: fmt.Sprintf("unknown tier %q for tool %q; defaulting to medium", tier, toolName),
		}, nil
	}
}
