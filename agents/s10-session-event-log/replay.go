package main

import (
	"encoding/json"
	"fmt"
	"sort"
)

// Message is the simple "what the LLM would see if we rebuilt context" shape.
// Like in s09 it deliberately uses a flat Content string instead of the rich
// ContentBlock list — Replay()'s job is to reconstruct enough message history
// to demonstrate the principle, not to be a drop-in replacement for the loop's
// own message representation. A real harness would replay into the same
// `[]ContentBlock` form its loop uses; we keep the demo simple.
type Message struct {
	// Role is one of "user", "assistant", or "tool" — same vocabulary the
	// upstream LLM APIs use. We deliberately do NOT emit "system" because the
	// system prompt is not in the event log; it's a property of the harness
	// configuration.
	Role string

	// Content is the human-readable text of the message. For tool_result events
	// this is the tool's output; for user_message and llm_call it's the
	// model/user-typed text.
	Content string
}

// Replay reconstructs the message history for sessionID by reading every event
// in the log, sorting by Timestamp, and converting each meaningful event into
// a Message. The conversion table is:
//
//	user_message  → Message{Role: "user",      Content: data.text}
//	llm_call      → Message{Role: "assistant", Content: data.text}
//	tool_result   → Message{Role: "tool",      Content: data.output}
//
// Everything else (`tool_call`, `error`, `session_end`, plus any future event
// types) is intentionally skipped. The reasoning:
//
//   - tool_call is the *request* the model made; the corresponding tool_result
//     carries the actual observable text. A naive replay that included
//     tool_call would double the assistant's turn weight.
//   - error events are diagnostic; including them in a re-played message
//     history would confuse the model on the next turn.
//   - session_end is a marker, not a turn.
//
// Production replay code would likely want a richer flag set (e.g., "include
// tool_call as assistant.tool_use") but for the teaching scope this minimal
// reconstruction is enough to demonstrate the durability property.
func Replay(store SessionStore, sessionID string) ([]Message, error) {
	events, err := store.GetEvents(sessionID, GetEventsOpts{})
	if err != nil {
		return nil, fmt.Errorf("replay: GetEvents: %w", err)
	}
	// Stable-sort by timestamp. The file store appends in emit order, which is
	// usually already timestamp-monotonic, but a sort here makes Replay robust
	// against out-of-order emits (e.g., a sub-agent flushing events after its
	// parent has moved on).
	sort.SliceStable(events, func(i, j int) bool {
		return events[i].Timestamp.Before(events[j].Timestamp)
	})

	msgs := make([]Message, 0, len(events))
	for _, ev := range events {
		switch ev.Type {
		case EventUserMessage:
			text, ok := extractText(ev.Data, "text")
			if !ok {
				continue
			}
			msgs = append(msgs, Message{Role: "user", Content: text})
		case EventLLMCall:
			text, ok := extractText(ev.Data, "text")
			if !ok {
				continue
			}
			msgs = append(msgs, Message{Role: "assistant", Content: text})
		case EventToolResult:
			text, ok := extractText(ev.Data, "output")
			if !ok {
				continue
			}
			msgs = append(msgs, Message{Role: "tool", Content: text})
		default:
			// tool_call, error, session_end, unknown — skip on purpose.
			continue
		}
	}
	return msgs, nil
}

// extractText pulls a single string field out of an event's Data payload. The
// Data is a json.RawMessage so we have to do an ad-hoc unmarshal; we keep it
// in a typed helper so the per-Type branches in Replay stay readable.
//
// Returns (text, ok). ok is false when the payload is missing, empty, or
// doesn't contain the named field — in which case Replay skips the event
// silently. A stricter implementation could surface this as an error, but for
// a teaching log we'd rather get a partial replay than a hard failure.
func extractText(data json.RawMessage, field string) (string, bool) {
	if len(data) == 0 {
		return "", false
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		return "", false
	}
	v, ok := raw[field]
	if !ok {
		return "", false
	}
	s, ok := v.(string)
	if !ok {
		return "", false
	}
	return s, true
}
