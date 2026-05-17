package main

import (
	"context"
	"encoding/json"
)

// Tool is the same contract as s03's Tool — we copy it instead of importing
// because the curriculum forbids cross-chapter imports (every session owns
// the types it depends on). The model sees Name/Description/Schema; the
// harness calls Run.
//
// In s03 the Registry talks directly to Tools. In s08 we put a layer above:
// each skill *holds* a list of Tools. The SkillRegistry exposes only the
// tools that belong to currently-active skills, so the LLM never sees
// schemas for skills it hasn't loaded — matching guide/skill-system.md
// L74-L101 (the menu pattern).
type Tool interface {
	Name() string
	Description() string
	Schema() json.RawMessage // JSON Schema for the tool's input object
	Run(ctx context.Context, args json.RawMessage) (string, error)
}

// ToolSchema is the wire-shape the registry hands to whoever assembles the
// LLM request. Mirrors s03 exactly so a future s_full integration could plug
// either registry into the same pipeline.
//
// We tag json fields so callers can marshal a schema list straight into an
// Anthropic-flavoured `tools: [...]` request without an extra translation
// step.
type ToolSchema struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Schema      json.RawMessage `json:"input_schema"`
}

// toSchema is the small helper every adapter writes — turn a Tool into the
// wire shape. Kept package-local so each test/main can use it without
// duplicating the field plumbing.
func toSchema(t Tool) ToolSchema {
	return ToolSchema{
		Name:        t.Name(),
		Description: t.Description(),
		Schema:      t.Schema(),
	}
}
