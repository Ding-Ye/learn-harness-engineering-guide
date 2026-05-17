# s08 — Skill 系统

> Skill 是工具 + 文档 + 行为规则的**捆绑包**，从菜单按需加载。只有活跃 skill 的工具 schema 会进入 LLM —— 所以一个有 80 个工具、加载 2 个 skill 的 harness 只为 2 个付费。

## Problem

s03 引入了 `Registry`，我们开开心心把所有工具都塞了进去。教学层面没问题 —— 但 `guide/skill-system.md` L91-L101 把生产环境的代价说得很清楚：

| 策略 | Token（8 个 skill，~60 个工具） |
| --- | --- |
| 启动时全部加载 | ~12,000（每轮都要） |
| Skill 菜单 + 加载 2 个 | ~150 菜单 + ~2,400 加载 = ~2,550 |

每轮都要 —— 每一轮。30 轮 session 累计省 ~280K token。问题不在"工具多了好"；问题在**模型任意一刻只用到少数几个工具**，剩下的 schema 在每次 API 调用里都是死重量。

还有一个质量维度：80 个 schema 摆在它面前，模型选工具就没那么准了。聚焦的子集能提升选择准确率。所以 skill 加载既省钱、也提质。

## Solution

`Skill` 是一个**捆绑包**：

```go
type Skill struct {
    Name        string
    Description string  // 菜单里显示
    Doc         string  // 完整的 SKILL.md，加载时返回给模型
    Tools       []Tool  // Load 时激活的 schema 与 handler
}
```

`SkillRegistry` 持有两张 map：

```go
type SkillRegistry struct {
    catalog map[string]*Skill  // 已知的全部 skill
    active  map[string]*Skill  // LLM 当下能看到的子集
}
```

模型通过两个 meta-tool 和 registry 交互 —— `list_skills`（菜单）和 `load_skill(name)`（激活一个捆绑包）。两者都是普通的 `Tool` 实现。没有特殊的 "skill RPC"；LLM 现成的工具调用通道把活全干了。

```go
mt1 := ListSkillsTool{Registry: reg}      // 返回菜单
mt2 := LoadSkillTool{Registry: reg}       // 变更 active 集合
mt3 := UnloadSkillTool{Registry: reg}     // 闭环（L254 的坑）
```

`ActiveSchemas()` 返回所有活跃 skill 里所有工具的 `ToolSchema` 切片 —— harness 每次调用 LLM 时把这个切片粘到 `tools: [...]` 里。active 集合空 → 切片空 → 模型只看见 3 个 meta-tool，没有任何业务工具。

## How It Works

**SKILL.md 解析。** `LoadSkillFromDir(dir)` 读一个 SKILL.md，提取：

- 第一个 H1（`# git`）→ `Name`
- 第一个 blockquote（`> Inspect the working tree.`）→ `Description`
- 整个 body（含 H1 和 blockquote）→ `Doc`

为什么用这个简单形状而不是上游的 YAML frontmatter？三个理由：（1）两行头部一眼能看清，文件本身也是可读文档；（2）blockquote 就是菜单描述，不存在 `description:` 和正文摘要漂移的风险；（3）**整个** SKILL.md 都成为 `Doc`，正是 L175-L178 希望模型加载时看到的。

`LoadSkillFromDir` 不会魔法附加 Go 工具实现 —— 它只解析 markdown。`Skill.WithTools(...)` 的调用方负责把代码接到捆绑包上。`main.go` 里这个接线是一个按 `Skill.Name` 分支的小 `switch`；测试里直接 inline。无论哪种方式，解析器和类型系统都保持解耦。

**catalog vs active。** `ScanDir(rootDir)` 遍历 `rootDir` 的子目录，挑出带 `SKILL.md` 的，把解析后的 `Skill` 加进 catalog。还没激活任何东西。catalog 是"免费的" —— skill 在 Load 之前是惰性的：没 schema、没 handler、没 token 成本。

`LoadSkill(name)` 把 `Skill*` 从 catalog 搬到 active，返回模型读到的消息：一行简介（"Loaded skill 'git' with 2 tools: git_status, git_diff"）+ 完整的 `Doc`。为什么加载时要附完整 doc？因为 SKILL.md 是 skill 的**大脑** —— 约定、示例、何时使用 —— 模型需要这些才能正确使用。菜单那一行太短了。

`UnloadSkill(name)` 从 active map 删掉。catalog 条目保留，方便模型之后重新 load，不必重扫文件系统。

**`ActiveSchemas()`** 是承重方法。它遍历 `active`，收集每个 `Tool` 的 schema，按工具名排序（让 prompt cache 跨轮稳定），返回切片。这个切片就是 harness 粘到 LLM 请求 `tools` 字段的内容。`active` 空 → 切片空。

**`DispatchTool(ctx, name, args)`** 是另一半 —— 在 active 集合里找工具、跑、把结果格式化成模型能读的字符串。三种失败模式各自变成一个特定字符串：

- `Error: tool 'X' not found. Is the skill loaded?` —— 复用 L219 的措辞，模型会感觉熟悉。
- `Error: invalid args: <reason>` —— args 不是合法 JSON。
- `Error running X: <msg>` —— 工具本身返回错误。

