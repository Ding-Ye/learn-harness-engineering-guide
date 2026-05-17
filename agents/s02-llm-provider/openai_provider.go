package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// OpenAIProvider talks to ANY OpenAI-compatible Chat Completions endpoint:
// OpenAI itself, DeepSeek, Moonshot/Kimi, Qwen via DashScope's compatible
// endpoint, Groq, Together, OpenRouter, self-hosted vLLM / SGLang, etc.
//
// Why this file exists (Phase G): the rest of the chapter — the Loop, the
// Message/ContentBlock types, every test — is written against the Anthropic
// block model. We don't want to rewrite that for every new vendor; instead we
// translate at the boundary. This file is THE translation. Other chapters
// stay untouched.
//
// Wire shape sent to the upstream (per OpenAI Chat Completions spec):
//
//	POST <baseURL>/chat/completions
//	Authorization: Bearer <key>
//	Content-Type: application/json
//
//	{
//	  "model": "gpt-4o-mini",
//	  "messages": [
//	    {"role":"system","content":"..."},
//	    {"role":"user","content":"..."},
//	    {"role":"assistant","tool_calls":[{"id":"...","type":"function","function":{"name":"...","arguments":"{...}"}}]},
//	    {"role":"tool","tool_call_id":"...","content":"..."}
//	  ],
//	  "tools": [{"type":"function","function":{"name":"...","description":"...","parameters":{...}}}],
//	  "max_tokens": 4096
//	}
//
// Notice the field is "parameters" (NOT Anthropic's "input_schema") and that
// the tool result lives in its own role:"tool" message keyed by tool_call_id —
// not as a content block on the user message like Anthropic does.
type OpenAIProvider struct {
	APIKey     string        // required; loaded from <PROVIDER>_API_KEY by the caller
	BaseURL    string        // e.g. https://api.deepseek.com/v1 — no trailing slash
	Model      string        // default model when ChatRequest.Model is empty
	HTTPClient *http.Client  // override for tests; defaults to a 120s-timeout client
	Timeout    time.Duration // overall request timeout; defaults to 120s
}

// NewOpenAIProvider returns a provider configured for production use.
// If baseURL is "" we default to OpenAI's own endpoint. Trailing slashes are
// trimmed so callers can pass either form. The 120s timeout is more generous
// than the Anthropic provider's 60s because some self-hosted backends
// (vLLM with large context) routinely take 60-90s for the first token.
func NewOpenAIProvider(apiKey, baseURL, model string) *OpenAIProvider {
	if baseURL == "" {
		baseURL = "https://api.openai.com/v1"
	}
	return &OpenAIProvider{
		APIKey:     apiKey,
		BaseURL:    strings.TrimRight(baseURL, "/"),
		Model:      model,
		HTTPClient: &http.Client{Timeout: 120 * time.Second},
		Timeout:    120 * time.Second,
	}
}

// --- request shape ---

type openAIChatRequest struct {
	Model     string          `json:"model"`
	Messages  []openAIMessage `json:"messages"`
	Tools     []openAITool    `json:"tools,omitempty"`
	MaxTokens int             `json:"max_tokens,omitempty"`
}

type openAIMessage struct {
	Role       string           `json:"role"`              // "system" | "user" | "assistant" | "tool"
	Content    interface{}      `json:"content,omitempty"` // string OR omitted when tool_calls is present
	ToolCalls  []openAIToolCall `json:"tool_calls,omitempty"`
	ToolCallID string           `json:"tool_call_id,omitempty"` // only set when role=="tool"
}

type openAIToolCall struct {
	ID       string             `json:"id"`
	Type     string             `json:"type"` // always "function"
	Function openAIToolCallFunc `json:"function"`
}

type openAIToolCallFunc struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"` // JSON-encoded STRING (not an object)
}

type openAITool struct {
	Type     string        `json:"type"` // always "function"
	Function openAIToolDef `json:"function"`
}

type openAIToolDef struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters"` // OpenAI's name for what Anthropic calls input_schema
}

// --- response shape ---

type openAIChatResponse struct {
	ID      string             `json:"id"`
	Choices []openAIChatChoice `json:"choices"`
}

type openAIChatChoice struct {
	Index        int           `json:"index"`
	Message      openAIMessage `json:"message"`
	FinishReason string        `json:"finish_reason"`
}

type openAIErrorResponse struct {
	Error struct {
		Message string `json:"message"`
		Type    string `json:"type"`
		Code    string `json:"code"`
	} `json:"error"`
}

