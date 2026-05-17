package main

import (
	"sort"
	"strings"
)

// ContextSection is one named, prioritized piece of context. Mirrors the rows
// of the upstream priority table in guide/context-engineering.md L19-L28:
// priority 0 = system prompt, 1 = tools, 2 = task, 3 = memory, 4 = files,
// 5 = recent conversation, 6 = older conversation.
//
// Priority ≤ 2 is critical (truncated when over budget); priority ≥ 3 is
// droppable (silently excluded).
type ContextSection struct {
	Priority int
	Name     string
	Content  string
}

// criticalCutoff is the priority threshold separating "truncate on overflow"
// from "drop on overflow". Sections with Priority <= criticalCutoff get a
// best-effort truncated slot; sections with Priority > criticalCutoff are
// dropped when the budget is exhausted.
const criticalCutoff = 2

// truncationSuffix is appended to the (token-trimmed) content of a critical
// section that didn't fully fit. Tests assert on this suffix.
const truncationSuffix = " (truncated)"

// orderedSection wraps a section with the order it was Add'd in so we can
// keep that order stable inside the same priority bucket after sorting.
type orderedSection struct {
	section  ContextSection
	addOrder int
}

// ContextAssembler builds a packed list of context sections within a token
// budget. Sections are sorted ascending by Priority (lower number = higher
// importance, matching the upstream table). Within the same priority bucket,
// add-order is preserved for determinism.
//
// The budget is `maxTokens - reserveTokens` — reserveTokens leaves headroom
// for the model's response. guide/context-engineering.md L43-L45 calls this
// out explicitly: packing to 100% leaves the model no room to reply.
type ContextAssembler struct {
	maxTokens     int
	reserveTokens int
	sections      []orderedSection
	nextOrder     int
}

// NewContextAssembler returns an assembler with the given total budget and
// the number of tokens to reserve for the model's response.
//
// Typical values from the upstream: maxTokens=128000, reserveTokens=4096.
// Tests use much smaller numbers (maxTokens=100, reserveTokens=0) so the
// budget logic can be exercised against tiny fixtures.
func NewContextAssembler(maxTokens, reserveTokens int) *ContextAssembler {
	return &ContextAssembler{
		maxTokens:     maxTokens,
		reserveTokens: reserveTokens,
	}
}

// Add registers a section. Lower priority number = higher importance.
// Sections are not packed until Build() is called.
func (a *ContextAssembler) Add(priority int, name, content string) {
	a.sections = append(a.sections, orderedSection{
		section:  ContextSection{Priority: priority, Name: name, Content: content},
		addOrder: a.nextOrder,
	})
	a.nextOrder++
}

// Budget returns the effective token budget — total minus reserve. A negative
// reserve is clamped to zero; a reserve larger than maxTokens yields a zero
// budget (everything droppable will be dropped, everything critical will be
// truncated to nothing or marked truncated only).
func (a *ContextAssembler) Budget() int {
	b := a.maxTokens - a.reserveTokens
	if b < 0 {
		return 0
	}
	return b
}

// Build packs sections into the budget and returns the packed slice plus the
// total tokens consumed. The packing rules:
//
//  1. Sort by Priority ascending; break ties by add-order ascending.
//  2. Walk in order. If the section's full content fits in the remaining
//     budget, include it as-is.
//  3. Otherwise:
//     - If Priority ≤ 2 (critical): truncate the content to fit the remaining
//       budget (less the suffix overhead) and include with " (truncated)"
//       appended. If even one token won't fit, include an empty-content
//       section so the caller can still tell the row existed.
//     - If Priority ≥ 3 (droppable): silently skip.
//
// `used` is the sum of EstimateTokens over the packed contents (post-truncation).
func (a *ContextAssembler) Build() ([]ContextSection, int) {
	// Sort by (priority, addOrder). sort.SliceStable preserves add-order for
	// ties even without the explicit secondary key, but the secondary key is
	// kept here for clarity and to make TestAssembler_DeterministicAddOrder
	// pass regardless of the sort algorithm in use.
	sorted := make([]orderedSection, len(a.sections))
	copy(sorted, a.sections)
	sort.SliceStable(sorted, func(i, j int) bool {
		if sorted[i].section.Priority != sorted[j].section.Priority {
			return sorted[i].section.Priority < sorted[j].section.Priority
		}
		return sorted[i].addOrder < sorted[j].addOrder
	})

	budget := a.Budget()
	used := 0
	out := make([]ContextSection, 0, len(sorted))

	for _, item := range sorted {
		sec := item.section
		cost := EstimateTokens(sec.Content)

		// Fits as-is.
		if used+cost <= budget {
			out = append(out, sec)
			used += cost
			continue
		}

		// Doesn't fit. Two outcomes depending on priority.
		if sec.Priority > criticalCutoff {
			// Droppable: skip silently.
			continue
		}

		// Critical: emit a truncated copy. Available room is the budget
		// minus what we've already used. We reserve a small budget for the
		// " (truncated)" suffix itself.
		remaining := budget - used
		if remaining < 0 {
			remaining = 0
		}
		truncated := truncateToTokenBudget(sec.Content, remaining)
		truncatedSec := ContextSection{
			Priority: sec.Priority,
			Name:     sec.Name,
			Content:  truncated + truncationSuffix,
		}
		out = append(out, truncatedSec)
		used += EstimateTokens(truncatedSec.Content)
	}

	return out, used
}

// truncateToTokenBudget shortens text so EstimateTokens of the result is at
// most max. We work in whitespace-separated words to match the estimator's
// notion of tokens; this keeps Build's bookkeeping consistent.
//
// If max <= 0 we return the empty string — the caller still appends the
// " (truncated)" suffix so the row remains visible.
func truncateToTokenBudget(content string, max int) string {
	if max <= 0 {
		return ""
	}
	// EstimateTokens uses words * 13 / 10, so the maximum word count that
	// fits in `max` tokens is ceil(max * 10 / 13) — but we want the LARGEST
	// wordCount whose tokens (= wordCount*13/10 with integer division) is
	// still <= max. Iterate from a safe ceiling downward.
	words := strings.Fields(content)
	if len(words) == 0 {
		return ""
	}
	// Closed-form upper bound and then refine by a small loop to be safe
	// against integer-division quirks.
	wordCap := max * 10 / 13
	if wordCap > len(words) {
		wordCap = len(words)
	}
	// Walk down until EstimateTokens of the joined prefix fits.
	for wordCap > 0 {
		candidate := strings.Join(words[:wordCap], " ")
		if EstimateTokens(candidate) <= max {
			return candidate
		}
		wordCap--
	}
	return ""
}
