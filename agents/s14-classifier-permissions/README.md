# s14-classifier-permissions

> Three-tier permission gate: Tier 1 whitelist + Tier 2 in-project paths short-circuit; Tier 3 invokes a two-stage LLM classifier (fast yes/no, then reasoning on flagged actions). The transcript fed to the classifier is **reasoning-blind** — agent thinking blocks are stripped before the LLM ever sees them.
> 三层权限闸门：Tier 1 白名单 + Tier 2 仓内路径直接放行；Tier 3 调用两阶段 LLM 分类器（先快速 yes/no，再对被标记的动作做带 reasoning 的判定）。喂给分类器的对话历史是**reasoning-blind** —— agent 的 thinking block 在喂进 LLM 之前就被剥掉。

## Scope / 范围

Implement the model-based permission system from `guide/classifier-permissions.md` L29-L169 in ~500 lines of Go. Three layers: a `WhitelistMatcher` (Tier 1), a `RepoPathMatcher` (Tier 2), and a two-stage `Classifier` (Tier 3) that does stage 1 (`MaxTokens=1`, fast yes/no) and only escalates to stage 2 (`MaxTokens=2048`, structured `Verdict: ... / Reasoning: ...`) on a "no". Reasoning-blind by design: `StripReasoning` walks the transcript and drops every `thinking` block plus any assistant text that sits before a `tool_use` in the same message. No real LLM — the test suite injects a `MockProvider` that records every request so we can assert (a) which stage ran, (b) how many provider calls happened, and (c) that the agent's thinking text never reached the classifier.

用 ~500 行 Go 实现 `guide/classifier-permissions.md` L29-L169 的"模型判权限"。三层：`WhitelistMatcher`（Tier 1）、`RepoPathMatcher`（Tier 2）、两阶段 `Classifier`（Tier 3）—— 先 stage 1（`MaxTokens=1` 快速 yes/no），只有 "no" 才升级到 stage 2（`MaxTokens=2048`、结构化输出 `Verdict: ... / Reasoning: ...`）。**reasoning-blind 是设计目标**：`StripReasoning` 走一遍 transcript，把所有 `thinking` block 和 assistant 消息里 `tool_use` 之前的 text block 都干掉。不调真实 LLM —— 测试用 `MockProvider` 记录每次请求，断言 (a) 哪一阶段在跑、(b) 一共调了几次、(c) agent 的 thinking 文本**没有**漏给分类器。

## Files / 文件

```
types.go                Local Provider/Message/ContentBlock + Decision + verdict constants
tiers.go                WhitelistMatcher + RepoPathMatcher (filepath.Abs + Clean + prefix check)
classifier.go           Classifier: Tier 1 → Tier 2 → Tier 3 (stage 1 then maybe stage 2)
prompts.go              Stage1Prompt + Stage2Prompt + ParseStage2 + IsAffirmative
reasoning_strip.go      StripReasoning: drop thinking blocks + leading assistant text
main.go                 CLI demo with offline demoProvider — runs all three tiers
classifier_test.go      6 tests + table-driven ParseStage2
```

## Run / 运行

```bash
cd agents/s14-classifier-permissions
go run .
# === s14-classifier-permissions demo ===
# [Tier 1 (whitelist)] tool=read_file ...        provider.calls=0
# [Tier 2 (in-project)] tool=write_file ...      provider.calls=0
# [Tier 3 (classifier)] tool=run_command ...     provider.calls=2 → verdict=deny
```

## Test / 测试

```bash
go vet ./... && go build ./... && go test -count=1 ./...
# PASS — 6 named tests + the table-driven ParseStage2 subtests
```

## Key teaching points / 教学要点

1. **Tiers exist for latency, not just policy.** Every Tier 1 / Tier 2 hit avoids an LLM call. Sending every `read_file` through Sonnet 4.6 would make the agent crawl. The split at L143-L145 is "the bulk of tool calls bypass the classifier; the dangerous few don't".
   **分层是为了延迟、不只是为了策略**。每一次 Tier 1 / Tier 2 命中都省下一次 LLM 调用。把所有 `read_file` 都喂给 Sonnet 4.6 agent 会卡死。L143-L145 的分割是"大部分工具调用绕过分类器、少数危险的不绕"。

