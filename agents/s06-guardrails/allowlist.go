package main

import (
	"fmt"
	"strings"
)

// AllowListChecker is the strict mode from guardrails.md L51-L73.
// Default-deny: a tool must be listed in policy.AllowedTools (and, if it
// takes a "path" arg, the path must match one of policy.AllowedPathGlobs)
// for the call to be allowed. Anything not explicitly permitted is denied.
//
// This is the right mode for managed agents, hosted demos, and any context
// where the operator can enumerate the legitimate tool surface ahead of
// time. The upstream Python sketch (L57-L72) only checks `paths` when the
// args dict carries one — we preserve that semantics: tools that don't
// take a path are allowed unconditionally so long as they're in the
// AllowedTools map.
type AllowListChecker struct {
	policy *Policy
}

// NewAllowListChecker wraps a Policy. The caller retains ownership of the
// policy — the checker only reads it. Passing nil is a usage error.
func NewAllowListChecker(p *Policy) *AllowListChecker {
	return &AllowListChecker{policy: p}
}

// Check returns Allow=true only when both:
//  1. toolName is in policy.AllowedTools with a true value, AND
//  2. if args carries a string "path", at least one of
//     policy.AllowedPathGlobs matches it. If no path globs are configured,
//     the path is not constrained.
//
// Returns nil error in all cases — denials are reported via Decision.Allow,
// not via err. The error channel is reserved for ErrNeedsApproval (tiered).
func (c *AllowListChecker) Check(toolName string, args map[string]any) (Decision, error) {
	if c.policy == nil || !c.policy.AllowedTools[toolName] {
		return Decision{
			Allow:  false,
			Reason: fmt.Sprintf("tool %q is not in the allow-list", toolName),
		}, nil
	}

	// Path-constrained tools must satisfy AllowedPathGlobs. We only check
	// when the caller actually passed a "path" arg AND globs are defined —
	// tools without a path (e.g. "list_models") fall through to allow.
	if path, ok := args["path"].(string); ok && len(c.policy.AllowedPathGlobs) > 0 {
		if !anyMatch(c.policy.AllowedPathGlobs, path) {
			return Decision{
				Allow: false,
				Reason: fmt.Sprintf(
					"path %q does not match any allowed glob %v",
					path, c.policy.AllowedPathGlobs,
				),
			}, nil
		}
	}

	return Decision{Allow: true, Reason: "allowed"}, nil
}

// anyMatch reports whether path matches at least one glob in patterns.
func anyMatch(patterns []string, path string) bool {
	for _, p := range patterns {
		if globMatch(p, path) {
			return true
		}
	}
	return false
}

// globMatch implements the small glob dialect we accept in
// AllowedPathGlobs. Two wildcards:
//
//	*   matches zero or more characters within a single path segment
//	    (does NOT cross "/")
//	**  matches zero or more characters across path segments
//	    (CAN cross "/")
//
// Anything else is literal. We deliberately avoid pulling in a
// `doublestar` library — the implementation is short enough to read and
// the upstream Python equivalent (fnmatch) is similarly minimal.
//
// Examples:
//
//	globMatch("/workspace/**",        "/workspace/a/b/c.txt") → true
//	globMatch("/workspace/**",        "/workspace")           → false (requires the "/" prefix)
//	globMatch("/workspace/**",        "/etc/passwd")          → false
//	globMatch("/workspace/*.go",      "/workspace/main.go")   → true
//	globMatch("/workspace/*.go",      "/workspace/sub/x.go")  → false ("*" must not cross /)
//
// The algorithm: split on "**" → an alternating sequence of single-segment
// patterns and "any chars across segments" gaps. Walk the path consuming
// the literal/star pieces; the gaps slide greedily to find a fit.
func globMatch(pattern, path string) bool {
	parts := strings.Split(pattern, "**")
	return matchAcross(parts, path)
}

// matchAcross consumes the doublestar-split pattern pieces against path.
// parts is non-empty; gaps between adjacent parts mean "zero or more
// arbitrary characters, may cross '/'."
func matchAcross(parts []string, path string) bool {
	if len(parts) == 1 {
		return segmentMatch(parts[0], path)
	}
	first := parts[0]
	rest := parts[1:]

	// The first piece anchors at the start of path. Find every prefix of
	// path that matches `first`, then try matchAcross(rest, suffix).
	for end := 0; end <= len(path); end++ {
		if segmentMatch(first, path[:end]) {
			// rest[0] must appear somewhere in path[end:]; the "**" gap
			// before rest[0] means we can skip any number of chars.
			if matchAcrossAfterGap(rest, path[end:]) {
				return true
			}
		}
	}
	return false
}

// matchAcrossAfterGap is matchAcross but the next piece may consume an
// arbitrary prefix of `path` (the "**" gap before it absorbs anything we
// skip). We slide the start of the next piece across `path` and recurse.
func matchAcrossAfterGap(parts []string, path string) bool {
	if len(parts) == 0 {
		return true // nothing left to match; gap eats the rest.
	}
	next := parts[0]
	tail := parts[1:]
	// Slide a starting position across `path`. At each position, try every
	// length k such that segmentMatch(next, path[start:start+k]) is true,
	// then recurse on the rest.
	for start := 0; start <= len(path); start++ {
		for k := 0; start+k <= len(path); k++ {
			if segmentMatch(next, path[start:start+k]) {
				// After matching `next`, the next gap (if any) handles
				// the remainder.
				if matchAcrossAfterGap(tail, path[start+k:]) {
					return true
				}
			}
		}
	}
	return false
}

// segmentMatch matches a pattern with optional "*" wildcards (no "**")
// against the full string s. "*" inside a segment matches zero or more
// non-"/" characters; literal characters must match exactly.
func segmentMatch(pattern, s string) bool {
	// Fast path: no wildcards → literal compare.
	if !containsStar(pattern) {
		return pattern == s
	}
	// Recursive star-matching. pi/si are pattern/string indices.
	var match func(pi, si int) bool
	match = func(pi, si int) bool {
		for pi < len(pattern) {
			pc := pattern[pi]
			if pc == '*' {
				// Try consuming 0..N non-"/" chars of s.
				// k=0 first (zero-length match), then grow.
				for k := 0; si+k <= len(s); k++ {
					// "*" must not have crossed a "/" when k>0.
					if k > 0 && s[si+k-1] == '/' {
						return false
					}
					if match(pi+1, si+k) {
						return true
					}
				}
				return false
			}
			if si >= len(s) || s[si] != pc {
				return false
			}
			pi++
			si++
		}
		return si == len(s)
	}
	return match(0, 0)
}

func containsStar(s string) bool { return strings.IndexByte(s, '*') >= 0 }
