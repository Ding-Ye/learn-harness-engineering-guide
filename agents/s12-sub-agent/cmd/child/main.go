// Child binary: reads <workdir>/TASK.md, does a tiny "tool" (counts unique
// words in the instruction body), writes RESULT.json. Honors two test-control
// directives so the parent-side tests can exercise timeout and crash paths
// without flakiness:
//
//	sleep:10s   → sleep that long before writing the result (timeout test)
//	crash:true  → os.Exit(2) without writing RESULT.json (crash test)
//
// The child is deliberately self-contained: it imports nothing from the
// parent's spawner package, only the shared SubResult type from the library
// root. That mirrors what real sub-agent harnesses do — the child runs in its
// own process and shouldn't share state with the parent beyond the file-IPC
// contract.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	subagent "learn-harness-engineering-guide/s12-sub-agent"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "child: missing workdir argument")
		os.Exit(2)
	}
	workDir := os.Args[1]

	// Read TASK.md from the work directory. We use a fixed filename so
	// the parent and the child can't drift. If the file is missing we
	// emit a stderr error and exit 2; the parent will translate that
	// into SubResult{Success: false}.
	taskPath := filepath.Join(workDir, "TASK.md")
	body, err := os.ReadFile(taskPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "child: read TASK.md: %v\n", err)
		os.Exit(2)
	}

	text := string(body)

	// Honor test-control directives BEFORE doing real work. They are
	// looked up as plain substrings (no fancy parsing) because TASK.md
	// is a free-form instruction document — we don't want a strict
	// format that production children would have to obey.
	if strings.Contains(text, "crash:true") {
		// Exit without writing RESULT.json. The parent should report
		// Success=false with a "child exited non-zero" message.
		fmt.Fprintln(os.Stderr, "child: simulated crash")
		os.Exit(2)
	}
	if d, ok := parseSleepDirective(text); ok {
		// Sleep that long. If the parent's per-task timeout is
		// shorter, exec.CommandContext will SIGKILL us before we
		// wake. That's exactly what the timeout test depends on.
		time.Sleep(d)
	}

	// Now the "real work". The tool is "count unique words in the
	// instruction" — small enough to fit in a few lines and large enough
	// to demonstrate the IPC round-trip. We trim a few well-known
	// directive prefixes so they don't pollute the word count.
	words := strings.Fields(stripDirectives(text))
	seen := make(map[string]struct{}, len(words))
	for _, w := range words {
		seen[strings.ToLower(w)] = struct{}{}
	}

	res := subagent.SubResult{
		// Name is filled in by the parent based on the task; the
		// child can't know it without the parent passing it. We
		// leave it empty here and let the parent overwrite from
		// the SubTask.
		Success:   true,
		Output:    fmt.Sprintf("%d unique words", len(seen)),
		Artifacts: nil,
	}
	out, err := json.MarshalIndent(res, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "child: marshal result: %v\n", err)
		os.Exit(2)
	}
	if err := os.WriteFile(filepath.Join(workDir, "RESULT.json"), out, 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "child: write RESULT.json: %v\n", err)
		os.Exit(2)
	}

	// As a side effect for the parallel-throughput test, append our
	// PID + timestamp to a shared file IF the instruction contains
	// `share:<path>`. The shared file lets the test observe that all
	// workers ran. We use O_APPEND so concurrent writers don't trample
	// each other (the OS guarantees atomic append for small writes on
	// the platforms we care about).
	if sharePath, ok := parseShareDirective(text); ok {
		// Best-effort: tests verify the file's final line count, so a
		// failure here would be noisy but not catastrophic. We still
		// log to stderr to aid diagnosis.
		f, err := os.OpenFile(sharePath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err == nil {
			fmt.Fprintf(f, "%d %s\n", os.Getpid(), time.Now().UTC().Format(time.RFC3339Nano))
			_ = f.Close()
		} else {
			fmt.Fprintf(os.Stderr, "child: open share file: %v\n", err)
		}
	}
}

// parseSleepDirective returns (duration, true) if the body has `sleep:<dur>`
// somewhere on a line. The duration syntax is `time.ParseDuration` — supports
// "500ms", "5s", "1m", etc. Returns (0, false) if absent or malformed; we
// silently ignore malformed durations rather than crashing, so a typo in
// TASK.md can't surface as a child-side panic.
func parseSleepDirective(body string) (time.Duration, bool) {
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "sleep:") {
			continue
		}
		raw := strings.TrimSpace(strings.TrimPrefix(line, "sleep:"))
		d, err := time.ParseDuration(raw)
		if err != nil {
			return 0, false
		}
		return d, true
	}
	return 0, false
}

// parseShareDirective returns (path, true) if the body has `share:<path>` on
// a line. Used by the parallel-tasks test to give every worker a path to
// O_APPEND into. Empty path → no shared file.
func parseShareDirective(body string) (string, bool) {
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "share:") {
			continue
		}
		return strings.TrimSpace(strings.TrimPrefix(line, "share:")), true
	}
	return "", false
}

// stripDirectives removes lines that are pure directive declarations so they
// don't count toward the unique-word total. This is a teaching nicety: it
// lets the test fixtures use the same TASK.md for both directive-driven
// behavior and the word-count assertion.
func stripDirectives(body string) string {
	var keep []string
	for _, line := range strings.Split(body, "\n") {
		trim := strings.TrimSpace(line)
		if strings.HasPrefix(trim, "sleep:") || strings.HasPrefix(trim, "crash:") || strings.HasPrefix(trim, "share:") {
			continue
		}
		keep = append(keep, line)
	}
	return strings.Join(keep, "\n")
}
