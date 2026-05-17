# s12 — 子 agent

> 用 `os/exec` 拉起隔离的 Go 子进程。文件级 IPC：父写 `TASK.md`、子写 `RESULT.json`。Worker pool 把并发上限卡在 `MaxWorkers`；用 `exec.CommandContext` 做每任务超时 —— deadline 到了自动发 SIGKILL。

## Problem

到 s11 为止，harness 已经能扛崩溃（checkpoint 恢复）和回放历史（event log）。但所有事情还是发生在**一个进程、一个 context window** 里。一个 4 小时、改 60 个文件的任务，无论 s09 的滑动窗口压得多狠，都塞不进 128K token。而且如 `guide/long-running-harness.md` L19-L46 所言，模型还没碰到硬上限就会**赶时间** —— 上下文焦虑、过早收尾、工具调用变少。

逃生通道是"复制"、不是"压缩"。拉起 N 个子进程、每个有自己 fresh 的 128K 窗口、每个只干一件小事。父进程保持小、负责编排；worker 保持专注、负责干完。

这就是上游 `guide/sub-agent.md` L66-L145 的模式。Python 版用 `ThreadPoolExecutor` + `subprocess.run`。Go 这两个原语都有 —— `chan struct{}` 做 pool 信号量、`exec.CommandContext` 做进程 —— 写出来比 Python 原版更短、更稳。

## Solution

`SubAgentSpawner` 接受 `[]SubTask`、返回长度相同的 `[]SubResult`。每个任务拿到：

1. 自己的 `WorkDir`（caller 给；测试里就是 `t.TempDir()`）。
2. 该目录下的 `TASK.md` 文件，body 就是任务的 `Instruction` 字符串。
3. 一次 fresh 的 `exec.CommandContext(timeout, childBinary, workDir)` 调用。
4. exit-0 的时候读 `RESULT.json`；否则（exit 非 0 / 超时 / 子进程忘了写文件）构造失败 SubResult。

```go
spawner := &SubAgentSpawner{
    ChildBinary: "/path/to/cmd/child",
    MaxWorkers:  4,
    Timeout:     30 * time.Second,
}
results := spawner.Spawn(ctx, []SubTask{
    {Name: "a", Instruction: "instruction: ...", WorkDir: dirA},
    {Name: "b", Instruction: "instruction: ...", WorkDir: dirB},
})
for _, r := range results {
    if !r.Success { log.Printf("%s failed: %s", r.Name, r.Output) }
}
```

三条纪律：

| | 规则 |
|---|---|
| IPC | 只走文件。父写 `<WorkDir>/TASK.md`、子读 `os.Args[1]` 当 work dir、子写 `<WorkDir>/RESULT.json`。**没有** stdin 管道、除了 `os.Environ()` 继承之外**没有** env var。 |
| Pool | 容量 `MaxWorkers` 的 buffered `chan struct{}`。每个任务一个 goroutine：进入时 acquire、defer 时 release。能响应取消。 |
| 失败 | **永远不**通过 error 返回。`Spawn` 永远给出 `len(tasks)` 个 SubResult。崩、超时、RESULT.json 没写 → `Success: false`，原因塞在 `Output` 里。 |

## How It Works

**Spawn** 是一个扁平的 task 循环，每个任务一个 goroutine：

```go
for i, t := range tasks {
    go func(i int, t SubTask) {
        sem <- struct{}{}        // 拿槽（用 select 可被取消）
        defer func() { <-sem }() // 还槽
        results[i] = s.runOne(ctx, t)
    }(i, t)
}
```

**runOne** 是单任务完整剧本：建 WorkDir、写 TASK.md、构造带 timeout 的 context、exec 子进程、判结果。判结果有四个分支：

1. `ctx.Err() == context.DeadlineExceeded` → 超时。Output：`"timeout: child killed after <dur>; stderr tail: <tail>"`。
2. `ctx.Err() == context.Canceled` → 父取消。Output：`"canceled: ..."`。
3. `runErr != nil`（且 ctx 还健康）→ 子 exit 非 0。Output：`"child exited non-zero: <err>; stderr tail: <tail>"`。
4. `runErr == nil` → 读 RESULT.json。文件不在 → Success=false 带原因。在 → unmarshal 成 SubResult，把字段抄到父侧的 SubResult 上。

**判断顺序很关键**。`exec.CommandContext` 把 context-cancel 翻译成的 `runErr` 是一个泛型 `*exec.ExitError`，和正常 child 崩长得一模一样；先看 `ctx.Err()` 才能产出对的错误信息。

