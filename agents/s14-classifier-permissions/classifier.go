package main

import (
	"context"
	"encoding/json"
	"fmt"
)

// Classifier is the three-tier permission gate from classifier-permissions.md
// L113-L141. The flow on every tool call:
//
//	┌─────────────────────────────────────────┐
//	│  Classify(transcript, toolName, args)   │
//	└────────────────┬────────────────────────┘
//	                 ▼
//	    ┌────────────────────────┐
//	    │  TIER 1: Whitelist     │  ── matched ──► return ALLOW
//	    │  (read_file, etc.)     │
//	    └────────────┬───────────┘
//	                 │ no match
//	                 ▼
//	    ┌────────────────────────┐
//	    │  TIER 2: In-project    │  ── matched ──► return ALLOW
//	    │  (path under repo)     │
//	    └────────────┬───────────┘
//	                 │ no match
//	                 ▼
//	    ┌────────────────────────┐
//	    │  TIER 3: Classifier    │
//	    │   Stage 1: max=1 yes/no│  ── "yes" ───► return ALLOW
//	    │                        │
//	    │   Stage 2: max=2048    │  ── parse ───► Decision
//	    │   (only on "no")       │
//	    └────────────────────────┘
//
// Tier 1+2 dominate the call volume in a real harness; Tier 3 catches the
// rare dangerous-or-unknown action. Stage 1 dominates Tier 3 in turn —
// stage 2 only runs when stage 1 returned "no", so the average cost is
// one tiny completion per tool call.
type Classifier struct {
	provider         Provider
	whitelistMatcher *WhitelistMatcher
	repoMatcher      *RepoPathMatcher
}

// NewClassifier wires the three tiers. Either matcher may be nil — passing
// nil for both reduces the classifier to "always run the LLM", which is the
// degenerate (but valid) configuration you'd use when measuring stage-1
// false-positive rates on real traffic.
func NewClassifier(p Provider, wl *WhitelistMatcher, repo *RepoPathMatcher) *Classifier {
	return &Classifier{
		provider:         p,
		whitelistMatcher: wl,
		repoMatcher:      repo,
	}
}

// Classify returns a Decision for a candidate tool call given the transcript
// so far. The transcript should be the *full* message history the agent
// would have sent to the model on this turn — including thinking blocks. The
// classifier strips them internally before calling the provider, so the
// caller doesn't have to remember to do it.
//
// Errors from Tier 1 + Tier 2 never occur — they're pure-Go matchers. Tier 3
// errors (provider failures) propagate, wrapped with the stage that failed.
// Callers should fail closed: treat any error as "deny".
func (c *Classifier) Classify(ctx context.Context, transcript []Message, toolName string, args map[string]any) (*Decision, error) {
	// --- Tier 1: whitelist -------------------------------------------------
	if c.whitelistMatcher != nil && c.whitelistMatcher.Match(toolName, args) {
		return &Decision{
			Verdict:    VerdictAllow,
			Reasoning:  fmt.Sprintf("tier 1: %q is in the built-in whitelist", toolName),
			Confidence: 1.0,
		}, nil
	}

	// --- Tier 2: in-project path ------------------------------------------
	if c.repoMatcher != nil && c.repoMatcher.Match(toolName, args) {
		return &Decision{
			Verdict:    VerdictAllow,
			Reasoning:  "tier 2: target path is under the repo root; git is the safety net",
			Confidence: 1.0,
		}, nil
	}

	// --- Tier 3: classifier ------------------------------------------------
	// If no provider was wired, we cannot run stage 1 or stage 2. Fail
	// closed: route to human review.
	if c.provider == nil {
		return &Decision{
			Verdict:    VerdictReview,
			Reasoning:  "tier 3 requested but no provider configured",
			Confidence: 0.0,
		}, nil
	}

	// Strip reasoning ONCE; both stages share the same input.
	visible := StripReasoning(transcript)

	// Render the candidate tool call as the closing user message. The
	// classifier sees: <stripped transcript> + <"Tool call: name(args)"> and
	// must judge whether running that tool call is appropriate.
	prompt := buildCallPrompt(toolName, args)
	stage1Messages := append([]Message{}, visible...)
	stage1Messages = append(stage1Messages, Message{
		Role: "user",
		Content: []ContentBlock{{
			Type: "text",
			Text: prompt,
		}},
	})

	// Stage 1: fast yes/no.
	resp1, err := c.provider.Chat(ctx, ChatRequest{
		System:    Stage1Prompt,
		Messages:  stage1Messages,
		MaxTokens: 1,
	})
	if err != nil {
		return nil, fmt.Errorf("stage 1: %w", err)
	}
	if IsAffirmative(resp1.Text) {
		return &Decision{
			Verdict:    VerdictAllow,
			Reasoning:  "tier 3 stage 1: classifier said yes",
			Confidence: 0.9,
		}, nil
	}

	// Stage 2: full chain-of-thought, only on stage-1 "no". We reuse the
	// same stripped transcript + call prompt, but with the stage-2 system
	// prompt and a larger token budget.
	resp2, err := c.provider.Chat(ctx, ChatRequest{
		System:    Stage2Prompt,
		Messages:  stage1Messages,
		MaxTokens: 2048,
	})
	if err != nil {
		return nil, fmt.Errorf("stage 2: %w", err)
	}
	d := ParseStage2(resp2.Text)
	return &d, nil
}

// buildCallPrompt renders the candidate tool call as a short user-visible
// string. The format ("Tool call: <name>(<json>)") is arbitrary but stable —
// tests grep on the "Tool call: " prefix, and the classifier is told in its
// system prompt that the final user turn holds the candidate action.
//
// We marshal args via encoding/json with MarshalIndent set to no indent so
// the output is one line. If marshaling fails (e.g. the caller passed a
// channel or a function value), we fall back to fmt.Sprintf("%v") — losing
// fidelity is fine here because the classifier just needs *something*
// readable; the actual dispatch never goes through this string.
func buildCallPrompt(toolName string, args map[string]any) string {
	b, err := json.Marshal(args)
	if err != nil {
		return fmt.Sprintf("Tool call: %s(%v)", toolName, args)
	}
	return fmt.Sprintf("Tool call: %s(%s)", toolName, string(b))
}
