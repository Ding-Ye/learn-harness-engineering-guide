# 附录 A — 上下文焦虑与"进展错觉"

> 一种从训练数据分布中涌现出来的失败模式，不是代码里的 bug。哪怕你在 s01 把 Loop 写得再严丝合缝，跑一个 4 小时的长任务，到第 4 个小时它照样会"掉下悬崖"。

## 这份附录讲什么

本课程的 14 个 session 每个都钉死一个**机制** —— 一个 struct、一个 interface、一个 loop body。它们都是那种你能写 `TestX_*` 然后看着它变绿的东西。上下文焦虑不是这一类。你 unit-test 不掉它，它不在任何单一文件里，它是一种**性质** —— 一个写得完全正确的 harness、面对一个真实 LLM、跑得足够久，让对话历史**长得像**一个快结束的对话时，自然涌现出来的性质。所以我们把它放在附录里，而不是正文章节里。

为什么这件事重要：本课程每一章在技术上都是独立完备的。s01 的 loop 会终止。s04 的 assembler 尊重预算。s11 的 checkpoint reload 是原子的。这些都没法救你 —— 当你把成品发出去，指向一个 4 小时的重构任务，agent 在第 90 分钟淡定地宣布完工，留下一半文件没动。防御是**架构级**的 —— 阶段边界、独立的 evaluator、滑动窗口、新鲜的 sub-agent 上下文 —— 课程本身已经把零件都给你了。这份附录是把它们串起来用的 *why-and-when* 指南。

**至少在做完 s12 之后**再读这份。在那之前，本附录里好几个交叉引用在你脑子里还没有落脚点。

## 现象

有经验的运维者学会识别的信号：agent 每回合工具调用次数开始往下走。输出变短。推理散文（"我应该先检查..."）变薄。然后，大约在 context 填到 60-80% 的位置，agent 来一句 "I've covered the main points" 或者 "the implementation looks complete" —— 然后就不再调工具了。

没有 crash，模型也没拒绝，没有 error 让你 catch。harness 完全按你写的方式在跑。agent 只是…… 提前收尾了。

`guide/long-running-harness.md` L19-L46 把这个叫做**上下文焦虑**（context anxiety），并且在用词上很小心。模型并不是"变紧张了" —— 那是拟人化（anthropomorphism）。模型对时间压力没有任何意识，临床意义上根本不存在"焦虑"。真正在发生的事（L30-L32）：训练数据里的对话**是会结束的**。它们以收尾总结、"I hope that helps"、任务完成信号结尾。一个快满的 context window，从分布上看**像是**对话的*末尾*。模型于是倾向于输出训练数据里"长上下文之后通常跟着"的那种 token 序列。那种 token 序列就是收尾。

三种可观察的症状，每一种配一个具体例子：

**1. 工具调用塌缩。** 一个 coding agent 前 30 回合每回合调 4-6 次 `read_file` / `grep` / `edit`，到第 45 回合稳定在每回合 0-1 次。它还在输出文字。文字越来越像"这是我的计划"而不是"我在执行计划"。

**2. 过早收尾。** 让它重构 12 个文件，做完 5 个就开始写 "I've updated the key files. The refactor is complete." —— 完全没意识到自己漏了 7 个。你追问一句"那 `auth.go` 呢？"，它立刻继续干。它没撒谎，是**主动性**没了。

**3. 回避会增加上下文的动作。** 那些会返回大块数据的工具（读文件、列目录）被跳过，转而靠先前上下文猜。这条很微妙，经常只能在 trace replay 里看到。

把"长任务里每回合工具调用数 vs 回合编号"画一张 mock 时序图：

```
每回合工具调用数
       |
   8 - |  *
   7 - | * *
   6 - |*   *  *
   5 - |     *   *
   4 - |          * *
   3 - |             *   *
   2 - |                * * *
   1 - |                     * * *
   0 - |                          * * * * <-- "完成"
       +-----------------------------------
         5   10  15  20  25  30  35  40  回合
         |                          |
         5K tok                   85K tok
```

