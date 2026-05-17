# s_full — 完整集成：穿越十四章的端到端轨迹

这是课程的收束章。本章没有新代码：下面要出现的每一个组件，都已经在 `s01..s14` 中实现并通过测试。我们要做的，是带着一条非常普通的用户请求从输入字符一路走到最终答复，沿途指明它经过的每一章，以及承载该步逻辑的 `agents/sNN-…/` 中那个具体的 Go 文件。

如果你把前十四章当作十四个独立的机制读完了，本章是把它们连起来的接线图。如果你跳着读，本章告诉你应该先回头看哪几章。

## 架构总览

```
                            ┌──────────────────────────┐
                            │       用户输入           │
                            └────────────┬─────────────┘
                                         │
              ┌──────────────────────────▼──────────────────────────┐
              │              Agentic Loop (s01)                      │
              │      "think → act → observe"，带 maxTurns 上限       │
              └───┬─────────┬─────────────┬────────────┬─────────┬──┘
                  │         │             │            │         │
        组装上下文│  调用   │  派发        │  瞬时错误 │ 每 N 轮 │
                  │   LLM   │   工具       │   重试    │  做快照 │
   ┌──────────────▼──┐ ┌────▼──────┐ ┌────▼─────────┐ ┌▼───────┐ ┌▼─────────┐
   │ Context (s04)   │ │ Provider  │ │ Tool Registry│ │Retry   │ │Checkpoint│
   │ Memory   (s05)  │ │   (s02)   │ │     (s03)    │ │ (s07)  │ │  (s11)   │
   │ Compress (s09)  │ │ Anthropic │ │ + Guardrail  │ │ 指数+  │ │.tmp+ren  │
   │ Skill    (s08)  │ │ + Mock    │ │     (s06)    │ │ 抖动   │ │          │
   └─────────────────┘ └───────────┘ └──┬───────────┘ └────────┘ └──────────┘
                                        │
                              ┌─────────▼─────────┐    ┌─────────────────┐
                              │ Classifier (s14)  │    │ Event Log (s10) │
                              │ 三级 + LLM 判分   │◄──►│   只追加日志    │
                              └─────────┬─────────┘    └─────────────────┘
                                        │
                              ┌─────────▼─────────┐    ┌─────────────────┐
                              │ Sub-agent (s12)   │    │  Cron   (s13)   │
                              │  子进程 + 文件 IPC│    │ NextRun/Should  │
                              └───────────────────┘    └─────────────────┘
```

主脊柱是 `s01` 那个 agentic loop：把消息塞进去，问模型，派发它返回的 `tool_use` 块，追加 `tool_result`，直到模型不再要求调用工具或轮次预算耗尽。其它一切要么是"喂"loop 的（上下文、记忆、技能 schema），要么是"包"loop 的（重试、检查点、事件日志），要么是在 loop 内部"否决"某一步的（守卫、分类器）。Cron 调度器和 sub-agent 派生器是带外入口，最终调用的还是同一套 loop。

## 执行轨迹

场景：用户输入 **"读 `data.json`，把摘要写到 `summary.txt`"**。

下面的轨迹就是 `.learn/research-notes.md` 中那条 16 步端到端流程。每一步给出承载该行为的本仓库文件，以及驱动该设计的上游章节坐标。

1. **用户提交请求。**
   - 本仓库：`agents/s01-minimum-loop/main.go` 接收用户字符串；`agents/s10-session-event-log/main.go` 开启会话。
   - 上游：`.learn/upstream/guide/your-first-harness.md` L75-L120（驱动循环）与 `.learn/upstream/guide/managed-agents-architecture.md` L74-L112（会话开始）。
   - 写入 `sessions/<id>.jsonl` 的第一条事件就是 `{type:"user_message"}`。

2. **harness 为第 1 轮组装上下文。**
   - 本仓库：`agents/s04-context-assembler/assembler.go` 按优先级排序各段；`agents/s05-memory-layer/memory.go` 读取 `MEMORY.md` 与最近几天的日志；`agents/s08-skill-system/registry.go` 只输出*已激活*技能的 schema，而非整个目录。
   - 上游：`.learn/upstream/guide/context-engineering.md` L15-L87（优先级序列），`.learn/upstream/guide/memory-and-context.md` L80-L144（两级记忆）。
   - 结果：一段打包好的字符串 + 一份 `tools[]` 切片，准备喂模型。

