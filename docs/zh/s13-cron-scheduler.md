# s13 — Cron 调度

> 自包含的 5 字段 cron 解析器 + 一个极小的调度器。`Parse` 拿到表达式和时区；`ShouldRun(now)` 判断 `now` 是不是正好压在触发分钟上；`NextRun(now)` 给出下一次触发的 UTC 时刻。**调用方持有时钟** —— 不开 goroutine、不 `time.Sleep`、不抖。

## Problem

到 s12 为止，harness 已经能 spawn 隔离 worker、把每个事件持久化记录（s10）、跨崩溃做 checkpoint（s11）、压缩自己的 context window（s09）、每个工具调用都过 guardrail（s06）。**用户让它做的事它都能做**。但上游 guide 还用整整一章在讲一个互补的价值：那种**没人开口它也会做事**的 agent。

`guide/scheduling-and-automation.md` L11-L18：

> 大多数人第一次接触 Agent 是聊天界面 —— 你打字、它回复。这种心智模型把价值锁在"更快的搜索引擎"。**真正的解锁，是 Agent 在没人要求时主动执行工作**。

要让这件事真的发生，你需要一个**时钟原语** —— 它知道"每个工作日早上 8 点（Asia/Shanghai），跑这个 job"。**概念上简单；却以掩盖时区、夏令时、'分钟边界到底有没有触发过一次'这些恶心 bug 而臭名昭著**。上游章节把生产里真正咬人的失败模式一一列出（L96-L108 讲时区、L300-L319 讲缺超时和重复投递）。我们重建一个**最小**的、把上述全部点对了的原语。

我们**故意不做**：`time.Ticker` goroutine、分布式锁、持久化、重试、jitter、`@daily`/`@hourly` 简写、带秒的 6 字段 cron、`MON`/`TUE` 别名。**那些都层叠在原语外面**；本章的工作就是把原语做对。

## Solution

`CronSchedule` 是一个 5 字段表达式 + 一个 IANA 时区名 解析之后的形态：

```go
sch, err := Parse("0 8 * * *", "Asia/Shanghai")
// sch.Expression = "0 8 * * *"
// sch.Timezone   = "Asia/Shanghai"
// sch.loc        = Asia/Shanghai 的 *time.Location
// sch.minute/hour/dom/month/dow = 解析好的 fieldSet 五件套

sch.ShouldRun(now)            // now 是否正好压在触发分钟上？
nextUTC, _ := sch.NextRun(now) // 下一次触发的 UTC 时刻是？
```

每个字段接受的语法就是上游 L84-L94 的最小集：`*`、`*/N`、`A-B`、`A,B,C`、绝对整数；外加 dow 的怪癖：`0` 和 `7` **都**表示 Sunday。除此之外都是 parse 错误。

`Scheduler` 是 `map[string]*CronSchedule` 的一层薄包装，只有一个 `Tick(now time.Time) []string` 方法 —— 返回 `ShouldRun(now)` 为 true 的全部 schedule 名字。**调用方推进时钟**。**没有 goroutine、没有 `time.Sleep`**。

| 纪律 | 理由 |
|---|---|
| UTC 存、本地显示、**本地匹配**。 | `time.Location` 是真相源；每次匹配第一件事就是 `now.In(loc)`。 |
| `ShouldRun` 里 truncate 到分钟。 | 真实时钟有抖动；对亚秒级噪声鲁棒的"一分钟粒度"才是契约。 |
| dom 和 dow 都被限制时是 OR。 | 经典 Vixie 语义；guide 例子都假设这个。 |
| dow 的 7 在 parse 阶段折叠到 0。 | 一个地方处理一次；匹配器只索引 0..6。 |
| 调用方驱动时钟。 | 冻结时钟测试、零 goroutine、零抖。 |

## How It Works

**Parse** 是先严格检查 5 字段、再调五次独立的 `parseField`。每次 `parseField` 处理 `,` 分隔的若干 term；每个 term 可能带 `/N` 步进、base 可以是 `*`、`A` 或 `A-B`。结果是一个尺寸**精确等于**字段合法范围的 `fieldSet = []bool` —— 范围外的值会触发 slice 越界，所以**范围检查只在 parser 里做一次**。

```go
// 一个 term，所有 case 都压到一个 stride 循环里：
for v := lo; v <= hi; v += step {
    out[v] = true
}
```

其中 `(lo, hi, step)` 来自：

