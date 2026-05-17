// Package fileops bundles the stub read_file/write_file tools that ship with
// the file_ops skill fixture. The implementations are deliberately
// in-memory: the chapter teaches the registry, not file I/O — those bits
// already live in s03. Returning canned strings keeps the demo and tests
// fast and free of OS quirks.
//
// Each tool type defined here satisfies the s08 main package's Tool
// interface STRUCTURALLY — Go does not need an explicit `implements`
// declaration, and we cannot import a `package main` from a subpackage.
// That structural-typing detail is the whole reason this layout works.
package fileops

import (
	"context"
	"encoding/json"
	"fmt"
)

// readFileSchema describes the input for read_file. Same shape as s03's
// fileops schema so a future s_full chapter can swap registries cleanly.
var readFileSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "path": {
      "type": "string",
      "description": "Path to the file to read."
    }
  },
  "required": ["path"]
}`)

// ReadFileTool is the stub implementation. Run records the path it was
// asked for and returns a deterministic mock content so tests can assert on
// the result without touching disk. Real I/O is out of scope for s08.
type ReadFileTool struct{}

func (ReadFileTool) Name() string             { return "read_file" }
func (ReadFileTool) Description() string      { return "Read the contents of a file at the given path." }
func (ReadFileTool) Schema() json.RawMessage  { return readFileSchema }

func (ReadFileTool) Run(_ context.Context, args json.RawMessage) (string, error) {
	var input struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(args, &input); err != nil {
		return "", fmt.Errorf("decode args: %w", err)
	}
	if input.Path == "" {
		return "", fmt.Errorf("path is required")
	}
	return fmt.Sprintf("(stub) contents of %s\n", input.Path), nil
}

var writeFileSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "path": {
      "type": "string",
      "description": "Path to the file. Parent directories are created if missing."
    },
    "content": {
      "type": "string",
      "description": "Content to write. Overwrites any existing file."
    }
  },
  "required": ["path", "content"]
}`)

// WriteFileTool is the matching stub for writes. It returns a short
// status string so the model can confirm the call succeeded; no bytes
// actually hit disk.
type WriteFileTool struct{}

func (WriteFileTool) Name() string             { return "write_file" }
func (WriteFileTool) Description() string      { return "Write content to a file (creates or overwrites)." }
func (WriteFileTool) Schema() json.RawMessage  { return writeFileSchema }

func (WriteFileTool) Run(_ context.Context, args json.RawMessage) (string, error) {
	var input struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	if err := json.Unmarshal(args, &input); err != nil {
		return "", fmt.Errorf("decode args: %w", err)
	}
	if input.Path == "" {
		return "", fmt.Errorf("path is required")
	}
	return fmt.Sprintf("(stub) wrote %d bytes to %s", len(input.Content), input.Path), nil
}