3. **loop 调用 LLM provider。**
   - 本仓库：`agents/s01-minimum-loop/loop.go` 调 `Provider.Chat`；实现在 `agents/s02-llm-provider/anthropic_provider.go`，HTTPS POST 到 `api.anthropic.com/v1/messages`；`agents/s07-error-retry/retry.go` 用指数退避包住整个调用，仅对瞬时类错误重试。
   - 上游：`.learn/upstream/guide/agentic-loop.md` L41-L65（loop 主体），`.learn/upstream/guide/your-first-harness.md` L209-L236（请求形状），`.learn/upstream/guide/error-handling.md` L62-L122（重试）。

4. **模型在服务端推理。**
   - 本仓库：无 — 此步发生在云端。后面在 `agents/s14-classifier-permissions/reasoning_strip.go` 中，我们会确保模型的推理块*不会*泄漏给权限分类器。
   - 上游：`.learn/upstream/guide/agentic-loop.md` L10-L35（reason→act→observe），`.learn/upstream/guide/classifier-permissions.md` L151-L169（reasoning-blind）。

5. **模型发出第一个 `tool_use` 块：`read_file(path="data.json")`。**
   - 本仓库：JSON 在 `ChatResponse.Content` 中以 `ContentBlock{Type:"tool_use"}` 形式到达，由 `agents/s02-llm-provider/anthropic_provider.go` 解析。
   - 上游：`.learn/upstream/guide/your-first-harness.md` L109（模型返回工具调用），`.learn/upstream/guide/tool-system.md` L36-L61（schema 与实现分离）。

6. **harness 从 assistant 消息中抽出所有 `tool_use` 块。**
   - 本仓库：`agents/s01-minimum-loop/loop.go` 遍历 `response.Content`，把纯文本与工具调用分开。
   - 上游：`.learn/upstream/guide/agentic-loop.md` L52-L53（只要还有 `tool_use` 块就继续 loop）。

7. **分类器审查这次工具调用。**
   - 本仓库：`agents/s14-classifier-permissions/tiers.go` 跑 Tier 1（白名单：对 `WORKDIR/**` 下任何路径放行 `read_file`）→ ALLOW；`agents/s14-classifier-permissions/classifier.go` 里的 LLM 判分**没**被调起，因为 Tier 1 已经短路。
   - 上游：`.learn/upstream/guide/classifier-permissions.md` L113-L141（三级流程）。

8. **被守卫包裹的派发执行 `read_file`。**
   - 本仓库：`agents/s06-guardrails/dispatch_wrapper.go` 先跑白名单与拒绝模式正则，再调进 `agents/s03-tool-registry/registry.go`，由名字路由到 `agents/s03-tool-registry/tools_fileops.go`。
   - 上游：`.learn/upstream/guide/guardrails.md` L22-L116（代码级检查），`.learn/upstream/guide/tool-system.md` L62（始终返回字符串）。

9. **工具结果追加到消息历史；事件日志记录之。**
   - 本仓库：`agents/s01-minimum-loop/loop.go` 追加一个 `ContentBlock{Type:"tool_result", ID:<同一 id>, Content:<文件内容>}`；`agents/s10-session-event-log/file_store.go` 用 `O_APPEND` 往 `sessions/<id>.jsonl` 追加一行 `{type:"tool_result"}`。
   - 上游：`.learn/upstream/guide/your-first-harness.md` L113-L117（追加纪律），`.learn/upstream/guide/managed-agents-architecture.md` L74-L112（只追加日志）。

10. **loop 继续下一轮；必要时滑动窗口压缩历史。**
    - 本仓库：在下一次 `Provider.Chat` 之前，`agents/s09-context-compression/sliding_window.go` 用 `agents/s09-context-compression/tokens.go` 估算 token 用量；超过 `maxTokens` 的 70% 时，让 `agents/s09-context-compression/summarize.go` 把最后 N=15 轮以外的内容压缩成摘要。
    - 上游：`.learn/upstream/guide/context-engineering.md` L91-L238（三条防线），`.learn/upstream/guide/long-running-harness.md` L19-L92（context anxiety + 紧凑化）。

11. **模型基于已进入上下文的文件内容继续推理。**
    - 本仓库：与第 4 步同路径。
    - 上游：`.learn/upstream/guide/agentic-loop.md` L10-L35。

12. **模型发出第二个 `tool_use`：`write_file(path="summary.txt", content="...")`。**
    - 本仓库：仍由 `agents/s01-minimum-loop/loop.go` 抽出。
    - 上游：`.learn/upstream/guide/your-first-harness.md` L109。

13. **分类器 Tier 2 命中——路径在仓库内。**
    - 本仓库：`agents/s14-classifier-permissions/tiers.go` 拿 `summary.txt` 去匹配项目内路径匹配器，直接返回 ALLOW，不调 LLM 判分。
    - 上游：`.learn/upstream/guide/classifier-permissions.md` L113-L141。

