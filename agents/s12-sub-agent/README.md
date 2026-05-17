# s12-sub-agent

> Spawn isolated Go child processes via `os/exec`. Parent writes `TASK.md`, child writes `RESULT.json`. Worker pool with `MaxWorkers` + per-task timeout via `exec.CommandContext`.
> 用 `os/exec` 拉起隔离的 Go 子进程。父进程写 `TASK.md`、子进程写 `RESULT.json`。worker pool 限制并发数 + 用 `exec.CommandContext` 做每任务超时。

## Scope / 范围

Implement the `SubAgentSpawner` pattern from `guide/sub-agent.md` L66-L145 in ~250 lines of Go. The upstream Python uses `ThreadPoolExecutor` + `subprocess.run`; we use a buffered-channel semaphore + `exec.CommandContext` so SIGKILL fires automatically when the context expires. No real LLM call — the child binary implements a deterministic toy "tool" (counts unique words in the instruction) so tests are reproducible and fast.
用 ~250 行 Go 实现 `guide/sub-agent.md` L66-L145 的 `SubAgentSpawner` 模式。上游 Python 用 `ThreadPoolExecutor` + `subprocess.run`；我们用带 buffer 的 channel 当信号量 + `exec.CommandContext` 让 context 过期时自动 SIGKILL。**不调真实 LLM** —— 子进程实现一个确定性的玩具"工具"（统计 instruction 里的不重复单词数），测试可复现且快。

## Files / 文件

```
go.mod                 module learn-harness-engineering-guide/s12-sub-agent
task.go                SubTask + SubResult (JSON-tagged wire types)
spawner.go             SubAgentSpawner.Spawn — worker pool, file IPC, timeout
cmd/parent/main.go     Demo CLI: scan a dir of task-*.md, fan out, print results
cmd/child/main.go      Child binary: read TASK.md, count words, write RESULT.json
spawner_test.go        TestMain builds child binary; 5 tests
```

## Run / 运行

```bash
cd agents/s12-sub-agent

# Build both binaries
go build -o /tmp/s12-child  ./cmd/child
go build -o /tmp/s12-parent ./cmd/parent

# Prepare a few tasks (each subdir = one task)
mkdir -p /tmp/s12demo/a /tmp/s12demo/b /tmp/s12demo/c
echo "instruction: count these unique words" > /tmp/s12demo/a/task-a.md
echo "instruction: another bag of words here" > /tmp/s12demo/b/task-b.md
echo "instruction: short input"               > /tmp/s12demo/c/task-c.md

# Fan out
/tmp/s12-parent -child /tmp/s12-child -root /tmp/s12demo
```

## Test / 测试

```bash
go vet ./... && go build ./... && go test -count=1 -timeout=60s ./...
# PASS — 5 tests
```

Tests spawn real child processes. `TestMain` builds `cmd/child` into a `t.TempDir()`-style location once at suite startup; individual tests then re-use that binary path. **`go test` must find `go` on PATH** — required by `TestMain`'s `exec.Command("go", "build", ...)` call. CI uses `actions/setup-go@v5` which guarantees this. Locally, if your shell's PATH was stripped, `TestMain` skips the suite with a stderr note rather than hanging.
测试会拉起真实子进程。`TestMain` 在 suite 启动时把 `cmd/child` 编译到临时目录，每个测试复用这个 binary。**`go test` 必须能在 PATH 里找到 `go`** —— `TestMain` 里要 `exec.Command("go", "build", ...)`。CI 用 `actions/setup-go@v5` 保证这一点。本地如果 shell 把 PATH 清掉了，`TestMain` 会在 stderr 打一行 skip，不会卡住。

## Key teaching points / 教学要点

