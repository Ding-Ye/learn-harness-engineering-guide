package main

import "context"

// Local copies of the canonical message shapes. As with every other chapter in
// this curriculum, s14 deliberately does NOT import from s02 — a reader who
// lands here should be able to follow the code without paging back. The shapes
// mirror s02's Anthropic-style types (Role + []ContentBlock) with two extras
// `Type="thinking"` and the fact that we keep a `Provider` interface local to
// this package.
//
// `Type="thinking"` is the load-bearing addition. The whole point of "reasoning
// blind" classification (upstream L151-L169) is to strip these blocks before
// handing the transcript to the classifier. We therefore need a Type value the
// stripper can recognize even though no production Anthropic ContentBlock JSON
// would carry it verbatim — the field is here for pedagogical clarity.

// Message is one entry in the conversation history. Role is one of
// "system" | "user" | "assistant" | "tool". The classifier reads a transcript
// (a slice of Message) and judges the next tool call against it.
type Message struct {
	Role    string
	Content []ContentBlock
}

// ContentBlock is one element of a message's Content list. Type selects which
// of the other fields is populated:
//
//	"text"       → Text
//	"thinking"   → Text  (internal reasoning — STRIPPED by the classifier)
//	"tool_use"   → ID, Name, Input
//	"tool_result"→ ID, Content, IsError
//
// We model the agent's internal monologue as Type="thinking" rather than as
// an out-of-band annotation because it keeps the stripping logic trivially
// expressible: "remove every block whose Type starts with thinking". Real
// providers (e.g. Anthropic's extended-thinking blocks) use the same shape.
type ContentBlock struct {
	Type    string // "text" | "thinking" | "tool_use" | "tool_result"
	Text    string // when Type=="text" or Type=="thinking"
	ID      string // tool_use id / tool_result_id
	Name    string // when Type=="tool_use"
	Input   string // when Type=="tool_use" — the JSON-encoded args (kept as string for simplicity)
	Content string // when Type=="tool_result"
	IsError bool   // when Type=="tool_result"
}

// Decision is the verdict the classifier returns for a tool call.
//
// Verdict is one of "allow" | "deny" | "review". "review" means "I am unsure;
// surface this to a human" — a real harness would route it to an approval
// queue. We expose it because the upstream block-rules (L240-L246) sometimes
// admit "we should not auto-approve, but it isn't an obvious block either".
//
// Confidence is a float in [0, 1]. The MockProvider in the demo emits 1.0 on
// the fast-yes path and 0.8 on a stage-2 verdict; a production classifier
// would derive this from the model's logprobs or an explicit "confidence:"
// header.
type Decision struct {
	Verdict    string
	Reasoning  string
	Confidence float64
}

// Verdict constants — used so callers don't drift on the string literal.
const (
	VerdictAllow  = "allow"
	VerdictDeny   = "deny"
	VerdictReview = "review"
)

// ChatRequest / ChatResponse are the minimum a classifier needs from a
// provider. We could re-use s02's full ChatRequest but that drags in tool
// schemas and stop-reason machinery that have no role here — the classifier
// gets a system prompt, a few messages, a token cap, and returns one short
// string. Keeping the surface area small makes the MockProvider in the test
// suite easy to read.
type ChatRequest struct {
	// System is the classifier's system prompt — exactly one of STAGE1_PROMPT
	// or STAGE2_PROMPT in the current implementation.
	System string
	// Messages is the (reasoning-stripped) transcript plus the candidate tool
	// call rendered as the final user message. Provider implementations should
	// treat this as opaque text — they don't run tools on it.
	Messages []Message
	// MaxTokens is 1 for stage 1, 2048 for stage 2. This is what makes the
	// two-stage design cheap on average — most calls are 1-token completions.
	MaxTokens int
}

// ChatResponse carries the raw text the provider returned. For stage 1 this is
// effectively a one-character string ("yes" / "no" / "y" / "n"); for stage 2
// it's the multi-line "Verdict: ...\nReasoning: ..." format parsed by
// ParseStage2.
type ChatResponse struct {
	Text string
}

// Provider is the boundary the classifier crosses. A real provider would make
// an HTTPS call to api.anthropic.com (or similar); the tests inject a
// MockProvider that returns scripted strings and records what it was asked.
// Keeping the interface tiny (one method, two POD types) makes the mock 30
// lines instead of 200.
type Provider interface {
	Chat(ctx context.Context, req ChatRequest) (*ChatResponse, error)
}
