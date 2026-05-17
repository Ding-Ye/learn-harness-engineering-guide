package main

import (
	"context"
	"fmt"
)

// summaryMarker is the literal prefix the upstream guide uses for the synthetic
// system message produced after compression. We keep the string in one place so
// `GetMessages` can emit it and tests can assert on it without spelling-drift.
const summaryMarker = "[Conversation history summary]"

// SlidingWindowContext implements the third "line of defense" against runaway
// context growth: keep the last N=windowSize non-system messages verbatim, and
// fold everything older into a single cumulative summary block. The trigger is
// a token-budget threshold (default 70% of maxTokens), NOT a message-count
// check — the upstream `context-engineering.md` L91-L138 is explicit that the
// budget is what matters; message-count is just a coarse proxy.
//
// Cumulative summary: each compression takes the *previous* summary plus the
// pre-window messages and produces a new summary that supersedes the previous.
// This is what makes the strategy work for very long sessions: a "summary of a
// summary" replaces, not concatenates, so the summary block stays roughly
// constant-size across the lifetime of the session.
//
// System messages are never compressed. They always pass through in their
// original order, in front of the (optional) summary block, in front of the
// recent-window block.
type SlidingWindowContext struct {
	// windowSize is the number of non-system messages to keep verbatim AFTER
	// a compression event. A natural default is 15 turns ≈ 15 messages.
	windowSize int

	// maxTokens is the conversation budget — typically a fraction of the
	// model's full context window (say 80% of 128K).
	maxTokens int

	// threshold is the fraction of maxTokens that triggers compression.
	// 0.7 matches the upstream "threshold compression" pattern at L110-L139.
	threshold float64

	// summarizer is the strategy for collapsing pre-window messages. In tests
	// we inject MockSummarizer; in production it would call a fast/cheap model.
	summarizer Summarizer

	// messages is the live conversation buffer. Order is preserved; system
	// messages may be interleaved with non-system ones (caller-controlled).
	messages []Message

	// summary is the cumulative summary text produced by the most recent
	// compression. Empty before the first compression event.
	summary string

	// CompressAttempts counts how many times compress() was invoked,
	// regardless of whether the summarizer was actually called. A no-op
	// compression (nothing pre-window to summarize) still increments this so
	// the threshold-trigger test can confirm the trigger fired.
	CompressAttempts int
}

// NewSlidingWindowContext wires the four knobs. Sensible defaults:
//
//	NewSlidingWindowContext(15, 128_000, 0.7, summarizer)
//
// If windowSize <= 0 or threshold <= 0, the caller has made a configuration
// error; we don't silently fix it because the resulting behavior is so
// confusing (everything would compress on the first Add) that a panic at
// construction is friendlier than a mystery at runtime.
func NewSlidingWindowContext(windowSize, maxTokens int, threshold float64, summarizer Summarizer) *SlidingWindowContext {
	if windowSize <= 0 {
		panic(fmt.Sprintf("SlidingWindowContext: windowSize must be > 0, got %d", windowSize))
	}
	if maxTokens <= 0 {
		panic(fmt.Sprintf("SlidingWindowContext: maxTokens must be > 0, got %d", maxTokens))
	}
	if threshold <= 0 || threshold > 1 {
		panic(fmt.Sprintf("SlidingWindowContext: threshold must be in (0, 1], got %f", threshold))
	}
	return &SlidingWindowContext{
		windowSize: windowSize,
		maxTokens:  maxTokens,
		threshold:  threshold,
		summarizer: summarizer,
	}
}

// Add appends a message and, if the running token count exceeds
// threshold*maxTokens, runs a compression pass. The compression itself may or
// may not actually summarize anything — see compress() for the details.
//
// We use context.Background() inside the synchronous Summarize call rather
// than threading a context through Add(). That keeps the public API ergonomic
// for the test fixtures (just a message argument) at the cost of giving up
// cancellation semantics — acceptable for a teaching implementation. A
// production version would take ctx as a parameter and propagate it.
func (s *SlidingWindowContext) Add(msg Message) error {
	s.messages = append(s.messages, msg)
	budget := int(float64(s.maxTokens) * s.threshold)
	if EstimateTokens(s.messages) > budget {
		return s.compress(context.Background())
	}
	return nil
}

