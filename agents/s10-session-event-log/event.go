package main

import (
	"encoding/json"
	"time"
)

// Event is one entry in the append-only session log. It is the single record
// type the SessionStore knows about — every "significant point" in the harness
// (user_message, llm_call, tool_call, tool_result, error, session_end, ...) is
// serialized as one of these. The Type string is open-ended on purpose: this
// chapter introduces ~6 canonical types but the design is forward-compatible
// (a future chapter that adds, say, classifier verdicts can just emit a new
// Type without touching the store).
//
// Field order matters because we want the on-disk JSONL to be human-readable
// and stable across runs. We pin the order via a custom MarshalJSON so the
// canonical layout is:
//
//	{"timestamp":"...","session_id":"...","type":"...","data":{...}}
//
// Without MarshalJSON the Go default ordering is "by field declaration order"
// which is already what we want — but documenting it via a custom marshaller
// makes the contract unambiguous and protects against future reorderings of
// the struct fields.
type Event struct {
	Timestamp time.Time
	SessionID string
	Type      string
	// Data is the event-type-specific payload. It is held as a json.RawMessage
	// (NOT decoded into a Go type) so the store can be agnostic about what each
	// event carries — only the producer (the harness) and the consumer (the
	// replay code below) need to know the shape per Type.
	Data json.RawMessage
}

// eventJSON is the on-disk shape. Lower-case field names match the Python /
// upstream `managed-agents-architecture.md` convention. Pinning this layout via
// a separate struct is what gives us field-order stability and lets us evolve
// the in-memory Event without breaking the file format.
type eventJSON struct {
	Timestamp time.Time       `json:"timestamp"`
	SessionID string          `json:"session_id"`
	Type      string          `json:"type"`
	Data      json.RawMessage `json:"data,omitempty"`
}

// MarshalJSON pins the JSONL field order. We could rely on Go's default field
// order, but spelling it out makes the on-disk contract immune to struct
// reordering and is what a teaching codebase wants — readers can grep for the
// JSON layout in one place.
func (e Event) MarshalJSON() ([]byte, error) {
	return json.Marshal(eventJSON{
		Timestamp: e.Timestamp,
		SessionID: e.SessionID,
		Type:      e.Type,
		Data:      e.Data,
	})
}

// UnmarshalJSON is the inverse of MarshalJSON. We accept either snake_case or
// the default Go field names; in practice the file we wrote will always be
// snake_case, but reading a hand-written test fixture is friendlier with both.
func (e *Event) UnmarshalJSON(b []byte) error {
	var raw eventJSON
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	e.Timestamp = raw.Timestamp
	e.SessionID = raw.SessionID
	e.Type = raw.Type
	e.Data = raw.Data
	return nil
}

// Canonical event-type constants. These are the six types s10 emits in the
// CLI demo and the four that Replay() knows how to convert into Messages. New
// types can be added freely — the store treats Type as an opaque string.
const (
	EventUserMessage = "user_message"
	EventLLMCall     = "llm_call"
	EventToolCall    = "tool_call"
	EventToolResult  = "tool_result"
	EventError       = "error"
	EventSessionEnd  = "session_end"
)
