package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

// newStore is a test helper that creates a CheckpointStore over t.TempDir().
// Using t.TempDir() means files are cleaned up automatically at test end and
// each test gets its own scratch space — no cross-test contamination.
func newStore(t *testing.T) *CheckpointStore {
	t.Helper()
	dir := t.TempDir()
	s, err := NewCheckpointStore(dir)
	if err != nil {
		t.Fatalf("NewCheckpointStore: %v", err)
	}
	return s
}

// TestCheckpoint_SaveAndLoadRoundTrip: write a fully-populated Checkpoint to
// disk and read it back. The loaded struct must equal the original (modulo
// the Timestamp field, which Save normalizes if zero). reflect.DeepEqual
// catches both shape mismatches and JSON encoder weirdness (e.g. losing the
// distinction between nil and empty slices).
func TestCheckpoint_SaveAndLoadRoundTrip(t *testing.T) {
	store := newStore(t)
	original := &Checkpoint{
		TaskID: "task-rt",
		Turn:   7,
		Messages: []Message{
			{Role: "user", Content: "hello"},
			{Role: "assistant", Content: "world"},
		},
		Metadata: map[string]any{
			"phase":  "explore",
			"budget": float64(100), // JSON numbers come back as float64
		},
		Timestamp: time.Date(2026, 5, 17, 12, 0, 0, 0, time.UTC),
	}
	if err := store.Save(original); err != nil {
		t.Fatalf("Save: %v", err)
	}

	loaded, err := store.Load("task-rt")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded == nil {
		t.Fatal("Load returned nil after Save")
	}

	// Compare via DeepEqual. We compare each top-level field separately for
	// better failure messages — DeepEqual on the whole struct would print a
	// wall of text on failure.
	if loaded.TaskID != original.TaskID {
		t.Errorf("TaskID mismatch: got %q, want %q", loaded.TaskID, original.TaskID)
	}
	if loaded.Turn != original.Turn {
		t.Errorf("Turn mismatch: got %d, want %d", loaded.Turn, original.Turn)
	}
	if !reflect.DeepEqual(loaded.Messages, original.Messages) {
		t.Errorf("Messages mismatch:\n got  %+v\n want %+v", loaded.Messages, original.Messages)
	}
	if !reflect.DeepEqual(loaded.Metadata, original.Metadata) {
		t.Errorf("Metadata mismatch:\n got  %+v\n want %+v", loaded.Metadata, original.Metadata)
	}
	if !loaded.Timestamp.Equal(original.Timestamp) {
		t.Errorf("Timestamp mismatch: got %v, want %v", loaded.Timestamp, original.Timestamp)
	}
}

// TestCheckpoint_AtomicWriteSurvivesPartialWrite is the keystone test. It
// proves the atomic-write contract: if Save fails mid-write, the file at
// `<TaskID>.json` is left in its previous good state — never half-written,
// never empty, never containing partially-marshaled JSON.
//
// Approach:
//
//  1. Save a "known good" checkpoint A. Confirm it's on disk.
//  2. Inject a faulty writeFile that simulates a partial write (writes a few
//     bytes then returns an error). The temp file may exist briefly with bad
//     content, but Save should clean it up and propagate the error.
//  3. Attempt to Save checkpoint B with the faulty writer. Expect an error.
//  4. Load taskID: should return checkpoint A (or, depending on the failure
//     point, nil if the rename was never attempted). Never half-B.
//
// We then verify the final file matches A byte-for-byte via the loaded
// struct.
func TestCheckpoint_AtomicWriteSurvivesPartialWrite(t *testing.T) {
	store := newStore(t)
	good := &Checkpoint{
		TaskID:   "task-atomic",
		Turn:     3,
		Messages: []Message{{Role: "user", Content: "good message"}},
		Metadata: map[string]any{"version": "A"},
	}
	if err := store.Save(good); err != nil {
		t.Fatalf("Save good: %v", err)
	}

	// Capture A's bytes on disk so we can confirm they're unchanged later.
	finalPath := store.path("task-atomic")
	goodBytes, err := os.ReadFile(finalPath)
	if err != nil {
		t.Fatalf("ReadFile(good): %v", err)
	}

	// Inject the faulty writer. It writes a SHORT (truncated) version of the
	// data to the temp file, then returns an error. This is exactly what a
	// disk-full scenario looks like to a Go program.
	store.writeFile = func(path string, data []byte, perm os.FileMode) error {
		// Write only the first half of the data, then claim failure.
		half := data[:len(data)/2]
		if err := os.WriteFile(path, half, perm); err != nil {
			return err
		}
		return errors.New("simulated partial write failure")
	}

	bad := &Checkpoint{
		TaskID:   "task-atomic",
		Turn:     99,
		Messages: []Message{{Role: "user", Content: "BAD CONTENT — must not be readable"}},
		Metadata: map[string]any{"version": "B"},
	}
	err = store.Save(bad)
	if err == nil {
		t.Fatalf("Save with faulty writer returned no error; expected one")
	}

	// Reset the writer so subsequent Loads aren't sabotaged.
	store.writeFile = nil

	// The final file must still contain A.
	stillBytes, err := os.ReadFile(finalPath)
	if err != nil {
		t.Fatalf("ReadFile(final): %v", err)
	}
	if string(stillBytes) != string(goodBytes) {
		t.Errorf("final file changed after failed Save:\n was  %s\n now  %s",
			string(goodBytes), string(stillBytes))
	}

	// And Load must return A's struct, not B's.
	loaded, err := store.Load("task-atomic")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded == nil {
		t.Fatal("Load returned nil after successful Save(A)")
	}
	if loaded.Turn != 3 {
		t.Errorf("loaded.Turn = %d, want 3 (A's value); got %d means a partial B leaked", loaded.Turn, loaded.Turn)
	}
	if v, _ := loaded.Metadata["version"].(string); v != "A" {
		t.Errorf("loaded.Metadata[version] = %v, want A; B's data leaked through", v)
	}

	// Also confirm the temp file was cleaned up — no `.tmp` left behind.
	tmpPath := store.tmpPath("task-atomic")
	if _, err := os.Stat(tmpPath); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("expected tmp file %s to be cleaned up, found stat err=%v", tmpPath, err)
	}
}

