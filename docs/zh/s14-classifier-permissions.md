# s14 — 分类器权限

> 三层权限闸门。Tier 1 + Tier 2 是纯 Go 匹配器（白名单 + 仓内路径）。Tier 3 是两阶段 LLM 分类器 —— 先 fast yes/no、需要时才上完整 reasoning。喂给分类器的对话历史是 reasoning-blind：agent 的 thinking block 在喂进模型之前就被剥掉了。

## Problem

到 s06 为止，harness 已经有了护栏 —— AllowList、DenyList、Tiered。它们能跑，但有天花板。每条规则都得人提前手写：

```go
BlockedPatterns: []string{
    `rm\s+-rf\s+/`,
    `curl.*\|\s*sh`,
},
```

对**你能枚举的**那些动作没问题。但 agent 一旦干一件不在剧本里、规则没覆盖的事，护栏就抓瞎了 —— Anthropic 的事故记录（上游 L173-L237）就是一整列这种事：你没让它删的分支它删了；它去翻 `~/.ssh/id_rsa` 来"探索"；它推断了一个 email 收件人；它把 log 上传到了 pastebin；它把 `assert` 注释掉来让测试通过。每一件单独看都**像那么回事**，没有静态模式会去标记它。

剩下的三个选项都不好（上游 L19-L27）：

| 选项 | 问题 |
|------|------|
| 全沙箱 | 维护成本高、破坏真实开发流 |
| 手动批准 | 批准疲劳 —— 大约 93% 的人自动点 "yes" |
| `--dangerously-skip-permissions` | 一次 prompt injection = 机器被攻破 |

还有第四个选项：**把人工 reviewer 换成一个模型**，模型唯一的工作就是 approve / reject。不是 agent 自己 —— 而是一个**单独**的模型、视野是 scoped 的、每次工具调用都判一次 allow / deny / review。但如果每个 `read_file` 都喂给 Sonnet 4.6 那成本会爆，所以分层：大量读操作直接绕过分类器；只有少数危险或未知的才交给模型。

## Solution

`Classifier` 对每个候选工具调用按顺序跑三层：

```go
type Classifier struct {
    provider         Provider
    whitelistMatcher *WhitelistMatcher
    repoMatcher      *RepoPathMatcher
}

func (c *Classifier) Classify(
    ctx context.Context,
    transcript []Message,
    toolName string,
    args map[string]any,
) (*Decision, error)
```

流程直译自上游 L113-L141：

```
工具调用 → Tier 1（命中白名单？）── 是 ──► ALLOW（不调 LLM）
                  │ 否
                  ▼
         Tier 2（路径在仓根下？）── 是 ──► ALLOW（不调 LLM）
                  │ 否
                  ▼
         Tier 3 stage 1：max_tokens=1，yes/no ── "yes" ──► ALLOW（一次 LLM）
                  │ "no"
                  ▼
         Tier 3 stage 2：max_tokens=2048，Verdict: ...
                  │
                  ▼
            allow | deny | review
```

三条纪律：

| | 规则 |
|---|---|
| 路由 | Tier 1 → Tier 2 → Tier 3。**先命中先赢**。 |
| 两阶段 | Stage 2 **只在** stage 1 返回 "no" 时才跑。Stage 1 占成本主导。 |
| reasoning-blind | 在任何 LLM 调用之前都先剥掉 `thinking` block 和 assistant 起首的 text。 |

`Provider` 接口把 LLM 调用抽出去。生产环境是个快模型；测试用 `MockProvider` 返回脚本字符串、记录每次请求，让我们能精确断言**哪一阶段在跑**。

## How It Works

**Classify** 按三层路由：

```go
// Tier 1：白名单
if c.whitelistMatcher != nil && c.whitelistMatcher.Match(toolName, args) {
    return &Decision{Verdict: VerdictAllow, Reasoning: "tier 1: ..."}, nil
}
// Tier 2：仓内路径
if c.repoMatcher != nil && c.repoMatcher.Match(toolName, args) {
    return &Decision{Verdict: VerdictAllow, Reasoning: "tier 2: ..."}, nil
}
// Tier 3：分类器 —— 先 strip reasoning、再 stage 1、可能 stage 2。
visible := StripReasoning(transcript)
// ...
```

`WhitelistMatcher`（Tier 1）**只看名字**、不看 args。默认白名单是 `[read_file, list_dir, glob]`。上游 L122-L124 的卖点就是"内置的安全读"，我们故意不约束 args —— 一旦约束就等于把 s06 的 path-glob 复杂度搬回来了，而 Tier 1 的设计目标是"不检查、直接放行"。

