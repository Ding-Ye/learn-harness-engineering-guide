// Package git bundles the stub git_status / git_diff tools shipped with the
// git skill fixture. As with skills/file_ops, the implementations are mock
// stubs — the chapter is about the registry, not real git plumbing.
package git

import (
	"context"
	"encoding/json"
	"fmt"
)

var gitStatusSchema = json.RawMessage(`{
  "type": "object",
  "properties": {},
  "additionalProperties": false
}`)

// StatusTool reports the working tree status. The stub returns a canned
// porcelain v1 snippet so the demo and tests are deterministic — same
// strings every run, no git binary required, no filesystem race.
type StatusTool struct{}

func (StatusTool) Name() string             { return "git_status" }
func (StatusTool) Description() string      { return "Show working tree status." }
func (StatusTool) Schema() json.RawMessage  { return gitStatusSchema }

func (StatusTool) Run(_ context.Context, _ json.RawMessage) (string, error) {
	return "On branch main\n M skill.go\n?? README.md\n", nil
}

var gitDiffSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "staged": {
      "type": "boolean",
      "description": "If true, show the staged diff; otherwise show unstaged."
    }
  }
}`)

// DiffTool returns a stub unified-diff. We branch on `staged` to prove the
// argument actually reaches the tool — useful when the registry tests check
// args round-tripped correctly.
type DiffTool struct{}

func (DiffTool) Name() string             { return "git_diff" }
func (DiffTool) Description() string      { return "Show changes, staged or unstaged." }
func (DiffTool) Schema() json.RawMessage  { return gitDiffSchema }

func (DiffTool) Run(_ context.Context, args json.RawMessage) (string, error) {
	var input struct {
		Staged bool `json:"staged"`
	}
	if len(args) > 0 {
		if err := json.Unmarshal(args, &input); err != nil {
			return "", fmt.Errorf("decode args: %w", err)
		}
	}
	if input.Staged {
		return "diff --git a/staged.go b/staged.go\n@@ ... staged diff ...\n", nil
	}
	return "diff --git a/skill.go b/skill.go\n@@ ... unstaged diff ...\n", nil
}
