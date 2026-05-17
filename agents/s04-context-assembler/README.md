# s04-context-assembler

> Priority-based context packing in ~250 lines of Go.
> 基于优先级的上下文打包（Go 实现，~250 行）。

## Scope / 范围

Implement the assembler described in `guide/context-engineering.md` L19-L87: seven priority tiers, a token budget, truncate-critical / drop-droppable when over budget. Word-count × 1.3 token heuristic, no external dependencies.
实现 `guide/context-engineering.md` L19-L87 描述的装配器：7 个优先级、token 预算、超额时关键段截断、可丢弃段丢弃。Token 估算用 词数×1.3，无外部依赖。

## Files / 文件

```
assembler.go        ContextSection / ContextAssembler / Build — packing logic
tokens.go           EstimateTokens — words × 1.3 heuristic
main.go             CLI demo with 6 sections at varying priorities
assembler_test.go   6 unit tests covering ordering, drop, truncate, reserve
```

## Run / 运行

```bash
cd agents/s04-context-assembler
go run .
# Budget: 180 (max=200, reserve=20)
# Used:   180 tokens across 3 sections
# (table of packed + dropped sections)
```

## Test / 测试

```bash
go test -count=1 ./...
# PASS - 6 tests
```

## Key teaching points / 教学要点

1. **Priority ≤ 2 is critical (truncate); priority ≥ 3 is droppable.** Matches the upstream table at `guide/context-engineering.md` L19-L28. System prompt, tool schemas, task instructions never disappear; conversation history can.
   优先级 ≤ 2 是关键段（截断）；≥ 3 是可丢弃段（丢弃）。对应上游表格 `guide/context-engineering.md` L19-L28。系统提示词、工具 schema、任务指令绝不会被丢弃；对话历史会。
2. **`reserveTokens` leaves headroom for the response.** Packing to 100% leaves the model no room to reply. See `guide/context-engineering.md` L43-L45, L89.
   `reserveTokens` 为模型响应预留余量。打包到 100% 模型就没空间回答了。
3. **Stable sort, deterministic add-order.** Two sections at the same priority come out in the order they were added. Tests run 50 trials to defeat any non-stable sort.
   稳定排序，add 顺序确定。同优先级两段按 Add 调用顺序输出，测试跑 50 轮防止排序算法不稳定。
4. **Token estimator is intentionally crude.** Words × 1.3 is enough for budget arithmetic. s09 swaps in a real tokenizer when summarization needs accuracy.
   Token 估算故意做得粗糙。词数 × 1.3 对预算够用了；s09 引入摘要时再换真正的 tokenizer。

## Priority tiers (from `guide/context-engineering.md` L19-L28)

| Priority | Section | On overflow |
|---:|---|---|
| 0 | System prompt | truncate |
| 1 | Tool schemas | truncate |
| 2 | Task instruction | truncate |
| 3 | Memory summary | drop |
| 4 | Injected files | drop |
| 5 | Recent conversation | drop |
| 6 | Older conversation | drop |

Priority ≤ 2 is critical (truncated); priority ≥ 3 is droppable (silently excluded).
优先级 ≤ 2 是关键段（截断）；≥ 3 是可丢弃段（静默排除）。

## What the next chapter changes / 下一节的变化

s05 introduces a `Memory` layer (MEMORY.md + daily logs) whose output becomes a `priority=3` section feeding into the s04 assembler. The assembler itself is reused as-is — memory is just another source of content.
s05 引入 `Memory` 层（MEMORY.md + 每日 log），其产出作为 `priority=3` 段灌入 s04 的装配器。装配器本身原样复用；memory 只是又一个内容源。

## Design constraints / 设计约束

- **No external dependencies.** The token estimator is `words * 13 / 10` integer arithmetic. No tiktoken, no provider-specific tokenizer. s09 swaps in a real tokenizer when summarization needs accuracy.
  **无外部依赖**。Token 估算就是 `words * 13 / 10` 整数运算。无 tiktoken，无 provider 特定 tokenizer。s09 引入摘要时再换真 tokenizer。
- **Stable sort.** Go's `sort.Slice` is *not* stable; we use `sort.SliceStable` and additionally track `addOrder` so determinism is testable.
  **稳定排序**。Go 的 `sort.Slice` **不**稳定；我们用 `sort.SliceStable` 并显式记录 `addOrder`，让确定性可测。
- **Truncation suffix is part of `Content`.** The marker `" (truncated)"` lives in the string so the *model* can see it — not a downstream type-assertion concern.
  **截断后缀写进 `Content`**。`" (truncated)"` 标记必须留在字符串里，**模型本身**要看到 —— 不是给下游做类型判断用的。