// Chat sends one request to <baseURL>/chat/completions, translates both
// directions, and returns a provider-agnostic ChatResponse.
func (o *OpenAIProvider) Chat(ctx context.Context, req ChatRequest) (*ChatResponse, error) {
	if o.APIKey == "" {
		return nil, fmt.Errorf("openai provider: missing API key")
	}

	body, err := o.buildRequestBody(req)
	if err != nil {
		return nil, fmt.Errorf("build request body: %w", err)
	}

	url := o.BaseURL + "/chat/completions"
	httpReq, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("build http request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+o.APIKey)

	resp, err := o.HTTPClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("http call: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response body: %w", err)
	}

	// 429 = rate limit. Surface the signal in the error string so a future
	// retry layer (s07) can detect it, mirroring the Anthropic provider.
	if resp.StatusCode == http.StatusTooManyRequests {
		return nil, fmt.Errorf("openai-compat rate limit (HTTP 429): %s", string(respBody))
	}

	if resp.StatusCode/100 != 2 {
		var apiErr openAIErrorResponse
		if json.Unmarshal(respBody, &apiErr) == nil && apiErr.Error.Message != "" {
			return nil, fmt.Errorf("openai-compat API error %d: %s", resp.StatusCode, apiErr.Error.Message)
		}
		return nil, fmt.Errorf("openai-compat API error %d: %s", resp.StatusCode, string(respBody))
	}

	var parsed openAIChatResponse
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return nil, fmt.Errorf("parse response: %w (body=%s)", err, string(respBody))
	}

	return translateResponseFromOpenAI(&parsed), nil
}

// buildRequestBody is exposed so tests can drive the translation in isolation
// without spinning up a Server. It converts the canonical ChatRequest into
// OpenAI's wire-format JSON and returns the bytes.
func (o *OpenAIProvider) buildRequestBody(req ChatRequest) ([]byte, error) {
	model := req.Model
	if model == "" {
		model = o.Model
	}
	maxTokens := req.MaxTokens
	if maxTokens == 0 {
		maxTokens = 4096
	}

	oaiReq := openAIChatRequest{
		Model:     model,
		MaxTokens: maxTokens,
	}

	// 1. The system prompt becomes the first message with role "system".
	//    OpenAI does NOT have a top-level "system" field like Anthropic does.
	if req.System != "" {
		oaiReq.Messages = append(oaiReq.Messages, openAIMessage{
			Role:    "system",
			Content: req.System,
		})
	}

	// 2. Each Anthropic-shape Message expands into 1+ OpenAI messages.
	for _, m := range req.Messages {
		oaiReq.Messages = append(oaiReq.Messages, anthropicMessageToOpenAI(m)...)
	}

	// 3. Tools: {name, description, schema} → {type:"function", function:{name, description, parameters}}.
	for _, t := range req.Tools {
		params := t.Schema
		if len(params) == 0 {
			params = json.RawMessage(`{"type":"object","properties":{}}`)
		}
		oaiReq.Tools = append(oaiReq.Tools, openAITool{
			Type: "function",
			Function: openAIToolDef{
				Name:        t.Name,
				Description: t.Description,
				Parameters:  params,
			},
		})
	}

	return json.Marshal(oaiReq)
}