| Term | lo | hi | step |
|---|---|---|---|
| `*` | min | max | 1 |
| `A` | A | A | 1 |
| `A-B` | A | B | 1 |
| `*/N` | min | max | N |
| `A/N` | A | max | N |
| `A-B/N` | A | B | N |

`A/N` 这个 Vixie 扩展最容易漏：它的意思是"从 A 到 max、每 N"，**不是**"就 A 一个"。`parseField` 看到"裸整数 base + step"时会把 `hi` 改成 `kind.max`。**容易写错的方式**：把这个特例硬编码进 `resolveBase`，结果 `A-B/N` 也被错误地放宽 —— 把放宽留在调用方让特例只影响特例。

**Match** 是对 `now.In(loc).Truncate(time.Minute)` 的五字段 bitmap 探针：

```go
if !c.minute[local.Minute()] { return false }
if !c.hour[local.Hour()]     { return false }
if !c.month[int(local.Month())] { return false }
// 两个都被限制时 OR；任一是 * 就跟另一个 AND：
domIsAll := isFullField(c.dom, kindDom)
dowIsAll := isFullField(c.dow, kindDow)
switch {
case domIsAll && dowIsAll: return true
case domIsAll:             return c.dow[dow]
case dowIsAll:             return c.dom[dom]
default:                   return c.dom[dom] || c.dow[dow]
}
```

`isFullField` 检查的是**解析后的 bitmap**、**不是**重新解析原始表达式。这样 `0-23` 写在小时位上也能正确被识别为"任意小时"参与 dom-vs-dow 的 OR 规则 —— 和 `*` 等价。**在这份代码里，"匹配一切"只有一种内部形态、用户怎么写都收敛**。

**NextRun** 走暴力路线：从 `now.In(loc).Truncate(Minute).Add(Minute)`（严格之后）开始、一分钟一分钟地往前走、每个候选都过 `matches()`，直到命中或 4 年过去。**4 年是覆盖 "仅 2 月 29 日触发" 这种 schedule 的最小窗口**；暴力循环最坏 ~210 万次迭代，测试集里全部在微秒级返回。"跳过不匹配字段"那种聪明优化省周期但加 100+ 行 off-by-one 风险 —— **教学章节里这笔交易不划算**。

**Scheduler.Tick** 字面意思：问每个注册的 schedule "`ShouldRun(now)` 吗？"、把回答 yes 的名字按字典序返回。排序很重要 —— 真实 harness 会把输出灌进 event log（s10）或 worker pool（s12）；**稳定排序让下游产物 diff 起来可读**。

**这个设计最防的一种 bug 形态**：`Scheduler` **不**持有 goroutine、`time.Ticker`、`time.Sleep`。真实 harness 都需要这些 —— 但要**层叠在原语外面**。测试直接传 `time.Date(...)`、生产直接传 `time.Now().UTC()`；类型本身不变。

## What Changed

| | s12（sub-agent） | s13（cron 调度） |
|---|---|---|
| 关心的事 | spawn 隔离 worker | **什么时候**做事 |
| 并发 | 是 —— 进程池、超时 | 没有 —— `(now, schedules)` 的纯函数 |
| 时间 | 不在乎 | **整个主题** |
| LLM | 子进程跑 mini-loop | **完全不涉及 LLM** |
| 外部状态 | 工作目录里的文件 | 没有 |

s13 是自 s04 以来第一个 happy path 里**没有 LLM 调用**的章节。这是设计 —— cron 原语就是纯时间算术。在 `s_full` 里接线是：`Scheduler.Tick(time.Now().UTC())` 每分钟跑 → 返回到期 schedule 名字 → 调用方用 s12 的 sub-agent spawner 分发每个名字 → 每个子进程开一个新的 agentic loop、过 s06 + s07 + s14。**cron 不挡 agent 的路；agent 不挡 cron 的路**。

## Try It

```bash
cd agents/s13-cron-scheduler
go vet ./... && go build ./... && go test -count=1 ./...
# PASS —— 7 个测试

go run .
# registered schedule "0 8 * * *" in Asia/Shanghai (next run after now in UTC = 2026-...)
#
# === ticking from 2026-05-17T22:00:00Z for 25 hours ===
# [hour  0 UTC=22:00 UTC] (nothing due)
# [hour  1 UTC=23:00 UTC] (nothing due)
# [hour  2 UTC=00:00 UTC local=2026-05-18 08:00 CST] FIRES: [daily-digest]
# [hour  3 UTC=01:00 UTC] (nothing due)
# ...
```

