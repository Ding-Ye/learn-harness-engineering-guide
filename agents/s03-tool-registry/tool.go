package main

import (
	"context"
	"encoding/json"
)

// Tool is the contract every tool implements. The contract has two halves,
// matching guide/tool-system.md L10-L33:
//
//   - The model sees Name(), Description() and Schema() — a JSON Schema
//     describing the tool's input shape. It uses that to decide *when* to call
//     the tool and *what* arguments to pass.
//   - The harness calls Run(ctx, args) to actually execute the tool. Run takes
//     the raw JSON the model emitted as input, returns a string result, and
//     never panics — errors are returned as Go errors so the registry can wrap
//     them into the canonical "Error running X: <msg>" string.
//
// The separation is deliberate: you can change how a tool runs without changing
// the schema the model is trained against, and you can restrict what a tool
// actually does (s06 guardrails) without the model knowing.
type Tool interface {
	Name() string
	Description() string
	Schema() json.RawMessage // JSON Schema for the tool's input object
	Run(ctx context.Context, args json.RawMessage) (string, error)
}

// ToolSchema is the wire-shape the registry exposes to whoever assembles the
// LLM request. The canonical Provider in s02 takes a []ToolSchema and forwards
// it to the model. We keep this as a plain struct (not a Tool reference) so
// the schema is decoupled from the implementation — exactly the split that
// guide/tool-system.md L9-L13 emphasises.
type ToolSchema struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Schema      json.RawMessage `json:"input_schema"`
}