**stderr 进一个 8 KiB 的尾部缓冲**。再多一点，一个嘴碎的 child 就能把父进程内存撑爆；再少一点，Go panic 的栈就截断了。stdout 直接丢 —— 契约是"写 RESULT.json"、不是"打 stdout"。

**子 binary** 是 `cmd/child/main.go` 的一个小 `main` 包。读 `os.Args[1]/TASK.md`、识别两个测试控制指令（`sleep:<dur>` 和 `crash:true`）方便父侧测试覆盖超时和崩溃路径，然后跑"真"工具 —— 对（去掉指令行后的）instruction 文本做不重复单词数统计。结果以 `{success, output, artifacts}` 的形式写到 `RESULT.json`。

```
Parent                                  Child
──────                                  ─────
mkdir -p WorkDir                        （独立进程）
write TASK.md                              ↓
exec.CommandContext(timeout,            read os.Args[1]+"/TASK.md"
  childBinary, workDir)                 处理 sleep:/crash: 指令
   │                                    统计不重复单词
   ↓ (等)                               write WorkDir+"/RESULT.json"
read RESULT.json                        exit 0
unmarshal → SubResult
```

父侧 `Spawn` 等所有 worker goroutine 都跑完（`wg.Wait()`）才返回，无论任务完成次序如何。Results slice 按输入次序（worker 用自己的 input index 写 `results[i] = ...`），调用方拿 `tasks[i]` 配 `results[i]` 不用看 `Name`。

## What Changed

| | s10（事件日志） | s11（checkpoint） | s12（子 agent） |
|---|---|---|---|
| 并发 | 一个 writer | 一个 writer | N 个进程 |
| 进程边界 | 进程内 | 进程内 | OS 级 |
| 失败隔离 | n/a（单进程崩 = log 没了） | 原子 .tmp + rename | 单任务隔离：一个 child 崩，其他 worker 不受影响 |
| Token 预算 | 共享 | 共享 | N 个独立窗口 |
| 墙钟 | 顺序一轮一轮 | 顺序一轮一轮 | 并行最多 MaxWorkers |

s11 保证状态持久；s12 保证**工作**并行。两者能组合：每个 sub-agent 可以 checkpoint 到自己的 WorkDir、父再读回来。这套接线我们留给 s_full —— 但文件级 IPC 让接起来很自然。

另一件变了的事：**`Spawn` 不返回 error**。前面每一章核心方法（`Provider.Chat`、`Registry.Dispatch`、`Memory.AppendLog`）都返回 `(value, error)`。`Spawn` 只返回 `[]SubResult`，因为**单任务失败模式很常见**，单一 error 返回会逼调用方在"整批失败"（丢掉兄弟任务的成果）和"忽略错误"（丢掉失败信号）之间二选一。Python 上游用 `as_completed` 抓异常解决；我们把异常折叠进 result 解决。

## Try It

```bash
cd agents/s12-sub-agent
go vet ./... && go build ./... && go test -count=1 -timeout=60s ./...
# PASS — 5 个测试

# Demo：3 个并行任务
go build -o /tmp/s12-child  ./cmd/child
go build -o /tmp/s12-parent ./cmd/parent
mkdir -p /tmp/s12demo/a /tmp/s12demo/b /tmp/s12demo/c
echo "instruction: count these words"   > /tmp/s12demo/a/task-a.md
echo "instruction: another bag of stuff" > /tmp/s12demo/b/task-b.md
echo "instruction: short"                > /tmp/s12demo/c/task-c.md
/tmp/s12-parent -child /tmp/s12-child -root /tmp/s12demo
# [OK] task=a name= duration=12ms
#        output: 4 unique words
# [OK] task=b name= duration=11ms
#        output: 5 unique words
# [OK] task=c name= duration=10ms
#        output: 2 unique words
```

想看超时怎么 work，把一个任务加上 `sleep:5s`、`-timeout 1s`：

```bash
printf "instruction: hang\nsleep:5s\n" > /tmp/s12demo/a/task-a.md
/tmp/s12-parent -child /tmp/s12-child -root /tmp/s12demo -timeout 1s
# [FAIL] task=a name= duration=1001ms
#        output: timeout: child killed after 1s; stderr tail:
```

## Upstream Source Reading

来源：`guide/sub-agent.md` L66-L145。Permalink：<https://github.com/nexu-io/harness-engineering-guide/blob/86fec9bea430cecb29ff10afaae36b96496a8f8e/guide/sub-agent.md#L66-L145>

交叉引用：`guide/multi-agent-orchestration.md` L104-L126（"Sub-Agent Spawning"）讲清楚**为什么**这是基础原语 —— 每个 spawn 出来的 session 拥有独立的 context window、system prompt、tool set，父只看最终结果。

