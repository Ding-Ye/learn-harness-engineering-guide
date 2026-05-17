package main

// Local copies of the canonical message shapes. We deliberately do NOT import
// from s02 — the curriculum's isolation rule is that every chapter ships its
// own copy of the types it touches, so a reader landing on this chapter can
// follow the code without paging back. The shapes mirror s02's Anthropic-style
// types (Role + []ContentBlock) with one omission: s09 doesn't need the Input
// field on tool_use blocks, so we leave it out to keep the diff small.

// Message is one entry in the conversation history. The Role is one of
// "system", "user", "assistant", or "tool"; system messages get special
// treatment by the sliding window — they are NEVER compressed.
type Message struct {
	Role    string
	Content []ContentBlock
}

// ContentBlock is one element of a message's content list. The Type selects
// which other fields are populated:
//
//	"text"        → Text
//	"tool_use"    → ID, Name
//	"tool_result" → ID, Content, IsError
//
// We omit s02's `Input json.RawMessage` because compression cares about token
// volume, not tool argument structure. If the field were present we'd have to
// re-marshal it for token estimation; leaving it out keeps EstimateTokens a
// pure string-walker.
type ContentBlock struct {
	Type    string // "text" | "tool_use" | "tool_result"
	Text    string // when Type=="text"
	ID      string // tool_use id (assistant) / tool_use_id (tool result)
	Name    string // when Type=="tool_use"
	Content string // when Type=="tool_result"
	IsError bool   // when Type=="tool_result"
}
