# s09 — 上下文压缩

> 对话历史上的滑动窗口。最近 15 条原样保留，更早的全部折叠成累积式摘要。**按 token 预算触发，不按消息条数**。

## Problem

到 s08 为止，harness 已经有了预算感知的上下文装配（s04）、记忆层（s05）、护栏（s06）、重试（s07）、按需 Skill（s08）。但还有一件事前面任何一章都没解决：**对话历史只增不减**。每一轮 user / assistant / tool 都在 append，没有东西离开。按 ~3 token/word、~500 word/轮（带工具结果），一个 50 轮的编码 session 就能跨过 75K token —— 一个 128K 模型每一轮都要为这段历史付全价。

还有另一个更微妙、guide 用一整章在讲的失败模式：随着 context window 被填满，模型开始抄近道。`guide/long-running-harness.md` L19-L46 把它叫做**上下文焦虑**（context anxiety）—— 一种涌现的赶时间行为，体现为工具调用变少、输出变短、过早宣布"完成"。**更大的窗口治标不治本，"管理"窗口才治本**。

所以 s09 干的是主动压缩的活。它**不**替代 s04（s04 决定"这一回合从已有 section 里挑哪些"）；它坐在 s04 *上游*，跨多回合改写对话历史本身。

## Solution

`SlidingWindowContext` 直译自 `guide/context-engineering.md` L194-L238：

```go
swc := NewSlidingWindowContext(
    /*windowSize=*/ 15,
    /*maxTokens=*/  128_000,
    /*threshold=*/  0.7,
    /*summarizer=*/ &MockSummarizer{}, // 或者真实的 LLM-backed summarizer
)
swc.Add(msg1)
swc.Add(msg2)
// ... 加了很多之后 ...
msgs := swc.GetMessages() // [system...] + [summary?] + [最近 15 条非 system]
```

三条纪律：

| | 规则 |
|---|---|
| 触发 | `EstimateTokens(messages) > threshold * maxTokens`（默认 70%）。**不是**消息条数。 |
| System | Role="system" 的消息**永不压缩**。每次压缩都原样穿过。 |
| 摘要 | **累积**。每次压缩接受 (prevSummary, oldMessages) → 新 summary。 |

`Summarizer` 接口把 LLM 调用抽出去。生产环境是快而便宜的小模型；测试里注入 `MockSummarizer` 返回确定性字符串（`[summarized N msgs; prev=L%d]`），让测试矩阵无需联网就能复现。

## How It Works

**Add** 做两件事：append、可能压缩。

```go
func (s *SlidingWindowContext) Add(msg Message) error {
    s.messages = append(s.messages, msg)
    budget := int(float64(s.maxTokens) * s.threshold)
    if EstimateTokens(s.messages) > budget {
        return s.compress(context.Background())
    }
    return nil
}
```

阈值检查是查**总 token 数**、不是 `len(s.messages)`。**这就是阈值检查存在的全部理由**：一条带超大工具结果的消息可能在消息数还很小的时候就撑爆预算。`TestSlidingWindow_ThresholdRespectsTokens` 就是金丝雀。

**compress** 分区、切分、摘要、重建：

```
全部消息
   ├── system     → 原样保留
   └── 非 system
        ├── pre-window  → 连同 prevSummary 喂给 summarizer → 新摘要
        └── recent (最后 windowSize 条) → 原样保留
```

新状态：`s.messages = system + recent`、`s.summary = newSummary`。摘要**不会**被塞回 `s.messages` —— 否则下一次压缩就得去判断"这条 system 是原始的还是我上次产出的？"。`GetMessages()` 在读取时即时合成摘要块：

```
GetMessages 输出：
    [原始 system 消息]
    [合成的 system 消息："[Conversation history summary]\n<summary>"]
    [最后 windowSize 条非 system 消息]
```

**累积**这件事是最关键、也最容易写错的属性。**第二次**压缩**不**是"摘要已经摘要过的东西"——而是请 summarizer 对 (上一次的摘要, 这次的旧消息) 产出一个全新的摘要。MockSummarizer 把 `len(prevSummary)` 编码进输出 (`prev=L%d`)，让 `TestSlidingWindow_SummaryAccumulates` 能精确断言"第 i 次调用的 prevSummary 等于第 i-1 次的输出"。这就是让这个策略能在超长 session 里跑得动的属性：summary 块的体积大致恒定，而不是线性增长。

有一个边界 case 值得专门说。如果**一条**消息就超过阈值、而 `len(nonSystem) <= windowSize`，那就**没有** pre-window 的消息可摘要。`compress()` 仍然跑（`CompressAttempts` 仍然 +1），但函数体短路、不调用 summarizer。调用方那条超大消息原样留在 buffer 里。**这是故意的**：偷偷把消息切两半比诚实地告诉调用方"预算救不了"更糟。`TestSlidingWindow_ThresholdRespectsTokens` 把这个行为钉死。

## What Changed

| | s04（assembler）| s09（compression） |
|---|---|---|
| 生命周期 | 一次 LLM 调用 | 跨多回合 |
| 修改对象 | 不改 —— 从固定 section 里挑子集 | 改写对话历史 |
| Token 预算 | 是 —— drop/truncate section | 是 —— 触发 `compress()` |
| 层次 | 读已经定好的历史 → 打包一次 | 坐在历史**上游**，每次 Add() 都跑 |
| 组合 | 从 s05 读 memory snippet | 和 s04 的组合在本章**外部**完成 |