**唯一触发**的那个 tick 在 UTC `00:00`、对应 Shanghai `08:00`。这就是 "UTC 存、本地匹配" 在干活：表达式 `0 8 * * *` 是 Asia/Shanghai 视角的；matcher 在一个 UTC 时刻上回答 true，是因为 `now.In(loc)` 先把它折成了本地时间。

## Upstream Source Reading

来源：`guide/scheduling-and-automation.md` L78-L160。永久链接：<https://github.com/nexu-io/harness-engineering-guide/blob/86fec9bea430cecb29ff10afaae36b96496a8f8e/guide/scheduling-and-automation.md#L78-L160>

```text
Cron Implementation Patterns (摘录，L78-L160)

Schedule Definition

Cron 表达式使用标准 5 字段格式：

    ┌───────── minute (0–59)
    │ ┌─────── hour (0–23)
    │ │ ┌───── day of month (1–31)
    │ │ │ ┌─── month (1–12)
    │ │ │ │ ┌─ day of week (0–7, 0 和 7 = Sunday)
    │ │ │ │ │
    * * * * *

时区处理是 Cron bug 最常见的来源。规则只有一句：

    > Store UTC. Display local.

内部，Harness 在 UTC 上评估所有 Cron 表达式；展示给用户时，转换到本地时区。

    User: "Run my digest every morning at 8am"
    Agent: "Got它—— daily digest at 8:00 AM Asia/Shanghai (0:00 UTC). ✓"

Session Targeting / Payload Types / Delivery

Isolated session vs main session 注入；agentTurn vs systemEvent payload；
announce / webhook / silent 投递。投递在创建时绑定到来源 session。
```

阅读笔记：

- **"Store UTC. Display local." 看上去是 advice，其实是个伪装成 advice 的类型系统断言**。Schedule 的权威形态是 `(UTC time.Time, *time.Location)`、其它一切都是 rendering。本 Go 版里，`CronSchedule.Timezone` 是 rendering 提示、`loc *time.Location` 是解析后的权威。**漏掉 `now.In(loc)` 这一步**，`TestNextRun_TimezoneConversion` 立刻挂。
- **L84-L94 的语法是故意做小的 —— 没有 `@daily`、没有秒、没有 `L`/`#`、没有 `MON`/`TUE`**。只实现 `*`、`*/N`、`A-B`、`A,B,C`、绝对整数，~200 LOC 覆盖 90% 真实场景。每个扩展都要加 ~50 行 + 自己的 edge case 测试；**教学章节里超出范围**。
- **L91 的 "0 和 7 = Sunday" 是这个字段唯一的"委员会设计"伤疤**。老 `cron` 接受 1-7、1=Sunday；BSD `cron` 翻成 0-6、0=Sunday；Vixie 一统江湖：**同时**接受 0 和 7 都表 Sunday。我们在 `parseField` 末尾把索引 7 折到 0，**匹配器只索引 0..6**。
- **Session targeting / payload / delivery（L111-L153）在我们这一层不是 cron 的事**。这些是下游消费者的事。原语只持有 `Expression`、`Timezone`、加一个不透明的 `json.RawMessage` Payload；**schedule 触发时拿 payload 去做什么，是调用方的问题**。和 s10 event log 一样的分层。
- **"delivery 绑定到来源 session"（L152-L153）是个结构性不变量**。我们的 `Payload` 是 `json.RawMessage`、构造时设、**没有 setter** —— 用户想换 delivery 就新建一个 schedule。把 "诶把这个改投到 #ops 吧" 这种 bug 形态在源头堵掉。

阅读地图：

| 主题 | 上游文件 | 行号 | 对应章节 |
|------|----------|------|----------|
| 为什么 scheduling 重要 | `guide/scheduling-and-automation.md` | L9-L75 | s13 心智模型 |
| 5 字段语法 | `guide/scheduling-and-automation.md` | L84-L94 | s13（本章） |
| Store UTC, display local | `guide/scheduling-and-automation.md` | L96-L107 | s13（本章） |
| Session targeting / payload / delivery | `guide/scheduling-and-automation.md` | L111-L153 | s13 消费方（本章外） |
| Heartbeat vs Cron | `guide/scheduling-and-automation.md` | L250-L272 | s13 交叉引用 |
| 反模式 | `guide/scheduling-and-automation.md` | L278-L319 | s13 + s12 交叉引用 |