// compress moves all but the last windowSize non-system messages into the
// summarizer and replaces the in-memory buffer with (system + last-window).
// It is idempotent in the sense that calling it twice in a row produces no
// further change on the second call.
//
// The two interesting cases:
//
//  1. nonSystem > windowSize: there ARE messages to summarize. We slice off the
//     pre-window prefix, hand it to the summarizer (along with the previous
//     summary as context), store the result as the new summary, and rebuild
//     s.messages = system + recent.
//
//  2. nonSystem <= windowSize: there is NOTHING pre-window to summarize. We
//     still increment CompressAttempts so the threshold-trigger test can
//     observe that compress() was invoked; otherwise we no-op. This case
//     arises when a single message exceeds 70% of maxTokens on its own —
//     compression can't help, and the caller will have to either raise
//     maxTokens or accept that the budget is blown.
func (s *SlidingWindowContext) compress(ctx context.Context) error {
	s.CompressAttempts++

	// Partition s.messages into system vs non-system. Order is preserved
	// within each partition. system stays exactly as-is; non-system is the
	// pool that gets split into "to-summarize" and "to-keep".
	system := make([]Message, 0, 4)
	nonSystem := make([]Message, 0, len(s.messages))
	for _, m := range s.messages {
		if m.Role == "system" {
			system = append(system, m)
		} else {
			nonSystem = append(nonSystem, m)
		}
	}

	// Case 2: not enough to summarize. Bail.
	if len(nonSystem) <= s.windowSize {
		return nil
	}

	// Case 1: split.
	cut := len(nonSystem) - s.windowSize
	old := nonSystem[:cut]
	recent := nonSystem[cut:]

	newSummary, err := s.summarizer.Summarize(ctx, s.summary, old)
	if err != nil {
		// Compression failed. We leave s.messages untouched so the caller
		// keeps a chance to retry on the next Add(). The error propagates so
		// the caller can surface it.
		return fmt.Errorf("summarize: %w", err)
	}
	s.summary = newSummary

	// Rebuild s.messages: system messages first (preserving their original
	// order), then the recent-window non-system messages. The summary itself
	// is NOT inserted into s.messages — it's exposed only via GetMessages(),
	// because storing it inline would mean the next compression has to figure
	// out "is this an original system message or a summary I made?".
	rebuilt := make([]Message, 0, len(system)+len(recent))
	rebuilt = append(rebuilt, system...)
	rebuilt = append(rebuilt, recent...)
	s.messages = rebuilt
	return nil
}

// GetMessages returns the context-ready message list, in order:
//
//	[all system messages]
//	[synthetic system message containing the summary, IF summary is non-empty]
//	[the last windowSize non-system messages]
//
// The summary is wrapped in a Message with Role="system" and one text block
// whose body is `[Conversation history summary]\n<summary>`. Marking it as
// system role matches the upstream Python at L232-L235 and means downstream
// providers will treat the summary as authoritative context, not as
// conversation that the model said.
func (s *SlidingWindowContext) GetMessages() []Message {
	out := make([]Message, 0, len(s.messages)+1)

	// 1. All original system messages, in original order.
	for _, m := range s.messages {
		if m.Role == "system" {
			out = append(out, m)
		}
	}

	// 2. Synthetic summary message, if we have one.
	if s.summary != "" {
		out = append(out, Message{
			Role: "system",
			Content: []ContentBlock{{
				Type: "text",
				Text: summaryMarker + "\n" + s.summary,
			}},
		})
	}

	// 3. All non-system messages in original order.
	for _, m := range s.messages {
		if m.Role != "system" {
			out = append(out, m)
		}
	}

	return out
}

// Summary returns the current cumulative summary text (without the marker
// prefix). Empty before the first compression. Tests assert on this directly
// rather than parsing GetMessages() output.
func (s *SlidingWindowContext) Summary() string {
	return s.summary
}

// Len returns the number of messages currently held in the buffer (system +
// non-system), not counting the synthetic summary block. Mostly for tests and
// the demo CLI.
func (s *SlidingWindowContext) Len() int {
	return len(s.messages)
}
