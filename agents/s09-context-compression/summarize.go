package main

import (
	"context"
	"fmt"
)

// SummarizePrompt is the verbatim text from `guide/context-engineering.md`
// L146-L148. A real summarizer would feed this plus prevSummary plus the
// messages-to-compress into an LLM and return the model's reply. The mock
// below ignores the prompt entirely (it's deterministic), but the constant is
// exported so a future swap to a real provider can reuse it.
const SummarizePrompt = `Summarize the key decisions, findings, and current state ` +
	`from this conversation. Include: files modified, tests run, errors encountered, ` +
	`and the current plan. Be concise — under 500 words.`

// Summarizer collapses a batch of older messages into a short text summary.
// The interface intentionally takes `prevSummary` separately rather than
// embedding it as a synthetic message — this makes the *cumulative* nature of
// the summary explicit in the type system. Implementations should treat
// prevSummary as the previous compression's output and produce a new summary
// that supersedes it.
type Summarizer interface {
	Summarize(ctx context.Context, prevSummary string, msgs []Message) (string, error)
}

// MockSummarizer is a deterministic test double. It does NOT call any LLM.
// The output format is `[summarized N msgs; prev=L%d]` where N is the number
// of messages passed in and the trailing %d is `len(prevSummary)`. The length
// of the previous summary leaks into the new one, which makes
// `TestSlidingWindow_SummaryAccumulates` able to assert *which* call produced
// the final summary just by inspecting the string.
//
// We also track Calls so tests can assert the summarizer was invoked the
// expected number of times across multiple compressions.
type MockSummarizer struct {
	Calls int
	// Inputs records, in order, the prevSummary passed to each call. Tests use
	// this to confirm that the second compression received the first
	// compression's output as prevSummary (i.e. the summary is cumulative).
	Inputs []string
	// MsgCounts records the len(msgs) passed to each call.
	MsgCounts []int
}

// Summarize implements Summarizer. The returned string is the only thing the
// caller (compress()) will store as the new summary, and it is the value that
// future calls will receive back via prevSummary.
func (m *MockSummarizer) Summarize(_ context.Context, prevSummary string, msgs []Message) (string, error) {
	m.Calls++
	m.Inputs = append(m.Inputs, prevSummary)
	m.MsgCounts = append(m.MsgCounts, len(msgs))
	return fmt.Sprintf("[summarized %d msgs; prev=L%d]", len(msgs), len(prevSummary)), nil
}
