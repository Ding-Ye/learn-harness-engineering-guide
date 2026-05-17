# s02 — LLM Provider

> 把 LLM 藏到一个稳定的接口后面，让 loop 永远不知道自己在和脚本、Anthropic 还是 OpenAI 说话。顺手把消息形状升级成规范的 Anthropic 协议形态，后续所有章节都继承这套类型。

## 问题

s01 把 `MockProvider` 直接硬编码进了 loop，消息形状也用的是简化版（`Message{Role, Content string}` 加独立的 `ToolCalls` / `ToolCallID` 字段）。这套设计跑通了循环，但两个限制立刻浮现：

1. **没法换模型。** 上游 guide 专门有一节（`guide/your-first-harness.md` L209-L236）演示"harness 是 model-agnostic 的 —— 换成 Anthropic Claude，只需要换 client"。s01 做不到这一点 —— `MockProvider` 这个具体类型在 loop 里被直接引用。
2. **消息形状跟现实不匹配。** 真实的 Anthropic assistant 消息是 `content: []` —— 一个块的列表，里面有些块是 `text`、有些是 `tool_use`，而且常常**在同一个消息里并存**。s01 的简化形状（字符串 + 并行的 ToolCalls 切片）丢失了块之间的顺序，无法忠实建模 wire format。

我们要在这一章一次性把两个问题修掉，并建立一个稳定的边界 —— 后面的 s07（retry）、s09（压缩）、s14（分类器）都不需要再改这个接口。

## 解决方案

引入三件事：

**a) 一个 `Provider` 接口。** 一个方法：`Chat(ctx, ChatRequest) (*ChatResponse, error)`。Loop 持有一个 `Provider` 字段，永远不引用任何具体类型。

**b) 两个实现。**
- `MockProvider` —— 按顺序回放 `ChatResponse` 列表。既可以在内存中构造，也可以从 `testdata/` 的 JSON 文件加载。确定性。无网络。无 API key。所有测试和默认 CLI 模式都用它。
- `AnthropicProvider` —— 真实发起 `POST https://api.anthropic.com/v1/messages`，带 `x-api-key` 和 `anthropic-version: 2023-06-01` header。把 `ChatRequest` 序列化成 Anthropic 的精确 wire 形状（工具变成 `{name, description, input_schema}`，**不是** `parameters`）。响应解析成 `[]ContentBlock`。429 响应返回的 error message 包含 "rate limit"，让 s07 的 retry 层能识别可重试的失败。

**c) 规范类型。** `Message{Role, []ContentBlock}` 以及一个 `ContentBlock` 联合类型，`Type ∈ {text, tool_use, tool_result}`。这就是 Anthropic 在 wire 上的实际形状，向 OpenAI 的扁平形态翻译不会丢任何信息。

## 工作原理

Loop 本体跟 s01 在结构上一模一样 —— 同样的 for 循环、同样的"先 append 再分支"纪律、同样的 `MaxTurns` 兜底。变化在于流经 loop 的数据。

```
                     ┌──────────────────────────┐
                     │     ChatRequest          │
                     │   {Model, System,        │
                     │    Messages, Tools,      │
                     │    MaxTokens}            │
                     └────────────┬─────────────┘
                                  │
                ┌─────────────────┴─────────────────┐
                ▼                                   ▼
        ┌───────────────┐                  ┌─────────────────┐
        │ MockProvider  │                  │ Anthropic       │
        │ (内存/脚本)   │                  │ Provider        │
        │               │                  │ (HTTPS)         │
        └───────┬───────┘                  └────────┬────────┘
                │                                   │
                └─────────────────┬─────────────────┘
                                  ▼
                       ┌─────────────────────┐
                       │   ChatResponse      │
                       │  {[]ContentBlock,   │
                       │   StopReason}       │
                       └─────────────────────┘
```

Loop 单次迭代长这样：