1. **The file IPC contract is the entire boundary.** Parent writes `<WorkDir>/TASK.md`; child reads `os.Args[1]` as the work directory; child writes `<WorkDir>/RESULT.json`. No stdin pipes, no env vars, no JSON-RPC. The whole protocol fits in one paragraph and survives the parent dying mid-task (the child's RESULT.json is still readable from the work dir).
   **整个边界就是文件 IPC**。父写 `<WorkDir>/TASK.md`、子读 `os.Args[1]` 作为 work dir、子写 `<WorkDir>/RESULT.json`。**没有** stdin 管道、env var、JSON-RPC。整套协议一段话讲完，且就算父进程在任务中途挂了，子写的 RESULT.json 还在 work dir 里能读出来。
2. **`exec.CommandContext` is the timeout primitive, not `time.AfterFunc`.** The standard library hooks SIGKILL onto context cancellation — no extra goroutine, no race between "kill" and "wait". `TestSpawner_TimeoutKillsHungChild` is the canary: a child that sleeps 5s with a parent timeout of 1s gets killed in well under 2s, every time.
   **超时的原语是 `exec.CommandContext`、不是 `time.AfterFunc`**。标准库把 SIGKILL 挂在 context 取消上 —— 不用额外 goroutine、不会出现 "kill 和 wait 竞速" 的问题。`TestSpawner_TimeoutKillsHungChild` 是金丝雀：子进程 sleep 5s、父超时 1s，每次都在 2s 内被干掉。
3. **Worker pool = buffered channel of capacity N.** No goroutine pool, no `sync.Pool`. Each task gets its own goroutine that acquires from `sem`, runs the child, releases. The semaphore caps concurrency at `MaxWorkers`. When the outer context fires while a goroutine is waiting on the semaphore, the goroutine bails out with a synthetic "context canceled" SubResult — we never block past cancellation.
   **Worker pool = 容量 N 的 buffered channel**。没有 goroutine 池、没有 `sync.Pool`。每个任务一个 goroutine：从 `sem` 取一个槽、拉起子进程、释放槽。信号量把并发上限卡在 `MaxWorkers`。外层 context 在 goroutine 等信号量的时候过期，goroutine 直接吐一个合成的 "context canceled" SubResult —— 永远不会卡在取消之后。
4. **Failures live inside the SubResult, not as error returns.** `Spawn` returns `[]SubResult` with the same length as the input slice. A child crash, a timeout, a missing RESULT.json — all collapse to `SubResult{Success: false, Output: <reason>}`. This mirrors the upstream `as_completed` exception-handling pattern (Python catches per-task and converts to result) and means the caller writes a flat loop, not an error tree.
   **失败信息塞在 SubResult 里、不走 error 返回值**。`Spawn` 的返回长度永远等于输入 tasks 长度。子进程崩、超时、RESULT.json 没写 —— 全都折叠成 `SubResult{Success: false, Output: <原因>}`。这套和上游 `as_completed` 抓异常转 result 的写法一致，调用方写一个扁平的循环就行、不用嵌错误树。
5. **Child binary imports the lib package, not vice versa.** `cmd/child/main.go` imports `learn-harness-engineering-guide/s12-sub-agent` so it can use the SAME `SubResult` struct the parent reads. This is the one Go-side type coupling we allow; everything else goes through the file. If the project grew, we'd extract a `wire.go` to make the coupling explicit.
   **子 binary import lib package、不是反过来**。`cmd/child/main.go` import `learn-harness-engineering-guide/s12-sub-agent`，这样它能用和父进程**同一个** `SubResult` 结构。这是我们唯一允许的 Go-side 类型耦合；其他全走文件。等项目变大就单独拆一个 `wire.go` 把耦合显式化。

## What the next chapter changes / 下一节的变化

s13 introduces `CronSchedule` — 5-field cron parser, `NextRun(now)`, `ShouldRun(now)`. It's a pure-stdlib mechanism with no LLM call, no child process. Composes neatly with s12 for the "every day at 8am, spawn a sub-agent to do a digest" pattern, but they're independent: s12 doesn't know what time it is, s13 doesn't know what work means. The natural place they meet is s_full.
s13 引入 `CronSchedule` —— 5 字段 cron parser、`NextRun(now)`、`ShouldRun(now)`。纯标准库的机制、不调 LLM、不拉子进程。和 s12 组合就能做"每天早上 8 点拉个 sub-agent 跑一下日报"这种模式，但两者本身独立：s12 不知道现在几点，s13 不知道"干活"是什么。它们在 s_full 里相遇。
