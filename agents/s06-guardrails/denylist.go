package main

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"
)

// DenyListChecker is the permissive mode from guardrails.md L75-L91. Every
// tool call is allowed by default; only calls whose stringified arguments
// match one of policy.BlockedPatterns (as a Go regexp) are blocked.
//
// Use this when you can't enumerate every legitimate action (e.g. a
// developer assistant that may run arbitrary shell), but you can name the
// specific shapes that are never acceptable: "rm -rf /", piping curl into
// a shell, leaking env vars, etc.
//
// Patterns are matched against:
//   - the value of args["command"] if present (the upstream sketch
//     specifically picks the shell command argument; L86-L90), OR
//   - the concatenation of all string-valued args if there is no
//     "command" arg, so the checker stays useful for tools whose
//     dangerous payload lives in a differently-named field.
//
// Patterns are compiled lazily and cached. Bad patterns surface on first
// Check call as a Decision{Allow:false, Reason:"...regex error..."} —
// fail-closed: if we can't parse the policy, we don't dispatch.
type DenyListChecker struct {
	policy *Policy

	once     sync.Once
	compiled []*regexp.Regexp
	compErr  error
}

// NewDenyListChecker wraps a Policy. Patterns are compiled on first use.
func NewDenyListChecker(p *Policy) *DenyListChecker {
	return &DenyListChecker{policy: p}
}

// Check returns Allow=false when the canonical-string form of args matches
// any blocked pattern. The canonical-string form is described above. If a
// pattern fails to compile, the call is blocked (fail-closed) and the
// reason names the bad pattern.
func (c *DenyListChecker) Check(toolName string, args map[string]any) (Decision, error) {
	c.once.Do(c.compile)
	if c.compErr != nil {
		return Decision{
			Allow:  false,
			Reason: fmt.Sprintf("deny-list policy invalid: %v", c.compErr),
		}, nil
	}

	haystack := canonicalArgs(args)
	for i, re := range c.compiled {
		if re.MatchString(haystack) {
			return Decision{
				Allow: false,
				Reason: fmt.Sprintf(
					"argument matched blocked pattern %q",
					c.policy.BlockedPatterns[i],
				),
			}, nil
		}
	}
	return Decision{Allow: true, Reason: "no blocked pattern matched"}, nil
}

// compile parses every policy.BlockedPatterns entry. Errors are captured
// on c.compErr (and the corresponding slot left nil) so the first Check
// call can fail-closed.
func (c *DenyListChecker) compile() {
	if c.policy == nil {
		return
	}
	c.compiled = make([]*regexp.Regexp, len(c.policy.BlockedPatterns))
	for i, p := range c.policy.BlockedPatterns {
		re, err := regexp.Compile(p)
		if err != nil {
			c.compErr = fmt.Errorf("pattern %q: %w", p, err)
			return
		}
		c.compiled[i] = re
	}
}

// canonicalArgs returns the string we run the regex against. If args has a
// "command" entry (the upstream picks this; L86-L90) we use that
// verbatim — most shell-risk patterns are written against shell syntax. If
// there is no command arg, we concatenate every string-valued arg in
// stable key order so the regex still has a target.
func canonicalArgs(args map[string]any) string {
	if cmd, ok := args["command"].(string); ok {
		return cmd
	}
	keys := make([]string, 0, len(args))
	for k := range args {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var parts []string
	for _, k := range keys {
		if s, ok := args[k].(string); ok {
			parts = append(parts, s)
		}
	}
	return strings.Join(parts, " ")
}
