package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
)

// MockProvider returns scripted ChatResponses in order. It exists so the loop
// has a deterministic counterpart for tests and demos — no network, no API key,
// no LLM. The script either comes from an in-memory slice (Responses) or from
// a JSON file on disk (loaded via NewMockProviderFromFile).
//
// Wire shape (JSON file is a single array of ChatResponse objects):
//
//	[
//	  {
//	    "Content": [
//	      {"type": "text", "text": "Let me echo that."},
//	      {"type": "tool_use", "id": "call_1", "name": "echo", "input": {"text": "hello"}}
//	    ],
//	    "StopReason": "tool_use"
//	  },
//	  {
//	    "Content": [{"type": "text", "text": "All done."}],
//	    "StopReason": "end_turn"
//	  }
//	]
//
// The provider asserts in-order consumption. Calling Chat more times than the
// script has responses returns an error so test bugs are loud.
type MockProvider struct {
	Responses []ChatResponse
	calls     int
}

// NewMockProviderFromFile reads a JSON array of ChatResponse from path and
// returns a provider that replays them in order.
func NewMockProviderFromFile(path string) (*MockProvider, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read mock script %q: %w", path, err)
	}

	// We can't json.Unmarshal directly into []ChatResponse because Content uses
	// lowercase JSON tags on ContentBlock but ChatResponse has no struct tags.
	// Decoding into an intermediate shape keeps the JSON tidy.
	var raw []struct {
		Content    []ContentBlock `json:"Content"`
		StopReason string         `json:"StopReason"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("parse mock script %q: %w", path, err)
	}

	resps := make([]ChatResponse, len(raw))
	for i, r := range raw {
		resps[i] = ChatResponse{Content: r.Content, StopReason: r.StopReason}
	}
	return &MockProvider{Responses: resps}, nil
}

// Chat returns the next scripted ChatResponse, ignoring the request entirely.
// The mock is deliberately oblivious to req.Messages so tests can assert what
// the loop builds without the provider second-guessing it.
func (m *MockProvider) Chat(_ context.Context, _ ChatRequest) (*ChatResponse, error) {
	if m.calls >= len(m.Responses) {
		return nil, fmt.Errorf("mock provider exhausted: %d calls made but only %d scripted", m.calls+1, len(m.Responses))
	}
	r := m.Responses[m.calls]
	m.calls++
	return &r, nil
}

// Calls reports how many times Chat was invoked. Tests use this to assert turn count.
func (m *MockProvider) Calls() int { return m.calls }
