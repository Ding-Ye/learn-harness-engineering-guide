package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// TestMockProvider_ScriptReplay loads testdata/two_turn.json, hands the
// provider to a Loop, and asserts the loop drives both turns to completion.
func TestMockProvider_ScriptReplay(t *testing.T) {
	mp, err := NewMockProviderFromFile("testdata/two_turn.json")
	if err != nil {
		t.Fatalf("load script: %v", err)
	}

	loop := &Loop{
		Provider: mp,
		Tools:    map[string]Tool{"echo": EchoTool{}},
		MaxTurns: 5,
		Model:    "test-model",
		System:   "you are a test",
	}

	final, err := loop.Run(context.Background(), "please echo hello s02")
	if err != nil {
		t.Fatalf("loop.Run: %v", err)
	}
	if !strings.Contains(final, "echo: hello s02") {
		t.Errorf("final text missing echo substring; got %q", final)
	}
	if mp.Calls() != 2 {
		t.Errorf("expected 2 provider calls; got %d", mp.Calls())
	}
}

// TestAnthropicProvider_RequestShape uses httptest to capture the outgoing
// HTTP body and verifies it has the canonical Anthropic-shape keys.
// Critically: tools[].input_schema (NOT parameters — that's OpenAI's name).
func TestAnthropicProvider_RequestShape(t *testing.T) {
	var capturedBody []byte
	var capturedHeaders http.Header

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedHeaders = r.Header.Clone()
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read request body: %v", err)
		}
		capturedBody = body

		// Return a minimal valid response so the call succeeds.
		w.Header().Set("content-type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{
			"id":"msg_test","type":"message","role":"assistant",
			"content":[{"type":"text","text":"ok"}],
			"stop_reason":"end_turn","model":"test"
		}`))
	}))
	defer server.Close()

	prov := &AnthropicProvider{
		APIKey:     "test-key",
		BaseURL:    server.URL,
		HTTPClient: server.Client(),
		Version:    "2023-06-01",
	}

	req := ChatRequest{
		Model:  "claude-test",
		System: "be helpful",
		Messages: []Message{
			{Role: "user", Content: []ContentBlock{{Type: "text", Text: "hi"}}},
		},
		Tools: []ToolSchema{EchoTool{}.Schema()},
	}

	if _, err := prov.Chat(context.Background(), req); err != nil {
		t.Fatalf("Chat: %v", err)
	}

	// Headers
	if got := capturedHeaders.Get("x-api-key"); got != "test-key" {
		t.Errorf("x-api-key header = %q; want %q", got, "test-key")
	}
	if got := capturedHeaders.Get("anthropic-version"); got != "2023-06-01" {
		t.Errorf("anthropic-version header = %q; want %q", got, "2023-06-01")
	}

	// Body shape
	var body map[string]any
	if err := json.Unmarshal(capturedBody, &body); err != nil {
		t.Fatalf("parse captured body: %v (body=%s)", err, string(capturedBody))
	}

	for _, key := range []string{"model", "system", "messages", "tools", "max_tokens"} {
		if _, ok := body[key]; !ok {
			t.Errorf("captured body missing key %q (body=%s)", key, string(capturedBody))
		}
	}

	// tools[0] must use "input_schema", NOT "parameters"
	tools, ok := body["tools"].([]any)
	if !ok || len(tools) == 0 {
		t.Fatalf("captured body tools not a non-empty array: %v", body["tools"])
	}
	tool0, ok := tools[0].(map[string]any)
	if !ok {
		t.Fatalf("tools[0] not an object: %v", tools[0])
	}
	if _, ok := tool0["input_schema"]; !ok {
		t.Errorf("tools[0] missing input_schema (body=%s)", string(capturedBody))
	}
	if _, ok := tool0["parameters"]; ok {
		t.Errorf("tools[0] uses OpenAI-shape \"parameters\" key; should be \"input_schema\"")
	}
	if got, want := tool0["name"], "echo"; got != want {
		t.Errorf("tools[0].name = %v; want %v", got, want)
	}
}

// TestAnthropicProvider_ResponseParsing feeds the fixture in
// testdata/anthropic_response.json through Chat (via httptest) and asserts the
// resulting ChatResponse has the expected text + tool_use blocks.
func TestAnthropicProvider_ResponseParsing(t *testing.T) {
	fixture, err := os.ReadFile("testdata/anthropic_response.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("content-type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(fixture)
	}))
	defer server.Close()

	prov := &AnthropicProvider{
		APIKey:     "test-key",
		BaseURL:    server.URL,
		HTTPClient: server.Client(),
		Version:    "2023-06-01",
	}

	resp, err := prov.Chat(context.Background(), ChatRequest{Model: "x"})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}

	if resp.StopReason != "tool_use" {
		t.Errorf("StopReason = %q; want %q", resp.StopReason, "tool_use")
	}
	if len(resp.Content) != 2 {
		t.Fatalf("Content len = %d; want 2", len(resp.Content))
	}

	if resp.Content[0].Type != "text" || !strings.Contains(resp.Content[0].Text, "echo that for you") {
		t.Errorf("Content[0] = %+v; want text block", resp.Content[0])
	}

	tu := resp.Content[1]
	if tu.Type != "tool_use" {
		t.Errorf("Content[1].Type = %q; want tool_use", tu.Type)
	}
	if tu.ID != "toolu_01XYZ" {
		t.Errorf("Content[1].ID = %q; want %q", tu.ID, "toolu_01XYZ")
	}
	if tu.Name != "echo" {
		t.Errorf("Content[1].Name = %q; want %q", tu.Name, "echo")
	}
	var input struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(tu.Input, &input); err != nil {
		t.Fatalf("parse tool_use input: %v", err)
	}
	if input.Text != "from anthropic" {
		t.Errorf("Content[1].Input.text = %q; want %q", input.Text, "from anthropic")
	}
}

// TestAnthropicProvider_RateLimit asserts that 429 responses produce an error
// whose message contains "rate limit" — s07's retry layer keys off this string.
func TestAnthropicProvider_RateLimit(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"type":"error","error":{"type":"rate_limit_error","message":"slow down"}}`))
	}))
	defer server.Close()

	prov := &AnthropicProvider{
		APIKey:     "test-key",
		BaseURL:    server.URL,
		HTTPClient: server.Client(),
		Version:    "2023-06-01",
	}

	_, err := prov.Chat(context.Background(), ChatRequest{Model: "x"})
	if err == nil {
		t.Fatal("expected error on 429; got nil")
	}
	if !strings.Contains(err.Error(), "rate limit") {
		t.Errorf("error message should contain \"rate limit\"; got %v", err)
	}
}

