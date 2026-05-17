package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

// frozen returns a FakeClock pinned to YYYY-MM-DD at noon UTC.
func frozen(y int, m time.Month, d int) FakeClock {
	return FakeClock{T: time.Date(y, m, d, 12, 0, 0, 0, time.UTC)}
}

// writeDailyLog is a test helper that drops a log file into <dir>/memory/.
func writeDailyLog(t *testing.T, baseDir, date, content string) {
	t.Helper()
	path := filepath.Join(baseDir, logSubdir, date+".md")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// TestMemory_ReadCombinesLongTermAndRecentLogs verifies the upstream
// startup-read order: MEMORY.md first, then today, then yesterday, all
// joined by "\n---\n". A two-days-ago log MUST NOT appear (we deliberately
// only include the most recent two days per upstream L137-L142).
func TestMemory_ReadCombinesLongTermAndRecentLogs(t *testing.T) {
	dir := t.TempDir()
	clock := frozen(2026, 5, 17) // Sunday

	// Fixture: long-term file at root + 3 day logs (today, yesterday, 2-days-ago).
	if err := os.WriteFile(filepath.Join(dir, longTermFile), []byte("LONG_TERM"), 0o644); err != nil {
		t.Fatalf("write MEMORY.md: %v", err)
	}
	writeDailyLog(t, dir, "2026-05-17", "TODAY_LOG")
	writeDailyLog(t, dir, "2026-05-16", "YESTERDAY_LOG")
	writeDailyLog(t, dir, "2026-05-15", "TWO_DAYS_AGO_LOG") // must be excluded

	mem, err := New(dir, clock)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	got, err := mem.Read()
	if err != nil {
		t.Fatalf("Read: %v", err)
	}

	want := "LONG_TERM\n---\nTODAY_LOG\n---\nYESTERDAY_LOG"
	if got != want {
		t.Errorf("Read() mismatch\n got: %q\nwant: %q", got, want)
	}
	if strings.Contains(got, "TWO_DAYS_AGO_LOG") {
		t.Errorf("Read() included a log older than yesterday:\n%s", got)
	}
}

// TestMemory_AppendLogCreatesDatedFile verifies that AppendLog routes the
// entry to memory/<frozen-date>.md, creating the file if absent, and that
// the file contains the entry followed by a newline.
func TestMemory_AppendLogCreatesDatedFile(t *testing.T) {
	dir := t.TempDir()
	clock := frozen(2026, 5, 17)
	mem, err := New(dir, clock)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	if err := mem.AppendLog("first entry"); err != nil {
		t.Fatalf("AppendLog: %v", err)
	}

	wantPath := filepath.Join(dir, logSubdir, "2026-05-17.md")
	b, err := os.ReadFile(wantPath)
	if err != nil {
		t.Fatalf("read %s: %v", wantPath, err)
	}
	want := "first entry\n"
	if string(b) != want {
		t.Errorf("log content = %q, want %q", string(b), want)
	}

	// A second append should add a second line, not overwrite.
	if err := mem.AppendLog("second entry"); err != nil {
		t.Fatalf("AppendLog 2: %v", err)
	}
	b, _ = os.ReadFile(wantPath)
	want = "first entry\nsecond entry\n"
	if string(b) != want {
		t.Errorf("log content after second append = %q, want %q", string(b), want)
	}
}

// TestMemory_AppendIsAtomicAcrossWriters exercises the concurrent-writer
// contract: N goroutines append distinct lines, and at the end every line
// MUST be present exactly once, in some order. This catches both lost
// writes (file truncation) and interleaved writes (line corruption).
// Run with `go test -race`.
func TestMemory_AppendIsAtomicAcrossWriters(t *testing.T) {
	dir := t.TempDir()
	clock := frozen(2026, 5, 17)
	mem, err := New(dir, clock)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	const n = 50
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		i := i
		go func() {
			defer wg.Done()
			if err := mem.AppendLog(fmt.Sprintf("entry-%02d", i)); err != nil {
				t.Errorf("AppendLog(%d): %v", i, err)
			}
		}()
	}
	wg.Wait()

	b, err := os.ReadFile(filepath.Join(dir, logSubdir, "2026-05-17.md"))
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	// Split into non-empty lines and verify the set matches what we wrote.
	got := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
	if len(got) != n {
		t.Fatalf("got %d lines, want %d. content:\n%s", len(got), n, string(b))
	}
	sort.Strings(got)
	for i, line := range got {
		want := fmt.Sprintf("entry-%02d", i)
		if line != want {
			t.Errorf("line %d = %q, want %q", i, line, want)
		}
	}
}

