# learn-harness-engineering-guide

> 用可运行的 Go 代码重新实现 Nexu 的 **Harness Engineering Guide** —— 14 章，渐进式。
>
> [中文](#中文) · [English](README.md)

[![Go](https://img.shields.io/badge/Go-1.21+-blue?logo=go)](https://golang.org)
[![License](https://img.shields.io/badge/license-MIT-green)](LICENSE)
[![Upstream](https://img.shields.io/badge/upstream-nexu--io/harness--engineering--guide-orange)](https://github.com/nexu-io/harness-engineering-guide)

## 中文

上游 [Harness Engineering Guide](https://github.com/nexu-io/harness-engineering-guide) 是 25 篇 guide、约 13K 行 markdown，讲清楚怎么构建一个 AI agent harness：agentic loop、工具注册、记忆分层、护栏、子 agent、分类器权限 —— 把裸 LLM 变成可用 agent 的整套技术栈。

本仓库把这些散文转写成 **14 章渐进式 Go 实现**，每章满足：

- ~150-700 行符合 Go 习惯的代码
- 独立模块（每章自己的 `go.mod`，章节之间**不互相 import**）
- 3-8 个单元测试，**无 API key 也能在 CI 通过**
- 双语 `docs/zh/` + `docs/en/`，六段式骨架：**问题 → 方案 → 工作原理 → 与上一节的变化 → 动手试 → 上游源码阅读**
- 一份注释版的上游 guide 节选，钉在固定 commit

第一章 s01 第一天就上线了（最小 agentic loop）。后续每章加一个机制。

### 课程（14 章）

| # | 章节 | 机制 | 状态 |
|---|------|------|:----:|
| s01 | [最小循环](docs/zh/s01-minimum-loop.md) | think → act → observe | ✅ |
| s02 | [LLM Provider](docs/zh/s02-llm-provider.md) | Anthropic / mock 同一接口 | ✅ |
| s03 | [工具注册](docs/zh/s03-tool-registry.md) | Schema + 分发 | ✅ |
| s04 | [上下文装配](docs/zh/s04-context-assembler.md) | 优先级打包 + token 预算 | ✅ |
| s05 | [记忆层](docs/zh/s05-memory-layer.md) | MEMORY.md + 每日 log | ✅ |
| s06 | [护栏](docs/zh/s06-guardrails.md) | 白/黑名单 + 分级审批 | ✅ |
| s07 | [错误与重试](docs/zh/s07-error-retry.md) | 分类 + 指数退避 | ✅ |
| s08 | [Skill 系统](docs/zh/s08-skill-system.md) | 按需加载的工具 bundle | ✅ |
| s09 | [上下文压缩](docs/zh/s09-context-compression.md) | 滑动窗口 + 摘要 | ✅ |
| s10 | [Session 事件日志](docs/zh/s10-session-event-log.md) | JSONL append-only | ✅ |
| s11 | [Checkpoint 恢复](docs/zh/s11-checkpoint-resume.md) | 原子 `.tmp` + rename | ✅ |
| s12 | [子 agent](docs/zh/s12-sub-agent.md) | 进程级 IPC（文件） | ✅ |
| s13 | [Cron 调度](docs/zh/s13-cron-scheduler.md) | 5 字段 cron、`NextRun`、`ShouldRun` | ✅ |
| s14 | [分类器权限](docs/zh/s14-classifier-permissions.md) | 两阶段模型决策 | ✅ |
| s_full | [集成](docs/zh/s_full-integration.md) | 16 步端到端追踪 | ✅ |
| A | [上下文焦虑](docs/zh/appendix-a-context-anxiety.md) | 长任务失败模式 | ✅ |
| B | [上游导读地图](docs/zh/appendix-b-upstream-map.md) | 25 篇 guide 的阅读次序 | ✅ |

### 快速开始

```bash
git clone https://github.com/Ding-Ye/learn-harness-engineering-guide.git
cd learn-harness-engineering-guide

# 运行 s01
cd agents/s01-minimum-loop
go test -count=1 ./...
go run . "hello world"
# → I ran the echo tool on "hello world". Task complete.
```

s01 不需要 API key —— `MockProvider` 返回脚本化响应，离线就能跑。s02 之后引入真实 `Provider` 接口，但测试在 `ANTHROPIC_API_KEY` 未设置时 `t.Skip()`，CI 仍然全绿。

### 怎么读这个仓库

1. 从 [`docs/zh/s01-minimum-loop.md`](docs/zh/s01-minimum-loop.md) 开始 —— 它用 6 段式讲清楚 agentic loop 这个核心抽象，最后给出注释版上游源码。
2. 打开 `agents/s01-minimum-loop/`，按 `loop.go` → `mock_provider.go` → `echo_tool.go` → `loop_test.go` 的次序读。
3. 想看下一层抽象，去 `docs/zh/s02-llm-provider.md`。

每一章的 doc 都是自包含的：你可以从任何一章开始，读完后知道一个上游概念 + 一个 Go 实现模式。

### 项目结构

```
learn-harness-engineering-guide/
├── README.md                 (英文)
├── README.zh-CN.md           (本文件)
├── go.work                   (所有章节模块的 workspace)
├── agents/
│   └── s01-minimum-loop/     (Go 模块：代码 + 测试 + 章节 README)
├── docs/
│   ├── en/s01-minimum-loop.md
│   └── zh/s01-minimum-loop.md
├── upstream-readings/        (上游节选 + 注释)
├── web/index.html            (课程入口页，无构建步骤)
├── LICENSE                   (MIT，含上游致谢)
└── .github/workflows/go.yml  (按章节矩阵化的 CI)
```

### 致谢

- 教学内容源自 [nexu-io/harness-engineering-guide](https://github.com/nexu-io/harness-engineering-guide)（MIT，© 2026 Nexu）。上游 guide 节选均在文件内署名并钉在 commit `86fec9b`。
- 教学法借鉴 [shareAI-lab/learn-claude-code](https://github.com/shareAI-lab/learn-claude-code)：六段式骨架、章节独立模块、无跨章 import。
- 由 [Claude Code](https://github.com/anthropics/claude-code) 通过 `learn-repo-generator` skill 生成。

### License

MIT —— 见 [LICENSE](LICENSE)。