// TestProvider_AcrossLoop wires a MockProvider into Loop and exercises the
// full round-trip with one tool call. This is the integration test that
// proves the canonical types let the loop work end-to-end.
func TestProvider_AcrossLoop(t *testing.T) {
	mock := &MockProvider{
		Responses: []ChatResponse{
			{
				Content: []ContentBlock{
					{Type: "tool_use", ID: "c1", Name: "echo", Input: json.RawMessage(`{"text":"loop"}`)},
				},
				StopReason: "tool_use",
			},
			{
				Content:    []ContentBlock{{Type: "text", Text: "Task complete."}},
				StopReason: "end_turn",
			},
		},
	}

	loop := &Loop{
		Provider: mock,
		Tools:    map[string]Tool{"echo": EchoTool{}},
		MaxTurns: 5,
		Model:    "test-model",
		System:   "test",
	}

	final, err := loop.Run(context.Background(), "echo loop")
	if err != nil {
		t.Fatalf("loop.Run: %v", err)
	}
	if final != "Task complete." {
		t.Errorf("final = %q; want %q", final, "Task complete.")
	}
	if mock.Calls() != 2 {
		t.Errorf("expected 2 provider calls; got %d", mock.Calls())
	}
}

// TestAnthropicProvider_SkipsWhenNoKey is the marker test that CI shows as
// "skipped" when ANTHROPIC_API_KEY is unset. It exists so the test suite has
// at least one test referencing real network behavior, and so a developer
// can run `go test` with a key set to validate the live wire path.
func TestAnthropicProvider_SkipsWhenNoKey(t *testing.T) {
	if os.Getenv("ANTHROPIC_API_KEY") == "" {
		t.Skip("ANTHROPIC_API_KEY is not set; skipping live Anthropic call")
	}

	prov := NewAnthropicProvider(os.Getenv("ANTHROPIC_API_KEY"))
	resp, err := prov.Chat(context.Background(), ChatRequest{
		Model:     "claude-sonnet-4-20250514",
		MaxTokens: 64,
		System:    "Respond with exactly the word OK.",
		Messages: []Message{
			{Role: "user", Content: []ContentBlock{{Type: "text", Text: "say OK"}}},
		},
	})
	if err != nil {
		t.Fatalf("live Anthropic call failed: %v", err)
	}
	if resp.StopReason == "" {
		t.Errorf("expected non-empty StopReason; got %+v", resp)
	}
	if len(resp.Content) == 0 {
		t.Errorf("expected non-empty Content; got %+v", resp)
	}
}