曲线是示意 —— 实际 run 噪声更大。但形状是真实的：context 填满的过程中单调下降，然后一个硬零点 —— agent 宣布完工。

更大的 context window 只是**推迟**这件事 —— `long-running-harness.md` L32 写得明明白白：*"A bigger window delays the problem; it doesn't solve it."* 解药不是 200K → 1M token，解药是**有意识地管理上下文生命周期**。

## 两种防御：重置与压缩

`long-running-harness.md` L49-L92 把这个设计选择讲得很清楚。两种防御都让上下文远离危险区，区别在于各自**牺牲什么**。

**上下文重置（Context reset）。** 当 context 超过某个阈值，把对话**整段抹掉**。手写一段 briefing —— 我们之前在做什么、做完了什么、接下来做什么 —— 用它作为新 session 的 user message 开始新一轮。**优点**：clean slate，token 预算可预测，新段彻底消除焦虑信号。**缺点**：有损。briefing 是个摘要，nuance 会丢；失败过的尝试不在 briefing 里，新 session 可能重新走进同一个死胡同。多次 reset 误差会复合 —— 你在摘要的摘要的摘要。

**上下文压缩（Context compaction）。** 对话保留，但**主动压**旧的回合。最近 N 回合原样保留，更旧的折叠成一个累积摘要。agent 仍然"看得见"自己试过方案 X 并且方案 X 失败了 —— 但那段历史的字节数缩了。**优点**：保留连续性和决策轨迹。**缺点**：压缩质量是个无界变量 —— 烂摘要比没摘要更容易把模型搞糊涂；工具结果多的历史压不出好东西。

怎么选？L86-L92 的决策矩阵：

| 场景 | 倾向 |
|---|---|
| 阶段清晰（research → write → review） | 阶段间用 **reset** |
| 在单个 artifact 上反复迭代 | **compaction** |
| agent 频繁回头看早期决策 | **compaction**（保留轨迹） |
| 历史里全是大块工具输出 | **reset**（工具输出压不动） |

很多生产 harness 是混合的：一个阶段内用 compaction，阶段边界用 reset。本课程 s09 实现了 compaction 那一半（`SlidingWindowContext`，见 [`docs/zh/s09-context-compression.md`](s09-context-compression.md)）。reset 那一半是 s11 的 checkpoint 给你的能力 —— 把阶段状态存盘、丢掉上下文、用从 checkpoint + s05 记忆层重新拼出来的 briefing 重新开始。

## 生成器-评估器架构

抵御**进展错觉**更深一层的防御 —— 跟上下文大小是两回事 —— 是从架构上**剥夺** agent 给自己打分的能力。`long-running-harness.md` L94-L138（以及 L96 显式提到的 GAN 类比）讲得很硬：**永远不要让生成器自己给自己改卷子**（never let the generator grade its own exam）。一个模型评估自己的输出，几乎一定给 8/10 —— 因为它的完整推理上下文让每一个选择都"显得合理"。

防御：**两个 agent，两个 context**。Generator 产出；Evaluator 判定，看不到 generator 的推理过程 —— 只看到输出、任务、和一份明确的 rubric。Evaluator 的实现方式是新发起一次独立的 LLM 调用（或 sub-agent），不同的 system prompt，干净的 message history。本课程 s12 是让这件事可落地的原语：一个新生 sub-agent **本身就是**一个独立 context。见 [`docs/zh/s12-sub-agent.md`](s12-sub-agent.md)。

L112-L117 给出的 4 条设计纪律：

1. **独立上下文（Separate contexts）。** Evaluator 绝**不**能看见 generator 的 chain-of-thought，只能看到产物。这一条杀同情偏（sympathy bias）。
2. **明确 rubric。** 按 checklist 打分（"Does the code handle empty input?"），不按 vibes 打分（"Is the code good?"）。
3. **可执行的反馈。** Evaluator 返回具体 issue（"function `parse_input` accepts empty strings without error handling"），**不**返回分数。"7/10" 是无用反馈。
4. **迭代预算。** Generator-evaluator loop 卡上限（上游用 3）。不卡的话，一个完美主义 evaluator 配一个孜孜不倦的 generator 能无限烧 token。

