package main

import (
	"context"
	"strings"
	"testing"
)

func TestLoopTerminatesOnTextOnly(t *testing.T) {
	mock := &MockProvider{
		Responses: []ChatResponse{
			{Content: "hello", StopReason: "end_turn"},
		},
	}
	loop := &Loop{
		Provider: mock,
		Tools:    map[string]Tool{"echo": EchoTool{}},
		MaxTurns: 5,
	}

	got, err := loop.Run(context.Background(), "hi")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "hello" {
		t.Errorf("got %q, want %q", got, "hello")
	}
	if mock.Calls() != 1 {
		t.Errorf("expected 1 provider call, got %d", mock.Calls())
	}
}

func TestLoopDispatchesToolCallAndAppendsResult(t *testing.T) {
	// Use a wrapping recorder so we can inspect what messages were sent on
	// the *second* call — they must include the tool result the model saw.
	recorder := &recordingProvider{
		scripted: []ChatResponse{
			{
				StopReason: "tool_use",
				ToolCalls: []ToolCall{{
					ID:   "call_1",
					Name: "echo",
					Args: map[string]any{"text": "world"},
				}},
			},
			{Content: "done", StopReason: "end_turn"},
		},
	}
	loop := &Loop{
		Provider: recorder,
		Tools:    map[string]Tool{"echo": EchoTool{}},
		MaxTurns: 5,
	}

	got, err := loop.Run(context.Background(), "say world")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "done" {
		t.Errorf("got %q, want %q", got, "done")
	}

	// Second provider call should see: system, user, assistant(tool_use), tool(result).
	second := recorder.sent[1]
	if len(second) != 4 {
		t.Fatalf("second call: expected 4 messages, got %d (%+v)", len(second), second)
	}
	if second[2].Role != "assistant" || len(second[2].ToolCalls) != 1 {
		t.Errorf("expected assistant turn with one tool call at index 2, got %+v", second[2])
	}
	if second[3].Role != "tool" || second[3].ToolCallID != "call_1" || !strings.Contains(second[3].Content, "echo: world") {
		t.Errorf("expected tool result for call_1 at index 3, got %+v", second[3])
	}
}

func TestLoopRespectsMaxTurns(t *testing.T) {
	// Always returns tool_use — should hit max turns and surface error.
	mock := &MockProvider{
		Responses: []ChatResponse{
			{StopReason: "tool_use", ToolCalls: []ToolCall{{ID: "1", Name: "echo", Args: map[string]any{"text": "a"}}}},
			{StopReason: "tool_use", ToolCalls: []ToolCall{{ID: "2", Name: "echo", Args: map[string]any{"text": "b"}}}},
			{StopReason: "tool_use", ToolCalls: []ToolCall{{ID: "3", Name: "echo", Args: map[string]any{"text": "c"}}}},
		},
	}
	loop := &Loop{
		Provider: mock,
		Tools:    map[string]Tool{"echo": EchoTool{}},
		MaxTurns: 3,
	}

	_, err := loop.Run(context.Background(), "loop forever")
	if err == nil {
		t.Fatal("expected error after max turns; got nil")
	}
	if !strings.Contains(err.Error(), "max turns reached") {
		t.Errorf("expected error mentioning max turns; got %v", err)
	}
}

func TestParallelToolCallsAllExecuted(t *testing.T) {
	mock := &MockProvider{
		Responses: []ChatResponse{
			{
				StopReason: "tool_use",
				ToolCalls: []ToolCall{
					{ID: "p1", Name: "echo", Args: map[string]any{"text": "alpha"}},
					{ID: "p2", Name: "echo", Args: map[string]any{"text": "beta"}},
				},
			},
			{Content: "got both", StopReason: "end_turn"},
		},
	}

	recorded := &recordingProvider{scripted: mock.Responses}
	loop := &Loop{
		Provider: recorded,
		Tools:    map[string]Tool{"echo": EchoTool{}},
		MaxTurns: 5,
	}

	got, err := loop.Run(context.Background(), "do both")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "got both" {
		t.Errorf("got %q, want %q", got, "got both")
	}

	// History fed to the second call should contain both tool messages.
	second := recorded.sent[1]
	toolMsgs := 0
	for _, m := range second {
		if m.Role == "tool" {
			toolMsgs++
		}
	}
	if toolMsgs != 2 {
		t.Errorf("expected 2 tool messages in history, got %d", toolMsgs)
	}
}

func TestUnknownToolReturnsErrorString(t *testing.T) {
	mock := &MockProvider{
		Responses: []ChatResponse{
			{
				StopReason: "tool_use",
				ToolCalls:  []ToolCall{{ID: "x", Name: "nope", Args: map[string]any{}}},
			},
			{Content: "ok", StopReason: "end_turn"},
		},
	}
	recorder := &recordingProvider{scripted: mock.Responses}
	loop := &Loop{
		Provider: recorder,
		Tools:    map[string]Tool{"echo": EchoTool{}},
		MaxTurns: 3,
	}

	_, err := loop.Run(context.Background(), "use missing tool")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// The second call must contain a tool message whose content reports the unknown tool.
	second := recorder.sent[1]
	if len(second) < 4 {
		t.Fatalf("expected at least 4 messages on second call, got %d", len(second))
	}
	last := second[3]
	if last.Role != "tool" || !strings.Contains(last.Content, "unknown tool") {
		t.Errorf("expected tool error message, got %+v", last)
	}
}

// recordingProvider wraps a scripted response slice and captures every message
// slice passed into Chat — used by tests to inspect history evolution.
type recordingProvider struct {
	scripted []ChatResponse
	calls    int
	sent     [][]Message
}

func (r *recordingProvider) Chat(_ context.Context, messages []Message) (*ChatResponse, error) {
	snapshot := make([]Message, len(messages))
	copy(snapshot, messages)
	r.sent = append(r.sent, snapshot)
	resp := r.scripted[r.calls]
	r.calls++
	return &resp, nil
}