14. **`write_file` 走同样的守卫 → 注册表流水线。**
    - 本仓库：`agents/s06-guardrails/dispatch_wrapper.go` → `agents/s03-tool-registry/registry.go` → `agents/s03-tool-registry/tools_fileops.go` 写文件并返回成功字符串。
    - 上游：`.learn/upstream/guide/your-first-harness.md` L80-L84。

15. **事件日志记录结果；若轮次是 N 的倍数则做检查点快照。**
    - 本仓库：`agents/s10-session-event-log/file_store.go` 又写一条 `tool_result` 事件；`agents/s11-checkpoint-resume/checkpoint.go` 在 `turn % 5 == 0` 时用 `.tmp` + `os.Rename` 原子地把 `{messages, turn}` 落盘。
    - 上游：`.learn/upstream/guide/error-handling.md` L231-L322（原子检查点），`.learn/upstream/guide/long-running-harness.md` L94-L138（恢复）。

16. **模型给出纯文本回复；loop 干净退出。**
    - 本仓库：`agents/s01-minimum-loop/loop.go` 看到 assistant 消息中没有任何 `tool_use` 块，返回；`agents/s10-session-event-log/main.go` 写入终止事件 `session_end`；任务成功，`agents/s11-checkpoint-resume/checkpoint.go` 清掉检查点文件。
    - 上游：`.learn/upstream/guide/agentic-loop.md` L52-L53（终止条件），`.learn/upstream/guide/error-handling.md` L296-L322（成功即清理）。

对于同一任务的长周期变体（例如四小时构建），第 1 步可能不是人类输入，而是来自 `agents/s13-cron-scheduler/scheduler.go`；第 5–14 步可能被 `agents/s12-sub-agent/spawner.go` 扇出成 N 个子进程，每个子进程在干净上下文里跑自己的那份 s01 loop。

## 跨章交互图

```
User                Loop              Provider          Tool                  Classifier        Event Log     Checkpoint
 │ "读 & 摘要"       │                  │                 │                      │                  │              │
 ├──────────────────►│                  │                 │                      │                  │              │
 │                   │ 组装 (s04+s05+s08)                                         │                  │              │
 │                   ├─────────────────►│ (s02, type contract / 类型契约)        │                  │              │
 │                   │                  │ Chat(messages,tools)                   │                  │              │
 │                   │                  │  retry (s07) 仅对瞬时错误重试           │                  │              │
 │                   │◄─────────────────┤ tool_use{read_file}                    │                  │              │
 │                   ├────────────────────────────────────►│ 审查 (Tier1 白名单)                    │              │
 │                   │                  │                 │◄─────────────────────┤ ALLOW           │              │
 │                   │                  │                 │  guardrail (s06)     │                  │              │
 │                   │                  │                 │  dispatch (s03, dynamic dispatch / 动态派发)│           │
 │                   │                  │                 │◄────── 结果 ─────────│                  │              │
 │                   │                  │                 │                      │                  │              │
 │                   ├──────────────────────────────────────────────────────────────────────────────►│ append tool_result
 │                   │ (loop 继续；超阈值则 s09 压缩)                              │                  │              │
 │                   ├─────────────────►│                                        │                  │              │
 │                   │◄─────────────────┤ tool_use{write_file}                   │                  │              │
 │                   ├────────────────────────────────────►│ 审查 (Tier2 项目内) │                  │              │
 │                   │                  │                 │◄─── ALLOW            │                  │              │
 │                   │                  │                 │ dispatch，写入       │                  │              │
 │                   │                  │                 │◄─────── ok ──────────│                  │              │
 │                   │                                                                                ├─►snapshot(s11)
 │                   ├─────────────────►│                                                                          │
 │                   │◄─────────────────┤ 纯文本 (end_turn)                                                         │
 │                   │                                                            │                  │ session_end  │
 │◄──────────────────┤                                                                                ├─► 清空检查点│
```

图中出现两种边：**类型契约**（`Provider` 接口、`Tool` 接口、`SessionStore` 接口 — 一次性在 `s02` / `s03` / `s10` 定下来的稳定边界）与**动态派发**（`s03` 按工具名在 registry 里查表，`s08` 激活某个技能，`s14` 让分类器在运行时决定 — 都是运行时决定）。整套架构之所以读得动，正是因为类型契约不多、动态派发位置都被局部化了。

## 故意省略

