# 多模型接入

> 把 DeepSeek、Qwen、Moonshot/Kimi、Groq、OpenRouter，或者你自己的 vLLM/SGLang
> 自建服务，全部接到同一个 agentic loop —— **而不改动 loop**。

s02 自带两个 `Provider` 实现：`MockProvider`（离线测试用）和
`AnthropicProvider`（真实 HTTPS 调用 `api.anthropic.com`）。Phase G 增加第三个 ——
`OpenAIProvider` —— 它说的是 **OpenAI Chat Completions** wire format。
这套格式已经是事实标准：绝大多数非 Anthropic 厂商都暴露一个 OpenAI 兼容端点，
所以**一层翻译就能解锁整个生态**。

## 为什么有两套 wire format

请先翻一下 `agents/s02-llm-provider/types.go`。s02 之后的每一章都通过这套
canonical `ChatRequest` / `ChatResponse` 跟 loop 对话，而这套形状是
**Anthropic Messages API** 的镜像：

- `Message` 包含 `Role` 和一组 `ContentBlock`。
- 每个 `ContentBlock` 有 `Type`（`text` / `tool_use` / `tool_result`），
  只填和该 Type 相关的字段。
- `system` 是 request 的顶层字段，**不是** message。
- 工具 schema 放在 `tools[].input_schema`。

OpenAI 用的是更扁的形状：

- 系统提示是 `messages[]` 的第一项，`role: "system"`。
- 工具调用挂在 assistant 消息的 `tool_calls[]` 上，不是 content block。
- 工具结果是一条独立的 `role: "tool"` 消息，通过 `tool_call_id` 串联。
- 工具 schema 在 `tools[].function.parameters`。

Anthropic 的 block 模型严格地更富 —— OpenAI 的任意消息都能用 Anthropic
block 表达，反过来不行。所以 **loop 保持 Anthropic 形状，`OpenAIProvider`
在 wire 边界做翻译**。这样 s03..s14 不需要任何改动 —— 它们根本不知道世上
还有第二套 wire format。

## 8 个 provider profile

`main.go` 里写死了 9 个条目（`mock` 加 8 个远端 profile）。用 `-provider`
选一个、`-model` 覆盖默认模型、`-base-url` 覆盖默认端点。

| `-provider`  | endpoint                                                | 默认模型                    | 环境变量              |
|--------------|---------------------------------------------------------|-----------------------------|-----------------------|
| `mock`（默认） | （无 —— 回放 JSON 脚本）                              | `mock`                      | （无）                |
| `anthropic`  | `api.anthropic.com`                                     | `claude-sonnet-4-5`         | `ANTHROPIC_API_KEY`   |
| `openai`     | `api.openai.com/v1`                                     | `gpt-4o-mini`               | `OPENAI_API_KEY`      |
| `deepseek`   | `api.deepseek.com/v1`                                   | `deepseek-chat`             | `DEEPSEEK_API_KEY`    |
| `moonshot`   | `api.moonshot.cn/v1`                                    | `moonshot-v1-8k`            | `MOONSHOT_API_KEY`    |
| `qwen`       | `dashscope.aliyuncs.com/compatible-mode/v1`             | `qwen-plus`                 | `DASHSCOPE_API_KEY`   |
| `groq`       | `api.groq.com/openai/v1`                                | `llama-3.3-70b-versatile`   | `GROQ_API_KEY`        |
| `openrouter` | `openrouter.ai/api/v1`                                  | `openai/gpt-4o-mini`        | `OPENROUTER_API_KEY`  |
| `local`      | `http://localhost:8000/v1`（vLLM / SGLang 等）          | `local-model`               | `OPENAI_API_KEY`      |

加第十个 provider 只需要在 `providerProfiles` 加两行 —— 只要端点遵循
OpenAI 规范，`openai_provider.go` 一行都不用动。

## 翻译规则

`openai_provider.go` 做 6 件事，其余都是管道。

| 方向 | Anthropic 形状                                          | OpenAI 形状                                                                  |
|------|---------------------------------------------------------|------------------------------------------------------------------------------|
| 出（请求） | `req.System`（顶层字段）                              | `messages[0] = {role:"system", content:<System>}`                            |
| 出（请求） | `Message{Role:"user", Content:[text blocks]}`         | `{role:"user", content:"<拼好的 text>"}`                                     |
| 出（请求） | `Message{Role:"assistant", Content:[tool_use blocks]}`| `{role:"assistant", tool_calls:[{id, type:"function", function:{name, arguments:"<JSON 字符串>"}}]}` |
| 出（请求） | `tool_result` ContentBlock                            | `{role:"tool", tool_call_id, content:<result>}`（一个 block 一条消息）      |
| 出（请求） | `tools[].input_schema`                                | `tools[].function.parameters`                                                |
| 入（响应） | `stop_reason`                                         | `finish_reason`：`"stop"`→`"end_turn"`、`"tool_calls"`→`"tool_use"`、`"length"`→`"max_tokens"` |

