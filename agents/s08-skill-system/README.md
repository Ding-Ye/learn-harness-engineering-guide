# s08-skill-system

> Skills are bundles of tools + docs + behavior rules. Load on demand from a menu — only the active skill's tool schemas reach the LLM.
> Skill 是工具 + 文档 + 行为规则的捆绑包。按需从菜单加载 —— 只有活跃 skill 的工具 schema 会进入 LLM 上下文。

## Scope / 范围

Implement the skill-bundle pattern from `guide/skill-system.md` L9-L220 in ~400 lines of Go. The `SkillRegistry` holds a catalog (every known skill) and an active set (loaded). Two meta-tools — `list_skills` and `load_skill` — let the model browse and activate bundles via the standard tool-call channel. `ActiveSchemas()` returns only currently-loaded tools, which is the entire point: a harness with 80 tools but 2 loaded skills pays for 2.
用 ~400 行 Go 实现 `guide/skill-system.md` L9-L220 的 skill 捆绑模式。`SkillRegistry` 持有 catalog（所有已知 skill）和 active 集合（已加载）。两个 meta-tool —— `list_skills` 和 `load_skill` —— 让模型通过标准工具调用通道浏览和激活捆绑包。`ActiveSchemas()` 只返回当前已加载的工具，这就是关键：80 个工具但只加载 2 个 skill 的 harness，只为 2 个付费。

## Files / 文件

```
tool.go           Tool interface + ToolSchema (local copy; no s03 import)
skill.go          Skill struct + LoadSkillFromDir parser
registry.go       SkillRegistry: ScanDir, Catalog, Menu, Load/Unload, ActiveSchemas, DispatchTool
meta_tools.go     ListSkillsTool / LoadSkillTool / UnloadSkillTool
skills/file_ops/  SKILL.md + tools.go (stub read_file / write_file)
skills/git/       SKILL.md + tools.go (stub git_status / git_diff)
skills/web/       SKILL.md + tools.go (stub http_get / http_post)
main.go           CLI demo: scan ./skills, render menu, load 'git', print active schemas
registry_test.go  7 tests covering scan, menu format, load/unload, dispatch, meta-tools
```

## Run / 运行

```bash
cd agents/s08-skill-system
go run .
# === Skill menu ===
# Available skills (use load_skill to activate):
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
# - git_diff: ...
# - git_status: ...
```

## Test / 测试

```bash
go test -count=1 -race ./...
# PASS — 7 tests, with race detector on
```

## Key teaching points / 教学要点

1. **Two maps, not one.** `catalog` is everything we know; `active` is what the LLM can currently see. Only `active` feeds `ActiveSchemas()`. Keeping them separate is the whole token-saving trick at L91-L101.
   **两张 map，不是一张**。`catalog` 是已知的全部；`active` 是 LLM 当下能看到的。只有 `active` 喂给 `ActiveSchemas()`。把它们分开就是 L91-L101 那个省 token 的关键。
2. **The model loads skills via the standard tool channel.** `load_skill` is just another Tool. The meta-tool path keeps the harness API surface flat — no special "skill RPC" needed.
   **模型通过标准工具通道加载 skill**。`load_skill` 就是一个普通 Tool。meta-tool 让 harness API 保持扁平 —— 不需要专门的 "skill RPC"。
3. **Structural typing across packages.** `skills/git/tools.go` lives in `package git` but its types satisfy the `package main` Tool interface anyway, because Go does interface satisfaction structurally. That is what lets the fixture layout match the upstream `skill/` directory shape without import cycles.
   **跨包结构类型**。`skills/git/tools.go` 在 `package git`，但它的类型依然满足 `package main` 的 Tool 接口 —— Go 是结构化接口满足。这就是为什么 fixture 布局能和上游 `skill/` 目录形状一致，且没有 import 环。
4. **Unload is not optional.** Without an unload path, a long session monotonically grows the active set. The L254 pitfall is real; the test `TestRegistry_UnloadFreesContext` is the regression sentinel.
   **Unload 不是可选项**。没有 unload 路径，长 session 的活跃集只会单调增长。L254 这个坑是真的；`TestRegistry_UnloadFreesContext` 就是回归哨兵。

## What the next chapter changes / 下一节的变化

s09 introduces sliding-window compression of message history — orthogonal to skill loading. s08 controls *which tool schemas* appear; s09 controls *how much message history* survives each turn. They compose in `s_full`.
s09 引入消息历史的滑动窗口压缩 —— 和 skill 加载正交。s08 控制**哪些工具 schema** 出现；s09 控制**多少消息历史**在每个轮次留存。它们在 `s_full` 里组合。