2. **Stage 1 dominates Tier 3 cost.** With `MaxTokens=1`, a stage-1 call is essentially a single-token completion — cheap, fast, paranoid. Most actions never leave stage 1. Stage 2 only runs on a "no" and produces an explanation you can log. `TestStage1ShortCircuitsOnYes` asserts `mock.Calls == 1` when stage 1 said yes.
   **Stage 1 是 Tier 3 成本的主要部分**。`MaxTokens=1` 意味着 stage-1 本质上就是一个 token 的补全 —— 便宜、快、偏执。大多数动作根本走不到 stage 2。stage 2 只在 "no" 时跑、产出可日志化的解释。`TestStage1ShortCircuitsOnYes` 用 `mock.Calls == 1` 断言这件事。

3. **Reasoning-blind is a robustness property, not paranoia.** The agent can rationalize anything — if the classifier reads "I'll drop the prod db, that's clean", the agent's prose becomes part of the verdict. The strip forces the classifier to judge the action on its own merits. `TestReasoningStrippedFromClassifierInput` walks every captured field on every block of every message the provider saw and asserts the agent's secret thinking text is absent.
   **reasoning-blind 是稳健性、不是偏执**。agent 能合理化任何事 —— 如果分类器读到"我要 DROP 生产库，这就是 clean"，agent 的话术就进了判定。剥掉 reasoning 之后，分类器只能基于动作本身判。`TestReasoningStrippedFromClassifierInput` 走一遍 provider 看到的每条消息、每个 block 的每个字段，断言 agent 偷偷塞进的 thinking 文本一处都没出现。

4. **Fail closed.** `ParseStage2` returns `Verdict: review` on any unparseable reply — not `allow`. If we can't read the model's verdict, the only safe move is to escalate. The same rule applies if the provider is nil or returns an error: never default to "allow". This matches the anti-pattern callout at L309 — "Trusting the classifier completely. It's a model. It will fail."
   **失败即拒绝**。`ParseStage2` 在无法解析时返回 `Verdict: review`、**不是** `allow`。模型回复读不懂的唯一安全做法就是升级给人。provider 是 nil 或返回错误的时候也一样：永远不要默认 "allow"。这对应 L309 的反模式："完全信任分类器。它是个模型，它一定会挂"。

5. **The strip is heuristic, not airtight.** Rule 1 (`Type=="thinking"`) catches explicit thinking blocks like Anthropic's extended-thinking output. Rule 2 (drop leading assistant text before a `tool_use`) catches the common "I'll first do X, then Y" pattern. A determined attacker could shove reasoning into a `tool_use.Input` string — but that string is visible to the classifier as part of the action, which is the right place for it to be inspected.
   **strip 是启发式、不是密不透风**。规则 1（`Type=="thinking"`）抓显式 thinking block（比如 Anthropic 的 extended-thinking 输出）；规则 2（assistant 消息里 `tool_use` 之前的 text 全删）抓"我先 X、再 Y"那种常见模式。真要绕，攻击者可以把 reasoning 塞进 `tool_use.Input`，但那个字符串作为动作本身就要被分类器看到 —— 那就是该看的地方。

## What changed vs s06 / 与 s06 的对比

s06's guardrails are static: AllowList / DenyList / Tiered are all driven by config (`AllowedTools`, `BlockedPatterns`, `ToolTiers`). They block on pattern match — fast, deterministic, brittle. s14 keeps Tier 1 + Tier 2 as the same kind of static gate (whitelist + in-repo path check), but Tier 3 is **a model**. The model can reason about user intent that no regex would catch — "the user said 'clean test data', is `DROP DATABASE prod` an in-scope action?". The two compose: in `s_full`, s06 runs first (cheap regex denies), then s14 runs on whatever survives.

s06 的护栏是静态的：AllowList / DenyList / Tiered 都是 config 驱动（`AllowedTools`、`BlockedPatterns`、`ToolTiers`）。靠模式匹配阻断 —— 快、确定、脆。s14 把 Tier 1 + Tier 2 留作同类型静态闸（白名单 + 仓内路径）、但把 Tier 3 换成**模型**。模型能推理用户意图，正则永远抓不到："用户说 'clean test data'，那 `DROP DATABASE prod` 算不算在范围内？"。两者组合：`s_full` 里 s06 先跑（便宜的正则先 deny），活下来的交给 s14。
