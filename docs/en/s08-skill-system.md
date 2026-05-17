# s08 — Skill System

> Skills are *bundles* of tools + docs + behavior rules, loaded on demand from a menu. Only the active skill's tool schemas reach the LLM — so a harness with 80 tools and 2 loaded skills pays for 2.

## Problem

s03 introduced a `Registry` and we happily stuffed every tool into it. That works for a tutorial — but `guide/skill-system.md` L91-L101 spells out the cost in production:

| Strategy | Tokens (8 skills, ~60 tools) |
| --- | --- |
| All tools upfront | ~12,000 (every turn) |
| Skill menu + 2 loaded | ~150 menu + ~2,400 loaded = ~2,550 |

Per turn — every turn. On a 30-turn session that is ~280K tokens saved. The problem is not "more tools is good"; the problem is that *the model only needs a handful of tools at any one moment*, and the schemas it doesn't use are dead weight on every API call.

There is also a quality angle. With 80 schemas in front of it the model picks the right tool less reliably. A focused subset improves selection accuracy. So skill loading is both a money-saving move and a quality-improving move.

## Solution

A `Skill` is a *bundle*:

```go
type Skill struct {
    Name        string
    Description string  // shown in the menu
    Doc         string  // full SKILL.md, returned to the model on load
    Tools       []Tool  // schemas/handlers activated by Load
}
```

A `SkillRegistry` holds two maps:

```go
type SkillRegistry struct {
    catalog map[string]*Skill  // every skill we know about
    active  map[string]*Skill  // the subset the LLM currently sees
}
```

The model interacts with the registry through two meta-tools — `list_skills` (menu) and `load_skill(name)` (activate a bundle). Both are normal `Tool` implementations. There is no special "skill RPC"; the LLM's existing tool-call channel does all the work.

```go
mt1 := ListSkillsTool{Registry: reg}      // returns the menu
mt2 := LoadSkillTool{Registry: reg}       // mutates active set
mt3 := UnloadSkillTool{Registry: reg}     // closes the loop (L254 pitfall)
```

`ActiveSchemas()` returns the slice of `ToolSchema` for every tool in every active skill — that slice is what the harness pastes into the LLM's `tools: [...]` array on each call. Empty active set → empty schemas slice → the model only sees the three meta-tools, no domain tools.

## How It Works

**SKILL.md parsing.** `LoadSkillFromDir(dir)` reads a single SKILL.md and pulls out:

- the first H1 (`# git`) → `Name`
- the first blockquote (`> Inspect the working tree.`) → `Description`
- the whole body (H1 and blockquote included) → `Doc`

We chose this simple shape over the upstream YAML-frontmatter style for three reasons: (1) the two-line header is visually obvious so the file doubles as readable docs; (2) the blockquote IS the menu description, so there's no risk of `description:` drifting from the prose summary; (3) the *entire* SKILL.md becomes `Doc`, which is exactly what L175-L178 wants the model to see on load.

`LoadSkillFromDir` does not magically attach Go tool implementations — it only parses markdown. The caller of `Skill.WithTools(...)` wires the code to the bundle. In `main.go` that wiring is a tiny `switch` keyed on `Skill.Name`. In tests it is in-line. Either way, the parser and the type system stay decoupled.

**Catalog vs active.** `ScanDir(rootDir)` walks the children of `rootDir`, picks up any subdir with a `SKILL.md`, and adds the parsed `Skill` to the catalog. Nothing is activated yet. The catalog stays "for free" because skills are inert until loaded — no schemas, no handlers, no token cost.

`LoadSkill(name)` moves a `Skill*` from catalog to active and returns the message the model reads back: a one-liner ("Loaded skill 'git' with 2 tools: git_status, git_diff") followed by the full `Doc`. Why include the whole doc on load? Because the SKILL.md is the skill's *brain* — conventions, examples, when-to-use — and the model needs all of that to use the skill correctly. The menu line is too short.

`UnloadSkill(name)` deletes from the active map. The catalog entry stays so the model can re-load later without re-scanning the filesystem.

**`ActiveSchemas()`** is the load-bearing method. It walks `active`, collects every `Tool` schema, sorts by tool name (so prompt caching stays stable across turns), and returns the slice. That slice is what the harness pastes into the LLM request's `tools` field. If `active` is empty, the slice is empty.

**`DispatchTool(ctx, name, args)`** is the other half — find the tool in the active set, run it, format the result as a string the model can read. Three failure modes each become a specific string:

- `Error: tool 'X' not found. Is the skill loaded?` — mirrors the L219 wording so the model gets a familiar hint.
- `Error: invalid args: <reason>` — the args don't parse as JSON.
- `Error running X: <msg>` — the tool itself returned an error.

