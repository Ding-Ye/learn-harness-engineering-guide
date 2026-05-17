package main

import (
	"context"
	"encoding/json"
	"fmt"
)

// list_skills and load_skill are "meta-tools" — they manage the skill
// registry rather than doing any domain work. They are always added to the
// LLM's tool list (see ActiveSchemas + main.go wiring) so the model can
// inspect and load skills entirely through the standard tool-call channel.
//
// Mirrors the meta-tool schema sketched at guide/skill-system.md L193-L204.

// listSkillsSchema describes the (empty) input of list_skills. We still emit
// a proper object schema with no required fields, because some providers
// (Anthropic in particular) reject tools that declare no schema at all.
var listSkillsSchema = json.RawMessage(`{
  "type": "object",
  "properties": {},
  "additionalProperties": false
}`)

// ListSkillsTool returns the human-readable skill menu.
//
// The model calls this when it has not yet decided which skill it needs —
// the menu is much smaller than the full tool catalog, so this round-trip is
// cheap and intentional.
type ListSkillsTool struct {
	Registry *SkillRegistry
}

func (ListSkillsTool) Name() string             { return "list_skills" }
func (ListSkillsTool) Description() string      { return "List available skills the model can load on demand." }
func (ListSkillsTool) Schema() json.RawMessage  { return listSkillsSchema }

func (t ListSkillsTool) Run(_ context.Context, _ json.RawMessage) (string, error) {
	if t.Registry == nil {
		return "", fmt.Errorf("list_skills: no registry wired")
	}
	return t.Registry.Menu(), nil
}

// loadSkillSchema mirrors guide/skill-system.md L194-L204.
var loadSkillSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "name": {
      "type": "string",
      "description": "Skill name from the menu (one of the entries returned by list_skills)."
    }
  },
  "required": ["name"]
}`)

// LoadSkillTool activates one skill by name. On success the model receives
// the full SKILL.md body so it knows what conventions to follow; on failure
// (unknown skill) it receives a clear error string and can recover by
// calling list_skills.
type LoadSkillTool struct {
	Registry *SkillRegistry
}

func (LoadSkillTool) Name() string             { return "load_skill" }
func (LoadSkillTool) Description() string      { return "Activate a skill by name to expose its tools and documentation." }
func (LoadSkillTool) Schema() json.RawMessage  { return loadSkillSchema }

func (t LoadSkillTool) Run(_ context.Context, args json.RawMessage) (string, error) {
	if t.Registry == nil {
		return "", fmt.Errorf("load_skill: no registry wired")
	}
	var input struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(args, &input); err != nil {
		return "", fmt.Errorf("decode args: %w", err)
	}
	if input.Name == "" {
		return "", fmt.Errorf("name is required")
	}
	out, err := t.Registry.LoadSkill(input.Name)
	if err != nil {
		// The contract for *model-facing* tool output is "always return a
		// string the model can read." We mirror the upstream wording at L168
		// so unknown-skill errors include a hint about the menu.
		return fmt.Sprintf("Error: %s. Check the skill menu with list_skills.", err), nil
	}
	return out, nil
}

// unloadSkillSchema is intentionally narrow — name only.
var unloadSkillSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "name": {
      "type": "string",
      "description": "Skill name to unload."
    }
  },
  "required": ["name"]
}`)

// UnloadSkillTool removes a skill from the active set. Pairs with
// LoadSkillTool to close the loop on context budget — without an unload
// path, a long session monotonically grows the active set (the L254
// pitfall).
type UnloadSkillTool struct {
	Registry *SkillRegistry
}

func (UnloadSkillTool) Name() string             { return "unload_skill" }
func (UnloadSkillTool) Description() string      { return "Deactivate a previously-loaded skill to free context budget." }
func (UnloadSkillTool) Schema() json.RawMessage  { return unloadSkillSchema }

func (t UnloadSkillTool) Run(_ context.Context, args json.RawMessage) (string, error) {
	if t.Registry == nil {
		return "", fmt.Errorf("unload_skill: no registry wired")
	}
	var input struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(args, &input); err != nil {
		return "", fmt.Errorf("decode args: %w", err)
	}
	if input.Name == "" {
		return "", fmt.Errorf("name is required")
	}
	if err := t.Registry.UnloadSkill(input.Name); err != nil {
		return fmt.Sprintf("Error: %s.", err), nil
	}
	return fmt.Sprintf("Unloaded skill '%s'.", input.Name), nil
}
