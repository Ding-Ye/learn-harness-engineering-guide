package main

import (
	"strings"
	"testing"
)

// repeatWord builds a content string with exactly n whitespace-separated
// words. EstimateTokens on the result is n*13/10 (integer division).
// Helper kept in the test file so tests are self-contained.
func repeatWord(word string, n int) string {
	if n <= 0 {
		return ""
	}
	parts := make([]string, n)
	for i := range parts {
		parts[i] = word
	}
	return strings.Join(parts, " ")
}

func TestAssembler_RespectsPriorityOrder(t *testing.T) {
	// Add five sections in scrambled order. After Build() they must come
	// back in ascending priority order regardless of add-order.
	ca := NewContextAssembler(10000, 0)
	ca.Add(5, "p5", "five")
	ca.Add(1, "p1", "one")
	ca.Add(3, "p3", "three")
	ca.Add(0, "p0", "zero")
	ca.Add(4, "p4", "four")
	ca.Add(2, "p2", "two")

	packed, _ := ca.Build()
	if len(packed) != 6 {
		t.Fatalf("expected 6 sections packed (budget large), got %d", len(packed))
	}

	wantPriorities := []int{0, 1, 2, 3, 4, 5}
	for i, sec := range packed {
		if sec.Priority != wantPriorities[i] {
			t.Errorf("packed[%d].Priority = %d, want %d (section name %q)",
				i, sec.Priority, wantPriorities[i], sec.Name)
		}
	}
}

func TestAssembler_DropsLowPriorityWhenOverBudget(t *testing.T) {
	// budget=100, priority-6 section of ~200 tokens → dropped (priority > 2).
	// We also include a priority-0 section so we can confirm the assembler
	// did pack *something*; only the low-priority one is dropped.
	ca := NewContextAssembler(100, 0)
	ca.Add(0, "critical", "tiny")               // 1 word → 1 token, easily fits
	ca.Add(6, "older-chat", repeatWord("w", 154)) // 154 words → 200 tokens

	packed, used := ca.Build()

	// "older-chat" must be absent.
	for _, sec := range packed {
		if sec.Name == "older-chat" {
			t.Errorf("priority-6 section should have been dropped over budget; saw %+v", sec)
		}
	}
	if used > 100 {
		t.Errorf("used = %d, want ≤ budget (100)", used)
	}
	// "critical" must still be there.
	found := false
	for _, sec := range packed {
		if sec.Name == "critical" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected priority-0 section to remain; got %+v", packed)
	}
}

func TestAssembler_TruncatesCriticalSections(t *testing.T) {
	// budget=100, priority-2 section of 200 tokens → truncated, suffix.
	ca := NewContextAssembler(100, 0)
	ca.Add(2, "task", repeatWord("w", 154)) // 154 words → 200 tokens

	packed, used := ca.Build()
	if len(packed) != 1 {
		t.Fatalf("expected 1 section (truncated, not dropped); got %d", len(packed))
	}
	sec := packed[0]
	if sec.Priority != 2 || sec.Name != "task" {
		t.Errorf("unexpected packed section: %+v", sec)
	}
	if !strings.HasSuffix(sec.Content, "(truncated)") {
		t.Errorf("truncated content should end with '(truncated)', got: %q", lastChars(sec.Content, 30))
	}
	if used > 100 {
		t.Errorf("used = %d after truncation, want ≤ budget (100)", used)
	}
	// And the truncated content must be strictly shorter (in tokens) than the
	// original — otherwise truncation didn't happen.
	if EstimateTokens(sec.Content) >= 200 {
		t.Errorf("expected truncated content to be < 200 tokens, got %d", EstimateTokens(sec.Content))
	}
}

func TestAssembler_ReserveLeavesHeadroomForResponse(t *testing.T) {
	// maxTokens=1000, reserveTokens=200 → effective budget 800.
	// Add a single oversized droppable section; the assembler should pack
	// nothing rather than spill past the budget.
	ca := NewContextAssembler(1000, 200)
	if got := ca.Budget(); got != 800 {
		t.Fatalf("Budget() = %d, want 800", got)
	}
	// 700 tokens (538 words) fits under 800; 200 tokens (154 words) at
	// priority 4 should be dropped only if it doesn't fit. Both fit here
	// (700+200=900>800). The second one is droppable → dropped.
	ca.Add(0, "system", repeatWord("a", 538))     // 700 tokens
	ca.Add(4, "files", repeatWord("b", 154))      // 200 tokens (won't fit on top)

	_, used := ca.Build()
	if used > 800 {
		t.Errorf("used = %d, want ≤ 800 (reserve must leave 200 headroom)", used)
	}
}

func TestEstimateTokens_HeuristicMonotonic(t *testing.T) {
	// Table-driven monotonicity test: estimate(longer) ≥ estimate(shorter)
	// for an increasing sequence of word counts.
	cases := []struct {
		name  string
		input string
	}{
		{"empty", ""},
		{"one word", "hello"},
		{"three words", "hello world again"},
		{"ten words", repeatWord("x", 10)},
		{"hundred words", repeatWord("y", 100)},
		{"thousand words", repeatWord("z", 1000)},
	}
	prev := -1
	for _, tc := range cases {
		got := EstimateTokens(tc.input)
		if got < prev {
			t.Errorf("monotonicity broken at %q: got %d, prev was %d", tc.name, got, prev)
		}
		prev = got
	}
	// Spot-check a known value: 10 words → 13 tokens (10*13/10 = 13).
	if got := EstimateTokens(repeatWord("x", 10)); got != 13 {
		t.Errorf("EstimateTokens(10 words) = %d, want 13", got)
	}
}

func TestAssembler_DeterministicAddOrder(t *testing.T) {
	// Two sections at the same priority must come out in the order they were
	// Add'd. Run this enough times to defeat any non-stable sort.
	for trial := 0; trial < 50; trial++ {
		ca := NewContextAssembler(10000, 0)
		ca.Add(3, "first", "one")
		ca.Add(3, "second", "two")
		ca.Add(3, "third", "three")

		packed, _ := ca.Build()
		if len(packed) != 3 {
			t.Fatalf("trial %d: expected 3 sections, got %d", trial, len(packed))
		}
		want := []string{"first", "second", "third"}
		for i, sec := range packed {
			if sec.Name != want[i] {
				t.Errorf("trial %d: packed[%d].Name = %q, want %q", trial, i, sec.Name, want[i])
			}
		}
	}
}

// lastChars returns the trailing n runes of s — used in error messages so a
// truncated 200-word string doesn't flood the test log.
func lastChars(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return "..." + s[len(s)-n:]
}
