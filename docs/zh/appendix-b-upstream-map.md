# 附录 B — 上游对照表

> 本课程每一章在 [`nexu-io/harness-engineering-guide`](https://github.com/nexu-io/harness-engineering-guide) 里对应哪段源文献，固定到 sha `86fec9bea430cecb29ff10afaae36b96496a8f8e`。用法：(a) 配合 Go 代码读上游；或者 (b) 把它当 syllabus，独立通读一遍原 guide。

## 阅读顺序

时间够的话，按**依赖顺序**读上游，而不是按字母序或 `ls` 顺序。`.learn/upstream/guide/` 里 25 个文件，推荐路径：

1. 先读 `guide/what-is-harness.md` —— **4 大支柱**总论（agentic loop / tool system / memory & context / guardrails）。这是背景，不映射到任何章节。
2. 读 `guide/glossary.md` —— 24 条术语定义。开个侧边 tab 留着，后面每一章都默认你已知。
3. 读 `guide/your-first-harness.md` —— 直接对应 s01 / s02 / s03。50 行的 Python agent 是 Go 移植的脊椎。
4. 读 `guide/agentic-loop.md` —— s01 中心 loop 的深入版。think / act / observe、turn budget、parallel tool calls、streaming。
5. 读 `guide/tool-system.md` —— s03 移植的 "schema vs implementation" 分离。
6. **一起读** `guide/memory-and-context.md` 和 `guide/context-engineering.md` —— 是 s04 / s05 / s09 的共同基础。先 memory，后 context engineering。
7. 先读 `guide/guardrails.md` 再读 `guide/classifier-permissions.md` —— 静态规则防御（s06）和基于模型的防御（s14）。它们是组合关系，先把静态那套放进脑子里。
8. 读 `guide/error-handling.md` —— 喂 s07（classify + retry）和 s11（checkpoint pattern）。
9. 读 `guide/skill-system.md` —— s08。"thin harness, thick skills" 反转就在这里。
10. 读 `guide/long-running-harness.md` —— 附录 A 的源头，也是 s09 / s11 的概念背景。
11. **一起读** `guide/managed-agents-architecture.md`、`guide/sub-agent.md`、`guide/multi-agent-orchestration.md` —— 喂 s10（event log）和 s12（sub-agent），也是 Phase G 扩展的依据。
12. 章节映射文件中，最后读 `guide/scheduling-and-automation.md` —— s13。

剩下的 guides（`harness-vs-framework.md`、`comparison.md`、`sandbox.md`、`eval-*.md`、`agent-teams.md`、`ghost-account-hunting.md`、`nexu-windows-packaging.md`、`initializer-coding-pattern.md`）是**广度阅读** —— 重要的背景，但不直接映射到章节。课程做完之后再读，把图景补全。

## 文件-章节映射

`.learn/upstream/guide/` 下全部 25 个 guide，每个标出关键行号 + 章节映射。行号锚定在上面那个 SHA，新版本可能漂移。

| Upstream guide | 关键行 | 教什么 | 我们的 session |
|---|---|---|---|
| `guide/what-is-harness.md` | 全文 | harness 的 4 大支柱；loop / tools / memory / guardrails 框架 | （背景，贯穿全部章节） |
| `guide/your-first-harness.md` | L24-L236 | 50 行 Python harness；OpenAI vs Anthropic 并排 | s01、s02、s03 |
| `guide/agentic-loop.md` | L9-L137 | think / act / observe；并行工具调用；streaming；turn budget | s01 |
| `guide/tool-system.md` | L9-L100 | registry；schema（模型看到的）vs 实现（运行时跑的） | s03 |
| `guide/context-engineering.md` | L15-L238 | 优先级装配 + 滑动窗口 + 三道防线 | s04、s09 |
| `guide/memory-and-context.md` | L21-L144 | session vs memory；三层装配；MEMORY.md 模式 | s04、s05、s10 |
| `guide/guardrails.md` | L22-L116 | 权限模型；allow / deny / 分级审批 | s06 |
| `guide/error-handling.md` | L9-L322 | 分类；带 jitter 的指数退避；`.tmp + rename` 原子写 | s07、s11 |
| `guide/skill-system.md` | L9-L220 | skill bundle（SKILL.md）；按需加载；省 context | s08 |
| `guide/long-running-harness.md` | L19-L138 | 上下文焦虑；reset vs compaction；generator-evaluator | s09、s11、**附录 A** |
| `guide/managed-agents-architecture.md` | L74-L112 | brain / hands / session 解耦；event-log 架构 | s10 |
| `guide/sub-agent.md` | L66-L145 | leader-worker；文件 IPC（TASK.md / RESULT.json）；隔离 | s12 |
| `guide/multi-agent-orchestration.md` | L33-L126 | fan-out / pipeline / supervisor / peer-to-peer | s12（交叉引用） |
| `guide/scheduling-and-automation.md` | L78-L204 | cron 语法；心跳；长任务触发 | s13 |
| `guide/classifier-permissions.md` | L29-L169 | 两阶段权限模型；reasoning-blind 分类器 | s14 |
| `guide/sandbox.md` | 全文 | Docker / Firecracker；capability drop；只读 fs | 范围外，从 s06 链接 |
| `guide/eval-awareness.md` | 全文 | agent 何时意识到自己在被测 | 范围外（广度） |
| `guide/eval-infrastructure.md` | 全文 | 资源配置对 benchmark 噪声的影响 | 范围外（广度） |
| `guide/initializer-coding-pattern.md` | 全文 | 长 agent 的两阶段 harness（init + coding） | 备选机制，未章节化 |
| `guide/agent-teams.md` | 全文 | 16 并行 agent；Ralph-loop；git 协调 | s12 交叉引用 |
| `guide/harness-vs-framework.md` | 全文 | 相对 LangChain / CrewAI / AutoGen 的定位 | 背景 |
| `guide/comparison.md` | 全文 | Harness vs Framework 功能矩阵 | 背景 |
| `guide/glossary.md` | 全文 | 24 条术语定义 | 每一章 footer 都链接 |
| `guide/ghost-account-hunting.md` | 全文 | 探测/防止 agent 滥用凭证 —— 案例 | 联系 s14 的动机 |
| `guide/nexu-windows-packaging.md` | 全文 | Electron 打包案例 | 范围外 |

## 符号对照表

Go 开发者侧的映射：我们的标准类型/函数，和上游对应的 Python（或散文）。

| 我们的类型 / 函数 | 上游对应 | 出处 |
|---|---|---|
| `Provider` interface（s02） | `client.chat.completions.create` + `client.messages.create` | `guide/your-first-harness.md` L98-L102、L218-L228 |
| `ChatRequest` / `ChatResponse`（s02） | OpenAI/Anthropic 请求/响应形状并排 | `guide/your-first-harness.md` L209-L236 |
| `Tool` interface（s03） | `TOOLS` 列表 + `execute_tool()` 函数 | `guide/your-first-harness.md` L42-L88 |
| `Registry.Dispatch`（s03） | `execute_tool(name, args)` switch | `guide/tool-system.md` L36-L88 |
| `ContextAssembler.Build()`（s04） | `ContextAssembler.build()` 散文 + 代码 | `guide/context-engineering.md` L36-L87 |
| `EstimateTokens` 启发式（s04） | 基于 `tiktoken` 的 `estimate_tokens` | `guide/context-engineering.md` L36-L38 |
| `Memory{baseDir, clock}`（s05） | MEMORY.md + `memory/YYYY-MM-DD.md` 文件系统布局 | `guide/memory-and-context.md` L80-L130 |
| `Checker` interface（s06） | （仅散文 —— 概念在 `guardrails.md` 里） | `guide/guardrails.md` L22-L116 |
| `Classify(err) ErrorClass`（s07） | 错误分类表（Transient / Permanent / Model / Resource） | `guide/error-handling.md` L20-L60 |
| `RetryWithBackoff`（s07） | 带指数退避的 `@retry` 装饰器 | `guide/error-handling.md` L62-L122 |
| `SkillRegistry`（s08） | skill menu + on-demand load 模式 | `guide/skill-system.md` L78-L101 |
| `list_skills` / `load_skill` 元工具（s08） | 上游同名的两个 meta-tool | `guide/skill-system.md` L100-L150 |
| `SlidingWindowContext`（s09） | 滑动窗口压缩防线 | `guide/context-engineering.md` L194-L238 |
| `Summarizer` interface（s09） | `SUMMARIZE_PROMPT` 常量 + 摘要调用 | `guide/context-engineering.md` L146-L148 |
| `SessionStore` interface（s10） | "events log" 概念 | `guide/managed-agents-architecture.md` L74-L112 |
| `Event{Timestamp, Type, Data}`（s10） | JSONL 事件记录 | `guide/memory-and-context.md` L62-L78 |
| `Checkpoint.Save`（s11） | `.tmp` + `os.rename` 原子写配方 | `guide/error-handling.md` L255-L259 |
| `SubAgentSpawner`（s12） | leader-worker spawn 模式 | `guide/sub-agent.md` L66-L145 |
| `TASK.md` / `RESULT.json` IPC（s12） | 上游用的就是这两个文件名 | `guide/sub-agent.md` L104-L130 |
| `CronSchedule.NextRun`（s13） | cron 表达式语法（5 字段） | `guide/scheduling-and-automation.md` L84-L94 |
| `Classifier`（s14） | 三层权限流程 | `guide/classifier-permissions.md` L113-L141 |
| `StripReasoning`（s14） | reasoning-blind 分类器的论证 | `guide/classifier-permissions.md` L151-L169 |

## 建议练手

5 个把章节组合起来、或者超出课程边界的练习。不打分，挑那些拉伸你理解的来做。

1. **组合 s10 + s11 —— 事件日志驱动的 checkpoint。** 把 s11 单独的快照格式扔掉。改用 replay s10 的事件日志来在 resume 时重建 loop 状态。你需要一个确定性的投影函数 `events → state`。想清楚 tool 结果写到一半时怎么办（提示：`.tmp` 在这里也是你朋友）。

2. **组合 s12 + s13 —— 定时 sub-agent。** 用 s13 的 cron 触发 `daily_at_8am`。每次触发派生一个 s12 sub-agent，在自己的上下文里跑"昨日 digest"任务。用 s10 把"派生-结果"对记成事件。并发上限设成 1（s12 的 `maxWorkers`）。

3. **扩展 s09 —— 真实 tokenizer。** 把 `tokens.go` 里的"词数 × 1.3"启发式换成 [`github.com/pkoukk/tiktoken-go`](https://github.com/pkoukk/tiktoken-go)。在一个真实 LLM 消息语料上跑 benchmark，对比启发式 vs 真实估算的误差。注意：阈值逻辑要在两种实现下都仍然能工作。

4. **扩展 s14 —— 被拦截动作报告。** 把 s14 分类器的判决喂回 s10 事件日志（`event.Type == "permission_verdict"`）。然后写一个日报脚本，读 `sessions/*.jsonl`，筛过去 24 小时的 `permission_verdict` 事件，输出被拦工具的直方图。纯离线 replay。

5. **Phase G —— OpenAI provider。** 把 `ChatRequest` / `ChatResponse` 翻译到 `chat.completions.create` 而不是 `messages.create`。要规划的差异：tools 嵌在 `function` 下面、结果走 `tool_calls` 而不是 `content[].tool_use`、system message 是一条 `role="system"` 的普通消息而不是顶层字段。接入 s02 的 `provider_test.go`。这条通了之后，同一套模式可以扩展到 DeepSeek、Qwen、或本地 vLLM 这种 OpenAI 兼容的服务。

## 注意事项

- **SHA 是钉死的。** 上游钉在 sha `86fec9bea430cecb29ff10afaae36b96496a8f8e`；新版本的行号会漂。在 issue 或 PR 里引用时，用 permalink 格式 `https://github.com/nexu-io/harness-engineering-guide/blob/86fec9b/guide/<file>#Lxx-Lyy`，链接才不会坏。
- **上游只有 markdown。** 没有可运行的 Python 源码。你在 `your-first-harness.md`、`error-handling.md` 等文件里看到的 Python 段落是**示例性**的 —— 真要跑得自己补 imports、fixture、`client` 初始化。当作"语法合法的伪代码"看。
- **有些 guide 是故意范围外的。** `sandbox.md`、`eval-awareness.md`、`eval-infrastructure.md`、`nexu-windows-packaging.md`、`agent-teams.md` 描述了我们没有章节化的基础设施或场景。读它们是为了广度 —— 值得花时间 —— 但 `agents/` 下面找不到对应的 Go 代码。
- **glossary 是权威。** `guide/glossary.md` 的术语是真理之源。如果附录 A、任何章节文档、甚至本文件**看起来**用法不同，**以 glossary 为准**。要小心的几个词：**session**（永远指一次 agent 运行，不是 TCP session）、**skill**（永远指一个 SKILL.md bundle，不是泛指能力）、**harness**（包装代码本身，不是 runtime）。
