package main

import "context"

// Provider is the one boundary every chapter from s02 onwards depends on.
// It abstracts "talk to an LLM" so the loop in loop.go never imports a
// concrete client.
//
// Two implementations ship with this chapter:
//   - MockProvider     — scripted, deterministic, file-driven (testdata/*.json)
//   - AnthropicProvider — real HTTPS call to api.anthropic.com/v1/messages
//
// Why an interface and not a struct: see guide/your-first-harness.md L209-L236.
// "Same loop. Same tools. Different model." — the whole point is being able to
// swap implementations without rewriting the loop. A future Phase G chapter
// adds OpenAIProvider with no change to Loop.Run.
type Provider interface {
	Chat(ctx context.Context, req ChatRequest) (*ChatResponse, error)
}
