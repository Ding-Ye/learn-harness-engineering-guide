package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// SkillRegistry holds two maps that look similar but serve very different
// purposes:
//
//   - catalog: every known skill. Built once at startup via ScanDir or by
//     calling Add() directly. The menu is rendered from this map.
//   - active : the subset the LLM is currently allowed to see. Mutated by
//     LoadSkill / UnloadSkill.
//
// The whole point of the chapter (guide/skill-system.md L91-L102) is that
// ActiveSchemas returns ONLY tools belonging to active skills. A harness
// with 80 tools but 2 loaded skills only pays the schema cost for those 2 —
// not 80. The catalog stays "for free" because skills are inert until loaded.
//
// Concurrency: every public method takes the mutex. The whole API is
// goroutine-safe so a future s_full integration can have the agentic loop
// dispatch and the harness reload skills concurrently without races.
type SkillRegistry struct {
	mu      sync.RWMutex
	catalog map[string]*Skill
	active  map[string]*Skill
}

// NewSkillRegistry returns an empty registry. Use ScanDir to populate from a
// filesystem skills/ tree, or Add to register Skills programmatically (used
// by tests that prefer to build fixtures in t.TempDir() without sprinkling
// files around).
func NewSkillRegistry() *SkillRegistry {
	return &SkillRegistry{
		catalog: make(map[string]*Skill),
		active:  make(map[string]*Skill),
	}
}

// Add inserts a fully-formed Skill into the catalog. Re-adding under the same
// name overwrites — useful for hot-reload during dev. We do NOT auto-activate
// it; the user (or the model via LoadSkillTool) decides when to spend the
// context budget.
func (r *SkillRegistry) Add(s *Skill) {
	if s == nil || s.Name == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.catalog[s.Name] = s
}

// ScanDir walks rootDir's direct children, treating each subdirectory that
// contains a SKILL.md as a skill. Files at the top level and directories
// without SKILL.md are silently skipped — same shape as the upstream
// _scan() at guide/skill-system.md L128-L155.
//
// ScanDir does NOT recurse: skills are flat. A nested layout would confuse
// the menu (which name takes the slot?) and complicate the unload story.
//
// Note: ScanDir builds Skills with empty Tools slices. The caller is
// responsible for attaching tool implementations after the scan — typically
// by looking up each Skill.Name in a code-side map and calling WithTools.
// See main.go for the canonical wiring.
func (r *SkillRegistry) ScanDir(rootDir string) error {
	entries, err := os.ReadDir(rootDir)
	if err != nil {
		return fmt.Errorf("read skills dir %s: %w", rootDir, err)
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dir := filepath.Join(rootDir, e.Name())
		skillMD := filepath.Join(dir, "SKILL.md")
		if _, err := os.Stat(skillMD); err != nil {
			// Missing SKILL.md → not a skill directory, skip silently. Any
			// other stat error we still skip rather than abort, because one
			// broken dir should not blow away the rest of the catalog.
			continue
		}
		s, err := LoadSkillFromDir(dir)
		if err != nil {
			return fmt.Errorf("load skill at %s: %w", dir, err)
		}
		r.mu.Lock()
		r.catalog[s.Name] = s
		r.mu.Unlock()
	}
	return nil
}

// Catalog returns a snapshot of all known skills, keyed by Name. The returned
// map is a fresh copy — callers can iterate without holding the registry
// mutex, and mutating the returned map has no effect on the registry.
func (r *SkillRegistry) Catalog() map[string]*Skill {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make(map[string]*Skill, len(r.catalog))
	for k, v := range r.catalog {
		out[k] = v
	}
	return out
}

// Menu renders the human-readable "Available skills" text from
// guide/skill-system.md L74-L88. The exact wording matters: tests assert the
// "Available skills" header survives so anyone replacing the registry can
// keep the menu prompt compatible.
//
// Format:
//
//	Available skills (use load_skill to activate):
//
//	- name1: description1
//	- name2: description2 [loaded]
//
// Sorted by name so the menu is deterministic across runs.
func (r *SkillRegistry) Menu() string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	names := make([]string, 0, len(r.catalog))
	for name := range r.catalog {
		names = append(names, name)
	}
	sort.Strings(names)

	var b strings.Builder
	b.WriteString("Available skills (use load_skill to activate):\n\n")
	for _, name := range names {
		s := r.catalog[name]
		suffix := ""
		if _, ok := r.active[name]; ok {
			suffix = " [loaded]"
		}
		fmt.Fprintf(&b, "- %s: %s%s\n", s.Name, s.Description, suffix)
	}
	return strings.TrimRight(b.String(), "\n")
}

