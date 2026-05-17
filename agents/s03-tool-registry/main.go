package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// main demonstrates Registry end-to-end without an LLM:
//   1. Register read_file + write_file.
//   2. Show the schemas the model would see (sorted by name).
//   3. Dispatch a write_file call, then a read_file call, then an unknown tool.
//
// Run with:
//   go run . demo
func main() {
	if len(os.Args) < 2 || os.Args[1] != "demo" {
		fmt.Fprintln(os.Stderr, "usage: s03-tool-registry demo")
		fmt.Fprintln(os.Stderr, "  (registers read_file + write_file and exercises Registry.Dispatch)")
		os.Exit(2)
	}

	reg := NewRegistry()
	reg.Register(ReadFileTool{})
	reg.Register(WriteFileTool{})

	fmt.Println("=== Schemas (what the model sees) ===")
	for _, s := range reg.Schemas() {
		fmt.Printf("- %s: %s\n", s.Name, s.Description)
	}

	tmp, err := os.MkdirTemp("", "s03-demo-*")
	if err != nil {
		fmt.Fprintf(os.Stderr, "tempdir: %v\n", err)
		os.Exit(1)
	}
	defer os.RemoveAll(tmp)

	target := filepath.Join(tmp, "nested", "hello.txt")

	fmt.Println("\n=== Dispatch: write_file ===")
	writeArgs, _ := json.Marshal(map[string]string{
		"path":    target,
		"content": "hello from s03\n",
	})
	fmt.Println(reg.Dispatch(context.Background(), "write_file", writeArgs))

	fmt.Println("\n=== Dispatch: read_file ===")
	readArgs, _ := json.Marshal(map[string]string{"path": target})
	fmt.Println(reg.Dispatch(context.Background(), "read_file", readArgs))

	fmt.Println("\n=== Dispatch: unknown tool (returns error string, no panic) ===")
	fmt.Println(reg.Dispatch(context.Background(), "delete_universe", json.RawMessage(`{}`)))
}
