package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// stubTool is a Tool that records the args it received and returns a canned
// result. Used by registry tests that don't need the real fileops behaviour.
type stubTool struct {
	name        string
	description string
	schema      json.RawMessage
	result      string
	err         error
	gotArgs     json.RawMessage
}

func (s *stubTool) Name() string             { return s.name }
func (s *stubTool) Description() string      { return s.description }
func (s *stubTool) Schema() json.RawMessage  { return s.schema }
func (s *stubTool) Run(_ context.Context, args json.RawMessage) (string, error) {
	s.gotArgs = args
	return s.result, s.err
}

func TestRegistry_SchemasReturnsAllRegistered(t *testing.T) {
	reg := NewRegistry()
	// Register out of alphabetical order to prove Schemas sorts.
	reg.Register(&stubTool{name: "zeta", description: "Z", schema: json.RawMessage(`{"type":"object"}`)})
	reg.Register(&stubTool{name: "alpha", description: "A", schema: json.RawMessage(`{"type":"object"}`)})
	reg.Register(&stubTool{name: "mu", description: "M", schema: json.RawMessage(`{"type":"object"}`)})

	got := reg.Schemas()
	if len(got) != 3 {
		t.Fatalf("expected 3 schemas, got %d", len(got))
	}
	wantNames := []string{"alpha", "mu", "zeta"}
	for i, name := range wantNames {
		if got[i].Name != name {
			t.Errorf("schema[%d].Name = %q, want %q (schemas must be alphabetical)", i, got[i].Name, name)
		}
	}
	// Descriptions should round-trip too.
	if got[0].Description != "A" || got[2].Description != "Z" {
		t.Errorf("descriptions did not round-trip; got %+v", got)
	}
}

func TestRegistry_DispatchUnknownToolReturnsErrorString(t *testing.T) {
	reg := NewRegistry()
	reg.Register(&stubTool{name: "known", schema: json.RawMessage(`{"type":"object"}`)})

	out := reg.Dispatch(context.Background(), "missing", json.RawMessage(`{}`))
	if !strings.Contains(out, "unknown tool") {
		t.Errorf("expected error string mentioning 'unknown tool', got %q", out)
	}
	if !strings.Contains(out, "missing") {
		t.Errorf("expected error string to include the offending tool name 'missing', got %q", out)
	}
	// Critical: must NOT contain Go panic markers or be empty — model needs a useful string.
	if out == "" {
		t.Error("Dispatch returned empty string for unknown tool; model would interpret as success")
	}
}

func TestRegistry_DispatchInvalidJSONReturnsErrorString(t *testing.T) {
	reg := NewRegistry()
	reg.Register(&stubTool{name: "ok", schema: json.RawMessage(`{"type":"object"}`)})

	out := reg.Dispatch(context.Background(), "ok", json.RawMessage(`{not json`))
	if !strings.Contains(out, "invalid args") {
		t.Errorf("expected 'invalid args' in error string, got %q", out)
	}
	// Tool.Run must NOT have been called when args fail to parse.
}

func TestRegistry_DispatchToolErrorWrapped(t *testing.T) {
	stub := &stubTool{
		name:   "boom",
		schema: json.RawMessage(`{"type":"object"}`),
		err:    errors.New("disk on fire"),
	}
	reg := NewRegistry()
	reg.Register(stub)

	out := reg.Dispatch(context.Background(), "boom", json.RawMessage(`{}`))
	if !strings.Contains(out, "Error running boom") || !strings.Contains(out, "disk on fire") {
		t.Errorf("expected wrapped tool error, got %q", out)
	}
}

func TestReadFileTool_HappyPath(t *testing.T) {
	tests := []struct {
		name    string
		content string
	}{
		{"ascii", "hello world\n"},
		{"unicode", "héllo · 世界\n"},
		{"empty", ""},
		{"multiline", "line1\nline2\nline3"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "input.txt")
			if err := os.WriteFile(path, []byte(tc.content), 0o644); err != nil {
				t.Fatalf("setup: write file: %v", err)
			}

			reg := NewRegistry()
			reg.Register(ReadFileTool{})

			args, _ := json.Marshal(map[string]string{"path": path})
			out := reg.Dispatch(context.Background(), "read_file", args)
			if out != tc.content {
				t.Errorf("read content mismatch:\n got: %q\nwant: %q", out, tc.content)
			}
		})
	}
}

func TestWriteFileTool_CreatesNestedDir(t *testing.T) {
	root := t.TempDir()
	// Three deep — none of these directories exist yet.
	target := filepath.Join(root, "a", "b", "c.txt")
	payload := "hello from a deeply nested write\n"

	reg := NewRegistry()
	reg.Register(WriteFileTool{})

	args, _ := json.Marshal(map[string]string{"path": target, "content": payload})
	out := reg.Dispatch(context.Background(), "write_file", args)

	if !strings.Contains(out, "Wrote") {
		t.Errorf("expected success message starting with 'Wrote', got %q", out)
	}

	// Verify the directories were created and the file contains payload.
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("expected file at %s, got error: %v", target, err)
	}
	if string(got) != payload {
		t.Errorf("file content mismatch:\n got: %q\nwant: %q", got, payload)
	}

	// Sanity: parent dir really did get auto-created.
	if _, err := os.Stat(filepath.Join(root, "a", "b")); err != nil {
		t.Errorf("expected nested dir to be auto-created: %v", err)
	}
}

func TestReadFileTool_MissingFile(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "does-not-exist.txt")

	reg := NewRegistry()
	reg.Register(ReadFileTool{})

	args, _ := json.Marshal(map[string]string{"path": missing})
	out := reg.Dispatch(context.Background(), "read_file", args)

	// The contract: Dispatch returns a string, never a Go error. The string
	// must mention the failure and the tool name so the model can react.
	if !strings.Contains(out, "Error running read_file") {
		t.Errorf("expected 'Error running read_file' prefix, got %q", out)
	}
	if out == "" {
		t.Error("missing-file error must surface as a non-empty string; got empty")
	}
}
