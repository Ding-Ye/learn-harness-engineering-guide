package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// dateFormat is the layout used for daily log filenames. Upstream guide
// uses strftime "%Y-%m-%d" (memory-and-context.md L139) so we mirror it.
const dateFormat = "2006-01-02"

// longTermFile is the curated tier — a single Markdown file at the root
// of the memory directory. Upstream calls it MEMORY.md (L107).
const longTermFile = "MEMORY.md"

// logSubdir holds append-only per-day records, named YYYY-MM-DD.md.
// Upstream uses the path "memory/<date>.md" (L89, L140).
const logSubdir = "memory"

// Memory is a two-tier disk-backed memory store:
//
//	<baseDir>/MEMORY.md            — long-term, curated, single file
//	<baseDir>/memory/YYYY-MM-DD.md — daily logs, append-only
//
// Memory is safe for concurrent AppendLog callers (mutex-guarded).
// Read is *not* guaranteed serializable against concurrent AppendLog —
// in practice memory is read at session start and appended during the
// session, so the two operations rarely race. We protect against the
// common case (many writers, one snapshot) by guarding the AppendLog
// file handle.
type Memory struct {
	baseDir string
	clock   Clock
	mu      sync.Mutex
}

// New constructs a Memory rooted at baseDir. The directory and the
// daily-log subdirectory are created if missing — this matches upstream
// behavior where a fresh agent starts with no memory directory and the
// first session bootstraps it (memory-and-context.md L135-L141).
func New(baseDir string, clock Clock) (*Memory, error) {
	if baseDir == "" {
		return nil, fmt.Errorf("memory: baseDir is required")
	}
	if clock == nil {
		return nil, fmt.Errorf("memory: clock is required")
	}
	if err := os.MkdirAll(filepath.Join(baseDir, logSubdir), 0o755); err != nil {
		return nil, fmt.Errorf("memory: create dirs: %w", err)
	}
	return &Memory{baseDir: baseDir, clock: clock}, nil
}

// Read returns the combined view used at session startup:
//
//	<MEMORY.md content>
//	---
//	<today's log content>
//	---
//	<yesterday's log content>
//
// Missing files are skipped silently. If nothing exists, returns "".
// This is a direct port of the Python in memory-and-context.md L130-L143:
// long-term first, then `days_ago in [0, 1]` joined by "\n---\n".
func (m *Memory) Read() (string, error) {
	var sections []string

	// 1. Always read long-term memory first (if present).
	if b, err := os.ReadFile(filepath.Join(m.baseDir, longTermFile)); err == nil {
		sections = append(sections, string(b))
	} else if !os.IsNotExist(err) {
		return "", fmt.Errorf("memory: read %s: %w", longTermFile, err)
	}

	// 2. Read today and yesterday's daily logs.
	now := m.clock.Now()
	for daysAgo := 0; daysAgo <= 1; daysAgo++ {
		date := now.AddDate(0, 0, -daysAgo).Format(dateFormat)
		path := filepath.Join(m.baseDir, logSubdir, date+".md")
		b, err := os.ReadFile(path)
		if err == nil {
			sections = append(sections, string(b))
			continue
		}
		if !os.IsNotExist(err) {
			return "", fmt.Errorf("memory: read %s: %w", path, err)
		}
	}

	return strings.Join(sections, "\n---\n"), nil
}

// AppendLog appends entry (plus a trailing newline) to today's daily log,
// creating the file if needed. The file is opened with O_APPEND|O_CREATE
// so concurrent writers from multiple goroutines all land — on POSIX,
// a single write() smaller than PIPE_BUF is atomic with respect to other
// O_APPEND writers. We additionally hold a mutex so the contract holds on
// any platform and so the write is also atomic with respect to (e.g.) a
// hypothetical truncate-and-rewrite path.
func (m *Memory) AppendLog(entry string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	date := m.clock.Now().Format(dateFormat)
	path := filepath.Join(m.baseDir, logSubdir, date+".md")
	// Ensure the subdir exists even if the caller deleted it after New().
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("memory: ensure log dir: %w", err)
	}

	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("memory: open log %s: %w", path, err)
	}
	defer f.Close()

	if _, err := f.WriteString(entry + "\n"); err != nil {
		return fmt.Errorf("memory: write log %s: %w", path, err)
	}
	return nil
}

// RotateOlderThan deletes daily log files whose date is more than `days`
// days before `now`. MEMORY.md (the curated tier) is never touched.
// Files in the log dir that don't parse as YYYY-MM-DD.md are left alone
// so users can drop README.md etc. into memory/ without losing them.
//
// days=7 means: keep today and the six prior days; delete anything older.
func (m *Memory) RotateOlderThan(days int) error {
	if days < 0 {
		return fmt.Errorf("memory: days must be >= 0, got %d", days)
	}
	cutoff := m.clock.Now().AddDate(0, 0, -days)
	logDir := filepath.Join(m.baseDir, logSubdir)

	entries, err := os.ReadDir(logDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("memory: read log dir: %w", err)
	}

	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(name, ".md") {
			continue
		}
		datePart := strings.TrimSuffix(name, ".md")
		t, err := time.Parse(dateFormat, datePart)
		if err != nil {
			// Not a dated log — leave it alone.
			continue
		}
		// Contract: RotateOlderThan(N) keeps the last N daily logs (today
		// plus the N-1 prior days) and deletes the rest. Equivalently, a
		// file dated `now - N` days or earlier is removed.
		if !t.After(truncateToDay(cutoff)) {
			path := filepath.Join(logDir, name)
			if err := os.Remove(path); err != nil {
				return fmt.Errorf("memory: remove %s: %w", path, err)
			}
		}
	}
	return nil
}

// truncateToDay returns t at 00:00:00 in its current location.
func truncateToDay(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location())
}