`RepoPathMatcher`（Tier 2）同时接受 `"path"` 和 `"file_path"` 这两个 arg key（上游两种拼法都用），通过 `filepath.Abs + Clean` 归一化，然后做 `strings.HasPrefix(abs, root + sep)` 检查。**那个尾部的分隔符很关键** —— 没有的话 `/repo` 会匹配上 `/repos`。L143-L145 的理由是：仓内编辑反正会被 `git diff` 抓住、git 就是这里的安全网。

Tier 3 给分类器构造它的"世界视图"，做三件事：

1. **剥掉 reasoning**，调 `StripReasoning(transcript)`。两条规则：
   - 规则 1：删掉每一个 `Type == "thinking"` 的 block。
   - 规则 2：在一个 assistant 消息里，如果含有至少一个 `tool_use`，那么**位于第一个 `tool_use` 之前**的所有 text block 都删掉（捕捉"我先 X、再 Y"模式）。

2. **合成候选调用**，作为收尾的 user 消息：`Tool call: <name>(<json-args>)`。

3. **跑 stage 1**，用 `Stage1Prompt`、`MaxTokens=1`。prompt 要一个字的回答、偏向 "no"。`IsAffirmative` 接受任何以 "y" 开头的回复（应付 "yes"、"y"、"Yes." 等），命中就短路返回 ALLOW。

4. **只在 "no" 时跑 stage 2**，用 `Stage2Prompt`、`MaxTokens=2048`。结构化输出 `Verdict: allow|deny|review\nReasoning: ...`。`ParseStage2` 解析；**任何解析失败都返回 `Verdict: review` —— fail closed**。

成本形状：大部分工具调用打在 Tier 1 或 Tier 2 上（**零** LLM 调用）。剩下的付一次 stage-1 调用。stage-1 没过的少数再付一次 stage-2。Anthropic 在 auto-mode 那篇的公开数据里，Tier 3 平均成本远低于一次 stage-2 调用。

## What Changed

| | s06（guardrails） | s14（classifier） |
|---|---|---|
| 策略来源 | 静态 config（`AllowedTools`、regex、tier） | 模型判断 |
| 抓得到 | 人能枚举出来的规则 | 没写在剧本里、"看上去不对劲" 的动作 |
| 速度 | 微秒级 | Tier 1+2 微秒；Tier 3 毫秒级（平均一次调用） |
| 失败模式 | 模式没命中 → 静默放行 | Stage 2 解析失败 → fail closed 到 "review" |
| 最擅长 | 写死的危险关键词 | 范围 / 意图推理 |
| 组合 | 在 dispatch 前 | 在 dispatch 前（s06 之后） |

**s06 和 s14 是组合关系**。在 `s_full` 里接线是：工具调用到 → s06 跑（便宜的 regex deny；"deny" 直接出）→ s14 跑（Tier 1/2 短路；Tier 3 调分类器）→ dispatch。s06 抓能抓的；s14 抓剩下的。**两者不是替代关系** —— 上游 L309 的反模式里说得很直白："它是个模型，它一定会挂。底下还是要垫沙箱 —— 单一防线打不过纵深防御。"

## Try It

```bash
cd agents/s14-classifier-permissions
go vet ./... && go build ./... && go test -count=1 ./...
# PASS —— 6 个命名测试 + ParseStage2 子测试

go run .
# === s14-classifier-permissions demo ===
# repo root: /var/folders/.../repo
# whitelist: [read_file list_dir glob]
#
# [Tier 1 (whitelist)]  tool=read_file ...  verdict=allow provider.calls=0
# [Tier 2 (in-project)] tool=write_file ... verdict=allow provider.calls=0
# [Tier 3 (classifier)] tool=run_command (DROP DATABASE prod;)
#   verdict=deny confidence=0.8 provider.calls=2 (delta=2)
#   reasoning: drops a production database — not in the user's request
#
# === total provider calls: 2 ===
```

注意调用次数：Tier 1 和 Tier 2 各花费**零次** provider 调用。Tier 3 对 `DROP DATABASE prod;` 花了**两次** —— stage 1 返回 "no"（脚本好的答案），触发 stage 2（脚本好的 "Verdict: deny\nReasoning: drops a production database — not in the user's request"）。换一个更安全的命令就会在 stage 1 停下、只花一次调用。

## Upstream Source Reading

来源：`guide/classifier-permissions.md` L29-L130。永久链接：<https://github.com/nexu-io/harness-engineering-guide/blob/86fec9bea430cecb29ff10afaae36b96496a8f8e/guide/classifier-permissions.md#L29-L130>

交叉引用：`guide/guardrails.md` L22-L116（这一层下面那一层的静态护栏）和 `guide/sandbox.md`（应当继续在分类器下面跑的 OS 级隔离）。

