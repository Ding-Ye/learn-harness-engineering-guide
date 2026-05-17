# s13-cron-scheduler

> Self-contained 5-field cron parser + scheduler. `Parse` an expression and a timezone, `ShouldRun(now)` for the exact-minute boundary check, `NextRun(now)` for the next firing instant. Caller drives the clock — no goroutines, no `time.Sleep`.
> 自包含的 5 字段 cron 解析器 + 调度器。`Parse` 拿到表达式和时区、`ShouldRun(now)` 判断是否正好在触发分钟、`NextRun(now)` 算下一次触发。**由调用方驱动时钟** —— 没有 goroutine、没有 `time.Sleep`。

## Scope / 范围

Implement the cron primitive from `guide/scheduling-and-automation.md` L78-L160 in ~400 lines of Go. Five-field grammar (`* * * * *` minute / hour / dom / month / dow), `*`, `*/N`, `A-B`, `A,B,C`, absolute numbers, day-of-week `0` and `7` both = Sunday. Timezone-aware: parse with an IANA name, store the schedule, evaluate against incoming UTC `time.Time` by converting to local first. Frozen-clock tests — no flakiness, no skew across CI runners.
用 ~400 行 Go 实现 `guide/scheduling-and-automation.md` L78-L160 的 cron 原语。五字段语法、支持 `*` / `*/N` / `A-B` / `A,B,C` / 绝对数、星期 `0` 和 `7` 都等于 Sunday。时区感知：parse 时给 IANA 时区名、存好之后用 UTC `time.Time` 进来、内部转成 local 再匹配。**测试用冻结时钟** —— 不抖、不跨 runner 飘。

## Files / 文件

```
cron.go         CronSchedule{Expression, Timezone, Payload, parsed fields} + Parse + matches + ShouldRun + NextRun
fields.go       fieldSet ([]bool) + parseField + applyTerm + resolveBase + parseInt
scheduler.go    Scheduler{schedules map}: Add / Tick / Len. No goroutine.
main.go         CLI demo: steps a frozen UTC clock through 25 hours.
cron_test.go    7 test groups (Parse happy/error, NextRun daily/tz/15min, ShouldRun boundary/dow, Scheduler.Tick)
```

## Run / 运行

```bash
cd agents/s13-cron-scheduler
go run .
# registered schedule "0 8 * * *" in Asia/Shanghai
# === ticking from 2026-05-17T22:00:00Z for 25 hours ===
# [hour  2 UTC=00:00 UTC local=2026-05-18 08:00 CST] FIRES: [daily-digest]
# ...
```

## Test / 测试

```bash
go test -count=1 ./...
# PASS — 7 tests, ~30 sub-cases
```

## Key teaching points / 教学要点

1. **Store UTC. Display local. Match in local.** The schedule's `time.Location` is the source of truth for the five fields; `ShouldRun(now)` and `NextRun(now)` both call `now.In(c.loc)` *before* truncating and comparing. The Asia/Shanghai 8am test (`TestNextRun_TimezoneConversion`) catches the off-by-one bug where you forget the conversion and end up comparing UTC components against a Shanghai-meant field.
   **UTC 存、本地显示、本地匹配**。Schedule 的 `time.Location` 是五个字段的真相源；`ShouldRun(now)` 和 `NextRun(now)` 都会先 `now.In(c.loc)` 再 truncate 比较。"Asia/Shanghai 8am"那个测试就是用来抓"忘记转换、拿 UTC 分量去比 Shanghai 字段"的经典 off-by-one。

2. **Truncate-to-minute gives ShouldRun a finite-precision boundary.** `ShouldRun` is true iff `now.In(loc).Truncate(time.Minute)` matches all five fields. That makes `14:30:00.000`, `14:30:30`, and `14:30:59.999` all answer true for a 30-14 schedule — and `14:29:59.999` and `14:31:00.000` both answer false. Sub-second jitter from a real clock won't desync the tick.
   **Truncate 到分钟，让 ShouldRun 有一个有限精度的边界**。`ShouldRun` 当且仅当 `now.In(loc).Truncate(time.Minute)` 命中所有五个字段才返回 true。这样 `14:30:00.000`、`14:30:30`、`14:30:59.999` 对于 30-14 这个 schedule 全是 true；`14:29:59.999` 和 `14:31:00.000` 全是 false。**真实时钟的亚秒级抖动**不会让 tick 错位。

