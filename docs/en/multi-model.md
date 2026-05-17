# Multi-model integration

> Plug DeepSeek, Qwen, Moonshot/Kimi, Groq, OpenRouter, or any self-hosted
> vLLM/SGLang server into the same agentic loop — without touching the loop.

s02 ships two `Provider` implementations: `MockProvider` (for offline tests)
and `AnthropicProvider` (real HTTPS to `api.anthropic.com`). Phase G adds a
third — `OpenAIProvider` — which speaks the **OpenAI Chat Completions** wire
format. That wire format has become the de-facto standard: most non-Anthropic
vendors expose an OpenAI-compatible endpoint, so one translation layer
unlocks the whole ecosystem.

## Why two wire formats

Read `agents/s02-llm-provider/types.go` first. Every chapter that follows s02
talks to the loop through the canonical `ChatRequest` / `ChatResponse` shape,
which mirrors **Anthropic's Messages API**:

- `Message` carries `Role` and a slice of `ContentBlock`.
- A `ContentBlock` has a `Type` (`text` / `tool_use` / `tool_result`) and
  only the fields relevant to that type are populated.
- `system` is a top-level field on the request, NOT a message.
- Tool schemas live under `tools[].input_schema`.

OpenAI uses a flatter shape:

- The system prompt is the first `messages[]` entry with `role: "system"`.
- A tool call lives on the assistant message in `tool_calls[]`, not as a
  content block.
- A tool result lives in its own `role: "tool"` message keyed by
  `tool_call_id`.
- Tool schemas live under `tools[].function.parameters`.

Anthropic's block model is strictly richer — every OpenAI message can be
expressed as Anthropic blocks, but not the other way around. So the loop
stays Anthropic-shaped and `OpenAIProvider` translates at the wire boundary.
That keeps s03..s14 untouched: they never learn there is a second wire
format in existence.

## The 8 provider profiles

`main.go` hardcodes nine entries (`mock` plus eight remote profiles). Pick
one with `-provider`, override the model with `-model`, override the
endpoint with `-base-url`.

| `-provider`  | endpoint                                                | default model               | env var               |
|--------------|---------------------------------------------------------|-----------------------------|-----------------------|
| `mock` (default) | (none — replays JSON script)                        | `mock`                      | (none)                |
| `anthropic`  | `api.anthropic.com`                                     | `claude-sonnet-4-5`         | `ANTHROPIC_API_KEY`   |
| `openai`     | `api.openai.com/v1`                                     | `gpt-4o-mini`               | `OPENAI_API_KEY`      |
| `deepseek`   | `api.deepseek.com/v1`                                   | `deepseek-chat`             | `DEEPSEEK_API_KEY`    |
| `moonshot`   | `api.moonshot.cn/v1`                                    | `moonshot-v1-8k`            | `MOONSHOT_API_KEY`    |
| `qwen`       | `dashscope.aliyuncs.com/compatible-mode/v1`             | `qwen-plus`                 | `DASHSCOPE_API_KEY`   |
| `groq`       | `api.groq.com/openai/v1`                                | `llama-3.3-70b-versatile`   | `GROQ_API_KEY`        |
| `openrouter` | `openrouter.ai/api/v1`                                  | `openai/gpt-4o-mini`        | `OPENROUTER_API_KEY`  |
| `local`      | `http://localhost:8000/v1` (e.g. vLLM / SGLang)         | `local-model`               | `OPENAI_API_KEY`      |

Adding a tenth provider is two lines in `providerProfiles` — no code change
in `openai_provider.go` is required as long as the endpoint follows the
OpenAI spec.

## Translation rules

`openai_provider.go` does six conversions; the rest is plumbing.

| Direction | Anthropic shape                                         | OpenAI shape                                                                |
|-----------|---------------------------------------------------------|-----------------------------------------------------------------------------|
| Out (req) | `req.System` (top-level)                                | `messages[0] = {role:"system", content:<System>}`                           |
| Out (req) | `Message{Role:"user", Content:[text blocks]}`           | `{role:"user", content:"<joined text>"}`                                    |
| Out (req) | `Message{Role:"assistant", Content:[tool_use blocks]}`  | `{role:"assistant", tool_calls:[{id, type:"function", function:{name, arguments:"<JSON-string>"}}]}` |
| Out (req) | `tool_result` ContentBlock                              | `{role:"tool", tool_call_id, content:<result>}` (one message per block)     |
| Out (req) | `tools[].input_schema`                                  | `tools[].function.parameters`                                               |
| In (resp) | `stop_reason`                                           | `finish_reason`: `"stop"`→`"end_turn"`, `"tool_calls"`→`"tool_use"`, `"length"`→`"max_tokens"` |