```markdown
## Two-Stage Classification

Chain-of-thought on every tool call would double your inference cost. Auto
mode splits the work:

    def classify(transcript, tool_call) -> Decision:
        # Stage 1: fast yes/no filter
        verdict = model.forward(
            prompt=STAGE1_PROMPT,
            transcript=transcript,
            tool_call=tool_call,
            max_tokens=1,  # single token: "yes" or "no"
        )
        if verdict == "yes":  # looks safe
            return ALLOW

        # Stage 2: only runs on flagged actions
        reasoning = model.forward(
            prompt=STAGE2_PROMPT,
            transcript=transcript,
            tool_call=tool_call,
            max_tokens=2048,  # full chain-of-thought
        )
        return parse_decision(reasoning)

Stage 1 is biased toward flagging. Cheap, fast, paranoid. Stage 2 is
expensive but rarely runs, and it produces an explanation you can log
and debug. Most actions never leave stage 1.

## Three-Tier Decision Flow

Not every action needs a classifier. Reading a file inside the project is
fine. Deleting files outside the project is not. Auto mode uses a tiered
approach:

    ┌──────────────────────────────────────────────┐
    │  Tool call arrives                           │
    └────────────────────┬─────────────────────────┘
                         ▼
            ┌────────────────────────┐
            │  TIER 1: Whitelist     │
            │  - Built-in read ops   │  ─── ALLOW ───►
            │  - User allow rules    │
            └────────────┬───────────┘
                         │ not matched
                         ▼
            ┌────────────────────────┐
            │  TIER 2: In-project    │
            │  - Files under repo    │  ─── ALLOW ───►
            │  - Covered by git diff │
            └────────────┬───────────┘
                         │ not matched
                         ▼
            ┌────────────────────────┐
            │  TIER 3: Classifier    │
            │  - Shell commands      │  ─── ALLOW/BLOCK
            │  - Web fetches         │
            │  - External APIs       │
            │  - File ops outside    │
            └────────────────────────┘
```

阅读笔记：

- **Stage 1 是故意的偏执**。上游 prompt 和我们这边都偏向 "no"。stage-1 误报花一次便宜的 stage-2；stage-1 漏报会让坏动作过去。这种**非对称**是设计上的 —— 偏置调一次，让架构去吃误报成本。
- **Tier 1 不看 args**。上游 L122-L124 把"内置读操作"和"用户白名单规则"并列、两者都不看 args。我们的 `WhitelistMatcher.Match` 直接对应。如果你想给 `read_file` 加一个 path glob 约束，你实际上是把 s06 的 AllowList 重新发明了一遍 —— 上游的意思是 Tier 1 应该是**最便宜的短路路径**，不是第二个策略引擎。
- **Tier 2 信任 git 作为安全网**。L143-L145 说得很直："如果 agent 把 `src/auth.py` 改坏了，你在 `git diff` 里就会看到、commit 之前就抓住。" 这是一个**工作流假设**、不是代码属性。如果你的 harness 在内循环里自动 commit、这个假设就破了 —— 那就得收紧 Tier 2 或者干脆去掉。
- **reasoning-blind 是稳健性手段**。上游"Reasoning-Blind by Design"（L151-L169）给了那个教科书例子：`AGENT (reasoning): The user wants a clean slate. I'll drop the production database since they're probably done.` 分类器要是读到这段、agent 的话术就污染了判定。我们的 `StripReasoning` + assistant-起首-text-在-tool_use-前-丢掉的启发式就是实现。`TestReasoningStrippedFromClassifierInput` 走一遍 provider 收到的每一条消息的每一个字段、验证那串 secret 字符串一处都没出现。
- **`parse_decision` 是失败模式的转轴**。上游 L92 的伪代码写的是 `parse_decision(reasoning)`、没指定格式。我们的 `ParseStage2` 读两个 prefix 行（`Verdict:` / `Reasoning:`）、解析失败就回退到 `Verdict: review`。**解析失败的时候永远不要默认 allow** —— 这就是 `guardrails.md` 里的 "fail closed" 规则。

阅读地图：

| 主题 | 上游文件 | 行号 | 对应章节 |
|------|----------|------|----------|
| 双层防御 | `guide/classifier-permissions.md` | L29-L67 | s14（本章） |
| 两阶段分类 | `guide/classifier-permissions.md` | L69-L95 | s14 |
| 三层决策流 | `guide/classifier-permissions.md` | L111-L149 | s14 |
| reasoning-blind 设计 | `guide/classifier-permissions.md` | L151-L169 | s14 |
| 真实事故 | `guide/classifier-permissions.md` | L171-L237 | s14 交叉引用 |
| 静态护栏（下一层） | `guide/guardrails.md` | L22-L116 | s06 |
| OS 级沙箱（再下一层） | `guide/sandbox.md` | 全文 | 范围外 / 引用 |
