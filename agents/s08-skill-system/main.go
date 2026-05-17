package main

import (
	"encoding/json"
	"fmt"
	"os"

	fileops "learn-harness-engineering-guide/s08-skill-system/skills/file_ops"
	gitops "learn-harness-engineering-guide/s08-skill-system/skills/git"
	webops "learn-harness-engineering-guide/s08-skill-system/skills/web"
)

// main walks the demo:
//   1. Scan ./skills to build the catalog from SKILL.md files.
//   2. Attach tool implementations by name (the parser does not know Go
//      types; the code-side map below is the source of truth).
//   3. Render the menu — this is what the model would see at startup.
//   4. Load the "git" skill via LoadSkillTool — exercise the full meta-tool
//      path (JSON args in, string out), exactly like the LLM would.
//   5. Print ActiveSchemas() so the reader sees that only the loaded skill's
//      tools appear, confirming the L91-L101 token-saving promise.
//
// Run with:
//   go run .
func main() {
	registry := NewSkillRegistry()

	if err := registry.ScanDir("./skills"); err != nil {
		fmt.Fprintf(os.Stderr, "scan: %v\n", err)
		os.Exit(1)
	}

	// Attach Go-side tools to the skills parsed from disk. This is the
	// "wiring" step the SKILL.md parser cannot do on its own — it knows
	// names, not types.
	for name, s := range registry.Catalog() {
		switch name {
		case "file_ops":
			s.WithTools(fileops.ReadFileTool{}, fileops.WriteFileTool{})
		case "git":
			s.WithTools(gitops.StatusTool{}, gitops.DiffTool{})
		case "web":
			s.WithTools(webops.GetTool{}, webops.PostTool{})
		}
	}

	fmt.Println("=== Skill menu (this is what the model sees) ===")
	fmt.Println(registry.Menu())

	fmt.Println("\n=== ActiveSchemas() before loading anything ===")
	printSchemas(registry.ActiveSchemas())

	fmt.Println("\n=== Loading 'git' via the load_skill meta-tool ===")
	loader := LoadSkillTool{Registry: registry}
	args := json.RawMessage(`{"name":"git"}`)
	out, err := loader.Run(nil, args)
	if err != nil {
		fmt.Fprintf(os.Stderr, "load: %v\n", err)
		os.Exit(1)
	}
	fmt.Println(out)

	fmt.Println("\n=== ActiveSchemas() after loading 'git' ===")
	printSchemas(registry.ActiveSchemas())

	fmt.Println("\n=== Dispatching one git tool call ===")
	fmt.Println(registry.DispatchTool(nil, "git_status", json.RawMessage(`{}`)))
}

// printSchemas formats a []ToolSchema as "- name: description" lines so the
// demo output reads at a glance. We deliberately avoid json.MarshalIndent
// here: the point is to show *which tools are visible*, not the JSON shape
// (s03 already covered schemas in detail).
func printSchemas(schemas []ToolSchema) {
	if len(schemas) == 0 {
		fmt.Println("(none — no skill is loaded yet)")
		return
	}
	for _, s := range schemas {
		fmt.Printf("- %s: %s\n", s.Name, s.Description)
	}
}