模型**永远**不会看到 Go panic 或空字符串 —— 这是从 s03 工具注册那里继承下来的契约，本章原封不动保留。

**并发。** 所有公开方法都加锁。整套 API 是 goroutine-safe 的；将来 `s_full` 集成时，agentic loop 可以一边分发工具、一边重载 skill，不会 race。

## What Changed

| | s03（tool-registry） | s08（skill-system） |
| --- | --- | --- |
| 粒度 | 单个 Tool | Tools + doc 的**捆绑包** |
| 模型可见工具 | 注册过的全部 | 只有活跃 skill 内的 |
| 加载时机 | 只在启动时 | 通过 meta-tool 按需 |
| 元数据存储 | 仅代码 | SKILL.md 在磁盘 |
| 上下文成本 | 随工具数线性增长 | 受 active 集合限定 |

s03 的 `Registry` 对一组永远在线的小工具集仍然是合适的原语（比如 `list_skills`/`load_skill` 这对）。s08 在概念上坐在它上面：s08 的 **active** 集合就是 s03 风格代码在某一刻应该收到的。它们组合；本章不 import s03，遵守课程的"章节不互相 import"规则。

## Try It

```bash
cd agents/s08-skill-system
go test -count=1 -race ./...
# PASS —— 7 个测试，race detector 干净。

go run .
# === Skill menu (this is what the model sees) ===
# Available skills (use load_skill to activate):
#
# - file_ops: Read and write files within the workspace.
# - git: Inspect working tree status and diffs for the current repository.
# - web: Fetch HTTP resources and post JSON bodies to remote endpoints.
#
# === ActiveSchemas() before loading anything ===
# (none — no skill is loaded yet)
#
# === Loading 'git' via the load_skill meta-tool ===
# Loaded skill 'git' with 2 tools: git_status, git_diff
# ...
# === ActiveSchemas() after loading 'git' ===
# - git_diff: Show changes, staged or unstaged.
# - git_status: Show working tree status.
```

`skills/file_ops/`、`skills/git/`、`skills/web/` 下的 fixture skill 展示了推荐布局：一个 skill 一个目录，`SKILL.md` 加上同目录的 `tools.go`（独立子包）。每个子包的工具类型**结构性**满足 `package main` 的 `Tool` 接口 —— 即便两边在同一个 module，也没有 import 环。

## Upstream Source Reading

来源：`guide/skill-system.md` L9-L100。永久链接：<https://github.com/nexu-io/harness-engineering-guide/blob/86fec9bea430cecb29ff10afaae36b96496a8f8e/guide/skill-system.md#L9-L100>

```markdown
## What is a Skill?

A tool is a single function the model can call. A **skill** is a packaged
capability that bundles:

- **Tools** — one or more related function schemas and handlers
- **Documentation** — a SKILL.md file explaining when and how to use the skill
- **Behavior rules** — constraints, patterns, and conventions for the model
  to follow

skill/
├── SKILL.md          # Documentation: when to use, how to use, constraints
├── tools.py          # Tool implementations
└── schema.json       # Tool schemas (or generated from code)
```

阅读笔记：

- **Skill ≠ tool**。整章都围着这个区分转。tool 是函数；skill 是**捆绑包**（工具 + 文档 + 规则）。混用就会落到上游 L251 那个 "all-tools-upfront" 反模式 —— 换个名字而已。
- **菜单 ~150 token；完整 schema ~12,000**。L91-L101 那段数学是承重论证。改菜单那行的格式，省的量大体不变；改**策略**（比如反正把所有 schema 先加载），那省的钱就退回去了。
- **SKILL.md 格式是描述性的，不是规范性的**。上游用 YAML frontmatter 写 `name` + `description`。我们用 H1 + blockquote —— 少一个解析器、文件本身也是可读 markdown。模型在乎的只是加载时收到的 body **包含**这些约定；包装方式由 harness 作者定。
- **名字要区分**。L255 警告：如果一个 skill 名叫 `git`、其中一个工具也叫 `git`，模型会把 skill 名当工具调。我们的 fixture 用 `git` 命名 skill、用 `git_status`/`git_diff` 命名工具，匹配 L24。
- **Unload 不是可选的**。L254 点名：没有 `unload_skill`，active 集合就只增不减，省钱效果就退还回去了。Go API 第一天就有 `UnloadSkill`，并配了专门的测试（`TestRegistry_UnloadFreesContext`），重构时不会被悄悄删掉。

阅读地图：

| 主题 | 上游文件 | 行号 | 对应章节 |
| --- | --- | --- | --- |
| Skill 定义与结构 | `guide/skill-system.md` | L9-L40 | s08（本章） |
| 菜单模式 + token 数学 | `guide/skill-system.md` | L73-L102 | s08 |
| Skill 注册实现 | `guide/skill-system.md` | L104-L220 | s08 |
| 薄 harness + 厚 skill | `guide/skill-system.md` | L222-L247 | s08（架构注记） |
| 常见坑 | `guide/skill-system.md` | L249-L256 | s08（决定了 Unload 与命名） |