// TestMemory_RotateDeletesOldLogs populates 30 consecutive days of logs,
// calls RotateOlderThan(7), and asserts: (a) MEMORY.md is untouched,
// (b) the last 7 daily logs (today + 6 prior days) remain, (c) all older
// daily logs are gone, (d) an unrelated file in memory/ is preserved.
func TestMemory_RotateDeletesOldLogs(t *testing.T) {
	dir := t.TempDir()
	clock := frozen(2026, 5, 17)
	mem, err := New(dir, clock)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// MEMORY.md must not be deleted.
	memoryMD := filepath.Join(dir, longTermFile)
	if err := os.WriteFile(memoryMD, []byte("CURATED"), 0o644); err != nil {
		t.Fatalf("write MEMORY.md: %v", err)
	}

	// 30 consecutive days ending at 2026-05-17.
	for daysAgo := 0; daysAgo < 30; daysAgo++ {
		date := clock.T.AddDate(0, 0, -daysAgo).Format(dateFormat)
		writeDailyLog(t, dir, date, fmt.Sprintf("day-%d", daysAgo))
	}
	// Unrelated file in memory/ — must be preserved.
	bystander := filepath.Join(dir, logSubdir, "README.md")
	if err := os.WriteFile(bystander, []byte("not a daily log"), 0o644); err != nil {
		t.Fatalf("write README.md: %v", err)
	}

	if err := mem.RotateOlderThan(7); err != nil {
		t.Fatalf("RotateOlderThan: %v", err)
	}

	// MEMORY.md should still be there.
	if _, err := os.Stat(memoryMD); err != nil {
		t.Fatalf("MEMORY.md gone: %v", err)
	}
	// README.md should still be there.
	if _, err := os.Stat(bystander); err != nil {
		t.Fatalf("memory/README.md gone: %v", err)
	}

	// Days 0..6 (today + 6 prior) should remain. Days 7..29 should be gone.
	for daysAgo := 0; daysAgo < 30; daysAgo++ {
		date := clock.T.AddDate(0, 0, -daysAgo).Format(dateFormat)
		path := filepath.Join(dir, logSubdir, date+".md")
		_, err := os.Stat(path)
		switch {
		case daysAgo < 7:
			if err != nil {
				t.Errorf("expected %s to remain (daysAgo=%d), got %v", path, daysAgo, err)
			}
		default:
			if err == nil {
				t.Errorf("expected %s to be removed (daysAgo=%d), but it remains", path, daysAgo)
			}
		}
	}
}

// TestMemory_HandlesMissingDir covers two graceful-degradation cases that
// upstream's `if os.path.exists(...)` checks imply (L135, L141):
//   - New() on a nonexistent baseDir creates it (and the memory/ subdir).
//   - Read() on a fresh empty dir returns "" with no error.
func TestMemory_HandlesMissingDir(t *testing.T) {
	parent := t.TempDir()
	dir := filepath.Join(parent, "does-not-exist-yet", "memory-root")

	mem, err := New(dir, frozen(2026, 5, 17))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// New must have created baseDir AND the log subdir.
	if _, err := os.Stat(filepath.Join(dir, logSubdir)); err != nil {
		t.Errorf("expected %s/%s to be created, got %v", dir, logSubdir, err)
	}

	got, err := mem.Read()
	if err != nil {
		t.Fatalf("Read on empty dir: %v", err)
	}
	if got != "" {
		t.Errorf("Read on empty dir = %q, want \"\"", got)
	}
}
