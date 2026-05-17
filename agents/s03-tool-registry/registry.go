package main

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
)

// Registry maps tool names to their Tool implementations. It owns two
// responsibilities that the upstream guide explicitly separates
// (guide/tool-system.md L36-L61):
//
//   1. Schemas() — emit the list of ToolSchema rows the LLM API call needs.
//   2. Dispatch() — given a tool name and JSON args, run the matching tool and
//      return a string (NEVER an error). This is the contract from
//      guide/tool-system.md L62: the model needs to *see* errors so it can
//      adapt; if Dispatch returned a Go error, the harness would have to
//      stringify it anyway. Centralising that here keeps every call site simple.
type Registry struct {
	tools map[string]Tool
}

// NewRegistry constructs an empty registry. Tools are added via Register.
func NewRegistry() *Registry {
	return &Registry{tools: make(map[string]Tool)}
}

// Register adds a tool. A later Register with the same Name() overwrites the
// previous entry — last-wins, matching the Python upstream's dict assignment
// at guide/tool-system.md L45. We deliberately do NOT panic on duplicate names
// because some chapters (s08 skills) re-register tools when a skill is loaded.
func (r *Registry) Register(t Tool) {
	r.tools[t.Name()] = t
}

// Schemas returns every registered tool's schema, sorted alphabetically by
// Name. Stable ordering matters for two reasons:
//
//   - Deterministic tests: a snapshot of Schemas() must compare equal across
//     runs regardless of map iteration order.
//   - Consistent prompt caching: the LLM provider often caches the prompt
//     prefix that contains the tool list; if the order shuffles, the cache
//     misses. The upstream guide alludes to this in the "skill loading" cost
//     analysis at guide/tool-system.md L65-L88.
func (r *Registry) Schemas() []ToolSchema {
	out := make([]ToolSchema, 0, len(r.tools))
	for _, t := range r.tools {
		out = append(out, ToolSchema{
			Name:        t.Name(),
			Description: t.Description(),
			Schema:      t.Schema(),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Dispatch executes one tool call by name and returns the result as a string.
// It NEVER returns a Go error and NEVER panics. The three failure modes the
// upstream guide enumerates (guide/tool-system.md L52-L60) each become a
// canonical string that the model can read and reason about:
//
//   - unknown tool         → "Error: unknown tool 'X'"
//   - invalid JSON args    → "Error: invalid args: <reason>"
//   - tool returned error  → "Error running X: <msg>"
//
// args is passed through to Tool.Run as-is so each tool owns its own argument
// schema validation. We only verify it's syntactically valid JSON first —
// otherwise an obviously broken request would force every Tool implementation
// to repeat the same json.Unmarshal+error-format boilerplate.
func (r *Registry) Dispatch(ctx context.Context, name string, args json.RawMessage) string {
	tool, ok := r.tools[name]
	if !ok {
		return fmt.Sprintf("Error: unknown tool '%s'", name)
	}

	// Empty args is acceptable: the tool may have no required parameters.
	// But if non-empty, it must parse as JSON before we hand it to Run.
	if len(args) > 0 {
		var probe any
		if err := json.Unmarshal(args, &probe); err != nil {
			return fmt.Sprintf("Error: invalid args: %v", err)
		}
	}

	out, err := tool.Run(ctx, args)
	if err != nil {
		return fmt.Sprintf("Error running %s: %v", name, err)
	}
	return out
}

// Names returns the registered tool names in alphabetical order. Useful for
// debug logging and the main.go CLI demo.
func (r *Registry) Names() []string {
	out := make([]string, 0, len(r.tools))
	for name := range r.tools {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}
