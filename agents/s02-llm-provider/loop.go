package main

import (
	"context"
	"fmt"
)

// Loop is the same think→act→observe agentic loop introduced in s01, but
// parameterized on the canonical Provider interface and the richer
// []ContentBlock message shape.
//
// Compared to s01:
//   - Provider.Chat takes a full ChatRequest, not just []Message.
//   - Messages carry Content []ContentBlock instead of Content string +
//     separate ToolCalls/ToolCallID fields.
//   - Tool calls are extracted from ContentBlock entries with Type=="tool_use".
//   - Tool results are appended as Messages with Role=="tool" containing a
//     ContentBlock of Type=="tool_result".
type Loop struct {
	Provider Provider
	Tools    map[string]Tool
	MaxTurns int

	// Model and System are passed straight through to ChatRequest. A real loop
	// would feed these from a config; the demo CLI in main.go uses defaults.
	Model  string
	System string
}

// Run drives one user message to completion. The invariant inherited from s01
// stands: every assistant message MUST be appended BEFORE its tool_result
// messages, otherwise the model loses track of what it asked for.
func (l *Loop) Run(ctx context.Context, userMessage string) (string, error) {
	tools := make([]ToolSchema, 0, len(l.Tools))
	for _, t := range l.Tools {
		tools = append(tools, t.Schema())
	}

	messages := []Message{
		{
			Role: "user",
			Content: []ContentBlock{
				{Type: "text", Text: userMessage},
			},
		},
	}

	for turn := 0; turn < l.MaxTurns; turn++ {
		req := ChatRequest{
			Model:    l.Model,
			System:   l.System,
			Messages: messages,
			Tools:    tools,
		}
		resp, err := l.Provider.Chat(ctx, req)
		if err != nil {
			return "", fmt.Errorf("turn %d: provider error: %w", turn, err)
		}

		// 1. Append the assistant turn — even when only tool calls are present.
		messages = append(messages, Message{Role: "assistant", Content: resp.Content})

		// 2. Decide based on stop reason.
		if resp.StopReason == "end_turn" {
			return extractText(resp.Content), nil
		}

		if resp.StopReason != "tool_use" {
			return "", fmt.Errorf("turn %d: unexpected stop_reason %q", turn, resp.StopReason)
		}

		// 3. Execute every tool_use block in this assistant turn. A real model
		// may emit several in parallel (e.g. "read file A and file B at once");
		// each becomes one tool_result block in a single tool message.
		toolResults := make([]ContentBlock, 0)
		for _, block := range resp.Content {
			if block.Type != "tool_use" {
				continue
			}
			result, isErr := l.executeTool(ctx, block)
			toolResults = append(toolResults, ContentBlock{
				Type:    "tool_result",
				ID:      block.ID,
				Content: result,
				IsError: isErr,
			})
		}

		if len(toolResults) > 0 {
			messages = append(messages, Message{Role: "tool", Content: toolResults})
		}
	}

	return "", fmt.Errorf("max turns reached (%d) without end_turn", l.MaxTurns)
}

// executeTool dispatches one tool_use block. It returns (result, isError) where
// isError is true if either the tool wasn't found or its Run returned an error.
// As in s01, errors become string content the model can reason about — they
// never propagate up through Loop.Run.
func (l *Loop) executeTool(ctx context.Context, block ContentBlock) (string, bool) {
	tool, ok := l.Tools[block.Name]
	if !ok {
		return fmt.Sprintf("Error: unknown tool %q", block.Name), true
	}
	out, err := tool.Run(ctx, block.Input)
	if err != nil {
		return fmt.Sprintf("Error running %s: %v", block.Name, err), true
	}
	return out, false
}

// extractText concatenates all text blocks in the assistant's final turn.
// A model normally emits a single text block at end_turn, but the API permits
// multiple, and we join them with a single newline so nothing is lost.
func extractText(blocks []ContentBlock) string {
	out := ""
	for _, b := range blocks {
		if b.Type != "text" {
			continue
		}
		if out != "" {
			out += "\n"
		}
		out += b.Text
	}
	return out
}
