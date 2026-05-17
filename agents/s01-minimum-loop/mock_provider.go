package main

import (
	"context"
	"fmt"
)

// MockProvider returns scripted ChatResponses in order. It exists so the loop
// has a deterministic counterpart for tests and demos — no network, no API key,
// no LLM. s02 replaces this with a real Provider implementation.
//
// Usage:
//
//	p := &MockProvider{Responses: []ChatResponse{
//	    {ToolCalls: []ToolCall{{ID: "1", Name: "echo", Args: map[string]any{"text": "hi"}}}, StopReason: "tool_use"},
//	    {Content: "All done.", StopReason: "end_turn"},
//	}}
//
// The provider asserts in-order consumption. Calling Chat more times than the
// script has responses is treated as a test bug, not a runtime error.
type MockProvider struct {
	Responses []ChatResponse
	calls     int // for asserting how many turns ran
}

func (m *MockProvider) Chat(_ context.Context, _ []Message) (*ChatResponse, error) {
	if m.calls >= len(m.Responses) {
		return nil, fmt.Errorf("mock provider exhausted: %d calls made but only %d scripted", m.calls+1, len(m.Responses))
	}
	r := m.Responses[m.calls]
	m.calls++
	return &r, nil
}

// Calls reports how many times Chat was invoked. Tests use this to assert turn count.
func (m *MockProvider) Calls() int { return m.calls }