```go
for turn := 0; turn < l.MaxTurns; turn++ {
    req := ChatRequest{
        Model: l.Model, System: l.System,
        Messages: messages, Tools: tools,
    }
    resp, err := l.Provider.Chat(ctx, req)
    if err != nil { return "", err }

    // 1. 追加 assistant 消息 —— Content 现在是 []ContentBlock。
    messages = append(messages, Message{Role: "assistant", Content: resp.Content})

    // 2. end_turn → 从 text 块里提取最终文本。
    if resp.StopReason == "end_turn" {
        return extractText(resp.Content), nil
    }

    // 3. tool_use → 执行本轮所有 tool_use 块。
    toolResults := make([]ContentBlock, 0)
    for _, block := range resp.Content {
        if block.Type != "tool_use" { continue }
        result, isErr := l.executeTool(ctx, block)
        toolResults = append(toolResults, ContentBlock{
            Type: "tool_result", ID: block.ID,
            Content: result, IsError: isErr,
        })
    }
    if len(toolResults) > 0 {
        messages = append(messages, Message{Role: "tool", Content: toolResults})
    }
}
```

四个要点：

1. **一个 assistant turn 可以同时包含多种类型的块。** 真实模型经常在一次响应里发"我先看看那个文件……"的推理文本 **加上** 一个 tool_use。我们遍历 `resp.Content`，按每个块的 `Type` 分派。s01 表达不了这种形态。
2. **工具结果在 `tool_result` 块里，不是独立字段。** 工具消息形如 `Message{Role:"tool", Content:[]ContentBlock{...tool_result blocks...}}`。当出现并行 tool call 时，一个工具消息可以装多个结果。
3. **Anthropic wire 字段是 `input_schema`。** OpenAI 叫 `parameters`。混淆这两个名字 = 5 分钟的迷惑性 debug。`TestAnthropicProvider_RequestShape` 把这个代价降到 0，专门断言这个 key。
4. **429 → error 含 "rate limit"。** 这一章不做重试（s07 才管），但信号留在了 error 里，等 s07 来取。

## 与上一节的变化

最大的 diff 是 `Message` 形状：

```diff
- // s01: 简化形状
- type Message struct {
-     Role       string
-     Content    string     // 纯文本
-     ToolCalls  []ToolCall // 仅 assistant turn
-     ToolCallID string     // 仅 tool turn
- }
- type ToolCall struct {
-     ID, Name string
-     Args     map[string]any
- }
- type ChatResponse struct {
-     Content    string
-     ToolCalls  []ToolCall
-     StopReason string
- }
- type Provider interface {
-     Chat(ctx context.Context, []Message) (*ChatResponse, error)
- }

+ // s02: 规范的 Anthropic 形状
+ type Message struct {
+     Role    string
+     Content []ContentBlock
+ }
+ type ContentBlock struct {
+     Type    string          // "text" | "tool_use" | "tool_result"
+     Text    string          // Type=="text"
+     ID      string          // tool_use id / tool_result 关联
+     Name    string          // Type=="tool_use"
+     Input   json.RawMessage // Type=="tool_use"
+     Content string          // Type=="tool_result"
+     IsError bool            // Type=="tool_result"
+ }
+ type ChatResponse struct {
+     Content    []ContentBlock
+     StopReason string
+ }
+ type Provider interface {
+     Chat(ctx context.Context, req ChatRequest) (*ChatResponse, error)
+ }
```

另外三处变化：

1. **`ChatRequest` 现在是一个结构体**，携带 `Model`、`System`、`Tools`、`MaxTokens`，不只是 `[]Message`。Loop 可以把配置传给 provider，不需要绕路。
2. **`Tool` 接口加了 `Schema()` 和 `Run(ctx, json.RawMessage)`**（之前是 `Run(map[string]any)`）。工具直接看 raw JSON，provider 可以原样透传不需要重新编码。
3. **`AnthropicProvider` 是课程里第一段真实 I/O 代码。** `httptest.Server` 让它在 CI 里完全安全。

## 动手试一试

