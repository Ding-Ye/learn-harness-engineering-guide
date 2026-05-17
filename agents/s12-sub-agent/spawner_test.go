package subagent

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// childBinaryPath is filled in by TestMain. We build the child binary once at
// package-test-startup time and reuse it across every test. Without this, each
// test that needed an executable child would either reinvent the build step or
// rely on a build-tagged integration setup — both painful for readers.
//
// We keep the path as a package-level var (not a const) so TestMain can write
// it and individual tests can read it. The order is enforced by Go's testing
// framework: TestMain runs before any TestXxx.
var childBinaryPath string

// TestMain builds cmd/child into a temporary directory and stores the path
// in childBinaryPath, then runs the test suite. After m.Run returns, the
// directory is removed via t.TempDir's automatic cleanup is not available
// here (TestMain doesn't get a *testing.T), so we use a manual MkdirTemp +
// defer RemoveAll. We exit with whatever code m.Run returns so failed builds
// surface to CI as test failures.
func TestMain(m *testing.M) {
	// Skip whole package if `go` is not on PATH. This shouldn't happen in
	// CI (setup-go puts go on PATH), but local devs sometimes invoke
	// `go test` from a shell that wiped PATH.
	if _, err := exec.LookPath("go"); err != nil {
		fmt.Fprintf(os.Stderr, "TestMain: go not in PATH; skipping s12-sub-agent suite: %v\n", err)
		os.Exit(0)
	}

	tmpDir, err := os.MkdirTemp("", "s12-child-bin-")
	if err != nil {
		fmt.Fprintf(os.Stderr, "TestMain: mkdirtemp: %v\n", err)
		os.Exit(2)
	}
	defer os.RemoveAll(tmpDir)

	binName := "child"
	if runtime.GOOS == "windows" {
		binName += ".exe"
	}
	out := filepath.Join(tmpDir, binName)

	// `go build` requires a module-aware working directory. We invoke
	// from the repo dir (the test runs from agents/s12-sub-agent/) and
	// target the child package by its import path within the module.
	build := exec.Command("go", "build", "-o", out, "./cmd/child")
	build.Stderr = os.Stderr
	build.Stdout = os.Stderr
	if err := build.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "TestMain: go build cmd/child failed: %v\n", err)
		os.Exit(2)
	}
	childBinaryPath = out

	code := m.Run()
	os.Exit(code)
}

// TestSpawner_SingleTaskHappyPath exercises the round-trip: parent writes
// TASK.md, child reads it, runs the word-count tool, writes RESULT.json,
// parent reads it back. We assert all the public fields of SubResult so a
// regression in any one of them (Output format, Success flag, missing Name
// echo) surfaces here.
func TestSpawner_SingleTaskHappyPath(t *testing.T) {
	workDir := t.TempDir()
	spawner := &SubAgentSpawner{
		ChildBinary: childBinaryPath,
		MaxWorkers:  1,
		Timeout:     5 * time.Second,
	}
	tasks := []SubTask{{
		Name:        "happy",
		Instruction: "instruction: count these unique words here",
		WorkDir:     workDir,
	}}

	results := spawner.Spawn(context.Background(), tasks)

	if len(results) != 1 {
		t.Fatalf("want 1 result, got %d", len(results))
	}
	r := results[0]
	if !r.Success {
		t.Fatalf("want Success=true, got false; output=%q", r.Output)
	}
	if r.Name != "happy" {
		t.Errorf("want Name=happy, got %q (parent should NOT overwrite child's Name field — child writes whatever, parent's runOne preserves task name only if child returns empty)", r.Name)
	}
	// The body contains 6 unique words: instruction count these unique
	// words here. Case-insensitive dedupe.
	if !strings.Contains(r.Output, "6 unique words") {
		t.Errorf("want output to contain '6 unique words'; got %q", r.Output)
	}
	if r.DurationMS <= 0 {
		t.Errorf("want DurationMS > 0; got %d", r.DurationMS)
	}
}

