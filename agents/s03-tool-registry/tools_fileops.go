package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// readFileSchema is the JSON Schema for ReadFileTool's input. It mirrors the
// upstream Python schema at guide/your-first-harness.md L46-L54.
//
// We keep this as a package-level json.RawMessage so Schema() returns the same
// bytes every time (json.Marshal of a Go map yields randomly-ordered keys —
// raw bytes avoid that drift and keep prompt caching stable).
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

// ReadFileTool reads the contents of a file at the given path and returns
// them as a UTF-8 string. Errors (missing file, permission denied) come back
// as Go errors so Registry.Dispatch can wrap them into the canonical
// "Error running read_file: ..." string the model expects.
//
// Source parallel: guide/your-first-harness.md L77-L79 (Python execute_tool's
// read_file branch).
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
	data, err := os.ReadFile(input.Path)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", input.Path, err)
	}
	return string(data), nil
}

// writeFileSchema mirrors guide/your-first-harness.md L60-L68.
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

// WriteFileTool writes content to a file, creating parent directories as
// needed. The upstream Python at guide/your-first-harness.md L81-L84 does
// the same dance with os.makedirs(..., exist_ok=True) before opening the
// file for write — we use os.MkdirAll for that.
//
// On success the tool returns a short status string ("Wrote N chars to P") so
// the model gets confirmation of the size and target path — useful for
// downstream reasoning ("now read it back to verify").
type WriteFileTool struct{}

func (WriteFileTool) Name() string             { return "write_file" }
func (WriteFileTool) Description() string      { return "Write content to a file (creates or overwrites). Parent directories are created if missing." }
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
	// os.MkdirAll handles the "parent dir does not exist yet" case the
	// upstream Python guards with os.makedirs(dirname or ".", exist_ok=True).
	dir := filepath.Dir(input.Path)
	if dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return "", fmt.Errorf("mkdir %s: %w", dir, err)
		}
	}
	if err := os.WriteFile(input.Path, []byte(input.Content), 0o644); err != nil {
		return "", fmt.Errorf("write %s: %w", input.Path, err)
	}
	return fmt.Sprintf("Wrote %d chars to %s", len(input.Content), input.Path), nil
}
