# s04 — 上下文装配

> 用 ~250 行 Go 实现基于优先级的上下文打包。7 个优先级、token 预算、关键段截断、其余丢弃。无网络、无 API key。

## Problem

s01 的循环把每一条 assistant 消息和工具结果都追加进历史。这对 2 轮 demo 没问题，但对一个真实的 50 轮编码 session 不够用：一个文件能吞掉 10K tokens，20 个 tool schema 吃掉 3K，对话历史每轮线性增长。一个真任务跑十几轮后，你已经在被迫选择"保留什么、丢弃什么"。

上游 `guide/context-engineering.md`（L9-L13）这样说：

> 128K-token 的上下文窗口听起来很大，直到你开始往里塞东西。[…] Context engineering 就是做这些选择的艺术，三大支柱：assembly（塞什么进去）、compression（怎么压缩）、budgeting（怎么分配容量）。

这一章实现 **assembly** —— 每轮挑选什么进入窗口。Compression（s09）和 budgeting 后续章节再叠加。

## Solution

一个小型基于优先级的打包器。数据模型两个结构体：

```go
type ContextSection struct {
    Priority int    // 0 最高，6 最低（对应上游表 L19-L28）
    Name     string // 调试 / 日志用
    Content  string // 进入 prompt 的原文
}

type ContextAssembler struct {
    maxTokens     int  // 总预算，如 128_000
    reserveTokens int  // 留给模型响应的余量，如 4_096
    sections      []orderedSection
}
```

`Build()` 的打包算法：

1. 按 `Priority` 升序排序。同优先级内保持 add 顺序（稳定排序）。
2. 按顺序遍历。若该段能装进剩余预算（`budget = maxTokens - reserveTokens`），则纳入。
3. 否则：
   - **优先级 ≤ 2**（关键段：system / tools / task）：截断内容以适配剩余预算，并追加 `" (truncated)"` 标记。
   - **优先级 ≥ 3**（可丢弃段：memory / files / recent / older conversation）：静默丢弃。

Token 计数用无依赖的启发式 —— `words * 13 / 10`。1.3 倍系数处理子词切分。故意做得偏悲观，这正是预算守护需要的。

## How It Works

```
Add(0, "system",   "...")
Add(1, "tools",    "...")
Add(5, "recent",   "...")  ─┐
Add(2, "task",     "...")   │  Build():
Add(4, "files",    "...")   │   1. 按 Priority 升序排序
Add(3, "memory",   "...")  ─┘   2. 按顺序遍历:
                                     能装下？  → 纳入
                                     pri ≤ 2 → 截断
                                     pri ≥ 3 → 丢弃
                                3. 返回 (packed, used)
```

追踪 `main.go` 的测试 fixture，`maxTokens=200, reserve=20`（即 `budget=180`）：

| Pri | 名称 | Tokens | 结果 |
|---:|------|------:|------|
| 0 | system-prompt | 10 | 装得下 → 纳入（used=10） |
| 1 | tool-schemas | 7 | 装得下 → 纳入（used=17） |
| 2 | task | 260 | 超预算 → **截断**为 163 tokens + `(truncated)`（used=180） |
| 3 | memory | 10 | 超预算且 pri ≥ 3 → **丢弃** |
| 4 | file-snippet | 65 | 超预算且 pri ≥ 3 → **丢弃** |
| 5 | recent-chat | 104 | 超预算且 pri ≥ 3 → **丢弃** |

关键的 `task` 行留下来 —— 可能被砍残但模型仍能看到"我有一个任务指令"。可丢弃行静默消失。

两个值得提的设计选择：

1. **稳定排序 + 显式 add 顺序**。Go 的 `sort.SliceStable` 已经为同键保持顺序，但我们仍显式追踪 `addOrder`。测试跑 50 轮，防止未来有人换成非稳定算法。
2. **截断后缀写进 `Content`**。比起在 `ContextSection` 上加一个 `Truncated bool` 字段更 Go 一些，但后缀必须在 `Content` 里 —— **模型本身**要看到这个标记，这是与 prompt 的契约，不是给下游做类型断言用的。

## What Changed (vs s03)

s03 给出了 `Tool` 接口和 `Registry`。loop 终于有了真实工具，但 prompt 装配仍然是"把每条消息原样拼起来"。s04 在 `messages := []Message{...}` 和 `Provider.Chat(ctx, messages)` **之间**插入一个新模块：

