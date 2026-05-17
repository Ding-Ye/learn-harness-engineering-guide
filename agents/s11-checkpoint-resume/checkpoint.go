package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Checkpoint is the serialized state of an in-flight task. It carries enough
// detail for a future process to pick up at the same turn with the same
// message history. The fields are deliberately verbose-named because they
// land in JSON files that a human may inspect with `cat` when debugging a
// crashed agent.
//
// The upstream Python at `guide/error-handling.md` L240-L259 stores a
// `task_id`, a `timestamp`, and a free-form `state` dict. We unpack that
// `state` into explicit fields (Turn, Messages, Metadata) because Go's
// type system prefers concrete shapes — but the on-disk JSON remains a
// human-readable single document with the same intent.
type Checkpoint struct {
	// TaskID names the task this checkpoint belongs to. It also doubles as
	// the filename stem on disk (`<TaskID>.json`), so it must be safe as a
	// path component — no slashes, no `..`. The Save method does NOT
	// validate this; the caller is responsible.
	TaskID string `json:"task_id"`

	// Turn is the next turn the loop should execute on resume. After turn 5
	// completes, we save `Turn=6` so the resumed loop starts at 6, not 5.
	Turn int `json:"turn"`

	// Messages is the full conversation history at checkpoint time. JSON
	// round-trips it byte-for-byte so a resumed loop sees exactly what the
	// crashed one saw.
	Messages []Message `json:"messages"`

	// Metadata is a freeform bucket for harness-specific extras (current
	// skill, sub-agent IDs, etc.). We keep it as `map[string]any` so JSON
	// will store strings/numbers/bools as-is and nested maps/arrays as
	// `map[string]any`/`[]any` — that's the standard Go JSON contract and
	// the test relies on it.
	Metadata map[string]any `json:"metadata,omitempty"`

	// Timestamp records when this checkpoint was written. Useful for
	// debugging "did the crash happen before or after the last save?".
	Timestamp time.Time `json:"timestamp"`
}

// CheckpointStore reads, writes, and clears checkpoints in a directory. One
// file per TaskID: `<dir>/<TaskID>.json`. A sync.Mutex serializes Save/Clear
// so two concurrent writers don't tangle the temp-file dance — the atomic
// rename is OS-level atomic for the final swap, but the temp file itself
// isn't shared between callers, so the lock is just defense against a
// programmer error where the same store is reused by two goroutines.
type CheckpointStore struct {
	dir string
	mu  sync.Mutex

	// writeFile is the function used to write the temp file. Tests inject a
	// faulty implementation to verify the atomic-rename invariant. nil means
	// "use the real os.WriteFile". A test-friendly seam beats a
	// half-functional global os.WriteFile shim.
	writeFile func(path string, data []byte, perm os.FileMode) error
}

// NewCheckpointStore creates a store rooted at dir. The directory is created
// with os.MkdirAll (0o755); existing dirs are left alone. Returning an error
// rather than panicking matches the upstream Python's `os.makedirs(..., exist_ok=True)`
// equivalent at L245-L246.
func NewCheckpointStore(dir string) (*CheckpointStore, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("checkpoint store: mkdir %s: %w", dir, err)
	}
	return &CheckpointStore{dir: dir}, nil
}

// path returns the canonical on-disk filename for a checkpoint. We do NOT
// call filepath.Clean on TaskID — see Checkpoint.TaskID's comment for the
// "caller's responsibility" note.
func (s *CheckpointStore) path(taskID string) string {
	return filepath.Join(s.dir, taskID+".json")
}

// tmpPath returns the temp filename used during atomic save. Keeping the
// `.tmp` suffix on the same directory means the eventual rename is on the
// same filesystem — `os.Rename` is only atomic within one filesystem.
func (s *CheckpointStore) tmpPath(taskID string) string {
	return filepath.Join(s.dir, taskID+".json.tmp")
}

