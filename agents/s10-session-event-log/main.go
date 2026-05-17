package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// main is a CLI demo for the session event log. It emits 6 events into a
// fresh session, lists everything back via GetEvents, then runs Replay to
// reconstruct what a re-hydrated harness would see.
//
// Usage:
//
//	go run .                  # uses os.MkdirTemp("", "s10-*")
//	go run . /tmp/my-sessions # uses /tmp/my-sessions
//
// The output is shaped to be useful as smoke-test reading material — you can
// re-run it, see the JSONL files at the printed path, and `cat` them to
// confirm the format matches the docs.
func main() {
	var dir string
	if len(os.Args) > 1 {
		dir = os.Args[1]
	} else {
		d, err := os.MkdirTemp("", "s10-*")
		if err != nil {
			fmt.Fprintln(os.Stderr, "mktemp:", err)
			os.Exit(1)
		}
		dir = d
	}

	store, err := NewFileStore(dir)
	if err != nil {
		fmt.Fprintln(os.Stderr, "open store:", err)
		os.Exit(1)
	}
	defer func() {
		if err := store.Close(); err != nil {
			fmt.Fprintln(os.Stderr, "close:", err)
		}
	}()

	const sessionID = "demo-001"
	now := time.Date(2026, 5, 17, 12, 0, 0, 0, time.UTC)

	// Six events that walk through one canonical loop turn end-to-end:
	// user_message → llm_call → tool_call → tool_result → llm_call → session_end.
	// (We skip emitting `error` so the happy-path replay is clean.)
	events := []Event{
		newEvent(sessionID, now.Add(0*time.Second), EventUserMessage,
			map[string]any{"text": "Read data.json and summarize it"}),
		newEvent(sessionID, now.Add(1*time.Second), EventLLMCall,
			map[string]any{"text": "I'll read the file first."}),
		newEvent(sessionID, now.Add(2*time.Second), EventToolCall,
			map[string]any{"name": "read_file", "args": map[string]string{"path": "data.json"}}),
		newEvent(sessionID, now.Add(3*time.Second), EventToolResult,
			map[string]any{"output": `{"name":"Ada","score":42}`}),
		newEvent(sessionID, now.Add(4*time.Second), EventLLMCall,
			map[string]any{"text": "The file contains Ada (score 42). Task complete."}),
		newEvent(sessionID, now.Add(5*time.Second), EventSessionEnd,
			map[string]any{"reason": "end_turn"}),
	}

	fmt.Printf("=== writing 6 events to %s ===\n", filepath.Join(dir, "sessions", sessionID+".jsonl"))
	for _, ev := range events {
		if err := store.EmitEvent(ev); err != nil {
			fmt.Fprintln(os.Stderr, "emit:", err)
			os.Exit(1)
		}
		fmt.Printf("  emit %s\n", ev.Type)
	}

	// GetEvents — show the raw event list, no filtering.
	fmt.Printf("\n=== GetEvents(opts={}) — full log ===\n")
	all, err := store.GetEvents(sessionID, GetEventsOpts{})
	if err != nil {
		fmt.Fprintln(os.Stderr, "get:", err)
		os.Exit(1)
	}
	for i, ev := range all {
		fmt.Printf("  [%d] %s  type=%-13s data=%s\n",
			i, ev.Timestamp.Format(time.RFC3339), ev.Type, string(ev.Data))
	}

	// GetEvents with a type filter — illustrative of the API.
	fmt.Printf("\n=== GetEvents(TypeFilter=[tool_result]) ===\n")
	filtered, err := store.GetEvents(sessionID, GetEventsOpts{TypeFilter: []string{EventToolResult}})
	if err != nil {
		fmt.Fprintln(os.Stderr, "get filtered:", err)
		os.Exit(1)
	}
	for i, ev := range filtered {
		fmt.Printf("  [%d] %s  data=%s\n", i, ev.Timestamp.Format(time.RFC3339), string(ev.Data))
	}

	// Replay — show what a re-hydrated harness would feed back to the model.
	msgs, err := Replay(store, sessionID)
	if err != nil {
		fmt.Fprintln(os.Stderr, "replay:", err)
		os.Exit(1)
	}
	fmt.Printf("\n=== Replay — reconstructed message history (%d messages) ===\n", len(msgs))
	for i, m := range msgs {
		fmt.Printf("  [%d] role=%-9s content=%q\n", i, m.Role, m.Content)
	}

	fmt.Printf("\nDemo dir: %s\n", dir)
}

// newEvent is a tiny helper for the demo above. It marshals `data` once so the
// per-event boilerplate stays terse. Errors are panicked because this is demo
// code; in production each event would surface its marshal error via a return.
func newEvent(sessionID string, ts time.Time, eventType string, data map[string]any) Event {
	raw, err := json.Marshal(data)
	if err != nil {
		panic(fmt.Sprintf("newEvent(%s): %v", eventType, err))
	}
	return Event{
		Timestamp: ts,
		SessionID: sessionID,
		Type:      eventType,
		Data:      raw,
	}
}
