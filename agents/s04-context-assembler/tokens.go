package main

import "strings"

// EstimateTokens returns a crude word-based token count. The upstream
// (guide/context-engineering.md L33-L38) uses tiktoken; for s04 we replace it
// with a dependency-free heuristic: whitespace-split word count multiplied by
// 1.3 to account for sub-word splits ("agentic" → "agent" + "ic"). The factor
// is encoded as integer arithmetic (* 13 / 10) to keep the function pure and
// reproducible across architectures.
//
// This is intentionally pessimistic enough to be safe as a budget guard but
// fast enough to call once per section per turn. s09 extends to model-aware
// tokenizers; s04 only needs monotonicity and rough proportionality.
func EstimateTokens(s string) int {
	if s == "" {
		return 0
	}
	words := strings.Fields(s)
	if len(words) == 0 {
		return 0
	}
	return len(words) * 13 / 10
}
