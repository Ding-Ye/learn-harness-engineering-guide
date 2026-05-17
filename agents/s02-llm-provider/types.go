package main

import "encoding/json"

// Canonical Anthropic-shaped types introduced in s02 and reused by every
// subsequent chapter. s01 used a stripped-down shape (Message{Role,Content string});
// this chapter is the upgrade.
//
// The shapes mirror Anthropic's Messages API wire format:
//   - A message has a Role and a slice of ContentBlock.
//   - Each ContentBlock has a Type that selects which fields are populated:
//       "text"        → Text
//       "tool_use"    → ID, Name, Input
//       "tool_result" → ID, Content, IsError
//
// Translating "down" to OpenAI's flatter shape is mechanical and is done inside
// a hypothetical OpenAIProvider in Phase G. The richer Anthropic shape is the
// canonical because it loses no information — OpenAI's `tool_calls`/`tool`
// messages can be expressed as ContentBlocks but not vice versa.

// Message is one entry in the conversation history.
type Message struct {
	Role    string         `json:"role"` // "user" | "assistant" | "tool"
	Content []ContentBlock `json:"content"`
}

// ContentBlock is one element of a message's content list.
// Only the fields relevant for Type are populated; the rest are zero values.
type ContentBlock struct {
	Type    string          `json:"type"`              // "text" | "tool_use" | "tool_result"
	Text    string          `json:"text,omitempty"`    // when Type=="text"
	ID      string          `json:"id,omitempty"`      // tool_use id (assistant) / tool_use_id (tool result)
	Name    string          `json:"name,omitempty"`    // when Type=="tool_use"
	Input   json.RawMessage `json:"input,omitempty"`   // when Type=="tool_use"
	Content string          `json:"content,omitempty"` // when Type=="tool_result"
	IsError bool            `json:"is_error,omitempty"`
}

// ToolSchema is the model-facing description of one tool: the name+description
// the model reads in the system prompt, plus a JSON Schema for the input shape.
// We keep Schema as json.RawMessage so providers can pass it through without
// re-encoding (saves CPU and avoids ordering surprises in tests).
type ToolSchema struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Schema      json.RawMessage `json:"schema"`
}

// ChatRequest is the provider-agnostic input to Provider.Chat.
// Providers translate this into their own wire format:
//   - Anthropic: messages, system, tools[].{name,description,input_schema}, max_tokens
//   - OpenAI:    messages (system as first message), tools[].function.{name,description,parameters}, max_tokens
type ChatRequest struct {
	Model     string
	System    string
	Messages  []Message
	Tools     []ToolSchema
	MaxTokens int
}

// ChatResponse is the provider-agnostic output of one LLM call.
// Content is a list of blocks — typically one "text" block, or one or more
// "tool_use" blocks (or a mix). StopReason indicates why generation stopped.
type ChatResponse struct {
	Content    []ContentBlock
	StopReason string // "end_turn" | "tool_use" | "max_tokens"
}
