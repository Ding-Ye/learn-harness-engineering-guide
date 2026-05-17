# learn-harness-engineering-guide

> Re-implement the Nexu **Harness Engineering Guide** in working Go — 14 chapters at a time.
>
> [English](#english) · [中文](README.zh-CN.md)

[![Go](https://img.shields.io/badge/Go-1.21+-blue?logo=go)](https://golang.org)
[![License](https://img.shields.io/badge/license-MIT-green)](LICENSE)
[![Upstream](https://img.shields.io/badge/upstream-nexu--io/harness--engineering--guide-orange)](https://github.com/nexu-io/harness-engineering-guide)

## English

The upstream [Harness Engineering Guide](https://github.com/nexu-io/harness-engineering-guide) is ~13K lines of markdown across 25 guides explaining how to build an AI agent harness: the agentic loop, tool registry, memory tiers, guardrails, sub-agents, classifier permissions — the whole stack that turns a bare LLM into a working agent.

This repo turns that prose into **14 progressive Go chapters**, each:

- ~150-700 LOC of idiomatic Go
- Self-contained module (its own `go.mod` — no cross-chapter imports)
- 3-8 unit tests, all passing in CI without an API key
- Bilingual `docs/zh/` + `docs/en/` with a six-section spine: **Problem → Solution → How It Works → What Changed → Try It → Upstream Source Reading**
- An annotated excerpt of the relevant upstream guide file pinned to a specific upstream commit

The first chapter (s01) lands on day one with the agentic loop. Subsequent chapters add one mechanism each.

### Curriculum (14 chapters)

| # | Chapter | Mechanism | Status |
|---|---------|-----------|:------:|
| s01 | [Minimum loop](docs/en/s01-minimum-loop.md) | think → act → observe | ✅ |
| s02 | LLM provider | Anthropic / mock behind one interface | ⏳ |
| s03 | Tool registry | Schemas + dispatch | ⏳ |
| s04 | Context assembler | Priority packing, token budget | ⏳ |
| s05 | Memory layer | MEMORY.md + daily logs | ⏳ |
| s06 | Guardrails | Allow / deny / tiered approval | ⏳ |
| s07 | Error + retry | Classify + exponential backoff | ⏳ |
| s08 | Skill system | On-demand tool bundles | ⏳ |
| s09 | Context compression | Sliding window + summary | ⏳ |
| s10 | Session event log | JSONL append-only events | ⏳ |
| s11 | Checkpoint resume | Atomic `.tmp` + rename | ⏳ |
| s12 | Sub-agent | Process IPC via files | ⏳ |
| s13 | Cron scheduler | 5-field cron, `NextRun`, `ShouldRun` | ⏳ |
| s14 | Classifier permissions | Two-stage model-based gate | ⏳ |
| s_full | Integration | 16-step end-to-end trace | ⏳ |
| A | Context anxiety | Long-running failure mode | ⏳ |
| B | Upstream map | Reading order through 25 guides | ⏳ |

### Quick start

```bash
git clone https://github.com/Ding-Ye/learn-harness-engineering-guide.git
cd learn-harness-engineering-guide

# Run s01
cd agents/s01-minimum-loop
go test -count=1 ./...
go run . "hello world"
# → I ran the echo tool on "hello world". Task complete.
```

No API key is required for s01: a `MockProvider` returns scripted responses so the loop is exercised offline. s02+ introduce a real `Provider` interface, but tests `t.Skip()` when `ANTHROPIC_API_KEY` is unset.

### How to read this repo

1. Start with [`docs/en/s01-minimum-loop.md`](docs/en/s01-minimum-loop.md) — it explains the central abstraction (the agentic loop) in 6 sections, ending with annotated upstream source.
2. Open `agents/s01-minimum-loop/` and read `loop.go` → `mock_provider.go` → `echo_tool.go` → `loop_test.go` in that order.
3. When ready for the next layer, jump to `docs/en/s02-llm-provider.md`.

Each chapter's doc is self-contained: you can land on any one of them with no prior context and walk away knowing one upstream concept and one Go implementation pattern.

### Project layout

```
learn-harness-engineering-guide/
├── README.md                 (this file)
├── README.zh-CN.md           (Chinese mirror)
├── go.work                   (workspace of all chapter modules)
├── agents/
│   └── s01-minimum-loop/     (Go module: code + tests + chapter README)
├── docs/
│   ├── en/s01-minimum-loop.md
│   └── zh/s01-minimum-loop.md
├── upstream-readings/        (annotated upstream excerpts)
├── web/index.html            (curriculum landing page, no build step)
├── LICENSE                   (MIT, with attribution to upstream)
└── .github/workflows/go.yml  (per-chapter matrix CI)
```

### Acknowledgements

- The teaching content is sourced from [nexu-io/harness-engineering-guide](https://github.com/nexu-io/harness-engineering-guide) (MIT, © 2026 Nexu). Upstream guide files are excerpted with inline attribution and pinned to commit `86fec9b`.
- The pedagogy mirrors [shareAI-lab/learn-claude-code](https://github.com/shareAI-lab/learn-claude-code): six-section spine, isolated per-chapter modules, no cross-session imports.
- Generated with [Claude Code](https://github.com/anthropics/claude-code) via the `learn-repo-generator` skill.

### License

MIT — see [LICENSE](LICENSE).
