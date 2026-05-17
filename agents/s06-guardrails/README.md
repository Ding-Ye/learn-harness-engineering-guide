# s06-guardrails

> Code-level intercept between "the model wants to call tool X" and "the harness calls tool X". Three modes: allow-list, deny-list (regex), tiered approval.
> 模型请求工具调用和真正分发之间的代码级拦截。三种模式：白名单、黑名单（正则）、分级审批。

## Scope / 范围

Implement the three permission models from `guide/guardrails.md` L51-L116 in ~400 lines of Go. The wrapper sits between the loop and the tool registry (s03); the model never sees the guardrail directly. No LLM, no network — pure policy evaluation.

实现 `guide/guardrails.md` L51-L116 的三种权限模型，约 400 行 Go。Wrapper 夹在循环和工具注册（s03）之间；模型从不直接看到护栏。不调 LLM、不走网络 —— 纯策略评估。

## Files / 文件

```
guardrail.go         Checker interface + Decision + ErrNeedsApproval sentinel
policy.go            Policy struct + LoadPolicyFromJSON (strict, fail-closed)
allowlist.go         AllowListChecker + tiny **/* glob matcher
denylist.go          DenyListChecker — regex on canonicalised args
tiered.go            TieredChecker — low/medium/high/critical
dispatch_wrapper.go  Guarded() — wraps a DispatchFunc with a Checker
main.go              CLI demo running all three checkers
guardrail_test.go    7 tests covering the spec from .learn/plan.md
```

## Run / 运行

```bash
cd agents/s06-guardrails
go run .
# Prints three sections: AllowListChecker, DenyListChecker, TieredChecker.
# Each section shows which calls were dispatched and which were blocked.
```

## Test / 测试

```bash
go test -count=1 ./...
# PASS — 7 tests covering allow-list / deny-list / tiered / wrapper.
```

## Key teaching points / 教学要点

1. **Guardrails are code, not prompts.** Telling the model "don't delete files" via text is not a guardrail — a prompt injection rewrites that instruction. Real guardrails inspect the parsed tool call and refuse to dispatch. The wrapper here makes the "never dispatched" invariant testable: `TestGuarded_BlocksAndReturnsString` fails immediately if the inner function ever runs on a blocked call.
   **护栏是代码、不是 prompt**。在文字里跟模型说"不要删文件"不是护栏 —— 一个 prompt injection 就能覆盖它。真正的护栏检查"已解析的工具调用"并拒绝分发。本章的 wrapper 把"绝不分发"做成可测属性：`TestGuarded_BlocksAndReturnsString` 一旦内层函数被调到就立刻挂。
2. **Default-deny vs. default-allow.** Allow-list is "default-deny": only listed tools/paths pass. Deny-list is "default-allow": only matched patterns block. Pick allow-list for hosted agents; pick deny-list for developer tools where you can't enumerate every legit action. They compose — real harnesses chain both.
   **默认拒绝 vs 默认允许**。白名单是"默认拒绝"：只有列出来的工具/路径能通过。黑名单是"默认允许"：只有命中模式的才拒。托管 agent 选白名单；开发者工具选黑名单。两者可以叠加 —— 真实 harness 通常同时挂两层。
3. **`ErrNeedsApproval` is a sentinel, not a generic error.** A critical tool is not the same as a denied one. Denied is "no, pick something else"; needs-approval is "ask a human, then retry". The wrapper formats them differently and the test pins the exact string (`"needs human approval (tier=critical)"`) so UIs can match on it.
   **`ErrNeedsApproval` 是 sentinel，不是普通 error**。"高危工具"和"被拒绝"不一样：被拒绝是"换个办法"；需要审批是"问人，然后再来"。Wrapper 给两者不同的字符串，测试钉死格式 (`"needs human approval (tier=critical)"`) 让 UI 可以匹配。
4. **Fail-closed.** A regex that fails to compile, a `LoadPolicyFromJSON` that returns an error, or an unknown checker error all result in *block*, not "default to allow". The cost of false-positive blocks is a frustrated agent; the cost of false-negative blocks is a deleted production database. Pick the safe side.
   **失败时拒绝**。无法编译的正则、`LoadPolicyFromJSON` 报错、checker 抛出未知 error —— 一律拒绝，不"默认放行"。误拒的代价是 agent 烦躁；漏拒的代价是生产库被删。选安全的一侧。

## What the next chapter changes / 下一节的变化

s07 adds error classification + exponential backoff around `Provider.Chat` (the LLM call), orthogonal to guardrails. The two compose in s_full: a failed tool dispatch from the guardrail isn't retryable (it's a permanent policy decision); a 429 from the LLM provider is retryable (s07's territory).

s07 在 `Provider.Chat`（LLM 调用）外加一层错误分类 + 指数退避，和护栏正交。两者在 s_full 里组合：护栏拒绝的工具调用**不可重试**（永久策略决定）；provider 返回 429**可重试**（s07 的事）。
