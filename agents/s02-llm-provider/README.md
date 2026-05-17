# s02-llm-provider

> Hide the LLM behind one interface. Ship a deterministic Mock + a real Anthropic implementation.
> 把 LLM 藏到一个接口后面 —— 确定性 Mock + 真实 Anthropic 实现。

## Scope / 范围

Upgrade s01's hard-coded `MockProvider` into a clean `Provider` interface. Implement two providers — `MockProvider` (script-driven, offline) and `AnthropicProvider` (real HTTPS call to `api.anthropic.com/v1/messages`). Upgrade the message shape from `Message{Role, Content string}` to the canonical Anthropic-shaped `Message{Role string, Content []ContentBlock}`. All subsequent chapters inherit these types.

把 s01 中硬编码的 `MockProvider` 升级为干净的 `Provider` 接口。实现两个 provider —— `MockProvider`（脚本驱动、离线）和 `AnthropicProvider`（真实调用 `api.anthropic.com/v1/messages`）。把消息形状从 `Message{Role, Content string}` 升级为符合 Anthropic 协议的 `Message{Role string, Content []ContentBlock}`，后续所有章节继承这套类型。

## Files / 文件

```
types.go              Message / ContentBlock / ToolSchema / ChatRequest / ChatResponse
provider.go           Provider interface (alone, easy to grep from later chapters)
mock_provider.go      MockProvider; in-memory or load from testdata/*.json
anthropic_provider.go AnthropicProvider; real /v1/messages call
tool.go               Tool interface + EchoTool demo
loop.go               Loop.Run parameterized on Provider; canonical message shape
main.go               CLI: -provider=mock|anthropic
provider_test.go      6 tests including 1 t.Skip()-when-no-key marker
testdata/two_turn.json          mock script: tool_use turn + end_turn
testdata/anthropic_response.json captured-shape Anthropic response fixture
```

## Run / 运行

```bash
cd agents/s02-llm-provider

# Mock (no API key needed)
go run . -provider=mock "say hello s02"
# Done. The echo tool returned "echo: hello s02".

# Anthropic (needs ANTHROPIC_API_KEY)
export ANTHROPIC_API_KEY=sk-ant-...
go run . -provider=anthropic "say hello" 
```

## Test / 测试

```bash
go test -count=1 ./...
# ok   ... 6 tests (1 skipped if ANTHROPIC_API_KEY unset)
```

## Key teaching points / 教学要点

1. **One interface, two implementations.** The loop never knows whether it's talking to a script or a real model — that's the whole point of the boundary.
   一个接口，两个实现。Loop 不关心 provider 是脚本还是真实模型 —— 这就是抽象边界的全部价值。
2. **Canonical types are richer than what s01 used.** `[]ContentBlock` preserves "text + tool_use" parallelism in one assistant turn. Translating *down* to OpenAI is mechanical; translating *up* would lose information.
   规范类型比 s01 更丰富。`[]ContentBlock` 保留了一个 assistant turn 里"文本 + tool_use"的并存关系。向下转 OpenAI 是机械的，向上转会丢信息。
3. **Tool field is `input_schema`, NOT `parameters`.** OpenAI uses `parameters`. The first thing I broke when learning this is worth a test (see `TestAnthropicProvider_RequestShape`).
   工具字段是 `input_schema`，不是 `parameters`。OpenAI 用 `parameters`，新人 50% 会踩坑 —— 所以这条专门写测试。
4. **429 errors surface "rate limit" in the message.** s07 wraps `Provider.Chat` in retry logic and keys off this string.
   429 错误的 message 包含 "rate limit"。s07 的 retry 层就靠这个字符串识别可重试的错误。

## What the next chapter changes / 下一节的变化

s03 introduces a `Registry` of tools with `Register/Schemas/Dispatch` — pulls the inline `Tools map[string]Tool` of this chapter into its own component. Loop body unchanged; tool plumbing gets richer.

s03 把工具变成一个 `Registry`，提供 `Register/Schemas/Dispatch`。本章里以内嵌 `map[string]Tool` 形式存在的工具表，会独立成一个组件。Loop 本体不变，工具装配更完整。
