package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

// makeEvent fabricates an Event for tests. The timestamp is derived from `i`
// so each event has a unique, monotonic Timestamp — important for the slicing
// test where we want a deterministic order.
func makeEvent(t *testing.T, sessionID string, i int, eventType string) Event {
	t.Helper()
	data, err := json.Marshal(map[string]any{"idx": i, "text": fmt.Sprintf("msg-%d", i)})
	if err != nil {
		t.Fatalf("marshal data: %v", err)
	}
	return Event{
		Timestamp: time.Unix(int64(i), 0).UTC(),
		SessionID: sessionID,
		Type:      eventType,
		Data:      data,
	}
}

// freshStore returns a FileStore backed by t.TempDir() and registers a Cleanup
// so the FDs get released at the end of the test.
func freshStore(t *testing.T) *FileStore {
	t.Helper()
	dir := t.TempDir()
	store, err := NewFileStore(dir)
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

// TestFileStore_AppendAndReadBack: emit 10 events, read back all 10, assert
// per-event equality (Type, SessionID, Data, monotonic timestamp). The point
// of this test is to nail down the happy path of EmitEvent + GetEvents before
// the more elaborate slicing / filtering / concurrency tests below.
func TestFileStore_AppendAndReadBack(t *testing.T) {
	store := freshStore(t)
	const sessionID = "sess-A"

	want := make([]Event, 10)
	for i := 0; i < 10; i++ {
		ev := makeEvent(t, sessionID, i, EventUserMessage)
		want[i] = ev
		if err := store.EmitEvent(ev); err != nil {
			t.Fatalf("emit %d: %v", i, err)
		}
	}

	got, err := store.GetEvents(sessionID, GetEventsOpts{})
	if err != nil {
		t.Fatalf("GetEvents: %v", err)
	}
	if len(got) != 10 {
		t.Fatalf("expected 10 events, got %d", len(got))
	}
	for i := range want {
		if got[i].Type != want[i].Type {
			t.Errorf("[%d] type: got %s, want %s", i, got[i].Type, want[i].Type)
		}
		if got[i].SessionID != want[i].SessionID {
			t.Errorf("[%d] session: got %s, want %s", i, got[i].SessionID, want[i].SessionID)
		}
		if !got[i].Timestamp.Equal(want[i].Timestamp) {
			t.Errorf("[%d] timestamp: got %s, want %s", i, got[i].Timestamp, want[i].Timestamp)
		}
		// Data is json.RawMessage — compare after normalizing via re-marshal so
		// we tolerate whitespace differences across encode/decode cycles.
		var gd, wd map[string]any
		if err := json.Unmarshal(got[i].Data, &gd); err != nil {
			t.Errorf("[%d] decode got data: %v", i, err)
			continue
		}
		if err := json.Unmarshal(want[i].Data, &wd); err != nil {
			t.Errorf("[%d] decode want data: %v", i, err)
			continue
		}
		if !reflect.DeepEqual(gd, wd) {
			t.Errorf("[%d] data: got %v, want %v", i, gd, wd)
		}
	}

	// Sanity: the file exists at the documented path.
	path := filepath.Join(store.dir, "sessions", sessionID+".jsonl")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expected file at %s, stat err: %v", path, err)
	}
}

// TestFileStore_GetEventsSlicing: emit 100 events, ask for {Offset:50, Limit:10},
// confirm we get events 50..59 in order. This is the canonical "show me page N"
// query that the upstream interface promises.
func TestFileStore_GetEventsSlicing(t *testing.T) {
	store := freshStore(t)
	const sessionID = "sess-slice"

	for i := 0; i < 100; i++ {
		ev := makeEvent(t, sessionID, i, EventLLMCall)
		if err := store.EmitEvent(ev); err != nil {
			t.Fatalf("emit %d: %v", i, err)
		}
	}

	got, err := store.GetEvents(sessionID, GetEventsOpts{Offset: 50, Limit: 10})
	if err != nil {
		t.Fatalf("GetEvents: %v", err)
	}
	if len(got) != 10 {
		t.Fatalf("expected 10 events, got %d", len(got))
	}
	for i, ev := range got {
		// Decode the data to recover the index, then check it.
		var d struct {
			Idx int `json:"idx"`
		}
		if err := json.Unmarshal(ev.Data, &d); err != nil {
			t.Fatalf("[%d] decode data: %v", i, err)
		}
		wantIdx := 50 + i
		if d.Idx != wantIdx {
			t.Errorf("[%d] expected idx=%d, got %d", i, wantIdx, d.Idx)
		}
	}

	// Bonus: Offset past end returns empty slice, not error.
	empty, err := store.GetEvents(sessionID, GetEventsOpts{Offset: 200})
	if err != nil {
		t.Fatalf("GetEvents past end: %v", err)
	}
	if len(empty) != 0 {
		t.Errorf("Offset past end should return empty, got %d events", len(empty))
	}
}

