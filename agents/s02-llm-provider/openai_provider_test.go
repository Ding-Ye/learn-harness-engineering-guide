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

// TestOpenAIProvider_RequestShape captures the outgoing HTTP body and asserts
// it matches the OpenAI Chat Completions wire format — in particular that
// req.System becomes a system MESSAGE, and that tools use "parameters"
// (NOT "input_schema" — that's Anthropic's name).
func TestOpenAIProvider_RequestShape(t *testing.T) {
	var capturedBody []byte
	var capturedAuth string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedAuth = r.Header.Get("Authorization")
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read request body: %v", err)
		}
		capturedBody = body

		w.Header().Set("content-type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{
			"id":"chatcmpl-test",
			"choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]
		}`))
	}))
	defer server.Close()

	prov := &OpenAIProvider{
		APIKey:     "test-key",
		BaseURL:    server.URL,
		Model:      "gpt-4o-mini",
		HTTPClient: server.Client(),
	}

	req := ChatRequest{
		Model:  "gpt-4o-mini",
		System: "be helpful",
		Messages: []Message{
			{Role: "user", Content: []ContentBlock{{Type: "text", Text: "hi"}}},
		},
		Tools: []ToolSchema{EchoTool{}.Schema()},
	}

	if _, err := prov.Chat(context.Background(), req); err != nil {
		t.Fatalf("Chat: %v", err)
	}

	if capturedAuth != "Bearer test-key" {
		t.Errorf("Authorization header = %q; want %q", capturedAuth, "Bearer test-key")
	}

	var body map[string]any
	if err := json.Unmarshal(capturedBody, &body); err != nil {
		t.Fatalf("parse captured body: %v (body=%s)", err, string(capturedBody))
	}

	for _, key := range []string{"model", "messages", "tools"} {
		if _, ok := body[key]; !ok {
			t.Errorf("captured body missing key %q (body=%s)", key, string(capturedBody))
		}
	}

	// OpenAI must NOT have a top-level "system" field — that's Anthropic.
	if _, ok := body["system"]; ok {
		t.Errorf("captured body should NOT have a top-level \"system\" field (body=%s)", string(capturedBody))
	}

	// messages[0] must be the system message extracted from req.System.
	messages, ok := body["messages"].([]any)
	if !ok || len(messages) == 0 {
		t.Fatalf("messages not a non-empty array: %v", body["messages"])
	}
	msg0, ok := messages[0].(map[string]any)
	if !ok {
		t.Fatalf("messages[0] not an object: %v", messages[0])
	}
	if role, _ := msg0["role"].(string); role != "system" {
		t.Errorf("messages[0].role = %q; want \"system\"", role)
	}
	if content, _ := msg0["content"].(string); content != "be helpful" {
		t.Errorf("messages[0].content = %q; want \"be helpful\"", content)
	}

	// tools[0].type must be "function", and tools[0].function.parameters must exist
	// (NOT input_schema — that's the Anthropic name).
	tools, ok := body["tools"].([]any)
	if !ok || len(tools) == 0 {
		t.Fatalf("tools not a non-empty array: %v", body["tools"])
	}
	tool0, ok := tools[0].(map[string]any)
	if !ok {
		t.Fatalf("tools[0] not an object: %v", tools[0])
	}
	if got, _ := tool0["type"].(string); got != "function" {
		t.Errorf("tools[0].type = %q; want \"function\"", got)
	}
	if _, ok := tool0["input_schema"]; ok {
		t.Errorf("tools[0] uses Anthropic-shape \"input_schema\" key at the top level; should be nested under function.parameters")
	}
	fn, ok := tool0["function"].(map[string]any)
	if !ok {
		t.Fatalf("tools[0].function not an object: %v", tool0["function"])
	}
	if _, ok := fn["parameters"]; !ok {
		t.Errorf("tools[0].function missing \"parameters\" key (body=%s)", string(capturedBody))
	}
	if _, ok := fn["input_schema"]; ok {
		t.Errorf("tools[0].function uses Anthropic-shape \"input_schema\"; should be \"parameters\"")
	}
}

// TestOpenAIProvider_ToolUseTranslation verifies that a Mock OpenAI response
// containing tool_calls becomes a ChatResponse with a tool_use ContentBlock
// and StopReason == "tool_use".
func TestOpenAIProvider_ToolUseTranslation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("content-type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{
			"id":"chatcmpl-tu",
			"choices":[{
				"index":0,
				"finish_reason":"tool_calls",
				"message":{
					"role":"assistant",
					"content":null,
					"tool_calls":[{
						"id":"call_abc",
						"type":"function",
						"function":{
							"name":"echo",
							"arguments":"{\"text\":\"hi\"}"
						}
					}]
				}
			}]
		}`))
	}))
	defer server.Close()

	prov := &OpenAIProvider{
		APIKey:     "test-key",
		BaseURL:    server.URL,
		Model:      "x",
		HTTPClient: server.Client(),
	}

	resp, err := prov.Chat(context.Background(), ChatRequest{Model: "x"})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}

	if resp.StopReason != "tool_use" {
		t.Errorf("StopReason = %q; want \"tool_use\"", resp.StopReason)
	}
	// One tool_use block (no text — content was null).
	if len(resp.Content) != 1 {
		t.Fatalf("Content len = %d; want 1 (block=%+v)", len(resp.Content), resp.Content)
	}
	b := resp.Content[0]
	if b.Type != "tool_use" {
		t.Errorf("Content[0].Type = %q; want \"tool_use\"", b.Type)
	}
	if b.ID != "call_abc" {
		t.Errorf("Content[0].ID = %q; want \"call_abc\"", b.ID)
	}
	if b.Name != "echo" {
		t.Errorf("Content[0].Name = %q; want \"echo\"", b.Name)
	}
	var input struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(b.Input, &input); err != nil {
		t.Fatalf("parse Input: %v (raw=%s)", err, string(b.Input))
	}
	if input.Text != "hi" {
		t.Errorf("Content[0].Input.text = %q; want \"hi\"", input.Text)
	}
}