// anthropicMessageToOpenAI splits ONE Anthropic-shape Message into the
// possibly-multiple OpenAI messages it represents.
//
// Cases:
//
//	user      + text          → one user message (string content)
//	user      + tool_result   → one role:"tool" message PER tool_result block
//	user      + mixed         → user message followed by N tool messages
//	assistant + text          → one assistant message (string content)
//	assistant + tool_use      → one assistant message with tool_calls
//	assistant + text+tool_use → one assistant message with both fields populated
//	tool      + tool_result   → role:"tool" message (loop emits this in s02)
//
// Both Anthropic-style "user" messages carrying tool_result content AND
// Anthropic-style "tool" messages (the shape our Loop currently emits, see
// loop.go L90) need to translate to OpenAI's role:"tool" format — so we handle
// them identically.
func anthropicMessageToOpenAI(m Message) []openAIMessage {
	var out []openAIMessage

	switch m.Role {
	case "user", "tool":
		var texts []string
		var tools []openAIMessage
		for _, b := range m.Content {
			switch b.Type {
			case "text":
				if b.Text != "" {
					texts = append(texts, b.Text)
				}
			case "tool_result":
				tools = append(tools, openAIMessage{
					Role:       "tool",
					ToolCallID: b.ID,
					Content:    b.Content,
				})
			}
		}
		if len(texts) > 0 {
			out = append(out, openAIMessage{Role: "user", Content: strings.Join(texts, "\n")})
		}
		out = append(out, tools...)

	case "assistant":
		var texts []string
		var calls []openAIToolCall
		for _, b := range m.Content {
			switch b.Type {
			case "text":
				if b.Text != "" {
					texts = append(texts, b.Text)
				}
			case "tool_use":
				// OpenAI requires Arguments to be a JSON STRING — even if the
				// content is structured JSON. We pass through the raw bytes if
				// the model gave us a valid object; otherwise fall back to "{}".
				args := string(b.Input)
				if args == "" {
					args = "{}"
				}
				calls = append(calls, openAIToolCall{
					ID:   b.ID,
					Type: "function",
					Function: openAIToolCallFunc{
						Name:      b.Name,
						Arguments: args,
					},
				})
			}
		}
		msg := openAIMessage{Role: "assistant"}
		if len(texts) > 0 {
			msg.Content = strings.Join(texts, "\n")
		}
		if len(calls) > 0 {
			msg.ToolCalls = calls
		}
		out = append(out, msg)
	}

	return out
}

// translateResponseFromOpenAI converts an OpenAI Chat Completions response
// back into our Anthropic-style ChatResponse.
//
// finish_reason mapping:
//
//	"stop"                  → "end_turn"
//	"tool_calls"            → "tool_use"
//	"function_call"         → "tool_use"  (legacy name some providers still emit)
//	"length"                → "max_tokens"
//	other / empty           → kept as-is so callers can log it
func translateResponseFromOpenAI(resp *openAIChatResponse) *ChatResponse {
	out := &ChatResponse{}
	if len(resp.Choices) == 0 {
		out.StopReason = "end_turn"
		return out
	}
	choice := resp.Choices[0]

	// Text content: most providers return a string. DeepSeek and some
	// Qwen modes return a list of {type:"text", text:"..."} blocks instead —
	// handle both via contentToString.
	if text, ok := contentToString(choice.Message.Content); ok && strings.TrimSpace(text) != "" {
		out.Content = append(out.Content, ContentBlock{Type: "text", Text: text})
	}

	for _, tc := range choice.Message.ToolCalls {
		// Arguments is a JSON-encoded string. If it parses, we store the raw
		// bytes as a RawMessage so the loop (and downstream tools) can decode
		// it with their own schemas. If it doesn't parse — a real-world quirk
		// with some smaller open models — we still preserve it so debugging
		// isn't blind.
		var input json.RawMessage
		if tc.Function.Arguments != "" && json.Valid([]byte(tc.Function.Arguments)) {
			input = json.RawMessage(tc.Function.Arguments)
		} else if tc.Function.Arguments != "" {
			// Wrap the raw text so callers can still see it.
			input = json.RawMessage(fmt.Sprintf(`{"_raw_arguments":%q}`, tc.Function.Arguments))
		} else {
			input = json.RawMessage(`{}`)
		}
		out.Content = append(out.Content, ContentBlock{
			Type:  "tool_use",
			ID:    tc.ID,
			Name:  tc.Function.Name,
			Input: input,
		})
	}

	switch choice.FinishReason {
	case "stop":
		out.StopReason = "end_turn"
	case "tool_calls", "function_call":
		out.StopReason = "tool_use"
	case "length":
		out.StopReason = "max_tokens"
	default:
		out.StopReason = choice.FinishReason
	}
	return out
}

// contentToString accepts the polymorphic shapes the various
// OpenAI-compatible servers return for choice.message.content:
//
//	string                            (canonical)
//	null / missing                    (when only tool_calls present)
//	[{"type":"text","text":"..."}]    (DeepSeek when content is structured)
func contentToString(v interface{}) (string, bool) {
	switch x := v.(type) {
	case string:
		return x, true
	case nil:
		return "", false
	case []interface{}:
		var parts []string
		for _, item := range x {
			m, ok := item.(map[string]interface{})
			if !ok {
				continue
			}
			if t, _ := m["type"].(string); t == "text" {
				if txt, ok := m["text"].(string); ok {
					parts = append(parts, txt)
				}
			}
		}
		if len(parts) == 0 {
			return "", false
		}
		return strings.Join(parts, ""), true
	}
	return "", false
}