```bash
cd agents/s02-llm-provider

go test -count=1 ./...
# ok  ...  6 个测试
# （没设 ANTHROPIC_API_KEY 时 TestAnthropicProvider_SkipsWhenNoKey 显示 PASS-skipped）

go run . -provider=mock "echo hello s02"
# Done. The echo tool returned "echo: hello s02".

# 可选：带真实 API key
export ANTHROPIC_API_KEY=sk-ant-...
go test -count=1 -run TestAnthropicProvider_SkipsWhenNoKey ./...
# （这次真的会跑一次活的请求）
```

用 mock provider 你会看到：
- Turn 0：provider 在一个 `Content` 切片里同时返回 `{text + tool_use}`。Loop 调度 `EchoTool.Run`，追加一个 `tool` 消息，里面是 `tool_result` 块。
- Turn 1：provider 返回一个 `text` 块，`StopReason="end_turn"`。Loop 提取文本返回。

如果换成 Anthropic provider，给同样的提示：loop 行为完全一样，文本来自真实 Claude，tool_use 块带真实 `toolu_*` ID。同样的 loop、同样的工具、不同的模型 —— 正是上游 guide L236 承诺的样子。

## 上游源码阅读

`guide/your-first-harness.md` L209-L236 的 Anthropic 一侧节选是本章的概念种子。Guide 演示了 provider 切换就是一次机械翻译 —— 同样的 loop、同样的工具，只是换了 client。

```python
# Source: guide/your-first-harness.md L209-L236
## Swapping Models

The harness is model-agnostic. Switch to Anthropic's Claude by changing the client:

from anthropic import Anthropic

client = Anthropic()

response = client.messages.create(
    model="claude-sonnet-4-20250514",
    max_tokens=4096,
    system=SYSTEM,
    messages=messages,
    tools=[{
        "name": t["function"]["name"],
        "description": t["function"]["description"],
        "input_schema": t["function"]["parameters"]
    } for t in TOOLS]
)

# Parse tool calls from response.content blocks
for block in response.content:
    if block.type == "tool_use":
        result = execute_tool(block.name, block.input)

Same loop. Same tools. Different model.
```

阅读笔记：

- **Python 上游的翻译就是调用点的一个推导式。** OpenAI client 返回 `tool_calls`（扁平），Anthropic client 返回 `content[]`（含 `tool_use` 块，结构化）。223-227 行那段推导式把 OpenAI 形态的工具定义映射成 Anthropic 形态（注意：`t["function"]["parameters"]` → `input_schema`）。Go 版把这个翻译内化了 —— `ChatRequest.Tools` 是 provider-agnostic 的，每个 provider 的请求体构造器在一个地方做翻译。
- **`response.content` 是列表，不是字符串。** 这是跟 OpenAI API 最关键的形状差异。Guide 的 `for block in response.content` 跟 Go 里 `for _, block := range resp.Content` 一一对应。
- **Anthropic 要求 `max_tokens`。** OpenAI 可以省略默认，Anthropic 不行。我们在 `buildRequestBody` 里当 `req.MaxTokens == 0` 时默认 4096。
- **本章故意省略的东西。** 流式（`messages.stream`）、prompt 缓存（块上的 `cache_control`）、视觉块（`image` content type）、extended thinking（`thinking` 参数）。所有这些都能放进现有接口；但讲清楚边界并不需要它们。
- **延伸阅读。** `guide/your-first-harness.md` L91-L120（s01 移植的 OpenAI loop）；`guide/agentic-loop.md` L41-L65（通用 loop 抽象 —— 本身就是 provider-agnostic 的设计）；`guide/error-handling.md` L62-L122（retry/backoff，s07 接手）。

上游 permalink：[guide/your-first-harness.md @ 86fec9b](https://github.com/nexu-io/harness-engineering-guide/blob/86fec9bea430cecb29ff10afaae36b96496a8f8e/guide/your-first-harness.md#L209-L236)
