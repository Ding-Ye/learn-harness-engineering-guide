package main

import (
	"fmt"
	"strings"
	"testing"
)

// makeUserMsg fabricates a user message with one text block. The exact words
// matter for the token-based threshold tests: each word counts as 1.3 tokens
// in EstimateTextTokens.
func makeUserMsg(text string) Message {
	return Message{
		Role:    "user",
		Content: []ContentBlock{{Type: "text", Text: text}},
	}
}

// makeSystemMsg fabricates a system message — used to verify that system
// messages are never compressed and always pass through GetMessages().
func makeSystemMsg(text string) Message {
	return Message{
		Role:    "system",
		Content: []ContentBlock{{Type: "text", Text: text}},
	}
}

// repeatWords returns "w w w ..." with n words. Token count under
// EstimateTextTokens is n*13/10.
func repeatWords(w string, n int) string {
	parts := make([]string, n)
	for i := range parts {
		parts[i] = w
	}
	return strings.Join(parts, " ")
}

// newSWC returns a SlidingWindowContext with a fresh MockSummarizer wired in.
// The summarizer is returned separately so tests can inspect its Calls,
// Inputs, and MsgCounts after the fact.
func newSWC(windowSize, maxTokens int, threshold float64) (*SlidingWindowContext, *MockSummarizer) {
	mock := &MockSummarizer{}
	return NewSlidingWindowContext(windowSize, maxTokens, threshold, mock), mock
}

// TestSlidingWindow_BelowWindowDoesNotCompress: add 5 messages with window=15.
// We're well under the message-count window AND well under the token threshold,
// so no compression should happen, and the summary should remain empty.
func TestSlidingWindow_BelowWindowDoesNotCompress(t *testing.T) {
	swc, mock := newSWC(15 /*windowSize*/, 10_000 /*maxTokens*/, 0.7 /*threshold*/)

	for i := 0; i < 5; i++ {
		if err := swc.Add(makeUserMsg(fmt.Sprintf("turn %d", i))); err != nil {
			t.Fatalf("Add #%d: %v", i, err)
		}
	}

	if swc.Summary() != "" {
		t.Fatalf("expected empty summary, got %q", swc.Summary())
	}
	if swc.CompressAttempts != 0 {
		t.Fatalf("expected 0 compress attempts, got %d", swc.CompressAttempts)
	}
	if mock.Calls != 0 {
		t.Fatalf("expected mock to be uncalled, got Calls=%d", mock.Calls)
	}
	if got := swc.Len(); got != 5 {
		t.Fatalf("expected 5 messages held, got %d", got)
	}

	// GetMessages should also return exactly the 5 user messages, no summary block.
	out := swc.GetMessages()
	if len(out) != 5 {
		t.Fatalf("GetMessages: expected 5, got %d", len(out))
	}
	for i, m := range out {
		if m.Role != "user" {
			t.Errorf("GetMessages[%d]: expected role=user, got %s", i, m.Role)
		}
	}
}

