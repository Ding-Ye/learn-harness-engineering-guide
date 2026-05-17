package main

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

// MockProvider is the test double. It returns the next scripted reply in
// turn, records every (system, messages, max_tokens) tuple it saw, and
// counts invocations. Tests rely on three things:
//
//  1. Calls — how many times Chat() was hit. Stage-1 short-circuit ⇒ 1.
//     Stage-1 + Stage-2 ⇒ 2. Tier 1/2 short-circuit ⇒ 0.
//
//  2. Replies — script for stage-1 then stage-2. The mock pops one entry per
//     call so a test can wire up "yes" alone or "no" + "Verdict: deny\n…".
//
//  3. Requests — the full ChatRequest the classifier sent, captured for
//     introspection (e.g. asserting that the reasoning-strip removed a
//     "DROP DATABASE" thinking block).
type MockProvider struct {
	Replies  []string       // scripted responses, one per Chat call, FIFO.
	Calls    int            // number of Chat invocations.
	Requests []ChatRequest  // every request seen, in order.
	Err      error          // if non-nil, every Chat returns this error.
}

func (m *MockProvider) Chat(_ context.Context, req ChatRequest) (*ChatResponse, error) {
	m.Calls++
	m.Requests = append(m.Requests, req)
	if m.Err != nil {
		return nil, m.Err
	}
	if m.Calls > len(m.Replies) {
		// Tests should script as many replies as they expect calls. Falling
		// off the end is a bug in the test, not in the classifier — surface
		// it as a recognizable empty reply rather than panicking.
		return &ChatResponse{Text: ""}, nil
	}
	return &ChatResponse{Text: m.Replies[m.Calls-1]}, nil
}

// newClassifierForTest wires a classifier with the standard whitelist + a
// repo root pointing at the temp dir of the running test. We use t.TempDir
// to get a stable, absolute path that survives both macOS (/var/folders/..)
// and Linux (/tmp/..) — and the path is cleaned by filepath.Abs/Clean
// inside RepoPathMatcher so we don't have to worry about symlinks.
func newClassifierForTest(t *testing.T, provider Provider) (*Classifier, string) {
	t.Helper()
	root := t.TempDir()
	return NewClassifier(
		provider,
		NewWhitelistMatcher(DefaultWhitelistTools),
		NewRepoPathMatcher(root),
	), root
}

