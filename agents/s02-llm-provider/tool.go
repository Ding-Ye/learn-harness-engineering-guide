package main

import (
	"context"
	"encoding/json"
	"fmt"
)

// Tool is the contract every tool implements in s02 and beyond.
// The shape matches plan.md "Shared types catalog":
//
//   - Name()        identifier the model emits in tool_use.name
//   - Schema()      ToolSchema (name+description+input JSON Schema)
//   - Run(ctx,args) executes the tool; returns the *string* the model sees
//
// Why string output: dispatch contract from guide/tool-system.md L62 —
// "tools always return strings, errors become string content". This keeps
// the loop simple: tool result → text block → next turn.
type Tool interface {
	Name() string
	Schema() ToolSchema
	Run(ctx context.Context, args json.RawMessage) (string, error)
}

// EchoTool is the demo tool for s02: it returns its `text` arg wrapped in
// "echo: ". The schema is minimal so the model has all it needs to call it.
type EchoTool struct{}

func (EchoTool) Name() string { return "echo" }

func (EchoTool) Schema() ToolSchema {
	return ToolSchema{
		Name:        "echo",
		Description: "Echo the provided text back, prefixed with \"echo: \".",
		Schema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"text": {
					"type": "string",
					"description": "The text to echo back."
				}
			},
			"required": ["text"]
		}`),
	}
}

func (EchoTool) Run(_ context.Context, args json.RawMessage) (string, error) {
	var parsed struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(args, &parsed); err != nil {
		return "", fmt.Errorf("invalid args for echo: %w", err)
	}
	if parsed.Text == "" {
		return "", fmt.Errorf("echo: text arg is required and must be non-empty")
	}
	return "echo: " + parsed.Text, nil
}