// TestFileStore_GetEventsTypeFilter: emit a 30-event mix (10 each of
// user_message, llm_call, tool_result), filter to [tool_result] only, expect
// exactly 10 tool_result events back in order.
func TestFileStore_GetEventsTypeFilter(t *testing.T) {
	store := freshStore(t)
	const sessionID = "sess-filter"

	types := []string{EventUserMessage, EventLLMCall, EventToolResult}
	for round := 0; round < 10; round++ {
		for ti, tp := range types {
			ev := makeEvent(t, sessionID, round*3+ti, tp)
			if err := store.EmitEvent(ev); err != nil {
				t.Fatalf("emit: %v", err)
			}
		}
	}

	got, err := store.GetEvents(sessionID, GetEventsOpts{
		TypeFilter: []string{EventToolResult},
	})
	if err != nil {
		t.Fatalf("GetEvents: %v", err)
	}
	if len(got) != 10 {
		t.Fatalf("expected 10 tool_result events, got %d", len(got))
	}
	for i, ev := range got {
		if ev.Type != EventToolResult {
			t.Errorf("[%d] expected tool_result, got %s", i, ev.Type)
		}
	}

	// Multi-type filter: tool_result OR llm_call → 20 events.
	multi, err := store.GetEvents(sessionID, GetEventsOpts{
		TypeFilter: []string{EventToolResult, EventLLMCall},
	})
	if err != nil {
		t.Fatalf("GetEvents multi: %v", err)
	}
	if len(multi) != 20 {
		t.Fatalf("expected 20 multi-filter events, got %d", len(multi))
	}
}

// TestFileStore_ConcurrentEmitSafe: 100 goroutines each emit one event into
// the same session. The final file must hold exactly 100 valid JSON lines, no
// partial writes, no interleaving. Run with -race to catch any lock omissions.
func TestFileStore_ConcurrentEmitSafe(t *testing.T) {
	store := freshStore(t)
	const sessionID = "sess-concurrent"
	const n = 100

	var wg sync.WaitGroup
	wg.Add(n)
	start := make(chan struct{}) // release all goroutines simultaneously
	for i := 0; i < n; i++ {
		go func(i int) {
			defer wg.Done()
			<-start
			ev := makeEvent(t, sessionID, i, EventToolCall)
			if err := store.EmitEvent(ev); err != nil {
				t.Errorf("emit %d: %v", i, err)
			}
		}(i)
	}
	close(start)
	wg.Wait()

	// Validate the on-disk file: every line must be parseable JSON, and there
	// must be exactly n lines. We read the raw bytes (not via GetEvents) to
	// confirm the file format is well-formed independent of our decoder.
	path := filepath.Join(store.dir, "sessions", sessionID+".jsonl")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	lines := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	if len(lines) != n {
		t.Fatalf("expected %d lines, got %d", n, len(lines))
	}
	seen := make(map[int]bool, n)
	for i, line := range lines {
		if line == "" {
			t.Errorf("line %d is empty", i)
			continue
		}
		var ev Event
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			t.Errorf("line %d invalid JSON: %v (line=%q)", i, err, line)
			continue
		}
		var d struct {
			Idx int `json:"idx"`
		}
		if err := json.Unmarshal(ev.Data, &d); err != nil {
			t.Errorf("line %d decode data: %v", i, err)
			continue
		}
		seen[d.Idx] = true
	}
	if len(seen) != n {
		t.Errorf("expected %d distinct indices, got %d (some emits collided?)", n, len(seen))
	}
}

// TestReplay_ReconstructsMessageHistory: emit a scripted 6-event sequence,
// run Replay, assert the reconstructed Message slice exactly matches what the
// conversion table specifies. This test is the contract for downstream code
// that depends on Replay's output (e.g., harness rehydration on restart).
func TestReplay_ReconstructsMessageHistory(t *testing.T) {
	store := freshStore(t)
	const sessionID = "sess-replay"

	base := time.Date(2026, 5, 17, 9, 0, 0, 0, time.UTC)
	scripted := []struct {
		offset int
		typ    string
		data   map[string]any
	}{
		{0, EventUserMessage, map[string]any{"text": "hello"}},
		{1, EventLLMCall, map[string]any{"text": "I'll help."}},
		{2, EventToolCall, map[string]any{"name": "read_file", "args": map[string]string{"path": "x.txt"}}},
		{3, EventToolResult, map[string]any{"output": "file contents here"}},
		{4, EventLLMCall, map[string]any{"text": "done."}},
		{5, EventSessionEnd, map[string]any{"reason": "end_turn"}},
	}
	for _, s := range scripted {
		raw, err := json.Marshal(s.data)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		ev := Event{
			Timestamp: base.Add(time.Duration(s.offset) * time.Second),
			SessionID: sessionID,
			Type:      s.typ,
			Data:      raw,
		}
		if err := store.EmitEvent(ev); err != nil {
			t.Fatalf("emit %s: %v", s.typ, err)
		}
	}

	msgs, err := Replay(store, sessionID)
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}
	want := []Message{
		{Role: "user", Content: "hello"},
		{Role: "assistant", Content: "I'll help."},
		{Role: "tool", Content: "file contents here"},
		{Role: "assistant", Content: "done."},
	}
	if !reflect.DeepEqual(msgs, want) {
		t.Fatalf("Replay output mismatch.\n got: %#v\nwant: %#v", msgs, want)
	}

	// Bonus: events without the expected field are skipped silently. Emit one
	// tool_result with no "output" key and confirm the replay length doesn't
	// grow.
	badRaw, _ := json.Marshal(map[string]any{"not_output": "oops"})
	if err := store.EmitEvent(Event{
		Timestamp: base.Add(6 * time.Second),
		SessionID: sessionID,
		Type:      EventToolResult,
		Data:      badRaw,
	}); err != nil {
		t.Fatalf("emit bad: %v", err)
	}
	msgs2, err := Replay(store, sessionID)
	if err != nil {
		t.Fatalf("Replay second: %v", err)
	}
	if len(msgs2) != len(want) {
		t.Fatalf("malformed tool_result should be skipped; got %d msgs, want %d", len(msgs2), len(want))
	}
}