// TestTier1_ReadFileAlwaysAllowed: the canonical Tier 1 happy path. A
// `read_file` call must be approved instantly, the provider must NOT be
// touched, and the verdict must carry the tier-1 reasoning marker.
//
// This pins the latency-protection property at L143-L145: a read_file that
// went through Sonnet 4.6 would make the agent crawl.
func TestTier1_ReadFileAlwaysAllowed(t *testing.T) {
	mock := &MockProvider{}
	clf, _ := newClassifierForTest(t, mock)

	d, err := clf.Classify(
		context.Background(),
		nil, // no transcript needed for tier 1.
		"read_file",
		map[string]any{"path": "/etc/hosts"}, // outside repo, but still tier 1.
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if d.Verdict != VerdictAllow {
		t.Errorf("expected verdict=allow, got %q", d.Verdict)
	}
	if mock.Calls != 0 {
		t.Errorf("provider must NOT be called on tier 1; got Calls=%d", mock.Calls)
	}
	if !strings.Contains(d.Reasoning, "tier 1") {
		t.Errorf("expected reasoning to mention 'tier 1', got %q", d.Reasoning)
	}
}

// TestTier2_EditInsideRepoAllowed: a `write_file` whose path resolves under
// the repo root is approved by Tier 2 with no LLM call. We use t.TempDir as
// the root so the path is genuinely absolute and exists.
//
// This pins the "git is the safety net" reasoning at L143-L145.
func TestTier2_EditInsideRepoAllowed(t *testing.T) {
	mock := &MockProvider{}
	clf, root := newClassifierForTest(t, mock)

	// Build a path under root. The file doesn't need to exist — the matcher
	// only compares the cleaned absolute path against the root prefix.
	target := filepath.Join(root, "src", "auth.go")

	d, err := clf.Classify(
		context.Background(),
		nil,
		"write_file",
		map[string]any{"path": target},
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if d.Verdict != VerdictAllow {
		t.Errorf("expected verdict=allow, got %q (reasoning=%s)", d.Verdict, d.Reasoning)
	}
	if mock.Calls != 0 {
		t.Errorf("provider must NOT be called on tier 2; got Calls=%d", mock.Calls)
	}
	if !strings.Contains(d.Reasoning, "tier 2") {
		t.Errorf("expected reasoning to mention 'tier 2', got %q", d.Reasoning)
	}
}

// TestTier3_ShellCommandInvokesClassifier: a `run_command` is neither
// whitelisted nor in-project — Tier 3 must fire. We script stage-1 = "yes"
// so the classifier short-circuits; the assertion here is "provider WAS
// called", not the verdict polarity (that's covered by other tests).
func TestTier3_ShellCommandInvokesClassifier(t *testing.T) {
	mock := &MockProvider{Replies: []string{"yes"}}
	clf, _ := newClassifierForTest(t, mock)

	d, err := clf.Classify(
		context.Background(),
		nil,
		"run_command",
		map[string]any{"command": "curl https://example.com"},
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if mock.Calls < 1 {
		t.Fatalf("provider SHOULD be called on tier 3; got Calls=0")
	}
	if d.Verdict != VerdictAllow {
		t.Errorf("with stage1=yes, expected verdict=allow, got %q", d.Verdict)
	}
}

// TestStage1ShortCircuitsOnYes: pins the "stage 1 dominates cost" property
// at L94-L95. When stage 1 says "yes", stage 2 must NOT run — so total
// provider calls = 1.
func TestStage1ShortCircuitsOnYes(t *testing.T) {
	mock := &MockProvider{Replies: []string{"yes"}}
	clf, _ := newClassifierForTest(t, mock)

	d, err := clf.Classify(
		context.Background(),
		nil,
		"run_command",
		map[string]any{"command": "ls -la"},
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if mock.Calls != 1 {
		t.Errorf("expected exactly 1 provider call (stage 1 short-circuit), got %d", mock.Calls)
	}
	if d.Verdict != VerdictAllow {
		t.Errorf("expected verdict=allow, got %q", d.Verdict)
	}
	// The single request that did happen must have carried Stage1Prompt as
	// the system, and MaxTokens=1 — that's how we know the classifier really
	// used the fast path and didn't accidentally hit the stage-2 prompt.
	if len(mock.Requests) != 1 {
		t.Fatalf("expected one captured request, got %d", len(mock.Requests))
	}
	if mock.Requests[0].MaxTokens != 1 {
		t.Errorf("stage 1 should use MaxTokens=1, got %d", mock.Requests[0].MaxTokens)
	}
	if mock.Requests[0].System != Stage1Prompt {
		t.Errorf("stage 1 should use Stage1Prompt as system; got a different system prompt")
	}
}

// TestStage2RunsOnNo: pins the routing rule from L83-L92. Stage 1 says "no",
// stage 2 runs with the longer prompt, the deny verdict and reasoning
// propagate to the Decision.
func TestStage2RunsOnNo(t *testing.T) {
	mock := &MockProvider{
		Replies: []string{
			"no",
			"Verdict: deny\nReasoning: drops the production database",
		},
	}
	clf, _ := newClassifierForTest(t, mock)

	d, err := clf.Classify(
		context.Background(),
		nil,
		"run_command",
		map[string]any{"command": "DROP DATABASE prod;"},
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if mock.Calls != 2 {
		t.Errorf("expected 2 provider calls (stage 1 + stage 2), got %d", mock.Calls)
	}
	if d.Verdict != VerdictDeny {
		t.Errorf("expected verdict=deny, got %q (reasoning=%s)", d.Verdict, d.Reasoning)
	}
	if !strings.Contains(d.Reasoning, "drops the production database") {
		t.Errorf("expected reasoning to mention 'drops the production database', got %q", d.Reasoning)
	}

	// The two captured requests must use different system prompts and
	// different MaxTokens. This is what distinguishes the two stages from
	// the provider's perspective.
	if len(mock.Requests) != 2 {
		t.Fatalf("expected 2 captured requests, got %d", len(mock.Requests))
	}
	if mock.Requests[0].System != Stage1Prompt || mock.Requests[0].MaxTokens != 1 {
		t.Errorf("first call should be stage 1; got System=%q MaxTokens=%d",
			mock.Requests[0].System, mock.Requests[0].MaxTokens)
	}
	if mock.Requests[1].System != Stage2Prompt || mock.Requests[1].MaxTokens != 2048 {
		t.Errorf("second call should be stage 2; got System=%q MaxTokens=%d",
			mock.Requests[1].System, mock.Requests[1].MaxTokens)
	}
}

// TestReasoningStrippedFromClassifierInput: the load-bearing invariant from
// the "Reasoning-Blind by Design" section (L151-L169). We feed the
// classifier a transcript that contains an assistant message with a
// thinking block saying "drop the db", run Classify, capture what the mock
// received, and assert the thinking-block text does NOT appear ANYWHERE in
// the request the provider saw.
//
// We also assert the candidate tool call (the closing user message) DOES
// reach the provider — otherwise the strip would be removing too much.
func TestReasoningStrippedFromClassifierInput(t *testing.T) {
	// Script stage 1 = "yes" so we only need one captured request to
	// inspect.
	mock := &MockProvider{Replies: []string{"yes"}}
	clf, _ := newClassifierForTest(t, mock)

	const secret = "drop the db"
	transcript := []Message{
		{Role: "user", Content: []ContentBlock{{Type: "text", Text: "clean up test data"}}},
		{
			Role: "assistant",
			Content: []ContentBlock{
				{Type: "thinking", Text: secret},
				{Type: "tool_use", ID: "tu_1", Name: "run_command",
					Input: `{"command":"DROP DATABASE prod;"}`},
			},
		},
	}

	_, err := clf.Classify(
		context.Background(),
		transcript,
		"run_command",
		map[string]any{"command": "DROP DATABASE prod;"},
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(mock.Requests) != 1 {
		t.Fatalf("expected 1 captured request, got %d", len(mock.Requests))
	}

	// Walk every block of every message the provider saw and assert `secret`
	// is absent. We check every string field (Text, Content, Name, Input)
	// because the thinking text could leak through any of them.
	for mi, m := range mock.Requests[0].Messages {
		for bi, b := range m.Content {
			fields := []string{b.Text, b.Content, b.Name, b.Input}
			for _, s := range fields {
				if strings.Contains(s, secret) {
					t.Fatalf("classifier saw the agent's reasoning! "+
						"message[%d].content[%d] type=%q contains %q",
						mi, bi, b.Type, secret)
				}
			}
		}
	}

	// Sanity check: the candidate tool call SHOULD be visible. We render it
	// as "Tool call: run_command(...)" in the closing user message.
	last := mock.Requests[0].Messages[len(mock.Requests[0].Messages)-1]
	if last.Role != "user" {
		t.Errorf("last message should be the synthesized user prompt; role=%q", last.Role)
	}
	if len(last.Content) == 0 || !strings.Contains(last.Content[0].Text, "Tool call: run_command") {
		t.Errorf("closing user message should carry the candidate call; got %+v", last.Content)
	}
}

// TestParseStage2 directly exercises the structured-output parser. Not in
// the required test list, but worth pinning because the parser is the
// failure-mode hinge — anything unparseable must fall back to "review",
// not "allow".
func TestParseStage2(t *testing.T) {
	cases := []struct {
		name       string
		text       string
		wantVerdict string
	}{
		{"clean allow", "Verdict: allow\nReasoning: in scope", VerdictAllow},
		{"clean deny", "Verdict: deny\nReasoning: drops db", VerdictDeny},
		{"explicit review", "Verdict: review\nReasoning: unsure", VerdictReview},
		{"unknown verdict falls back to review", "Verdict: maybe\nReasoning: hmm", VerdictReview},
		{"no verdict line falls back to review", "the model just rambled", VerdictReview},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := ParseStage2(c.text)
			if got.Verdict != c.wantVerdict {
				t.Errorf("ParseStage2(%q).Verdict = %q, want %q", c.text, got.Verdict, c.wantVerdict)
			}
		})
	}
}