// Save writes cp to disk atomically. Sequence:
//
//  1. Marshal cp to JSON.
//  2. Write the JSON to `<TaskID>.json.tmp` via writeFile (a seam; defaults
//     to os.WriteFile).
//  3. Open the temp file and fsync it so the bytes are durable before the
//     rename publishes the change.
//  4. `os.Rename` the temp file over the final path. On POSIX systems this
//     is atomic: a concurrent reader sees either the old file or the new
//     file, never a half-written one.
//
// The atomic-write pattern (from `guide/error-handling.md` L255-L259) is the
// whole point of this chapter. If step 2 fails the final file is untouched.
// If step 4 fails the temp file is left behind (callers can detect and
// clean up via Load — see L255-L256).
func (s *CheckpointStore) Save(cp *Checkpoint) error {
	if cp == nil {
		return errors.New("checkpoint store: cannot save nil checkpoint")
	}
	if cp.TaskID == "" {
		return errors.New("checkpoint store: checkpoint has empty TaskID")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	// Stamp the time at save (not at construction) so the timestamp reflects
	// reality even if the caller built the struct minutes ago.
	if cp.Timestamp.IsZero() {
		cp.Timestamp = time.Now()
	}

	data, err := json.MarshalIndent(cp, "", "  ")
	if err != nil {
		return fmt.Errorf("checkpoint store: marshal %s: %w", cp.TaskID, err)
	}

	tmp := s.tmpPath(cp.TaskID)
	final := s.path(cp.TaskID)

	// Write via the injectable seam. Real path uses os.WriteFile; tests
	// substitute a function that simulates a partial-write failure to verify
	// the final file is never corrupted by a botched save.
	writer := s.writeFile
	if writer == nil {
		writer = os.WriteFile
	}
	if err := writer(tmp, data, 0o644); err != nil {
		// Clean up any partial temp file before propagating; otherwise the
		// next Save with the same TaskID would race against a stale .tmp.
		_ = os.Remove(tmp)
		return fmt.Errorf("checkpoint store: write tmp %s: %w", tmp, err)
	}

	// fsync the temp file. Without this, the rename can succeed before the
	// bytes actually hit disk; a crash between rename and writeback would
	// publish an empty file. The cost is one syscall per checkpoint, which
	// is dwarfed by the LLM call we just made.
	if err := fsyncFile(tmp); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("checkpoint store: fsync %s: %w", tmp, err)
	}

	if err := os.Rename(tmp, final); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("checkpoint store: rename %s -> %s: %w", tmp, final, err)
	}
	return nil
}

// fsyncFile opens path read-only and calls Sync on the descriptor. We do
// this in a tiny helper so the writeFile seam doesn't need to expose a file
// handle. The function tolerates platforms where Sync is a no-op.
func fsyncFile(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}

// Load reads the checkpoint for taskID. The "missing checkpoint" case is
// deliberately NOT an error: a freshly-started task has no checkpoint yet,
// and the loop's LoadOrStartFresh flow distinguishes that case via a nil
// return value, not via err != nil.
//
// Other failures (permission denied, malformed JSON) ARE returned as errors
// — the caller cannot safely proceed without knowing the difference between
// "never started" and "checkpoint exists but is broken".
func (s *CheckpointStore) Load(taskID string) (*Checkpoint, error) {
	if taskID == "" {
		return nil, errors.New("checkpoint store: cannot load with empty TaskID")
	}
	final := s.path(taskID)
	data, err := os.ReadFile(final)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			// The "no checkpoint" case — not an error.
			return nil, nil
		}
		return nil, fmt.Errorf("checkpoint store: read %s: %w", final, err)
	}
	var cp Checkpoint
	if err := json.Unmarshal(data, &cp); err != nil {
		return nil, fmt.Errorf("checkpoint store: unmarshal %s: %w", final, err)
	}
	return &cp, nil
}

// Clear removes the checkpoint for taskID. The "already gone" case is NOT
// an error (matches upstream's `if os.path.exists(...)` guard at L271-L273):
// after a successful run we want Clear to be idempotent so cleanup code can
// call it without checking-then-removing.
//
// We do NOT clear stray `.tmp` files here. A leftover tmp from a previous
// crashed save is harmless — the next Save will overwrite it, and Load
// ignores tmp files because they don't match the `<TaskID>.json` filename.
func (s *CheckpointStore) Clear(taskID string) error {
	if taskID == "" {
		return errors.New("checkpoint store: cannot clear with empty TaskID")
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	final := s.path(taskID)
	if err := os.Remove(final); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil // idempotent
		}
		return fmt.Errorf("checkpoint store: remove %s: %w", final, err)
	}
	return nil
}
