package main

// SessionStore is the boundary between the harness (event producer) and the
// durable session log (event consumer). The contract is small on purpose:
// EmitEvent appends ONE event, GetEvents reads back a slice, Close releases
// resources. Anything more elaborate (queries, indexes, retention policies)
// belongs in a higher layer.
//
// The upstream guide's `managed-agents-architecture.md` L74-L83 phrases the
// interface as:
//
//	emitEvent(sessionId, event)
//	getEvents(sessionId, options)
//	getSession(sessionId)            // not implemented here — out of scope
//
// We collapse the first two into Go method shapes and skip the metadata
// accessor for s10. The third one would be a one-liner that returns the
// directory listing; we leave it for the reader as an exercise.
type SessionStore interface {
	// EmitEvent appends a single event to the session identified by
	// ev.SessionID. The implementation MUST be safe to call concurrently from
	// multiple goroutines and MUST be durable: when EmitEvent returns nil, the
	// event has hit the disk (or whatever the backing store is).
	EmitEvent(ev Event) error

	// GetEvents reads back the event slice for a session. Offset/Limit/
	// TypeFilter let the caller skip events, cap the response size, and only
	// return certain event types — useful for replay code that only cares about
	// tool_result events, or for an observability dashboard paginating through
	// the log. See GetEventsOpts for semantics.
	GetEvents(sessionID string, opts GetEventsOpts) ([]Event, error)

	// Close releases any resources the store is holding (file handles,
	// background goroutines, ...). Calling EmitEvent after Close is undefined
	// behavior in the contract — the FileStore implementation below makes it a
	// soft error.
	Close() error
}

// GetEventsOpts controls how GetEvents slices the underlying event list.
// All three fields are optional; the zero value means "all events".
type GetEventsOpts struct {
	// Offset is the number of events to skip BEFORE applying the type filter.
	// This matches the upstream "positional slicing" wording: callers think of
	// the log as a position-indexed array, not a query-language matrix.
	Offset int

	// Limit is the maximum number of events to return after type filtering.
	// Zero means "no limit" (return everything past Offset).
	Limit int

	// TypeFilter, when non-empty, restricts results to events whose Type is in
	// the slice. The filter is applied AFTER Offset, which matches the upstream
	// semantics ("show me the next 10 user_message events past position 100").
	TypeFilter []string
}