s04 和 s09 是互补的。在 `s_full` 里接线是：第 N 回合开始 → s09（需要时压缩）→ s04（把剩余历史 + memory + 工具 schema 打进预算）→ 调 LLM。s04 拿到的是被 s09 缩短**之后**的对话。两章互不 import —— s04 的 `EstimateTokens` 启发式被**复制**到 `tokens.go`，因为课程规则是每章一个自包含模块。

## Try It

```bash
cd agents/s09-context-compression
go vet ./... && go build ./... && go test -count=1 ./...
# PASS —— 5 个测试

go run .
# === feeding 60 turns into SlidingWindowContext ===
# [turn 29] compression #1: len(messages)=16 summary="[summarized 15 msgs; prev=L0]"
# [turn 44] compression #2: len(messages)=16 summary="[summarized 15 msgs; prev=L29]"
# [turn 59] compression #3: len(messages)=16 summary="[summarized 15 msgs; prev=L30]"
#
# === what the LLM sees (GetMessages) ===
# [ 0] role=system    You are a careful coding assistant.
# [ 1] role=system    [Conversation history summary]
#                     [summarized 15 msgs; prev=L30...
# [ 2] role=user      turn 45: token token token token
# ...
# [16] role=user      turn 59: token token token token
```

注意第三次压缩的摘要带着 `prev=L30` —— 它接收到了第二次压缩输出的 30 字符作为 `prevSummary`。这就是累积属性的实证。

## Upstream Source Reading

来源：`guide/context-engineering.md` L91-L160。永久链接：<https://github.com/nexu-io/harness-engineering-guide/blob/86fec9bea430cecb29ff10afaae36b96496a8f8e/guide/context-engineering.md#L91-L160>

交叉引用：`guide/long-running-harness.md` L19-L92（"Context Anxiety" + "Reset vs Compaction"）解释这件事**为什么**重要、**怎么思考**这个权衡。

```python
# guide/context-engineering.md L199-L237（教科书版 SlidingWindowContext）
class SlidingWindowContext:
    def __init__(self, window_size: int = 15, max_tokens: int = 128_000):
        self.window_size = window_size
        self.max_tokens = max_tokens
        self.summary = ""
        self.messages: list[dict] = []

    def add(self, message: dict):
        self.messages.append(message)
        conversation = [m for m in self.messages if m["role"] != "system"]
        if len(conversation) > self.window_size * 3:
            self._compress()

    def _compress(self):
        conversation = [m for m in self.messages if m["role"] != "system"]
        system = [m for m in self.messages if m["role"] == "system"]
        old = conversation[:-(self.window_size * 3)]
        recent = conversation[-(self.window_size * 3):]
        new_summary = summarize_with_llm(
            [{"role": "system", "content": self.summary}] + old
        )
        self.summary = new_summary
        self.messages = system + recent

    def get_messages(self) -> list[dict]:
        result = [m for m in self.messages if m["role"] == "system"]
        if self.summary:
            result.append({
                "role": "system",
                "content": f"[Conversation history summary]\n{self.summary}",
            })
        result.extend(m for m in self.messages if m["role"] != "system")
        return result
```

阅读笔记：

- **上游 Python 的 `* 3` 是"轮 vs 消息"的换算，我们直接砍掉**。Python 的 "window_size" 计数的是"轮"，而一轮大致是 user + assistant + tool ≈ 3 条消息。我们让 `windowSize` 直接计数消息 —— 反正阈值检查才是真触发，windowSize 现在的含义只是"压缩时保留多少条原样"，少一层算术、效果一致。
- **上游的触发条件是消息数，不是 token 数**。看 `if len(conversation) > self.window_size * 3` —— Python 数条数。我们升级成 token 预算检查，因为 `context-engineering.md` L110-L138（"threshold compression"）明确说预算才是真问题；消息数只是粗糙代理、在工具结果上会失灵。
- **`summarize_with_llm` 把上一次的 summary 作为 system prompt 传入**。这就是上游版本里的累积属性。我们的 `Summarizer` 接口把 `prevSummary` 做成**显式参数**，而不是合成消息 —— 语义一样、类型更清晰。
- **`[Conversation history summary]` 这个 marker 是原文照抄**。别改。下游工具（eval harness、可观测性）会 grep 这个字符串。
- **long-running-harness.md 是 "why"、context-engineering.md 是 "how"**。先读前者后读后者。"上下文焦虑"那张心智模型（`long-running-harness.md` L19-L46）才是你之所以做这个的理由；滑动窗口类只是**处理它**的一种机制。`s_full` 会把两者编织起来。

阅读地图：

| 主题 | 上游文件 | 行号 | 对应章节 |
|------|----------|------|----------|
| 三道防线 | `guide/context-engineering.md` | L91-L158 | s09（本章） |
| 滑动窗口类 | `guide/context-engineering.md` | L194-L238 | s09 |
| 上下文焦虑心智模型 | `guide/long-running-harness.md` | L19-L46 | 附录 A + s09 交叉引用 |
| Reset vs compaction 权衡 | `guide/long-running-harness.md` | L49-L92 | s09 交叉引用 |
| Generator-evaluator（替代方案） | `guide/long-running-harness.md` | L94-L138 | s11 |
| 预算感知的 assembler（互补） | `guide/context-engineering.md` | L15-L87 | s04 |