OpenAI 的 `tool_call.function.arguments` 字段是一个 **JSON 字符串**，不是
JSON 对象 —— provider 在返回时会重新包装成 `json.RawMessage`，让 loop 看到
跟 Anthropic 一样的形状。

## 实战

Mock 模式（默认 —— 不需要 API key）：

```bash
cd agents/s02-llm-provider
go test -count=1 ./...
go run . "please echo hello s02"
# Done. The echo tool returned "echo: hello s02".
```

DeepSeek（便宜，中文调得很好，话痨）：

```bash
cd agents/s02-llm-provider
export DEEPSEEK_API_KEY=sk-...
go run . -provider deepseek "echo 工具刚刚返回了什么？帮我总结一下"
```

Qwen（DashScope 的 OpenAI 兼容端点）：

```bash
export DASHSCOPE_API_KEY=sk-...
go run . -provider qwen "用 echo 工具 echo 一下 hello qwen"
```

Moonshot/Kimi：

```bash
export MOONSHOT_API_KEY=sk-...
go run . -provider moonshot "先回 OK，然后用 echo 工具 echo kimi"
```

自建 vLLM（先起服务，再让 s02 指过去）：

```bash
# Terminal 1 —— vLLM（工具调用需要 --enable-auto-tool-choice）
vllm serve Qwen/Qwen2.5-7B-Instruct --enable-auto-tool-choice --tool-call-parser hermes

# Terminal 2 —— 让 s02 用本地服务
cd agents/s02-llm-provider
go run . -provider local -base-url http://localhost:8000/v1 -model Qwen/Qwen2.5-7B-Instruct "..."
```

任意 profile 配合 `-base-url` 和 `-model` 都能换底层端点：

```bash
export OPENROUTER_API_KEY=sk-...
go run . -provider openrouter -model anthropic/claude-3.5-sonnet "..."
```

## 接入其他章节

其他章节（s03..s14、`s_full`）默认用 `MockProvider`，让测试始终离线、
始终确定。`OpenAIProvider` **只存在于 s02** —— 各章之间不互相 import。

如果你想让某一章（比如 s07 重试章）打真实网络，怎么做：

1. 把 `openai_provider.go` 拷贝到目标章节；再把 `main.go` 里的
   `providerProfiles` 表和 `case "anthropic"` / `default:` 分支也拷过去。
2. 把那章的 `provider = NewMockProviderFromFile(...)` 改成同样的多 provider
   switch。

这是**有意为之的复制粘贴** —— s01 doc 立下的规矩（"每章独立 Go 模块，无跨
章 import"）优先级高于 DRY。如果你想把它们抽成共享包，fork 一份课程仓库
自己合并就好；上游教程为了可读性保持各章独立。

## 已知坑

各家厂商的小毛病，翻译层有的能兜底、有的只能提示。第一次跑多 provider 之前
最好背一下：

- **DeepSeek**：有时 `choices[0].message.content` 不是字符串而是
  `{type:"text", text:"..."}` 数组。`openai_provider.go` 里的
  `contentToString` 会自动拼接。
- **Qwen**：DashScope 端点的默认 `max_tokens` 偏小（~6K）。**一定显式传
  `MaxTokens`** —— provider 内部默认 4096，但长任务自己得调大。
- **Groq**：免费档 RPM 限制很严（~30/min）。要么用 s07 的重试层包起来，
  要么一分钟内就会 429。
- **OpenRouter**：很多托管模型根本不支持 tool use。发请求前先查模型页 ——
  "明明发了 tools 字段但模型回纯文本" 是最常见的首次失败。
- **自建 vLLM / SGLang**：tool use 需要 `--enable-auto-tool-choice` 加上
  对应模型族的 `--tool-call-parser`。少了它，模型会把 tool call 用 XML 形式
  写在文本里，而不是给你结构化的 `tool_calls` 数组。
- **Moonshot/Kimi**：不传 `max_tokens` 会直接拒服务。provider 的 4096 兜底
  默认值能挡住这个坑，但你别手动清零。
- **Anthropic vs OpenAI 并发工具调用**：两边都支持。我们的 loop 不管
  provider 是哪家，遇到 `tool_use` block 就一个个执行 —— 这一层对章节
  代码透明。

调试多 provider 问题：先把请求体抓出来（让 `prov.HTTPClient` 指向一个
`httptest.NewServer.Client()` 包装），跟已知能跑的 cURL 对比 diff。
90 % 的多 provider bug 是 wire format 对不上，不是逻辑 bug。
