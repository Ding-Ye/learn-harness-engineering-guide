package main

import (
	"fmt"
	"os"
)

// main is a tiny CLI demo of the two-tier memory store:
//
//  1. Create a Memory in a fresh tmpdir.
//  2. Drop a curated MEMORY.md so Read has something to combine with.
//  3. Append three log lines for "today".
//  4. Print the combined view that a session would consume at startup.
//
// Run with:
//
//	go run .
func main() {
	dir, err := os.MkdirTemp("", "s05-memory-demo-")
	if err != nil {
		fmt.Fprintln(os.Stderr, "tmp dir:", err)
		os.Exit(1)
	}
	defer os.RemoveAll(dir)

	mem, err := New(dir, RealClock{})
	if err != nil {
		fmt.Fprintln(os.Stderr, "memory:", err)
		os.Exit(1)
	}

	// Seed a curated long-term memory file so Read has the long-term tier.
	longTerm := "# Long-term Memory\n\n## User Preferences\n- Prefers explicit error messages\n"
	if err := os.WriteFile(dir+"/"+longTermFile, []byte(longTerm), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "seed MEMORY.md:", err)
		os.Exit(1)
	}

	// Append three log lines — these all land in today's YYYY-MM-DD.md.
	entries := []string{
		"## 09:00 — Started session",
		"- Reviewed inbox, no urgent items",
		"## 10:30 — Refactored memory layer",
	}
	for _, e := range entries {
		if err := mem.AppendLog(e); err != nil {
			fmt.Fprintln(os.Stderr, "append log:", err)
			os.Exit(1)
		}
	}

	combined, err := mem.Read()
	if err != nil {
		fmt.Fprintln(os.Stderr, "read:", err)
		os.Exit(1)
	}

	fmt.Println("=== combined memory view (long-term + today + yesterday) ===")
	fmt.Println(combined)
}
