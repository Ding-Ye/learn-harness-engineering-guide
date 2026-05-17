package main

import (
	"path/filepath"
	"strings"
)

// Tier 1 + Tier 2 from the upstream three-tier decision flow
// (classifier-permissions.md L113-L141). These run before the classifier even
// looks at the request, and they short-circuit the LLM call so the agent stays
// responsive on the common path:
//
//	Tier 1: built-in safe reads + user allow-list  → ALLOW immediately
//	Tier 2: any read/write under the repo root     → ALLOW immediately
//	Tier 3: everything else                         → invoke classifier
//
// The whole point of tiering is latency: a `read_file` that has to spin up
// Sonnet 4.6 makes the agent feel sluggish. Tier 1+2 cover the vast majority
// of tool calls; tier 3 is reserved for the few that can affect the world
// outside the repo.

// DefaultWhitelistTools is the built-in safe-read set. Matches the upstream
// "Built-in read ops" example at L122-L124. A real harness would let the user
// extend this via config; here we expose it as a package-level slice so the
// demo and tests can reuse it without re-typing.
var DefaultWhitelistTools = []string{
	"read_file",
	"list_dir",
	"glob",
}

// WhitelistMatcher implements Tier 1. It carries a list of tool names that are
// always safe regardless of arguments — pure reads, no side effects, no
// network. The matcher is intentionally name-only: it does NOT inspect args,
// because that would re-introduce the path-glob complexity from s06's
// allow-list, and the upstream's whole pitch for Tier 1 is "no inspection,
// just ALLOW".
type WhitelistMatcher struct {
	tools []string
}

// NewWhitelistMatcher constructs a WhitelistMatcher from a slice of tool names.
// We make a defensive copy so callers can mutate the slice they passed in
// without affecting the matcher's view of the world.
func NewWhitelistMatcher(tools []string) *WhitelistMatcher {
	cp := make([]string, len(tools))
	copy(cp, tools)
	return &WhitelistMatcher{tools: cp}
}

// Match reports whether toolName is in the whitelist. args is accepted for
// interface symmetry with RepoPathMatcher but is unused — see the type doc
// for why we deliberately don't inspect it.
func (w *WhitelistMatcher) Match(toolName string, args map[string]any) bool {
	_ = args
	for _, t := range w.tools {
		if t == toolName {
			return true
		}
	}
	return false
}

// RepoPathMatcher implements Tier 2. The reasoning from L143-L145 is that
// in-project edits get caught by `git diff` anyway — git is your safety net,
// so the classifier doesn't need to gate them. The matcher returns true if
// the tool call carries a string "path" argument that, after Clean + Abs,
// has the repo root as a prefix.
//
// Note that "is path under repo" is intentionally a single-arg check. Tools
// with multiple paths (e.g. "move_file" with src + dst) would need a richer
// matcher; we stick to the single-path pattern because it covers read_file,
// write_file, and edit_file — the majority of in-project tools.
type RepoPathMatcher struct {
	// root is the absolute, cleaned repo root. We store it pre-normalized so
	// every Match() call is a string-prefix check, no I/O.
	root string
}

// NewRepoPathMatcher normalizes root via Abs + Clean. If filepath.Abs fails
// (which on practice basically never happens for relative paths — it just
// joins to the cwd), we fall back to Clean alone. We document this fallback
// rather than panicking because the matcher is intended to be permissive on
// errors (Tier 2 says "not matched, fall through to Tier 3"), and a panic at
// construction time would be ugly for downstream callers.
func NewRepoPathMatcher(root string) *RepoPathMatcher {
	abs, err := filepath.Abs(root)
	if err != nil {
		abs = filepath.Clean(root)
	}
	return &RepoPathMatcher{root: filepath.Clean(abs)}
}

// Match returns true if args carries a string "path" that resolves to a
// location under the matcher's root.
//
// Implementation notes:
//
//   - We accept BOTH "path" and "file_path" keys because the upstream guide
//     uses both spellings interchangeably (`read_file` takes "path",
//     `write_file` takes "file_path"). A real harness would converge on one;
//     we accept both for the demo.
//
//   - filepath.HasPrefix is the obvious choice but is deliberately undocumented
//     in Go's stdlib and lies about "filepath.HasPrefix is deprecated" in some
//     versions. We use plain strings.HasPrefix after appending a separator —
//     same intent, fewer surprises.
//
//   - We require the path to be UNDER root, not equal to root: a tool call
//     that targets the repo root itself isn't a meaningful "in-project edit".
//     The trailing separator on `root` enforces this.
func (r *RepoPathMatcher) Match(toolName string, args map[string]any) bool {
	_ = toolName // tier 2 is about the path, not the tool.
	if r.root == "" || args == nil {
		return false
	}
	var raw string
	if p, ok := args["path"].(string); ok {
		raw = p
	} else if p, ok := args["file_path"].(string); ok {
		raw = p
	}
	if raw == "" {
		return false
	}

	abs, err := filepath.Abs(raw)
	if err != nil {
		abs = filepath.Clean(raw)
	}
	abs = filepath.Clean(abs)

	// Compare with a trailing separator so "/repo" doesn't match "/repos".
	rootWithSep := r.root + string(filepath.Separator)
	return strings.HasPrefix(abs, rootWithSep)
}