```python
# guide/sub-agent.md L66-L145（典型 SubAgentSpawner）
class SubAgentSpawner:
    def __init__(self, agent_command="python -m agent",
                 max_workers=4, timeout=300):
        self.agent_command = agent_command
        self.max_workers = max_workers
        self.timeout = timeout

    def spawn(self, tasks):
        results = []
        with ThreadPoolExecutor(max_workers=self.max_workers) as pool:
            futures = {pool.submit(self._run_agent, task): task
                       for task in tasks}
            for future in as_completed(futures):
                task = futures[future]
                try:
                    results.append(future.result(timeout=self.timeout))
                except Exception as e:
                    results.append(SubResult(
                        name=task.name, success=False,
                        output=f"Agent failed: {type(e).__name__}: {e}",
                        artifacts=[],
                    ))
        return results

    def _run_agent(self, task):
        work_dir = task.working_dir or tempfile.mkdtemp(...)
        task_file = os.path.join(work_dir, "TASK.md")
        with open(task_file, "w") as f:
            f.write(task.instruction)
        result_file = os.path.join(work_dir, "RESULT.json")
        env = os.environ.copy()
        env["AGENT_TASK_FILE"] = task_file
        env["AGENT_RESULT_FILE"] = result_file
        env["AGENT_WORK_DIR"] = work_dir
        proc = subprocess.run(
            self.agent_command.split(), cwd=work_dir, env=env,
            capture_output=True, text=True, timeout=self.timeout,
        )
        if os.path.exists(result_file):
            with open(result_file) as f:
                result_data = json.load(f)
            return SubResult(
                name=task.name,
                success=result_data.get("success", True),
                output=result_data.get("output", ""),
                artifacts=result_data.get("artifacts", []),
            )
        return SubResult(
            name=task.name, success=proc.returncode == 0,
            output=proc.stdout[-5000:] or proc.stderr[-5000:],
            artifacts=[],
        )
```

阅读笔记：

- **上游用 env var（`AGENT_TASK_FILE`、`AGENT_RESULT_FILE`、`AGENT_WORK_DIR`）；我们只传一个 CLI arg。** Env var 也能 work，但 child 要读三件互不相干的东西；只传一个 work-dir arg、child 读**一**个、其余用 `filepath.Join` 推出来。耦合更少、漂移更少。
- **上游的 `as_completed` 按完成次序收 results；我们保持输入次序。** Python 那套很方便，因为典型用法是 `print(result)` 立即消费。Go 的紧类型系统鼓励 "results[i] 对应 tasks[i]"，调用方写 `for i, r := range results { fmt.Println(tasks[i].Name, r) }`、不用看 `r.Name`。
- **`exec.CommandContext` 顶掉 `subprocess.run(timeout=...)`**。两者扮演同一角色，但 Go 原语把 SIGKILL 接到了 context 上。单独搞一个 `time.AfterFunc(timeout, cmd.Process.Kill)` 也能 work，但会引入 "kill 到了，进程已退出" 的竞速，context API 把这件事关上了。
- **stderr/stdout 的优先级反过来了**。Python 是 stdout 空才看 stderr；我们直接抓 stderr、stdout 一律丢。原因：生产环境 child 是 `python -m agent`（上游默认），它自己的日志就写到 stderr —— stdout 留给那些不走文件 IPC 的工具输出。直接丢 stdout 比每次猜 "诊断信息在哪边" 便宜。
- **`[Conversation history summary]` 和 `[Conversation summary]` 的命名漂移是上游真的存在的维护风险**。s12 的对应物是文件名：`TASK.md` 和 `RESULT.json` 在父和子里都是硬编码常量。别改，否则父子静默地停止通信。

阅读地图：

| 主题 | 上游文件 | 行号 | 对应章节 |
|------|----------|------|----------|
| SubAgentSpawner 类 | `guide/sub-agent.md` | L66-L145 | s12（本章） |
| 子 agent 基础原语 | `guide/multi-agent-orchestration.md` | L104-L126 | s12 交叉引用 |
| 文件级消息传递（inbox/claim） | `guide/sub-agent.md` | L147-L195 | s12 设计依据 |
| Session 隔离（独立 context window） | `guide/sub-agent.md` | L197-L224 | s12 动机 |
| Git worktree 用于并行改代码 | `guide/sub-agent.md` | L226-L273 | 后续扩展 |
| 常见陷阱（过度拆分、不设 timeout） | `guide/sub-agent.md` | L275-L281 | s12 设计约束 |