// TestOpenAIProvider_PlainTextResponse — content+finish_reason="stop" maps to
// one text block + StopReason == "end_turn".
func TestOpenAIProvider_PlainTextResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("content-type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{
			"id":"chatcmpl-pt",
			"choices":[{
				"index":0,
				"finish_reason":"stop",
				"message":{"role":"assistant","content":"Hello, world!"}
			}]
		}`))
	}))
	defer server.Close()

	prov := &OpenAIProvider{
		APIKey:     "test-key",
		BaseURL:    server.URL,
		Model:      "x",
		HTTPClient: server.Client(),
	}

	resp, err := prov.Chat(context.Background(), ChatRequest{Model: "x"})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}

	if resp.StopReason != "end_turn" {
		t.Errorf("StopReason = %q; want \"end_turn\"", resp.StopReason)
	}
	if len(resp.Content) != 1 {
		t.Fatalf("Content len = %d; want 1 (blocks=%+v)", len(resp.Content), resp.Content)
	}
	if resp.Content[0].Type != "text" {
		t.Errorf("Content[0].Type = %q; want \"text\"", resp.Content[0].Type)
	}
	if resp.Content[0].Text != "Hello, world!" {
		t.Errorf("Content[0].Text = %q; want \"Hello, world!\"", resp.Content[0].Text)
	}
}

// TestOpenAIProvider_ToolResultRoundTrip — build a ChatRequest with a full
// user→assistant(tool_use)→user(tool_result) history; capture the outgoing
// body and assert the tool_result becomes a role:"tool" message with the
// matching tool_call_id and content.
func TestOpenAIProvider_ToolResultRoundTrip(t *testing.T) {
	var capturedBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read body: %v", err)
		}
		capturedBody = body
		w.Header().Set("content-type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{
			"id":"chatcmpl-rt",
			"choices":[{"index":0,"finish_reason":"stop","message":{"role":"assistant","content":"ack"}}]
		}`))
	}))
	defer server.Close()

	prov := &OpenAIProvider{
		APIKey:     "test-key",
		BaseURL:    server.URL,
		Model:      "x",
		HTTPClient: server.Client(),
	}

	// The Loop emits tool results as role:"tool" messages (see loop.go L90),
	// but a real client might also send role:"user" with tool_result content
	// blocks (Anthropic's preferred shape). We exercise the loop's shape here.
	req := ChatRequest{
		Model: "x",
		Messages: []Message{
			{Role: "user", Content: []ContentBlock{{Type: "text", Text: "echo hello"}}},
			{Role: "assistant", Content: []ContentBlock{
				{Type: "tool_use", ID: "call_xyz", Name: "echo", Input: json.RawMessage(`{"text":"hello"}`)},
			}},
			{Role: "tool", Content: []ContentBlock{
				{Type: "tool_result", ID: "call_xyz", Content: "echo: hello"},
			}},
		},
	}

	if _, err := prov.Chat(context.Background(), req); err != nil {
		t.Fatalf("Chat: %v", err)
	}

	var body map[string]any
	if err := json.Unmarshal(capturedBody, &body); err != nil {
		t.Fatalf("parse body: %v", err)
	}

	messages, _ := body["messages"].([]any)
	// Expect 3 messages: user, assistant(tool_call), tool(tool_call_id).
	// (No system message because req.System was empty.)
	if len(messages) != 3 {
		t.Fatalf("messages count = %d; want 3 (body=%s)", len(messages), string(capturedBody))
	}

	// messages[0] — user with the original prompt
	m0, _ := messages[0].(map[string]any)
	if role, _ := m0["role"].(string); role != "user" {
		t.Errorf("messages[0].role = %q; want \"user\"", role)
	}

	// messages[1] — assistant with tool_calls
	m1, _ := messages[1].(map[string]any)
	if role, _ := m1["role"].(string); role != "assistant" {
		t.Errorf("messages[1].role = %q; want \"assistant\"", role)
	}
	tcs, ok := m1["tool_calls"].([]any)
	if !ok || len(tcs) != 1 {
		t.Fatalf("messages[1].tool_calls = %v; want 1 entry", m1["tool_calls"])
	}
	tc0, _ := tcs[0].(map[string]any)
	if id, _ := tc0["id"].(string); id != "call_xyz" {
		t.Errorf("messages[1].tool_calls[0].id = %q; want \"call_xyz\"", id)
	}

	// messages[2] — tool message keyed by tool_call_id
	m2, _ := messages[2].(map[string]any)
	if role, _ := m2["role"].(string); role != "tool" {
		t.Errorf("messages[2].role = %q; want \"tool\" (body=%s)", role, string(capturedBody))
	}
	if id, _ := m2["tool_call_id"].(string); id != "call_xyz" {
		t.Errorf("messages[2].tool_call_id = %q; want \"call_xyz\"", id)
	}
	if content, _ := m2["content"].(string); content != "echo: hello" {
		t.Errorf("messages[2].content = %q; want \"echo: hello\"", content)
	}
}