// TestSlidingWindow_OverWindowTriggersCompress: feed 50 reasonably-sized user
// messages with a small token budget so the threshold is crossed early.
// Assertions: summary is non-empty; mock summarizer was called at least once;
// final state has exactly windowSize (=15) non-system messages, all of which
// are the *last* 15 we added (verbatim, not summarized).
func TestSlidingWindow_OverWindowTriggersCompress(t *testing.T) {
	// Each message is "word word ... word" with 4 words = 5 tokens.
	// maxTokens=200, threshold=0.7 → trigger at 140 tokens.
	// 50 messages * 5 tokens = 250 tokens total before any compression — but
	// compression triggers as soon as we cross 140, which is after ~28 adds.
	swc, mock := newSWC(15, 200, 0.7)

	const totalAdded = 50
	for i := 0; i < totalAdded; i++ {
		// Tag each message with its index so we can verify which ones survived.
		body := fmt.Sprintf("idx-%d %s", i, repeatWords("w", 3))
		if err := swc.Add(makeUserMsg(body)); err != nil {
			t.Fatalf("Add #%d: %v", i, err)
		}
	}

	if swc.Summary() == "" {
		t.Fatalf("expected non-empty summary after %d adds", totalAdded)
	}
	if mock.Calls == 0 {
		t.Fatalf("expected mock summarizer to have been called at least once")
	}
	// After many compressions interleaved with adds, the buffer size is
	// somewhere between windowSize and (windowSize + adds-since-last-compress).
	// The hard invariant: it must hold AT LEAST windowSize and at most the
	// total number of non-system messages added.
	if got := swc.Len(); got < 15 {
		t.Fatalf("buffer should hold at least windowSize=15 messages, got %d", got)
	}
	if got := swc.Len(); got > totalAdded {
		t.Fatalf("buffer should hold at most %d messages, got %d", totalAdded, got)
	}

	// The CORE invariant: the LAST 15 messages in the buffer are the LAST 15
	// we added (verbatim, not summarized). Slice the tail of GetMessages and
	// check each tag matches its expected index.
	out := swc.GetMessages()
	// The tail of length 15 should hold idx-(totalAdded-15)..idx-(totalAdded-1).
	tail := out[len(out)-15:]
	for i, m := range tail {
		if m.Role != "user" {
			t.Errorf("tail[%d]: expected user role, got %s", i, m.Role)
			continue
		}
		expectedIdx := totalAdded - 15 + i
		expectedTag := fmt.Sprintf("idx-%d ", expectedIdx)
		if !strings.HasPrefix(m.Content[0].Text, expectedTag) {
			t.Errorf("tail[%d]: expected prefix %q, got %q", i, expectedTag, m.Content[0].Text)
		}
	}

	// And the summary block must be in the output between system messages and
	// user messages. We check it's somewhere before the tail.
	foundSummary := false
	for i, m := range out {
		if m.Role == "system" && len(m.Content) > 0 && strings.Contains(m.Content[0].Text, summaryMarker) {
			if i >= len(out)-15 {
				t.Errorf("summary marker found inside the tail of recent messages at index %d", i)
			}
			foundSummary = true
			break
		}
	}
	if !foundSummary {
		t.Errorf("expected to find a [Conversation history summary] message in GetMessages output")
	}
}

// TestSlidingWindow_SummaryAccumulates: trigger 3 separate compression events
// and assert (a) the mock summarizer was called 3 times, (b) each subsequent
// call received the *previous* summary's text as `prevSummary` (i.e. the
// summary is cumulative — each compression feeds back the last one as
// context).
func TestSlidingWindow_SummaryAccumulates(t *testing.T) {
	// Setup: a small budget so each batch of ~16 messages crosses the threshold.
	// We want at least 3 compressions, so we'll Add 4 * (windowSize+1) messages.
	// With windowSize=5 and maxTokens=80 (threshold=0.7 → 56), each user msg
	// is ~3 tokens, so 56/3 ≈ 19 messages will cross threshold; tuning below.
	const window = 5
	const maxTok = 80
	swc, mock := newSWC(window, maxTok, 0.7)

	// Add enough messages to force at least 3 compressions. Each message is
	// "word word word" (3 words → 3 tokens with the *13/10 rule). 3 tokens
	// per msg * X = 56 → X ≈ 19. We add 60 messages to make 3 cycles certain.
	for i := 0; i < 60; i++ {
		body := repeatWords("w", 3)
		if err := swc.Add(makeUserMsg(body)); err != nil {
			t.Fatalf("Add #%d: %v", i, err)
		}
	}

	if mock.Calls < 3 {
		t.Fatalf("expected at least 3 summarizer calls, got %d (CompressAttempts=%d)",
			mock.Calls, swc.CompressAttempts)
	}

	// Cumulative check: first call receives prevSummary="" (length 0); every
	// subsequent call receives a NON-empty prevSummary (length > 0) AND that
	// prevSummary equals the previous call's *output*. We re-derive each
	// call's expected output as `[summarized N msgs; prev=L%d]` using the
	// recorded MsgCount and the previous prevSummary's length.
	if mock.Inputs[0] != "" {
		t.Errorf("first call: expected empty prevSummary, got %q", mock.Inputs[0])
	}
	for i := 1; i < mock.Calls; i++ {
		if mock.Inputs[i] == "" {
			t.Errorf("call #%d: expected non-empty prevSummary, got empty (summary did not accumulate)", i)
		}
		// The summarizer is deterministic; the i-th call's prevSummary should
		// be exactly the (i-1)-th call's output.
		prevOutput := fmt.Sprintf("[summarized %d msgs; prev=L%d]",
			mock.MsgCounts[i-1], len(mock.Inputs[i-1]))
		if mock.Inputs[i] != prevOutput {
			t.Errorf("call #%d: prevSummary=%q, want %q (mock not seeing previous summary)",
				i, mock.Inputs[i], prevOutput)
		}
	}
}

