package main

import (
	"fmt"
	"strings"
)

// repeatPhrase returns "word word word ..." with the requested number of words.
// It is the cheapest way to fabricate a section whose token count, under
// EstimateTokens, is predictable: n words → n*13/10 tokens.
func repeatPhrase(word string, n int) string {
	parts := make([]string, n)
	for i := range parts {
		parts[i] = word
	}
	return strings.Join(parts, " ")
}

func main() {
	// Budget tight enough that the lowest-priority sections will be dropped
	// and one critical section will exceed the budget on its own.
	const maxTokens = 200
	const reserve = 20
	ca := NewContextAssembler(maxTokens, reserve)

	// Add six sections in deliberately scrambled add-order to demonstrate
	// that Build() reorders by priority, not by add-order. Cf. the upstream
	// priority table at guide/context-engineering.md L19-L28.
	ca.Add(5, "recent-chat", repeatPhrase("recent", 80)) // 80 words → 104 tokens
	ca.Add(0, "system-prompt", "You are a helpful assistant. Keep answers short.")
	ca.Add(4, "file-snippet", repeatPhrase("file", 50))  // 50 words → 65 tokens
	ca.Add(2, "task", repeatPhrase("step", 200))         // 200 words → 260 tokens — will be truncated
	ca.Add(1, "tool-schemas", "tool: echo(text), tool: read_file(path), tool: write_file(path,content)")
	ca.Add(3, "memory", "User prefers terse output. Project: learn-harness-engineering-guide.")

	packed, used := ca.Build()

	budget := ca.Budget()
	fmt.Printf("Budget: %d (max=%d, reserve=%d)\n", budget, maxTokens, reserve)
	fmt.Printf("Used:   %d tokens across %d sections\n\n", used, len(packed))

	fmt.Println("Packed sections (priority-sorted):")
	fmt.Println("┌─────┬──────────────────┬───────┬──────────────────────────────────────────")
	fmt.Printf("│ pri │ %-16s │ %5s │ preview\n", "name", "tok")
	fmt.Println("├─────┼──────────────────┼───────┼──────────────────────────────────────────")
	for _, sec := range packed {
		tokens := EstimateTokens(sec.Content)
		preview := sec.Content
		if len(preview) > 40 {
			preview = preview[:37] + "..."
		}
		fmt.Printf("│  %d  │ %-16s │ %5d │ %s\n", sec.Priority, sec.Name, tokens, preview)
	}
	fmt.Println("└─────┴──────────────────┴───────┴──────────────────────────────────────────")

	// Note which sections were dropped (in catalog but not in packed output).
	addedNames := map[string]bool{
		"recent-chat":   true,
		"system-prompt": true,
		"file-snippet":  true,
		"task":          true,
		"tool-schemas":  true,
		"memory":        true,
	}
	for _, sec := range packed {
		delete(addedNames, sec.Name)
	}
	if len(addedNames) > 0 {
		fmt.Println("\nDropped (over budget, priority ≥ 3):")
		for name := range addedNames {
			fmt.Printf("  - %s\n", name)
		}
	}
}