// TestCheckpoint_LoadMissingReturnsNilNoError: the "no checkpoint" case must
// be (nil, nil), not an error. The Loop's LoadOrStartFresh relies on this:
// it interprets nil as "fresh start" and an error as "something broke on
// disk and we should NOT silently start over".
func TestCheckpoint_LoadMissingReturnsNilNoError(t *testing.T) {
	store := newStore(t)
	cp, err := store.Load("nonexistent")
	if err != nil {
		t.Fatalf("Load(missing): got err=%v, want nil", err)
	}
	if cp != nil {
		t.Fatalf("Load(missing): got cp=%+v, want nil", cp)
	}
}

// TestLoop_ResumesFromCheckpoint is the chapter's headline integration test.
//
// Sequence:
//  1. Run loop with PanicAtTurn=6 and CheckpointEvery=5. Turn 5's save lands
//     before the panic. We recover() and inspect the checkpoint.
//  2. Run a SECOND loop with the same taskID. LoadOrStartFresh should
//     return Turn=5 (the saved cadence-checkpoint after turn 4 completed).
//     The second provider's Calls count tells us how many turns it actually
//     ran — that should be `script_len - resume_turn`.
//
// The exact resume turn depends on the CheckpointEvery cadence. We save
// `Turn = turn+1` after every Nth turn, so with CheckpointEvery=5 and
// PanicAtTurn=6 the on-disk Turn after the crash is 5 (saved at end of turn
// 4) — turn 5 itself appended a message but the next cadence save is at end
// of turn 9, so turn-5's append never made it to disk. Resume restarts at
// turn 5.
func TestLoop_ResumesFromCheckpoint(t *testing.T) {
	store := newStore(t)
	taskID := "task-resume"
	script := []string{
		"plan", "read", "draft", "fix", "test", // 0..4
		"refine", "polish", "summarize",         // 5..7
	}

	// First run: crash at turn 6.
	p1 := &MockProvider{Script: script, PanicAtTurn: -1, FailAtTurn: -1}
	loop1 := &Loop{
		Store:           store,
		Provider:        p1,
		MaxTurns:        20,
		CheckpointEvery: 5,
		PanicAtTurn:     6,
	}
	func() {
		defer func() {
			_ = recover() // expected: loop panics at turn 6
		}()
		_, _ = loop1.Run(context.Background(), taskID, "the user prompt")
	}()
	// After the panic, the checkpoint from end-of-turn-4 (saved as Turn=5)
	// must be on disk.
	cp, err := store.Load(taskID)
	if err != nil {
		t.Fatalf("Load after crash: %v", err)
	}
	if cp == nil {
		t.Fatalf("expected a checkpoint after crash, got nil")
	}
	if cp.Turn != 5 {
		t.Errorf("checkpoint.Turn = %d, want 5 (saved at end of turn 4)", cp.Turn)
	}
	// Sanity: that checkpoint should have history through turn 4's
	// assistant message. The history is [user] + 5 assistant messages = 6.
	if len(cp.Messages) != 6 {
		t.Errorf("checkpoint.Messages len = %d, want 6 (user + 5 assistant)", len(cp.Messages))
	}

	// Second run: same taskID, fresh provider.
	p2 := &MockProvider{Script: script, PanicAtTurn: -1, FailAtTurn: -1}
	loop2 := &Loop{
		Store:           store,
		Provider:        p2,
		MaxTurns:        20,
		CheckpointEvery: 5,
		PanicAtTurn:     -1,
	}
	history, err := loop2.Run(context.Background(), taskID, "the user prompt")
	if err != nil {
		t.Fatalf("Run 2: %v", err)
	}

	// The second provider should have been called for turns 5, 6, 7 (script
	// entries) + one more call at turn 8 where script runs out and the
	// provider reports done=true. Total = 4 calls. Critically it should NOT
	// be 9 (turns 0..8) — that would mean we re-ran turns 0-4 needlessly.
	if p2.Calls != 4 {
		t.Errorf("provider2.Calls = %d, want 4 (turns 5-8 only — earlier turns should be skipped)", p2.Calls)
	}

	// And the final history should include the resumed turns: 1 user + 5
	// assistant from run 1's checkpoint + 4 assistant from run 2 (3 script
	// outputs + 1 "task complete") = 10.
	if len(history) != 10 {
		t.Errorf("final history len = %d, want 10", len(history))
	}

	// Final checkpoint should be cleared on graceful exit.
	cp2, err := store.Load(taskID)
	if err != nil {
		t.Fatalf("Load after success: %v", err)
	}
	if cp2 != nil {
		t.Errorf("expected checkpoint cleared after success, got %+v", cp2)
	}
}