// TestSlidingWindow_SystemMessagesPreserved: add 2 system messages
// interleaved with 30 user messages and verify both system messages survive
// every compression and appear in GetMessages() in their original order,
// ahead of (a) the summary block and (b) the recent-window user messages.
func TestSlidingWindow_SystemMessagesPreserved(t *testing.T) {
	swc, _ := newSWC(15, 200, 0.7)

	// Two system messages added at the very beginning so we can assert on
	// their text.
	sysA := makeSystemMsg("you are agent A")
	sysB := makeSystemMsg("you must be precise")
	if err := swc.Add(sysA); err != nil {
		t.Fatalf("Add sysA: %v", err)
	}
	if err := swc.Add(sysB); err != nil {
		t.Fatalf("Add sysB: %v", err)
	}

	// Add 30 user messages — enough to trigger at least one compression
	// under the 200-token, 0.7-threshold (140 tokens) budget.
	for i := 0; i < 30; i++ {
		body := repeatWords("u", 4) // 4 tokens each
		if err := swc.Add(makeUserMsg(body)); err != nil {
			t.Fatalf("Add user #%d: %v", i, err)
		}
	}

	// System messages must STILL be in s.messages — neither was summarized.
	systemFound := 0
	for _, m := range swc.GetMessages() {
		if m.Role == "system" && len(m.Content) > 0 {
			text := m.Content[0].Text
			if text == "you are agent A" || text == "you must be precise" {
				systemFound++
			}
		}
	}
	if systemFound != 2 {
		t.Fatalf("expected both system messages in GetMessages, found %d", systemFound)
	}

	// Order check: in GetMessages output, sysA must come before sysB, and
	// both must come before any user message.
	out := swc.GetMessages()
	sysAIdx, sysBIdx, firstUserIdx := -1, -1, -1
	for i, m := range out {
		if m.Role == "system" && len(m.Content) > 0 {
			text := m.Content[0].Text
			if text == "you are agent A" && sysAIdx < 0 {
				sysAIdx = i
			}
			if text == "you must be precise" && sysBIdx < 0 {
				sysBIdx = i
			}
		}
		if m.Role == "user" && firstUserIdx < 0 {
			firstUserIdx = i
		}
	}
	if !(sysAIdx >= 0 && sysBIdx >= 0 && firstUserIdx >= 0) {
		t.Fatalf("missing message in output: sysA=%d sysB=%d user=%d", sysAIdx, sysBIdx, firstUserIdx)
	}
	if !(sysAIdx < sysBIdx) {
		t.Errorf("sysA (%d) should precede sysB (%d)", sysAIdx, sysBIdx)
	}
	if !(sysBIdx < firstUserIdx) {
		t.Errorf("both system messages should precede the first user message; sysB=%d firstUser=%d",
			sysBIdx, firstUserIdx)
	}
}

// TestSlidingWindow_ThresholdRespectsTokens: a SINGLE huge message of >70%
// tokens triggers compress() even though the message-count is far below
// windowSize. Because there's nothing pre-window to summarize, compress() is a
// no-op (summary stays empty, mock.Calls stays at 0), but CompressAttempts is
// incremented so the test can verify the trigger fired.
//
// This test exists specifically to confirm the trigger is *token-based*, not
// *message-count-based*.
func TestSlidingWindow_ThresholdRespectsTokens(t *testing.T) {
	// windowSize=15 (plenty of room for one message), maxTokens=100
	// (threshold=0.7 → trigger at 70 tokens).
	swc, mock := newSWC(15, 100, 0.7)

	// 80 words → 80*13/10 = 104 tokens. Easily above the 70-token threshold,
	// and a single message that won't be compressed because nonSystem=1 ≤
	// windowSize=15.
	huge := repeatWords("blob", 80)
	if err := swc.Add(makeUserMsg(huge)); err != nil {
		t.Fatalf("Add huge: %v", err)
	}

	if swc.CompressAttempts != 1 {
		t.Fatalf("expected CompressAttempts=1 (threshold should have tripped), got %d",
			swc.CompressAttempts)
	}
	if swc.Summary() != "" {
		t.Errorf("expected empty summary (nothing to compress with 1 msg < window=15), got %q",
			swc.Summary())
	}
	if mock.Calls != 0 {
		t.Errorf("mock summarizer should not be invoked when there's nothing pre-window; got Calls=%d",
			mock.Calls)
	}
	if got := swc.Len(); got != 1 {
		t.Errorf("huge message should still be in buffer verbatim, got %d messages", got)
	}
}