| 特性 | 上游实现位置 | 我们为什么不做 |
|---|---|---|
| 流式响应 | `.learn/upstream/guide/agentic-loop.md` L119-L137 | 会平白增加两到三章 channel 管线，但不揭示新的心智模型。loop 跑通之后，流式只是重构问题，不是概念问题。 |
| 操作系统级沙箱（Docker / Firecracker / chroot） | `.learn/upstream/guide/sandbox.md`（整篇，尤其 L40-L160） | 这是 OS 基础设施而非 Go 代码；每行的教学边际递减太快。我们用 `s06` 的策略级守卫顶上，并把链接留好。 |
| MCP（Model Context Protocol）协议 | `.learn/upstream/guide/tool-system.md`（提及）+ Anthropic 文档 | 独立规范，与教"工具派发如何工作"正交。`s03` 把边界画好之后，再加一个 MCP 传输只是给 `Tool` 多写一份实现。 |
| 真正的分类器模型 | `.learn/upstream/guide/classifier-permissions.md` L29-L169 | `s14` 用 `MockProvider` 保证测试确定性；线上要靠 Phase G 的多模型层 + 一个像 Haiku 这样的快模型跑 Stage 1。 |
| Eval harness（LLM 判分基准） | `.learn/upstream/guide/eval-awareness.md` 、 `eval-infrastructure.md` | 这是另一门学科；我们用 `go test` 校验机制，而非用判分提示校验能力。 |
| Initializer-coding 两阶段范式 | `.learn/upstream/guide/initializer-coding-pattern.md` | 它本质上是 `s01 + s11` 之上的一个*模式*；loop 和检查点齐了之后这个模式就读成"跑两次 loop"。挪到附录 B 作为扩展练习。 |
| Generator-evaluator 对偶 | `.learn/upstream/guide/long-running-harness.md` L94-L138 | 另一个*模式*，构成是 `s01` × 2 + `s10`。在附录 A 里讨论。 |
| 子代理的真正 Docker 进程隔离 | `.learn/upstream/guide/sub-agent.md` L150-L210 | `s12` 已经用 `os/exec` + 文件 IPC 教清楚了*形状*，再深就是运维而不是 Go。 |
| Prompt-injection 语料 / 启发式 | `.learn/upstream/guide/guardrails.md` L120-L156 | `s06` 担住了拒绝列表的形态；真正的注入语料需要数百条样本，而且过时很快。 |
| 带 leader 选举的分布式 cron | `.learn/upstream/guide/scheduling-and-automation.md` L205-L300 | `s13` 单进程、可重放；做成集群是运维问题。 |

## 多模型桥接

`agents/s02-llm-provider/provider.go` 里那个 `Provider` 接口是按 Anthropic Messages API 形状定的：一个 system 字符串、一串由角色 + 内容块（`text`、`tool_use`、`tool_result`）组成的 messages、以及 `end_turn | tool_use | max_tokens` 三选一的 `stop_reason`。这个形状之所以好用，是因为它与模型的思维结构几乎 1:1 对齐；代价是 `anthropic_provider.go` 几乎是个透明的序列化层，而 OpenAI provider 就要多做翻译——函数调用参数在 `tool_calls[].function.arguments` 里以 JSON 字符串形式回传，而不是结构化的块，并且角色分布更扁平。

如果你想让同一套 loop 跑在 OpenAI、DeepSeek、Qwen、Moonshot 或本地 vLLM 这类 OpenAI Chat Completions 兼容端点上，看 `docs/zh/multi-model.md`（Phase G 增订）。那篇文档会规定一个实现同一 `Provider` 接口的 `OpenAIProvider`，外加一层从规范 `[]ContentBlock` 到 OpenAI `messages[].content` + `tool_calls[]` 的翻译。`s01..s14` 其余部分不需要改。

## 延伸阅读

- 上面提到的每一章，对应的 `docs/{zh,en}/sNN-…md` 都有一节 **Upstream Source Reading**，以 Go 工程师的口吻逐行注释上游对应段落——想深挖任何一个机制，从这里开始。
- 对操作系统级沙箱（Docker、Firecracker、chroot、capability drop、只读文件系统）感兴趣，直接读上游 `.learn/upstream/guide/sandbox.md`；我们刻意没有把它移植成 Go 一章。
- 想了解"测试 agent"这门学科——能力评估、判分提示、eval-awareness 泄漏——读 `.learn/upstream/guide/eval-awareness.md` 与 `.learn/upstream/guide/eval-infrastructure.md`。
- 想理解长会话为什么会"悄悄退化"，读 `docs/zh/appendix-a-context-anxiety.md`；它解释了同时驱动 `s09`、`s11`、`s12` 设计动机的那个失败模式。
- 想要 25 篇上游 guide 与本仓库 14 章之间的完整阅读顺序，看 `docs/zh/appendix-b-upstream-map.md`。
- 所有上游引用都可在 `https://github.com/nexu-io/harness-engineering-guide/blob/86fec9bea430cecb29ff10afaae36b96496a8f8e/<path>` 重现。