// TestCheckpoint_ClearAfterSuccess: a happy-path run (no panics, provider
// reports done) must end with no checkpoint file on disk. The Loop calls
// store.Clear after the provider signals done; this test confirms that
// cleanup is wired and the file goes away.
func TestCheckpoint_ClearAfterSuccess(t *testing.T) {
	store := newStore(t)
	taskID := "task-clear"
	script := []string{"a", "b", "c"} // just 3 turns, no panic
	p := &MockProvider{Script: script, PanicAtTurn: -1, FailAtTurn: -1}
	loop := &Loop{
		Store:           store,
		Provider:        p,
		MaxTurns:        10,
		CheckpointEvery: 5,
		PanicAtTurn:     -1,
	}

	// Pre-populate a checkpoint to confirm Clear actually removes it. We
	// stamp it via the store rather than running a crash test because the
	// "3-turn script with CheckpointEvery=5" path wouldn't otherwise trigger
	// a save (cadence boundary not hit). The end-of-task Clear must still
	// kill that pre-existing file.
	if err := store.Save(&Checkpoint{
		TaskID:   taskID,
		Turn:     1,
		Messages: []Message{{Role: "user", Content: "old"}},
	}); err != nil {
		t.Fatalf("pre-save: %v", err)
	}

	_, err := loop.Run(context.Background(), taskID, "go")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	// File should be gone.
	finalPath := store.path(taskID)
	if _, err := os.Stat(finalPath); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("expected checkpoint file %s removed; stat err = %v", finalPath, err)
	}

	// And Load should match the contract: (nil, nil) for missing.
	cp, err := store.Load(taskID)
	if err != nil {
		t.Fatalf("Load after success: %v", err)
	}
	if cp != nil {
		t.Errorf("Load after success: got %+v, want nil", cp)
	}
}

// TestCheckpoint_AtomicFileIsValidJSON sanity-checks that what we write IS
// valid JSON that a `jq` or `cat` user could read. We don't make a separate
// promise about the on-disk shape, but a human inspecting `<TaskID>.json`
// must see something parseable; otherwise the debugging workflow upstream
// describes ("inspect the checkpoint file") is broken.
//
// Reason this isn't merged into the round-trip test: round-trip uses
// json.Unmarshal which is generous; this test runs the file through
// json.Valid to assert the bytes themselves are syntactically right, with
// no Unicode mishaps or trailing junk.
func TestCheckpoint_OnDiskJSONIsValid(t *testing.T) {
	store := newStore(t)
	cp := &Checkpoint{
		TaskID:   "task-validity",
		Turn:     0,
		Messages: []Message{{Role: "user", Content: "hello"}},
	}
	if err := store.Save(cp); err != nil {
		t.Fatalf("Save: %v", err)
	}
	finalPath := filepath.Join(store.dir, "task-validity.json")
	data, err := os.ReadFile(finalPath)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if !json.Valid(data) {
		t.Errorf("on-disk checkpoint is not valid JSON:\n%s", string(data))
	}
}