```diff
  loop.go（概念上，s_full 集成时使用）:
    messages := []Message{}
+   ca := NewContextAssembler(maxTokens, reserveTokens)
+   ca.Add(0, "system", systemPrompt)
+   ca.Add(1, "tools", toolSchemasAsText)
+   ca.Add(5, "recent", recentConversation)
+   packed, _ := ca.Build()
+   messages = packedToMessages(packed)
    resp, _ := provider.Chat(ctx, messages)
```

工具注册（s03）原样不动。循环（s01）原样不动。Assembler 是纯加法 —— 它位于 LLM 调用之前，决定 send 哪个子集。

## Try It

```bash
cd agents/s04-context-assembler
go test -count=1 ./...
# PASS - 6 tests in ~0.4s

go run .
# Budget: 180 (max=200, reserve=20)
# Used:   180 tokens across 3 sections
# (打包 + 丢弃段表格)
```

刚才发生了什么（标注版）：
- 我们加了 6 段，优先级 0、1、2、3、4、5，**乱序**加入。
- Build 按优先级排序（0 在前），打包到 180 token 预算用完。
- 优先级 2 的 `task` 整段装不下 → 截断并标记。
- 优先级 3 / 4 / 5 的段超预算 → 静默丢弃。

输出是确定性的 —— 重跑得到完全相同的打包表和 `used`。这是测试依赖的性质。

## Upstream Source Reading

两份上游文件喂这一章：`guide/context-engineering.md`（优先级表和 Python `ContextAssembler`）以及 `guide/memory-and-context.md`（"context vs session vs memory" 概念铺垫，附一个更简单的 Python sketch）。

```python
# Source: guide/context-engineering.md L40-L79
class ContextAssembler:
    """Assemble context with priority-based token budgeting."""

    def __init__(self, max_tokens: int = 128_000, reserve: int = 4_096):
        self.max_tokens = max_tokens
        self.reserve = reserve
        self.budget = max_tokens - reserve
        self.sections: list[tuple[int, str, str]] = []

    def add(self, priority: int, name: str, content: str):
        self.sections.append((priority, name, content))

    def build(self) -> list[dict]:
        self.sections.sort(key=lambda s: s[0])
        messages = []
        used = 0
        for priority, name, content in self.sections:
            tokens = estimate_tokens(content)
            if used + tokens <= self.budget:
                messages.append({"role": "system", "content": f"[{name}]\n{content}"})
                used += tokens
            elif priority <= 2:
                remaining = self.budget - used
                truncated = self._truncate_to_tokens(content, remaining)
                if truncated:
                    messages.append({"role": "system",
                                     "content": f"[{name} (truncated)]\n{truncated}"})
                    used += estimate_tokens(truncated)
            # Priority > 2: silently dropped
        return messages
```

阅读笔记：

- **`priority <= 2` 是核心规则**。上游那三行 `elif priority <= 2:` 编码了整个"关键段 vs 可丢弃段"的切分。我们 Go 版给这个阈值命名 `const criticalCutoff = 2`，后来人可以 grep 到。
- **Python 的 `estimate_tokens` 用 `tiktoken`**。那是 OpenAI 库，绑定特定 model encoding。我们的 Go 版用 1.3 倍词数启发式，保持本章无依赖；s09 引入摘要需要精度时再换真 tokenizer。
- **`reserve = 4_096` 不直观**。L43-L45 明确说："你需要给模型响应留余量。如果上下文打到 100%，模型就没空间回答了。" 漏掉这个，生产环境会拿到莫名其妙被截断的答案。
- **上游返回 `list[dict]`**（Anthropic / OpenAI 消息形态）。我们 Go 版返回 `[]ContextSection`，因为这是自然的领域对象；s_full 在集成点写一个 `packedToMessages(packed []ContextSection) []Message` 适配器。
- **上游隐含我们显式化的部分**。Python 静默依赖 `sort()` 是稳定的（CPython 的 Timsort 是稳定的）。Go 的 `sort.Slice` **不**稳定，必须显式调用 `sort.SliceStable`。一个微妙的可移植性陷阱。

上游永久链接：[guide/context-engineering.md @ 86fec9b L15-L87](https://github.com/nexu-io/harness-engineering-guide/blob/86fec9bea430cecb29ff10afaae36b96496a8f8e/guide/context-engineering.md#L15-L87)

延伸阅读：[guide/memory-and-context.md L21-L60](https://github.com/nexu-io/harness-engineering-guide/blob/86fec9bea430cecb29ff10afaae36b96496a8f8e/guide/memory-and-context.md#L21-L60) 引入三个概念（context / session / memory），附一张优先级栈示意图 —— 就是我们的 assembler 实现的那张。
