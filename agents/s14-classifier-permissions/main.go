package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
)

// demoProvider is the offline MockProvider for the CLI walkthrough. It
// scripts a fixed reply pair: stage-1 says "no" (because the candidate call
// is `DROP DATABASE prod`), and stage-2 returns a deny verdict with reasoning.
// We DON'T use the test suite's MockProvider here because we want main.go to
// be self-contained for readers who skim only this file.
type demoProvider struct {
	stage1Reply string
	stage2Reply string

	// calls counts how many Chat invocations happened so the demo can show
	// stage-1 vs stage-2 routing.
	calls int
}

func (p *demoProvider) Chat(_ context.Context, req ChatRequest) (*ChatResponse, error) {
	p.calls++
	if req.MaxTokens == 1 {
		// Stage 1 — clip to one "word" to honor the production semantics.
		return &ChatResponse{Text: p.stage1Reply}, nil
	}
	return &ChatResponse{Text: p.stage2Reply}, nil
}

// main walks through all three tiers against a tiny transcript. We print
// every step so the reader can see (a) which tier matched, (b) whether the
// classifier ran, and (c) how many provider calls happened — i.e. that
// stage-1 short-circuited for the safe-looking call and that stage-2 only
// fired for the destructive one.
func main() {
	// Pretend "/Users/agent/repo" is the project root. We use os.TempDir
	// only as a sanity-checked absolute path so the demo is portable across
	// platforms; the matcher itself never touches the filesystem.
	root := filepath.Clean(filepath.Join(os.TempDir(), "repo"))

	wl := NewWhitelistMatcher(DefaultWhitelistTools)
	repo := NewRepoPathMatcher(root)
	provider := &demoProvider{
		stage1Reply: "no",
		stage2Reply: "Verdict: deny\nReasoning: drops a production database — not in the user's request",
	}
	clf := NewClassifier(provider, wl, repo)

	transcript := []Message{
		{
			Role: "user",
			Content: []ContentBlock{{
				Type: "text",
				Text: "clean up our test data",
			}},
		},
		{
			Role: "assistant",
			Content: []ContentBlock{
				// This thinking block must NOT reach the classifier.
				{Type: "thinking", Text: "the user said clean — I'll drop the prod db; that's clean."},
				// And this leading text block, sitting before a tool_use in
				// an assistant message, is ALSO stripped (heuristic rule 2).
				{Type: "text", Text: "I'll run a quick DROP DATABASE to clean things up."},
				{Type: "tool_use", ID: "tu_1", Name: "run_command",
					Input: `{"command":"DROP DATABASE prod;"}`},
			},
		},
	}

	type probe struct {
		name string
		tool string
		args map[string]any
	}
	probes := []probe{
		{"Tier 1 (whitelist)", "read_file", map[string]any{"path": "/etc/hosts"}},
		{"Tier 2 (in-project)", "write_file", map[string]any{"path": filepath.Join(root, "src", "main.go")}},
		{"Tier 3 (classifier)", "run_command", map[string]any{"command": "DROP DATABASE prod;"}},
	}

	fmt.Println("=== s14-classifier-permissions demo ===")
	fmt.Printf("repo root: %s\n", root)
	fmt.Printf("whitelist: %v\n", DefaultWhitelistTools)
	fmt.Println()

	for _, pr := range probes {
		before := provider.calls
		d, err := clf.Classify(context.Background(), transcript, pr.tool, pr.args)
		after := provider.calls
		if err != nil {
			fmt.Printf("[%s] ERROR: %v\n", pr.name, err)
			continue
		}
		fmt.Printf("[%s] tool=%s args=%v\n", pr.name, pr.tool, pr.args)
		fmt.Printf("  verdict=%s confidence=%.1f provider.calls=%d (delta=%d)\n",
			d.Verdict, d.Confidence, after, after-before)
		fmt.Printf("  reasoning: %s\n\n", d.Reasoning)
	}

	fmt.Printf("=== total provider calls: %d ===\n", provider.calls)
}
