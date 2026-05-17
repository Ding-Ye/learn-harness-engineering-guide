package main

import (
	"context"
	"fmt"
)

// Loop is the smallest think→act→observe agentic loop. It deliberately omits:
// retries (s07), guardrails (s06), context compression (s09), checkpoints (s11).
// Those are layered on top in later chapters.
type Loop struct {
	Provider Provider
	Tools    map[string]Tool
	MaxTurns int
}

// Run drives one user message to completion. It returns the assistant's final
// text response, or an error if MaxTurns is exhausted before the model emits
// an "end_turn" stop reason.
//
// The invariant every chapter inherits: every assistant message (including the
// one that contains tool_use blocks) MUST be appended to history BEFORE its
// tool_result messages, otherwise the model loses track of what it asked for.
// See guide/your-first-harness.md L113-L117.
func (l *Loop) Run(ctx context.Context, userMessage string) (string, error) {
	messages := []Message{
		{Role: "system", Content: "You are a helpful assistant. Use the echo tool when asked."},
		{Role: "user", Content: userMessage},
	}

	for turn := 0; turn < l.MaxTurns; turn++ {
		resp, err := l.Provider.Chat(ctx, messages)
		if err != nil {
			return "", fmt.Errorf("turn %d: provider error: %w", turn, err)
		}

		// 1. Append the assistant turn — even when only tool calls are present.
		messages = append(messages, Message{
			Role:      "assistant",
			Content:   resp.Content,
			ToolCalls: resp.ToolCalls,
		})

		// 2. Decide based on stop reason.
		if resp.StopReason == "end_turn" {
			return resp.Content, nil
		}

		if resp.StopReason != "tool_use" {
			return "", fmt.Errorf("turn %d: unexpected stop_reason %q", turn, resp.StopReason)
		}

		// 3. Execute every tool call in this assistant turn (the model may
		// request several in parallel). Each becomes a separate tool message.
		for _, call := range resp.ToolCalls {
			result := l.executeTool(call)
			messages = append(messages, Message{
				Role:       "tool",
				ToolCallID: call.ID,
				Content:    result,
			})
		}
	}

	return "", fmt.Errorf("max turns reached (%d) without end_turn", l.MaxTurns)
}

// executeTool dispatches one call. It never returns an error to the caller —
// tool errors become string content so the model can reason about them.
// See guide/tool-system.md L62 ("dispatch always returns a string").
func (l *Loop) executeTool(call ToolCall) string {
	tool, ok := l.Tools[call.Name]
	if !ok {
		return fmt.Sprintf("Error: unknown tool %q", call.Name)
	}
	out, err := tool.Run(call.Args)
	if err != nil {
		return fmt.Sprintf("Error running %s: %v", call.Name, err)
	}
	return out
}
