package main

import (
	"fmt"
	"strings"
)

// main is a CLI demo for the sliding window. It feeds 60 dummy turns through
// a small-budget SlidingWindowContext, prints the running message count and
// the state of the summary at each compression event, then prints the final
// GetMessages() output so a reader can see what the LLM would actually
// receive after compression.
//
// We deliberately keep maxTokens small (300) so compression triggers a few
// times during the run — at real production budgets (~128K) you'd have to
// feed thousands of messages before seeing the same effect.
func main() {
	const (
		windowSize = 15
		maxTokens  = 300
		threshold  = 0.7
	)
	mock := &MockSummarizer{}
	swc := NewSlidingWindowContext(windowSize, maxTokens, threshold, mock)

	// Seed a system message so we can demonstrate the never-compressed
	// property in the final output.
	if err := swc.Add(Message{
		Role:    "system",
		Content: []ContentBlock{{Type: "text", Text: "You are a careful coding assistant."}},
	}); err != nil {
		fmt.Println("seed error:", err)
		return
	}

	fmt.Printf("=== feeding 60 turns into SlidingWindowContext "+
		"(window=%d, maxTokens=%d, threshold=%.2f) ===\n\n", windowSize, maxTokens, threshold)

	for i := 0; i < 60; i++ {
		body := fmt.Sprintf("turn %d: %s", i, strings.Repeat("token ", 4))
		prevAttempts := swc.CompressAttempts
		if err := swc.Add(Message{
			Role:    "user",
			Content: []ContentBlock{{Type: "text", Text: body}},
		}); err != nil {
			fmt.Printf("Add #%d error: %v\n", i, err)
			return
		}
		// If a compression happened on this Add, narrate it.
		if swc.CompressAttempts > prevAttempts {
			fmt.Printf("[turn %d] compression #%d: len(messages)=%d summary=%q\n",
				i, swc.CompressAttempts, swc.Len(), shorten(swc.Summary(), 60))
		}
	}

	fmt.Println()
	fmt.Printf("=== final state ===\n")
	fmt.Printf("CompressAttempts: %d  Summarizer.Calls: %d\n", swc.CompressAttempts, mock.Calls)
	fmt.Printf("Messages in buffer: %d  (system + last-window)\n", swc.Len())
	fmt.Printf("Tokens in buffer:   %d\n", EstimateTokens(swc.messages))
	fmt.Printf("Summary:            %q\n", shorten(swc.Summary(), 80))

	fmt.Println()
	fmt.Println("=== what the LLM sees (GetMessages) ===")
	for i, m := range swc.GetMessages() {
		preview := ""
		if len(m.Content) > 0 {
			preview = shorten(m.Content[0].Text, 60)
		}
		fmt.Printf("[%2d] role=%-9s %s\n", i, m.Role, preview)
	}
}

// shorten truncates s to n runes, appending "..." if it was longer. Used to
// keep the demo output tidy.
func shorten(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "..."
}