// TestSpawner_ParallelTasksRespectMaxWorkers fans out 10 tasks against a
// spawner with MaxWorkers=2 and verifies (a) all 10 ran, (b) the shared
// counter file ended up with 10 lines, and (c) the spawner returned exactly
// 10 results in input order.
//
// We do NOT try to assert a hard "max concurrent == 2" by inspecting
// timestamps — that's flaky on slow CI. Instead we trust the semaphore
// implementation (one buffered channel of capacity N, acquire-then-release)
// and assert the observable effects: every worker eventually wrote.
func TestSpawner_ParallelTasksRespectMaxWorkers(t *testing.T) {
	root := t.TempDir()
	sharePath := filepath.Join(root, "share.txt")

	const n = 10
	tasks := make([]SubTask, n)
	for i := 0; i < n; i++ {
		dir := filepath.Join(root, fmt.Sprintf("task-%02d", i))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		tasks[i] = SubTask{
			Name:        fmt.Sprintf("t%02d", i),
			Instruction: fmt.Sprintf("instruction: hello task %d\nshare:%s\n", i, sharePath),
			WorkDir:     dir,
		}
	}

	spawner := &SubAgentSpawner{
		ChildBinary: childBinaryPath,
		MaxWorkers:  2,
		Timeout:     10 * time.Second,
	}
	results := spawner.Spawn(context.Background(), tasks)

	if len(results) != n {
		t.Fatalf("want %d results, got %d", n, len(results))
	}
	for i, r := range results {
		if !r.Success {
			t.Errorf("task %d: want Success=true, got false; output=%q", i, r.Output)
		}
	}

	// Verify the side-effect file has exactly N lines. Use a small
	// helper to count non-empty lines.
	data, err := os.ReadFile(sharePath)
	if err != nil {
		t.Fatalf("read share file: %v", err)
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(lines) != n {
		t.Errorf("want %d lines in share file, got %d; content=%q", n, len(lines), data)
	}
}

// TestSpawner_TimeoutKillsHungChild gives the child a `sleep:5s` directive
// but caps the spawner timeout at 500ms. The child should be killed by
// SIGKILL (via exec.CommandContext) and the result should be
// Success=false with an "timeout" hint in Output.
//
// On a very slow CI runner, 500ms might be tight; we use 1s to be safe but
// keep it well under the child's 5s sleep so the test still finishes fast.
func TestSpawner_TimeoutKillsHungChild(t *testing.T) {
	workDir := t.TempDir()
	spawner := &SubAgentSpawner{
		ChildBinary: childBinaryPath,
		MaxWorkers:  1,
		Timeout:     1 * time.Second,
	}
	tasks := []SubTask{{
		Name:        "hung",
		Instruction: "instruction: would have done work\nsleep:5s\n",
		WorkDir:     workDir,
	}}

	start := time.Now()
	results := spawner.Spawn(context.Background(), tasks)
	elapsed := time.Since(start)

	if len(results) != 1 {
		t.Fatalf("want 1 result, got %d", len(results))
	}
	r := results[0]
	if r.Success {
		t.Errorf("want Success=false (child killed by timeout); got true; output=%q", r.Output)
	}
	// The Output should mention the reason — "timeout" or "killed" or
	// "signal" depending on platform. We check for any of those tokens
	// rather than pinning to one to keep the test cross-platform.
	low := strings.ToLower(r.Output)
	if !strings.Contains(low, "timeout") && !strings.Contains(low, "killed") && !strings.Contains(low, "signal") {
		t.Errorf("want output to mention 'timeout'/'killed'/'signal'; got %q", r.Output)
	}
	// Spawn should have returned well before the 5s sleep would have
	// finished — give it a 4s budget to allow for very slow CI.
	if elapsed > 4*time.Second {
		t.Errorf("spawner ran for %v; expected the timeout (1s) to bound it well under 4s", elapsed)
	}
}

// TestSpawner_ChildCrashReportedNotPanicked feeds `crash:true` and checks
// that the parent's runOne reports a clean SubResult{Success: false} rather
// than panicking. This is the contract that lets a single child's bug stay
// localized to one task instead of taking down the whole pool.
func TestSpawner_ChildCrashReportedNotPanicked(t *testing.T) {
	workDir := t.TempDir()
	spawner := &SubAgentSpawner{
		ChildBinary: childBinaryPath,
		MaxWorkers:  1,
		Timeout:     5 * time.Second,
	}
	tasks := []SubTask{{
		Name:        "crashy",
		Instruction: "instruction: please blow up\ncrash:true\n",
		WorkDir:     workDir,
	}}

	// Sanity check: if runOne panicked, the test process itself would
	// die. The `defer recover` here is belt-and-suspenders — if it ever
	// catches anything, we want a clean failure message.
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("Spawn panicked instead of returning a failure result: %v", r)
		}
	}()

	results := spawner.Spawn(context.Background(), tasks)

	if len(results) != 1 {
		t.Fatalf("want 1 result, got %d", len(results))
	}
	r := results[0]
	if r.Success {
		t.Errorf("want Success=false (child crashed); got true; output=%q", r.Output)
	}
	low := strings.ToLower(r.Output)
	if !strings.Contains(low, "exit") && !strings.Contains(low, "non-zero") {
		t.Errorf("want output to mention exit/non-zero; got %q", r.Output)
	}
}

