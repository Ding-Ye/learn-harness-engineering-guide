package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// AnthropicProvider talks to Anthropic's Messages API at /v1/messages.
//
// Why this file exists: s01's MockProvider proved the loop runs, but proves
// nothing about real LLM integration. This is the smallest piece of code that
// reaches a real model. It deliberately omits retries (s07), streaming
// (out of scope), prompt caching, vision blocks, and OpenTelemetry — keeping
// the focus on the wire-format translation.
//
// Wire shape sent to Anthropic (per https://docs.anthropic.com/api/messages):
//
//	POST https://api.anthropic.com/v1/messages
//	x-api-key: <key>
//	anthropic-version: 2023-06-01
//	content-type: application/json
//
//	{
//	  "model": "claude-sonnet-4-...",
//	  "max_tokens": 4096,
//	  "system": "...",
//	  "messages": [{"role":"user","content":[{"type":"text","text":"..."}]}],
//	  "tools": [{"name":"...", "description":"...", "input_schema": {...}}]
//	}
//
// Note the tool field is "input_schema" (NOT "parameters" — that's OpenAI's name).
type AnthropicProvider struct {
	APIKey     string        // required; usually loaded from ANTHROPIC_API_KEY
	BaseURL    string        // override for tests; defaults to https://api.anthropic.com
	HTTPClient *http.Client  // override for tests; defaults to a 60s-timeout client
	Version    string        // anthropic-version header; defaults to "2023-06-01"
	Timeout    time.Duration // overall request timeout; defaults to 60s
}

// NewAnthropicProvider returns a provider configured for production use.
// It picks up the API key from the ANTHROPIC_API_KEY env var (read by the caller).
func NewAnthropicProvider(apiKey string) *AnthropicProvider {
	return &AnthropicProvider{
		APIKey:     apiKey,
		BaseURL:    "https://api.anthropic.com",
		HTTPClient: &http.Client{Timeout: 60 * time.Second},
		Version:    "2023-06-01",
		Timeout:    60 * time.Second,
	}
}

// --- request shape ---

type anthropicRequest struct {
	Model     string             `json:"model"`
	MaxTokens int                `json:"max_tokens"`
	System    string             `json:"system,omitempty"`
	Messages  []anthropicMessage `json:"messages"`
	Tools     []anthropicTool    `json:"tools,omitempty"`
}

type anthropicMessage struct {
	Role    string         `json:"role"`
	Content []ContentBlock `json:"content"`
}

type anthropicTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"input_schema"`
}

// --- response shape ---

type anthropicResponse struct {
	ID         string         `json:"id"`
	Type       string         `json:"type"` // "message"
	Role       string         `json:"role"` // "assistant"
	Content    []ContentBlock `json:"content"`
	StopReason string         `json:"stop_reason"`
	Model      string         `json:"model"`
}

type anthropicErrorResponse struct {
	Type  string `json:"type"`
	Error struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error"`
}

// Chat sends one request to /v1/messages and parses the response into a
// provider-agnostic ChatResponse.
func (a *AnthropicProvider) Chat(ctx context.Context, req ChatRequest) (*ChatResponse, error) {
	if a.APIKey == "" {
		return nil, fmt.Errorf("anthropic provider: missing API key")
	}

	body, err := a.buildRequestBody(req)
	if err != nil {
		return nil, fmt.Errorf("build request body: %w", err)
	}

	url := a.BaseURL + "/v1/messages"
	httpReq, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("build http request: %w", err)
	}
	httpReq.Header.Set("x-api-key", a.APIKey)
	httpReq.Header.Set("anthropic-version", a.Version)
	httpReq.Header.Set("content-type", "application/json")

	resp, err := a.HTTPClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("http call: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response body: %w", err)
	}

	// 429 = rate limit. s07 will wrap this in retry; for now we surface the
	// signal in the error string so callers can detect it.
	if resp.StatusCode == http.StatusTooManyRequests {
		return nil, fmt.Errorf("anthropic rate limit (HTTP 429): %s", string(respBody))
	}

	if resp.StatusCode >= 400 {
		var apiErr anthropicErrorResponse
		if json.Unmarshal(respBody, &apiErr) == nil && apiErr.Error.Message != "" {
			return nil, fmt.Errorf("anthropic API error %d: %s", resp.StatusCode, apiErr.Error.Message)
		}
		return nil, fmt.Errorf("anthropic API error %d: %s", resp.StatusCode, string(respBody))
	}

	var parsed anthropicResponse
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return nil, fmt.Errorf("parse response: %w (body=%s)", err, string(respBody))
	}

	return &ChatResponse{
		Content:    parsed.Content,
		StopReason: parsed.StopReason,
	}, nil
}

// buildRequestBody is exposed for testing — it converts the canonical
// ChatRequest into Anthropic's wire-format JSON and returns the bytes.
func (a *AnthropicProvider) buildRequestBody(req ChatRequest) ([]byte, error) {
	msgs := make([]anthropicMessage, len(req.Messages))
	for i, m := range req.Messages {
		msgs[i] = anthropicMessage{Role: m.Role, Content: m.Content}
	}

	tools := make([]anthropicTool, len(req.Tools))
	for i, t := range req.Tools {
		// Use t.Schema verbatim if non-nil, else encode an empty object so the
		// API still accepts the tool. We pass the schema as json.RawMessage to
		// avoid re-encoding and to keep field ordering stable for tests.
		schema := t.Schema
		if len(schema) == 0 {
			schema = json.RawMessage(`{"type":"object","properties":{}}`)
		}
		tools[i] = anthropicTool{
			Name:        t.Name,
			Description: t.Description,
			InputSchema: schema,
		}
	}

	maxTokens := req.MaxTokens
	if maxTokens == 0 {
		maxTokens = 4096
	}

	body := anthropicRequest{
		Model:     req.Model,
		MaxTokens: maxTokens,
		System:    req.System,
		Messages:  msgs,
		Tools:     tools,
	}
	return json.Marshal(body)
}