The model NEVER sees a Go panic or an empty string for any of these — that's the contract from the s03 tool registry, kept intact here.

**Concurrency.** Every public method takes a mutex. The whole API is goroutine-safe; a future `s_full` integration can have the agentic loop dispatch tools concurrently with skill load/unload calls without races.

## What Changed

| | s03 (tool-registry) | s08 (skill-system) |
| --- | --- | --- |
| Granularity | one Tool | a *bundle* of Tools + doc |
| Tools visible to model | all registered | only those in active skills |
| Loading | startup-only | on demand via meta-tool |
| Storage of metadata | code only | SKILL.md on disk |
| Context cost | linear in #tools | bounded by active set |

s03's `Registry` is still the right primitive for a small set of always-on tools (a `list_skills`/`load_skill` pair, for example). s08 sits above it conceptually: the *active* set in s08 is what s03-style code would receive at any given moment. They compose; the chapter does not import s03, matching the curriculum's no-cross-chapter-import rule.

## Try It

```bash
cd agents/s08-skill-system
go test -count=1 -race ./...
# PASS — 7 tests, race detector clean.

go run .
# === Skill menu (this is what the model sees) ===
# Available skills (use load_skill to activate):
#
# - file_ops: Read and write files within the workspace.
# - git: Inspect working tree status and diffs for the current repository.
# - web: Fetch HTTP resources and post JSON bodies to remote endpoints.
#
# === ActiveSchemas() before loading anything ===
# (none — no skill is loaded yet)
#
# === Loading 'git' via the load_skill meta-tool ===
# Loaded skill 'git' with 2 tools: git_status, git_diff
# ...
# === ActiveSchemas() after loading 'git' ===
# - git_diff: Show changes, staged or unstaged.
# - git_status: Show working tree status.
```

The fixture skills under `skills/file_ops/`, `skills/git/`, `skills/web/` show the recommended layout: one directory per skill, a `SKILL.md` plus a `tools.go` in its own sub-package. Each sub-package's tool types satisfy `package main`'s `Tool` interface *structurally*, so there is no import cycle even though both halves live in the same module.

## Upstream Source Reading

Source: `guide/skill-system.md` L9-L100. Permalink: <https://github.com/nexu-io/harness-engineering-guide/blob/86fec9bea430cecb29ff10afaae36b96496a8f8e/guide/skill-system.md#L9-L100>

```markdown
## What is a Skill?

A tool is a single function the model can call. A **skill** is a packaged
capability that bundles:

- **Tools** — one or more related function schemas and handlers
- **Documentation** — a SKILL.md file explaining when and how to use the skill
- **Behavior rules** — constraints, patterns, and conventions for the model
  to follow

skill/
├── SKILL.md          # Documentation: when to use, how to use, constraints
├── tools.py          # Tool implementations
└── schema.json       # Tool schemas (or generated from code)
```

Reading notes:

- **Skill ≠ tool.** The whole chapter pivots on this distinction. A tool is a function; a skill is a *bundle* (tools + docs + rules). Conflate them and you end up shipping the upstream all-tools-upfront anti-pattern (L251) under a different name.
- **The menu is ~150 tokens; full schemas are ~12,000.** The math at L91-L101 is the load-bearing argument. If you change the menu line format, the savings stay in the same ballpark; if you change the *strategy* (e.g. preload all schemas anyway), you give the savings back.
- **SKILL.md format is descriptive, not normative.** Upstream uses YAML frontmatter for `name` + `description`. We use H1 + blockquote because it is one fewer parser and the file doubles as readable markdown. The model only cares that the body it gets at load time *contains* the conventions; the wrapper choice is up to the harness author.
- **Distinct names matter.** L255 warns against having a skill named `git` and a tool also named `git` — the model will try to call the skill name as if it were a tool. Our fixture uses `git` for the skill and `git_status`/`git_diff` for the tools, matching L24.
- **Unload is not optional.** L254 calls out that without `unload_skill`, the active set grows monotonically and you lose the savings. The Go API ships `UnloadSkill` from day one, with a dedicated test (`TestRegistry_UnloadFreesContext`) so a refactor can't quietly drop it.

Reading map:

| Topic | Upstream file | Lines | Mapped chapter |
| --- | --- | --- | --- |
| Skill definition + structure | `guide/skill-system.md` | L9-L40 | s08 (this) |
| Menu pattern + token math | `guide/skill-system.md` | L73-L102 | s08 |
| Skill registry implementation | `guide/skill-system.md` | L104-L220 | s08 |
| Thin harness + thick skills | `guide/skill-system.md` | L222-L247 | s08 (architecture note) |
| Common pitfalls | `guide/skill-system.md` | L249-L256 | s08 (informs Unload/naming) |