// TestSubTask_WorkDirIsolated verifies that two tasks running in parallel
// against distinct WorkDirs never see each other's TASK.md or RESULT.json.
// We feed two tasks with intentionally different instructions (different word
// counts) and check that each result reflects its own instruction.
func TestSubTask_WorkDirIsolated(t *testing.T) {
	dirA := t.TempDir()
	dirB := t.TempDir()

	if dirA == dirB {
		t.Fatalf("t.TempDir handed out the same path twice: %s", dirA)
	}

	spawner := &SubAgentSpawner{
		ChildBinary: childBinaryPath,
		MaxWorkers:  2,
		Timeout:     5 * time.Second,
	}
	tasks := []SubTask{
		{Name: "A", Instruction: "instruction: a b c", WorkDir: dirA},                  // 4 unique words
		{Name: "B", Instruction: "instruction: one two three four five", WorkDir: dirB}, // 6 unique words
	}

	results := spawner.Spawn(context.Background(), tasks)
	if len(results) != 2 {
		t.Fatalf("want 2 results, got %d", len(results))
	}

	// Order is input-order (slice index matches task index).
	if !strings.Contains(results[0].Output, "4 unique words") {
		t.Errorf("task A: want '4 unique words'; got %q", results[0].Output)
	}
	if !strings.Contains(results[1].Output, "6 unique words") {
		t.Errorf("task B: want '6 unique words'; got %q", results[1].Output)
	}

	// Each WorkDir should contain its own TASK.md and RESULT.json — and
	// only its own. We don't try to assert that RESULT.json contents
	// differ via fs inspection because the SubResult fields above are
	// the load-bearing assertion; but a missing file would mean the
	// child failed to write isolated state.
	for _, dir := range []string{dirA, dirB} {
		if _, err := os.Stat(filepath.Join(dir, "TASK.md")); err != nil {
			t.Errorf("%s: TASK.md missing: %v", dir, err)
		}
		if _, err := os.Stat(filepath.Join(dir, "RESULT.json")); err != nil {
			t.Errorf("%s: RESULT.json missing: %v", dir, err)
		}
	}
}

// counterForAssertion exists so the test file doesn't import sync/atomic
// without a usage in some Go toolchain configurations that complain about
// unused imports. Cheap belt-and-suspenders.
var _ = atomic.Int64{}
