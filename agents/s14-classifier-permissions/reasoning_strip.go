package main

// StripReasoning produces a "reasoning-blind" copy of a transcript suitable
// for feeding to the classifier. This is the architectural heart of the
// upstream design at classifier-permissions.md L151-L169: the classifier sees
// user messages, tool calls, and tool results, but it does NOT see the
// agent's own internal monologue.
//
// Why? Because the agent can rationalize anything. If the classifier reads
// the reasoning, the agent's explanation becomes part of its context. A
// jailbroken or prompt-injected agent can write paragraphs of plausible
// justification that lead the classifier to approve. Reasoning-blindness
// forces the classifier to judge the action on its own merits — "given the
// user said X, is doing Y a reasonable interpretation?".
//
// Our heuristic strip has two rules:
//
//  1. Any ContentBlock with Type=="thinking" is removed outright. This is the
//     explicit case: providers that emit thinking blocks (e.g. Anthropic's
//     extended-thinking mode) mark them with this Type, and the stripper just
//     filters them out.
//
//  2. In an assistant turn that also contains tool_use blocks, any text
//     blocks that appear BEFORE the first tool_use are treated as reasoning
//     and dropped. The intuition is the agent's "I'll first read the file
//     and then…" prose that often precedes a tool call. The text blocks that
//     come AFTER a tool_use (or in a turn with NO tool_use, which is a real
//     reply to the user) are preserved.
//
// Rule 2 is a heuristic, not a guarantee. A determined attacker could embed
// reasoning in a tool_use's input field, but that would be visible to the
// classifier as part of the action and is therefore inspectable. The point
// of the stripper is not to be paranoid-perfect — it's to remove the most
// common channel by which the agent's monologue could pollute the
// classifier's judgment.
//
// We do NOT mutate the input slice. The stripped transcript is a new slice;
// the original is preserved so the agent's main loop can keep operating on
// its full message history.
func StripReasoning(messages []Message) []Message {
	out := make([]Message, 0, len(messages))
	for _, m := range messages {
		stripped := Message{Role: m.Role}
		stripped.Content = stripContentBlocks(m.Role, m.Content)
		// Drop messages that ended up empty after stripping — sending the
		// classifier a Message with zero ContentBlocks would be noise.
		if len(stripped.Content) == 0 {
			continue
		}
		out = append(out, stripped)
	}
	return out
}

// stripContentBlocks applies the two rules within a single message. Returns a
// new slice; caller decides what to do with empty results.
//
// Algorithm:
//
//  1. Find the index of the first tool_use block in the message, if any.
//  2. Walk the blocks. For each block:
//     - Drop any Type=="thinking" block (rule 1).
//     - For an assistant message that has at least one tool_use: drop any
//       text block whose index is BEFORE the first tool_use (rule 2).
//     - Keep everything else.
//
// Non-assistant messages (user, tool, system) skip rule 2 entirely — only the
// agent has internal monologue. A user's text is the user's input, period.
func stripContentBlocks(role string, blocks []ContentBlock) []ContentBlock {
	firstToolUse := -1
	if role == "assistant" {
		for i, b := range blocks {
			if b.Type == "tool_use" {
				firstToolUse = i
				break
			}
		}
	}

	out := make([]ContentBlock, 0, len(blocks))
	for i, b := range blocks {
		// Rule 1: explicit thinking blocks always go.
		if b.Type == "thinking" {
			continue
		}
		// Rule 2: assistant text BEFORE a tool_use is treated as reasoning.
		if role == "assistant" && b.Type == "text" && firstToolUse >= 0 && i < firstToolUse {
			continue
		}
		out = append(out, b)
	}
	return out
}
