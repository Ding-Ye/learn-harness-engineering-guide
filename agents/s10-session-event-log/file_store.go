package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
)

// FileStore is the only SessionStore implementation s10 ships. It writes one
// `<sessionID>.jsonl` file per session, append-only, one JSON event per line.
// Files are opened lazily on first EmitEvent and cached in `openFiles` so we
// don't pay an open(2) syscall on every event. A single mutex guards both the
// cache and the append step; that's slightly conservative (we could shard by
// session ID) but adequate for the teaching scope and trivially correct.
//
// File format (one event per line):
//
//	{"timestamp":"2026-05-17T12:34:56Z","session_id":"s1","type":"user_message","data":{"text":"hi"}}
//	{"timestamp":"2026-05-17T12:34:57Z","session_id":"s1","type":"llm_call","data":{"text":"hello"}}
//
// On-disk layout (e.g. dir="/tmp/sess"):
//
//	/tmp/sess/sessions/<sessionID>.jsonl
//
// The literal `sessions/` subdir matches the upstream `managed-agents-
// architecture.md` L74 phrasing. The dir given to NewFileStore is the parent;
// the subdir is created on first emit. This lets the test pass a single
// t.TempDir() and not have to think about subdirectories.
type FileStore struct {
	// dir is the root the caller passed to NewFileStore. The actual JSONL files
	// live at filepath.Join(dir, "sessions", sessionID+".jsonl"). We keep the
	// root in a field so we can recompute the per-session path lazily.
	dir string

	// mu guards both openFiles and the file-write step itself. Holding the lock
	// across the os.OpenFile + Write + Sync sequence keeps the implementation
	// correct under concurrent EmitEvent calls without needing per-file locks.
	mu sync.Mutex

	// openFiles caches one *os.File per session. Re-using the FD across emits
	// saves the open(2) cost (a few microseconds on Linux, more on macOS).
	// Files are closed on Close(). A future improvement would be to evict idle
	// FDs after a TTL; the teaching implementation keeps everything until
	// shutdown.
	openFiles map[string]*os.File

	// closed flips to true when Close() runs. Subsequent EmitEvent calls return
	// an error rather than panicking on a closed FD.
	closed bool
}

// NewFileStore returns a FileStore rooted at dir, creating dir and the
// `sessions/` subdirectory if they don't exist. We MkdirAll up-front so the
// caller gets the failure mode (bad path, missing perms) at construction
// time rather than at the first EmitEvent.
func NewFileStore(dir string) (*FileStore, error) {
	if dir == "" {
		return nil, errors.New("filestore: dir is required")
	}
	sub := filepath.Join(dir, "sessions")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		return nil, fmt.Errorf("filestore: mkdir %s: %w", sub, err)
	}
	return &FileStore{
		dir:       dir,
		openFiles: make(map[string]*os.File),
	}, nil
}

// pathFor returns the absolute JSONL path for sessionID. We don't validate the
// session ID for shell-safety here — the teaching implementation accepts any
// string that's a valid path component. A production version would reject "/"
// and "..".
func (s *FileStore) pathFor(sessionID string) string {
	return filepath.Join(s.dir, "sessions", sessionID+".jsonl")
}

// openLocked returns the cached *os.File for sessionID, opening (and caching)
// it on miss. MUST be called with s.mu held.
//
// We use O_APPEND|O_CREATE|O_WRONLY so the write is positioned at end-of-file
// even if another process has written since we opened. On POSIX systems the
// kernel guarantees that an O_APPEND write of less than PIPE_BUF (usually 4KiB
// or more) is atomic with respect to other O_APPEND writers — so even if the
// mutex didn't protect us, two writers wouldn't interleave bytes. We still
// hold the mutex because we ALSO mutate openFiles and the closed flag.
func (s *FileStore) openLocked(sessionID string) (*os.File, error) {
	if f, ok := s.openFiles[sessionID]; ok {
		return f, nil
	}
	path := s.pathFor(sessionID)
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	s.openFiles[sessionID] = f
	return f, nil
}

