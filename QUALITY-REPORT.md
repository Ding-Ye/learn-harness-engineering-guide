# Quality report: learn-harness-engineering-guide

Generated: 2026-05-17T10:14:09Z
Repo: https://github.com/Ding-Ye/learn-harness-engineering-guide
Total commits: 16
CI status: All 6 most-recent GitHub Actions `go` runs completed with conclusion=success (latest sha ca5bc8c).

## Summary

- P0 issues: 0
- P1 issues: 0
- P2 issues: 1

## P0 issues (must fix)

**No P0 issues found.**

All eight P0 checks pass:

- **P0.1 Bilingual heading parity** — every `docs/zh/<basename>.md` has a matching `docs/en/<basename>.md` and the `##` heading counts are identical for all 18 doc pairs (s01..s14 + s_full + appendix-a + appendix-b + multi-model).
- **P0.2 Six-section spine** — s01..s14 in both `docs/zh/` and `docs/en/` contain all five required headings (`Problem`/`问题`, `Solution`/`解决方案`, `How It Works`/`工作原理`, `Try It`/`动手试`, `Upstream Source`/`上游源码`). s02..s14 additionally contain `What Changed`. s01 correctly omits `What Changed`.
- **P0.3 No cross-session imports** — every `agents/sNN-*/` module's Go imports stay within its own session path; no chapter pulls from another chapter's module.
- **P0.4 Upstream citation reality** — all 25 distinct `guide/<file>.md` references across `docs/`, `upstream-readings/`, and `agents/*/README.md` resolve against `.learn/upstream/guide/` (HEAD = 86fec9bea430cecb29ff10afaae36b96496a8f8e).
- **P0.5 Tests pass for every session** — `go vet ./... && go build ./... && go test -count=1 ./...` returns `ok` for all 14 chapter modules. (s08 and s12 have additional sub-packages with `[no test files]`, which is expected for skill bundles and the parent/child cmd wiring.)
- **P0.6 CI status on GitHub** — 6/6 of the most recent `go` workflow runs completed with `conclusion=success`. No queued, in-progress, or failed runs observed.
- **P0.7 No leaked .learn files** — `git ls-files | grep '^\.learn/'` returns empty; `.learn/` is gitignored and the on-disk `.learn/` dir contains no tracked files.
- **P0.8 No stray built binaries tracked** — `*.test`, `*.out`, and per-chapter binaries (`agents/*/sNN-*`) are gitignored. Local binaries exist on disk (built during the test runs above) but are not committed.

## P1 issues (should fix)

**No P1 issues found.**

- **P1.1 README links resolve** — every `docs/(zh|en)/...md` link in `README.md` and `README.zh-CN.md` points to a file that exists on disk (table rows s01..s14 verified plus s_full, appendix A/B, multi-model).
- **P1.2 web/index.html validity** — well-formed HTML5, single self-contained file (125 lines), table of 18 rows in each language section. Every row shows `<span class="badge ok">ready</span>` (EN) or `<span class="badge ok">已上线</span>` (ZH). No `pending` badges in actual content; the string `pending` only appears in CSS variable/class definitions on lines 8 and 29.
- **P1.3 go.work lists every chapter module** — all 14 expected `./agents/sNN-*` entries are present in `go.work`.
- **P1.4 CI matrix lists every chapter** — all 14 expected sessions are present in `.github/workflows/go.yml` matrix.

## P2 issues (nice to have)

### P2.1 — Only s01 and s02 have a `testdata/` directory
- File(s): `agents/s03-tool-registry/` through `agents/s14-classifier-permissions/`
- Detail: A directory listing shows `testdata` exists only under `s01-minimum-loop` and `s02-llm-provider`. The other 12 chapters rely on inline table-driven fixtures inside `*_test.go`. Tests still pass for all sessions, so this is a stylistic/conventional gap rather than a correctness issue.
- Suggested fix: Optional — add a one-line README note or move a few representative fixtures into `testdata/` for sessions that grow non-trivial fixture data later (e.g. s08 skill bundles, s10 event log golden files).

Other P2 checks pass cleanly:
- **P2.2 Each session README ≥ 40 lines** — line counts: s01=51, s02=63, s03=66, s04=75, s05=49, s06=56, s07=56, s08=68, s09=52, s10=55, s11=61, s12=67, s13=62, s14=63. All ≥ 40.
- **P2.3 Phase G multi-model parity** — both `docs/zh/multi-model.md` and `docs/en/multi-model.md` exist and have 6 `##` headings each. Sections align one-to-one (Why two wire formats / 为什么有两套 wire format, etc.).

## Strengths

- **End-to-end bilingual parity is perfect.** All 18 zh/en doc pairs agree on `##` heading counts, and the six-section spine is intact across all 14 chapters with the expected s01 exemption for `What Changed`.
- **All 14 chapter modules compile, vet clean, and pass tests in < 1s each.** Independence between chapters is real — no cross-session imports were detected.
- **CI is green for the most recent 6 pushes**, including the final Phase G multi-model commit (sha ca5bc8c).
- **Upstream citations are honest.** All 25 distinct `guide/*.md` references resolve against the pinned upstream HEAD, so readers can click through every citation.
- **Static web viewer ships in a single self-contained file** with no broken or pending rows. Both EN and ZH curricula list 18 entries, all badged `ready` / `已上线`.
- **Repo hygiene is good.** `.learn/` workdir is gitignored and contains no leaked files; built binaries are gitignored; .gitignore explicitly enumerates every chapter binary name.

## Recommendations

Ready to ship.

- **Headline metrics for the announcement:** 14 chapters + integration + 2 appendices + multi-model layer, ~5,800 LOC across 14 independent Go modules, 100% bilingual parity, all CI green.
- **Optional polish (post-ship):** address P2.1 by introducing `testdata/` directories for sessions that already use moderate-sized inline fixtures (s08, s10, s14) to make adding new fixtures easier later. Not a blocker.
- **Local artifact cleanup:** before tagging a release, optionally `find agents -maxdepth 2 -type f -perm +111 -not -name '*.go' -not -name '*.json' -not -name '*.md' -delete` to remove local-only chapter binaries — they are already gitignored, so this is purely tidiness for users browsing the cloned tree.
