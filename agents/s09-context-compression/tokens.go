package main

import "strings"

// EstimateTextTokens returns a crude word-based token count for a single string.
// Matches s04's heuristic exactly (whitespace-split word count × 13 / 10).
//
// Why a copy and not an import? The curriculum's isolation rule: each chapter
// is a self-contained module, and a reader on s09 should not have to chase
// imports across modules to understand the budget math. The integer arithmetic
// (× 13 / 10) avoids float comparisons and is identical across architectures.
//
// Upstream's `context-engineering.md` uses tiktoken; we replace it with a
// dependency-free heuristic that is pessimistic enough to act as a safe budget
// guard and fast enough to call once per `Add()`.
func EstimateTextTokens(s string) int {
	if s == "" {
		return 0
	}
	words := strings.Fields(s)
	if len(words) == 0 {
		return 0
	}
	return len(words) * 13 / 10
}

// EstimateTokens sums EstimateTextTokens across every content block of every
// message. We walk Text, Content, and Name fields — these are the only ones
// that contribute meaningful payload. Tool-use IDs and the IsError flag are
// fixed-size and not worth counting.
//
// The sum is intentionally over-counting (Text + Content + Name are not all
// populated at once on the same block, but adding them anyway is cheap and
// keeps the threshold check on the safe side).
func EstimateTokens(msgs []Message) int {
	total := 0
	for _, m := range msgs {
		for _, b := range m.Content {
			total += EstimateTextTokens(b.Text)
			total += EstimateTextTokens(b.Content)
			total += EstimateTextTokens(b.Name)
		}
	}
	return total
}