3. **Day-of-month vs day-of-week is OR, not AND.** Classic Vixie-cron semantics: if both `dom` and `dow` are restricted (not `*`), the schedule fires when *either* matches. We detect "is this field equivalent to `*`?" by checking `isFullField(fs, kind)` against the parsed bitmap, not by re-parsing the raw expression — that way `0-23` on the hour field is treated as `*` for the OR rule.
   **dom 和 dow 是 OR，不是 AND**。经典 Vixie-cron 语义：如果 dom 和 dow 都被限制（都不是 `*`），任意一个命中就触发。我们判断"这个字段是不是等价于 `*`"用的是 `isFullField(fs, kind)` 检查 parsed bitmap，**不是**回去 re-parse 原始字符串 —— 这样 `0-23` 这种写在小时位上的"伪 \*"也能被识别。

4. **Sunday is both 0 and 7 — fold at parse time.** `parseField` accepts up to 7 for the day-of-week field, then at the end of the function, if index 7 is set, it copies that bit to index 0 and clears index 7. The matcher only ever indexes 0..6, so the fold happens once in one place. Test `TestShouldRun_DowSundayAcceptsBoth0And7` pins the behavior.
   **Sunday 既是 0 也是 7 —— 在 parse 阶段折叠**。`parseField` 接受最大 7 的 dow 字段输入；在函数末尾，如果索引 7 被设置了，就把那一位拷到索引 0、再清零索引 7。匹配器只会索引 0..6，**折叠在一个地方做一次**。`TestShouldRun_DowSundayAcceptsBoth0And7` 把这个性质钉死。

5. **Brute-force NextRun is fine for a 4-year horizon.** The implementation walks forward one minute at a time from `now.Truncate(Minute).Add(time.Minute)` until a match is found or 4 years elapse. That's at most ~2.1M iterations and finishes in microseconds for the realistic schedules in the test suite. Trading clarity for cycles in a teaching chapter would not be a win.
   **暴力 NextRun 在 4 年窗口内完全够用**。实现是从 `now.Truncate(Minute).Add(time.Minute)` 开始一分钟一分钟往前走、命中或者 4 年过去就停。最多 ~210 万次迭代、测试集里所有实际 schedule 都在微秒级返回。**教学章节里**为了周期数把代码绕得看不懂不划算。

6. **The scheduler does not own the clock.** `Scheduler.Tick(now)` is a pure function of `(now, registered schedules)`. The caller drives. This is the same discipline as s05's injected Clock and s09's deterministic message feeder — it's what makes the tests deterministic and what lets a real harness layer in distributed locking, retry, persistence, etc., *around* the primitive.
   **Scheduler 不持有时钟**。`Scheduler.Tick(now)` 是 `(now, 注册的 schedules)` 的纯函数。调用方推进。和 s05 的注入 Clock、s09 的确定性 feeder 一个套路 —— **这才让测试可确定**，也让真实 harness 在原语**外面**层叠分布式锁、重试、持久化这些。

## What the next chapter changes / 下一节的变化

s14 (`classifier-permissions`) is the LAST mechanism chapter and tackles a different problem: replacing s06's static allow/deny lists with a two-stage model-based permission classifier. The cron primitive of this chapter would, in `s_full`, *call into* s14 — every time a scheduled job fires, the action it wants to take goes through the classifier. But the two are otherwise unrelated: s13 is pure time arithmetic; s14 is pure LLM-based decisioning.
s14（`classifier-permissions`）是**最后一个机制章**，处理一个完全不同的问题：用两阶段、基于模型的权限分类器**替换** s06 的静态白/黑名单。本章的 cron 原语在 `s_full` 里会**调用** s14 —— 每次定时任务触发，它想做的动作都要走分类器。但除此之外两者不相干：s13 是纯时间算术、s14 是纯 LLM 决策。