The `Arguments` field on an OpenAI `tool_call` is a JSON-encoded **string**,
not a JSON object — the provider rewraps it as a `json.RawMessage` on the way
back so the loop sees the same shape it would from Anthropic.

## Hands-on

Mock mode (default — no API key needed):

```bash
cd agents/s02-llm-provider
go test -count=1 ./...
go run . "please echo hello s02"
# Done. The echo tool returned "echo: hello s02".
```

DeepSeek (cheap; very chatty Chinese-tuned model):

```bash
cd agents/s02-llm-provider
export DEEPSEEK_API_KEY=sk-...
go run . -provider deepseek "summarize what the echo tool just returned"
```

Qwen (DashScope's OpenAI-compatible endpoint):

```bash
export DASHSCOPE_API_KEY=sk-...
go run . -provider qwen "Use the echo tool to echo \"hello qwen\""
```

Moonshot/Kimi:

```bash
export MOONSHOT_API_KEY=sk-...
go run . -provider moonshot "say OK then call the echo tool with \"kimi\""
```

Self-hosted vLLM (start a server, then point at it):

```bash
# Terminal 1 — vLLM (tool use needs --enable-auto-tool-choice)
vllm serve Qwen/Qwen2.5-7B-Instruct --enable-auto-tool-choice --tool-call-parser hermes

# Terminal 2 — drive it from s02
cd agents/s02-llm-provider
go run . -provider local -base-url http://localhost:8000/v1 -model Qwen/Qwen2.5-7B-Instruct "..."
```

Custom endpoint with any provider profile — `-base-url` and `-model`
override the profile defaults:

```bash
export OPENROUTER_API_KEY=sk-...
go run . -provider openrouter -model anthropic/claude-3.5-sonnet "..."
```

## Wiring it into other chapters

Other chapters (s03..s14, `s_full`) use `MockProvider` so their tests stay
offline-deterministic. `OpenAIProvider` lives in s02 only — no cross-chapter
import.

To turn on real-network calls in, say, s07 (the retry chapter):

1. Copy `openai_provider.go` (and the `providerProfiles` block + the
   `case "anthropic"` / `default:` arms from `main.go`) into the target
   chapter.
2. Replace that chapter's `provider = NewMockProviderFromFile(...)` line
   with the same multi-provider switch.

This is intentional duplication — the curriculum's rule from the s01 doc
("each chapter is its own Go module, no cross-chapter imports") wins over
DRY. If you want one shared package, fork the curriculum and consolidate;
the upstream tutorial keeps things split for teachability.

## Known quirks

A handful of vendor-specific edges the translation layer either handles or
flags. Worth memorising before debugging your first multi-provider failure:

- **DeepSeek**: occasionally returns `choices[0].message.content` as an
  array of `{type:"text", text:"..."}` blocks instead of a plain string.
  `contentToString` in `openai_provider.go` concatenates them.
- **Qwen**: the DashScope endpoint's default `max_tokens` is low (~6K).
  Always pass `MaxTokens` explicitly — the provider does (4096 fallback)
  but a long-running task will want more.
- **Groq**: free-tier RPM limits are aggressive (often ~30/min). Wrap calls
  in s07's retry layer or you'll see 429s within a minute.
- **OpenRouter**: many of its hosted models don't implement tool use at all.
  Check the model page before sending tools — silent fallthrough to
  text-only is a frequent first failure.
- **Self-hosted vLLM / SGLang**: tool use requires
  `--enable-auto-tool-choice` plus the right `--tool-call-parser` for your
  model family. Without it, the model will emit tool-call XML inside the
  text block instead of a structured `tool_calls` array.
- **Moonshot/Kimi**: refuses requests without an explicit `max_tokens`.
  The provider's 4096 default keeps this from biting; just be aware if you
  zero it out.
- **Anthropic vs OpenAI parallel tool calls**: both support them, but the
  shape differs slightly — our loop iterates `tool_use` blocks regardless
  of provider, so this is invisible to chapter code.

When something breaks: log the captured request body
(`prov.HTTPClient = ...wrap with httptest.NewServer.Client()`) and diff
against a known-good cURL. 90 % of multi-provider bugs are wire-format
disagreements, not logic bugs.
