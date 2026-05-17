# s08 upstream excerpt: skill-system.md L9-L100 (skills, menu, token math)

Source: `guide/skill-system.md` L9-L100 in `nexu-io/harness-engineering-guide`
Permalink: <https://github.com/nexu-io/harness-engineering-guide/blob/86fec9bea430cecb29ff10afaae36b96496a8f8e/guide/skill-system.md#L9-L100>
License: MIT (© 2026 Nexu)

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

For example, a `git` skill doesn't expose a single `git` tool — it bundles
`git_status`, `git_diff`, `git_commit`, `git_push`, `git_log`, and includes
documentation on commit message conventions, branch naming, and when to ask
before pushing.

## Skill vs. Tool

|                  | Tool                 | Skill                                    |
|------------------|----------------------|------------------------------------------|
| Scope            | Single function      | Bundle of related functions              |
| Documentation    | Parameter description| Full SKILL.md with examples, conventions |
| Loading          | Always present/absent| Loaded on demand from a menu             |
| Context cost     | ~100–200 tokens/schema | ~200 token menu + ~1,000 tokens loaded |
| Behavior rules   | None                 | Can include constraints, workflows       |

The distinction matters for token economics. A harness with 80 tools pays
~12,000 tokens per API call just for schemas. A skill system with 15 skills
and a 300-token menu loads only what's needed.

## The Skill Menu Pattern

Instead of loading all tools at startup, present the model with a compact
menu of available skills. The model reads the menu, decides which skill it
needs, and loads it:

    SKILL_MENU = """Available skills (use load_skill to activate):

    - file_ops: Read, write, search, and edit files in the workspace
    - git: Version control — status, diff, commit, push, log
    - web: HTTP requests, web search, URL fetching
    - shell: Execute shell commands in a sandbox
    ...
    """

The menu costs ~150 tokens. Loading a skill adds its SKILL.md (~500–1,000
tokens) and tool schemas (~200–800 tokens). Compared to loading all tools
upfront:

    Strategy                    Tokens (8 skills, ~60 tools)
    ────────────────────────────────────────────────────────
    All tools upfront:          ~12,000 tokens (always)
    Skill menu + 2 loaded:      ~150 + ~2,400 = ~2,550 tokens
    ────────────────────────────────────────────────────────
    Savings:                    ~9,450 tokens per turn (78%)

Over a 30-turn session, that's ~280K tokens saved — real money at API pricing.
```

## Reading notes

1. **Skill is a bundle, not a renamed Tool.** L9-L24 is unusually explicit on this: a `git` skill is *not* a `git` tool. It is `git_status` + `git_diff` + ... + SKILL.md + conventions. If the Go port collapses the two, the whole token math goes out the window. The `Skill` struct in this chapter keeps `Tools []Tool` and `Doc string` as separate fields precisely to preserve that distinction.

2. **The menu format is load-bearing — kind of.** L78-L88 prescribes "Available skills (use load_skill to activate):" plus `- name: description` bullets. The model is trained on broad patterns, not on this exact phrase, so a reasonable rewrite still works. But keeping the header identical to upstream costs nothing and means anyone reading both feels at home. We assert on the header text in `TestRegistry_MenuFormatMatchesGuide` as a regression sentinel.

3. **L91-L101 is the *argument*, not just a table.** It says, with numbers: if you preload everything you pay 12K tokens per turn, vs ~2.5K with on-demand loading. Over 30 turns that's 280K tokens. Skip this passage and a reader can think the skill system is a code-organisation nicety; with this passage they understand it is a financial intervention.

4. **YAML frontmatter vs. simple markdown header.** Upstream's `skills/abuse-hunter/SKILL.md` uses YAML `---name: ... description: ...---`. We use H1 + blockquote because (a) one fewer parser to maintain, (b) the file is more readable as raw markdown, and (c) there is no single point at which `description:` and the prose summary can drift apart. The Go parser at `LoadSkillFromDir` is ~50 lines and has one knob (bufio scanner buffer).

5. **The four pitfalls at L249-L256 are the unit-test backlog.** "No unload mechanism" → `TestRegistry_UnloadFreesContext`. "Monolithic skills" → fixture design (3 small skills, 2 tools each). "Missing SKILL.md" → `LoadSkillFromDir` returns a Go error when SKILL.md is absent or has no H1. "Confusing skill/tool names" → `git` vs `git_status`/`git_diff` naming throughout. If you only read one paragraph of upstream before extending this chapter, read these eight lines.

## Reading map

| Topic | Upstream file | Lines | Mapped chapter |
|-------|---------------|-------|----------------|
| Skill definition (bundle of tools + doc + rules) | `guide/skill-system.md` | L9-L40 | s08 (this) |
| SKILL.md format with conventions and examples | `guide/skill-system.md` | L40-L72 | s08 |
| Skill menu pattern + token math | `guide/skill-system.md` | L73-L102 | s08 |
| Skill registry reference impl in Python | `guide/skill-system.md` | L104-L220 | s08 |
| Thin harness + thick skills architecture | `guide/skill-system.md` | L222-L247 | s08 (architecture note) |
| Common pitfalls (incl. no-unload, name clash) | `guide/skill-system.md` | L249-L256 | s08 (test backlog) |
| Further reading (MCP, Anthropic tool guide) | `guide/skill-system.md` | L258-L262 | (not chapter-ized) |