// TestOpenAIProvider_SkipsWhenNoKey is the marker test that turns into a
// "skipped" line in CI when OPENAI_API_KEY is unset. With a key, it sends a
// real one-shot request to api.openai.com to validate the live path.
func TestOpenAIProvider_SkipsWhenNoKey(t *testing.T) {
	if os.Getenv("OPENAI_API_KEY") == "" {
		t.Skip("OPENAI_API_KEY is not set; skipping live OpenAI call")
	}

	prov := NewOpenAIProvider(os.Getenv("OPENAI_API_KEY"), "", "gpt-4o-mini")
	resp, err := prov.Chat(context.Background(), ChatRequest{
		Model:     "gpt-4o-mini",
		MaxTokens: 64,
		System:    "Respond with exactly the word OK.",
		Messages: []Message{
			{Role: "user", Content: []ContentBlock{{Type: "text", Text: "say OK"}}},
		},
	})
	if err != nil {
		t.Fatalf("live OpenAI call failed: %v", err)
	}
	if resp.StopReason == "" {
		t.Errorf("expected non-empty StopReason; got %+v", resp)
	}
	if len(resp.Content) == 0 {
		t.Errorf("expected non-empty Content; got %+v", resp)
	}
	// Guard against the suite silently passing on a wrong key — quick sanity:
	if !strings.Contains(strings.ToUpper(resp.Content[0].Text), "OK") {
		t.Logf("note: response did not contain OK as instructed: %q", resp.Content[0].Text)
	}
}