// LoadSkill activates a skill by name and returns the load message the model
// reads back. Mirrors the Python load_skill at guide/skill-system.md
// L165-L179: include the tool list AND the full SKILL.md body so the model
// gets both "what tools are now available" and "how to use them".
//
// Returns an error only when callers want to react programmatically. The
// LoadSkillTool wrapper (see meta_tools.go) converts any error to a string
// for the model, so the LLM-facing path never sees a Go error.
func (r *SkillRegistry) LoadSkill(name string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	skill, ok := r.catalog[name]
	if !ok {
		return "", fmt.Errorf("unknown skill '%s'", name)
	}
	if _, already := r.active[name]; already {
		return fmt.Sprintf("Skill '%s' is already loaded.", name), nil
	}
	r.active[name] = skill

	toolNames := make([]string, 0, len(skill.Tools))
	for _, t := range skill.Tools {
		toolNames = append(toolNames, t.Name())
	}
	return fmt.Sprintf(
		"Loaded skill '%s' with %d tools: %s\n\nDocumentation:\n%s",
		name,
		len(skill.Tools),
		strings.Join(toolNames, ", "),
		skill.Doc,
	), nil
}

// UnloadSkill removes a skill from the active set. Returns an error if it was
// not loaded. Unloading is the OTHER half of context economics: keeping a
// skill active forever defeats the "save tokens" promise. See pitfall L254.
func (r *SkillRegistry) UnloadSkill(name string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.active[name]; !ok {
		return fmt.Errorf("skill '%s' is not loaded", name)
	}
	delete(r.active, name)
	return nil
}

// ActiveSchemas returns the ToolSchema rows for every tool in every currently
// active skill. This is the slice the LLM request builder appends into
// `tools: [...]` — the *whole point* of the chapter is that this slice
// stays small when fewer skills are loaded.
//
// The list is sorted by tool name so prompt caching stays stable across
// turns (an LLM provider that fingerprints the tool array would otherwise
// miss the cache on every shuffle).
func (r *SkillRegistry) ActiveSchemas() []ToolSchema {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var out []ToolSchema
	for _, s := range r.active {
		for _, t := range s.Tools {
			out = append(out, toSchema(t))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// ActiveSkillNames returns the names of currently loaded skills, sorted.
// Useful for tests and the demo CLI; not load-bearing.
func (r *SkillRegistry) ActiveSkillNames() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	names := make([]string, 0, len(r.active))
	for name := range r.active {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// DispatchTool runs one tool call by walking the active skills until it finds
// a tool with the matching name. Same contract as s03.Registry.Dispatch:
// never panics, always returns a string the model can read.
//
// Error shapes:
//
//   - "Error: tool 'X' not found. Is the skill loaded?"  ← matches the
//     upstream wording at L219 to make this code feel familiar to anyone
//     reading both.
//   - "Error: invalid args: <reason>"                    ← args don't parse
//   - "Error running X: <msg>"                           ← tool itself errored
func (r *SkillRegistry) DispatchTool(ctx context.Context, name string, args json.RawMessage) string {
	r.mu.RLock()
	// Find the tool while holding the read lock, then release before calling
	// Run — the tool may itself reach back into the registry (e.g. a tool
	// that loads another skill) and we don't want to deadlock.
	var found Tool
	for _, s := range r.active {
		for _, t := range s.Tools {
			if t.Name() == name {
				found = t
				break
			}
		}
		if found != nil {
			break
		}
	}
	r.mu.RUnlock()

	if found == nil {
		return fmt.Sprintf("Error: tool '%s' not found. Is the skill loaded?", name)
	}
	if len(args) > 0 {
		var probe any
		if err := json.Unmarshal(args, &probe); err != nil {
			return fmt.Sprintf("Error: invalid args: %v", err)
		}
	}
	out, err := found.Run(ctx, args)
	if err != nil {
		return fmt.Sprintf("Error running %s: %v", name, err)
	}
	return out
}
