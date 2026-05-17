package main

import "context"

// Message is the simplified shape we use in s01: role + plain content,
// plus optional ToolCalls for assistant turns and ToolCallID for tool turns.
// s02 will upgrade Content to a slice of ContentBlock to match Anthropic's wire format.
type Message struct {
	Role       string     // "system" | "user" | "assistant" | "tool"
	Content    string     // user/assistant text, or tool result content
	ToolCalls  []ToolCall // populated only when Role == "assistant" and the model emits tool_use
	ToolCallID string     // populated only when Role == "tool"
}

// ToolCall is what an assistant turn requests when StopReason is "tool_use".
type ToolCall struct {
	ID   string
	Name string
	Args map[string]any
}

// ChatResponse is a single round trip with the model.
type ChatResponse struct {
	Content    string     // free-form text (may be empty when only tool calls)
	ToolCalls  []ToolCall // empty when StopReason is "end_turn"
	StopReason string     // "end_turn" or "tool_use"
}

// Provider abstracts the LLM call. s01 uses a hand-scripted MockProvider so the
// loop is exercised without any network or API keys. s02 introduces a Provider
// interface that hides the wire-format difference between Anthropic and OpenAI.
type Provider interface {
	Chat(ctx context.Context, messages []Message) (*ChatResponse, error)
}

// Tool is the contract every tool implements. s03 makes this richer (Schema,
// JSON args). s01 uses string-only args to keep the surface minimal.
type Tool interface {
	Name() string
	Run(args map[string]any) (string, error)
}