对更复杂的任务扩展到**三角色**（L140-L218）：**Planner** 把目标拆成带成功标准的子任务，**Generator** 在新鲜上下文里执行每个子任务，**Evaluator** 依 Planner 的标准判定每个产物。Planner 在 evaluator 标 fail 时**重新规划**（受 `max_replans` 限制）。关键性质是**每个角色都活在自己的上下文窗口里** —— generator 那边 200K-token 的代码挖坑历史不会污染 planner 的高层视图。

## 缓解手册

8 个具体 pattern，每一个都由你已经做过的章节实现（或可实现）。

- **基于阶段的 checkpoint。** 每个阶段结束时 `Checkpoint.Save()`，下一个阶段以新鲜上下文 + checkpoint 摘要重新启动。reset 而不丢状态。→ [`s11`](s11-checkpoint-resume.md)。
- **工具结果截断。** 限制单次工具结果进入对话的字节上限。一个 50KB 的文件读变成前 5KB + "... (truncated, N more bytes)"。assembler 边界（s04）实现，压缩层（s09）兜底。→ [`s04`](s04-context-assembler.md)、[`s09`](s09-context-compression.md)。
- **独立的 evaluator agent。** 派生一个 sub-agent，唯一职责是按 rubric 给 generator 的输出打分。不同模型实例，干净上下文，看不到推理块。→ [`s12`](s12-sub-agent.md)。
- **写到 loop 外面的摘要文档。** 持久化一份 `PROGRESS.md` 或 session 摘要文件，活在 context window 之外。loop 每个阶段开始时**重新读**它，而不是把那些信息一直拖在 message history 里。→ [`s05`](s05-memory-layer.md)。
- **Token 预算告警。** 在 context assembler 上暴露一个 `Budget()` 方法。占用率过 70% 时**主动**触发压缩 —— 别等撞墙。这条阈值检查是 s04 assembler 里最重要的那一行。→ [`s04`](s04-context-assembler.md)。
- **每个阶段边界做一次显式 "are we done?"。** 不要相信 agent 自己发出的"完成"信号。跑一次 evaluator 调用："给定原始任务 `T` 和当前产物 `A`，列出剩余工作。" 列表非空就继续这个阶段。便宜的保险。
- **三角色流水线（planner → generator → evaluator）。** 把三个角色 fan-out 到三个独立上下文。planner 守住长 horizon 目标；generator 50 回合一爆发；evaluator 每爆发检查一次。→ s12 fan-out + 交叉引用 [`multi-agent-orchestration`](https://github.com/nexu-io/harness-engineering-guide/blob/86fec9b/guide/multi-agent-orchestration.md)。
- **心跳工具调用。** 一个定时 cron tick 注入一条 "进度检查" 消息 —— 强迫 agent 把状态显式总结到外部，而不是闷头慢慢收尾。→ [`s13`](s13-cron-scheduler.md)。

8 条之间的共同 pattern：把 agent 本来需要**揣在脑子里**的状态，挪到**外部、显式、定期刷新**的地方。

## 延伸阅读

- `guide/long-running-harness.md` —— 本附录的源头。L19-L138 是高密度核心；L140-L218 讲三角色扩展；L222-L275 列了要回避的 anti-pattern。
- `guide/context-engineering.md` —— 具体压缩策略（优先级装配、滑动窗口、summarization prompt 设计）。在"上下文生命周期"这一面是本附录的姐妹篇。
- `guide/multi-agent-orchestration.md` L33-L126 —— fan-out / pipeline / supervisor pattern，是 generator-evaluator 和三角色架构的落地。
- `guide/error-handling.md` L231-L322 —— checkpoint-resume pattern，让 "reset" 真正可用。s11 直译这一段。
- Anthropic engineering: *Building effective agents* —— 上游 guide 在 `long-running-harness.md` L350 引用。本附录不重新 fetch，URL 请去上游对应行查。
- 关于"长度相关的输出质量下降"的已发表研究，上游 guide 的参考文献是最干净的入口。写作时还没有一篇公认的 canonical paper —— 这个现象在不同架构上都被经验性观察到。