// EmitEvent appends ev to the session log. The event is marshalled to JSON,
// suffixed with '\n', and written in one syscall. On success the data has
// reached the OS buffer; we do NOT fsync per write because the cost would be
// prohibitive for a chatty harness. A caller that needs durability across an
// OS crash (rather than just a harness crash) can wrap EmitEvent in a
// per-event Sync, or call os.File.Sync after Close.
func (s *FileStore) EmitEvent(ev Event) error {
	if ev.SessionID == "" {
		return errors.New("filestore: event SessionID is required")
	}
	line, err := json.Marshal(ev)
	if err != nil {
		return fmt.Errorf("marshal event: %w", err)
	}
	// Append the line terminator. We pre-allocate the buffer at exactly
	// len(line)+1 to keep the write a single syscall.
	out := make([]byte, 0, len(line)+1)
	out = append(out, line...)
	out = append(out, '\n')

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return errors.New("filestore: emit after close")
	}
	f, err := s.openLocked(ev.SessionID)
	if err != nil {
		return err
	}
	if _, err := f.Write(out); err != nil {
		return fmt.Errorf("write event: %w", err)
	}
	return nil
}

// GetEvents reads the on-disk JSONL file for sessionID, decodes each line,
// applies Offset/Limit/TypeFilter (in that order), and returns the resulting
// slice. We read the file from scratch every call rather than maintaining an
// in-memory index — the assumption is that GetEvents is rare (debug,
// observability, replay-on-start) compared to EmitEvent.
//
// Missing session returns an empty slice + nil error. Treating "no session"
// as "empty log" matches the upstream pattern (sessions are append-only;
// before the first emit, the empty log is the natural representation).
func (s *FileStore) GetEvents(sessionID string, opts GetEventsOpts) ([]Event, error) {
	path := s.pathFor(sessionID)
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()

	// Scan line-by-line. bufio.Scanner's default max line length is 64KiB
	// which would truncate large tool results — we bump the buffer to 1MiB
	// per line, which is far more than any reasonable single event payload.
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	var (
		all []Event
	)
	for scanner.Scan() {
		raw := scanner.Bytes()
		if len(raw) == 0 {
			continue // skip blank lines defensively
		}
		var ev Event
		if err := json.Unmarshal(raw, &ev); err != nil {
			return nil, fmt.Errorf("decode line %d: %w", len(all)+1, err)
		}
		all = append(all, ev)
	}
	if err := scanner.Err(); err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("scan %s: %w", path, err)
	}

	// Apply Offset, then TypeFilter, then Limit. Order matters: the upstream
	// semantics is "skip N raw events, then filter, then return up to Limit".
	if opts.Offset > 0 {
		if opts.Offset >= len(all) {
			return []Event{}, nil
		}
		all = all[opts.Offset:]
	}
	if len(opts.TypeFilter) > 0 {
		allowed := make(map[string]struct{}, len(opts.TypeFilter))
		for _, t := range opts.TypeFilter {
			allowed[t] = struct{}{}
		}
		filtered := make([]Event, 0, len(all))
		for _, ev := range all {
			if _, ok := allowed[ev.Type]; ok {
				filtered = append(filtered, ev)
			}
		}
		all = filtered
	}
	if opts.Limit > 0 && len(all) > opts.Limit {
		all = all[:opts.Limit]
	}
	return all, nil
}

// Close releases all cached file handles. Subsequent EmitEvent calls return an
// error. Idempotent: calling Close twice is safe.
func (s *FileStore) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	var firstErr error
	for id, f := range s.openFiles {
		if err := f.Close(); err != nil && firstErr == nil {
			firstErr = fmt.Errorf("close %s: %w", id, err)
		}
		delete(s.openFiles, id)
	}
	return firstErr
}
